package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kairos-community/isotope/pkg/installer"
)

// --- keyboard and timezone steps ---------------------------------------------

func TestWizardStartsOnKeyboardWithDefaults(t *testing.T) {
	m := newWizard(testDisks())
	if m.Page != installer.PageKeyboard {
		t.Fatalf("step = %v, want installer.PageKeyboard first, so the password is typed with the chosen layout", m.Page)
	}
	if !strings.Contains(m.View(), "> English (US)  us") {
		t.Errorf("cursor is not on the default layout:\n%s", m.View())
	}
	m = keys(t, m, "enter")
	if m.Choice.KeyboardLayout != "us" || m.Choice.KeyboardVariant != "" {
		t.Errorf("keyboard = %q/%q, want us with no variant", m.Choice.KeyboardLayout, m.Choice.KeyboardVariant)
	}
	if m.Page != installer.PageTimezone {
		t.Fatalf("step = %v, want installer.PageTimezone", m.Page)
	}
	m = keys(t, m, "enter")
	if m.Choice.Timezone != installer.DefaultTimezone {
		t.Errorf("timezone = %q, want %q", m.Choice.Timezone, installer.DefaultTimezone)
	}
}

func TestWizardSearchesAndPicksKeyboardAndTimezone(t *testing.T) {
	dir := t.TempDir()
	writeZone(t, dir, "i386/qwertz/de-latin1-nodeadkeys.map.gz")
	m := newWizard(testDisks())
	m.KeymapsDir = dir
	var loaded []string
	m.ApplyKeymap = func(k string) { loaded = append(loaded, k) }

	m = keys(t, m, "german dead", "enter")
	if m.Choice.KeyboardLayout != "de" || m.Choice.KeyboardVariant != "nodeadkeys" {
		t.Fatalf("keyboard = %q/%q, want de/nodeadkeys", m.Choice.KeyboardLayout, m.Choice.KeyboardVariant)
	}
	if m.Choice.ConsoleKeymap != "de-latin1-nodeadkeys" {
		t.Errorf("console keymap = %q, want de-latin1-nodeadkeys", m.Choice.ConsoleKeymap)
	}
	if len(loaded) != 1 || loaded[0] != "de-latin1-nodeadkeys" {
		t.Errorf("live console keymap loads = %v, want [de-latin1-nodeadkeys]", loaded)
	}

	m = keys(t, m, "rome", "enter")
	if m.Choice.Timezone != "Europe/Rome" {
		t.Fatalf("timezone = %q, want Europe/Rome", m.Choice.Timezone)
	}
	if m.Page != installer.PageHostname {
		t.Fatalf("step = %v, want installer.PageHostname", m.Page)
	}

	// Going back finds the cursor on the earlier choices, with the filter
	// cleared.
	m = keys(t, m, "esc")
	if !strings.Contains(m.View(), "> Europe/Rome") {
		t.Errorf("timezone cursor lost after going back:\n%s", m.View())
	}
	m = keys(t, m, "esc")
	if !strings.Contains(m.View(), "> German (no dead keys)  de(nodeadkeys)") {
		t.Errorf("keyboard cursor lost after going back:\n%s", m.View())
	}

	m = keys(t, m, "enter", "enter")
	m = clear(t, m, "")
	m = keys(t, m, "box", "enter", "ada", "enter", "pw", "enter", "enter", "enter", "enter")
	if m.Page != installer.PageReview {
		t.Fatalf("step = %v, want installer.PageReview", m.Page)
	}
	for _, want := range []string{"de(nodeadkeys)", "Europe/Rome"} {
		if !strings.Contains(m.View(), want) {
			t.Errorf("review does not show %q:\n%s", want, m.View())
		}
	}
}

func TestWizardListStepRefusesAnEmptyMatch(t *testing.T) {
	m := keys(t, newWizard(testDisks()), "no such layout", "enter")
	if m.Page != installer.PageKeyboard {
		t.Fatalf("step = %v, want to stay on installer.PageKeyboard", m.Page)
	}
	if !strings.Contains(m.View(), "nothing matches") {
		t.Errorf("no error shown for an empty match:\n%s", m.View())
	}
}

func TestWizardListStepMovesWithArrows(t *testing.T) {
	m := keys(t, newWizard(testDisks()), "down", "enter")
	want := installer.CuratedKeyboards[1]
	if m.Choice.KeyboardLayout != want.Layout || m.Choice.KeyboardVariant != want.Variant {
		t.Errorf("keyboard = %q/%q, want %+v", m.Choice.KeyboardLayout, m.Choice.KeyboardVariant, want)
	}
}

func TestWizardListStepsFitTheConsole(t *testing.T) {
	m := newWizardWith(testDisks(), installer.CuratedKeyboards, installer.CuratedTimezones, "UTC")
	for _, s := range []installer.Page{installer.PageKeyboard, installer.PageTimezone} {
		if m.Page != s {
			t.Fatalf("step = %v, want %v", m.Page, s)
		}
		if n := len(strings.Split(m.View(), "\n")); n > 25 {
			t.Errorf("%s view is %d lines, more than a 25-row console", stepName[s], n)
		}
		m = keys(t, m, "enter")
	}
}

// writeZone writes a fixture file under dir, as core's locale tests do.
func writeZone(t *testing.T, dir, name string) {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("TZif2\x00"), 0o644); err != nil {
		t.Fatal(err)
	}
}
