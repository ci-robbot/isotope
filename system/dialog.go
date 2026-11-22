package main

import (
	"gioui.org/io/key"
	"gioui.org/layout"
	"gioui.org/widget"

	"github.com/kairos-community/isotope/guikit"
)

// choice is one button of a dialog: its label, its look and what it does.
// do runs on the UI goroutine after the dialog closes; nil only closes it.
type choice struct {
	Label string
	Kind  guikit.ButtonKind
	Do    func()
}

// dialog is a question over the window (guikit's Confirm).
type dialog struct {
	title, body string
	choices     []choice
	clicks      []widget.Clickable
	// cancel is the index of the choice Esc picks, -1 for none.
	cancel  int
	focused bool
}

// ask shows a question over the window. The first choice is the one Esc
// picks; the last gets the focus. Safe from any goroutine.
func (u *UI) ask(title, body string, choices ...choice) {
	d := &dialog{title: title, body: body, choices: choices, clicks: make([]widget.Clickable, len(choices)), cancel: -1}
	if len(choices) > 0 {
		d.cancel = 0
	}
	u.mu.Lock()
	u.dlg = d
	u.mu.Unlock()
	u.hooks.Invalidate()
}

// tell is a message with one OK button.
func (u *UI) tell(title, body string) {
	u.ask(title, body, choice{Label: "OK", Kind: guikit.ButtonPrimary})
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
func (u *UI) pick(d *dialog, i int) {
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
		gtx.Execute(key.FocusCmd{Tag: &d.clicks[len(d.clicks)-1]})
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
		acts[i] = guikit.Action{Click: &d.clicks[i], Kind: c.Kind, Label: c.Label}
	}
	u.Confirm(gtx, d.title, d.body, acts)
}
