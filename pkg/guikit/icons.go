package guikit

import (
	"image"
	"image/color"
	"math"

	"gioui.org/f32"
	"gioui.org/layout"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
)

// IconName names one of the line icons of a settings window's navigation.
// They are drawn here rather than read from the icon theme, so they follow
// the palette, need no SVG reader and look the same in the screenshot tests
// as on a desktop.
type IconName string

// The icons.
const (
	IconApps       IconName = "apps"
	IconUpdates    IconName = "updates"
	IconDesktop    IconName = "desktop"
	IconExtensions IconName = "extensions"
	IconBackup     IconName = "backup"
	IconSecurity   IconName = "security"
	IconNetwork    IconName = "network"
	IconBluetooth  IconName = "bluetooth"
	IconPrinters   IconName = "printers"
	IconUsers      IconName = "users"
	IconDisks      IconName = "disks"
	IconCompat     IconName = "compat"
	IconAbout      IconName = "about"
	IconSidebar    IconName = "sidebar"
)

// Icons is every icon, in no order.
var Icons = []IconName{IconApps, IconUpdates, IconDesktop, IconExtensions, IconBackup, IconSecurity,
	IconNetwork, IconBluetooth, IconPrinters, IconUsers, IconDisks, IconCompat, IconAbout, IconSidebar}

// pen draws strokes in a 16x16 box scaled to the icon's size.
type pen struct {
	gtx layout.Context
	k   float32
	c   color.NRGBA
	w   float32
}

func (p pen) pt(x, y float32) f32.Point { return f32.Pt(x*p.k, y*p.k) }

// lines strokes the polyline through pts (x, y pairs); closed joins the
// last point to the first.
func (p pen) lines(closed bool, pts ...float32) {
	var pa clip.Path
	pa.Begin(p.gtx.Ops)
	pa.MoveTo(p.pt(pts[0], pts[1]))
	for i := 2; i+1 < len(pts); i += 2 {
		pa.LineTo(p.pt(pts[i], pts[i+1]))
	}
	if closed {
		pa.Close()
	}
	paint.FillShape(p.gtx.Ops, p.c, clip.Stroke{Path: pa.End(), Width: p.w * p.k}.Op())
}

// rect strokes the rectangle from (x0, y0) to (x1, y1) with corner radius r.
func (p pen) rect(x0, y0, x1, y1, r float32) {
	ri := image.Rect(int(x0*p.k+0.5), int(y0*p.k+0.5), int(x1*p.k+0.5), int(y1*p.k+0.5))
	// StrokeRRect draws inside its rectangle; grow it by half the pen.
	h := int(p.w * p.k / 2)
	StrokeRRect(p.gtx, ri.Inset(-h), int(r*p.k+0.5)+h, p.w*p.k, p.c)
}

// arc strokes the arc of the circle at (cx, cy), radius r, from angle a0
// through sweep (radians, clockwise on screen).
func (p pen) arc(cx, cy, r, a0, sweep float32) {
	var pa clip.Path
	pa.Begin(p.gtx.Ops)
	c := p.pt(cx, cy)
	pa.MoveTo(p.pt(cx+r*float32(math.Cos(float64(a0))), cy+r*float32(math.Sin(float64(a0)))))
	pa.ArcTo(c, c, sweep)
	paint.FillShape(p.gtx.Ops, p.c, clip.Stroke{Path: pa.End(), Width: p.w * p.k}.Op())
}

// dot fills a round dot at (x, y).
func (p pen) dot(x, y, r float32) {
	c := p.pt(x, y)
	rr := r * p.k
	b := image.Rect(int(c.X-rr+0.5), int(c.Y-rr+0.5), int(c.X+rr+0.5), int(c.Y+rr+0.5))
	FillRRect(p.gtx, b, b.Dx()/2, p.c)
}

// Icon draws icon name in a square of size dp, in colour c.
func Icon(gtx layout.Context, name IconName, size float32, c color.NRGBA) layout.Dimensions {
	px := Fdp(gtx, size)
	p := pen{gtx: gtx, k: px / 16, c: c, w: 1.4}
	const pi = math.Pi
	switch name {
	case IconApps:
		p.rect(2, 2, 7, 7, 1.5)
		p.rect(9, 2, 14, 7, 1.5)
		p.rect(2, 9, 7, 14, 1.5)
		p.rect(9, 9, 14, 14, 1.5)
	case IconUpdates:
		p.arc(8, 8, 5.5, -pi/2+0.5, 2*pi-1.0)
		p.lines(false, 10.2, 1.2, 10.9, 3.5, 8.6, 4.4)
	case IconDesktop:
		p.rect(1.5, 2.5, 14.5, 11, 1.5)
		p.lines(false, 5.5, 14, 10.5, 14)
		p.lines(false, 8, 11, 8, 14)
	case IconExtensions:
		p.rect(2, 2, 14, 14, 2)
		p.lines(false, 8, 5, 8, 11)
		p.lines(false, 5, 8, 11, 8)
	case IconBackup:
		p.rect(1.5, 2, 14.5, 5.5, 1)
		p.lines(false, 2.5, 5.5, 2.5, 14, 13.5, 14, 13.5, 5.5)
		p.lines(false, 6, 8.5, 10, 8.5)
	case IconSecurity:
		p.lines(true, 8, 1.5, 13.5, 3.5, 13.5, 7.5, 12.5, 10.5, 8, 14.5, 3.5, 10.5, 2.5, 7.5, 2.5, 3.5)
	case IconNetwork:
		p.arc(8, 13, 11, -pi/2-0.75, 1.5)
		p.arc(8, 13, 7, -pi/2-0.75, 1.5)
		p.arc(8, 13, 3, -pi/2-0.75, 1.5)
		p.dot(8, 13, 1.1)
	case IconBluetooth:
		p.lines(false, 4, 5, 11.5, 11, 8, 14.5, 8, 1.5, 11.5, 5, 4, 11)
	case IconPrinters:
		p.lines(false, 4.5, 6, 4.5, 1.5, 11.5, 1.5, 11.5, 6)
		p.rect(1.5, 6, 14.5, 11.5, 1.5)
		p.rect(4.5, 9.5, 11.5, 14.5, 0.5)
	case IconUsers:
		p.arc(8, 5, 3, 0, 2*pi)
		p.arc(8, 15.5, 6, -pi+0.25, pi-0.5)
	case IconDisks:
		p.rect(1.5, 3.5, 14.5, 12.5, 2)
		p.lines(false, 1.5, 9, 14.5, 9)
		p.dot(11.5, 10.8, 0.9)
	case IconCompat:
		p.rect(1.5, 1.5, 10, 10, 1.5)
		p.lines(false, 6, 12, 6, 14.5, 14.5, 14.5, 14.5, 6, 12, 6)
	case IconAbout:
		p.arc(8, 8, 6.5, 0, 2*pi)
		p.lines(false, 8, 7, 8, 11.5)
		p.dot(8, 4.8, 0.9)
	case IconSidebar:
		p.rect(1.5, 2.5, 14.5, 13.5, 2)
		p.lines(false, 6, 2.5, 6, 13.5)
	}
	s := int(px + 0.5)
	return layout.Dimensions{Size: image.Pt(s, s)}
}
