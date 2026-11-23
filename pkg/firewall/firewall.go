// Package firewall is what the isotope command (cmd/isotope/, which keeps
// the rules in isotope-updated) and Isotope System (cmd/isotope-system/, which
// asks for them) share about an allowed port: its shape and its checks, so
// the window refuses what the daemon would refuse.
package firewall

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Rule is one allowed port. Over D-Bus it is the struct (qss).
type Rule struct {
	Port  uint16 `yaml:"port"`
	Proto string `yaml:"proto"`
	Note  string `yaml:"note,omitempty"`
}

// String is PORT/PROTO, such as 8080/tcp.
func (r Rule) String() string { return fmt.Sprintf("%d/%s", r.Port, r.Proto) }

// Defaults is the allowed list of a machine with no saved settings: SSH, so
// an installed system that is reached over SSH (every server, and desktops
// managed that way) keeps working when the firewall comes on. It is a rule
// like any other: an administrator can remove it. The image ships the same
// rules as /usr/share/isotope/firewall-default.nft, which the loader uses
// until there are saved settings.
var Defaults = []Rule{{Port: 22, Proto: "tcp", Note: "SSH"}}

// Status is the firewall as isotope-updated reports it: the saved setting
// and what is loaded now, which differ when the unit failed or does not run
// on this boot.
type Status struct {
	// On is the saved setting.
	On bool
	// Active says table inet isotope is loaded now.
	Active bool
	// Skipped says this boot never runs the firewall: a live or install
	// boot of an ISO. Changes are refused then.
	Skipped bool
	// Broken says the saved settings could not be read. On and Rules are
	// then what could still be read from the file; changes are refused
	// until a reset, which keeps those rules.
	Broken bool
	// Reason says why Active is not On, why the boot is Skipped, or why the
	// settings are Broken.
	Reason string
	Rules  []Rule
}

// Summary is the state in a few words: "On", "Off", "On, but not
// running", "Off, but still loaded" or "Not running on this boot".
func (s Status) Summary() string {
	switch {
	case s.Skipped:
		return "Not running on this boot"
	case s.Broken:
		return "The firewall settings could not be read"
	case s.On && s.Active:
		return "On"
	case s.On:
		return "On, but not running"
	case s.Active:
		return "Off, but still loaded"
	}
	return "Off"
}

// MaxNote is the longest note, in characters. It keeps the note inside
// nft's 128-byte comment limit.
const MaxNote = 64

// Check refuses what nft could not take, or what could break out of the
// generated ruleset: a port outside 1-65535, a protocol other than tcp or
// udp, and a note with a quote, a backslash or a control character.
func Check(port int, proto, note string) error {
	if port < 1 || port > 65535 {
		return fmt.Errorf("%d is not a port: use 1 to 65535", port)
	}
	if proto != "tcp" && proto != "udp" {
		return fmt.Errorf("%q is not a protocol: use tcp or udp", proto)
	}
	return CheckNote(note)
}

// CheckNote is the note half of Check.
func CheckNote(note string) error {
	switch {
	case !utf8.ValidString(note):
		return errors.New("the description is not valid text")
	case utf8.RuneCountInString(note) > MaxNote:
		return fmt.Errorf("the description is longer than %d characters", MaxNote)
	case len(note) > 128:
		// nft's comment limit is in bytes: emoji take four each.
		return errors.New("the description is too long for the firewall: use fewer emoji or accented letters")
	}
	for _, r := range note {
		if r == '"' || r == '\\' || !unicode.IsPrint(r) {
			return errors.New("the description can have letters, digits, spaces and punctuation, but no quotes or backslashes")
		}
	}
	return nil
}

// ParsePort reads a port number typed by a person.
func ParsePort(s string) (int, error) {
	port, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		return 0, fmt.Errorf("%q is not a port number", s)
	}
	if port < 1 || port > 65535 {
		return 0, fmt.Errorf("%d is not a port: use 1 to 65535", port)
	}
	return port, nil
}

// ParsePortProto reads PORT/PROTO, such as 8080/tcp.
func ParsePortProto(s string) (int, string, error) {
	p, proto, ok := strings.Cut(s, "/")
	if !ok {
		return 0, "", fmt.Errorf("%q: give the port and the protocol, for example 8080/tcp", s)
	}
	port, err := ParsePort(p)
	if err != nil {
		return 0, "", err
	}
	return port, proto, Check(port, proto, "")
}
