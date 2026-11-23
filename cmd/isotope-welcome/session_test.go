package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReadDesktop(t *testing.T) {
	for in, want := range map[string]Desktop{
		"COSMIC": Cosmic, "sway": Sway, "i3": I3, "niri": Niri,
	} {
		f := filepath.Join(t.TempDir(), "session")
		if err := os.WriteFile(f, []byte("# x\nOTHER=1\nDESKTOP_NAME="+in+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		got, err := ReadDesktop(f)
		if err != nil || got != want {
			t.Errorf("%s: got %q, %v", in, got, err)
		}
	}
	f := filepath.Join(t.TempDir(), "session")
	if err := os.WriteFile(f, []byte("OTHER=1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadDesktop(f); err == nil {
		t.Error("missing key must error")
	}
	if _, err := ReadDesktop(filepath.Join(t.TempDir(), "none")); err == nil {
		t.Error("missing file must error")
	}
}
