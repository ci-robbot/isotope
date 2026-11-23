package main

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
)

// fakeTools answers pw-dump from testdata and records every call.
type fakeTools struct {
	mu    sync.Mutex
	calls []string
}

func (f *fakeTools) run(name string, args ...string) (string, error) {
	f.mu.Lock()
	f.calls = append(f.calls, strings.TrimSpace(name+" "+strings.Join(args, " ")))
	f.mu.Unlock()
	switch {
	case name == "pw-dump" && len(args) == 0:
		b, err := os.ReadFile(filepath.Join("testdata", "pw-dump.json"))
		return string(b), err
	case name == "wpctl" && len(args) == 3 && args[0] == "set-profile":
		return "", nil
	}
	return "", errors.New("unexpected " + name + " " + strings.Join(args, " "))
}

func (f *fakeTools) called() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

func withTools(t *testing.T) *fakeTools {
	t.Helper()
	f := &fakeTools{}
	old := setTool(f.run)
	t.Cleanup(func() { setTool(old) })
	return f
}

func TestProfilesParse(t *testing.T) {
	withTools(t)
	got, err := Profiles("38:18:4c:aa:bb:cc") // BlueZ and wpctl differ in case
	if err != nil {
		t.Fatal(err)
	}
	want := []Profile{
		{Name: "a2dp-sink-sbc", Label: "SBC"},
		{Name: "a2dp-sink-sbc_xq", Label: "SBC-XQ"},
		{Name: "a2dp-sink-aac", Label: "AAC", Active: true},
		{Name: "a2dp-sink-ldac", Label: "LDAC"},
		{Name: "a2dp-sink-future_codec", Label: "a2dp-sink-future_codec"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v\nwant %+v", got, want)
	}
}

// The best codec has the bare name a2dp-sink; its label comes from the
// description, or is "Automatic" when that names no codec.
func TestProfilesBareNameTakesTheDescriptionsCodec(t *testing.T) {
	withTools(t)
	for addr, want := range map[string]Profile{
		"00:11:22:33:44:55": {Name: "a2dp-sink", Label: "SBC", Active: true},
		"AA:BB:CC:00:00:01": {Name: "a2dp-sink", Label: "AAC", Active: true},
		"AA:BB:CC:00:00:02": {Name: "a2dp-sink", Label: "Automatic", Active: true},
	} {
		got, err := Profiles(addr)
		if err != nil || len(got) == 0 || got[0] != want {
			t.Errorf("%s: got %+v, %v; want first %+v", addr, got, err, want)
		}
	}
}

func TestProfilesBadJSON(t *testing.T) {
	old := setTool(func(string, ...string) (string, error) { return "not json", nil })
	defer setTool(old)
	if _, err := Profiles("38:18:4C:AA:BB:CC"); err == nil {
		t.Fatal("want the parse error")
	}
}

func TestProfileLabels(t *testing.T) {
	for name, want := range map[string]string{
		"a2dp-sink-sbc": "SBC", "a2dp-sink-sbc_xq": "SBC-XQ", "a2dp-sink-aac": "AAC", "a2dp-sink-aptx": "aptX",
		"a2dp-sink-aptx_hd": "aptX HD", "a2dp-sink-ldac": "LDAC", "a2dp-sink-lc3plus_h3": "LC3",
		"a2dp-sink-mystery": "a2dp-sink-mystery",
	} {
		if got := profileLabel(name); got != want {
			t.Errorf("%s: %q, want %q", name, got, want)
		}
	}
}

func TestProfilesUnknownDeviceHasNone(t *testing.T) {
	withTools(t)
	got, err := Profiles("AA:AA:AA:AA:AA:AA")
	if err != nil || got != nil {
		t.Fatalf("got %+v, %v", got, err)
	}
}

func TestProfilesPwDumpMissing(t *testing.T) {
	old := setTool(func(string, ...string) (string, error) { return "", errors.New("not found") })
	defer setTool(old)
	if _, err := Profiles("38:18:4C:AA:BB:CC"); err == nil {
		t.Fatal("want the error")
	}
}

func TestSetProfileUsesTheCardAndIndex(t *testing.T) {
	f := withTools(t)
	if err := SetProfile("38:18:4C:AA:BB:CC", "a2dp-sink-ldac"); err != nil {
		t.Fatal(err)
	}
	if c := f.called(); c[len(c)-1] != "wpctl set-profile 52 4" {
		t.Fatalf("calls %q", c)
	}
	if err := SetProfile("38:18:4C:AA:BB:CC", "a2dp-sink-nope"); err == nil {
		t.Fatal("an unknown profile must fail")
	}
	if err := SetProfile("AA:AA:AA:AA:AA:AA", "a2dp-sink-sbc"); err == nil {
		t.Fatal("a device with no card must fail")
	}
}
