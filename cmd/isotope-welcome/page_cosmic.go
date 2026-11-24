package main

import (
	"image"
	"image/png"
	"os"
	"sync"

	"gioui.org/layout"
	"gioui.org/op/paint"
	"gioui.org/widget"

	"github.com/kairos-community/isotope/pkg/guikit"
	"github.com/kairos-community/isotope/pkg/i18n"
)

func init() {
	pageMakers[PLayout] = func(u *UI) page { return &layoutPage{basePage: basePage{u}} }
	pageMakers[PWorkflow] = func(u *UI) page { return &infoPage{basePage: basePage{u}, id: PWorkflow} }
	pageMakers[PLauncher] = func(u *UI) page { return &infoPage{basePage: basePage{u}, id: PLauncher} }
}

// layoutCard is one layout on the page: its icon, decoded once off the
// frame path, and the click target.
type layoutCard struct {
	Layout
	img   paint.ImageOp
	ok    bool
	click widget.Clickable
}

// layoutPage picks the panel layout. The layouts and the current one are
// read in the background; Continue installs a changed choice (the copy and
// the panel restart take a moment) and goes on when it worked. The
// restarted panel shows the layout at once, so there is no "log in again".
type layoutPage struct {
	basePage
	list widget.List

	started bool

	mu       sync.Mutex
	wg       sync.WaitGroup
	cards    []*layoutCard
	loaded   bool
	loadErr  string
	current  string // the layout installed now, "" when none matches
	selected string // the user's choice, "" before one
	busy     bool
	applied  bool
	err      string
}

func (p *layoutPage) start() {
	if p.started {
		return
	}
	p.started = true
	h := p.u.hooks
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		ls, err := Layouts(h.LayoutsDir)
		cards := make([]*layoutCard, len(ls))
		for i, l := range ls {
			c := &layoutCard{Layout: l}
			if f, err := os.Open(l.Icon); err == nil {
				if im, err := png.Decode(f); err == nil {
					c.img, c.ok = paint.NewImageOp(im), true
				}
				f.Close()
			}
			cards[i] = c
		}
		cur := CurrentLayout(h.Home, ls)
		p.mu.Lock()
		p.cards, p.loaded, p.current = cards, true, cur
		if err != nil {
			p.loadErr = i18n.Tf("Could not read the panel layouts: %v", err)
		}
		if p.selected == "" {
			p.selected = cur
		}
		p.mu.Unlock()
		p.invalidate()
	}()
}

func (p *layoutPage) wait() { p.wg.Wait() }

func (p *layoutPage) invalidate() {
	if p.u.hooks.Invalidate != nil {
		p.u.hooks.Invalidate()
	}
}

func (p *layoutPage) choose(id string) {
	p.mu.Lock()
	p.selected = id
	p.mu.Unlock()
}

func (p *layoutPage) Update(gtx layout.Context) {
	p.start()
	p.mu.Lock()
	cards := p.cards
	p.mu.Unlock()
	for _, c := range cards {
		if c.click.Clicked(gtx) {
			p.choose(c.ID)
		}
	}
	p.mu.Lock()
	done := p.applied
	p.applied = false
	p.mu.Unlock()
	if done {
		p.u.flow.Apply()
		p.u.flow.Next()
	}
}

func (p *layoutPage) Continue() {
	h := p.u.hooks
	p.mu.Lock()
	if p.busy {
		p.mu.Unlock()
		return
	}
	sel, cur := p.selected, p.current
	var pick *Layout
	for _, c := range p.cards {
		if c.ID == sel {
			l := c.Layout
			pick = &l
		}
	}
	if pick == nil || sel == cur || h.Runner == nil {
		// Nothing chosen, or the choice is already installed: leave the
		// panel alone and do not restart it.
		p.mu.Unlock()
		if pick != nil && sel == cur {
			p.u.flow.Apply()
		}
		p.u.flow.Next()
		return
	}
	p.busy, p.err = true, ""
	p.mu.Unlock()
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		err := ApplyLayout(h.Runner, h.Home, *pick)
		p.mu.Lock()
		p.busy = false
		if err != nil {
			p.err = i18n.Tf("Could not change the layout: %v", err)
		} else {
			p.applied, p.current = true, sel
		}
		p.mu.Unlock()
		p.invalidate()
	}()
}

func (p *layoutPage) Body(gtx layout.Context) layout.Dimensions {
	u := p.u
	p.mu.Lock()
	cards, loaded, loadErr, sel, busy, errText := p.cards, p.loaded, p.loadErr, p.selected, p.busy, p.err
	p.mu.Unlock()
	return u.scrolled(gtx, &p.list, func(gtx layout.Context) layout.Dimensions {
		gtx.Constraints.Max.X = min(gtx.Constraints.Max.X, guikit.Dp(gtx, 680))
		gtx.Constraints.Min.X = gtx.Constraints.Max.X
		kids := []layout.FlexChild{
			layout.Rigid(u.Heading(i18n.X("page", "Layout"), i18n.T("Move the panel or dock to any edge, change their size, and automatically hide them in Settings."))),
			guikit.VGap(22),
		}
		switch {
		case errText != "":
			kids = append(kids, layout.Rigid(func(gtx layout.Context) layout.Dimensions { return u.Warning(gtx, errText) }), guikit.VGap(14))
		case loadErr != "":
			kids = append(kids, layout.Rigid(func(gtx layout.Context) layout.Dimensions { return u.Warning(gtx, loadErr) }), guikit.VGap(14))
		case busy:
			kids = append(kids, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return u.Label(gtx, guikit.StSmall, guikit.ColDim, i18n.T("Applying…"))
			}), guikit.VGap(14))
		}
		if loaded && len(cards) > 0 {
			kids = append(kids, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				var row []layout.FlexChild
				for i, c := range cards {
					if i > 0 {
						row = append(row, layout.Rigid(layout.Spacer{Width: 14}.Layout))
					}
					row = append(row, layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
						return p.card(gtx, c, c.ID == sel)
					}))
				}
				return layout.Flex{}.Layout(gtx, row...)
			}), guikit.VGap(14), layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return u.Label(gtx, guikit.StSmall, guikit.ColDim, i18n.T("The panel restarts when you continue."))
			}))
		}
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx, kids...)
	})
}

// card is a layout's picture over its name, in a card.
func (p *layoutPage) card(gtx layout.Context, c *layoutCard, on bool) layout.Dimensions {
	u := p.u
	gtx.Constraints.Min.X = gtx.Constraints.Max.X
	return u.Card(gtx, &c.click, on, layout.UniformInset(14), func(gtx layout.Context) layout.Dimensions {
		gtx.Constraints.Min.X = gtx.Constraints.Max.X
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				if !c.ok {
					return layout.Dimensions{Size: image.Pt(gtx.Constraints.Max.X, guikit.Dp(gtx, 80))}
				}
				return widget.Image{Src: c.img, Fit: widget.Contain, Position: layout.Center}.Layout(gtx)
			}),
			guikit.VGap(12),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
					layout.Rigid(func(gtx layout.Context) layout.Dimensions { return guikit.Radio(gtx, on) }),
					layout.Rigid(layout.Spacer{Width: 12}.Layout),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						return u.Label(gtx, guikit.StCard, guikit.ColText, c.Name)
					}),
				)
			}),
		)
	})
}

// infoPage is Workflow or Launcher. cosmic-initial-setup (epoch-1.11.0,
// src/page/workflow.rs and launcher.rs) shows an illustration and a
// paragraph on both and writes no setting, so neither does this: there is
// nothing to apply, and Continue only goes on. Skip is left out of the
// footer, since there is nothing to skip.
type infoPage struct {
	basePage
	id   PageID
	list widget.List
}

func (p *infoPage) Footer(n nav) (leading, trailing []guikit.Action) {
	if n.Back != nil {
		leading = append(leading, *n.Back)
	}
	return leading, []guikit.Action{*n.Continue}
}

func (p *infoPage) Body(gtx layout.Context) layout.Dimensions {
	u := p.u
	return u.scrolled(gtx, &p.list, func(gtx layout.Context) layout.Dimensions {
		gtx.Constraints.Max.X = min(gtx.Constraints.Max.X, guikit.Dp(gtx, 680))
		gtx.Constraints.Min.X = gtx.Constraints.Max.X
		if p.id == PWorkflow {
			return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
				layout.Rigid(u.Heading(i18n.T("Workspaces for your workflow"), i18n.T("Float or automatically tile windows per-workspace using the tiling applet."))),
				guikit.VGap(22),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return p.note(gtx, i18n.T("Vertical or horizontal"), i18n.T("You can select vertical or horizontal workspaces. You can also pin workspaces to make them static."), nil)
				}),
				guikit.VGap(14),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return u.Label(gtx, guikit.StSmall, guikit.ColDim, i18n.T("Change these any time in COSMIC Settings. Nothing is set on this page."))
				}),
			)
		}
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
			layout.Rigid(u.Heading(i18n.T("Fast and efficient"), i18n.T("Press the Super (or Windows) key to activate the Launcher. Search and press Enter to open an app or switch to it."))),
			guikit.VGap(22),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return p.note(gtx, i18n.T("Press Super"), i18n.T("You can also jump to settings or system functions like suspend. Type “?” to learn about the Launcher's advanced features."),
					func(gtx layout.Context) layout.Dimensions { return u.KeyCaps(gtx, []string{"Super"}) })
			}),
			guikit.VGap(14),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return u.Label(gtx, guikit.StSmall, guikit.ColDim, i18n.T("Nothing is set on this page."))
			}),
		)
	})
}

// note is a titled paragraph in a panel, with an optional widget (the key)
// beside the title.
func (p *infoPage) note(gtx layout.Context, title, body string, extra layout.Widget) layout.Dimensions {
	u := p.u
	return guikit.Panel(gtx, 12, layout.UniformInset(18), func(gtx layout.Context) layout.Dimensions {
		gtx.Constraints.Min.X = gtx.Constraints.Max.X
		kids := []layout.FlexChild{
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return u.Label(gtx, guikit.StCard, guikit.ColText, title)
			}),
		}
		if extra != nil {
			kids = append(kids, guikit.VGap(10), layout.Rigid(extra))
		}
		kids = append(kids, guikit.VGap(10), layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return u.LabelLines(gtx, guikit.StBody, guikit.ColDim, 1.5, body)
		}))
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx, kids...)
	})
}
