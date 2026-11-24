package main

import (
	"sync"

	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/widget"

	"github.com/kairos-community/isotope/pkg/guikit"
	"github.com/kairos-community/isotope/pkg/i18n"
)

func init() { pageMakers[PWelcome] = func(u *UI) page { return &welcomePage{basePage: basePage{u}} } }

// welcomePage is the first page: the mark, the edition and version, and
// how to set the machine up: "Set up as new", or "Restore from a backup",
// which is offered only to an administrator, only in the full flow, and
// only on a machine the daemon calls new (BackupStatus's can_restore).
type welcomePage struct {
	basePage
	list                 widget.List
	asNew, restore, quit widget.Clickable

	mu      sync.Mutex // the status call writes under it
	wg      sync.WaitGroup
	started bool
	offered bool
}

// start asks the daemon whether the machine is new, in the background.
// The page does not decide that itself; the daemon checks again on
// Restore.
func (p *welcomePage) start() {
	if p.started {
		return
	}
	p.started = true
	u := p.u
	b := u.hooks.Backup
	if b == nil || !u.hooks.Admin || len(u.setupFlow.Pages()) < 2 || u.setupFlow.Pages()[0] != PWelcome {
		return
	}
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		st, err := b.BackupStatus()
		p.mu.Lock()
		p.offered = err == nil && st.Available && st.CanRestore
		p.mu.Unlock()
		if u.hooks.Invalidate != nil {
			u.hooks.Invalidate()
		}
	}()
}

// wait blocks until the status call has ended (tests).
func (p *welcomePage) wait() { p.wg.Wait() }

func (p *welcomePage) restoreOffered() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.offered
}

// restoring: "Restore from a backup" is chosen.
func (p *welcomePage) restoring() bool {
	return p.u.restoreFlow != nil && p.u.flow == p.u.restoreFlow
}

// choose picks the flow: the restore steps, or the setup. Leaving the
// restore drops its secrets.
func (p *welcomePage) choose(restore bool) {
	u := p.u
	switch {
	case restore && p.restoreOffered():
		if u.restoreFlow == nil {
			u.restoreFlow = NewRestoreFlow()
		}
		u.flow = u.restoreFlow
		u.restoreState().start()
	case !restore && p.restoring():
		u.restoreState().leave()
		u.flow = u.setupFlow
	}
}

func (p *welcomePage) Footer(n nav) (leading, trailing []guikit.Action) {
	return []guikit.Action{{Click: &p.quit, Kind: guikit.ButtonGhost, Label: i18n.T("Skip setup")}},
		[]guikit.Action{*n.Continue}
}

func (p *welcomePage) Update(gtx layout.Context) {
	if p.quit.Clicked(gtx) {
		p.u.askSkip()
	}
	p.start()
	if p.asNew.Clicked(gtx) {
		if p.restoreOffered() {
			// Two choices: a click selects, and Continue goes on.
			p.choose(false)
		} else {
			// The only choice: a click on it is a Continue.
			p.u.next()
		}
	}
	if p.restore.Clicked(gtx) {
		p.choose(true)
	}
}

func (p *welcomePage) Body(gtx layout.Context) layout.Dimensions {
	u := p.u
	return u.scrolled(gtx, &p.list, func(gtx layout.Context) layout.Dimensions {
		// A short column (a large scale, a small screen) gets a smaller
		// mark, and a very short one none (the sidebar has it), so the
		// choice stays in view.
		glow, mark := float32(150), float32(110)
		switch h := gtx.Constraints.Min.Y; {
		case h < guikit.Dp(gtx, 420):
			glow = 0
		case h < guikit.Dp(gtx, 560):
			glow, mark = 96, 70
		}
		return layout.Center.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			gtx.Constraints.Max.X = min(gtx.Constraints.Max.X, guikit.Dp(gtx, 480))
			return layout.Flex{Axis: layout.Vertical, Alignment: layout.Middle}.Layout(gtx,
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					if glow == 0 {
						return layout.Dimensions{}
					}
					guikit.Glow(gtx, glow, guikit.ColAccent, 0.14)
					o := op.Offset(guikit.Pt(gtx, (glow-mark)/2, (glow-mark)/2)).Push(gtx.Ops)
					guikit.Mark(gtx, mark, false, 1, 0)
					o.Pop()
					return layout.Dimensions{Size: guikit.Pt(gtx, glow, glow)}
				}),
				guikit.VGap(14),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return u.Label(gtx, guikit.StHero, guikit.ColText, i18n.T("Welcome to Isotope"))
				}),
				guikit.VGap(8),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return u.Label(gtx, guikit.StBody, guikit.ColDim, u.version)
				}),
				guikit.VGap(20),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return u.Centered(gtx, guikit.StLead, guikit.ColText2, 1.55,
						i18n.T("A few choices make this computer yours: the language, the network, the look and the apps. Each step can be skipped, and you can come back to this window later from the Isotope menu."))
				}),
				guikit.VGap(26),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					gtx.Constraints.Max.X = min(gtx.Constraints.Max.X, guikit.Dp(gtx, 420))
					return u.ChoiceCard(gtx, &p.asNew, !p.restoring(), i18n.T("Set up as new"), i18n.T("Choose the settings step by step"))
				}),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					if !p.restoreOffered() {
						return layout.Dimensions{}
					}
					return layout.Inset{Top: 10}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
						gtx.Constraints.Max.X = min(gtx.Constraints.Max.X, guikit.Dp(gtx, 420))
						return u.ChoiceCard(gtx, &p.restore, p.restoring(), i18n.T("Restore from a backup"), i18n.T("Bring back a machine from its Isotope backup"))
					})
				}),
			)
		})
	})
}
