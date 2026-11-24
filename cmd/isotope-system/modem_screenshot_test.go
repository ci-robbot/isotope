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
)

// The mobile broadband screenshot tests render the Network page with a
// modem and the APN dialog, and compare them with the PNGs in testdata/,
// within a small tolerance, like the other harnesses of this package (the
// network shares' one, every name prefixed so that they do not clash).
// `go test -tags nox11,novulkan -run TestModemScreenshots . -update`
// rewrites them, on the Mesa CI uses (Ubuntu 24.04's). They skip without an
// EGL surfaceless platform or with ISOTOPE_GUI_SKIP_SCREENSHOTS=1.
func init() {
	if flag.Lookup("update") == nil {
		flag.Bool("update", false, "rewrite the golden screenshots in testdata/")
	}
}

func modemShotUpdate() bool {
	f := flag.Lookup("update")
	return f != nil && f.Value.String() == "true"
}

const (
	modemPixelTol = 24
	modemPageTol  = 0.004
)

var modemShotSize = image.Pt(1100, 720)

var modemShotAt = time.Date(2026, 11, 24, 9, 30, 0, 0, time.UTC)

// modemShots: the Network page with a connected modem on Ethernet's side,
// and the APN dialog of a modem that is not connected.
func modemShots(t *testing.T) map[string]*UI {
	t.Helper()
	page := func(m Modem) (*UI, *networkPage) {
		u, _, n := testUI(t, true)
		u.now = func() time.Time { return modemShotAt }
		n.st = NetState{Connected: true, Name: "Mobile broadband", Devices: []Device{{Iface: "eth0", Kind: "Wired", State: "Cable unplugged"}}, Modems: []Modem{m}}
		p := u.view(PageNetwork).(*networkPage)
		u.show(PageNetwork)
		eventually(t, "the modem", func() bool { u.mu.Lock(); defer u.mu.Unlock(); return len(p.state.Modems) == 1 && !p.loading })
		u.pageAt = time.Time{} // no fade-in
		return u, p
	}
	out := map[string]*UI{}
	u, _ := page(Modem{ID: modemID, Iface: "cdc-wdm0", State: "Connected", Connection: "Mobile broadband", Address: "10.64.12.7", Operator: "22210", Connected: true})
	out["modem-network"] = u
	u, p := page(Modem{ID: modemID, Iface: "cdc-wdm0", State: "Disconnected", Operator: "26201", Ready: true})
	p.apnDialog(Modem{ID: modemID, Iface: "cdc-wdm0", State: "Disconnected", Ready: true})
	waitQuestion(t, u, "Mobile broadband APN")
	p.apn.Editor.SetText("internet.telekom")
	out["modem-apn"] = u
	return out
}

func TestModemScreenshots(t *testing.T) {
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
	for name, u := range modemShots(t) {
		t.Run(name, func(t *testing.T) { modemCompare(t, name, modemRender(t, u, modemShotSize)) })
	}
}

func modemRender(t *testing.T, u *UI, sz image.Point) *image.RGBA {
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

func modemCompare(t *testing.T, name string, got *image.RGBA) {
	t.Helper()
	path := filepath.Join("testdata", name+".png")
	if modemShotUpdate() {
		if err := modemWritePNG(path, got); err != nil {
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
			if modemFar(r1, r2) || modemFar(g1, g2) || modemFar(b1, b2) {
				diff++
			}
		}
	}
	if frac := float64(diff) / float64(b.Dx()*b.Dy()); frac > modemPageTol {
		out := filepath.Join(t.TempDir(), name+".png")
		_ = modemWritePNG(out, got)
		t.Errorf("%.2f%% of the pixels differ from %s (limit %.2f%%); this run's image: %s", frac*100, path, modemPageTol*100, out)
	}
}

func modemFar(a, b uint32) bool {
	a, b = a>>8, b>>8
	if a > b {
		return a-b > modemPixelTol
	}
	return b-a > modemPixelTol
}

func modemWritePNG(path string, img image.Image) error {
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
