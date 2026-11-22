package main

import (
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"
)

// backupDaemon is a daemon whose backup is fn, for the job tests.
func backupDaemon(t *testing.T, fn func(progress func(string)) (string, error)) *Daemon {
	t.Helper()
	daemonEnv(t, "v0.0.5")
	d := NewDaemon(&fakeBackend{})
	d.backup = fn
	return d
}

func TestBackupNowRunsAJobAndReportsTheOutcome(t *testing.T) {
	release := make(chan struct{})
	d := backupDaemon(t, func(progress func(string)) (string, error) {
		progress("Files: 10 new")
		<-release
		return "Backed up.", nil
	})
	var mu sync.Mutex
	var lines []string
	d.OnProgress(func(l string) { mu.Lock(); lines = append(lines, l); mu.Unlock() })

	if err := d.BackupNow(); err != nil {
		t.Fatal(err)
	}
	st := waitState(t, d, StateBackup)
	if st.Detail == "" {
		t.Fatal("the backup state says nothing")
	}
	// One job at a time: an extension or another backup waits.
	if err := d.BackupNow(); !errors.Is(err, ErrBusy) {
		t.Fatalf("second BackupNow: %v, want ErrBusy", err)
	}
	if err := d.Reboot(); !errors.Is(err, ErrBusy) {
		t.Fatalf("Reboot during a backup: %v, want ErrBusy", err)
	}
	if err := d.Cancel(); !errors.Is(err, ErrNotCancellable) {
		t.Fatalf("Cancel during a backup: %v, want ErrNotCancellable", err)
	}
	close(release)
	d.wg.Wait()
	st = d.Status()
	if st.State != StateIdle || st.BackupResult != backupResultOK || st.BackupDetail != "Backed up." {
		t.Fatalf("after the job: %+v", st)
	}
	// Callbacks are delivered by another goroutine.
	deadline := time.Now().Add(5 * time.Second)
	for {
		mu.Lock()
		got := append([]string(nil), lines...)
		mu.Unlock()
		if reflect.DeepEqual(got, []string{"Files: 10 new"}) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("progress %q", got)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func TestBackupNowSaysPartialAndFailed(t *testing.T) {
	for _, c := range []struct {
		err          error
		result, want string
	}{
		{errPartial, backupResultPartial, "Backed up, but " + errPartial.Error() + "."},
		{errBackupRefused{"there is no backup target"}, backupResultFailed, "there is no backup target"},
	} {
		d := backupDaemon(t, func(func(string)) (string, error) { return "", c.err })
		if err := d.BackupNow(); err != nil {
			t.Fatal(err)
		}
		d.wg.Wait()
		if st := d.Status(); st.BackupResult != c.result || st.BackupDetail != c.want {
			t.Fatalf("%v: result %q detail %q", c.err, st.BackupResult, st.BackupDetail)
		}
	}
}

func TestBackupNowWithoutABackupIsRefused(t *testing.T) {
	d := backupDaemon(t, nil)
	if err := d.BackupNow(); !errors.Is(err, errBackupUnavailable) {
		t.Fatalf("%v", err)
	}
}

func TestLineWriterSplitsOnCRAndLF(t *testing.T) {
	var got []string
	w := &lineWriter{line: func(s string) { got = append(got, s) }}
	w.Write([]byte("one\r[0:01] 10%"))
	w.Write([]byte("\rtwo\n\nthree"))
	w.Flush()
	if want := []string{"one", "[0:01] 10%", "two", "three"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("%q, want %q", got, want)
	}
}

// TestBusBackupAsksTheBackupAction: Back up now and the schedule ask polkit
// for the backup action; the status asks nothing.
func TestBusBackupAsksTheBackupAction(t *testing.T) {
	p := backupSetup(t)
	ran := 0
	d := backupDaemon(t, func(func(string)) (string, error) { ran++; return "ok", nil })
	auth := &fakeAuth{allow: false}
	f := startService(t, d, auth)

	for _, c := range []*dbus.Call{f.call("BackupNow"), f.call("SetBackupSchedule", "daily")} {
		if got := errName(c.Err); got != busInterface+".Error.NotAuthorized" {
			t.Fatalf("%s (%v), want NotAuthorized", got, c.Err)
		}
	}
	me := f.client.Names()[0]
	for _, q := range auth.questions() {
		if q != me+" "+actionBackup {
			t.Fatalf("asked %q, want the backup action", q)
		}
	}
	if ran != 0 {
		t.Fatal("the backup ran without authorization")
	}

	if err := saveBackupConfig(p, backupConfig{Target: "/media/usb/backup", Schedule: "weekly", Last: 1700000000, Result: "ok"}); err != nil {
		t.Fatal(err)
	}
	var m map[string]dbus.Variant
	if err := f.call("BackupStatus").Store(&m); err != nil {
		t.Fatalf("BackupStatus: %v", err)
	}
	if m["target"].Value() != "/media/usb/backup" || m["schedule"].Value() != "weekly" ||
		m["last"].Value() != int64(1700000000) || m["present"].Value() != false {
		t.Fatalf("BackupStatus %v", m)
	}
	if n := len(auth.questions()); n != 2 {
		t.Fatalf("asked polkit %d times, want 2 (BackupStatus asks nothing)", n)
	}
}

func TestBusSetBackupScheduleWritesTheConfig(t *testing.T) {
	p := backupSetup(t)
	d := backupDaemon(t, nil)
	f := startService(t, d, &fakeAuth{allow: true})
	// Without a target only "off" is accepted, as in the CLI.
	if c := f.call("SetBackupSchedule", "daily"); c.Err == nil {
		t.Fatal("a schedule without a target was accepted")
	}
	if err := saveBackupConfig(p, backupConfig{Target: "/media/usb/backup", Schedule: "off"}); err != nil {
		t.Fatal(err)
	}
	if c := f.call("SetBackupSchedule", "hourly"); c.Err == nil {
		t.Fatal("hourly was accepted")
	}
	if c := f.call("SetBackupSchedule", "daily"); c.Err != nil {
		t.Fatal(c.Err)
	}
	c, err := loadBackupConfig(p)
	if err != nil || c.Schedule != "daily" {
		t.Fatalf("schedule %q, %v", c.Schedule, err)
	}
}
