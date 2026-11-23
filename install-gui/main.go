// Command isotope-install-gui is the graphical installer of the desktop
// ISOs. isotope-install-ui starts it inside cage, a kiosk Wayland
// compositor (see install-ui/gui.go). It asks the same questions as the text
// installer through the same rules (install-ui/core), writes the same
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
	"gioui.org/op"
	"gioui.org/unit"

	"github.com/kairos-community/isotope/guikit"
	"github.com/kairos-community/isotope/install-ui/core"
)

func main() {
	for _, a := range os.Args[1:] {
		fmt.Fprintf(os.Stderr, "isotope-install-gui: unknown argument: %s\n", a)
		os.Exit(2)
	}
	_ = os.MkdirAll(core.RunDir(), 0o700)

	win := new(app.Window)
	// cage shows one window over the whole output and draws no decorations;
	// Decorated(false) keeps Gio from drawing its own title bar.
	win.Option(app.Title("Install Isotope"), app.Size(unit.Dp(1440), unit.Dp(900)), app.Decorated(false), app.Fullscreen.Option())

	wizards := make(chan core.Wizard, 1)
	confirmed := make(chan core.Choice, 1)
	u := newUI(nil, guikit.NewShaper(), hooks{
		confirm:  func(c core.Choice) { confirmed <- c },
		relaunch: relaunch,
		poweroff: func() { _ = exec.Command("poweroff").Run() },
		reboot: func() {
			if err := core.RebootCommand().Run(); err != nil {
				fmt.Fprintf(os.Stderr, "isotope-install-gui: reboot: %v\n", err)
			}
		},
		started:  func() { _ = os.WriteFile(core.GUIStartedPath(), nil, 0o600) },
		describe: func(path string) core.DiskInfo { return core.DescribeDisk(sysBlock, path) },
		githubKeys: func(ctx context.Context, name string) (int, error) {
			return core.CountGithubKeys(ctx, http.DefaultClient, name)
		},
		saveLog:    saveLog,
		terminal:   openTerminal,
		invalidate: win.Invalidate,
	})
	u.zoneCoords = core.ZoneCoords()
	u.version = versionLine("/etc/isotope/release")
	u.runLayout = orDefault(os.Getenv("XKB_DEFAULT_LAYOUT"), core.DefaultLayout)
	u.runVariant = os.Getenv("XKB_DEFAULT_VARIANT")
	u.canRelaunch = os.Getenv("ISOTOPE_INSTALL_GUI_RELAUNCH") == "1"
	u.software = os.Getenv("ISOTOPE_SOFTWARE_RENDERING") == "1"
	u.gpuClass = os.Getenv("ISOTOPE_GPU_CLASS")
	u.vmImage = releaseKey("/etc/isotope/release", "GPU") == "vm"

	var restored *core.GUIState
	if s, err := core.ReadGUIState(core.GUIStatePath()); err == nil {
		restored = &s
	}
	_ = os.Remove(core.GUIStatePath())

	go install(win, u, wizards, confirmed)
	go func() {
		var ops op.Ops
		focused := false
		for {
			switch e := win.Event().(type) {
			case app.ConfigEvent:
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
		dirs = core.USBMountPoints(f, sysBlock)
		f.Close()
	}
	return core.SaveInstallLog(core.InstallLogPath(), dirs)
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
	os.Exit(core.GUIShellExit)
}

// relaunch saves the answers and asks isotope-install-ui for a new cage with
// the layout they name (relaunch.go).
func relaunch(s core.GUIState) {
	if err := core.WriteGUIState(core.GUIStatePath(), s); err != nil {
		fmt.Fprintf(os.Stderr, "isotope-install-gui: %v\n", err)
		return
	}
	os.Exit(core.GUIRelaunchExit)
}

// install is the installer's work, off the window's goroutine: core's wizard
// entry point (which hands its wizard to the window and waits for the
// confirmed choice), then the install as the text installer runs it.
func install(win *app.Window, u *UI, wizards chan<- core.Wizard, confirmed <-chan core.Choice) {
	send := func(e core.Event) {
		u.install.events <- e
		win.Invalidate()
	}
	fail := func(err error) { send(core.Event{Kind: core.EventFailed, Err: err}) }

	choice, err := core.RunWizard(func(w core.Wizard) (core.Choice, error) {
		wizards <- w
		win.Invalidate()
		return <-confirmed, nil
	})
	if err != nil {
		fail(err)
		return
	}
	pre, cleanup, post := core.EncryptionSteps(choice)
	manual := ""
	if pre != nil {
		manual = core.ConfigOut()
		if err := pre(); err != nil {
			fail(err)
			return
		}
	}
	in, err := core.NewInstall(manual, post)
	if err != nil {
		fail(err)
		return
	}
	defer in.Close()
	_ = os.WriteFile(core.InstallRunningPath(), nil, 0o600)
	defer os.Remove(core.InstallRunningPath())
	// The whole log, for "Save the log": the window keeps only the end.
	logFile, err := os.OpenFile(core.InstallLogPath(), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err == nil {
		defer logFile.Close()
	}

	// The last event waits for the cleanup, so the passphrase file is gone
	// before the window says the install is over.
	var last *core.Event
	in.Run(func(e core.Event) {
		if e.Kind == core.EventLine && logFile != nil {
			fmt.Fprintln(logFile, e.Line)
		}
		if e.Kind == core.EventFinished || e.Kind == core.EventFailed {
			last = &e
			if e.Kind == core.EventFailed && logFile != nil && e.Err != nil {
				fmt.Fprintf(logFile, "isotope-install-gui: %v\n", e.Err)
			}
			return
		}
		send(e)
	})
	if cleanup != nil {
		if cerr := cleanup(); cerr != nil && last != nil && last.Kind == core.EventFinished {
			last = &core.Event{Kind: core.EventFailed, Err: cerr}
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
