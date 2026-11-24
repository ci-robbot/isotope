package remotedesktop

import (
	"strings"
	"testing"

	"github.com/kairos-community/isotope/pkg/firewall"
)

func TestCheckPassword(t *testing.T) {
	for _, pw := range []string{"12345678", "correct horse battery", "pässwörd", "a=b#c!d$", strings.Repeat("x", MaxPassword)} {
		if err := CheckPassword(pw); err != nil {
			t.Errorf("%q refused: %v", pw, err)
		}
	}
	for _, pw := range []string{"", "1234567", " leading1", "trailing1 ", "tab\there1", "new\nline1", strings.Repeat("x", MaxPassword+1), strings.Repeat("😀", 64), "bad\xffutf8"} {
		if err := CheckPassword(pw); err == nil {
			t.Errorf("%q accepted", pw)
		}
	}
}

func TestGeneratePassword(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 50; i++ {
		pw, err := GeneratePassword()
		if err != nil {
			t.Fatal(err)
		}
		if len(pw) != 23 || strings.Count(pw, "-") != 3 {
			t.Fatalf("%q is not four groups of five", pw)
		}
		if err := CheckPassword(pw); err != nil {
			t.Fatalf("a generated password is refused: %q: %v", pw, err)
		}
		if strings.ContainsAny(pw, "01lo") {
			t.Fatalf("%q has a character that looks like another", pw)
		}
		if seen[pw] {
			t.Fatalf("%q twice", pw)
		}
		seen[pw] = true
	}
}

// The firewall rule's note fits the firewall's own checks for any login
// name, the longest (32 characters) too.
func TestNoteIsAValidFirewallNote(t *testing.T) {
	for _, u := range []string{"anna", "a_b-c", strings.Repeat("u", 32)} {
		if err := firewall.Check(Port, Proto, Note(u)); err != nil {
			t.Errorf("Note(%q) = %q: %v", u, Note(u), err)
		}
	}
}
