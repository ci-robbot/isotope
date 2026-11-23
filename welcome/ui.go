package main

import (
	"fmt"
	"image"
	"time"

	"gioui.org/io/event"
	"gioui.org/io/key"
	"gioui.org/io/system"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/text"
	"gioui.org/widget"

	"github.com/kairos-community/isotope/guikit"
)

// Hooks are every side effect the UI triggers. main wires them to the
// session; the tests pass fakes.
type Hooks struct {
	// Invalidate asks for a frame from another goroutine.
	Invalidate func()
	// Logout ends the desktop session.
	Logout func() error
	// OpenShortcutSheet opens the full sheet, as Super+F1 does.
	OpenShortcutSheet func() error
	// WriteStamp records how the flow ended. The UI calls it once.
	WriteStamp func(Stamp)
	// Window runs a window action (minimize, maximize, close, ...) the
	// title bar asked for. Nil with no title bar.
	Window func(system.Action)
	// Close asks the window to close. The request comes back through
	// CloseRequested, which lets it through once the flow has ended.
	Close func()
	// Runner runs the programs of the pages (flatpak, nm-online). Nil
	// leaves the Apps page with nothing to install.
	Runner Runner
	// Network is NetworkManager for the Network page. Nil shows the page
	// as having no network device.
	Network Network
	// Settings changes machine settings through isotope-updated (the
	// Language page). Nil makes the page read-only.
	Settings Settings
	// Admin says the user may change them (member of the admin group). The
	// daemon decides again on every call; this only lets the page start
	// read-only instead of failing on Continue.
	Admin bool
	// SystemLocale is the LANG of /etc/locale.conf, "" when it has none.
	SystemLocale string
	// Home is the user's home directory, where the Appearance page writes
	// the GTK and COSMIC settings.
	Home string
	// LayoutsDir is where the COSMIC panel layouts are (CosmicLayoutsDir).
	LayoutsDir string
	// AppInstalled is called, from a background goroutine, for each app
	// that installed: main pins a browser to the COSMIC dock.
	AppInstalled func(id string)
	// Backup is the backup methods of isotope-updated, for "Restore from a
	// backup". Nil offers no restore.
	Backup Backup
	// USBDisks lists the USB disks a backup may be on (udisks2). Nil lists
	// none.
	USBDisks func() ([]USBDisk, error)

	// The live welcome. Live is the session's language and keyboard when
	// it opens; LiveChoicesFile is where each change is written for the
	// installer ("" writes nothing). SessionLocale and SessionKeymap apply
	// a change to the running session; nil applies nothing. XKBRules is
	// the keyboard list (XKBRulesList).
	Live            LiveChoices
	LiveChoicesFile string
	SessionLocale   func(lang string) error
	SessionKeymap   func(layout, variant string) error
	XKBRules        string
	// LiveSession is set on the live desktop: the Language page then sets
	// the session's language (SessionLocale) and the choices file, never
	// the machine's through isotope-updated, which writes /oem.
	LiveSession bool
}

// page is one screen of the flow. Each lives in its own page_*.go file and
// registers its constructor in pageMakers from an init function; a page
// with no constructor yet is a stubPage. Embed basePage for the usual
// footer and Continue.
type page interface {
	// Body draws the page above the footer, in the whole column. Use
	// u.scrolled so a large scale scrolls instead of cutting it.
	Body(gtx layout.Context) layout.Dimensions
	// Footer is the footer's buttons, left and right, made from the
	// buttons the UI owns.
	Footer(n nav) (leading, trailing []guikit.Action)
	// Update handles the page's own input before it is drawn.
	Update(gtx layout.Context)
	// Continue is the Continue button, and Enter while it has the focus.
	// It moves the flow on; the UI finishes when the flow is done.
	Continue()
}

// nav are the footer buttons the UI owns, already labelled. Back is nil on
// the first page.
type nav struct {
	Back, Skip, Continue *guikit.Action
}

// pageMakers builds the page for an ID; each page file adds itself.
var pageMakers = map[PageID]func(u *UI) page{}

// UI is the Welcome window: the flow, the page on screen and its frame.
type UI struct {
	*guikit.Theme
	flow *Flow
	// setupFlow is the flow Welcome started with; restoreFlow is
	// "Restore from a backup", made when it is chosen. flow is one of them.
	setupFlow, restoreFlow *Flow
	// restore is the state of the restore steps, made with restoreFlow.
	restore *restoreState
	// question is a page's question over the window (the restore's
	// confirmation), nil when none is shown.
	question *question
	desktop  Desktop
	hooks    Hooks
	now      func() time.Time

	// version is the sidebar's note, "Isotope v0.0.25 · sway".
	version string
	// shortcuts is the variant's sheet (ShortcutsFile).
	shortcuts []Shortcut
	// appsPending is set when Apps was skipped for being offline: the
	// stamp then asks the next login to remind the user.
	appsPending bool
	// appList is the curated apps (AppListFile); installer is the batch
	// Continue on the Apps page started, if it did.
	appList   []App
	installer *Installer
	// batches counts the batches started; appsPrior is what the earlier
	// ones installed (the Outcome's names).
	batches   int
	appsPrior []string

	pages  map[PageID]page
	shown  PageID    // the page drawn last frame
	pageAt time.Time // when it came in, for the slide
	focus  event.Tag // focus this on the next frame
	scale  float32   // Ctrl+Plus and Ctrl+Minus
	// bar is the title bar Welcome draws when the compositor draws none
	// (COSMIC); nil when the window manager has its own.
	bar *guikit.TitleBar

	// stamped: the stamp is written; closing: the UI asked to close.
	stamped, closing bool
	// confirming: "Skip setup?" is shown over the page.
	confirming bool

	back, skip, cont widget.Clickable
	skipYes, skipNo  widget.Clickable
}

func newUI(flow *Flow, d Desktop, sh *text.Shaper, h Hooks) *UI {
	u := &UI{
		Theme:   &guikit.Theme{Shaper: sh},
		flow:    flow,
		desktop: d,
		hooks:   h,
		now:     time.Now,
		version: "Isotope",
		pages:   map[PageID]page{},
		scale:   1,
	}
	u.setupFlow = flow
	u.focus = &u.cont
	return u
}

// page is the current page, made on first use.
func (u *UI) page() page {
	id := u.flow.Current()
	p, ok := u.pages[id]
	if !ok {
		if mk, ok := pageMakers[id]; ok {
			p = mk(u)
		} else {
			p = &stubPage{basePage{u}, id}
		}
		u.pages[id] = p
	}
	return p
}

// Layout draws one frame over the whole OS window.
func (u *UI) Layout(gtx layout.Context) layout.Dimensions {
	gtx = guikit.Scaled(gtx, u.scale)
	u.syncApps()
	event.Op(gtx.Ops, u)
	guikit.WindowKeys(gtx, &u.scale, u.prev)
	if u.confirming {
		if u.skipYes.Clicked(gtx) {
			u.answerSkip(true)
		}
		if u.skipNo.Clicked(gtx) {
			u.answerSkip(false)
		}
	} else if q := u.question; q != nil {
		q.update(gtx)
	} else {
		u.update(gtx)
	}
	if cur := u.flow.Current(); cur != u.shown {
		// A new page slides in; the first one is simply there.
		if u.shown != "" {
			u.pageAt = u.now()
		}
		u.shown = cur
		if !u.confirming && u.question == nil {
			u.focus = &u.cont
			if f, ok := u.page().(focuser); ok {
				u.focus = f.firstFocus()
			}
		}
	}
	if u.focus != nil {
		gtx.Execute(key.FocusCmd{Tag: u.focus})
		u.focus = nil
	}

	size := gtx.Constraints.Max
	guikit.FillRect(gtx, image.Rectangle{Max: size}, guikit.ColWindow)
	gtx.Constraints = layout.Exact(size)
	// The bar is part of the window under the "Skip setup?" modal and a
	// page's question, so the modal's scrim covers it too: it is dimmed, and
	// neither its buttons nor its drag area get the pointer. Its Update still runs, to drain the
	// events, but a request that came with the modal up is dropped.
	full := gtx.Constraints
	content := func(gtx layout.Context) {
		p := u.page()
		if w, ok := p.(windowPage); ok {
			w.Window(gtx)
			return
		}
		u.Frame(gtx, u.steps(), u.slide(p.Body), func(gtx layout.Context) layout.Dimensions {
			lead, trail := p.Footer(u.nav())
			return u.Footer(gtx, lead, nil, trail)
		})
	}
	if u.bar != nil {
		acts := u.bar.Update(gtx)
		if acts != 0 && !u.confirming && u.question == nil && u.hooks.Window != nil {
			u.hooks.Window(acts)
		}
		h := u.bar.Layout(gtx, u.Theme).Size.Y
		st := op.Offset(image.Pt(0, h)).Push(gtx.Ops)
		size.Y -= h
		gtx.Constraints = layout.Exact(size)
		content(gtx)
		st.Pop()
	} else {
		content(gtx)
	}
	if q := u.question; q != nil {
		gtx.Constraints = full
		u.ConfirmForm(gtx, q.title, q.body, q.form, q.actions)
	}
	if u.confirming {
		gtx.Constraints = full
		u.Confirm(gtx, "Skip setup?",
			"Isotope keeps the settings it has. You can open this window again from the Isotope menu (Super+Esc → Welcome).",
			[]guikit.Action{
				{Click: &u.skipNo, Kind: guikit.ButtonSecondary, Label: "Keep going"},
				{Click: &u.skipYes, Kind: guikit.ButtonPrimary, Label: "Skip setup"},
			})
	}
	if !u.pageAt.IsZero() && u.now().Sub(u.pageAt) < guikit.SlideTime {
		gtx.Execute(op.InvalidateCmd{})
	}
	return layout.Dimensions{Size: full.Max}
}

// update handles the footer's buttons, then the page's own input.
func (u *UI) update(gtx layout.Context) {
	if u.back.Clicked(gtx) {
		u.prev()
	}
	if u.skip.Clicked(gtx) {
		u.skipPage()
	}
	if u.cont.Clicked(gtx) {
		u.next()
	}
	u.page().Update(gtx)
}

// steps is the sidebar: the pages of the flow, the current one lit.
func (u *UI) steps() guikit.Steps {
	var names []string
	for _, p := range u.flow.Pages() {
		name := PageTitle(p)
		if p == PApps && u.installing() {
			done, total, _ := u.installer.Progress()
			name = fmt.Sprintf("%s %d/%d", name, min(done+1, total), total)
		}
		names = append(names, name)
	}
	return guikit.Steps{Title: "Isotope", Names: names, Current: u.flow.Index(), Note: u.version}
}

// slide is w sliding in as the page changes.
func (u *UI) slide(w layout.Widget) layout.Widget {
	return func(gtx layout.Context) layout.Dimensions { return guikit.Slide(gtx, u.pageAt, u.now(), w) }
}

// nav are the footer buttons for the current page.
func (u *UI) nav() nav {
	n := nav{
		Skip:     &guikit.Action{Click: &u.skip, Kind: guikit.ButtonGhost, Label: "Skip"},
		Continue: &guikit.Action{Click: &u.cont, Kind: guikit.ButtonPrimary, Label: "Continue"},
	}
	if u.flow.Index() == len(u.flow.Pages())-1 {
		n.Continue.Label = "Done"
	}
	if u.flow.Index() > 0 {
		n.Back = &guikit.Action{Click: &u.back, Kind: guikit.ButtonSecondary, Label: "Go back"}
	}
	return n
}

// next is Continue.
func (u *UI) next() {
	u.page().Continue()
	if u.flow.Done() {
		u.finish(u.endStamp())
	}
}

// prev is Go back and Esc. Esc on the question is Keep going.
func (u *UI) prev() {
	if u.confirming {
		u.answerSkip(false)
		return
	}
	if q := u.question; q != nil {
		q.esc()
		return
	}
	if u.held() {
		return
	}
	if b, ok := u.page().(backer); ok {
		b.goBack()
		return
	}
	u.flow.Back()
}

// backer is a page with its own Go back (the restore steps, which drop
// the secrets they kept).
type backer interface{ goBack() }

// windowPage is a page that draws the whole window itself, with no step
// list or footer of the flow (the live welcome).
type windowPage interface {
	Window(gtx layout.Context) layout.Dimensions
}

// focuser is a page whose first focus is not Continue.
type focuser interface{ firstFocus() event.Tag }

// question is a page's question over the window: Esc runs esc, and
// update handles its buttons while it is shown. form, when set, is drawn
// between the body and the buttons (a search and a list).
type question struct {
	title, body string
	form        layout.Widget
	actions     []guikit.Action
	update      func(gtx layout.Context)
	esc         func()
}

// ask shows q and gives the focus to focus.
func (u *UI) ask(q *question, focus event.Tag) {
	u.question = q
	u.focus = focus
	if u.hooks.Invalidate != nil {
		u.hooks.Invalidate()
	}
}

// holder is a page that holds the footer's Skip and Go back for now (the
// Language page while it sets the language).
type holder interface{ holding() bool }

// held: the current page holds Skip and Go back.
func (u *UI) held() bool {
	h, ok := u.page().(holder)
	return ok && h.holding()
}

// skipPage is Skip: the page's setting is left as it was.
func (u *UI) skipPage() {
	if u.held() {
		return
	}
	// Apps skipped for lack of a network, or before the network check
	// answered (it counts as offline): the next login reminds. A page
	// opened alone writes no stamp, so it leaves no reminder either.
	if ap, ok := u.page().(*appsPage); ok && (ap.unavailable() || ap.checking()) && len(u.flow.Pages()) > 1 {
		u.appsPending = true
	}
	u.flow.Skip()
	if u.flow.Done() {
		u.finish(u.endStamp())
	}
}

// goBackTo goes back to an earlier page p, if the flow has it.
func (u *UI) goBackTo(p PageID) {
	for u.flow.Current() != p && u.flow.Index() > 0 {
		u.flow.Back()
	}
}

// endStamp is the stamp of a flow that reached its end.
func (u *UI) endStamp() Stamp {
	if u.appsPending {
		return StampAppsPending
	}
	return StampDone
}

// skipStamp is the stamp of setup skipped or the window closed before the
// end. It asks the next login to remind about the apps when they are not
// done: Apps was skipped offline, the network check had not answered yet,
// or a batch is still installing (logging out would kill it).
func (u *UI) skipStamp() Stamp {
	if len(u.flow.Pages()) > 1 && (u.appsPending || u.installing() || u.appsChecking()) {
		return StampAppsPending
	}
	return StampSkipped
}

// appsChecking: the Apps page is still asking whether there is a network.
func (u *UI) appsChecking() bool {
	ap, ok := u.pages[PApps].(*appsPage)
	return ok && ap.checking()
}

// stamp writes the stamp s, once.
func (u *UI) stamp(s Stamp) {
	// A restore that started ends with the restart: the restored homes
	// have their own stamps, and a restore that fails leaves the machine
	// new, so Welcome offers it again at the next login.
	// The live welcome writes none: after the install, the first login
	// still runs the setup.
	if u.stamped || u.restoreStarted() || u.live() {
		return
	}
	u.stamped = true
	if u.hooks.WriteStamp != nil {
		u.hooks.WriteStamp(s)
	}
}

// live: the window is the live welcome, which writes no stamp, so the
// first login after the install still runs the real Welcome.
func (u *UI) live() bool {
	p := u.setupFlow.Pages()
	return len(p) == 1 && p[0] == PageLive
}

// close asks the window to close; CloseRequested lets it through.
func (u *UI) close() {
	u.closing = true
	if u.hooks.Close != nil {
		u.hooks.Close()
	}
}

// finish ends the flow with stamp s and closes the window.
func (u *UI) finish(s Stamp) {
	u.stamp(s)
	u.close()
}

// askSkip shows "Skip setup?": the window manager's close button, and
// "Skip setup" on the Welcome page.
func (u *UI) askSkip() {
	u.confirming = true
	u.focus = &u.skipNo
	if u.hooks.Invalidate != nil {
		u.hooks.Invalidate()
	}
}

// answerSkip is the answer to "Skip setup?".
func (u *UI) answerSkip(skip bool) {
	u.confirming = false
	if skip {
		u.finish(u.skipStamp())
		return
	}
	u.focus = &u.cont
}

// CloseRequested is a request to close the window (app.ClosingEvent); true
// keeps the window open. Before the flow has ended, the full flow asks
// "Skip setup?" first. A close the UI asked for, a flow whose stamp is
// written, and a page opened on its own close at once.
func (u *UI) CloseRequested() (keep bool) {
	if u.closing || u.stamped || len(u.flow.Pages()) < 2 || u.restoreStarted() {
		return false
	}
	u.askSkip()
	return true
}

// Destroyed is the window gone (app.DestroyEvent). A window that failed,
// most likely before it ever showed Welcome (no GPU context), writes no
// stamp, so the next login tries again.
func (u *UI) Destroyed(err error) {
	if err == nil {
		u.Closed()
	}
}

// Closed is the window going away. Before the stamp is written that is a
// skip: a close that came without a request the UI could keep.
func (u *UI) Closed() { u.stamp(u.skipStamp()) }

// scrolled lays w in the page's padding, in a list that scrolls when w is
// taller than the column (a large scale, a small screen). w gets at least
// the column's height, so it can centre itself.
func (u *UI) scrolled(gtx layout.Context, l *widget.List, w layout.Widget) layout.Dimensions {
	l.Axis = layout.Vertical
	h := gtx.Constraints.Max.Y
	return guikit.Scrolled(gtx, l, func(gtx layout.Context) layout.Dimensions {
		gtx.Constraints.Min.Y = h
		return guikit.Pad(gtx, func(gtx layout.Context) layout.Dimensions {
			gtx.Constraints.Min.X = gtx.Constraints.Max.X
			return w(gtx)
		})
	})
}

// basePage is the usual page: Go back, Skip and Continue, where Continue
// moves on without applying anything.
type basePage struct{ u *UI }

func (b basePage) Footer(n nav) (leading, trailing []guikit.Action) {
	if n.Back != nil {
		leading = append(leading, *n.Back)
	}
	return leading, []guikit.Action{*n.Skip, *n.Continue}
}

func (b basePage) Update(layout.Context) {}

func (b basePage) Continue() { b.u.flow.Next() }

// stubPage stands for a page whose file is not written yet.
type stubPage struct {
	basePage
	id PageID
}

func (s *stubPage) Body(gtx layout.Context) layout.Dimensions {
	return guikit.Pad(gtx, s.u.Heading(PageTitle(s.id), "This page is not available yet."))
}
