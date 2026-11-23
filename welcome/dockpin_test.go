package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func favorites(t *testing.T, home string) []string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(home, ".config/cosmic/com.system76.CosmicAppList/v1/favorites"))
	if err != nil {
		t.Fatal(err)
	}
	return ronStrings(string(b))
}

func TestPinToCosmicDock(t *testing.T) {
	def := filepath.Join(t.TempDir(), "favorites")
	if err := os.WriteFile(def, []byte("[\n    \"com.system76.CosmicFiles\",\n    \"com.system76.CosmicTerm\",\n]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	old := cosmicDefaultFavorites
	cosmicDefaultFavorites = def
	defer func() { cosmicDefaultFavorites = old }()

	home := t.TempDir()
	if err := PinToCosmicDock(home, "org.mozilla.firefox"); err != nil {
		t.Fatal(err)
	}
	want := "com.system76.CosmicFiles,com.system76.CosmicTerm,org.mozilla.firefox"
	if got := strings.Join(favorites(t, home), ","); got != want {
		t.Fatalf("favorites %q, want %q", got, want)
	}
	if err := PinToCosmicDock(home, "org.mozilla.firefox"); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(favorites(t, home), ","); got != want {
		t.Errorf("pinned twice: %q, want %q", got, want)
	}
	// The user's own file wins over the default from now on.
	if err := PinToCosmicDock(home, "com.brave.Browser"); err != nil {
		t.Fatal(err)
	}
	if got := favorites(t, home); len(got) != 4 || got[3] != "com.brave.Browser" {
		t.Errorf("%v", got)
	}
}

func TestPinToCosmicDockOneLineAndNoDefault(t *testing.T) {
	old := cosmicDefaultFavorites
	cosmicDefaultFavorites = filepath.Join(t.TempDir(), "missing")
	defer func() { cosmicDefaultFavorites = old }()
	home := t.TempDir()
	if err := PinToCosmicDock(home, "a.b"); err != nil {
		t.Fatal(err)
	}
	if got := favorites(t, home); len(got) != 1 || got[0] != "a.b" {
		t.Fatalf("%v", got)
	}
	p := filepath.Join(home, ".config/cosmic/com.system76.CosmicAppList/v1/favorites")
	if err := os.WriteFile(p, []byte(`["x.y", "z.w"]`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := PinToCosmicDock(home, "a.b"); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(favorites(t, home), ","); got != "x.y,z.w,a.b" {
		t.Errorf("%q", got)
	}
}

func TestPinFirstToCosmicDock(t *testing.T) {
	def := filepath.Join(t.TempDir(), "favorites")
	if err := os.WriteFile(def, []byte("[\n    \"com.system76.CosmicFiles\",\n    \"com.system76.CosmicTerm\",\n]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	old := cosmicDefaultFavorites
	cosmicDefaultFavorites = def
	defer func() { cosmicDefaultFavorites = old }()

	home := t.TempDir()
	if err := PinFirstToCosmicDock(home, "isotope-install"); err != nil {
		t.Fatal(err)
	}
	want := "isotope-install,com.system76.CosmicFiles,com.system76.CosmicTerm"
	if got := strings.Join(favorites(t, home), ","); got != want {
		t.Fatalf("favorites %q, want %q", got, want)
	}
	// Twice: no second entry. Already there but not first: left where it is.
	if err := PinFirstToCosmicDock(home, "isotope-install"); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(favorites(t, home), ","); got != want {
		t.Errorf("pinned twice: %q, want %q", got, want)
	}
	// An empty list.
	empty := t.TempDir()
	cosmicDefaultFavorites = filepath.Join(t.TempDir(), "none")
	if err := PinFirstToCosmicDock(empty, "isotope-install"); err != nil {
		t.Fatal(err)
	}
	if got := favorites(t, empty); len(got) != 1 || got[0] != "isotope-install" {
		t.Errorf("%v", got)
	}
}

// The live welcome pins the installer on COSMIC, and only there.
func TestLivePinsInstallerOnCosmic(t *testing.T) {
	home := t.TempDir()
	var pinned []string
	pin := func(h, id string) error { pinned = append(pinned, h+":"+id); return nil }
	liveDockPin(Sway, home, pin)
	if len(pinned) != 0 {
		t.Fatalf("pinned on sway: %v", pinned)
	}
	liveDockPin(Cosmic, home, pin)
	if len(pinned) != 1 || pinned[0] != home+":isotope-install" {
		t.Fatalf("pinned %v", pinned)
	}
}
