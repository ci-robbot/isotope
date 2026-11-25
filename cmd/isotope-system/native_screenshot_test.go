package main

import (
	"image"
	"os"
	"testing"
	"time"

	"github.com/kairos-community/isotope/pkg/cosmictheme"
	"github.com/kairos-community/isotope/pkg/guikit"
)

// The window in both of Isotope's modes, on every page, at its opening
// size: testdata/native-<page>-<dark|light>.png. Then a narrow window with
// the navigation open, and COSMIC's header bar with the COSMIC theme.
// Pages whose service the test UI has no fake for show their "not
// available" state; their own screenshot tests cover their content.
func TestNativeScreenshots(t *testing.T) {
	needGPU(t)
	modes := map[string]bool{"dark": true, "light": false}
	for _, p := range Pages {
		for name, dark := range modes {
			t.Run(string(p)+"-"+name, func(t *testing.T) {
				u, _, _ := testUI(t, true)
				u.SetPalette(guikit.IsotopePalette(dark))
				u.show(p)
				u.pageAt = time.Time{}
				compare(t, "native-"+string(p)+"-"+name, render(t, u, image.Pt(defWindowW, defWindowH)))
			})
		}
	}
	t.Run("narrow", func(t *testing.T) {
		u, _, _ := testUI(t, true)
		u.SetPalette(guikit.IsotopePalette(true))
		u.show(PagePrinters)
		u.pageAt = time.Time{}
		compare(t, "native-narrow-dark", render(t, u, image.Pt(minWindowW, minWindowH)))
		u.navOpen = true
		compare(t, "native-narrow-nav-dark", render(t, u, image.Pt(minWindowW, minWindowH)))
	})
	for name, dark := range modes {
		t.Run("cosmic-"+name, func(t *testing.T) {
			user := t.TempDir()
			if !dark {
				if err := os.MkdirAll(user+"/com.system76.CosmicTheme.Mode/v1", 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(user+"/com.system76.CosmicTheme.Mode/v1/is_dark", []byte("false"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			u, _, _ := testUI(t, true)
			u.SetPalette(guikit.CosmicPalette(cosmictheme.Load([]string{user, cosmicTestDir}, os.ReadFile)))
			u.bar = &guikit.HeaderBar{Left: true}
			u.show(PageUpdates)
			u.pageAt = time.Time{}
			compare(t, "native-cosmic-"+name, render(t, u, image.Pt(defWindowW, defWindowH)))
		})
	}
}
