package main

import "testing"

func TestWindowSize(t *testing.T) {
	for _, c := range []struct {
		name         string
		w, h         int
		s            float64
		wantW, wantH int
	}{
		{"unknown screen", 0, 0, 1, defaultW, defaultH},
		{"1280x800", 1280, 800, 1, 1000, 620},
		{"1366x768", 1366, 768, 1, 1000, 618},
		{"1920x1080", 1920, 1080, 1, 1000, 620},
		{"1280x800 at 1.5", 1280, 800, 1.5, 805, 383},
		{"tiny", 800, 600, 2, minW, minH},
		{"bad scale", 1280, 800, 0, 1000, 620},
	} {
		w, h := WindowSize(c.w, c.h, c.s)
		if w != c.wantW || h != c.wantH {
			t.Errorf("%s: %dx%d, want %dx%d", c.name, w, h, c.wantW, c.wantH)
		}
	}
}

func TestScreenSize(t *testing.T) {
	r := &fakeRunner{out: map[string]string{cosmicList: cosmicKDL}}
	if w, h, s := screenSize(r, Cosmic); w != 2560 || h != 1600 || s != 1.25 {
		t.Errorf("COSMIC: %d %d %v", w, h, s)
	}
	if w, _, _ := screenSize(r, Sway); w != 0 {
		t.Error("only COSMIC reads the screen")
	}
	if w, _, _ := screenSize(&fakeRunner{fail: []string{"cosmic-randr"}}, Cosmic); w != 0 {
		t.Error("a failing cosmic-randr must give the default")
	}
	if w, h, s := screenSize(nil, Cosmic); w != 0 || h != 0 || s != 1 {
		t.Errorf("nil runner: %d %d %v", w, h, s)
	}
	if w, _, _ := screenSize(nil, Sway); w != 0 {
		t.Error("only COSMIC reads the screen")
	}
}
