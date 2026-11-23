package main

import (
	"os"

	"gioui.org/layout"
	"gioui.org/widget"

	"github.com/kairos-community/isotope/pkg/guikit"
)

// liveMarker exists only on a live desktop session (see the live-desktop
// design); main reads it into Hooks.Live. A variable for the tests.
var liveMarker = "/run/isotope/live-desktop"

// isLive reports whether this is a live desktop session.
func isLive() bool {
	_, err := os.Stat(liveMarker)
	return err == nil
}

// liveInstaller is the program the Install Isotope button starts.
const liveInstaller = "isotope-live-install"

// liveText is what the pages that need an installed system say on the live
// system.
const liveText = "Not available on the live system. Install Isotope first."

// liveOnly are the pages that cannot work on the live system: they change
// the installed image, its disk or its users, and the live system is in
// memory. Extensions is one of them: an extension installed there would
// be gone at the next boot. Network, Bluetooth, Disks and About still work.
var liveOnly = map[Page]bool{
	PageUpdates:    true,
	PageDesktop:    true,
	PageExtensions: true,
	PageBackup:     true,
	PageSecurity:   true,
	PageUsers:      true,
}

// livePage takes the place of a page in liveOnly on the live system: the
// page's heading, one note and the Install Isotope button.
type livePage struct {
	u       *UI
	page    Page
	list    widget.List
	install widget.Clickable
}

func (p *livePage) Shown()                                      {}
func (p *livePage) Footer() (leading, trailing []guikit.Action) { return nil, nil }
func (p *livePage) Update(gtx layout.Context) {
	if p.install.Clicked(gtx) {
		p.startInstaller()
	}
}

// startInstaller runs the installer. The installer shows its own window,
// so only a failure to start it is told here.
func (p *livePage) startInstaller() {
	if err := p.u.hooks.Start(liveInstaller); err != nil {
		p.u.tell("Cannot start the installer", err.Error())
	}
}

func (p *livePage) Body(gtx layout.Context) layout.Dimensions {
	u := p.u
	return u.scrolled(gtx, &p.list, func(gtx layout.Context) layout.Dimensions {
		var c col
		c.add(u.Heading(p.page.Title(), ""))
		c.gap(20)
		c.add(u.notice(liveText))
		c.gap(14)
		c.add(u.buttons(guikit.Action{Click: &p.install, Kind: guikit.ButtonPrimary, Label: "Install Isotope"}))
		return c.layout(gtx)
	})
}
