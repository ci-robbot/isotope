package main

import (
	"gioui.org/io/key"
	"gioui.org/layout"
	"gioui.org/widget"

	"github.com/kairos-community/isotope/pkg/guikit"
	"github.com/kairos-community/isotope/pkg/i18n"
)

// choice is one button of a dialog: its label, its look and what it does.
// Do runs on the UI goroutine after the dialog closes; nil only closes it.
// Enabled nil means always enabled; a disabled choice cannot be picked.
type choice struct {
	Label   string
	Kind    guikit.ButtonKind
	Do      func()
	Enabled func() bool
}

func (c choice) enabled() bool { return c.Enabled == nil || c.Enabled() }

// dialog is a question over the window (guikit's Confirm).
type dialog struct {
	title, body string
	choices     []choice
	form        layout.Widget
	// focus is the index of the choice that gets the focus.
	focus  int
	clicks []widget.Clickable
	// cancel is the index of the choice Esc picks, -1 for none.
	cancel  int
	focused bool
	// key is the state of a key dialog (keydialog.go), nil for others.
	key *keyCheck
}

// ask shows a question over the window. The first choice is the one Esc
// picks; the last gets the focus. Safe from any goroutine.
func (u *UI) ask(title, body string, choices ...choice) {
	u.askForm(title, body, nil, len(choices)-1, choices...)
}

// askForm is ask with a form and the index of the choice that gets the focus.
func (u *UI) askForm(title, body string, form layout.Widget, focus int, choices ...choice) {
	u.open(newDialog(title, body, form, focus, choices...))
}

func newDialog(title, body string, form layout.Widget, focus int, choices ...choice) *dialog {
	d := &dialog{title: title, body: body, form: form, focus: max(focus, 0), choices: choices, clicks: make([]widget.Clickable, len(choices)), cancel: -1}
	if len(choices) > 0 {
		d.cancel = 0
	}
	return d
}

// open shows d over the window.
func (u *UI) open(d *dialog) {
	u.mu.Lock()
	u.dlg = d
	u.mu.Unlock()
	u.hooks.Invalidate()
}

// tell is a message with one OK button.
func (u *UI) tell(title, body string) {
	u.ask(title, body, choice{Label: i18n.T("OK"), Kind: guikit.ButtonPrimary})
}

func (u *UI) closeDialog() {
	u.mu.Lock()
	u.dlg = nil
	u.mu.Unlock()
}

// question is the open dialog, for the tests.
func (u *UI) question() *dialog {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.dlg
}

// pick answers the dialog with its choice i, as a click does.
// A disabled choice is ignored.
func (u *UI) pick(d *dialog, i int) {
	if !d.choices[i].enabled() {
		return
	}
	u.mu.Lock()
	if u.dlg == d {
		u.dlg = nil
	}
	u.mu.Unlock()
	if f := d.choices[i].Do; f != nil {
		f()
	}
}

func (d *dialog) update(gtx layout.Context, u *UI) {
	if !d.focused && len(d.clicks) > 0 {
		gtx.Execute(key.FocusCmd{Tag: &d.clicks[min(d.focus, len(d.clicks)-1)]})
		d.focused = true
	}
	for i := range d.clicks {
		if d.clicks[i].Clicked(gtx) {
			u.pick(d, i)
			return
		}
	}
}

func (d *dialog) layout(gtx layout.Context, u *UI) {
	acts := make([]guikit.Action, len(d.choices))
	for i, c := range d.choices {
		acts[i] = guikit.Action{Click: &d.clicks[i], Kind: c.Kind, Label: c.Label, Disabled: !c.enabled()}
	}
	u.ConfirmForm(gtx, d.title, d.body, d.form, acts)
}

// closeIf closes d if it is still the open dialog; another dialog stays.
// Safe from any goroutine.
func (u *UI) closeIf(d *dialog) {
	u.mu.Lock()
	if u.dlg == d {
		u.dlg = nil
	}
	u.mu.Unlock()
	u.hooks.Invalidate()
}
