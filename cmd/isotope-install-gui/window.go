package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/kairos-community/isotope/pkg/guikit"
	"github.com/kairos-community/isotope/pkg/installer"
)

// Window mode: `isotope-install-gui --window [--choices FILE]`, the installer
// as a normal window in the live desktop session (isotope-live-install starts
// it as root, through sudo, on the user's display). Without --window it is
// the cage installer and none of this applies.
//
//   - The window is decorated, not fullscreen, sized like Isotope Welcome's
//     (guikit.FitWindowSize), with the app ID and X11 class appID, which the
//     sway, i3 and niri configs float. On COSMIC, where the compositor asks
//     clients to draw their own decorations, it draws guikit.TitleBar.
//   - It never exits to restart itself: there is no cage to restart. A
//     keyboard layout is for the installed system (and its console); the live
//     session's keymap is the live welcome's job.
//   - FILE is the live welcome's live-choices.json. A keyboard set there
//     replaces the keyboard question (the page asks only the time zone), and
//     its language becomes Choice.Locale, which seeds the first boot. A value
//     that is missing, empty or that core would not accept counts as not set:
//     the question stays, and Locale stays empty, so the first-login Welcome
//     still asks for the language.

// appID is the window's Wayland app_id and X11 class in window mode: the
// window rules of sway, i3 and niri float it.
const appID = "isotope-install"

// installedMarker says the install finished: isotope-live-install reads it
// to say "Isotope is installed. Restart to use it." It is in /run/isotope,
// the live session's own directory.
const installedMarker = "/run/isotope/installed"

// writeInstalledMarker creates the marker file, and its directory.
func writeInstalledMarker(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, nil, 0o644)
}

// windowTitle is the window's title, and the title bar's on COSMIC.
const windowTitle = "Install Isotope"

// options are the command line's.
type options struct {
	window  bool
	choices string
}

func parseArgs(args []string) (options, error) {
	var o options
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--window":
			o.window = true
		case a == "--choices":
			if i+1 >= len(args) {
				return options{}, errors.New("--choices needs a file")
			}
			i++
			o.choices = args[i]
		case strings.HasPrefix(a, "--choices="):
			o.choices = strings.TrimPrefix(a, "--choices=")
		default:
			return options{}, fmt.Errorf("unknown argument: %s", a)
		}
	}
	if o.choices != "" && !o.window {
		return options{}, errors.New("--choices needs --window")
	}
	return o, nil
}

// liveChoices is the live welcome's $XDG_RUNTIME_DIR/isotope/live-choices.json.
type liveChoices struct {
	Lang    string `json:"lang"`
	Layout  string `json:"layout"`
	Variant string `json:"variant"`
}

// readLiveChoices reads path. A missing, unreadable or malformed file is no
// choice at all, never an error: the installer asks as usual.
func readLiveChoices(path string) liveChoices {
	var c liveChoices
	if path == "" {
		return c
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return c
	}
	if json.Unmarshal(b, &c) != nil {
		return liveChoices{}
	}
	return c
}

// windowWizard makes w the window mode's wizard: the console keymap is left
// alone (the live session is not a console), and the live session's choices
// that pass core's checks are taken. It reports whether the keyboard was.
func windowWizard(w *installer.Wizard, c liveChoices) bool {
	w.ApplyKeymap = func(string) {}
	if installer.ValidLocale(c.Lang) {
		w.Choice.Locale = c.Lang
	}
	if c.Layout == "" || installer.ValidateKeyboard(c.Layout, c.Variant) != nil {
		return false
	}
	for i, k := range w.Keyboards {
		if k.Layout == c.Layout && k.Variant == c.Variant {
			page := w.Page
			w.SetKeyboard(i)
			w.Page = page
			return true
		}
	}
	return false
}

// windowSize is the window's opening size in dp: the installer's page
// (guikit.WindowWidth x guikit.WindowHeight), plus the title bar when it
// draws one, cut to the screen as Isotope Welcome's is.
func windowSize(w, h int, s float64, bar bool) (int, int) {
	defH := guikit.WindowHeight
	if bar {
		defH += guikit.TitleBarHeight
	}
	return guikit.FitWindowSize(w, h, s, guikit.WindowWidth, defH)
}

// onCosmic says whether the session is COSMIC, whose compositor leaves the
// decorations to the client.
func onCosmic(getenv func(string) string) bool {
	for _, d := range strings.Split(getenv("XDG_CURRENT_DESKTOP"), ":") {
		if strings.EqualFold(d, "COSMIC") {
			return true
		}
	}
	return false
}

// rootEnv keeps a root installer from leaving root-owned files in the
// user's directories. It runs on the user's display with the user's
// environment, and Mesa writes its shader cache under XDG_CACHE_HOME or
// HOME/.cache. So, as root: HOME is /root, the cache is under runDir (root's
// tmpfs, gone at shutdown), and the user's config, data and state dirs are
// dropped. XDG_RUNTIME_DIR and WAYLAND_DISPLAY stay, to reach the
// compositor's socket; nothing in the installer writes there (libwayland's
// buffers are memfds). An X server's cookie the user's HOME held is kept
// through XAUTHORITY. Not root: nothing changes.
func rootEnv(euid int, runDir string, getenv func(string) string, setenv func(k, v string), unsetenv func(string)) {
	if euid != 0 {
		return
	}
	if home := getenv("HOME"); getenv("XAUTHORITY") == "" && home != "" && home != "/root" {
		if xa := filepath.Join(home, ".Xauthority"); fileExists(xa) {
			setenv("XAUTHORITY", xa)
		}
	}
	setenv("HOME", "/root")
	setenv("XDG_CACHE_HOME", filepath.Join(runDir, "cache"))
	for _, k := range []string{"XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_STATE_HOME"} {
		unsetenv(k)
	}
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// installRunning says whether the install has started and not ended: the
// window must not close then.
func (u *UI) installRunning() bool {
	return u.page == pInstalling && !u.install.failed && !u.install.finished
}

// stepNames is the sidebar's step list: the second step is only the time
// zone when the live session set the keyboard.
func (u *UI) stepNames() []string {
	if !u.kbFixed {
		return stepNames
	}
	names := append([]string(nil), stepNames...)
	names[1] = "Time zone"
	return names
}
