package main

import (
	"image"
	"math"

	"gioui.org/f32"
	"gioui.org/layout"
	"gioui.org/op/clip"
	"gioui.org/op/paint"

	"github.com/kairos-community/isotope/pkg/guikit"
)

// The installer's own icons; the shared ones are in guikit.

// keyIcon is the key beside "SSH keys from GitHub".
func keyIcon(gtx layout.Context, size float32) layout.Dimensions {
	px := guikit.Fdp(gtx, size)
	k := px / 16
	var p clip.Path
	p.Begin(gtx.Ops)
	c := f32.Pt(5*k, 6*k)
	p.MoveTo(f32.Pt(8*k, 6*k))
	p.ArcTo(c, c, 2*math.Pi)
	p.MoveTo(f32.Pt(8*k, 6*k))
	p.LineTo(f32.Pt(15*k, 6*k))
	p.MoveTo(f32.Pt(13*k, 6*k))
	p.LineTo(f32.Pt(13*k, 9*k))
	p.MoveTo(f32.Pt(11*k, 6*k))
	p.LineTo(f32.Pt(11*k, 8.5*k))
	paint.FillShape(gtx.Ops, guikit.ColGlow, clip.Stroke{Path: p.End(), Width: 1.2 * k}.Op())
	s := int(px + 0.5)
	return layout.Dimensions{Size: image.Pt(s, s)}
}

// diskIcon is the drive drawn on each disk card; usb draws a stick.
func diskIcon(gtx layout.Context, size float32, selected, usb bool) layout.Dimensions {
	px := guikit.Fdp(gtx, size)
	k := px / 48
	edge, led := guikit.ColRing, guikit.ColRing
	if selected {
		edge, led = guikit.ColGlow, guikit.ColAccent
	}
	rect := func(x, y, w, h float32) image.Rectangle {
		return image.Rect(int(x*k+0.5), int(y*k+0.5), int((x+w)*k+0.5), int((y+h)*k+0.5))
	}
	if usb {
		r := rect(14, 6, 20, 36)
		guikit.FillRRect(gtx, r, int(4*k), guikit.ColField)
		guikit.StrokeRRect(gtx, r, int(4*k), 2*k, edge)
		guikit.FillRRect(gtx, rect(19, 2, 10, 6), int(k), edge)
	} else {
		r := rect(6, 10, 36, 28)
		guikit.FillRRect(gtx, r, int(5*k), guikit.ColField)
		guikit.StrokeRRect(gtx, r, int(5*k), 2*k, edge)
		d := int(5 * k)
		cx, cy := int(34*k), int(31*k)
		guikit.FillRRect(gtx, image.Rect(cx-d/2, cy-d/2, cx+d/2+1, cy+d/2+1), d/2+1, led)
		guikit.FillRect(gtx, rect(12, 17, 18, 2), guikit.ColRing)
	}
	s := int(px + 0.5)
	return layout.Dimensions{Size: image.Pt(s, s)}
}
