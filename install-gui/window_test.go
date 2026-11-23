package main

import (
	"errors"
	"image"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"

	"github.com/kairos-community/isotope/guikit"
	"github.com/kairos-community/isotope/install-ui/core"
)

// windowFixture is the fixture in window mode with the live session's
// choices c, as main sets it up for --window --choices.
func windowFixture(t *testing.T, c liveChoices) *UI {
	t.Helper()
	return fixtureMode(t, false, func(u *UI) {
		u.window = true
		u.live = c
		u.hooks.relaunch = func(core.GUIState) { t.Fatal("window mode relaunched") }
	})
}

func writeChoices(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "isotope", "live-choices.json")
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestWindowModeSkipsKeyboardWhenSet(t *testing.T) {
	c := readLiveChoices(writeChoices(t, `{"lang":"de_DE.UTF-8","layout":"de","variant":"nodeadkeys"}`))
	u := windowFixture(t, c)
	if !u.kbFixed {
		t.Fatal("the keyboard from the live session is not taken")
	}
	if got := u.stepNames()[1]; got != "Time zone" {
		t.Errorf("step 2 is %q, want Time zone (no keyboard question)", got)
	}
	mustNext(t, u, pKeyboard)
	if u.focus != &u.tzField.Editor {
		t.Error("the page without the keyboard must focus the time zone")
	}
	mustNext(t, u, pAccount)
	ch := u.w.Choice
	if ch.KeyboardLayout != "de" || ch.KeyboardVariant != "nodeadkeys" {
		t.Errorf("keyboard %q/%q, want de/nodeadkeys", ch.KeyboardLayout, ch.KeyboardVariant)
	}
	if ch.Locale != "de_DE.UTF-8" {
		t.Errorf("Locale = %q, want de_DE.UTF-8", ch.Locale)
	}
	// The summary's Change on the keyboard row asks the question again.
	toSummaryFromAccount(t, u)
	u.Layout(layoutCtx(u)) // the summary lists its rows
	u.change[0].Click()
	u.update(layoutCtx(u))
	if u.page != pKeyboard || u.kbFixed {
		t.Fatalf("Change keyboard: page %d, kbFixed %v", u.page, u.kbFixed)
	}
	if got := u.stepNames()[1]; got != stepNames[1] {
		t.Errorf("after Change the step is %q", got)
	}
}

func TestWindowModeKeepsStepsWithoutChoices(t *testing.T) {
	for name, path := range map[string]string{
		"no file":      filepath.Join(t.TempDir(), "missing.json"),
		"empty values": writeChoices(t, `{"lang":"","layout":"","variant":""}`),
		"no flag":      "",
	} {
		t.Run(name, func(t *testing.T) {
			u := windowFixture(t, readLiveChoices(path))
			if u.kbFixed {
				t.Fatal("keyboard taken from nothing")
			}
			if !reflect.DeepEqual(u.stepNames(), stepNames) {
				t.Errorf("steps %v, want %v", u.stepNames(), stepNames)
			}
			mustNext(t, u, pKeyboard)
			if u.focus != &u.kbSearch.Editor {
				t.Error("the keyboard question must have the focus")
			}
			// A layout other than the session's: no relaunch in a window
			// (the hook fails the test), and no keymap on the console.
			set(u.kbSearch, "ital")
			u.filterKeyboards()
			u.pickKeyboard(u.kbShown[0])
			mustNext(t, u, pAccount)
			if u.w.Choice.KeyboardLayout != "it" {
				t.Errorf("layout %q, want it", u.w.Choice.KeyboardLayout)
			}
		})
	}
}

func TestWindowModeLocaleEmptyWhenNotPicked(t *testing.T) {
	for name, body := range map[string]string{
		"keyboard only": `{"layout":"fr"}`,
		"empty lang":    `{"lang":"","layout":"fr","variant":""}`,
	} {
		t.Run(name, func(t *testing.T) {
			u := windowFixture(t, readLiveChoices(writeChoices(t, body)))
			if u.w.Choice.Locale != "" {
				t.Fatalf("Locale = %q, want empty: the first-login Welcome must still ask", u.w.Choice.Locale)
			}
			if !u.kbFixed || u.w.Choice.KeyboardLayout != "fr" {
				t.Errorf("keyboard not taken: %v %q", u.kbFixed, u.w.Choice.KeyboardLayout)
			}
		})
	}
	u := windowFixture(t, readLiveChoices(filepath.Join(t.TempDir(), "missing.json")))
	toSummary(t, u)
	if u.w.Choice.Locale != "" {
		t.Fatalf("no file: Locale = %q at the summary", u.w.Choice.Locale)
	}
}

func TestWindowModeIgnoresInvalidChoices(t *testing.T) {
	for name, body := range map[string]string{
		"bad json":       `{"lang":`,
		"injected lang":  `{"lang":"de_DE.UTF-8\nstages:","layout":"de\nx"}`,
		"bad variant":    `{"lang":"de DE","layout":"de","variant":"a b"}`,
		"unknown layout": `{"layout":"zz"}`,
		"too long lang":  `{"lang":"` + strings.Repeat("a", 65) + `"}`,
	} {
		t.Run(name, func(t *testing.T) {
			u := windowFixture(t, readLiveChoices(writeChoices(t, body)))
			if u.w.Choice.Locale != "" || u.kbFixed {
				t.Fatalf("an invalid value was taken: Locale %q, kbFixed %v", u.w.Choice.Locale, u.kbFixed)
			}
			if u.w.Choice.KeyboardLayout != core.DefaultLayout {
				t.Errorf("layout %q, want the default", u.w.Choice.KeyboardLayout)
			}
			// The install goes on: the keyboard question as usual.
			toSummary(t, u)
		})
	}
	// One good value still counts when the other is bad.
	u := windowFixture(t, readLiveChoices(writeChoices(t, `{"lang":"it_IT.UTF-8","layout":"../x"}`)))
	if u.w.Choice.Locale != "it_IT.UTF-8" || u.kbFixed {
		t.Errorf("Locale %q, kbFixed %v", u.w.Choice.Locale, u.kbFixed)
	}
}

func TestDefaultModeIgnoresChoices(t *testing.T) {
	// Without --window nothing reads the file: the UI has no live choices.
	u := fixture(t, false)
	if u.window || u.kbFixed || u.bar != nil {
		t.Fatal("default mode is not the cage installer")
	}
}

func TestParseArgs(t *testing.T) {
	for _, c := range []struct {
		args []string
		want options
		ok   bool
	}{
		{nil, options{}, true},
		{[]string{"--window"}, options{window: true}, true},
		{[]string{"--window", "--choices", "/run/user/1000/isotope/live-choices.json"}, options{window: true, choices: "/run/user/1000/isotope/live-choices.json"}, true},
		{[]string{"--window", "--choices=/tmp/c.json"}, options{window: true, choices: "/tmp/c.json"}, true},
		{[]string{"--choices", "/tmp/c.json"}, options{}, false},
		{[]string{"--window", "--choices"}, options{}, false},
		{[]string{"--nonsense"}, options{}, false},
		{[]string{"extra"}, options{}, false},
	} {
		got, err := parseArgs(c.args)
		if (err == nil) != c.ok || (c.ok && got != c.want) {
			t.Errorf("parseArgs(%q) = %+v, %v", c.args, got, err)
		}
	}
}

func TestRootEnvKeepsFilesOutOfTheUsersDirs(t *testing.T) {
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, ".Xauthority"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	env := map[string]string{
		"HOME": home, "XDG_CACHE_HOME": home + "/.cache", "XDG_CONFIG_HOME": home + "/.config",
		"XDG_DATA_HOME": home + "/.local/share", "XDG_STATE_HOME": home + "/.local/state",
		"XDG_RUNTIME_DIR": "/run/user/1000", "WAYLAND_DISPLAY": "wayland-1",
	}
	run := t.TempDir()
	rootEnv(0, run, func(k string) string { return env[k] }, func(k, v string) { env[k] = v }, func(k string) { delete(env, k) })
	want := map[string]string{
		"HOME": "/root", "XDG_CACHE_HOME": filepath.Join(run, "cache"),
		"XAUTHORITY":      filepath.Join(home, ".Xauthority"),
		"XDG_RUNTIME_DIR": "/run/user/1000", "WAYLAND_DISPLAY": "wayland-1",
	}
	if !reflect.DeepEqual(env, want) {
		t.Errorf("env %v, want %v", env, want)
	}
	// Not root: nothing changes.
	env = map[string]string{"HOME": home}
	rootEnv(1000, run, func(k string) string { return env[k] }, func(k, v string) { env[k] = v }, func(k string) { delete(env, k) })
	if len(env) != 1 || env["HOME"] != home {
		t.Errorf("non-root env changed: %v", env)
	}
}

// layoutCtx is a frame's context with no input, for Layout and update
// without a GPU: Click()ed buttons still report their clicks.
func layoutCtx(u *UI) layout.Context {
	return layout.Context{
		Ops:         new(op.Ops),
		Metric:      unit.Metric{PxPerDp: 1, PxPerSp: 1},
		Constraints: layout.Exact(image.Pt(920, 620)),
		Now:         u.now(),
	}
}

// toSummaryFromAccount fills the account page and goes on to the summary,
// without encryption.
func toSummaryFromAccount(t *testing.T, u *UI) {
	t.Helper()
	set(u.user, "alex")
	set(u.pw, "hunter22hunter")
	set(u.pw2, "hunter22hunter")
	mustNext(t, u, pDisk)
	mustNext(t, u, pEncrypt)
	mustNext(t, u, pSummary)
}

// The window on COSMIC: Isotope's title bar over the installer, at the
// size it opens at when the screen cannot be read, on the page that asks
// only the time zone (the live session set the keyboard).
func TestWindowScreenshot(t *testing.T) {
	needGPU(t)
	u := windowFixture(t, liveChoices{Lang: "it_IT.UTF-8", Layout: "it"})
	u.bar = &guikit.TitleBar{Title: windowTitle, Left: true}
	u.version = "Isotope v0.0.20 · COSMIC"
	mustNext(t, u, pKeyboard)
	u.pageAt = time.Time{}
	w, h := windowSize(0, 0, 1, true)
	compare(t, "window-cosmic", render(t, u, image.Pt(w, h)))
}

// In the live window the last page offers "Restart now" and "Keep trying"
// and nothing else; Keep trying leaves the window open to its owner, and
// the install's success is recorded for the launcher.
func TestWindowDonePageKeepTrying(t *testing.T) {
	u := windowFixture(t, liveChoices{})
	var rebooted, kept, installed int
	u.hooks.reboot = func() { rebooted++ }
	u.hooks.keepTrying = func() { kept++ }
	u.hooks.installed = func() { installed++ }
	u.hooks.poweroff = func() { t.Fatal("the last page powered off") }
	u.apply(core.Event{Kind: core.EventFinished})
	if u.page != pDone {
		t.Fatalf("page %v, want done", u.page)
	}
	if installed != 1 {
		t.Errorf("installed hook ran %d times, want 1", installed)
	}
	u.keep.Click()
	gtx := layoutCtx(u)
	u.update(gtx)
	if kept != 1 || rebooted != 0 {
		t.Errorf("Keep trying: kept %d, rebooted %d", kept, rebooted)
	}
	u.restart.Click()
	u.update(layoutCtx(u))
	if rebooted != 1 {
		t.Errorf("Restart now: rebooted %d, want 1", rebooted)
	}
}

// A failed install does not record a success, and the default (cage) mode
// has no hooks to call.
func TestInstalledHookOnlyOnSuccess(t *testing.T) {
	u := windowFixture(t, liveChoices{})
	u.hooks.installed = func() { t.Fatal("recorded a failed install") }
	u.apply(core.Event{Kind: core.EventFailed, Err: errors.New("x")})
	d := fixtureMode(t, false, nil)
	d.apply(core.Event{Kind: core.EventFinished}) // nil hook: no panic
}

func TestInstalledMarker(t *testing.T) {
	p := filepath.Join(t.TempDir(), "isotope", "installed")
	if err := writeInstalledMarker(p); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(p); err != nil {
		t.Fatal(err)
	}
}

// The last page in the live window: Restart now and Keep trying.
func TestWindowDoneScreenshot(t *testing.T) {
	needGPU(t)
	u := windowFixture(t, liveChoices{})
	u.version = "Isotope v0.0.20 · sway"
	u.install.user = "alex"
	u.apply(core.Event{Kind: core.EventFinished})
	u.pageAt = time.Time{}
	compare(t, "window-done", render(t, u, image.Pt(920, 620)))
}

// A failed install in the live window can be closed: "Close" is the way out
// besides Restart. The cage page has no such button.
func TestWindowFailedPageClose(t *testing.T) {
	u := windowFixture(t, liveChoices{})
	kept := 0
	u.hooks.keepTrying = func() { kept++ }
	u.hooks.installed = func() { t.Fatal("recorded a failed install") }
	u.apply(core.Event{Kind: core.EventFailed, Err: errors.New("exit status 1")})
	u.keep.Click()
	u.update(layoutCtx(u))
	if kept != 1 {
		t.Errorf("Close on the failed page: kept %d, want 1", kept)
	}
	if !u.failedPageHasClose() {
		t.Error("the failed page has no Close button in window mode")
	}
	d := fixtureMode(t, false, nil)
	d.apply(core.Event{Kind: core.EventFailed, Err: errors.New("x")})
	if d.failedPageHasClose() {
		t.Error("the cage failed page has a Close button")
	}
}

func TestWindowFailedScreenshot(t *testing.T) {
	needGPU(t)
	u := windowFixture(t, liveChoices{})
	u.version = "Isotope v0.0.20 · sway"
	toInstalling(t, u)
	u.apply(core.Event{Kind: core.EventLine, Line: "ERROR failed copying /run/rootfsbase: no space left on device"})
	u.apply(core.Event{Kind: core.EventFailed, Err: errors.New("exit status 1")})
	u.pageAt = time.Time{}
	compare(t, "window-failed", render(t, u, image.Pt(920, 620)))
}
