package main

import (
	"context"

	"gioui.org/layout"
	"gioui.org/widget"

	"github.com/kairos-community/isotope/pkg/guikit"
	"github.com/kairos-community/isotope/pkg/i18n"
)

// The FINGERPRINTS section of the Users page: the user's own enrolled
// fingers, with Add and Remove, over fprintd (fprint.go). Fingerprints
// belong to one user, as the password does, so they sit on the page where
// the user changes their own password, under the list. Without fprintd
// (the fingerprint extension is not installed) the section offers the
// extension instead. See
// docs/superpowers/specs/2026-11-24-fingerprint-design.md.

const fpExtension = "fingerprint"

// The section's texts, marked here and translated where they are shown.
var (
	fpNotNeeded  = i18n.Mark("A finger unlocks the screen and approves sudo and administrator prompts. Logging in still asks for your password, which also unlocks your keyring.")
	fpNoExt      = i18n.Mark("Unlock the screen and approve sudo and administrator prompts with a finger. Fingerprint sign-in comes with the fingerprint extension.")
	fpNoExtUser  = i18n.Mark("Unlock the screen and approve sudo and administrator prompts with a finger. Fingerprint sign-in comes with the fingerprint extension. An administrator can install it.")
	fpNoReader   = i18n.Mark("No fingerprint reader was found. Connect one, or check that this computer's reader is supported by libfprint.")
	fpRestart    = i18n.Mark("Restart the computer to finish setting up fingerprint sign-in.")
	fpPassTitle  = i18n.Mark("Confirm it is you")
	fpPassBody   = i18n.Mark("Enter your password to change your fingerprints.")
	fpPickTitle  = i18n.Mark("Add a fingerprint")
	fpEnrollHead = i18n.Mark("Adding a fingerprint")
)

// fingerSection is the section's state.
type fingerSection struct {
	u *UI

	// Under u.mu.
	st      FingerState
	loaded  bool
	loadErr string
	// The running enrollment: finger, steps passed, the last message;
	// cancel stops it.
	enrolling string
	passed    int
	message   string
	cancel    context.CancelFunc

	install, add widget.Clickable
	remove       map[string]*widget.Clickable
	pick         map[string]*widget.Clickable
	picked       string // the finger chosen in the Add dialog
	// pass is the open password dialog's field, under u.mu, for the tests.
	pass *guikit.Field
}

func newFingerSection(u *UI) *fingerSection {
	return &fingerSection{u: u, remove: map[string]*widget.Clickable{}, pick: map[string]*widget.Clickable{}}
}

func (s *fingerSection) clickFor(m map[string]*widget.Clickable, k string) *widget.Clickable {
	c, ok := m[k]
	if !ok {
		c = new(widget.Clickable)
		m[k] = c
	}
	return c
}

func (s *fingerSection) state() (FingerState, bool, string) {
	s.u.mu.Lock()
	defer s.u.mu.Unlock()
	return s.st, s.loaded, s.loadErr
}

// load reads fprintd again.
func (s *fingerSection) load() {
	u, f := s.u, s.u.hooks.Fingers
	if f == nil {
		u.mu.Lock()
		s.st, s.loaded, s.loadErr = FingerState{}, true, ""
		u.mu.Unlock()
		return
	}
	u.async(func() {
		st, err := f.State()
		u.mu.Lock()
		defer u.mu.Unlock()
		s.loaded = true
		if err != nil {
			s.loadErr = i18n.Tf("Could not read the fingerprints: %s", describeFprint(err))
			return
		}
		s.st, s.loadErr = st, ""
	})
}

// describeFprint is an fprintd or polkit error in words.
func describeFprint(err error) string {
	switch dbusErrName(err) {
	case "net.reactivated.Fprint.Error.PermissionDenied", "org.freedesktop.PolicyKit1.Error.NotAuthorized":
		return i18n.T("you were not allowed to (the password was not given or not right).")
	case "net.reactivated.Fprint.Error.AlreadyInUse":
		return i18n.T("the reader is in use by another program. Try again in a moment.")
	case "net.reactivated.Fprint.Error.NoSuchDevice":
		return i18n.T("the reader is gone.")
	case "net.reactivated.Fprint.Error.NoEnrolledPrints":
		return i18n.T("that fingerprint is not enrolled.")
	case "net.reactivated.Fprint.Error.PrintsNotDeleted", "net.reactivated.Fprint.Error.PrintsNotDeletedFromDevice":
		return i18n.T("the reader kept the fingerprint.")
	case "org.freedesktop.DBus.Error.ServiceUnknown", "org.freedesktop.DBus.Error.NameHasNoOwner":
		return i18n.T("fprintd is not running.")
	}
	return Describe(err)
}

// askPassword is the agent's asker: a password dialog over the window,
// answered from the UI goroutine. The dialog that was open (the
// enrollment's) comes back after it.
func (s *fingerSection) askPassword(ctx context.Context, q AuthAsk) (string, bool) {
	u := s.u
	type answer struct {
		pw string
		ok bool
	}
	ch := make(chan answer, 1)
	f := guikit.NewSecret()
	focused := false // read and written by the frame only
	u.mu.Lock()
	prev := u.dlg
	s.pass = f
	u.mu.Unlock()
	body := i18n.T(fpPassBody)
	if q.Info != "" {
		body += "\n\n" + q.Info
	}
	var d *dialog
	d = newDialog(i18n.T(fpPassTitle), body, func(gtx layout.Context) layout.Dimensions {
		if f.Submitted(gtx) && f.Editor.Text() != "" {
			u.pick(d, 1)
		}
		if !focused {
			focused = true
			gtx.Execute(focus(&f.Editor))
		}
		return u.Labelled(gtx, i18n.T("Password"), f, "", func(gtx layout.Context) layout.Dimensions {
			return u.Input(gtx, f, guikit.StBody, 38, "", nil)
		})
	}, 1,
		choice{Label: i18n.T("Cancel"), Kind: guikit.ButtonSecondary, Do: func() { ch <- answer{} }},
		choice{Label: i18n.T("OK"), Kind: guikit.ButtonPrimary, Enabled: func() bool { return f.Editor.Text() != "" }, Do: func() {
			ch <- answer{pw: f.Editor.Text(), ok: true}
		}})
	u.open(d)
	var a answer
	select {
	case a = <-ch:
	case <-ctx.Done():
		u.closeIf(d)
	}
	f.Editor.SetText("")
	if prev != nil {
		u.mu.Lock()
		if u.dlg == nil {
			u.dlg = prev
		}
		u.mu.Unlock()
		u.hooks.Invalidate()
	}
	return a.pw, a.ok
}

// openAdd asks which finger to add, then enrolls it.
func (s *fingerSection) openAdd() {
	u := s.u
	st, _, _ := s.state()
	enrolled := map[string]bool{}
	for _, f := range st.Enrolled {
		enrolled[f] = true
	}
	s.picked = ""
	for _, n := range []string{"right-index-finger", "left-index-finger"} {
		if !enrolled[n] {
			s.picked = n
			break
		}
	}
	if s.picked == "" {
		for _, n := range fingerNames {
			if !enrolled[n] {
				s.picked = n
				break
			}
		}
	}
	u.askForm(i18n.T(fpPickTitle), i18n.T("Which finger? You can add more than one."), func(gtx layout.Context) layout.Dimensions {
		return s.pickForm(gtx, enrolled)
	}, 1,
		choice{Label: i18n.T("Cancel"), Kind: guikit.ButtonSecondary},
		choice{Label: i18n.T("Next"), Kind: guikit.ButtonPrimary, Enabled: func() bool { return s.picked != "" }, Do: func() { s.enroll(s.picked, st) }})
}

// pickForm is the fingers in two columns, left hand and right hand; an
// enrolled finger cannot be picked.
func (s *fingerSection) pickForm(gtx layout.Context, enrolled map[string]bool) layout.Dimensions {
	u := s.u
	for _, n := range fingerNames {
		if s.clickFor(s.pick, n).Clicked(gtx) && !enrolled[n] {
			s.picked = n
		}
	}
	hand := func(names []string, title string) layout.Widget {
		return func(gtx layout.Context) layout.Dimensions {
			var c col
			c.add(u.caps(title))
			for _, n := range names {
				n := n
				c.gap(6)
				c.add(func(gtx layout.Context) layout.Dimensions {
					label := fingerShort(n)
					if enrolled[n] {
						label = i18n.Tf("%s (added)", label)
					}
					return u.Card(gtx, s.clickFor(s.pick, n), s.picked == n, layout.Inset{Top: 8, Bottom: 8, Left: 12, Right: 12}, func(gtx layout.Context) layout.Dimensions {
						return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
							layout.Rigid(func(gtx layout.Context) layout.Dimensions { return u.Radio(gtx, s.picked == n) }),
							layout.Rigid(layout.Spacer{Width: 10}.Layout),
							layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
								c := u.Colors().Text
								if enrolled[n] {
									c = u.Colors().Dim
								}
								return u.Label(gtx, guikit.StBody, c, label)
							}),
						)
					})
				})
			}
			return c.layout(gtx)
		}
	}
	return layout.Flex{}.Layout(gtx,
		layout.Flexed(1, hand(fingerNames[:5], i18n.T("Left"))),
		layout.Rigid(layout.Spacer{Width: 14}.Layout),
		layout.Flexed(1, hand(fingerNames[5:], i18n.T("Right"))),
	)
}

// enrollText is the enrollment dialog's lines: what to do and how far.
func (s *fingerSection) enrollText(st FingerState) (string, string) {
	s.u.mu.Lock()
	finger, passed, msg := s.enrolling, s.passed, s.message
	s.u.mu.Unlock()
	// TRANSLATORS: %s is a finger, such as "your right index finger".
	do := i18n.Tf("Touch the reader with %s.", fingerPhrase(finger))
	if st.Swipe {
		// TRANSLATORS: %s is a finger, such as "your right index finger".
		do = i18n.Tf("Swipe the reader with %s.", fingerPhrase(finger))
	}
	if msg != "" {
		do = msg
	}
	far := ""
	if st.Stages > 0 {
		far = i18n.Tf("%[1]d of %[2]d scans", min(passed, st.Stages), st.Stages)
	} else if passed > 0 {
		far = i18n.Tf("%d scans", passed)
	}
	return do, far
}

// enroll runs fprintd's enrollment of finger behind a dialog that follows
// it; Cancel stops it.
func (s *fingerSection) enroll(finger string, st FingerState) {
	u, f := s.u, s.u.hooks.Fingers
	if f == nil || finger == "" {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	u.mu.Lock()
	s.enrolling, s.passed, s.message, s.cancel = finger, 0, "", cancel
	u.mu.Unlock()
	d := newDialog(i18n.T(fpEnrollHead), fingerLabel(finger), func(gtx layout.Context) layout.Dimensions {
		do, far := s.enrollText(st)
		var c col
		c.add(func(gtx layout.Context) layout.Dimensions {
			return u.LabelLines(gtx, guikit.StCard, u.Colors().Text, 1.4, do)
		})
		if far != "" {
			c.gap(8)
			c.add(u.dim(far))
		}
		return c.layout(gtx)
	}, 0, choice{Label: i18n.T("Cancel"), Kind: guikit.ButtonSecondary, Do: cancel})
	u.open(d)
	go func() {
		err := f.Enroll(ctx, finger, s.askPassword, func(step EnrollStep) {
			u.mu.Lock()
			s.passed = step.Passed
			if step.Message != "" {
				s.message = step.Message
			}
			u.mu.Unlock()
			u.hooks.Invalidate()
		})
		u.mu.Lock()
		s.enrolling, s.cancel = "", nil
		u.mu.Unlock()
		u.closeIf(d)
		switch {
		case ctx.Err() != nil:
		case err != nil:
			u.tell(i18n.T("Could not add the fingerprint"),
				// TRANSLATORS: %[1]s is a finger ("your right index finger"), %[2]s the reason.
				i18n.Tf("Isotope could not add %[1]s: %[2]s", fingerPhrase(finger), describeFprintEnroll(err)))
		default:
			u.tell(i18n.T("Fingerprint added"), i18n.Tf("%s now unlocks the screen and approves sudo and administrator prompts.", fingerLabel(finger)))
		}
		cancel()
		s.load()
	}()
}

// describeFprintEnroll is describeFprint for the errors Enroll makes.
func describeFprintEnroll(err error) string {
	if dbusErrName(err) != "" {
		return describeFprint(err)
	}
	return err.Error() + "."
}

func (s *fingerSection) openRemove(finger string) {
	u, f := s.u, s.u.hooks.Fingers
	if f == nil {
		return
	}
	u.ask(i18n.Tf("Remove %s?", fingerLabel(finger)), i18n.T("It will no longer unlock the screen or approve prompts."),
		choice{Label: i18n.T("Cancel"), Kind: guikit.ButtonSecondary},
		choice{Label: i18n.T("Remove"), Kind: guikit.ButtonDanger, Do: func() {
			u.async(func() {
				if err := f.Delete(context.Background(), finger, s.askPassword); err != nil {
					u.tell(i18n.T("Could not remove the fingerprint"), i18n.Tf("Isotope could not remove %[1]s: %[2]s", fingerPhrase(finger), describeFprint(err)))
				}
				s.load()
			})
		}})
}

func (s *fingerSection) update(gtx layout.Context) {
	if s.install.Clicked(gtx) {
		s.u.show(PageExtensions)
	}
	if s.add.Clicked(gtx) {
		s.openAdd()
	}
	st, _, _ := s.state()
	for _, n := range st.Enrolled {
		if s.clickFor(s.remove, n).Clicked(gtx) {
			s.openRemove(n)
		}
	}
}

// body is the section: its title and a panel.
func (s *fingerSection) body() layout.Widget {
	u := s.u
	st, loaded, loadErr := s.state()
	return func(gtx layout.Context) layout.Dimensions {
		var c col
		c.add(u.caps(i18n.T("Fingerprints")))
		c.gap(10)
		switch {
		case !loaded:
			c.add(u.spinnerLine(i18n.T("Looking for a fingerprint reader…")))
		case loadErr != "":
			c.add(u.errorBox(loadErr))
		case !st.Installed:
			sub := i18n.T(fpNoExt)
			if !u.sys.Admin {
				sub = i18n.T(fpNoExtUser)
			}
			c.add(u.panel(func(gtx layout.Context) layout.Dimensions {
				var p col
				p.add(u.row(i18n.T("Fingerprint sign-in is not installed"), sub, nil))
				p.gap(10)
				p.add(u.buttons(guikit.Action{Click: &s.install, Kind: guikit.ButtonSecondary, Label: i18n.T("Install the fingerprint extension")}))
				return p.layout(gtx)
			}))
		case st.NeedsRestart:
			c.add(u.notice(i18n.T(fpRestart)))
		case st.Reader == "":
			c.add(u.panel(u.row(i18n.T("No fingerprint reader"), i18n.T(fpNoReader), nil)))
		default:
			c.add(u.panel(func(gtx layout.Context) layout.Dimensions {
				var p col
				sub := i18n.T("No fingerprints yet.")
				if n := len(st.Enrolled); n > 0 {
					sub = i18n.Nf("%d fingerprint", "%d fingerprints", n, n)
				}
				p.add(u.row(st.Reader, sub, u.buttons(guikit.Action{Click: &s.add, Kind: guikit.ButtonSecondary, Label: i18n.T("Add a fingerprint…")})))
				for _, n := range st.Enrolled {
					n := n
					p.gap(8)
					p.add(u.row(fingerLabel(n), "", u.buttons(guikit.Action{Click: s.clickFor(s.remove, n), Kind: guikit.ButtonGhost, Label: i18n.T("Remove…")})))
				}
				p.gap(12)
				p.add(u.dim(i18n.T(fpNotNeeded)))
				return p.layout(gtx)
			}))
		}
		return c.layout(gtx)
	}
}
