package main

import (
	"os"
	"path/filepath"
	"testing"
)

func touch(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestShouldOpenAtLogin(t *testing.T) {
	fresh := filepath.Join(t.TempDir(), "isotope")
	if !ShouldOpenAtLogin(fresh) {
		t.Fatal("fresh state dir must open the flow")
	}
	// Stamps are relative to the parent of the state dir, as the old
	// scripts compute ${state%/*}/hadron/...
	for _, rel := range []string{
		"isotope/welcome",
		"isotope/first-run-apps",
		"isotope/welcomed",
		"hadron/first-run-apps",
		"hadron/welcomed",
	} {
		t.Run(rel, func(t *testing.T) {
			root := t.TempDir()
			touch(t, filepath.Join(root, rel))
			if ShouldOpenAtLogin(filepath.Join(root, "isotope")) {
				t.Fatalf("%s must keep the flow from opening", rel)
			}
		})
	}
}

func TestWriteStampRoundTrip(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "a", "isotope")
	if err := WriteStamp(dir, StampSkipped); err != nil {
		t.Fatal(err)
	}
	s, ok := ReadStamp(dir)
	if s != StampSkipped || !ok {
		t.Fatalf("got %q, %v", s, ok)
	}
	fi, err := os.Stat(filepath.Join(dir, "welcome"))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o644 {
		t.Fatalf("mode %v", fi.Mode().Perm())
	}
	if ShouldOpenAtLogin(dir) {
		t.Fatal("a written stamp must close the gate")
	}
	if _, ok := ReadStamp(t.TempDir()); ok {
		t.Fatal("no stamp must read as false")
	}
}

func TestStateDirHonorsXDG(t *testing.T) {
	got := StateDir("/home/u", func(k string) string {
		if k == "XDG_STATE_HOME" {
			return "/x"
		}
		return ""
	})
	if got != "/x/isotope" {
		t.Fatal(got)
	}
	got = StateDir("/home/u", func(string) string { return "" })
	if got != "/home/u/.local/state/isotope" {
		t.Fatal(got)
	}
}

func TestShouldRemindApps(t *testing.T) {
	dir := t.TempDir()
	if ShouldRemindApps(dir) {
		t.Error("no stamp: nothing to remind")
	}
	for s, want := range map[Stamp]bool{StampDone: false, StampSkipped: false, StampAppsPending: true} {
		if err := WriteStamp(dir, s); err != nil {
			t.Fatal(err)
		}
		if got := ShouldRemindApps(dir); got != want {
			t.Errorf("stamp %s: ShouldRemindApps = %v, want %v", s, got, want)
		}
		if ShouldOpenAtLogin(dir) {
			t.Errorf("stamp %s: the flow must not open at login", s)
		}
	}
}
