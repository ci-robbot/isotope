package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestSignalStopsTheExportAndItCleansUp: a signal reaches the daemon
// while an export packs its file. The job is cancelled, and it removes its
// work folder, the .partial file and the password in /run before serveCtx
// returns.
func TestSignalStopsTheExportAndItCleansUp(t *testing.T) {
	p := backupSetup(t)
	fakeRestic(t)
	writeSecret(p.password(), "machine-pw")
	reached, release := make(chan struct{}), make(chan struct{})
	old := fchownFile
	fchownFile = func(*os.File, int, int) error {
		close(reached)
		<-release
		return nil
	}
	t.Cleanup(func() { fchownFile = old })
	d := backupDaemon(t, nil)
	file := filepath.Join(t.TempDir(), "machine.isotope-backup")
	if err := d.BackupExport(file, os.Getuid(), 0); err != nil {
		t.Fatal(err)
	}
	<-reached
	if w, _ := filepath.Glob(filepath.Join(p.State, workPattern)); len(w) != 1 || !exists(file+".partial") {
		t.Fatalf("work %q, partial %v: the export is not where the test wants it", w, exists(file+".partial"))
	}

	stopped := make(chan bool)
	go func() { stopped <- d.Stop(5 * time.Second) }()
	// The job's context is cancelled before the pack ends.
	time.Sleep(50 * time.Millisecond)
	close(release)
	if !<-stopped {
		t.Fatal("the job did not end")
	}
	if w, _ := filepath.Glob(filepath.Join(p.State, workPattern)); len(w) != 0 {
		t.Fatalf("work folders left: %q", w)
	}
	if exists(file) || exists(file+".partial") {
		t.Fatal("the file or its .partial is left")
	}
	if e, _ := os.ReadDir(p.Run); len(e) != 0 {
		t.Fatalf("files left in %s: %v", p.Run, e)
	}
	if st := d.Status(); st.BackupDetail != "Cancelled" {
		t.Fatalf("status %+v", st)
	}
}

// TestNoJobStartsAfterStop: a call that was already in progress when the
// signal came cannot start a job; the client is told the service exits.
func TestNoJobStartsAfterStop(t *testing.T) {
	p := backupSetup(t)
	fakeRestic(t)
	writeSecret(p.password(), "machine-pw")
	d := backupDaemon(t, nil)
	if !d.Stop(time.Second) {
		t.Fatal("Stop with no job did not return at once")
	}
	file := filepath.Join(t.TempDir(), "machine.isotope-backup")
	err := d.BackupExport(file, os.Getuid(), 0)
	if !errors.Is(err, ErrStopping) {
		t.Fatalf("export after Stop: %v", err)
	}
	if e := busError(err); e == nil || e.Name != busInterface+".Error.Exiting" {
		t.Fatalf("bus error %v", e)
	}
	if exists(file) || exists(file+".partial") || d.Busy() {
		t.Fatal("a job started after Stop")
	}
}

// TestServeStopsOnItsContext: serveCtx returns when its context is done (a
// signal), after the job has ended.
func TestServeStopsOnItsContext(t *testing.T) {
	ended := make(chan struct{})
	d := backupDaemon(t, func(ctx context.Context, _ func(string)) (string, error) {
		<-ctx.Done()
		close(ended)
		return "", ctx.Err()
	})
	addr := privateBus(t)
	conn := busConn(t, addr)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- serveCtx(ctx, conn, d, &fakeAuth{allow: true}) }()
	client := busConn(t, addr)
	deadline := time.Now().Add(5 * time.Second)
	for {
		var has bool
		if client.BusObject().Call("org.freedesktop.DBus.NameHasOwner", 0, busName).Store(&has) == nil && has {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the service never owned its name")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if err := d.BackupNow(); err != nil {
		t.Fatal(err)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("serveCtx did not return")
	}
	select {
	case <-ended:
	default:
		t.Fatal("serveCtx returned before the job ended")
	}
}

// TestDaemonContextEndsOnASignal: daemonContext catches the signals that
// stop the daemon, instead of Go's default exit.
func TestDaemonContextEndsOnASignal(t *testing.T) {
	ctx, stop := daemonContext()
	defer stop()
	if err := syscall.Kill(os.Getpid(), syscall.SIGHUP); err != nil {
		t.Fatal(err)
	}
	select {
	case <-ctx.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("SIGHUP did not end the context")
	}
}

// TestSweepRemovesOldWorkFolders: at start the daemon removes the work
// folders and the restore stage that a killed job left, but not one a job
// holds or one just made.
func TestSweepRemovesOldWorkFolders(t *testing.T) {
	p := backupSetup(t)
	old := time.Now().Add(-time.Hour)
	mk := func(dir string) string {
		if err := os.MkdirAll(filepath.Join(dir, "data"), 0o700); err != nil {
			t.Fatal(err)
		}
		write(t, filepath.Join(dir, "data", "pack"), "x")
		os.Chtimes(dir, old, old)
		return dir
	}
	stale := mk(filepath.Join(p.State, "work-111"))
	stage := mk(p.stage())
	held := mk(filepath.Join(p.State, "work-222"))
	unlock, err := lockDir(held)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	fresh := filepath.Join(p.State, "work-333")
	os.MkdirAll(fresh, 0o700)
	keep := mk(filepath.Join(p.State, "ssh"))

	var log bytes.Buffer
	sweepBackupWork(p, &log)
	for _, d := range []string{stale, stage} {
		if exists(d) {
			t.Errorf("%s is left", d)
		}
	}
	for _, d := range []string{held, fresh, keep} {
		if !exists(d) {
			t.Errorf("%s was removed", d)
		}
	}
	if !strings.Contains(log.String(), "work-111") {
		t.Fatalf("log %q", log.String())
	}
}

// TestWorkFolderIsLockedWhileInUse: makeWorkDir's folder cannot be swept
// until done.
func TestWorkFolderIsLockedWhileInUse(t *testing.T) {
	p := backupSetup(t)
	dir, done, err := makeWorkDir(p)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lockDir(dir); err == nil {
		t.Fatal("a work folder in use could be locked")
	}
	done()
	done()
	if exists(dir) {
		t.Fatal("done left the folder")
	}
}
