package main

// The Backup page's setup, export and restore: where backups go (a USB
// disk by the UUID of its file system, or a REST, S3 or SFTP server), the
// password made here and shown once, the export to a file, and the restore
// of a whole machine, offered only on a new machine. See "The Backup page",
// "Disks" and "Remote targets" in
// docs/superpowers/specs/2026-11-20-isotope-welcome-design.md. Secrets go to
// the daemon only through the credentials pipe (Credentials), never in a
// target string.

import (
	"errors"
	"fmt"
	"net/url"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"gioui.org/layout"
	"gioui.org/widget"

	"github.com/kairos-community/isotope/pkg/guikit"
	"github.com/kairos-community/isotope/pkg/recoverykey"
)

const (
	whereUSB    = "usb"
	whereRemote = "remote"

	remoteREST = "rest"
	remoteS3   = "s3"
	remoteSFTP = "sftp"

	defaultFolder = "isotope-backup"
)

var remoteKinds = []struct{ ID, Name, Sub string }{
	{remoteREST, "REST server", "restic's rest-server, over HTTP or HTTPS"},
	{remoteS3, "S3", "Amazon S3, or a service that works like it"},
	{remoteSFTP, "SFTP", "A server you reach with SSH"},
}

// The jobs this window started (backupPage.started).
const (
	jobBackup  = "backup"
	jobExport  = "export"
	jobRestore = "restore"
)

// setupForm is the "Back up this machine" form.
type setupForm struct {
	where, remote string
	// disk is the block ID of the chosen USB file system ("" for the first).
	disk       string
	containers bool

	folder                                                  *guikit.Field
	restURL, restUser, restPw                               *guikit.Field
	s3Endpoint, s3Bucket, s3Path, s3Key, s3Secret, s3Region *guikit.Field
	sftpUserHost, sftpPort, sftpPath                        *guikit.Field
	existing                                                *guikit.Field
	existingFocused                                         bool

	usbClick, remoteClick, containersClick widget.Clickable
	remoteClicks                           [3]widget.Clickable
	diskClicks                             map[string]*widget.Clickable
	copyClick, connectClick, restoreClick  widget.Clickable

	// Under u.mu.
	sshKey, sshKeyErr  string
	loadingKey         bool
	hostKey, hostKeyOf string // the confirmed fingerprint, and its "host:port"
	// scanned are the keys of the open "Is this the server's key?"
	// question, ED25519 first; scannedChosen the one chosen.
	scanned       []hostKey
	scannedChosen int
	scannedClicks []widget.Clickable
	connecting    bool
	copied        bool
}

func newSetupForm() setupForm {
	f := setupForm{where: whereUSB, remote: remoteREST, diskClicks: map[string]*widget.Clickable{},
		folder: guikit.NewField(false), restURL: guikit.NewField(false), restUser: guikit.NewField(false), restPw: guikit.NewSecret(),
		s3Endpoint: guikit.NewField(false), s3Bucket: guikit.NewField(false), s3Path: guikit.NewField(false),
		s3Key: guikit.NewField(false), s3Secret: guikit.NewSecret(), s3Region: guikit.NewField(false),
		sftpUserHost: guikit.NewField(false), sftpPort: guikit.NewField(false), sftpPath: guikit.NewField(false),
		existing: guikit.NewSecret()}
	f.folder.Editor.SetText(defaultFolder)
	f.sftpPort.Editor.SetText("22")
	return f
}

// clearSecrets empties the fields that hold a secret.
func (f *setupForm) clearSecrets() {
	for _, x := range []*guikit.Field{f.restPw, f.s3Secret, f.existing} {
		x.Editor.SetText("")
		x.Err = ""
	}
}

// exportForm is the "Export to a file" dialog: the disk and the file name.
type exportForm struct {
	name    *guikit.Field
	disk    string // block ID, "" for the first
	clicks  map[string]*widget.Clickable
	focused bool
}

// restoreForm is the restore's dialogs: the password, then the backup.
type restoreForm struct {
	pw      *guikit.Field
	focused bool
	snaps   []Snapshot
	chosen  int
	clicks  []widget.Clickable
}

// ---- targets, as plain functions ----

func hasControl(s string) bool { return strings.ContainsFunc(s, unicode.IsControl) }

// cleanFolder is a folder on the disk without the slashes around it.
func cleanFolder(folder string) (string, error) {
	f := strings.TrimSpace(folder)
	if hasControl(f) {
		return "", errors.New("The folder name has a control character.")
	}
	for _, part := range strings.Split(f, "/") {
		if part == "." || part == ".." {
			return "", errors.New("The folder cannot have . or .. in it.")
		}
	}
	f = strings.Trim(path.Clean("/"+f), "/")
	if f == "" {
		return "", errors.New("Type the folder on the disk for the backup.")
	}
	return f, nil
}

// usbTarget is uuid:<UUID>/<folder>: the daemon finds the disk by the UUID
// of its file system, wherever it is mounted.
func usbTarget(b Block, folder string) (string, error) {
	f, err := cleanFolder(folder)
	if err != nil {
		return "", err
	}
	return "uuid:" + b.UUID + "/" + f, nil
}

// envLine is NAME=value, refused when the value would break the lines of
// the credentials pipe.
func envLine(name, value string) (string, error) {
	if strings.ContainsAny(value, "\r\n\x00") {
		return "", fmt.Errorf("The value of %s has a line break in it.", name)
	}
	return name + "=" + value, nil
}

// restTarget is rest:<url>, with the user and the password as credentials.
func restTarget(raw, user, password string) (string, []string, error) {
	u := strings.TrimSpace(raw)
	pu, err := url.Parse(u)
	if u == "" || hasControl(u) || err != nil || (pu.Scheme != "http" && pu.Scheme != "https") || pu.Host == "" {
		return "", nil, errors.New("Type the address of the server, starting with http:// or https://.")
	}
	if pu.User != nil {
		return "", nil, errors.New("Put the user and the password in their fields, not in the address.")
	}
	var env []string
	for _, kv := range [][2]string{{"RESTIC_REST_USERNAME", user}, {"RESTIC_REST_PASSWORD", password}} {
		if kv[1] == "" {
			continue
		}
		l, err := envLine(kv[0], kv[1])
		if err != nil {
			return "", nil, err
		}
		env = append(env, l)
	}
	return "rest:" + u, env, nil
}

// s3Target is s3:<endpoint>/<bucket>/<path>, with the keys and the region
// as credentials.
func s3Target(endpoint, bucket, folder, keyID, secret, region string) (string, []string, error) {
	e := strings.TrimRight(strings.TrimSpace(endpoint), "/")
	b := strings.TrimSpace(bucket)
	p := strings.Trim(strings.TrimSpace(folder), "/")
	switch {
	case e == "" || hasControl(e) || strings.ContainsAny(e, "@ "):
		return "", nil, errors.New("Type the endpoint of the service, such as https://s3.amazonaws.com.")
	case b == "" || hasControl(b) || strings.ContainsAny(b, "/ "):
		return "", nil, errors.New("Type the name of the bucket.")
	case hasControl(p):
		return "", nil, errors.New("The path has a control character.")
	case strings.TrimSpace(keyID) == "" || secret == "":
		return "", nil, errors.New("Type the access key ID and the secret access key.")
	}
	t := "s3:" + e + "/" + b
	if p != "" {
		t += "/" + p
	}
	var env []string
	for _, kv := range [][2]string{{"AWS_ACCESS_KEY_ID", strings.TrimSpace(keyID)}, {"AWS_SECRET_ACCESS_KEY", secret}, {"AWS_DEFAULT_REGION", strings.TrimSpace(region)}} {
		if kv[1] == "" {
			continue
		}
		l, err := envLine(kv[0], kv[1])
		if err != nil {
			return "", nil, err
		}
		env = append(env, l)
	}
	return t, env, nil
}

const hostChars = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789.-_"

// sftpTarget is sftp:user@host:path on port 22, and
// sftp://user@host:port/path on another port. host and port are for
// BackupHostKey.
func sftpTarget(userHost, portText, folder string) (target, host string, port uint32, err error) {
	uh := strings.TrimSpace(userHost)
	user, host := "", uh
	if i := strings.LastIndex(uh, "@"); i >= 0 {
		user, host = uh[:i], uh[i+1:]
		if user == "" || strings.HasPrefix(user, "-") || hasControl(user) || strings.ContainsAny(user, " :/@") {
			return "", "", 0, errors.New("Type the user and the server as user@server.")
		}
	}
	if host == "" || strings.HasPrefix(host, "-") || strings.Trim(host, hostChars) != "" {
		return "", "", 0, errors.New("Type the user and the server as user@server.")
	}
	n := uint64(22)
	if ps := strings.TrimSpace(portText); ps != "" {
		n, err = strconv.ParseUint(ps, 10, 16)
		if err != nil || n == 0 {
			return "", "", 0, errors.New("The port is a number from 1 to 65535.")
		}
	}
	p := strings.TrimSpace(folder)
	if p == "" || hasControl(p) {
		return "", "", 0, errors.New("Type the folder on the server for the backup.")
	}
	if n == 22 {
		return "sftp:" + uh + ":" + p, host, 22, nil
	}
	return "sftp://" + uh + ":" + strconv.FormatUint(n, 10) + "/" + p, host, uint32(n), nil
}

// exportNameProblem says what is wrong with the name of an export file, ""
// for nothing.
func exportNameProblem(name string) string {
	switch {
	case strings.TrimSpace(name) == "":
		return "Type a file name."
	case strings.Contains(name, "/"):
		return "A file name has no /."
	case strings.HasPrefix(name, "."):
		return "A file name does not start with a dot."
	case hasControl(name):
		return "The file name has a control character."
	}
	return ""
}

// restoreBody is the restore's last question: what comes back (the
// Welcome design's restore step 4).
func restoreBody(s Snapshot, when string) string {
	from := "The backup of " + when
	if s.Host != "" {
		from += " from " + s.Host
	}
	return from + " brings this whole machine back as it was: the users and their passwords, the hostname, " +
		"every home, the SSH keys, the extensions, the settings and the Flatpak apps (installed again after the restart).\n" +
		"What is on this machine now is replaced. The machine restarts at the end."
}

// ---- the form on the page ----

// setupTarget is a target the form describes.
type setupTarget struct {
	target, name string
	opt          TargetOptions
	env          []string
	disk         Block // a USB file system; ID "" for a remote target
}

// listOpt are the options BackupList and Restore take: only the SFTP
// server's key and port.
func (t setupTarget) listOpt() TargetOptions {
	return TargetOptions{HostKey: t.opt.HostKey, Port: t.opt.Port}
}

// backupDisks are the file systems on removable drives a backup may go to.
// A drive with any Isotope block (Isotope run from a USB disk) offers none
// of its file systems. welcome's usbDisksFrom has the same rule (twins).
func (p *backupPage) backupDisks() []Block {
	p.u.mu.Lock()
	defer p.u.mu.Unlock()
	var out []Block
	for _, d := range p.drives {
		if !d.Removable || driveReason(d, p.rootDevs) == reasonIsotope {
			continue
		}
		for _, b := range d.Blocks {
			if usableForBackupOn(b, p.rootDevs) {
				out = append(out, b)
			}
		}
	}
	return out
}

// pick is the block id among disks; id "" is the first. A chosen disk that
// is gone is not replaced by another.
func pick(disks []Block, id string) (Block, bool) {
	for _, b := range disks {
		if b.ID == id {
			return b, true
		}
	}
	if id == "" && len(disks) > 0 {
		return disks[0], true
	}
	return Block{}, false
}

func (p *backupPage) pickDisk(id string) {
	p.form.where, p.form.disk = whereUSB, id
}

func (p *backupPage) pickRemote(kind string) {
	p.form.where, p.form.remote = whereRemote, kind
	if kind == remoteSFTP {
		p.loadSSHKey()
	}
}

// formTarget is the target the form describes, or what is missing.
func (p *backupPage) formTarget() (setupTarget, string) {
	f := &p.form
	if f.where == whereUSB {
		b, ok := pick(p.backupDisks(), f.disk)
		if !ok {
			return setupTarget{}, "Plug in a USB disk, and choose it."
		}
		t, err := usbTarget(b, f.folder.Editor.Text())
		if err != nil {
			return setupTarget{}, err.Error()
		}
		return setupTarget{target: t, name: labelOf(b) + " (USB disk)", disk: b,
			opt: TargetOptions{Label: b.Label, Containers: f.containers}}, ""
	}
	var t setupTarget
	var err error
	t.opt.Containers = f.containers
	switch f.remote {
	case remoteREST:
		t.target, t.env, err = restTarget(f.restURL.Editor.Text(), f.restUser.Editor.Text(), f.restPw.Editor.Text())
	case remoteS3:
		t.target, t.env, err = s3Target(f.s3Endpoint.Editor.Text(), f.s3Bucket.Editor.Text(), f.s3Path.Editor.Text(),
			f.s3Key.Editor.Text(), f.s3Secret.Editor.Text(), f.s3Region.Editor.Text())
	case remoteSFTP:
		var host string
		var port uint32
		t.target, host, port, err = sftpTarget(f.sftpUserHost.Editor.Text(), f.sftpPort.Editor.Text(), f.sftpPath.Editor.Text())
		if err == nil {
			p.u.mu.Lock()
			fp, of := f.hostKey, f.hostKeyOf
			p.u.mu.Unlock()
			if fp == "" || of != hostPort(host, port) {
				return setupTarget{}, "Connect to the server and confirm its key."
			}
			t.opt.HostKey, t.opt.Port = fp, port
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

func hostPort(host string, port uint32) string { return fmt.Sprintf("%s:%d", host, port) }

// ready: the form describes a target, so Continue is on.
func (p *backupPage) ready() bool {
	_, problem := p.formTarget()
	return problem == ""
}

// mountPoint is where b is mounted, after mounting it if it is not.
func (p *backupPage) mountPoint(b Block) (string, error) {
	if len(b.MountPoints) > 0 {
		return b.MountPoints[0], nil
	}
	ds := p.u.hooks.Disks
	if ds == nil {
		return "", errors.New("udisks2 is not running")
	}
	mp, err := ds.Mount(b.ID)
	if err != nil {
		return "", err
	}
	if mp == "" {
		return "", errors.New("it was mounted nowhere")
	}
	return mp, nil
}

// cont is Continue: the disk is mounted if it must be, then "Create a new
// backup" or "Use an existing backup".
func (p *backupPage) cont() {
	u, d := p.u, p.u.hooks.Daemon
	if !u.sys.Admin || d == nil || p.busyHere() {
		return
	}
	t, problem := p.formTarget()
	if problem != "" {
		return
	}
	u.async(func() {
		if t.disk.ID != "" {
			if _, err := p.mountPoint(t.disk); err != nil {
				u.tell("Could not mount "+labelOf(t.disk), err.Error())
				return
			}
		}
		u.askForm("Set up the backup", "Backups go to "+t.name+". Create a new backup there, with a new password, or use a backup that is there already, with its password.", nil, 2,
			choice{Label: "Cancel", Kind: guikit.ButtonSecondary, Do: p.form.clearSecrets},
			choice{Label: "Use an existing backup", Kind: guikit.ButtonSecondary, Do: func() { p.openExisting(t) }},
			choice{Label: "Create a new backup", Kind: guikit.ButtonPrimary, Do: func() { p.createNew(t) }})
	})
}

// createNew makes the backup's password, shows it once and has a group
// typed back; only then does BackupInit run.
func (p *backupPage) createNew(t setupTarget) {
	u := p.u
	key, err := recoverykey.New()
	if err != nil {
		u.tell("Could not make a password", err.Error())
		return
	}
	u.keyDialog("The password of this backup",
		"Write it down and keep it away from this computer. It is shown only now. Without it, nobody can read the backup, not even you.",
		key, func() { p.initTarget(t, Credentials{Password: key, Env: t.env}, false) })
}

// openExisting asks the password of the backup that is there.
func (p *backupPage) openExisting(t setupTarget) {
	u, f := p.u, &p.form
	f.existing.Editor.SetText("")
	f.existingFocused = false
	clearPw := func() { f.existing.Editor.SetText("") }
	ok := func() bool { pw := f.existing.Editor.Text(); return pw != "" && !strings.ContainsAny(pw, "\r\n\x00") }
	var dlg *dialog
	dlg = newDialog("Use an existing backup", "Type the password of the backup in "+t.name+".", func(gtx layout.Context) layout.Dimensions {
		if f.existing.Submitted(gtx) && ok() {
			u.pick(dlg, len(dlg.choices)-1)
		}
		if !f.existingFocused {
			f.existingFocused = true
			gtx.Execute(focus(&f.existing.Editor))
		}
		return fieldsCol(gtx, u, fieldRow{title: "Password", f: f.existing})
	}, 0,
		choice{Label: "Cancel", Kind: guikit.ButtonSecondary, Do: f.clearSecrets},
		choice{Label: "Use this backup", Kind: guikit.ButtonPrimary, Enabled: ok, Do: func() {
			pw := f.existing.Editor.Text()
			clearPw()
			p.initTarget(t, Credentials{Password: pw, Env: t.env}, true)
		}})
	u.open(dlg)
}

// initTarget runs BackupInit, which can take minutes on a slow server.
func (p *backupPage) initTarget(t setupTarget, cred Credentials, existing bool) {
	u, d := p.u, p.u.hooks.Daemon
	if d == nil {
		return
	}
	opt := t.opt
	opt.Existing = existing
	p.setWorking("Setting up the backup in " + t.name + "…")
	u.async(func() {
		// The deferred setWorking clears the busy flag even on a panic.
		err := func() error {
			defer p.setWorking("")
			return d.BackupInit(t.target, opt, cred)
		}()
		if err != nil {
			u.tell("The backup was not set up", Describe(err))
			return
		}
		u.tell("The backup is set up", "This machine backs up to "+t.name+". Click Back up now for the first backup, or choose a schedule.")
		p.load()
	})
	p.form.clearSecrets()
}

// busyHere: a setup or a list started here still runs; its answer opens a
// dialog, which would replace a new one.
func (p *backupPage) busyHere() bool {
	p.u.mu.Lock()
	defer p.u.mu.Unlock()
	return p.working != ""
}

func (p *backupPage) setWorking(s string) {
	p.u.mu.Lock()
	p.working = s
	p.u.mu.Unlock()
	p.u.hooks.Invalidate()
}

// loadSSHKey reads the backup's public SSH key, made the first time.
func (p *backupPage) loadSSHKey() {
	u, d := p.u, p.u.hooks.Daemon
	if !u.sys.Admin || d == nil {
		return
	}
	u.mu.Lock()
	if p.form.sshKey != "" || p.form.loadingKey {
		u.mu.Unlock()
		return
	}
	p.form.loadingKey = true
	u.mu.Unlock()
	u.async(func() {
		key, err := d.BackupSSHKey()
		u.mu.Lock()
		defer u.mu.Unlock()
		p.form.loadingKey = false
		p.form.sshKey, p.form.sshKeyErr = key, ""
		if err != nil {
			p.form.sshKeyErr = "Could not read the backup's SSH key: " + Describe(err)
		}
	})
}

func (p *backupPage) publicKey() string {
	p.u.mu.Lock()
	defer p.u.mu.Unlock()
	return p.form.sshKey
}

// connect fetches the SFTP server's key and asks the user to confirm its
// fingerprint.
func (p *backupPage) connect() {
	u, d, f := p.u, p.u.hooks.Daemon, &p.form
	if !u.sys.Admin || d == nil {
		return
	}
	_, host, port, err := sftpTarget(f.sftpUserHost.Editor.Text(), f.sftpPort.Editor.Text(), f.sftpPath.Editor.Text())
	if err != nil {
		u.tell("Cannot connect", err.Error())
		return
	}
	u.mu.Lock()
	if f.connecting {
		u.mu.Unlock()
		return
	}
	f.connecting = true
	u.mu.Unlock()
	u.async(func() {
		fps, err := d.BackupHostKey(host, port)
		u.mu.Lock()
		f.connecting = false
		u.mu.Unlock()
		if err == nil && len(fps) == 0 {
			err = errors.New("the server sent no key")
		}
		if err != nil {
			u.tell("Could not reach "+host, Describe(err))
			return
		}
		keys := hostKeyChoices(fps)
		u.mu.Lock()
		f.scanned, f.scannedChosen = keys, 0
		f.scannedClicks = make([]widget.Clickable, len(keys))
		u.mu.Unlock()
		u.askForm("Is this the server's key?",
			fmt.Sprintf("%s (port %d) sent these keys. On the server, ssh-keygen -lf /etc/ssh/ssh_host_*_key.pub shows its own, with their type. Choose the key you compared, and say yes only when it is the same.", host, port),
			p.hostKeyForm, 0,
			choice{Label: "No", Kind: guikit.ButtonSecondary},
			choice{Label: "Yes", Kind: guikit.ButtonPrimary, Do: func() {
				u.mu.Lock()
				defer u.mu.Unlock()
				if i := f.scannedChosen; i >= 0 && i < len(f.scanned) {
					f.hostKey, f.hostKeyOf = f.scanned[i].fingerprint, hostPort(host, port)
				}
			}})
	})
}

// hostKey is one key BackupHostKey sent: "TYPE SHA256:...".
type hostKey struct{ typ, fingerprint string }

// hostKeyChoices are the keys BackupHostKey sent, ED25519 first: the type
// most servers show the user. An entry without a type is "Key".
func hostKeyChoices(fps []string) []hostKey {
	var out []hostKey
	for _, s := range fps {
		f := strings.Fields(s)
		switch len(f) {
		case 0:
			continue
		case 1:
			out = append(out, hostKey{typ: "Key", fingerprint: f[0]})
		default:
			out = append(out, hostKey{typ: f[0], fingerprint: f[len(f)-1]})
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].typ == "ED25519" && out[j].typ != "ED25519" })
	return out
}

// hostKeyRows are the keys as the question lists them, for the tests.
func (p *backupPage) hostKeyRows() (rows []string, chosen int) {
	p.u.mu.Lock()
	defer p.u.mu.Unlock()
	for _, k := range p.form.scanned {
		rows = append(rows, k.typ+" "+k.fingerprint)
	}
	return rows, p.form.scannedChosen
}

// chooseHostKey chooses the i-th key of the question, as a click does.
func (p *backupPage) chooseHostKey(i int) {
	p.u.mu.Lock()
	defer p.u.mu.Unlock()
	if i >= 0 && i < len(p.form.scanned) {
		p.form.scannedChosen = i
	}
}

// hostKeyForm lists the server's keys, one to choose.
func (p *backupPage) hostKeyForm(gtx layout.Context) layout.Dimensions {
	u, f := p.u, &p.form
	u.mu.Lock()
	keys, chosen, clicks := f.scanned, f.scannedChosen, f.scannedClicks
	u.mu.Unlock()
	var c col
	for i, k := range keys {
		if i >= len(clicks) {
			break
		}
		if clicks[i].Clicked(gtx) {
			p.chooseHostKey(i)
			chosen = i
		}
		if i > 0 {
			c.gap(8)
		}
		c.add(func(gtx layout.Context) layout.Dimensions {
			return u.ChoiceCard(gtx, &clicks[i], i == chosen, k.typ, k.fingerprint)
		})
	}
	return c.layout(gtx)
}

// ---- export ----

// openExport asks where the export goes: a removable disk and a file name.
func (p *backupPage) openExport() {
	u, d := p.u, p.u.hooks.Daemon
	if !u.sys.Admin || d == nil {
		return
	}
	if !p.canExport() {
		return
	}
	if u.hooks.Disks == nil {
		u.tell("Cannot export", "USB disks cannot be listed: udisks2 is not running.")
		return
	}
	e := &p.export
	e.name.Editor.SetText(exportName(u.sys.Hostname, u.now()))
	e.name.Err = ""
	e.disk, e.focused = "", false
	chosen := func() (Block, bool) { return pick(p.backupDisks(), e.disk) }
	ok := func() bool { _, there := chosen(); return there && exportNameProblem(e.name.Editor.Text()) == "" }
	u.askForm("Export to a file", "The file holds the whole backup, encrypted with its password. isotope restore reads it on any machine.",
		func(gtx layout.Context) layout.Dimensions {
			disks := p.backupDisks()
			on, _ := chosen()
			var c col
			if len(disks) == 0 {
				c.add(u.para("Plug in a USB disk."))
			}
			for i, b := range disks {
				k, have := e.clicks[b.ID]
				if !have {
					k = new(widget.Clickable)
					e.clicks[b.ID] = k
				}
				if k.Clicked(gtx) {
					e.disk = b.ID
					on = b
				}
				if i > 0 {
					c.gap(8)
				}
				c.add(func(gtx layout.Context) layout.Dimensions {
					return u.ChoiceCard(gtx, k, on.ID == b.ID, labelOf(b), diskSub(b))
				})
			}
			c.gap(12)
			c.add(func(gtx layout.Context) layout.Dimensions {
				if !e.focused {
					e.focused = true
					gtx.Execute(focus(&e.name.Editor))
				}
				e.name.Err = ""
				if e.name.Editor.Text() != "" {
					e.name.Err = exportNameProblem(e.name.Editor.Text())
				}
				return fieldsCol(gtx, u, fieldRow{title: "File name", f: e.name, hint: "Written at the top of the disk."})
			})
			return c.layout(gtx)
		}, 0,
		choice{Label: "Cancel", Kind: guikit.ButtonSecondary},
		choice{Label: "Export", Kind: guikit.ButtonPrimary, Enabled: ok, Do: func() {
			b, there := chosen()
			name := e.name.Editor.Text()
			if !there || exportNameProblem(name) != "" {
				return
			}
			p.startExport(b, name)
		}})
}

func (p *backupPage) startExport(b Block, name string) {
	u, d := p.u, p.u.hooks.Daemon
	u.async(func() {
		mp, err := p.mountPoint(b)
		if err != nil {
			u.tell("Could not mount "+labelOf(b), err.Error())
			return
		}
		file := strings.TrimRight(mp, "/") + "/" + name
		u.mu.Lock()
		p.started, p.exportFile = jobExport, file
		u.mu.Unlock()
		if err := d.BackupExport(file); err != nil {
			u.mu.Lock()
			p.started = ""
			u.mu.Unlock()
			u.tell("The export did not start", Describe(err))
		}
	})
}

// ---- restore ----

// restoreOffered: restoring the whole machine is offered only on a new
// machine (the daemon's can_restore), and only to an administrator.
func (p *backupPage) restoreOffered() bool {
	p.u.mu.Lock()
	defer p.u.mu.Unlock()
	return p.u.sys.Admin && p.loaded && p.status.Available && p.status.CanRestore
}

// restoreLines say where a restore is done when the page does not offer
// it.
func (p *backupPage) restoreLines() []string {
	p.u.mu.Lock()
	b, loaded := p.status, p.loaded
	p.u.mu.Unlock()
	if !p.u.sys.Admin || !loaded || !b.Available || b.CanRestore {
		return nil
	}
	lines := []string{"Restoring over a machine in use is done in a terminal: isotope restore --only homes,apps,extensions"}
	if b.RestoreRefusal != "" {
		lines = append(lines, "A whole restore is offered only on a new machine, and on this one "+b.RestoreRefusal+".")
	}
	return lines
}

// openRestore asks the password of the backup in the place the form
// describes, lists its backups, and asks which one and to confirm.
func (p *backupPage) openRestore() {
	u, d := p.u, p.u.hooks.Daemon
	if d == nil || !p.restoreOffered() || p.busyHere() {
		return
	}
	t, problem := p.formTarget()
	if problem != "" {
		u.tell("Choose where the backup is", problem)
		return
	}
	r := &p.restore
	r.pw.Editor.SetText("")
	r.focused = false
	clearPw := func() { r.pw.Editor.SetText("") }
	ok := func() bool { pw := r.pw.Editor.Text(); return pw != "" && !strings.ContainsAny(pw, "\r\n\x00") }
	u.async(func() {
		if t.disk.ID != "" {
			if _, err := p.mountPoint(t.disk); err != nil {
				u.tell("Could not mount "+labelOf(t.disk), err.Error())
				return
			}
		}
		var dlg *dialog
		dlg = newDialog("Restore this machine from a backup", "Type the password of the backup in "+t.name+".", func(gtx layout.Context) layout.Dimensions {
			if r.pw.Submitted(gtx) && ok() {
				u.pick(dlg, len(dlg.choices)-1)
			}
			if !r.focused {
				r.focused = true
				gtx.Execute(focus(&r.pw.Editor))
			}
			return fieldsCol(gtx, u, fieldRow{title: "Password", f: r.pw})
		}, 0,
			choice{Label: "Cancel", Kind: guikit.ButtonSecondary, Do: func() { clearPw(); p.form.clearSecrets() }},
			choice{Label: "Find the backups", Kind: guikit.ButtonPrimary, Enabled: ok, Do: func() {
				pw := r.pw.Editor.Text()
				clearPw()
				p.form.clearSecrets()
				p.listForRestore(t, Credentials{Password: pw, Env: t.env})
			}})
		u.open(dlg)
	})
}

func (p *backupPage) listForRestore(t setupTarget, cred Credentials) {
	u, d := p.u, p.u.hooks.Daemon
	p.setWorking("Reading the backups in " + t.name + "…")
	u.async(func() {
		// The deferred setWorking clears the busy flag even on a panic.
		snaps, err := func() ([]Snapshot, error) {
			defer p.setWorking("")
			return d.BackupList(t.target, t.listOpt(), cred)
		}()
		if err != nil {
			u.tell("Could not read the backup", Describe(err))
			return
		}
		if len(snaps) == 0 {
			u.tell("There is no backup there", t.name+" holds no backup yet.")
			return
		}
		snaps = newestFirst(snaps)
		u.mu.Lock()
		p.restore.snaps, p.restore.chosen = snaps, 0
		p.restore.clicks = make([]widget.Clickable, len(snaps))
		u.mu.Unlock()
		u.askForm("Which backup?", "The backups in "+t.name+", the newest first.", p.snapshotChoices, 1,
			choice{Label: "Cancel", Kind: guikit.ButtonSecondary},
			choice{Label: "Continue", Kind: guikit.ButtonPrimary, Do: func() { p.confirmRestore(t, cred) }})
	})
}

// snapshotChoices is the "Which backup?" form.
func (p *backupPage) snapshotChoices(gtx layout.Context) layout.Dimensions {
	u, r := p.u, &p.restore
	u.mu.Lock()
	snaps, chosen := r.snaps, r.chosen
	u.mu.Unlock()
	now := u.now()
	var c col
	for i, s := range snaps {
		if i >= len(r.clicks) {
			break
		}
		if r.clicks[i].Clicked(gtx) {
			u.mu.Lock()
			r.chosen, chosen = i, i
			u.mu.Unlock()
		}
		sub := "Backup " + shortID(s.ID)
		if s.Size > 0 {
			sub = humanSize(s.Size) + " · " + sub
		}
		if i > 0 {
			c.gap(8)
		}
		c.add(func(gtx layout.Context) layout.Dimensions {
			return u.ChoiceCard(gtx, &r.clicks[i], i == chosen, snapshotTitle(s, now), sub)
		})
	}
	return c.layout(gtx)
}

func shortID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

// confirmRestore is the last question; Cancel has the focus.
func (p *backupPage) confirmRestore(t setupTarget, cred Credentials) {
	u, d := p.u, p.u.hooks.Daemon
	u.mu.Lock()
	snaps, i := p.restore.snaps, p.restore.chosen
	u.mu.Unlock()
	if i < 0 || i >= len(snaps) {
		return
	}
	s := snaps[i]
	u.askForm("Restore this machine?", restoreBody(s, ago(u.now(), time.Unix(s.Time, 0))), nil, 0,
		choice{Label: "Cancel", Kind: guikit.ButtonSecondary},
		choice{Label: "Restore and restart", Kind: guikit.ButtonDanger, Do: func() {
			u.mu.Lock()
			p.started = jobRestore
			u.mu.Unlock()
			u.async(func() {
				if err := d.Restore(t.target, s.ID, s.Host, t.listOpt(), cred); err != nil {
					u.mu.Lock()
					p.started = ""
					u.mu.Unlock()
					u.tell("The restore did not start", Describe(err))
				}
			})
		}})
}

// ---- drawing the form ----

// diskSub is a USB file system's line: type, size, free space.
func diskSub(b Block) string {
	s := fsName(b) + " · " + humanSize(b.Size)
	if len(b.MountPoints) == 0 {
		return s + " · not mounted"
	}
	if b.Used <= b.Size {
		s += " · " + humanSize(b.Size-b.Used) + " free"
	}
	return s
}

// setupBody is the "Back up this machine" form, for an administrator.
func (p *backupPage) setupBody(gtx layout.Context, busy bool) layout.Dimensions {
	u, f := p.u, &p.form
	_, problem := p.formTarget()
	u.mu.Lock()
	sshKey, sshErr, loadingKey, connecting, copied := f.sshKey, f.sshKeyErr, f.loadingKey, f.connecting, f.copied
	confirmed, confirmedOf := f.hostKey, f.hostKeyOf
	working := p.working
	u.mu.Unlock()
	if busy || working != "" {
		gtx = gtx.Disabled()
	}
	var c col
	c.add(u.caps("BACK UP THIS MACHINE"))
	c.gap(10)
	c.add(func(gtx layout.Context) layout.Dimensions {
		return layout.Flex{}.Layout(gtx,
			layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
				return u.ChoiceCard(gtx, &f.usbClick, f.where == whereUSB, "A USB disk", "Plugged in to this computer")
			}),
			layout.Rigid(layout.Spacer{Width: 10}.Layout),
			layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
				return u.ChoiceCard(gtx, &f.remoteClick, f.where == whereRemote, "A remote server", "A REST server, S3 or SFTP")
			}),
		)
	})
	c.gap(16)
	if f.where == whereUSB {
		disks := p.backupDisks()
		on, _ := pick(disks, f.disk)
		switch {
		case u.hooks.Disks == nil:
			c.add(u.dim("USB disks cannot be listed: udisks2 is not running."))
		case len(disks) == 0:
			c.add(u.para("Plug in a USB disk."))
			c.gap(4)
			c.add(u.dim("It shows here when it is plugged in. A disk with exFAT, FAT, NTFS, ext4, Btrfs or XFS can hold backups."))
		}
		for i, b := range disks {
			k, have := f.diskClicks[b.ID]
			if !have {
				k = new(widget.Clickable)
				f.diskClicks[b.ID] = k
			}
			if i > 0 {
				c.gap(8)
			}
			c.add(func(gtx layout.Context) layout.Dimensions {
				return u.ChoiceCard(gtx, k, on.ID == b.ID, labelOf(b), diskSub(b))
			})
		}
		c.gap(12)
		c.add(func(gtx layout.Context) layout.Dimensions {
			return fieldsCol(gtx, u, fieldRow{title: "Folder", f: f.folder, hint: "On the disk. It is made if it is not there."})
		})
	} else {
		c.add(func(gtx layout.Context) layout.Dimensions {
			var kids []layout.FlexChild
			for i, k := range remoteKinds {
				if i > 0 {
					kids = append(kids, layout.Rigid(layout.Spacer{Width: 10}.Layout))
				}
				kids = append(kids, layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
					return u.ChoiceCard(gtx, &f.remoteClicks[i], f.remote == k.ID, k.Name, k.Sub)
				}))
			}
			return layout.Flex{}.Layout(gtx, kids...)
		})
		c.gap(12)
		switch f.remote {
		case remoteREST:
			c.add(func(gtx layout.Context) layout.Dimensions {
				return fieldsCol(gtx, u,
					fieldRow{title: "Address", f: f.restURL, hint: "Such as https://backup.example.com:8000/anna/"},
					fieldRow{title: "User", f: f.restUser},
					fieldRow{title: "Password", f: f.restPw})
			})
		case remoteS3:
			c.add(func(gtx layout.Context) layout.Dimensions {
				return fieldsCol(gtx, u,
					fieldRow{title: "Endpoint", f: f.s3Endpoint, hint: "Such as https://s3.amazonaws.com"},
					fieldRow{title: "Bucket", f: f.s3Bucket},
					fieldRow{title: "Path", f: f.s3Path, hint: "In the bucket. Optional."},
					fieldRow{title: "Access key ID", f: f.s3Key},
					fieldRow{title: "Secret access key", f: f.s3Secret},
					fieldRow{title: "Region", f: f.s3Region, hint: "Optional."})
			})
		case remoteSFTP:
			c.add(func(gtx layout.Context) layout.Dimensions {
				return fieldsCol(gtx, u,
					fieldRow{title: "User and server", f: f.sftpUserHost, hint: "Such as anna@nas.local"},
					fieldRow{title: "Port", f: f.sftpPort},
					fieldRow{title: "Folder on the server", f: f.sftpPath, hint: "Such as /srv/backup"})
			})
			c.gap(14)
			switch {
			case sshErr != "":
				c.add(u.errorBox(sshErr))
			case sshKey == "" || loadingKey:
				c.add(u.spinnerLine("Reading the backup's SSH key…"))
			default:
				c.add(u.para("Add this key to the server's authorized_keys, then Connect."))
				c.gap(8)
				c.add(u.panel(func(gtx layout.Context) layout.Dimensions {
					return u.LabelLines(gtx, guikit.StMono12, guikit.ColText, 1.3, sshKey)
				}))
				c.gap(10)
				c.add(u.buttons(
					guikit.Action{Click: &f.copyClick, Kind: guikit.ButtonSecondary, Label: "Copy"},
					guikit.Action{Click: &f.connectClick, Kind: guikit.ButtonSecondary, Label: "Connect", Disabled: connecting}))
				if copied {
					c.gap(6)
					c.add(u.dim("Copied."))
				}
			}
			if connecting {
				c.gap(8)
				c.add(u.spinnerLine("Fetching the server's key…"))
			}
			if confirmed != "" {
				c.gap(8)
				c.add(u.dim("Key of " + confirmedOf + " confirmed: " + confirmed))
			}
		}
	}
	c.gap(14)
	c.add(func(gtx layout.Context) layout.Dimensions {
		return u.CheckCard(gtx, &f.containersClick, f.containers, "Include Docker and Podman storage", "Images, containers and volumes. They can be large.")
	})
	if problem != "" {
		c.gap(8)
		c.add(u.dim(problem))
	}
	if working != "" {
		c.gap(12)
		c.add(u.spinnerLine(working))
	}
	return c.layout(gtx)
}

// updateSetup handles the form's clicks.
func (p *backupPage) updateSetup(gtx layout.Context) {
	u, f := p.u, &p.form
	if f.usbClick.Clicked(gtx) {
		f.where = whereUSB
	}
	if f.remoteClick.Clicked(gtx) {
		p.pickRemote(f.remote)
	}
	for i := range f.remoteClicks {
		if f.remoteClicks[i].Clicked(gtx) {
			p.pickRemote(remoteKinds[i].ID)
		}
	}
	for id, k := range f.diskClicks {
		if k.Clicked(gtx) {
			p.pickDisk(id)
		}
	}
	if f.containersClick.Clicked(gtx) {
		f.containers = !f.containers
	}
	if f.copyClick.Clicked(gtx) {
		if key := p.publicKey(); key != "" {
			gtx.Execute(copyCmd(key))
			u.mu.Lock()
			f.copied = true
			u.mu.Unlock()
		}
	}
	if f.connectClick.Clicked(gtx) {
		p.connect()
	}
	if f.restoreClick.Clicked(gtx) {
		p.openRestore()
	}
}
