package main

import (
	"reflect"
	"testing"

	"github.com/kairos-community/isotope/pkg/guikit"
)

// aboutUI is an Isotope System on the About page whose Start records what it
// would run.
func aboutUI(t *testing.T, sys System) (*aboutPage, *[][]string) {
	t.Helper()
	var started [][]string
	u := newUI(sys, guikit.NewShaper(), Hooks{Daemon: newFakeDaemon(),
		Start: func(argv ...string) error { started = append(started, argv); return nil }}, PageAbout)
	return u.view(PageAbout).(*aboutPage), &started
}

func footerLabels(p *aboutPage) []string {
	lead, trail := p.Footer()
	var out []string
	for _, a := range append(lead, trail...) {
		out = append(out, a.Label)
	}
	return out
}

// The About page opens the user guide, and the release notes when the
// release names its GitHub repository and version.
func TestAboutOpensTheUserGuide(t *testing.T) {
	p, started := aboutUI(t, testSystem(false))
	if got, want := footerLabels(p), []string{"User guide", "What's new in this release"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("footer %q, want %q", got, want)
	}
	p.guide.Click()
	p.Update(gtxOf(1100, 720))
	p.notes.Click()
	p.Update(gtxOf(1100, 720))
	want := [][]string{
		{"xdg-open", "https://kairos-community.github.io/isotope/guide/"},
		{"xdg-open", "https://github.com/kairos-community/isotope/releases/tag/v1.0.0"},
	}
	if !reflect.DeepEqual(*started, want) {
		t.Fatalf("started %q, want %q", *started, want)
	}

	// No release file: no release notes, the guide stays.
	sys := testSystem(false)
	sys.Release.GitHubRepo, sys.Release.Version = "", ""
	p, _ = aboutUI(t, sys)
	if got, want := footerLabels(p), []string{"User guide"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("footer without a release %q, want %q", got, want)
	}
}
