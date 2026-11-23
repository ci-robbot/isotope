package main

import (
	"flag"
	"fmt"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"testing"
	"time"

	"gioui.org/gpu/headless"
)

// The Updates page's automatic update states, rendered at 1100x720 (the
// preview size) and compared with testdata/updates-auto-*.png within a
// small tolerance: llvmpipe's anti-aliasing moves a little between Mesa
// releases. `go test -tags nox11,novulkan -run TestUpdatesAutoScreenshots
// ./cmd/isotope-system/ -update` rewrites them. The harness is Isotope Welcome's
// (cmd/isotope-welcome/screenshot_test.go); every helper here is private
// to this file and named for it, so it sits next to another screenshot
// harness in this package. The -update flag is shared: this file defines
// it only when no other file of the package has.
//
// It needs an EGL with a surfaceless platform (Mesa), and skips without
// one or with ISOTOPE_GUI_SKIP_SCREENSHOTS=1; ISOTOPE_GUI_REQUIRE_SCREENSHOTS=1
// fails instead.
func init() {
	if flag.Lookup("update") == nil {
		flag.Bool("update", false, "rewrite the golden screenshots in testdata/")
	}
}

// updatesAutoUpdate says whether -update was given.
func updatesAutoUpdate() bool {
	f := flag.Lookup("update")
	return f != nil && f.Value.String() == "true"
}

const (
	updatesAutoPixelTol = 24
	updatesAutoPageTol  = 0.004
)

var updatesAutoShotSize = image.Pt(1100, 720)

// updatesAutoClock is the fixed time of every screenshot.
var updatesAutoClock = time.Date(2026, 11, 24, 10, 0, 0, 0, time.Local)

// updatesAutoShots are the Updates page with automatic updates in each state.
var updatesAutoShots = []struct {
	name string
	st   Status
	auto AutoStatus
}{
	{"updates-auto-off", Status{State: StateUpToDate, Installed: "v1.0.0", Latest: "v1.0.0"}, AutoStatus{}},
	{"updates-auto-on-skipped", Status{State: StateUpdateAvailable, Installed: "v1.0.0", Latest: "v1.1.0"},
		AutoStatus{Enabled: true, LastTime: updatesAutoClock.Add(-3 * time.Hour).Unix(), LastResult: "skipped", LastDetail: "Not now: the connection is metered."}},
	{"updates-auto-staged", Status{State: StateRebootPending, Pending: pendingInstalled, Installed: "v1.0.0", Latest: "v1.1.0", Staged: "v1.1.0", StagedAuto: true},
		AutoStatus{Enabled: true, LastTime: updatesAutoClock.Add(-2 * time.Hour).Unix(), LastResult: "installed", LastVersion: "v1.1.0", Staged: "v1.1.0", StagedAuto: true}},
	{"updates-auto-failed", Status{State: StateUpdateAvailable, Installed: "v1.0.0", Latest: "v1.1.0"},
		AutoStatus{Enabled: true, LastTime: updatesAutoClock.Add(-26 * time.Hour).Unix(), LastResult: "failed", LastVersion: "v1.1.0", LastDetail: "Could not check for updates: dial tcp: lookup api.github.com: no such host"}},
}

func TestUpdatesAutoScreenshots(t *testing.T) {
	if os.Getenv("ISOTOPE_GUI_SKIP_SCREENSHOTS") == "1" {
		t.Skip("ISOTOPE_GUI_SKIP_SCREENSHOTS=1")
	}
	if os.Getenv("EGL_PLATFORM") == "" {
		t.Setenv("EGL_PLATFORM", "surfaceless")
	}
	probe, err := headless.NewWindow(8, 8)
	if err != nil {
		if os.Getenv("ISOTOPE_GUI_REQUIRE_SCREENSHOTS") == "1" {
			t.Fatalf("no headless GPU context: %v", err)
		}
		t.Skipf("no headless GPU context (EGL with Mesa's surfaceless platform): %v", err)
	}
	probe.Release()
	for _, c := range updatesAutoShots {
		t.Run(c.name, func(t *testing.T) {
			u, d, _ := testUI(t, true)
			u.now = func() time.Time { return updatesAutoClock }
			d.mu.Lock()
			d.st, d.auto = c.st, c.auto
			d.mu.Unlock()
			u.setStatus(c.st)
			u.show(PageUpdates)
			p := u.view(PageUpdates).(*updatesPage)
			eventually(t, "the page's reads", func() bool {
				u.mu.Lock()
				defer u.mu.Unlock()
				return p.auto.Loaded && !p.checking && len(p.tags) > 0
			})
			eventually(t, "the history read", func() bool { return contains(d.called(), "UpdateHistory") })
			u.pageAt = time.Time{}
			updatesAutoCompare(t, fmt.Sprintf("%s-%dx%d", c.name, updatesAutoShotSize.X, updatesAutoShotSize.Y), updatesAutoRender(t, u, updatesAutoShotSize))
		})
	}
}

func updatesAutoRender(t *testing.T, u *UI, sz image.Point) *image.RGBA {
	t.Helper()
	path := filepath.Join(t.TempDir(), "shot.png")
	shot(t, u, sz, path)
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	img, err := png.Decode(f)
	if err != nil {
		t.Fatal(err)
	}
	rgba, ok := img.(*image.RGBA)
	if !ok {
		rgba = image.NewRGBA(img.Bounds())
		for y := img.Bounds().Min.Y; y < img.Bounds().Max.Y; y++ {
			for x := img.Bounds().Min.X; x < img.Bounds().Max.X; x++ {
				rgba.Set(x, y, img.At(x, y))
			}
		}
	}
	return rgba
}

func updatesAutoCompare(t *testing.T, name string, got *image.RGBA) {
	t.Helper()
	path := filepath.Join("testdata", name+".png")
	if updatesAutoUpdate() {
		f, err := os.Create(path)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		if err := png.Encode(f, got); err != nil {
			t.Fatal(err)
		}
		return
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("%v (run go test -run TestUpdatesAutoScreenshots ./cmd/isotope-system/ -update to make it)", err)
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
			if updatesAutoFar(r1, r2) || updatesAutoFar(g1, g2) || updatesAutoFar(b1, b2) {
				diff++
			}
		}
	}
	if frac := float64(diff) / float64(b.Dx()*b.Dy()); frac > updatesAutoPageTol {
		out := filepath.Join(os.TempDir(), "isotope-system-"+name+".png")
		if f, err := os.Create(out); err == nil {
			_ = png.Encode(f, got)
			f.Close()
		}
		t.Fatalf("%s: %.2f%% of the pixels differ (more than %.1f%%); got %s", name, 100*frac, 100*updatesAutoPageTol, out)
	}
}

func updatesAutoFar(a, b uint32) bool {
	a, b = a>>8, b>>8
	if a > b {
		return a-b > updatesAutoPixelTol
	}
	return b-a > updatesAutoPixelTol
}
