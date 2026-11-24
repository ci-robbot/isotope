package main

import (
	"fmt"
	"os"
	"testing"

	"gioui.org/gpu/headless"

	"github.com/kairos-community/isotope/pkg/i18n"
)

// TestI18nScreenshots renders pages in Italian, which runs 20-30% longer
// than English, at both sizes and compares them with
// testdata/i18n-it-*.png: the buttons, the step list and the text must
// hold the longer words. `go test -tags nox11,novulkan -run
// TestI18nScreenshots . -update` rewrites them.
func TestI18nScreenshots(t *testing.T) {
	if os.Getenv("ISOTOPE_GUI_SKIP_SCREENSHOTS") == "1" {
		t.Skip("ISOTOPE_GUI_SKIP_SCREENSHOTS=1")
	}
	probe, err := headless.NewWindow(8, 8)
	if err != nil {
		if os.Getenv("ISOTOPE_GUI_REQUIRE_SCREENSHOTS") == "1" {
			t.Fatalf("no headless GPU context: %v", err)
		}
		t.Skipf("no headless GPU context: %v", err)
	}
	probe.Release()
	apps = loadApps(t)
	if i18n.SetLanguage("it") != "it" {
		t.Fatal("no it catalog")
	}
	t.Cleanup(func() { i18n.SetLanguage() })
	for _, p := range pages {
		if p.name != "1-welcome" && p.name != "ready-language-changed" {
			continue
		}
		for _, sz := range p.sizes {
			name := fmt.Sprintf("i18n-it-%s-%dx%d", p.name, sz.X, sz.Y)
			t.Run(name, func(t *testing.T) {
				u := fixture(t, p.d)
				p.setup(u)
				u.scale = p.scale
				compare(t, name, render(t, u, sz))
			})
		}
	}
}
