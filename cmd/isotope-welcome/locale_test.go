package main

import (
	"errors"
	"testing"

	"github.com/godbus/dbus/v5"
)

func TestLocalesFiltersUTF8(t *testing.T) {
	r := &fakeRunner{out: map[string]string{
		"locale -a": "C\nPOSIX\nde_DE.UTF-8\nde_DE.utf8\nen_US.utf8\nfr_FR.ISO-8859-1\nxx_YY.UTF-8\n\n",
	}}
	got, err := Locales(r)
	if err != nil {
		t.Fatal(err)
	}
	want := []Locale{
		{"de_DE.UTF-8", "Deutsch (Deutschland)"},
		{"en_US.utf8", "English (United States)"},
		{"xx_YY.UTF-8", "xx_YY"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("locale %d = %v, want %v", i, got[i], want[i])
		}
	}
}

func TestLocalesFallbackNames(t *testing.T) {
	got := parseLocales("de_XX.UTF-8\nde_AT.UTF-8@euro\nC.UTF-8\n")
	names := map[string]string{}
	for _, l := range got {
		names[l.Code] = l.Name
	}
	for code, want := range map[string]string{
		"de_XX.UTF-8":      "Deutsch (XX)",
		"de_AT.UTF-8@euro": "Deutsch (Österreich) @euro",
		"C.UTF-8":          "Default (C.UTF-8)",
	} {
		if names[code] != want {
			t.Errorf("%s = %q, want %q", code, names[code], want)
		}
	}
}

// Every name the page can show is drawable: the table's own name passes the
// font coverage check, or the entry has an English name that does.
func TestLocaleNamesAreDrawable(t *testing.T) {
	for code, n := range localeNames {
		if covered(n) {
			continue
		}
		if e := englishNames[code]; e == "" || !covered(e) {
			t.Errorf("%s: %q is not drawable and has no English name", code, n)
		}
	}
	for lang, n := range languageNames {
		if covered(n) {
			continue
		}
		if e := englishLanguages[lang]; e == "" || !covered(e) {
			t.Errorf("%s: %q is not drawable and has no English name", lang, n)
		}
	}
	if got := localeName("zh_CN"); got != "Chinese (China)" {
		t.Errorf("zh_CN = %q", got)
	}
	if got := localeName("ja_XX"); got != "Japanese (XX)" {
		t.Errorf("ja_XX = %q", got)
	}
	if got := LocaleName("de_DE.UTF-8"); got != "Deutsch (Deutschland)" {
		t.Errorf("de_DE = %q", got)
	}
}

func TestLocalesRunnerError(t *testing.T) {
	r := &fakeRunner{fail: []string{"locale"}}
	if _, err := Locales(r); err == nil {
		t.Fatal("no error")
	}
}

func TestCurrentLocale(t *testing.T) {
	for in, want := range map[string]string{
		"LANG=de_DE.UTF-8\n":                  "de_DE.UTF-8",
		"# c\nLANG=\"en_US.UTF-8\"\nLC_ALL=C": "en_US.UTF-8",
		"LC_ALL=C\n":                          "",
		"":                                    "",
	} {
		if got := CurrentLocale(in); got != want {
			t.Errorf("CurrentLocale(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestMapSettingsErr(t *testing.T) {
	no := dbus.Error{Name: updateNotAuthed, Body: []any{"not authorized"}}
	if err := mapSettingsErr(no); !errors.Is(err, ErrNotAuthorized) {
		t.Errorf("got %v", err)
	}
	other := dbus.Error{Name: updateBus + ".Error.Failed", Body: []any{`"xx" is not a locale of this system`}}
	if err := mapSettingsErr(other); err == nil || errors.Is(err, ErrNotAuthorized) || err.Error() != `"xx" is not a locale of this system` {
		t.Errorf("got %v", err)
	}
	if mapSettingsErr(nil) != nil {
		t.Error("nil became an error")
	}
}
