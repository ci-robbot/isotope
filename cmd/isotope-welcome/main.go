// Command isotope-welcome is the graphical first-login flow.
package main

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"gioui.org/app"
	"gioui.org/io/system"
	"gioui.org/op"
	"gioui.org/unit"

	"github.com/kairos-community/isotope/pkg/guikit"
	"github.com/kairos-community/isotope/pkg/installer"
)

// appID is the window's Wayland app_id and X11 class: the window rules of
// sway, i3 and niri float it by this name.
const appID = "isotope-welcome"

// Gio reads app.ID when it makes the window, and defaults it to the name the
// binary was started as; `isotope welcome` may start it under another one.
func init() { app.ID = appID }

// PageArg names where the flow starts.
type PageArg string

const (
	PageAll           PageArg = "all"
	PageLogin         PageArg = "--login"
	PageApps          PageArg = "apps"
	PageBackup        PageArg = "backup"
	PageLanguage      PageArg = "language"
	PageNetwork       PageArg = "network"
	PageAppearance    PageArg = "appearance"
	PageAccessibility PageArg = "accessibility"
	PageShortcuts     PageArg = "shortcuts"
	// PageLiveArg is the live welcome (`isotope welcome live`); the login
	// hook opens it on the live desktop.
	PageLiveArg PageArg = PageArg(PageLive)
)

// ParseArgs maps the command line to a PageArg; ok is false for anything else.
// No argument is the login hook, as --login: configs from before Isotope
// Welcome start a bare `isotope-welcome`, and it must stay stamp-gated. The
// whole flow without the gate is "all" (`isotope welcome`).
func ParseArgs(args []string) (PageArg, bool) {
	if len(args) == 0 {
		return PageLogin, true
	}
	if len(args) > 1 {
		return "", false
	}
	switch p := PageArg(args[0]); p {
	case PageAll, PageLogin, PageApps, PageBackup, PageLanguage, PageNetwork,
		PageAppearance, PageAccessibility, PageShortcuts, PageLiveArg:
		return p, true
	}
	return "", false
}

func main() {
	page, ok := ParseArgs(os.Args[1:])
	if !ok {
		fmt.Fprintln(os.Stderr, "usage: isotope-welcome [--login | all | apps | language | network | appearance | accessibility | shortcuts | live]")
		os.Exit(2)
	}
	if page == PageBackup {
		fmt.Fprintln(os.Stderr, "isotope-welcome: backup is not available yet")
		os.Exit(2)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "isotope-welcome: %v\n", err)
		os.Exit(1)
	}
	stateDir := StateDir(home, os.Getenv)
	// On the live desktop the login hook and the whole flow are the live
	// welcome, and no stamp is read or written: the first login after the
	// install is still new.
	page, err = resolvePage(page, LiveMarker)
	if err != nil {
		fmt.Fprintln(os.Stderr, "isotope-welcome:", err)
		os.Exit(2)
	}
	if page == PageLogin && !ShouldOpenAtLogin(stateDir) {
		// Apps was skipped for want of a network: remind once, then go.
		if ShouldRemindApps(stateDir) {
			remindApps(ExecRunner{}, stateDir, loadAppList())
		}
		os.Exit(0)
	}
	d, err := ReadDesktop(SessionFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "isotope-welcome: %v\n", err)
		os.Exit(1)
	}
	// Only the whole flow writes the stamp: a page opened from the menu
	// says nothing about the first login.
	stamps := writesStamp(page)
	// One whole flow at a time: the login hook and the menu's "Welcome"
	// would both write the stamp. The live welcome is one window too.
	unlock := func() {}
	if stamps || page == PageLiveArg {
		var ok bool
		if unlock, ok = lockInstance(os.Getenv("XDG_RUNTIME_DIR")); !ok {
			fmt.Fprintln(os.Stderr, "isotope-welcome: already open")
			os.Exit(0)
		}
	}

	appList := loadAppList()

	win := new(app.Window)
	// closeAsked: the UI asked to close; the close is sent after the frame.
	closeAsked := false
	// COSMIC's compositor asks clients to draw their own decorations and
	// Gio's fallback bar is blue with the buttons on the right, so Welcome
	// draws an Isotope bar there (buttons on the left, as the COSMIC header
	// bars). Other desktops keep their own bars.
	ownBar := d == Cosmic
	sw, sh, ss := screenSize(ExecRunner{}, d)
	ww, wh := guikit.WindowSize(sw, sh, ss)
	win.Option(app.Title("Welcome to Isotope"), app.Size(unit.Dp(ww), unit.Dp(wh)),
		app.MinSize(unit.Dp(guikit.MinWindowW), unit.Dp(guikit.MinWindowH)), app.Decorated(!ownBar))
	u := newUI(flowFor(page, d, systemLocale()), d, guikit.NewShaper(), Hooks{
		Invalidate:        win.Invalidate,
		Logout:            func() error { return start(logoutCommand(d)...) },
		OpenShortcutSheet: func() error { return start("isotope-menu", "Help") },
		WriteStamp: func(s Stamp) {
			if !stamps {
				return
			}
			if err := WriteStamp(stateDir, s); err != nil {
				fmt.Fprintf(os.Stderr, "isotope-welcome: %v\n", err)
			}
		},
		Close:   func() { closeAsked = true },
		Window:  func(a system.Action) { win.Perform(a) },
		Runner:  ExecRunner{},
		Network: nmNetwork{},
		// The Language page asks the daemon; the group only decides
		// whether it starts read-only.
		Settings: daemonSettings{},
		// Welcome's "Restore from a backup": the daemon says whether the
		// machine is new, and decides again on Restore.
		Backup:       busBackup{},
		USBDisks:     USBDisksOnBus,
		Admin:        InAdminGroup(),
		Home:         home,
		LayoutsDir:   CosmicLayoutsDir,
		SystemLocale: systemLocale(),
		// The live welcome: the session's language and keyboard, applied
		// at once and written for the installer.
		Live:            liveChoicesNow(os.Getenv, systemLocale()),
		LiveChoicesFile: LiveChoicesPath(os.Getenv("XDG_RUNTIME_DIR")),
		SessionLocale:   func(lang string) error { return SetSessionLocale(ExecRunner{}, lang) },
		SessionKeymap: func(layout, variant string) error {
			return ApplySessionKeymap(ExecRunner{}, home, d, layout, variant)
		},
		XKBRules:    installer.XKBRulesList,
		LiveSession: OnLiveDesktop(LiveMarker),
		AppInstalled: func(id string) {
			// The chosen browser goes to the COSMIC dock.
			if d == Cosmic && isBrowser(appList, id) {
				if err := PinToCosmicDock(home, id); err != nil {
					fmt.Fprintf(os.Stderr, "isotope-welcome: %v\n", err)
				}
			}
		},
	})
	u.appList = appList
	if page == PageLiveArg {
		// COSMIC's dock: "Install Isotope" first, on the live session only.
		liveDockPin(d, home, PinFirstToCosmicDock)
	}
	if ownBar {
		u.bar = &guikit.TitleBar{Title: "Welcome to Isotope", Left: guikit.ControlsLeftHere()}
	}
	u.version = versionLine(releaseFile)
	if page == PageLiveArg {
		u.version = liveVersionLine(releaseFile, SessionFile)
	}
	if f, err := os.Open(ShortcutsFile); err == nil {
		if s, err := ParseShortcuts(f); err == nil {
			u.shortcuts = s
		} else {
			fmt.Fprintf(os.Stderr, "isotope-welcome: %v\n", err)
		}
		f.Close()
	}

	go func() {
		var ops op.Ops
		for {
			switch e := win.Event().(type) {
			case *app.ClosingEvent:
				// The window manager's close button: before the flow has
				// ended the UI keeps the window and asks "Skip setup?".
				// The close Finish asks for passes.
				if u.CloseRequested() {
					e.Abort()
					win.Invalidate()
				}
			case app.DestroyEvent:
				// A close nothing asked about (or that could not be
				// stopped): before the stamp, a skip. A window that
				// failed writes none, so the next login tries again.
				u.Destroyed(e.Err)
				// The window is gone: Welcome may open again while the
				// installs below finish.
				unlock()
				// An install the window started keeps running; say when
				// it is done, as the window is gone.
				if ok, failed, running := u.AwaitInstalls(); running {
					s, b := CompletionNotice(ok, failed)
					if err := Notify(ExecRunner{}, s, b, 15, 2*time.Second); err != nil {
						fmt.Fprintf(os.Stderr, "isotope-welcome: %s: %s\n", s, b)
					}
				}
				if e.Err != nil {
					fmt.Fprintf(os.Stderr, "isotope-welcome: %v\n", e.Err)
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
				if closeAsked {
					closeAsked = false
					win.Perform(system.ActionClose)
				}
			}
		}
	}()
	app.Main()
}

// loadAppList reads the curated apps (AppListFile); nil when it cannot.
func loadAppList() []App {
	f, err := os.Open(AppListFile)
	if err != nil {
		return nil
	}
	defer f.Close()
	a, err := ParseAppList(f)
	if err != nil {
		fmt.Fprintf(os.Stderr, "isotope-welcome: %v\n", err)
		return nil
	}
	return a
}

// releaseFile is the image's release identity (the Dockerfile's kairos
// stage writes it).
const releaseFile = "/etc/isotope/release"

// flowFor is the flow a command line opens: the whole flow, the live
// welcome, or one page.
func flowFor(p PageArg, d Desktop, lang string) *Flow {
	switch p {
	case PageAll:
		return NewFlow(d)
	case PageLogin:
		return NewLoginFlow(d, lang)
	case PageLiveArg:
		return NewLiveFlow()
	}
	return Single(PageID(p))
}

// resolvePage is the page a command line opens on this boot. When marker
// exists, the login hook (--login, or no argument) and the whole flow
// (`isotope welcome`, the menu's "Welcome") are the live welcome: the setup
// pages are for an installed system. Without it, the live welcome is
// refused.
func resolvePage(p PageArg, marker string) (PageArg, error) {
	live := OnLiveDesktop(marker)
	switch {
	case (p == PageLogin || p == PageAll) && live:
		return PageLiveArg, nil
	case p == PageLiveArg && !live:
		return "", ErrNotLive
	}
	return p, nil
}

// writesStamp: only the whole first-login flow writes the stamp. A page
// opened from the menu says nothing about the first login, and the live
// welcome must leave it for the installed system.
func writesStamp(p PageArg) bool { return p == PageAll || p == PageLogin }

// logoutCommand ends the session of desktop d, as the power menus do.
func logoutCommand(d Desktop) []string {
	switch d {
	case Sway:
		return []string{"swaymsg", "exit"}
	case I3:
		return []string{"i3-msg", "exit"}
	case Niri:
		return []string{"niri", "msg", "action", "quit", "--skip-confirmation"}
	}
	return []string{"cosmic-osd", "log-out"}
}

// isBrowser: id is a browser of the list.
func isBrowser(apps []App, id string) bool {
	for _, a := range apps {
		if a.ID == id {
			return a.Group == "browser"
		}
	}
	return false
}

// start runs a command in the background and does not wait for it.
func start(argv ...string) error {
	cmd := exec.Command(argv[0], argv[1:]...)
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}

// versionLine is the edition and version, "Isotope v0.0.25 · sway", from
// the release file the image build writes (as install-gui's).
func versionLine(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return "Isotope"
	}
	defer f.Close()
	var version, flavor string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		k, v, _ := strings.Cut(sc.Text(), "=")
		switch k {
		case "VERSION":
			version = v
		case "FLAVOR":
			flavor = v
		}
	}
	s := "Isotope"
	if version != "" {
		s += " " + version
	}
	if flavor != "" {
		s += " · " + flavor
	}
	return s
}

// localeConfPath is a variable for the tests.
var localeConfPath = "/etc/locale.conf"

// systemLocale is the LANG of /etc/locale.conf.
func systemLocale() string {
	b, err := os.ReadFile(localeConfPath)
	if err != nil {
		return ""
	}
	return CurrentLocale(string(b))
}
