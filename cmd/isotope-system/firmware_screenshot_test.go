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

	"github.com/kairos-community/isotope/pkg/fwupd"
)

// The Updates page's FIRMWARE section in its states, rendered at 1100x720
// (the preview size) with the page scrolled down to the section, and
// compared with testdata/firmware-*.png within a small tolerance, as the
// automatic update screenshots (page_updates_screenshot_test.go, whose
// harness this copies). `go test -tags nox11,novulkan -run
// TestFirmwareScreenshots ./cmd/isotope-system/ -update` rewrites them.
// Every helper here is private to this file and named for it. The -update
// flag is shared: this file defines it only when no other file has.
//
// It needs an EGL with a surfaceless platform (Mesa), and skips without
// one or with ISOTOPE_GUI_SKIP_SCREENSHOTS=1; ISOTOPE_GUI_REQUIRE_SCREENSHOTS=1
// fails instead.
func init() {
	if flag.Lookup("update") == nil {
		flag.Bool("update", false, "rewrite the golden screenshots in testdata/")
	}
}

// firmwareUpdate says whether -update was given.
func firmwareUpdate() bool {
	f := flag.Lookup("update")
	return f != nil && f.Value.String() == "true"
}

const (
	firmwarePixelTol = 24
	firmwarePageTol  = 0.004
)

var firmwareShotSize = image.Pt(1100, 720)

// firmwareScroll is how far down the page is scrolled, in pixels: the
// heading and the status line go out of view, the section comes in.
const firmwareScroll = 240

// firmwareClock is the fixed time of every screenshot.
var firmwareClock = time.Date(2026, 11, 24, 10, 0, 0, 0, time.Local)

// firmwareShots are the section's states: devices with updates, one being
// written, one staged for a restart, and fwupd missing. admin false is the
// view of a user who is not an administrator.
var firmwareShots = []struct {
	name  string
	admin bool
	setup func(f *fakeFirmware)
	in    func(in *firmwareInput)
}{
	{"firmware-updates", true, nil, nil},
	{"firmware-not-admin", false, nil, nil},
	{"firmware-installing", true, nil, func(in *firmwareInput) { in.Working, in.Line = "ssd", "Writing… 37%" }},
	{"firmware-restart", true, func(f *fakeFirmware) {
		f.devices[1].Flags |= fwupd.FlagNeedsReboot
		f.updates = f.updates[1:]
	}, nil},
	{"firmware-missing", true, func(f *fakeFirmware) {
		f.err = firmwareMissingErr
	}, nil},
}

func TestFirmwareScreenshots(t *testing.T) {
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
	for _, c := range firmwareShots {
		t.Run(c.name, func(t *testing.T) {
			f := fwFake()
			if c.setup != nil {
				c.setup(f)
			}
			u, p, d := firmwareUI(t, c.admin, f)
			u.now = func() time.Time { return firmwareClock }
			eventually(t, "the page's reads", func() bool {
				u.mu.Lock()
				defer u.mu.Unlock()
				return p.auto.Loaded && !p.checking && len(p.tags) > 0
			})
			eventually(t, "the history read", func() bool { return contains(d.called(), "UpdateHistory") })
			if c.in != nil {
				u.mu.Lock()
				c.in(&p.firm.in)
				u.mu.Unlock()
			}
			u.pageAt = time.Time{}
			p.list.Position.Offset = firmwareScroll
			firmwareCompare(t, fmt.Sprintf("%s-%dx%d", c.name, firmwareShotSize.X, firmwareShotSize.Y), firmwareRender(t, u, firmwareShotSize))
		})
	}
}

func firmwareRender(t *testing.T, u *UI, sz image.Point) *image.RGBA {
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

func firmwareCompare(t *testing.T, name string, got *image.RGBA) {
	t.Helper()
	path := filepath.Join("testdata", name+".png")
	if firmwareUpdate() {
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
		t.Fatalf("%v (run go test -run TestFirmwareScreenshots ./cmd/isotope-system/ -update to make it)", err)
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
			if firmwareFar(r1, r2) || firmwareFar(g1, g2) || firmwareFar(b1, b2) {
				diff++
			}
		}
	}
	if frac := float64(diff) / float64(b.Dx()*b.Dy()); frac > firmwarePageTol {
		out := filepath.Join(os.TempDir(), "isotope-system-"+name+".png")
		if f, err := os.Create(out); err == nil {
			_ = png.Encode(f, got)
			f.Close()
		}
		t.Fatalf("%s: %.2f%% of the pixels differ (more than %.1f%%); got %s", name, 100*frac, 100*firmwarePageTol, out)
	}
}

func firmwareFar(a, b uint32) bool {
	a, b = a>>8, b>>8
	if a > b {
		return a-b > firmwarePixelTol
	}
	return b-a > firmwarePixelTol
}
