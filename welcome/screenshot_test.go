package main

import (
	"errors"
	"flag"
	"fmt"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"testing"
	"time"

	"gioui.org/gpu/headless"
	"gioui.org/io/input"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"

	"github.com/kairos-community/isotope/guikit"
)

// The screenshot tests render the pages at the two sizes of the design
// note and compare them with the PNGs in testdata/, within a small
// tolerance: llvmpipe's anti-aliasing moves a little between Mesa releases.
// `go test -update` rewrites the PNGs; a change to a page shows in its PR.
// The harness is install-gui's (install-gui/screenshot_test.go).
//
// They need an EGL with a surfaceless platform (Mesa), and skip without one
// or with ISOTOPE_GUI_SKIP_SCREENSHOTS=1 (the image build, whose Mesa is not
// the one the PNGs were made with).
var update = flag.Bool("update", false, "rewrite the golden screenshots in testdata/")

var sizes = []image.Point{{1440, 900}, {1024, 768}}

// tolerance: a pixel differs when a channel is off by more than pixelTol,
// and a page fails when more than pageTol of its pixels differ.
const (
	pixelTol = 24
	pageTol  = 0.004
)

func TestMain(m *testing.M) {
	if os.Getenv("EGL_PLATFORM") == "" {
		os.Setenv("EGL_PLATFORM", "surfaceless")
	}
	os.Exit(m.Run())
}

// fixture is a UI on the full flow of d, with the variant's real shortcut
// sheet, a fixed clock and version, and hooks that touch nothing.
func fixture(t *testing.T, d Desktop) *UI {
	t.Helper()
	u := newUI(NewFlow(d), d, guikit.NewShaper(), Hooks{
		Invalidate:        func() {},
		Logout:            func() error { t.Error("logout"); return nil },
		OpenShortcutSheet: func() error { return nil },
		WriteStamp:        func(s Stamp) { t.Errorf("stamp %s", s) },
		Close:             func() { t.Error("close") },
	})
	at := time.Date(2026, 11, 20, 9, 30, 0, 0, time.UTC)
	u.now = func() time.Time { return at }
	u.version = "Isotope v0.0.25 · " + string(d)
	u.shortcuts = sheet(t, d)
	u.hooks.LayoutsDir = layoutFixtures
	u.hooks.Home = "/nonexistent"
	return u
}

// applied is a flow that set every page it could.
func applied(u *UI) {
	o := &u.flow.Outcome
	for _, p := range u.flow.Pages() {
		switch p {
		case PWelcome, PShortcuts, PReady:
		default:
			o.Applied = append(o.Applied, p)
		}
	}
	o.AppsInstalled = []string{"Firefox", "Loupe", "VLC", "Text Editor", "Papers"}
}

// appsAt is the Apps page, loaded from the runner r.
func appsAt(u *UI, r *fakeRunner) {
	u.hooks.Runner = r
	u.appList = apps
	to(u, PApps)
	p := u.page().(*appsPage)
	p.started = true
	p.load(true)
}

// accessAt is the Accessibility page with what r says is set.
func accessAt(u *UI, r *fakeRunner) {
	u.hooks.Runner = r
	to(u, PAccessibility)
	p := u.page().(*accessPage)
	p.start()
	p.wait()
}

var pages = []struct {
	name  string
	d     Desktop
	scale float32
	sizes []image.Point
	setup func(*UI)
}{
	{"1-welcome", Sway, 1, sizes, func(*UI) {}},
	{"shortcuts-cosmic", Cosmic, 1, sizes, func(u *UI) { to(u, PShortcuts) }},
	{"shortcuts-sway", Sway, 1, sizes, func(u *UI) { to(u, PShortcuts) }},
	{"ready-all-applied", Sway, 1, sizes, func(u *UI) { applied(u); to(u, PReady) }},
	{"ready-language-changed", Cosmic, 1, sizes, func(u *UI) {
		applied(u)
		u.flow.Outcome.LanguageChanged = "Deutsch (Deutschland)"
		to(u, PReady)
	}},
	// I1: Log out waits while the apps install (it would kill them).
	{"ready-language-installing", Cosmic, 1, sizes, func(u *UI) {
		applied(u)
		u.flow.Outcome.LanguageChanged = "Deutsch (Deutschland)"
		u.appList = apps
		u.installer = NewInstaller(online(), u.appList)
		u.installer.setProgress(1, 5, "org.videolan.VLC")
		to(u, PReady)
	}},
	{"ready-apps-failed", Sway, 1, sizes, func(u *UI) {
		o := &u.flow.Outcome
		o.Applied = []PageID{PLanguage, PAppearance, PApps}
		o.Skipped = []PageID{PNetwork, PAccessibility}
		o.AppsInstalled = []string{"Firefox", "Loupe"}
		o.AppsFailed = []string{"VLC", "Papers"}
		to(u, PReady)
	}},
	{"apps-default", Sway, 1, sizes, func(u *UI) { appsAt(u, online()) }},
	{"apps-installed-greyed", Sway, 1, sizes, func(u *UI) {
		r := online()
		r.out[installedList] = "org.mozilla.firefox\n"
		appsAt(u, r)
	}},
	{"apps-offline", Sway, 1, sizes, func(u *UI) { appsAt(u, offline()) }},
	{"apps-installing", Sway, 1, sizes, func(u *UI) {
		appsAt(u, online())
		u.installer = NewInstaller(online(), u.appList)
		u.installer.setProgress(1, 5, "org.videolan.VLC")
	}},
	{"network-connected", Sway, 1, sizes, func(u *UI) {
		networkAt(u, &fakeNet{state: NetState{Connected: true, Name: "Home", HasWifi: true}})
	}},
	{"network-wifi-list", Sway, 1, sizes, func(u *UI) {
		networkAt(u, &fakeNet{state: NetState{HasWifi: true}, aps: homeAPs})
	}},
	{"network-password", Sway, 1, sizes, func(u *UI) {
		p := networkAt(u, &fakeNet{state: NetState{HasWifi: true}, aps: homeAPs})
		p.choose("Home")
		p.pass.Editor.SetText("hunter22")
	}},
	{"network-error", Sway, 1, sizes, func(u *UI) {
		p := networkAt(u, &fakeNet{state: NetState{HasWifi: true}, aps: homeAPs})
		p.choose("Home")
		p.pass.Editor.SetText("hunter22")
		p.pass.Err = "Wrong password"
	}},
	{"network-no-device", Sway, 1, sizes, func(u *UI) { networkAt(u, &fakeNet{}) }},
	{"network-load-error", Sway, 1, sizes, func(u *UI) {
		networkAt(u, &fakeNet{stateErr: errors.New("no bus")})
	}},
	{"language-list", Sway, 1, sizes, func(u *UI) {
		u.hooks.Runner = &fakeRunner{out: map[string]string{"locale -a": localeList}}
		u.hooks.Settings, u.hooks.Admin, u.hooks.SystemLocale = &fakeSettings{}, true, "en_US.UTF-8"
		to(u, PLanguage)
	}},
	{"language-search", Sway, 1, sizes, func(u *UI) {
		p := languageAt(u, &fakeSettings{}, true, "en_US.UTF-8")
		p.search.Editor.SetText("deu")
		u.focus = &p.search.Editor
	}},
	{"language-readonly", Sway, 1, sizes, func(u *UI) {
		languageAt(u, &fakeSettings{}, false, "de_DE.UTF-8")
	}},
	{"appearance-tiling", Sway, 1, sizes, func(u *UI) {
		u.hooks.Runner = &fakeRunner{out: map[string]string{"gsettings get org.gnome.desktop.interface color-scheme": "'prefer-dark'\n"}}
		to(u, PAppearance)
		u.page().(*appearancePage).start()
		u.page().(*appearancePage).wait()
	}},
	{"appearance-cosmic", Cosmic, 1, sizes, func(u *UI) {
		u.hooks.Runner = &fakeRunner{}
		to(u, PAppearance)
		u.page().(*appearancePage).start()
		u.page().(*appearancePage).wait()
	}},
	{"access-tiling", Sway, 1, sizes, func(u *UI) {
		accessAt(u, &fakeRunner{out: map[string]string{swayOutputs: `[{"name":"eDP-1","active":true,"scale":1.25}]`}})
	}},
	{"access-i3", I3, 1, sizes, func(u *UI) {
		accessAt(u, &fakeRunner{out: map[string]string{xrdbQuery: "Xft.dpi:\t96\n", textGet: "1.25\n"}})
	}},
	{"access-cosmic", Cosmic, 1, sizes, func(u *UI) {
		accessAt(u, &fakeRunner{out: map[string]string{cosmicList: cosmicKDL}})
	}},
	{"cosmic-layout", Cosmic, 1, sizes, func(u *UI) {
		to(u, PLayout)
		p := u.page().(*layoutPage)
		p.start()
		p.wait()
		p.choose("bottom-panel")
	}},
	{"cosmic-workflow", Cosmic, 1, sizes, func(u *UI) { to(u, PWorkflow) }},
	{"cosmic-launcher", Cosmic, 1, sizes, func(u *UI) { to(u, PLauncher) }},
	{"skip-confirm", Sway, 1, sizes, func(u *UI) { u.askSkip() }},
	{"welcome-scale-2", Cosmic, 2, []image.Point{{1440, 900}}, func(*UI) {}},
	{"live-cosmic", Cosmic, 1, sizes, func(u *UI) { liveAt(u, "COSMIC") }},
	{"live-sway", Sway, 1, sizes, func(u *UI) { liveAt(u, "Sway") }},
	{"live-keyboard", Sway, 1, sizes, func(u *UI) {
		p := liveAt(u, "Sway")
		p.openKeyboards()
		p.keyboards.search.Editor.SetText("german")
	}},
}

// liveAt is the live welcome of the live desktop, its version line naming
// the variant as the session file does (name), with COSMIC's title bar.
func liveAt(u *UI, name string) *livePage {
	u.flow = NewLiveFlow()
	u.setupFlow = u.flow
	u.version = "Isotope v0.0.25 · " + name
	u.hooks.Runner = &fakeRunner{out: map[string]string{"locale -a": localeList}}
	u.hooks.Live = LiveChoices{Lang: "en_US.UTF-8", Layout: "us"}
	if u.desktop == Cosmic {
		u.bar = &guikit.TitleBar{Title: "Welcome to Isotope", Left: true}
	}
	return u.page().(*livePage)
}

// apps is the real list, for the Apps cases.
var apps []App

func TestScreenshots(t *testing.T) {
	apps = loadApps(t)
	if os.Getenv("ISOTOPE_GUI_SKIP_SCREENSHOTS") == "1" {
		t.Skip("ISOTOPE_GUI_SKIP_SCREENSHOTS=1")
	}
	probe, err := headless.NewWindow(8, 8)
	if err != nil {
		// CI may set ISOTOPE_GUI_REQUIRE_SCREENSHOTS, so a runner
		// without Mesa fails rather than passing with nothing compared.
		if os.Getenv("ISOTOPE_GUI_REQUIRE_SCREENSHOTS") == "1" {
			t.Fatalf("no headless GPU context: %v", err)
		}
		t.Skipf("no headless GPU context (EGL with Mesa's surfaceless platform): %v", err)
	}
	probe.Release()
	for _, p := range pages {
		for _, sz := range p.sizes {
			name := fmt.Sprintf("%s-%dx%d", p.name, sz.X, sz.Y)
			t.Run(name, func(t *testing.T) {
				u := fixture(t, p.d)
				p.setup(u)
				u.scale = p.scale
				img := render(t, u, sz)
				compare(t, name, img)
			})
		}
	}
}

func render(t *testing.T, u *UI, sz image.Point) *image.RGBA {
	t.Helper()
	win, err := headless.NewWindow(sz.X, sz.Y)
	if err != nil {
		t.Fatal(err)
	}
	defer win.Release()
	var r input.Router
	var ops op.Ops
	// Two frames: the first runs the focus request of the page, the
	// second draws the focus ring.
	for i := 0; i < 2; i++ {
		ops.Reset()
		gtx := layout.Context{
			Ops:         &ops,
			Metric:      unit.Metric{PxPerDp: 1, PxPerSp: 1},
			Constraints: layout.Exact(sz),
			Now:         u.now(),
			Source:      r.Source(),
		}
		u.Layout(gtx)
		r.Frame(&ops)
	}
	if err := win.Frame(&ops); err != nil {
		t.Fatal(err)
	}
	img := image.NewRGBA(image.Rectangle{Max: sz})
	if err := win.Screenshot(img); err != nil {
		t.Fatal(err)
	}
	return img
}

func compare(t *testing.T, name string, got *image.RGBA) {
	t.Helper()
	path := filepath.Join("testdata", name+".png")
	if *update {
		if err := writePNG(path, got); err != nil {
			t.Fatal(err)
		}
		return
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("%v (run go test -update to make it)", err)
	}
	defer f.Close()
	want, err := png.Decode(f)
	if err != nil {
		t.Fatal(err)
	}
	if want.Bounds() != got.Bounds() {
		t.Fatalf("size %v, golden %v", got.Bounds(), want.Bounds())
	}
	diff := 0
	b := got.Bounds()
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			r1, g1, b1, _ := got.At(x, y).RGBA()
			r2, g2, b2, _ := want.At(x, y).RGBA()
			if far(r1, r2) || far(g1, g2) || far(b1, b2) {
				diff++
			}
		}
	}
	if frac := float64(diff) / float64(b.Dx()*b.Dy()); frac > pageTol {
		out := filepath.Join(t.TempDir(), name+".png")
		_ = writePNG(out, got)
		t.Errorf("%.2f%% of the pixels differ from %s (limit %.2f%%); this run's image: %s", frac*100, path, pageTol*100, out)
	}
}

func far(a, b uint32) bool {
	a, b = a>>8, b>>8
	if a > b {
		return a-b > pixelTol
	}
	return b-a > pixelTol
}

func writePNG(path string, img image.Image) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	if err := (&png.Encoder{CompressionLevel: png.BestCompression}).Encode(f, img); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}
