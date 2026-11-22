package guikit

import (
	"image"
	"math"
	"time"

	"gioui.org/io/event"
	"gioui.org/io/key"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/unit"
	"gioui.org/widget"
)

// The window is 920x620 logical pixels, centred on the desktop background.
// On an output smaller than 1000x700 it fills the output with no corners or
// shadow.
const (
	WindowWidth, WindowHeight = 920, 620
	floatW, floatH            = 1000, 700
)

// Window is the window of a graphical tool on its desktop: the background
// (a glow at the top, and the window's shadow) and the window, floating and
// rounded when the output is large enough. It keeps the background between
// frames; use one Window for the life of the app.
type Window struct {
	bg desktop
}

// Layout draws the desktop over the whole of gtx, then behind (when not
// nil) on the desktop, with win the window's rectangle, then content in the
// window with exact constraints.
func (wn *Window) Layout(gtx layout.Context, behind func(gtx layout.Context, win image.Rectangle), content layout.Widget) layout.Dimensions {
	size := gtx.Constraints.Max
	floating := size.X >= Dp(gtx, floatW) && size.Y >= Dp(gtx, floatH)
	win := image.Rectangle{Max: size}
	if floating {
		w, h := Dp(gtx, WindowWidth), Dp(gtx, WindowHeight)
		win = image.Rectangle{Min: image.Pt((size.X-w)/2, (size.Y-h)/2)}
		win.Max = win.Min.Add(image.Pt(w, h))
	}
	bg := wn.bg.image(size, win, floating, gtx.Metric.PxPerDp)
	paint.NewImageOp(bg).Add(gtx.Ops)
	paint.PaintOp{}.Add(gtx.Ops)

	if behind != nil {
		behind(gtx, win)
	}

	rad := 0
	if floating {
		rad = Dp(gtx, 16)
	}
	o := op.Offset(win.Min).Push(gtx.Ops)
	wr := image.Rectangle{Max: win.Size()}
	cl := clip.UniformRRect(wr, rad).Push(gtx.Ops)
	FillRect(gtx, wr, ColWindow)
	wgtx := gtx
	wgtx.Constraints = layout.Exact(win.Size())
	content(wgtx)
	cl.Pop()
	if floating {
		StrokeRRect(gtx, wr, rad, Fdp(gtx, 1), Over(ColWindow, ColText, 0.09))
	}
	o.Pop()
	return layout.Dimensions{Size: size}
}

// desktop is the background behind the window: the radial glow of the
// mocks' radial-gradient(ellipse 90% 70% at 50% -15%) and, when the window
// is floating, its shadow (0 40px 120px rgba(0,0,0,.55)). It is drawn on the
// CPU once per size; Gio has no radial gradients or blurs.
type desktop struct {
	size, win image.Rectangle
	shadow    bool
	img       *image.RGBA
}

func (d *desktop) image(size image.Point, win image.Rectangle, shadow bool, pxPerDp float32) *image.RGBA {
	r := image.Rectangle{Max: size}
	if d.img != nil && d.size == r && d.win == win && d.shadow == shadow {
		return d.img
	}
	img := image.NewRGBA(r)
	w, h := float64(size.X), float64(size.Y)
	cx, cy, rx, ry := w*0.5, -0.15*h, 0.9*w, 0.7*h
	sigma := 60 * float64(pxPerDp)
	offY := 40 * float64(pxPerDp)
	erf := func(v float64) float64 { return math.Erf(v / (sigma * math.Sqrt2)) }
	// The shadow is separable: a rectangle blurred by a Gaussian is the
	// product of two 1D blurred steps.
	sx := make([]float64, size.X)
	sy := make([]float64, size.Y)
	if shadow {
		for x := range sx {
			fx := float64(x) + 0.5
			sx[x] = 0.5 * (erf(fx-float64(win.Min.X)) - erf(fx-float64(win.Max.X)))
		}
		for y := range sy {
			fy := float64(y) + 0.5 - offY
			sy[y] = 0.5 * (erf(fy-float64(win.Min.Y)) - erf(fy-float64(win.Max.Y)))
		}
	}
	c1, c2, c3 := ColDesk1, ColDesk2, ColDesk3
	lerp := func(a, b uint8, t float64) float64 { return float64(a) + (float64(b)-float64(a))*t }
	for y := 0; y < size.Y; y++ {
		for x := 0; x < size.X; x++ {
			dx, dy := (float64(x)+0.5-cx)/rx, (float64(y)+0.5-cy)/ry
			t := math.Sqrt(dx*dx + dy*dy)
			var r, g, b float64
			switch {
			case t <= 0.55:
				u := t / 0.55
				r, g, b = lerp(c1.R, c2.R, u), lerp(c1.G, c2.G, u), lerp(c1.B, c2.B, u)
			case t < 1:
				u := (t - 0.55) / 0.45
				r, g, b = lerp(c2.R, c3.R, u), lerp(c2.G, c3.G, u), lerp(c2.B, c3.B, u)
			default:
				r, g, b = float64(c3.R), float64(c3.G), float64(c3.B)
			}
			if shadow {
				a := 0.55 * sx[x] * sy[y]
				r, g, b = r*(1-a), g*(1-a), b*(1-a)
			}
			i := img.PixOffset(x, y)
			img.Pix[i], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3] = uint8(r+0.5), uint8(g+0.5), uint8(b+0.5), 0xff
		}
	}
	d.size, d.win, d.shadow, d.img = r, win, shadow, img
	return img
}

// Steps is what the sidebar shows.
type Steps struct {
	Title   string   // beside the mark at the top: "Install Isotope"
	Names   []string // the steps, in order
	Current int      // the index in Names of the step on screen
	Note    string   // at the bottom, small: the version
}

// Sidebar is the 236 dp column at the left of the window: the mark and the
// title, the steps (done ones ticked, the current one lit) and the note.
func (th *Theme) Sidebar(gtx layout.Context, s Steps) layout.Dimensions {
	w, h := Dp(gtx, 236), gtx.Constraints.Max.Y
	FillRect(gtx, image.Rect(0, 0, w, h), ColSidebar)
	FillRect(gtx, image.Rect(w-Dp(gtx, 1), 0, w, h), Over(ColSidebar, ColText, 0.06))
	gtx.Constraints = layout.Exact(image.Pt(w, h))
	return layout.Inset{Top: 28, Bottom: 28, Left: 22, Right: 22}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
					layout.Rigid(func(gtx layout.Context) layout.Dimensions { return Mark(gtx, 30, false, 1, 0) }),
					layout.Rigid(layout.Spacer{Width: 10}.Layout),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						return th.Label(gtx, StBrand, ColText, s.Title)
					}),
				)
			}),
			layout.Rigid(layout.Spacer{Height: 28}.Layout),
			// The steps take the room between the title and the note.
			layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
				return th.stepList(gtx, s)
			}),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return th.Label(gtx, StTiny, ColDim, s.Note)
			}),
		)
	})
}

// stepList draws the steps from the top of the room it has. When they do
// not fit (many steps, a large scale on a small screen), the rows close up;
// if even that is too tall, the list is cut at the bottom of its room
// rather than drawn over the note.
func (th *Theme) stepList(gtx layout.Context, s Steps) layout.Dimensions {
	room := gtx.Constraints.Max.Y
	gtx.Constraints.Min = image.Point{}
	pad, gap := unit.Dp(9), unit.Dp(4)
	if n := len(s.Names); n > 0 {
		m := op.Record(gtx.Ops)
		row := th.stepRow(gtx, s.Names[0], 0, s.Current, pad).Size.Y
		m.Stop()
		if n*row+(n-1)*gtx.Dp(gap) > room {
			pad, gap = 2, 0
		}
	}
	defer clip.Rect{Max: image.Pt(gtx.Constraints.Max.X, room)}.Push(gtx.Ops).Pop()
	var kids []layout.FlexChild
	for i, name := range s.Names {
		if i > 0 {
			kids = append(kids, layout.Rigid(layout.Spacer{Height: gap}.Layout))
		}
		kids = append(kids, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			gtx.Constraints.Max.Y = 1 << 24
			return th.stepRow(gtx, name, i, s.Current, pad)
		}))
	}
	layout.Flex{Axis: layout.Vertical}.Layout(gtx, kids...)
	return layout.Dimensions{Size: gtx.Constraints.Max}
}

func (th *Theme) stepRow(gtx layout.Context, name string, i, cur int, pad unit.Dp) layout.Dimensions {
	gtx.Constraints.Min.X = gtx.Constraints.Max.X
	st, col := StBody, ColDim
	switch {
	case i < cur:
		col = ColText2
	case i == cur:
		st, col = StBody600, ColText
	}
	m := op.Record(gtx.Ops)
	dims := layout.Inset{Top: pad, Bottom: pad, Left: 10, Right: 10}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				switch {
				case i < cur:
					return CheckIcon(gtx, 10)
				case i == cur:
					s := Dp(gtx, 10)
					r := Dp(gtx, 4)
					FillRRect(gtx, image.Rect(-r, -r, s+r, s+r), s/2+r, Over(Over(ColSidebar, ColAccent, 0.12), ColAccent, 0.2))
					FillRRect(gtx, image.Rect(0, 0, s, s), s/2, ColAccent)
					return layout.Dimensions{Size: image.Pt(s, s)}
				}
				return RingIcon(gtx, 10)
			}),
			layout.Rigid(layout.Spacer{Width: 12}.Layout),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions { return th.Label(gtx, st, col, name) }),
		)
	})
	c := m.Stop()
	if i == cur {
		FillRRect(gtx, image.Rectangle{Max: dims.Size}, Dp(gtx, 8), Over(ColSidebar, ColAccent, 0.12))
	}
	c.Add(gtx.Ops)
	return dims
}

// Frame lays the sidebar at the left of the window and, beside it, body
// above footer. With a nil footer, body has the whole column.
func (th *Theme) Frame(gtx layout.Context, s Steps, body, footer layout.Widget) layout.Dimensions {
	return layout.Flex{}.Layout(gtx,
		layout.Rigid(func(gtx layout.Context) layout.Dimensions { return th.Sidebar(gtx, s) }),
		layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
			gtx.Constraints.Min = gtx.Constraints.Max
			if footer == nil {
				return body(gtx)
			}
			return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
				layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
					gtx.Constraints.Min = gtx.Constraints.Max
					return body(gtx)
				}),
				layout.Rigid(footer),
			)
		}),
	)
}

// Pad is the padding of a page's body in the frame.
func Pad(gtx layout.Context, w layout.Widget) layout.Dimensions {
	return layout.Inset{Top: 36, Bottom: 20, Left: 40, Right: 40}.Layout(gtx, w)
}

// SlideTime is how long a new page takes to slide in.
const SlideTime = 180 * time.Millisecond

// Slide draws w as a page shown at shownAt, at time now: within SlideTime
// it slides in from 24 dp to the right and fades in. A zero shownAt draws w
// in place. While it slides, the app asks for the next frame.
func Slide(gtx layout.Context, shownAt, now time.Time, w layout.Widget) layout.Dimensions {
	if shownAt.IsZero() {
		return w(gtx)
	}
	t := float32(now.Sub(shownAt)) / float32(SlideTime)
	if t >= 1 {
		return w(gtx)
	}
	e := 1 - (1-t)*(1-t)
	defer op.Offset(image.Pt(Dp(gtx, 24*(1-e)), 0)).Push(gtx.Ops).Pop()
	defer paint.PushOpacity(gtx.Ops, e).Pop()
	return w(gtx)
}

// Action is a footer button.
type Action struct {
	Click    *widget.Clickable
	Kind     ButtonKind
	Label    string
	Disabled bool
}

// Footer is the bar under a page: leading at the left, middle (when not nil)
// in the space between, trailing at the right, 10 dp apart. The usual one
// is Go back and Continue at the right.
func (th *Theme) Footer(gtx layout.Context, leading []Action, middle layout.Widget, trailing []Action) layout.Dimensions {
	buttons := func(kids []layout.FlexChild, as []Action) []layout.FlexChild {
		for i, a := range as {
			if i > 0 {
				kids = append(kids, layout.Rigid(layout.Spacer{Width: 10}.Layout))
			}
			kids = append(kids, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return th.Button(gtx, a.Click, a.Kind, a.Label, !a.Disabled)
			}))
		}
		return kids
	}
	m := op.Record(gtx.Ops)
	gtx.Constraints.Min.X = gtx.Constraints.Max.X
	dims := layout.Inset{Top: 18, Bottom: 18, Left: 24, Right: 24}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		gtx.Constraints.Min.X = gtx.Constraints.Max.X
		kids := buttons(nil, leading)
		kids = append(kids, layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
			if middle == nil {
				return layout.Dimensions{Size: gtx.Constraints.Min}
			}
			return middle(gtx)
		}))
		kids = buttons(kids, trailing)
		return layout.Flex{Alignment: layout.Middle}.Layout(gtx, kids...)
	})
	c := m.Stop()
	FillRect(gtx, image.Rectangle{Max: dims.Size}, ColFooter)
	FillRect(gtx, image.Rect(0, 0, dims.Size.X, Dp(gtx, 1)), Over(ColFooter, ColText, 0.06))
	c.Add(gtx.Ops)
	return dims
}

// Scaled is gtx with its dp and sp scaled by scale, the window's zoom.
func Scaled(gtx layout.Context, scale float32) layout.Context {
	if scale != 1 {
		gtx.Metric.PxPerDp *= scale
		gtx.Metric.PxPerSp *= scale
	}
	return gtx
}

// WindowKeys handles the window-wide keys: Esc calls back, Ctrl+Plus and
// Ctrl+Minus change scale between 1 and 2 in quarters, Ctrl+0 resets it.
// The app registers its tag for these keys with event.Op beforehand.
func WindowKeys(gtx layout.Context, scale *float32, back func()) {
	for {
		e, ok := gtx.Event(
			key.Filter{Name: key.NameEscape},
			key.Filter{Name: "+", Required: key.ModShortcut, Optional: key.ModShift},
			key.Filter{Name: "=", Required: key.ModShortcut},
			key.Filter{Name: "-", Required: key.ModShortcut},
			key.Filter{Name: "0", Required: key.ModShortcut},
		)
		if !ok {
			return
		}
		k, ok := e.(key.Event)
		if !ok || k.State != key.Press {
			continue
		}
		switch k.Name {
		case key.NameEscape:
			if back != nil {
				back()
			}
		case "+", "=":
			*scale = min(2, *scale+0.25)
		case "-":
			*scale = max(1, *scale-0.25)
		case "0":
			*scale = 1
		}
	}
}

// Confirm is a question over the window: the window dimmed, and in the
// middle a card with title, the paragraph body and the buttons at its
// bottom right. It draws over all of gtx and takes the pointer, so the page
// under it cannot be clicked; the app gives the keyboard focus to one of
// the buttons.
func (th *Theme) Confirm(gtx layout.Context, title, body string, actions []Action) layout.Dimensions {
	return th.ConfirmForm(gtx, title, body, nil, actions)
}

// ConfirmForm is Confirm with a form (fields, a list) between the body and
// the buttons. The box is wider when there is a form.
func (th *Theme) ConfirmForm(gtx layout.Context, title, body string, form layout.Widget, actions []Action) layout.Dimensions {
	size := gtx.Constraints.Max
	r := image.Rectangle{Max: size}
	FillRect(gtx, r, Alpha(ColKeyBox, 0.72))
	area := clip.Rect(r).Push(gtx.Ops)
	event.Op(gtx.Ops, &th.scrim)
	for {
		if _, ok := gtx.Event(pointer.Filter{Target: &th.scrim, Kinds: pointer.Press | pointer.Release | pointer.Scroll, ScrollY: pointer.ScrollRange{Min: -1 << 20, Max: 1 << 20}}); !ok {
			break
		}
	}
	area.Pop()
	gtx.Constraints = layout.Exact(size)
	return layout.Center.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		width := float32(460)
		if form != nil {
			width = 520
		}
		w := min(Dp(gtx, width), size.X-Dp(gtx, 48))
		gtx.Constraints = layout.Constraints{Min: image.Pt(w, 0), Max: image.Pt(w, size.Y)}
		b := Box{Radius: 14, Fill: ColWindow, Border: Over(ColWindow, ColText, 0.12), BorderWidth: 1}
		return b.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			return layout.UniformInset(24).Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				gtx.Constraints.Min.X = gtx.Constraints.Max.X
				kids := []layout.FlexChild{
					layout.Rigid(func(gtx layout.Context) layout.Dimensions { return th.Label(gtx, StTitle, ColText, title) }),
					VGap(10),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions { return th.LabelLines(gtx, StBody, ColText2, 1.5, body) }),
				}
				if form != nil {
					kids = append(kids, VGap(18), layout.Rigid(form))
				}
				kids = append(kids,
					VGap(22),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						gtx.Constraints.Min.X = gtx.Constraints.Max.X
						bs := []layout.FlexChild{layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
							return layout.Dimensions{Size: image.Pt(gtx.Constraints.Min.X, 0)}
						})}
						for i, a := range actions {
							if i > 0 {
								bs = append(bs, layout.Rigid(layout.Spacer{Width: 10}.Layout))
							}
							bs = append(bs, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
								return th.Button(gtx, a.Click, a.Kind, a.Label, !a.Disabled)
							}))
						}
						return layout.Flex{Alignment: layout.Middle}.Layout(gtx, bs...)
					}),
				)
				return layout.Flex{Axis: layout.Vertical}.Layout(gtx, kids...)
			})
		})
	})
}
