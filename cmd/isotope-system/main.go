// Command isotope-system is Isotope System: one window for the jobs of
// the isotope command (updates, desktop and image, extensions, backup, the
// disk's keys) and the network. `isotope system [PAGE]` runs it; the tray
// and the Isotope menu open its pages. See
// docs/superpowers/specs/2026-11-21-isotope-system-app-design.md.
package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	"gioui.org/app"
	"gioui.org/io/system"
	"gioui.org/op"
	"gioui.org/unit"
	"github.com/godbus/dbus/v5"

	"github.com/kairos-community/isotope/pkg/cosmictheme"
	"github.com/kairos-community/isotope/pkg/guikit"
	"github.com/kairos-community/isotope/pkg/i18n"
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
	i18n.Init()
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
	hooks.RemoteDesktop = remoteCLI{bin: "isotope"}
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
	hooks.Window = func(a system.Action) { win.Perform(a) }
	// COSMIC asks clients to draw their own title bar: the window draws a
	// COSMIC header bar there. Elsewhere the window manager draws its own
	// bar (or none, as for every window).
	cosmic := isCosmic(os.Getenv)
	ww, wh := windowSize(cosmic)
	win.Option(app.Title("Isotope System"), app.Size(unit.Dp(ww), unit.Dp(wh)),
		app.MinSize(unit.Dp(minWindowW), unit.Dp(minWindowH)), app.Decorated(!cosmic))
	u := newUI(sys, guikit.NewShaper(), hooks, page)
	home, _ := os.UserHomeDir()
	if cosmic {
		u.bar = &guikit.HeaderBar{Title: u.page.Title(), Left: guikit.ControlsLeftHere(),
			Height: guikit.HeaderHeight(os.Getenv, home, os.ReadFile)}
	}
	followTheme(paletteSource{cosmic: cosmic, dirs: cosmictheme.Dirs(os.Getenv, home), read: os.ReadFile,
		scheme: portalScheme(session)}, session, u.SetPalette)
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
			case app.ConfigEvent:
				if u.bar != nil {
					u.bar.Maximized = e.Config.Mode == app.Maximized
					win.Invalidate()
				}
			case app.FrameEvent:
				gtx := app.NewContext(&ops, e)
				u.Layout(gtx)
				e.Frame(gtx.Ops)
			}
		}
	}()
	app.Main()
}

// The window's opening size in dp, cut to the screen, and its minimum.
const (
	defWindowW, defWindowH = 960, 680
	minWindowW, minWindowH = 640, 480
)

// windowSize is the opening size: defWindowW x defWindowH, cut to what
// the screen leaves free. On COSMIC the screen is read from cosmic-randr;
// elsewhere the window manager places and cuts the window itself.
func windowSize(cosmic bool) (int, int) {
	w, h, scale := 0, 0, 1.0
	if cosmic {
		if out, err := exec.Command("cosmic-randr", "list", "--kdl").Output(); err == nil {
			if outs, err := guikit.ParseCosmicOutputs(out); err == nil {
				w, h, scale = guikit.CosmicScreen(outs)
			}
		}
	}
	ww, wh := guikit.FitWindowSize(w, h, scale, defWindowW, defWindowH)
	return max(ww, minWindowW), max(wh, minWindowH)
}
