package guikit

import (
	"bufio"
	"errors"
	"strconv"
	"strings"
)

// The opening size of a tool's window on a desktop. Isotope Welcome opens at
// 1000x620 dp (the title bar included); the graphical installer in its
// window mode opens at its own size (FitWindowSize). Neither goes below
// 640x360: the page scrolls and the sidebar closes up, so the window does not
// have to grow to show the content.
const (
	DefaultWindowW, DefaultWindowH = 1000, 620
	MinWindowW, MinWindowH         = 640, 360
	// Room a desktop keeps for itself on a screen: COSMIC's top bar (32 dp)
	// above, its dock (about 90 dp with its margin) below, and a margin.
	reserveTop, reserveBottom, reserveSide = 40, 110, 24
)

// WindowSize is Isotope Welcome's opening size in dp for a screen of w x h
// physical pixels at display scale s: FitWindowSize with the default size.
func WindowSize(w, h int, s float64) (int, int) {
	return FitWindowSize(w, h, s, DefaultWindowW, DefaultWindowH)
}

// FitWindowSize is the opening size in dp for a screen of w x h physical
// pixels at display scale s (a dp is one logical pixel, a physical pixel / s):
// defW x defH, cut to what the desktop leaves free, and no smaller than the
// minimum. A screen that cannot be read (w or h 0) gets defW x defH.
func FitWindowSize(w, h int, s float64, defW, defH int) (int, int) {
	if w <= 0 || h <= 0 {
		return defW, defH
	}
	if s <= 0 {
		s = 1
	}
	lw, lh := int(float64(w)/s), int(float64(h)/s)
	return clamp(lw-2*reserveSide, MinWindowW, defW), clamp(lh-reserveTop-reserveBottom, MinWindowH, defH)
}

func clamp(v, lo, hi int) int { return min(max(v, lo), hi) }

// CosmicOutput is one output of `cosmic-randr list --kdl`.
type CosmicOutput struct {
	Name                   string
	Enabled, Mirroring     bool
	Scale                  float64
	Width, Height, Refresh int
}

// ParseCosmicOutputs reads the output of `cosmic-randr list --kdl`
// (cosmic-randr epoch-1.11.0, list_kdl in cli/src/main.rs): an
// `output "<name>" enabled=#true {` node per output with `scale 1.25`, an
// optional `mirroring "<name>"` and `mode <w> <h> <mHz> current=#true` under
// `modes`.
func ParseCosmicOutputs(kdl []byte) ([]CosmicOutput, error) {
	var outs []CosmicOutput
	sc := bufio.NewScanner(strings.NewReader(string(kdl)))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) == 0 {
			continue
		}
		if f[0] == "output" && len(f) >= 2 {
			outs = append(outs, CosmicOutput{
				Name:    strings.Trim(f[1], `"`),
				Enabled: strings.Contains(sc.Text(), "enabled=#true"),
			})
			continue
		}
		if len(outs) == 0 {
			continue
		}
		o := &outs[len(outs)-1]
		switch f[0] {
		case "scale":
			if len(f) >= 2 {
				o.Scale, _ = strconv.ParseFloat(f[1], 64)
			}
		case "mirroring":
			o.Mirroring = true
		case "mode":
			if len(f) >= 5 && strings.Contains(sc.Text(), "current=#true") {
				o.Width, _ = strconv.Atoi(f[1])
				o.Height, _ = strconv.Atoi(f[2])
				o.Refresh, _ = strconv.Atoi(f[3])
			}
		}
	}
	if len(outs) == 0 {
		return nil, errors.New("cosmic-randr lists no outputs")
	}
	return outs, nil
}

// CosmicScreen is the physical size and scale of the first enabled output
// that mirrors no other, or 0, 0, 1 when there is none.
func CosmicScreen(outs []CosmicOutput) (w, h int, scale float64) {
	for _, o := range outs {
		if o.Enabled && !o.Mirroring && o.Width > 0 {
			s := o.Scale
			if s <= 0 {
				s = 1
			}
			return o.Width, o.Height, s
		}
	}
	return 0, 0, 1
}
