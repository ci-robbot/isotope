// Command isotope-system is Isotope System: one window for the jobs of
// the isotope command (updates, desktop and image, extensions, backup, the
// disk's keys) and the network. `isotope system [PAGE]` runs it; the tray
// and the Isotope menu open its pages. See
// docs/superpowers/specs/2026-11-21-isotope-system-app-design.md.
package main

import (
	"fmt"
	"os"
	"strings"

	"gioui.org/app"
	"gioui.org/op"
	"gioui.org/unit"
	"github.com/godbus/dbus/v5"

	"github.com/kairos-community/isotope/pkg/guikit"
)

// appID is the window's Wayland app_id and X11 class: the window rules of
// sway, i3 and niri float it by this name.
const appID = "isotope-system"

// Gio reads app.ID when it makes the window, and defaults it to the name the
// binary was started as.
func init() { app.ID = appID }

// The session-bus name one window owns. A second `isotope system PAGE`
// asks it to show PAGE and exits, so the tray and the menu never open a
// second window.
const (
	instanceName  = "io.kairos.IsotopeSystem1"
	instancePath  = dbus.ObjectPath("/io/kairos/IsotopeSystem1")
	instanceIface = "io.kairos.IsotopeSystem1"
)

// instance is the exported object of the window.
type instance struct{ u *UI }

// ShowPage takes a page, or a page and its tab ("apps updates").
func (i instance) ShowPage(page string) *dbus.Error {
	p, tab, ok := ParseTarget(strings.Fields(page))
	if !ok {
		return dbus.MakeFailedError(fmt.Errorf("%q is not a page", page))
	}
	i.u.ShowTab(p, tab)
	return nil
}

// handOff asks a running window to show page (and tab). ok is true when
// one did.
func handOff(conn *dbus.Conn, page Page, tab string) bool {
	return conn.Object(instanceName, instancePath).Call(instanceIface+".ShowPage", dbus.FlagNoAutoStart, strings.TrimSpace(string(page)+" "+tab)).Err == nil
}

func main() {
	if len(os.Args) > 1 && os.Args[1] == "ask" {
		os.Exit(runAsk(os.Args[2:]))
	}
	page, tab, ok := ParseTarget(os.Args[1:])
	if !ok {
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(2)
	}
	session, err := dbus.ConnectSessionBus()
	if err == nil {
		reply, rerr := session.RequestName(instanceName, dbus.NameFlagDoNotQueue)
		if rerr == nil && reply == dbus.RequestNameReplyExists {
			if handOff(session, page, tab) {
				os.Exit(0)
			}
		}
	} else {
		// Without a session bus every call opens its own window.
		session = nil
	}

	sys := readSystem()
	var hooks Hooks
	if d, err := dialDaemon(); err == nil {
		hooks.Daemon = d
	} else {
		fmt.Fprintf(os.Stderr, "isotope-system: the system bus: %v\n", err)
	}
	hooks.Live = isLive()
	hooks.Network = nmNetwork{}
	hooks.Bluetooth = newBluez()
	hooks.Files = newPortalFiles()
	hooks.Glibc = glibcCLI{bin: "isotope"}
	hooks.Printing = newCUPSPrinting()
	if d, err := dialDisks(); err == nil {
		hooks.Disks = d
	} else {
		fmt.Fprintf(os.Stderr, "isotope-system: %v\n", err)
	}
	if st, err := newFileShares(); err == nil {
		hooks.Shares = st
	}
	if f, err := dialFirmware(); err == nil {
		hooks.Firmware = f
	}
	hooks.Apps = newFlatpakApps()
	if c, err := dbus.ConnectSystemBus(); err == nil {
		hooks.Fingers = newFprintd(c, sys.User, uint32(os.Getuid()))
	}

	win := new(app.Window)
	hooks.Invalidate = win.Invalidate
	win.Option(app.Title("Isotope System"), app.Size(unit.Dp(1100), unit.Dp(720)), app.Decorated(true))
	u := newUI(sys, guikit.NewShaper(), hooks, page)
	if tab != "" {
		u.showTab(page, tab)
	}
	if session != nil {
		_ = session.Export(instance{u}, instancePath, instanceIface)
	}
	go u.follow()

	go func() {
		var ops op.Ops
		for {
			switch e := win.Event().(type) {
			case app.DestroyEvent:
				// A job the window started keeps running in the daemon;
				// the tray reports its end. So does the glibc
				// container's setup or reset, in its own session
				// (compat.go).
				if e.Err != nil {
					fmt.Fprintf(os.Stderr, "isotope-system: %v\n", e.Err)
					os.Exit(1)
				}
				os.Exit(0)
			case app.FrameEvent:
				gtx := app.NewContext(&ops, e)
				u.Layout(gtx)
				e.Frame(gtx.Ops)
			}
		}
	}()
	app.Main()
}
