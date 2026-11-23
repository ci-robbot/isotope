package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/kairos-community/isotope/pkg/guikit"
)

func liveUI(t *testing.T, live bool) (*UI, *[][]string) {
	t.Helper()
	var started [][]string
	u := newUI(testSystem(true), guikit.NewShaper(), Hooks{Daemon: newFakeDaemon(), Live: live,
		Start: func(argv ...string) error { started = append(started, argv); return nil }}, PageUpdates)
	return u, &started
}

func TestLivePagesSayInstallFirst(t *testing.T) {
	u, started := liveUI(t, true)
	for _, p := range []Page{PageUpdates, PageDesktop, PageExtensions, PageBackup, PageSecurity, PageUsers} {
		lp, ok := u.view(p).(*livePage)
		if !ok {
			t.Fatalf("%s: the live system shows %T, want the install-first page", p, u.view(p))
		}
		if lp.page.Title() != p.Title() {
			t.Fatalf("%s: heading %q", p, lp.page.Title())
		}
		if lead, trail := lp.Footer(); len(lead)+len(trail) != 0 {
			t.Fatalf("%s: footer buttons on the live page", p)
		}
		u.show(p)
		u.Layout(gtxOf(1100, 720))
	}
	if liveText != "Not available on the live system. Install Isotope first." {
		t.Fatalf("text %q", liveText)
	}
	u.view(PageUsers).(*livePage).startInstaller()
	if len(*started) != 1 || len((*started)[0]) != 1 || (*started)[0][0] != "isotope-live-install" {
		t.Fatalf("the button started %q, want isotope-live-install", *started)
	}
	for _, p := range []Page{PageNetwork, PageDisks, PageAbout} {
		if _, ok := u.view(p).(*livePage); ok {
			t.Fatalf("%s must keep working on the live system", p)
		}
	}
}

func TestNotLiveNothingChanges(t *testing.T) {
	u, started := liveUI(t, false)
	for _, p := range Pages {
		if _, ok := u.view(p).(*livePage); ok {
			t.Fatalf("%s: install-first page when not live", p)
		}
	}
	if len(*started) != 0 {
		t.Fatalf("started %q", *started)
	}
}

func TestLiveMarker(t *testing.T) {
	old := liveMarker
	defer func() { liveMarker = old }()
	liveMarker = filepath.Join(t.TempDir(), "live-desktop")
	if isLive() {
		t.Fatal("live without the marker")
	}
	if err := os.WriteFile(liveMarker, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if !isLive() {
		t.Fatal("not live with the marker")
	}
}
