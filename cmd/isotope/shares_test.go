package main

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"

	"github.com/kairos-community/isotope/pkg/shares"
)

// fakeMountScript stands for mount: it logs its arguments, its environment
// and the credentials file it was given, fails when the file "fail" exists
// (with its text), refuses vers=4.2 when "no-4.2" exists, and otherwise
// adds the mount to the fake mount table.
const fakeMountScript = `#!/bin/sh
d=$(dirname "$0")
{ printf 'mount'; for a in "$@"; do printf ' %s' "$a"; done; printf '\n'; env | sed 's/^/env /'; } >> "$d/log"
for a in "$@"; do
  case "$a" in *credentials=*)
    f=${a##*credentials=}; f=${f%%,*}
    printf 'cred-mode %s\n' "$(stat -c %a "$f")" >> "$d/log"
    sed 's/^/cred /' "$f" >> "$d/log";;
  esac
done
if [ -e "$d/block" ]; then
  echo blocked >> "$d/log"
  i=0; while [ ! -e "$d/release" ] && [ $i -lt 400 ]; do sleep 0.05; i=$((i+1)); done
fi
if [ -e "$d/fail" ]; then cat "$d/fail" >&2; exit 32; fi
case "$*" in *vers=4.2*) if [ -e "$d/no-4.2" ]; then echo "mount.nfs4: Protocol not supported" >&2; exit 32; fi;; esac
prev=; type=; src=; point=
for a in "$@"; do
  [ "$prev" = -t ] && type=$a
  src=$point; point=$a; prev=$a
done
printf '99 25 0:77 / %s rw - %s %s rw\n' "$point" "$type" "$src" >> "$d/mountinfo"
`

// fakeUmountScript stands for umount: it logs and drops the mount (its last
// argument).
const fakeUmountScript = `#!/bin/sh
d=$(dirname "$0")
echo "umount $*" >> "$d/log"
for a in "$@"; do p=$a; done
grep -v " $p rw - " "$d/mountinfo" > "$d/mountinfo.new"
mv "$d/mountinfo.new" "$d/mountinfo"
`

type sharesEnv struct {
	bin, media, creds string
	chowns            []string
}

func (e *sharesEnv) log(t *testing.T) string {
	t.Helper()
	b, _ := os.ReadFile(filepath.Join(e.bin, "log"))
	return string(b)
}

func (e *sharesEnv) mountLines(t *testing.T) []string {
	var out []string
	for _, l := range strings.Split(e.log(t), "\n") {
		if strings.HasPrefix(l, "mount ") {
			out = append(out, l)
		}
	}
	return out
}

func (e *sharesEnv) touch(t *testing.T, name, text string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(e.bin, name), []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

// sharesFixture points the daemon at fake mount and umount commands, a
// fake mount table and a temporary /run/media, and starts the service with
// polkit's answer allow. The caller (the test process's uid) is anna.
func sharesFixture(t *testing.T, allow bool) (*sharesEnv, *busFixture, *fakeAuth) {
	t.Helper()
	auth := &fakeAuth{allow: allow}
	e, f := sharesFixtureAuth(t, auth)
	return e, f, auth
}

// sharesFixtureAuth is sharesFixture with any Authorizer.
func sharesFixtureAuth(t *testing.T, auth Authorizer) (*sharesEnv, *busFixture) {
	t.Helper()
	dir := t.TempDir()
	e := &sharesEnv{bin: filepath.Join(dir, "bin"), media: filepath.Join(dir, "media"), creds: filepath.Join(dir, "run", "shares")}
	os.Mkdir(e.bin, 0o755)
	os.WriteFile(filepath.Join(e.bin, "mount"), []byte(fakeMountScript), 0o755)
	os.WriteFile(filepath.Join(e.bin, "umount"), []byte(fakeUmountScript), 0o755)
	os.WriteFile(filepath.Join(e.bin, "mountinfo"), []byte("22 1 8:2 / / rw - ext4 /dev/sda2 rw\n"), 0o644)
	set := func(p *string, v string) {
		old := *p
		*p = v
		t.Cleanup(func() { *p = old })
	}
	set(&sharesMediaRoot, e.media)
	set(&sharesCredDir, e.creds)
	set(&sharesPath, e.bin+":/usr/bin:/bin")
	set(&mountinfoPath, filepath.Join(e.bin, "mountinfo"))
	oldGID, oldChown, oldOwned := sharesGID, sharesChown, sharesRootOwned
	sharesGID = func(name string) (uint32, error) {
		if name != "anna" {
			return 0, errors.New("no such user")
		}
		return 1234, nil
	}
	sharesChown = func(p string, uid, gid int) error {
		e.chowns = append(e.chowns, p+" "+strconv.Itoa(uid)+":"+strconv.Itoa(gid))
		return nil
	}
	sharesRootOwned = func(fs.FileInfo) bool { return true }
	t.Cleanup(func() { sharesGID, sharesChown, sharesRootOwned = oldGID, oldChown, oldOwned })
	fakeUserName(t, func(uid uint32) (string, error) {
		if uid == uint32(os.Getuid()) {
			return "anna", nil
		}
		return "", errors.New("unknown uid")
	})
	daemonEnv(t, "v0.0.5")
	return e, startService(t, NewDaemon(&fakeBackend{}), auth)
}

// addMount puts a mount into the fake mount table.
func (e *sharesEnv) addMount(t *testing.T, point, fstype, source string) {
	t.Helper()
	os.MkdirAll(point, 0o700)
	f, err := os.OpenFile(filepath.Join(e.bin, "mountinfo"), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	fmt.Fprintf(f, "99 25 0:77 / %s rw - %s %s rw\n", point, fstype, source)
}

func mountShareCall(t *testing.T, f *busFixture, url string, opts map[string]dbus.Variant, lines []string) (string, *dbus.Error) {
	t.Helper()
	if opts == nil {
		opts = map[string]dbus.Variant{}
	}
	var point string
	var err error
	if serr := withSecrets(lines, func(fd dbus.UnixFD) error {
		err = f.call("MountShare", url, opts, fd).Store(&point)
		return nil
	}); serr != nil {
		t.Fatal(serr)
	}
	return point, asDBusError(err)
}

func TestMountShareCIFS(t *testing.T) {
	e, f, auth := sharesFixture(t, true)
	uid := strconv.Itoa(os.Getuid())
	point, err := mountShareCall(t, f, "smb://nas:4455/media/films",
		map[string]dbus.Variant{"name": dbus.MakeVariant("Films"), "read_only": dbus.MakeVariant(true)},
		[]string{"bob", "HOME", "pa ss=wo,rd"})
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(e.media, "anna", "Films"); point != want {
		t.Fatalf("mount point %q, want %q", point, want)
	}
	if q := auth.questions(); len(q) != 1 || !strings.HasSuffix(q[0], " "+actionShares) {
		t.Fatalf("asked %q", q)
	}
	lines := e.mountLines(t)
	if len(lines) != 1 {
		t.Fatalf("mount ran %d times: %q", len(lines), lines)
	}
	want := "mount -t cifs -o nosuid,nodev,noexec,ro,port=4455,uid=" + uid + ",gid=1234,forceuid,forcegid,file_mode=0600,dir_mode=0700,credentials="
	if !strings.HasPrefix(lines[0], want) || !strings.HasSuffix(lines[0], " //nas/media/films "+point) {
		t.Fatalf("mount line\n%s\nwant\n%s...", lines[0], want)
	}
	log := e.log(t)
	for _, l := range []string{"cred-mode 600", "cred username=bob", "cred password=pa ss=wo,rd", "cred domain=HOME"} {
		if !strings.Contains(log, l+"\n") {
			t.Errorf("log has no %q:\n%s", l, log)
		}
	}
	for _, l := range strings.Split(log, "\n") {
		if strings.HasPrefix(l, "env ") && !strings.HasPrefix(l, "env PATH=") && !strings.HasPrefix(l, "env PWD=") && !strings.HasPrefix(l, "env SHLVL=") {
			t.Errorf("mount got the environment variable %q", l)
		}
	}
	if strings.Contains(lines[0], "pa ss") {
		t.Error("the password is on the command line")
	}
	left, _ := os.ReadDir(e.creds)
	if len(left) != 0 {
		t.Errorf("the credentials file is left: %v", left)
	}
	if fi, _ := os.Stat(e.creds); fi.Mode().Perm() != 0o700 {
		t.Errorf("the credentials folder is %v", fi.Mode())
	}
	if fi, _ := os.Stat(filepath.Join(e.media, "anna")); fi.Mode().Perm() != 0o750 {
		t.Errorf("/run/media/anna is %v", fi.Mode())
	}
	if len(e.chowns) != 1 || e.chowns[0] != filepath.Join(e.media, "anna")+" 0:1234" {
		t.Errorf("chowns %q", e.chowns)
	}

	// The same share again (another port, another case): the same folder,
	// no second mount.
	again, err := mountShareCall(t, f, "smb://NAS/media/films/", nil, []string{"bob", "HOME", "x"})
	if err != nil || again != point || len(e.mountLines(t)) != 1 {
		t.Fatalf("second mount: %q %v, %d mount calls", again, err, len(e.mountLines(t)))
	}

	var list []map[string]dbus.Variant
	if err := f.call("MountedShares").Store(&list); err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0]["mountpoint"].Value() != point || list[0]["fstype"].Value() != "cifs" || list[0]["source"].Value() != "//nas/media/films" {
		t.Fatalf("MountedShares = %v", list)
	}

	if err := f.call("UnmountShare", point).Err; err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(e.log(t), "umount "+point+"\n") {
		t.Error("umount did not run")
	}
	if _, err := os.Stat(point); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("the folder is left: %v", err)
	}
	if err := f.call("MountedShares").Store(&list); err != nil || len(list) != 0 {
		t.Fatalf("MountedShares after the unmount = %v %v", list, err)
	}
}

func TestMountShareGuestAndTakenName(t *testing.T) {
	e, f, _ := sharesFixture(t, true)
	os.MkdirAll(filepath.Join(e.media, "anna", "public"), 0o700)
	point, err := mountShareCall(t, f, "smb://nas/public", nil, []string{"", "", ""})
	if err != nil {
		t.Fatal(err)
	}
	if point != filepath.Join(e.media, "anna", "public-2") {
		t.Fatalf("a taken name gets a number: %q", point)
	}
	l := e.mountLines(t)[0]
	if !strings.Contains(l, ",guest,user=guest ") || strings.Contains(l, "credentials=") || !strings.Contains(l, ",rw,") {
		t.Fatalf("guest mount: %s", l)
	}
}

func TestMountShareNFS(t *testing.T) {
	e, f, _ := sharesFixture(t, true)
	e.touch(t, "no-4.2", "")
	point, err := mountShareCall(t, f, "nfs://srv/export/data", nil, []string{"", "", ""})
	if err != nil {
		t.Fatal(err)
	}
	if point != filepath.Join(e.media, "anna", "data") {
		t.Fatalf("mount point %q", point)
	}
	lines := e.mountLines(t)
	want := []string{
		"mount -t nfs4 -o vers=4.2,nosuid,nodev,noexec,rw,retry=0 srv:/export/data " + point,
		"mount -t nfs4 -o vers=4.1,nosuid,nodev,noexec,rw,retry=0 srv:/export/data " + point,
	}
	if strings.Join(lines, "\n") != strings.Join(want, "\n") {
		t.Fatalf("mount lines\n%s\nwant\n%s", strings.Join(lines, "\n"), strings.Join(want, "\n"))
	}
	_, derr := mountShareCall(t, f, "nfs://srv/other", nil, []string{"bob", "", "pw"})
	wantBusErr(t, "NFS with a password", derr, "Refused", "no user name")
}

func TestMountShareFailureRemovesTheFolder(t *testing.T) {
	e, f, _ := sharesFixture(t, true)
	e.touch(t, "fail", "mount error(13): Permission denied")
	_, err := mountShareCall(t, f, "smb://nas/media", nil, []string{"bob", "", "wrong"})
	wantBusErr(t, "a failed mount", err, "Failed", "Permission denied")
	if _, err := os.Stat(filepath.Join(e.media, "anna", "media")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("the folder is left: %v", err)
	}
	if left, _ := os.ReadDir(e.creds); len(left) != 0 {
		t.Errorf("the credentials file is left: %v", left)
	}
}

func TestMountShareRefusals(t *testing.T) {
	e, f, _ := sharesFixture(t, true)
	cases := []struct {
		what, url string
		opts      map[string]dbus.Variant
		lines     []string
		msg       string
	}{
		{"a bad address", "smb://nas/a,uid=0", nil, []string{"", "", ""}, "comma"},
		{"a user in the address", "smb://bob@nas/a", nil, []string{"", "", ""}, "User field"},
		{"an unknown option", "smb://nas/a", map[string]dbus.Variant{"exec": dbus.MakeVariant(true)}, []string{"", "", ""}, "unknown option"},
		{"a name with a slash", "smb://nas/a", map[string]dbus.Variant{"name": dbus.MakeVariant("etc/x")}, []string{"", "", ""}, "slash"},
		{"a user with a comma", "smb://nas/a", nil, []string{"bob,uid=0", "", ""}, "comma"},
		{"a password without a user", "smb://nas/a", nil, []string{"", "", "pw"}, "needs a user"},
		{"two lines", "smb://nas/a", nil, []string{"bob", "pw"}, "three lines"},
	}
	for _, c := range cases {
		_, err := mountShareCall(t, f, c.url, c.opts, c.lines)
		wantBusErr(t, c.what, err, "Refused", c.msg)
	}
	if n := len(e.mountLines(t)); n != 0 {
		t.Fatalf("mount ran %d times", n)
	}
	wantBusErr(t, "another user's folder", asDBusError(f.call("UnmountShare", filepath.Join(e.media, "bob", "x")).Err), "Refused", "")
	wantBusErr(t, "a path that climbs", asDBusError(f.call("UnmountShare", e.media+"/anna/../anna/x").Err), "Refused", "")
	wantBusErr(t, "a folder that is no share", asDBusError(f.call("UnmountShare", filepath.Join(e.media, "anna", "usb")).Err), "Refused", "")
	if strings.Contains(e.log(t), "umount") {
		t.Fatal("umount ran")
	}
}

func TestMountShareRefusesAUserFolderThatIsALink(t *testing.T) {
	e, f, _ := sharesFixture(t, true)
	os.MkdirAll(e.media, 0o755)
	os.Symlink(t.TempDir(), filepath.Join(e.media, "anna"))
	_, err := mountShareCall(t, f, "smb://nas/media", nil, []string{"", "", ""})
	wantBusErr(t, "a linked /run/media/anna", err, "Failed", "only root can change")
	if n := len(e.mountLines(t)); n != 0 {
		t.Fatal("mount ran")
	}
}

func TestMountShareRefusesAFolderNotOwnedByRoot(t *testing.T) {
	e, f, _ := sharesFixture(t, true)
	sharesRootOwned = func(fs.FileInfo) bool { return false }
	_, err := mountShareCall(t, f, "smb://nas/media", nil, []string{"", "", ""})
	wantBusErr(t, "a user-owned /run/media", err, "Failed", "only root can change")
	if n := len(e.mountLines(t)); n != 0 {
		t.Fatal("mount ran")
	}
}

func TestShareMethodsAskTheSharesAction(t *testing.T) {
	e, f, auth := sharesFixture(t, false)
	_, err := mountShareCall(t, f, "smb://nas/media", nil, []string{"bob", "", "pw"})
	wantBusErr(t, "MountShare", err, "NotAuthorized", "")
	wantBusErr(t, "UnmountShare", asDBusError(f.call("UnmountShare", filepath.Join(e.media, "anna", "media")).Err), "NotAuthorized", "")
	q := auth.questions()
	if len(q) != 2 {
		t.Fatalf("asked %q", q)
	}
	for _, x := range q {
		if !strings.HasSuffix(x, " "+actionShares) {
			t.Fatalf("asked %q", x)
		}
	}
	if e.log(t) != "" {
		t.Fatalf("ran %s", e.log(t))
	}
	// MountedShares needs no polkit answer.
	var list []map[string]dbus.Variant
	if err := f.call("MountedShares").Store(&list); err != nil || len(list) != 0 {
		t.Fatalf("MountedShares = %v %v", list, err)
	}
	if len(auth.questions()) != 2 {
		t.Fatal("MountedShares asked polkit")
	}
}

// The tray's mounter speaks MountShare as the daemon reads it.
func TestBusMounter(t *testing.T) {
	e, f, _ := sharesFixture(t, true)
	m := busMounter{f.client}
	point, err := m.MountShare("smb://nas/media", shares.Options{Name: "Media", ReadOnly: true}, shares.Credentials{User: "bob", Password: "pw"})
	if err != nil || point != filepath.Join(e.media, "anna", "Media") {
		t.Fatalf("MountShare = %q %v", point, err)
	}
	if !strings.Contains(e.log(t), "cred password=pw\n") || !strings.Contains(e.mountLines(t)[0], ",ro,") {
		t.Fatalf("log:\n%s", e.log(t))
	}
	_, err = m.MountShare("smb://nas/a,b", shares.Options{}, shares.Credentials{})
	if daemonErrName(err) != "Refused" {
		t.Fatalf("a bad address: %v", err)
	}
}

// denyAuth says no to the actions in deny and yes to the others.
type denyAuth struct {
	mu    sync.Mutex
	deny  map[string]bool
	asked []string
}

func (a *denyAuth) Allowed(sender dbus.Sender, action string) (bool, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.asked = append(a.asked, action)
	return !a.deny[action], nil
}

// NFS needs the administrators' action too; SMB does not.
func TestMountShareNFSNeedsTheNFSAction(t *testing.T) {
	auth := &denyAuth{deny: map[string]bool{actionSharesNFS: true}}
	e, f := sharesFixtureAuth(t, auth)
	_, err := mountShareCall(t, f, "nfs://srv/export", nil, []string{"", "", ""})
	wantBusErr(t, "NFS for a non-admin", err, "NotAuthorized", "only an administrator can mount an NFS share")
	_, err = mountShareCall(t, f, "NFS://srv/export", nil, []string{"", "", ""})
	wantBusErr(t, "NFS in capitals", err, "NotAuthorized", "NFS")
	if n := len(e.mountLines(t)); n != 0 {
		t.Fatalf("mount ran %d times", n)
	}
	if _, err := mountShareCall(t, f, "smb://nas/media", nil, []string{"", "", ""}); err != nil {
		t.Fatalf("SMB for a non-admin: %v", err)
	}
	auth.mu.Lock()
	asked := strings.Join(auth.asked, ",")
	auth.mu.Unlock()
	want := strings.Join([]string{actionShares, actionSharesNFS, actionShares, actionSharesNFS, actionShares}, ",")
	if asked != want {
		t.Fatalf("asked %s, want %s", asked, want)
	}
}

func TestMountShareCapPerUser(t *testing.T) {
	e, f, _ := sharesFixture(t, true)
	dir := filepath.Join(e.media, "anna")
	for i := 1; i <= maxSharesPerUser; i++ {
		e.addMount(t, filepath.Join(dir, fmt.Sprintf("s%d", i)), "cifs", fmt.Sprintf("//nas/s%d", i))
	}
	// Another user's shares do not count.
	e.addMount(t, filepath.Join(e.media, "bob", "x"), "cifs", "//nas/x")
	_, err := mountShareCall(t, f, "smb://nas/one-more", nil, []string{"", "", ""})
	wantBusErr(t, "one share too many", err, "Refused", "16 network shares mounted")
	// A share that is already mounted is still answered.
	point, err := mountShareCall(t, f, "smb://nas/s3", nil, []string{"", "", ""})
	if err != nil || point != filepath.Join(dir, "s3") {
		t.Fatalf("an existing share: %q %v", point, err)
	}
	if n := len(e.mountLines(t)); n != 0 {
		t.Fatalf("mount ran %d times", n)
	}
	// After an unmount there is room again.
	if err := f.call("UnmountShare", filepath.Join(dir, "s1")).Err; err != nil {
		t.Fatal(err)
	}
	if _, err := mountShareCall(t, f, "smb://nas/one-more", nil, []string{"", "", ""}); err != nil {
		t.Fatal(err)
	}
}

// One mount at a time per user, and no lock across the mount call: while
// anna's slow server holds her mount, her second mount is refused at once,
// her unmount goes through, and another user's mount slot is free.
func TestMountShareOneAtATimePerUser(t *testing.T) {
	e, f, _ := sharesFixture(t, true)
	old := filepath.Join(e.media, "anna", "old")
	e.addMount(t, old, "cifs", "//nas/old")
	e.touch(t, "block", "")
	first := make(chan *dbus.Error, 1)
	go func() {
		_, err := mountShareCall(t, f, "smb://slow/media", nil, []string{"", "", ""})
		first <- err
	}()
	deadline := time.Now().Add(10 * time.Second)
	for !strings.Contains(e.log(t), "blocked") {
		if time.Now().After(deadline) {
			t.Fatal("the first mount never started")
		}
		time.Sleep(10 * time.Millisecond)
	}
	start := time.Now()
	_, err := mountShareCall(t, f, "smb://nas/other", nil, []string{"", "", ""})
	wantBusErr(t, "a second mount", err, "Refused", "another network share is being mounted")
	if err := f.call("UnmountShare", old).Err; err != nil {
		t.Fatalf("an unmount during a mount: %v", err)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatal("the calls waited for the slow mount")
	}
	end, berr := beginShareMount("bob")
	if berr != nil {
		t.Fatalf("bob's slot: %v", berr)
	}
	end()
	e.touch(t, "release", "")
	if err := <-first; err != nil {
		t.Fatalf("the slow mount: %v", err)
	}
	// The slot is free again.
	os.Remove(filepath.Join(e.bin, "block"))
	if _, err := mountShareCall(t, f, "smb://nas/other", nil, []string{"", "", ""}); err != nil {
		t.Fatal(err)
	}
}

// At logout every share of the user goes, lazily, NFS forced too; another
// user's stay. No polkit is asked.
func TestUnmountSharesAtLogout(t *testing.T) {
	e, _, auth := sharesFixture(t, false)
	smb := filepath.Join(e.media, "anna", "media")
	nfs := filepath.Join(e.media, "anna", "data")
	bob := filepath.Join(e.media, "bob", "x")
	e.addMount(t, smb, "cifs", "//nas/media")
	e.addMount(t, nfs, "nfs4", "srv:/data")
	e.addMount(t, bob, "cifs", "//nas/x")
	e.addMount(t, filepath.Join(e.media, "anna", "USB"), "exfat", "/dev/sdb1")
	var stderr bytes.Buffer
	if rc := cmdUnmountShares(strconv.Itoa(os.Getuid()), &stderr); rc != exitOK {
		t.Fatalf("rc %d: %s", rc, stderr.String())
	}
	log := e.log(t)
	for _, l := range []string{"umount -l " + smb + "\n", "umount -f -l " + nfs + "\n"} {
		if !strings.Contains(log, l) {
			t.Errorf("log has no %q:\n%s", l, log)
		}
	}
	if strings.Contains(log, bob) || strings.Contains(log, "USB") {
		t.Errorf("unmounted something that is not anna's share:\n%s", log)
	}
	for _, p := range []string{smb, nfs} {
		if _, err := os.Stat(p); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("%s is left: %v", p, err)
		}
	}
	if len(auth.questions()) != 0 {
		t.Error("the logout asked polkit")
	}
	if rc := cmdUnmountShares("anna", &stderr); rc != exitUsage {
		t.Errorf("a name for a uid: rc %d", rc)
	}
}

// The daemon takes the logout unit's flag (the unit itself, bound to
// user@UID.service, is checked by test/shares/check-shares.sh).
func TestDaemonTakesUnmountShares(t *testing.T) {
	var stderr bytes.Buffer
	if rc := cmdDaemon([]string{"--unmount-shares", "1000"}, &stderr); rc == exitUsage {
		t.Fatalf("daemon refuses --unmount-shares: %s", stderr.String())
	}
}
