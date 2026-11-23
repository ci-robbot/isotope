package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func appearanceUI(t *testing.T, d Desktop, r *fakeRunner) (*UI, *recorder, *appearancePage) {
	t.Helper()
	u, rec := newRecorded(NewFlow(d), d)
	u.hooks.Runner, u.hooks.Home = r, t.TempDir()
	to(u, PAppearance)
	p := u.page().(*appearancePage)
	p.start()
	p.wait()
	return u, rec, p
}

func TestAppearanceShowsWhatIsSet(t *testing.T) {
	get := "gsettings get org.gnome.desktop.interface color-scheme"
	_, _, p := appearanceUI(t, Sway, &fakeRunner{out: map[string]string{get: "'prefer-light'\n"}})
	if p.dark {
		t.Fatal("the page must show the light scheme that is set")
	}
	_, _, p = appearanceUI(t, Sway, &fakeRunner{fail: []string{"gsettings"}})
	if !p.dark {
		t.Fatal("unknown means dark")
	}
}

func TestAppearanceContinueApplies(t *testing.T) {
	u, _, p := appearanceUI(t, Cosmic, &fakeRunner{})
	p.choose(false)
	p.Continue()
	p.wait()
	p.mu.Lock()
	done := p.applied
	p.mu.Unlock()
	if !done {
		t.Fatalf("not applied: %q", p.err)
	}
	if got := readFile(t, filepath.Join(u.hooks.Home, ".config/cosmic/com.system76.CosmicTheme.Mode/v1/is_dark")); got != "false" {
		t.Errorf("is_dark = %q", got)
	}
}

func TestAppearanceFailureStaysWithAnError(t *testing.T) {
	u, _, p := appearanceUI(t, Sway, &fakeRunner{fail: []string{"gsettings set"}})
	p.Continue()
	p.wait()
	if u.flow.Current() != PAppearance || !strings.Contains(p.err, "Could not change the appearance") || p.applied {
		t.Fatalf("page %s, err %q, applied %v", u.flow.Current(), p.err, p.applied)
	}
}

func TestAppearanceSwayLeavesCosmicAlone(t *testing.T) {
	u, _, p := appearanceUI(t, Sway, &fakeRunner{})
	p.Continue()
	p.wait()
	if _, err := os.Stat(filepath.Join(u.hooks.Home, ".config", "cosmic")); err == nil {
		t.Fatal("sway wrote COSMIC files")
	}
}
