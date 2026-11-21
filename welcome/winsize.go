package main

// The window opens at 1000x620 dp (the title bar included) and never goes
// below 640x360: the page scrolls and the sidebar closes up, so the window
// does not have to grow to show the content.
const (
	defaultW, defaultH = 1000, 620
	minW, minH         = 640, 360
	// Room a desktop keeps for itself on a screen: COSMIC's top bar (32 dp)
	// above, its dock (about 90 dp with its margin) below, and a margin.
	reserveTop, reserveBottom, reserveSide = 40, 110, 24
)

// WindowSize is the opening size in dp for a screen of w x h physical pixels
// at display scale s (a dp is one logical pixel, a physical pixel / s). The
// default, cut to what the desktop leaves free, and no smaller than the
// minimum. A screen that cannot be read (w or h 0) gets the default.
func WindowSize(w, h int, s float64) (int, int) {
	if w <= 0 || h <= 0 {
		return defaultW, defaultH
	}
	if s <= 0 {
		s = 1
	}
	lw, lh := int(float64(w)/s), int(float64(h)/s)
	return clamp(lw-2*reserveSide, minW, defaultW), clamp(lh-reserveTop-reserveBottom, minH, defaultH)
}

func clamp(v, lo, hi int) int { return min(max(v, lo), hi) }

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
	for _, o := range outs {
		if o.enabled && !o.mirroring && o.width > 0 {
			s := o.scale
			if s <= 0 {
				s = 1
			}
			return o.width, o.height, s
		}
	}
	return 0, 0, 1
}
