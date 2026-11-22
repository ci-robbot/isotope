package main

import (
	"gioui.org/layout"
	"gioui.org/widget"

	"github.com/kairos-community/isotope/guikit"
)

func init() {
	pageMakers[PageBackup] = func(u *UI) pageView {
		p := &backupPage{u: u}
		u.onJobEnd(p.jobEnded)
		return p
	}
}

// backupPage: where and when this machine backs up, the last backup, Back
// up now and the schedule, through isotope-updated (BackupStatus,
// BackupNow, SetBackupSchedule). Choosing a target and restoring are the
// CLI's until phase 2 of the design.
type backupPage struct {
	u    *UI
	list widget.List

	// Under u.mu.
	status  BackupStatus
	loaded  bool
	loadErr string
	started bool // this window started the running backup

	now      widget.Clickable
	schedule [3]widget.Clickable
}

var schedules = []struct{ ID, Name, Sub string }{
	{"off", "Off", "Only when you click Back up now"},
	{"daily", "Daily", "Once a day, when the disk is there"},
	{"weekly", "Weekly", "Once a week, when the disk is there"},
}

func (p *backupPage) Shown() { p.load() }

func (p *backupPage) load() {
	u, d := p.u, p.u.hooks.Daemon
	if d == nil {
		return
	}
	u.async(func() {
		b, err := d.BackupStatus()
		u.mu.Lock()
		defer u.mu.Unlock()
		if err != nil {
			p.loadErr = "Could not read the backup settings: " + Describe(err)
			return
		}
		p.status, p.loaded, p.loadErr = b, true, ""
	})
}

func (p *backupPage) jobEnded(prev string, st Status) {
	if prev != StateBackup {
		return
	}
	u := p.u
	u.mu.Lock()
	mine := p.started
	p.started = false
	u.mu.Unlock()
	if mine {
		switch st.BackupResult {
		case "ok":
			u.tell("Backed up", "This machine is backed up.")
		case "partial":
			u.tell("Backed up, with gaps", st.BackupDetail)
		case "failed":
			u.tell("The backup failed", st.BackupDetail)
		}
	}
	p.load()
}

func (p *backupPage) Update(gtx layout.Context) {
	u, d := p.u, p.u.hooks.Daemon
	if d == nil {
		return
	}
	if p.now.Clicked(gtx) {
		u.mu.Lock()
		p.started = true
		u.mu.Unlock()
		u.async(func() {
			if err := d.BackupNow(); err != nil {
				u.mu.Lock()
				p.started = false
				u.mu.Unlock()
				u.tell("The backup did not start", Describe(err))
			}
		})
	}
	for i := range p.schedule {
		if p.schedule[i].Clicked(gtx) {
			s := schedules[i].ID
			u.async(func() {
				if err := d.SetBackupSchedule(s); err != nil {
					u.tell("Could not change the schedule", Describe(err))
				}
				p.load()
			})
		}
	}
}

func (p *backupPage) Footer() (leading, trailing []guikit.Action) {
	st, ok, _ := p.u.status()
	p.u.mu.Lock()
	v := BackupViewOf(p.status, p.u.now())
	loaded := p.loaded
	p.u.mu.Unlock()
	if !loaded || v.Setup || !p.status.Available {
		return nil, nil
	}
	return nil, []guikit.Action{{Click: &p.now, Kind: guikit.ButtonPrimary, Label: "Back up now",
		Disabled: !ok || st.Busy() || !v.CanBackUp || !p.u.sys.Admin}}
}

func (p *backupPage) Body(gtx layout.Context) layout.Dimensions {
	u := p.u
	st, ok, why := u.status()
	u.mu.Lock()
	b, loaded, loadErr := p.status, p.loaded, p.loadErr
	u.mu.Unlock()
	v := BackupViewOf(b, u.now())
	lines := u.progress()
	return u.scrolled(gtx, &p.list, func(gtx layout.Context) layout.Dimensions {
		var c col
		c.add(u.Heading("Backup", "A backup keeps what survives a restart: your home folders, the system settings, the list of your apps and extensions. It is encrypted with its own password."))
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
			c.add(u.spinnerLine("Reading the backup settings…"))
			return c.layout(gtx)
		}
		if v.Warning != "" {
			c.add(u.notice(v.Warning))
			c.gap(14)
		}
		if !u.sys.Admin {
			c.add(u.notice("Only an administrator (a member of the admin group) can back up this machine."))
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
		if st.State == StateBackup {
			c.gap(16)
			c.add(u.spinnerLine("Backing up this machine…"))
			c.gap(10)
			c.add(u.log(lines, 8))
		}
		if v.Setup {
			c.gap(16)
			c.add(u.para("To choose where backups go (a folder on a USB disk, or a server restic can reach), run this once in a terminal:"))
			c.gap(10)
			c.add(u.panel(func(gtx layout.Context) layout.Dimensions {
				return u.Label(gtx, guikit.StMono13, guikit.ColText, "isotope backup init /run/media/$USER/DISK/isotope-backup")
			}))
			c.gap(8)
			c.add(u.dim("Choosing the disk here is coming in a later version of this page."))
			return c.layout(gtx)
		}
		if !b.Available {
			return c.layout(gtx)
		}
		c.gap(28)
		c.add(u.caps("AUTOMATIC BACKUPS"))
		c.gap(10)
		c.add(func(gtx layout.Context) layout.Dimensions {
			var kids []layout.FlexChild
			for i, s := range schedules {
				if i > 0 {
					kids = append(kids, layout.Rigid(layout.Spacer{Width: 10}.Layout))
				}
				kids = append(kids, layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
					if !u.sys.Admin || st.State == StateBackup {
						gtx = gtx.Disabled()
					}
					on := b.Schedule == s.ID || (s.ID == "off" && b.Schedule == "")
					return u.ChoiceCard(gtx, &p.schedule[i], on, s.Name, s.Sub)
				}))
			}
			return layout.Flex{}.Layout(gtx, kids...)
		})
		c.gap(10)
		c.add(u.dim("Restoring a backup is done with isotope restore in a terminal for now (isotope restore help)."))
		return c.layout(gtx)
	})
}
