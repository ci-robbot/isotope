package main

// Backups over D-Bus, for Isotope System' Backup page: the status (no
// polkit: the config holds no secret, and `isotope status` reads it as any
// user), back up now and the schedule (polkit's backup action, admin only).
// They run the code `isotope backup` runs as root. Setting up a target and
// restoring stay with the CLI for now; see
// docs/superpowers/specs/2026-11-21-isotope-system-app-design.md.

import (
	"bytes"
	"errors"
	"strings"
	"sync"

	"github.com/godbus/dbus/v5"
)

// The backup job's outcome, Status.BackupResult. The values are part of the
// D-Bus interface.
const (
	backupResultOK      = "ok"
	backupResultPartial = "partial"
	backupResultFailed  = "failed"
)

var errBackupUnavailable = errors.New("backup is not available on this edition (no restic)")

// daemonBackup is the daemon's backup: `isotope backup` as root, with every
// line restic and the backup print sent to progress.
var daemonBackup = func(progress func(string)) (string, error) {
	if !exists(restic()) {
		return "", errBackupUnavailable
	}
	w := &lineWriter{line: progress}
	defer w.Flush()
	if _, err := runConfiguredBackup(defaultBackupPaths(), false, false, w, w); err != nil {
		return "", err
	}
	return "Backed up.", nil
}

// lineWriter calls line for every line written to it, CR or LF ended, so
// restic's progress reaches the clients as it comes. Safe for the two
// writers (stdout, stderr) of one command.
type lineWriter struct {
	mu   sync.Mutex
	buf  []byte
	line func(string)
}

func (w *lineWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.buf = append(w.buf, p...)
	for {
		i := bytes.IndexAny(w.buf, "\r\n")
		if i < 0 {
			return len(p), nil
		}
		if s := strings.TrimSpace(string(w.buf[:i])); s != "" {
			w.line(s)
		}
		w.buf = w.buf[i+1:]
	}
}

// Flush sends what is left without a line end.
func (w *lineWriter) Flush() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if s := strings.TrimSpace(string(w.buf)); s != "" {
		w.line(s)
	}
	w.buf = nil
}

// BackupNow starts a backup to the configured target and returns at once,
// like an extension job: it takes the one job slot, so it refuses while an
// update or an extension job runs and they refuse while it runs. The
// outcome is Status.BackupResult and BackupDetail when the state leaves
// backup. Unlike an install it does not need a managed system.
func (d *Daemon) BackupNow() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	switch {
	case d.backup == nil:
		return errBackupUnavailable
	case d.rebooting:
		return ErrRebooting
	case d.busyLocked() || d.job != nil:
		return ErrBusy
	}
	j := &job{cancel: func() {}, prev: d.state, prevDetail: d.detail}
	d.job = j
	d.backupResult, d.backupDetail = "", ""
	d.setLocked(StateBackup, "Backing up this machine")
	d.wg.Add(1)
	go func() {
		defer d.wg.Done()
		msg, err := d.backup(d.progress)
		d.mu.Lock()
		defer d.mu.Unlock()
		d.job = nil
		switch {
		case errors.Is(err, errPartial):
			d.backupResult, d.backupDetail = backupResultPartial, "Backed up, but "+err.Error()+"."
		case err != nil:
			d.backupResult, d.backupDetail = backupResultFailed, err.Error()
		default:
			d.backupResult, d.backupDetail = backupResultOK, msg
		}
		prev, detail := j.prev, j.prevDetail
		if prev == StateError {
			prev, detail = StateIdle, ""
		}
		d.setLocked(prev, detail)
	}()
	return nil
}

// backupStatusDict is BackupStatus's answer: what `isotope backup status`
// prints, as values.
func backupStatusDict(p backupPaths) (map[string]dbus.Variant, error) {
	c, err := loadBackupConfig(p)
	if err != nil {
		return nil, err
	}
	restore := strings.TrimSpace(readFileString(p.finishResult()))
	if restore == "ok" {
		restore = ""
	}
	return map[string]dbus.Variant{
		"available":  dbus.MakeVariant(exists(restic())),
		"target":     dbus.MakeVariant(c.Target),
		"schedule":   dbus.MakeVariant(c.Schedule),
		"containers": dbus.MakeVariant(c.Containers),
		"last":       dbus.MakeVariant(c.Last),
		"result":     dbus.MakeVariant(c.Result),
		"warning":    dbus.MakeVariant(backupWarning(c)),
		"present":    dbus.MakeVariant(c.Target != "" && targetPresent(c.Target)),
		"restore":    dbus.MakeVariant(restore),
	}, nil
}

func (a busAPI) BackupStatus() (map[string]dbus.Variant, *dbus.Error) {
	done, derr := a.s.enter()
	if derr != nil {
		return nil, derr
	}
	defer done()
	m, err := backupStatusDict(defaultBackupPaths())
	if err != nil {
		return nil, busError(err)
	}
	return m, nil
}

func (a busAPI) BackupNow(sender dbus.Sender) *dbus.Error {
	done, derr := a.s.enter()
	if derr != nil {
		return derr
	}
	defer done()
	if err := a.s.authorize(sender, actionBackup); err != nil {
		return err
	}
	return busError(a.s.d.BackupNow())
}

func (a busAPI) SetBackupSchedule(sender dbus.Sender, schedule string) *dbus.Error {
	done, derr := a.s.enter()
	if derr != nil {
		return derr
	}
	defer done()
	if err := a.s.authorize(sender, actionBackup); err != nil {
		return err
	}
	// The job writes the config when it ends; a schedule change must not
	// be lost to that write.
	if a.s.d.Status().State == StateBackup {
		return busError(ErrBusy)
	}
	_, err := setBackupSchedule(defaultBackupPaths(), schedule)
	return busError(err)
}
