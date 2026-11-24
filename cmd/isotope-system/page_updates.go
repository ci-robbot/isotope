package main

import (
	"strings"

	"gioui.org/layout"
	"gioui.org/widget"

	"github.com/kairos-community/isotope/pkg/guikit"
)

func init() {
	pageMakers[PageUpdates] = func(u *UI) pageView {
		p := &updatesPage{u: u}
		u.onJobEnd(func(string, Status) { p.loadHistory(); p.loadAuto() })
		return p
	}
}

// updatesPage: the installed release, Check now, install the latest or
// another release, the progress of a running install, restart, rollback,
// in advanced mode any image, and the firmware of the machine and its
// devices (fwupd).
type updatesPage struct {
	u    *UI
	list widget.List

	// tags are the published releases (Releases), under u.mu.
	tags     []string
	checking bool
	checkErr string
	// history is UpdateHistory, oldest first, under u.mu.
	history []HistoryEntry

	// auto is AutoUpdateStatus, under u.mu; autoWorking is the change
	// that runs.
	auto                                                autoInput
	autoWorking                                         string
	autoToggle                                          widget.Clickable
	check, install, cancel, restart, rollback, advanced widget.Clickable
	others                                              []widget.Clickable
	image                                               *guikit.Field

	// firm is the FIRMWARE section (firmware.go), under u.mu.
	firm firmwareState
}

func (p *updatesPage) Shown() {
	if p.image == nil {
		p.image = guikit.NewField(true)
	}
	p.refresh(false)
	p.loadHistory()
	p.loadAuto()
	p.loadFirmware()
}

// loadAuto reads the automatic update setting and its last run. Anyone
// may read them.
func (p *updatesPage) loadAuto() {
	u, d := p.u, p.u.hooks.Daemon
	if d == nil {
		return
	}
	u.async(func() {
		a, err := d.AutoUpdateStatus()
		u.mu.Lock()
		defer u.mu.Unlock()
		p.auto.Loaded = true
		p.auto.Err = ""
		if err != nil {
			p.auto.Err = "Could not read the automatic update setting: " + Describe(err)
			return
		}
		p.auto.Auto = a
	})
}

// autoInput is the section's input now.
func (p *updatesPage) autoInputLocked() autoInput {
	in := p.auto
	in.Admin, in.Now = p.u.sys.Admin, p.u.now()
	return in
}

// setAuto turns automatic updates on or off and reads the setting again.
func (p *updatesPage) setAuto(on bool) {
	u, d := p.u, p.u.hooks.Daemon
	if d == nil || !u.sys.Admin {
		return
	}
	u.mu.Lock()
	p.autoWorking = map[bool]string{true: "Turning automatic updates on…", false: "Turning automatic updates off…"}[on]
	u.mu.Unlock()
	u.async(func() {
		err := d.SetAutoUpdate(on)
		u.mu.Lock()
		p.autoWorking = ""
		u.mu.Unlock()
		if err != nil {
			u.tell("Automatic updates", Describe(err))
		}
		p.loadAuto()
	})
}

// loadHistory reads the install history. Anyone may read it.
func (p *updatesPage) loadHistory() {
	u, d := p.u, p.u.hooks.Daemon
	if d == nil {
		return
	}
	u.async(func() {
		h, err := d.UpdateHistory()
		if err != nil {
			return
		}
		u.mu.Lock()
		p.history = h
		u.mu.Unlock()
	})
}

// refresh lists the releases; with check it first has the daemon check
// for a new one (the tray's own check runs on its schedule).
func (p *updatesPage) refresh(check bool) {
	u, d := p.u, p.u.hooks.Daemon
	if d == nil {
		return
	}
	u.mu.Lock()
	if p.checking {
		u.mu.Unlock()
		return
	}
	p.checking, p.checkErr = true, ""
	u.mu.Unlock()
	u.async(func() {
		var err error
		if check {
			err = d.Check()
		}
		tags, lerr := d.Releases()
		u.mu.Lock()
		defer u.mu.Unlock()
		p.checking = false
		if lerr == nil {
			p.tags = tags
		}
		switch {
		case err != nil:
			p.checkErr = "Could not check for updates: " + Describe(err)
		case lerr != nil:
			p.checkErr = "Could not list the releases: " + Describe(lerr)
		}
	})
}

func (p *updatesPage) offer() (UpdateOffer, Status, bool, string) {
	st, ok, why := p.u.status()
	p.u.mu.Lock()
	tags := append([]string(nil), p.tags...)
	p.u.mu.Unlock()
	return OfferFor(st, tags, p.u.sys.Release.Line()), st, ok, why
}

func (p *updatesPage) Update(gtx layout.Context) {
	u := p.u
	o, _, _, _ := p.offer()
	if p.check.Clicked(gtx) {
		p.refresh(true)
	}
	if p.install.Clicked(gtx) && o.Latest != "" && o.CanInstall {
		p.installTag(o.Latest)
	}
	for i := range p.others {
		if p.others[i].Clicked(gtx) && i < len(o.Others) && o.CanInstall {
			p.installTag(o.Others[i])
		}
	}
	u.mu.Lock()
	av, autoOn, working := autoViewOf(p.autoInputLocked()), p.auto.Auto.Enabled && p.auto.Auto.Broken == "", p.autoWorking
	u.mu.Unlock()
	if p.autoToggle.Clicked(gtx) && av.Enabled && av.Toggle != "" && working == "" {
		p.setAuto(!autoOn)
	}
	if p.cancel.Clicked(gtx) {
		p.call("Cannot cancel", func(d Daemon) error { return d.Cancel() })
	}
	if p.restart.Clicked(gtx) {
		u.reboot()
	}
	p.updateFirmwareClicks(gtx)
	if p.rollback.Clicked(gtx) {
		if u.writingFirmware() {
			u.tell("Cannot restart", firmwareWritingText)
		} else {
			u.ask("Restart into the previous system?",
				"The computer restarts into the system you had before the last update, for the next boot only. The one after starts this system again.",
				choice{Label: "Cancel", Kind: guikit.ButtonSecondary},
				choice{Label: "Restart", Kind: guikit.ButtonPrimary, Do: func() {
					if u.writingFirmware() {
						u.tell("Cannot restart", firmwareWritingText)
						return
					}
					p.call("Cannot restart into the previous system", func(d Daemon) error { return d.RebootPrevious() })
				}})
		}
	}
	submitted := p.image != nil && p.image.Submitted(gtx)
	if (p.advanced.Clicked(gtx) || submitted) && o.Advanced && o.CanInstall {
		if ref := strings.TrimSpace(p.image.Editor.Text()); ref != "" {
			u.startInstall(ref, ref, true, "")
		}
	}
}

func (p *updatesPage) installTag(tag string) {
	ref, err := releaseRef(p.u.sys.Release, p.u.sys.Release.Flavor, p.u.sys.Release.Line(), tag)
	if err != nil {
		p.u.tell("Cannot install "+tag, err.Error())
		return
	}
	p.u.startInstall("Isotope "+tag, ref, false, "")
}

// call runs a daemon call that needs no answer and says why it failed.
func (p *updatesPage) call(title string, f func(Daemon) error) {
	d := p.u.hooks.Daemon
	if d == nil {
		return
	}
	p.u.async(func() {
		if err := f(d); err != nil {
			p.u.tell(title, Describe(err))
		}
	})
}

func (p *updatesPage) Footer() (leading, trailing []guikit.Action) {
	o, st, ok, _ := p.offer()
	p.u.mu.Lock()
	checking := p.checking
	p.u.mu.Unlock()
	label := "Check now"
	if checking {
		label = "Checking…"
	}
	trailing = append(trailing, guikit.Action{Click: &p.check, Kind: guikit.ButtonSecondary, Label: label,
		Disabled: !ok || checking || st.Busy()})
	if o.Latest != "" {
		trailing = append(trailing, guikit.Action{Click: &p.install, Kind: guikit.ButtonPrimary,
			Label: "Install " + o.Latest, Disabled: !o.CanInstall || !p.u.sys.Admin})
	}
	if o.Restart {
		trailing = append(trailing, guikit.Action{Click: &p.restart, Kind: guikit.ButtonPrimary, Label: "Restart now",
			Disabled: p.u.writingFirmware()})
	}
	return nil, trailing
}

func (p *updatesPage) Body(gtx layout.Context) layout.Dimensions {
	u := p.u
	o, st, ok, why := p.offer()
	lines := u.progress()
	u.mu.Lock()
	checkErr := p.checkErr
	history := append([]HistoryEntry(nil), p.history...)
	u.mu.Unlock()
	return u.scrolled(gtx, &p.list, func(gtx layout.Context) layout.Dimensions {
		var c col
		c.add(u.Heading("Updates", "Isotope updates as a whole image: the running system is not touched, the new one starts at the next boot, and the previous one stays in the boot menu."))
		c.gap(24)
		if u.sys.ReleaseErr != "" {
			c.add(u.errorBox("This system has no Isotope release file: " + u.sys.ReleaseErr))
			return c.layout(gtx)
		}
		if !ok {
			c.add(u.errorBox(orUnknown(why)))
			return c.layout(gtx)
		}
		c.add(u.lead(o.Headline))
		sub := "Installed: Isotope " + orUnknown(st.Installed) + " · " + lineLabels[u.sys.Release.Line()]
		if st.Pin != "" {
			sub += " · pinned to " + st.Pin + " (installing the latest release removes the pin)"
		}
		c.gap(6)
		c.add(u.dim(sub))
		if st.State == StateError && st.Detail != "" {
			c.gap(12)
			c.add(u.errorBox(st.Detail))
		}
		if st.State == StateRebootPending && st.Detail != "" {
			c.gap(12)
			c.add(u.notice(st.Detail))
		}
		if checkErr != "" {
			c.gap(12)
			c.add(u.errorBox(checkErr))
		}
		if s := u.installing(); s != "" {
			c.gap(16)
			c.add(u.spinnerLine(s))
		}
		if st.State == StateInspecting || st.State == StateApplying {
			c.gap(16)
			c.add(u.spinnerLine(headline(st)))
			c.gap(10)
			c.add(u.log(lines, 8))
			if o.Cancel {
				c.gap(10)
				c.add(u.buttons(guikit.Action{Click: &p.cancel, Kind: guikit.ButtonSecondary, Label: "Cancel the update"}))
			}
		}
		if !u.sys.Admin {
			c.gap(16)
			c.add(u.notice("Only an administrator (a member of the admin group) can install updates. You can still check for them."))
		}
		c.gap(28)
		c.add(p.autoSection())
		if u.hooks.Firmware != nil {
			c.gap(28)
			c.add(p.firmwareSection())
		}
		if len(o.Others) > 0 {
			if len(p.others) < len(o.Others) {
				p.others = make([]widget.Clickable, len(o.Others))
			}
			c.gap(28)
			c.add(u.caps("OTHER RELEASES"))
			c.gap(4)
			c.add(u.dim("Installing an older release pins the system to it, until you install the latest again."))
			c.gap(10)
			c.add(u.panel(func(gtx layout.Context) layout.Dimensions {
				var rows col
				for i, tag := range o.Others {
					if i > 0 {
						rows.gap(8)
					}
					rows.add(u.row("Isotope "+tag, "", u.buttons(guikit.Action{Click: &p.others[i], Kind: guikit.ButtonSecondary,
						Label: "Install", Disabled: !o.CanInstall || !u.sys.Admin})))
				}
				return rows.layout(gtx)
			}))
		}
		if o.Advanced {
			c.gap(28)
			c.add(u.caps("INSTALL AN IMAGE"))
			c.gap(4)
			c.add(u.dim("Advanced mode is on: any OCI image can be installed. An image that is not a published Isotope release is asked about first."))
			c.gap(10)
			c.add(func(gtx layout.Context) layout.Dimensions {
				return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
					layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
						return u.Input(gtx, p.image, guikit.StMono13, 38, "", nil)
					}),
					layout.Rigid(layout.Spacer{Width: 10}.Layout),
					layout.Rigid(u.buttons(guikit.Action{Click: &p.advanced, Kind: guikit.ButtonSecondary, Label: "Install…",
						Disabled: !o.CanInstall || !u.sys.Admin})),
				)
			})
		}
		if hl := HistoryLines(history, st.Installed, u.now()); len(hl) > 0 {
			c.gap(28)
			c.add(u.caps("HISTORY"))
			c.gap(10)
			c.add(u.panel(func(gtx layout.Context) layout.Dimensions {
				var rows col
				for i, l := range hl {
					if i > 0 {
						rows.gap(6)
					}
					rows.add(u.dim(l))
				}
				return rows.layout(gtx)
			}))
		}
		if o.Rollback {
			c.gap(28)
			c.add(u.caps("PREVIOUS SYSTEM"))
			c.gap(10)
			c.add(u.row("Restart into the previous system", "For the next boot only: the system you had before the last update.",
				u.buttons(guikit.Action{Click: &p.rollback, Kind: guikit.ButtonSecondary, Label: "Restart…", Disabled: !u.sys.Admin || u.writingFirmware()})))
		}
		return c.layout(gtx)
	})
}

// autoSection is the AUTOMATIC UPDATES heading and its panel.
func (p *updatesPage) autoSection() layout.Widget {
	u := p.u
	u.mu.Lock()
	v, working := autoViewOf(p.autoInputLocked()), p.autoWorking
	u.mu.Unlock()
	return func(gtx layout.Context) layout.Dimensions {
		var c col
		c.add(u.caps("AUTOMATIC UPDATES"))
		c.gap(10)
		c.add(u.panel(func(gtx layout.Context) layout.Dimensions {
			var rows col
			var right layout.Widget
			if v.Toggle != "" {
				right = u.buttons(guikit.Action{Click: &p.autoToggle, Kind: guikit.ButtonSecondary, Label: v.Toggle,
					Disabled: !v.Enabled || working != ""})
			}
			rows.add(u.row("Install updates automatically", v.Sub, right))
			if working != "" {
				rows.gap(8)
				rows.add(u.spinnerLine(working))
			}
			if v.Last != "" {
				rows.gap(8)
				rows.add(u.dim(v.Last))
			}
			if v.Hint != "" {
				rows.gap(8)
				rows.add(u.dim(v.Hint))
			}
			return rows.layout(gtx)
		}))
		return c.layout(gtx)
	}
}
