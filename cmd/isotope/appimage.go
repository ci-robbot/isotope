package main

// AppImages (type 2): an ELF runtime followed by a squashfs image. Isotope
// does not run the runtime (it needs FUSE, and would start the binfmt
// handler again); it reads the squashfs itself, extracts it once into
// $XDG_CACHE_HOME/isotope/appimages/<sha256 of the file>/, and runs AppRun
// from there: on the host when nothing in it is built for glibc (musl or
// static), in the glibc box otherwise. The binfmt rule
// (rootfs/common/usr/lib/binfmt.d/isotope-appimage.conf) sends every type 2
// AppImage to /usr/libexec/isotope-appimage (tools/isotope-appimage.c),
// which runs `isotope glibc run --appimage -- FILE ARGS...` on the host; in a
// container it runs the AppImage's own runtime instead. See
// docs/superpowers/specs/2026-11-23-glibc-programs-design.md, "AppImages".
//
// The squashfs is untrusted input. The extraction never writes outside a
// fresh temporary directory next to the cache entry:
//   - every entry is listed and checked before anything is written: a name
//     that is empty, ".", "..", or has a "/" or NUL fails, and so does a
//     name twice in one directory, a device, a FIFO or a socket, and a
//     symlink whose target is absolute or climbs out of the AppDir;
//   - a symlink is written as a symlink with its cleaned target and never
//     followed: every directory on a path is one this process made with
//     mkdir, and files are created with O_EXCL|O_NOFOLLOW;
//   - setuid, setgid, sticky and group or other write bits are dropped;
//   - the total is the sum of the sizes the squashfs declares, refused when
//     it is far larger than the image or than the free space, and no file
//     gets more bytes than it declares;
//   - the temporary directory is renamed into place only when complete,
//     so a crash or a full disk never leaves a half-made entry, and two runs
//     of one AppImage at once take turns on the entry's lock.

import (
	"crypto/sha256"
	"debug/elf"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/diskfs/go-diskfs/backend/file"
	"github.com/diskfs/go-diskfs/filesystem"
	"github.com/diskfs/go-diskfs/filesystem/squashfs"
	"golang.org/x/sys/unix"
)

const (
	// appimagePruneAge: an entry not used for this long is removed by the
	// next AppImage run.
	appimagePruneAge = 30 * 24 * time.Hour
	// appimageTmpAge: a temporary directory this old was left by a crash.
	appimageTmpAge = 24 * time.Hour
	// An AppImage may unpack to at most appimageRatio times its squashfs
	// plus appimageMargin. Real AppImages unpack to 2 to 5 times their size;
	// a squashfs of sparse files can declare terabytes in a few kilobytes.
	appimageRatio  = 20
	appimageMargin = 64 << 20
	// The free space kept on top of the declared total.
	appimageFreeMargin = 16 << 20
	appimageMaxEntries = 1 << 20
	appimageMaxDepth   = 128
)

// appimageFreeSpace is the space this user can write under dir. The tests
// replace it.
var appimageFreeSpace = func(dir string) (uint64, error) {
	var st unix.Statfs_t
	if err := unix.Statfs(dir, &st); err != nil {
		return 0, err
	}
	return st.Bavail * uint64(st.Bsize), nil
}

// The squashfs compressions go-diskfs reads (superblock compression ids).
// lz4 (5) fails in go-diskfs 1.9.4 on mksquashfs's output.
var appimageCompressions = map[uint16]string{1: "gzip", 2: "lzma", 3: "lzo", 4: "xz", 6: "zstd"}

func appimageCacheDir() string {
	dir := os.Getenv("XDG_CACHE_HOME")
	if dir == "" {
		if c, err := os.UserCacheDir(); err == nil {
			dir = c
		} else {
			home, _ := os.UserHomeDir()
			dir = filepath.Join(home, ".cache")
		}
	}
	return filepath.Join(dir, "isotope", "appimages")
}

// appimageOffset is where the squashfs starts: the end of the runtime ELF's
// section header table, as the AppImage specification defines it.
func appimageOffset(f *os.File) (int64, error) {
	h := make([]byte, 64)
	if _, err := f.ReadAt(h, 0); err != nil {
		return 0, fmt.Errorf("it is not an ELF file: %w", err)
	}
	if string(h[:4]) != "\x7fELF" {
		return 0, errors.New("it is not an ELF file")
	}
	var bo binary.ByteOrder
	switch h[5] {
	case 1:
		bo = binary.LittleEndian
	case 2:
		bo = binary.BigEndian
	default:
		return 0, errors.New("its ELF header is damaged")
	}
	var shoff uint64
	var shentsize, shnum uint16
	switch h[4] {
	case 2:
		shoff, shentsize, shnum = bo.Uint64(h[0x28:]), bo.Uint16(h[0x3a:]), bo.Uint16(h[0x3c:])
	case 1:
		shoff, shentsize, shnum = uint64(bo.Uint32(h[0x20:])), bo.Uint16(h[0x2e:]), bo.Uint16(h[0x30:])
	default:
		return 0, errors.New("its ELF header is damaged")
	}
	if shoff == 0 || shnum == 0 || shentsize == 0 || shoff > 1<<40 {
		return 0, errors.New("its runtime has no section headers, so the image cannot be found")
	}
	end := shoff + uint64(shentsize)*uint64(shnum)
	st, err := f.Stat()
	if err != nil {
		return 0, err
	}
	if end > uint64(st.Size()) {
		return 0, errors.New("its runtime ends after the end of the file")
	}
	return int64(end), nil
}

// appimageType says whether f is a type 2 AppImage, with a sentence when it
// is not.
func appimageType(f *os.File) error {
	m := make([]byte, 3)
	if _, err := f.ReadAt(m, 8); err != nil || m[0] != 'A' || m[1] != 'I' {
		return errors.New("it is not an AppImage")
	}
	switch m[2] {
	case 2:
		return nil
	case 1:
		return errors.New("it is a type 1 AppImage, an old format Isotope does not run; ask its maker for a current (type 2) AppImage")
	default:
		return errors.New("it is not an AppImage Isotope knows")
	}
}

type aiEntry struct {
	rel    string // slash-separated, relative to the AppDir
	kind   fs.FileMode
	perm   fs.FileMode
	size   int64
	target string // cleaned symlink target
	// open opens a regular file through its directory entry. go-diskfs's
	// OpenFile by path reads the inode of every entry of the parent
	// directory on each call, which is quadratic in a large directory.
	open func() (filesystem.File, error)
}

// extractAppImage extracts the AppImage at path into cacheDir/<sha256>/ and
// returns that directory. An entry that is already there is reused and
// marked as used.
func extractAppImage(path, cacheDir string) (appDir string, err error) {
	return extractAppImageNotify(path, cacheDir, nil)
}

// extractAppImageNotify is extractAppImage that calls unpacking, when it
// is not nil, just before an extraction starts (not when the entry is
// there already).
func extractAppImageNotify(path, cacheDir string, unpacking func()) (appDir string, err error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	if err := appimageType(f); err != nil {
		return "", err
	}
	off, err := appimageOffset(f)
	if err != nil {
		return "", err
	}
	st, err := f.Stat()
	if err != nil {
		return "", err
	}
	sb := make([]byte, 96)
	if _, err := f.ReadAt(sb, off); err != nil || binary.LittleEndian.Uint32(sb) != 0x73717368 {
		return "", errors.New("its image is missing or damaged (no squashfs after the runtime)")
	}
	if c := binary.LittleEndian.Uint16(sb[20:]); appimageCompressions[c] == "" {
		return "", fmt.Errorf("this AppImage uses a compression Isotope cannot read yet (squashfs compression %d)", c)
	}
	bytesUsed := binary.LittleEndian.Uint64(sb[40:])
	if bytesUsed > uint64(st.Size()-off) {
		return "", errors.New("its image is cut short: the file is incomplete or damaged")
	}

	h := sha256.New()
	if _, err := io.Copy(h, io.NewSectionReader(f, 0, st.Size())); err != nil {
		return "", err
	}
	sum := hex.EncodeToString(h.Sum(nil))
	entry := filepath.Join(cacheDir, sum)
	appDir = entry
	if appimageReady(appDir) {
		return appDir, nil
	}
	if err := os.MkdirAll(cacheDir, 0o700); err != nil {
		return "", err
	}
	// One extraction per entry at a time; a second run waits, then finds
	// it done.
	lock, err := appimageLock(appDir, true)
	if err != nil {
		return "", err
	}
	defer func() {
		// A failed extraction leaves nothing, not even the lock file.
		if err != nil {
			if _, serr := os.Lstat(entry); errors.Is(serr, os.ErrNotExist) {
				_ = os.Remove(entry + ".lock")
			}
		}
		lock.Close()
	}()
	if appimageReady(appDir) {
		return appDir, nil
	}
	if unpacking != nil {
		unpacking()
	}
	tmp, err := os.MkdirTemp(cacheDir, ".tmp-"+sum+"-")
	if err != nil {
		return "", err
	}
	if err := appimageUnpack(f, off, st.Size()-off, bytesUsed, tmp); err != nil {
		_ = removeTree(tmp)
		if errors.Is(err, syscall.ENOSPC) {
			return "", fmt.Errorf("there is not enough free space in %s to unpack it", cacheDir)
		}
		return "", err
	}
	if err := os.Rename(tmp, appDir); err != nil {
		_ = removeTree(tmp)
		if appimageReady(appDir) {
			return appDir, nil
		}
		return "", err
	}
	return appDir, nil
}

// appimageReady says whether appDir is a finished entry, and marks it used.
func appimageReady(appDir string) bool {
	st, err := os.Lstat(appDir)
	if err != nil || !st.IsDir() {
		return false
	}
	now := time.Now()
	_ = os.Chtimes(appDir, now, now)
	return true
}

// appimageUnpack lists and checks the squashfs at off, then writes it into
// the empty directory dst.
//
// go-diskfs decompresses each block without a cap of its own (io.ReadAll,
// zstd.DecodeAll) and exposes none, so a crafted block can use much memory
// before the declared size check sees it. That is a crash of this process,
// as the user, for an AppImage the user is about to run anyway; the recover
// below cannot catch it.
func appimageUnpack(f *os.File, off, size int64, bytesUsed uint64, dst string) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("its image is damaged (%v)", r)
		}
	}()
	sq, err := squashfs.Read(file.New(f, true), size, off, 0)
	if err != nil {
		return fmt.Errorf("its image cannot be read: %w", err)
	}
	var entries []aiEntry
	var total int64
	if err := appimageList(sq, ".", 0, &entries, &total); err != nil {
		return err
	}
	if limit := int64(bytesUsed)*appimageRatio + appimageMargin; total > limit {
		return fmt.Errorf("it says it unpacks to %d MB, far larger than its own %d MB image; it is damaged or made to fill the disk",
			total>>20, bytesUsed>>20)
	}
	if free, err := appimageFreeSpace(filepath.Dir(dst)); err == nil && uint64(total)+appimageFreeMargin > free {
		return fmt.Errorf("there is not enough free space in %s to unpack it: it needs %d MB, %d MB are free",
			filepath.Dir(dst), (total+appimageFreeMargin)>>20, free>>20)
	}
	for _, e := range entries {
		full := filepath.Join(dst, filepath.FromSlash(e.rel))
		switch e.kind {
		case fs.ModeDir:
			if err := os.Mkdir(full, 0o755); err != nil {
				return err
			}
		case fs.ModeSymlink:
			if err := os.Symlink(e.target, full); err != nil {
				return err
			}
		default:
			if err := appimageWriteFile(e, full); err != nil {
				return err
			}
		}
	}
	return os.Chmod(dst, 0o755)
}

func appimageWriteFile(e aiEntry, full string) error {
	src, err := e.open()
	if err != nil {
		return fmt.Errorf("its image is damaged at %s: %w", e.rel, err)
	}
	out, err := os.OpenFile(full, os.O_WRONLY|os.O_CREATE|os.O_EXCL|unix.O_NOFOLLOW, 0o600)
	if err != nil {
		return err
	}
	n, err := io.Copy(out, io.LimitReader(src, e.size))
	if err == nil && n != e.size {
		err = fmt.Errorf("its image is damaged: %s is shorter than it says", e.rel)
	}
	if err == nil {
		err = out.Chmod(e.perm)
	}
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	return err
}

// appimageList appends the entries under dir, parents before children, and
// checks each one.
func appimageList(sq *squashfs.FileSystem, dir string, depth int, out *[]aiEntry, total *int64) error {
	if depth > appimageMaxDepth {
		return errors.New("its image has directories nested too deep; it is damaged")
	}
	ents, err := sq.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("its image cannot be read at %s: %w", dir, err)
	}
	seen := make(map[string]bool, len(ents))
	for _, d := range ents {
		name := d.Name()
		if name == "" || name == "." || name == ".." || strings.ContainsAny(name, "/\x00") {
			return fmt.Errorf("its image has an entry with an unsafe name (%q in %s)", name, dir)
		}
		if seen[name] {
			return fmt.Errorf("its image has %s twice in %s", name, dir)
		}
		seen[name] = true
		if len(*out) >= appimageMaxEntries {
			return errors.New("its image has too many files")
		}
		rel := path.Join(dir, name)
		info, err := d.Info()
		if err != nil {
			return fmt.Errorf("its image is damaged at %s: %w", rel, err)
		}
		mode := info.Mode()
		e := aiEntry{rel: rel, perm: mode.Perm()&^0o022 | 0o600}
		switch {
		case mode.IsDir():
			e.kind = fs.ModeDir
			*out = append(*out, e)
			if err := appimageList(sq, rel, depth+1, out, total); err != nil {
				return err
			}
			continue
		case mode&fs.ModeSymlink != 0:
			e.kind = fs.ModeSymlink
			var target string
			if s, ok := info.Sys().(*squashfs.StatT); ok {
				target = s.LinkTarget
			}
			if e.target, err = appimageLinkTarget(rel, target); err != nil {
				return err
			}
		case mode.IsRegular():
			if info.Size() < 0 {
				return fmt.Errorf("its image is damaged at %s", rel)
			}
			e.size = info.Size()
			o, ok := d.(interface {
				Open() (filesystem.File, error)
			})
			if !ok {
				return fmt.Errorf("its image cannot be read at %s", rel)
			}
			e.open = o.Open
			*total += e.size
			if *total < 0 {
				return errors.New("its image is damaged: the sizes overflow")
			}
		case mode&fs.ModeDevice != 0:
			return fmt.Errorf("its image has a device file (%s), which an AppImage never needs", rel)
		default:
			return fmt.Errorf("its image has a special file (%s, %v), which an AppImage never needs", rel, mode.Type())
		}
		*out = append(*out, e)
	}
	return nil
}

// appimageLinkTarget checks the target of the symlink at rel and returns
// it cleaned. Cleaning leaves ".." only at the front, so the link climbs
// through real directories this extraction made, never through another
// link, and the climb is checked against the link's depth.
func appimageLinkTarget(rel, target string) (string, error) {
	if target == "" || strings.ContainsRune(target, 0) {
		return "", fmt.Errorf("its image has a damaged symlink (%s)", rel)
	}
	if path.IsAbs(target) {
		return "", fmt.Errorf("its image has a symlink that points outside the AppImage (%s -> %s)", rel, target)
	}
	clean := path.Clean(target)
	up := 0
	for _, p := range strings.Split(clean, "/") {
		if p != ".." {
			break
		}
		up++
	}
	if up > strings.Count(rel, "/") {
		return "", fmt.Errorf("its image has a symlink that points outside the AppImage (%s -> %s)", rel, target)
	}
	return clean, nil
}

// removeTree removes dir, making read-only directories in it writable
// first. It follows no symlink.
func removeTree(dir string) error {
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err == nil && d.IsDir() {
			_ = os.Chmod(p, 0o700)
		}
		return nil
	})
	return os.RemoveAll(dir)
}

// appimageLock takes the lock of a cache entry, appDir + ".lock": shared
// while an AppImage runs from it (the fd is kept across the exec, so for as
// long as the program runs), exclusive while it is extracted or removed. It
// waits. The lock file is the one at the path when the lock is taken: a
// prune may remove it in between.
func appimageLock(appDir string, exclusive bool) (*os.File, error) {
	how := unix.LOCK_SH
	if exclusive {
		how = unix.LOCK_EX
	}
	for {
		l, err := os.OpenFile(appDir+".lock", os.O_RDWR|os.O_CREATE, 0o600)
		if err != nil {
			return nil, err
		}
		if err := flockRetry(l, how); err != nil {
			l.Close()
			return nil, err
		}
		if sameFile(l, appDir+".lock") {
			return l, nil
		}
		l.Close()
	}
}

func flockRetry(f *os.File, how int) error {
	for {
		err := unix.Flock(int(f.Fd()), how)
		if !errors.Is(err, unix.EINTR) {
			return err
		}
	}
}

func sameFile(f *os.File, p string) bool {
	a, err1 := f.Stat()
	b, err2 := os.Stat(p)
	return err1 == nil && err2 == nil && os.SameFile(a, b)
}

func isSHA256Hex(s string) bool {
	if len(s) != 64 {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil && strings.ToLower(s) == s
}

// appimagePrune removes the cache entries not used for olderThan, and
// every entry with olderThan 0 (isotope glibc reset). An entry in use (its
// lock held) stays. Temporary directories of a crashed extraction go after
// a day.
func appimagePrune(cacheDir string, olderThan time.Duration) {
	ents, err := os.ReadDir(cacheDir)
	if err != nil {
		return
	}
	now := time.Now()
	old := func(name string, age time.Duration) bool {
		st, err := os.Lstat(filepath.Join(cacheDir, name))
		return err == nil && (olderThan == 0 || now.Sub(st.ModTime()) > age)
	}
	for _, e := range ents {
		name := e.Name()
		switch {
		case strings.HasPrefix(name, ".tmp-") && len(name) > 5+64 && isSHA256Hex(name[5:69]):
			if old(name, appimageTmpAge) {
				appimageRemove(filepath.Join(cacheDir, name[5:69]), filepath.Join(cacheDir, name), func() bool { return old(name, appimageTmpAge) })
			}
		case isSHA256Hex(name) && e.IsDir():
			// Checked again under the lock: a run may have just used it.
			if old(name, olderThan) {
				appimageRemove(filepath.Join(cacheDir, name), filepath.Join(cacheDir, name), func() bool { return old(name, olderThan) })
			}
		case strings.HasSuffix(name, ".lock") && isSHA256Hex(strings.TrimSuffix(name, ".lock")):
			// A lock whose entry is gone.
			entry := filepath.Join(cacheDir, strings.TrimSuffix(name, ".lock"))
			if _, err := os.Lstat(entry); errors.Is(err, os.ErrNotExist) {
				appimageRemove(entry, "", nil)
			}
		case strings.HasPrefix(name, ".index-"):
			// An index write cut short.
			if old(name, appimageTmpAge) {
				_ = os.Remove(filepath.Join(cacheDir, name))
			}
		}
	}
}

// appimageRemove removes target (an entry, or a temporary directory of
// entry) when entry's lock is free and still (when not nil) says yes under
// it, and then the lock file when the entry is gone.
func appimageRemove(entry, target string, still func() bool) {
	l, err := os.OpenFile(entry+".lock", os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return
	}
	defer l.Close()
	if unix.Flock(int(l.Fd()), unix.LOCK_EX|unix.LOCK_NB) != nil || !sameFile(l, entry+".lock") {
		return
	}
	if target != "" && (still == nil || still()) {
		_ = removeTree(target)
	}
	if _, err := os.Lstat(entry); errors.Is(err, os.ErrNotExist) {
		_ = os.Remove(entry + ".lock")
	}
}

// glibcLoader is the x86-64 glibc loader's name. The box is x86-64 Ubuntu
// without multiarch: an i386 (ld-linux.so.2) or aarch64 program would not
// run there either.
const glibcLoader = "ld-linux-x86-64.so.2"

// needsGlibc says whether the AppDir must run in the glibc box.
//   - When AppRun is an ELF, or a symlink to one, that ELF alone decides:
//     glibc goes to the box, musl or static runs here. A glibc program it
//     starts reaches the stand-in loader, which hands it to the box, so a
//     wrong "native" costs nothing; a wrong "box" would break a musl
//     AppRun, which cannot start in the box.
//   - When AppRun is a script: the box when it runs a bundled glibc
//     loader, or when an ELF under usr/bin or usr/lib names the glibc
//     loader.
func needsGlibc(appDir string) (bool, error) {
	root, err := filepath.EvalSymlinks(appDir)
	if err != nil {
		return false, err
	}
	appRun := filepath.Join(root, "AppRun")
	if _, err := os.Lstat(appRun); err != nil {
		return false, errors.New("it has no AppRun, so Isotope does not know what to start")
	}
	target, err := filepath.EvalSymlinks(appRun)
	if err != nil || (target != root && !strings.HasPrefix(target, root+"/")) {
		return false, errors.New("its AppRun points to nothing inside it")
	}
	if interp, ok := elfInterp(target); ok {
		return glibcInterpName(interp), nil
	}
	if appimageRunsBundledLoader(root, target) {
		return true, nil
	}
	found := false
	for _, dir := range []string{"usr/bin", "usr/lib"} {
		_ = filepath.WalkDir(filepath.Join(root, dir), func(p string, d fs.DirEntry, err error) error {
			if err != nil || !d.Type().IsRegular() {
				return nil
			}
			if interp, ok := elfInterp(p); ok && glibcInterpName(interp) {
				found = true
				return filepath.SkipAll
			}
			return nil
		})
		if found {
			return true, nil
		}
	}
	return false, nil
}

// appimageRunsBundledLoader: the script names the glibc loader, and the
// AppDir has one.
func appimageRunsBundledLoader(root, script string) bool {
	f, err := os.Open(script)
	if err != nil {
		return false
	}
	head, _ := io.ReadAll(io.LimitReader(f, 64<<10))
	f.Close()
	if !strings.Contains(string(head), glibcLoader) {
		return false
	}
	found := false
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err == nil && d.Name() == glibcLoader && d.Type().IsRegular() {
			found = true
			return filepath.SkipAll
		}
		return nil
	})
	return found
}

// glibcInterpName: the x86-64 glibc loader, wherever it is (the system's,
// or one the AppImage bundles).
func glibcInterpName(interp string) bool {
	return path.Base(interp) == glibcLoader
}

// elfInterp returns the PT_INTERP of the ELF file p, "" for a static one;
// ok is false when p is not an ELF file.
func elfInterp(p string) (interp string, ok bool) {
	f, err := os.Open(p)
	if err != nil {
		return "", false
	}
	defer f.Close()
	m := make([]byte, 4)
	if _, err := io.ReadFull(f, m); err != nil || string(m) != "\x7fELF" {
		return "", false
	}
	defer func() {
		if recover() != nil {
			interp, ok = "", false
		}
	}()
	e, err := elf.NewFile(f)
	if err != nil {
		return "", false
	}
	for _, prog := range e.Progs {
		if prog.Type != elf.PT_INTERP {
			continue
		}
		b, _ := io.ReadAll(io.LimitReader(prog.Open(), 4096))
		return strings.TrimRight(string(b), "\x00"), true
	}
	return "", true
}

// appimageRun is `isotope glibc run --appimage|--native|--box FILE ARGS`.
func appimageRun(opts runOpts, file string, args []string) int {
	abs, err := filepath.Abs(file)
	if err != nil {
		abs = file
	}
	refuse := func(why, todo string) int {
		glibcFail(refusalText(abs, why, todo), "Cannot run "+filepath.Base(abs))
		return glibcExitCannotRun
	}
	// --box is known to need the box before anything is unpacked.
	if opts.Box {
		if why, todo := glibcRefusal(false); why != "" {
			return refuse(why, todo)
		}
	}
	cache := appimageCacheDir()
	appimagePrune(cache, appimagePruneAge)
	fail := func(err error) int {
		glibcFail(fmt.Sprintf("Cannot run %s: %v.", abs, err), "Cannot run "+filepath.Base(abs))
		return glibcExitCannotRun
	}
	unpacking := func() {
		const summary = "Opening an AppImage, first start only"
		if glibc.isTerminal() {
			fmt.Fprintf(glibc.stderr, "Unpacking %s into %s, first start only...\n", filepath.Base(abs), cache)
		} else {
			_ = glibc.notify(summary, "Isotope is unpacking "+filepath.Base(abs)+". It starts when this is done.")
		}
	}
	key, keyOK := appimageKey(abs)
	rec, hit := appimageIndexRec{}, false
	if keyOK {
		rec, hit = appimageIndexGet(cache, key)
	}
	var appDir string
	var lock *os.File
	for attempt := 0; attempt < 3 && lock == nil; attempt++ {
		if hit && appimageReady(filepath.Join(cache, rec.SHA)) {
			appDir = filepath.Join(cache, rec.SHA)
		} else {
			hit = false
			if appDir, err = extractAppImageNotify(abs, cache, unpacking); err != nil {
				return fail(err)
			}
		}
		l, err := appimageLock(appDir, false)
		if err != nil {
			return fail(err)
		}
		// A reset may have removed the entry before the lock was taken.
		if _, err := os.Lstat(appDir); err == nil {
			lock = l
		} else {
			l.Close()
			hit = false
		}
	}
	if lock == nil {
		return fail(errors.New("its unpacked copy was removed while it started (by `isotope glibc reset`?); try again"))
	}
	defer lock.Close() // reached only in tests, or when the exec failed
	box := opts.Box
	if !opts.Box && !opts.Native {
		if hit {
			box = rec.Box
		} else {
			if box, err = needsGlibc(appDir); err != nil {
				return fail(err)
			}
			if keyOK {
				appimageIndexPut(cache, key, appimageIndexRec{SHA: filepath.Base(appDir), Box: box})
			}
		}
	}
	argv0 := os.Getenv(glibcArgv0Env)
	if argv0 == "" {
		argv0 = file
	}
	owd, _ := os.Getwd()
	appRun := filepath.Join(appDir, "AppRun")

	if !box {
		// The entry's lock lasts as long as the program.
		_, _ = unix.FcntlInt(lock.Fd(), unix.F_SETFD, 0)
		env := appimageEnv(appDir, abs, argv0, owd)
		code, err := glibc.exec(appRun, append([]string{argv0}, args...), env)
		if err != nil {
			return fail(fmt.Errorf("its AppRun could not be started: %w", err))
		}
		return code
	}
	if !opts.Box {
		// The automatic hand-off honours auto=off, as the loader's does;
		// an explicit --box is consent.
		if glibcAutoOff() {
			return refuse("running them automatically is turned off for you",
				fmt.Sprintf("Run `isotope glibc run --box %s` yourself, or turn it on with `isotope glibc auto on`.", abs))
		}
		if why, todo := glibcRefusal(false); why != "" {
			return refuse(why, todo)
		}
	}
	_, _ = unix.FcntlInt(lock.Fd(), unix.F_SETFD, 0)
	home, _ := os.UserHomeDir()
	// env sets the variables in the box: distrobox passes its environment
	// on, but drops values that hold `"`, a backquote or `$`.
	pre := []string{"env",
		"APPDIR=" + boxPath(home, appDir),
		"APPIMAGE=" + boxPath(home, abs),
		"ARGV0=" + argv0,
		"OWD=" + boxPath(home, owd),
	}
	return glibcBoxRun(boxPath(home, appRun), argv0, pre, args)
}

// The index, index.json in the cache directory, maps a file's identity
// (device, inode, size, mtime and ctime in nanoseconds) to its entry and
// its box-or-native decision, so a later start neither hashes the file
// nor scans the AppDir. ctime cannot be set back (touch -d and cp -p set
// only mtime), so a changed file is a miss and is hashed as before. The
// cache is the user's own, so the user is the only one who can lie to it.
// A missing or broken index is a miss, never an error.
const appimageIndexName = "index.json"

type appimageIndexRec struct {
	SHA string `json:"sha"`
	Box bool   `json:"box"`
}

func appimageKey(p string) (string, bool) {
	var st unix.Stat_t
	if err := unix.Stat(p, &st); err != nil {
		return "", false
	}
	return fmt.Sprintf("%d:%d:%d:%d:%d", st.Dev, st.Ino, st.Size,
		st.Mtim.Sec*1e9+st.Mtim.Nsec, st.Ctim.Sec*1e9+st.Ctim.Nsec), true
}

func appimageIndexRead(cache string) map[string]appimageIndexRec {
	m := map[string]appimageIndexRec{}
	b, err := os.ReadFile(filepath.Join(cache, appimageIndexName))
	if err != nil || json.Unmarshal(b, &m) != nil {
		return map[string]appimageIndexRec{}
	}
	return m
}

func appimageIndexGet(cache, key string) (appimageIndexRec, bool) {
	rec, ok := appimageIndexRead(cache)[key]
	if !ok || !isSHA256Hex(rec.SHA) {
		return appimageIndexRec{}, false
	}
	return rec, true
}

// appimageIndexPut adds a record and drops those whose entry is gone, then
// writes the index whole through a rename. Two runs that write at once
// lose one record, which is only a later miss.
func appimageIndexPut(cache, key string, rec appimageIndexRec) {
	m := appimageIndexRead(cache)
	for k, r := range m {
		if !isSHA256Hex(r.SHA) {
			delete(m, k)
			continue
		}
		if _, err := os.Lstat(filepath.Join(cache, r.SHA)); err != nil {
			delete(m, k)
		}
	}
	m[key] = rec
	b, err := json.Marshal(m)
	if err != nil {
		return
	}
	f, err := os.CreateTemp(cache, ".index-")
	if err != nil {
		return
	}
	_, err = f.Write(b)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(f.Name(), filepath.Join(cache, appimageIndexName))
	}
	if err != nil {
		_ = os.Remove(f.Name())
	}
}

// appimageEnv is the environment of a native AppRun: this process's, with
// what the AppImage runtime sets.
func appimageEnv(appDir, appImage, argv0, owd string) []string {
	var env []string
	for _, e := range os.Environ() {
		k, _, _ := strings.Cut(e, "=")
		switch k {
		case glibcArgv0Env, glibcGuardEnv, "APPDIR", "APPIMAGE", "ARGV0", "OWD":
			continue
		}
		env = append(env, e)
	}
	return append(env, "APPDIR="+appDir, "APPIMAGE="+appImage, "ARGV0="+argv0, "OWD="+owd)
}
