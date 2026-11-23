package main

import (
	"os"
	"path/filepath"
	"strings"
)

// Stamp records how the first-login flow ended.
type Stamp string

const (
	StampDone        Stamp = "completed"
	StampSkipped     Stamp = "skipped"
	StampAppsPending Stamp = "apps-pending"
)

const stampName = "welcome"

// StateDir is $XDG_STATE_HOME/isotope, default ~/.local/state/isotope.
func StateDir(home string, env func(string) string) string {
	base := env("XDG_STATE_HOME")
	if base == "" {
		base = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(base, "isotope")
}

// ShouldOpenAtLogin is false once the new stamp or any stamp of the old
// terminal checklist exists. The old scripts keep theirs in ${state%/*}/hadron.
func ShouldOpenAtLogin(stateDir string) bool {
	parent := filepath.Dir(stateDir)
	for _, p := range []string{
		filepath.Join(stateDir, stampName),
		filepath.Join(stateDir, "first-run-apps"),
		filepath.Join(stateDir, "welcomed"),
		filepath.Join(parent, "hadron", "first-run-apps"),
		filepath.Join(parent, "hadron", "welcomed"),
	} {
		if _, err := os.Stat(p); err == nil {
			return false
		}
	}
	return true
}

// WriteStamp writes the stamp, creating the state directory.
func WriteStamp(stateDir string, s Stamp) error {
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		return err
	}
	p := filepath.Join(stateDir, stampName)
	if err := os.WriteFile(p, []byte(s+"\n"), 0o644); err != nil {
		return err
	}
	return os.Chmod(p, 0o644)
}

// ReadStamp returns the stamp and whether one exists.
func ReadStamp(stateDir string) (Stamp, bool) {
	b, err := os.ReadFile(filepath.Join(stateDir, stampName))
	if err != nil {
		return "", false
	}
	return Stamp(strings.TrimSpace(string(b))), true
}

// ShouldRemindApps is true when the flow ended with Apps skipped for lack of
// a network or a Flathub: the login sends the reminder notification.
func ShouldRemindApps(stateDir string) bool {
	s, ok := ReadStamp(stateDir)
	return ok && s == StampAppsPending
}
