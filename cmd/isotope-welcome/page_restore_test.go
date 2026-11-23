package main

import (
	"errors"
	"image"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"
)

// fakeBackup is the backup methods of isotope-updated. It records each
// call, with the secrets it got, as the daemon would read them from the
// pipe.
type fakeBackup struct {
	mu        sync.Mutex
	status    BackupState
	statusErr error
	sshKey    string
	hostKeys  []string
	snaps     []Snapshot
	listErr   error
	restErr   error
	jobs      []JobStatus // Status answers, in turn; the last one repeats
	jobErr    error       // Status fails: the daemon is gone
	calls     []string
	creds     []Credentials
	opts      []TargetOptions
}

func (f *fakeBackup) record(s string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, s)
}

func (f *fakeBackup) BackupStatus() (BackupState, error) {
	f.record("BackupStatus")
	return f.status, f.statusErr
}

func (f *fakeBackup) JobStatus() (JobStatus, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.jobErr != nil {
		return JobStatus{}, f.jobErr
	}
	if len(f.jobs) == 0 {
		return JobStatus{State: "restore"}, nil
	}
	j := f.jobs[0]
	if len(f.jobs) > 1 {
		f.jobs = f.jobs[1:]
	}
	return j, nil
}

func (f *fakeBackup) BackupSSHKey() (string, error) {
	f.record("BackupSSHKey")
	return f.sshKey, nil
}

func (f *fakeBackup) BackupHostKey(host string, port uint32) ([]string, error) {
	f.record("BackupHostKey " + host + " " + strconv.FormatUint(uint64(port), 10))
	return f.hostKeys, nil
}

func (f *fakeBackup) BackupList(source string, opt TargetOptions, cred Credentials) ([]Snapshot, error) {
	f.record("BackupList " + source)
	f.mu.Lock()
	f.creds, f.opts = append(f.creds, cred), append(f.opts, opt)
	f.mu.Unlock()
	return f.snaps, f.listErr
}

func (f *fakeBackup) Restore(source, snapshot, host string, opt TargetOptions, cred Credentials) error {
	f.record("Restore " + source + " " + snapshot + " " + host)
	f.mu.Lock()
	f.creds, f.opts = append(f.creds, cred), append(f.opts, opt)
	f.mu.Unlock()
	return f.restErr
}

func (f *fakeBackup) called() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

func (f *fakeBackup) lastCred() (Credentials, TargetOptions) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.creds[len(f.creds)-1], f.opts[len(f.opts)-1]
}

var usbDisk = USBDisk{UUID: "1234-ABCD", Label: "BACKUP", FSType: "exfat", Size: 64e9}

var twoSnaps = []Snapshot{
	{ID: "aaaa1111", Host: "anna-pc", Time: time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC).Unix()},
	{ID: "bbbb2222", Host: "anna-pc", Time: time.Date(2026, 11, 10, 10, 0, 0, 0, time.UTC).Unix()},
}

// welcomeWith is the full flow's Welcome page with the backup methods of
// b, for an administrator or not, after the status has loaded.
func welcomeWith(t *testing.T, b *fakeBackup, admin bool) (*UI, *recorder, *welcomePage) {
	t.Helper()
	u, r := newRecorded(NewFlow(Sway), Sway)
	u.hooks.Backup, u.hooks.Admin = b, admin
	u.hooks.USBDisks = func() ([]USBDisk, error) { return []USBDisk{usbDisk}, nil }
	p := u.page().(*welcomePage)
	p.start()
	p.wait()
	return u, r, p
}

var newMachine = BackupState{Available: true, CanRestore: true}

func TestRestoreIsOfferedOnlyToAnAdminOnANewMachine(t *testing.T) {
	for _, c := range []struct {
		name   string
		admin  bool
		status BackupState
		err    error
		want   bool
	}{
		{"admin, new machine", true, newMachine, nil, true},
		{"not an admin", false, newMachine, nil, false},
		{"machine in use", true, BackupState{Available: true, RestoreRefusal: "there is more than one user"}, nil, false},
		{"no restic", true, BackupState{CanRestore: true}, nil, false},
		{"no daemon", true, BackupState{}, errors.New("not on the bus"), false},
	} {
		b := &fakeBackup{status: c.status, statusErr: c.err}
		_, _, p := welcomeWith(t, b, c.admin)
		if got := p.restoreOffered(); got != c.want {
			t.Errorf("%s: offered %v, want %v", c.name, got, c.want)
		}
		if !c.admin && len(b.called()) != 0 {
			t.Errorf("%s: asked the daemon %v", c.name, b.called())
		}
	}
}

// A page opened alone (isotope welcome PAGE) offers no restore.
func TestRestoreIsNotOfferedInASinglePage(t *testing.T) {
	u, _ := newRecorded(Single(PWelcome), Sway)
	b := &fakeBackup{status: newMachine}
	u.hooks.Backup, u.hooks.Admin = b, true
	p := u.page().(*welcomePage)
	p.start()
	p.wait()
	if p.restoreOffered() {
		t.Fatal("offered in a single page")
	}
}

// Choosing Restore turns the steps into the restore's; "Set up as new"
// turns them back.
func TestChoosingRestoreChangesTheSteps(t *testing.T) {
	u, _, p := welcomeWith(t, &fakeBackup{status: newMachine}, true)
	p.choose(true)
	if got := u.flow.Pages(); !slices.Equal(got, []PageID{PWelcome, PRestoreWhere, PRestorePassword, PRestoreBackup, PRestoreRun}) {
		t.Fatalf("pages %v", got)
	}
	u.next()
	if u.flow.Current() != PRestoreWhere {
		t.Fatalf("on %s", u.flow.Current())
	}
	u.prev()
	p.choose(false)
	if u.flow.Current() != PWelcome || len(u.flow.Pages()) != len(Pages(Sway)) {
		t.Fatalf("on %s of %v", u.flow.Current(), u.flow.Pages())
	}
	u.next()
	if u.flow.Current() != PLanguage {
		t.Fatalf("on %s", u.flow.Current())
	}
}

// restoreAt goes through Welcome to the Where step.
func restoreAt(t *testing.T, b *fakeBackup) (*UI, *recorder, *restoreState) {
	t.Helper()
	u, r, p := welcomeWith(t, b, true)
	p.choose(true)
	u.next()
	rs := u.restoreState()
	rs.poll = time.Millisecond
	rs.wait()
	return u, r, rs
}

// passwordStep types the password and lists the backups.
func passwordStep(t *testing.T, u *UI, rs *restoreState, pw string) {
	t.Helper()
	if u.flow.Current() != PRestorePassword {
		t.Fatalf("on %s, not the password step", u.flow.Current())
	}
	rs.pw.Editor.SetText(pw)
	u.next()
	rs.wait()
	rs.settle()
}

func TestRestoreFromAUSBDisk(t *testing.T) {
	b := &fakeBackup{status: newMachine, snaps: twoSnaps, jobs: []JobStatus{{State: "restore"}, {State: "idle", Result: "ok"}}}
	u, r, rs := restoreAt(t, b)
	if disks := rs.disksNow(); len(disks) != 1 || disks[0] != usbDisk {
		t.Fatalf("disks %v", disks)
	}
	u.next() // the first disk, folder isotope-backup
	passwordStep(t, u, rs, "abcdef-ghijkl")
	if rs.pw.Editor.Text() != "" {
		t.Fatal("the password field still holds the password")
	}
	if got := b.called(); !slices.Contains(got, "BackupList uuid:1234-ABCD/isotope-backup") {
		t.Fatalf("calls %v", got)
	}
	if c, _ := b.lastCred(); c.Password != "abcdef-ghijkl" || len(c.Env) != 0 {
		t.Fatalf("cred %+v", c)
	}
	if u.flow.Current() != PRestoreBackup {
		t.Fatalf("on %s", u.flow.Current())
	}
	if rows := rs.snapshotRows(); len(rows) != 2 || rs.chosenSnapshot().ID != "aaaa1111" {
		t.Fatalf("rows %v, chosen %+v", rows, rs.chosenSnapshot())
	}
	rs.chooseSnapshot(1)

	// Continue asks first; Cancel has the focus.
	u.next()
	if u.question == nil || u.focus != &rs.cancel {
		t.Fatalf("question %v, focus %v", u.question, u.focus)
	}
	if !strings.Contains(u.question.body, "restarts") {
		t.Fatalf("body %q", u.question.body)
	}
	rs.confirm()
	rs.wait()
	if got := b.called(); !slices.Contains(got, "Restore uuid:1234-ABCD/isotope-backup bbbb2222 anna-pc") {
		t.Fatalf("calls %v", got)
	}
	if c, _ := b.lastCred(); c.Password != "abcdef-ghijkl" {
		t.Fatalf("cred %+v", c)
	}
	if rs.holdsSecrets() {
		t.Fatal("the secrets are kept after the restore started")
	}
	if u.question != nil || u.flow.Current() != PRestoreRun {
		t.Fatalf("question %v, on %s", u.question, u.flow.Current())
	}
	rs.waitJob()
	if v, _ := rs.runView(); v != runRestarting {
		t.Fatalf("view %v", v)
	}
	// The flow ends with the restart: a close writes no stamp (the restored
	// homes have their own), so a failed restore leaves the machine new.
	r.requestClose()
	if !r.closed || len(r.stamps) != 0 {
		t.Fatalf("closed %v, stamps %v", r.closed, r.stamps)
	}
}

func TestRestoreFromARESTServer(t *testing.T) {
	b := &fakeBackup{status: newMachine, snaps: twoSnaps, jobs: []JobStatus{{State: "idle", Result: "ok"}}}
	u, _, rs := restoreAt(t, b)
	rs.chooseWhere(whereRemote)
	rs.chooseRemote(remoteREST)
	rs.restURL.Editor.SetText("https://nas:8000/anna/")
	rs.restUser.Editor.SetText("anna")
	rs.restPw.Editor.SetText("rest-pw")
	u.next()
	if rs.restPw.Editor.Text() != "" {
		t.Fatal("the server password is still in its field")
	}
	passwordStep(t, u, rs, "the-backup-pw")
	c, _ := b.lastCred()
	if c.Password != "the-backup-pw" || !slices.Equal(c.Env, []string{"RESTIC_REST_USERNAME=anna", "RESTIC_REST_PASSWORD=rest-pw"}) {
		t.Fatalf("cred %+v", c)
	}
	if got := b.called(); !slices.Contains(got, "BackupList rest:https://nas:8000/anna/") {
		t.Fatalf("calls %v", got)
	}
	u.next()
	rs.confirm()
	rs.wait()
	if c, _ := b.lastCred(); !slices.Equal(c.Env, []string{"RESTIC_REST_USERNAME=anna", "RESTIC_REST_PASSWORD=rest-pw"}) {
		t.Fatalf("Restore cred %+v", c)
	}
	rs.waitJob()
}

// SFTP: the backup's key is shown, every server key is listed with its
// type, and the one the user picks goes as the bare fingerprint.
func TestRestoreFromSFTPPicksTheHostKey(t *testing.T) {
	b := &fakeBackup{status: newMachine, snaps: twoSnaps, sshKey: "ssh-ed25519 AAAA isotope-backup",
		hostKeys: []string{"ED25519 SHA256:eee", "RSA SHA256:rrr"}}
	u, _, rs := restoreAt(t, b)
	rs.chooseWhere(whereRemote)
	rs.chooseRemote(remoteSFTP)
	rs.wait()
	if rs.publicKey() != "ssh-ed25519 AAAA isotope-backup" {
		t.Fatalf("key %q", rs.publicKey())
	}
	rs.sftpUserHost.Editor.SetText("anna@nas.local")
	rs.sftpPort.Editor.SetText("2222")
	rs.sftpPath.Editor.SetText("/srv/backup")
	if _, problem := rs.formTarget(); problem == "" {
		t.Fatal("a target without a confirmed key is ready")
	}
	rs.connect()
	rs.wait()
	if got := b.called(); !slices.Contains(got, "BackupHostKey nas.local 2222") {
		t.Fatalf("calls %v", got)
	}
	if rows := rs.hostKeyRows(); !slices.Equal(rows, []string{"ED25519 SHA256:eee", "RSA SHA256:rrr"}) {
		t.Fatalf("rows %v", rows)
	}
	rs.chooseHostKey(1)
	rs.acceptHostKey()
	u.next()
	passwordStep(t, u, rs, "pw")
	if _, o := b.lastCred(); o.HostKey != "SHA256:rrr" || o.Port != 2222 {
		t.Fatalf("options %+v", o)
	}
	// A changed server needs its key confirmed again.
	u.prev()
	u.prev()
	rs.sftpUserHost.Editor.SetText("anna@other")
	if _, problem := rs.formTarget(); problem == "" {
		t.Fatal("the key of another server counts")
	}
}

// The daemon's refusal is shown as it comes, and Go back leads to Where:
// the secrets went with the call, so the steps ask them again.
func TestRestoreRefusalIsShownAsItComes(t *testing.T) {
	refusal := "This machine is already in use; restore parts of a backup with isotope restore --only"
	b := &fakeBackup{status: newMachine, snaps: twoSnaps, restErr: errors.New(refusal)}
	u, r, rs := restoreAt(t, b)
	u.next()
	passwordStep(t, u, rs, "pw")
	u.next()
	rs.confirm()
	rs.wait()
	if v, msg := rs.runView(); v != runRefused || msg != refusal {
		t.Fatalf("view %v, %q", v, msg)
	}
	if rs.holdsSecrets() {
		t.Fatal("the secrets are kept after a refusal")
	}
	u.prev()
	if u.flow.Current() != PRestoreWhere {
		t.Fatalf("on %s", u.flow.Current())
	}
	// Not started: closing is the usual skip.
	r.requestClose()
	if r.closed {
		t.Fatal("closed without asking")
	}
}

func TestRestoreJobFailureIsShown(t *testing.T) {
	b := &fakeBackup{status: newMachine, snaps: twoSnaps,
		jobs: []JobStatus{{State: "restore"}, {State: "idle", Result: "failed", Detail: "the disk is set up differently"}}}
	u, r, rs := restoreAt(t, b)
	u.next()
	passwordStep(t, u, rs, "pw")
	u.next()
	rs.confirm()
	rs.wait()
	rs.waitJob()
	if v, msg := rs.runView(); v != runFailed || msg != "the disk is set up differently" {
		t.Fatalf("view %v, %q", v, msg)
	}
	// The daemon's job also fails after it moved the files and asked for
	// the restart: the step never says nothing changed, and offers no Go
	// back.
	title, sub := runTexts(runFailed)
	if title != "The restore failed" || strings.Contains(sub, "stays as it was") || !strings.Contains(sub, "may restart") {
		t.Fatalf("%q, %q", title, sub)
	}
	if lead, _ := u.page().Footer(u.nav()); len(lead) != 0 {
		t.Fatalf("footer %v", lead)
	}
	u.prev()
	if u.flow.Current() != PRestoreRun {
		t.Fatalf("on %s", u.flow.Current())
	}
	// Stamping stays off: the machine may have changed.
	r.requestClose()
	if !r.closed || len(r.stamps) != 0 {
		t.Fatalf("closed %v, stamps %v", r.closed, r.stamps)
	}
}

// The daemon gone while the restore runs is the restart.
func TestRestoreDaemonGoneIsTheRestart(t *testing.T) {
	b := &fakeBackup{status: newMachine, snaps: twoSnaps, jobErr: errors.New("org.freedesktop.DBus.Error.ServiceUnknown")}
	u, _, rs := restoreAt(t, b)
	u.next()
	passwordStep(t, u, rs, "pw")
	u.next()
	rs.confirm()
	rs.wait()
	rs.waitJob()
	if v, _ := rs.runView(); v != runRestarting {
		t.Fatalf("view %v", v)
	}
}

func TestRestoreListErrorStaysOnThePassword(t *testing.T) {
	b := &fakeBackup{status: newMachine, listErr: errors.New("could not read the backup (wrong password?): Fatal: wrong password")}
	u, _, rs := restoreAt(t, b)
	u.next()
	passwordStep(t, u, rs, "bad")
	if u.flow.Current() != PRestorePassword || rs.listError() != "could not read the backup (wrong password?): Fatal: wrong password" {
		t.Fatalf("on %s, err %q", u.flow.Current(), rs.listError())
	}
	if rs.pw.Editor.Text() != "" || rs.holdsSecrets() {
		t.Fatal("the password is kept after a failed list")
	}
}

func TestRestoreNoBackupThere(t *testing.T) {
	b := &fakeBackup{status: newMachine}
	u, _, rs := restoreAt(t, b)
	u.next()
	passwordStep(t, u, rs, "pw")
	if u.flow.Current() != PRestorePassword || rs.listError() == "" {
		t.Fatalf("on %s, err %q", u.flow.Current(), rs.listError())
	}
}

// Cancel on the question restores nothing and keeps the choice.
func TestRestoreCancelAsksNothing(t *testing.T) {
	b := &fakeBackup{status: newMachine, snaps: twoSnaps}
	u, _, rs := restoreAt(t, b)
	u.next()
	passwordStep(t, u, rs, "pw")
	u.next()
	u.prev() // Esc is Cancel
	if u.question != nil || u.flow.Current() != PRestoreBackup {
		t.Fatalf("question %v, on %s", u.question, u.flow.Current())
	}
	for _, c := range b.called() {
		if strings.HasPrefix(c, "Restore ") {
			t.Fatalf("restored: %v", b.called())
		}
	}
}

// Leaving the restore clears every secret: the password, the server's,
// and what the steps kept.
func TestLeavingTheRestoreClearsTheSecrets(t *testing.T) {
	b := &fakeBackup{status: newMachine, snaps: twoSnaps}
	u, _, rs := restoreAt(t, b)
	u.next()
	passwordStep(t, u, rs, "pw")
	if !rs.holdsSecrets() {
		t.Fatal("nothing kept for the restore")
	}
	u.prev() // to the password
	if rs.holdsSecrets() {
		t.Fatal("the password is kept on Go back")
	}
	u.prev() // to Where
	rs.chooseWhere(whereRemote)
	rs.chooseRemote(remoteS3)
	rs.s3Secret.Editor.SetText("s3-secret")
	u.prev() // to Welcome
	if rs.s3Secret.Editor.Text() != "" || rs.holdsSecrets() {
		t.Fatal("a secret is kept after leaving the restore")
	}
}

// A disabled Continue does nothing: no target, no password.
func TestRestoreContinueNeedsWhatIsMissing(t *testing.T) {
	b := &fakeBackup{status: newMachine}
	u, _, rs := restoreAt(t, b)
	rs.chooseWhere(whereRemote)
	rs.chooseRemote(remoteREST)
	u.next()
	if u.flow.Current() != PRestoreWhere {
		t.Fatalf("on %s", u.flow.Current())
	}
	rs.chooseWhere(whereUSB)
	u.next()
	u.next() // an empty password
	if u.flow.Current() != PRestorePassword || len(b.called()) != 1 {
		t.Fatalf("on %s, calls %v", u.flow.Current(), b.called())
	}
}

// frame draws one frame of the whole window.
func frame(u *UI) {
	var ops op.Ops
	u.Layout(layout.Context{Ops: &ops, Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1},
		Constraints: layout.Exact(image.Pt(1100, 720)), Now: time.Now()})
}

// Every face of every step draws: the Welcome page with both choices, each
// kind of target, the server's keys, the backups, the question, and each
// end of the restore.
func TestRestoreStepsDraw(t *testing.T) {
	b := &fakeBackup{status: newMachine, snaps: twoSnaps, sshKey: "ssh-ed25519 AAAA isotope-backup",
		hostKeys: []string{"ED25519 SHA256:eee", "RSA SHA256:rrr"}, jobs: []JobStatus{{State: "idle", Result: "ok"}}}
	u, _, rs := restoreAt(t, b)
	frame(u)
	u.flow.Back()
	frame(u) // Welcome, with Restore chosen
	u.flow.Next()
	for _, kind := range []string{remoteREST, remoteS3, remoteSFTP} {
		rs.chooseRemote(kind)
		rs.wait()
		frame(u)
	}
	rs.sftpUserHost.Editor.SetText("anna@nas.local")
	rs.sftpPath.Editor.SetText("/srv/backup")
	rs.connect()
	rs.wait()
	frame(u)
	rs.acceptHostKey()
	frame(u)
	u.next()
	frame(u)
	passwordStep(t, u, rs, "pw")
	frame(u)
	u.next()
	frame(u) // the question
	rs.confirm()
	frame(u)
	rs.wait()
	frame(u)
	rs.waitJob()
	frame(u)
	for _, v := range []runView{runRefused, runFailed, runRunning, runStarting} {
		rs.mu.Lock()
		rs.run, rs.runMsg = v, "the daemon's words"
		rs.mu.Unlock()
		frame(u)
	}
}
