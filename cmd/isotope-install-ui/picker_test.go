package main

import "testing"

func TestPickerFiltersByEveryWordAndKeepsTheSelection(t *testing.T) {
	p := newPicker([]string{"English (US)  us", "German  de", "German (no dead keys)  de(nodeadkeys)"})
	p.move(1) // German
	p.filter("GERMAN")
	if i, _ := p.selected(); i != 1 {
		t.Errorf("selection moved to %d after a filter that still matches it, want 1", i)
	}
	p.filter("german dead")
	if len(p.match) != 1 || p.match[0] != 2 {
		t.Errorf("matches = %v, want only entry 2", p.match)
	}
	p.filter("nothing like this")
	if _, ok := p.selected(); ok {
		t.Error("an empty match list must have no selection")
	}
	p.move(1) // must not panic or go out of range
	if p.cursor != 0 {
		t.Errorf("cursor = %d on an empty list, want 0", p.cursor)
	}
}
