// Package remotedesktop is what the isotope command (cmd/isotope/, which
// sets up and runs the user's VNC server, wayvnc from the remote-desktop
// system extension) and Isotope System (cmd/isotope-system/, the Network
// page's REMOTE DESKTOP section) share: the port, the firewall rule's note,
// the password rules and the status that `isotope remote-desktop status
// --json` prints. See docs/superpowers/specs/2026-11-24-remote-desktop-design.md.
package remotedesktop

import (
	"crypto/rand"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Port is where the server listens (TCP, every IPv4 address), VNC's own.
const Port = 5900

// Proto is the firewall protocol of Port.
const Proto = "tcp"

// Note is the note of the firewall rule that remote desktop adds for user,
// and the mark by which it knows that rule is its own: a rule for Port
// with another note was added by someone else, and is never removed.
// Within firewall.CheckNote for any login name (at most 32 characters).
func Note(user string) string { return "Remote desktop (" + user + ")" }

// The length of a password, in characters. The upper bound keeps it inside
// neatvnc's 256-byte buffer.
const (
	MinPassword = 8
	MaxPassword = 64
)

// CheckPassword refuses what wayvnc's configuration file cannot hold, or
// what is too short to stand against guessing: fewer than MinPassword or
// more than MaxPassword characters, a control character, or spaces at
// either end (wayvnc trims them).
func CheckPassword(pw string) error {
	switch n := utf8.RuneCountInString(pw); {
	case !utf8.ValidString(pw):
		return errors.New("the password is not valid text")
	case n < MinPassword:
		return fmt.Errorf("use at least %d characters", MinPassword)
	case n > MaxPassword || len(pw) > 255:
		return fmt.Errorf("use at most %d characters", MaxPassword)
	case strings.TrimSpace(pw) != pw:
		return errors.New("the password cannot start or end with a space")
	}
	for _, r := range pw {
		if unicode.IsControl(r) {
			return errors.New("the password cannot have a tab, a new line or another control character")
		}
	}
	return nil
}

// passwordAlphabet has no letters that look alike (0 and o, 1 and l), so a
// generated password can be read out and typed on another computer.
const passwordAlphabet = "abcdefghijkmnpqrstuvwxyz23456789"

// GeneratePassword is four groups of five random characters, 100 bits:
// "k3tqa-wm9xe-...".
func GeneratePassword() (string, error) {
	max := big.NewInt(int64(len(passwordAlphabet)))
	var b strings.Builder
	for i := 0; i < 20; i++ {
		if i > 0 && i%5 == 0 {
			b.WriteByte('-')
		}
		n, err := rand.Int(rand.Reader, max)
		if err != nil {
			return "", err
		}
		b.WriteByte(passwordAlphabet[n.Int64()])
	}
	return b.String(), nil
}

// The firewall states of a running server (Status.Firewall).
const (
	// FirewallOpen: the firewall allows Port through this user's rule.
	FirewallOpen = "open"
	// FirewallAllowed: another rule allows Port (an administrator's own,
	// or another user's server).
	FirewallAllowed = "allowed"
	// FirewallOff: the firewall is off, so every port is open.
	FirewallOff = "off"
	// FirewallClosed: the firewall is on and nothing allows Port, so
	// nobody can connect.
	FirewallClosed = "closed"
)

// Status is `isotope remote-desktop status --json`.
type Status struct {
	// Available says the server can run in this session. When it is
	// false, Reason says why, and NeedsExtension says that installing the
	// remote-desktop system extension is the way.
	Available      bool   `json:"available"`
	Reason         string `json:"reason,omitempty"`
	NeedsExtension bool   `json:"needs_extension,omitempty"`
	// Running: the server is started for this user.
	Running bool `json:"running"`
	// PasswordSet: a password is saved; the server does not start without.
	PasswordSet bool `json:"password_set"`
	// User is the user name a viewer logs in with: the user's own.
	User string `json:"user"`
	Port int    `json:"port"`
	// Addresses are this computer's IPv4 addresses, to connect to.
	Addresses []string `json:"addresses"`
	// Fingerprint is the SHA-256 fingerprint of the TLS certificate, which
	// a viewer shows on the first connection; empty before the first start.
	Fingerprint string `json:"fingerprint,omitempty"`
	// Firewall is one of the Firewall* states while Running, else empty.
	Firewall string `json:"firewall,omitempty"`
}
