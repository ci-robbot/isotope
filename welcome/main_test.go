package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseArgs(t *testing.T) {
	for _, a := range []string{"--login", "all", "apps", "backup", "language", "network", "appearance", "accessibility", "shortcuts"} {
		if p, ok := ParseArgs([]string{a}); !ok || string(p) != a {
			t.Errorf("%q: %q %v", a, p, ok)
		}
	}
	// No argument is the login hook: a config from before Isotope Welcome
	// still starts a bare `isotope-welcome`, which must stay stamp-gated.
	if p, ok := ParseArgs(nil); !ok || p != PageLogin {
		t.Errorf("no argument: %q %v, want --login", p, ok)
	}
	// "all" is the whole flow without the gate (`isotope welcome`).
	if p, _ := ParseArgs([]string{"all"}); p != PageAll {
		t.Errorf("all: %q, want PageAll", p)
	}
	for _, bad := range [][]string{{"--nonsense"}, {"apps", "x"}} {
		if _, ok := ParseArgs(bad); ok {
			t.Errorf("%v accepted", bad)
		}
	}
}

func TestVersionLine(t *testing.T) {
	p := filepath.Join(t.TempDir(), "release")
	if err := os.WriteFile(p, []byte("FLAVOR=cosmic\nGPU=full\nVERSION=v0.0.25\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := versionLine(p); got != "Isotope v0.0.25 · cosmic" {
		t.Errorf("versionLine = %q", got)
	}
	if got := versionLine(filepath.Join(t.TempDir(), "none")); got != "Isotope" {
		t.Errorf("no file: %q", got)
	}
}

func TestFlowFor(t *testing.T) {
	if got := flowFor(PageAll, Niri, "").Pages(); len(got) != len(Pages(Niri)) {
		t.Errorf("PageAll: %v", got)
	}
	if got := flowFor(PageLogin, Cosmic, "").Pages(); len(got) != len(Pages(Cosmic)) {
		t.Errorf("PageLogin: %v", got)
	}
	if got := flowFor(PageShortcuts, Sway, "").Pages(); len(got) != 1 || got[0] != PShortcuts {
		t.Errorf("shortcuts: %v", got)
	}
	if got := flowFor(PageApps, Sway, "").Pages(); len(got) != 1 || got[0] != PApps {
		t.Errorf("apps: %v", got)
	}
}

func TestSystemLocaleReadsTheFile(t *testing.T) {
	old := localeConfPath
	t.Cleanup(func() { localeConfPath = old })
	dir := t.TempDir()
	localeConfPath = dir + "/locale.conf"
	if got := systemLocale(); got != "" {
		t.Errorf("no file: %q", got)
	}
	if err := os.WriteFile(localeConfPath, []byte("LANG=de_DE.UTF-8\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := systemLocale(); got != "de_DE.UTF-8" {
		t.Errorf("got %q", got)
	}
}
