package main

import (
	"bufio"
	"context"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/kairos-community/isotope/pkg/fwupd"
)

// The tray says when a device has a firmware update: fwupd (pkg/fwupd, as
// the user; reading needs no polkit) a few minutes after login and then
// every few hours, one notification per device and version, with "Open
// Isotope System" (its Updates page installs it). The record of what was
// said is $XDG_STATE_HOME/isotope-update/firmware-notified, one
// "<device id> <version>" line each. A failure is logged, never a
// notification. See docs/superpowers/specs/2026-11-24-firmware-updates-design.md.

// The first look waits for the session and the network; fwupd-refresh.timer
// refreshes the metadata once a day.
var (
	firmwareFirstCheck = 5 * time.Minute
	firmwareInterval   = 6 * time.Hour
)

// firmwareWatch is the watcher's seams.
type firmwareWatch struct {
	updates func() ([]fwupd.Update, error)
	notify  func(summary, body, label string, invoked func()) error
	open    func(page string) error
	record  string
	log     *log.Logger
}

// firmwareRecord is the file of the notifications already sent.
func firmwareRecord() string { return filepath.Join(stateDir(), "firmware-notified") }

// watchFirmware runs the watcher for the session; it never returns.
func watchFirmware(logger *log.Logger, open func(page string) error) {
	w := firmwareWatch{
		updates: func() ([]fwupd.Update, error) {
			c, conn, err := fwupd.Dial()
			if err != nil {
				return nil, err
			}
			defer conn.Close()
			return c.Updates()
		},
		notify: NotifyAction,
		open:   open,
		record: firmwareRecord(),
		log:    logger,
	}
	w.run(context.Background(), firmwareFirstCheck, firmwareInterval)
}

func (w firmwareWatch) run(ctx context.Context, first, every time.Duration) {
	t := time.NewTimer(first)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			w.once()
			t.Reset(every)
		}
	}
}

// once looks for updates and announces the new ones.
func (w firmwareWatch) once() {
	ups, err := w.updates()
	if err != nil {
		if !fwupd.Missing(err) {
			w.log.Printf("firmware updates: %v", err)
		}
		return
	}
	seen := readFirmwareRecord(w.record)
	for _, u := range ups {
		key := u.Device.ID + " " + u.Release.Version
		if seen[key] {
			continue
		}
		name := u.Device.Name
		if name == "" {
			name = "A device"
		}
		body := name + " can be updated to version " + u.Release.Version + ". Isotope System's Updates page installs it."
		err := w.notify("Firmware update available", body, "Open Isotope System", func() {
			if err := w.open("updates"); err != nil {
				w.log.Printf("opening Isotope System: %v", err)
			}
		})
		if err != nil {
			w.log.Printf("showing a notification: %v", err)
			return
		}
		seen[key] = true
		if err := appendFirmwareRecord(w.record, key); err != nil {
			w.log.Printf("recording that %s was announced: %v", key, err)
		}
	}
}

func readFirmwareRecord(path string) map[string]bool {
	seen := map[string]bool{}
	f, err := os.Open(path)
	if err != nil {
		return seen
	}
	defer f.Close()
	s := bufio.NewScanner(f)
	for s.Scan() {
		if l := strings.TrimSpace(s.Text()); l != "" {
			seen[l] = true
		}
	}
	return seen
}

func appendFirmwareRecord(path, key string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.WriteString(key + "\n"); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}
