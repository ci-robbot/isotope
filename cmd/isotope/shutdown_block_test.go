package main

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"
)

// fwupdBlocker is what fwupd's logind plugin holds while it writes.
var fwupdBlocker = shutdownBlocker{What: "shutdown:sleep:idle:handle-power-key:handle-suspend-key:handle-hibernate-key:handle-lid-switch",
	Who: "fwupd", Why: "Firmware Update in Progress", Mode: "block", PID: 42}

func TestRestartWaitsWhileFwupdWritesFirmware(t *testing.T) {
	daemonEnv(t, "v0.0.5")
	b := &fakeBackend{}
	d := NewDaemon(b)
	list := []shutdownBlocker{fwupdBlocker}
	d.blockers = func() ([]shutdownBlocker, error) { return list, nil }
	if err := d.Reboot(); !errors.Is(err, errFirmwareWriting) {
		t.Fatalf("Reboot: %v, want the firmware refusal", err)
	}
	if err := d.RebootPrevious(); !errors.Is(err, errFirmwareWriting) {
		t.Fatalf("RebootPrevious: %v, want the firmware refusal", err)
	}
	if b.rebootCount() != 0 || b.previousCount() != 0 {
		t.Fatalf("restarted anyway: %d %d", b.rebootCount(), b.previousCount())
	}
	// The D-Bus answer carries the sentence, so the page and the tray show it.
	be := busError(errFirmwareWriting)
	if !strings.HasSuffix(be.Name, ".Refused") || !strings.Contains(be.Body[0].(string), "A firmware update is being written. Restarting now could damage the device.") {
		t.Fatalf("%s %v", be.Name, be.Body)
	}
	// Once fwupd is done, the restart goes ahead.
	list = nil
	if err := d.Reboot(); err != nil || b.rebootCount() != 1 {
		t.Fatalf("Reboot after the write: %v, %d", err, b.rebootCount())
	}
}

func TestRestartBlockers(t *testing.T) {
	for _, c := range []struct {
		name string
		list []shutdownBlocker
		err  error
		want string
	}{
		{"nothing", nil, nil, ""},
		{"fwupd", []shutdownBlocker{fwupdBlocker}, nil, "firmware update is being written"},
		{"a delay lock", []shutdownBlocker{{What: "shutdown", Who: "fwupd", Mode: "delay"}}, nil, ""},
		{"sleep only", []shutdownBlocker{{What: "sleep:idle", Who: "fwupd", Mode: "block"}}, nil, ""},
		{"our own job's lock", []shutdownBlocker{{What: "shutdown:sleep", Who: ownInhibitorWho, Mode: "block"}}, nil, ""},
		{"another program", []shutdownBlocker{{What: "shutdown", Who: "backup-tool", Why: "copying", Mode: "block"}}, nil, "backup-tool is holding off restarts (copying)"},
		{"fwupd first among others", []shutdownBlocker{{What: "shutdown", Who: "x", Mode: "block"}, fwupdBlocker}, nil, "firmware"},
		{"logind unreachable", nil, errors.New("no logind"), ""},
	} {
		d := &Daemon{blockers: func() ([]shutdownBlocker, error) { return c.list, c.err }}
		err := d.restartBlocked()
		switch {
		case c.want == "" && err != nil:
			t.Errorf("%s: %v, want none", c.name, err)
		case c.want != "" && (err == nil || !strings.Contains(err.Error(), c.want)):
			t.Errorf("%s: %v, want %q", c.name, err, c.want)
		}
	}
	if (&Daemon{}).restartBlocked() != nil {
		t.Fatal("no lister blocks nothing")
	}
	if be := busError(blockedError{who: "x"}); !strings.HasSuffix(be.Name, ".Refused") {
		t.Fatalf("%s", be.Name)
	}
}

func TestParseLogindInhibitors(t *testing.T) {
	got := parseBlockers([][]any{
		{"shutdown:sleep", "fwupd", "Firmware Update in Progress", "block", uint32(0), uint32(42)},
		{"short"},
	})
	if len(got) != 1 || got[0].Who != "fwupd" || got[0].Mode != "block" || got[0].PID != 42 {
		t.Fatalf("%+v", got)
	}
}

// logindBlockers reads ListInhibitors from a fake logind on a private bus.
func TestLogindBlockersOnTheBus(t *testing.T) {
	addr := privateBus(t)
	srv := busConn(t, addr)
	if err := srv.Export(fakeLogindInhibitors{}, "/org/freedesktop/login1", "org.freedesktop.login1.Manager"); err != nil {
		t.Fatal(err)
	}
	if r, err := srv.RequestName("org.freedesktop.login1", dbus.NameFlagDoNotQueue); err != nil || r != dbus.RequestNameReplyPrimaryOwner {
		t.Fatalf("%v %v", r, err)
	}
	list, err := logindBlockers(busConn(t, addr))()
	if err != nil || len(list) != 1 || list[0].Who != "fwupd" {
		t.Fatalf("%+v %v", list, err)
	}
}

type logindEntry struct {
	What, Who, Why, Mode string
	UID, PID             uint32
}

type fakeLogindInhibitors struct{}

func (fakeLogindInhibitors) ListInhibitors() ([]logindEntry, *dbus.Error) {
	return []logindEntry{{"shutdown:sleep", "fwupd", "Firmware Update in Progress", "block", 0, 42}}, nil
}

// The tray's Restart while fwupd writes: the daemon refuses, and the tray
// shows the daemon's sentence.
func TestTrayRestartSaysAFirmwareWriteBlocksIt(t *testing.T) {
	f := &trayFake{st: Status{State: StateRebootPending, Installed: "v0.1.0"}}
	f.rebootErr = &daemonError{Name: "Refused", Msg: errFirmwareWriting.Error()}
	h := startTray(t, dialFake(f), time.Hour, time.Hour)
	h.ui.waitView(t, "reboot", func(v trayView) bool { return v.Icon == "reboot" })
	h.app.clicks <- "restart"
	deadline := time.Now().Add(5 * time.Second)
	for len(h.notified()) == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if len(h.notified()) != 1 || !strings.Contains(h.notified()[0], "A firmware update is being written. Restarting now could damage the device.") {
		t.Fatalf("notes %q", h.notified())
	}
}
