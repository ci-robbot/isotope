package guikit

import (
	"image"
	"os"
	"testing"

	"gioui.org/io/input"
	"gioui.org/io/pointer"
	"gioui.org/io/system"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"
)

func TestHeaderHeightFromDensity(t *testing.T) {
	env := func(string) string { return "" }
	for want, val := range map[float32]string{36: "Compact", 40: "Standard", 48: "Spacious"} {
		read := func(p string) ([]byte, error) {
			if p == "/home/u/.config/cosmic/"+headerKey {
				return []byte(val + "\n"), nil
			}
			return nil, os.ErrNotExist
		}
		if got := HeaderHeight(env, "/home/u", read); got != want {
			t.Errorf("%s: %v, want %v", val, got, want)
		}
	}
	none := func(string) ([]byte, error) { return nil, os.ErrNotExist }
	if got := HeaderHeight(env, "/home/u", none); got != TitleBarHeight {
		t.Errorf("no key: %v", got)
	}
}

// frame lays w out once in a router-backed context of size w x h.
func frame(r *input.Router, w, h int, lay func(gtx layout.Context)) {
	ops := new(op.Ops)
	gtx := layout.Context{Ops: ops, Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1},
		Constraints: layout.Exact(image.Pt(w, h)), Source: r.Source()}
	lay(gtx)
	r.Frame(ops)
}

func click(r *input.Router, at image.Point) {
	pos := layout.FPt(at)
	r.Queue(
		pointer.Event{Kind: pointer.Press, Source: pointer.Mouse, Buttons: pointer.ButtonPrimary, Position: pos},
		pointer.Event{Kind: pointer.Release, Source: pointer.Mouse, Buttons: pointer.ButtonPrimary, Position: pos},
	)
}

func TestNavListClickPicksRow(t *testing.T) {
	th := NewTheme()
	n := &NavList{Items: []NavEntry{{"Apps", IconApps}, {"Updates", IconUpdates}, {"About", IconAbout}}}
	var r input.Router
	hit := -1
	lay := func(gtx layout.Context) {
		if i := n.Clicked(gtx); i >= 0 {
			hit = i
		}
		th.NavList(gtx, n)
	}
	frame(&r, 400, 300, lay)
	// Rows are about 38 dp from an 8 dp inset: the second row's middle.
	click(&r, image.Pt(60, 8+40+19))
	frame(&r, 400, 300, lay)
	if hit != 1 {
		t.Fatalf("clicked row %d, want 1", hit)
	}
}

func TestHeaderBarNavButton(t *testing.T) {
	th := NewTheme()
	h := &HeaderBar{Title: "Isotope System", Left: true, ShowNavButton: true}
	var r input.Router
	toggled := false
	var acts system.Action
	lay := func(gtx layout.Context) {
		a, nav := h.Update(gtx)
		acts |= a
		toggled = toggled || nav
		h.Layout(gtx, th)
	}
	frame(&r, 800, 40, lay)
	// Left: close, minimize, maximize (16 dp, 8 apart) from 14 dp, then 14
	// dp and the 28 dp navigation button.
	click(&r, image.Pt(14+3*16+2*8+14+14, 20))
	frame(&r, 800, 40, lay)
	if !toggled {
		t.Fatal("the navigation button did not toggle")
	}
	click(&r, image.Pt(14+8, 20))
	frame(&r, 800, 40, lay)
	if acts&system.ActionClose == 0 {
		t.Fatalf("close not asked: %v", acts)
	}
}

func TestEveryIconDraws(t *testing.T) {
	for _, name := range Icons {
		gtx := testContext(64, 64)
		d := Icon(gtx, name, 16, ColText)
		if d.Size != image.Pt(16, 16) {
			t.Errorf("%s: size %v", name, d.Size)
		}
	}
}
