package main

import (
	"errors"
	"slices"
	"sync"
	"testing"
)

// fakeSettings is a Settings that records the languages it was asked for.
type fakeSettings struct {
	mu  sync.Mutex
	got []string
	err error
}

func (f *fakeSettings) SetLocale(lang string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.got = append(f.got, lang)
	return f.err
}

func (f *fakeSettings) calls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.got...)
}

const localeList = "C\nPOSIX\nC.UTF-8\nde_AT.UTF-8\nde_CH.UTF-8\nde_DE.UTF-8\nel_GR.UTF-8\nen_GB.UTF-8\n" +
	"en_US.UTF-8\nes_ES.UTF-8\nfi_FI.UTF-8\nfr_CA.UTF-8\nfr_FR.UTF-8\nit_IT.UTF-8\nja_JP.UTF-8\nko_KR.UTF-8\n" +
	"nl_NL.UTF-8\npl_PL.UTF-8\npt_BR.UTF-8\npt_PT.UTF-8\nru_RU.UTF-8\nsv_SE.UTF-8\ntr_TR.UTF-8\nuk_UA.UTF-8\nzh_CN.UTF-8\n"

// languageAt is the Language page with the list loaded, for a user who is
// (or is not) an administrator and a system that has the language current.
func languageAt(u *UI, s Settings, admin bool, current string) *languagePage {
	u.hooks.Runner = &fakeRunner{out: map[string]string{"locale -a": localeList}}
	u.hooks.Settings, u.hooks.Admin, u.hooks.SystemLocale = s, admin, current
	to(u, PLanguage)
	p := u.page().(*languagePage)
	p.started = true
	p.load()
	return p
}

func pickCode(t *testing.T, p *languagePage, code string) {
	t.Helper()
	for i, l := range p.locales {
		if l.Code == code {
			p.sel = i
			return
		}
	}
	t.Fatalf("no locale %s", code)
}

func TestLanguageSelectsTheCurrentLocale(t *testing.T) {
	u, _ := newRecorded(NewFlow(Sway), Sway)
	p := languageAt(u, &fakeSettings{}, true, "en_US.utf8")
	if p.sel < 0 || p.locales[p.sel].Code != "en_US.UTF-8" {
		t.Fatalf("selected %d", p.sel)
	}
}

func TestLanguageSearchFilters(t *testing.T) {
	u, _ := newRecorded(NewFlow(Sway), Sway)
	p := languageAt(u, &fakeSettings{}, true, "en_US.UTF-8")
	p.search.Editor.SetText("deu")
	p.filter()
	var got []string
	for _, i := range p.shown {
		got = append(got, p.locales[i].Code)
	}
	if !slices.Equal(got, []string{"de_DE.UTF-8", "de_CH.UTF-8", "de_AT.UTF-8"}) {
		t.Fatalf("shown %v", got)
	}
}

// A non-administrator sees the current language and is told to ask; Continue
// goes on without calling the daemon, and the page counts as skipped.
func TestLanguageReadOnlyForANonAdmin(t *testing.T) {
	u, _ := newRecorded(NewFlow(Sway), Sway)
	s := &fakeSettings{}
	p := languageAt(u, s, false, "de_DE.UTF-8")
	if !p.readOnly() || p.notice() != "Ask an administrator to change the language" {
		t.Fatalf("readOnly %v, notice %q", p.readOnly(), p.notice())
	}
	pickCode(t, p, "fr_FR.UTF-8")
	p.Continue()
	p.wait()
	if len(s.calls()) != 0 {
		t.Fatalf("the daemon was called: %v", s.calls())
	}
	if u.flow.Current() == PLanguage || len(u.flow.Outcome.Applied) != 0 {
		t.Fatalf("page %s, applied %v", u.flow.Current(), u.flow.Outcome.Applied)
	}
}

// The daemon refuses a user the group check let through (polkit, or a
// Flatpak caller): the page turns read-only and stays.
func TestLanguageDaemonRefusalMakesItReadOnly(t *testing.T) {
	u, _ := newRecorded(NewFlow(Sway), Sway)
	s := &fakeSettings{err: ErrNotAuthorized}
	p := languageAt(u, s, true, "en_US.UTF-8")
	if p.readOnly() {
		t.Fatal("an admin starts read-only")
	}
	pickCode(t, p, "de_DE.UTF-8")
	p.Continue()
	p.wait()
	p.settle()
	if !p.readOnly() || u.flow.Current() != PLanguage || len(u.flow.Outcome.Applied) != 0 {
		t.Fatalf("readOnly %v, page %s", p.readOnly(), u.flow.Current())
	}
}

func TestLanguageContinueSetsThePick(t *testing.T) {
	u, _ := newRecorded(NewFlow(Cosmic), Cosmic)
	s := &fakeSettings{}
	p := languageAt(u, s, true, "en_US.UTF-8")
	pickCode(t, p, "de_DE.UTF-8")
	p.Continue()
	if u.flow.Current() != PLanguage {
		t.Fatal("went on before the daemon answered")
	}
	p.wait()
	p.settle()
	if got := s.calls(); !slices.Equal(got, []string{"de_DE.UTF-8"}) {
		t.Fatalf("SetLocale calls %v", got)
	}
	if u.flow.Current() == PLanguage || !slices.Contains(u.flow.Outcome.Applied, PLanguage) {
		t.Fatalf("page %s, applied %v", u.flow.Current(), u.flow.Outcome.Applied)
	}
	if u.flow.Outcome.LanguageChanged != "Deutsch (Deutschland)" {
		t.Fatalf("LanguageChanged %q", u.flow.Outcome.LanguageChanged)
	}
}

func TestLanguageFailureStaysAndShowsTheError(t *testing.T) {
	u, _ := newRecorded(NewFlow(Sway), Sway)
	s := &fakeSettings{err: errors.New("disk full")}
	p := languageAt(u, s, true, "en_US.UTF-8")
	pickCode(t, p, "de_DE.UTF-8")
	p.Continue()
	p.wait()
	p.settle()
	if u.flow.Current() != PLanguage || p.applyErr != "disk full" || p.readOnly() {
		t.Fatalf("page %s, err %q, readOnly %v", u.flow.Current(), p.applyErr, p.readOnly())
	}
}

// Nothing new picked: Continue goes on and does not call the daemon.
func TestLanguageUnchangedContinueIsSkip(t *testing.T) {
	u, _ := newRecorded(NewFlow(Sway), Sway)
	s := &fakeSettings{}
	p := languageAt(u, s, true, "en_US.UTF-8")
	p.Continue()
	p.wait()
	if len(s.calls()) != 0 || u.flow.Current() == PLanguage || len(u.flow.Outcome.Applied) != 0 {
		t.Fatalf("calls %v, page %s, applied %v", s.calls(), u.flow.Current(), u.flow.Outcome.Applied)
	}
}

// A search that matches nothing clears the pick, so Continue cannot apply a
// locale that is not on the screen.
func TestLanguageNoMatchClearsThePick(t *testing.T) {
	u, _ := newRecorded(NewFlow(Sway), Sway)
	s := &fakeSettings{}
	p := languageAt(u, s, true, "en_US.UTF-8")
	pickCode(t, p, "de_DE.UTF-8")
	p.search.Editor.SetText("zzzz")
	p.refilter()
	if len(p.shown) != 0 || p.sel != -1 {
		t.Fatalf("shown %v, sel %d", p.shown, p.sel)
	}
	p.Continue()
	p.wait()
	if len(s.calls()) != 0 {
		t.Fatalf("applied %v", s.calls())
	}
}

// The search also matches the English name, for locales shown in it.
func TestLanguageSearchMatchesTheEnglishName(t *testing.T) {
	u, _ := newRecorded(NewFlow(Sway), Sway)
	p := languageAt(u, &fakeSettings{}, true, "en_US.UTF-8")
	p.search.Editor.SetText("chinese")
	p.refilter()
	if len(p.shown) != 1 || p.locales[p.shown[0]].Code != "zh_CN.UTF-8" {
		t.Fatalf("shown %v", p.shown)
	}
}

func TestHasGroup(t *testing.T) {
	if !hasGroup([]string{"1000", "27"}, "27") || hasGroup([]string{"1000"}, "27") {
		t.Fatal("hasGroup")
	}
}
