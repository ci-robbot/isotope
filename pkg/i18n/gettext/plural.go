package gettext

import (
	"fmt"
	"strconv"
	"strings"
)

// Plural is a catalog's plural rule: how many forms it has and which form
// a count takes.
type Plural struct {
	N    int
	expr node
}

// English is the rule of the source strings: one form for 1, another for
// every other count.
var English = Plural{N: 2, expr: binary{op: "!=", l: varN{}, r: num(1)}}

// Form is the index of the msgstr form for count n, clamped to the forms
// the rule has.
func (p Plural) Form(n int) int {
	if p.expr == nil {
		return English.Form(n)
	}
	f := p.expr.eval(int64(n))
	if f < 0 || f >= int64(p.N) {
		return 0
	}
	return int(f)
}

// ParsePluralForms reads a header value such as
// "nplurals=2; plural=(n != 1);": the C expression gettext uses, with n,
// integers, ! * / % + - < > <= >= == != && || and ?:.
func ParsePluralForms(s string) (Plural, error) {
	var p Plural
	var exprText string
	for _, part := range strings.Split(s, ";") {
		k, v, ok := strings.Cut(part, "=")
		if !ok {
			continue
		}
		switch strings.TrimSpace(k) {
		case "nplurals":
			n, err := strconv.Atoi(strings.TrimSpace(v))
			if err != nil || n < 1 || n > 6 {
				return p, fmt.Errorf("bad nplurals %q", v)
			}
			p.N = n
		case "plural":
			exprText = strings.TrimSpace(v)
		}
	}
	if p.N == 0 || exprText == "" {
		return p, fmt.Errorf("Plural-Forms needs nplurals and plural: %q", s)
	}
	ps := &parser{s: exprText}
	e, err := ps.ternary()
	if err != nil {
		return p, err
	}
	ps.skip()
	if ps.i != len(ps.s) {
		return p, fmt.Errorf("unexpected %q in plural expression", ps.s[ps.i:])
	}
	p.expr = e
	return p, nil
}

type node interface{ eval(n int64) int64 }

type num int64
type varN struct{}
type unary struct{ x node }
type binary struct {
	op   string
	l, r node
}
type cond struct{ c, a, b node }

func (v num) eval(int64) int64  { return int64(v) }
func (varN) eval(n int64) int64 { return n }
func (u unary) eval(n int64) int64 {
	if u.x.eval(n) == 0 {
		return 1
	}
	return 0
}
func (c cond) eval(n int64) int64 {
	if c.c.eval(n) != 0 {
		return c.a.eval(n)
	}
	return c.b.eval(n)
}
func b2i(b bool) int64 {
	if b {
		return 1
	}
	return 0
}
func (b binary) eval(n int64) int64 {
	l := b.l.eval(n)
	switch b.op {
	case "&&":
		return b2i(l != 0 && b.r.eval(n) != 0)
	case "||":
		return b2i(l != 0 || b.r.eval(n) != 0)
	}
	r := b.r.eval(n)
	switch b.op {
	case "==":
		return b2i(l == r)
	case "!=":
		return b2i(l != r)
	case "<":
		return b2i(l < r)
	case ">":
		return b2i(l > r)
	case "<=":
		return b2i(l <= r)
	case ">=":
		return b2i(l >= r)
	case "+":
		return l + r
	case "-":
		return l - r
	case "*":
		return l * r
	case "/", "%":
		if r == 0 {
			return 0
		}
		if b.op == "/" {
			return l / r
		}
		return l % r
	}
	return 0
}

// parser is a precedence-climbing parser for the plural expression.
type parser struct {
	s string
	i int
}

func (p *parser) skip() {
	for p.i < len(p.s) && (p.s[p.i] == ' ' || p.s[p.i] == '\t') {
		p.i++
	}
}

func (p *parser) peek(tok string) bool {
	p.skip()
	return strings.HasPrefix(p.s[p.i:], tok)
}

func (p *parser) eat(tok string) bool {
	if p.peek(tok) {
		p.i += len(tok)
		return true
	}
	return false
}

func (p *parser) ternary() (node, error) {
	c, err := p.binary(0)
	if err != nil {
		return nil, err
	}
	if !p.eat("?") {
		return c, nil
	}
	a, err := p.ternary()
	if err != nil {
		return nil, err
	}
	if !p.eat(":") {
		return nil, fmt.Errorf("missing : in plural expression")
	}
	b, err := p.ternary()
	if err != nil {
		return nil, err
	}
	return cond{c, a, b}, nil
}

// levels from loosest to tightest; longer operators first within a level.
var levels = [][]string{
	{"||"},
	{"&&"},
	{"==", "!="},
	{"<=", ">=", "<", ">"},
	{"+", "-"},
	{"*", "/", "%"},
}

func (p *parser) binary(level int) (node, error) {
	if level == len(levels) {
		return p.unary()
	}
	l, err := p.binary(level + 1)
	if err != nil {
		return nil, err
	}
	for {
		op := ""
		for _, o := range levels[level] {
			if p.peek(o) && !(o == "<" && p.peek("<=")) && !(o == ">" && p.peek(">=")) {
				op = o
				break
			}
		}
		if op == "" {
			return l, nil
		}
		p.i += len(op)
		r, err := p.binary(level + 1)
		if err != nil {
			return nil, err
		}
		l = binary{op: op, l: l, r: r}
	}
}

func (p *parser) unary() (node, error) {
	if p.peek("!") && !p.peek("!=") {
		p.i++
		x, err := p.unary()
		if err != nil {
			return nil, err
		}
		return unary{x}, nil
	}
	if p.eat("(") {
		e, err := p.ternary()
		if err != nil {
			return nil, err
		}
		if !p.eat(")") {
			return nil, fmt.Errorf("missing ) in plural expression")
		}
		return e, nil
	}
	p.skip()
	if p.eat("n") {
		return varN{}, nil
	}
	j := p.i
	for j < len(p.s) && p.s[j] >= '0' && p.s[j] <= '9' {
		j++
	}
	if j == p.i {
		return nil, fmt.Errorf("unexpected %q in plural expression", p.s[p.i:])
	}
	v, err := strconv.ParseInt(p.s[p.i:j], 10, 64)
	if err != nil {
		return nil, err
	}
	p.i = j
	return num(v), nil
}
