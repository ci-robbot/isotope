package main

import (
	"errors"
	"image"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"gioui.org/io/input"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"
)

// keymapCall is one call of the SessionKeymap hook.
type keymapCall struct{ layout, variant string }

// liveHooks records the session hooks of the live page.
type liveHooks struct {
	mu      sync.Mutex
	keymaps []keymapCall
	langs   []string
}

func (h *liveHooks) keymap(layout, variant string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.keymaps = append(h.keymaps, keymapCall{layout, variant})
	return nil
}

func (h *liveHooks) locale(lang string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.langs = append(h.langs, lang)
	return nil
}

// liveUI is the live flow on desktop d, with a recording runner, the
// session hooks recorded and the choices file in a temporary directory.
func liveUI(t *testing.T, d Desktop) (*UI, *recorder, *fakeRunner, *liveHooks, *livePage) {
	t.Helper()
	u, r := newRecorded(NewLiveFlow(), d)
	run := &fakeRunner{out: map[string]string{"locale -a": localeList}}
	h := &liveHooks{}
	u.hooks.Runner = run
	u.hooks.SessionKeymap = h.keymap
	u.hooks.SessionLocale = h.locale
	u.hooks.LiveChoicesFile = filepath.Join(t.TempDir(), "isotope", "live-choices.json")
	u.hooks.Live = LiveChoices{Lang: "en_US.UTF-8", Layout: "us"}
	liveFrame(u)
	p, ok := u.page().(*livePage)
	if !ok {
		t.Fatalf("page %T, want the live page", u.page())
	}
	return u, r, run, h, p
}

// image1000x700 is the window the click tests lay out.
var image1000x700 = image.Pt(1000, 700)

// liveFrame lays the window out once, so the clicks of the last frame
// are handled.
func liveFrame(u *UI) {
	var r input.Router
	var ops op.Ops
	gtx := layout.Context{
		Ops:         &ops,
		Source:      r.Source(),
		Metric:      unit.Metric{PxPerDp: 1, PxPerSp: 1},
		Constraints: layout.Exact(image1000x700),
		Now:         time.Now(),
	}
	u.Layout(gtx)
	r.Frame(&ops)
}

func TestLoginOnLiveOpensLiveFlow(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "live-desktop")
	if err := os.WriteFile(marker, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(dir, "state", "isotope")

	// The bare command and --login are the login hook; on the live
	// desktop both open the live flow, and so do `isotope welcome` (all,
	// the menu's "Welcome", whose Language page would call SetLocale) and
	// `isotope welcome live`.
	bare, _ := ParseArgs(nil)
	live, ok := ParseArgs([]string{"live"})
	if !ok || live != PageLiveArg {
		t.Fatalf("live: %q %v", live, ok)
	}
	for _, p := range []PageArg{bare, PageLogin, PageAll, live} {
		got, err := resolvePage(p, marker)
		if err != nil || got != PageLiveArg {
			t.Fatalf("%q on live: %q, want the live flow", p, got)
		}
		if writesStamp(got) {
			t.Errorf("%q on live writes the stamp", p)
		}
		if pages := flowFor(got, Cosmic, "").Pages(); !slices.Equal(pages, []PageID{PageLive}) {
			t.Errorf("%q on live: pages %v", p, pages)
		}
	}
	// Off the live desktop the login hook is the first-login flow, a page
	// stays that page, and the live welcome is refused.
	none := filepath.Join(dir, "none")
	if got, err := resolvePage(PageLogin, none); err != nil || got != PageLogin || !writesStamp(got) {
		t.Errorf("no marker: %q %v", got, err)
	}
	if got, err := resolvePage(PageAll, none); err != nil || got != PageAll {
		t.Errorf("all, no marker: %q %v", got, err)
	}
	if got, err := resolvePage(PageLanguage, marker); err != nil || got != PageLanguage {
		t.Errorf("language on live: %q %v", got, err)
	}
	if _, err := resolvePage(PageLiveArg, none); err == nil || err.Error() != "This window is for the live system." {
		t.Errorf("live without the marker: %v", err)
	}
	// Closing the live window writes no stamp: after the install, the
	// first login still runs the real Welcome.
	u, r := newRecorded(flowFor(PageLiveArg, Sway, ""), Sway)
	u.hooks.WriteStamp = func(s Stamp) {
		if writesStamp(PageLiveArg) {
			_ = WriteStamp(state, s)
		}
		r.stamps = append(r.stamps, s)
	}
	r.requestClose()
	if !r.closed {
		t.Fatal("the live window did not close")
	}
	u.Destroyed(nil)
	if !ShouldOpenAtLogin(state) {
		t.Error("the first-login Welcome would not open after the install")
	}
	wantStamps(t, r)
}

func TestLivePageInstallRunsInstaller(t *testing.T) {
	u, r, run, _, p := liveUI(t, Sway)
	p.install.Click()
	liveFrame(u)
	if !slices.Contains(run.called(), "isotope-live-install") {
		t.Fatalf("calls %v, want isotope-live-install", run.called())
	}
	if !r.closed {
		t.Error("the window stays open after Install")
	}
	wantStamps(t, r)
}

// noStart is a runner that cannot start a program (it is not installed).
type noStart struct{ *fakeRunner }

func (noStart) Start(string, ...string) (Process, error) {
	return nil, errors.New("executable file not found")
}

func TestLivePageInstallFailureStays(t *testing.T) {
	u, r, run, _, p := liveUI(t, Sway)
	u.hooks.Runner = noStart{run}
	p.install.Click()
	liveFrame(u)
	if r.closed {
		t.Fatal("the window closed although the installer did not start")
	}
	if p.err == "" {
		t.Error("no error shown")
	}
}

func TestLivePageTryCloses(t *testing.T) {
	u, r, run, _, p := liveUI(t, Cosmic)
	p.try.Click()
	liveFrame(u)
	if !r.closed {
		t.Fatal("Try it out did not close the window")
	}
	if slices.Contains(run.called(), "isotope-live-install") {
		t.Error("Try it out started the installer")
	}
	wantStamps(t, r)
}

// pickIn selects the entry with code in an open chooser.
func pickIn(t *testing.T, c *chooser, code string) {
	t.Helper()
	for i, e := range c.entries {
		if e.code == code {
			c.sel = i
			return
		}
	}
	t.Fatalf("no entry %s", code)
}

func TestLiveSessionChoicesReachInstaller(t *testing.T) {
	u, _, _, h, p := liveUI(t, Sway)
	read := func() string {
		t.Helper()
		b, err := os.ReadFile(u.hooks.LiveChoicesFile)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}

	p.keyboardBtn.Click()
	liveFrame(u)
	if u.question == nil {
		t.Fatal("the keyboard chooser did not open")
	}
	pickIn(t, p.keyboards, "de")
	p.keyboards.use.Click()
	liveFrame(u)
	p.wait()
	if u.question != nil {
		t.Error("the chooser stays open")
	}
	if got := read(); got != `{"lang":"en_US.UTF-8","layout":"de","variant":""}` {
		t.Errorf("choices %s", got)
	}
	if !slices.Equal(h.keymaps, []keymapCall{{"de", ""}}) {
		t.Errorf("keymap calls %v", h.keymaps)
	}

	p.languageBtn.Click()
	liveFrame(u)
	pickIn(t, p.languages, "de_DE.UTF-8")
	p.languages.use.Click()
	liveFrame(u)
	p.wait()
	if got := read(); got != `{"lang":"de_DE.UTF-8","layout":"de","variant":""}` {
		t.Errorf("choices %s", got)
	}
	if !slices.Equal(h.langs, []string{"de_DE.UTF-8"}) {
		t.Errorf("locale calls %v", h.langs)
	}

	// Esc closes the chooser and changes nothing.
	p.keyboardBtn.Click()
	liveFrame(u)
	pickIn(t, p.keyboards, "fr")
	u.prev()
	liveFrame(u)
	p.wait()
	if u.question != nil || len(h.keymaps) != 1 {
		t.Errorf("Esc: question %v, keymaps %v", u.question != nil, h.keymaps)
	}
}

func TestLiveHelpLineFromShortcuts(t *testing.T) {
	// A sheet with no help key: that item is dropped, never made up.
	fixture := "# a sheet\n" +
		"Basics\tSuper + D / Space\tLaunch an application\n" +
		"Basics\tSuper + Escape\tIsotope menu: apps, Wi-Fi, Bluetooth, updates, power\n" +
		"System\tSuper + S\tControl center: Wi-Fi, Bluetooth, sound\n"
	s, err := ParseShortcuts(strings.NewReader(fixture))
	if err != nil {
		t.Fatal(err)
	}
	got := HelpItems(s)
	want := []HelpItem{{Label: "Wi-Fi", Keys: []string{"Super", "S"}}, {Label: "Apps", Keys: []string{"Super", "D"}}}
	if !slices.EqualFunc(got, want, func(a, b HelpItem) bool { return a.Label == b.Label && slices.Equal(a.Keys, b.Keys) }) {
		t.Fatalf("help line %v, want %v", got, want)
	}

	// The real sheets: the tiling variants have all three; COSMIC binds
	// no Wi-Fi or help key, so only Apps is left.
	for d, n := range map[Desktop]int{Sway: 3, I3: 3, Niri: 3, Cosmic: 1} {
		items := HelpItems(sheet(t, d))
		if len(items) != n {
			t.Errorf("%s: %v, want %d items", d, items, n)
		}
	}
	if items := HelpItems(sheet(t, Sway)); items[0].Label != "Wi-Fi" || !slices.Equal(items[0].Keys, []string{"Super", "N"}) {
		t.Errorf("sway Wi-Fi: %v", items[0])
	}
}

func TestSessionKeymapPerDesktop(t *testing.T) {
	home := t.TempDir()
	r := &fakeRunner{}
	if err := ApplySessionKeymap(r, home, Sway, "de", "nodeadkeys"); err != nil {
		t.Fatal(err)
	}
	want := []string{
		`swaymsg input type:keyboard xkb_variant ""`,
		"swaymsg input type:keyboard xkb_layout de",
		"swaymsg input type:keyboard xkb_variant nodeadkeys",
	}
	if !slices.Equal(r.called(), want) {
		t.Errorf("sway: %q", r.called())
	}

	r = &fakeRunner{}
	if err := ApplySessionKeymap(r, home, I3, "fr", ""); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(r.called(), []string{"setxkbmap -layout fr"}) {
		t.Errorf("i3: %q", r.called())
	}
	r = &fakeRunner{}
	if err := ApplySessionKeymap(r, home, I3, "de", "nodeadkeys"); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(r.called(), []string{"setxkbmap -layout de -variant nodeadkeys"}) {
		t.Errorf("i3 with a variant: %q", r.called())
	}

	r = &fakeRunner{}
	if err := ApplySessionKeymap(r, home, Niri, "de", "nodeadkeys"); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(home, ".config", "niri", "isotope-keyboard.kdl"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `layout "de"`) || !strings.Contains(string(b), `variant "nodeadkeys"`) {
		t.Errorf("niri file:\n%s", b)
	}
	// The shipped niri config includes the file, or niri never reads it.
	cfg, err := os.ReadFile(filepath.Join("..", "rootfs-niri", "etc", "niri", "config.kdl"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(cfg), `include optional=true "~/.config/niri/isotope-keyboard.kdl"`) {
		t.Error("rootfs-niri/etc/niri/config.kdl does not include isotope-keyboard.kdl")
	}

	r = &fakeRunner{}
	if err := ApplySessionKeymap(r, home, Cosmic, "it", ""); err != nil {
		t.Fatal(err)
	}
	b, err = os.ReadFile(cosmicKey(home, "com.system76.CosmicComp", 1, "xkb_config"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `layout: "it"`) || !strings.Contains(string(b), `variant: ""`) {
		t.Errorf("cosmic xkb_config:\n%s", b)
	}
	if len(r.called()) != 0 {
		t.Errorf("cosmic ran %q", r.called())
	}

	// A name that is not an XKB name never reaches a command or a file.
	if err := ApplySessionKeymap(&fakeRunner{}, home, Sway, `de"; exec x`, ""); err == nil {
		t.Error("a bad layout was applied")
	}
}

func TestSessionLocaleUpdatesTheActivationEnvironment(t *testing.T) {
	r := &fakeRunner{}
	if err := SetSessionLocale(r, "de_DE.UTF-8"); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(r.called(), []string{"dbus-update-activation-environment --systemd LANG=de_DE.UTF-8"}) {
		t.Errorf("calls %q", r.called())
	}
}

func TestLiveVersionLine(t *testing.T) {
	dir := t.TempDir()
	rel, ses := filepath.Join(dir, "release"), filepath.Join(dir, "session")
	_ = os.WriteFile(rel, []byte("FLAVOR=cosmic\nVERSION=v0.0.25\n"), 0o644)
	_ = os.WriteFile(ses, []byte("SESSION=cosmic\nDESKTOP_NAME=COSMIC\nDISPLAY_NAME=Wayland\n"), 0o644)
	if got := liveVersionLine(rel, ses); got != "Isotope v0.0.25 · COSMIC" {
		t.Errorf("liveVersionLine = %q", got)
	}
}

func TestLiveTryTextPerDesktop(t *testing.T) {
	// COSMIC has a dock; the tiling variants have the Isotope menu.
	if got := tryText(Cosmic); !strings.Contains(got, "the dock or the app launcher") {
		t.Errorf("cosmic: %q", got)
	}
	for _, d := range []Desktop{Sway, I3, Niri} {
		got := tryText(d)
		if !strings.Contains(got, "the Isotope menu (Super+Esc) or the app list") || strings.Contains(got, "dock") {
			t.Errorf("%s: %q", d, got)
		}
	}
	// The page shows it.
	_, _, _, _, p := liveUI(t, Niri)
	if got := tryText(p.u.desktop); strings.Contains(got, "dock") {
		t.Errorf("niri page: %q", got)
	}
}

func TestLiveHelpCosmicTopBar(t *testing.T) {
	items := LiveHelp(Cosmic, sheet(t, Cosmic))
	if len(items) != 2 || items[0].Label != "Wi-Fi" || items[0].Where != "top bar" || items[1].Label != "Apps" {
		t.Errorf("cosmic help line %v", items)
	}
	// The tiling variants keep their keys.
	if items := LiveHelp(Sway, sheet(t, Sway)); items[0].Where != "" || len(items[0].Keys) == 0 {
		t.Errorf("sway help line %v", items)
	}
}

// The worker applies one change at a time, and the latest: three quick
// picks while the first is still applying end with the last one applied.
func TestLiveSessionHooksOneAtATime(t *testing.T) {
	_, _, _, _, p := liveUI(t, Sway)
	release := make(chan struct{})
	var mu sync.Mutex
	running, most := 0, 0
	var got []string
	p.u.hooks.SessionKeymap = func(layout, _ string) error {
		mu.Lock()
		running++
		most = max(most, running)
		got = append(got, layout)
		first := len(got) == 1
		mu.Unlock()
		if first {
			<-release
		}
		mu.Lock()
		running--
		mu.Unlock()
		return nil
	}
	pick := func(l string) {
		p.apply(func(c *LiveChoices) { c.Layout, c.Variant = l, ""; p.keyDue = true })
	}
	pick("de")
	for _, l := range []string{"fr", "it", "es"} {
		pick(l)
	}
	close(release)
	p.wait()
	if most != 1 {
		t.Errorf("%d hooks ran at once", most)
	}
	if len(got) == 0 || got[len(got)-1] != "es" || len(got) > 2 {
		t.Errorf("applied %v, want de then es", got)
	}
}

// On the live desktop the Language page sets the session's LANG and the
// choices file, and never calls isotope-updated (which writes /oem).
func TestLanguageOnLiveSetsOnlyTheSession(t *testing.T) {
	u, _ := newRecorded(NewFlow(Sway), Sway)
	h := &liveHooks{}
	u.hooks.LiveSession = true
	u.hooks.SessionLocale = h.locale
	u.hooks.LiveChoicesFile = filepath.Join(t.TempDir(), "live-choices.json")
	u.hooks.Live = LiveChoices{Lang: "en_US.UTF-8", Layout: "it"}
	s := &fakeSettings{}
	p := languageAt(u, s, false, "en_US.UTF-8")
	if p.readOnly() {
		t.Fatal("read-only on live")
	}
	pickCode(t, p, "de_DE.UTF-8")
	p.Continue()
	p.wait()
	if len(s.calls()) != 0 {
		t.Errorf("SetLocale called on live: %v", s.calls())
	}
	if !slices.Equal(h.langs, []string{"de_DE.UTF-8"}) {
		t.Errorf("session locale %v", h.langs)
	}
	b, err := os.ReadFile(u.hooks.LiveChoicesFile)
	if err != nil || string(b) != `{"lang":"de_DE.UTF-8","layout":"it","variant":""}` {
		t.Errorf("choices %s %v", b, err)
	}
}
