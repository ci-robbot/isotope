package main

import (
	"context"
	"errors"
	"image"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"gioui.org/gesture"
	"gioui.org/io/event"
	"gioui.org/io/key"
	"gioui.org/io/system"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/text"
	"gioui.org/widget"

	"github.com/kairos-community/isotope/pkg/guikit"
	"github.com/kairos-community/isotope/pkg/i18n"
	"github.com/kairos-community/isotope/pkg/installer"
)

// gpage is a page of the graphical installer. The text installer's fourteen
// steps (installer.Page) fold into seven pages, because a window has room for
// several fields; guiPage maps one to the other.
type gpage int

const (
	pWelcome gpage = iota
	pKeyboard
	pAccount
	pDisk
	pEncrypt
	pRecovery
	pSummary
	pInstalling
	pDone
)

// stepNames is the sidebar's step list.
var stepNames = []string{i18n.Mark("Introduction"), i18n.Mark("Keyboard and time zone"), i18n.Mark("Account"), i18n.Mark("Disk"), i18n.Mark("Encryption"), i18n.Mark("Summary"), i18n.Mark("Installation")}

func stepOf(p gpage) int {
	switch p {
	case pWelcome:
		return 0
	case pKeyboard:
		return 1
	case pAccount:
		return 2
	case pDisk:
		return 3
	case pEncrypt, pRecovery:
		return 4
	case pSummary:
		return 5
	}
	return 6
}

func guiPage(p installer.Page) gpage {
	switch p {
	case installer.PageKeyboard, installer.PageTimezone:
		return pKeyboard
	case installer.PageHostname, installer.PageUser, installer.PagePass, installer.PageGithub:
		return pAccount
	case installer.PageDisk:
		return pDisk
	case installer.PageEncrypt, installer.PagePassphrase, installer.PagePassphraseConfirm, installer.PageRecovery:
		return pEncrypt
	case installer.PageRecoveryShow, installer.PageRecoveryConfirm:
		return pRecovery
	}
	return pSummary
}

// firstCorePage is the core page a GUI page starts at.
func (u *UI) firstCorePage(p gpage) installer.Page {
	switch p {
	case pKeyboard:
		return installer.PageKeyboard
	case pAccount:
		return installer.PageHostname
	case pDisk:
		return installer.PageDisk
	case pEncrypt:
		if u.w.TrustedBoot {
			return installer.PageRecovery
		}
		return installer.PageEncrypt
	case pRecovery:
		return installer.PageRecoveryShow
	}
	return installer.PageReview
}

// hooks are what the UI does to the world; the screenshot tests replace
// them.
type hooks struct {
	// confirm hands the reviewed choice to the install.
	confirm func(installer.Choice)
	// relaunch saves s and restarts the window with the layout s names.
	relaunch func(s installer.GUIState)
	poweroff func()
	reboot   func()
	// installed records that the install finished (window mode only: the
	// live launcher reads it); keepTrying closes the live window after one
	// (window mode only). Both are nil in the cage installer.
	installed  func()
	keepTrying func()
	// started is called once the first frame is drawn.
	started func()
	// describe says what is on a disk now (installer.DescribeDisk).
	describe func(path string) installer.DiskInfo
	// githubKeys counts a GitHub user's keys (installer.CountGithubKeys).
	githubKeys func(ctx context.Context, name string) (int, error)
	// saveLog saves the install log and says where it went.
	saveLog func() (string, error)
	// terminal opens a terminal, or leaves for the rescue shell.
	terminal func()
	// invalidate asks for a frame from another goroutine.
	invalidate func()
	// window performs a title bar button's action (window mode on COSMIC).
	window func(system.Action)
}

// UI is the graphical installer: the window's state and the wizard it
// drives. Every rule is core's: the UI collects a page's fields, calls the
// installer.Wizard methods for that page, shows their errors and follows Page.
type UI struct {
	*guikit.Theme
	w      *installer.Wizard
	page   gpage
	pageAt time.Time
	now    func() time.Time
	hooks  hooks

	// version is the sidebar's footer, "Isotope v0.0.20 · sway".
	version string
	// runLayout and runVariant are the keymap the compositor was started
	// with; canRelaunch says isotope-install-ui can restart it with another.
	runLayout, runVariant string
	canRelaunch           bool
	// software says the screen is drawn in software, because Mesa has no
	// working driver for the GPU: isotope-install-ui started cage that way
	// (ISOTOPE_SOFTWARE_RENDERING=1; not on a virtual GPU). The first page
	// says so. gpuClass is why (ISOTOPE_GPU_CLASS: hardware or firmware),
	// and vmImage says this is the vm image line, which has no hardware
	// drivers at all.
	software bool
	gpuClass string
	vmImage  bool

	// window is window mode (--window, window.go): a window in the live
	// session, not cage. live is the live session's choices (--choices),
	// and kbFixed says the keyboard came from them, so the keyboard page
	// asks only the time zone. bar is the title bar drawn on COSMIC; nil
	// elsewhere and in cage.
	window  bool
	live    liveChoices
	kbFixed bool
	bar     *guikit.TitleBar

	focus     event.Tag // focus this on the next frame
	scale     float32   // Ctrl+Plus and Ctrl+Minus
	win       guikit.Window
	startedOK bool

	back, cont, shutdown widget.Clickable

	kbSearch, kbTry *guikit.Field
	kbList          widget.List
	kbRows          []guikit.Row
	kbSel           int
	kbShown         []int
	kbApplied       bool
	tzField         *guikit.Field
	tzList          widget.List
	tzRows          []guikit.Row
	tzSel           int
	tzShown         []int
	zoneCoords      map[string]installer.Coord
	mapClick        gesture.Click
	mapSize         image.Point

	host, user, pw, pw2, gh *guikit.Field
	ghState                 githubState
	acctList                widget.List
	encList                 widget.List

	diskSel   int
	diskInfo  []installer.DiskInfo
	diskCards []widget.Clickable
	diskList  widget.List
	rescan    widget.Clickable

	encOn, recKey  bool
	encNo, encYes  widget.Clickable
	recBox         widget.Clickable
	pp, pp2        *guikit.Field
	encErr         string
	rk             *guikit.Field
	showQR         bool
	qrButton       widget.Clickable
	change         [6]widget.Clickable
	summaryChanges []gpage

	install  installState
	details  widget.Clickable
	logList  widget.List
	restart  widget.Clickable
	keep     widget.Clickable
	saveLog  widget.Clickable
	terminal widget.Clickable
	savedTo  string
}

// githubState is the account page's key lookup: the name typed, when, and
// what GitHub said for which name.
type githubState struct {
	typed    string
	typedAt  time.Time
	asked    string
	answered string
	n        int
	err      error
	results  chan githubResult
}

type githubResult struct {
	name string
	n    int
	err  error
}

// installState is what the installing page shows, fed by installer.Install.
type installState struct {
	step     int
	lines    []string
	failed   bool
	err      error
	finished bool
	encrypt  bool
	user     string
	show     bool
	events   chan installer.Event
	// started is when the install began, activeAt when the active system
	// started installing: the estimate needs both.
	started  time.Time
	activeAt time.Time
}

const logKeep = 200

func newUI(w *installer.Wizard, shaper *text.Shaper, h hooks) *UI {
	u := &UI{Theme: &guikit.Theme{Shaper: shaper}, w: w, hooks: h, now: time.Now, scale: 1}
	u.kbSearch = guikit.NewField(true)
	u.kbTry = guikit.NewField(true)
	u.tzField = guikit.NewField(true)
	u.tzField.SelectOnFocus = true
	u.host = guikit.NewField(true)
	u.host.SelectOnFocus = true
	u.user = guikit.NewField(true)
	u.pw = guikit.NewSecret()
	u.pw2 = guikit.NewSecret()
	u.gh = guikit.NewField(true)
	u.pp = guikit.NewSecret()
	u.pp2 = guikit.NewSecret()
	u.rk = guikit.NewField(true)
	u.rk.Editor.MaxLen = 80
	u.kbList.Axis = layout.Vertical
	u.tzList.Axis = layout.Vertical
	u.logList.Axis = layout.Vertical
	u.logList.ScrollToEnd = true
	u.diskList.Axis = layout.Vertical
	u.acctList.Axis = layout.Vertical
	u.encList.Axis = layout.Vertical
	u.install.events = make(chan installer.Event, 256)
	u.ghState.results = make(chan githubResult, 4)
	u.runLayout = installer.DefaultLayout
	if w != nil {
		u.setWizard(w)
	}
	return u
}

// setWizard starts the UI on w, the wizard installer.RunWizard made for this
// machine.
func (u *UI) setWizard(w *installer.Wizard) {
	if u.window {
		u.kbFixed = windowWizard(w, u.live)
	}
	u.w = w
	u.kbSel = w.KeyboardIndex()
	u.tzSel = w.TimezoneIndex()
	if u.tzSel >= 0 {
		u.tzField.Editor.SetText(w.Zones[u.tzSel])
		u.tzField.Editor.SetCaret(u.tzField.Editor.Len(), u.tzField.Editor.Len())
	}
	u.host.Editor.SetText(w.HostnameValue())
	u.kbRows = make([]guikit.Row, len(w.Keyboards))
	u.tzRows = make([]guikit.Row, len(w.Zones))
	u.diskCards = make([]widget.Clickable, len(w.Disks))
	u.describeDisks()
	u.scrollToSelection()
	// Continue has the focus, so Enter goes on from the first page.
	u.refocus()
}

func (u *UI) scrollToSelection() {
	u.filterKeyboards()
	u.filterZones()
	for i, k := range u.kbShown {
		if k == u.kbSel {
			u.kbList.Position.First = max(0, i-2)
		}
	}
	for i, z := range u.tzShown {
		if z == u.tzSel {
			u.tzList.Position.First = max(0, i-2)
		}
	}
}

// goTo shows p; the page slides in.
func (u *UI) goTo(p gpage) {
	if p != u.page {
		u.pageAt = u.now()
	}
	u.page = p
	u.refocus()
}

// refocus gives the page's first control the keyboard focus: on a new page,
// and when the window gets the keyboard (Gio drops a focus set before that).
func (u *UI) refocus() {
	switch u.page {
	case pWelcome, pDisk:
		u.focus = &u.cont
	case pKeyboard:
		u.focus = &u.kbSearch.Editor
		if u.kbFixed {
			u.focus = &u.tzField.Editor
		}
		if u.kbApplied {
			// A layout was just applied: try it.
			u.focus = &u.kbTry.Editor
		}
	case pAccount:
		u.focus = &u.user.Editor
	case pEncrypt:
		switch {
		case u.encOn && !u.w.TrustedBoot:
			u.focus = &u.pp.Editor
		case u.encOn || u.recKey:
			u.focus = &u.encYes
		default:
			u.focus = &u.encNo
		}
	case pRecovery:
		u.focus = &u.rk.Editor
	case pSummary:
		// Nothing: Enter must neither erase a disk nor go back by itself.
		// Tab reaches the Change links and both buttons.
	case pDone:
		u.focus = &u.restart
	case pInstalling:
		if u.install.failed {
			u.focus = &u.restart
		}
	}
}

// next is Continue.
func (u *UI) next() {
	switch u.page {
	case pWelcome:
		u.goTo(pKeyboard)
	case pKeyboard:
		u.nextKeyboard()
	case pAccount:
		u.nextAccount()
	case pDisk:
		u.nextDisk()
	case pEncrypt:
		u.nextEncrypt()
	case pRecovery:
		u.nextRecovery()
	case pSummary:
		u.hooks.confirm(u.w.Choice)
		u.install.encrypt = u.w.Choice.Encrypt
		u.install.user = u.w.Choice.Username
		u.install.started = u.now()
		u.goTo(pInstalling)
	case pDone:
		u.hooks.reboot()
	}
}

// prev is Go back and Esc: the core page before this GUI page.
func (u *UI) prev() {
	switch u.page {
	case pWelcome, pInstalling, pDone:
		return
	case pKeyboard:
		u.goTo(pWelcome)
		return
	}
	cur := u.page
	u.w.Page = u.firstCorePage(cur)
	for guiPage(u.w.Page) == cur {
		if !u.w.Back() {
			u.goTo(pWelcome)
			return
		}
	}
	u.goTo(guiPage(u.w.Page))
}

// --- keyboard and time zone -------------------------------------------------

func (u *UI) filterKeyboards() {
	q := strings.ToLower(strings.TrimSpace(u.kbSearch.Editor.Text()))
	u.kbShown = u.kbShown[:0]
	for i, k := range u.w.Keyboards {
		if q == "" || strings.Contains(strings.ToLower(k.Description), q) || strings.Contains(strings.ToLower(k.Code()), q) {
			u.kbShown = append(u.kbShown, i)
		}
	}
}

func zoneMatch(zone, q string) bool {
	z := strings.ToLower(strings.ReplaceAll(zone, "_", " "))
	return strings.Contains(z, strings.ReplaceAll(q, "_", " "))
}

func (u *UI) filterZones() {
	q := strings.ToLower(strings.TrimSpace(u.tzField.Editor.Text()))
	if u.tzSel >= 0 && strings.EqualFold(q, u.w.Zones[u.tzSel]) {
		q = ""
	}
	u.tzShown = u.tzShown[:0]
	for i, z := range u.w.Zones {
		if q == "" || zoneMatch(z, q) {
			u.tzShown = append(u.tzShown, i)
		}
	}
}

// pickKeyboard selects Keyboards[i] and applies it at once: a layout the
// compositor was not started with restarts it (see relaunch.go).
func (u *UI) pickKeyboard(i int) {
	u.kbSel = i
	if u.needsRelaunch() {
		u.hooks.relaunch(u.saveState(pKeyboard))
		return
	}
	if u.window {
		// The layout is for the installed system: nothing to try here.
		return
	}
	u.kbApplied = true
	u.focus = &u.kbTry.Editor
}

func (u *UI) needsRelaunch() bool {
	if !u.canRelaunch || u.kbSel < 0 {
		return false
	}
	k := u.w.Keyboards[u.kbSel]
	return k.Layout != u.runLayout || k.Variant != u.runVariant
}

func (u *UI) pickZone(i int) {
	u.tzSel = i
	u.tzField.Editor.SetText(u.w.Zones[i])
	u.tzField.Editor.SetCaret(len(u.w.Zones[i]), len(u.w.Zones[i]))
	u.tzField.Err = ""
	u.filterZones()
}

func (u *UI) nextKeyboard() {
	u.tzField.Err = ""
	if u.kbSel < 0 {
		return
	}
	if u.tzSel < 0 || u.tzField.Editor.Text() != u.w.Zones[u.tzSel] {
		// The field takes a zone from the list; Enter picks the first
		// match, so a typed city is one key away.
		if len(u.tzShown) == 1 || (len(u.tzShown) > 0 && u.tzField.Editor.Text() != "") {
			u.pickZone(u.tzShown[0])
		} else {
			u.tzField.Err = i18n.T("Pick a time zone from the list.")
			u.focus = &u.tzField.Editor
			return
		}
	}
	u.w.SetKeyboard(u.kbSel)
	u.w.SetTimezone(u.tzSel)
	if u.needsRelaunch() {
		u.hooks.relaunch(u.saveState(pAccount))
		return
	}
	u.goTo(pAccount)
}

// --- account ----------------------------------------------------------------

// sentence is core's error text as a sentence for the window: the words are
// the text installer's, with a capital and a full stop. A translation
// keeps the same shape: no capital, no full stop.
func sentence(err error) string {
	s := err.Error()
	r, n := utf8.DecodeRuneInString(s)
	s = string(unicode.ToUpper(r)) + s[n:]
	if !strings.HasSuffix(s, ".") {
		s += "."
	}
	return s
}

func (u *UI) passwordsMatch() bool { return u.pw.Editor.Text() == u.pw2.Editor.Text() }

func (u *UI) nextAccount() {
	for _, f := range []*guikit.Field{u.host, u.user, u.pw, u.pw2, u.gh} {
		f.Err = ""
	}
	w := u.w
	w.SetHostname(u.host.Editor.Text())
	var first *guikit.Field
	fail := func(f *guikit.Field, err error) {
		f.Err = sentence(err)
		if first == nil {
			first = f
		}
	}
	if err := w.SetUsername(u.user.Editor.Text()); err != nil {
		fail(u.user, err)
	}
	if err := w.SetPassword(u.pw.Editor.Text()); err != nil {
		fail(u.pw, err)
	} else if !u.passwordsMatch() {
		fail(u.pw2, errors.New(i18n.T("the passwords do not match")))
	}
	if err := w.SetGithub(u.gh.Editor.Text()); err != nil && len(w.Disks) > 0 {
		fail(u.gh, err)
	} else {
		// The keys are checked before Continue too, but never wait for:
		// they are optional.
		u.lookupGithub(true)
	}
	if first != nil {
		u.focus = &first.Editor
		return
	}
	// With no disk, SetGithub stays: the disk page says so and rescans.
	w.Page = installer.PageDisk
	if u.diskSel >= len(w.Disks) {
		u.diskSel = 0
	}
	u.goTo(pDisk)
}

// --- disk -------------------------------------------------------------------

// sysBlock is where Rescan looks for disks.
var sysBlock = "/sys/block"

func (u *UI) rescanDisks() {
	disks, err := installer.ListDisks(sysBlock)
	if err != nil {
		disks = nil
	}
	u.w.Disks = disks
	u.diskCards = make([]widget.Clickable, len(disks))
	u.diskSel = 0
	u.describeDisks()
}

// describeDisks reads what is on each disk now, for the cards, the bar and
// the warning.
func (u *UI) describeDisks() {
	u.diskInfo = make([]installer.DiskInfo, len(u.w.Disks))
	if u.hooks.describe == nil {
		return
	}
	for i, d := range u.w.Disks {
		u.diskInfo[i] = u.hooks.describe(d.Path)
	}
}

func (u *UI) nextDisk() {
	if len(u.w.Disks) == 0 {
		return
	}
	u.w.SetDisk(u.diskSel)
	u.goTo(guiPage(u.w.Page))
}

// --- encryption and the recovery key ----------------------------------------

func (u *UI) nextEncrypt() {
	u.pp.Err, u.pp2.Err, u.encErr = "", "", ""
	w := u.w
	if !w.TrustedBoot {
		w.SetEncrypt(u.encOn)
		if u.encOn {
			if err := w.SetPassphrase(u.pp.Editor.Text()); err != nil {
				u.pp.Err = sentence(err)
				u.focus = &u.pp.Editor
				return
			}
			if err := w.ConfirmPassphrase(u.pp2.Editor.Text()); err != nil {
				u.pp2.Err = sentence(err)
				u.focus = &u.pp2.Editor
				return
			}
		}
	}
	if w.TrustedBoot || u.encOn {
		if err := w.SetRecoveryKey(u.recKey); err != nil {
			u.encErr = sentence(err)
			return
		}
	}
	if w.Page == installer.PageRecoveryShow {
		// The key and the check share one page: the group to type back
		// is picked as the page opens.
		w.KeySaved()
		u.rk.Editor.SetText("")
		u.rk.Err = ""
		u.goTo(pRecovery)
		return
	}
	u.goTo(pSummary)
}

// recoveryConfirmed says whether the typed group is right, without moving
// the wizard: Continue stays disabled until it is.
func (u *UI) recoveryConfirmed() bool {
	c := *u.w
	return c.ConfirmRecoveryKey(u.rk.Editor.Text()) == nil
}

func (u *UI) nextRecovery() {
	if err := u.w.ConfirmRecoveryKey(u.rk.Editor.Text()); err != nil {
		u.rk.Err = sentence(err)
		return
	}
	u.goTo(pSummary)
}

// --- install ----------------------------------------------------------------

// drain applies the install's events and the GitHub answers that arrived
// since the last frame.
func (u *UI) drain() {
	for {
		select {
		case e := <-u.install.events:
			u.apply(e)
		case r := <-u.ghState.results:
			u.ghState.answered, u.ghState.n, u.ghState.err = r.name, r.n, r.err
		default:
			return
		}
	}
}

// githubDelay is how long the name has to stay the same before GitHub is
// asked, so typing does not send a request per letter.
const githubDelay = 700 * time.Millisecond

// lookupGithub asks GitHub how many keys the typed name has, once per
// name, after githubDelay or at once when now is set.
func (u *UI) lookupGithub(now bool) {
	g := &u.ghState
	name := strings.TrimSpace(u.gh.Editor.Text())
	if u.hooks.githubKeys == nil || name == "" || name == g.asked || installer.ValidateGithub(name) != nil {
		return
	}
	if !now && u.now().Sub(g.typedAt) < githubDelay {
		return
	}
	g.asked = name
	look, results, inval := u.hooks.githubKeys, g.results, u.hooks.invalidate
	go func() {
		n, err := look(context.Background(), name)
		results <- githubResult{name, n, err}
		if inval != nil {
			inval()
		}
	}()
}

// githubStatus is the line under the GitHub field: whether there is one, its
// words, and whether it is good news.
func (u *UI) githubStatus() (string, bool, bool) {
	g := &u.ghState
	name := strings.TrimSpace(u.gh.Editor.Text())
	switch {
	case name == "" || installer.ValidateGithub(name) != nil:
		return "", false, false
	case g.answered == name:
		txt, ok := installer.GithubKeysText(g.n, g.err)
		return txt, ok, true
	case g.asked == name:
		return i18n.T("Checking GitHub"), false, true
	}
	return "", false, false
}

func (u *UI) apply(e installer.Event) {
	s := &u.install
	if u.page < pInstalling {
		// An install that needed no questions (a seeded config).
		u.goTo(pInstalling)
	}
	if s.started.IsZero() {
		s.started = u.now()
	}
	switch e.Kind {
	case installer.EventLine:
		s.lines = append(s.lines, e.Line)
		if len(s.lines) > logKeep {
			s.lines = s.lines[len(s.lines)-logKeep:]
		}
	case installer.EventStep:
		s.step = e.Step
		if s.step >= installer.ActiveStep && s.activeAt.IsZero() {
			s.activeAt = u.now()
		}
	case installer.EventFinished:
		s.finished = true
		if u.hooks.installed != nil {
			u.hooks.installed()
		}
		u.goTo(pDone)
	case installer.EventFailed:
		s.failed, s.err = true, e.Err
		u.page = pInstalling
		u.focus = &u.restart
	}
}

// visibleSteps are the install stages the checklist shows: not the last
// ("Complete"), and the passphrase check only for an encrypted install.
func (u *UI) visibleSteps() []int {
	var out []int
	for i, s := range installer.Steps[:len(installer.Steps)-1] {
		if strings.Contains(s.Match, "checking the disk passphrase") && !u.install.encrypt {
			continue
		}
		out = append(out, i)
	}
	return out
}

// --- keys -------------------------------------------------------------------

// keys handles the window-wide keys: Esc is Go back, Ctrl+Plus and
// Ctrl+Minus scale the window.
func (u *UI) keys(gtx layout.Context) { guikit.WindowKeys(gtx, &u.scale, u.prev) }

// update handles the input of the current page before it is drawn.
func (u *UI) update(gtx layout.Context) {
	if u.cont.Clicked(gtx) {
		u.next()
	}
	if u.back.Clicked(gtx) {
		u.prev()
	}
	if u.shutdown.Clicked(gtx) {
		u.hooks.poweroff()
	}
	if u.restart.Clicked(gtx) {
		u.hooks.reboot()
	}
	if u.keep.Clicked(gtx) && u.hooks.keepTrying != nil {
		u.hooks.keepTrying()
	}
	if u.details.Clicked(gtx) {
		u.install.show = !u.install.show
	}
	if u.saveLog.Clicked(gtx) && u.hooks.saveLog != nil {
		if where, err := u.hooks.saveLog(); err != nil {
			u.savedTo = sentence(err)
		} else {
			u.savedTo = i18n.Tf("Saved to %s", where)
		}
	}
	if u.terminal.Clicked(gtx) && u.hooks.terminal != nil {
		u.hooks.terminal()
	}
	if u.qrButton.Clicked(gtx) {
		u.showQR = !u.showQR
	}
	switch u.page {
	case pKeyboard:
		for {
			e, ok := u.mapClick.Update(gtx.Source)
			if !ok {
				break
			}
			if e.Kind == gesture.KindClick {
				u.clickMap(e.Position)
			}
		}
		before := u.kbSearch.Editor.Text()
		if u.kbSearch.Submitted(gtx) && u.kbSel >= 0 {
			u.pickKeyboard(u.kbSel)
		}
		if u.kbSearch.Editor.Text() != before {
			u.filterKeyboards()
			if len(u.kbShown) > 0 {
				u.kbSel = u.kbShown[0]
			}
			u.kbList.Position.First = 0
		}
		for {
			down, ok := guikit.ArrowKey(gtx, &u.kbSearch.Editor)
			if !ok {
				break
			}
			u.kbSel = guikit.MoveSelection(u.kbShown, u.kbSel, down, &u.kbList)
		}

		for _, i := range u.kbShown {
			if u.kbRows[i].Clicked(gtx) {
				u.pickKeyboard(i)
			}
		}
		if u.kbTry.Submitted(gtx) {
			u.nextKeyboard()
			return
		}
		beforeTZ := u.tzField.Editor.Text()
		if u.tzField.Submitted(gtx) {
			u.nextKeyboard()
			return
		}
		if u.tzField.Editor.Text() != beforeTZ {
			u.filterZones()
			u.tzList.Position.First = 0
		}
		for {
			down, ok := guikit.ArrowKey(gtx, &u.tzField.Editor)
			if !ok {
				break
			}
			if i := guikit.MoveSelection(u.tzShown, u.tzSel, down, &u.tzList); i >= 0 {
				u.pickZone(i)
			}
		}

		for _, i := range u.tzShown {
			if u.tzRows[i].Clicked(gtx) {
				u.pickZone(i)
			}
		}
	case pAccount:
		for _, f := range []*guikit.Field{u.host, u.user, u.pw, u.pw2, u.gh} {
			if f.Submitted(gtx) {
				u.next()
				return
			}
		}
		if t := strings.TrimSpace(u.gh.Editor.Text()); t != u.ghState.typed {
			u.ghState.typed, u.ghState.typedAt = t, u.now()
		}
		u.lookupGithub(false)
		if u.ghState.typed != "" && u.ghState.asked != u.ghState.typed {
			gtx.Execute(op.InvalidateCmd{At: u.ghState.typedAt.Add(githubDelay)})
		}
	case pDisk:
		for i := range u.diskCards {
			if u.diskCards[i].Clicked(gtx) {
				u.diskSel = i
			}
		}
		if u.rescan.Clicked(gtx) {
			u.rescanDisks()
		}
	case pEncrypt:
		// Left and Right move between the two cards, as in a radio group.
		for {
			e, ok := gtx.Event(
				key.Filter{Focus: &u.encNo, Name: key.NameRightArrow},
				key.Filter{Focus: &u.encYes, Name: key.NameLeftArrow},
			)
			if !ok {
				break
			}
			if k, ok := e.(key.Event); ok && k.State == key.Press {
				if k.Name == key.NameRightArrow {
					u.encYes.Click()
					u.focus = &u.encYes
				} else {
					u.encNo.Click()
					u.focus = &u.encNo
				}
			}
		}
		if u.encNo.Clicked(gtx) {
			u.encOn = false
			if u.w.TrustedBoot {
				u.recKey = false
			}
		}
		if u.encYes.Clicked(gtx) {
			if u.w.TrustedBoot {
				u.recKey = true
			} else {
				u.encOn = true
				u.focus = &u.pp.Editor
			}
		}
		if u.recBox.Clicked(gtx) {
			u.recKey = !u.recKey
		}
		for _, f := range []*guikit.Field{u.pp, u.pp2} {
			if f.Submitted(gtx) {
				u.next()
				return
			}
		}
	case pRecovery:
		if u.rk.Submitted(gtx) && u.recoveryConfirmed() {
			u.next()
			return
		}
	case pSummary:
		for i, p := range u.summaryChanges {
			if u.change[i].Clicked(gtx) {
				if i == 0 {
					// The keyboard row: ask it, also when the live
					// session set it.
					u.kbFixed = false
				}
				u.w.Page = u.firstCorePage(p)
				u.goTo(p)
			}
		}
	}
}

// zoneFiltering says whether the zone field holds a search rather than the
// chosen zone: the map gives way to the list of matches.
func (u *UI) zoneFiltering() bool {
	t := strings.TrimSpace(u.tzField.Editor.Text())
	return t != "" && (u.tzSel < 0 || t != u.w.Zones[u.tzSel])
}
