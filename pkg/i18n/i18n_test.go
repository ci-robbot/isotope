package i18n

import (
	"bytes"
	"io/fs"
	"path"
	"reflect"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/kairos-community/isotope/pkg/i18n/gettext"
)

const fixtureIT = `msgid ""
msgstr ""
"Language: it\n"
"Plural-Forms: nplurals=2; plural=(n != 1);\n"

msgid "Updates"
msgstr "Aggiornamenti"

#, go-format
msgid "Isotope %s is available"
msgstr "Isotope %s è disponibile"

#, go-format
msgid "%[1]s on %[2]s"
msgstr "%[2]s: %[1]s"

msgctxt "verb"
msgid "Open"
msgstr "Apri"

#, go-format
msgid "One app update"
msgid_plural "%d app updates"
msgstr[0] "Un aggiornamento di app"
msgstr[1] "%d aggiornamenti di app"

msgid "Untranslated"
msgstr ""

#, fuzzy
msgid "Fuzzy"
msgstr "Sfocato"
`

const fixturePL = `msgid ""
msgstr ""
"Plural-Forms: nplurals=3; plural=(n==1 ? 0 : n%10>=2 && n%10<=4 && (n%100<10 || n%100>=20) ? 1 : 2);\n"

msgid "%d file"
msgid_plural "%d files"
msgstr[0] "%d plik"
msgstr[1] "%d pliki"
msgstr[2] "%d plików"
`

// withFixture swaps the catalogs for two domains of fixtures.
func withFixture(t *testing.T) {
	t.Helper()
	old := catalogs
	catalogs = fstest.MapFS{
		"po/a/it.po": {Data: []byte(fixtureIT)},
		"po/b/it.po": {Data: []byte("msgid \"\"\nmsgstr \"\"\n\"Language: it\\n\"\n\nmsgid \"Close\"\nmsgstr \"Chiudi\"\n")},
		"po/b/pl.po": {Data: []byte(fixturePL)},
	}
	t.Cleanup(func() {
		catalogs = old
		SetLanguage()
	})
}

func TestEnglishUntilALanguageIsSet(t *testing.T) {
	withFixture(t)
	if got := T("Updates"); got != "Updates" {
		t.Fatalf("T before SetLanguage = %q", got)
	}
	if got := Nf("One app update", "%d app updates", 3, 3); got != "3 app updates" {
		t.Fatalf("Nf = %q", got)
	}
	if got := Nf("One app update", "%d app updates", 1, 1); got != "One app update" {
		t.Fatalf("Nf(1) = %q, want no EXTRA", got)
	}
}

func TestTranslate(t *testing.T) {
	withFixture(t)
	if got := SetLanguage("it_IT", "it"); got != "it" {
		t.Fatalf("SetLanguage = %q", got)
	}
	for _, c := range []struct{ got, want string }{
		{T("Updates"), "Aggiornamenti"},
		{T("Close"), "Chiudi"}, // from the second domain
		{Tf("Isotope %s is available", "v1.2"), "Isotope v1.2 è disponibile"},
		{Tf("%[1]s on %[2]s", "a", "b"), "b: a"},
		{X("verb", "Open"), "Apri"},
		{T("Open"), "Open"}, // no context: not the verb's entry
		{T("Untranslated"), "Untranslated"},
		{T("Fuzzy"), "Fuzzy"},
		{T("Missing"), "Missing"},
		{Nf("One app update", "%d app updates", 1, 1), "Un aggiornamento di app"},
		{Nf("One app update", "%d app updates", 0, 0), "0 aggiornamenti di app"},
		{Nf("One app update", "%d app updates", 5, 5), "5 aggiornamenti di app"},
		{N("One app update", "%d app updates", 2), "%d aggiornamenti di app"},
		{Mark("Updates"), "Updates"},
	} {
		if c.got != c.want {
			t.Errorf("got %q, want %q", c.got, c.want)
		}
	}
	if Language() != "it" {
		t.Errorf("Language = %q", Language())
	}
	if got := SetLanguage("en_US"); got != "" || T("Updates") != "Updates" {
		t.Errorf("English: SetLanguage = %q, T = %q", got, T("Updates"))
	}
	if got := SetLanguage("de", "fr"); got != "" {
		t.Errorf("no catalog: SetLanguage = %q", got)
	}
}

func TestPluralRuleOfTheCatalog(t *testing.T) {
	withFixture(t)
	SetLanguage("pl")
	for n, want := range map[int]string{1: "1 plik", 3: "3 pliki", 5: "5 plików", 22: "22 pliki", 112: "112 plików"} {
		if got := Nf("%d file", "%d files", n, n); got != want {
			t.Errorf("n=%d: %q, want %q", n, got, want)
		}
	}
}

func TestCandidates(t *testing.T) {
	env := func(kv ...string) func(string) string {
		m := map[string]string{}
		for i := 0; i < len(kv); i += 2 {
			m[kv[i]] = kv[i+1]
		}
		return func(k string) string { return m[k] }
	}
	for _, c := range []struct {
		env  func(string) string
		want []string
	}{
		{env(), nil},
		{env("LANG", "C.UTF-8"), nil},
		{env("LANG", "POSIX", "LANGUAGE", "it"), nil}, // LANGUAGE needs a real locale
		{env("LANG", "it_IT.UTF-8"), []string{"it_IT", "it"}},
		{env("LANG", "es_ES.UTF-8@euro"), []string{"es_ES@euro", "es_ES", "es@euro", "es"}},
		{env("LANG", "de_DE.UTF-8", "LC_MESSAGES", "es_MX.UTF-8"), []string{"es_MX", "es"}},
		{env("LANG", "de_DE.UTF-8", "LC_MESSAGES", "es_MX.UTF-8", "LC_ALL", "it_CH.UTF-8"), []string{"it_CH", "it"}},
		{env("LANG", "de_DE.UTF-8", "LANGUAGE", "es:it"), []string{"es", "it", "de_DE", "de"}},
	} {
		if got := Candidates(c.env); !reflect.DeepEqual(got, c.want) {
			t.Errorf("Candidates = %q, want %q", got, c.want)
		}
	}
}

// TestShippedCatalogs: every embedded catalog parses, has a plural rule,
// and the languages of po/LINGUAS are there for every domain.
func TestShippedCatalogs(t *testing.T) {
	langs := Languages()
	for _, want := range []string{"es", "it"} {
		found := false
		for _, l := range langs {
			found = found || l == want
		}
		if !found {
			t.Errorf("no %s catalog (have %v)", want, langs)
		}
	}
	domains, err := fs.ReadDir(embedded, "po")
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range domains {
		for _, l := range []string{"es", "it"} {
			p := path.Join("po", d.Name(), l+".po")
			b, err := fs.ReadFile(embedded, p)
			if err != nil {
				t.Errorf("%s: %v", p, err)
				continue
			}
			f, err := gettext.Parse(bytes.NewReader(b))
			if err != nil {
				t.Errorf("%s: %v", p, err)
				continue
			}
			if _, err := gettext.ParsePluralForms(f.HeaderField("Plural-Forms")); err != nil {
				t.Errorf("%s: %v", p, err)
			}
			if lang := f.HeaderField("Language"); lang != l {
				t.Errorf("%s: Language %q", p, lang)
			}
		}
	}
	for _, l := range []string{"it", "es"} {
		if SetLanguage(l) != l {
			t.Errorf("SetLanguage(%s) failed", l)
		}
	}
	SetLanguage()
}

// TestSessionLocaleReachesTheCatalog: the LANG values the Welcome page and
// the installer write (/etc/locale.conf, exported by isotope-locale.sh)
// select the catalog.
func TestSessionLocaleReachesTheCatalog(t *testing.T) {
	t.Cleanup(func() { SetLanguage() })
	for lang, want := range map[string]string{
		"it_IT.UTF-8": "it", "it_CH.UTF-8": "it", "es_ES.UTF-8": "es", "es_MX.UTF-8": "es",
		"en_US.UTF-8": "", "de_DE.UTF-8": "", "C.UTF-8": "",
	} {
		t.Setenv("LANGUAGE", "")
		t.Setenv("LC_ALL", "")
		t.Setenv("LC_MESSAGES", "")
		t.Setenv("LANG", lang)
		if got := Init(); got != want {
			t.Errorf("LANG=%s: Init = %q, want %q", lang, got, want)
		}
		if !strings.HasPrefix(lang, Language()) {
			t.Errorf("LANG=%s: Language = %q", lang, Language())
		}
	}
}
