package guikit

import (
	"image"
	"os"
	"testing"

	"gioui.org/io/system"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"
	"gioui.org/widget"
)

func TestFontsHaveTheThreeFaces(t *testing.T) {
	seen := map[string]bool{}
	for _, f := range Fonts() {
		seen[string(f.Font.Typeface)] = true
	}
	for _, face := range []string{string(FaceTitle), string(FaceBody), string(FaceMono)} {
		if !seen[face] {
			t.Errorf("no %s in the embedded fonts (have %v)", face, seen)
		}
	}
}

func TestOverMixesInSRGB(t *testing.T) {
	if got := Over(RGB(0x000000), RGB(0xffffff), 0.5); got != RGB(0x808080) {
		t.Fatalf("half white on black: %v", got)
	}
	if got := Over(ColWindow, ColAccent, 0); got != ColWindow {
		t.Fatalf("nothing laid on the window: %v", got)
	}
}

func TestMoveSelectionStepsThroughTheShownEntries(t *testing.T) {
	var l widget.List
	shown := []int{3, 5, 9}
	sel := MoveSelection(shown, -1, true, &l)
	if sel != 3 {
		t.Fatalf("from nothing: %d, want the first shown", sel)
	}
	for _, want := range []int{5, 9, 9} {
		if sel = MoveSelection(shown, sel, true, &l); sel != want {
			t.Fatalf("down: %d, want %d", sel, want)
		}
	}
	for _, want := range []int{5, 3, 3} {
		if sel = MoveSelection(shown, sel, false, &l); sel != want {
			t.Fatalf("up: %d, want %d", sel, want)
		}
	}
	if sel = MoveSelection(nil, 7, true, &l); sel != 7 {
		t.Fatalf("empty list moved the selection to %d", sel)
	}
}

func testContext(w, h int) layout.Context {
	return layout.Context{
		Ops:         new(op.Ops),
		Metric:      unit.Metric{PxPerDp: 1, PxPerSp: 1},
		Constraints: layout.Constraints{Max: image.Pt(w, h)},
	}
}

func TestKeyCapsGrowWithTheKeys(t *testing.T) {
	th := NewTheme()
	size := func(keys ...string) image.Point { return th.KeyCaps(testContext(1000, 200), keys).Size }
	one, two, alt := size("Super"), size("Super", "D"), size("Super", "D / Space")
	if one.X <= 0 || one.Y <= 0 {
		t.Fatalf("one key: %v", one)
	}
	if two.X <= one.X || alt.X <= two.X {
		t.Fatalf("widths %d, %d, %d: each added cap must widen the row", one.X, two.X, alt.X)
	}
	if one.Y != two.Y || two.Y != alt.Y {
		t.Fatalf("heights %d, %d, %d differ", one.Y, two.Y, alt.Y)
	}
	// A cap is at least 30 dp wide, so a one-letter key is not a sliver.
	if d := size("D"); d.X < 30 {
		t.Fatalf("D is %d wide", d.X)
	}
}

func TestConfirmCoversTheWindow(t *testing.T) {
	th := NewTheme()
	var yes, no widget.Clickable
	dims := th.Confirm(testContext(800, 600), "Skip setup?", "Words.", []Action{
		{Click: &no, Kind: ButtonSecondary, Label: "Keep going"},
		{Click: &yes, Kind: ButtonPrimary, Label: "Skip"},
	})
	if dims.Size != image.Pt(800, 600) {
		t.Fatalf("Confirm is %v, want the whole 800x600", dims.Size)
	}
	// On a window narrower than the card, the card shrinks to fit.
	if d := th.Confirm(testContext(300, 400), "Skip setup?", "Words.", nil); d.Size != image.Pt(300, 400) {
		t.Fatalf("narrow: %v", d.Size)
	}
}

func TestControlOrder(t *testing.T) {
	l, r := ControlOrder(true), ControlOrder(false)
	if l[0] != system.ActionClose || l[2] != system.ActionMaximize {
		t.Errorf("left order = %v", l)
	}
	if r[0] != system.ActionMinimize || r[2] != system.ActionClose {
		t.Errorf("right order = %v", r)
	}
}

func TestControlsLeft(t *testing.T) {
	files := map[string]string{}
	read := func(p string) ([]byte, error) {
		if v, ok := files[p]; ok {
			return []byte(v), nil
		}
		return nil, os.ErrNotExist
	}
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	cosmic := env(map[string]string{"XDG_CURRENT_DESKTOP": "COSMIC"})
	sys := "/usr/share/cosmic/com.system76.CosmicTk/v1/window_controls_left"
	usr := "/home/u/.config/cosmic/com.system76.CosmicTk/v1/window_controls_left"

	if ControlsLeft(env(map[string]string{"XDG_CURRENT_DESKTOP": "sway"}), "/home/u", read) {
		t.Error("sway: buttons left")
	}
	if ControlsLeft(cosmic, "/home/u", read) {
		t.Error("COSMIC without the key: buttons left")
	}
	files[sys] = "true\n"
	if !ControlsLeft(cosmic, "/home/u", read) {
		t.Error("COSMIC with the system key: want left")
	}
	files[usr] = "false"
	if ControlsLeft(cosmic, "/home/u", read) {
		t.Error("the user's false must win")
	}
	delete(files, usr)
	xdg := env(map[string]string{"XDG_CURRENT_DESKTOP": "COSMIC", "XDG_CONFIG_HOME": "/cfg"})
	files["/cfg/cosmic/com.system76.CosmicTk/v1/window_controls_left"] = "false"
	if ControlsLeft(xdg, "/home/u", read) {
		t.Error("XDG_CONFIG_HOME override ignored")
	}
}

func TestTitleBarLayoutHeight(t *testing.T) {
	th := &Theme{Shaper: NewShaper()}
	for _, left := range []bool{true, false} {
		bar := &TitleBar{Title: "Welcome to Isotope", Left: left}
		gtx := layout.Context{
			Ops:         new(op.Ops),
			Metric:      unit.Metric{PxPerDp: 1, PxPerSp: 1},
			Constraints: layout.Exact(image.Pt(800, 600)),
		}
		d := bar.Layout(gtx, th)
		if d.Size != image.Pt(800, TitleBarHeight) {
			t.Errorf("left=%v: size %v", left, d.Size)
		}
		bar.Update(gtx)
	}
}
