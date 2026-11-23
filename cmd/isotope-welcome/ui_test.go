package main

import (
	"errors"
	"image"
	"testing"
	"time"

	"gioui.org/app"
	"gioui.org/f32"
	"gioui.org/io/input"
	"gioui.org/io/pointer"
	"gioui.org/io/system"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"

	"github.com/kairos-community/isotope/pkg/guikit"
)

// recorder is a Hooks that keeps what the UI asked for, and closes the
// window as main does on Wayland: Close sends a close request back through
// CloseRequested, and only a request the UI does not keep destroys the
// window (closed, then Closed).
type recorder struct {
	u       *UI
	stamps  []Stamp
	logouts int
	sheets  int
	closed  bool
	logout  func() error
}

func (r *recorder) hooks() Hooks {
	return Hooks{
		Invalidate: func() {},
		Logout: func() error {
			r.logouts++
			if r.logout != nil {
				return r.logout()
			}
			return nil
		},
		OpenShortcutSheet: func() error { r.sheets++; return nil },
		WriteStamp:        func(s Stamp) { r.stamps = append(r.stamps, s) },
		Close:             func() { r.requestClose() },
	}
}

// requestClose is a close request, from Close or the window manager.
func (r *recorder) requestClose() {
	if r.u.CloseRequested() {
		return // aborted: the window stays
	}
	r.closed = true
	r.u.Closed()
}

func newRecorded(f *Flow, d Desktop) (*UI, *recorder) {
	r := &recorder{}
	r.u = newUI(f, d, guikit.NewShaper(), r.hooks())
	return r.u, r
}

func to(u *UI, p PageID) {
	for u.flow.Current() != p {
		u.flow.Next()
	}
}

func wantStamps(t *testing.T, r *recorder, want ...Stamp) {
	t.Helper()
	if len(r.stamps) != len(want) {
		t.Fatalf("stamps %v, want %v", r.stamps, want)
	}
	for i := range want {
		if r.stamps[i] != want[i] {
			t.Fatalf("stamps %v, want %v", r.stamps, want)
		}
	}
}

func TestCloseRequestAsksBeforeSkipping(t *testing.T) {
	u, r := newRecorded(NewFlow(Sway), Sway)
	to(u, PNetwork)
	r.requestClose()
	if r.closed || !u.confirming {
		t.Fatalf("closed %v, asking %v: the close must ask first", r.closed, u.confirming)
	}
	wantStamps(t, r)
	u.answerSkip(true)
	wantStamps(t, r, StampSkipped)
	if !r.closed {
		t.Fatal("Skip did not close the window")
	}
}

func TestCloseRequestKeepGoing(t *testing.T) {
	u, r := newRecorded(NewFlow(Sway), Sway)
	to(u, PNetwork)
	r.requestClose()
	u.answerSkip(false)
	if r.closed || u.confirming || u.flow.Current() != PNetwork {
		t.Fatalf("closed %v, asking %v, page %s", r.closed, u.confirming, u.flow.Current())
	}
	wantStamps(t, r)
}

func TestEscDismissesTheQuestion(t *testing.T) {
	u, r := newRecorded(NewFlow(Sway), Sway)
	to(u, PNetwork)
	r.requestClose()
	u.prev()
	if u.confirming || u.flow.Current() != PNetwork {
		t.Fatalf("Esc: asking %v, page %s", u.confirming, u.flow.Current())
	}
}

// A close the UI cannot stop (the window destroyed) is still a skip.
func TestForcedCloseIsASkip(t *testing.T) {
	u, r := newRecorded(NewFlow(Sway), Sway)
	to(u, PNetwork)
	u.Closed()
	wantStamps(t, r, StampSkipped)
}

func TestSkipSetupAsks(t *testing.T) {
	u, r := newRecorded(NewFlow(Sway), Sway)
	u.askSkip()
	if !u.confirming {
		t.Fatal("Skip setup did not ask")
	}
	wantStamps(t, r)
	u.answerSkip(true)
	wantStamps(t, r, StampSkipped)
	if !r.closed {
		t.Fatal("not closed")
	}
}

func TestFinishOnReadyClosesWithoutAsking(t *testing.T) {
	u, r := newRecorded(NewFlow(Cosmic), Cosmic)
	to(u, PReady)
	u.next()
	wantStamps(t, r, StampDone)
	if !r.closed || u.confirming {
		t.Fatalf("closed %v, asking %v", r.closed, u.confirming)
	}
}

func TestFinishAppsPending(t *testing.T) {
	u, r := newRecorded(NewFlow(Sway), Sway)
	u.appsPending = true
	to(u, PReady)
	u.next()
	wantStamps(t, r, StampAppsPending)
}

func TestLogOutWritesTheStampThenCloses(t *testing.T) {
	u, r := newRecorded(NewFlow(Sway), Sway)
	r.logout = func() error {
		if len(r.stamps) != 1 || r.stamps[0] != StampDone {
			t.Errorf("logout before the stamp: %v", r.stamps)
		}
		if r.closed {
			t.Error("window closed before the logout ran")
		}
		return nil
	}
	u.flow.Outcome.LanguageChanged = "Deutsch (Deutschland)"
	to(u, PReady)
	u.page().(*readyPage).logOut()
	if r.logouts != 1 || !r.closed {
		t.Fatalf("logouts %d, closed %v", r.logouts, r.closed)
	}
	wantStamps(t, r, StampDone)
}

func TestLogOutFailureKeepsTheWindow(t *testing.T) {
	u, r := newRecorded(NewFlow(Sway), Sway)
	r.logout = func() error { return errors.New("swaymsg: not found") }
	u.flow.Outcome.LanguageChanged = "Deutsch (Deutschland)"
	to(u, PReady)
	rp := u.page().(*readyPage)
	rp.logOut()
	if r.closed {
		t.Fatal("the window closed with the error on it")
	}
	if rp.logoutErr == "" {
		t.Fatal("no error shown")
	}
	wantStamps(t, r, StampDone)
	// The stamp is written: closing now neither asks nor stamps again,
	// and Finish just closes.
	r.requestClose()
	if !r.closed || u.confirming {
		t.Fatalf("closed %v, asking %v", r.closed, u.confirming)
	}
	wantStamps(t, r, StampDone)
}

func TestBackAndContinue(t *testing.T) {
	u, r := newRecorded(NewFlow(Sway), Sway)
	u.next()
	if u.flow.Current() != PLanguage {
		t.Fatalf("Continue on Welcome went to %s", u.flow.Current())
	}
	u.prev()
	u.prev() // Esc on the first page stays there.
	if u.flow.Current() != PWelcome {
		t.Fatalf("Go back went to %s", u.flow.Current())
	}
	to(u, PShortcuts)
	u.next()
	if u.flow.Current() != PReady || len(r.stamps) != 0 {
		t.Fatalf("Continue on Shortcuts: %s, stamps %v", u.flow.Current(), r.stamps)
	}
}

func TestSinglePageFlow(t *testing.T) {
	u, r := newRecorded(Single(PShortcuts), Sway)
	// One page opened from the menu: closing it does not ask.
	r.requestClose()
	if !r.closed || u.confirming {
		t.Fatalf("closed %v, asking %v", r.closed, u.confirming)
	}
	u, r = newRecorded(Single(PShortcuts), Sway)
	u.next()
	wantStamps(t, r, StampDone)
	if !r.closed {
		t.Fatal("Done did not close")
	}
}

func TestShowAllShortcuts(t *testing.T) {
	u, r := newRecorded(NewFlow(Sway), Sway)
	to(u, PShortcuts)
	u.page().(*shortcutsPage).showAll()
	if r.sheets != 1 {
		t.Fatalf("sheet opened %d times", r.sheets)
	}
}

// The window's Wayland app_id and X11 class, which the window rules of
// sway, i3 and niri float.
func TestAppID(t *testing.T) {
	if app.ID != "isotope-welcome" {
		t.Fatalf("app.ID = %q", app.ID)
	}
}

// installingUI is the full flow of sway on page p, with a batch of apps
// installing in the background.
func installingUI(t *testing.T, p PageID) (*UI, *recorder) {
	t.Helper()
	u, r := newRecorded(NewFlow(Sway), Sway)
	u.appList = loadApps(t)
	u.installer = NewInstaller(online(), u.appList)
	u.installer.setProgress(1, 5, "org.videolan.VLC")
	to(u, p)
	return u, r
}

// I1: Log out on Ready ends the session, which kills the installs. While
// they run, Log out does nothing; once they are done it logs out.
func TestLogOutWaitsForTheInstalls(t *testing.T) {
	u, r := installingUI(t, PReady)
	u.flow.Outcome.LanguageChanged = "Deutsch (Deutschland)"
	rp := u.page().(*readyPage)
	if !rp.logoutBlocked() {
		t.Fatal("Log out must be disabled while apps install")
	}
	rp.logOut()
	if r.logouts != 0 || r.closed {
		t.Fatalf("logouts %d, closed %v: Log out ran during the installs", r.logouts, r.closed)
	}
	wantStamps(t, r)
	u.installer.setFinished()
	if rp.logoutBlocked() {
		t.Fatal("Log out must be enabled once the installs are done")
	}
	rp.logOut()
	if r.logouts != 1 || !r.closed {
		t.Fatalf("logouts %d, closed %v", r.logouts, r.closed)
	}
	wantStamps(t, r, StampDone)
}

// I1: setup skipped (or the window closed) while apps still install
// leaves the reminder, so the next login offers the apps again.
func TestSkipSetupWhileInstallingLeavesAppsPending(t *testing.T) {
	u, r := installingUI(t, PShortcuts)
	r.requestClose()
	u.answerSkip(true)
	wantStamps(t, r, StampAppsPending)
	if !r.closed {
		t.Fatal("not closed")
	}
}

func TestForcedCloseWhileInstallingLeavesAppsPending(t *testing.T) {
	u, r := installingUI(t, PReady)
	u.Closed()
	wantStamps(t, r, StampAppsPending)
}

// Apps skipped offline, then setup skipped on a later page: the reminder
// the Apps skip asked for stays.
func TestSkipSetupAfterAnOfflineAppsSkipKeepsTheReminder(t *testing.T) {
	u, rec, _ := appsUI(t, NewFlow(Sway), offline())
	u.skipPage()
	rec.requestClose()
	u.answerSkip(true)
	wantStamps(t, rec, StampAppsPending)
}

// M3: a window that failed (no GPU context) and never showed Welcome
// writes no stamp, so the next login tries again.
func TestWindowErrorWritesNoStamp(t *testing.T) {
	u, r := newRecorded(NewFlow(Sway), Sway)
	u.Destroyed(errors.New("egl: no display"))
	wantStamps(t, r)
	u.Destroyed(nil)
	wantStamps(t, r, StampSkipped)
}

// M5: while SetLocale runs, Skip and Go back wait, so Ready reports the
// language the call sets.
func TestLanguageBusyHoldsSkipAndBack(t *testing.T) {
	u, _ := newRecorded(NewFlow(Sway), Sway)
	to(u, PLanguage)
	p := u.page().(*languagePage)
	p.busy = true
	u.skipPage()
	u.prev()
	if u.flow.Current() != PLanguage || contains(u.flow.Outcome.Skipped, PLanguage) {
		t.Fatalf("page %s, skipped %v", u.flow.Current(), u.flow.Outcome.Skipped)
	}
	lead, trail := p.Footer(u.nav())
	for _, a := range append(lead, trail...) {
		if (a.Click == &u.skip || a.Click == &u.back) && !a.Disabled {
			t.Errorf("%s is enabled while the language is set", a.Label)
		}
	}
	p.busy = false
	u.skipPage()
	if u.flow.Current() == PLanguage {
		t.Fatal("Skip once the call is done must move on")
	}
}

// pressAt runs two frames of u in a router, the second with a click at pt,
// so a button under pt acts. It returns the window actions that came out.
func pressAt(u *UI, pt image.Point) []system.Action {
	var got []system.Action
	u.hooks.Window = func(a system.Action) { got = append(got, a) }
	var r input.Router
	size := image.Pt(1000, 700)
	frame := func() {
		var ops op.Ops
		gtx := layout.Context{
			Ops:         &ops,
			Source:      r.Source(),
			Metric:      unit.Metric{PxPerDp: 1, PxPerSp: 1},
			Constraints: layout.Exact(size),
			Now:         time.Now(),
		}
		u.Layout(gtx)
		r.Frame(&ops)
	}
	frame()
	r.Queue(
		pointer.Event{Kind: pointer.Move, Position: f32.Pt(float32(pt.X), float32(pt.Y)), Source: pointer.Mouse},
		pointer.Event{Kind: pointer.Press, Buttons: pointer.ButtonPrimary, Position: f32.Pt(float32(pt.X), float32(pt.Y)), Source: pointer.Mouse},
		pointer.Event{Kind: pointer.Release, Buttons: pointer.ButtonPrimary, Position: f32.Pt(float32(pt.X), float32(pt.Y)), Source: pointer.Mouse},
	)
	frame()
	frame()
	return got
}

// The title bar the UI draws on COSMIC is inert while the "Skip setup?"
// question is up, as the page under it is: the close button does nothing.
func TestTitleBarIsInertUnderTheSkipQuestion(t *testing.T) {
	// The close button is the first of the left-hand controls: 14 dp in
	// and 16 dp wide, in a bar 40 dp high.
	closeBtn := image.Pt(14+8, guikit.TitleBarHeight/2)
	newBar := func() (*UI, *recorder) {
		u, r := newRecorded(NewFlow(Cosmic), Cosmic)
		u.bar = &guikit.TitleBar{Title: "Welcome to Isotope", Left: true}
		return u, r
	}

	u, _ := newBar()
	got := pressAt(u, closeBtn)
	if len(got) != 1 || got[0] != system.ActionClose {
		t.Fatalf("without the question the close button asked for %v, want one close: the test misses the button", got)
	}

	u, _ = newBar()
	u.confirming = true
	if got := pressAt(u, closeBtn); len(got) != 0 {
		t.Fatalf("under the question the title bar asked for %v", got)
	}
}
