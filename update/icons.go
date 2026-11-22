package main

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"math"
)

// The tray icons are drawn here at startup instead of shipped as PNG files,
// so the repository carries no binary assets. Every icon is the Isotope mark
// (docs/assets/isotope-mark.svg) in the panel's text colour, like the other
// icons on the panel. Up to date is the mark alone: nothing to act on, so
// nothing stands out. Every other state cuts a round badge into the mark's
// bottom-right corner, in the state's palette colour with a glyph on it, so
// the one coloured thing on a quiet panel is the thing that wants you.
//
// The tray hands the panel a finished pixmap, so the icon cannot follow the
// panel's colours by itself: there is one set for dark panels and one for
// light panels, and the tray picks one from the desktop's colour scheme.
// Colours: generated from theme/palette.toml by tools/gen-theme.py (night for
// dark panels, day for light ones).

// iconTheme is the colours of the icons on one kind of panel.
type iconTheme struct {
	mark  color.NRGBA            // the Isotope mark: the panel's text colour
	badge map[string]color.NRGBA // the badge of each state that has one
	glyph color.NRGBA            // the glyph on a badge
}

// iconThemes is keyed by dark: true for a dark panel, false for a light one.
var iconThemes = map[bool]iconTheme{
	true: {
		mark: rgb(0xf4f5f2),
		badge: map[string]color.NRGBA{
			"update": rgb(0xe9e7c1),
			"busy":   rgb(0xea8a18),
			"reboot": rgb(0xd896db),
			"error":  rgb(0xff7172),
		},
		glyph: rgb(0x141411),
	},
	false: {
		mark: rgb(0x1c1d19),
		badge: map[string]color.NRGBA{
			"update": rgb(0x3e3e32),
			"busy":   rgb(0x832d00),
			"reboot": rgb(0x6432a9),
			"error":  rgb(0xb00e50),
		},
		glyph: rgb(0xffffff),
	},
}

// iconKinds is every icon the tray shows.
var iconKinds = []string{"idle", "update", "busy", "reboot", "error"}

// rgb is the opaque colour 0xRRGGBB.
func rgb(v uint32) color.NRGBA {
	return color.NRGBA{uint8(v >> 16), uint8(v >> 8), uint8(v), 0xff}
}

// iconFor is the icon of a daemon state.
func iconFor(s State) string {
	switch s {
	case StateUpdateAvailable:
		return "update"
	case StateInspecting, StateApplying, StateExtension, StateBackup, StateRestore:
		return "busy"
	case StateRebootPending:
		return "reboot"
	case StateError:
		return "error"
	}
	return "idle"
}

// The mark, in the 512 × 512 box of docs/assets/isotope-mark.svg: the open
// hexagon (ring), the pushed side above it (spark) and the core.
var (
	markRing = [][2]float64{
		{423.12, 171.21}, {423.12, 376.01}, {245.76, 478.41}, {68.40, 376.01},
		{68.40, 171.21}, {245.76, 68.81}, {245.76, 104.29}, {99.12, 188.95},
		{99.12, 358.28}, {245.76, 442.94}, {392.40, 358.28}, {392.40, 188.95},
	}
	markSpark = [][2]float64{{266.24, 33.34}, {443.60, 135.74}, {412.88, 153.48}, {266.24, 68.81}}
)

const (
	coreX, coreY, coreR = 245.76, 273.61, 34.82
	// The badge sits in the bottom-right corner, inside the box; the cut is
	// a wider circle around it, so a gap of panel separates badge and mark.
	badgeX, badgeY, badgeR = 404.0, 404.0, 104.0
	cutR                   = 132.0
)

// inPolygon reports whether (x, y) is inside the polygon p (even-odd rule).
func inPolygon(p [][2]float64, x, y float64) bool {
	in := false
	for i, j := 0, len(p)-1; i < len(p); j, i = i, i+1 {
		xi, yi, xj, yj := p[i][0], p[i][1], p[j][0], p[j][1]
		if (yi > y) != (yj > y) && x < (xj-xi)*(y-yi)/(yj-yi)+xi {
			in = !in
		}
	}
	return in
}

// inMark reports whether (x, y), in the 512 box, is on the mark.
func inMark(x, y float64) bool {
	return inPolygon(markRing, x, y) || inPolygon(markSpark, x, y) || math.Hypot(x-coreX, y-coreY) < coreR
}

// segDist is the distance from (x, y) to the segment a-b.
func segDist(x, y, ax, ay, bx, by float64) float64 {
	dx, dy := bx-ax, by-ay
	t := math.Max(0, math.Min(1, ((x-ax)*dx+(y-ay)*dy)/(dx*dx+dy*dy)))
	return math.Hypot(x-ax-t*dx, y-ay-t*dy)
}

// glyph reports whether the point (x, y) belongs to the badge glyph of kind.
// The glyphs are drawn in a 24 × 24 box (y grows down) whose middle 80%
// covers the badge, as round-capped strokes and dots.
func glyph(kind string, x, y float64) bool {
	switch kind {
	case "update": // an arrow down: a release to install
		return segDist(x, y, 12, 5, 12, 15) < 1.5 ||
			segDist(x, y, 7.5, 11, 12, 15.5) < 1.5 || segDist(x, y, 12, 15.5, 16.5, 11) < 1.5
	case "busy": // three dots: working
		for _, cx := range []float64{6, 12, 18} {
			if math.Hypot(x-cx, y-12) < 2.4 {
				return true
			}
		}
	case "reboot": // a restart arrow: an open circle and its arrowhead
		a := math.Atan2(y-12.5, x-12) * 180 / math.Pi // 0 = right, 90 = down
		if math.Abs(math.Hypot(x-12, y-12.5)-6) < 1.4 && (a >= 0 || a < -42) {
			return true
		}
		return segDist(x, y, 17, 4.5, 17, 8.8) < 1.4 || segDist(x, y, 17, 8.8, 12.7, 8.8) < 1.4
	case "error": // an exclamation mark
		return segDist(x, y, 12, 5.5, 12, 13.5) < 1.6 || math.Hypot(x-12, y-18) < 1.9
	}
	return false
}

// iconPNG draws the icon of kind for a dark or light panel, at size × size
// pixels, 4×4 supersampled.
func iconPNG(kind string, dark bool, size int) []byte {
	const ss = 4
	th := iconThemes[dark]
	bg, badged := th.badge[kind]
	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	scale := 512 / float64(size)
	for py := 0; py < size; py++ {
		for px := 0; px < size; px++ {
			// Sum the colour of every covered sample; alpha is coverage.
			var r, g, b, n int
			add := func(c color.NRGBA) { r, g, b, n = r+int(c.R), g+int(c.G), b+int(c.B), n+1 }
			for sy := 0; sy < ss; sy++ {
				for sx := 0; sx < ss; sx++ {
					x := (float64(px) + (float64(sx)+0.5)/ss) * scale
					y := (float64(py) + (float64(sy)+0.5)/ss) * scale
					d := math.Hypot(x-badgeX, y-badgeY)
					switch {
					case badged && d < badgeR:
						// The badge's 24-unit glyph box spans 80% of it.
						gx := 12 + (x-badgeX)/(badgeR*0.8)*12
						gy := 12 + (y-badgeY)/(badgeR*0.8)*12
						if glyph(kind, gx, gy) {
							add(th.glyph)
						} else {
							add(bg)
						}
					case badged && d < cutR:
						// The gap around the badge: panel shows through.
					case inMark(x, y):
						add(th.mark)
					}
				}
			}
			if n == 0 {
				continue
			}
			img.SetNRGBA(px, py, color.NRGBA{uint8(r / n), uint8(g / n), uint8(b / n), uint8(0xff * n / (ss * ss))})
		}
	}
	var buf bytes.Buffer
	_ = png.Encode(&buf, img) // encoding an in-memory NRGBA cannot fail
	return buf.Bytes()
}
