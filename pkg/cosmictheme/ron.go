package cosmictheme

import (
	"fmt"
	"image/color"
	"strconv"
	"strings"
)

// value is one RON value of the subset cosmic-config writes for a theme: a
// struct of named fields, a tuple, a string, a number, a boolean, Some(x) or
// None. A struct's fields keep no order.
type value struct {
	str    *string
	num    *float64
	b      *bool
	fields map[string]value // a struct: ( name: v, ... )
	items  []value          // a tuple: ( v, v ), or Some(v) as one item
	none   bool
}

// parse reads one value from src; anything after it but blanks is an error.
func parse(src []byte) (value, error) {
	p := &parser{s: string(src)}
	v, err := p.value()
	if err != nil {
		return value{}, err
	}
	p.blank()
	if p.i != len(p.s) {
		return value{}, p.errorf("trailing text")
	}
	return v, nil
}

type parser struct {
	s string
	i int
}

func (p *parser) errorf(f string, a ...any) error {
	return fmt.Errorf("offset %d: %s", p.i, fmt.Sprintf(f, a...))
}

// blank skips spaces and // comments.
func (p *parser) blank() {
	for p.i < len(p.s) {
		switch c := p.s[p.i]; {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			p.i++
		case strings.HasPrefix(p.s[p.i:], "//"):
			for p.i < len(p.s) && p.s[p.i] != '\n' {
				p.i++
			}
		default:
			return
		}
	}
}

func (p *parser) value() (value, error) {
	p.blank()
	if p.i >= len(p.s) {
		return value{}, p.errorf("no value")
	}
	switch c := p.s[p.i]; {
	case c == '"':
		s, err := p.str()
		return value{str: &s}, err
	case c == '(':
		return p.group()
	case c == '-' || c == '+' || c == '.' || (c >= '0' && c <= '9'):
		return p.number()
	case isIdent(c):
		id := p.ident()
		switch id {
		case "true", "false":
			b := id == "true"
			return value{b: &b}, nil
		case "None":
			return value{none: true}, nil
		}
		// Some(x), or a named struct or tuple: Name( ... ).
		p.blank()
		if p.i < len(p.s) && p.s[p.i] == '(' {
			return p.group()
		}
		// A unit enum variant ("Round"): kept as its name.
		return value{str: &id}, nil
	}
	return value{}, p.errorf("unexpected %q", p.s[p.i])
}

func isIdent(c byte) bool {
	return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
}

func (p *parser) ident() string {
	j := p.i
	for p.i < len(p.s) && isIdent(p.s[p.i]) {
		p.i++
	}
	return p.s[j:p.i]
}

func (p *parser) str() (string, error) {
	j := p.i
	p.i++
	for p.i < len(p.s) {
		switch p.s[p.i] {
		case '\\':
			p.i += 2
			continue
		case '"':
			p.i++
			return strconv.Unquote(p.s[j:p.i])
		}
		p.i++
	}
	return "", p.errorf("unterminated string")
}

func (p *parser) number() (value, error) {
	j := p.i
	for p.i < len(p.s) && strings.IndexByte("+-.0123456789eE_", p.s[p.i]) >= 0 {
		p.i++
	}
	f, err := strconv.ParseFloat(strings.ReplaceAll(p.s[j:p.i], "_", ""), 64)
	if err != nil {
		return value{}, p.errorf("number %q", p.s[j:p.i])
	}
	return value{num: &f}, nil
}

// group reads ( ... ): a struct when its first entry is "name:", else a
// tuple. An empty () is an empty tuple.
func (p *parser) group() (value, error) {
	p.i++ // (
	var v value
	for {
		p.blank()
		if p.i >= len(p.s) {
			return value{}, p.errorf("unterminated (")
		}
		if p.s[p.i] == ')' {
			p.i++
			if v.fields == nil && v.items == nil {
				v.items = []value{}
			}
			return v, nil
		}
		// A field name: an identifier followed by ':'.
		j := p.i
		if isIdent(p.s[p.i]) && !(p.s[p.i] >= '0' && p.s[p.i] <= '9') {
			name := p.ident()
			p.blank()
			if p.i < len(p.s) && p.s[p.i] == ':' {
				if v.items != nil {
					return value{}, p.errorf("field %s in a tuple", name)
				}
				p.i++
				fv, err := p.value()
				if err != nil {
					return value{}, err
				}
				if v.fields == nil {
					v.fields = map[string]value{}
				}
				v.fields[name] = fv
				if err := p.sep(); err != nil {
					return value{}, err
				}
				continue
			}
			p.i = j
		}
		if v.fields != nil {
			return value{}, p.errorf("tuple item in a struct")
		}
		iv, err := p.value()
		if err != nil {
			return value{}, err
		}
		v.items = append(v.items, iv)
		if err := p.sep(); err != nil {
			return value{}, err
		}
	}
}

// sep takes the comma after an entry, or sees the closing parenthesis.
func (p *parser) sep() error {
	p.blank()
	if p.i < len(p.s) && p.s[p.i] == ',' {
		p.i++
		return nil
	}
	if p.i < len(p.s) && p.s[p.i] == ')' {
		return nil
	}
	return p.errorf("expected , or )")
}

// colour reads a colour: "#RRGGBB" or "#RRGGBBAA" (cosmic-theme v2), or a
// struct of float red, green, blue and alpha from 0 to 1 (older themes).
func (v value) colour() (color.NRGBA, error) {
	if v.str != nil {
		h, ok := strings.CutPrefix(*v.str, "#")
		if len(h) == 6 {
			h += "ff"
		}
		if !ok || len(h) != 8 {
			return color.NRGBA{}, fmt.Errorf("colour %q is not #RRGGBB[AA]", *v.str)
		}
		n, err := strconv.ParseUint(h, 16, 32)
		if err != nil {
			return color.NRGBA{}, fmt.Errorf("colour %q: %v", *v.str, err)
		}
		return color.NRGBA{R: uint8(n >> 24), G: uint8(n >> 16), B: uint8(n >> 8), A: uint8(n)}, nil
	}
	if v.fields != nil {
		var c [4]uint8
		for i, k := range []string{"red", "green", "blue", "alpha"} {
			f, ok := v.fields[k]
			if !ok || f.num == nil {
				if k == "alpha" {
					c[i] = 0xff
					continue
				}
				return color.NRGBA{}, fmt.Errorf("colour has no %s", k)
			}
			x := min(max(*f.num, 0), 1)
			c[i] = uint8(x*255 + 0.5)
		}
		return color.NRGBA{R: c[0], G: c[1], B: c[2], A: c[3]}, nil
	}
	return color.NRGBA{}, fmt.Errorf("not a colour")
}

// field is the field name of a struct.
func (v value) field(name string) (value, error) {
	f, ok := v.fields[name]
	if !ok {
		return value{}, fmt.Errorf("no field %s", name)
	}
	return f, nil
}

// colourField is the colour in field name.
func (v value) colourField(name string) (color.NRGBA, error) {
	f, err := v.field(name)
	if err != nil {
		return color.NRGBA{}, err
	}
	c, err := f.colour()
	if err != nil {
		return color.NRGBA{}, fmt.Errorf("%s: %v", name, err)
	}
	return c, nil
}
