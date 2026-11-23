package main

import (
	"errors"
	"strings"
	"sync"

	"gioui.org/layout"
	"gioui.org/widget"

	"github.com/kairos-community/isotope/guikit"
)

func init() {
	pageMakers[PLanguage] = func(u *UI) page {
		p := &languagePage{basePage: basePage{u}, search: guikit.NewField(true), sel: -1}
		p.list.Axis = layout.Vertical
		return p
	}
}

// askAnAdmin is the page's words for a user who may not change the language.
const askAnAdmin = "Ask an administrator to change the language"

// languagePage is a searchable list of the system's UTF-8 locales. The
// language is the system's, for every user; isotope-updated sets it, under
// the settings action. A user who is not an administrator sees the current
// language and the way to change it, read-only. Continue applies the pick
// in the background.
type languagePage struct {
	basePage
	list   widget.List // the locales
	scroll widget.List // the read-only and error faces

	started bool
	locales []Locale
	loadErr string

	mu       sync.Mutex // SetLocale runs in the background and writes under it
	wg       sync.WaitGroup
	busy     bool   // SetLocale is running
	applied  string // the code SetLocale set; Update goes on to the next page
	applyErr string // SetLocale failed
	refused  bool   // the daemon said no: read-only from now on

	search *guikit.Field
	query  string // the search text the list was last filtered with
	shown  []int  // indices into locales, in order
	sel    int    // index into locales, -1 for none
	rows   []guikit.Row
	retry  widget.Clickable
}

// load runs `locale -a`. It blocks, on purpose: it is one quick command, and
// the page would draw an empty list for a frame otherwise.
func (p *languagePage) load() {
	var list []Locale
	var err error
	if r := p.u.hooks.Runner; r != nil {
		list, err = Locales(r)
	}
	if err != nil {
		p.loadErr = "Could not list the languages: " + err.Error()
		return
	}
	p.loadErr = ""
	p.locales = list
	p.rows = make([]guikit.Row, len(list))
	p.sel = -1
	for i, l := range list {
		if sameLocale(l.Code, p.u.hooks.SystemLocale) {
			p.sel = i
		}
	}
	p.filter()
	for n, i := range p.shown {
		if i == p.sel {
			p.list.Position.First = n
		}
	}
}

// sameLocale compares two locale names, whatever the spelling of the
// encoding (.UTF-8 or .utf8).
func sameLocale(a, b string) bool {
	norm := func(s string) string {
		return strings.ReplaceAll(strings.ToLower(s), "utf-8", "utf8")
	}
	return a != "" && norm(a) == norm(b)
}

// filter fills shown with the locales whose name or code has the search
// text in it.
func (p *languagePage) filter() {
	q := strings.ToLower(strings.TrimSpace(p.search.Editor.Text()))
	p.query = p.search.Editor.Text()
	p.shown = p.shown[:0]
	for i, l := range p.locales {
		if q == "" || strings.Contains(strings.ToLower(l.Name), q) || strings.Contains(strings.ToLower(l.Code), q) ||
			strings.Contains(strings.ToLower(englishName(l.Code)), q) {
			p.shown = append(p.shown, i)
		}
	}
}

// readOnly: the user may not change the language, by group or because the
// daemon refused.
func (p *languagePage) readOnly() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.readOnlyLocked()
}

func (p *languagePage) readOnlyLocked() bool {
	if p.u.hooks.LiveSession {
		return p.u.hooks.SessionLocale == nil || p.refused
	}
	return !p.u.hooks.Admin || p.u.hooks.Settings == nil || p.refused
}

// wait blocks until the background call has ended (tests).
func (p *languagePage) wait() { p.wg.Wait() }

// notice is the line under a read-only page's current language.
func (p *languagePage) notice() string { return askAnAdmin }

func (p *languagePage) Update(gtx layout.Context) {
	u := p.u
	if !p.started {
		p.started = true
		p.load()
		if !p.readOnly() {
			u.focus = &p.search.Editor
		}
	}
	p.settle()
	if p.retry.Clicked(gtx) {
		p.load()
	}
	if p.readOnly() {
		return
	}
	submitted := p.search.Submitted(gtx)
	p.refilter()
	for {
		down, ok := guikit.ArrowKey(gtx, &p.search.Editor)
		if !ok {
			break
		}
		p.sel = guikit.MoveSelection(p.shown, p.sel, down, &p.list)
	}
	for _, i := range p.shown {
		if p.rows[i].Clicked(gtx) {
			p.sel = i
		}
	}
	if submitted && p.sel >= 0 {
		p.Continue()
	}
}

// refilter filters again when the search text changed. The pick is the first
// match; with no match there is no pick, so Continue cannot apply a locale
// the list does not show.
func (p *languagePage) refilter() {
	if p.search.Editor.Text() == p.query {
		return
	}
	p.filter()
	p.sel = -1
	if len(p.shown) > 0 {
		p.sel = p.shown[0]
	}
	p.list.Position.First, p.list.Position.Offset = 0, 0
}

// holding: SetLocale runs. Skip and Go back wait for it, so Ready says
// what the call did (settle runs only while the page is current).
func (p *languagePage) holding() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.busy
}

func (p *languagePage) Footer(n nav) (leading, trailing []guikit.Action) {
	held := p.holding()
	if n.Back != nil {
		b := *n.Back
		b.Disabled = held
		leading = append(leading, b)
	}
	s := *n.Skip
	s.Disabled = held
	return leading, []guikit.Action{s, *n.Continue}
}

// pick is the locale chosen in the list, if it differs from the system's.
func (p *languagePage) pick() (Locale, bool) {
	if p.sel < 0 || p.sel >= len(p.locales) {
		return Locale{}, false
	}
	l := p.locales[p.sel]
	return l, !sameLocale(l.Code, p.u.hooks.SystemLocale)
}

// Continue sets the picked language in the background and goes on when the
// daemon has accepted it. With nothing new picked, or read-only, it just
// goes on, and the page counts as skipped, not applied.
func (p *languagePage) Continue() {
	l, changed := p.pick()
	if !changed || p.readOnly() {
		p.u.flow.Next()
		return
	}
	p.mu.Lock()
	if p.busy {
		p.mu.Unlock()
		return
	}
	p.busy, p.applyErr = true, ""
	p.mu.Unlock()
	set := p.setter()
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		err := set(l.Code)
		p.mu.Lock()
		p.busy = false
		switch {
		case err == nil:
			p.applied = l.Code
		case errors.Is(err, ErrNotAuthorized):
			p.refused = true
		default:
			p.applyErr = err.Error()
		}
		p.mu.Unlock()
		p.invalidate()
	}()
}

// setter is how the pick is applied: isotope-updated's SetLocale, or on
// the live desktop the session's LANG and the choices file the installer
// reads (nothing reaches /oem there).
func (p *languagePage) setter() func(string) error {
	h := p.u.hooks
	if !h.LiveSession {
		return h.Settings.SetLocale
	}
	return func(code string) error {
		if err := h.SessionLocale(code); err != nil {
			return err
		}
		if h.LiveChoicesFile == "" {
			return nil
		}
		return UpdateLiveChoices(h.LiveChoicesFile, h.Live, func(c *LiveChoices) { c.Lang = code })
	}
}

// settle takes the result of the background call: the pick was accepted,
// so Ready can say what changed, and the flow goes on.
func (p *languagePage) settle() {
	p.mu.Lock()
	code := p.applied
	p.applied = ""
	p.mu.Unlock()
	if code == "" {
		return
	}
	u := p.u
	u.flow.Outcome.LanguageChanged = LocaleName(code)
	u.hooks.SystemLocale = code
	u.flow.Apply()
	u.flow.Next()
}

func (p *languagePage) invalidate() {
	if p.u.hooks.Invalidate != nil {
		p.u.hooks.Invalidate()
	}
}

func (p *languagePage) Body(gtx layout.Context) layout.Dimensions {
	u := p.u
	p.mu.Lock()
	busy, applyErr := p.busy, p.applyErr
	p.mu.Unlock()
	col := func(w layout.Widget) layout.Widget {
		return func(gtx layout.Context) layout.Dimensions {
			gtx.Constraints.Max.X = min(gtx.Constraints.Max.X, guikit.Dp(gtx, 580))
			gtx.Constraints.Min.X = gtx.Constraints.Max.X
			return w(gtx)
		}
	}
	switch {
	case p.loadErr != "":
		return u.scrolled(gtx, &p.scroll, func(gtx layout.Context) layout.Dimensions {
			return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
				layout.Rigid(u.Heading("Language", "Isotope could not list the languages.")),
				guikit.VGap(22),
				layout.Rigid(col(func(gtx layout.Context) layout.Dimensions { return u.Warning(gtx, p.loadErr) })),
				guikit.VGap(16),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return u.Button(gtx, &p.retry, guikit.ButtonSecondary, "Try again", true)
				}))
		})
	case p.readOnly():
		name := LocaleName(u.hooks.SystemLocale)
		if u.hooks.SystemLocale == "" {
			name = "Not set"
		}
		return u.scrolled(gtx, &p.scroll, func(gtx layout.Context) layout.Dimensions {
			return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
				layout.Rigid(u.Heading("Language", "The system language applies to every user of this computer.")),
				guikit.VGap(22),
				layout.Rigid(col(func(gtx layout.Context) layout.Dimensions {
					return guikit.Panel(gtx, 10, layout.Inset{Top: 14, Bottom: 14, Left: 16, Right: 16}, func(gtx layout.Context) layout.Dimensions {
						return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
							layout.Rigid(func(gtx layout.Context) layout.Dimensions {
								return u.Label(gtx, guikit.StSmall, guikit.ColDim, "Current language")
							}),
							guikit.VGap(4),
							layout.Rigid(func(gtx layout.Context) layout.Dimensions {
								return u.Label(gtx, guikit.StCard, guikit.ColText, name)
							}))
					})
				})),
				guikit.VGap(14),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return u.Label(gtx, guikit.StBody, guikit.ColDim, p.notice())
				}))
		})
	}
	return guikit.Pad(gtx, func(gtx layout.Context) layout.Dimensions {
		kids := []layout.FlexChild{
			layout.Rigid(u.Heading("Language", "The system language applies to every user of this computer. You can change it later in the Isotope menu.")),
			guikit.VGap(16),
			layout.Rigid(col(func(gtx layout.Context) layout.Dimensions {
				return u.Labelled(gtx, "Search languages", nil, "", func(gtx layout.Context) layout.Dimensions {
					return u.Input(gtx, p.search, guikit.StBody, 38, "", nil)
				})
			})),
			guikit.VGap(10),
		}
		switch {
		case applyErr != "":
			kids = append(kids, layout.Rigid(col(func(gtx layout.Context) layout.Dimensions { return u.Warning(gtx, applyErr) })), guikit.VGap(10))
		case busy:
			kids = append(kids, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return u.Label(gtx, guikit.StSmall, guikit.ColDim, "Setting the language…")
			}), guikit.VGap(10))
		}
		kids = append(kids, layout.Flexed(1, col(func(gtx layout.Context) layout.Dimensions {
			return u.PickList(gtx, &p.list, p.shown, p.sel, p.rows, func(i int) (string, string) {
				return p.locales[i].Name, p.locales[i].Code
			})
		})))
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx, kids...)
	})
}
