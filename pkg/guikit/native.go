package guikit

import (
	"image"
	"image/color"
	"path/filepath"
	"strings"

	"gioui.org/io/pointer"
	"gioui.org/io/system"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/widget"
)

// The parts of a settings window that is an ordinary window of its
// desktop (Isotope System): a header bar where the app draws its own
// (COSMIC), and a navigation list. They draw from the theme's palette and
// paint no background of their own outside their area.

// headerKey is COSMIC's header bar density (libcosmic's Density: Compact,
// Standard or Spacious).
const headerKey = "com.system76.CosmicTk/v1/header_size"

// HeaderHeight is the height in dp of COSMIC's header bars for the user's
// density: 36 Compact, 40 Standard (the default), 48 Spacious. The user's
// key (XDG_CONFIG_HOME, or home/.config) wins over /usr/share/cosmic.
func HeaderHeight(getenv func(string) string, home string, read func(string) ([]byte, error)) float32 {
	cfg := getenv("XDG_CONFIG_HOME")
	if cfg == "" {
		cfg = filepath.Join(home, ".config")
	}
	for _, p := range []string{filepath.Join(cfg, "cosmic", headerKey), filepath.Join("/usr/share/cosmic", headerKey)} {
		b, err := read(p)
		if err != nil {
			continue
		}
		switch strings.TrimSpace(string(b)) {
		case "Compact":
			return 36
		case "Standard":
			return TitleBarHeight
		case "Spacious":
			return 48
		}
	}
	return TitleBarHeight
}

// HeaderBar is the bar at the top of a window whose compositor asks the app
// to draw its own (COSMIC): the window buttons on the side the desktop
// uses, a button that shows the navigation list on a narrow window, and the
// title in the middle. Dragging it moves the window.
type HeaderBar struct {
	Title string
	// Left puts the window buttons at the left (ControlsLeft).
	Left bool
	// Maximized is the window's state, from the app's ConfigEvent.
	Maximized bool
	// ShowNavButton shows the navigation button, beside the window
	// buttons.
	ShowNavButton bool
	// CloseOnly draws the close button alone (a small question window).
	CloseOnly bool
	// Height in dp; zero is TitleBarHeight.
	Height float32

	deco widget.Decorations
	nav  widget.Clickable
}

// Update returns the window actions asked for and whether the navigation
// button was clicked.
func (h *HeaderBar) Update(gtx layout.Context) (system.Action, bool) {
	h.deco.Maximized = h.Maximized
	return h.deco.Update(gtx), h.nav.Clicked(gtx)
}

// NavButton is the navigation button's clickable, to give it the focus.
func (h *HeaderBar) NavButton() *widget.Clickable { return &h.nav }

func (h *HeaderBar) height() float32 {
	if h.Height <= 0 {
		return TitleBarHeight
	}
	return h.Height
}

// Layout draws the bar over the width of gtx.
func (h *HeaderBar) Layout(gtx layout.Context, th *Theme) layout.Dimensions {
	p := th.Colors()
	size := image.Pt(gtx.Constraints.Max.X, Dp(gtx, h.height()))
	gtx.Constraints = layout.Exact(size)
	FillRect(gtx, image.Rectangle{Max: size}, p.Sidebar)
	h.deco.LayoutMove(gtx, func(gtx layout.Context) layout.Dimensions {
		return layout.Center.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			gtx.Constraints.Min = image.Point{}
			gtx.Constraints.Max.X = max(0, size.X-Dp(gtx, 2*130))
			return th.LabelMax(gtx, StBody600, p.Text, 1, h.Title)
		})
	})
	acts := ControlOrder(h.Left)
	if h.CloseOnly {
		acts = []system.Action{system.ActionClose}
	}
	var kids []layout.FlexChild
	for i, a := range acts {
		if i > 0 {
			kids = append(kids, layout.Rigid(layout.Spacer{Width: 8}.Layout))
		}
		kids = append(kids, layout.Rigid(func(gtx layout.Context) layout.Dimensions { return h.button(gtx, p, a) }))
	}
	if h.ShowNavButton {
		nav := layout.Rigid(func(gtx layout.Context) layout.Dimensions { return h.navButton(gtx, th) })
		gap := layout.Rigid(layout.Spacer{Width: 14}.Layout)
		if h.Left {
			kids = append(kids, gap, nav)
		} else {
			kids = append([]layout.FlexChild{nav, gap}, kids...)
		}
	}
	layout.Inset{Left: 14, Right: 14}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		sp := layout.SpaceStart
		if h.Left {
			sp = layout.SpaceEnd
		}
		return layout.Flex{Alignment: layout.Middle, Spacing: sp}.Layout(gtx, kids...)
	})
	return layout.Dimensions{Size: size}
}

// button is one round window button, its glyph shown under the pointer, as
// Isotope's COSMIC header bars draw them.
func (h *HeaderBar) button(gtx layout.Context, p *Palette, a system.Action) layout.Dimensions {
	clk := h.deco.Clickable(a)
	d := Dp(gtx, 16)
	r := image.Rectangle{Max: image.Pt(d, d)}
	return clk.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		fill, fg := Over(p.Sidebar, p.Text, 0.14), p.Text
		if clk.Hovered() || gtx.Focused(clk) {
			fill = Over(p.Sidebar, p.Text, 0.26)
			if a == system.ActionClose {
				fill, fg = p.Destructive, p.OnDestructive
			}
		}
		FillRRect(gtx, r, d/2, fill)
		if clk.Hovered() || gtx.Focused(clk) {
			glyphIn(gtx, a, h.Maximized, r, fg)
		}
		pointer.CursorPointer.Add(gtx.Ops)
		return layout.Dimensions{Size: r.Size()}
	})
}

// navButton shows or hides the navigation list.
func (h *HeaderBar) navButton(gtx layout.Context, th *Theme) layout.Dimensions {
	p := th.Colors()
	d := Dp(gtx, 28)
	return h.nav.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		r := image.Rectangle{Max: image.Pt(d, d)}
		if h.nav.Hovered() {
			FillRRect(gtx, r, Dp(gtx, p.RadiusS), p.Hover)
		}
		if gtx.Focused(&h.nav) {
			StrokeRRect(gtx, r, Dp(gtx, p.RadiusS), Fdp(gtx, 2), p.Focus)
		}
		o := op.Offset(image.Pt((d-Dp(gtx, 16))/2, (d-Dp(gtx, 16))/2)).Push(gtx.Ops)
		Icon(gtx, IconSidebar, 16, p.Text)
		o.Pop()
		pointer.CursorPointer.Add(gtx.Ops)
		return layout.Dimensions{Size: r.Size()}
	})
}

// glyphIn draws the symbol of action a inside r in colour c.
func glyphIn(gtx layout.Context, a system.Action, maximized bool, r image.Rectangle, c color.NRGBA) {
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

// NavEntry is one row of a NavList.
type NavEntry struct {
	Name string
	Icon IconName
}

// NavList is the navigation list of a settings window: an icon and a name
// per page, the current page highlighted, in the sidebar colour. Keep one
// for the life of the window; it owns the rows' clickables.
type NavList struct {
	Items   []NavEntry
	Current int

	rows []widget.Clickable
	list widget.List
}

// NavWidth is the navigation list's width in dp.
const NavWidth = 240

// Clicked handles the rows' clicks (and Enter or Space on a focused row)
// and returns the index of the row clicked last, or -1.
func (n *NavList) Clicked(gtx layout.Context) int {
	n.grow()
	hit := -1
	for i := range n.rows {
		if n.rows[i].Clicked(gtx) {
			hit = i
		}
	}
	return hit
}

// Row is the clickable of row i.
func (n *NavList) Row(i int) *widget.Clickable {
	n.grow()
	return &n.rows[i]
}

func (n *NavList) grow() {
	if len(n.rows) < len(n.Items) {
		n.rows = append(n.rows, make([]widget.Clickable, len(n.Items)-len(n.rows))...)
	}
}

// NavList draws n as a column NavWidth wide and as tall as gtx allows; the
// rows scroll when they do not fit.
func (th *Theme) NavList(gtx layout.Context, n *NavList) layout.Dimensions {
	n.grow()
	p := th.Colors()
	size := image.Pt(Dp(gtx, NavWidth), gtx.Constraints.Max.Y)
	FillRect(gtx, image.Rectangle{Max: size}, p.Sidebar)
	gtx.Constraints = layout.Exact(size)
	n.list.Axis = layout.Vertical
	layout.Inset{Top: 8, Bottom: 8, Left: 8, Right: 8}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		return n.list.Layout(gtx, len(n.Items), func(gtx layout.Context, i int) layout.Dimensions {
			return layout.Inset{Bottom: 2}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				return th.navEntry(gtx, n, i)
			})
		})
	})
	return layout.Dimensions{Size: size}
}

func (th *Theme) navEntry(gtx layout.Context, n *NavList, i int) layout.Dimensions {
	p := th.Colors()
	it, c := n.Items[i], &n.rows[i]
	cur := i == n.Current
	gtx.Constraints.Min.X = gtx.Constraints.Max.X
	return c.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		pointer.CursorPointer.Add(gtx.Ops)
		st := StBody
		if cur {
			st = StBody600
		}
		m := op.Record(gtx.Ops)
		dims := layout.Inset{Top: 10, Bottom: 10, Left: 12, Right: 12}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			gtx.Constraints.Min.X = gtx.Constraints.Max.X
			return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
				layout.Rigid(func(gtx layout.Context) layout.Dimensions { return Icon(gtx, it.Icon, 16, p.Text) }),
				layout.Rigid(layout.Spacer{Width: 12}.Layout),
				layout.Flexed(1, func(gtx layout.Context) layout.Dimensions { return th.LabelMax(gtx, st, p.Text, 1, it.Name) }),
			)
		})
		call := m.Stop()
		r := image.Rectangle{Max: dims.Size}
		rad := Dp(gtx, p.RadiusS)
		switch {
		case cur:
			FillRRect(gtx, r, rad, p.Selected)
		case c.Hovered():
			FillRRect(gtx, r, rad, Over(p.Sidebar, p.Text, 0.06))
		}
		if gtx.Focused(c) {
			StrokeRRect(gtx, r, rad, Fdp(gtx, 2), p.Focus)
		}
		call.Add(gtx.Ops)
		return dims
	})
}
