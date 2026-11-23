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

	"github.com/kairos-community/isotope/pkg/shares"
)

// The network shares screenshot tests render a few views of the window and
// compare them with the PNGs in testdata/, within a small tolerance: llvmpipe's
// anti-aliasing moves a little between Mesa releases. `go test -update`
// rewrites the PNGs; a change to a view shows in its PR. The harness is
// Isotope Welcome's (cmd/isotope-welcome/screenshot_test.go), with every
// name prefixed so that it does not clash with the other harnesses of this
// package.
//
// They need an EGL with a surfaceless platform (Mesa), and skip without one
// or with ISOTOPE_GUI_SKIP_SCREENSHOTS=1 (the image build, whose Mesa is not
// the one the PNGs were made with). Make the PNGs on the Mesa CI uses
// (Ubuntu 24.04's).
//
// The -update flag is shared with the other screenshot harnesses of this
// package: whichever file registers it first owns it, and this one only
// reads it.
func init() {
	if flag.Lookup("update") == nil {
		flag.Bool("update", false, "rewrite the golden screenshots in testdata/")
	}
}

// sharesShotUpdate says whether -update was given.
func sharesShotUpdate() bool {
	f := flag.Lookup("update")
	return f != nil && f.Value.String() == "true"
}

const (
	sharesPixelTol = 24
	sharesPageTol  = 0.004
)

var sharesShotSize = image.Pt(1100, 720)

// sharesShotAt is the fixed clock of the screenshots (the caret, the spinners).
var sharesShotAt = time.Date(2026, 11, 24, 9, 30, 0, 0, time.UTC)

// shareShots builds the views: a Disks page with a USB stick, two saved
// shares (one mounted) and a mounted share nobody saved; and the "Connect to
// server" dialog filled in, for SMB and for NFS (an administrator, and a
// user who may not mount NFS).
func shareShots(t *testing.T) map[string]*UI {
	t.Helper()
	page := func(admin bool) (*UI, *disksPage) {
		u, d, st, p := sharesUIAs(t, admin)
		u.now = func() time.Time { return sharesShotAt }
		f := u.hooks.Disks.(*fakeDisks)
		f.mu.Lock()
		f.drives = f.drives[:1]
		f.mu.Unlock()
		st.saved = []shares.Saved{
			{URL: "smb://nas.local/media", Name: "media", User: "anna", AutoMount: true},
			{URL: "nfs://files.lan/srv/projects", Name: "projects"},
		}
		d.mounted = []MountedShare{
			{MountPoint: "/run/media/anna/media", FSType: "cifs", Source: "//nas.local/media"},
			{MountPoint: "/run/media/anna/scans", FSType: "cifs", Source: "//printer/scans"},
		}
		showShares(t, u, p)
		u.pageAt = time.Time{} // no fade-in
		return u, p
	}
	out := map[string]*UI{}
	u, _ := page(false)
	out["shares-list"] = u

	u, p := page(false)
	p.connectDialog(nil)
	waitQuestion(t, u, "Connect to server")
	p.connForm.url.Editor.SetText("smb://nas.local/photos")
	p.connForm.user.Editor.SetText("anna")
	p.connForm.domain.Editor.SetText("HOME")
	p.connForm.password.Editor.SetText("correct horse")
	p.connForm.auto = true
	out["shares-connect"] = u

	u, p = page(true)
	p.connectDialog(nil)
	waitQuestion(t, u, "Connect to server")
	p.connForm.url.Editor.SetText("nfs://files.lan/srv/archive")
	out["shares-connect-nfs"] = u

	// NFS for a user who is not an administrator: the dialog says why.
	u, p = page(false)
	p.connectDialog(nil)
	waitQuestion(t, u, "Connect to server")
	p.connForm.url.Editor.SetText("nfs://files.lan/srv/archive")
	out["shares-connect-nfs-user"] = u
	return out
}

func TestSharesScreenshots(t *testing.T) {
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
	for name, u := range shareShots(t) {
		t.Run(name, func(t *testing.T) { sharesCompare(t, name, sharesRender(t, u, sharesShotSize)) })
	}
}

func sharesRender(t *testing.T, u *UI, sz image.Point) *image.RGBA {
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

func sharesCompare(t *testing.T, name string, got *image.RGBA) {
	t.Helper()
	path := filepath.Join("testdata", name+".png")
	if sharesShotUpdate() {
		if err := sharesWritePNG(path, got); err != nil {
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
			if sharesFar(r1, r2) || sharesFar(g1, g2) || sharesFar(b1, b2) {
				diff++
			}
		}
	}
	if frac := float64(diff) / float64(b.Dx()*b.Dy()); frac > sharesPageTol {
		out := filepath.Join(t.TempDir(), name+".png")
		_ = sharesWritePNG(out, got)
		t.Errorf("%.2f%% of the pixels differ from %s (limit %.2f%%); this run's image: %s", frac*100, path, sharesPageTol*100, out)
	}
}

func sharesFar(a, b uint32) bool {
	a, b = a>>8, b>>8
	if a > b {
		return a-b > sharesPixelTol
	}
	return b-a > sharesPixelTol
}

func sharesWritePNG(path string, img image.Image) error {
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
