package main

import (
	"fmt"
	"sync"

	"gioui.org/layout"
	"gioui.org/widget"

	"github.com/kairos-community/isotope/pkg/guikit"
)

func init() {
	pageMakers[PAccessibility] = func(u *UI) page {
		return &accessPage{basePage: basePage{u}, scale: 1, setScale: 1, scaleC: make([]widget.Clickable, len(Scales))}
	}
}

// accessNoteI3 is the scale's note on i3: Xft.dpi reaches X11 apps only
// when they start.
const accessNoteI3 = "Some X11 apps need a restart. i3 restarts at once to use it; your windows stay open."

// accessNoteOutputs is the note where the scale is set per screen (niri,
// COSMIC): a screen plugged in later is not covered.
const accessNoteOutputs = "Set for every screen connected now. A screen you plug in later keeps its own scale."

// accessNoteCosmicText replaces "Larger text" on COSMIC, where nothing
// but the scale makes text larger (see SetLargerText).
const accessNoteCosmicText = "To make text bigger, choose a larger scale."

// accessPage is the display scale and larger text. What is set now is read
// in the background, and Continue applies the choice there too, so the
// frame never waits on swaymsg, niri, xrdb or cosmic-randr.
type accessPage struct {
	basePage
	list widget.List

	started bool
	scaleC  []widget.Clickable
	largerC widget.Clickable

	mu       sync.Mutex
	wg       sync.WaitGroup
	scale    float64 // the chosen scale
	setScale float64 // the scale that is set now
	larger   bool
	setLg    bool   // larger text is set now
	picked   bool   // the user chose before the read came back
	busy     bool   // Continue is applying
	applied  bool   // it worked; Update goes on
	err      string // it failed
}

func (p *accessPage) start() {
	if p.started {
		return
	}
	p.started = true
	h, d := p.u.hooks, p.u.desktop
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		s := NearestScale(CurrentScale(h.Runner, d))
		lg := d != Cosmic && ReadLargerText(h.Runner)
		p.mu.Lock()
		p.setScale, p.setLg = s, lg
		if !p.picked {
			p.scale, p.larger = s, lg
		}
		p.mu.Unlock()
		p.invalidate()
	}()
}

func (p *accessPage) wait() { p.wg.Wait() }

func (p *accessPage) invalidate() {
	if p.u.hooks.Invalidate != nil {
		p.u.hooks.Invalidate()
	}
}

func (p *accessPage) chooseScale(s float64) {
	p.mu.Lock()
	p.scale, p.picked = s, true
	p.mu.Unlock()
}

func (p *accessPage) toggleLarger() {
	p.mu.Lock()
	p.larger, p.picked = !p.larger, true
	p.mu.Unlock()
}

func (p *accessPage) Update(gtx layout.Context) {
	p.start()
	for i := range p.scaleC {
		if p.scaleC[i].Clicked(gtx) {
			p.chooseScale(Scales[i])
		}
	}
	if p.largerC.Clicked(gtx) {
		p.toggleLarger()
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

// Continue applies what changed in the background and goes on when it
// worked. A scale that is already set is not set again (on i3 that would
// restart i3 for nothing). A failure stays on the page.
func (p *accessPage) Continue() {
	h, d := p.u.hooks, p.u.desktop
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
	s, setS, lg := p.scale, p.setScale, p.larger
	p.mu.Unlock()
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		msg := ""
		if s != setS {
			if err := ApplyScale(h.Runner, h.Home, d, s); err != nil {
				msg = "Could not change the display scale: " + err.Error()
			} else {
				// Set now: a Continue after a text failure must not set it
				// again (on i3, a second restart).
				p.mu.Lock()
				p.setScale = s
				p.mu.Unlock()
			}
		}
		if msg == "" {
			if err := SetLargerText(h.Runner, h.Home, d, lg); err != nil {
				msg = "Could not change the text size: " + err.Error()
			}
		}
		p.mu.Lock()
		p.busy = false
		if msg != "" {
			p.err = msg
		} else {
			p.applied, p.setScale, p.setLg = true, s, lg
		}
		p.mu.Unlock()
		p.invalidate()
	}()
}

func (p *accessPage) Body(gtx layout.Context) layout.Dimensions {
	u := p.u
	p.mu.Lock()
	scale, larger, busy, errText := p.scale, p.larger, p.busy, p.err
	p.mu.Unlock()
	var note string
	switch u.desktop {
	case I3:
		note = accessNoteI3
	case Niri, Cosmic:
		note = accessNoteOutputs
	}
	textSub := "Text in apps is a quarter larger. Apps follow at once."
	if u.desktop == I3 {
		textSub = "Text in GTK Flatpak apps is a quarter larger. For every app, choose a larger scale."
	}
	return u.scrolled(gtx, &p.list, func(gtx layout.Context) layout.Dimensions {
		gtx.Constraints.Max.X = min(gtx.Constraints.Max.X, guikit.Dp(gtx, 620))
		gtx.Constraints.Min.X = gtx.Constraints.Max.X
		kids := []layout.FlexChild{
			layout.Rigid(u.Heading("Accessibility", "Make the screen easier to read. Your choice applies now and stays for later logins.")),
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
		kids = append(kids,
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return u.Label(gtx, guikit.StBody600, guikit.ColText, "Display scale")
			}),
			guikit.VGap(10),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				row := make([]layout.FlexChild, 0, 2*len(Scales))
				for i, s := range Scales {
					if i > 0 {
						row = append(row, layout.Rigid(layout.Spacer{Width: 10}.Layout))
					}
					sub := ""
					if s == 1 {
						sub = "Default"
					}
					row = append(row, layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
						return u.ChoiceCard(gtx, &p.scaleC[i], scale == s, fmt.Sprintf("%d%%", int(s*100)), sub)
					}))
				}
				return layout.Flex{}.Layout(gtx, row...)
			}),
		)
		if note != "" {
			kids = append(kids, guikit.VGap(10), layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return u.Label(gtx, guikit.StSmall, guikit.ColDim, note)
			}))
		}
		if u.desktop == Cosmic {
			kids = append(kids, guikit.VGap(4), layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return u.Label(gtx, guikit.StSmall, guikit.ColDim, accessNoteCosmicText)
			}))
		}
		if u.desktop != Cosmic { // see SetLargerText
			kids = append(kids, guikit.VGap(22), layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return u.CheckCard(gtx, &p.largerC, larger, "Larger text", textSub)
			}))
		}
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx, kids...)
	})
}
