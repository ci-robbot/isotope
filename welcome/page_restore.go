package main

// "Restore from a backup": the restore steps of the Welcome design (§1
// Welcome, "Restore steps"). Where the backup is (a USB disk, or a REST,
// S3 or SFTP server, the same fields as System's Backup page), its
// password, which backup, a confirmation, then Restore, which ends in the
// restart. The daemon decides everything again: the machine-is-new rule,
// polkit, the password. Secrets reach it only through a pipe fd, and the
// steps drop them after each submit, on Go back and when the restore is
// left.

import (
	"errors"
	"io"
	"strings"
	"sync"
	"time"

	"gioui.org/io/clipboard"
	"gioui.org/io/key"
	"gioui.org/layout"
	"gioui.org/widget"

	"github.com/kairos-community/isotope/guikit"
)

func init() {
	pageMakers[PRestoreWhere] = func(u *UI) page { return &restoreWherePage{restorePage: restorePage{u.restoreState()}} }
	pageMakers[PRestorePassword] = func(u *UI) page { return &restorePasswordPage{restorePage: restorePage{u.restoreState()}} }
	pageMakers[PRestoreBackup] = func(u *UI) page { return &restoreBackupPage{restorePage: restorePage{u.restoreState()}} }
	pageMakers[PRestoreRun] = func(u *UI) page { return &restoreRunPage{restorePage: restorePage{u.restoreState()}} }
}

// runView is what the last step shows.
type runView int

const (
	runNone       runView = iota
	runStarting           // Restore was called
	runRefused            // the daemon refused to start it
	runRunning            // the job runs
	runFailed             // the job failed; the machine stays as it was
	runRestarting         // the job ended well; the machine restarts
)

// jobPoll is how often the last step asks how the restore goes.
const jobPoll = 2 * time.Second

// setupTarget is a target the Where step describes.
type setupTarget struct {
	target, name string
	opt          TargetOptions
	env          []string // NAME=value credentials
}

// restoreState is the state of the restore steps, shared by their pages.
// The background calls write under mu.
type restoreState struct {
	u     *UI
	mu    sync.Mutex
	wg    sync.WaitGroup // the short calls
	jobWG sync.WaitGroup // the job's watch

	once bool          // start ran
	poll time.Duration // jobPoll; shorter in the tests

	// Where.
	where, remote, disk string // disk is a UUID, "" for the first
	disks               []USBDisk
	disksErr            string
	loadingDisks        bool

	folder                                                  *guikit.Field
	restURL, restUser, restPw                               *guikit.Field
	s3Endpoint, s3Bucket, s3Path, s3Key, s3Secret, s3Region *guikit.Field
	sftpUserHost, sftpPort, sftpPath                        *guikit.Field

	usbClick, remoteClick, rescan, copyClick, connectClick, acceptClick widget.Clickable
	remoteClicks                                                        [3]widget.Clickable
	diskClicks                                                          map[string]*widget.Clickable

	sshKey, sshKeyErr  string
	loadingKey, copied bool
	connecting         bool
	connectErr         string
	scanned            []hostKey // the keys of the server asked last, ED25519 first
	scannedChosen      int
	scannedOf          string // their "host:port"
	scannedClicks      []widget.Clickable
	hostKey, hostKeyOf string // the confirmed fingerprint, and its "host:port"

	// Kept between the steps; dropped on Go back, when the restore is
	// left, and when Restore is called.
	target *setupTarget
	cred   *Credentials

	// Password.
	pw        *guikit.Field
	pwFocused bool
	listing   bool
	listErr   string
	listed    bool // BackupList answered: settle goes on

	// Backup, and the question.
	snaps      []Snapshot
	chosen     int
	snapClicks []widget.Clickable
	cancel     widget.Clickable
	yes        widget.Clickable

	// The restore.
	run     runView
	runMsg  string
	started bool // the daemon took the restore
}

// restoreState is the restore's state, made on first use.
func (u *UI) restoreState() *restoreState {
	if u.restore == nil {
		f := guikit.NewField
		u.restore = &restoreState{u: u, where: whereUSB, remote: remoteREST, diskClicks: map[string]*widget.Clickable{},
			folder: f(false), restURL: f(false), restUser: f(false), restPw: guikit.NewSecret(),
			s3Endpoint: f(false), s3Bucket: f(false), s3Path: f(false), s3Key: f(false), s3Secret: guikit.NewSecret(), s3Region: f(false),
			sftpUserHost: f(false), sftpPort: f(false), sftpPath: f(false), pw: guikit.NewSecret(), poll: jobPoll}
		u.restore.folder.Editor.SetText(defaultFolder)
		u.restore.sftpPort.Editor.SetText("22")
	}
	return u.restore
}

// restoreStarted: the daemon took a restore, which ends in the restart.
func (u *UI) restoreStarted() bool {
	r := u.restore
	if r == nil {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.started
}

// start lists the USB disks, once.
func (r *restoreState) start() {
	if r.once {
		return
	}
	r.once = true
	r.loadDisks()
}

func (r *restoreState) invalidate() {
	if r.u.hooks.Invalidate != nil {
		r.u.hooks.Invalidate()
	}
}

// wait blocks until the short background calls have ended (tests).
func (r *restoreState) wait() { r.wg.Wait() }

// waitJob blocks until the watch of the restore has ended (tests).
func (r *restoreState) waitJob() { r.jobWG.Wait() }

// errText is the daemon's error as the user reads it: its own words.
func errText(err error) string {
	if errors.Is(err, ErrNotAuthorized) {
		return "Only members of the admin group can do this."
	}
	return err.Error()
}

// ---- secrets ----

// clearFields empties the fields that hold a secret.
func (r *restoreState) clearFields() {
	for _, f := range []*guikit.Field{r.pw, r.restPw, r.s3Secret} {
		f.Editor.SetText("")
		f.Err = ""
	}
}

// dropKept forgets what the steps kept: the target with its credentials,
// the password, the backups.
func (r *restoreState) dropKept() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.target, r.cred = nil, nil
	r.snaps, r.listed, r.listErr = nil, false, ""
}

// leave is the restore left (Go back to Welcome, "Set up as new"): every
// secret is dropped.
func (r *restoreState) leave() {
	r.clearFields()
	r.dropKept()
}

// holdsSecrets: a secret is kept, in a field or between the steps (tests).
func (r *restoreState) holdsSecrets() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cred != nil || (r.target != nil && len(r.target.env) > 0) {
		return true
	}
	return r.pw.Editor.Text() != "" || r.restPw.Editor.Text() != "" || r.s3Secret.Editor.Text() != ""
}

// ---- Where ----

func (r *restoreState) loadDisks() {
	list := r.u.hooks.USBDisks
	if list == nil {
		return
	}
	r.mu.Lock()
	if r.loadingDisks {
		r.mu.Unlock()
		return
	}
	r.loadingDisks = true
	r.mu.Unlock()
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		disks, err := list()
		r.mu.Lock()
		r.loadingDisks = false
		r.disks, r.disksErr = disks, ""
		if err != nil {
			r.disksErr = "USB disks cannot be listed: " + err.Error()
		}
		r.mu.Unlock()
		r.invalidate()
	}()
}

func (r *restoreState) disksNow() []USBDisk {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]USBDisk(nil), r.disks...)
}

// pickDisk is the disk with the UUID id; id "" is the first. A chosen disk
// that is gone is not replaced by another.
func pickDisk(disks []USBDisk, id string) (USBDisk, bool) {
	for _, d := range disks {
		if d.UUID == id {
			return d, true
		}
	}
	if id == "" && len(disks) > 0 {
		return disks[0], true
	}
	return USBDisk{}, false
}

func diskName(d USBDisk) string {
	if d.Label != "" {
		return d.Label
	}
	return "USB disk " + d.UUID
}

func (r *restoreState) chooseWhere(w string) {
	r.where = w
	if w == whereRemote && r.remote == remoteSFTP {
		r.loadSSHKey()
	}
}

func (r *restoreState) chooseRemote(kind string) {
	r.where, r.remote = whereRemote, kind
	if kind == remoteSFTP {
		r.loadSSHKey()
	}
}

// formTarget is the target the Where step describes, or what is missing.
func (r *restoreState) formTarget() (setupTarget, string) {
	if r.where == whereUSB {
		d, ok := pickDisk(r.disksNow(), r.disk)
		if !ok {
			return setupTarget{}, "Plug in the USB disk with the backup, and choose it."
		}
		t, err := usbTarget(d, r.folder.Editor.Text())
		if err != nil {
			return setupTarget{}, err.Error()
		}
		return setupTarget{target: t, name: diskName(d) + " (USB disk)"}, ""
	}
	var t setupTarget
	var err error
	switch r.remote {
	case remoteREST:
		t.target, t.env, err = restTarget(r.restURL.Editor.Text(), r.restUser.Editor.Text(), r.restPw.Editor.Text())
	case remoteS3:
		t.target, t.env, err = s3Target(r.s3Endpoint.Editor.Text(), r.s3Bucket.Editor.Text(), r.s3Path.Editor.Text(),
			r.s3Key.Editor.Text(), r.s3Secret.Editor.Text(), r.s3Region.Editor.Text())
	case remoteSFTP:
		var host string
		var port uint32
		t.target, host, port, err = sftpTarget(r.sftpUserHost.Editor.Text(), r.sftpPort.Editor.Text(), r.sftpPath.Editor.Text())
		if err == nil {
			r.mu.Lock()
			fp, of := r.hostKey, r.hostKeyOf
			r.mu.Unlock()
			if fp == "" || of != hostPort(host, port) {
				return setupTarget{}, "Connect to the server and confirm its key."
			}
			t.opt = TargetOptions{HostKey: fp, Port: port}
		}
	default:
		err = errors.New("Choose the kind of server.")
	}
	if err != nil {
		return setupTarget{}, err.Error()
	}
	t.name = t.target
	return t, ""
}

// loadSSHKey reads the backup's public SSH key, which the SFTP server must
// have in authorized_keys.
func (r *restoreState) loadSSHKey() {
	b := r.u.hooks.Backup
	if b == nil {
		return
	}
	r.mu.Lock()
	if r.sshKey != "" || r.loadingKey {
		r.mu.Unlock()
		return
	}
	r.loadingKey = true
	r.mu.Unlock()
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		key, err := b.BackupSSHKey()
		r.mu.Lock()
		r.loadingKey = false
		r.sshKey, r.sshKeyErr = key, ""
		if err != nil {
			r.sshKeyErr = "Could not read the backup's SSH key: " + errText(err)
		}
		r.mu.Unlock()
		r.invalidate()
	}()
}

func (r *restoreState) publicKey() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.sshKey
}

// connect fetches the SFTP server's keys, for the user to pick the one
// they compared.
func (r *restoreState) connect() {
	b := r.u.hooks.Backup
	if b == nil {
		return
	}
	_, host, port, err := sftpTarget(r.sftpUserHost.Editor.Text(), r.sftpPort.Editor.Text(), r.sftpPath.Editor.Text())
	r.mu.Lock()
	if r.connecting {
		r.mu.Unlock()
		return
	}
	if err != nil {
		r.connectErr = err.Error()
		r.mu.Unlock()
		return
	}
	r.connecting, r.connectErr, r.scanned = true, "", nil
	r.mu.Unlock()
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		fps, err := b.BackupHostKey(host, port)
		keys := hostKeyChoices(fps)
		r.mu.Lock()
		defer r.invalidate()
		defer r.mu.Unlock()
		r.connecting = false
		switch {
		case err != nil:
			r.connectErr = "Could not reach " + host + ": " + errText(err)
		case len(keys) == 0:
			r.connectErr = "Could not reach " + host + ": the server sent no key"
		default:
			r.scanned, r.scannedChosen, r.scannedOf = keys, 0, hostPort(host, port)
			r.scannedClicks = make([]widget.Clickable, len(keys))
		}
	}()
}

// hostKeyRows are the server's keys as the step lists them (tests).
func (r *restoreState) hostKeyRows() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var rows []string
	for _, k := range r.scanned {
		rows = append(rows, k.typ+" "+k.fingerprint)
	}
	return rows
}

func (r *restoreState) chooseHostKey(i int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if i >= 0 && i < len(r.scanned) {
		r.scannedChosen = i
	}
}

// acceptHostKey is "This is the server's key": the chosen key, as the bare
// SHA256: fingerprint, goes as the host_key option.
func (r *restoreState) acceptHostKey() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if i := r.scannedChosen; i >= 0 && i < len(r.scanned) {
		r.hostKey, r.hostKeyOf = r.scanned[i].fingerprint, r.scannedOf
	}
	r.scanned = nil
}

// ---- Password ----

func pwOK(pw string) bool { return pw != "" && !strings.ContainsAny(pw, "\r\n\x00") }

// list sends the password with the target's credentials to BackupList, and
// clears the field.
func (r *restoreState) list() {
	b := r.u.hooks.Backup
	pw := r.pw.Editor.Text()
	r.mu.Lock()
	if b == nil || r.target == nil || r.listing || !pwOK(pw) {
		r.mu.Unlock()
		return
	}
	t := *r.target
	cred := Credentials{Password: pw, Env: t.env}
	r.listing, r.listErr = true, ""
	r.mu.Unlock()
	r.pw.Editor.SetText("")
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		snaps, err := b.BackupList(t.target, t.opt, cred)
		r.mu.Lock()
		defer r.invalidate()
		defer r.mu.Unlock()
		r.listing = false
		switch {
		case err != nil:
			r.listErr = errText(err)
		case len(snaps) == 0:
			r.listErr = t.name + " holds no backup."
		default:
			r.snaps, r.chosen = byHost(snaps), 0
			r.snapClicks = make([]widget.Clickable, len(snaps))
			r.cred, r.listed = &cred, true
		}
	}()
}

func (r *restoreState) listError() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.listErr
}

// settle goes on to the backups when BackupList has answered.
func (r *restoreState) settle() {
	r.mu.Lock()
	go1 := r.listed
	r.listed = false
	r.mu.Unlock()
	if go1 && r.u.flow.Current() == PRestorePassword {
		r.u.flow.Next()
	}
}

// ---- Backup ----

func (r *restoreState) snapshotRows() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var rows []string
	for _, s := range r.snaps {
		rows = append(rows, snapshotTitle(s, r.u.now()))
	}
	return rows
}

func snapshotTitle(s Snapshot, now time.Time) string {
	t := ago(now, time.Unix(s.Time, 0))
	if s.Host != "" {
		t += " · " + s.Host
	}
	return t
}

func (r *restoreState) chooseSnapshot(i int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if i >= 0 && i < len(r.snaps) {
		r.chosen = i
	}
}

func (r *restoreState) chosenSnapshot() Snapshot {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.chosen < 0 || r.chosen >= len(r.snaps) {
		return Snapshot{}
	}
	return r.snaps[r.chosen]
}

// restoreBody is the confirmation: what comes back, and the restart.
func restoreBody(s Snapshot, when string) string {
	from := "The backup of " + when
	if s.Host != "" {
		from += " from " + s.Host
	}
	return from + " brings this whole machine back as it was: the users and their passwords, the hostname, " +
		"every home, the SSH keys, the extensions, the settings and the Flatpak apps (installed again after the restart).\n" +
		"What is on this machine now is replaced. The machine restarts at the end."
}

// askConfirm is the last question. Cancel has the focus.
func (r *restoreState) askConfirm() {
	u := r.u
	r.mu.Lock()
	ok := r.cred != nil && r.target != nil && r.chosen >= 0 && r.chosen < len(r.snaps)
	r.mu.Unlock()
	if !ok {
		return
	}
	s := r.chosenSnapshot()
	u.ask(&question{
		title: "Restore this machine?",
		body:  restoreBody(s, ago(u.now(), time.Unix(s.Time, 0))),
		actions: []guikit.Action{
			{Click: &r.cancel, Kind: guikit.ButtonSecondary, Label: "Cancel"},
			{Click: &r.yes, Kind: guikit.ButtonDanger, Label: "Restore and restart"},
		},
		update: func(gtx layout.Context) {
			if r.cancel.Clicked(gtx) {
				r.cancelAsk()
			}
			if r.yes.Clicked(gtx) {
				r.confirm()
			}
		},
		esc: r.cancelAsk,
	}, &r.cancel)
}

// cancelAsk closes the question: nothing is restored, the choice stays.
func (r *restoreState) cancelAsk() {
	r.u.question = nil
	r.u.focus = &r.u.cont
}

// confirm calls Restore. The secrets go with the call and are not kept.
func (r *restoreState) confirm() {
	u, b := r.u, r.u.hooks.Backup
	u.question = nil
	r.mu.Lock()
	if b == nil || r.cred == nil || r.target == nil || r.chosen < 0 || r.chosen >= len(r.snaps) {
		r.mu.Unlock()
		return
	}
	cred, t, s := *r.cred, *r.target, r.snaps[r.chosen]
	r.cred = nil
	r.target = &setupTarget{target: t.target, name: t.name, opt: t.opt}
	r.run, r.runMsg = runStarting, ""
	r.mu.Unlock()
	u.flow.Next()
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		err := b.Restore(t.target, s.ID, s.Host, t.opt, cred)
		r.mu.Lock()
		if err != nil {
			r.run, r.runMsg = runRefused, errText(err)
		} else {
			r.run, r.started = runRunning, true
			r.jobWG.Add(1)
			go r.watch(b, r.poll)
		}
		r.mu.Unlock()
		r.invalidate()
	}()
}

// watch asks how the restore goes until it ends. An error is the daemon
// gone for the restart: the restore started, so nothing else stops it.
//
// A failed job does not tell whether the daemon had already moved files:
// it also fails after it put /usr/local and /oem back and asked for the
// restart ("some parts were not restored"). So every failure counts as
// "the machine may have changed": started stays true (no stamp, no Go
// back).
func (r *restoreState) watch(b Backup, every time.Duration) {
	defer r.jobWG.Done()
	for {
		time.Sleep(every)
		js, err := b.JobStatus()
		if err == nil && js.State == "restore" {
			continue
		}
		r.mu.Lock()
		switch {
		case err == nil && js.Result == "failed":
			r.run, r.runMsg = runFailed, js.Detail
			if r.runMsg == "" {
				r.runMsg = "The restore failed."
			}
		default:
			r.run = runRestarting
		}
		r.mu.Unlock()
		r.invalidate()
		return
	}
}

func (r *restoreState) runView() (runView, string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.run, r.runMsg
}

// ---- the pages ----

// restorePage is the base of the restore steps: Go back and Continue, no
// Skip.
type restorePage struct{ r *restoreState }

func (p restorePage) footer(n nav, label string, disabled bool) (leading, trailing []guikit.Action) {
	c := *n.Continue
	c.Label, c.Disabled = label, disabled
	return []guikit.Action{*n.Back}, []guikit.Action{c}
}

// column caps the width of a step's content.
func column(w layout.Widget) layout.Widget {
	return func(gtx layout.Context) layout.Dimensions {
		gtx.Constraints.Max.X = min(gtx.Constraints.Max.X, guikit.Dp(gtx, 620))
		gtx.Constraints.Min.X = gtx.Constraints.Max.X
		return w(gtx)
	}
}

// field is a labelled input.
func (r *restoreState) field(title string, f *guikit.Field, hint string) layout.FlexChild {
	u := r.u
	return layout.Rigid(func(gtx layout.Context) layout.Dimensions {
		return layout.Inset{Bottom: 12}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			return u.Labelled(gtx, title, f, hint, func(gtx layout.Context) layout.Dimensions {
				return u.Input(gtx, f, guikit.StBody, 38, "", nil)
			})
		})
	})
}

func (r *restoreState) text(s string) layout.FlexChild {
	u := r.u
	return layout.Rigid(func(gtx layout.Context) layout.Dimensions {
		return u.LabelLines(gtx, guikit.StBody, guikit.ColText2, 1.45, s)
	})
}

func (r *restoreState) dim(s string) layout.FlexChild {
	u := r.u
	return layout.Rigid(func(gtx layout.Context) layout.Dimensions {
		return u.LabelLines(gtx, guikit.StSmall, guikit.ColDim, 1.4, s)
	})
}

func (r *restoreState) warning(s string) layout.FlexChild {
	u := r.u
	return layout.Rigid(func(gtx layout.Context) layout.Dimensions { return u.Warning(gtx, s) })
}

func (r *restoreState) button(c *widget.Clickable, label string, enabled bool) layout.FlexChild {
	u := r.u
	return layout.Rigid(func(gtx layout.Context) layout.Dimensions {
		return u.Button(gtx, c, guikit.ButtonSecondary, label, enabled)
	})
}

// step lays out a step: its heading, then kids, in the page's scroll.
func (r *restoreState) step(gtx layout.Context, l *widget.List, title, sub string, kids ...layout.FlexChild) layout.Dimensions {
	u := r.u
	return u.scrolled(gtx, l, func(gtx layout.Context) layout.Dimensions {
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
			layout.Rigid(u.Heading(title, sub)),
			guikit.VGap(20),
			layout.Rigid(column(func(gtx layout.Context) layout.Dimensions {
				return layout.Flex{Axis: layout.Vertical}.Layout(gtx, kids...)
			})))
	})
}

// restoreWherePage is step 1: where the backup is.
type restoreWherePage struct {
	restorePage
	list widget.List
}

func (p *restoreWherePage) Footer(n nav) (leading, trailing []guikit.Action) {
	_, problem := p.r.formTarget()
	return p.footer(n, "Continue", problem != "")
}

func (p *restoreWherePage) Continue() {
	r := p.r
	t, problem := r.formTarget()
	if problem != "" {
		return
	}
	r.mu.Lock()
	r.target, r.cred, r.listErr = &t, nil, ""
	r.mu.Unlock()
	// Submitted: the server's secrets are kept in t until Restore.
	r.restPw.Editor.SetText("")
	r.s3Secret.Editor.SetText("")
	r.pwFocused = false
	r.u.flow.Next()
}

// goBack leaves the restore for Welcome.
func (p *restoreWherePage) goBack() {
	p.r.leave()
	p.r.u.flow.Back()
}

func (p *restoreWherePage) Update(gtx layout.Context) {
	r := p.r
	if r.usbClick.Clicked(gtx) {
		r.chooseWhere(whereUSB)
	}
	if r.remoteClick.Clicked(gtx) {
		r.chooseWhere(whereRemote)
	}
	for i := range r.remoteClicks {
		if r.remoteClicks[i].Clicked(gtx) {
			r.chooseRemote(remoteKinds[i].ID)
		}
	}
	for id, k := range r.diskClicks {
		if k.Clicked(gtx) {
			r.disk = id
		}
	}
	if r.rescan.Clicked(gtx) {
		r.loadDisks()
	}
	if r.copyClick.Clicked(gtx) {
		if k := r.publicKey(); k != "" {
			gtx.Execute(clipboard.WriteCmd{Type: "application/text", Data: io.NopCloser(strings.NewReader(k))})
			r.mu.Lock()
			r.copied = true
			r.mu.Unlock()
		}
	}
	if r.connectClick.Clicked(gtx) {
		r.connect()
	}
	r.mu.Lock()
	clicks := r.scannedClicks
	r.mu.Unlock()
	for i := range clicks {
		if clicks[i].Clicked(gtx) {
			r.chooseHostKey(i)
		}
	}
	if r.acceptClick.Clicked(gtx) {
		r.acceptHostKey()
	}
}

func (p *restoreWherePage) Body(gtx layout.Context) layout.Dimensions {
	r, u := p.r, p.r.u
	_, problem := r.formTarget()
	r.mu.Lock()
	disks, disksErr, loadingDisks := r.disks, r.disksErr, r.loadingDisks
	sshKey, sshErr, loadingKey, copied := r.sshKey, r.sshKeyErr, r.loadingKey, r.copied
	connecting, connectErr := r.connecting, r.connectErr
	scanned, scannedChosen, scannedOf, scannedClicks := r.scanned, r.scannedChosen, r.scannedOf, r.scannedClicks
	confirmed, confirmedOf := r.hostKey, r.hostKeyOf
	r.mu.Unlock()

	kids := []layout.FlexChild{
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return layout.Flex{}.Layout(gtx,
				layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
					return u.ChoiceCard(gtx, &r.usbClick, r.where == whereUSB, "A USB disk", "Plugged in to this computer")
				}),
				layout.Rigid(layout.Spacer{Width: 10}.Layout),
				layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
					return u.ChoiceCard(gtx, &r.remoteClick, r.where == whereRemote, "A remote server", "A REST server, S3 or SFTP")
				}))
		}),
		guikit.VGap(16),
	}
	if r.where == whereUSB {
		on, _ := pickDisk(disks, r.disk)
		switch {
		case r.u.hooks.USBDisks == nil:
			kids = append(kids, r.dim("USB disks cannot be listed: udisks2 is not running."))
		case disksErr != "":
			kids = append(kids, r.warning(disksErr))
		case loadingDisks && len(disks) == 0:
			kids = append(kids, r.dim("Looking for USB disks…"))
		case len(disks) == 0:
			kids = append(kids, r.text("Plug in the USB disk with the backup, then click Look again."))
		}
		for i, d := range disks {
			k, have := r.diskClicks[d.UUID]
			if !have {
				k = new(widget.Clickable)
				r.diskClicks[d.UUID] = k
			}
			if i > 0 {
				kids = append(kids, guikit.VGap(8))
			}
			kids = append(kids, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return u.ChoiceCard(gtx, k, on.UUID == d.UUID, diskName(d), d.FSType+" · "+humanSize(d.Size))
			}))
		}
		kids = append(kids, guikit.VGap(10), r.button(&r.rescan, "Look again", !loadingDisks), guikit.VGap(14),
			r.field("Folder", r.folder, "The folder on the disk where the backup is."))
	} else {
		kids = append(kids, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			var cs []layout.FlexChild
			for i, k := range remoteKinds {
				if i > 0 {
					cs = append(cs, layout.Rigid(layout.Spacer{Width: 10}.Layout))
				}
				cs = append(cs, layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
					return u.ChoiceCard(gtx, &r.remoteClicks[i], r.remote == k.ID, k.Name, k.Sub)
				}))
			}
			return layout.Flex{}.Layout(gtx, cs...)
		}), guikit.VGap(14))
		switch r.remote {
		case remoteREST:
			kids = append(kids,
				r.field("Address", r.restURL, "Such as https://backup.example.com:8000/anna/"),
				r.field("User", r.restUser, ""),
				r.field("Password", r.restPw, ""))
		case remoteS3:
			kids = append(kids,
				r.field("Endpoint", r.s3Endpoint, "Such as https://s3.amazonaws.com"),
				r.field("Bucket", r.s3Bucket, ""),
				r.field("Path", r.s3Path, "In the bucket. Optional."),
				r.field("Access key ID", r.s3Key, ""),
				r.field("Secret access key", r.s3Secret, ""),
				r.field("Region", r.s3Region, "Optional."))
		case remoteSFTP:
			kids = append(kids,
				r.field("User and server", r.sftpUserHost, "Such as anna@nas.local"),
				r.field("Port", r.sftpPort, ""),
				r.field("Folder on the server", r.sftpPath, "Such as /srv/backup"))
			switch {
			case sshErr != "":
				kids = append(kids, r.warning(sshErr))
			case sshKey == "" || loadingKey:
				kids = append(kids, r.dim("Reading the backup's SSH key…"))
			default:
				kids = append(kids, r.text("Add this key to the server's authorized_keys, then Connect."), guikit.VGap(8),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						return guikit.Panel(gtx, 10, layout.UniformInset(12), func(gtx layout.Context) layout.Dimensions {
							return u.LabelLines(gtx, guikit.StMono12, guikit.ColText, 1.3, sshKey)
						})
					}), guikit.VGap(10),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						return layout.Flex{}.Layout(gtx,
							layout.Rigid(func(gtx layout.Context) layout.Dimensions {
								return u.Button(gtx, &r.copyClick, guikit.ButtonSecondary, "Copy", true)
							}),
							layout.Rigid(layout.Spacer{Width: 10}.Layout),
							layout.Rigid(func(gtx layout.Context) layout.Dimensions {
								return u.Button(gtx, &r.connectClick, guikit.ButtonSecondary, "Connect", !connecting)
							}))
					}))
				if copied {
					kids = append(kids, guikit.VGap(6), r.dim("Copied."))
				}
			}
			if connecting {
				kids = append(kids, guikit.VGap(8), r.dim("Fetching the server's keys…"))
			}
			if connectErr != "" {
				kids = append(kids, guikit.VGap(8), r.warning(connectErr))
			}
			if len(scanned) > 0 {
				kids = append(kids, guikit.VGap(14),
					r.text(scannedOf+" sent these keys. On the server, ssh-keygen -lf /etc/ssh/ssh_host_*_key.pub shows its own, with their type. Choose the key you compared, and confirm it only when it is the same."),
					guikit.VGap(8))
				for i, k := range scanned {
					if i >= len(scannedClicks) {
						break
					}
					if i > 0 {
						kids = append(kids, guikit.VGap(8))
					}
					kids = append(kids, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						return u.ChoiceCard(gtx, &scannedClicks[i], i == scannedChosen, k.typ, k.fingerprint)
					}))
				}
				kids = append(kids, guikit.VGap(10), r.button(&r.acceptClick, "This is the server's key", true))
			}
			if confirmed != "" {
				kids = append(kids, guikit.VGap(8), r.dim("Key of "+confirmedOf+" confirmed: "+confirmed))
			}
		}
	}
	if problem != "" {
		kids = append(kids, guikit.VGap(8), r.dim(problem))
	}
	return r.step(gtx, &p.list, "Where is the backup?", "A USB disk, or the server this machine backed up to.", kids...)
}

// restorePasswordPage is step 2: the backup's password.
type restorePasswordPage struct {
	restorePage
	list widget.List
}

func (p *restorePasswordPage) holding() bool {
	p.r.mu.Lock()
	defer p.r.mu.Unlock()
	return p.r.listing
}

func (p *restorePasswordPage) Footer(n nav) (leading, trailing []guikit.Action) {
	return p.footer(n, "Find the backups", p.holding() || !pwOK(p.r.pw.Editor.Text()))
}

func (p *restorePasswordPage) Continue() { p.r.list() }

// goBack drops the target and its credentials: Where asks them again.
func (p *restorePasswordPage) goBack() {
	if p.holding() {
		return
	}
	p.r.pw.Editor.SetText("")
	p.r.dropKept()
	p.r.u.flow.Back()
}

func (p *restorePasswordPage) Update(gtx layout.Context) {
	if p.r.pw.Submitted(gtx) {
		p.r.u.next()
	}
	p.r.settle()
}

func (p *restorePasswordPage) Body(gtx layout.Context) layout.Dimensions {
	r := p.r
	r.mu.Lock()
	listing, listErr := r.listing, r.listErr
	name := ""
	if r.target != nil {
		name = r.target.name
	}
	r.mu.Unlock()
	if !r.pwFocused {
		r.pwFocused = true
		gtx.Execute(key.FocusCmd{Tag: &r.pw.Editor})
	}
	kids := []layout.FlexChild{
		r.field("Password", r.pw, "The password shown when the backup was set up. For a key shown by Isotope, spaces and capitals do not matter."),
	}
	switch {
	case listing:
		kids = append(kids, r.dim("Reading the backups in "+name+"…"))
	case listErr != "":
		kids = append(kids, r.warning(listErr))
	}
	return r.step(gtx, &p.list, "The backup's password", "Type the password of the backup in "+name+".", kids...)
}

// restoreBackupPage is step 3: which backup. Continue asks to confirm.
type restoreBackupPage struct {
	restorePage
	list widget.List
}

func (p *restoreBackupPage) Footer(n nav) (leading, trailing []guikit.Action) {
	return p.footer(n, "Restore…", false)
}

func (p *restoreBackupPage) Continue() { p.r.askConfirm() }

// goBack drops the password: the Password step asks it again.
func (p *restoreBackupPage) goBack() {
	r := p.r
	r.mu.Lock()
	r.cred, r.snaps, r.listErr = nil, nil, ""
	r.mu.Unlock()
	r.pwFocused = false
	r.u.flow.Back()
}

func (p *restoreBackupPage) Update(gtx layout.Context) {
	r := p.r
	r.mu.Lock()
	clicks := r.snapClicks
	r.mu.Unlock()
	for i := range clicks {
		if clicks[i].Clicked(gtx) {
			r.chooseSnapshot(i)
		}
	}
}

func (p *restoreBackupPage) Body(gtx layout.Context) layout.Dimensions {
	r, u := p.r, p.r.u
	r.mu.Lock()
	snaps, chosen, clicks := r.snaps, r.chosen, r.snapClicks
	r.mu.Unlock()
	now := u.now()
	var kids []layout.FlexChild
	for i, s := range snaps {
		if i >= len(clicks) {
			break
		}
		if i > 0 {
			kids = append(kids, guikit.VGap(8))
		}
		sub := "Backup " + s.ID[:min(8, len(s.ID))]
		if s.Size > 0 {
			sub = humanSize(s.Size) + " · " + sub
		}
		kids = append(kids, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return u.ChoiceCard(gtx, &clicks[i], i == chosen, snapshotTitle(s, now), sub)
		}))
	}
	return r.step(gtx, &p.list, "Which backup?", "The backups there, by computer, the newest first.", kids...)
}

// restoreRunPage is the last step: the restore, then the restart.
type restoreRunPage struct {
	restorePage
	list widget.List
}

// holding: Go back only after a refusal, when the daemon did not start the
// restore. After a failed job the machine may have changed.
func (p *restoreRunPage) holding() bool {
	v, _ := p.r.runView()
	return v != runRefused
}

func (p *restoreRunPage) Footer(n nav) (leading, trailing []guikit.Action) {
	if p.holding() {
		return nil, nil
	}
	return []guikit.Action{*n.Back}, nil
}

func (p *restoreRunPage) Continue() {}

// goBack goes to Where after a refusal: the secrets went with Restore, so
// the steps ask them again.
func (p *restoreRunPage) goBack() {
	r := p.r
	r.mu.Lock()
	r.run, r.runMsg = runNone, ""
	r.mu.Unlock()
	r.dropKept()
	r.u.goBackTo(PRestoreWhere)
}

func (p *restoreRunPage) Update(layout.Context) {}

// runTexts are the heading and the line of the last step for v.
func runTexts(v runView) (title, sub string) {
	switch v {
	case runRefused:
		return "The restore did not start", "Nothing on this machine changed."
	case runFailed:
		return "The restore failed", "Parts of the backup may already be on this machine, and it may restart. If it does not, restart it and check your files; to try again, use isotope restore in a terminal."
	case runRestarting:
		return "Restarting", "The backup is back. The machine restarts now; after the restart it installs the apps and extensions again."
	case runRunning:
		return "Restoring the backup", "This can take a long time. Leave the computer on: it restarts when the restore is done."
	}
	return "Starting the restore", "Asking the update service…"
}

func (p *restoreRunPage) Body(gtx layout.Context) layout.Dimensions {
	r := p.r
	v, msg := r.runView()
	title, sub := runTexts(v)
	if v == runRefused || v == runFailed {
		return r.step(gtx, &p.list, title, sub, r.warning(msg))
	}
	return r.step(gtx, &p.list, title, sub)
}
