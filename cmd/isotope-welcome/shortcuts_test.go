package main

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// sheet reads a variant's real shortcut sheet, the file the image ships.
func sheet(t *testing.T, d Desktop) []Shortcut {
	t.Helper()
	f, err := os.Open(filepath.Join("..", "..", "rootfs", string(d), "usr", "share", "isotope", "shortcuts.txt"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	s, err := ParseShortcuts(f)
	if err != nil {
		t.Fatalf("%s: %v", d, err)
	}
	return s
}

func TestParseShortcuts(t *testing.T) {
	for _, d := range []Desktop{Sway, I3, Cosmic, Niri} {
		s := sheet(t, d)
		if len(s) == 0 {
			t.Fatalf("%s: no shortcuts", d)
		}
		found := false
		for _, r := range s {
			if reflect.DeepEqual(r.Keys, []string{"Super", "Escape"}) {
				found = true
				if !strings.HasPrefix(r.Action, "Isotope menu") {
					t.Errorf("%s: Super+Escape is %q", d, r.Action)
				}
			}
			if len(r.Keys) == 0 || r.Action == "" {
				t.Errorf("%s: empty row %+v", d, r)
			}
		}
		if !found {
			t.Errorf("%s: no Super+Escape", d)
		}
	}
}

func TestParseShortcutsRow(t *testing.T) {
	s, err := ParseShortcuts(strings.NewReader("# a comment\n\nBasics\tSuper + Shift + E\tPower menu\n"))
	if err != nil {
		t.Fatal(err)
	}
	want := []Shortcut{{Keys: []string{"Super", "Shift", "E"}, Action: "Power menu"}}
	if !reflect.DeepEqual(s, want) {
		t.Fatalf("got %+v, want %+v", s, want)
	}
}

func TestParseShortcutsBadRow(t *testing.T) {
	if _, err := ParseShortcuts(strings.NewReader("Basics\tSuper + D\n")); err == nil {
		t.Fatal("a row with two columns was accepted")
	}
}

func keysOf(s []Shortcut) []string {
	var out []string
	for _, r := range s {
		out = append(out, strings.Join(r.Keys, "+"))
	}
	return out
}

func TestMainShortcuts(t *testing.T) {
	sway := MainShortcuts(sheet(t, Sway))
	want := []string{"Super+Escape", "Super+D", "Super+F1", "Super+Shift+E"}
	if got := keysOf(sway); len(got) < 4 || !reflect.DeepEqual(got[:4], want) {
		t.Errorf("sway: %v, want %v first", got, want)
	}
	if len(sway) > maxShortcuts {
		t.Errorf("sway: %d rows, more than %d", len(sway), maxShortcuts)
	}
	for _, r := range sway {
		if r.Action == "This help" {
			t.Errorf("sway: Super+F1 still says %q", r.Action)
		}
	}
	// niri writes Super+D as "Super + D / Space": it still counts.
	niri := keysOf(MainShortcuts(sheet(t, Niri)))
	if len(niri) < 4 || niri[1] != "Super+D / Space" {
		t.Errorf("niri: %v", niri)
	}
	// COSMIC has only Super+Escape of the four; the rest of the page is
	// what COSMIC adds.
	cosmic := keysOf(MainShortcuts(sheet(t, Cosmic)))
	if len(cosmic) != maxShortcuts || cosmic[0] != "Super+Escape" {
		t.Errorf("cosmic: %v", cosmic)
	}
	seen := map[string]bool{}
	for _, k := range cosmic {
		if seen[k] {
			t.Errorf("cosmic: %s twice", k)
		}
		seen[k] = true
	}
}
