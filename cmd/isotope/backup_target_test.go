package main

import (
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/godbus/dbus/v5"
)

// fakeByUUID points byUUIDDir at a temporary /dev/disk/by-uuid with one
// link per uuid, to the device given.
func fakeByUUID(t *testing.T, links map[string]string) {
	t.Helper()
	dir := t.TempDir()
	for uuid, dev := range links {
		if err := os.Symlink(dev, filepath.Join(dir, uuid)); err != nil {
			t.Fatal(err)
		}
	}
	old := byUUIDDir
	byUUIDDir = dir
	t.Cleanup(func() { byUUIDDir = old })
}

// fakeMounter replaces mountByUUID: it "mounts" every disk at dir and counts
// the mounts and the unmounts.
type fakeMounter struct {
	dir              string
	mounts, unmounts int
}

func fakeMount(t *testing.T) *fakeMounter {
	t.Helper()
	m := &fakeMounter{dir: t.TempDir()}
	old := mountByUUID
	mountByUUID = func(uuid string) (string, func(), error) {
		m.mounts++
		return m.dir, func() { m.unmounts++ }, nil
	}
	t.Cleanup(func() { mountByUUID = old })
	return m
}

func writeMountinfo(t *testing.T, s string) {
	t.Helper()
	mountinfoPath = filepath.Join(t.TempDir(), "mountinfo")
	write(t, mountinfoPath, s)
}

const usbMountinfo = `22 1 0:20 / / rw - overlay overlay rw
30 22 253:1 / /usr/local rw - ext4 /dev/mapper/persistent rw
33 22 8:17 / /run/media/anna/USB rw,nosuid - vfat /dev/sdb1 rw
`

func TestNormalizeTargetAcceptsUUID(t *testing.T) {
	for in, want := range map[string]string{
		"uuid:1234-ABCD/isotope-backup": "uuid:1234-ABCD/isotope-backup",
		"uuid:1234-ABCD/a/b/":           "uuid:1234-ABCD/a/b",
		"uuid:1234-ABCD//a":             "uuid:1234-ABCD/a",
		"uuid:":                         "",
		"uuid:1234-ABCD":                "",
		"uuid:1234-ABCD/":               "",
		"uuid:x/../y":                   "",
		"uuid:x/a/..":                   "",
		"uuid:../x":                     "",
		"uuid:ab$c/x":                   "",
		"rest:https://nas/x\ntarget=/x": "",
	} {
		got, err := normalizeTarget(in)
		if (want == "") != (err != nil) || got != want {
			t.Errorf("%q: %q, %v; want %q", in, got, err, want)
		}
	}
}

func TestResolveTargetFindsAMountedUUID(t *testing.T) {
	backupSetup(t)
	usb := filepath.Join(t.TempDir(), "USB")
	os.Mkdir(usb, 0o755)
	writeMountinfo(t, strings.ReplaceAll(usbMountinfo, "/run/media/anna/USB", usb))
	fakeByUUID(t, map[string]string{"1234-ABCD": "/dev/sdb1"})
	m := fakeMount(t)
	repo, release, err := resolveTarget("uuid:1234-ABCD/isotope-backup")
	if err != nil {
		t.Fatal(err)
	}
	release()
	if repo != usb+"/isotope-backup" {
		t.Fatalf("repo %q", repo)
	}
	if m.mounts != 0 || m.unmounts != 0 {
		t.Fatalf("mounted %d, unmounted %d: the disk was already mounted", m.mounts, m.unmounts)
	}
	// Not a uuid: target: as it is.
	if repo, release, err := resolveTarget("/mnt/backup"); err != nil || repo != "/mnt/backup" {
		t.Fatalf("dir: %q, %v", repo, err)
	} else {
		release()
	}
}

func TestUUIDTargetUnmountsWhatItMounted(t *testing.T) {
	p := backupSetup(t)
	writeMountinfo(t, usbMountinfo) // another disk; 5678-EF01 is not mounted
	fakeByUUID(t, map[string]string{"5678-EF01": "/dev/sdc1"})
	m := fakeMount(t)
	repo, release, err := resolveTarget("uuid:5678-EF01/isotope-backup")
	if err != nil {
		t.Fatal(err)
	}
	if repo != filepath.Join(m.dir, "isotope-backup") || m.mounts != 1 {
		t.Fatalf("repo %q, mounts %d", repo, m.mounts)
	}
	release()
	release()
	if m.unmounts != 1 {
		t.Fatalf("unmounted %d times, want 1", m.unmounts)
	}

	// The scheduled run: backs up into the mounted disk, then unmounts it.
	log := fakeRestic(t)
	os.MkdirAll(filepath.Join(m.dir, "isotope-backup"), 0o700)
	write(t, filepath.Join(m.dir, "isotope-backup", "config"), "restic")
	saveBackupConfig(p, backupConfig{Target: "uuid:5678-EF01/isotope-backup", Label: "USB", Schedule: "daily"})
	code, out, errOut := runCLI("", "backup", "--scheduled")
	if code != exitOK || !strings.Contains(out, "Backed up.") {
		t.Fatalf("exit %d, out %q, err %q", code, out, errOut)
	}
	b, _ := os.ReadFile(log)
	if !strings.HasPrefix(string(b), filepath.Join(m.dir, "isotope-backup")+" | backup") {
		t.Fatalf("restic: %s", b)
	}
	if m.mounts != 2 || m.unmounts != 2 {
		t.Fatalf("mounts %d, unmounts %d", m.mounts, m.unmounts)
	}
}

// TestUUIDTargetNotPresentIsSkipped is Review Focus 4: a USB disk that is
// not plugged in is skipped, and nothing writes to a bare mount point.
func TestUUIDTargetNotPresentIsSkipped(t *testing.T) {
	p := backupSetup(t)
	log := fakeRestic(t)
	fakeByUUID(t, nil)
	m := fakeMount(t)
	// A stale mount point of the disk is still there, on the internal disk.
	stale := filepath.Join(t.TempDir(), "run-media-anna-USB")
	os.MkdirAll(filepath.Join(stale, "isotope-backup"), 0o755)
	write(t, filepath.Join(stale, "isotope-backup", "config"), "restic")
	writeMountinfo(t, usbMountinfo)
	saveBackupConfig(p, backupConfig{Target: "uuid:1234-ABCD/isotope-backup", Label: "My USB", Schedule: "daily"})

	code, out, errOut := runCLI("", "backup", "--scheduled")
	if code != exitOK || !strings.Contains(out, "My USB is not plugged in") {
		t.Fatalf("exit %d, out %q, err %q", code, out, errOut)
	}
	if b, _ := os.ReadFile(log); len(b) != 0 {
		t.Fatalf("restic ran: %s", b)
	}
	if m.mounts != 0 {
		t.Fatal("mountByUUID was called for a disk that is not there")
	}
	// By hand it is an error that says so.
	if code, _, errOut := runCLI("", "backup", "now"); code != exitError || !strings.Contains(errOut, "My USB is not plugged in") {
		t.Fatalf("by hand: exit %d, err %q", code, errOut)
	}
	d, err := backupStatusDict(p)
	if err != nil || d["present"].Value() != false {
		t.Fatalf("status %v, %v", d, err)
	}
}

// failingRestic makes restic exit 1 after it logs its call.
func failingRestic(t *testing.T) (log string) {
	t.Helper()
	dir := t.TempDir()
	log = filepath.Join(dir, "log")
	bin := filepath.Join(dir, "restic")
	write(t, bin, "#!/bin/sh\necho \"$RESTIC_REPOSITORY | $*\" >> "+log+"\necho 'Fatal: no' >&2\nexit 1\n")
	os.Chmod(bin, 0o755)
	t.Setenv("ISOTOPE_RESTIC", bin)
	return log
}

// envRestic logs the call and the REST user it was given.
func envRestic(t *testing.T) (log string) {
	t.Helper()
	dir := t.TempDir()
	log = filepath.Join(dir, "log")
	bin := filepath.Join(dir, "restic")
	write(t, bin, "#!/bin/sh\necho \"$RESTIC_REPOSITORY | $* | user=$RESTIC_REST_USERNAME\" >> "+log+"\n")
	os.Chmod(bin, 0o755)
	t.Setenv("ISOTOPE_RESTIC", bin)
	return log
}

func TestInitTargetWritesEnvOnlyAfterRestic(t *testing.T) {
	p := backupSetup(t)
	failingRestic(t)
	target := "rest:https://nas:8000/isotope"
	env := []string{"RESTIC_REST_USERNAME=anna", "RESTIC_REST_PASSWORD=s3cret"}
	err := initTarget(p, target, initOptions{Label: "NAS"}, "the-password", env)
	if err == nil {
		t.Fatal("init succeeded with a failing restic")
	}
	for _, f := range []string{p.envFile(), p.password(), filepath.Join(p.Run, "env.new"), p.password() + ".new"} {
		if exists(f) {
			t.Fatalf("%s is there after a failed init", f)
		}
	}
	if c, _ := loadBackupConfig(p); c.Target != "" {
		t.Fatalf("config %+v", c)
	}

	log := envRestic(t)
	if err := initTarget(p, target, initOptions{Label: "NAS", Containers: true}, "the-password", env); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(log); string(b) != target+" | init | user=anna\n" {
		t.Fatalf("restic: %q", b)
	}
	if b, _ := os.ReadFile(p.envFile()); string(b) != strings.Join(env, "\n")+"\n" {
		t.Fatalf("env %q", b)
	}
	if b, _ := os.ReadFile(p.password()); string(b) != "the-password\n" {
		t.Fatalf("password %q", b)
	}
	for _, f := range []string{p.envFile(), p.password()} {
		if info, err := os.Stat(f); err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("%s: %v %v", f, info.Mode(), err)
		}
	}
	if exists(filepath.Join(p.Run, "env.new")) {
		t.Fatal("env.new is left behind")
	}
	c, _ := loadBackupConfig(p)
	if c.Target != target || c.Label != "NAS" || !c.Containers {
		t.Fatalf("config %+v", c)
	}
	// The label survives a load.
	if b, _ := os.ReadFile(p.config()); !strings.Contains(string(b), "label=NAS\n") {
		t.Fatalf("config file %q", b)
	}
}

func TestInitTargetRefusesUnknownEnv(t *testing.T) {
	p := backupSetup(t)
	log := fakeRestic(t)
	for _, env := range [][]string{
		{"PATH=/tmp"},
		{"RESTIC_REST_USERNAME=anna", "LD_PRELOAD=/tmp/x.so"},
		{"not-a-pair-but-a-secret"},
	} {
		err := initTarget(p, "rest:https://nas:8000/isotope", initOptions{}, "pw", env)
		if err == nil {
			t.Fatalf("%q was accepted", env)
		}
		if strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "/tmp") {
			t.Fatalf("the error shows a value: %v", err)
		}
	}
	for _, f := range []string{p.envFile(), p.password(), filepath.Join(p.Run, "env.new"), p.config()} {
		if exists(f) {
			t.Fatalf("%s was written", f)
		}
	}
	if b, _ := os.ReadFile(log); len(b) != 0 {
		t.Fatalf("restic ran: %s", b)
	}
}

// fakeSSH replaces ssh-keygen and ssh-keyscan; the scan answers line.
func fakeSSH(t *testing.T, line string) (calls *[]string) {
	t.Helper()
	var got []string
	old := sysCommand
	sysCommand = func(name string, args ...string) *exec.Cmd {
		switch name {
		case "ssh-keyscan":
			got = append(got, name+" "+strings.Join(args, " "))
			return exec.Command("printf", "%s\n", "# a comment", line)
		case "ssh-keygen":
			got = append(got, name+" "+strings.Join(args, " "))
			key := args[len(args)-1]
			return exec.Command("sh", "-c", "echo private > "+key+" && echo 'ssh-ed25519 AAAAPUB isotope-backup@x' > "+key+".pub")
		}
		return exec.Command(name, args...)
	}
	t.Cleanup(func() { sysCommand = old })
	return &got
}

func hostKeyLine(host string) (line, fp string) {
	blob := []byte("\x00\x00\x00\x0bssh-ed25519\x00\x00\x00\x20" + strings.Repeat("k", 32))
	sum := sha256.Sum256(blob)
	return host + " ssh-ed25519 " + base64.StdEncoding.EncodeToString(blob),
		"SHA256:" + base64.RawStdEncoding.EncodeToString(sum[:])
}

func TestSFTPNeedsAConfirmedHostKey(t *testing.T) {
	p := backupSetup(t)
	log := fakeRestic(t)
	line, fp := hostKeyLine("[nas]:2222")
	calls := fakeSSH(t, line)

	pub, err := ensureSSHKey(p)
	if err != nil || pub != "ssh-ed25519 AAAAPUB isotope-backup@x" {
		t.Fatalf("pub %q, %v", pub, err)
	}
	if pub2, _ := ensureSSHKey(p); pub2 != pub || len(*calls) != 1 {
		t.Fatalf("the key was made again: %q", *calls)
	}
	if !strings.Contains((*calls)[0], "-t ed25519 -N  -C isotope-backup@") {
		t.Fatalf("ssh-keygen %q", (*calls)[0])
	}
	fps, err := scanHostKey(p, "nas", 2222)
	if err != nil || len(fps) != 1 || fps[0] != "ED25519 "+fp {
		t.Fatalf("fingerprints %q, %v; want %s", fps, err, fp)
	}
	if !strings.Contains((*calls)[1], "-p 2222") {
		t.Fatalf("ssh-keyscan %q", (*calls)[1])
	}
	if _, err := scanHostKey(p, "-oProxyCommand=x", 22); err == nil {
		t.Fatal("a host that is an option was accepted")
	}

	target := "sftp://backup@nas:2222//srv/isotope"
	kh := filepath.Join(sshDir(p), "known_hosts")
	if err := initTarget(p, target, initOptions{}, "pw", nil); err == nil {
		t.Fatal("init without a host key was accepted")
	}
	wrong := "SHA256:" + strings.Repeat("A", 43)
	if err := initTarget(p, target, initOptions{HostKey: wrong}, "pw", nil); err == nil {
		t.Fatal("init with the wrong host key was accepted")
	}
	if exists(kh) || exists(p.password()) {
		t.Fatal("a refused init wrote known_hosts or the password")
	}
	if b, _ := os.ReadFile(log); len(b) != 0 {
		t.Fatalf("restic ran: %s", b)
	}
	if err := initTarget(p, target, initOptions{HostKey: fp, Port: 2222}, "pw", nil); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(kh); string(b) != line+"\n" {
		t.Fatalf("known_hosts %q", b)
	}
	if b, _ := os.ReadFile(log); !strings.Contains(string(b), "StrictHostKeyChecking=yes") {
		t.Fatalf("restic: %s", b)
	}
	// The same key again is not added twice.
	if err := acceptHostKey(p, "nas", 2222, fp); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(kh); strings.Count(string(b), "\n") != 1 {
		t.Fatalf("known_hosts %q", b)
	}
}

func TestResticCmdAddsSFTPArgs(t *testing.T) {
	p := backupSetup(t)
	os.MkdirAll(sshDir(p), 0o700)
	write(t, filepath.Join(sshDir(p), "id_ed25519"), "private")
	line, _ := hostKeyLine("nas")
	write(t, filepath.Join(sshDir(p), "known_hosts"), line+"\n")
	c, err := resticCmd(p, "sftp:anna@nas:/srv/isotope", "/pw", "snapshots")
	if err != nil {
		t.Fatal(err)
	}
	args := strings.Join(c.Args[1:], "\x00")
	want := "-o\x00sftp.args=-i " + filepath.Join(sshDir(p), "id_ed25519") +
		" -o UserKnownHostsFile=" + filepath.Join(sshDir(p), "known_hosts") +
		" -o StrictHostKeyChecking=yes -o BatchMode=yes\x00snapshots"
	if args != want {
		t.Fatalf("args %q\nwant %q", args, want)
	}
	c, _ = resticCmd(p, "/mnt/backup", "/pw", "snapshots")
	if len(c.Args) != 2 {
		t.Fatalf("a directory target got %q", c.Args)
	}
}

func TestBusBackupInitReadsTheFd(t *testing.T) {
	p := backupSetup(t)
	log := fakeRestic(t)
	d := backupDaemon(t, nil)
	auth := &fakeAuth{allow: true}
	f := startService(t, d, auth)
	target := "rest:https://nas:8000/isotope"
	opts := map[string]dbus.Variant{"label": dbus.MakeVariant("NAS"), "containers": dbus.MakeVariant(true)}
	lines := []string{"aaaaaaaa-bbbbbbbb", "RESTIC_REST_USERNAME=anna", "RESTIC_REST_PASSWORD=s3cret"}
	if err := secretCall(t, f, "BackupInit", lines, target, opts); err != nil {
		t.Fatalf("BackupInit: %v", err)
	}
	if b, _ := os.ReadFile(p.password()); string(b) != "aaaaaaaa-bbbbbbbb\n" {
		t.Fatalf("password %q", b)
	}
	if b, _ := os.ReadFile(p.envFile()); string(b) != strings.Join(lines[1:], "\n")+"\n" {
		t.Fatalf("env %q", b)
	}
	if c, _ := loadBackupConfig(p); c.Target != target || c.Label != "NAS" || !c.Containers {
		t.Fatalf("config %+v", c)
	}
	if b, _ := os.ReadFile(log); strings.Contains(string(b), "s3cret") || strings.Contains(string(b), "aaaaaaaa") {
		t.Fatalf("a secret is in restic's argv: %s", b)
	}
	me := f.client.Names()[0]
	if q := auth.questions(); len(q) != 1 || q[0] != me+" "+actionBackup {
		t.Fatalf("asked %q", q)
	}
	// An unknown name in the fd, and an unknown option, are refused.
	if err := secretCall(t, f, "BackupInit", []string{"pw", "PATH=/tmp"}, target, opts); err == nil {
		t.Fatal("PATH was accepted")
	}
	if err := secretCall(t, f, "BackupInit", []string{"pw"}, target, map[string]dbus.Variant{"colour": dbus.MakeVariant("red")}); err == nil {
		t.Fatal("an unknown option was accepted")
	}
	if err := secretCall(t, f, "BackupInit", []string{"pw"}, "rest:https://anna:s3cret@nas:8000/isotope", opts); err == nil {
		t.Fatal("a password in the target was accepted")
	}

	// polkit says no: refused, nothing read, the fd closed.
	auth.mu.Lock()
	auth.allow = false
	auth.mu.Unlock()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	c := f.call("BackupInit", "/mnt/other", map[string]dbus.Variant{}, dbus.UnixFD(r.Fd()))
	r.Close()
	wantBusErr(t, "BackupInit", asDBusError(c.Err), "NotAuthorized", "")
	if _, err := w.Write([]byte("x\n")); !errors.Is(err, syscall.EPIPE) {
		t.Fatalf("the service kept the fd open (write: %v)", err)
	}
	for _, c := range []*dbus.Call{f.call("BackupSSHKey"), f.call("BackupHostKey", "nas", uint32(22))} {
		wantBusErr(t, "ssh", asDBusError(c.Err), "NotAuthorized", "")
	}
	if c, _ := loadBackupConfig(p); c.Target != target {
		t.Fatalf("a refused call changed the config: %+v", c)
	}
}

func TestBackupStatusSaysKindAndLabel(t *testing.T) {
	p := backupSetup(t)
	fakeByUUID(t, map[string]string{"1234-ABCD": "/dev/sdb1"})
	for target, kind := range map[string]string{
		"uuid:1234-ABCD/isotope-backup":       "disk",
		"/mnt/backup":                         "dir",
		"rest:https://nas:8000/isotope":       "rest",
		"s3:https://s3.example.com/b/isotope": "s3",
		"sftp://u@nas:2222//srv/isotope":      "sftp",
		"sftp:u@nas:/srv/isotope":             "sftp",
		"b2:bucket:path":                      "other",
		"":                                    "",
	} {
		saveBackupConfig(p, backupConfig{Target: target, Label: "My USB", Schedule: "off"})
		m, err := backupStatusDict(p)
		if err != nil {
			t.Fatal(err)
		}
		if m["kind"].Value() != kind || m["label"].Value() != "My USB" {
			t.Errorf("%q: kind %v label %v, want %q", target, m["kind"].Value(), m["label"].Value(), kind)
		}
	}
	saveBackupConfig(p, backupConfig{Target: "uuid:1234-ABCD/isotope-backup", Label: "My USB", Schedule: "off"})
	if m, _ := backupStatusDict(p); m["present"].Value() != true {
		t.Fatalf("a plugged-in disk is not present: %v", m)
	}
	if _, out, _ := runCLI("", "backup", "status"); !strings.Contains(out, "Target:     My USB (USB disk)\n") {
		t.Fatalf("status %q", out)
	}
}

// TestOldSFTPTargetKeepsRootsSSH: the backup's key exists (an SFTP setup
// that was not finished), but this target's host is not in the backup's
// known_hosts: restic uses root's own ssh setup, as before.
func TestOldSFTPTargetKeepsRootsSSH(t *testing.T) {
	p := backupSetup(t)
	os.MkdirAll(sshDir(p), 0o700)
	write(t, filepath.Join(sshDir(p), "id_ed25519"), "private")
	line, _ := hostKeyLine("[nas]:2222")
	write(t, filepath.Join(sshDir(p), "known_hosts"), line+"\n")
	saveBackupConfig(p, backupConfig{Target: "sftp:anna@oldnas:/srv/isotope", Schedule: "daily"})
	for target, want := range map[string]bool{
		"sftp:anna@oldnas:/srv/isotope":     false,
		"sftp:anna@nas:/srv/isotope":        false, // port 22, the key is for 2222
		"sftp://anna@nas:2222//srv/x":       true,
		"sftp://anna@oldnas:2222//srv/x":    false,
		"sftp://anna@[nas]:2222//srv/x":     true,
		"rest:https://nas:2222/srv/isotope": false,
	} {
		c, err := resticCmd(p, target, "/pw", "snapshots")
		if err != nil {
			t.Fatal(err)
		}
		if got := strings.Contains(strings.Join(c.Args, " "), "sftp.args="); got != want {
			t.Errorf("%s: sftp.args %v, want %v (%q)", target, got, want, c.Args)
		}
	}
}

// TestUUIDTargetRefusesALinkOffTheDisk: a crafted disk with the same UUID
// whose folder is a link to the internal disk is refused.
func TestUUIDTargetRefusesALinkOffTheDisk(t *testing.T) {
	p := backupSetup(t)
	log := fakeRestic(t)
	fakeByUUID(t, map[string]string{"5678-EF01": "/dev/sdc1"})
	m := fakeMount(t)
	inside := t.TempDir()
	os.Symlink(inside, filepath.Join(m.dir, "isotope-backup"))
	os.MkdirAll(filepath.Join(m.dir, "real"), 0o755)
	os.Symlink(inside, filepath.Join(m.dir, "real", "deeper"))
	for _, target := range []string{"uuid:5678-EF01/isotope-backup", "uuid:5678-EF01/isotope-backup/sub", "uuid:5678-EF01/real/deeper/x"} {
		if _, _, err := resolveTarget(target); err == nil {
			t.Errorf("%s: a link off the disk was accepted", target)
		}
		if err := initTarget(p, target, initOptions{}, "pw", nil); err == nil {
			t.Errorf("%s: init through a link off the disk was accepted", target)
		}
	}
	if m.mounts != m.unmounts {
		t.Fatalf("mounted %d, unmounted %d", m.mounts, m.unmounts)
	}
	if entries, _ := os.ReadDir(inside); len(entries) != 0 {
		t.Fatalf("init wrote off the disk: %v", entries)
	}
	if b, _ := os.ReadFile(log); len(b) != 0 {
		t.Fatalf("restic ran: %s", b)
	}
	// A link that stays on the disk is fine.
	os.Symlink(filepath.Join(m.dir, "real"), filepath.Join(m.dir, "alias"))
	if repo, release, err := resolveTarget("uuid:5678-EF01/alias/b"); err != nil || repo != filepath.Join(m.dir, "alias", "b") {
		t.Fatalf("a link on the disk: %q, %v", repo, err)
	} else {
		release()
	}
}

func TestBusBackupInitRefusesOtherSchemes(t *testing.T) {
	p := backupSetup(t)
	log := fakeRestic(t)
	d := backupDaemon(t, nil)
	f := startService(t, d, &fakeAuth{allow: true})
	for _, target := range []string{"rclone:remote:path --config=/tmp/x", "b2:bucket:path", "azure:c:/p", "gs:b:/p", "swift:c:/p", "local:relative"} {
		if err := secretCall(t, f, "BackupInit", []string{"pw"}, target, map[string]dbus.Variant{}); err == nil {
			t.Errorf("%s was accepted", target)
		}
	}
	if b, _ := os.ReadFile(log); len(b) != 0 {
		t.Fatalf("restic ran: %s", b)
	}
	if exists(p.config()) {
		t.Fatal("the config was written")
	}
	// The CLI still takes them, as before.
	if got, err := normalizeTarget("b2:bucket:path"); err != nil || got != "b2:bucket:path" {
		t.Fatalf("CLI: %q, %v", got, err)
	}
}

func TestCheckEnvNamesOnlyAName(t *testing.T) {
	err := checkEnv([]string{"c2VjcmV0LXRoYXQtaGFz=YmFzZTY0"})
	if err == nil || strings.Contains(err.Error(), "c2Vj") {
		t.Fatalf("%v", err)
	}
	err = checkEnv([]string{"aws_secret=x"})
	if err == nil || strings.Contains(err.Error(), "aws_secret") {
		t.Fatalf("%v", err)
	}
	if err = checkEnv([]string{"PATH=/tmp"}); err == nil || !strings.Contains(err.Error(), "PATH is not") {
		t.Fatalf("%v", err)
	}
}

// keyLine is a known_hosts line of the key type typ, and its fingerprint.
func keyLine(host, typ, body string) (line, fp string) {
	blob := []byte(typ + body)
	sum := sha256.Sum256(blob)
	return host + " " + typ + " " + base64.StdEncoding.EncodeToString(blob),
		"SHA256:" + base64.RawStdEncoding.EncodeToString(sum[:])
}

func TestScanHostKeyNamesTheTypesED25519First(t *testing.T) {
	p := backupSetup(t)
	rsa, rsaFP := keyLine("nas", "ssh-rsa", "r")
	ecdsa, ecdsaFP := keyLine("nas", "ecdsa-sha2-nistp256", "e")
	ed, edFP := keyLine("nas", "ssh-ed25519", "d")
	sk, skFP := keyLine("nas", "sk-ssh-ed25519@openssh.com", "s")
	old := sysCommand
	sysCommand = func(name string, args ...string) *exec.Cmd {
		return exec.Command("printf", "%s\n", "# nas:22 SSH-2.0-OpenSSH", rsa, ecdsa, sk, ed)
	}
	t.Cleanup(func() { sysCommand = old })
	fps, err := scanHostKey(p, "nas", 22)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"ED25519 " + edFP, "ECDSA " + ecdsaFP, "RSA " + rsaFP, "ED25519-SK " + skFP}
	if strings.Join(fps, "\n") != strings.Join(want, "\n") {
		t.Fatalf("got %q\nwant %q", fps, want)
	}
	// host_key is the bare fingerprint; a whole "TYPE SHA256:..." line is
	// taken too. Only the chosen key goes to known_hosts.
	if err := acceptHostKey(p, "nas", 22, ecdsaFP); err != nil {
		t.Fatal(err)
	}
	if err := acceptHostKey(p, "nas", 22, "RSA "+rsaFP); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(sshDir(p), "known_hosts"))
	if string(b) != ecdsa+"\n"+rsa+"\n" {
		t.Fatalf("known_hosts %q", b)
	}
}

func TestCLIInitListsTheHostKeysWithTheirType(t *testing.T) {
	p := backupSetup(t)
	line, fp := hostKeyLine("nas")
	fakeSSH(t, line)
	var out strings.Builder
	if _, err := cliHostKey(p, "nas", 22, "", &out); err == nil || !strings.Contains(err.Error(), "--host-key SHA256:") {
		t.Fatalf("err %v", err)
	}
	if !strings.Contains(out.String(), "    ED25519 "+fp+"\n") || !strings.Contains(out.String(), "/etc/ssh/ssh_host_*_key.pub") {
		t.Fatalf("out %q", out.String())
	}
}

// TestSFTPInitKeepsKnownHostsUntilResticAccepts: restic init sees the new
// host key through a staged copy; the backup's known_hosts gets it only
// when init works. A failed init leaves known_hosts as it was, so an old
// target on the same host and port does not start to use the backup's key.
func TestSFTPInitKeepsKnownHostsUntilResticAccepts(t *testing.T) {
	p := backupSetup(t)
	line, fp := hostKeyLine("[nas]:2222")
	fakeSSH(t, line)
	if _, err := scanHostKey(p, "nas", 2222); err != nil {
		t.Fatal(err)
	}
	other, _ := hostKeyLine("oldnas")
	kh := filepath.Join(sshDir(p), "known_hosts")
	os.MkdirAll(sshDir(p), 0o700)
	write(t, kh, other+"\n")

	dir := t.TempDir()
	log := filepath.Join(dir, "log")
	bin := filepath.Join(dir, "restic")
	// It logs its args and the known_hosts its ssh would read, then fails.
	write(t, bin, "#!/bin/sh\necho \"$*\" >> "+log+"\nf=$(echo \"$*\" | sed -n 's/.*UserKnownHostsFile=\\([^ ]*\\).*/\\1/p')\ncat \"$f\" >> "+log+"\nexit 1\n")
	os.Chmod(bin, 0o755)
	t.Setenv("ISOTOPE_RESTIC", bin)
	target := "sftp://backup@nas:2222//srv/isotope"
	if err := initTarget(p, target, initOptions{HostKey: fp, Port: 2222}, "pw", nil); err == nil {
		t.Fatal("a failed init returned nil")
	}
	b, _ := os.ReadFile(log)
	if !strings.Contains(string(b), "UserKnownHostsFile="+filepath.Join(p.Run, "known_hosts-")) || !strings.Contains(string(b), other+"\n"+line+"\n") {
		t.Fatalf("restic did not see the staged key: %s", b)
	}
	if b, _ := os.ReadFile(kh); string(b) != other+"\n" {
		t.Fatalf("a failed init changed known_hosts: %q", b)
	}
	if left, _ := filepath.Glob(filepath.Join(p.Run, "known_hosts*")); len(left) != 0 {
		t.Fatalf("the staged known_hosts is left: %q", left)
	}

	fakeRestic(t)
	if err := initTarget(p, target, initOptions{HostKey: fp, Port: 2222}, "pw", nil); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(kh); string(b) != other+"\n"+line+"\n" {
		t.Fatalf("known_hosts %q", b)
	}
}

// TestStagedKnownHostsAreSeparate: two setups at the same time each get
// their own staged file, which only root can read.
func TestStagedKnownHostsAreSeparate(t *testing.T) {
	p := backupSetup(t)
	a, err := stageKnownHosts(p, "nas ssh-ed25519 AAAA")
	if err != nil {
		t.Fatal(err)
	}
	b, err := stageKnownHosts(p, "other ssh-ed25519 BBBB")
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Fatalf("one staged file for both: %s", a)
	}
	for f, want := range map[string]string{a: "nas ssh-ed25519 AAAA\n", b: "other ssh-ed25519 BBBB\n"} {
		got, _ := os.ReadFile(f)
		fi, _ := os.Stat(f)
		if string(got) != want || fi.Mode().Perm() != 0o600 {
			t.Errorf("%s: %q, mode %v", f, got, fi.Mode().Perm())
		}
	}
}
