package recoverykey

import (
	"bytes"
	"regexp"
	"strings"
	"testing"
)

const testKey = "cbdefghi-jklnrtuv-cbdefghi-jklnrtuv-cbdefghi-jklnrtuv-cbdefghi-jklnrtuv"

var shape = regexp.MustCompile(`^([cbdefghijklnrtuv]{8}-){7}[cbdefghijklnrtuv]{8}$`)

func TestCanonicalAndGeneratedKeys(t *testing.T) {
	for _, in := range []string{testKey, strings.ToUpper(testKey), strings.ReplaceAll(testKey, "-", " ")} {
		if got := Canonical(in); got != testKey {
			t.Errorf("Canonical(%q) = %q", in, got)
		}
	}
	if Canonical("correct horse battery") != "" {
		t.Error("a passphrase read as a key")
	}
	k, err := Generate(bytes.NewReader(make([]byte, 32)))
	if err != nil || k != "cccccccc-cccccccc-cccccccc-cccccccc-cccccccc-cccccccc-cccccccc-cccccccc" {
		t.Fatalf("%q %v", k, err)
	}
	a, _ := New()
	b, _ := New()
	if a == b || !shape.MatchString(a) {
		t.Fatalf("%q %q", a, b)
	}
}

func TestGenerateShortReader(t *testing.T) {
	if _, err := Generate(bytes.NewReader(make([]byte, 5))); err == nil {
		t.Error("a short random source was accepted")
	}
}

func TestLinesTwoGroupsPerLine(t *testing.T) {
	lines := Lines(testKey)
	if len(lines) != 4 {
		t.Fatalf("got %d lines: %q", len(lines), lines)
	}
	for _, l := range lines {
		if len(l) != 17 || l != "cbdefghi-jklnrtuv" {
			t.Errorf("line %q", l)
		}
	}
	if Lines("not-a-key") != nil {
		t.Error("lines for a malformed key")
	}
}

func TestConfirmsGroup(t *testing.T) {
	for _, c := range []struct {
		group int
		typed string
		want  bool
	}{
		{1, "cbdefghi", true},
		{2, " JKLN rtuv ", true},
		{2, "cbdefghi", false},
		{1, "", false},
		{0, "cbdefghi", false},
		{9, "cbdefghi", false},
		{3, strings.ReplaceAll(testKey, "-", ""), true},
	} {
		if got := ConfirmsGroup(testKey, c.group, c.typed); got != c.want {
			t.Errorf("ConfirmsGroup(%d, %q) = %v", c.group, c.typed, got)
		}
	}
}
