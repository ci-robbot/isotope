// Command isotope-install-gui is the graphical installer of the desktop
// ISOs. isotope-install-ui starts it inside cage, a kiosk Wayland
// compositor (see cmd/isotope-install-ui/gui.go). It asks the same questions as the text
// installer through the same rules (pkg/installer), writes the same
// cloud-config and runs the same install. See
// docs/superpowers/specs/2026-11-17-graphical-installer-design.md.
package main

import (
	"bufio"
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strings"

	"gioui.org/app"
	"gioui.org/io/system"
	"gioui.org/op"
	"gioui.org/unit"

	"github.com/kairos-community/isotope/pkg/guikit"
	"github.com/kairos-community/isotope/pkg/i18n"
	"github.com/kairos-community/isotope/pkg/installer"
)

func main() {
	i18n.Init()
	opts, err := parseArgs(os.Args[1:])
	if err != nil {
		fmt.Fprintf(os.Stderr, "isotope-install-gui: %v\n", err)
		fmt.Fprintln(os.Stderr, "usage: isotope-install-gui [--window [--choices FILE]]")
		os.Exit(2)
	}
	_ = os.MkdirAll(installer.RunDir(), 0o700)

	// Window mode (window.go): on COSMIC the window draws its own bar,
	// with the buttons where the user's COSMIC puts them (read before
	// rootEnv drops the user's config dir).
	var live liveChoices
	if opts.window {
		live = readLiveChoices(opts.choices)
		liveLanguage(live)
	}
	ownBar := opts.window && onCosmic(os.Getenv)
	barLeft := ownBar && guikit.ControlsLeftHere()
	if opts.window {
		app.ID = appID
		rootEnv(os.Geteuid(), installer.RunDir(), os.Getenv,
			func(k, v string) { _ = os.Setenv(k, v) }, func(k string) { _ = os.Unsetenv(k) })
	}

	win := new(app.Window)
	if opts.window {
		sw, sh, ss := 0, 0, 1.0
		if ownBar {
			if out, err := exec.Command("cosmic-randr", "list", "--kdl").Output(); err == nil {
				if outs, err := guikit.ParseCosmicOutputs(out); err == nil {
					sw, sh, ss = guikit.CosmicScreen(outs)
				}
			}
		}
		ww, wh := windowSize(sw, sh, ss, ownBar)
		win.Option(app.Title(i18n.T(windowTitle)), app.Size(unit.Dp(ww), unit.Dp(wh)),
			app.MinSize(unit.Dp(guikit.MinWindowW), unit.Dp(guikit.MinWindowH)), app.Decorated(!ownBar))
	} else {
		// cage shows one window over the whole output and draws no
		// decorations; Decorated(false) keeps Gio from drawing its own
		// title bar.
		win.Option(app.Title(i18n.T("Install Isotope")), app.Size(unit.Dp(1440), unit.Dp(900)), app.Decorated(false), app.Fullscreen.Option())
	}

	wizards := make(chan installer.Wizard, 1)
	confirmed := make(chan installer.Choice, 1)
	u := newUI(nil, guikit.NewShaper(), hooks{
		confirm:  func(c installer.Choice) { confirmed <- c },
		relaunch: relaunch,
		poweroff: func() { _ = exec.Command("poweroff").Run() },
		reboot: func() {
			if err := installer.RebootCommand().Run(); err != nil {
				fmt.Fprintf(os.Stderr, "isotope-install-gui: reboot: %v\n", err)
			}
		},
		started:  func() { _ = os.WriteFile(installer.GUIStartedPath(), nil, 0o600) },
		describe: func(path string) installer.DiskInfo { return installer.DescribeDisk(sysBlock, path) },
		githubKeys: func(ctx context.Context, name string) (int, error) {
			return installer.CountGithubKeys(ctx, http.DefaultClient, name)
		},
		saveLog:    saveLog,
		terminal:   openTerminal,
		invalidate: win.Invalidate,
		window:     func(a system.Action) { win.Perform(a) },
	})
	if opts.window {
		// Never exit to be started again: no cage to restart, and the
		// live session has its own terminal and its own first frame.
		u.hooks.relaunch = func(installer.GUIState) {}
		u.hooks.terminal = nil
		u.hooks.started = nil
		u.hooks.installed = func() {
			if err := writeInstalledMarker(installedMarker); err != nil {
				fmt.Fprintf(os.Stderr, "isotope-install-gui: %v\n", err)
			}
		}
		u.hooks.keepTrying = func() { win.Perform(system.ActionClose) }
		u.window = true
		u.live = live
		if ownBar {
			u.bar = &guikit.TitleBar{Title: i18n.T(windowTitle), Left: barLeft}
		}
	}
	u.zoneCoords = installer.ZoneCoords()
	u.version = versionLine("/etc/isotope/release")
	u.runLayout = orDefault(os.Getenv("XKB_DEFAULT_LAYOUT"), installer.DefaultLayout)
	u.runVariant = os.Getenv("XKB_DEFAULT_VARIANT")
	u.canRelaunch = !opts.window && os.Getenv("ISOTOPE_INSTALL_GUI_RELAUNCH") == "1"
	u.software = os.Getenv("ISOTOPE_SOFTWARE_RENDERING") == "1"
	u.gpuClass = os.Getenv("ISOTOPE_GPU_CLASS")
	u.vmImage = releaseKey("/etc/isotope/release", "GPU") == "vm"

	// A relaunched cage's answers; window mode never relaunches.
	var restored *installer.GUIState
	if !opts.window {
		if s, err := installer.ReadGUIState(installer.GUIStatePath()); err == nil {
			restored = &s
		}
		_ = os.Remove(installer.GUIStatePath())
	}

	go install(win, u, wizards, confirmed)
	go func() {
		var ops op.Ops
		focused := false
		for {
			switch e := win.Event().(type) {
			case *app.ClosingEvent:
				// Window mode: the window manager's close button does not
				// stop an install halfway.
				if opts.window && u.installRunning() {
					e.Abort()
				}
			case app.ConfigEvent:
				if u.bar != nil {
					u.bar.Maximized = e.Config.Mode == app.Maximized
				}
				if e.Config.Focused && !focused && u.w != nil {
					u.refocus()
					win.Invalidate()
				}
				focused = e.Config.Focused
			case app.DestroyEvent:
				if e.Err != nil {
					fmt.Fprintf(os.Stderr, "isotope-install-gui: %v\n", e.Err)
					os.Exit(1)
				}
				os.Exit(0)
			case app.FrameEvent:
				select {
				case w := <-wizards:
					u.setWizard(&w)
					if restored != nil {
						u.restore(*restored)
					}
				default:
				}
				gtx := app.NewContext(&ops, e)
				u.Layout(gtx)
				e.Frame(gtx.Ops)
			}
		}
	}()
	app.Main()
}

// saveLog copies the install log to a mounted USB stick, or leaves it in
// /run when there is none, and says where it is.
func saveLog() (string, error) {
	var dirs []string
	if f, err := os.Open("/proc/mounts"); err == nil {
		dirs = installer.USBMountPoints(f, sysBlock)
		f.Close()
	}
	return installer.SaveInstallLog(installer.InstallLogPath(), dirs)
}

// footPath is the terminal "Open a terminal" starts as a second window in
// cage, where the image has it (sway and niri). Elsewhere (i3, cosmic) the
// window leaves for the text installer's rescue shell on tty1.
var footPath = "/usr/bin/foot"

func openTerminal() {
	if _, err := os.Stat(footPath); err == nil {
		cmd := exec.Command(footPath)
		if err := cmd.Start(); err == nil {
			go func() { _ = cmd.Wait() }()
			return
		}
	}
	os.Exit(installer.GUIShellExit)
}

// relaunch saves the answers and asks isotope-install-ui for a new cage with
// the layout they name (relaunch.go).
func relaunch(s installer.GUIState) {
	if err := installer.WriteGUIState(installer.GUIStatePath(), s); err != nil {
		fmt.Fprintf(os.Stderr, "isotope-install-gui: %v\n", err)
		return
	}
	os.Exit(installer.GUIRelaunchExit)
}

// install is the installer's work, off the window's goroutine: core's wizard
// entry point (which hands its wizard to the window and waits for the
// confirmed choice), then the install as the text installer runs it.
func install(win *app.Window, u *UI, wizards chan<- installer.Wizard, confirmed <-chan installer.Choice) {
	send := func(e installer.Event) {
		u.install.events <- e
		win.Invalidate()
	}
	fail := func(err error) { send(installer.Event{Kind: installer.EventFailed, Err: err}) }

	choice, err := installer.RunWizard(func(w installer.Wizard) (installer.Choice, error) {
		wizards <- w
		win.Invalidate()
		return <-confirmed, nil
	})
	if err != nil {
		fail(err)
		return
	}
	pre, cleanup, post := installer.EncryptionSteps(choice)
	manual := ""
	if pre != nil {
		manual = installer.ConfigOut()
		if err := pre(); err != nil {
			fail(err)
			return
		}
	}
	in, err := installer.NewInstall(manual, post)
	if err != nil {
		fail(err)
		return
	}
	defer in.Close()
	_ = os.WriteFile(installer.InstallRunningPath(), nil, 0o600)
	defer os.Remove(installer.InstallRunningPath())
	// The whole log, for "Save the log": the window keeps only the end.
	logFile, err := os.OpenFile(installer.InstallLogPath(), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err == nil {
		defer logFile.Close()
	}

	// The last event waits for the cleanup, so the passphrase file is gone
	// before the window says the install is over.
	var last *installer.Event
	in.Run(func(e installer.Event) {
		if e.Kind == installer.EventLine && logFile != nil {
			fmt.Fprintln(logFile, e.Line)
		}
		if e.Kind == installer.EventFinished || e.Kind == installer.EventFailed {
			last = &e
			if e.Kind == installer.EventFailed && logFile != nil && e.Err != nil {
				fmt.Fprintf(logFile, "isotope-install-gui: %v\n", e.Err)
			}
			return
		}
		send(e)
	})
	if cleanup != nil {
		if cerr := cleanup(); cerr != nil && last != nil && last.Kind == installer.EventFinished {
			last = &installer.Event{Kind: installer.EventFailed, Err: cerr}
		}
	}
	if last != nil {
		send(*last)
	}
}

// releaseKey is one KEY=value of the release file, "" when it is missing.
func releaseKey(path, key string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	for _, l := range strings.Split(string(b), "\n") {
		if k, v, ok := strings.Cut(l, "="); ok && k == key {
			return v
		}
	}
	return ""
}

// versionLine is the sidebar's "Isotope v0.0.20 · sway", from the release
// file the image build writes.
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

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}
