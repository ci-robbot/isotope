package main

import (
	"bufio"
	"fmt"
	"io"
	"slices"
	"strings"
)

// ShortcutsFile is the variant's shortcut sheet, written by
// tools/gen-shortcuts.py (COSMIC's from tools/shortcuts-cosmic.txt). It is
// the file isotope-help shows.
const ShortcutsFile = "/usr/share/isotope/shortcuts.txt"

// Shortcut is one row of the sheet: the keys, as caps, and what they do.
type Shortcut struct {
	Keys   []string
	Action string
}

// ParseShortcuts reads a sheet: one row per line, SECTION, KEYS and
// DESCRIPTION separated by tabs, with the keys joined by " + ". Lines that
// start with "#" and empty lines are skipped.
func ParseShortcuts(r io.Reader) ([]Shortcut, error) {
	var out []Shortcut
	sc := bufio.NewScanner(r)
	n := 0
	for sc.Scan() {
		n++
		line := strings.TrimRight(sc.Text(), "\r")
		if strings.TrimSpace(line) == "" || strings.HasPrefix(line, "#") {
			continue
		}
		cols := strings.Split(line, "\t")
		if len(cols) != 3 || strings.TrimSpace(cols[1]) == "" || strings.TrimSpace(cols[2]) == "" {
			return nil, fmt.Errorf("shortcuts line %d: want section, keys and description separated by tabs", n)
		}
		var keys []string
		for _, k := range strings.Split(cols[1], " + ") {
			keys = append(keys, strings.TrimSpace(k))
		}
		out = append(out, Shortcut{Keys: keys, Action: strings.TrimSpace(cols[2])})
	}
	return out, sc.Err()
}

// mainKeys are the keys the Shortcuts page puts first, where the variant
// has them: the Isotope menu, apps, all shortcuts, power.
var mainKeys = [][]string{
	{"Super", "Escape"},
	{"Super", "D"},
	{"Super", "F1"},
	{"Super", "Shift", "E"},
}

// maxShortcuts is how many rows the page shows.
const maxShortcuts = 6

// MainShortcuts is what the Shortcuts page shows: the main keys the sheet
// has, in mainKeys' order, then the sheet's first other rows (what the
// variant adds), up to maxShortcuts.
func MainShortcuts(all []Shortcut) []Shortcut {
	var out []Shortcut
	used := make([]bool, len(all))
	for _, want := range mainKeys {
		for i, s := range all {
			if !used[i] && sameKeys(s.Keys, want) {
				used[i] = true
				out = append(out, s)
				break
			}
		}
	}
	for i, s := range all {
		if len(out) >= maxShortcuts {
			break
		}
		if !used[i] {
			out = append(out, s)
		}
	}
	for i := range out {
		// The sheet's words for Super+F1 are about the sheet itself.
		if out[i].Action == "This help" {
			out[i].Action = "All keyboard shortcuts"
		}
	}
	return out
}

// sameKeys reports whether keys are want; a cap that lists alternatives
// ("D / Space") matches any of them.
func sameKeys(keys, want []string) bool {
	if len(keys) != len(want) {
		return false
	}
	for i := range keys {
		if !slices.Contains(strings.Split(keys[i], " / "), want[i]) {
			return false
		}
	}
	return true
}
