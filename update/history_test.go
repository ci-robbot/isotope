package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/godbus/dbus/v5"
)

// historyEnv points historyFile at a temp file for one test.
func historyEnv(t *testing.T) string {
	t.Helper()
	old := historyFile
	historyFile = filepath.Join(t.TempDir(), "isotope-update", "history")
	t.Cleanup(func() { historyFile = old })
	return historyFile
}

func TestApplyRecordsTheOutcome(t *testing.T) {
	t.Run("installed", func(t *testing.T) {
		dg := daemonEnv(t, "v0.0.5")
		historyEnv(t)
		d := NewDaemon(&fakeBackend{})
		ref := ghcrRepo + ":sway-v0.0.6"
		if err := d.Apply(context.Background(), ref, dg[ref], false); err != nil {
			t.Fatal(err)
		}
		waitState(t, d, StateRebootPending)
		d.wg.Wait()
		h, err := readHistory()
		if err != nil || len(h) != 1 {
			t.Fatalf("history %+v, %v", h, err)
		}
		e := h[0]
		if e.Kind != "install" || e.Result != "installed" || e.Ref != ref || e.Digest != dg[ref] || e.Version != "v0.0.6" || e.Time == 0 {
			t.Fatalf("entry %+v", e)
		}
	})
	t.Run("failed", func(t *testing.T) {
		dg := daemonEnv(t, "v0.0.5")
		historyEnv(t)
		d := NewDaemon(&fakeBackend{err: errors.New("disk full")})
		ref := ghcrRepo + ":sway-v0.0.6"
		if err := d.Apply(context.Background(), ref, dg[ref], false); err != nil {
			t.Fatal(err)
		}
		waitState(t, d, StateRebootPending)
		d.wg.Wait()
		h, _ := readHistory()
		if len(h) != 1 || h[0].Kind != "install" || h[0].Result != "failed" || h[0].Ref != ref {
			t.Fatalf("history %+v", h)
		}
	})
	t.Run("refused before the write", func(t *testing.T) {
		daemonEnv(t, "v0.0.5")
		historyEnv(t)
		d := NewDaemon(&fakeBackend{})
		if err := d.Apply(context.Background(), ghcrRepo+":sway-v0.0.6", "sha256:"+hex64, false); err != nil {
			t.Fatal(err)
		}
		waitState(t, d, StateError)
		d.wg.Wait()
		h, _ := readHistory()
		if len(h) != 1 || h[0].Result != "failed" {
			t.Fatalf("history %+v", h)
		}
	})
	t.Run("cancelled", func(t *testing.T) {
		dg := daemonEnv(t, "v0.0.5")
		historyEnv(t)
		d := NewDaemon(&fakeBackend{})
		if err := d.Check(context.Background()); err != nil {
			t.Fatal(err)
		}
		noReleaseCache(t)
		arrived := blockingGitHub(t)
		ref := ghcrRepo + ":sway-v0.0.6"
		if err := d.Apply(context.Background(), ref, dg[ref], false); err != nil {
			t.Fatal(err)
		}
		<-arrived
		if err := d.Cancel(); err != nil {
			t.Fatal(err)
		}
		d.wg.Wait()
		h, _ := readHistory()
		if len(h) != 1 || h[0].Result != "cancelled" || h[0].Ref != ref {
			t.Fatalf("history %+v", h)
		}
	})
}

func TestRollbackIsRecorded(t *testing.T) {
	daemonEnv(t, "v0.0.5")
	historyEnv(t)
	b := &fakeBackend{previousErr: errors.New("no entry")}
	d := NewDaemon(b)
	if err := d.RebootPrevious(); err == nil {
		t.Fatal("RebootPrevious succeeded")
	}
	if h, _ := readHistory(); len(h) != 0 {
		t.Fatalf("a failed rollback was recorded: %+v", h)
	}
	b.mu.Lock()
	b.previousErr = nil
	b.mu.Unlock()
	if err := d.RebootPrevious(); err != nil {
		t.Fatal(err)
	}
	h, _ := readHistory()
	if len(h) != 1 || h[0].Kind != "rollback" || h[0].Result != "" || h[0].Time == 0 {
		t.Fatalf("history %+v", h)
	}
}

func TestHistoryKeepsTheNewest200(t *testing.T) {
	path := historyEnv(t)
	for i := 1; i <= 250; i++ {
		if err := appendHistory(historyEntry{Time: int64(i), Kind: "install", Version: fmt.Sprintf("v%d", i), Result: "installed"}); err != nil {
			t.Fatal(err)
		}
	}
	h, err := readHistory()
	if err != nil || len(h) != 200 {
		t.Fatalf("%d entries, %v", len(h), err)
	}
	if h[0].Time != 250 || h[199].Time != 51 {
		t.Fatalf("newest %d, oldest %d", h[0].Time, h[199].Time)
	}
	raw, _ := os.ReadFile(path)
	if n := strings.Count(string(raw), "\n"); n != 200 {
		t.Fatalf("%d lines on disk", n)
	}
	st, _ := os.Stat(path)
	if st.Mode().Perm() != 0o644 {
		t.Fatalf("mode %v", st.Mode())
	}
}

func TestHistorySkipsMalformedLines(t *testing.T) {
	path := historyEnv(t)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	body := "10\tinstall\tv1\tr1\td1\tinstalled\n" +
		"garbage\n" +
		"x\tinstall\tv1\tr1\td1\tinstalled\n" +
		"11\tinstall\tv2\n" +
		"\n" +
		"12\trollback\t\t\t\t\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	h, err := readHistory()
	if err != nil || len(h) != 2 || h[0].Time != 12 || h[1].Time != 10 {
		t.Fatalf("history %+v, %v", h, err)
	}
	// A missing file is an empty history.
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if h, err := readHistory(); err != nil || len(h) != 0 {
		t.Fatalf("missing file: %+v, %v", h, err)
	}
}

func TestHistoryFieldsCannotBreakTheFormat(t *testing.T) {
	historyEnv(t)
	long := strings.Repeat("a", 5000)
	if err := appendHistory(historyEntry{Time: 1, Kind: "install", Version: "v1\n2\t3", Ref: long, Result: "installed"}); err != nil {
		t.Fatal(err)
	}
	h, _ := readHistory()
	if len(h) != 1 || strings.ContainsAny(h[0].Version, "\n\t") || len(h[0].Ref) > 512 {
		t.Fatalf("history %+v", h)
	}
}

func TestBusUpdateHistory(t *testing.T) {
	historyEnv(t)
	daemonEnv(t, "v0.0.5")
	for i := int64(1); i <= 3; i++ {
		if err := appendHistory(historyEntry{Time: i, Kind: "install", Version: "v0.0.6", Ref: "r", Digest: "d", Result: "installed"}); err != nil {
			t.Fatal(err)
		}
	}
	auth := &fakeAuth{allow: false}
	f := startService(t, NewDaemon(&fakeBackend{}), auth)
	var out []map[string]dbus.Variant
	if err := f.call("UpdateHistory").Store(&out); err != nil {
		t.Fatal(err)
	}
	if len(out) != 3 {
		t.Fatalf("%d entries", len(out))
	}
	var first int64
	if err := out[0]["time"].Store(&first); err != nil || first != 3 {
		t.Fatalf("first time %d, %v", first, err)
	}
	for _, k := range []string{"kind", "version", "ref", "digest", "result"} {
		if _, ok := out[0][k]; !ok {
			t.Errorf("key %q missing", k)
		}
	}
	auth.mu.Lock()
	defer auth.mu.Unlock()
	if len(auth.asked) != 0 {
		t.Fatalf("polkit was asked: %v", auth.asked)
	}
}

// TestRollbackIsRecordedBeforeTheRestart: the entry is written once the
// previous system is selected, before systemctl reboot, which may stop the
// daemon. A failed restart keeps it: the selection stays for the next boot.
// A failed selection records nothing.
func TestRollbackIsRecordedBeforeTheRestart(t *testing.T) {
	daemonEnv(t, "v0.0.5")
	hist := historyEnv(t)
	logFile, _ := fakeRebootTools(t, 0)
	bin := strings.Split(os.Getenv("PATH"), string(os.PathListSeparator))[0]
	sc := fmt.Sprintf("#!/bin/sh\necho \"systemctl $*\" >> %s\ncat %s >> %s 2>/dev/null\nexit 1\n", logFile, hist, logFile)
	if err := os.WriteFile(filepath.Join(bin, "systemctl"), []byte(sc), 0o755); err != nil {
		t.Fatal(err)
	}
	d := NewDaemon(kairosBackend{})
	if err := d.RebootPrevious(); err == nil {
		t.Fatal("a failed restart returned nil")
	}
	// systemctl saw the entry in the file.
	if got := readLog(t, logFile); !strings.Contains(got, "systemctl reboot\n") || !strings.Contains(got, "\trollback\t") {
		t.Fatalf("log %q", got)
	}
	if h, _ := readHistory(); len(h) != 1 || h[0].Kind != "rollback" {
		t.Fatalf("history %+v", h)
	}

	historyEnv(t)
	fakeRebootTools(t, 1)
	if err := NewDaemon(kairosBackend{}).RebootPrevious(); err == nil {
		t.Fatal("a failed selection returned nil")
	}
	if h, _ := readHistory(); len(h) != 0 {
		t.Fatalf("a failed selection was recorded: %+v", h)
	}
}
