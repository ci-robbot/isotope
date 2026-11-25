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

	"github.com/kairos-community/isotope/pkg/guikit"
)

// The Printers page's screenshots, compared with the PNGs in testdata/
// within a small tolerance (llvmpipe's anti-aliasing moves a little between
// Mesa releases), as Isotope Welcome's are. `go test -update -run
// PrintersScreenshots` rewrites them. They need an EGL with a surfaceless
// platform (Mesa) and skip without one, or with
// ISOTOPE_GUI_SKIP_SCREENSHOTS=1 (the image build, whose Mesa is not the one
// the PNGs were made with); ISOTOPE_GUI_REQUIRE_SCREENSHOTS=1 fails instead.
// Every name is prefixed with printers, so the harness does not clash with
// the other screenshot harnesses of this package.
//
// The -update flag is shared with those harnesses: whichever file registers
// it first owns it, and this one only reads it.
func init() {
	if flag.Lookup("update") == nil {
		flag.Bool("update", false, "rewrite the golden screenshots in testdata/")
	}
}

// printersUpdate says whether -update was given.
func printersUpdate() bool {
	f := flag.Lookup("update")
	return f != nil && f.Value.String() == "true"
}

const (
	printersPixelTol = 24
	printersPageTol  = 0.004
)

var printersShotSize = image.Pt(1100, 720)

// printersShot is the window on the Printers page with fake CUPS and
// Avahi, after the first read and search, at a fixed time.
func printersShot(t *testing.T, admin bool, pr *fakePrinting) *UI {
	t.Helper()
	u, d, _ := testUI(t, admin)
	d.queues = pr
	u.hooks.Printing = pr
	at := time.Date(2026, 11, 24, 9, 30, 0, 0, time.UTC)
	u.now = func() time.Time { return at }
	u.show(PagePrinters)
	p := u.view(PagePrinters).(*printersPage)
	waitFor(t, func() bool { s := p.snap(); return s.loaded && s.searched })
	u.pageAt = at.Add(-time.Minute) // no slide-in
	return u
}

func TestPrintersScreenshots(t *testing.T) {
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
	full := func() *fakePrinting {
		return &fakePrinting{printers: []Printer{officeQueue, hallQueue}, found: []FoundPrinter{foundCanon, foundOffice, foundOld}}
	}
	cases := []struct {
		name  string
		setup func(t *testing.T) *UI
	}{
		{"printers-admin", func(t *testing.T) *UI { return printersShot(t, true, full()) }},
		{"printers-user", func(t *testing.T) *UI { return printersShot(t, false, full()) }},
		{"printers-empty", func(t *testing.T) *UI {
			return printersShot(t, true, &fakePrinting{found: []FoundPrinter{foundCanon}})
		}},
		{"printers-nothing-found", func(t *testing.T) *UI { return printersShot(t, true, &fakePrinting{}) }},
		{"printers-no-cups", func(t *testing.T) *UI {
			return printersShot(t, true, &fakePrinting{readErr: errNoCUPS, findErr: errNoAvahi})
		}},
		{"printers-remove", func(t *testing.T) *UI {
			u := printersShot(t, true, full())
			r := printerRows([]Printer{hallQueue}, true)[0]
			u.ask("Remove "+r.Title+"?", "The printer is removed for every user of this computer, with the jobs waiting for it. It can be added again from the printers found below.",
				choice{Label: "Cancel", Kind: guikit.ButtonSecondary}, choice{Label: "Remove", Kind: guikit.ButtonDanger})
			return u
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			u := c.setup(t)
			printersCompare(t, c.name, printersRender(t, u, printersShotSize))
		})
	}
}

func printersRender(t *testing.T, u *UI, sz image.Point) *image.RGBA {
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

func printersCompare(t *testing.T, name string, got *image.RGBA) {
	t.Helper()
	path := filepath.Join("testdata", name+".png")
	if printersUpdate() {
		if err := printersWritePNG(path, got); err != nil {
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
			if printersFar(r1, r2) || printersFar(g1, g2) || printersFar(b1, b2) {
				diff++
			}
		}
	}
	if frac := float64(diff) / float64(b.Dx()*b.Dy()); frac > printersPageTol {
		out := filepath.Join(t.TempDir(), name+".png")
		_ = printersWritePNG(out, got)
		t.Errorf("%.2f%% of the pixels differ from %s (limit %.2f%%); this run's image: %s", frac*100, path, printersPageTol*100, out)
	}
}

func printersFar(a, b uint32) bool {
	a, b = a>>8, b>>8
	if a > b {
		return a-b > printersPixelTol
	}
	return b-a > printersPixelTol
}

func printersWritePNG(path string, img image.Image) error {
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
