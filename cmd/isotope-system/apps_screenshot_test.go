package main

import (
	"flag"
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

	"github.com/kairos-community/isotope/pkg/flatpak"
	"github.com/kairos-community/isotope/pkg/guikit"
)

// The Apps page's screenshots, compared with testdata/apps-*.png within a
// small tolerance (llvmpipe's anti-aliasing moves a little between Mesa
// releases), as the Printers page's are. `go test -tags nox11,novulkan
// -run TestAppsScreenshots ./cmd/isotope-system/ -update` rewrites them.
// They need an EGL with a surfaceless platform (Mesa) and skip without
// one, or with ISOTOPE_GUI_SKIP_SCREENSHOTS=1;
// ISOTOPE_GUI_REQUIRE_SCREENSHOTS=1 fails instead. Every name is prefixed
// with apps. The -update flag is shared: this file defines it only when no
// other file has. The icons are generated (appsTestIcon), not an app's
// artwork.
func init() {
	if flag.Lookup("update") == nil {
		flag.Bool("update", false, "rewrite the golden screenshots in testdata/")
	}
}

func appsUpdateFlag() bool {
	f := flag.Lookup("update")
	return f != nil && f.Value.String() == "true"
}

const (
	appsPixelTol = 24
	appsPageTol  = 0.004
)

var appsShotSize = image.Pt(1100, 720)

// appsClock is the fixed time of every screenshot.
var appsClock = time.Date(2026, 11, 24, 10, 0, 0, 0, time.Local)

// appsShot is the window on the Apps page with fake apps after its reads,
// at a fixed time, on tab.
func appsShot(t *testing.T, f *fakeApps, tab string) (*UI, *appsPage) {
	t.Helper()
	u, p, _ := appsUI(t, f)
	u.now = func() time.Time { return appsClock }
	p.SetTab(tab)
	u.pageAt = appsClock.Add(-time.Minute) // no slide-in
	return u, p
}

func TestAppsScreenshots(t *testing.T) {
	if os.Getenv("ISOTOPE_GUI_SKIP_SCREENSHOTS") == "1" {
		t.Skip("ISOTOPE_GUI_SKIP_SCREENSHOTS=1")
	}
	if os.Getenv("EGL_PLATFORM") == "" {
		os.Setenv("EGL_PLATFORM", "surfaceless")
	}
	probe, err := headless.NewWindow(8, 8)
	if err != nil {
		if os.Getenv("ISOTOPE_GUI_REQUIRE_SCREENSHOTS") == "1" {
			t.Fatalf("no headless GPU context: %v", err)
		}
		t.Skipf("no headless GPU context (EGL with Mesa's surfaceless platform): %v", err)
	}
	probe.Release()
	lastRun := flatpak.LastRun{Time: appsClock.Add(-48 * time.Minute).Unix(), Result: flatpak.RunUpdated, Updated: []string{"Image Viewer"},
		Held: []flatpak.Held{{Name: "GNU Image Manipulation Program", Why: "it asks for more: all devices"}}}
	cases := []struct {
		name  string
		setup func(t *testing.T) *UI
	}{
		{"apps-browse", func(t *testing.T) *UI {
			u, _ := appsShot(t, newFakeApps(t), tabBrowse)
			return u
		}},
		{"apps-category", func(t *testing.T) *UI {
			u, p := appsShot(t, newFakeApps(t), tabBrowse)
			u.mu.Lock()
			p.st.chip = "Graphics"
			u.mu.Unlock()
			return u
		}},
		{"apps-search", func(t *testing.T) *UI {
			u, p := appsShot(t, newFakeApps(t), tabBrowse)
			p.search.Editor.SetText("photo")
			return u
		}},
		{"apps-details", func(t *testing.T) *UI {
			f := newFakeApps(t)
			f.installed = f.installed[:3]
			u, p := appsShot(t, f, tabBrowse)
			p.openDetail("org.gimp.GIMP")
			eventually(t, "the details", func() bool {
				u.mu.Lock()
				defer u.mu.Unlock()
				return p.st.detail.Loaded
			})
			return u
		}},
		{"apps-installed", func(t *testing.T) *UI {
			u, _ := appsShot(t, newFakeApps(t), tabInstalled)
			return u
		}},
		{"apps-installing", func(t *testing.T) *UI {
			f := newFakeApps(t)
			f.installed = f.installed[:3]
			u, p := appsShot(t, f, tabInstalled)
			u.mu.Lock()
			p.st.job = &appsJob{Kind: jobInstall, ID: "org.gimp.GIMP", Name: "GNU Image Manipulation Program",
				Progress: flatpak.Progress{Percent: 46, Step: 1, Steps: 2, Line: "Installing 1/2…"}}
			u.mu.Unlock()
			return u
		}},
		{"apps-remove", func(t *testing.T) *UI {
			u, p := appsShot(t, newFakeApps(t), tabInstalled)
			p.askRemove("org.mozilla.firefox")
			return u
		}},
		{"apps-updates", func(t *testing.T) *UI {
			f := newFakeApps(t)
			f.last, f.hasLast = lastRun, true
			u, _ := appsShot(t, f, tabUpdates)
			eventually(t, "the last run", func() bool {
				u.mu.Lock()
				defer u.mu.Unlock()
				return u.view(PageApps).(*appsPage).st.hasLast
			})
			return u
		}},
		{"apps-no-flathub", func(t *testing.T) *UI {
			f := newFakeApps(t)
			f.flathub = false
			u, _ := appsShot(t, f, tabBrowse)
			return u
		}},
		{"apps-no-flatpak", func(t *testing.T) *UI {
			f := newFakeApps(t)
			f.available = false
			u, _ := appsShot(t, f, tabBrowse)
			return u
		}},
		{"apps-live", func(t *testing.T) *UI {
			u := newUI(testSystem(true), guikit.NewShaper(), Hooks{Live: true, Apps: newFakeApps(t), Start: func(...string) error { return nil }}, PageApps)
			u.now = func() time.Time { return appsClock }
			u.pageAt = appsClock.Add(-time.Minute)
			return u
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			u := c.setup(t)
			appsCompare(t, c.name, appsRender(t, u, appsShotSize))
		})
	}
}

func appsRender(t *testing.T, u *UI, sz image.Point) *image.RGBA {
	t.Helper()
	win, err := headless.NewWindow(sz.X, sz.Y)
	if err != nil {
		t.Fatal(err)
	}
	defer win.Release()
	var r input.Router
	var ops op.Ops
	for i := 0; i < 2; i++ {
		ops.Reset()
		gtx := layout.Context{Ops: &ops, Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1},
			Constraints: layout.Exact(sz), Now: u.now(), Source: r.Source()}
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

func appsCompare(t *testing.T, name string, got *image.RGBA) {
	t.Helper()
	path := filepath.Join("testdata", name+".png")
	if appsUpdateFlag() {
		if err := appsWritePNG(path, got); err != nil {
			t.Fatal(err)
		}
		return
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("%v (run go test -tags nox11,novulkan -run TestAppsScreenshots ./cmd/isotope-system/ -update to make it)", err)
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
			if appsFar(r1, r2) || appsFar(g1, g2) || appsFar(b1, b2) {
				diff++
			}
		}
	}
	if frac := float64(diff) / float64(b.Dx()*b.Dy()); frac > appsPageTol {
		out := filepath.Join(t.TempDir(), name+".png")
		_ = appsWritePNG(out, got)
		t.Errorf("%.2f%% of the pixels differ from %s (limit %.2f%%); this run's image: %s", frac*100, path, appsPageTol*100, out)
	}
}

func appsFar(a, b uint32) bool {
	a, b = a>>8, b>>8
	if a > b {
		return a-b > appsPixelTol
	}
	return b-a > appsPixelTol
}

func appsWritePNG(path string, img image.Image) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	if err := png.Encode(f, img); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}
