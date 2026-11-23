package main

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const (
	swayOutputs    = "swaymsg -t get_outputs -r"
	niriOutputsCmd = "niri msg --json outputs"
	cosmicList     = "cosmic-randr list --kdl"
	xrdbQuery      = "xrdb -query"
	textGet        = "gsettings get org.gnome.desktop.interface text-scaling-factor"
)

// niriJSON is `niri msg --json outputs` with two outputs, the laptop at 2.
const niriJSON = `{
  "eDP-1": {"name": "eDP-1", "make": "BOE", "model": "0x0BCA", "logical": {"x": 0, "y": 0, "width": 1280, "height": 800, "scale": 2.0, "transform": "Normal"}},
  "HDMI-A-1": {"name": "HDMI-A-1", "make": "Dell", "model": "U2720Q", "logical": {"x": 1280, "y": 0, "width": 2560, "height": 1440, "scale": 1.0, "transform": "Normal"}}
}`

// cosmicKDL is `cosmic-randr list --kdl` at epoch-1.11.0: an enabled
// laptop panel at 1.25, a disabled monitor.
const cosmicKDL = `output "eDP-1" enabled=#true {
  description make="BOE" model="0x0BCA"
  physical 302 189
  position 0 0
  scale 1.25
  adaptive_sync #false
  modes {
    mode 2560 1600 60000 current=#true preferred=#true
    mode 1920 1200 59950
  }
}
output "HDMI-A-1" enabled=#false {
  description make="Dell" model="U2720Q"
  physical 597 336
  position 2048 0
  scale 1.00
  modes {
    mode 3840 2160 60000 preferred=#true
  }
}
`

func TestScales(t *testing.T) {
	if want := []float64{1, 1.25, 1.5, 1.75, 2}; !reflect.DeepEqual(Scales, want) {
		t.Fatalf("Scales = %v", Scales)
	}
}

func TestApplyScaleSway(t *testing.T) {
	home := t.TempDir()
	r := &fakeRunner{}
	if err := ApplyScale(r, home, Sway, 1.5); err != nil {
		t.Fatal(err)
	}
	if want := []string{"swaymsg output * scale 1.5"}; !reflect.DeepEqual(r.called(), want) {
		t.Fatalf("calls = %q", r.called())
	}
	got := readFile(t, filepath.Join(home, ".config/sway/isotope-scale.conf"))
	if !strings.HasSuffix(got, "\noutput * scale 1.5\n") || strings.Count(got, "output ") != 1 {
		t.Fatalf("isotope-scale.conf:\n%s", got)
	}
	if _, err := os.Stat(filepath.Join(home, ".config/sway/config")); err == nil {
		t.Fatal("wrote ~/.config/sway/config, which replaces the Isotope config")
	}
}

func TestApplyScaleNiri(t *testing.T) {
	home := t.TempDir()
	r := &fakeRunner{out: map[string]string{niriOutputsCmd: niriJSON}}
	if err := ApplyScale(r, home, Niri, 1.5); err != nil {
		t.Fatal(err)
	}
	want := []string{niriOutputsCmd, "niri msg output HDMI-A-1 scale 1.5", "niri msg output eDP-1 scale 1.5"}
	if !reflect.DeepEqual(r.called(), want) {
		t.Fatalf("calls = %q", r.called())
	}
	got := readFile(t, filepath.Join(home, ".config/niri/isotope-scale.kdl"))
	for _, s := range []string{"output \"HDMI-A-1\" {\n    scale 1.5\n}\n", "output \"eDP-1\" {\n    scale 1.5\n}\n"} {
		if !strings.Contains(got, s) {
			t.Fatalf("isotope-scale.kdl has no %q:\n%s", s, got)
		}
	}
	if _, err := os.Stat(filepath.Join(home, ".config/niri/config.kdl")); err == nil {
		t.Fatal("wrote ~/.config/niri/config.kdl, which replaces /etc/niri/config.kdl")
	}
}

// An output that is not plugged in keeps the scale an earlier run gave it.
func TestApplyScaleNiriKeepsOtherOutputs(t *testing.T) {
	home := t.TempDir()
	p := filepath.Join(home, ".config/niri/isotope-scale.kdl")
	os.MkdirAll(filepath.Dir(p), 0o755)
	os.WriteFile(p, []byte(niriScaleHeader+"output \"DP-3\" {\n    scale 1.75\n}\n\noutput \"eDP-1\" {\n    scale 2\n}\n"), 0o644)
	r := &fakeRunner{out: map[string]string{niriOutputsCmd: niriJSON}}
	if err := ApplyScale(r, home, Niri, 1.25); err != nil {
		t.Fatal(err)
	}
	got := readFile(t, p)
	for _, s := range []string{"output \"DP-3\" {\n    scale 1.75\n}", "output \"eDP-1\" {\n    scale 1.25\n}", "output \"HDMI-A-1\" {\n    scale 1.25\n}"} {
		if !strings.Contains(got, s) {
			t.Fatalf("no %q in:\n%s", s, got)
		}
	}
	if strings.Count(got, "eDP-1") != 1 {
		t.Fatalf("eDP-1 twice:\n%s", got)
	}
}

func TestApplyScaleI3(t *testing.T) {
	home := t.TempDir()
	r := &fakeRunner{}
	if err := ApplyScale(r, home, I3, 1.5); err != nil {
		t.Fatal(err)
	}
	xres := filepath.Join(home, ".Xresources")
	want := []string{"xrdb -merge " + xres, "i3-msg restart"}
	if !reflect.DeepEqual(r.called(), want) {
		t.Fatalf("calls = %q", r.called())
	}
	if got := readFile(t, xres); got != "Xft.dpi: 144\n" {
		t.Fatalf(".Xresources = %q", got)
	}
}

func TestApplyScaleKeepsXresources(t *testing.T) {
	home := t.TempDir()
	xres := filepath.Join(home, ".Xresources")
	os.WriteFile(xres, []byte("! mine\nXTerm*faceName: Monospace\nXft.dpi:\t120\nXft.antialias: true\n"), 0o644)
	if err := ApplyScale(&fakeRunner{}, home, I3, 1.75); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, xres); got != "! mine\nXTerm*faceName: Monospace\nXft.dpi: 168\nXft.antialias: true\n" {
		t.Fatalf(".Xresources = %q", got)
	}
}

func TestApplyScaleCosmic(t *testing.T) {
	home := t.TempDir()
	r := &fakeRunner{out: map[string]string{cosmicList: cosmicKDL}}
	if err := ApplyScale(r, home, Cosmic, 1.5); err != nil {
		t.Fatal(err)
	}
	// What COSMIC Settings' display page runs; cosmic-comp keeps it in
	// ~/.local/state/cosmic-comp/outputs.ron. The disabled output is left.
	want := []string{cosmicList, "cosmic-randr mode --scale 1.50 --refresh 60.000 eDP-1 2560 1600"}
	if !reflect.DeepEqual(r.called(), want) {
		t.Fatalf("calls = %q", r.called())
	}
	if ents, _ := os.ReadDir(home); len(ents) != 0 {
		t.Fatalf("COSMIC wrote files in the home: %v", ents)
	}
}

func TestApplyScaleFailure(t *testing.T) {
	// The file is written first, so a failed live change still persists.
	kept := map[Desktop]string{Sway: ".config/sway/isotope-scale.conf", Niri: ".config/niri/isotope-scale.kdl", I3: ".Xresources"}
	for _, d := range []Desktop{Sway, Niri, I3, Cosmic} {
		r := &fakeRunner{out: map[string]string{niriOutputsCmd: niriJSON, cosmicList: cosmicKDL}, fail: []string{"scale", "xrdb", "i3-msg"}}
		home := t.TempDir()
		if err := ApplyScale(r, home, d, 2); err == nil {
			t.Errorf("%s: no error when the command failed", d)
		}
		if f, ok := kept[d]; ok {
			if _, err := os.Stat(filepath.Join(home, f)); err != nil {
				t.Errorf("%s: not persisted before the live change: %v", d, err)
			}
		}
	}
}

func TestCurrentScale(t *testing.T) {
	cases := []struct {
		d    Desktop
		out  map[string]string
		want float64
	}{
		{Sway, map[string]string{swayOutputs: `[{"name":"eDP-1","active":false,"scale":-1},{"name":"DP-1","active":true,"scale":1.75}]`}, 1.75},
		{Niri, map[string]string{niriOutputsCmd: `{"eDP-1": {"name": "eDP-1", "logical": {"scale": 2.0}}}`}, 2},
		{I3, map[string]string{xrdbQuery: "Xft.antialias:\t1\nXft.dpi:\t120\n"}, 1.25},
		{I3, map[string]string{xrdbQuery: "Xft.antialias:\t1\n"}, 1},
		{Cosmic, map[string]string{cosmicList: cosmicKDL}, 1.25},
		{Sway, nil, 1},
	}
	for _, c := range cases {
		if got := CurrentScale(&fakeRunner{out: c.out}, c.d); got != c.want {
			t.Errorf("%s %v: %v, want %v", c.d, c.out, got, c.want)
		}
	}
	if got := CurrentScale(&fakeRunner{fail: []string{"niri"}}, Niri); got != 1 {
		t.Errorf("failed niri: %v", got)
	}
}

func TestNearestScale(t *testing.T) {
	for in, want := range map[float64]float64{1: 1, 1.2: 1.25, 1.333: 1.25, 1.6: 1.5, 3: 2, 0.5: 1} {
		if got := NearestScale(in); got != want {
			t.Errorf("NearestScale(%v) = %v, want %v", in, got, want)
		}
	}
}

func TestLargerText(t *testing.T) {
	for _, d := range []Desktop{Sway, I3, Niri} {
		r := &fakeRunner{}
		home := t.TempDir()
		if err := SetLargerText(r, home, d, true); err != nil {
			t.Fatal(err)
		}
		if err := SetLargerText(r, home, d, false); err != nil {
			t.Fatal(err)
		}
		want := []string{
			"gsettings set org.gnome.desktop.interface text-scaling-factor 1.25",
			"gsettings set org.gnome.desktop.interface text-scaling-factor 1.0",
		}
		if !reflect.DeepEqual(r.called(), want) {
			t.Fatalf("%s: calls = %q", d, r.called())
		}
	}
	// COSMIC's portal answers 1.0 whatever the key says: nothing is set.
	r := &fakeRunner{}
	if err := SetLargerText(r, t.TempDir(), Cosmic, true); err != nil || len(r.called()) != 0 {
		t.Fatalf("COSMIC: err %v, calls %q", err, r.called())
	}
	if err := SetLargerText(&fakeRunner{fail: []string{"gsettings"}}, t.TempDir(), Sway, true); err == nil {
		t.Fatal("no error from a failed gsettings")
	}
	if !ReadLargerText(&fakeRunner{out: map[string]string{textGet: "1.25\n"}}) {
		t.Fatal("1.25 is larger text")
	}
	if ReadLargerText(&fakeRunner{out: map[string]string{textGet: "1.0\n"}}) || ReadLargerText(&fakeRunner{fail: []string{"gsettings"}}) {
		t.Fatal("1.0, or unknown, is not larger text")
	}
}
