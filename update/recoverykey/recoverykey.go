// Package recoverykey is the recovery key format, as in
// install-ui/core/recoverykey.go and systemd: 32 random bytes, each nibble
// one of 16 modhex letters, 8 groups of 8, dash-separated.
package recoverykey

import (
	"crypto/rand"
	"fmt"
	"io"
	"strings"
)

// Alphabet is the 16 modhex letters.
const Alphabet = "cbdefghijklnrtuv"

// Generate reads 32 bytes from r and returns them as a key.
func Generate(r io.Reader) (string, error) {
	buf := make([]byte, 32)
	if _, err := io.ReadFull(r, buf); err != nil {
		return "", fmt.Errorf("reading random bytes for the recovery key: %w", err)
	}
	var b strings.Builder
	for i, c := range buf {
		if i > 0 && i%4 == 0 {
			b.WriteByte('-')
		}
		b.WriteByte(Alphabet[c>>4])
		b.WriteByte(Alphabet[c&0x0f])
	}
	return b.String(), nil
}

// New returns a new key from the system random source.
func New() (string, error) { return Generate(rand.Reader) }

// Canonical returns s as a key in its canonical form (lower case, dashes),
// or "" when s is not 64 modhex letters once spaces and dashes are dropped.
func Canonical(s string) string {
	var chars []rune
	for _, r := range strings.ToLower(s) {
		switch {
		case r == '-' || r == ' ' || r == '\t':
		case strings.ContainsRune(Alphabet, r):
			chars = append(chars, r)
		default:
			return ""
		}
	}
	if len(chars) != 64 {
		return ""
	}
	var b strings.Builder
	for i := 0; i < 64; i += 8 {
		if i > 0 {
			b.WriteByte('-')
		}
		b.WriteString(string(chars[i : i+8]))
	}
	return b.String()
}

// Lines shows the key two groups per line, as "aaaaaaaa-bbbbbbbb". It
// returns nil when key is not a key.
func Lines(key string) []string {
	c := Canonical(key)
	if c == "" {
		return nil
	}
	g := strings.Split(c, "-")
	var out []string
	for i := 0; i < len(g); i += 2 {
		out = append(out, g[i]+"-"+g[i+1])
	}
	return out
}

// ConfirmsGroup tells if typed is group number group (1-based) of key, or
// the whole key. Spaces, dashes and case are ignored.
func ConfirmsGroup(key string, group int, typed string) bool {
	t := strings.NewReplacer("-", "", " ", "").Replace(strings.ToLower(strings.TrimSpace(typed)))
	if t == "" {
		return false
	}
	if t == strings.ReplaceAll(key, "-", "") {
		return true
	}
	g := strings.Split(key, "-")
	return group >= 1 && group <= len(g) && t == g[group-1]
}
