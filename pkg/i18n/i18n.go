// Package i18n translates the user-visible strings of Isotope's graphical
// tools (Isotope System, Isotope Welcome, the installer) and of the tray.
//
// The catalogs are gettext PO files embedded in the binary
// (po/<domain>/<lang>.po). A message is its English source string; a
// missing or empty translation falls back to it. Until Init or SetLanguage
// picks a language, every call returns English, so a command-line program
// that shares code with the tray stays English and tests see the source.
//
// Mark a string where it is written:
//
//	i18n.T("Check for updates")
//	i18n.Tf("Isotope %s is available", v)
//	i18n.Nf("%d app update", "%d app updates", n, n)
//	i18n.X("verb", "Open")              // a context for a short, ambiguous word
//	var title = i18n.Mark("Updates")    // marked only; translate with T(title) where it is shown
//
// The first arguments must be string literals (or constant concatenations
// of literals): tools/i18n reads them from the source to write the
// catalogs' templates. A format string with two or more verbs that a
// translation may reorder should use indexed verbs (%[1]s, %[2]d).
// Pure Go: cmd/isotope links it.
package i18n

import (
	"embed"
	"fmt"
	"io/fs"
	"os"
	"path"
	"sort"
	"strings"
	"sync"

	"github.com/kairos-community/isotope/pkg/i18n/gettext"
)

//go:embed po/*/*.po
var embedded embed.FS

// catalogs is the catalog tree, po/<domain>/<lang>.po; tests swap it.
var catalogs fs.FS = embedded

type catalog struct {
	lang   string
	msgs   map[gettext.Key][]string
	plural gettext.Plural
}

var (
	mu  sync.RWMutex
	cur *catalog // nil: English
)

// Languages is the languages that have a catalog, sorted ("es", "it", ...).
func Languages() []string {
	seen := map[string]bool{}
	domains, _ := fs.ReadDir(catalogs, "po")
	for _, d := range domains {
		if !d.IsDir() {
			continue
		}
		files, _ := fs.ReadDir(catalogs, path.Join("po", d.Name()))
		for _, f := range files {
			if strings.HasSuffix(f.Name(), ".po") {
				seen[strings.TrimSuffix(f.Name(), ".po")] = true
			}
		}
	}
	out := make([]string, 0, len(seen))
	for l := range seen {
		out = append(out, l)
	}
	sort.Strings(out)
	return out
}

// Candidates is the list of catalog names a locale environment asks for,
// in gettext's order: LANGUAGE (a colon-separated list) when the locale is
// set and is not C or POSIX, then the first set of LC_ALL, LC_MESSAGES and
// LANG. Each locale name gives, without its encoding, the name, the name
// without its modifier, then the language alone: it_IT.UTF-8@euro gives
// it_IT@euro, it_IT, it@euro, it.
func Candidates(getenv func(string) string) []string {
	locale := ""
	for _, v := range []string{"LC_ALL", "LC_MESSAGES", "LANG"} {
		if s := getenv(v); s != "" {
			locale = s
			break
		}
	}
	if locale == "" || locale == "C" || locale == "POSIX" || strings.HasPrefix(locale, "C.") {
		return nil
	}
	var names []string
	if l := getenv("LANGUAGE"); l != "" {
		names = append(names, strings.Split(l, ":")...)
	}
	names = append(names, locale)
	var out []string
	seen := map[string]bool{}
	add := func(s string) {
		if s != "" && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	for _, n := range names {
		n = strings.TrimSpace(n)
		if n == "" || n == "C" || n == "POSIX" {
			continue
		}
		base, mod, _ := strings.Cut(n, "@")
		base, _, _ = strings.Cut(base, ".")
		lang, _, _ := strings.Cut(base, "_")
		if mod != "" {
			add(base + "@" + mod)
		}
		add(base)
		if mod != "" {
			add(lang + "@" + mod)
		}
		add(lang)
	}
	return out
}

// Init picks the language from the process's locale environment (see
// Candidates) and returns it, "" for English.
func Init() string {
	return SetLanguage(Candidates(os.Getenv)...)
}

// SetLanguage uses the first of names that has a catalog and returns it.
// English ("en", "C", none) or no catalog returns "" and turns
// translation off.
func SetLanguage(names ...string) string {
	have := map[string]bool{}
	for _, l := range Languages() {
		have[l] = true
	}
	for _, n := range names {
		if n == "en" || strings.HasPrefix(n, "en_") || strings.HasPrefix(n, "en@") {
			break
		}
		if !have[n] {
			continue
		}
		c, err := load(n)
		if err != nil {
			fmt.Fprintf(os.Stderr, "i18n: %v\n", err)
			continue
		}
		mu.Lock()
		cur = c
		mu.Unlock()
		return n
	}
	mu.Lock()
	cur = nil
	mu.Unlock()
	return ""
}

// Language is the language in use, "" for English.
func Language() string {
	mu.RLock()
	defer mu.RUnlock()
	if cur == nil {
		return ""
	}
	return cur.lang
}

// load merges the catalogs of every domain for lang. Domains are only a
// way to split the work; a msgid means one thing everywhere (tools/i18n
// check fails when two domains translate it differently).
func load(lang string) (*catalog, error) {
	c := &catalog{lang: lang, msgs: map[gettext.Key][]string{}, plural: gettext.English}
	domains, err := fs.ReadDir(catalogs, "po")
	if err != nil {
		return nil, err
	}
	pluralSet := false
	for _, d := range domains {
		p := path.Join("po", d.Name(), lang+".po")
		f, err := catalogs.Open(p)
		if err != nil {
			continue
		}
		pf, err := gettext.Parse(f)
		f.Close()
		if err != nil {
			return nil, fmt.Errorf("%s: %v", p, err)
		}
		if !pluralSet {
			if pfs := pf.HeaderField("Plural-Forms"); pfs != "" {
				pl, err := gettext.ParsePluralForms(pfs)
				if err != nil {
					return nil, fmt.Errorf("%s: %v", p, err)
				}
				c.plural, pluralSet = pl, true
			}
		}
		for _, e := range pf.Entries {
			if !e.Translated() {
				continue
			}
			if _, dup := c.msgs[e.Key()]; !dup {
				c.msgs[e.Key()] = e.Str
			}
		}
	}
	return c, nil
}

func lookup(ctx, id string) ([]string, *catalog) {
	mu.RLock()
	c := cur
	mu.RUnlock()
	if c == nil {
		return nil, nil
	}
	return c.msgs[gettext.Key{Context: ctx, ID: id}], c
}

// T is msgid in the current language.
func T(msgid string) string {
	if s, _ := lookup("", msgid); len(s) == 1 {
		return s[0]
	}
	return msgid
}

// X is msgid in the current language, in a context that tells two uses of
// the same English apart ("verb", "noun", a page name).
func X(context, msgid string) string {
	if s, _ := lookup(context, msgid); len(s) == 1 {
		return s[0]
	}
	return msgid
}

// N is the form of singular/plural for count n in the current language.
func N(singular, plural string, n int) string {
	s, c := lookup("", singular)
	if c != nil && len(s) > 1 {
		if f := c.plural.Form(n); f < len(s) {
			return s[f]
		}
	}
	if n == 1 {
		return singular
	}
	return plural
}

// Mark returns s unchanged. It marks a string for the catalogs where it
// cannot be translated yet (a package-level variable, a table); translate
// it with T where it is shown.
func Mark(s string) string { return s }

// Tf is fmt.Sprintf of the translated format.
func Tf(format string, args ...any) string { return sprintf(T(format), args) }

// Nf is fmt.Sprintf of N's form; a form that leaves the count out ("One
// app update") is fine.
func Nf(singular, plural string, n int, args ...any) string {
	return sprintf(N(singular, plural, n), args)
}

func sprintf(format string, args []any) string {
	if v, err := gettext.ParseVerbs(format); err == nil && !strings.Contains(format, "%[") && v.Count() < len(args) {
		args = args[:v.Count()]
	}
	return fmt.Sprintf(format, args...)
}
