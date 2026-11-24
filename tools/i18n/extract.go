package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/kairos-community/isotope/pkg/i18n/gettext"
)

// call is how each marker function's arguments are read: the index of the
// msgctxt, msgid and msgid_plural arguments (-1: none) and whether the
// message is a format string.
type call struct {
	ctx, id, plural int
	format          bool
}

var calls = map[string]call{
	"T":    {-1, 0, -1, false},
	"Tf":   {-1, 0, -1, true},
	"X":    {0, 1, -1, false},
	"N":    {-1, 0, 1, false},
	"Nf":   {-1, 0, 1, true},
	"Mark": {-1, 0, -1, false},
}

// goFiles is the non-test Go files under dir (testdata left out), sorted.
func goFiles(root, dir string) ([]string, error) {
	var out []string
	if strings.HasSuffix(dir, ".go") {
		return []string{filepath.Join(root, dir)}, nil
	}
	err := filepath.WalkDir(filepath.Join(root, dir), func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == "testdata" {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(p, ".go") && !strings.HasSuffix(p, "_test.go") {
			out = append(out, p)
		}
		return nil
	})
	sort.Strings(out)
	return out, err
}

// i18nName is the name the file imports pkg/i18n as, "" when it does not.
func i18nName(f *ast.File) string {
	for _, im := range f.Imports {
		p, _ := strconv.Unquote(im.Path.Value)
		if p != importPath {
			continue
		}
		if im.Name != nil {
			return im.Name.Name
		}
		return "i18n"
	}
	return ""
}

// constString folds a string literal or a + of them, ok false otherwise.
func constString(e ast.Expr) (string, bool) {
	switch x := e.(type) {
	case *ast.BasicLit:
		if x.Kind != token.STRING {
			return "", false
		}
		s, err := strconv.Unquote(x.Value)
		return s, err == nil
	case *ast.ParenExpr:
		return constString(x.X)
	case *ast.BinaryExpr:
		if x.Op != token.ADD {
			return "", false
		}
		a, ok1 := constString(x.X)
		b, ok2 := constString(x.Y)
		return a + b, ok1 && ok2
	}
	return "", false
}

// extract reads a domain's messages into a template: entries in the order
// of their first use (files sorted by path), each with the files that use
// it (no line numbers, so moving code does not change the template).
func extract(root string, d domain) (*gettext.File, error) {
	pot := &gettext.File{Header: &gettext.Entry{Str: []string{
		"Project-Id-Version: isotope " + d.Name + "\n" +
			"MIME-Version: 1.0\n" +
			"Content-Type: text/plain; charset=UTF-8\n" +
			"Content-Transfer-Encoding: 8bit\n",
	}}}
	idx := map[gettext.Key]*gettext.Entry{}
	var errs []string
	for _, dir := range d.Dirs {
		files, err := goFiles(root, dir)
		if err != nil {
			return nil, err
		}
		for _, p := range files {
			fset := token.NewFileSet()
			f, err := parser.ParseFile(fset, p, nil, parser.ParseComments)
			if err != nil {
				return nil, err
			}
			name := i18nName(f)
			if name == "" {
				continue
			}
			rel, _ := filepath.Rel(root, p)
			rel = filepath.ToSlash(rel)
			notes := translatorNotes(fset, f)
			ast.Inspect(f, func(n ast.Node) bool {
				ce, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := ce.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				pkg, ok := sel.X.(*ast.Ident)
				if !ok || pkg.Name != name {
					return true
				}
				c, ok := calls[sel.Sel.Name]
				if !ok {
					return true
				}
				pos := fset.Position(ce.Pos())
				at := fmt.Sprintf("%s:%d", rel, pos.Line)
				arg := func(i int) (string, bool) {
					if i < 0 {
						return "", true
					}
					if i >= len(ce.Args) {
						return "", false
					}
					return constString(ce.Args[i])
				}
				id, ok := arg(c.id)
				if !ok {
					// T(variable) translates a string marked elsewhere;
					// anything else must be a literal.
					if sel.Sel.Name != "T" && sel.Sel.Name != "X" {
						errs = append(errs, fmt.Sprintf("%s: %s.%s needs a string literal", at, name, sel.Sel.Name))
					}
					return true
				}
				ctx, ok1 := arg(c.ctx)
				plural, ok2 := arg(c.plural)
				if !ok1 || !ok2 {
					errs = append(errs, fmt.Sprintf("%s: %s.%s needs string literals", at, name, sel.Sel.Name))
					return true
				}
				if id == "" {
					errs = append(errs, fmt.Sprintf("%s: empty message", at))
					return true
				}
				if c.format {
					if _, err := gettext.ParseVerbs(id); err != nil {
						errs = append(errs, fmt.Sprintf("%s: %v", at, err))
					}
				}
				key := gettext.Key{Context: ctx, ID: id}
				e := idx[key]
				if e == nil {
					e = &gettext.Entry{Context: ctx, ID: id, Plural: plural}
					idx[key] = e
					pot.Entries = append(pot.Entries, e)
				} else if e.Plural != plural {
					errs = append(errs, fmt.Sprintf("%s: %q is used with two plural forms (%q and %q)", at, id, e.Plural, plural))
				}
				if !contains(e.Refs, rel) {
					e.Refs = append(e.Refs, rel)
				}
				if c.format && !e.HasFlag("go-format") {
					e.Flags = append(e.Flags, "go-format")
				}
				if note := notes[pos.Line]; note != "" && !contains(e.Extracted, note) {
					e.Extracted = append(e.Extracted, note)
				}
				return true
			})
		}
	}
	if len(errs) > 0 {
		return nil, fmt.Errorf("domain %s:\n  %s", d.Name, strings.Join(errs, "\n  "))
	}
	for _, e := range pot.Entries {
		e.Str = make([]string, 1)
		if e.Plural != "" {
			e.Str = make([]string, 2)
		}
	}
	return pot, nil
}

// translatorNotes maps a line to the "TRANSLATORS:" comment that ends on
// it or on the line before it.
func translatorNotes(fset *token.FileSet, f *ast.File) map[int]string {
	m := map[int]string{}
	for _, cg := range f.Comments {
		text := strings.TrimSpace(cg.Text())
		i := strings.Index(text, "TRANSLATORS:")
		if i < 0 {
			continue
		}
		note := strings.Join(strings.Fields(text[i:]), " ")
		end := fset.Position(cg.End()).Line
		m[end] = note
		m[end+1] = note
	}
	return m
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// strayCalls is every Go file outside the domains that imports pkg/i18n:
// its strings would never reach a catalog.
func strayCalls(root string) ([]string, error) {
	inDomain := func(rel string) bool {
		for _, d := range domains {
			for _, dir := range d.Dirs {
				if strings.HasPrefix(rel, dir+"/") {
					return true
				}
			}
		}
		return strings.HasPrefix(rel, "pkg/i18n/") || strings.HasPrefix(rel, "tools/i18n/")
	}
	var problems []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			switch d.Name() {
			case ".git", "testdata", "node_modules", "build":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") || inDomain(rel) {
			return nil
		}
		f, err := parser.ParseFile(token.NewFileSet(), p, nil, parser.ImportsOnly)
		if err != nil {
			return nil
		}
		if i18nName(f) != "" {
			problems = append(problems, fmt.Sprintf("%s: imports pkg/i18n but is in no domain of tools/i18n", rel))
		}
		return nil
	})
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	return problems, nil
}
