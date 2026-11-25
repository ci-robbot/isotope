package guikit

import (
	"image"

	"gioui.org/io/event"
	"gioui.org/io/key"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/unit"
	"gioui.org/widget"
)

// Settings rows, as COSMIC Settings draws them: a section's rows in one
// rounded group split by thin lines, each row a title and a line under it
// at the left and its controls at the right.

// Group lays rows in one rounded container of the palette's Group colour,
// a divider between two rows. Each row gets 16 dp at the sides and 12 dp
// above and below.
func (th *Theme) Group(gtx layout.Context, rows ...layout.Widget) layout.Dimensions {
	p := th.Colors()
	gtx.Constraints.Min.X = gtx.Constraints.Max.X
	b := Box{Radius: p.RadiusS, Fill: p.Group, Border: p.GroupBorder, BorderWidth: 1}
	return b.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		gtx.Constraints.Min.X = gtx.Constraints.Max.X
		var kids []layout.FlexChild
		for i, r := range rows {
			if i > 0 {
				kids = append(kids, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					w, side := gtx.Constraints.Max.X, Dp(gtx, 16)
					h := max(1, Dp(gtx, 1))
					FillRect(gtx, image.Rect(side, 0, w-side, h), p.Divider)
					return layout.Dimensions{Size: image.Pt(w, h)}
				}))
			}
			kids = append(kids, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				gtx.Constraints.Min.X = gtx.Constraints.Max.X
				return layout.Inset{Top: 12, Bottom: 12, Left: 16, Right: 16}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
					gtx.Constraints.Min.X = gtx.Constraints.Max.X
					gtx.Constraints.Min.Y = Dp(gtx, 34)
					// W keeps the minimum height: the row is centred in
					// its 34 dp only when it does not take them.
					return layout.W.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
						gtx.Constraints.Min.Y = 0
						return r(gtx)
					})
				})
			}))
		}
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx, kids...)
	})
}

// RowActions is how many actions a row shows as buttons; the others go in
// its "⋯" menu. A row narrower than NarrowRow shows one.
const (
	RowActions = 2
	NarrowRow  = 480
)

// Overflow is the state of a row's "⋯" menu. Keep one per row, for as
// long as the row is shown.
type Overflow struct {
	btn   widget.Clickable
	open  bool
	items []widget.Clickable
}

// Open says whether the menu is showing.
func (o *Overflow) Open() bool { return o.open }

// Close hides the menu.
func (o *Overflow) Close() { o.open = false }

// SplitActions is the actions a row of width w (in dp) shows as buttons,
// and the ones it puts in its menu. A destructive action (ButtonDanger)
// goes in the menu when the row has other actions, last; alone it is a
// plain button, the confirmation that follows it is the red one.
func SplitActions(as []Action, w float32) (shown, more []Action) {
	n := RowActions
	if w < NarrowRow {
		n = 1
	}
	if len(as) == 1 {
		a := as[0]
		if a.Kind == ButtonDanger {
			a.Kind = ButtonSecondary
		}
		return []Action{a}, nil
	}
	var plain, danger []Action
	for _, a := range as {
		if a.Kind == ButtonDanger {
			danger = append(danger, a)
		} else {
			plain = append(plain, a)
		}
	}
	if len(danger) == 0 && len(plain) <= n {
		return plain, nil
	}
	k := min(n, len(plain))
	more = append(append(more, plain[k:]...), danger...)
	return plain[:k], more
}

// ActionRow is a settings row: title over sub at the left; at the right
// extra (a badge or a status word, may be nil), then the actions as
// buttons, the ones past RowActions in a "⋯" menu kept in o (o may be nil
// when the row never has more than RowActions actions).
func (th *Theme) ActionRow(gtx layout.Context, title, sub string, extra layout.Widget, as []Action, o *Overflow) layout.Dimensions {
	p := th.Colors()
	shown, more := SplitActions(as, float32(gtx.Constraints.Max.X)/gtx.Metric.PxPerDp)
	if o == nil {
		shown, more = as, nil
	}
	gtx.Constraints.Min.X = gtx.Constraints.Max.X
	// The row is centred in its line by Group; its parts are centred in
	// the row, so none takes the line's height as its own.
	gtx.Constraints.Min.Y = 0
	var kids []layout.FlexChild
	kids = append(kids, layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return th.LabelMax(gtx, StBody, p.Text, 2, title)
			}),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				if sub == "" {
					return layout.Dimensions{}
				}
				return layout.Inset{Top: 2}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
					return th.LabelMax(gtx, StSmall, p.Dim, 3, sub)
				})
			}),
		)
	}))
	if extra != nil {
		kids = append(kids, layout.Rigid(layout.Spacer{Width: 12}.Layout), layout.Rigid(extra))
	}
	for _, a := range shown {
		kids = append(kids, layout.Rigid(layout.Spacer{Width: 8}.Layout), layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return th.RowButton(gtx, a.Click, a.Kind, a.Label, !a.Disabled)
		}))
	}
	if len(more) > 0 {
		kids = append(kids, layout.Rigid(layout.Spacer{Width: 8}.Layout), layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return th.overflow(gtx, o, more)
		}))
	} else if o != nil {
		o.open = false
	}
	return layout.Flex{Alignment: layout.Middle}.Layout(gtx, kids...)
}

// overflow draws the "⋯" button and, while it is open, its menu under
// it, over the rest of the window.
func (th *Theme) overflow(gtx layout.Context, o *Overflow, more []Action) layout.Dimensions {
	p := th.Colors()
	if len(o.items) < len(more) {
		o.items = make([]widget.Clickable, len(more))
	}
	// A press outside the menu closes it.
	for {
		ev, ok := gtx.Event(pointer.Filter{Target: o, Kinds: pointer.Press})
		if !ok {
			break
		}
		if e, ok := ev.(pointer.Event); ok && e.Kind == pointer.Press {
			o.open = false
		}
	}
	for i, a := range more {
		if o.items[i].Clicked(gtx) && !a.Disabled {
			// From the keyboard, the focus goes back to the "⋯" button.
			if gtx.Focused(&o.items[i]) {
				gtx.Execute(key.FocusCmd{Tag: &o.btn})
			}
			o.open = false
			a.Click.Click()
			gtx.Execute(op.InvalidateCmd{})
		}
	}
	if o.open {
		o.menuKeys(gtx, more)
	}
	if o.btn.Clicked(gtx) {
		o.open = !o.open
		// Opened from the keyboard (a mouse click does not focus the
		// button): the focus moves to the first item.
		if o.open && gtx.Focused(&o.btn) {
			o.focusNext(gtx, more, -1, 1)
		}
	}
	sz := Dp(gtx, 34)
	dims := o.btn.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		pointer.CursorPointer.Add(gtx.Ops)
		b := Box{Radius: p.RadiusS, Fill: p.Button, Border: p.ButtonBorder, BorderWidth: 1}
		if o.btn.Hovered() || o.open {
			b.Fill = On(p.Group, p.Hover)
		}
		if gtx.Focused(&o.btn) {
			b.Ring, b.RingWidth = p.Focus, 3
		}
		b.Paint(gtx, image.Pt(sz, sz))
		d := max(3, Dp(gtx, 3.5))
		gap := Dp(gtx, 5)
		cy := sz / 2
		for i := -1; i <= 1; i++ {
			cx := sz/2 + i*gap
			r := image.Rect(cx-d/2, cy-d/2, cx-d/2+d, cy-d/2+d)
			FillRRect(gtx, r, d/2, p.OnButton)
		}
		return layout.Dimensions{Size: image.Pt(sz, sz)}
	})
	if !o.open {
		return dims
	}
	// The menu, deferred so it draws over the rows below.
	m := op.Record(gtx.Ops)
	// The catcher: everything outside the menu.
	big := clip.Rect{Min: image.Pt(-1<<15, -1<<15), Max: image.Pt(1<<15, 1<<15)}.Push(gtx.Ops)
	event.Op(gtx.Ops, o)
	big.Pop()
	mg := gtx
	mg.Constraints = layout.Constraints{Min: image.Pt(Dp(gtx, 180), 0), Max: image.Pt(Dp(gtx, 320), Dp(gtx, 600))}
	rec := op.Record(gtx.Ops)
	md := th.menu(mg, o, more)
	call := rec.Stop()
	// Right-aligned under the button.
	off := op.Offset(image.Pt(dims.Size.X-md.Size.X, dims.Size.Y+Dp(gtx, 4))).Push(gtx.Ops)
	Box{Radius: p.RadiusS + 2, Ring: p.Shade, RingWidth: 6}.Paint(gtx, md.Size)
	Box{Radius: p.RadiusS, Fill: p.Group, Border: p.GroupBorder, BorderWidth: 1}.Paint(gtx, md.Size)
	call.Add(gtx.Ops)
	off.Pop()
	op.Defer(gtx.Ops, m.Stop())
	return dims
}

// menuKeys moves the focus in an open menu: Down and Tab to the next
// enabled item, Up and Shift+Tab to the one before, Home and End to the
// first and last, both ends wrapping; Escape closes the menu and gives the
// focus back to the "⋯" button. Down on the button enters the menu.
func (o *Overflow) menuKeys(gtx layout.Context, more []Action) {
	for {
		ev, ok := gtx.Event(
			key.Filter{Focus: &o.btn, Name: key.NameEscape},
			key.Filter{Focus: &o.btn, Name: key.NameDownArrow},
		)
		if !ok {
			break
		}
		if e, ok := ev.(key.Event); ok && e.State == key.Press {
			if e.Name == key.NameEscape {
				o.open = false
			} else {
				o.focusNext(gtx, more, -1, 1)
			}
		}
	}
	for i := range more {
		t := &o.items[i]
		for {
			ev, ok := gtx.Event(
				key.Filter{Focus: t, Name: key.NameEscape},
				key.Filter{Focus: t, Name: key.NameDownArrow},
				key.Filter{Focus: t, Name: key.NameUpArrow},
				key.Filter{Focus: t, Name: key.NameHome},
				key.Filter{Focus: t, Name: key.NameEnd},
				key.Filter{Focus: t, Name: key.NameTab, Optional: key.ModShift},
			)
			if !ok {
				break
			}
			e, ok := ev.(key.Event)
			if !ok || e.State != key.Press {
				continue
			}
			switch {
			case e.Name == key.NameEscape:
				o.open = false
				gtx.Execute(key.FocusCmd{Tag: &o.btn})
			case e.Name == key.NameDownArrow, e.Name == key.NameTab && !e.Modifiers.Contain(key.ModShift):
				o.focusNext(gtx, more, i, 1)
			case e.Name == key.NameUpArrow, e.Name == key.NameTab:
				o.focusNext(gtx, more, i, -1)
			case e.Name == key.NameHome:
				o.focusNext(gtx, more, -1, 1)
			case e.Name == key.NameEnd:
				o.focusNext(gtx, more, len(more), -1)
			}
		}
	}
}

// focusNext focuses the first enabled item after from in the direction dir
// (1 or -1), wrapping round; it does nothing when every item is disabled.
func (o *Overflow) focusNext(gtx layout.Context, more []Action, from, dir int) {
	n := len(more)
	for step := 1; step <= n; step++ {
		i := ((from+dir*step)%n + n) % n
		if !more[i].Disabled {
			gtx.Execute(key.FocusCmd{Tag: &o.items[i]})
			gtx.Execute(op.InvalidateCmd{})
			return
		}
	}
}

// menu lays the menu's items, one 36 dp line each.
func (th *Theme) menu(gtx layout.Context, o *Overflow, more []Action) layout.Dimensions {
	p := th.Colors()
	var kids []layout.FlexChild
	for i, a := range more {
		c := &o.items[i]
		kids = append(kids, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			gtx.Constraints.Min.X = gtx.Constraints.Max.X
			fg := p.Text
			switch {
			case a.Disabled:
				fg = p.Dim
				gtx = gtx.Disabled()
			case a.Kind == ButtonDanger:
				fg = p.ErrorText
			}
			return c.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				if !a.Disabled {
					pointer.CursorPointer.Add(gtx.Ops)
				}
				m := op.Record(gtx.Ops)
				dims := layout.Inset{Left: 14, Right: 14}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
					gtx.Constraints.Min.Y = Dp(gtx, 36)
					return layout.W.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
						return th.Label(gtx, StBody, fg, a.Label)
					})
				})
				label := m.Stop()
				if !a.Disabled && (c.Hovered() || gtx.Focused(c)) {
					FillRRect(gtx, image.Rectangle{Max: dims.Size}.Inset(Dp(gtx, 4)), Dp(gtx, max(p.RadiusS-2, 2)), On(p.Group, p.Hover))
				}
				label.Add(gtx.Ops)
				return dims
			})
		}))
	}
	return layout.Inset{Top: unit.Dp(4), Bottom: unit.Dp(4)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx, kids...)
	})
}
