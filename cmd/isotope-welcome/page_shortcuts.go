package main

import (
	"gioui.org/layout"
	"gioui.org/widget"

	"github.com/kairos-community/isotope/pkg/guikit"
)

func init() {
	pageMakers[PShortcuts] = func(u *UI) page { return &shortcutsPage{basePage: basePage{u}} }
}

// shortcutsPage shows the variant's main keys as key caps, and opens the
// full sheet. There is nothing to apply, so there is no Skip.
type shortcutsPage struct {
	basePage
	list    widget.List
	all     widget.Clickable
	openErr string
}

func (p *shortcutsPage) Footer(n nav) (leading, trailing []guikit.Action) {
	if n.Back != nil {
		leading = append(leading, *n.Back)
	}
	return leading, []guikit.Action{*n.Continue}
}

func (p *shortcutsPage) Update(gtx layout.Context) {
	if p.all.Clicked(gtx) {
		p.showAll()
	}
}

// showAll is "Show all shortcuts".
func (p *shortcutsPage) showAll() {
	p.openErr = ""
	if p.u.hooks.OpenShortcutSheet == nil {
		return
	}
	if err := p.u.hooks.OpenShortcutSheet(); err != nil {
		p.openErr = "The shortcut sheet did not open: " + err.Error()
	}
}

func (p *shortcutsPage) Body(gtx layout.Context) layout.Dimensions {
	u := p.u
	rows := MainShortcuts(u.shortcuts)
	return u.scrolled(gtx, &p.list, func(gtx layout.Context) layout.Dimensions {
		kids := []layout.FlexChild{
			layout.Rigid(u.Heading("Shortcuts", "Isotope is made to be used from the keyboard. Super is the key with the Windows logo. These keys work in every app.")),
			guikit.VGap(26),
		}
		for i, s := range rows {
			if i > 0 {
				kids = append(kids, guikit.VGap(12))
			}
			kids = append(kids, layout.Rigid(func(gtx layout.Context) layout.Dimensions { return p.row(gtx, s) }))
		}
		kids = append(kids, guikit.VGap(26),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return u.Link(gtx, &p.all, "Show all shortcuts")
			}))
		if p.openErr != "" {
			kids = append(kids, guikit.VGap(12), layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return u.Warning(gtx, p.openErr)
			}))
		}
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx, kids...)
	})
}

// row is one shortcut: its caps in a column, then what it does.
func (p *shortcutsPage) row(gtx layout.Context, s Shortcut) layout.Dimensions {
	u := p.u
	return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			gtx.Constraints.Min.X = min(guikit.Dp(gtx, 250), gtx.Constraints.Max.X)
			gtx.Constraints.Max.X = gtx.Constraints.Min.X
			return u.KeyCaps(gtx, s.Keys)
		}),
		layout.Rigid(layout.Spacer{Width: 16}.Layout),
		layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
			return u.Label(gtx, guikit.StBody, guikit.ColText2, s.Action)
		}),
	)
}
