package main

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func accessUI(t *testing.T, d Desktop, r *fakeRunner) (*UI, *accessPage) {
	t.Helper()
	u, _ := newRecorded(NewFlow(d), d)
	u.hooks.Runner, u.hooks.Home = r, t.TempDir()
	to(u, PAccessibility)
	p := u.page().(*accessPage)
	p.start()
	p.wait()
	return u, p
}

func TestAccessShowsWhatIsSet(t *testing.T) {
	_, p := accessUI(t, Sway, &fakeRunner{out: map[string]string{
		swayOutputs: `[{"name":"eDP-1","active":true,"scale":1.5}]`,
		textGet:     "1.25\n",
	}})
	if p.scale != 1.5 || !p.larger {
		t.Fatalf("scale %v, larger %v", p.scale, p.larger)
	}
}

func TestAccessContinueApplies(t *testing.T) {
	u, p := accessUI(t, Sway, &fakeRunner{})
	p.chooseScale(1.25)
	p.toggleLarger()
	p.Continue()
	p.wait()
	if !p.applied {
		t.Fatalf("not applied: %q", p.err)
	}
	r := u.hooks.Runner.(*fakeRunner)
	for _, c := range []string{"swaymsg output * scale 1.25", "gsettings set org.gnome.desktop.interface text-scaling-factor 1.25"} {
		if !slices.Contains(r.called(), c) {
			t.Errorf("did not run %q: %q", c, r.called())
		}
	}
	if got := readFile(t, filepath.Join(u.hooks.Home, ".config/sway/isotope-scale.conf")); !strings.Contains(got, "output * scale 1.25") {
		t.Errorf("isotope-scale.conf:\n%s", got)
	}
}

// The scale that is already set is not set again: on i3 that would
// restart i3 for nothing.
func TestAccessUnchangedScaleRunsNothing(t *testing.T) {
	u, p := accessUI(t, I3, &fakeRunner{out: map[string]string{xrdbQuery: "Xft.dpi:\t96\n"}})
	p.Continue()
	p.wait()
	if !p.applied {
		t.Fatalf("not applied: %q", p.err)
	}
	for _, c := range u.hooks.Runner.(*fakeRunner).called() {
		if strings.HasPrefix(c, "xrdb -merge") || strings.HasPrefix(c, "i3-msg") {
			t.Fatalf("ran %q for an unchanged scale", c)
		}
	}
}

func TestAccessFailureStaysWithAnError(t *testing.T) {
	u, p := accessUI(t, Sway, &fakeRunner{fail: []string{"swaymsg output"}})
	p.chooseScale(2)
	p.Continue()
	p.wait()
	if u.flow.Current() != PAccessibility || !strings.Contains(p.err, "Could not change the display scale") || p.applied {
		t.Fatalf("page %s, err %q, applied %v", u.flow.Current(), p.err, p.applied)
	}
}

// After the scale applied and the text size failed, a second Continue sets
// only the text size: i3 is not restarted twice.
func TestAccessRetryAfterPartialFailure(t *testing.T) {
	r := &fakeRunner{out: map[string]string{xrdbQuery: "Xft.dpi:\t96\n"}, fail: []string{"gsettings set"}}
	_, p := accessUI(t, I3, r)
	p.chooseScale(1.5)
	p.Continue()
	p.wait()
	if !strings.Contains(p.err, "Could not change the text size") || p.applied {
		t.Fatalf("err %q, applied %v", p.err, p.applied)
	}
	r.mu.Lock()
	r.fail = nil
	r.mu.Unlock()
	p.Continue()
	p.wait()
	if !p.applied {
		t.Fatalf("not applied: %q", p.err)
	}
	n := 0
	for _, c := range r.called() {
		if c == "i3-msg restart" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("i3 restarted %d times: %q", n, r.called())
	}
}

// COSMIC has no "Larger text": Continue sets no gsettings key.
func TestAccessCosmicSetsNoTextKey(t *testing.T) {
	r := &fakeRunner{out: map[string]string{cosmicList: cosmicKDL}}
	_, p := accessUI(t, Cosmic, r)
	p.Continue()
	p.wait()
	if !p.applied {
		t.Fatalf("not applied: %q", p.err)
	}
	for _, c := range r.called() {
		if strings.HasPrefix(c, "gsettings") {
			t.Fatalf("ran %q on COSMIC", c)
		}
	}
}
