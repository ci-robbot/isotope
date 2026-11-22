package guikit

import (
	"image"

	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/widget"
)

// NavItem is one row of a navigation sidebar.
type NavItem struct {
	Name string
	// Later marks a page that is listed but not there yet: drawn dim.
	Later bool
}

// Nav is a sidebar for an app whose pages are not a flow (a settings app):
// every row can be clicked, none is ticked, the current one is lit. Keep
// one Nav for the life of the app; it owns the rows' clickables.
type Nav struct {
	Title   string // beside the mark at the top
	Note    string // at the bottom, small: the version
	Items   []NavItem
	Current int

	rows []widget.Clickable
}

// Clicked handles the rows' clicks (and Enter or Space on a focused row)
// and returns the index of the row clicked last, or -1.
func (n *Nav) Clicked(gtx layout.Context) int {
	n.grow()
	hit := -1
	for i := range n.rows {
		if n.rows[i].Clicked(gtx) {
			hit = i
		}
	}
	return hit
}

// Row is the clickable of row i, to give it the keyboard focus.
func (n *Nav) Row(i int) *widget.Clickable {
	n.grow()
	return &n.rows[i]
}

func (n *Nav) grow() {
	if len(n.rows) < len(n.Items) {
		n.rows = append(n.rows, make([]widget.Clickable, len(n.Items)-len(n.rows))...)
	}
}

// NavSidebar is Sidebar for a Nav: the same 236 dp column, mark, title and
// note, with clickable rows in place of the steps.
func (th *Theme) NavSidebar(gtx layout.Context, n *Nav) layout.Dimensions {
	n.grow()
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
						return th.Label(gtx, StBrand, ColText, n.Title)
					}),
				)
			}),
			layout.Rigid(layout.Spacer{Height: 28}.Layout),
			layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
				gtx.Constraints.Min = image.Point{}
				var kids []layout.FlexChild
				for i := range n.Items {
					if i > 0 {
						kids = append(kids, layout.Rigid(layout.Spacer{Height: 2}.Layout))
					}
					kids = append(kids, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						return th.navRow(gtx, n, i)
					}))
				}
				layout.Flex{Axis: layout.Vertical}.Layout(gtx, kids...)
				return layout.Dimensions{Size: gtx.Constraints.Max}
			}),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return th.Label(gtx, StTiny, ColDim, n.Note)
			}),
		)
	})
}

func (th *Theme) navRow(gtx layout.Context, n *Nav, i int) layout.Dimensions {
	it, c := n.Items[i], &n.rows[i]
	cur := i == n.Current
	gtx.Constraints.Min.X = gtx.Constraints.Max.X
	return c.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		pointer.CursorPointer.Add(gtx.Ops)
		st, col := StBody, ColText2
		switch {
		case cur:
			st, col = StBody600, ColText
		case it.Later:
			col = ColDim
		}
		m := op.Record(gtx.Ops)
		dims := layout.Inset{Top: 9, Bottom: 9, Left: 12, Right: 10}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			gtx.Constraints.Min.X = gtx.Constraints.Max.X
			return th.Label(gtx, st, col, it.Name)
		})
		call := m.Stop()
		r := image.Rectangle{Max: dims.Size}
		switch {
		case cur:
			FillRRect(gtx, r, Dp(gtx, 8), Over(ColSidebar, ColAccent, 0.12))
			bar := Dp(gtx, 3)
			FillRRect(gtx, image.Rect(0, Dp(gtx, 8), bar, dims.Size.Y-Dp(gtx, 8)), bar/2, ColAccent)
		case c.Hovered():
			FillRRect(gtx, r, Dp(gtx, 8), Over(ColSidebar, ColText, 0.05))
		}
		if gtx.Focused(c) {
			StrokeRRect(gtx, r, Dp(gtx, 8), Fdp(gtx, 2), FocusRingStrong)
		}
		call.Add(gtx.Ops)
		return dims
	})
}
