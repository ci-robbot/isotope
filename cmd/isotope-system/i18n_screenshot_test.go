package main

import (
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/kairos-community/isotope/pkg/i18n"
)

// TestI18nScreenshots renders the Updates page in Italian and Spanish,
// which run 20-30% longer than English, and compares them with
// testdata/i18n-<lang>-*.png: the sidebar, the buttons and the sections
// must hold the longer text. It reuses the automatic updates harness
// (page_updates_screenshot_test.go); `go test -tags nox11,novulkan -run
// TestI18nScreenshots ./cmd/isotope-system/ -update` rewrites them.
func TestI18nScreenshots(t *testing.T) {
	if os.Getenv("ISOTOPE_GUI_SKIP_SCREENSHOTS") == "1" {
		t.Skip("ISOTOPE_GUI_SKIP_SCREENSHOTS=1")
	}
	needGPU(t)
	t.Cleanup(func() { i18n.SetLanguage() })
	c := updatesAutoShots[1] // automatic updates on, the last run skipped, an update offered
	for _, lang := range []string{"it", "es"} {
		t.Run(lang, func(t *testing.T) {
			if i18n.SetLanguage(lang) != lang {
				t.Fatalf("no %s catalog", lang)
			}
			defer i18n.SetLanguage()
			u, d, _ := testUI(t, true)
			u.now = func() time.Time { return updatesAutoClock }
			d.mu.Lock()
			d.st, d.auto = c.st, c.auto
			d.mu.Unlock()
			u.setStatus(c.st)
			u.show(PageUpdates)
			p := u.view(PageUpdates).(*updatesPage)
			eventually(t, "the page's reads", func() bool {
				u.mu.Lock()
				defer u.mu.Unlock()
				return p.auto.Loaded && !p.checking && len(p.tags) > 0
			})
			eventually(t, "the history read", func() bool { return contains(d.called(), "UpdateHistory") })
			u.pageAt = time.Time{}
			for _, sz := range []struct{ X, Y int }{{1100, 720}} {
				name := fmt.Sprintf("i18n-%s-updates-%dx%d", lang, sz.X, sz.Y)
				updatesAutoCompare(t, name, updatesAutoRender(t, u, updatesAutoShotSize))
			}
		})
	}
}
