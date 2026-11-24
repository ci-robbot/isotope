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

	"github.com/kairos-community/isotope/pkg/remotedesktop"
)

// The remote desktop screenshot tests render the Network page's REMOTE
// DESKTOP section (off, on, the password dialog, the extension missing,
// COSMIC) and compare them with the PNGs in testdata/, within a small
// tolerance, like the other harnesses of this package (every name
// prefixed remote). `go test -tags nox11,novulkan -run
// TestRemoteScreenshots . -update` rewrites them, on the Mesa CI uses
// (Ubuntu 24.04's). They skip without an EGL surfaceless platform or with
// ISOTOPE_GUI_SKIP_SCREENSHOTS=1.
func init() {
	if flag.Lookup("update") == nil {
		flag.Bool("update", false, "rewrite the golden screenshots in testdata/")
	}
}

func remoteShotUpdate() bool {
	f := flag.Lookup("update")
	return f != nil && f.Value.String() == "true"
}

const (
	remotePixelTol = 24
	remotePageTol  = 0.004
)

var remoteShotSize = image.Pt(1100, 720)

var remoteShotAt = time.Date(2026, 11, 24, 9, 30, 0, 0, time.UTC)

// remoteShots: the Network page of a wired computer, scrolled to the
// REMOTE DESKTOP section.
func remoteShots(t *testing.T) map[string]*UI {
	t.Helper()
	page := func(admin bool, st remotedesktop.Status) (*UI, *networkPage) {
		u, _, n := testUI(t, admin)
		u.now = func() time.Time { return remoteShotAt }
		n.st = NetState{Connected: true, Name: "Wired connection 1", Devices: []Device{{Iface: "eth0", Kind: "Wired", State: "Connected", Connection: "Wired connection 1", Address: "192.168.1.20"}}}
		n.aps = nil
		u.hooks.RemoteDesktop = &fakeRemote{st: st}
		p := u.view(PageNetwork).(*networkPage)
		u.show(PageNetwork)
		eventually(t, "the page", func() bool {
			u.mu.Lock()
			defer u.mu.Unlock()
			return len(p.state.Devices) == 1 && !p.loading && p.rdLoaded && p.fwLoaded
		})
		u.pageAt = time.Time{} // no fade-in
		p.list.Position.Offset = 240
		return u, p
	}
	out := map[string]*UI{}
	u, _ := page(true, rdAvailable(false, false))
	out["remote-off"] = u
	u, _ = page(true, rdAvailable(true, true))
	out["remote-on"] = u
	u, p := page(true, rdAvailable(false, false))
	p.rdPasswordDialog(true)
	waitQuestion(t, u, "Remote desktop password")
	p.rdPw.Editor.SetText("correct horse")
	out["remote-password"] = u
	u, _ = page(true, remotedesktop.Status{Reason: "Remote desktop needs the remote-desktop system extension: isotope ext install remote-desktop", NeedsExtension: true, Port: 5900})
	out["remote-extension"] = u
	u, _ = page(false, remotedesktop.Status{Reason: "COSMIC has no remote desktop server yet. Sway and niri have one.", Port: 5900})
	out["remote-cosmic"] = u
	return out
}

func TestRemoteScreenshots(t *testing.T) {
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
	for name, u := range remoteShots(t) {
		t.Run(name, func(t *testing.T) { remoteCompare(t, name, remoteRender(t, u, remoteShotSize)) })
	}
}

func remoteRender(t *testing.T, u *UI, sz image.Point) *image.RGBA {
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

func remoteCompare(t *testing.T, name string, got *image.RGBA) {
	t.Helper()
	path := filepath.Join("testdata", name+".png")
	if remoteShotUpdate() {
		if err := remoteWritePNG(path, got); err != nil {
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
			if remoteFar(r1, r2) || remoteFar(g1, g2) || remoteFar(b1, b2) {
				diff++
			}
		}
	}
	if frac := float64(diff) / float64(b.Dx()*b.Dy()); frac > remotePageTol {
		out := filepath.Join(t.TempDir(), name+".png")
		_ = remoteWritePNG(out, got)
		t.Errorf("%.2f%% of the pixels differ from %s (limit %.2f%%); this run's image: %s", frac*100, path, remotePageTol*100, out)
	}
}

func remoteFar(a, b uint32) bool {
	a, b = a>>8, b>>8
	if a > b {
		return a-b > remotePixelTol
	}
	return b-a > remotePixelTol
}

func remoteWritePNG(path string, img image.Image) error {
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
