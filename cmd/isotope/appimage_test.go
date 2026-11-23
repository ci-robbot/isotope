package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// The test AppImages are built at test time: a small ELF runtime, then a
// squashfs from mksquashfs, then the AppImage magic at offset 8. CI installs
// squashfs-tools and sets ISOTOPE_REQUIRE_MKSQUASHFS=1, so the tests fail
// there instead of skipping; the image's update-build stage has no
// mksquashfs and skips them.

// fakeELF is a minimal ELF64 x86-64 file: a program header table with a
// PT_INTERP naming interp (none when interp is ""), and a section header
// table of shnum zeroed entries at the end.
func fakeELF(interp string, shnum int) []byte {
	const ehsize, phentsize, shentsize = 64, 56, 64
	phnum := 0
	if interp != "" {
		phnum = 1
	}
	interpOff := ehsize + phnum*phentsize
	body := interpOff
	if interp != "" {
		body += len(interp) + 1
	}
	shoff := (body + 7) &^ 7
	b := make([]byte, shoff+shnum*shentsize)
	copy(b, "\x7fELF")
	b[4], b[5], b[6] = 2, 1, 1 // 64-bit, little endian, version 1
	le := binary.LittleEndian
	le.PutUint16(b[16:], 2)  // ET_EXEC
	le.PutUint16(b[18:], 62) // EM_X86_64
	le.PutUint32(b[20:], 1)
	if phnum > 0 {
		le.PutUint64(b[32:], ehsize) // e_phoff
	}
	if shnum > 0 {
		le.PutUint64(b[40:], uint64(shoff)) // e_shoff
	}
	le.PutUint16(b[52:], ehsize)
	le.PutUint16(b[54:], phentsize)
	le.PutUint16(b[56:], uint16(phnum))
	le.PutUint16(b[58:], shentsize)
	le.PutUint16(b[60:], uint16(shnum))
	if interp != "" {
		ph := b[ehsize:]
		le.PutUint32(ph[0:], 3) // PT_INTERP
		le.PutUint32(ph[4:], 4) // PF_R
		le.PutUint64(ph[8:], uint64(interpOff))
		le.PutUint64(ph[16:], uint64(interpOff))
		le.PutUint64(ph[24:], uint64(interpOff))
		le.PutUint64(ph[32:], uint64(len(interp)+1))
		le.PutUint64(ph[40:], uint64(len(interp)+1))
		le.PutUint64(ph[48:], 1)
		copy(b[interpOff:], interp)
	}
	return b
}

const (
	glibcInterp = "/lib64/ld-linux-x86-64.so.2"
	muslInterp  = "/lib/ld-musl-x86_64.so.1"
)

func needMksquashfs(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("mksquashfs"); err != nil {
		if os.Getenv("ISOTOPE_REQUIRE_MKSQUASHFS") == "1" {
			t.Fatal("mksquashfs is missing and ISOTOPE_REQUIRE_MKSQUASHFS=1")
		}
		t.Skip("mksquashfs (squashfs-tools) is not installed")
	}
}

// aiTree is the content of a test AppImage: path -> entry. An entry is a
// file's content, "->target" for a symlink, or "/" for an empty directory.
type aiTree map[string]string

type aiOpts struct {
	comp    string            // mksquashfs -comp; "" is gzip
	raw     bool              // -no-compression, so names can be patched
	patch   map[string]string // byte patches on the squashfs (raw only)
	pseudo  []string          // mksquashfs -p definitions
	setuid  []string          // files to chmod u+s,g+s
	sparse  map[string]int64  // sparse files of this size
	runtime []byte            // the ELF before the squashfs; fakeELF by default
}

func writeTree(t *testing.T, dir string, tree aiTree) {
	t.Helper()
	for p, v := range tree {
		full := filepath.Join(dir, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		switch {
		case v == "/":
			if err := os.MkdirAll(full, 0o755); err != nil {
				t.Fatal(err)
			}
		case strings.HasPrefix(v, "->"):
			if err := os.Symlink(v[2:], full); err != nil {
				t.Fatal(err)
			}
		default:
			mode := os.FileMode(0o644)
			if strings.HasPrefix(v, "#!") || strings.HasPrefix(v, "\x7fELF") {
				mode = 0o755
			}
			if err := os.WriteFile(full, []byte(v), mode); err != nil {
				t.Fatal(err)
			}
		}
	}
}

// buildAppImage writes a type 2 AppImage of tree into dir and returns its
// path.
func buildAppImage(t *testing.T, dir string, tree aiTree, o aiOpts) string {
	t.Helper()
	needMksquashfs(t)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	src, err := os.MkdirTemp(dir, "src-")
	if err != nil {
		t.Fatal(err)
	}
	writeTree(t, src, tree)
	for _, p := range o.setuid {
		if err := os.Chmod(filepath.Join(src, p), 0o755|fs.ModeSetuid|fs.ModeSetgid); err != nil {
			t.Fatal(err)
		}
	}
	for p, size := range o.sparse {
		f, err := os.Create(filepath.Join(src, p))
		if err != nil {
			t.Fatal(err)
		}
		if err := f.Truncate(size); err != nil {
			t.Fatal(err)
		}
		f.Close()
	}
	img := filepath.Join(dir, fmt.Sprintf("sq-%d", time.Now().UnixNano()))
	args := []string{src, img, "-noappend", "-quiet", "-no-progress", "-all-root", "-no-xattrs"}
	if o.raw {
		args = append(args, "-no-compression")
	} else if o.comp != "" {
		args = append(args, "-comp", o.comp)
	}
	for _, p := range o.pseudo {
		args = append(args, "-p", p)
	}
	if out, err := exec.Command("mksquashfs", args...).CombinedOutput(); err != nil {
		t.Fatalf("mksquashfs: %v\n%s", err, out)
	}
	sq, err := os.ReadFile(img)
	if err != nil {
		t.Fatal(err)
	}
	for from, to := range o.patch {
		if len(from) != len(to) || bytes.Count(sq, []byte(from)) != 1 {
			t.Fatalf("patch %q -> %q: %d matches", from, to, bytes.Count(sq, []byte(from)))
		}
		sq = bytes.Replace(sq, []byte(from), []byte(to), 1)
	}
	rt := o.runtime
	if rt == nil {
		rt = fakeELF("", 3)
	}
	rt = append([]byte(nil), rt...)
	copy(rt[8:], "AI\x02")
	out := filepath.Join(dir, fmt.Sprintf("App-%d.AppImage", time.Now().UnixNano()))
	if err := os.WriteFile(out, append(rt, sq...), 0o755); err != nil {
		t.Fatal(err)
	}
	return out
}

// envAppRun is an AppRun script that writes what the AppImage runtime
// would set, and its arguments, to $AI_TEST_OUT.
const envAppRun = "#!/bin/sh\nprintf '%s\\n' \"APPDIR=$APPDIR\" \"APPIMAGE=$APPIMAGE\" \"ARGV0=$ARGV0\" \"OWD=$OWD\" \"PWD=$(pwd)\" \"GUARD=$" + glibcGuardEnv + "\" \"$@\" > \"$AI_TEST_OUT\"\nexit 5\n"

func muslTree() aiTree {
	return aiTree{"AppRun": envAppRun, "usr/bin/app": string(fakeELF(muslInterp, 0)), "app.desktop": "[Desktop Entry]\n"}
}

func glibcTree() aiTree {
	return aiTree{"AppRun": envAppRun, "usr/bin/app": string(fakeELF(glibcInterp, 0)), "usr/lib/libx.so": string(fakeELF("", 0))}
}

func TestAppImageOffset(t *testing.T) {
	needMksquashfs(t)
	dir := t.TempDir()
	runtimes := map[string][]byte{"fake": fakeELF("", 5)}
	if b, err := os.ReadFile("/bin/true"); err == nil && bytes.HasPrefix(b, []byte("\x7fELF")) {
		runtimes["/bin/true"] = b
	}
	for name, rt := range runtimes {
		t.Run(name, func(t *testing.T) {
			path := buildAppImage(t, dir, aiTree{"AppRun": "#!/bin/sh\n"}, aiOpts{runtime: rt})
			f, err := os.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			off, err := appimageOffset(f)
			if err != nil {
				t.Fatal(err)
			}
			if off != int64(len(rt)) {
				t.Fatalf("offset %d, want %d (the end of the runtime's section headers)", off, len(rt))
			}
		})
	}
	// Not an ELF, and an ELF with no section headers: errors, no panic.
	for name, b := range map[string][]byte{"text": []byte("#!/bin/sh\necho hi\n"), "no sections": fakeELF("", 0)} {
		p := filepath.Join(dir, strings.ReplaceAll(name, " ", "-"))
		write(t, p, string(b))
		f, _ := os.Open(p)
		if _, err := appimageOffset(f); err == nil {
			t.Errorf("%s: no error", name)
		}
		f.Close()
	}
}

func fileSHA(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

func TestExtractCachesBySHA(t *testing.T) {
	dir := t.TempDir()
	cache := filepath.Join(dir, "cache")
	for _, comp := range []string{"gzip", "xz", "zstd"} {
		t.Run(comp, func(t *testing.T) {
			tree := muslTree()
			tree["usr/share/doc/a b.txt"] = strings.Repeat("hello AppImage ", 4000) + comp
			tree["usr/lib/libapp.so"] = "->libapp.so.1"
			tree["usr/lib/libapp.so.1"] = "lib"
			tree["usr/share/empty"] = "/"
			path := buildAppImage(t, dir, tree, aiOpts{comp: comp})
			appDir, err := extractAppImage(path, cache)
			if err != nil {
				t.Fatal(err)
			}
			if want := filepath.Join(cache, fileSHA(t, path)); appDir != want {
				t.Fatalf("appDir %s, want %s", appDir, want)
			}
			for p, v := range tree {
				full := filepath.Join(appDir, p)
				switch {
				case v == "/":
					if st, err := os.Lstat(full); err != nil || !st.IsDir() {
						t.Errorf("%s: not a directory: %v", p, err)
					}
				case strings.HasPrefix(v, "->"):
					if got, err := os.Readlink(full); err != nil || got != v[2:] {
						t.Errorf("%s: link %q %v, want %q", p, got, err, v[2:])
					}
				default:
					if got, err := os.ReadFile(full); err != nil || string(got) != v {
						t.Errorf("%s: content differs (%v)", p, err)
					}
				}
			}
			if st, _ := os.Stat(filepath.Join(appDir, "AppRun")); st.Mode().Perm()&0o100 == 0 {
				t.Errorf("AppRun lost its exec bit: %v", st.Mode())
			}
			// The second run reuses the cache: a file changed in it stays.
			write(t, filepath.Join(appDir, "marker"), "x")
			again, err := extractAppImage(path, cache)
			if err != nil || again != appDir {
				t.Fatalf("second extract: %s %v", again, err)
			}
			if _, err := os.Stat(filepath.Join(appDir, "marker")); err != nil {
				t.Fatal("the second run extracted again")
			}
		})
	}
	// No temp dirs are left behind.
	for _, name := range cacheEntries(t, cache) {
		if strings.HasPrefix(name, ".") {
			t.Errorf("left behind: %s", name)
		}
	}
}

func TestExtractUnsupported(t *testing.T) {
	dir := t.TempDir()
	cache := filepath.Join(dir, "cache")
	path := buildAppImage(t, dir, muslTree(), aiOpts{comp: "lz4"})
	_, err := extractAppImage(path, cache)
	if err == nil || !strings.Contains(err.Error(), "uses a compression Isotope cannot read yet") {
		t.Fatalf("lz4: %v", err)
	}
	notAI := filepath.Join(dir, "plain")
	write(t, notAI, string(fakeELF(glibcInterp, 2)))
	if _, err := extractAppImage(notAI, cache); err == nil || !strings.Contains(err.Error(), "not an AppImage") {
		t.Fatalf("plain ELF: %v", err)
	}
	// Type 1 (ISO 9660) AppImages are out of scope.
	t1 := fakeELF("", 2)
	copy(t1[8:], "AI\x01")
	write(t, notAI, string(t1))
	if _, err := extractAppImage(notAI, cache); err == nil || !strings.Contains(err.Error(), "type 1") {
		t.Fatalf("type 1: %v", err)
	}
	// A squashfs cut short is an error, not a panic.
	good := buildAppImage(t, dir, muslTree(), aiOpts{})
	b, _ := os.ReadFile(good)
	write(t, notAI, string(b[:len(fakeELF("", 3))+100]))
	if _, err := extractAppImage(notAI, cache); err == nil {
		t.Fatal("a truncated AppImage extracted")
	}
	if ents, _ := os.ReadDir(cache); len(ents) != 0 {
		t.Fatalf("the failures left %v in the cache", ents)
	}
}

// cacheEntries is the cache's entries, without the entries' lock files.
func cacheEntries(t *testing.T, cache string) []string {
	t.Helper()
	ents, _ := os.ReadDir(cache)
	var names []string
	for _, e := range ents {
		if !strings.HasSuffix(e.Name(), ".lock") {
			names = append(names, e.Name())
		}
	}
	return names
}

// listTree is every path under root, so a test can see nothing was written
// elsewhere.
func listTree(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err == nil {
			out = append(out, p)
		}
		return nil
	})
	return out
}

func TestExtractRefusesEscapingPaths(t *testing.T) {
	cases := []struct {
		name string
		tree aiTree
		o    aiOpts
		want string
	}{
		{"dot-dot entry", aiTree{"AppRun": "#!/bin/sh\n", "Zq": "evil"}, aiOpts{raw: true, patch: map[string]string{"Zq": ".."}}, "unsafe name"},
		{"slash in a name", aiTree{"AppRun": "#!/bin/sh\n", "eKtc": "evil"}, aiOpts{raw: true, patch: map[string]string{"eKtc": "e/tc"}}, "unsafe name"},
		{"absolute name", aiTree{"AppRun": "#!/bin/sh\n", "Ktmpx": "evil"}, aiOpts{raw: true, patch: map[string]string{"Ktmpx": "/tmpx"}}, "unsafe name"},
		{"absolute symlink", aiTree{"AppRun": "#!/bin/sh\n", "usr/lib/x": "->/etc/passwd"}, aiOpts{}, "points outside"},
		{"escaping symlink", aiTree{"AppRun": "#!/bin/sh\n", "usr/x": "->../../outside"}, aiOpts{}, "points outside"},
		{"device", aiTree{"AppRun": "#!/bin/sh\n"}, aiOpts{pseudo: []string{"null c 0666 0 0 1 3"}}, "device"},
		// A symlink and a directory of the same name: the directory's file
		// would be written through the symlink.
		{"duplicate name", aiTree{"AppRun": "#!/bin/sh\n", "aaaa": "->c", "bbbb/passwd": "evil"}, aiOpts{raw: true, patch: map[string]string{"bbbb": "aaaa"}}, "twice"},
		{"sparse bomb", aiTree{"AppRun": "#!/bin/sh\n"}, aiOpts{sparse: map[string]int64{"big": 1 << 30}}, "larger than"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			outside := filepath.Join(dir, "outside")
			if err := os.Mkdir(outside, 0o755); err != nil {
				t.Fatal(err)
			}
			cache := filepath.Join(dir, "home", "cache")
			path := buildAppImage(t, filepath.Join(dir), c.tree, c.o)
			before := listTree(t, outside)
			appDir, err := extractAppImage(path, cache)
			if err == nil {
				t.Fatalf("extracted to %s", appDir)
			}
			if c.want != "" && !strings.Contains(err.Error(), c.want) {
				t.Errorf("error %q lacks %q", err, c.want)
			}
			if after := listTree(t, outside); strings.Join(after, "\n") != strings.Join(before, "\n") {
				t.Errorf("wrote outside the cache: %v", after)
			}
			if ents, _ := os.ReadDir(cache); len(ents) != 0 {
				t.Errorf("a failed extraction left %v in the cache", ents)
			}
		})
	}
}

// Links that stay inside are kept, and none resolves outside the AppDir,
// even through another link (s -> . then s/../x).
func TestExtractKeepsInnerSymlinks(t *testing.T) {
	dir := t.TempDir()
	cache := filepath.Join(dir, "cache")
	tree := aiTree{
		"AppRun":         "->usr/bin/app",
		"usr/bin/app":    envAppRun,
		"usr/lib/a.so":   "->../../usr/lib/b.so",
		"usr/lib/b.so":   "b",
		"usr/lib/s":      "->.",
		"usr/lib/x":      "->s/../../x",
		"usr/share/self": "->../share",
		// Lexically "outside" in the AppDir; written as is, the kernel
		// would follow s to the AppDir, then climb out of it.
		"s":   "->.",
		"esc": "->s/../outside",
	}
	path := buildAppImage(t, dir, tree, aiOpts{setuid: []string{"usr/bin/app"}})
	// What esc would reach, so that EvalSymlinks resolves it.
	if err := os.MkdirAll(filepath.Join(cache, "outside"), 0o755); err != nil {
		t.Fatal(err)
	}
	appDir, err := extractAppImage(path, cache)
	if err != nil {
		t.Fatal(err)
	}
	realDir, _ := filepath.EvalSymlinks(appDir)
	_ = filepath.WalkDir(appDir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.Type()&fs.ModeSymlink == 0 {
			return nil
		}
		target, _ := os.Readlink(p)
		resolved := filepath.Join(filepath.Dir(p), target)
		if r, err := filepath.EvalSymlinks(p); err == nil {
			resolved = r
		}
		if resolved != realDir && !strings.HasPrefix(resolved, realDir+"/") {
			t.Errorf("%s -> %s resolves to %s, outside %s", p, target, resolved, realDir)
		}
		return nil
	})
	st, err := os.Stat(filepath.Join(appDir, "usr/bin/app"))
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode()&(fs.ModeSetuid|fs.ModeSetgid) != 0 || st.Mode().Perm()&0o022 != 0 {
		t.Errorf("mode %v: setuid/setgid or group/other write kept", st.Mode())
	}
}

func TestExtractChecksFreeSpace(t *testing.T) {
	dir := t.TempDir()
	cache := filepath.Join(dir, "cache")
	path := buildAppImage(t, dir, muslTree(), aiOpts{})
	old := appimageFreeSpace
	t.Cleanup(func() { appimageFreeSpace = old })
	appimageFreeSpace = func(string) (uint64, error) { return 100, nil }
	_, err := extractAppImage(path, cache)
	if err == nil || !strings.Contains(err.Error(), "not enough free space") {
		t.Fatalf("err %v", err)
	}
	if ents, _ := os.ReadDir(cache); len(ents) != 0 {
		t.Fatalf("left %v", ents)
	}
}

// Two runs of the same AppImage at once: both get the whole AppDir, and
// the cache has one entry.
func TestExtractConcurrent(t *testing.T) {
	dir := t.TempDir()
	cache := filepath.Join(dir, "cache")
	tree := muslTree()
	for i := 0; i < 50; i++ {
		tree[fmt.Sprintf("usr/share/f%02d", i)] = strings.Repeat(fmt.Sprint(i), 5000)
	}
	path := buildAppImage(t, dir, tree, aiOpts{comp: "xz"})
	var wg sync.WaitGroup
	dirs := make([]string, 6)
	errs := make([]error, 6)
	for i := range dirs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			dirs[i], errs[i] = extractAppImage(path, cache)
		}(i)
	}
	wg.Wait()
	for i := range dirs {
		if errs[i] != nil || dirs[i] != dirs[0] {
			t.Fatalf("run %d: %s %v", i, dirs[i], errs[i])
		}
	}
	for p, v := range tree {
		if got, err := os.ReadFile(filepath.Join(dirs[0], p)); err != nil || string(got) != v {
			t.Errorf("%s differs: %v", p, err)
		}
	}
	if names := cacheEntries(t, cache); len(names) != 1 {
		t.Fatalf("cache holds %v, want one entry", names)
	}
}

func TestNeedsGlibc(t *testing.T) {
	cases := []struct {
		name string
		tree aiTree
		want bool
	}{
		{"AppRun links to a glibc ELF", aiTree{"AppRun": "->usr/bin/app", "usr/bin/app": string(fakeELF(glibcInterp, 0))}, true},
		{"AppRun is a glibc ELF", aiTree{"AppRun": string(fakeELF(glibcInterp, 0))}, true},
		{"AppRun links to a musl ELF", aiTree{"AppRun": "->usr/bin/app", "usr/bin/app": string(fakeELF(muslInterp, 0))}, false},
		{"AppRun is static", aiTree{"AppRun": string(fakeELF("", 0))}, false},
		{"script over musl and static ELFs", aiTree{"AppRun": "#!/bin/sh\n", "usr/bin/a": string(fakeELF(muslInterp, 0)), "usr/lib/b.so": string(fakeELF("", 0)), "usr/share/c": "text"}, false},
		{"script over ELFs, one glibc", aiTree{"AppRun": "#!/bin/sh\n", "usr/bin/a": string(fakeELF(muslInterp, 0)), "usr/lib/deep/b": string(fakeELF(glibcInterp, 0))}, true},
		// AppRun decides when it is an ELF: a glibc program it starts
		// reaches the stand-in loader, which hands it to the box.
		{"static AppRun over a glibc program", aiTree{"AppRun": string(fakeELF("", 0)), "usr/bin/a": string(fakeELF(glibcInterp, 0))}, false},
		{"musl AppRun with a glibc helper in usr/lib", aiTree{"AppRun": "->usr/bin/app", "usr/bin/app": string(fakeELF(muslInterp, 0)), "usr/lib/helper": string(fakeELF(glibcInterp, 0)), "usr/lib/ld-linux-x86-64.so.2": string(fakeELF("", 0))}, false},
		{"bundles its own ld-linux", aiTree{"AppRun": "#!/bin/sh\nexec \"$APPDIR/lib64/ld-linux-x86-64.so.2\" \"$APPDIR/opt/app\"\n", "lib64/ld-linux-x86-64.so.2": string(fakeELF("", 0)), "opt/app": "x"}, true},
		{"ELF naming a bundled loader", aiTree{"AppRun": string(fakeELF("/tmp/.mount_x/lib/ld-linux-x86-64.so.2", 0))}, true},
		// Only the x86-64 glibc loader: the box is x86-64 Ubuntu.
		{"i386 glibc AppRun", aiTree{"AppRun": string(fakeELF("/lib/ld-linux.so.2", 0))}, false},
		{"aarch64 glibc AppRun", aiTree{"AppRun": string(fakeELF("/lib/ld-linux-aarch64.so.1", 0))}, false},
		// A script AppRun: only usr/bin and usr/lib are looked at.
		{"script over a glibc ELF elsewhere", aiTree{"AppRun": "#!/bin/sh\n", "opt/app": string(fakeELF(glibcInterp, 0)), "usr/bin/a": string(fakeELF(muslInterp, 0))}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			writeTree(t, dir, c.tree)
			got, err := needsGlibc(dir)
			if err != nil {
				t.Fatal(err)
			}
			if got != c.want {
				t.Fatalf("needsGlibc = %v, want %v", got, c.want)
			}
		})
	}
	if _, err := needsGlibc(t.TempDir()); err == nil {
		t.Error("no AppRun: no error")
	}
}

// Real toolchain output, where a C compiler is available: a dynamic
// program follows the host's libc, a static one is native.
// A directory of thousands of files extracts in linear time: each file is
// opened through its directory entry, not looked up again by path.
func TestExtractManyFiles(t *testing.T) {
	dir := t.TempDir()
	tree := muslTree()
	for i := 0; i < 5000; i++ {
		tree[fmt.Sprintf("usr/share/icons/i%05d", i)] = fmt.Sprint(i)
	}
	path := buildAppImage(t, dir, tree, aiOpts{})
	start := time.Now()
	appDir, err := extractAppImage(path, filepath.Join(dir, "cache"))
	if err != nil {
		t.Fatal(err)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("5000 files took %v", d)
	}
	if got, err := os.ReadFile(filepath.Join(appDir, "usr/share/icons/i04321")); err != nil || string(got) != "4321" {
		t.Fatalf("content %q %v", got, err)
	}
}

func TestNeedsGlibcRealCompiler(t *testing.T) {
	cc, err := exec.LookPath("cc")
	if err != nil {
		t.Skip("no C compiler")
	}
	dir := t.TempDir()
	src := filepath.Join(dir, "m.c")
	write(t, src, "int main(void){return 0;}\n")
	for _, static := range []bool{false, true} {
		app := filepath.Join(dir, fmt.Sprintf("app-%v", static))
		if err := os.MkdirAll(app, 0o755); err != nil {
			t.Fatal(err)
		}
		args := []string{"-o", filepath.Join(app, "AppRun"), src}
		if static {
			args = append([]string{"-static"}, args...)
		}
		if out, err := exec.Command(cc, args...).CombinedOutput(); err != nil {
			t.Logf("cc %v: %v\n%s", args, err, out)
			continue
		}
		interp, _ := elfInterp(filepath.Join(app, "AppRun"))
		want := strings.Contains(interp, "ld-linux")
		got, err := needsGlibc(app)
		if err != nil || got != want {
			t.Errorf("static=%v interp %q: needsGlibc %v %v, want %v", static, interp, got, err, want)
		}
		if static && got {
			t.Errorf("a static program goes to the box")
		}
	}
}

func readOut(t *testing.T, path string) map[string]string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("AppRun did not run: %v", err)
	}
	m := map[string]string{}
	for i, line := range strings.Split(strings.TrimRight(string(b), "\n"), "\n") {
		if k, v, ok := strings.Cut(line, "="); ok && i < 6 {
			m[k] = v
		} else {
			m[fmt.Sprintf("arg%d", i-6)] = line
		}
	}
	return m
}

func appimageFake(t *testing.T) (*glibcFake, string) {
	t.Helper()
	f := newGlibcFake(t)
	t.Setenv("XDG_CACHE_HOME", filepath.Join(f.home, ".cache"))
	out := filepath.Join(f.dir, "apprun.out")
	t.Setenv("AI_TEST_OUT", out)
	return f, out
}

func TestRunAppImageNative(t *testing.T) {
	f, out := appimageFake(t)
	// Native needs no Docker and works on the live system.
	os.Remove(glibc.dockerSock)
	write(t, glibc.liveMarker, "")
	path := buildAppImage(t, filepath.Join(f.home, "Downloads"), muslTree(), aiOpts{})
	wd := t.TempDir()
	t.Chdir(wd)
	code := glibcRun(runOpts{AppImage: true}, path, []string{"a b", "-x"})
	if code != 5 {
		t.Fatalf("exit %d, want AppRun's 5: %s", code, f.stderr.String())
	}
	m := readOut(t, out)
	appDir := filepath.Join(f.home, ".cache", "isotope", "appimages", fileSHA(t, path))
	want := map[string]string{"APPDIR": appDir, "APPIMAGE": path, "ARGV0": path, "OWD": wd, "PWD": wd, "GUARD": "", "arg0": "a b", "arg1": "-x"}
	for k, v := range want {
		if m[k] != v {
			t.Errorf("%s = %q, want %q", k, m[k], v)
		}
	}
	if n := len(f.calls(t)); n != 0 {
		t.Errorf("a musl AppImage called docker or distrobox: %v", f.calls(t))
	}
}

func TestRunAppImageBox(t *testing.T) {
	f, out := appimageFake(t)
	write(t, f.state, "")
	path := buildAppImage(t, filepath.Join(f.home, "Downloads"), glibcTree(), aiOpts{})
	wd := filepath.Join(f.home, "work")
	if err := os.MkdirAll(wd, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(wd)
	code := glibcRun(runOpts{AppImage: true}, path, []string{"x y"})
	if code != 5 {
		t.Fatalf("exit %d, want 5: %s", code, f.stderr.String())
	}
	appDir := filepath.Join(f.home, ".cache", "isotope", "appimages", fileSHA(t, path))
	if n := f.count(t, "distrobox enter isotope-glibc -- "); n != 1 {
		t.Fatalf("calls %v", f.calls(t))
	}
	calls := f.calls(t)
	last := calls[len(calls)-1]
	if !strings.Contains(last, filepath.Join(appDir, "AppRun")) {
		t.Errorf("the box did not run the cached AppRun: %s", last)
	}
	m := readOut(t, out)
	want := map[string]string{"APPDIR": appDir, "APPIMAGE": path, "ARGV0": path, "OWD": wd, "GUARD": "1", "arg0": "x y"}
	for k, v := range want {
		if m[k] != v {
			t.Errorf("%s = %q, want %q", k, m[k], v)
		}
	}

	// auto=off: the automatic hand-off to the box is refused, and the
	// message says how to ask for it; --box is consent.
	os.Remove(f.log)
	t.Setenv("ISOTOPE_GLIBC_AUTO", "0")
	if code := glibcRun(runOpts{AppImage: true}, path, nil); code != 127 ||
		!strings.Contains(f.stderr.String(), "isotope glibc run --box "+path) {
		t.Fatalf("auto=off: exit %d: %s", code, f.stderr.String())
	}
	if f.count(t, "distrobox") != 0 {
		t.Fatalf("auto=off used the box: %v", f.calls(t))
	}
	if code := glibcRun(runOpts{AppImage: true, Box: true}, path, nil); code != 5 {
		t.Fatalf("auto=off --box: exit %d: %s", code, f.stderr.String())
	}
	// A native AppImage still runs with auto=off.
	musl := buildAppImage(t, filepath.Join(f.home, "Downloads"), muslTree(), aiOpts{})
	if code := glibcRun(runOpts{AppImage: true}, musl, nil); code != 5 {
		t.Fatalf("auto=off native: exit %d: %s", code, f.stderr.String())
	}
	os.Unsetenv("ISOTOPE_GLIBC_AUTO")

	// With --box, live is refused before anything is unpacked.
	other := buildAppImage(t, filepath.Join(f.home, "Downloads"), glibcTree(), aiOpts{comp: "xz"})
	write(t, glibc.liveMarker, "")
	if code := glibcRun(runOpts{AppImage: true, Box: true}, other, nil); code != 127 || !strings.Contains(f.stderr.String(), "live system") {
		t.Fatalf("live --box: exit %d: %s", code, f.stderr.String())
	}
	if _, err := os.Stat(filepath.Join(f.home, ".cache", "isotope", "appimages", fileSHA(t, other))); err == nil {
		t.Fatal("unpacked before the live refusal")
	}
	os.Remove(glibc.liveMarker)

	// The box's refusals apply to a glibc AppImage.
	os.Remove(f.log)
	glibc.euid, glibc.uid = func() int { return 0 }, func() int { return 0 }
	if code := glibcRun(runOpts{AppImage: true}, path, nil); code != 127 || !strings.Contains(f.stderr.String(), "root user") {
		t.Fatalf("root: exit %d: %s", code, f.stderr.String())
	}
}

func TestNativeBoxOverride(t *testing.T) {
	f, out := appimageFake(t)
	write(t, f.state, "")
	dl := filepath.Join(f.home, "Downloads")
	glibcAI := buildAppImage(t, dl, glibcTree(), aiOpts{})
	muslAI := buildAppImage(t, dl, muslTree(), aiOpts{})

	if code := glibcRun(runOpts{AppImage: true, Native: true}, glibcAI, nil); code != 5 {
		t.Fatalf("--native: exit %d: %s", code, f.stderr.String())
	}
	if len(f.calls(t)) != 0 || readOut(t, out)["GUARD"] != "" {
		t.Fatalf("--native went to the box: %v", f.calls(t))
	}
	if code := glibcRun(runOpts{AppImage: true, Box: true}, muslAI, nil); code != 5 {
		t.Fatalf("--box: exit %d: %s", code, f.stderr.String())
	}
	if f.count(t, "distrobox enter isotope-glibc -- ") != 1 || readOut(t, out)["GUARD"] != "1" {
		t.Fatalf("--box did not use the box: %v", f.calls(t))
	}

	// The flags through the CLI: --native and --box imply --appimage, and
	// cannot be given together.
	var buf bytes.Buffer
	os.Remove(f.log)
	if code := cmdGlibc([]string{"run", "--native", "--", glibcAI}, strings.NewReader(""), &buf, &buf); code != 5 || len(f.calls(t)) != 0 {
		t.Fatalf("CLI --native: exit %d, calls %v: %s", code, f.calls(t), f.stderr.String())
	}
	if code := cmdGlibc([]string{"run", "--native", "--box", glibcAI}, strings.NewReader(""), &buf, &buf); code != exitUsage {
		t.Fatalf("--native --box: exit %d", code)
	}
	// Not an AppImage: a clear sentence and 127.
	plain := filepath.Join(dl, "plain")
	write(t, plain, "#!/bin/sh\n")
	buf.Reset()
	if code := cmdGlibc([]string{"run", "--appimage", plain}, strings.NewReader(""), &buf, &buf); code != 127 ||
		!strings.Contains(buf.String(), "not an AppImage") {
		t.Fatalf("plain: exit %d: %s", code, buf.String())
	}
}

func TestCachePrune(t *testing.T) {
	dir := t.TempDir()
	cache := filepath.Join(dir, "appimages")
	old := time.Now().Add(-31 * 24 * time.Hour)
	recent := time.Now().Add(-29 * 24 * time.Hour)
	sha := func(c byte) string { return strings.Repeat(string(c), 64) }
	mk := func(name string, when time.Time) string {
		p := filepath.Join(cache, name)
		if err := os.MkdirAll(filepath.Join(p, "usr", "ro"), 0o755); err != nil {
			t.Fatal(err)
		}
		write(t, filepath.Join(p, "usr", "ro", "f"), "x")
		// A read-only directory, as an AppImage may ship, is still removed.
		if err := os.Chmod(filepath.Join(p, "usr", "ro"), 0o555); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(p, when, when); err != nil {
			t.Fatal(err)
		}
		return p
	}
	stale := mk(sha('a'), old)
	fresh := mk(sha('b'), recent)
	busy := mk(sha('c'), old)
	tmpOld := mk(".tmp-"+sha('d')+"-1", time.Now().Add(-48*time.Hour))
	tmpNew := mk(".tmp-"+sha('e')+"-1", time.Now())
	other := filepath.Join(cache, "README")
	write(t, other, "not ours")

	lock, err := appimageLock(busy, false)
	if err != nil {
		t.Fatal(err)
	}
	appimagePrune(cache, appimagePruneAge)
	for p, keep := range map[string]bool{stale: false, fresh: true, busy: true, tmpOld: false, tmpNew: true, other: true} {
		_, err := os.Lstat(p)
		if keep && err != nil {
			t.Errorf("%s removed", filepath.Base(p))
		}
		if !keep && err == nil {
			t.Errorf("%s kept", filepath.Base(p))
		}
	}
	lock.Close()

	// reset removes every entry that is not in use.
	appimagePrune(cache, 0)
	for _, p := range []string{fresh, busy} {
		if _, err := os.Lstat(p); err == nil {
			t.Errorf("%s kept by a full prune", filepath.Base(p))
		}
	}

	// A run prunes: a stale entry goes, the one it runs is touched.
	f, out := appimageFake(t)
	_ = out
	aiCache := filepath.Join(f.home, ".cache", "isotope", "appimages")
	gone := filepath.Join(aiCache, sha('f'))
	if err := os.MkdirAll(gone, 0o755); err != nil {
		t.Fatal(err)
	}
	_ = os.Chtimes(gone, old, old)
	path := buildAppImage(t, filepath.Join(f.home, "Downloads"), muslTree(), aiOpts{})
	if code := glibcRun(runOpts{AppImage: true}, path, nil); code != 5 {
		t.Fatalf("exit %d: %s", code, f.stderr.String())
	}
	if _, err := os.Lstat(gone); err == nil {
		t.Error("a run did not prune a stale entry")
	}
	used := filepath.Join(aiCache, fileSHA(t, path))
	_ = os.Chtimes(used, old, old)
	if code := glibcRun(runOpts{AppImage: true}, path, nil); code != 5 {
		t.Fatalf("second run: exit %d", code)
	}
	if st, err := os.Stat(used); err != nil || time.Since(st.ModTime()) > time.Hour {
		t.Errorf("the entry in use was not touched: %v", err)
	}

	// isotope glibc reset prunes the whole cache.
	write(t, f.state, "")
	var buf bytes.Buffer
	if code := cmdGlibc([]string{"reset", "--yes"}, strings.NewReader(""), &buf, &buf); code != 0 {
		t.Fatalf("reset: exit %d: %s", code, buf.String())
	}
	if _, err := os.Lstat(used); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("reset kept the AppImage cache: %v", err)
	}
}

// The index maps a file's identity to its entry and decision: a second
// run neither hashes the file nor scans the AppDir.
func TestAppImageIndex(t *testing.T) {
	f, out := appimageFake(t)
	_ = out
	tree := aiTree{"AppRun": envAppRun, "usr/bin/app": string(fakeELF(muslInterp, 0))}
	path := buildAppImage(t, filepath.Join(f.home, "Downloads"), tree, aiOpts{})
	if code := glibcRun(runOpts{AppImage: true}, path, nil); code != 5 {
		t.Fatalf("exit %d: %s", code, f.stderr.String())
	}
	cache := filepath.Join(f.home, ".cache", "isotope", "appimages")
	appDir := filepath.Join(cache, fileSHA(t, path))
	// A scan would now say glibc: the hit keeps the stored decision, so
	// neither the scan nor the extraction (which hashes) ran.
	write(t, filepath.Join(appDir, "usr/bin/app"), string(fakeELF(glibcInterp, 0)))
	if code := glibcRun(runOpts{AppImage: true}, path, nil); code != 5 || len(f.calls(t)) != 0 {
		t.Fatalf("index hit: exit %d, calls %v: %s", code, f.calls(t), f.stderr.String())
	}
	// Any change of the file changes its ctime: a chmod is a miss.
	if err := os.Chmod(path, 0o700); err != nil {
		t.Fatal(err)
	}
	write(t, f.state, "")
	if code := glibcRun(runOpts{AppImage: true}, path, nil); code != 5 || f.count(t, "distrobox enter") != 1 {
		t.Fatalf("chmod: exit %d, calls %v", code, f.calls(t))
	}
	os.Remove(f.log)

	// A broken index is a miss: the file is hashed and the AppDir scanned
	// again (and is now glibc).
	write(t, filepath.Join(cache, appimageIndexName), "{not json")
	if code := glibcRun(runOpts{AppImage: true}, path, nil); code != 5 || f.count(t, "distrobox enter") != 1 {
		t.Fatalf("broken index: exit %d, calls %v: %s", code, f.calls(t), f.stderr.String())
	}

	// A changed file is a miss: a new entry.
	b, _ := os.ReadFile(path)
	if err := os.WriteFile(path, append(b, 0), 0o755); err != nil {
		t.Fatal(err)
	}
	os.Remove(f.log)
	if code := glibcRun(runOpts{AppImage: true}, path, nil); code != 5 {
		t.Fatalf("changed: exit %d: %s", code, f.stderr.String())
	}
	if _, err := os.Stat(filepath.Join(cache, fileSHA(t, path))); err != nil {
		t.Fatalf("a changed file did not get its own entry: %v", err)
	}
}
