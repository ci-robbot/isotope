package main

import (
	"context"
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
)

// The fingerprint screenshot tests render the Users page's FINGERPRINTS
// section (without the extension, and with a reader and two fingers) and
// its dialogs (which finger, the enrollment, the password polkit asks for),
// and compare them with the PNGs in testdata/, within a small tolerance,
// like the other harnesses of this package (the modem's one, every name
// prefixed so that they do not clash).
// `go test -tags nox11,novulkan -run TestFingerprintScreenshots . -update`
// rewrites them, on the Mesa CI uses (Ubuntu 24.04's). They skip without an
// EGL surfaceless platform or with ISOTOPE_GUI_SKIP_SCREENSHOTS=1.
func init() {
	if flag.Lookup("update") == nil {
		flag.Bool("update", false, "rewrite the golden screenshots in testdata/")
	}
}

func fpShotUpdate() bool {
	f := flag.Lookup("update")
	return f != nil && f.Value.String() == "true"
}

const (
	fpPixelTol = 24
	fpPageTol  = 0.004
)

var fpShotSize = image.Pt(1100, 720)

var fpShotAt = time.Date(2026, 11, 24, 9, 30, 0, 0, time.UTC)

// fpShots: the section without the extension (not an admin), with a reader
// and two fingers (an admin), and its three dialogs.
func fpShots(t *testing.T) map[string]*UI {
	t.Helper()
	page := func(admin bool, f *fakeFingers) (*UI, *usersPage) {
		u, p := fingersUI(t, admin, f)
		u.now = func() time.Time { return fpShotAt }
		u.show(PageUsers)
		u.pageAt = time.Time{} // no fade-in
		// The section is under the list of users: scroll to the end.
		p.list.Position.Offset = 2000
		return u, p
	}
	out := map[string]*UI{}
	u, _ := page(false, &fakeFingers{})
	out["fingerprint-not-installed"] = u
	u, _ = page(true, &fakeFingers{st: readerState("left-index-finger", "right-index-finger")})
	out["fingerprint-list"] = u
	u, p := page(false, &fakeFingers{st: readerState("right-index-finger")})
	p.fp.openAdd()
	waitQuestion(t, u, fpPickTitle)
	out["fingerprint-pick"] = u
	f := &fakeFingers{st: readerState(), hold: make(chan struct{})}
	u, p = page(false, f)
	t.Cleanup(func() { close(f.hold) })
	p.fp.enroll("right-index-finger", f.st)
	waitQuestion(t, u, fpEnrollHead)
	u.mu.Lock()
	p.fp.passed, p.fp.message = 1, "Good. Lift your finger, then touch the reader again."
	u.mu.Unlock()
	out["fingerprint-enroll"] = u
	u, p = page(false, &fakeFingers{st: readerState()})
	go p.fp.askPassword(context.Background(), AuthAsk{Prompt: "Password: ", Info: "Place your finger on the fingerprint reader"})
	waitQuestion(t, u, fpPassTitle)
	out["fingerprint-password"] = u
	return out
}

func TestFingerprintScreenshots(t *testing.T) {
	if os.Getenv("ISOTOPE_GUI_SKIP_SCREENSHOTS") == "1" {
		t.Skip("ISOTOPE_GUI_SKIP_SCREENSHOTS=1")
	}
	if os.Getenv("EGL_PLATFORM") == "" {
		t.Setenv("EGL_PLATFORM", "surfaceless")
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
	for name, u := range fpShots(t) {
		t.Run(name, func(t *testing.T) { fpCompare(t, name, fpRender(t, u, fpShotSize)) })
	}
}

func fpRender(t *testing.T, u *UI, sz image.Point) *image.RGBA {
	t.Helper()
	win, err := headless.NewWindow(sz.X, sz.Y)
	if err != nil {
		t.Fatal(err)
	}
	defer win.Release()
	var r input.Router
	var ops op.Ops
	// Two frames: the first runs the focus request, the second draws the
	// focus ring.
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

func fpCompare(t *testing.T, name string, got *image.RGBA) {
	t.Helper()
	path := filepath.Join("testdata", name+".png")
	if fpShotUpdate() {
		if err := fpWritePNG(path, got); err != nil {
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
			if fpFar(r1, r2) || fpFar(g1, g2) || fpFar(b1, b2) {
				diff++
			}
		}
	}
	if frac := float64(diff) / float64(b.Dx()*b.Dy()); frac > fpPageTol {
		out := filepath.Join(t.TempDir(), name+".png")
		_ = fpWritePNG(out, got)
		t.Errorf("%.2f%% of the pixels differ from %s (limit %.2f%%); this run's image: %s", frac*100, path, fpPageTol*100, out)
	}
}

func fpFar(a, b uint32) bool {
	a, b = a>>8, b>>8
	if a > b {
		return a-b > fpPixelTol
	}
	return b-a > fpPixelTol
}

func fpWritePNG(path string, img image.Image) error {
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
