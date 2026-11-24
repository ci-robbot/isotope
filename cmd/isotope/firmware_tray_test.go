package main

import (
	"bytes"
	"errors"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/godbus/dbus/v5"

	"github.com/kairos-community/isotope/pkg/fwupd"
)

type firmwareNote struct{ summary, body, label string }

func firmwareWatchFor(t *testing.T, ups []fwupd.Update, err error) (*firmwareWatch, *[]firmwareNote, *[]string, *bytes.Buffer) {
	t.Helper()
	var notes []firmwareNote
	var opened []string
	var logs bytes.Buffer
	w := &firmwareWatch{
		updates: func() ([]fwupd.Update, error) { return ups, err },
		notify: func(s, b, l string, invoked func()) error {
			notes = append(notes, firmwareNote{s, b, l})
			invoked()
			return nil
		},
		open:   func(page string) error { opened = append(opened, page); return nil },
		record: filepath.Join(t.TempDir(), "state", "firmware-notified"),
		log:    log.New(&logs, "", 0),
	}
	return w, &notes, &opened, &logs
}

func TestFirmwareTrayNotifiesOncePerDeviceAndVersion(t *testing.T) {
	ups := []fwupd.Update{
		{Device: fwupd.Device{ID: "ssd", Name: "Samsung SSD"}, Release: fwupd.Release{Version: "2.0"}},
		{Device: fwupd.Device{ID: "bios", Name: "System Firmware"}, Release: fwupd.Release{Version: "1.45"}},
	}
	w, notes, opened, _ := firmwareWatchFor(t, ups, nil)
	w.once()
	if len(*notes) != 2 {
		t.Fatalf("notes %+v", *notes)
	}
	n := (*notes)[0]
	if n.summary != "Firmware update available" || !strings.Contains(n.body, "Samsung SSD can be updated to version 2.0") || n.label != "Open Isotope System" {
		t.Fatalf("%+v", n)
	}
	if len(*opened) != 2 || (*opened)[0] != "updates" {
		t.Fatalf("the action opened %v", *opened)
	}
	w.once()
	if len(*notes) != 2 {
		t.Fatalf("notified again: %+v", *notes)
	}
	// A newer version of the same device is new.
	w2, notes2, _, _ := firmwareWatchFor(t, []fwupd.Update{{Device: ups[0].Device, Release: fwupd.Release{Version: "2.1"}}}, nil)
	w2.record = w.record
	w2.once()
	if len(*notes2) != 1 {
		t.Fatalf("a new version was not announced: %+v", *notes2)
	}
	b, _ := os.ReadFile(w.record)
	if string(b) != "ssd 2.0\nbios 1.45\nssd 2.1\n" {
		t.Fatalf("record %q", b)
	}
}

func TestFirmwareTrayFailuresAreLoggedNotNotified(t *testing.T) {
	w, notes, _, logs := firmwareWatchFor(t, nil, errors.New("bus down"))
	w.once()
	if len(*notes) != 0 || !strings.Contains(logs.String(), "bus down") {
		t.Fatalf("notes %v logs %q", *notes, logs.String())
	}
	// No fwupd at all (an image without it) is quiet.
	w, notes, _, logs = firmwareWatchFor(t, nil, dbus.Error{Name: "org.freedesktop.DBus.Error.ServiceUnknown"})
	w.once()
	if len(*notes) != 0 || logs.Len() != 0 {
		t.Fatalf("notes %v logs %q", *notes, logs.String())
	}
	// A notification server that fails leaves nothing recorded.
	w, _, _, _ = firmwareWatchFor(t, []fwupd.Update{{Device: fwupd.Device{ID: "x"}, Release: fwupd.Release{Version: "1"}}}, nil)
	w.notify = func(string, string, string, func()) error { return errors.New("no server") }
	w.once()
	if _, err := os.Stat(w.record); err == nil {
		t.Fatal("recorded a notification that was not shown")
	}
}
