package guikit

import (
	"image"
	"testing"

	"gioui.org/io/input"
	"gioui.org/io/key"
	"gioui.org/layout"
	"gioui.org/widget"
)

func TestSplitActions(t *testing.T) {
	as := make([]Action, 4)
	for _, c := range []struct {
		n     int
		w     float32
		shown int
	}{
		{1, 600, 1}, {2, 600, 2}, {3, 600, 2}, {4, 600, 2},
		{1, 400, 1}, {2, 400, 1}, {3, 400, 1},
	} {
		shown, more := SplitActions(as[:c.n], c.w)
		if len(shown) != c.shown || len(shown)+len(more) != c.n {
			t.Errorf("%d actions at %v dp: %d shown, %d in the menu", c.n, c.w, len(shown), len(more))
		}
	}
}

func TestSplitActionsPutsDangerInTheMenu(t *testing.T) {
	ok := Action{Label: "Print test page", Kind: ButtonSecondary}
	rm := Action{Label: "Remove…", Kind: ButtonDanger}
	shown, more := SplitActions([]Action{rm, ok}, 600)
	if len(shown) != 1 || shown[0].Label != ok.Label || len(more) != 1 || more[0].Label != rm.Label {
		t.Fatalf("shown %v, more %v", shown, more)
	}
	shown, more = SplitActions([]Action{rm}, 600)
	if len(shown) != 1 || shown[0].Kind != ButtonSecondary || more != nil {
		t.Fatalf("a lone Remove: shown %v, more %v", shown, more)
	}
}

// The third action of a row is in its "⋯" menu: the button opens the
// menu, a click on the item fires the action's own Clickable and closes
// the menu, and a press outside closes it too.
func TestActionRowOverflowMenu(t *testing.T) {
	th := NewTheme()
	var test, def, rm widget.Clickable
	as := []Action{
		{Click: &test, Kind: ButtonSecondary, Label: "Print test page"},
		{Click: &def, Kind: ButtonSecondary, Label: "Make default"},
		{Click: &rm, Kind: ButtonGhost, Label: "Remove…"},
	}
	o := new(Overflow)
	var r input.Router
	removed := 0
	lay := func(gtx layout.Context) {
		if rm.Clicked(gtx) {
			removed++
		}
		th.Group(gtx, func(gtx layout.Context) layout.Dimensions {
			return th.ActionRow(gtx, "Office", "Ready", nil, as, o)
		})
	}
	const w, h = 700, 400
	frame(&r, w, h, lay)
	// The "⋯" button is the last 34 dp before the 16 dp inset; the row is
	// 12 + 34/2 dp from the top (+1 for the border).
	btn := image.Pt(w-1-16-17, 1+12+17)
	click(&r, btn)
	frame(&r, w, h, lay)
	frame(&r, w, h, lay)
	if !o.Open() {
		t.Fatal("the ⋯ button did not open the menu")
	}
	// The menu's first item: 4 dp under the button, 4 dp of padding, 36 dp lines.
	click(&r, image.Pt(w-1-16-60, btn.Y+17+4+4+18))
	frame(&r, w, h, lay)
	frame(&r, w, h, lay)
	frame(&r, w, h, lay)
	if o.Open() || removed != 1 {
		t.Fatalf("after the item: open %v, removed %d", o.Open(), removed)
	}
	click(&r, btn)
	frame(&r, w, h, lay)
	frame(&r, w, h, lay)
	click(&r, image.Pt(20, 300))
	frame(&r, w, h, lay)
	frame(&r, w, h, lay)
	if o.Open() || removed != 1 {
		t.Fatalf("a press outside: open %v, removed %d", o.Open(), removed)
	}
}

// Two actions need no menu; a narrow row moves the second into one.
func TestActionRowNarrowUsesTheMenu(t *testing.T) {
	th := NewTheme()
	var a, b widget.Clickable
	as := []Action{{Click: &a, Label: "Connect"}, {Click: &b, Label: "Forget…"}}
	o := new(Overflow)
	var r input.Router
	lay := func(w int) func(gtx layout.Context) {
		return func(gtx layout.Context) { th.ActionRow(gtx, "Home", "", nil, as, o) }
	}
	frame(&r, 360, 60, lay(360))
	click(&r, image.Pt(360-17, 30))
	frame(&r, 360, 60, lay(360))
	frame(&r, 360, 60, lay(360))
	if !o.Open() {
		t.Fatal("no menu on a narrow row with two actions")
	}
	frame(&r, 700, 60, lay(700))
	if o.Open() {
		t.Fatal("a wide row shows both buttons and keeps no menu open")
	}
}

func press(r *input.Router, name key.Name, mods key.Modifiers) {
	r.Queue(key.Event{Name: name, Modifiers: mods, State: key.Press}, key.Event{Name: name, Modifiers: mods, State: key.Release})
}

// The menu from the keyboard: Enter on the focused "⋯" opens it on its
// first enabled item; Down, Up, Tab, Home and End move between the
// enabled items, wrapping; Enter fires the item and gives the focus back
// to "⋯"; Escape closes it the same way.
func TestOverflowMenuKeyboard(t *testing.T) {
	th := NewTheme()
	var a, b, c, d, e widget.Clickable
	as := []Action{
		{Click: &a, Label: "Connect"},
		{Click: &b, Label: "Rename"},
		{Click: &c, Label: "Disabled", Disabled: true},
		{Click: &d, Label: "Share"},
		{Click: &e, Label: "Forget", Kind: ButtonDanger},
	}
	// Connect and Rename are buttons; the menu is Disabled, Share, Forget.
	o := new(Overflow)
	var r input.Router
	fired := map[string]int{}
	focusBtn := true
	lay := func(gtx layout.Context) {
		for _, x := range as {
			if x.Click.Clicked(gtx) {
				fired[x.Label]++
			}
		}
		if focusBtn {
			gtx.Execute(key.FocusCmd{Tag: &o.btn})
			focusBtn = false
		}
		th.ActionRow(gtx, "Office", "", nil, as, o)
	}
	run := func() {
		for range 3 {
			frame(&r, 700, 400, lay)
		}
	}
	_, more := SplitActions(as, 700)
	focused := func() string {
		for i := range more {
			if r.Source().Focused(&o.items[i]) {
				return more[i].Label
			}
		}
		if r.Source().Focused(&o.btn) {
			return "⋯"
		}
		return ""
	}
	run()
	if focused() != "⋯" {
		t.Fatalf("focus %q, want the ⋯ button", focused())
	}
	press(&r, key.NameReturn, 0)
	run()
	if !o.Open() {
		t.Fatal("Enter did not open the menu")
	}
	want := func(label string) {
		t.Helper()
		run()
		if got := focused(); got != label {
			t.Fatalf("focus on %q, want %q", got, label)
		}
	}
	want("Share") // the first enabled item: Disabled is skipped
	press(&r, key.NameDownArrow, 0)
	want("Forget")
	press(&r, key.NameDownArrow, 0)
	want("Share") // wraps past the disabled one
	press(&r, key.NameUpArrow, 0)
	want("Forget")
	press(&r, key.NameTab, key.ModShift)
	want("Share")
	press(&r, key.NameEnd, 0)
	want("Forget")
	press(&r, key.NameHome, 0)
	want("Share")
	press(&r, key.NameReturn, 0)
	run()
	if o.Open() || fired["Share"] != 1 || focused() != "⋯" {
		t.Fatalf("after Enter: open %v, fired %v, focus %q", o.Open(), fired, focused())
	}
	press(&r, key.NameReturn, 0)
	run()
	press(&r, key.NameEscape, 0)
	run()
	if o.Open() || focused() != "⋯" || len(fired) != 1 {
		t.Fatalf("after Escape: open %v, focus %q, fired %v", o.Open(), focused(), fired)
	}
}
