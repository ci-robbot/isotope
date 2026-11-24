package main

import (
	"context"
	"io"
	"reflect"
	"sync"

	"fyne.io/systray"
	"github.com/godbus/dbus/v5"

	"github.com/kairos-community/isotope/pkg/i18n"
)

// cmdTray runs the session's update icon: a StatusNotifierItem that follows
// the root service. --no-icon runs the same loop without the icon, which is
// what `notify` is now.
func cmdTray(args []string, stderr io.Writer) int {
	fs := newFlags("tray", stderr)
	noIcon := fs.Bool("no-icon", false, "run without the tray icon")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	// The tray speaks the session's language; the rest of the CLI stays
	// English.
	i18n.Init()
	return runTray(*noIcon, stderr)
}

func runTray(noIcon bool, stderr io.Writer) int {
	// The agent flavor has no service and no panel; its session autostart
	// may still carry an old line, so leave quietly before touching any bus.
	// A missing release file is not an exit: the service reports unmanaged.
	rel, err := LoadRelease(releaseFile())
	if err == nil && rel.Flavor == flavorAgent {
		return exitOK
	}
	app := newTrayApp(rel, noUI{}, dialDaemon, stderr)
	// The agents answer VPN passwords and incoming Bluetooth files while
	// Isotope System is closed. --no-icon (notify) is not a session tray.
	if !noIcon && systemAppInstalled() {
		go startAgents(app.log)
	}
	// The saved network shares marked "mount at login" (Isotope System's
	// Disks page).
	if !noIcon {
		go mountSavedShares(app.log)
	}
	// Firmware updates from fwupd: one notification per device and version,
	// which opens Isotope System's Updates page, so only where it is.
	if !noIcon && systemAppInstalled() {
		go watchFirmware(app.log, openSystem)
	}
	// App updates from Flathub: with automatic app updates off, one
	// notification per set of pending updates; with them on, only the ones
	// that ask for more permissions. Their button opens the Apps page.
	if !noIcon && systemAppInstalled() {
		go watchApps(app.log, openSystem)
	}
	// A port that remote desktop left open when the last session ended
	// without stopping its server (a power cut): closed at login.
	if !noIcon {
		go rdCleanup(io.Discard)
	}
	if noIcon {
		app.noIcon = true
		app.run(context.Background())
		return exitOK
	}
	// systray registers with the StatusNotifierWatcher itself, and again
	// whenever a watcher takes the name, so a bar that starts after the tray
	// still shows it.
	systray.Run(func() {
		systray.SetTitle(i18n.T("Isotope updates"))
		ui := newSystrayUI(app.clicks)
		app.ui = ui
		if conn, err := dbus.ConnectSessionBus(); err == nil {
			go watchColorScheme(conn, ui.SetDark)
		}
		go app.run(context.Background())
	}, nil)
	return exitOK
}

// systrayUI is the thin glue between a trayView and fyne.io/systray. Menu
// rules live in BuildMenu; this only draws what it is given.
type systrayUI struct {
	clicks chan<- string
	icons  map[bool]map[string][]byte // dark panel -> icon -> PNG

	mu    sync.Mutex // Show and SetDark run on different goroutines
	dark  bool
	last  trayView
	shown bool
}

func newSystrayUI(clicks chan<- string) *systrayUI {
	u := &systrayUI{clicks: clicks, icons: map[bool]map[string][]byte{}, dark: true}
	// One pixmap per icon and panel: SNI hosts scale it to the panel, and
	// 44 px stays sharp on a HiDPI panel.
	for _, dark := range []bool{true, false} {
		u.icons[dark] = map[string][]byte{}
		for _, kind := range iconKinds {
			u.icons[dark][kind] = iconPNG(kind, dark, 44)
		}
	}
	return u
}

// SetDark switches the icons to the ones for a dark (or light) panel.
func (u *systrayUI) SetDark(dark bool) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if dark == u.dark {
		return
	}
	u.dark = dark
	if u.shown {
		systray.SetIcon(u.icons[dark][u.last.Icon])
	}
}

func (u *systrayUI) Show(v trayView) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if !u.shown || v.Icon != u.last.Icon {
		systray.SetIcon(u.icons[u.dark][v.Icon])
	}
	if !u.shown || v.Tooltip != u.last.Tooltip {
		systray.SetTooltip(v.Tooltip)
	}
	if !u.shown || !reflect.DeepEqual(v.Menu, u.last.Menu) {
		// ResetMenu closes every old item's ClickedCh, which ends its
		// listener below.
		systray.ResetMenu()
		for _, it := range v.Menu {
			u.add(it, systray.AddMenuItem(it.Label, ""))
		}
	}
	u.last, u.shown = v, true
}

func (u *systrayUI) add(it MenuItem, m *systray.MenuItem) {
	if !it.Enabled {
		m.Disable()
	}
	for _, c := range it.Children {
		u.add(c, m.AddSubMenuItem(c.Label, ""))
	}
	if len(it.Children) == 0 && it.Enabled {
		go func(id string) {
			for range m.ClickedCh {
				u.clicks <- id
			}
		}(it.ID)
	}
}
