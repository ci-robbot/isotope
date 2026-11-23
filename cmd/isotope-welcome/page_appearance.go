package main

import (
	"sync"

	"gioui.org/layout"
	"gioui.org/widget"

	"github.com/kairos-community/isotope/pkg/guikit"
)

func init() {
	pageMakers[PAppearance] = func(u *UI) page { return &appearancePage{basePage: basePage{u}, dark: true} }
}

// lightNoteTiling is the light card's line where only the apps follow the
// choice: the bar, terminal, notifications and menus stay dark.
const lightNoteTiling = "Apps use a light theme; the bar and menus stay dark"

// lightNoteCosmic is its line on COSMIC, where the whole desktop turns light.
const lightNoteCosmic = "Light windows and panels"

// appearancePage is dark or light. What is set now is read in the
// background, and Continue writes the choice there too (gsettings can take a
// moment), so the frame never waits.
type appearancePage struct {
	basePage
	list widget.List

	started      bool
	darkC, light widget.Clickable

	mu      sync.Mutex
	wg      sync.WaitGroup
	dark    bool
	picked  bool   // the user chose before the read came back
	busy    bool   // Continue is writing
	applied bool   // the write worked; Update goes on
	err     string // the write failed
}

// start reads what is set now, once, in the background.
func (p *appearancePage) start() {
	if p.started {
		return
	}
	p.started = true
	h := p.u.hooks
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		dark := ReadColorScheme(h.Runner, h.Home, p.u.desktop)
		p.mu.Lock()
		if !p.picked {
			p.dark = dark
		}
		p.mu.Unlock()
		p.invalidate()
	}()
}

func (p *appearancePage) wait() { p.wg.Wait() }

func (p *appearancePage) invalidate() {
	if p.u.hooks.Invalidate != nil {
		p.u.hooks.Invalidate()
	}
}

func (p *appearancePage) choose(dark bool) {
	p.mu.Lock()
	p.dark, p.picked = dark, true
	p.mu.Unlock()
}

func (p *appearancePage) Update(gtx layout.Context) {
	p.start()
	if p.darkC.Clicked(gtx) {
		p.choose(true)
	}
	if p.light.Clicked(gtx) {
		p.choose(false)
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

// Continue writes the choice in the background and goes on when it worked. A
// failure is shown on the page and the flow stays, so the user can try again
// or skip.
func (p *appearancePage) Continue() {
	h := p.u.hooks
	if h.Runner == nil {
		p.u.flow.Next()
		return
	}
	p.mu.Lock()
	if p.busy {
		p.mu.Unlock()
		return
	}
	p.busy, p.err = true, ""
	dark := p.dark
	p.mu.Unlock()
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		err := SetColorScheme(h.Runner, h.Home, p.u.desktop, dark)
		p.mu.Lock()
		p.busy = false
		if err != nil {
			p.err = "Could not change the appearance: " + err.Error()
		} else {
			p.applied = true
		}
		p.mu.Unlock()
		p.invalidate()
	}()
}

func (p *appearancePage) Body(gtx layout.Context) layout.Dimensions {
	u := p.u
	lightNote := lightNoteTiling
	if u.desktop == Cosmic {
		lightNote = lightNoteCosmic
	}
	p.mu.Lock()
	dark, busy, errText := p.dark, p.busy, p.err
	p.mu.Unlock()
	return u.scrolled(gtx, &p.list, func(gtx layout.Context) layout.Dimensions {
		gtx.Constraints.Max.X = min(gtx.Constraints.Max.X, guikit.Dp(gtx, 620))
		gtx.Constraints.Min.X = gtx.Constraints.Max.X
		kids := []layout.FlexChild{
			layout.Rigid(u.Heading("Appearance", "Dark is Isotope's look. You can change it later in the Isotope menu.")),
			guikit.VGap(22),
		}
		switch {
		case errText != "":
			kids = append(kids, layout.Rigid(func(gtx layout.Context) layout.Dimensions { return u.Warning(gtx, errText) }), guikit.VGap(14))
		case busy:
			kids = append(kids, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return u.Label(gtx, guikit.StSmall, guikit.ColDim, "Applying…")
			}), guikit.VGap(14))
		}
		kids = append(kids, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return layout.Flex{}.Layout(gtx,
				layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
					return u.ChoiceCard(gtx, &p.darkC, dark, "Dark", "Easy on the eyes, the Isotope look")
				}),
				layout.Rigid(layout.Spacer{Width: 14}.Layout),
				layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
					return u.ChoiceCard(gtx, &p.light, !dark, "Light", lightNote)
				}))
		}))
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx, kids...)
	})
}
