package main

import (
	"flag"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"gioui.org/gpu/headless"
	"gioui.org/io/input"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"
)

// The screenshot tests render a page at the window's default size and
// compare it with the PNGs in testdata/, within a small tolerance:
// llvmpipe's anti-aliasing moves a little between Mesa releases. `go test
// -update` rewrites the PNGs; a change to a page shows in its PR. Only the
// pages with a test have a PNG (the Compatibility page, page_compat_test.go).
//
// They need an EGL with a surfaceless platform (Mesa), and skip without one
// or with ISOTOPE_GUI_SKIP_SCREENSHOTS=1; CI sets
// ISOTOPE_GUI_REQUIRE_SCREENSHOTS=1 so that a runner without Mesa fails. The
// same helpers as cmd/isotope-install-gui/screenshot_test.go.
var update = flag.Bool("update", false, "rewrite the golden screenshots in testdata/")

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

// needGPU skips a screenshot test without a headless GPU context, or with
// ISOTOPE_GUI_SKIP_SCREENSHOTS=1.
func needGPU(t *testing.T) {
	t.Helper()
	if os.Getenv("ISOTOPE_GUI_SKIP_SCREENSHOTS") == "1" {
		t.Skip("ISOTOPE_GUI_SKIP_SCREENSHOTS=1")
	}
	probe, err := headless.NewWindow(8, 8)
	if err != nil {
		// CI sets ISOTOPE_GUI_REQUIRE_SCREENSHOTS, so a runner without
		// Mesa fails rather than passing with nothing compared.
		if os.Getenv("ISOTOPE_GUI_REQUIRE_SCREENSHOTS") == "1" {
			t.Fatalf("no headless GPU context: %v", err)
		}
		t.Skipf("no headless GPU context (EGL with Mesa's surfaceless platform): %v", err)
	}
	probe.Release()
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
