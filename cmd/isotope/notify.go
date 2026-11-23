package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/godbus/dbus/v5"
)

// busctl is the D-Bus client. It ships in the Hadron base, and as a
// `--user` client it honours DBUS_SESSION_BUS_ADDRESS, which is how it reaches
// the private bus start-desktop opens with dbus-run-session (mako and dunst
// both own org.freedesktop.Notifications there). ISOTOPE_UPDATE_BUSCTL replaces
// it, so a test can record the call instead of needing a bus.
func busctl() string {
	if p := os.Getenv("ISOTOPE_UPDATE_BUSCTL"); p != "" {
		return p
	}
	return "/usr/bin/busctl"
}

// notifyArgs is the org.freedesktop.Notifications.Notify call: app name,
// replaces_id, icon, summary, body, no actions, no hints, default timeout.
// The `--` ends busctl's own option parsing: the timeout argument is -1, and
// without it busctl rejects that as an unknown option and nothing is shown.
func notifyArgs(summary, body string) []string {
	return []string{
		"--user", "--", "call",
		"org.freedesktop.Notifications", "/org/freedesktop/Notifications",
		"org.freedesktop.Notifications", "Notify", "susssasa{sv}i",
		"Isotope", "0", "system-software-update", summary, body,
		"0", "0", "-1",
	}
}

// Notify shows a desktop notification on the session bus.
func Notify(summary, body string) error {
	out, err := exec.Command(busctl(), notifyArgs(summary, body)...).CombinedOutput()
	if err != nil {
		return errors.New(strings.TrimSpace(string(out)) + ": " + err.Error())
	}
	return nil
}

// stateDir holds the last version the user was told about, so a session that
// restarts every few hours does not repeat the same notification.
func stateDir() string {
	if d := os.Getenv("XDG_STATE_HOME"); d != "" {
		return filepath.Join(d, "isotope-update")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "state", "isotope-update")
}

func alreadyNotified(tag string) bool {
	b, err := os.ReadFile(filepath.Join(stateDir(), "notified"))
	return err == nil && strings.TrimSpace(string(b)) == tag
}

func markNotified(tag string) error {
	if err := os.MkdirAll(stateDir(), 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(stateDir(), "notified"), []byte(tag+"\n"), 0o644)
}

// notifyActionWait is how long a notification's action stays answerable.
var notifyActionWait = 24 * time.Hour

// NotifyAction shows a desktop notification with one action button
// (label), and calls invoked when the user picks it. It needs the session
// bus for the answer, which busctl cannot wait for. Only the notification
// server's own signals count: the match names its unique name.
func NotifyAction(summary, body, label string, invoked func()) error {
	conn, err := dbus.ConnectSessionBus()
	if err != nil {
		return err
	}
	var owner string
	if err := conn.BusObject().Call("org.freedesktop.DBus.GetNameOwner", 0, "org.freedesktop.Notifications").Store(&owner); err != nil {
		conn.Close()
		return err
	}
	opts := []dbus.MatchOption{dbus.WithMatchSender(owner), dbus.WithMatchInterface("org.freedesktop.Notifications")}
	if err := conn.AddMatchSignal(opts...); err != nil {
		conn.Close()
		return err
	}
	ch := make(chan *dbus.Signal, 8)
	conn.Signal(ch)
	var id uint32
	err = conn.Object("org.freedesktop.Notifications", "/org/freedesktop/Notifications").Call(
		"org.freedesktop.Notifications.Notify", 0,
		"Isotope", uint32(0), "system-software-update", summary, body,
		// No "default" action: a click on the notification, often how it
		// is dismissed, must not restart the machine.
		[]string{"restart", label}, map[string]dbus.Variant{}, int32(-1)).Store(&id)
	if err != nil {
		conn.Close()
		return err
	}
	go func() {
		defer conn.Close()
		t := time.NewTimer(notifyActionWait)
		defer t.Stop()
		for {
			select {
			case <-t.C:
				return
			case s, ok := <-ch:
				if !ok {
					return
				}
				if done, pick := notificationAnswer(s, owner, id); done {
					if pick {
						invoked()
					}
					return
				}
			}
		}
	}()
	return nil
}

// notificationAnswer reads one signal for notification id: done when the
// notification is answered or closed, pick when its action was chosen.
func notificationAnswer(s *dbus.Signal, owner string, id uint32) (done, pick bool) {
	if s.Sender != owner || len(s.Body) == 0 {
		return false, false
	}
	if got, ok := s.Body[0].(uint32); !ok || got != id {
		return false, false
	}
	switch s.Name {
	case "org.freedesktop.Notifications.ActionInvoked":
		if len(s.Body) < 2 {
			return false, false
		}
		key, _ := s.Body[1].(string)
		return true, key == "restart"
	case "org.freedesktop.Notifications.NotificationClosed":
		return true, false
	}
	return false, false
}

// stagedNotified says whether the user was told that version waits for a
// restart, so each staged version notifies once.
func stagedNotified(version string) bool {
	b, err := os.ReadFile(filepath.Join(stateDir(), "staged-notified"))
	return err == nil && strings.TrimSpace(string(b)) == version
}

func markStagedNotified(version string) error {
	if err := os.MkdirAll(stateDir(), 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(stateDir(), "staged-notified"), []byte(version+"\n"), 0o644)
}
