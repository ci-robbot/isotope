package main

import (
	"os"
	"os/exec"
	"os/user"
	"strings"

	"github.com/kairos-community/isotope/pkg/i18n"
	"github.com/kairos-community/isotope/pkg/isorelease"
)

// System is what the pages know about the machine without asking a
// service. main reads it once; the tests make their own.
type System struct {
	Release isorelease.Release
	// ReleaseErr is why the release file could not be read ("" when it
	// was): the update pages then say so instead of guessing.
	ReleaseErr string
	// Desktop is DESKTOP_NAME from /etc/isotope/session: Sway, i3, COSMIC,
	// niri.
	Desktop string
	// Admin: the user is in the admin group. The daemon decides again on
	// every call; this only starts the pages read-only for others.
	Admin bool
	// Encrypted: the installer's marker exists, or Trusted Boot.
	Encrypted bool
	Kernel    string
	Hostname  string
	// User is the name of the user who runs the app.
	User string
}

const (
	sessionFile          = "/etc/isotope/session"
	encryptionMarkerFile = "/etc/isotope/encryption"
)

// readSystem reads the release file, the session file, the groups and the
// encryption marker.
func readSystem() System {
	var s System
	if rel, err := isorelease.Load(isorelease.File()); err == nil {
		s.Release = rel
	} else {
		s.ReleaseErr = err.Error()
	}
	if kv, err := isorelease.ReadKV(sessionFile); err == nil {
		s.Desktop = kv["DESKTOP_NAME"]
	}
	s.Admin = inAdminGroup()
	_, err := os.Stat(encryptionMarkerFile)
	s.Encrypted = err == nil || s.Release.TrustedBoot
	if b, err := os.ReadFile("/proc/sys/kernel/osrelease"); err == nil {
		s.Kernel = strings.TrimSpace(string(b))
	}
	s.Hostname, _ = os.Hostname()
	if u, err := user.Current(); err == nil {
		s.User = u.Username
	}
	return s
}

// Note is the sidebar's note: "Isotope v0.1.2 · sway".
func (s System) Note() string {
	n := "Isotope"
	if s.Release.Version != "" {
		n += " " + s.Release.Version
	}
	if s.Release.Flavor != "" {
		n += " · " + s.Release.Flavor
	}
	return n
}

func inAdminGroup() bool {
	u, err := user.Current()
	if err != nil {
		return false
	}
	ids, err := u.GroupIds()
	if err != nil {
		return false
	}
	g, err := user.LookupGroup("admin")
	if err != nil {
		return false
	}
	return hasGroup(ids, g.Gid)
}

func hasGroup(ids []string, gid string) bool {
	for _, id := range ids {
		if id == gid {
			return true
		}
	}
	return false
}

// otherSettings is the desktop's own settings for what this app does not
// cover, and the command that opens its network page; "" when the variant
// has none (sway and i3 use the isotope-*-menu scripts).
func otherSettings(desktop string) (name string, network []string) {
	switch desktop {
	case "COSMIC":
		return i18n.T("COSMIC Settings"), []string{"cosmic-settings", "wireless"}
	case "niri":
		return i18n.T("the control center"), []string{"noctalia", "msg", "panel-open", "control-center", "network"}
	}
	return "", nil
}

// start runs argv and does not wait for it.
func start(argv ...string) error {
	cmd := exec.Command(argv[0], argv[1:]...)
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}
