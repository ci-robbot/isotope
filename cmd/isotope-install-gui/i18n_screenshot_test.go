package main

import (
	"fmt"
	"testing"
	"time"

	"github.com/kairos-community/isotope/pkg/i18n"
)

// TestI18nScreenshots renders installer steps in Italian, which runs
// 20-30% longer than English, at both sizes and compares them with
// testdata/i18n-it-*.png: the step list, the fields' labels and the
// buttons must hold the longer words. `go test -tags nox11,novulkan -run
// TestI18nScreenshots . -update` rewrites them.
func TestI18nScreenshots(t *testing.T) {
	needGPU(t)
	if i18n.SetLanguage("it") != "it" {
		t.Fatal("no it catalog")
	}
	t.Cleanup(func() { i18n.SetLanguage() })
	want := map[string]bool{"1-welcome": true, "3-account": true, "5-encryption": true, "7-summary": true}
	for _, p := range pages {
		if !want[p.name] {
			continue
		}
		for _, sz := range sizes {
			name := fmt.Sprintf("i18n-it-%s-%dx%d", p.name, sz.X, sz.Y)
			t.Run(name, func(t *testing.T) {
				u := fixture(t, p.tb)
				p.setup(t, u)
				u.pageAt = time.Time{}
				compare(t, name, render(t, u, sz))
			})
		}
	}
}
