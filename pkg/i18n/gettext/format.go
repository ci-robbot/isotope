package gettext

import (
	"fmt"
	"sort"
	"strings"
)

// Verbs is what a Go format string does with its arguments: for each
// argument (0-based) the verb that formats it. "%[2]s %[1]d" and
// "%d %s" use the same arguments as different verbs, and so differ;
// "%[2]s di %[1]s" and "%s of %s" swap them and so differ too, which is why
// a translation that reorders must say so with indexes on both sides
// (see SameVerbs).
type Verbs map[int]rune

// ParseVerbs reads the verbs of a Go format string as fmt does: an
// explicit [n] sets the next argument, a * width or precision takes one.
func ParseVerbs(format string) (Verbs, error) {
	v := Verbs{}
	arg := 0
	for i := 0; i < len(format); i++ {
		if format[i] != '%' {
			continue
		}
		i++
		if i < len(format) && format[i] == '%' {
			continue
		}
		// flags
		for i < len(format) && strings.IndexByte("+-# 0", format[i]) >= 0 {
			i++
		}
		index := func() error {
			if i < len(format) && format[i] == '[' {
				j := strings.IndexByte(format[i:], ']')
				if j < 0 {
					return fmt.Errorf("unclosed [ in %q", format)
				}
				var n int
				if _, err := fmt.Sscanf(format[i+1:i+j], "%d", &n); err != nil || n < 1 {
					return fmt.Errorf("bad argument index in %q", format)
				}
				arg = n - 1
				i += j + 1
			}
			return nil
		}
		star := func() {
			if i < len(format) && format[i] == '*' {
				v[arg] = '*'
				arg++
				i++
				return
			}
			for i < len(format) && format[i] >= '0' && format[i] <= '9' {
				i++
			}
		}
		if err := index(); err != nil {
			return nil, err
		}
		star() // width
		if i < len(format) && format[i] == '.' {
			i++
			if err := index(); err != nil {
				return nil, err
			}
			star() // precision
		}
		if err := index(); err != nil {
			return nil, err
		}
		if i >= len(format) {
			return nil, fmt.Errorf("format %q ends inside a verb", format)
		}
		r := rune(format[i])
		if prev, ok := v[arg]; ok && prev != r {
			return nil, fmt.Errorf("argument %d of %q is formatted as both %%%c and %%%c", arg+1, format, prev, r)
		}
		v[arg] = r
		arg++
	}
	return v, nil
}

// Count is the number of arguments the format uses: its highest argument
// plus one.
func (v Verbs) Count() int {
	n := 0
	for a := range v {
		if a+1 > n {
			n = a + 1
		}
	}
	return n
}

func (v Verbs) String() string {
	keys := make([]int, 0, len(v))
	for a := range v {
		keys = append(keys, a)
	}
	sort.Ints(keys)
	var parts []string
	for _, a := range keys {
		parts = append(parts, fmt.Sprintf("%%[%d]%c", a+1, v[a]))
	}
	return strings.Join(parts, " ")
}

// SameVerbs says whether translation formats exactly the arguments source
// formats, each with the same verb. With subset, translation may leave
// arguments out (a plural form such as "One app" for "%d apps").
func SameVerbs(source, translation string, subset bool) error {
	s, err := ParseVerbs(source)
	if err != nil {
		return err
	}
	t, err := ParseVerbs(translation)
	if err != nil {
		return err
	}
	for a, r := range t {
		if sr, ok := s[a]; !ok || sr != r {
			return fmt.Errorf("translation uses %s, the source %s", t, s)
		}
	}
	if !subset && len(t) != len(s) {
		return fmt.Errorf("translation uses %s, the source %s", t, s)
	}
	return nil
}
