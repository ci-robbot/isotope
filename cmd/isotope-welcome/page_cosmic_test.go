package main

import (
	"github.com/kairos-community/isotope/pkg/guikit"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func layoutUI(t *testing.T, r *fakeRunner) (*UI, *layoutPage) {
	t.Helper()
	u, _ := newRecorded(NewFlow(Cosmic), Cosmic)
	u.hooks.Runner, u.hooks.Home, u.hooks.LayoutsDir = r, t.TempDir(), layoutFixtures
	to(u, PLayout)
	p := u.page().(*layoutPage)
	p.start()
	p.wait()
	return u, p
}

func TestLayoutPageShowsWhatIsSet(t *testing.T) {
	u, p := layoutUI(t, &fakeRunner{})
	if len(p.cards) != 2 || p.selected != "" {
		t.Fatalf("cards %d, selected %q: nothing is installed yet", len(p.cards), p.selected)
	}
	for _, c := range p.cards {
		if !c.ok {
			t.Errorf("icon of %s not decoded", c.ID)
		}
	}
	if err := ApplyLayout(&fakeRunner{}, u.hooks.Home, fixtureLayout(t, "bottom-panel")); err != nil {
		t.Fatal(err)
	}
	p2 := &layoutPage{basePage: basePage{u}}
	p2.start()
	p2.wait()
	if p2.selected != "bottom-panel" {
		t.Fatalf("selected %q, want the installed bottom-panel", p2.selected)
	}
}

func TestLayoutPageContinueAppliesAndRestartsPanel(t *testing.T) {
	r := &fakeRunner{}
	u, p := layoutUI(t, r)
	p.choose("bottom-panel")
	p.Continue()
	p.wait()
	if !p.applied {
		t.Fatalf("not applied: %q", p.err)
	}
	if got := readFile(t, filepath.Join(u.hooks.Home, ".config/cosmic/com.system76.CosmicPanel.Panel/v1/anchor")); got != "Bottom" {
		t.Errorf("anchor %q", got)
	}
	if c := r.called(); len(c) != 1 || c[0] != "pkill -x cosmic-panel" {
		t.Errorf("commands %q", c)
	}
}

func TestLayoutPageUnchangedChoiceLeavesThePanel(t *testing.T) {
	r := &fakeRunner{}
	u, p := layoutUI(t, r)
	ApplyLayout(&fakeRunner{}, u.hooks.Home, fixtureLayout(t, "bottom-panel"))
	p.current, p.selected = "bottom-panel", "bottom-panel"
	p.Continue()
	if len(r.called()) != 0 || u.flow.Current() != PWorkflow {
		t.Fatalf("commands %q, page %s", r.called(), u.flow.Current())
	}
	if len(u.flow.Outcome.Applied) != 1 || u.flow.Outcome.Applied[0] != PLayout {
		t.Errorf("applied %v", u.flow.Outcome.Applied)
	}
}

func TestLayoutPageNoChoiceGoesOnUntouched(t *testing.T) {
	r := &fakeRunner{}
	u, p := layoutUI(t, r)
	p.Continue()
	if len(r.called()) != 0 || u.flow.Current() != PWorkflow || len(u.flow.Outcome.Applied) != 0 {
		t.Fatalf("commands %q, page %s, applied %v", r.called(), u.flow.Current(), u.flow.Outcome.Applied)
	}
}

func TestLayoutPageFailureStaysWithAnError(t *testing.T) {
	u, p := layoutUI(t, &fakeRunner{})
	u.hooks.LayoutsDir = layoutFixtures
	// A layout whose directory vanished cannot be copied.
	p.cards[0].Layout.Dir = filepath.Join(t.TempDir(), "gone")
	p.choose(p.cards[0].ID)
	p.Continue()
	p.wait()
	if u.flow.Current() != PLayout || p.applied || !strings.Contains(p.err, "Could not change the layout") {
		t.Fatalf("page %s applied %v err %q", u.flow.Current(), p.applied, p.err)
	}
}

// Workflow and Launcher write nothing upstream, so they write nothing here.
func TestWorkflowAndLauncherSetNothing(t *testing.T) {
	r := &fakeRunner{}
	u, _ := newRecorded(NewFlow(Cosmic), Cosmic)
	u.hooks.Runner, u.hooks.Home = r, t.TempDir()
	for _, id := range []PageID{PWorkflow, PLauncher} {
		to(u, id)
		pg := u.page()
		if _, ok := pg.(*infoPage); !ok {
			t.Fatalf("%s is %T", id, pg)
		}
		lead, trail := pg.Footer(nav{Back: &guikitAction, Skip: &guikitAction, Continue: &guikitAction})
		if len(lead) != 1 || len(trail) != 1 {
			t.Errorf("%s footer %d+%d, want Back and Continue only", id, len(lead), len(trail))
		}
		pg.Continue()
	}
	if len(r.called()) != 0 {
		t.Errorf("commands %q", r.called())
	}
	if _, err := os.Stat(filepath.Join(u.hooks.Home, ".config")); err == nil {
		t.Error("a page wrote settings")
	}
	if len(u.flow.Outcome.Applied) != 0 {
		t.Errorf("applied %v: nothing was set", u.flow.Outcome.Applied)
	}
}

var guikitAction guikit.Action
