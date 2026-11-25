package main

import (
	"image"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"

	"github.com/kairos-community/isotope/pkg/guikit"
)

// cosmicTestDir is the Isotope COSMIC theme as the image ships it.
var cosmicTestDir = filepath.Join("..", "..", "pkg", "cosmictheme", "testdata", "usr", "share", "cosmic")

func TestPaletteSourceCosmic(t *testing.T) {
	src := paletteSource{cosmic: true, dirs: []string{t.TempDir(), cosmicTestDir}, read: os.ReadFile}
	p := src.palette()
	if !p.Dark || p.Window != guikit.RGB(0x141411) || p.Group != guikit.RGB(0x1d1d1a) {
		t.Fatalf("COSMIC dark: dark %v window %v group %v", p.Dark, p.Window, p.Group)
	}
}

func TestPaletteSourcePortal(t *testing.T) {
	for _, c := range []struct {
		name   string
		scheme func() (uint32, bool)
		dark   bool
	}{
		{"prefers dark", func() (uint32, bool) { return 1, true }, true},
		{"prefers light", func() (uint32, bool) { return 2, true }, false},
		{"no preference", func() (uint32, bool) { return 0, true }, true},
		{"no portal", func() (uint32, bool) { return 0, false }, true},
		{"nothing asked", nil, true},
	} {
		p := paletteSource{scheme: c.scheme}.palette()
		if p.Dark != c.dark || p != guikit.IsotopePalette(c.dark) {
			t.Errorf("%s: dark %v, want %v and Isotope's palette", c.name, p.Dark, c.dark)
		}
	}
}

func TestSchemeValueUnwrapsVariants(t *testing.T) {
	if v, ok := schemeValue(dbus.MakeVariant(uint32(2))); !ok || v != 2 {
		t.Errorf("ReadOne: %v %v", v, ok)
	}
	if v, ok := schemeValue(dbus.MakeVariant(dbus.MakeVariant(uint32(1)))); !ok || v != 1 {
		t.Errorf("Read: %v %v", v, ok)
	}
	if _, ok := schemeValue(dbus.MakeVariant("dark")); ok {
		t.Error("a string is not a color-scheme")
	}
}

func TestIsCosmic(t *testing.T) {
	for v, want := range map[string]bool{"COSMIC": true, "cosmic": true, "sway": false, "": false, "niri:COSMIC": true} {
		if got := isCosmic(func(string) string { return v }); got != want {
			t.Errorf("%q: %v", v, got)
		}
	}
}

// A theme switch in COSMIC Settings renames new key files into the user's
// theme directory: the watcher reloads.
func TestWatchCosmicSeesARename(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "com.system76.CosmicTheme.Mode", "v1")
	got := make(chan struct{}, 4)
	stop := make(chan struct{})
	defer close(stop)
	if err := watchCosmic([]string{dir}, func() { got <- struct{}{} }, stop); err != nil {
		t.Fatal(err)
	}
	tmp := filepath.Join(filepath.Dir(dir), ".is_dark.tmp")
	if err := os.WriteFile(tmp, []byte("false"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, filepath.Join(dir, "is_dark")); err != nil {
		t.Fatal(err)
	}
	select {
	case <-got:
	case <-time.After(5 * time.Second):
		t.Fatal("no reload after the key changed")
	}
}

func TestNarrowWindowHidesTheNavigation(t *testing.T) {
	u, _, _ := testUI(t, true)
	u.Layout(gtxOf(640, 480))
	if u.navOpen {
		t.Fatal("the navigation starts hidden on a narrow window")
	}
	u.navOpen = true
	u.Layout(gtxOf(640, 480))
	if !u.navOpen {
		t.Fatal("the toggle's state is kept on a narrow window")
	}
	u.show(PagePrinters)
	if u.navOpen {
		t.Fatal("picking a page hides the navigation")
	}
	u.navOpen = true
	u.Layout(gtxOf(960, 680))
	if u.navOpen {
		t.Fatal("a wide window shows the navigation beside the page, not over it")
	}
}

func TestHeaderBarFollowsThePage(t *testing.T) {
	u, _, _ := testUI(t, true)
	if u.bar != nil {
		t.Fatal("no header bar unless main asks for one (COSMIC)")
	}
	u.bar = &guikit.HeaderBar{Left: true}
	u.show(PageNetwork)
	u.Layout(gtxOf(640, 480))
	if u.bar.Title != PageNetwork.Title() || !u.bar.ShowNavButton {
		t.Fatalf("bar %q, nav button %v", u.bar.Title, u.bar.ShowNavButton)
	}
	u.Layout(gtxOf(960, 680))
	if u.bar.ShowNavButton {
		t.Fatal("no navigation button when the navigation shows")
	}
}

func TestPaletteSwapUnderADialog(t *testing.T) {
	u, _, _ := testUI(t, true)
	u.mu.Lock()
	u.dlg = newDialog("Remove?", "It goes.", nil, 0, choice{Label: "Cancel", Kind: guikit.ButtonSecondary, Do: func() {}})
	u.mu.Unlock()
	u.Layout(gtxOf(960, 680))
	light := guikit.IsotopePalette(false)
	u.SetPalette(light)
	u.Layout(gtxOf(960, 680))
	u.Layout(gtxOf(960, 680))
	if u.Colors().Window != light.Window || u.Colors().Shade != light.Shade {
		t.Fatalf("the palette did not change under the dialog: %v", u.Colors().Window)
	}
}

// renderTheme renders u at sz on the GPU (screenshot_test.go's render).
func pixelAt(img *image.RGBA, x, y int) [3]uint8 {
	c := img.RGBAAt(x, y)
	return [3]uint8{c.R, c.G, c.B}
}

func rgb3(c interface{ RGBA() (r, g, b, a uint32) }) [3]uint8 {
	r, g, b, _ := c.RGBA()
	return [3]uint8{uint8(r >> 8), uint8(g >> 8), uint8(b >> 8)}
}

// The window is the app: its corners are the sidebar and the page, never
// the installer's desktop or a card's shadow.
func TestFrameHasNoDesktop(t *testing.T) {
	needGPU(t)
	for _, dark := range []bool{true, false} {
		u, _, _ := testUI(t, true)
		pal := guikit.IsotopePalette(dark)
		u.SetPalette(pal)
		img := render(t, u, image.Pt(1200, 800))
		desk := map[[3]uint8]bool{rgb3(guikit.ColDesk1): true, rgb3(guikit.ColDesk2): true, rgb3(guikit.ColDesk3): true}
		for _, pt := range []image.Point{{0, 0}, {1199, 0}, {0, 799}, {1199, 799}, {600, 2}} {
			px := pixelAt(img, pt.X, pt.Y)
			if desk[px] && px != rgb3(pal.Window) {
				t.Errorf("dark %v: %v is the desktop's %v", dark, pt, px)
			}
			if px != rgb3(pal.Window) && px != rgb3(pal.Sidebar) {
				t.Errorf("dark %v: %v is %v, neither window %v nor sidebar %v", dark, pt, px, rgb3(pal.Window), rgb3(pal.Sidebar))
			}
		}
	}
}

// In light mode no surface keeps a night colour.
func TestLightPaletteHasNoNightSurfaces(t *testing.T) {
	needGPU(t)
	night := map[[3]uint8]bool{}
	for _, c := range []interface{ RGBA() (r, g, b, a uint32) }{guikit.ColWindow, guikit.ColSidebar, guikit.ColField, guikit.ColFooter, guikit.ColDesk2, guikit.ColDesk3} {
		night[rgb3(c)] = true
	}
	for _, p := range []Page{PageUpdates, PagePrinters, PageNetwork, PageUsers} {
		u, _, _ := testUI(t, true)
		u.SetPalette(guikit.IsotopePalette(false))
		u.show(p)
		u.pageAt = time.Time{}
		img := render(t, u, image.Pt(960, 680))
		n := 0
		b := img.Bounds()
		for y := b.Min.Y; y < b.Max.Y; y += 2 {
			for x := b.Min.X; x < b.Max.X; x += 2 {
				if night[pixelAt(img, x, y)] {
					n++
				}
			}
		}
		if n > 0 {
			t.Errorf("%s: %d sampled pixels in a night surface colour", p, n)
		}
	}
}
