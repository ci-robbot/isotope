package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"

	"github.com/kairos-community/isotope/update/recoverykey"
)

// snapshotsJSON is what `restic snapshots --json` prints: two snapshots,
// the older one first, the newer one without a summary.
const snapshotsJSON = `[
 {"time":"2026-11-01T10:00:00Z","hostname":"laptop","id":"aaaaaaaa11111111","short_id":"aaaaaaaa","summary":{"total_bytes_processed":12345}},
 {"time":"2026-11-20T10:00:00.5+01:00","hostname":"desk","id":"bbbbbbbb22222222","short_id":"bbbbbbbb"}
]`

// jsonRestic logs each call with its repository and the content of its
// password file, and prints snapshotsJSON.
func jsonRestic(t *testing.T) (log string) {
	t.Helper()
	dir := t.TempDir()
	log = filepath.Join(dir, "log")
	write(t, filepath.Join(dir, "out.json"), snapshotsJSON)
	bin := filepath.Join(dir, "restic")
	write(t, bin, "#!/bin/sh\necho \"$RESTIC_REPOSITORY | $* | pass=$(cat \"$RESTIC_PASSWORD_FILE\") | user=$RESTIC_REST_USERNAME\" >> "+log+"\ncat "+filepath.Join(dir, "out.json")+"\n")
	if err := os.Chmod(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ISOTOPE_RESTIC", bin)
	return log
}

func wantSnapshots(t *testing.T, got []map[string]dbus.Variant) {
	t.Helper()
	if len(got) != 2 {
		t.Fatalf("%d snapshots: %v", len(got), got)
	}
	newer, older := got[0], got[1]
	if newer["id"].Value() != "bbbbbbbb" || newer["host"].Value() != "desk" || newer["size"].Value() != uint64(0) ||
		newer["time"].Value() != time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC).Unix() {
		t.Fatalf("newest %v", newer)
	}
	if older["id"].Value() != "aaaaaaaa" || older["host"].Value() != "laptop" || older["size"].Value() != uint64(12345) {
		t.Fatalf("older %v", older)
	}
}

func TestBackupListOfTheConfiguredTarget(t *testing.T) {
	p := backupSetup(t)
	log := jsonRestic(t)
	target := filepath.Join(t.TempDir(), "repo")
	os.MkdirAll(target, 0o700)
	write(t, filepath.Join(target, "config"), "x")
	if err := saveBackupConfig(p, backupConfig{Target: target, Schedule: "off"}); err != nil {
		t.Fatal(err)
	}
	if err := writeSecret(p.password(), "machine-pw"); err != nil {
		t.Fatal(err)
	}
	got, err := listSnapshots(context.Background(), p, "", initOptions{}, []string{""})
	if err != nil {
		t.Fatal(err)
	}
	wantSnapshots(t, got)
	b, _ := os.ReadFile(log)
	if want := target + " | snapshots --json --tag isotope | pass=machine-pw"; !strings.HasPrefix(string(b), want) {
		t.Fatalf("restic ran %q, want %q", b, want)
	}
}

func TestBackupListOfAnExportFile(t *testing.T) {
	p := backupSetup(t)
	log := jsonRestic(t)
	repo := filepath.Join(t.TempDir(), "repo")
	os.MkdirAll(filepath.Join(repo, "data"), 0o700)
	write(t, filepath.Join(repo, "config"), "x")
	file := filepath.Join(t.TempDir(), "machine.isotope-backup")
	if err := packDir(repo, file); err != nil {
		t.Fatal(err)
	}
	got, err := listSnapshots(context.Background(), p, file, initOptions{}, []string{"file-pw", "RESTIC_REST_USERNAME=anna"})
	if err != nil {
		t.Fatal(err)
	}
	wantSnapshots(t, got)
	b, _ := os.ReadFile(log)
	line := strings.SplitN(string(b), " | ", 2)
	if !strings.HasPrefix(line[0], filepath.Join(p.State, "work-")) {
		t.Fatalf("restic read %q, not a work folder under %s", line[0], p.State)
	}
	if !strings.Contains(string(b), "pass=file-pw | user=anna") {
		t.Fatalf("restic got %q", b)
	}
	if m, _ := filepath.Glob(filepath.Join(p.State, "work-*")); len(m) != 0 {
		t.Fatalf("left behind: %v", m)
	}
	if e, _ := os.ReadDir(p.Run); len(e) != 0 {
		t.Fatalf("secrets left in %s: %v", p.Run, e)
	}
	// A password is needed for any other source.
	if _, err := listSnapshots(context.Background(), p, file, initOptions{}, []string{""}); err == nil {
		t.Fatal("an empty password was accepted")
	}
}

// fakeChown records fchownFile's calls.
func fakeChown(t *testing.T) *[]string {
	t.Helper()
	var mu sync.Mutex
	calls := &[]string{}
	old := fchownFile
	fchownFile = func(f *os.File, uid, gid int) error {
		mu.Lock()
		defer mu.Unlock()
		*calls = append(*calls, fmt.Sprintf("%s %d:%d", f.Name(), uid, gid))
		return nil
	}
	t.Cleanup(func() { fchownFile = old })
	return calls
}

func TestBackupExportChownsToTheCaller(t *testing.T) {
	p := backupSetup(t)
	fakeRestic(t)
	calls := fakeChown(t)
	if err := writeSecret(p.password(), "machine-pw"); err != nil {
		t.Fatal(err)
	}
	d := backupDaemon(t, nil)
	file := filepath.Join(t.TempDir(), "machine.isotope-backup")
	if err := d.BackupExport(file, os.Getuid(), 4343); err != nil {
		t.Fatal(err)
	}
	d.wg.Wait()
	st := d.Status()
	if st.BackupResult != backupResultOK || st.State != StateIdle {
		t.Fatalf("after the export: %+v", st)
	}
	if !exists(file) || exists(file+".partial") {
		t.Fatal("no file, or the partial one is left")
	}
	// The open file is given away before it gets its name.
	if want := fmt.Sprintf("%s.partial %d:4343", file, os.Getuid()); len(*calls) != 1 || (*calls)[0] != want {
		t.Fatalf("chown %q, want %q", *calls, want)
	}
}

// TestBackupExportRefusesAFolderItCannotTrust: root never works in a folder
// that a link leads to, another user owns, or other users can change.
func TestBackupExportRefusesAFolderItCannotTrust(t *testing.T) {
	p := backupSetup(t)
	fakeRestic(t)
	fakeChown(t)
	writeSecret(p.password(), "machine-pw")
	victim := t.TempDir() // stands for /etc
	base := t.TempDir()
	link := filepath.Join(base, "link")
	if err := os.Symlink(victim, link); err != nil {
		t.Fatal(err)
	}
	shared := filepath.Join(base, "shared")
	os.Mkdir(shared, 0o755)
	os.Chmod(shared, 0o777)
	me := os.Getuid()
	for _, c := range []struct {
		file string
		uid  int
		want string
	}{
		{filepath.Join(link, "shadow"), me, "is a link"},
		{filepath.Join(base, "machine.isotope-backup"), me + 1, "belongs to another user"},
		{filepath.Join(shared, "machine.isotope-backup"), me, "can be changed by other users"},
	} {
		_, err := daemonExport(context.Background(), func(string) {}, c.file, c.uid, 0)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Fatalf("%s for uid %d: %v, want %q", c.file, c.uid, err, c.want)
		}
		if exists(c.file) || exists(c.file+".partial") {
			t.Fatalf("%s was written", c.file)
		}
	}
	if e, _ := os.ReadDir(victim); len(e) != 0 {
		t.Fatalf("written through the link: %v", e)
	}
}

func TestBackupExportRefusesAFolderOfRoot(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("runs as root")
	}
	// /usr stands for /oem or /etc/profile.d: root's, 0755.
	if _, err := openExportDir("/usr", &exportOwner{uid: os.Getuid()}); err == nil ||
		!strings.Contains(err.Error(), "belongs to another user") {
		t.Fatalf("%v", err)
	}
}

func TestBackupExportIntoAUSBDiskOfTheCaller(t *testing.T) {
	p := backupSetup(t)
	fakeRestic(t)
	calls := fakeChown(t)
	writeSecret(p.password(), "machine-pw")
	// /run/media/anna/USB: a vfat or exFAT disk mounted with the user as
	// owner.
	usb := filepath.Join(t.TempDir(), "run", "media", "anna", "USB")
	if err := os.MkdirAll(usb, 0o755); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(usb, "machine.isotope-backup")
	if _, err := daemonExport(context.Background(), func(string) {}, file, os.Getuid(), os.Getgid()); err != nil {
		t.Fatal(err)
	}
	if !exists(file) || len(*calls) != 1 {
		t.Fatalf("file %v, chown %q", exists(file), *calls)
	}
}

func TestBackupExportKeepsAFileThatIsTheCallersAlready(t *testing.T) {
	p := backupSetup(t)
	fakeRestic(t)
	writeSecret(p.password(), "machine-pw")
	old := fchownFile
	fchownFile = func(*os.File, int, int) error { return syscall.EPERM }
	t.Cleanup(func() { fchownFile = old })
	dir := t.TempDir()
	// The test runs as the owner: the file is theirs already.
	file := filepath.Join(dir, "machine.isotope-backup")
	if _, err := daemonExport(context.Background(), func(string) {}, file, os.Getuid(), 4343); err != nil {
		t.Fatal(err)
	}
	if !exists(file) {
		t.Fatal("the file was removed")
	}
}

func TestDaemonExportWithoutAPasswordPrintsNoKey(t *testing.T) {
	backupSetup(t)
	fakeRestic(t)
	var lines []string
	_, err := daemonExport(context.Background(), func(l string) { lines = append(lines, l) },
		filepath.Join(t.TempDir(), "x"), os.Getuid(), 0)
	if !errors.Is(err, errExportNeedsPassword) {
		t.Fatalf("%v", err)
	}
	if len(lines) != 0 {
		t.Fatalf("progress %q", lines)
	}
}

func TestCancelledBackupIsRecordedAsCancelled(t *testing.T) {
	p := backupSetup(t)
	fakeRestic(t)
	target := filepath.Join(t.TempDir(), "repo")
	os.MkdirAll(target, 0o700)
	write(t, filepath.Join(target, "config"), "x")
	saveBackupConfig(p, backupConfig{Target: target, Schedule: "off"})
	writeSecret(p.password(), "pw")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := runConfiguredBackup(ctx, p, false, false, io.Discard, io.Discard)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("%v", err)
	}
	if c, _ := loadBackupConfig(p); c.Result != "Cancelled" || c.Last != 0 {
		t.Fatalf("config %+v", c)
	}
}

func TestRestoreBlocksUserAndSettingChanges(t *testing.T) {
	backupSetup(t)
	newMachine(t)
	resetRestoring(t)
	release := make(chan struct{})
	d := backupDaemon(t, func(context.Context, func(string)) (string, error) { return "ok", nil })
	d.restore = func(context.Context, func(string), restoreRequest) (string, error) {
		<-release
		return "", errors.New("wrong password")
	}
	f := startService(t, d, &fakeAuth{allow: true})
	if err := d.Restore(restoreRequest{Source: "/mnt/x", Password: "pw"}); err != nil {
		t.Fatal(err)
	}
	waitState(t, d, StateRestore)
	wantBusErr(t, "AddUser", secretCall(t, f, "AddUser", []string{"pw"}, "ben", "Ben", false), "Busy", "being restored")
	wantBusErr(t, "SetAdmin", asDBusError(f.call("SetAdmin", "anna", false).Err), "Busy", "")
	wantBusErr(t, "SetLocale", asDBusError(f.call("SetLocale", "de_DE.UTF-8").Err), "Busy", "")
	// The functions refuse too, under their lock: a call that passed the
	// state check before the job started.
	if err := addUser("ben", "Ben", false, "pw"); !errors.Is(err, ErrBusy) {
		t.Fatalf("addUser: %v", err)
	}
	if err := setAdmin("anna", false); !errors.Is(err, ErrBusy) {
		t.Fatalf("setAdmin: %v", err)
	}
	close(release)
	d.wg.Wait()
	usersMu.Lock()
	still := restoring
	usersMu.Unlock()
	if still {
		t.Fatal("a failed restore still blocks the users")
	}
}

func TestBackupExportWithoutAPasswordIsRefused(t *testing.T) {
	backupSetup(t)
	ran := false
	d := backupDaemon(t, nil)
	d.export = func(context.Context, func(string), string, int, int) (string, error) { ran = true; return "", nil }
	err := d.BackupExport(filepath.Join(t.TempDir(), "x"), 1000, 1000)
	if err == nil || err.Error() != "Set up a backup first; the file is encrypted with its password" {
		t.Fatalf("%v", err)
	}
	d.wg.Wait()
	if ran || d.Status().State != StateIdle {
		t.Fatal("the export ran")
	}
}

func TestBackupExportRunsAsAJob(t *testing.T) {
	p := backupSetup(t)
	writeSecret(p.password(), "pw")
	release := make(chan struct{})
	d := backupDaemon(t, func(context.Context, func(string)) (string, error) { return "ok", nil })
	var got string
	d.export = func(_ context.Context, progress func(string), file string, uid, gid int) (string, error) {
		got = file
		progress("Files: 1 new")
		<-release
		return "Exported.", nil
	}
	file := filepath.Join(t.TempDir(), "machine.isotope-backup")
	if err := d.BackupExport(file, 1, 1); err != nil {
		t.Fatal(err)
	}
	waitState(t, d, StateBackup)
	if err := d.BackupNow(); !errors.Is(err, ErrBusy) {
		t.Fatalf("BackupNow during an export: %v", err)
	}
	close(release)
	d.wg.Wait()
	if st := d.Status(); got != file || st.BackupResult != backupResultOK || st.BackupDetail != "Exported." {
		t.Fatalf("file %q, %+v", got, st)
	}
}

func TestBackupCancelStopsTheJob(t *testing.T) {
	d := backupDaemon(t, func(ctx context.Context, progress func(string)) (string, error) {
		<-ctx.Done()
		return "", ctx.Err()
	})
	if err := d.BackupCancel(); !errors.Is(err, errNothingToCancel) {
		t.Fatalf("BackupCancel with no job: %v", err)
	}
	if err := d.BackupNow(); err != nil {
		t.Fatal(err)
	}
	waitState(t, d, StateBackup)
	if err := d.BackupCancel(); err != nil {
		t.Fatal(err)
	}
	d.wg.Wait()
	if st := d.Status(); st.State != StateIdle || st.BackupResult != backupResultFailed || st.BackupDetail != "Cancelled" {
		t.Fatalf("after the cancel: %+v", st)
	}
}

func TestRunCtxKillsTheCommand(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.Command("sh", "-c", "exec sleep 30")
	go func() { time.Sleep(50 * time.Millisecond); cancel() }()
	start := time.Now()
	err := runCtx(ctx, cmd)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("%v", err)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatal("restic was not stopped")
	}
}

// newMachine makes the machine new: one user, whose home is in a temporary
// folder, and no backup. It returns that home.
func newMachine(t *testing.T) (home string) {
	t.Helper()
	usersFixture(t)
	home = t.TempDir()
	writeLines(t, passwdFile, []string{
		"root:x:0:0:root:/root:/bin/bash",
		"anna:x:1000:1000:Anna:" + home + ":/bin/bash",
		"nobody:x:65534:65534:nobody:/:/sbin/nologin",
	})
	return home
}

func TestMachineIsNew(t *testing.T) {
	cases := []struct {
		name   string
		break_ func(t *testing.T, p backupPaths, home string)
		reason string
	}{
		{"new", func(*testing.T, backupPaths, string) {}, ""},
		{"two users", func(t *testing.T, p backupPaths, home string) {
			appendLine(t, passwdFile, "ben:x:1001:1001:Ben:/home/ben:/bin/bash")
		}, "there is more than one user"},
		{"no user", func(t *testing.T, p backupPaths, home string) {
			writeLines(t, passwdFile, []string{"root:x:0:0:root:/root:/bin/bash"})
		}, "there is no user"},
		{"a target", func(t *testing.T, p backupPaths, home string) {
			saveBackupConfig(p, backupConfig{Target: "/mnt/usb", Schedule: "off"})
		}, "a backup target is set up"},
		{"a backup", func(t *testing.T, p backupPaths, home string) {
			saveBackupConfig(p, backupConfig{Schedule: "off", Last: 1700000000, Result: "ok"})
		}, "a backup was made on this machine"},
		{"an export", func(t *testing.T, p backupPaths, home string) {
			os.MkdirAll(p.manifest(), 0o755)
		}, "a backup was made on this machine"},
		{"a pending restore", func(t *testing.T, p backupPaths, home string) {
			os.MkdirAll(p.pending(), 0o700)
		}, "a backup was restored on this machine"},
		{"a finished restore", func(t *testing.T, p backupPaths, home string) {
			os.MkdirAll(p.State, 0o755)
			write(t, p.finishResult(), "ok\n")
		}, "a backup was restored on this machine"},
	}
	for _, stamp := range []string{"isotope/welcome", "isotope/first-run-apps", "isotope/welcomed",
		"hadron/welcome", "hadron/first-run-apps", "hadron/welcomed"} {
		cases = append(cases, struct {
			name   string
			break_ func(t *testing.T, p backupPaths, home string)
			reason string
		}{"stamp " + stamp, func(t *testing.T, p backupPaths, home string) {
			f := filepath.Join(home, ".local/state", stamp)
			os.MkdirAll(filepath.Dir(f), 0o755)
			write(t, f, "done\n")
		}, "the first login of anna is over"})
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := backupSetup(t)
			home := newMachine(t)
			c.break_(t, p, home)
			ok, reason := machineIsNew(p)
			if ok != (c.reason == "") || reason != c.reason {
				t.Fatalf("machineIsNew = %v %q, want reason %q", ok, reason, c.reason)
			}
		})
	}
}

func TestBackupStatusSaysWhetherItCanRestore(t *testing.T) {
	p := backupSetup(t)
	newMachine(t)
	m, err := backupStatusDict(p)
	if err != nil {
		t.Fatal(err)
	}
	if m["can_restore"].Value() != true || m["restore_refusal"].Value() != "" {
		t.Fatalf("%v", m)
	}
	appendLine(t, passwdFile, "ben:x:1001:1001:Ben:/home/ben:/bin/bash")
	m, _ = backupStatusDict(p)
	if m["can_restore"].Value() != false || m["restore_refusal"].Value() != "there is more than one user" {
		t.Fatalf("%v", m)
	}
}

// resetRestoring clears restoring after the test, with the locks that
// guard it: a restore that asked for the restart keeps blocking the users.
func resetRestoring(t *testing.T) {
	t.Helper()
	t.Cleanup(func() {
		usersMu.Lock()
		settingsMu.Lock()
		restoring = false
		settingsMu.Unlock()
		usersMu.Unlock()
	})
}

// restoreDaemon is a daemon whose restore records its request and waits
// for release.
func restoreDaemon(t *testing.T, release chan struct{}) (*Daemon, *restoreRequest, *sync.Mutex) {
	t.Helper()
	resetRestoring(t)
	d := backupDaemon(t, func(context.Context, func(string)) (string, error) { return "ok", nil })
	var mu sync.Mutex
	got := &restoreRequest{}
	d.restore = func(ctx context.Context, progress func(string), req restoreRequest) (string, error) {
		mu.Lock()
		*got = req
		mu.Unlock()
		progress("restoring /usr/local")
		<-release
		return "Restored; the machine restarts.", nil
	}
	return d, got, &mu
}

func TestRestoreRefusesAMachineInUse(t *testing.T) {
	p := backupSetup(t)
	newMachine(t)
	appendLine(t, passwdFile, "ben:x:1001:1001:Ben:/home/ben:/bin/bash")
	old := secretReadTimeout
	secretReadTimeout = time.Minute
	t.Cleanup(func() { secretReadTimeout = old })
	d, _, mu := restoreDaemon(t, make(chan struct{}))
	ran := false
	d.restore = func(context.Context, func(string), restoreRequest) (string, error) {
		mu.Lock()
		ran = true
		mu.Unlock()
		return "", nil
	}
	f := startService(t, d, &fakeAuth{allow: true})

	// The write end stays open and holds nothing: a service that read the
	// fd would wait for it until secretReadTimeout.
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	start := time.Now()
	c := f.obj.Call(busInterface+".Restore", 0, "/mnt/usb/backup", "", "", map[string]dbus.Variant{}, dbus.UnixFD(r.Fd()))
	r.Close()
	if time.Since(start) > 5*time.Second {
		t.Fatal("the service read the fd before it refused")
	}
	wantBusErr(t, "Restore", asDBusError(c.Err), "Refused",
		"This machine is already in use; restore parts of a backup with isotope restore --only")
	if _, err := w.Write([]byte("x\n")); !errors.Is(err, syscall.EPIPE) {
		t.Fatalf("the service kept the fd open (write: %v)", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if ran || d.Status().State != StateIdle {
		t.Fatal("the restore ran")
	}
	if e, _ := os.ReadDir(p.Run); len(e) != 0 {
		t.Fatalf("files in %s: %v", p.Run, e)
	}
}

func TestRestoreRunsAsAJob(t *testing.T) {
	backupSetup(t)
	newMachine(t)
	fakeRestic(t)
	release := make(chan struct{})
	d, got, mu := restoreDaemon(t, release)
	var lmu sync.Mutex
	var lines []string
	d.OnProgress(func(l string) { lmu.Lock(); lines = append(lines, l); lmu.Unlock() })
	f := startService(t, d, &fakeAuth{allow: true})
	source := "rest:https://nas:8000/isotope"
	if err := secretCall(t, f, "Restore", []string{"backup-pw", "RESTIC_REST_USERNAME=anna"},
		source, "bbbbbbbb", "desk", map[string]dbus.Variant{}); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	st := f.waitBusState(t, StateRestore)
	if st["detail"].Value() == "" {
		t.Fatal("the restore state says nothing")
	}
	// The slot is taken.
	if err := d.BackupNow(); !errors.Is(err, ErrBusy) {
		t.Fatalf("BackupNow during a restore: %v", err)
	}
	close(release)
	d.wg.Wait()
	mu.Lock()
	req := *got
	mu.Unlock()
	if req.Source != source || req.Snapshot != "bbbbbbbb" || req.Host != "desk" || req.Password != "backup-pw" ||
		len(req.Env) != 1 || req.Env[0] != "RESTIC_REST_USERNAME=anna" {
		t.Fatalf("restore got %+v", req)
	}
	if st := d.Status(); st.BackupResult != backupResultOK {
		t.Fatalf("after the restore: %+v", st)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		lmu.Lock()
		n := len(lines)
		lmu.Unlock()
		if n > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("no progress line")
		}
		time.Sleep(2 * time.Millisecond)
	}
	// A target with a password in it, or another scheme, is refused.
	for _, bad := range []string{"rest:https://anna:pw@nas/isotope", "rclone:x:y"} {
		if err := secretCall(t, f, "Restore", []string{"pw"}, bad, "", "", map[string]dbus.Variant{}); err == nil {
			t.Fatalf("%s was accepted", bad)
		}
	}
	// A snapshot or a host that restic would read as an option is refused.
	for _, bad := range [][2]string{{"--password-command=id", ""}, {"", "--x"}} {
		if err := secretCall(t, f, "Restore", []string{"pw"}, source, bad[0], bad[1], map[string]dbus.Variant{}); err == nil {
			t.Fatalf("%q was accepted", bad)
		}
	}
}

func TestRestoreIsNotCancellable(t *testing.T) {
	backupSetup(t)
	newMachine(t)
	release := make(chan struct{})
	d, _, _ := restoreDaemon(t, release)
	if err := d.Restore(restoreRequest{Source: "/mnt/x", Password: "pw"}); err != nil {
		t.Fatal(err)
	}
	waitState(t, d, StateRestore)
	if err := d.BackupCancel(); !errors.Is(err, ErrNotCancellable) {
		t.Fatalf("BackupCancel: %v", err)
	}
	if err := d.Cancel(); !errors.Is(err, ErrNotCancellable) {
		t.Fatalf("Cancel: %v", err)
	}
	if err := d.Reboot(); !errors.Is(err, ErrBusy) {
		t.Fatalf("Reboot: %v", err)
	}
	if err := d.RebootPrevious(); !errors.Is(err, ErrBusy) {
		t.Fatalf("RebootPrevious: %v", err)
	}
	if !d.Busy() {
		t.Fatal("not busy")
	}
	close(release)
	d.wg.Wait()
}

// TestDaemonRestoreUsesTheCLIPath: the password reaches restic through the
// CLI's own read, and the credentials through a file in p.Run, which is
// gone at the end.
func TestDaemonRestoreUsesTheCLIPath(t *testing.T) {
	p := backupSetup(t)
	dir := t.TempDir()
	log := filepath.Join(dir, "log")
	bin := filepath.Join(dir, "restic")
	write(t, bin, "#!/bin/sh\necho \"$* | pass=$(cat \"$RESTIC_PASSWORD_FILE\") | user=$RESTIC_REST_USERNAME\" >> "+log+"\nexit 1\n")
	os.Chmod(bin, 0o755)
	t.Setenv("ISOTOPE_RESTIC", bin)
	repo := filepath.Join(t.TempDir(), "repo")
	os.MkdirAll(repo, 0o700)
	write(t, filepath.Join(repo, "config"), "x")

	var lines []string
	_, err := daemonRestore(context.Background(), func(l string) { lines = append(lines, l) },
		restoreRequest{Source: repo, Host: "desk", Password: "backup-pw", Env: []string{"RESTIC_REST_USERNAME=anna"}})
	if err == nil || !strings.Contains(err.Error(), "wrong password?") {
		t.Fatalf("%v", err)
	}
	b, _ := os.ReadFile(log)
	if !strings.Contains(string(b), "snapshots --json --tag isotope --latest 1 --host desk | pass=backup-pw | user=anna") {
		t.Fatalf("restic ran %q", b)
	}
	if e, _ := os.ReadDir(p.Run); len(e) != 0 {
		t.Fatalf("files left in %s: %v", p.Run, e)
	}
	if !exists(repo) {
		t.Fatal("the source is gone")
	}
}

func TestRestoreAsksTheRestoreAction(t *testing.T) {
	backupSetup(t)
	newMachine(t)
	d, _, _ := restoreDaemon(t, make(chan struct{}))
	auth := &fakeAuth{allow: false}
	f := startService(t, d, auth)
	me := f.client.Names()[0]
	for _, c := range []struct {
		method, action string
		args           []any
		fd             bool
	}{
		{"BackupList", actionBackup, []any{"", map[string]dbus.Variant{}}, true},
		{"BackupExport", actionBackup, []any{"/tmp/x"}, false},
		{"BackupCancel", actionBackup, nil, false},
		{"Restore", actionRestore, []any{"/mnt/x", "", "", map[string]dbus.Variant{}}, true},
	} {
		n := len(auth.questions())
		var err *dbus.Error
		if c.fd {
			err = secretCall(t, f, c.method, []string{"pw"}, c.args...)
		} else {
			err = asDBusError(f.call(c.method, c.args...).Err)
		}
		wantBusErr(t, c.method, err, "NotAuthorized", "")
		q := auth.questions()
		if len(q) != n+1 || q[n] != me+" "+c.action {
			t.Fatalf("%s asked %q, want %s", c.method, q[n:], c.action)
		}
	}
	if d.Status().State != StateIdle {
		t.Fatal("a refused call started a job")
	}
}

func TestTrayKnowsTheRestoreState(t *testing.T) {
	if !busyState(StateRestore) || iconFor(StateRestore) != "busy" {
		t.Fatal("the tray does not see the restore as a job")
	}
	if got := statusLine(Status{State: StateRestore}); got != "Restoring a backup…" {
		t.Fatalf("%q", got)
	}
}

// typedLikeShown is key as a person may type it back from the four shown
// lines: the lines joined with spaces, in upper case.
func typedLikeShown(t *testing.T) (key, typed string) {
	t.Helper()
	key, err := recoverykey.New()
	if err != nil {
		t.Fatal(err)
	}
	return key, strings.ToUpper(strings.Join(recoverykey.Lines(key), " "))
}

func TestBackupPasswordTakesTheShownLines(t *testing.T) {
	key, typed := typedLikeShown(t)
	for _, in := range []string{key, typed, strings.Join(recoverykey.Lines(key), " "), strings.ReplaceAll(key, "-", " ")} {
		if got := backupPassword(in); got != key {
			t.Errorf("%q gave %q", in, got)
		}
	}
	for _, other := range []string{"my secret", "My-Secret", "pw-123", strings.ToUpper(key) + "x"} {
		if got := backupPassword(other); got != other {
			t.Errorf("%q was changed to %q", other, got)
		}
	}
	// --password-stdin hands the text as typed to initTarget, which makes
	// it canonical itself (and tries the typed form for an old backup).
	got, err := newPassword(true, bufio.NewReader(strings.NewReader(typed+"\n")), io.Discard)
	if err != nil || got != typed || backupPassword(got) != key {
		t.Fatalf("--password-stdin: %q %v", got, err)
	}
	// The restore reads it as typed, and makes it canonical itself (it
	// tries the typed form too).
	got, err = readSecretLine(bufio.NewReader(strings.NewReader(typed + "\n")))
	if err != nil || got != typed || backupPassword(got) != key {
		t.Fatalf("restore's password: %q %v", got, err)
	}
}

// oldPasswordRestic is a restic that takes only the password old, and
// says "wrong password" (exit 12) to any other. It lists the snapshots, but
// fails a restore. Each call is logged with its password.
func oldPasswordRestic(t *testing.T, old string) (log string) {
	t.Helper()
	dir := t.TempDir()
	log = filepath.Join(dir, "log")
	write(t, filepath.Join(dir, "out.json"), snapshotsJSON)
	write(t, filepath.Join(dir, "old"), old+"\n")
	bin := filepath.Join(dir, "restic")
	write(t, bin, "#!/bin/sh\necho \"$* | pass=$(cat \"$RESTIC_PASSWORD_FILE\")\" >> "+log+
		"\nif ! cmp -s \"$RESTIC_PASSWORD_FILE\" "+filepath.Join(dir, "old")+"; then echo 'Fatal: wrong password or no key found' >&2; exit 12; fi"+
		"\ncase \"$1\" in snapshots) cat "+filepath.Join(dir, "out.json")+";; restore) exit 1;; esac\n")
	if err := os.Chmod(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ISOTOPE_RESTIC", bin)
	return log
}

// TestOldNonCanonicalPasswordStillWorks: an old backup whose password has
// the recovery key format but was stored as typed (upper case, spaces). The
// canonical form is tried first; when restic refuses it, the password as
// typed. Opening, listing and restoring all work, and an opened target keeps
// the password that worked.
func TestOldNonCanonicalPasswordStillWorks(t *testing.T) {
	p := backupSetup(t)
	key, typed := typedLikeShown(t)
	log := oldPasswordRestic(t, typed)

	if err := initTarget(p, "rest:https://nas:8000/isotope", initOptions{Existing: true}, typed, []string{}); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(p.password()); string(b) != typed+"\n" {
		t.Fatalf("password %q, want the typed one", b)
	}
	if b, _ := os.ReadFile(log); !strings.Contains(string(b), "cat config | pass="+key+"\n") || !strings.Contains(string(b), "cat config | pass="+typed+"\n") {
		t.Fatalf("restic ran %q", b)
	}

	got, err := listSnapshots(context.Background(), p, "rest:https://nas:8000/other", initOptions{}, []string{typed})
	if err != nil {
		t.Fatal(err)
	}
	wantSnapshots(t, got)

	// The CLI: `isotope backup init TARGET --password-stdin` opens it too.
	os.Remove(p.password())
	if code, _, errOut := runCLI(typed+"\n", "backup", "init", "rest:https://nas:8000/cli", "--password-stdin"); code != exitOK {
		t.Fatalf("CLI init: exit %d: %s", code, errOut)
	}
	if b, _ := os.ReadFile(p.password()); string(b) != typed+"\n" {
		t.Fatalf("CLI init stored %q, want the typed password", b)
	}

	os.Truncate(log, 0)
	repo := filepath.Join(t.TempDir(), "repo")
	os.MkdirAll(repo, 0o700)
	write(t, filepath.Join(repo, "config"), "x")
	_, err = daemonRestore(context.Background(), func(string) {}, restoreRequest{Source: repo, Snapshot: "aaaaaaaa", Password: typed})
	if err == nil || !strings.Contains(err.Error(), "wrong password?") {
		t.Fatalf("%v", err)
	}
	b, _ := os.ReadFile(log)
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	if len(lines) != 3 || lines[0] != "cat config | pass="+key || lines[1] != "cat config | pass="+typed ||
		!strings.HasPrefix(lines[2], "restore aaaaaaaa ") || !strings.HasSuffix(lines[2], " | pass="+typed) {
		t.Fatalf("restic ran %q", lines)
	}

	// A password that is wrong in both forms gives the usual error, and the
	// canonical form stays.
	oldPasswordRestic(t, "something else")
	if err := initTarget(p, "rest:https://nas:8000/isotope", initOptions{Existing: true}, typed, []string{}); err == nil {
		t.Fatal("a wrong password was accepted")
	}
	if b, _ := os.ReadFile(p.password()); string(b) != typed+"\n" {
		t.Fatalf("a refused init changed the password: %q", b)
	}
}

func TestInitAndListTakeTheShownLines(t *testing.T) {
	p := backupSetup(t)
	key, typed := typedLikeShown(t)
	fakeRestic(t)
	if err := initTarget(p, "rest:https://nas:8000/isotope", initOptions{Existing: true}, typed, []string{}); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(p.password()); string(b) != key+"\n" {
		t.Fatalf("password %q, want %q", b, key)
	}
	log := jsonRestic(t)
	if _, err := listSnapshots(context.Background(), p, "rest:https://nas:8000/other", initOptions{}, []string{typed}); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(log); !strings.Contains(string(b), "pass="+key+" ") {
		t.Fatalf("restic got %q", b)
	}
}

// On a new machine the SFTP server is not in known_hosts yet: BackupList
// and Restore trust the key the user confirmed, as BackupInit does.
func TestListAndRestoreTrustTheConfirmedHostKey(t *testing.T) {
	p := backupSetup(t)
	line, fp := hostKeyLine("nas")
	fakeSSH(t, line)
	kh := filepath.Join(sshDir(p), "known_hosts")
	src := "sftp:backup@nas:/srv/isotope"
	log := jsonRestic(t)
	if _, err := listSnapshots(context.Background(), p, src, initOptions{}, []string{"pw"}); err == nil || !strings.Contains(err.Error(), "not confirmed") {
		t.Fatalf("no host key: %v", err)
	}
	if _, err := scanHostKey(p, "nas", 22); err != nil {
		t.Fatal(err)
	}
	got, err := listSnapshots(context.Background(), p, src, initOptions{HostKey: fp, Port: 22}, []string{"pw"})
	if err != nil {
		t.Fatal(err)
	}
	wantSnapshots(t, got)
	if b, _ := os.ReadFile(kh); string(b) != line+"\n" {
		t.Fatalf("known_hosts %q", b)
	}
	if b, _ := os.ReadFile(log); !strings.Contains(string(b), "StrictHostKeyChecking=yes") {
		t.Fatalf("restic %q", b)
	}

	// Restore, from a fresh known_hosts, with the password as typed.
	os.Remove(kh)
	key, typed := typedLikeShown(t)
	dir := t.TempDir()
	rlog := filepath.Join(dir, "log")
	bin := filepath.Join(dir, "restic")
	write(t, bin, "#!/bin/sh\necho \"$* | pass=$(cat \"$RESTIC_PASSWORD_FILE\")\" >> "+rlog+"\nexit 1\n")
	os.Chmod(bin, 0o755)
	t.Setenv("ISOTOPE_RESTIC", bin)
	if _, err := daemonRestore(context.Background(), func(string) {}, restoreRequest{Source: src, Password: typed}); err == nil || !strings.Contains(err.Error(), "not confirmed") {
		t.Fatalf("restore without a host key: %v", err)
	}
	if exists(kh) {
		t.Fatal("known_hosts written without a confirmed key")
	}
	_, err = daemonRestore(context.Background(), func(string) {}, restoreRequest{Source: src, Password: typed, HostKey: fp, Port: 22})
	if err == nil || !strings.Contains(err.Error(), "wrong password?") {
		t.Fatalf("%v", err)
	}
	if b, _ := os.ReadFile(kh); string(b) != line+"\n" {
		t.Fatalf("known_hosts after restore %q", b)
	}
	if b, _ := os.ReadFile(rlog); !strings.Contains(string(b), "StrictHostKeyChecking=yes") || !strings.Contains(string(b), "pass="+key) {
		t.Fatalf("restic ran %q", b)
	}
}

// fakeSignalNotify counts the calls of signalNotify, and asks for no signal.
func fakeSignalNotify(t *testing.T) *atomic.Int32 {
	t.Helper()
	var n atomic.Int32
	old := signalNotify
	signalNotify = func(chan<- os.Signal, ...os.Signal) { n.Add(1) }
	t.Cleanup(func() { signalNotify = old })
	return &n
}

// TestDaemonJobsInstallNoSignalHandler: the CLI's handler exits the
// process on SIGTERM, so the daemon's export and restore must not install
// it; the CLI's export does.
func TestDaemonJobsInstallNoSignalHandler(t *testing.T) {
	p := backupSetup(t)
	fakeRestic(t)
	fakeChown(t)
	writeSecret(p.password(), "machine-pw")
	n := fakeSignalNotify(t)
	file := filepath.Join(t.TempDir(), "machine.isotope-backup")
	if _, err := daemonExport(context.Background(), func(string) {}, file, os.Getuid(), 0); err != nil {
		t.Fatal(err)
	}
	if !exists(file) {
		t.Fatal("no export")
	}
	repo := filepath.Join(t.TempDir(), "repo")
	os.MkdirAll(repo, 0o700)
	write(t, filepath.Join(repo, "config"), "x")
	daemonRestore(context.Background(), func(string) {}, restoreRequest{Source: repo, Password: "pw"})
	if got := n.Load(); got != 0 {
		t.Fatalf("the daemon asked for signals %d times", got)
	}
	cli := filepath.Join(t.TempDir(), "cli.isotope-backup")
	if code := backupExport(p, cli, false, false, bufio.NewReader(strings.NewReader("")), io.Discard, io.Discard); code != exitOK {
		t.Fatalf("CLI export: exit %d", code)
	}
	if got := n.Load(); got != 1 {
		t.Fatalf("the CLI asked for signals %d times, want 1", got)
	}
}

// TestDaemonExportCancelledWhilePackingLeavesNoFile: BackupCancel while
// the file is written removes the .partial and makes no file.
func TestDaemonExportCancelledWhilePackingLeavesNoFile(t *testing.T) {
	p := backupSetup(t)
	fakeRestic(t)
	writeSecret(p.password(), "machine-pw")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	old := fchownFile
	fchownFile = func(*os.File, int, int) error { cancel(); return nil }
	t.Cleanup(func() { fchownFile = old })
	file := filepath.Join(t.TempDir(), "machine.isotope-backup")
	if _, err := daemonExport(ctx, func(string) {}, file, os.Getuid(), 0); !errors.Is(err, context.Canceled) {
		t.Fatalf("%v, want cancelled", err)
	}
	if exists(file) || exists(file+".partial") {
		t.Fatal("the file or its .partial is left")
	}
	if e, _ := os.ReadDir(p.Run); len(e) != 0 {
		t.Fatalf("files left in %s: %v", p.Run, e)
	}
}

// TestWithTypedPasswordReportsAFailedWrite: when the typed form cannot be
// written to the password file, the error says so, not "wrong password".
func TestWithTypedPasswordReportsAFailedWrite(t *testing.T) {
	key, typed := typedLikeShown(t)
	dir := t.TempDir()
	passFile := filepath.Join(dir, "pw")
	if err := writeSecret(passFile, key); err != nil {
		t.Fatal(err)
	}
	// writeSecret writes passFile.tmp first: a folder there makes it fail.
	if err := os.Mkdir(passFile+".tmp", 0o700); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(passFile+".tmp", "x"), nil, 0o600)
	wrong := &exec.ExitError{}
	runs := 0
	stderr, err := withTypedPassword(passFile, typed, func() (string, error) {
		runs++
		return "Fatal: wrong password or no key found", wrong
	})
	if err == nil || !strings.Contains(err.Error(), "could not try the password as typed") || runs != 1 || stderr != "" {
		t.Fatalf("%v, stderr %q, after %d runs", err, stderr, runs)
	}
}

// TestFailedPasswordWriteIsWhatTheUserSees: through initTarget, a password
// file that cannot be rewritten is reported as such, not as restic's
// "wrong password".
func TestFailedPasswordWriteIsWhatTheUserSees(t *testing.T) {
	p := backupSetup(t)
	_, typed := typedLikeShown(t)
	dir := t.TempDir()
	bin := filepath.Join(dir, "restic")
	// It refuses the password and blocks the next write of the password
	// file: writeSecret writes path.tmp first, and a folder is there.
	write(t, bin, "#!/bin/sh\nmkdir -p \"$RESTIC_PASSWORD_FILE.tmp\" && touch \"$RESTIC_PASSWORD_FILE.tmp/x\"\n"+
		"echo 'Fatal: wrong password or no key found' >&2; exit 12\n")
	if err := os.Chmod(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ISOTOPE_RESTIC", bin)
	err := initTarget(p, "rest:https://nas:8000/isotope", initOptions{Existing: true}, typed, []string{})
	if err == nil || !strings.Contains(err.Error(), "could not try the password as typed") || strings.Contains(err.Error(), "wrong password") {
		t.Fatalf("the user sees %v", err)
	}
}
