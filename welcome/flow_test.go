package main

import (
	"reflect"
	"slices"
	"testing"
)

func TestPagesCosmic(t *testing.T) {
	want := []PageID{PWelcome, PLanguage, PNetwork, PAppearance, PLayout, PWorkflow, PLauncher, PAccessibility, PApps, PShortcuts, PReady}
	got := Pages(Cosmic)
	if len(got) != 11 || !reflect.DeepEqual(got, want) {
		t.Fatalf("Pages(Cosmic) = %v, want %v", got, want)
	}
}

func TestPagesTiling(t *testing.T) {
	want := []PageID{PWelcome, PLanguage, PNetwork, PAppearance, PAccessibility, PApps, PShortcuts, PReady}
	for _, d := range []Desktop{Sway, I3, Niri} {
		got := Pages(d)
		if len(got) != 8 || !reflect.DeepEqual(got, want) {
			t.Errorf("Pages(%s) = %v, want %v", d, got, want)
		}
	}
}

func TestPageTitle(t *testing.T) {
	if PageTitle(PAccessibility) != "Accessibility" || PageTitle(PReady) != "Ready" {
		t.Fatal("unexpected titles")
	}
}

func TestFlowBackAtStart(t *testing.T) {
	f := NewFlow(Sway)
	f.Back()
	if f.Index() != 0 || f.Current() != PWelcome {
		t.Fatalf("index %d current %s", f.Index(), f.Current())
	}
}

func TestFlowNextBack(t *testing.T) {
	f := NewFlow(Sway)
	f.Next()
	if f.Current() != PLanguage {
		t.Fatalf("current %s", f.Current())
	}
	f.Back()
	if f.Current() != PWelcome {
		t.Fatalf("current %s", f.Current())
	}
}

func TestFlowSkipRecords(t *testing.T) {
	f := NewFlow(Cosmic)
	f.Next() // Language
	f.Skip()
	if f.Current() != PNetwork {
		t.Fatalf("current %s", f.Current())
	}
	if !reflect.DeepEqual(f.Outcome.Skipped, []PageID{PLanguage}) {
		t.Fatalf("skipped = %v", f.Outcome.Skipped)
	}
}

func TestFlowEndsAtReady(t *testing.T) {
	f := NewFlow(Niri)
	for i := 0; i < 20 && !f.Done(); i++ {
		f.Next()
	}
	if !f.Done() || f.Current() != PReady {
		t.Fatalf("done %v current %s", f.Done(), f.Current())
	}
}

func TestSingle(t *testing.T) {
	f := Single(PApps)
	if f.Current() != PApps || f.Index() != 0 || f.Done() {
		t.Fatalf("bad start: %s %d", f.Current(), f.Index())
	}
	f.Next()
	if !f.Done() {
		t.Fatal("Next should end a single-page flow")
	}
	if f.Current() != PApps {
		t.Fatalf("current %s", f.Current())
	}
}

func TestApplyRecordsOnce(t *testing.T) {
	f := NewFlow(Sway)
	f.Apply()
	f.Apply()
	if !reflect.DeepEqual(f.Outcome.Applied, []PageID{PWelcome}) {
		t.Fatalf("applied = %v", f.Outcome.Applied)
	}
}

func TestSkipBackApply(t *testing.T) {
	f := NewFlow(Sway)
	f.Skip()
	f.Back()
	f.Apply()
	if len(f.Outcome.Skipped) != 0 || !reflect.DeepEqual(f.Outcome.Applied, []PageID{PWelcome}) {
		t.Fatalf("%+v", f.Outcome)
	}
}

func TestApplyBackSkip(t *testing.T) {
	f := NewFlow(Sway)
	f.Apply()
	f.Skip()
	f.Back()
	f.Skip()
	if len(f.Outcome.Applied) != 0 || !reflect.DeepEqual(f.Outcome.Skipped, []PageID{PWelcome}) {
		t.Fatalf("%+v", f.Outcome)
	}
}

func TestBackAfterDone(t *testing.T) {
	f := Single(PApps)
	f.Next()
	f.Back()
	if f.Done() {
		t.Fatal("Back should revive a finished flow")
	}
}

func TestPagesIsCopy(t *testing.T) {
	f := NewFlow(Sway)
	f.Pages()[0] = PReady
	if f.Current() != PWelcome {
		t.Fatal("Pages leaked internal slice")
	}
}

func TestLanguagePageSkippedWhenLocaleSet(t *testing.T) {
	has := func(f *Flow) bool { return slices.Contains(f.Pages(), PLanguage) }
	// The installer wrote /etc/locale.conf with a LANG: no Language page.
	conf := "LANG=de_DE.UTF-8\n"
	if f := flowFor(PageLogin, Sway, CurrentLocale(conf)); has(f) || len(f.Pages()) != len(Pages(Sway))-1 {
		t.Errorf("login flow with LANG set: %v", f.Pages())
	}
	// No file, no LANG line, or a neutral value: the user never picked.
	for _, c := range []string{"", "# nothing\n", "LANG=\n", "LANG=C\n", "LANG=C.UTF-8\n", "LANG=POSIX\n"} {
		if f := flowFor(PageLogin, Sway, CurrentLocale(c)); !has(f) {
			t.Errorf("login flow for %q lost the Language page", c)
		}
	}
	// `isotope welcome` asks for every page, so it keeps it.
	if !has(flowFor(PageAll, Sway, "de_DE.UTF-8")) {
		t.Error("the whole flow lost the Language page")
	}
}
