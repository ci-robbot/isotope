package main

import (
	"io"
	"strings"

	"gioui.org/io/clipboard"
	"gioui.org/layout"
	"gioui.org/widget"

	"github.com/kairos-community/isotope/pkg/guikit"
	"github.com/kairos-community/isotope/pkg/i18n"
)

func init() {
	pageMakers[PageBackup] = func(u *UI) pageView {
		p := &backupPage{u: u, roots: rootDevices, form: newSetupForm(),
			export:  exportForm{name: guikit.NewField(false), clicks: map[string]*widget.Clickable{}},
			restore: restoreForm{pw: guikit.NewSecret()}}
		u.onJobEnd(p.jobEnded)
		return p
	}
}

// backupPage: where and when this machine backs up, through isotope-updated.
// Without a target, an administrator chooses one (a USB disk or a server,
// backup_setup.go). With one: the status, Back up now, the schedule, the
// export to a file and the list of backups. A whole restore is offered only
// on a new machine; on others the page names the CLI command.
type backupPage struct {
	u    *UI
	list widget.List

	// Under u.mu.
	status  BackupStatus
	loaded  bool
	loadErr string
	// started is the job this window started: jobBackup, jobExport,
	// jobRestore, or "".
	started    string
	exportFile string
	working    string

	snaps       []Snapshot
	snapsLoaded bool
	snapsErr    string

	// roots reads the devices of the running system (rootDevices; the
	// tests give their own).
	roots       func() ([]string, error)
	drives      []Drive
	rootDevs    []string
	disksLoaded bool
	watching    bool

	form    setupForm
	export  exportForm
	restore restoreForm

	now, cancel, exportBtn, contBtn widget.Clickable
	schedule                        [3]widget.Clickable
}

var schedules = []struct{ ID, Name, Sub string }{
	{"off", i18n.Mark("Off"), i18n.Mark("Only when you click Back up now")},
	{"daily", i18n.Mark("Daily"), i18n.Mark("Once a day, when the disk is there")},
	{"weekly", i18n.Mark("Weekly"), i18n.Mark("Once a week, when the disk is there")},
}

func (p *backupPage) Shown() {
	p.load()
	if p.u.sys.Admin {
		p.watchDisks()
		p.loadDisks()
	}
}

func (p *backupPage) setRoots(f func() ([]string, error)) {
	p.u.mu.Lock()
	p.roots = f
	p.u.mu.Unlock()
}

// load reads the status, then the list of backups when there is a target
// the daemon can reach.
func (p *backupPage) load() {
	u, d := p.u, p.u.hooks.Daemon
	if d == nil {
		return
	}
	u.async(func() {
		b, err := d.BackupStatus()
		u.mu.Lock()
		if err != nil {
			p.loadErr = i18n.Tf("Could not read the backup settings: %s", Describe(err))
			u.mu.Unlock()
			return
		}
		p.status, p.loaded, p.loadErr = b, true, ""
		list := u.sys.Admin && b.Available && b.Target != "" && b.Present
		if !list {
			p.snaps, p.snapsLoaded, p.snapsErr = nil, false, ""
		}
		u.mu.Unlock()
		if !list {
			return
		}
		snaps, err := d.BackupList("", TargetOptions{}, Credentials{})
		u.mu.Lock()
		defer u.mu.Unlock()
		p.snapsLoaded, p.snapsErr = true, ""
		if err != nil {
			p.snapsErr = i18n.Tf("Could not list the backups: %s", Describe(err))
			return
		}
		p.snaps = snaps
	})
}

// watchDisks reads the disks again on every change udisks2 signals, for
// the life of the window.
func (p *backupPage) watchDisks() {
	u, ds := p.u, p.u.hooks.Disks
	if ds == nil {
		return
	}
	u.mu.Lock()
	if p.watching {
		u.mu.Unlock()
		return
	}
	p.watching = true
	u.mu.Unlock()
	go func() {
		changed, stop, err := ds.Watch()
		if err != nil {
			u.mu.Lock()
			p.watching = false
			u.mu.Unlock()
			return
		}
		defer stop()
		for range changed {
			p.loadDisks()
		}
	}()
}

func (p *backupPage) loadDisks() {
	u, ds := p.u, p.u.hooks.Disks
	if ds == nil {
		return
	}
	u.mu.Lock()
	read := p.roots
	u.mu.Unlock()
	u.async(func() {
		roots, _ := read() // on an error roots is everyDevice: no disk is offered
		list, err := ds.Drives()
		u.mu.Lock()
		defer u.mu.Unlock()
		if err != nil {
			return
		}
		p.drives, p.rootDevs, p.disksLoaded = list, roots, true
	})
}

func (p *backupPage) snapshotRows() []string {
	p.u.mu.Lock()
	snaps := p.snaps
	p.u.mu.Unlock()
	return SnapshotRows(snaps, p.u.now())
}

func (p *backupPage) jobEnded(prev string, st Status) {
	if prev != StateBackup && prev != StateRestore {
		return
	}
	u := p.u
	u.mu.Lock()
	mine, file := p.started, p.exportFile
	p.started = ""
	u.mu.Unlock()
	switch mine {
	case jobBackup:
		switch st.BackupResult {
		case "ok":
			u.tell(i18n.T("Backed up"), i18n.T("This machine is backed up."))
		case "partial":
			u.tell(i18n.T("Backed up, with gaps"), st.BackupDetail)
		case "failed":
			u.tell(i18n.T("The backup failed"), st.BackupDetail)
		}
	case jobExport:
		switch st.BackupResult {
		case "ok":
			u.tell(i18n.T("Exported"), i18n.Tf("The backup is in %s.", file))
		case "partial":
			u.tell(i18n.T("Exported, with gaps"), st.BackupDetail)
		case "failed":
			u.tell(i18n.T("The export failed"), st.BackupDetail)
		}
	case jobRestore:
		if st.BackupResult == "failed" {
			u.tell(i18n.T("The restore failed"), st.BackupDetail)
		}
	}
	p.load()
}

// cancelJob stops the backup or export this window started.
func (p *backupPage) cancelJob() {
	u, d := p.u, p.u.hooks.Daemon
	if d == nil {
		return
	}
	u.async(func() {
		if err := d.BackupCancel(); err != nil {
			u.tell(i18n.T("Could not cancel"), Describe(err))
		}
	})
}

// copyCmd puts s on the clipboard.
func copyCmd(s string) clipboard.WriteCmd {
	return clipboard.WriteCmd{Type: "application/text", Data: io.NopCloser(strings.NewReader(s))}
}

func (p *backupPage) Update(gtx layout.Context) {
	u, d := p.u, p.u.hooks.Daemon
	if d == nil {
		return
	}
	if p.now.Clicked(gtx) && u.sys.Admin {
		u.mu.Lock()
		p.started = jobBackup
		u.mu.Unlock()
		u.async(func() {
			if err := d.BackupNow(); err != nil {
				u.mu.Lock()
				p.started = ""
				u.mu.Unlock()
				u.tell(i18n.T("The backup did not start"), Describe(err))
			}
		})
	}
	if p.cancel.Clicked(gtx) {
		p.cancelJob()
	}
	if p.exportBtn.Clicked(gtx) {
		p.openExport()
	}
	if p.contBtn.Clicked(gtx) {
		p.cont()
	}
	for i := range p.schedule {
		if p.schedule[i].Clicked(gtx) {
			s := schedules[i].ID
			u.async(func() {
				if err := d.SetBackupSchedule(s); err != nil {
					u.tell(i18n.T("Could not change the schedule"), Describe(err))
				}
				p.load()
			})
		}
	}
	if u.sys.Admin {
		p.updateSetup(gtx)
	}
}

// mineRunning: the backup or export this window started runs.
func (p *backupPage) mineRunning(st Status) bool {
	p.u.mu.Lock()
	defer p.u.mu.Unlock()
	return st.State == StateBackup && (p.started == jobBackup || p.started == jobExport)
}

func (p *backupPage) Footer() (leading, trailing []guikit.Action) {
	u := p.u
	st, ok, _ := u.status()
	u.mu.Lock()
	v := BackupViewOf(p.status, u.now())
	loaded, available, working := p.loaded, p.status.Available, p.working
	u.mu.Unlock()
	if !loaded || !available {
		return nil, nil
	}
	if p.mineRunning(st) {
		leading = []guikit.Action{{Click: &p.cancel, Kind: guikit.ButtonSecondary, Label: i18n.T("Cancel")}}
	}
	if v.Setup {
		if !u.sys.Admin {
			return leading, nil
		}
		return leading, []guikit.Action{{Click: &p.contBtn, Kind: guikit.ButtonPrimary, Label: i18n.T("Continue"),
			Disabled: !ok || st.Busy() || working != "" || !p.ready()}}
	}
	return leading, []guikit.Action{{Click: &p.now, Kind: guikit.ButtonPrimary, Label: i18n.T("Back up now"),
		Disabled: !ok || st.Busy() || !v.CanBackUp || !u.sys.Admin}}
}

func (p *backupPage) Body(gtx layout.Context) layout.Dimensions {
	u := p.u
	st, ok, why := u.status()
	u.mu.Lock()
	b, loaded, loadErr, mine := p.status, p.loaded, p.loadErr, p.started
	snapsLoaded, snapsErr := p.snapsLoaded, p.snapsErr
	u.mu.Unlock()
	v := BackupViewOf(b, u.now())
	rows := p.snapshotRows()
	lines := u.progress()
	offered := p.restoreOffered()
	restoreHint := p.restoreLines()
	admin := u.sys.Admin
	return u.scrolled(gtx, &p.list, func(gtx layout.Context) layout.Dimensions {
		var c col
		c.add(u.Heading(i18n.T("Backup"), i18n.T("A backup keeps what survives a restart: your home folders, the system settings, the list of your apps and extensions. It is encrypted with its own password.")))
		c.gap(24)
		if !ok {
			c.add(u.errorBox(orUnknown(why)))
			return c.layout(gtx)
		}
		if loadErr != "" {
			c.add(u.errorBox(loadErr))
			return c.layout(gtx)
		}
		if !loaded {
			c.add(u.spinnerLine(i18n.T("Reading the backup settings…")))
			return c.layout(gtx)
		}
		if v.Warning != "" {
			c.add(u.notice(v.Warning))
			c.gap(14)
		}
		if !admin {
			c.add(u.notice(i18n.T("Only an administrator (a member of the admin group) can back up this machine.")))
			c.gap(14)
		}
		for i, l := range v.Lines {
			if i == 0 {
				c.add(u.lead(l))
				c.gap(6)
				continue
			}
			c.add(u.dim(l))
			c.gap(2)
		}
		switch st.State {
		case StateBackup:
			txt := i18n.T("Backing up this machine…")
			if mine == jobExport {
				txt = i18n.T("Exporting the backup…")
			}
			c.gap(16)
			c.add(u.spinnerLine(txt))
			c.gap(10)
			c.add(u.log(lines, 8))
		case StateRestore:
			c.gap(16)
			c.add(u.spinnerLine(i18n.T("Restoring this machine…")))
			c.gap(10)
			c.add(u.log(lines, 8))
		}
		if !b.Available {
			return c.layout(gtx)
		}
		if v.Setup {
			if !admin {
				return c.layout(gtx)
			}
			c.gap(24)
			c.add(func(gtx layout.Context) layout.Dimensions { return p.setupBody(gtx, st.Busy()) })
			if offered {
				c.gap(28)
				c.add(u.caps(i18n.T("Restore")))
				c.gap(10)
				c.add(u.para(i18n.T("This machine is new, so it can become the machine of a backup. Choose where the backup is above, then:")))
				c.gap(10)
				c.add(u.buttons(guikit.Action{Click: &p.form.restoreClick, Kind: guikit.ButtonSecondary,
					Label: i18n.T("Restore this machine from a backup…"), Disabled: st.Busy() || p.busyHere() || !p.ready()}))
			}
			p.addRestoreHint(&c, restoreHint)
			return c.layout(gtx)
		}
		c.gap(28)
		c.add(u.caps(i18n.T("Automatic backups")))
		c.gap(10)
		c.add(func(gtx layout.Context) layout.Dimensions {
			var kids []layout.FlexChild
			for i, s := range schedules {
				if i > 0 {
					kids = append(kids, layout.Rigid(layout.Spacer{Width: 10}.Layout))
				}
				kids = append(kids, layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
					if !admin || st.State == StateBackup {
						gtx = gtx.Disabled()
					}
					on := b.Schedule == s.ID || (s.ID == "off" && b.Schedule == "")
					return u.ChoiceCard(gtx, &p.schedule[i], on, i18n.T(s.Name), i18n.T(s.Sub))
				}))
			}
			return layout.Flex{}.Layout(gtx, kids...)
		})
		c.gap(16)
		c.add(u.buttons(p.exportAction(st)))
		if !admin {
			return c.layout(gtx)
		}
		c.gap(28)
		c.add(u.caps(i18n.T("Backups")))
		c.gap(10)
		switch {
		case !b.Present:
			c.add(u.dim(i18n.Tf("Plug in %s to see its backups.", backupDiskName(b))))
		case snapsErr != "":
			c.add(u.errorBox(snapsErr))
		case !snapsLoaded:
			c.add(u.spinnerLine(i18n.T("Reading the backups…")))
		case len(rows) == 0:
			c.add(u.dim(i18n.T("No backups yet.")))
		default:
			c.add(u.panel(func(gtx layout.Context) layout.Dimensions {
				var rc col
				for i, r := range rows {
					if i > 0 {
						rc.gap(6)
					}
					rc.add(func(gtx layout.Context) layout.Dimensions {
						return u.LabelMax(gtx, guikit.StBody, u.Colors().Text2, 1, r)
					})
				}
				return rc.layout(gtx)
			}))
		}
		p.addRestoreHint(&c, restoreHint)
		return c.layout(gtx)
	})
}

// canExport: there is a target and it is there. A USB disk that is not
// plugged in has nothing to export, as Back up now.
func (p *backupPage) canExport() bool {
	p.u.mu.Lock()
	defer p.u.mu.Unlock()
	b := p.status
	return p.loaded && b.Available && b.Target != "" && b.Present
}

// exportAction is the "Export to a file…" button.
func (p *backupPage) exportAction(st Status) guikit.Action {
	return guikit.Action{Click: &p.exportBtn, Kind: guikit.ButtonSecondary, Label: i18n.T("Export to a file…"),
		Disabled: !p.u.sys.Admin || st.Busy() || !p.canExport()}
}

// addRestoreHint adds the lines that say restore is a CLI job here.
func (p *backupPage) addRestoreHint(c *col, lines []string) {
	for i, l := range lines {
		if i == 0 {
			c.gap(20)
			c.add(p.u.para(l))
			continue
		}
		c.gap(4)
		c.add(p.u.dim(l))
	}
}
