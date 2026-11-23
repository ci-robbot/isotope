package main

import "testing"

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
