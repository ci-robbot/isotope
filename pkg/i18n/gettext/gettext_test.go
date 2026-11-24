package gettext

import (
	"bytes"
	"strings"
	"testing"
)

const sample = `# Italian translation.
msgid ""
msgstr ""
"Language: it\n"
"Plural-Forms: nplurals=2; plural=(n != 1);\n"

# translator note
#. TRANSLATORS: the page title
#: cmd/isotope-system/ui.go
msgid "Updates"
msgstr "Aggiornamenti"

#: a.go
#: b.go
#, go-format
msgid "Line one\n"
"line two %s"
msgstr ""
"Riga uno\n"
"riga due %s"

msgctxt "verb"
msgid "Open"
msgstr "Apri"

#, go-format
msgid "%d app"
msgid_plural "%d apps"
msgstr[0] "%d app"
msgstr[1] "%d app"

msgid "Quote \" and \\ and \t tab"
msgstr ""

#~ msgid "Gone"
#~ msgstr "Andato"
`

func TestParse(t *testing.T) {
	f, err := Parse(strings.NewReader(sample))
	if err != nil {
		t.Fatal(err)
	}
	if got := f.HeaderField("plural-forms"); got != "nplurals=2; plural=(n != 1);" {
		t.Errorf("Plural-Forms = %q", got)
	}
	if len(f.Entries) != 5 {
		t.Fatalf("%d entries", len(f.Entries))
	}
	e := f.Entries[0]
	if e.ID != "Updates" || e.Str[0] != "Aggiornamenti" || e.Translator[0] != "translator note" ||
		e.Extracted[0] != "TRANSLATORS: the page title" || e.Refs[0] != "cmd/isotope-system/ui.go" {
		t.Errorf("entry 0 = %+v", e)
	}
	e = f.Entries[1]
	if e.ID != "Line one\nline two %s" || e.Str[0] != "Riga uno\nriga due %s" || !e.HasFlag("go-format") || len(e.Refs) != 2 {
		t.Errorf("entry 1 = %+v", e)
	}
	if e := f.Entries[2]; e.Context != "verb" || e.Str[0] != "Apri" {
		t.Errorf("entry 2 = %+v", e)
	}
	if e := f.Entries[3]; e.Plural != "%d apps" || len(e.Str) != 2 || !e.Translated() {
		t.Errorf("entry 3 = %+v", e)
	}
	if e := f.Entries[4]; e.ID != "Quote \" and \\ and \t tab" || e.Translated() {
		t.Errorf("entry 4 = %+v", e)
	}
	// Round trip: what Bytes writes parses to the same entries, and writing
	// that again gives the same bytes.
	b := f.Bytes()
	g, err := Parse(bytes.NewReader(b))
	if err != nil {
		t.Fatalf("%v\n%s", err, b)
	}
	if !bytes.Equal(g.Bytes(), b) {
		t.Errorf("not stable:\n%s\n---\n%s", b, g.Bytes())
	}
	if len(g.Entries) != len(f.Entries) || g.Entries[1].ID != f.Entries[1].ID {
		t.Errorf("round trip lost entries")
	}
}

func TestParseErrors(t *testing.T) {
	for _, s := range []string{
		"msgid \"a\"\nmsgstr \"b\"\n\nmsgid \"a\"\nmsgstr \"c\"\n",
		"msgstr \"b\"\n",
		"msgid \"a\nmsgstr \"b\"\n",
		"msgid \"a\" \nmsgid_plural \"as\"\nmsgstr \"x\"\n",
		"msgid \"a\"\nmsgstr[1] \"x\"\n",
		"msgid \"a\\q\"\nmsgstr \"\"\n",
		"bogus \"a\"\n",
	} {
		if _, err := Parse(strings.NewReader(s)); err == nil {
			t.Errorf("no error for %q", s)
		}
	}
}

func TestPluralForms(t *testing.T) {
	for _, c := range []struct {
		rule string
		want map[int]int
	}{
		{"nplurals=2; plural=(n != 1);", map[int]int{0: 1, 1: 0, 2: 1, 100: 1}},
		{"nplurals=2; plural=(n > 1);", map[int]int{0: 0, 1: 0, 2: 1}},
		{"nplurals=1; plural=0;", map[int]int{0: 0, 1: 0, 7: 0}},
		{"nplurals=3; plural=(n%10==1 && n%100!=11 ? 0 : n%10>=2 && n%10<=4 && (n%100<10 || n%100>=20) ? 1 : 2);",
			map[int]int{1: 0, 2: 1, 4: 1, 5: 2, 11: 2, 12: 2, 21: 0, 22: 1, 25: 2, 111: 2}},
		{"nplurals=4; plural=(n==1) ? 0 : (n==2) ? 1 : (n > 10 && n%10 == 0) ? 2 : 3;", map[int]int{1: 0, 2: 1, 20: 2, 5: 3}},
		{"nplurals=2; plural=!(n == 1);", map[int]int{1: 0, 3: 1}},
	} {
		p, err := ParsePluralForms(c.rule)
		if err != nil {
			t.Fatalf("%s: %v", c.rule, err)
		}
		for n, want := range c.want {
			if got := p.Form(n); got != want {
				t.Errorf("%s: n=%d form %d, want %d", c.rule, n, got, want)
			}
		}
	}
	for _, bad := range []string{"", "nplurals=2;", "nplurals=x; plural=n;", "nplurals=2; plural=(n != 1;", "nplurals=2; plural=n ? 1;", "nplurals=2; plural=m;"} {
		if _, err := ParsePluralForms(bad); err == nil {
			t.Errorf("no error for %q", bad)
		}
	}
	if English.Form(1) != 0 || English.Form(0) != 1 {
		t.Error("English rule")
	}
}

func TestSameVerbs(t *testing.T) {
	for _, c := range []struct {
		src, tr string
		subset  bool
		ok      bool
	}{
		{"Isotope %s is available", "Isotope %s è disponibile", false, true},
		{"%s of %d", "%s di %d", false, true},
		{"%s of %d", "%d di %s", false, false},
		{"%[1]s on %[2]s", "%[2]s: %[1]s", false, true},
		{"%s on %s", "%[2]s: %[1]s", false, true},
		{"100%% done %s", "%s fatto al 100%%", false, true},
		{"%d apps", "Un'app", true, true},
		{"%d apps", "Un'app", false, false},
		{"%d apps", "%s app", true, false},
		{"%v", "%v %v", false, false},
		{"%5.2f%%", "%5.2f %%", false, true},
		{"failed: %w", "non riuscito: %w", false, true},
		{"failed: %w", "non riuscito: %v", false, false},
	} {
		err := SameVerbs(c.src, c.tr, c.subset)
		if (err == nil) != c.ok {
			t.Errorf("SameVerbs(%q, %q, %v) = %v", c.src, c.tr, c.subset, err)
		}
	}
	if _, err := ParseVerbs("%[x]s"); err == nil {
		t.Error("bad index accepted")
	}
	if _, err := ParseVerbs("50%"); err == nil {
		t.Error("format ending in a verb accepted")
	}
	v, _ := ParseVerbs("%[3]s %s")
	if v.Count() != 4 {
		t.Errorf("Count = %d", v.Count())
	}
}
