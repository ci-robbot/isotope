package main

import (
	"image"
	"image/color"
	"strings"
	"sync"

	"gioui.org/f32"
	"gioui.org/font"
	"gioui.org/io/event"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/unit"
	"gioui.org/widget"

	"github.com/kairos-community/isotope/pkg/guikit"
	"github.com/kairos-community/isotope/pkg/i18n"
	"github.com/kairos-community/isotope/pkg/installer"
)

func init() {
	pageMakers[PageLive] = func(u *UI) page {
		p := &livePage{basePage: basePage{u}}
		p.choices = u.hooks.Live
		return p
	}
}

// liveInstaller starts the installer on the live desktop (Task 8's script:
// it runs isotope-install-gui as root in a window).
const liveInstaller = "isotope-live-install"

// stCardTitle is the title of the live welcome's two large cards.
var stCardTitle = guikit.Style{Face: guikit.FaceTitle, Weight: font.SemiBold, Size: 22}

// livePage is the live welcome (spec section 4): the mark, "Welcome to
// Isotope" with the version and the variant, Install or Try it out, the
// help line, and the session's language and keyboard in the bar under it.
// It draws the whole window: one page has no steps to list.
type livePage struct {
	basePage
	list                     widget.List
	install, try             widget.Clickable
	languageBtn, keyboardBtn widget.Clickable

	loaded               bool
	locales              []Locale
	keyboardList         []installer.Keyboard
	languages, keyboards *chooser

	err string // the installer did not start

	// The session hooks run on one worker in the background, one at a
	// time, each with the latest choice: a quick second pick does not race
	// the first. mu guards these fields.
	mu              sync.Mutex
	wg              sync.WaitGroup
	choices         LiveChoices
	langDue, keyDue bool // a change the worker has not applied yet
	working         bool
	applyErr        string
}

func (p *livePage) firstFocus() event.Tag { return &p.install }

// load reads the two lists once: `locale -a`, as the Language page, and
// xkeyboard-config's layouts, as the installer.
func (p *livePage) load() {
	if p.loaded {
		return
	}
	p.loaded = true
	if r := p.u.hooks.Runner; r != nil {
		if l, err := Locales(r); err == nil {
			p.locales = l
		}
	}
	p.keyboardList = installer.LoadKeyboards(p.u.hooks.XKBRules)
	var langs, kbs []chooserEntry
	for _, l := range p.locales {
		langs = append(langs, chooserEntry{name: l.Name, code: l.Code, also: englishName(l.Code)})
	}
	for _, k := range p.keyboardList {
		kbs = append(kbs, chooserEntry{name: k.Description, code: k.Code()})
	}
	// The chooser's texts are translated where they are shown: the
	// language may change while the page is open.
	p.languages = newChooser(i18n.Mark("Language"), i18n.Mark("New apps use this language. The installer uses it too."), i18n.Mark("Search languages"), i18n.Mark("Use this language"), langs)
	p.keyboards = newChooser(i18n.Mark("Keyboard layout"), i18n.Mark("The layout changes at once. The installer uses it too."), i18n.Mark("Search layouts"), i18n.Mark("Use this layout"), kbs)
}

// wait blocks until the session hooks have run (tests).
func (p *livePage) wait() { p.wg.Wait() }

func (p *livePage) Footer(nav) (leading, trailing []guikit.Action) { return nil, nil }

// Continue: the page has no Continue; Install and Try it out end it.
func (p *livePage) Continue() {}

func (p *livePage) Update(gtx layout.Context) {
	p.load()
	if p.install.Clicked(gtx) {
		p.startInstall()
	}
	if p.try.Clicked(gtx) {
		p.u.close()
	}
	if p.languageBtn.Clicked(gtx) {
		p.openLanguages()
	}
	if p.keyboardBtn.Clicked(gtx) {
		p.openKeyboards()
	}
}

// startInstall starts the installer, detached, and closes the window. If
// it cannot start, the window stays and says why.
func (p *livePage) startInstall() {
	r := p.u.hooks.Runner
	if r == nil {
		p.err = i18n.T("The installer is not available.")
		return
	}
	proc, err := r.Start(liveInstaller)
	if err != nil {
		p.err = i18n.Tf("Could not start the installer: %v", err)
		return
	}
	p.err = ""
	go func() { _ = proc.Wait() }()
	p.u.close()
}

func (p *livePage) current() LiveChoices {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.choices
}

// openLanguages shows the language list over the window.
func (p *livePage) openLanguages() {
	p.load()
	p.open(p.languages, p.current().Lang, &p.languageBtn, func(e chooserEntry) {
		p.apply(func(c *LiveChoices) { c.Lang = e.code; p.langDue = true })
	})
}

// openKeyboards shows the keyboard list over the window.
func (p *livePage) openKeyboards() {
	p.load()
	c := p.current()
	p.open(p.keyboards, installer.Keyboard{Layout: c.Layout, Variant: c.Variant}.Code(), &p.keyboardBtn, func(e chooserEntry) {
		for _, k := range p.keyboardList {
			if k.Code() == e.code {
				p.apply(func(c *LiveChoices) { c.Layout, c.Variant = k.Layout, k.Variant; p.keyDue = true })
				return
			}
		}
	})
}

// open asks c over the window, with code picked; chosen runs on a pick.
// Closing it gives the focus back to the button that opened it.
func (p *livePage) open(c *chooser, code string, back *widget.Clickable, chosen func(chooserEntry)) {
	u := p.u
	c.reset(code)
	done := func() {
		u.question = nil
		u.focus = back
	}
	u.ask(&question{
		title: i18n.T(c.title),
		body:  i18n.T(c.body),
		form:  func(gtx layout.Context) layout.Dimensions { return c.form(gtx, u.Theme) },
		actions: []guikit.Action{
			{Click: &c.cancel, Kind: guikit.ButtonSecondary, Label: i18n.T("Cancel")},
			{Click: &c.use, Kind: guikit.ButtonPrimary, Label: i18n.T(c.useLabel)},
		},
		update: func(gtx layout.Context) {
			switch e, act := c.update(gtx); act {
			case chooserUse:
				done()
				chosen(e)
			case chooserCancel:
				done()
			}
		},
		esc: done,
	}, &c.search.Editor)
}

// apply records a change (under mu, marking what is due), writes the
// choices for the installer at once, and wakes the worker that applies
// them to the session.
func (p *livePage) apply(change func(*LiveChoices)) {
	p.mu.Lock()
	change(&p.choices)
	c := p.choices
	p.applyErr = ""
	start := !p.working
	p.working = true
	p.mu.Unlock()
	if path := p.u.hooks.LiveChoicesFile; path != "" {
		if err := WriteLiveChoices(path, c); err != nil {
			p.setApplyErr(i18n.Tf("Could not save the choice for the installer: %v", err))
		}
	}
	if start {
		p.wg.Add(1)
		go p.work()
	}
}

// work applies what is due, always with the latest choices, until nothing
// is due.
func (p *livePage) work() {
	defer p.wg.Done()
	h := p.u.hooks
	for {
		p.mu.Lock()
		c, lang, key := p.choices, p.langDue, p.keyDue
		p.langDue, p.keyDue = false, false
		if !lang && !key {
			p.working = false
			p.mu.Unlock()
			return
		}
		p.mu.Unlock()
		var err error
		if lang && h.SessionLocale != nil {
			err = h.SessionLocale(c.Lang)
		}
		if key && h.SessionKeymap != nil {
			if e := h.SessionKeymap(c.Layout, c.Variant); err == nil {
				err = e
			}
		}
		if err != nil {
			p.setApplyErr(i18n.Tf("Could not apply it to this session: %v", err))
		}
		if h.Invalidate != nil {
			h.Invalidate()
		}
	}
}

func (p *livePage) setApplyErr(s string) {
	p.mu.Lock()
	p.applyErr = s
	p.mu.Unlock()
}

// languageName and keyboardName are what the two buttons show.
func (p *livePage) languageName(c LiveChoices) string {
	if c.Lang == "" {
		return i18n.T("Not set")
	}
	return LocaleName(c.Lang)
}

func (p *livePage) keyboardName(c LiveChoices) string {
	code := installer.Keyboard{Layout: c.Layout, Variant: c.Variant}.Code()
	for _, k := range p.keyboardList {
		if k.Code() == code {
			return k.Description
		}
	}
	return code
}

func (p *livePage) Body(gtx layout.Context) layout.Dimensions { return p.Window(gtx) }

// Window is the whole window: the page above, the session bar under it.
func (p *livePage) Window(gtx layout.Context) layout.Dimensions {
	u := p.u
	gtx.Constraints.Min = gtx.Constraints.Max
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
			gtx.Constraints.Min = gtx.Constraints.Max
			return u.slide(p.main)(gtx)
		}),
		layout.Rigid(p.sessionBar),
	)
}

// main is the mark, the title, the two cards and the lines under them.
func (p *livePage) main(gtx layout.Context) layout.Dimensions {
	u := p.u
	p.mu.Lock()
	applyErr := p.applyErr
	p.mu.Unlock()
	help := LiveHelp(u.desktop, u.shortcuts)
	return u.scrolled(gtx, &p.list, func(gtx layout.Context) layout.Dimensions {
		// A short window (a large scale, a small screen) gets a smaller
		// mark, and a very short one none, so the cards stay in view.
		glow, mark := float32(132), float32(96)
		switch h := gtx.Constraints.Min.Y; {
		case h < guikit.Dp(gtx, 560):
			glow = 0
		case h < guikit.Dp(gtx, 720):
			glow, mark = 88, 64
		}
		return layout.Center.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			gtx.Constraints.Max.X = min(gtx.Constraints.Max.X, guikit.Dp(gtx, 700))
			kids := []layout.FlexChild{
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
				guikit.VGap(12),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return u.Label(gtx, guikit.StHero, guikit.ColText, i18n.T("Welcome to Isotope"))
				}),
				guikit.VGap(8),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return u.Label(gtx, guikit.StBody, guikit.ColDim, u.version)
				}),
				guikit.VGap(18),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					gtx.Constraints.Max.X = min(gtx.Constraints.Max.X, guikit.Dp(gtx, 520))
					return u.Centered(gtx, guikit.StLead, guikit.ColText2, 1.55,
						i18n.T("You are running Isotope from the USB stick. Nothing on this computer changes until you install it."))
				}),
				guikit.VGap(30),
				layout.Rigid(p.cards),
			}
			if p.err != "" || applyErr != "" {
				msg := p.err
				if msg == "" {
					msg = applyErr
				}
				kids = append(kids, guikit.VGap(16), layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return u.Warning(gtx, msg)
				}))
			}
			if len(help) > 0 {
				kids = append(kids, guikit.VGap(28), layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return p.helpLine(gtx, help)
				}))
			}
			kids = append(kids, guikit.VGap(14), layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return u.Centered(gtx, guikit.StSmall, guikit.ColDim, 1.4, i18n.T("Changes in this session are lost when you shut down."))
			}))
			return layout.Flex{Axis: layout.Vertical, Alignment: layout.Middle}.Layout(gtx, kids...)
		})
	})
}

// liveCard is one of the two large choices.
type liveCard struct {
	click     *widget.Clickable
	primary   bool
	title, by string
	icon      func(gtx layout.Context, size float32, c color.NRGBA)
}

// cards lays Install and Try it out side by side, the same height, or one
// above the other in a narrow window.
func (p *livePage) cards(gtx layout.Context) layout.Dimensions {
	cs := []liveCard{
		{&p.install, true, i18n.T("Install Isotope"), i18n.T("Put Isotope on this computer's disk. It takes a few minutes."), installIcon},
		{&p.try, false, i18n.T("Try it out"), tryText(p.u.desktop), tryIcon},
	}
	gap := guikit.Dp(gtx, 16-2*ringRoom)
	stacked := gtx.Constraints.Max.X < guikit.Dp(gtx, 520)
	w := (gtx.Constraints.Max.X - gap) / 2
	if stacked {
		w = gtx.Constraints.Max.X
	}
	// Measure both, then draw both at the taller height.
	h := 0
	for _, c := range cs {
		m := op.Record(gtx.Ops)
		cg := gtx
		iw := w - 2*guikit.Dp(gtx, ringRoom)
		cg.Constraints = layout.Constraints{Min: image.Pt(iw, 0), Max: image.Pt(iw, gtx.Constraints.Max.Y)}
		d := p.cardContent(cg, c)
		m.Stop()
		h = max(h, d.Size.Y+2*guikit.Dp(gtx, ringRoom))
	}
	draw := func(c liveCard) layout.Widget {
		return func(gtx layout.Context) layout.Dimensions {
			gtx.Constraints = layout.Exact(image.Pt(w, h))
			return p.card(gtx, c)
		}
	}
	if stacked {
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
			layout.Rigid(draw(cs[0])), layout.Rigid(layout.Spacer{Height: unit.Dp(12 - 2*ringRoom)}.Layout), layout.Rigid(draw(cs[1])))
	}
	return layout.Flex{}.Layout(gtx,
		layout.Rigid(draw(cs[0])), layout.Rigid(layout.Spacer{Width: unit.Dp(16 - 2*ringRoom)}.Layout), layout.Rigid(draw(cs[1])))
}

// ringRoom is the room around a card for its ring. guikit's Card paints
// the ring outside the box, where the Clickable's clip cuts it, so only
// its corners show; this page lays the box inside the clickable, inset by
// the widest ring, so the whole ring is drawn.
const ringRoom = 4

// card is guikit's Card (the same box, the selected look for the primary
// one, the focus ring), with the ring inside the clickable's area.
func (p *livePage) card(gtx layout.Context, c liveCard) layout.Dimensions {
	b := guikit.Box{Radius: 12, Fill: guikit.ColSidebar, Border: guikit.Over(guikit.ColSidebar, guikit.ColText, 0.08), BorderWidth: 1}
	if c.primary {
		b.Fill, b.Border, b.BorderWidth = guikit.Over(guikit.ColWindow, guikit.ColAccent, 0.08), guikit.ColAccent, 2
		b.Ring, b.RingWidth = guikit.Over(guikit.ColWindow, guikit.ColAccent, 0.12), 4
	}
	if gtx.Focused(c.click) {
		b.Ring, b.RingWidth = guikit.FocusRingStrong, 3
		if !c.primary {
			b.Border = guikit.Over(guikit.ColWindow, guikit.ColAccent, 0.6)
		}
	}
	return c.click.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		pointer.CursorPointer.Add(gtx.Ops)
		return layout.UniformInset(ringRoom).Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			return b.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				gtx.Constraints.Min = gtx.Constraints.Max
				return p.cardContent(gtx, c)
			})
		})
	})
}

// cardContent is a card's icon, title and line, padded, at the width it
// is given.
func (p *livePage) cardContent(gtx layout.Context, c liveCard) layout.Dimensions {
	u := p.u
	gtx.Constraints.Min.X = gtx.Constraints.Max.X
	return layout.UniformInset(22).Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		gtx.Constraints.Min.X = gtx.Constraints.Max.X
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				s := guikit.Dp(gtx, 44)
				tile, fg := guikit.Over(guikit.ColSidebar, guikit.ColText, 0.08), guikit.ColText2
				if c.primary {
					tile, fg = guikit.ColAccent, guikit.ColOnAccent
				}
				guikit.FillRRect(gtx, image.Rect(0, 0, s, s), guikit.Dp(gtx, 11), tile)
				o := op.Offset(guikit.Pt(gtx, 10, 10)).Push(gtx.Ops)
				c.icon(gtx, 24, fg)
				o.Pop()
				return layout.Dimensions{Size: image.Pt(s, s)}
			}),
			guikit.VGap(16),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return u.Label(gtx, stCardTitle, guikit.ColText, c.title)
			}),
			guikit.VGap(6),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return u.LabelLines(gtx, guikit.StBody, guikit.ColDim, 1.45, c.by)
			}),
		)
	})
}

// stroke draws the open path pts, in a 24x24 box scaled to size dp, with a
// line 2 units wide.
func stroke(gtx layout.Context, size float32, c color.NRGBA, pts ...f32.Point) {
	k := guikit.Fdp(gtx, size) / 24
	var path clip.Path
	path.Begin(gtx.Ops)
	for i, pt := range pts {
		q := f32.Pt(pt.X*k, pt.Y*k)
		if i == 0 {
			path.MoveTo(q)
		} else {
			path.LineTo(q)
		}
	}
	paint.FillShape(gtx.Ops, c, clip.Stroke{Path: path.End(), Width: 2 * k}.Op())
}

// installIcon is an arrow down into a tray: put it on the disk.
func installIcon(gtx layout.Context, size float32, col color.NRGBA) {
	stroke(gtx, size, col, f32.Pt(12, 3), f32.Pt(12, 14.5))
	stroke(gtx, size, col, f32.Pt(7, 9.5), f32.Pt(12, 14.5), f32.Pt(17, 9.5))
	stroke(gtx, size, col, f32.Pt(4, 15), f32.Pt(4, 20), f32.Pt(20, 20), f32.Pt(20, 15))
}

// tryIcon is a compass: look around.
func tryIcon(gtx layout.Context, size float32, fg color.NRGBA) {
	k := guikit.Fdp(gtx, size) / 24
	r := 9 * k
	ring := clip.Ellipse{Min: image.Pt(int(12*k-r), int(12*k-r)), Max: image.Pt(int(12*k+r+0.5), int(12*k+r+0.5))}
	paint.FillShape(gtx.Ops, fg, clip.Stroke{Path: ring.Path(gtx.Ops), Width: 2 * k}.Op())
	var needle clip.Path
	needle.Begin(gtx.Ops)
	for i, pt := range []f32.Point{{X: 16, Y: 8}, {X: 13.4, Y: 13.4}, {X: 8, Y: 16}, {X: 10.6, Y: 10.6}} {
		q := f32.Pt(pt.X*k, pt.Y*k)
		if i == 0 {
			needle.MoveTo(q)
		} else {
			needle.LineTo(q)
		}
	}
	needle.Close()
	paint.FillShape(gtx.Ops, fg, clip.Outline{Path: needle.End()}.Op())
}

// helpLine is "Wi-Fi [Super]+[N] · Apps [Super]+[D] · Help [Super]+[F1]".
func (p *livePage) helpLine(gtx layout.Context, items []HelpItem) layout.Dimensions {
	u := p.u
	var kids []layout.FlexChild
	for i, it := range items {
		if i > 0 {
			kids = append(kids, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return layout.Inset{Left: 14, Right: 14}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
					return u.Label(gtx, guikit.StSmall, guikit.ColRing, "·")
				})
			}))
		}
		kids = append(kids,
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				// Labels are marked in HelpItems; "Wi-Fi" stays as it is.
				return u.Label(gtx, guikit.StSmall500, guikit.ColText2, i18n.T(it.Label))
			}),
			layout.Rigid(layout.Spacer{Width: 8}.Layout),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				if it.Where != "" {
					return u.Label(gtx, guikit.StSmall, guikit.ColDim, i18n.T(it.Where))
				}
				return u.KeyCaps(gtx, it.Keys)
			}),
		)
	}
	return layout.Flex{Alignment: layout.Middle}.Layout(gtx, kids...)
}

// sessionBar is the bar at the bottom: the session's language and
// keyboard, each a button that opens its list.
func (p *livePage) sessionBar(gtx layout.Context) layout.Dimensions {
	u := p.u
	c := p.current()
	field := func(label string, click *widget.Clickable, value string) []layout.FlexChild {
		return []layout.FlexChild{
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return u.Label(gtx, guikit.StLabel, guikit.ColText2, label)
			}),
			layout.Rigid(layout.Spacer{Width: 10}.Layout),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions { return p.selectBox(gtx, click, value) }),
		}
	}
	return u.Footer(gtx, nil, func(gtx layout.Context) layout.Dimensions {
		kids := field(i18n.T("Language"), &p.languageBtn, p.languageName(c))
		kids = append(kids, layout.Rigid(layout.Spacer{Width: 28}.Layout))
		kids = append(kids, field(i18n.T("Keyboard"), &p.keyboardBtn, p.keyboardName(c))...)
		return layout.Flex{Alignment: layout.Middle}.Layout(gtx, kids...)
	}, nil)
}

// selectBox is a button that shows a value and opens a list: the value
// and a chevron in a field's box.
func (p *livePage) selectBox(gtx layout.Context, c *widget.Clickable, value string) layout.Dimensions {
	u := p.u
	b := guikit.Box{Radius: 8, Fill: guikit.ColField, Border: guikit.Over(guikit.ColFooter, guikit.ColText, 0.1), BorderWidth: 1}
	if gtx.Focused(c) {
		b.Ring, b.RingWidth = guikit.FocusRingStrong, 3
	}
	return c.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		pointer.CursorPointer.Add(gtx.Ops)
		gtx.Constraints.Min = image.Pt(0, guikit.Dp(gtx, 40))
		return b.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			return layout.Inset{Left: 14, Right: 12}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				gtx.Constraints.Min.X = 0
				return layout.Center.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
					return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
						layout.Rigid(func(gtx layout.Context) layout.Dimensions {
							gtx.Constraints.Max.X = min(gtx.Constraints.Max.X, guikit.Dp(gtx, 230))
							return u.LabelMax(gtx, guikit.StButton500, guikit.ColText, 1, value)
						}),
						layout.Rigid(layout.Spacer{Width: 12}.Layout),
						layout.Rigid(func(gtx layout.Context) layout.Dimensions {
							stroke(gtx, 14, guikit.ColDim, f32.Pt(6, 9), f32.Pt(12, 15), f32.Pt(18, 9))
							return layout.Dimensions{Size: guikit.Pt(gtx, 14, 14)}
						}),
					)
				})
			})
		})
	})
}

// chooserEntry is one entry of a chooser: its name, its code (what is
// applied, drawn on the right) and other words the search matches.
type chooserEntry struct{ name, code, also string }

// chooser is a searchable list over the window, the Language page's search
// and list: the language or the keyboard of the live session.
type chooser struct {
	title, body, searchLabel, useLabel string

	entries []chooserEntry
	search  *guikit.Field
	list    widget.List
	rows    []guikit.Row
	query   string
	shown   []int
	sel     int

	cancel, use widget.Clickable
}

func newChooser(title, body, searchLabel, useLabel string, entries []chooserEntry) *chooser {
	c := &chooser{title: title, body: body, searchLabel: searchLabel, useLabel: useLabel,
		entries: entries, search: guikit.NewField(true), rows: make([]guikit.Row, len(entries)), sel: -1}
	c.list.Axis = layout.Vertical
	return c
}

// reset clears the search and picks code, scrolled into view.
func (c *chooser) reset(code string) {
	c.search.Editor.SetText("")
	c.filter()
	c.sel = -1
	for i, e := range c.entries {
		if e.code == code || (strings.Contains(e.code, ".") && sameLocale(e.code, code)) {
			c.sel = i
		}
	}
	c.list.Position.First, c.list.Position.Offset = 0, 0
	for n, i := range c.shown {
		if i == c.sel {
			c.list.Position.First = max(0, n-2)
		}
	}
}

// filter fills shown with the entries the search text is in.
func (c *chooser) filter() {
	q := strings.ToLower(strings.TrimSpace(c.search.Editor.Text()))
	c.query = c.search.Editor.Text()
	c.shown = c.shown[:0]
	for i, e := range c.entries {
		if q == "" || strings.Contains(strings.ToLower(e.name), q) || strings.Contains(strings.ToLower(e.code), q) ||
			(e.also != "" && strings.Contains(strings.ToLower(e.also), q)) {
			c.shown = append(c.shown, i)
		}
	}
}

// What a frame of the chooser did.
const (
	chooserNone = iota
	chooserUse
	chooserCancel
)

// update handles the search, the arrows, the rows and the buttons. With
// chooserUse it returns the picked entry.
func (c *chooser) update(gtx layout.Context) (chooserEntry, int) {
	submitted := c.search.Submitted(gtx)
	if c.search.Editor.Text() != c.query {
		// As the Language page: the pick is the first match, or none.
		c.filter()
		c.sel = -1
		if len(c.shown) > 0 {
			c.sel = c.shown[0]
		}
		c.list.Position.First, c.list.Position.Offset = 0, 0
	}
	for {
		down, ok := guikit.ArrowKey(gtx, &c.search.Editor)
		if !ok {
			break
		}
		c.sel = guikit.MoveSelection(c.shown, c.sel, down, &c.list)
	}
	for _, i := range c.shown {
		if c.rows[i].Clicked(gtx) {
			c.sel = i
		}
	}
	if c.cancel.Clicked(gtx) {
		return chooserEntry{}, chooserCancel
	}
	if (c.use.Clicked(gtx) || submitted) && c.sel >= 0 && c.sel < len(c.entries) {
		return c.entries[c.sel], chooserUse
	}
	return chooserEntry{}, chooserNone
}

// form is the search above the list, the list a fixed height.
func (c *chooser) form(gtx layout.Context, th *guikit.Theme) layout.Dimensions {
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return th.Labelled(gtx, i18n.T(c.searchLabel), nil, "", func(gtx layout.Context) layout.Dimensions {
				return th.Input(gtx, c.search, guikit.StBody, 38, "", nil)
			})
		}),
		guikit.VGap(10),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			h := min(guikit.Dp(gtx, 280), max(guikit.Dp(gtx, 120), gtx.Constraints.Max.Y/3))
			gtx.Constraints = layout.Exact(image.Pt(gtx.Constraints.Max.X, h))
			return th.PickList(gtx, &c.list, c.shown, c.sel, c.rows, func(i int) (string, string) {
				return c.entries[i].name, c.entries[i].code
			})
		}),
	)
}
