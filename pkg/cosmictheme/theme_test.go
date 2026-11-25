package cosmictheme

import (
	"image/color"
	"os"
	"path/filepath"
	"testing"
)

// testdata/usr/share/cosmic is the Isotope theme as tools/isotope-cosmic-theme
// writes it into the image (the keys Load reads, both modes, and the mode).
var sysDir = filepath.Join("testdata", "usr", "share", "cosmic")

func rgba(v uint32) color.NRGBA {
	return color.NRGBA{R: uint8(v >> 24), G: uint8(v >> 16), B: uint8(v >> 8), A: uint8(v)}
}

// files is a read function over a map, for user directories in the tests.
func files(m map[string]string, fallback func(string) ([]byte, error)) func(string) ([]byte, error) {
	return func(p string) ([]byte, error) {
		if s, ok := m[p]; ok {
			return []byte(s), nil
		}
		return fallback(p)
	}
}

func TestLoadIsotopeDark(t *testing.T) {
	th := Load([]string{"/nonexistent", sysDir}, os.ReadFile)
	if !th.Dark {
		t.Fatal("the image's mode is dark")
	}
	for _, k := range []string{KeyBackground, KeyPrimary, KeyAccent, KeyAccentButton, KeyButton, KeyDestructive, KeyWarning, KeySuccess, KeyShade, KeyCornerRadii} {
		if !th.Have[k] {
			t.Errorf("key %s not read", k)
		}
	}
	checks := []struct {
		name      string
		got, want color.NRGBA
	}{
		{"background.base", th.Background.Base, rgba(0x141411FF)},
		{"background.component.selected", th.Background.Component.Selected, rgba(0x3D3D3AFF)},
		{"primary.base", th.Primary.Base, rgba(0x1D1D1AFF)},
		{"primary.on", th.Primary.On, rgba(0xF9F9F4FF)},
		{"primary.divider", th.Primary.Divider, rgba(0x494946FF)},
		{"accent.base", th.Accent.Base, rgba(0xE9E7C1FF)},
		{"accent_button.on", th.AccentButton.On, rgba(0x141411FF)},
		{"button.base", th.Button.Base, rgba(0x9F9F9B40)},
		{"destructive.base", th.Destructive.Base, rgba(0xFF7172FF)},
		{"shade", th.Shade, rgba(0x00000052)},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s = %v, want %v", c.name, c.got, c.want)
		}
	}
	if th.RadiusS != 8 || th.RadiusM != 16 {
		t.Errorf("radii %v, %v; want 8, 16", th.RadiusS, th.RadiusM)
	}
}

func TestLoadIsotopeLight(t *testing.T) {
	user := t.TempDir()
	mode := filepath.Join(user, modeDir)
	if err := os.MkdirAll(mode, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(mode, "is_dark"), []byte("false"), 0o644); err != nil {
		t.Fatal(err)
	}
	th := Load([]string{user, sysDir}, os.ReadFile)
	if th.Dark {
		t.Fatal("the user chose light")
	}
	if th.Primary.Base != rgba(0xFFFFFFFF) || th.Background.Base != rgba(0xF4F5F2FF) {
		t.Errorf("light surfaces: %v, %v", th.Primary.Base, th.Background.Base)
	}
	if th.Accent.Base != rgba(0x3E3E32FF) {
		t.Errorf("light accent %v", th.Accent.Base)
	}
}

func TestUserKeyOverridesSystem(t *testing.T) {
	user := "/home/u/.config/cosmic"
	read := files(map[string]string{
		filepath.Join(user, darkDir, "accent"): `(base: "#FF0000", hover: "#FF0000", pressed: "#FF0000", selected: "#FF0000",
			selected_text: "#FF0000", focus: "#FF0000", divider: "#FF0000", on: "#000000",
			disabled: "#FF0000", on_disabled: "#FF0000", border: "#FF0000", disabled_border: "#FF0000",)`,
	}, os.ReadFile)
	th := Load([]string{user, sysDir}, read)
	if th.Accent.Base != rgba(0xFF0000FF) {
		t.Errorf("accent %v: the user's key must win", th.Accent.Base)
	}
	if th.Primary.Base != rgba(0x1D1D1AFF) {
		t.Errorf("primary %v: keys the user did not change come from the system", th.Primary.Base)
	}
}

func TestMalformedKeyIsSkipped(t *testing.T) {
	read := files(map[string]string{
		filepath.Join("/only", darkDir, "accent"):  `(base: "#FF00`,
		filepath.Join("/only", darkDir, "primary"): `(base: "#101010FF")`, // no component
	}, func(string) ([]byte, error) { return nil, os.ErrNotExist })
	th := Load([]string{"/only"}, read)
	if th.Have[KeyAccent] || th.Have[KeyPrimary] {
		t.Errorf("a broken key counts as read: %v", th.Have)
	}
	if th.Accent.Base != (color.NRGBA{}) {
		t.Errorf("a broken key left %v", th.Accent.Base)
	}
}

func TestIsDarkDefaultsToTrue(t *testing.T) {
	none := func(string) ([]byte, error) { return nil, os.ErrNotExist }
	if !IsDark([]string{"/a", "/b"}, none) {
		t.Fatal("no mode file: dark")
	}
}

func TestParseLegacyFloatColour(t *testing.T) {
	v, err := parse([]byte(`(red: 1.0, green: 0.5, blue: 0.0, alpha: 1.0)`))
	if err != nil {
		t.Fatal(err)
	}
	c, err := v.colour()
	if err != nil {
		t.Fatal(err)
	}
	if c != (color.NRGBA{R: 255, G: 128, B: 0, A: 255}) {
		t.Fatalf("got %v", c)
	}
}

func TestParseSubset(t *testing.T) {
	for _, src := range []string{
		`Some((a: 1, b: "x",))`, `None`, `Round`, `(1, 2, 3)`, `()`, `// c
		(a: (1.0, 2.0), b: true)`,
	} {
		if _, err := parse([]byte(src)); err != nil {
			t.Errorf("%q: %v", src, err)
		}
	}
	for _, src := range []string{`(a: 1, 2)`, `(1, a: 2)`, `"open`, `(a: 1`, `#`} {
		if _, err := parse([]byte(src)); err == nil {
			t.Errorf("%q parsed", src)
		}
	}
}

func TestDirsAndWatchDirs(t *testing.T) {
	env := map[string]string{}
	d := Dirs(func(k string) string { return env[k] }, "/home/u")
	if d[0] != "/home/u/.config/cosmic" || d[1] != SystemDir {
		t.Fatalf("dirs %v", d)
	}
	env["XDG_CONFIG_HOME"] = "/cfg"
	d = Dirs(func(k string) string { return env[k] }, "/home/u")
	if d[0] != "/cfg/cosmic" {
		t.Fatalf("dirs %v", d)
	}
	w := WatchDirs(d)
	if len(w) != 3 || w[0] != "/cfg/cosmic/"+modeDir {
		t.Fatalf("watch %v", w)
	}
}
