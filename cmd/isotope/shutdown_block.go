package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/godbus/dbus/v5"
)

// Reboot and RebootPrevious first ask logind whether something holds a
// "block" inhibitor on shutdown. fwupd takes one while it writes firmware
// (its logind plugin: who "fwupd", why "Firmware Update in Progress"), and
// a restart then could leave the device without working firmware.
// isotope-updated restarts as root with `systemctl reboot`, which skips the
// inhibitor check (root holds reboot-ignore-inhibit), so the check is here.
//
// logind, not fwupd's Status property: reading fwupd's Status would start
// fwupd (it is D-Bus activated and enumerates the hardware when it starts)
// on every restart, a stopped fwupd has no answer anyway, and logind also
// sees a write by fwupdtool, which is not the daemon, and any other
// program that asked to hold restarts off.

// shutdownBlocker is one entry of logind's ListInhibitors.
type shutdownBlocker struct {
	What, Who, Why, Mode string
	UID, PID             uint32
}

// ownInhibitorWho is the name isotope-updated's own inhibitor carries
// (logindInhibitor); its own jobs are refused by state instead.
const ownInhibitorWho = "Isotope update"

// errFirmwareWriting is the refusal while fwupd writes firmware.
var errFirmwareWriting = errors.New("A firmware update is being written. Restarting now could damage the device. Try again when it is done.")

// blockedError is a refusal because of another program's inhibitor.
type blockedError struct{ who, why string }

func (e blockedError) Error() string {
	msg := e.who + " is holding off restarts"
	if e.why != "" {
		msg += " (" + e.why + ")"
	}
	return msg + ". Try again when it is done."
}

// restartBlocked says why a restart must wait, nil when nothing blocks it.
// An unreadable list does not block: logind may be gone, and the restart
// is the user's own choice.
func (d *Daemon) restartBlocked() error {
	if d.blockers == nil {
		return nil
	}
	list, err := d.blockers()
	if err != nil {
		return nil
	}
	var other error
	for _, b := range list {
		if b.Mode != "block" || b.Who == ownInhibitorWho || !covers(b.What, "shutdown") {
			continue
		}
		if b.Who == "fwupd" {
			return errFirmwareWriting
		}
		if other == nil {
			other = blockedError{who: b.Who, why: b.Why}
		}
	}
	return other
}

func covers(what, kind string) bool {
	for _, w := range strings.Split(what, ":") {
		if w == kind {
			return true
		}
	}
	return false
}

// logindBlockers is Daemon.blockers over logind on the system bus.
func logindBlockers(conn *dbus.Conn) func() ([]shutdownBlocker, error) {
	return func() ([]shutdownBlocker, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		var raw [][]any
		err := conn.Object("org.freedesktop.login1", "/org/freedesktop/login1").
			CallWithContext(ctx, "org.freedesktop.login1.Manager.ListInhibitors", 0).Store(&raw)
		if err != nil {
			return nil, fmt.Errorf("logind: %w", err)
		}
		return parseBlockers(raw), nil
	}
}

// parseBlockers reads ListInhibitors' a(ssssuu): what, who, why, mode, uid,
// pid.
func parseBlockers(raw [][]any) []shutdownBlocker {
	var out []shutdownBlocker
	for _, r := range raw {
		if len(r) != 6 {
			continue
		}
		var b shutdownBlocker
		b.What, _ = r[0].(string)
		b.Who, _ = r[1].(string)
		b.Why, _ = r[2].(string)
		b.Mode, _ = r[3].(string)
		b.UID, _ = r[4].(uint32)
		b.PID, _ = r[5].(uint32)
		out = append(out, b)
	}
	return out
}
