// Command i18n keeps the translation catalogs of pkg/i18n in step with the
// source. Run it from the repository root:
//
//	go run ./tools/i18n update   # rewrite the .pot templates, merge them into every .po
//	go run ./tools/i18n check    # CI: fail on a stale template or catalog, a missing
//	                             # translation, a format verb that does not match
//	go run ./tools/i18n list PATH...  # the messages marked in these files or directories, as JSON
//
// It reads the Go source of each domain's directories for the calls of
// pkg/i18n (T, Tf, X, N, Nf, Mark) and writes po/<domain>/<domain>.pot,
// then po/<domain>/<lang>.po for every language in po/LINGUAS: existing
// translations are kept, new messages are added untranslated, messages gone
// from the source are dropped. It needs no GNU gettext tools.
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/kairos-community/isotope/pkg/i18n/gettext"
)

// poDir is the catalogs' directory, relative to the repository root.
const poDir = "pkg/i18n/po"

// importPath is the package whose calls are extracted.
const importPath = "github.com/kairos-community/isotope/pkg/i18n"

// domain is one catalog: the source directories its messages come from.
// Each directory is read with its subdirectories, except testdata.
type domain struct {
	Name string
	Dirs []string
}

var domains = []domain{
	{"guikit", []string{"pkg/guikit"}},
	{"isotope-installer", []string{"cmd/isotope-install-gui", "cmd/isotope-install-ui", "pkg/installer"}},
	{"isotope-system", []string{"cmd/isotope-system"}},
	{"isotope-tray", []string{"cmd/isotope"}},
	{"isotope-welcome", []string{"cmd/isotope-welcome"}},
}

// pluralForms is the Plural-Forms header a new catalog gets, by language.
// A language not listed here needs its rule added before its catalog can
// be made (see GNU gettext's manual, "Plural forms").
var pluralForms = map[string]string{
	"ca":    "nplurals=2; plural=(n != 1);",
	"da":    "nplurals=2; plural=(n != 1);",
	"de":    "nplurals=2; plural=(n != 1);",
	"el":    "nplurals=2; plural=(n != 1);",
	"es":    "nplurals=2; plural=(n != 1);",
	"fi":    "nplurals=2; plural=(n != 1);",
	"fr":    "nplurals=2; plural=(n > 1);",
	"it":    "nplurals=2; plural=(n != 1);",
	"ja":    "nplurals=1; plural=0;",
	"ko":    "nplurals=1; plural=0;",
	"nb":    "nplurals=2; plural=(n != 1);",
	"nl":    "nplurals=2; plural=(n != 1);",
	"pl":    "nplurals=3; plural=(n==1 ? 0 : n%10>=2 && n%10<=4 && (n%100<10 || n%100>=20) ? 1 : 2);",
	"pt":    "nplurals=2; plural=(n != 1);",
	"pt_BR": "nplurals=2; plural=(n > 1);",
	"ru":    "nplurals=3; plural=(n%10==1 && n%100!=11 ? 0 : n%10>=2 && n%10<=4 && (n%100<10 || n%100>=20) ? 1 : 2);",
	"sv":    "nplurals=2; plural=(n != 1);",
	"tr":    "nplurals=2; plural=(n != 1);",
	"uk":    "nplurals=3; plural=(n%10==1 && n%100!=11 ? 0 : n%10>=2 && n%10<=4 && (n%100<10 || n%100>=20) ? 1 : 2);",
	"zh_CN": "nplurals=1; plural=0;",
	"zh_TW": "nplurals=1; plural=0;",
}

func main() {
	if len(os.Args) > 2 && os.Args[1] == "list" {
		if err := list(".", os.Args[2:]); err != nil {
			fmt.Fprintf(os.Stderr, "i18n: %v\n", err)
			os.Exit(1)
		}
		return
	}
	if len(os.Args) != 2 || (os.Args[1] != "update" && os.Args[1] != "check") {
		fmt.Fprintln(os.Stderr, "usage: go run ./tools/i18n update|check|list PATH...   (from the repository root)")
		os.Exit(2)
	}
	if _, err := os.Stat(filepath.Join(poDir, "LINGUAS")); err != nil {
		fmt.Fprintf(os.Stderr, "i18n: %v (run it from the repository root)\n", err)
		os.Exit(2)
	}
	var problems []string
	var err error
	if os.Args[1] == "update" {
		err = update(".")
	} else {
		problems, err = check(".")
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "i18n: %v\n", err)
		os.Exit(1)
	}
	if len(problems) > 0 {
		for _, p := range problems {
			fmt.Fprintln(os.Stderr, p)
		}
		fmt.Fprintf(os.Stderr, "\n%d problem(s). Run `go run ./tools/i18n update`, then translate the empty msgstr entries in %s/*/*.po.\n", len(problems), poDir)
		os.Exit(1)
	}
	if os.Args[1] == "check" {
		fmt.Println("i18n: templates and catalogs are up to date and complete")
	}
}

// linguas is the languages every catalog must translate completely.
func linguas(root string) ([]string, error) {
	b, err := os.ReadFile(filepath.Join(root, poDir, "LINGUAS"))
	if err != nil {
		return nil, err
	}
	var out []string
	for _, line := range strings.Split(string(b), "\n") {
		line, _, _ = strings.Cut(line, "#")
		out = append(out, strings.Fields(line)...)
	}
	sort.Strings(out)
	return out, nil
}

// result is what update would write: path (relative to root) and bytes.
type result struct {
	path string
	data []byte
}

// build extracts every domain and merges each language; it writes nothing.
func build(root string) ([]result, error) {
	langs, err := linguas(root)
	if err != nil {
		return nil, err
	}
	var out []result
	for _, d := range domains {
		pot, err := extract(root, d)
		if err != nil {
			return nil, err
		}
		dir := filepath.Join(poDir, d.Name)
		out = append(out, result{filepath.Join(dir, d.Name+".pot"), pot.Bytes()})
		for _, lang := range langs {
			p := filepath.Join(dir, lang+".po")
			var old *gettext.File
			if b, err := os.ReadFile(filepath.Join(root, p)); err == nil {
				if old, err = gettext.Parse(bytes.NewReader(b)); err != nil {
					return nil, fmt.Errorf("%s: %v", p, err)
				}
			} else if !os.IsNotExist(err) {
				return nil, err
			}
			merged, err := merge(pot, old, lang, d.Name)
			if err != nil {
				return nil, fmt.Errorf("%s: %v", p, err)
			}
			out = append(out, result{p, merged.Bytes()})
		}
	}
	return out, nil
}

func update(root string) error {
	res, err := build(root)
	if err != nil {
		return err
	}
	for _, r := range res {
		p := filepath.Join(root, r.path)
		if old, err := os.ReadFile(p); err == nil && bytes.Equal(old, r.data) {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(p, r.data, 0o644); err != nil {
			return err
		}
		fmt.Println("wrote", r.path)
	}
	return nil
}

// merge makes the catalog for lang from the template pot and the old
// catalog: the template's entries and order, the old translations.
func merge(pot, old *gettext.File, lang, dom string) (*gettext.File, error) {
	var header *gettext.Entry
	if old != nil && old.Header != nil {
		h := *old.Header
		header = &h
	} else {
		pf, ok := pluralForms[lang]
		if !ok {
			return nil, fmt.Errorf("no Plural-Forms known for %q: add it to pluralForms in tools/i18n/main.go", lang)
		}
		header = &gettext.Entry{Str: []string{
			"Project-Id-Version: isotope " + dom + "\n" +
				"Language: " + lang + "\n" +
				"MIME-Version: 1.0\n" +
				"Content-Type: text/plain; charset=UTF-8\n" +
				"Content-Transfer-Encoding: 8bit\n" +
				"Plural-Forms: " + pf + "\n",
		}}
	}
	f := &gettext.File{Header: header}
	nplurals := 2
	if pfs := f.HeaderField("Plural-Forms"); pfs != "" {
		pl, err := gettext.ParsePluralForms(pfs)
		if err != nil {
			return nil, err
		}
		nplurals = pl.N
	}
	var oldIdx map[gettext.Key]*gettext.Entry
	if old != nil {
		oldIdx = old.Index()
	}
	for _, te := range pot.Entries {
		e := &gettext.Entry{
			Context:   te.Context,
			ID:        te.ID,
			Plural:    te.Plural,
			Extracted: te.Extracted,
			Refs:      te.Refs,
			Flags:     append([]string(nil), te.Flags...),
		}
		forms := 1
		if te.Plural != "" {
			forms = nplurals
		}
		e.Str = make([]string, forms)
		if oe := oldIdx[te.Key()]; oe != nil && (oe.Plural != "") == (te.Plural != "") {
			e.Translator = oe.Translator
			copy(e.Str, oe.Str)
			if oe.HasFlag("fuzzy") || oe.Plural != te.Plural {
				e.Flags = append(e.Flags, "fuzzy")
			}
		}
		f.Entries = append(f.Entries, e)
	}
	return f, nil
}

func check(root string) ([]string, error) {
	var problems []string
	res, err := build(root)
	if err != nil {
		return nil, err
	}
	for _, r := range res {
		old, err := os.ReadFile(filepath.Join(root, r.path))
		switch {
		case os.IsNotExist(err):
			problems = append(problems, r.path+": missing")
		case err != nil:
			return nil, err
		case !bytes.Equal(old, r.data):
			problems = append(problems, r.path+": not up to date with the source")
		}
	}
	// What the catalogs on disk say: complete, verbs right, one meaning per msgid.
	langs, err := linguas(root)
	if err != nil {
		return nil, err
	}
	required := map[string]bool{}
	for _, l := range langs {
		required[l] = true
	}
	type seenAt struct {
		str  []string
		path string
	}
	seen := map[string]map[gettext.Key]seenAt{}
	files, _ := filepath.Glob(filepath.Join(root, poDir, "*", "*.po"))
	sort.Strings(files)
	for _, p := range files {
		rel, _ := filepath.Rel(root, p)
		lang := strings.TrimSuffix(filepath.Base(p), ".po")
		b, err := os.ReadFile(p)
		if err != nil {
			return nil, err
		}
		f, err := gettext.Parse(bytes.NewReader(b))
		if err != nil {
			problems = append(problems, fmt.Sprintf("%s: %v", rel, err))
			continue
		}
		pl, err := gettext.ParsePluralForms(f.HeaderField("Plural-Forms"))
		if err != nil {
			problems = append(problems, fmt.Sprintf("%s: %v", rel, err))
			continue
		}
		if seen[lang] == nil {
			seen[lang] = map[gettext.Key]seenAt{}
		}
		for _, e := range f.Entries {
			where := fmt.Sprintf("%s: %q", rel, e.ID)
			if e.Context != "" {
				where = fmt.Sprintf("%s: %q (context %q)", rel, e.ID, e.Context)
			}
			if e.Plural != "" && len(e.Str) != pl.N {
				problems = append(problems, fmt.Sprintf("%s: %d plural forms, the header says %d", where, len(e.Str), pl.N))
			}
			if !e.Translated() {
				if required[lang] {
					if e.HasFlag("fuzzy") {
						problems = append(problems, where+": fuzzy, check the translation and drop the flag")
					} else {
						problems = append(problems, where+": not translated")
					}
				}
				continue
			}
			if e.HasFlag("go-format") {
				for i, s := range e.Str {
					src, subset := e.ID, false
					if e.Plural != "" {
						src, subset = e.Plural, true
					}
					if err := gettext.SameVerbs(src, s, subset); err != nil {
						problems = append(problems, fmt.Sprintf("%s: msgstr[%d]: %v", where, i, err))
					}
				}
			}
			if prev, ok := seen[lang][e.Key()]; ok && strings.Join(prev.str, "\x00") != strings.Join(e.Str, "\x00") {
				problems = append(problems, fmt.Sprintf("%s: translated differently in %s; use one translation, or tell the uses apart with i18n.X", where, prev.path))
			} else if !ok {
				seen[lang][e.Key()] = seenAt{e.Str, rel}
			}
		}
	}
	strays, err := strayCalls(root)
	if err != nil {
		return nil, err
	}
	problems = append(problems, strays...)
	return problems, nil
}

// list prints the messages marked in paths (files or directories), one
// JSON object per line: ctx, id, plural, format.
func list(root string, paths []string) error {
	for _, p := range paths {
		pot, err := extract(root, domain{Name: "list", Dirs: []string{p}})
		if err != nil {
			return err
		}
		for _, e := range pot.Entries {
			b, _ := json.Marshal(map[string]any{"ctx": e.Context, "id": e.ID, "plural": e.Plural, "format": e.HasFlag("go-format")})
			fmt.Println(string(b))
		}
	}
	return nil
}
