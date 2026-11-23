package guikit

import "testing"

func TestWindowSize(t *testing.T) {
	for _, c := range []struct {
		name         string
		w, h         int
		s            float64
		wantW, wantH int
	}{
		{"unknown screen", 0, 0, 1, DefaultWindowW, DefaultWindowH},
		{"1280x800", 1280, 800, 1, 1000, 620},
		{"1366x768", 1366, 768, 1, 1000, 618},
		{"1920x1080", 1920, 1080, 1, 1000, 620},
		{"1280x800 at 1.5", 1280, 800, 1.5, 805, 383},
		{"tiny", 800, 600, 2, MinWindowW, MinWindowH},
		{"bad scale", 1280, 800, 0, 1000, 620},
	} {
		w, h := WindowSize(c.w, c.h, c.s)
		if w != c.wantW || h != c.wantH {
			t.Errorf("%s: %dx%d, want %dx%d", c.name, w, h, c.wantW, c.wantH)
		}
	}
}

func TestFitWindowSize(t *testing.T) {
	// The installer's window: its own default, cut to the screen.
	if w, h := FitWindowSize(0, 0, 1, 920, 660); w != 920 || h != 660 {
		t.Errorf("unknown screen: %dx%d", w, h)
	}
	if w, h := FitWindowSize(1366, 768, 1, 920, 660); w != 920 || h != 618 {
		t.Errorf("1366x768: %dx%d", w, h)
	}
}

func TestParseCosmicOutputs(t *testing.T) {
	kdl := `output "eDP-1" enabled=#true {
    scale 1.25
    modes {
        mode 2560 1600 60000 current=#true
    }
}
output "HDMI-A-1" enabled=#true {
    mirroring "eDP-1"
}
`
	outs, err := ParseCosmicOutputs([]byte(kdl))
	if err != nil || len(outs) != 2 {
		t.Fatalf("%v %v", outs, err)
	}
	if w, h, s := CosmicScreen(outs); w != 2560 || h != 1600 || s != 1.25 {
		t.Errorf("screen %d %d %v", w, h, s)
	}
	if _, err := ParseCosmicOutputs(nil); err == nil {
		t.Error("no outputs must be an error")
	}
}
