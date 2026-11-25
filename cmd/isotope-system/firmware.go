package main

import (
	"context"
	"net/http"
	"sync"
	"time"

	"gioui.org/layout"
	"gioui.org/widget"

	"github.com/kairos-community/isotope/pkg/fwupd"
	"github.com/kairos-community/isotope/pkg/guikit"
	"github.com/kairos-community/isotope/pkg/i18n"
)

// The FIRMWARE section of the Updates page: fwupd's updatable devices, the
// update each one has, an Update button with fwupd's progress, and a
// restart when a UEFI capsule waits for one. fwupd is called as the user,
// over D-Bus; polkit (50-isotope-fwupd.rules) lets only admin install. See
// docs/superpowers/specs/2026-11-24-firmware-updates-design.md.

// Firmware is fwupd as the page needs it.
type Firmware interface {
	// Devices lists the devices fwupd knows.
	Devices() ([]fwupd.Device, error)
	// Updates lists the updatable devices that have an update.
	Updates() ([]fwupd.Update, error)
	// Install downloads the release and has fwupd write it. progress gets
	// fwupd's line while it works.
	Install(ctx context.Context, up fwupd.Update, progress func(string)) error
}

// fwupdFirmware is Firmware over pkg/fwupd on the system bus.
type fwupdFirmware struct {
	c    *fwupd.Client
	http *http.Client
}

func dialFirmware() (*fwupdFirmware, error) {
	c, _, err := fwupd.Dial()
	if err != nil {
		return nil, err
	}
	return &fwupdFirmware{c: c, http: &http.Client{Timeout: 30 * time.Minute}}, nil
}

func (f *fwupdFirmware) Devices() ([]fwupd.Device, error) { return f.c.Devices() }
func (f *fwupdFirmware) Updates() ([]fwupd.Update, error) { return f.c.Updates() }

// progressEvery is how often the page reads fwupd's progress.
var progressEvery = 500 * time.Millisecond

func (f *fwupdFirmware) Install(ctx context.Context, up fwupd.Update, progress func(string)) error {
	progress(i18n.Tf("Downloading %s…", up.Release.Version))
	cab, err := fwupd.Download(ctx, f.http, up.Release, "")
	if err != nil {
		return err
	}
	defer cab.Close()
	done := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		t := time.NewTicker(progressEvery)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-t.C:
				if p, err := f.c.Progress(); err == nil && p.Text() != "" {
					progress(p.Text())
				}
			}
		}
	}()
	err = f.c.Install(ctx, up.Device.ID, cab)
	close(done)
	wg.Wait()
	return err
}

// firmwareInput is everything the section shows, read under u.mu.
type firmwareInput struct {
	Admin   bool
	Loaded  bool
	Missing bool
	Err     string
	Devices []fwupd.Device
	Updates []fwupd.Update
	// Working is the device being updated, Line fwupd's last progress.
	Working string
	Line    string
}

// firmwareRow is one device in the section.
type firmwareRow struct {
	ID, Title, Sub string
	// Action is the button's label, "" for none.
	Action  string
	Enabled bool
	// Progress is the line under a device that is being updated.
	Progress string
}

// firmwareView is the section, worked out from its input.
type firmwareView struct {
	Rows []firmwareRow
	// Note says why there is no list, or what a non-admin cannot do.
	Note string
	Err  string
	// Restart offers "Restart…": a device waits for it.
	Restart bool
	// Writing says fwupd is writing firmware for this window: every
	// restart button is off until it is done.
	Writing bool
}

func firmwareViewOf(in firmwareInput) firmwareView {
	var v firmwareView
	v.Writing = in.Working != ""
	switch {
	case !in.Loaded:
		v.Note = i18n.T("Looking for devices…")
		return v
	case in.Missing:
		v.Note = i18n.T("Firmware updates are not available on this system.")
		return v
	case in.Err != "":
		v.Err = in.Err
		return v
	}
	ups := map[string]fwupd.Release{}
	for _, u := range in.Updates {
		ups[u.Device.ID] = u.Release
	}
	anyUpdate := false
	for _, d := range in.Devices {
		if !d.Updatable() {
			continue
		}
		r := firmwareRow{ID: d.ID, Title: d.Name}
		version := i18n.Tf("Version %s", orUnknown(d.Version))
		if d.Vendor != "" {
			r.Sub = d.Vendor + " · " + version
		} else {
			r.Sub = version
		}
		rel, has := ups[d.ID]
		switch {
		case d.NeedsReboot():
			r.Sub = i18n.Tf("%s. The update is ready: restart to finish it.", r.Sub)
			v.Restart = true
		case has:
			anyUpdate = true
			if rel.Summary != "" {
				r.Sub = i18n.Tf("%[1]s. Version %[2]s is available: %[3]s", r.Sub, rel.Version, rel.Summary)
			} else {
				r.Sub = i18n.Tf("%[1]s. Version %[2]s is available.", r.Sub, rel.Version)
			}
			r.Action = i18n.T("Update")
			r.Enabled = in.Admin && in.Working == ""
		default:
			r.Sub = i18n.Tf("%s, up to date.", r.Sub)
		}
		if d.UpdateState == fwupd.StateFailed && d.UpdateError != "" && !d.NeedsReboot() {
			r.Sub += " " + i18n.Tf("The last update failed: %s", d.UpdateError)
		}
		if in.Working == d.ID {
			r.Progress = in.Line
			if r.Progress == "" {
				r.Progress = i18n.T("Starting…")
			}
		}
		v.Rows = append(v.Rows, r)
	}
	switch {
	case len(v.Rows) == 0:
		v.Note = i18n.T("No device in this computer gets firmware updates through the LVFS.")
	case anyUpdate && !in.Admin:
		v.Note = i18n.T("Only an administrator (a member of the admin group) can update firmware.")
	}
	return v
}

// firmwareState is the section's state on the page, under u.mu.
type firmwareState struct {
	in      firmwareInput
	buttons map[string]*widget.Clickable
	restart widget.Clickable
}

func (s *firmwareState) button(id string) *widget.Clickable {
	if s.buttons == nil {
		s.buttons = map[string]*widget.Clickable{}
	}
	b := s.buttons[id]
	if b == nil {
		b = new(widget.Clickable)
		s.buttons[id] = b
	}
	return b
}

// loadFirmware reads the devices and their updates. Anyone may read them.
func (p *updatesPage) loadFirmware() {
	u, f := p.u, p.u.hooks.Firmware
	if f == nil {
		return
	}
	u.async(func() {
		devs, err := f.Devices()
		var ups []fwupd.Update
		if err == nil {
			ups, err = f.Updates()
		}
		u.mu.Lock()
		defer u.mu.Unlock()
		in := &p.firm.in
		in.Loaded, in.Missing, in.Err = true, false, ""
		switch {
		case fwupd.Missing(err):
			in.Missing = true
		case err != nil:
			in.Err = i18n.Tf("Could not read the firmware updates: %s", fwupd.Message(err))
		default:
			in.Devices, in.Updates = devs, ups
		}
	})
}

// firmwareInputLocked is the section's input now.
func (p *updatesPage) firmwareInputLocked() firmwareInput {
	in := p.firm.in
	in.Admin = p.u.sys.Admin
	return in
}

// updateFirmware asks first, then downloads and installs one device's
// update, and offers the restart when it is staged for one.
func (p *updatesPage) updateFirmware(id string) {
	u, f := p.u, p.u.hooks.Firmware
	if f == nil || !u.sys.Admin {
		return
	}
	u.mu.Lock()
	var up fwupd.Update
	for _, x := range p.firm.in.Updates {
		if x.Device.ID == id {
			up = x
		}
	}
	busy := p.firm.in.Working != ""
	u.mu.Unlock()
	if up.Device.ID == "" || busy {
		return
	}
	u.ask(i18n.Tf("Update %s?", up.Device.Name), firmwareConfirmText(up),
		choice{Label: i18n.T("Cancel"), Kind: guikit.ButtonSecondary},
		choice{Label: i18n.T("Update"), Kind: guikit.ButtonPrimary, Do: func() { p.runFirmware(up) }})
}

// firmwareAuthText is the page's answer when polkit refused fwupd. The
// page only installs signed upgrades, which 50-isotope-fwupd.rules lets an
// administrator do without a password; anything else (a downgrade, BIOS
// settings) asks for the password, and sway, i3 and niri have no polkit
// agent to show that prompt, while fwupdmgr in a terminal has one
// (pkttyagent).
var firmwareAuthText = i18n.Mark("fwupd refused this. Installing an update needs an administrator (a member of the admin group); a downgrade or a firmware setting also needs the administrator password, which this window cannot ask for. Run fwupdmgr in a terminal (for example: fwupdmgr downgrade): it asks for the password there.")

// firmwareErrorText says why fwupd failed.
func firmwareErrorText(err error) string {
	switch fwupd.ErrorName(err) {
	case "org.freedesktop.fwupd.AuthFailed", "org.freedesktop.fwupd.PermissionDenied":
		return i18n.T(firmwareAuthText)
	}
	return fwupd.Message(err)
}

// secureBootWarning is added to the confirmation of a UEFI capsule (the
// system firmware). Many vendor capsules reset the firmware settings to the
// factory defaults, which turn Secure Boot on, and the standard edition's
// shim is not signed, so the computer then refuses to start Isotope until
// Secure Boot is off again. Trusted Boot images have the capsule plugin
// off, so this only shows on the standard edition.
var secureBootWarning = i18n.Mark("Some firmware updates turn Secure Boot back on. If the computer then does not start Isotope, turn Secure Boot off in the firmware setup.")

// firmwareConfirmText is the body of the update's confirmation.
func firmwareConfirmText(up fwupd.Update) string {
	s := i18n.Tf("Version %s is downloaded from the LVFS and written to the device. Keep the computer plugged in, and do not unplug the device until it is done. Some updates finish at the next restart.", up.Release.Version)
	if up.Device.Plugin == "uefi_capsule" {
		s += "\n\n" + i18n.T(secureBootWarning)
	}
	return s
}

func (p *updatesPage) runFirmware(up fwupd.Update) {
	u, f := p.u, p.u.hooks.Firmware
	u.mu.Lock()
	if p.firm.in.Working != "" {
		u.mu.Unlock()
		return
	}
	p.firm.in.Working, p.firm.in.Line = up.Device.ID, ""
	u.firmwareWriting = true
	u.mu.Unlock()
	u.async(func() {
		err := f.Install(context.Background(), up, func(line string) {
			u.mu.Lock()
			p.firm.in.Line = line
			u.mu.Unlock()
			u.hooks.Invalidate()
		})
		devs, derr := f.Devices()
		u.mu.Lock()
		p.firm.in.Working, p.firm.in.Line = "", ""
		u.firmwareWriting = false
		u.mu.Unlock()
		if err != nil {
			u.tell(i18n.Tf("Could not update %s", up.Device.Name), firmwareErrorText(err))
			p.loadFirmware()
			return
		}
		p.loadFirmware()
		if derr != nil {
			return
		}
		for _, d := range devs {
			if d.ID == up.Device.ID && d.NeedsReboot() {
				p.askFirmwareRestart(up.Device.Name)
			}
		}
	})
}

// askFirmwareRestart offers the restart a staged update waits for. It goes
// through the page's own restart (isotope-updated's Reboot).
func (p *updatesPage) askFirmwareRestart(name string) {
	title := i18n.T("Restart to finish the firmware update?")
	body := i18n.T("The update is ready and is written at the next restart. The computer may restart more than once and show its own update screen: do not turn it off.")
	if name != "" {
		body = i18n.Tf("The update of %s is ready and is written at the next restart. The computer may restart more than once and show its own update screen: do not turn it off.", name)
	}
	p.u.ask(title, body,
		choice{Label: i18n.T("Later"), Kind: guikit.ButtonSecondary},
		choice{Label: i18n.T("Restart"), Kind: guikit.ButtonPrimary, Do: p.u.reboot})
}

// updateFirmwareClicks handles the section's buttons.
func (p *updatesPage) updateFirmwareClicks(gtx layout.Context) {
	if p.u.hooks.Firmware == nil {
		return
	}
	p.u.mu.Lock()
	ids := make([]string, 0, len(p.firm.buttons))
	for id, b := range p.firm.buttons {
		if b.Clicked(gtx) {
			ids = append(ids, id)
		}
	}
	restart := p.firm.restart.Clicked(gtx)
	v := firmwareViewOf(p.firmwareInputLocked())
	p.u.mu.Unlock()
	for _, id := range ids {
		for _, r := range v.Rows {
			if r.ID == id && r.Action != "" && r.Enabled {
				p.updateFirmware(id)
			}
		}
	}
	if restart && v.Restart && !v.Writing && p.u.sys.Admin {
		p.askFirmwareRestart("")
	}
}

// firmwareSection is the FIRMWARE heading and its panel.
func (p *updatesPage) firmwareSection() layout.Widget {
	u := p.u
	u.mu.Lock()
	v := firmwareViewOf(p.firmwareInputLocked())
	btns := map[string]*widget.Clickable{}
	for _, r := range v.Rows {
		if r.Action != "" {
			btns[r.ID] = p.firm.button(r.ID)
		}
	}
	u.mu.Unlock()
	return func(gtx layout.Context) layout.Dimensions {
		var c col
		c.add(u.caps(i18n.T("Firmware")))
		c.gap(4)
		c.add(u.dim(i18n.T("Firmware of this computer and its devices, from the Linux Vendor Firmware Service (LVFS).")))
		c.gap(10)
		if v.Err != "" {
			c.add(u.errorBox(v.Err))
			return c.layout(gtx)
		}
		if len(v.Rows) == 0 {
			c.add(u.panel(u.dim(v.Note)))
			return c.layout(gtx)
		}
		c.add(u.panel(func(gtx layout.Context) layout.Dimensions {
			var rows col
			for i, r := range v.Rows {
				if i > 0 {
					rows.gap(10)
				}
				var right layout.Widget
				if r.Action != "" {
					right = u.buttons(guikit.Action{Click: btns[r.ID], Kind: guikit.ButtonSecondary, Label: r.Action, Disabled: !r.Enabled})
				}
				rows.add(u.row(r.Title, r.Sub, right))
				if r.Progress != "" {
					rows.gap(6)
					rows.add(u.spinnerLine(r.Progress))
				}
			}
			if v.Restart {
				rows.gap(12)
				rows.add(u.row(i18n.T("Restart to finish the firmware update"), i18n.T("The firmware is written while the computer starts."),
					u.buttons(guikit.Action{Click: &p.firm.restart, Kind: guikit.ButtonPrimary, Label: i18n.T("Restart…"), Disabled: !u.sys.Admin || v.Writing})))
			}
			return rows.layout(gtx)
		}))
		if v.Note != "" {
			c.gap(10)
			c.add(u.notice(v.Note))
		}
		return c.layout(gtx)
	}
}
