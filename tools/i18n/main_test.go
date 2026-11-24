package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const srcA = `package a

import "github.com/kairos-community/isotope/pkg/i18n"

var title = i18n.Mark("Updates")

func f(n int, v string) {
	_ = i18n.T(title)
	_ = i18n.T("Check for " + "updates")
	// TRANSLATORS: v is a version such as v1.2.
	_ = i18n.Tf("Isotope %s is available", v)
	_ = i18n.Nf("One update", "%d updates", n, n)
	_ = i18n.X("verb", "Open")
	_ = i18n.T("Updates")
}
`

const srcB = `package b

import tr "github.com/kairos-community/isotope/pkg/i18n"

func g() string { return tr.T("Close") }
`

// tree makes a repository with one domain of two directories and returns
// its root; the domains are swapped for the test's.
func tree(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	write := func(p, s string) {
		p = filepath.Join(root, p)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("a/a.go", srcA)
	write("a/a_test.go", "package a\n\nimport \"github.com/kairos-community/isotope/pkg/i18n\"\n\nvar _ = i18n.T(\"Test only\")\n")
	write("b/sub/b.go", srcB)
	write(poDir+"/LINGUAS", "# comment\nit\nes\n")
	old := domains
	domains = []domain{{"demo", []string{"a", "b"}}}
	t.Cleanup(func() { domains = old })
	return root
}

func TestUpdateThenCheck(t *testing.T) {
	root := tree(t)
	if problems, err := check(root); err != nil || len(problems) == 0 {
		t.Fatalf("check before update: %v %v", problems, err)
	}
	if err := update(root); err != nil {
		t.Fatal(err)
	}
	pot, err := os.ReadFile(filepath.Join(root, poDir, "demo", "demo.pot"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"#: a/a.go\nmsgid \"Updates\"",
		"msgid \"Check for updates\"",
		"#. TRANSLATORS: v is a version such as v1.2.\n#: a/a.go\n#, go-format\nmsgid \"Isotope %s is available\"",
		"msgid \"One update\"\nmsgid_plural \"%d updates\"",
		"msgctxt \"verb\"\nmsgid \"Open\"",
		"#: b/sub/b.go\nmsgid \"Close\"",
	} {
		if !strings.Contains(string(pot), want) {
			t.Errorf("template lacks %q:\n%s", want, pot)
		}
	}
	if strings.Contains(string(pot), "Test only") {
		t.Error("a _test.go file was read")
	}
	if strings.Count(string(pot), "msgid \"Updates\"") != 1 {
		t.Error("Updates twice")
	}
	// Untranslated: check names each message of each language.
	problems, err := check(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(problems) != 12 || !strings.Contains(strings.Join(problems, "\n"), "it.po: \"Close\": not translated") {
		t.Fatalf("problems = %d:\n%s", len(problems), strings.Join(problems, "\n"))
	}
	// Translate everything, with one bad verb.
	for _, lang := range []string{"it", "es"} {
		p := filepath.Join(root, poDir, "demo", lang+".po")
		b, _ := os.ReadFile(p)
		s := strings.ReplaceAll(string(b), "msgstr \"\"\n\n", "msgstr \"x\"\n\n")
		s = strings.ReplaceAll(s, "msgstr[0] \"\"", "msgstr[0] \"Un aggiornamento\"")
		s = strings.ReplaceAll(s, "msgstr[1] \"\"", "msgstr[1] \"%d aggiornamenti\"")
		s = strings.Replace(s, "msgid \"Isotope %s is available\"\nmsgstr \"x\"", "msgid \"Isotope %s is available\"\nmsgstr \"Isotope %d\"", 1)
		if strings.HasSuffix(s, "msgstr \"\"\n") {
			s = strings.TrimSuffix(s, "msgstr \"\"\n") + "msgstr \"x\"\n"
		}
		if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	problems, _ = check(root)
	if len(problems) != 2 || !strings.Contains(problems[0], "translation uses") {
		t.Fatalf("problems:\n%s", strings.Join(problems, "\n"))
	}
	// Fix the verb; a second update keeps the translations and changes nothing.
	for _, lang := range []string{"it", "es"} {
		p := filepath.Join(root, poDir, "demo", lang+".po")
		b, _ := os.ReadFile(p)
		os.WriteFile(p, []byte(strings.Replace(string(b), "Isotope %d", "Isotope %s", 1)), 0o644)
	}
	if problems, _ := check(root); len(problems) != 0 {
		t.Fatalf("problems:\n%s", strings.Join(problems, "\n"))
	}
	if err := update(root); err != nil {
		t.Fatal(err)
	}
	if problems, _ := check(root); len(problems) != 0 {
		t.Fatalf("after a second update:\n%s", strings.Join(problems, "\n"))
	}
	// A new message in the source: the template is stale.
	os.WriteFile(filepath.Join(root, "b/sub/b.go"), []byte(strings.Replace(srcB, `tr.T("Close")`, `tr.T("Close") + tr.T("Quit")`, 1)), 0o644)
	problems, _ = check(root)
	if !strings.Contains(strings.Join(problems, "\n"), "demo.pot: not up to date") {
		t.Fatalf("stale template not found:\n%s", strings.Join(problems, "\n"))
	}
	// After update the old translations stay, the new message is empty.
	update(root)
	it, _ := os.ReadFile(filepath.Join(root, poDir, "demo", "it.po"))
	if !strings.Contains(string(it), "msgid \"Quit\"\nmsgstr \"\"") || !strings.Contains(string(it), "msgstr[0] \"Un aggiornamento\"") {
		t.Errorf("merge:\n%s", it)
	}
}

func TestCheckFindsConflictsAndStrays(t *testing.T) {
	root := tree(t)
	os.WriteFile(filepath.Join(root, "b/sub/b.go"), []byte(srcB), 0o644)
	update(root)
	// The same msgid in a second domain with another translation.
	domains = append(domains, domain{"other", []string{"c"}})
	os.MkdirAll(filepath.Join(root, "c"), 0o755)
	os.WriteFile(filepath.Join(root, "c/c.go"), []byte(srcB), 0o644)
	update(root)
	fill := func(dom, word string) {
		for _, lang := range []string{"it", "es"} {
			p := filepath.Join(root, poDir, dom, lang+".po")
			b, _ := os.ReadFile(p)
			s := strings.ReplaceAll(string(b), "msgstr \"\"\n\n", "msgstr \""+word+"\"\n\n")
			s = strings.ReplaceAll(s, "msgstr[0] \"\"", "msgstr[0] \"Un aggiornamento\"")
			s = strings.ReplaceAll(s, "msgstr[1] \"\"", "msgstr[1] \"%d aggiornamenti\"")
			if strings.HasSuffix(s, "msgstr \"\"\n") {
				s = strings.TrimSuffix(s, "msgstr \"\"\n") + "msgstr \"" + word + "\"\n"
			}
			os.WriteFile(p, []byte(s), 0o644)
		}
	}
	fill("demo", "Chiudi")
	fill("other", "Chiudere")
	// A file outside every domain that uses the package.
	os.MkdirAll(filepath.Join(root, "d"), 0o755)
	os.WriteFile(filepath.Join(root, "d/d.go"), []byte(srcB), 0o644)
	problems, err := check(root)
	if err != nil {
		t.Fatal(err)
	}
	all := strings.Join(problems, "\n")
	if !strings.Contains(all, "translated differently") || !strings.Contains(all, "d/d.go: imports pkg/i18n but is in no domain") {
		t.Fatalf("problems:\n%s", all)
	}
}

func TestExtractRejectsNonLiterals(t *testing.T) {
	root := tree(t)
	os.WriteFile(filepath.Join(root, "a/bad.go"), []byte(`package a

import "github.com/kairos-community/isotope/pkg/i18n"

func h(s string) { _ = i18n.Tf(s); _ = i18n.Mark(s) }
`), 0o644)
	if err := update(root); err == nil || !strings.Contains(err.Error(), "bad.go:5") {
		t.Fatalf("err = %v", err)
	}
}
