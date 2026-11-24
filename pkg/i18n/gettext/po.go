// Package gettext reads and writes gettext PO and POT files, evaluates
// their Plural-Forms expressions and compares the Go format verbs of a
// message with those of its translation. It is the part of pkg/i18n that
// the runtime and the catalog tool (tools/i18n) share. Pure Go.
//
// The subset is the one GNU gettext writes: comments (`#`, `#.`, `#:`,
// `#,`, `#|`), msgctxt, msgid, msgid_plural, msgstr and msgstr[N], with
// strings continued over several lines, and obsolete entries (`#~`), which
// are skipped.
package gettext

import (
	"bufio"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
)

// Entry is one message of a catalog.
type Entry struct {
	Context    string   // msgctxt, "" for none
	ID         string   // msgid: the English source string
	Plural     string   // msgid_plural, "" for a message without plural forms
	Str        []string // msgstr, or msgstr[0..n-1] for a plural message
	Translator []string // "# " comments, kept as written by the translator
	Extracted  []string // "#." comments, from the source
	Refs       []string // "#:" references
	Flags      []string // "#," flags: fuzzy, go-format
}

// Key is how a catalog finds an entry: its context and its msgid.
type Key struct{ Context, ID string }

// Key of the entry.
func (e *Entry) Key() Key { return Key{e.Context, e.ID} }

// HasFlag says whether the entry carries flag f.
func (e *Entry) HasFlag(f string) bool {
	for _, x := range e.Flags {
		if x == f {
			return true
		}
	}
	return false
}

// Translated says every form of the entry has a non-empty msgstr and the
// entry is not fuzzy.
func (e *Entry) Translated() bool {
	if e.HasFlag("fuzzy") || len(e.Str) == 0 {
		return false
	}
	for _, s := range e.Str {
		if s == "" {
			return false
		}
	}
	return true
}

// File is a PO or POT file: its header (the entry with an empty msgid)
// and its messages in file order.
type File struct {
	Header  *Entry
	Entries []*Entry
}

// HeaderField is the value of one "Name: value" line of the header, "" when
// it is missing.
func (f *File) HeaderField(name string) string {
	if f.Header == nil || len(f.Header.Str) == 0 {
		return ""
	}
	for _, line := range strings.Split(f.Header.Str[0], "\n") {
		k, v, ok := strings.Cut(line, ":")
		if ok && strings.EqualFold(strings.TrimSpace(k), name) {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// Parse reads a PO or POT file.
func Parse(r io.Reader) (*File, error) {
	f := &File{}
	var (
		cur     *Entry
		field   *string // the string a continuation line appends to
		lineNo  int
		started bool // cur has seen a msgid (comments alone do not start one)
	)
	flush := func() error {
		if cur == nil {
			return nil
		}
		if !started {
			// Comments with no message after them (end of file): dropped.
			cur = nil
			return nil
		}
		if cur.ID == "" && cur.Context == "" {
			if f.Header != nil {
				return fmt.Errorf("line %d: a second entry with an empty msgid", lineNo)
			}
			f.Header = cur
		} else {
			f.Entries = append(f.Entries, cur)
		}
		cur, field, started = nil, nil, false
		return nil
	}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for sc.Scan() {
		lineNo++
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			if err := flush(); err != nil {
				return nil, err
			}
			continue
		}
		if strings.HasPrefix(line, "#~") {
			// Obsolete entry: gone from the source, not used.
			continue
		}
		if strings.HasPrefix(line, "#") {
			if started {
				// A comment after a message starts the next one.
				if err := flush(); err != nil {
					return nil, err
				}
			}
			if cur == nil {
				cur = &Entry{}
			}
			field = nil
			switch {
			case strings.HasPrefix(line, "#."):
				cur.Extracted = append(cur.Extracted, strings.TrimSpace(line[2:]))
			case strings.HasPrefix(line, "#:"):
				cur.Refs = append(cur.Refs, strings.Fields(line[2:])...)
			case strings.HasPrefix(line, "#,"):
				for _, fl := range strings.Split(line[2:], ",") {
					if fl = strings.TrimSpace(fl); fl != "" {
						cur.Flags = append(cur.Flags, fl)
					}
				}
			case strings.HasPrefix(line, "#|"):
				// Previous msgid of a fuzzy entry: not kept.
			default:
				cur.Translator = append(cur.Translator, strings.TrimPrefix(strings.TrimPrefix(line, "#"), " "))
			}
			continue
		}
		if strings.HasPrefix(line, `"`) {
			if field == nil {
				return nil, fmt.Errorf("line %d: a string with no keyword before it", lineNo)
			}
			s, err := unquote(line)
			if err != nil {
				return nil, fmt.Errorf("line %d: %v", lineNo, err)
			}
			*field += s
			continue
		}
		kw, rest, _ := strings.Cut(line, " ")
		rest = strings.TrimSpace(rest)
		s, err := unquote(rest)
		if err != nil {
			return nil, fmt.Errorf("line %d: %v", lineNo, err)
		}
		switch {
		case kw == "msgctxt":
			if started {
				if err := flush(); err != nil {
					return nil, err
				}
			}
			if cur == nil {
				cur = &Entry{}
			}
			cur.Context = s
			field = &cur.Context
		case kw == "msgid":
			if started && (cur.ID != "" || len(cur.Str) > 0) {
				if err := flush(); err != nil {
					return nil, err
				}
			}
			if cur == nil {
				cur = &Entry{}
			}
			started = true
			cur.ID = s
			field = &cur.ID
		case kw == "msgid_plural":
			if cur == nil || !started {
				return nil, fmt.Errorf("line %d: msgid_plural before msgid", lineNo)
			}
			cur.Plural = s
			field = &cur.Plural
		case kw == "msgstr":
			if cur == nil || !started {
				return nil, fmt.Errorf("line %d: msgstr before msgid", lineNo)
			}
			cur.Str = append(cur.Str, s)
			field = &cur.Str[len(cur.Str)-1]
		case strings.HasPrefix(kw, "msgstr[") && strings.HasSuffix(kw, "]"):
			if cur == nil || !started {
				return nil, fmt.Errorf("line %d: msgstr before msgid", lineNo)
			}
			n, err := strconv.Atoi(kw[len("msgstr[") : len(kw)-1])
			if err != nil || n != len(cur.Str) {
				return nil, fmt.Errorf("line %d: %s out of order", lineNo, kw)
			}
			cur.Str = append(cur.Str, s)
			field = &cur.Str[len(cur.Str)-1]
		default:
			return nil, fmt.Errorf("line %d: unknown keyword %q", lineNo, kw)
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if err := flush(); err != nil {
		return nil, err
	}
	seen := map[Key]bool{}
	for _, e := range f.Entries {
		if seen[e.Key()] {
			return nil, fmt.Errorf("duplicate entry %q", e.ID)
		}
		seen[e.Key()] = true
		if e.Plural != "" && len(e.Str) == 1 {
			return nil, fmt.Errorf("entry %q has msgid_plural but a plain msgstr", e.ID)
		}
		if e.Plural == "" && len(e.Str) > 1 {
			return nil, fmt.Errorf("entry %q has msgstr[N] but no msgid_plural", e.ID)
		}
	}
	return f, nil
}

func unquote(s string) (string, error) {
	if len(s) < 2 || s[0] != '"' || s[len(s)-1] != '"' {
		return "", fmt.Errorf("not a quoted string: %s", s)
	}
	var b strings.Builder
	in := s[1 : len(s)-1]
	for i := 0; i < len(in); i++ {
		c := in[i]
		if c == '"' {
			return "", fmt.Errorf("unescaped quote in %s", s)
		}
		if c != '\\' {
			b.WriteByte(c)
			continue
		}
		i++
		if i == len(in) {
			return "", fmt.Errorf("trailing backslash in %s", s)
		}
		switch in[i] {
		case 'n':
			b.WriteByte('\n')
		case 't':
			b.WriteByte('\t')
		case 'r':
			b.WriteByte('\r')
		case '"':
			b.WriteByte('"')
		case '\\':
			b.WriteByte('\\')
		default:
			return "", fmt.Errorf("unknown escape \\%c in %s", in[i], s)
		}
	}
	return b.String(), nil
}

func quote(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		case '\t':
			b.WriteString(`\t`)
		case '\r':
			b.WriteString(`\r`)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

// writeString writes `kw "s"`, or, when s has line breaks inside it,
// `kw ""` and one quoted line per line of s, as GNU gettext does.
func writeString(w *strings.Builder, kw, s string) {
	i := strings.Index(s, "\n")
	if i < 0 || i == len(s)-1 {
		fmt.Fprintf(w, "%s %s\n", kw, quote(s))
		return
	}
	fmt.Fprintf(w, "%s \"\"\n", kw)
	for s != "" {
		j := strings.Index(s, "\n")
		if j < 0 {
			j = len(s) - 1
		}
		fmt.Fprintf(w, "%s\n", quote(s[:j+1]))
		s = s[j+1:]
	}
}

func writeEntry(w *strings.Builder, e *Entry) {
	for _, c := range e.Translator {
		if c == "" {
			w.WriteString("#\n")
		} else {
			fmt.Fprintf(w, "# %s\n", c)
		}
	}
	for _, c := range e.Extracted {
		fmt.Fprintf(w, "#. %s\n", c)
	}
	for _, r := range e.Refs {
		fmt.Fprintf(w, "#: %s\n", r)
	}
	if len(e.Flags) > 0 {
		fmt.Fprintf(w, "#, %s\n", strings.Join(e.Flags, ", "))
	}
	if e.Context != "" {
		writeString(w, "msgctxt", e.Context)
	}
	writeString(w, "msgid", e.ID)
	if e.Plural != "" {
		writeString(w, "msgid_plural", e.Plural)
		for i, s := range e.Str {
			writeString(w, fmt.Sprintf("msgstr[%d]", i), s)
		}
		return
	}
	s := ""
	if len(e.Str) > 0 {
		s = e.Str[0]
	}
	writeString(w, "msgstr", s)
}

// Bytes is the file in GNU gettext's layout: the header, then each entry
// after a blank line. Lines are not wrapped, so a diff shows one message per
// change.
func (f *File) Bytes() []byte {
	var w strings.Builder
	if f.Header != nil {
		writeEntry(&w, f.Header)
	}
	for _, e := range f.Entries {
		if w.Len() > 0 {
			w.WriteString("\n")
		}
		writeEntry(&w, e)
	}
	return []byte(w.String())
}

// Index maps the entries of f by key.
func (f *File) Index() map[Key]*Entry {
	m := make(map[Key]*Entry, len(f.Entries))
	for _, e := range f.Entries {
		m[e.Key()] = e
	}
	return m
}

// SortedKeys is the keys of m, sorted (tests and stable output).
func SortedKeys(m map[Key]*Entry) []Key {
	ks := make([]Key, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Slice(ks, func(i, j int) bool {
		if ks[i].ID != ks[j].ID {
			return ks[i].ID < ks[j].ID
		}
		return ks[i].Context < ks[j].Context
	})
	return ks
}
