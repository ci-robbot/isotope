package main

import "github.com/kairos-community/isotope/guikit"

// The window's opening size is guikit.WindowSize, shared with the graphical
// installer's window mode.

// screenSize is the physical size of the first enabled output on COSMIC
// (0, 0 when it cannot be read or elsewhere), with its scale.
func screenSize(r Runner, d Desktop) (w, h int, scale float64) {
	if d != Cosmic || r == nil {
		return 0, 0, 1
	}
	outs, err := cosmicOutputs(r)
	if err != nil {
		return 0, 0, 1
	}
	return guikit.CosmicScreen(outs)
}
