package guikit

import (
	"image/color"
	"math"
	"os"
	"path/filepath"
	"testing"

	"github.com/kairos-community/isotope/pkg/cosmictheme"
)

func TestInstallerPaletteIsTheColValues(t *testing.T) {
	p := InstallerPalette
	for _, c := range []struct {
		name      string
		got, want color.NRGBA
	}{
		{"Window", p.Window, ColWindow}, {"Sidebar", p.Sidebar, ColSidebar}, {"Footer", p.Footer, ColFooter},
		{"Text", p.Text, ColText}, {"Dim", p.Dim, ColDim}, {"Suggested", p.Suggested, ColAccent},
		{"Destructive", p.Destructive, ColError}, {"Focus", p.Focus, FocusRingStrong}, {"FocusSoft", p.FocusSoft, FocusRing},
		{"Shade", p.Shade, Alpha(ColKeyBox, 0.72)},
	} {
		if c.got != c.want {
			t.Errorf("%s = %v, want %v", c.name, c.got, c.want)
		}
	}
	var th Theme
	if th.Colors() != &InstallerPalette {
		t.Fatal("a theme with no palette draws the installer's")
	}
}

// contrast is the WCAG contrast ratio of two opaque colours.
func contrast(a, b color.NRGBA) float64 {
	lum := func(c color.NRGBA) float64 {
		ch := func(v uint8) float64 {
			x := float64(v) / 255
			if x <= 0.04045 {
				return x / 12.92
			}
			return math.Pow((x+0.055)/1.055, 2.4)
		}
		return 0.2126*ch(c.R) + 0.7152*ch(c.G) + 0.0722*ch(c.B)
	}
	hi, lo := lum(a), lum(b)
	if lo > hi {
		hi, lo = lo, hi
	}
	return (hi + 0.05) / (lo + 0.05)
}

// checkContrast fails for each text of p under 4.5:1 on its surface.
func checkContrast(t *testing.T, name string, p Palette) {
	t.Helper()
	pairs := []struct {
		what   string
		fg, bg color.NRGBA
	}{
		{"text on window", p.Text, p.Window}, {"text2 on window", p.Text2, p.Window}, {"dim on window", p.Dim, p.Window},
		{"text on group", p.Text, p.Group}, {"text2 on group", p.Text2, p.Group}, {"dim on group", p.Dim, p.Group},
		{"text on sidebar", p.Text, p.Sidebar}, {"text on selected", p.Text, p.Selected},
		{"button label", p.OnButton, p.Button}, {"suggested label", p.OnSuggested, p.Suggested},
		{"destructive label", p.OnDestructive, p.Destructive}, {"error text on window", p.ErrorText, p.Window},
		{"text on field", p.Text, p.Field},
	}
	for _, c := range pairs {
		if r := contrast(c.fg, c.bg); r < 4.5 {
			t.Errorf("%s: %s is %.2f:1 (%v on %v), below 4.5:1", name, c.what, r, c.fg, c.bg)
		}
	}
}

func TestIsotopePaletteContrast(t *testing.T) {
	checkContrast(t, "night", IsotopePalette(true))
	checkContrast(t, "day", IsotopePalette(false))
}

var cosmicDir = filepath.Join("..", "cosmictheme", "testdata", "usr", "share", "cosmic")

func TestCosmicPaletteFromIsotopeTheme(t *testing.T) {
	th := cosmictheme.Load([]string{cosmicDir}, os.ReadFile)
	p := CosmicPalette(th)
	if p.Window != RGB(0x141411) || p.Group != RGB(0x1d1d1a) || p.Suggested != RGB(0xe9e7c1) {
		t.Errorf("window %v group %v suggested %v", p.Window, p.Group, p.Suggested)
	}
	// button.base is translucent (#9F9F9B40): laid on the group, opaque.
	if p.Button.A != 0xff || p.Button == p.Group {
		t.Errorf("button %v must be the veil laid on the group", p.Button)
	}
	if p.RadiusS != 8 || p.RadiusM != 16 {
		t.Errorf("radii %v %v", p.RadiusS, p.RadiusM)
	}
	checkContrast(t, "COSMIC Isotope dark", p)
}

func TestCosmicPaletteLight(t *testing.T) {
	user := t.TempDir()
	mode := filepath.Join(user, "com.system76.CosmicTheme.Mode", "v1")
	if err := os.MkdirAll(mode, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(mode, "is_dark"), []byte("false"), 0o644); err != nil {
		t.Fatal(err)
	}
	p := CosmicPalette(cosmictheme.Load([]string{user, cosmicDir}, os.ReadFile))
	if p.Dark || p.Group != RGB(0xffffff) || p.Window != RGB(0xf4f5f2) {
		t.Fatalf("light: dark %v group %v window %v", p.Dark, p.Group, p.Window)
	}
	checkContrast(t, "COSMIC Isotope light", p)
}

func TestCosmicPaletteFallsBackPerKey(t *testing.T) {
	red := color.NRGBA{R: 0xff, A: 0xff}
	th := cosmictheme.Theme{Dark: true, Accent: cosmictheme.Component{Base: red, On: RGB(0xffffff), Focus: red},
		Have: map[string]bool{cosmictheme.KeyAccent: true}}
	p := CosmicPalette(th)
	want := IsotopePalette(true)
	if p.Accent != red {
		t.Fatalf("accent %v", p.Accent)
	}
	if p.Window != want.Window || p.Group != want.Group || p.Text != want.Text || p.Button != want.Button {
		t.Errorf("keys not in the theme must stay Isotope's: %+v", p)
	}
}
