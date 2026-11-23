package guikit

import (
	"image"
	"os"
	"path/filepath"
	"strings"

	"gioui.org/f32"
	"gioui.org/io/pointer"
	"gioui.org/io/system"
	"gioui.org/layout"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/widget"
)

// TitleBarHeight is the height of the title bar, in dp.
const TitleBarHeight = 40

// controlsKey is the CosmicTk key Isotope sets (patches/cosmic/libcosmic):
// true puts the window buttons at the start of every COSMIC header bar.
const controlsKey = "com.system76.CosmicTk/v1/window_controls_left"

// ControlsLeft says whether the window buttons go on the left. On COSMIC
// they do, as in every COSMIC header bar, unless the user's CosmicTk key
// (which overrides the system one) says false; elsewhere they go right.
// getenv is os.Getenv; the key is read from XDG_CONFIG_HOME (or
// home/.config) first, then /usr/share/cosmic.
func ControlsLeft(getenv func(string) string, home string, read func(string) ([]byte, error)) bool {
	if !strings.EqualFold(getenv("XDG_CURRENT_DESKTOP"), "COSMIC") {
		return false
	}
	cfg := getenv("XDG_CONFIG_HOME")
	if cfg == "" {
		cfg = filepath.Join(home, ".config")
	}
	for _, p := range []string{
		filepath.Join(cfg, "cosmic", controlsKey),
		filepath.Join("/usr/share/cosmic", controlsKey),
	} {
		if b, err := read(p); err == nil {
			switch strings.TrimSpace(string(b)) {
			case "true":
				return true
			case "false":
				return false
			}
		}
	}
	// The image ships the key; a COSMIC session without it is not ours.
	return false
}

// ControlsLeftHere is ControlsLeft for this process.
func ControlsLeftHere() bool {
	home, _ := os.UserHomeDir()
	return ControlsLeft(os.Getenv, home, os.ReadFile)
}

// ControlOrder is the window buttons in the order they are drawn from the
// left: close, minimize, maximize at the left (COSMIC's order with
// window_controls_left), minimize, maximize, close at the right.
func ControlOrder(left bool) []system.Action {
	if left {
		return []system.Action{system.ActionClose, system.ActionMinimize, system.ActionMaximize}
	}
	return []system.Action{system.ActionMinimize, system.ActionMaximize, system.ActionClose}
}

// TitleBar is a title bar the app draws when the compositor draws none.
type TitleBar struct {
	Title string
	// Left puts the buttons at the left.
	Left bool
	// Maximized is the window's state, from the app's ConfigEvent.
	Maximized bool
	deco      widget.Decorations
}

// Update returns the actions the user asked for (a button clicked).
func (t *TitleBar) Update(gtx layout.Context) system.Action {
	t.deco.Maximized = t.Maximized
	return t.deco.Update(gtx)
}

// Layout draws the bar over the width of gtx; dragging it moves the window.
func (t *TitleBar) Layout(gtx layout.Context, th *Theme) layout.Dimensions {
	size := image.Pt(gtx.Constraints.Max.X, Dp(gtx, TitleBarHeight))
	gtx.Constraints = layout.Exact(size)
	FillRect(gtx, image.Rectangle{Max: size}, ColSidebar)
	FillRect(gtx, image.Rect(0, size.Y-Dp(gtx, 1), size.X, size.Y), Over(ColSidebar, ColText, 0.06))
	t.deco.LayoutMove(gtx, func(gtx layout.Context) layout.Dimensions {
		// The title is centred in the whole bar, not in what the buttons
		// leave.
		return layout.Center.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			gtx.Constraints.Min = image.Point{}
			return th.Label(gtx, StSmall, ColText2, t.Title)
		})
	})
	var kids []layout.FlexChild
	for i, a := range ControlOrder(t.Left) {
		if i > 0 {
			kids = append(kids, layout.Rigid(layout.Spacer{Width: 8}.Layout))
		}
		kids = append(kids, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return t.button(gtx, a)
		}))
	}
	layout.Inset{Left: 14, Right: 14}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		sp := layout.SpaceStart
		if t.Left {
			sp = layout.SpaceEnd
		}
		return layout.Flex{Alignment: layout.Middle, Spacing: sp}.Layout(gtx, kids...)
	})
	return layout.Dimensions{Size: size}
}

// button is one round window button: neutral and outlined, its glyph shown
// under the pointer (as libcosmic's patched header bar does).
func (t *TitleBar) button(gtx layout.Context, a system.Action) layout.Dimensions {
	clk := t.deco.Clickable(a)
	d := Dp(gtx, 16)
	r := image.Rectangle{Max: image.Pt(d, d)}
	return clk.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		fill := Over(ColSidebar, ColText, 0.12)
		if clk.Hovered() {
			fill = Over(ColSidebar, ColText, 0.24)
			if a == system.ActionClose {
				fill = ColError
			}
		}
		FillRRect(gtx, r, d/2, fill)
		StrokeRRect(gtx, r, d/2, Fdp(gtx, 1), Over(ColSidebar, ColText, 0.14))
		if clk.Hovered() {
			glyph(gtx, a, t.Maximized, r)
		}
		pointer.CursorPointer.Add(gtx.Ops)
		return layout.Dimensions{Size: r.Size()}
	})
}

// glyph draws the symbol of action a inside r: a cross, a bar, a square.
func glyph(gtx layout.Context, a system.Action, maximized bool, r image.Rectangle) {
	c := ColOnAccent
	cx, cy := float32(r.Dx())/2, float32(r.Dy())/2
	u := float32(r.Dx()) / 8
	var p clip.Path
	p.Begin(gtx.Ops)
	switch a {
	case system.ActionClose:
		p.MoveTo(f(cx-u, cy-u))
		p.LineTo(f(cx+u, cy+u))
		p.MoveTo(f(cx+u, cy-u))
		p.LineTo(f(cx-u, cy+u))
	case system.ActionMinimize:
		p.MoveTo(f(cx-1.5*u, cy))
		p.LineTo(f(cx+1.5*u, cy))
	default:
		if maximized {
			u *= 0.8
		}
		p.MoveTo(f(cx-1.5*u, cy-1.5*u))
		p.LineTo(f(cx+1.5*u, cy-1.5*u))
		p.LineTo(f(cx+1.5*u, cy+1.5*u))
		p.LineTo(f(cx-1.5*u, cy+1.5*u))
		p.Close()
	}
	paint.FillShape(gtx.Ops, c, clip.Stroke{Path: p.End(), Width: Fdp(gtx, 1.4)}.Op())
}

func f(x, y float32) f32.Point { return f32.Pt(x, y) }
