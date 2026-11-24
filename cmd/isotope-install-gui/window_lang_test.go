package main

import (
	"testing"

	"github.com/kairos-community/isotope/pkg/i18n"
)

// The live session's language, from --choices, is the window's language.
func TestLiveLanguageFromChoices(t *testing.T) {
	t.Cleanup(func() { i18n.SetLanguage() })

	liveLanguage(liveChoices{Lang: "it_IT.UTF-8"})
	if got := i18n.Language(); got != "it" {
		t.Fatalf("language %q after lang it_IT.UTF-8, want it", got)
	}

	// A lang core does not accept leaves the language as it was.
	for _, lang := range []string{"", "not a locale", "es_ES.UTF-8\nx"} {
		liveLanguage(liveChoices{Lang: lang})
		if got := i18n.Language(); got != "it" {
			t.Errorf("language %q after lang %q, want it kept", got, lang)
		}
	}
}
