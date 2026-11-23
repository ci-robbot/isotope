package main

import "testing"

func TestParseArgs(t *testing.T) {
	if p, ok := ParseArgs(nil); !ok || p != PageUpdates {
		t.Fatalf("no argument: %q %v, want updates", p, ok)
	}
	for _, p := range Pages {
		if got, ok := ParseArgs([]string{string(p)}); !ok || got != p {
			t.Errorf("%s: %q %v", p, got, ok)
		}
	}
	for _, bad := range [][]string{{"--nonsense"}, {"updates", "backup"}, {"Updates"}, {""}} {
		if _, ok := ParseArgs(bad); ok {
			t.Errorf("%q accepted", bad)
		}
	}
}

// The tray (cmd/isotope/trayapp.go systemPage) and isotope-menu open these by
// name; they must stay.
func TestTheTrayAndMenuPagesExist(t *testing.T) {
	for _, name := range []string{"updates", "desktop", "extensions", "backup", "security"} {
		if _, ok := ParseArgs([]string{name}); !ok {
			t.Errorf("%s is not a page", name)
		}
	}
}

func TestEveryPageHasATitleAndAPlace(t *testing.T) {
	seen := map[string]bool{}
	for i, p := range Pages {
		if p.Title() == string(p) || seen[p.Title()] {
			t.Errorf("%s: title %q", p, p.Title())
		}
		seen[p.Title()] = true
		if p.index() != i {
			t.Errorf("%s: index %d, want %d", p, p.index(), i)
		}
	}
}
