package guikit

import (
	"image"
	"image/color"
	"strings"

	"gioui.org/gesture"
	"gioui.org/io/event"
	"gioui.org/io/key"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/unit"
	"gioui.org/widget"
)

// ButtonKind is the look of a button.
type ButtonKind int

const (
	ButtonPrimary   ButtonKind = iota // accent: Continue, Restart
	ButtonSecondary                   // Go back
	ButtonGhost                       // Shut down, Show details
	ButtonDanger                      // Erase and install
)

// Button draws a 40 dp button. A disabled button is drawn greyed and
// ignores clicks and keys.
func (th *Theme) Button(gtx layout.Context, c *widget.Clickable, kind ButtonKind, txt string, enabled bool) layout.Dimensions {
	if !enabled {
		gtx = gtx.Disabled()
	}
	padX := float32(22)
	st, fg := StButton, ColOnAccent
	b := Box{Radius: 8, Fill: ColAccent}
	switch {
	case !enabled:
		b.Fill, fg = ColDisabled, ColDim
	case kind == ButtonSecondary:
		padX, st, fg = 18, StButton500, ColText
		b.Fill, b.Border, b.BorderWidth = ColField, Over(ColFooter, ColText, 0.1), 1
	case kind == ButtonGhost:
		padX, st, fg = 16, StButton500, ColDim
		b.Fill = color.NRGBA{}
	case kind == ButtonDanger:
		b.Fill = ColError
	}
	if enabled && gtx.Focused(c) {
		b.Ring, b.RingWidth = FocusRingStrong, 3
		if kind == ButtonGhost {
			fg = ColText
		}
	}
	return c.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		if enabled {
			pointer.CursorPointer.Add(gtx.Ops)
		}
		gtx.Constraints.Min.Y = Dp(gtx, 40)
		return b.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			return layout.Inset{Left: unit.Dp(padX), Right: unit.Dp(padX)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				gtx.Constraints.Min.X = 0
				return layout.Center.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
					return th.Label(gtx, st, fg, txt)
				})
			})
		})
	})
}

// Field is a one-line text input with the error shown under it.
type Field struct {
	Editor widget.Editor
	// Err is shown under the field, in red, instead of its hint; the
	// field's border turns red too.
	Err string
	// SelectOnFocus selects the text when the field gets the focus, so
	// typing replaces a prefilled value ("UTC", "isotope") instead of
	// appending to it.
	SelectOnFocus bool
	hadFocus      bool
}

// NewField is an empty one-line field. With submit set, Enter in it is a
// submit (see Submitted).
func NewField(submit bool) *Field {
	f := &Field{}
	f.Editor.SingleLine = true
	f.Editor.Submit = submit
	return f
}

// NewSecret is a submitting field that shows bullets for what is typed.
func NewSecret() *Field {
	f := NewField(true)
	f.Editor.Mask = '•'
	return f
}

// Submitted handles the field's events and says whether Enter was pressed
// in it. Call it once a frame, before the field is drawn.
func (f *Field) Submitted(gtx layout.Context) bool {
	hit := false
	for {
		e, ok := f.Editor.Update(gtx)
		if !ok {
			return hit
		}
		if _, ok := e.(widget.SubmitEvent); ok {
			hit = true
		}
	}
}

// Input draws the field's editor in its box, height dp high (38 for a
// normal field). prefix, when set, is drawn in the box before the text
// (the "github.com/" of an SSH key field); trailing, when set, at its end.
func (th *Theme) Input(gtx layout.Context, f *Field, s Style, height float32, prefix string, trailing layout.Widget) layout.Dimensions {
	focused := gtx.Focused(&f.Editor)
	if focused && !f.hadFocus && f.SelectOnFocus {
		f.Editor.SetCaret(f.Editor.Len(), 0)
	}
	f.hadFocus = focused
	b := Box{Fill: ColField, Border: Over(ColField, ColText, 0.08), BorderWidth: 1, Radius: 8}
	if focused {
		b.Border, b.Ring, b.RingWidth = ColAccent, FocusRing, 3
	}
	if f.Err != "" {
		b.Border = ColError
	}
	gtx.Constraints.Min.Y = Dp(gtx, height)
	gtx.Constraints.Max.Y = Dp(gtx, height)
	return b.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		gtx.Constraints.Min.X = gtx.Constraints.Max.X
		return layout.Inset{Left: 12, Right: 12}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			gtx.Constraints.Min = gtx.Constraints.Max
			var kids []layout.FlexChild
			if prefix != "" {
				kids = append(kids, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					gtx.Constraints.Min.Y = gtx.Constraints.Max.Y
					return layout.W.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
						gtx.Constraints.Min.Y = 0
						return th.Label(gtx, s, ColDim, prefix)
					})
				}))
			}
			kids = append(kids, layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
				gtx.Constraints.Min.Y = gtx.Constraints.Max.Y
				// W keeps the vertical minimum; zero it so the text is
				// centred rather than drawn at the top of the box.
				return layout.W.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
					gtx.Constraints.Min = image.Pt(gtx.Constraints.Max.X, 0)
					return f.Editor.Layout(gtx, th.Shaper, s.Font(), s.Size, Material(gtx, ColText), Material(gtx, Alpha(ColAccent, 0.35)))
				})
			}))
			if trailing != nil {
				kids = append(kids, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					gtx.Constraints.Min.Y = gtx.Constraints.Max.Y
					return layout.E.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
						gtx.Constraints.Min.Y = 0
						return trailing(gtx)
					})
				}))
			}
			return layout.Flex{Alignment: layout.Middle}.Layout(gtx, kids...)
		})
	})
}

// Labelled stacks a label, the input in and a hint, or f's error when it has
// one. f may be nil.
func (th *Theme) Labelled(gtx layout.Context, title string, f *Field, hint string, in layout.Widget) layout.Dimensions {
	kids := []layout.FlexChild{
		layout.Rigid(func(gtx layout.Context) layout.Dimensions { return th.Label(gtx, StLabel, ColText2, title) }),
		layout.Rigid(layout.Spacer{Height: 6}.Layout),
		layout.Rigid(in),
	}
	msg, col := hint, ColDim
	if f != nil && f.Err != "" {
		msg, col = f.Err, ColError
	}
	if msg != "" {
		kids = append(kids,
			layout.Rigid(layout.Spacer{Height: 6}.Layout),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions { return th.Label(gtx, StTiny, col, msg) }))
	}
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx, kids...)
}

// Radio is the round marker of a choice card.
func Radio(gtx layout.Context, on bool) layout.Dimensions {
	s := Dp(gtx, 18)
	r := image.Rect(0, 0, s, s)
	if on {
		StrokeRRect(gtx, r, s/2, Fdp(gtx, 5), ColAccent)
	} else {
		StrokeRRect(gtx, r, s/2, Fdp(gtx, 1.5), ColRing)
	}
	return layout.Dimensions{Size: r.Max}
}

// Checkbox is the square box: accent filled with a tick when on.
func Checkbox(gtx layout.Context, on bool) layout.Dimensions {
	s := Dp(gtx, 18)
	r := image.Rect(0, 0, s, s)
	if on {
		FillRRect(gtx, r, Dp(gtx, 4), ColAccent)
		m := op.Offset(image.Pt(Dp(gtx, 1), Dp(gtx, 1))).Push(gtx.Ops)
		Tick(gtx, 16, ColOnAccent)
		m.Pop()
	} else {
		FillRRect(gtx, r, Dp(gtx, 4), ColField)
		StrokeRRect(gtx, r, Dp(gtx, 4), Fdp(gtx, 1.5), ColRing)
	}
	return layout.Dimensions{Size: r.Max}
}

// Card is a selectable card, w inside it with padding pad.
func (th *Theme) Card(gtx layout.Context, c *widget.Clickable, selected bool, pad layout.Inset, w layout.Widget) layout.Dimensions {
	b := Box{Radius: 12, Fill: ColSidebar, Border: Over(ColSidebar, ColText, 0.08), BorderWidth: 1}
	if selected {
		b.Fill, b.Border, b.BorderWidth = Over(ColWindow, ColAccent, 0.08), ColAccent, 2
		b.Ring, b.RingWidth = Over(ColWindow, ColAccent, 0.12), 4
	}
	if gtx.Focused(c) {
		b.Ring, b.RingWidth = FocusRingStrong, 3
		if !selected {
			b.Border = Over(ColWindow, ColAccent, 0.6)
		}
	}
	return c.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		pointer.CursorPointer.Add(gtx.Ops)
		return b.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			return pad.Layout(gtx, w)
		})
	})
}

// ChoiceCard is one card of a choice between cards: a radio marker, a title
// and a line under it. It fills the width it is given.
func (th *Theme) ChoiceCard(gtx layout.Context, c *widget.Clickable, on bool, title, sub string) layout.Dimensions {
	gtx.Constraints.Min.X = gtx.Constraints.Max.X
	return th.Card(gtx, c, on, layout.Inset{Top: 16, Bottom: 16, Left: 18, Right: 18}, func(gtx layout.Context) layout.Dimensions {
		return layout.Flex{}.Layout(gtx,
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return layout.Inset{Top: 2}.Layout(gtx, func(gtx layout.Context) layout.Dimensions { return Radio(gtx, on) })
			}),
			layout.Rigid(layout.Spacer{Width: 14}.Layout),
			layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
				return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
					layout.Rigid(func(gtx layout.Context) layout.Dimensions { return th.Label(gtx, StCard, ColText, title) }),
					VGap(4),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions { return th.Label(gtx, StSmall, ColDim, sub) }),
				)
			}),
		)
	})
}

// CheckCard is an option in a box: a checkbox, a title and a paragraph
// under it. A click on the box toggles it; the app flips on when c is
// clicked. It fills the width it is given.
func (th *Theme) CheckCard(gtx layout.Context, c *widget.Clickable, on bool, title, sub string) layout.Dimensions {
	gtx.Constraints.Min.X = gtx.Constraints.Max.X
	b := Box{Radius: 10, Fill: ColSidebar, Border: Over(ColSidebar, ColText, 0.06), BorderWidth: 1}
	if gtx.Focused(c) {
		b.Border, b.Ring, b.RingWidth = ColAccent, FocusRing, 3
	}
	return c.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		return b.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			gtx.Constraints.Min.X = gtx.Constraints.Max.X
			return layout.Inset{Top: 14, Bottom: 14, Left: 16, Right: 16}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				return layout.Flex{}.Layout(gtx,
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						return layout.Inset{Top: 1}.Layout(gtx, func(gtx layout.Context) layout.Dimensions { return Checkbox(gtx, on) })
					}),
					layout.Rigid(layout.Spacer{Width: 12}.Layout),
					layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
						return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
							layout.Rigid(func(gtx layout.Context) layout.Dimensions {
								return th.Label(gtx, StBody600, ColText, title)
							}),
							VGap(4),
							layout.Rigid(func(gtx layout.Context) layout.Dimensions {
								return th.LabelLines(gtx, StSmall, ColDim, 1.45, sub)
							}),
						)
					}),
				)
			})
		})
	})
}

// Panel is the mocks' dark inset box (a key box, a list), with corner
// radius dp and w inside it with padding pad.
func Panel(gtx layout.Context, radius float32, pad layout.Inset, w layout.Widget) layout.Dimensions {
	b := Box{Radius: radius, Fill: ColSidebar, Border: Over(ColSidebar, ColText, 0.06), BorderWidth: 1}
	return b.Layout(gtx, func(gtx layout.Context) layout.Dimensions { return pad.Layout(gtx, w) })
}

// Warning is the red box with an icon and words, the full width.
func (th *Theme) Warning(gtx layout.Context, txt string) layout.Dimensions {
	b := Box{Radius: 10, Fill: Over(ColWindow, ColError, 0.1), Border: Over(ColWindow, ColError, 0.35), BorderWidth: 1}
	return b.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		gtx.Constraints.Min.X = gtx.Constraints.Max.X
		return layout.Inset{Top: 12, Bottom: 12, Left: 16, Right: 16}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
				layout.Rigid(func(gtx layout.Context) layout.Dimensions { return WarnIcon(gtx, 18, ColError) }),
				layout.Rigid(layout.Spacer{Width: 12}.Layout),
				layout.Flexed(1, func(gtx layout.Context) layout.Dimensions { return th.Label(gtx, StBody, ColErrorText, txt) }),
			)
		})
	})
}

// Link is a text button in the glow colour, underlined when focused.
func (th *Theme) Link(gtx layout.Context, c *widget.Clickable, txt string) layout.Dimensions {
	col := ColGlow
	if c.Hovered() || gtx.Focused(c) {
		col = ColText
	}
	return c.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		pointer.CursorPointer.Add(gtx.Ops)
		dims := th.Label(gtx, StSmall, col, txt)
		if gtx.Focused(c) {
			FillRect(gtx, image.Rect(0, dims.Size.Y-Dp(gtx, 1), dims.Size.X, dims.Size.Y), ColAccent)
		}
		return dims
	})
}

// Heading is a page title and the paragraph under it.
func (th *Theme) Heading(title, sub string) layout.Widget {
	return func(gtx layout.Context) layout.Dimensions {
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
			layout.Rigid(func(gtx layout.Context) layout.Dimensions { return th.Label(gtx, StTitle, ColText, title) }),
			layout.Rigid(layout.Spacer{Height: 6}.Layout),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				gtx.Constraints.Max.X = min(gtx.Constraints.Max.X, Dp(gtx, 580))
				return th.LabelLines(gtx, StBody, ColDim, 1.5, sub)
			}),
		)
	}
}

// Scrolled lets a page that grows (error lines, a larger scale) scroll in l
// instead of being cut at the footer. l must be vertical.
func Scrolled(gtx layout.Context, l *widget.List, w layout.Widget) layout.Dimensions {
	return l.List.Layout(gtx, 1, func(gtx layout.Context, _ int) layout.Dimensions { return w(gtx) })
}

// VGap is a vertical space of h dp in a vertical Flex.
func VGap(h float32) layout.FlexChild { return layout.Rigid(layout.Spacer{Height: unit.Dp(h)}.Layout) }

// TwoCols lays a and b side by side in equal columns, 14 dp apart.
func TwoCols(gtx layout.Context, a, b layout.Widget) layout.Dimensions {
	return layout.Flex{}.Layout(gtx,
		layout.Flexed(1, a),
		layout.Rigid(layout.Spacer{Width: 14}.Layout),
		layout.Flexed(1, b),
	)
}

// Row is a row of a pick list: clickable, but not a Tab stop, so Tab goes
// from a search field to the next control instead of through every row.
// The arrow keys move in the list (see ArrowKey and MoveSelection).
type Row struct{ c gesture.Click }

// Layout draws w and makes it clickable.
func (r *Row) Layout(gtx layout.Context, w layout.Widget) layout.Dimensions {
	dims := w(gtx)
	defer clip.Rect(image.Rectangle{Max: dims.Size}).Push(gtx.Ops).Pop()
	pointer.CursorPointer.Add(gtx.Ops)
	r.c.Add(gtx.Ops)
	return dims
}

// Clicked handles the row's events and says whether it was clicked.
func (r *Row) Clicked(gtx layout.Context) bool {
	hit := false
	for {
		e, ok := r.c.Update(gtx.Source)
		if !ok {
			return hit
		}
		if e.Kind == gesture.KindClick {
			hit = true
		}
	}
}

// Hovered says whether the pointer is over the row.
func (r *Row) Hovered() bool { return r.c.Hovered() }

// PickList is a list to pick one entry from, filling the space it is given:
// the entries whose indices are in shown, in that order, in l; entry sel in
// the accent colour. rows has one Row per entry (indexed like the entries,
// not like shown). text gives an entry's name and an optional code drawn on
// the right.
func (th *Theme) PickList(gtx layout.Context, l *widget.List, shown []int, sel int, rows []Row, text func(int) (string, string)) layout.Dimensions {
	gtx.Constraints.Min = gtx.Constraints.Max
	return Panel(gtx, 10, layout.UniformInset(6), func(gtx layout.Context) layout.Dimensions {
		gtx.Constraints.Min = gtx.Constraints.Max
		if len(shown) == 0 {
			return layout.Inset{Top: 9, Left: 12}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				return th.Label(gtx, StBody, ColDim, "Nothing matches.")
			})
		}
		return l.List.Layout(gtx, len(shown), func(gtx layout.Context, n int) layout.Dimensions {
			i := shown[n]
			name, code := text(i)
			on := i == sel
			gtx.Constraints.Min.X = gtx.Constraints.Max.X
			return layout.Inset{Bottom: 2}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				return rows[i].Layout(gtx, func(gtx layout.Context) layout.Dimensions {
					m := op.Record(gtx.Ops)
					dims := layout.Inset{Top: 9, Bottom: 9, Left: 12, Right: 12}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
						fg, st, cc := ColText2, StBody, ColDim
						if on {
							fg, st, cc = ColOnAccent, StBody600, ColOnAccent
						}
						return layout.Flex{Spacing: layout.SpaceBetween, Alignment: layout.Middle}.Layout(gtx,
							layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
								gtx.Constraints.Min.X = 0
								return th.LabelMax(gtx, st, fg, 1, name)
							}),
							layout.Rigid(func(gtx layout.Context) layout.Dimensions {
								if code == "" {
									return layout.Dimensions{}
								}
								return layout.Inset{Left: 8}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
									return th.Label(gtx, StMono12, cc, code)
								})
							}),
						)
					})
					c := m.Stop()
					switch {
					case on:
						FillRRect(gtx, image.Rectangle{Max: dims.Size}, Dp(gtx, 6), ColAccent)
					case rows[i].Hovered():
						FillRRect(gtx, image.Rectangle{Max: dims.Size}, Dp(gtx, 6), Over(ColSidebar, ColText, 0.05))
					}
					c.Add(gtx.Ops)
					return dims
				})
			})
		})
	})
}

// ArrowKey reads the next Up or Down press while focus (a search field
// above a pick list) has the keyboard focus. Call it until ok is false.
func ArrowKey(gtx layout.Context, focus event.Tag) (down, ok bool) {
	for {
		e, ok := gtx.Event(
			key.Filter{Focus: focus, Name: key.NameUpArrow},
			key.Filter{Focus: focus, Name: key.NameDownArrow},
		)
		if !ok {
			return false, false
		}
		if k, ok := e.(key.Event); ok && k.State == key.Press {
			return k.Name == key.NameDownArrow, true
		}
	}
}

// MoveSelection moves sel to the next or previous entry of shown, scrolls
// l to keep it in view, and returns it.
func MoveSelection(shown []int, sel int, down bool, l *widget.List) int {
	if len(shown) == 0 {
		return sel
	}
	pos := -1
	for i, v := range shown {
		if v == sel {
			pos = i
		}
	}
	switch {
	case pos < 0:
		pos = 0
	case down && pos < len(shown)-1:
		pos++
	case !down && pos > 0:
		pos--
	}
	if pos < l.Position.First {
		l.Position.First = pos
		l.Position.Offset = 0
	} else if pos >= l.Position.First+l.Position.Count-1 && l.Position.Count > 0 {
		l.Position.First = pos - l.Position.Count + 2
		l.Position.Offset = 0
	}
	return shown[pos]
}

// KeyCaps draws a key combination as key caps joined by "+": keys are the
// keys in order ("Super", "Shift", "E"). A key may list alternatives joined
// by " / " ("D / Space"); they are drawn as caps joined by "/".
func (th *Theme) KeyCaps(gtx layout.Context, keys []string) layout.Dimensions {
	var kids []layout.FlexChild
	sep := func(s string) layout.FlexChild {
		return layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return layout.Inset{Left: 6, Right: 6}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				return th.Label(gtx, StSmall, ColDim, s)
			})
		})
	}
	for i, k := range keys {
		if i > 0 {
			kids = append(kids, sep("+"))
		}
		for j, alt := range strings.Split(k, " / ") {
			if j > 0 {
				kids = append(kids, sep("/"))
			}
			kids = append(kids, layout.Rigid(func(gtx layout.Context) layout.Dimensions { return th.keyCap(gtx, alt) }))
		}
	}
	return layout.Flex{Alignment: layout.Middle}.Layout(gtx, kids...)
}

// keyCap is one key: its name in the mono face on the key box colour.
func (th *Theme) keyCap(gtx layout.Context, name string) layout.Dimensions {
	b := Box{Radius: 6, Fill: ColKeyBox, Border: Over(ColKeyBox, ColText, 0.16), BorderWidth: 1}
	gtx.Constraints.Min = image.Point{}
	return b.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		gtx.Constraints.Min.X = Dp(gtx, 30)
		return layout.Inset{Top: 5, Bottom: 5, Left: 10, Right: 10}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			return layout.Center.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				return th.Label(gtx, StMono, ColText, name)
			})
		})
	})
}
