package main

import (
	"bytes"
	"slices"
	"testing"

	"github.com/godbus/dbus/v5"
)

func TestDedupeAPs(t *testing.T) {
	got := dedupeAPs([]AccessPoint{
		{SSID: "Home", Strength: 40, Secured: true},
		{SSID: "Cafe", Strength: 60},
		{SSID: "", Strength: 99},
		{SSID: "Home", Strength: 80, Secured: true},
		{SSID: "Far", Strength: 10},
	})
	want := []AccessPoint{
		{SSID: "Home", Strength: 80, Secured: true},
		{SSID: "Cafe", Strength: 60},
		{SSID: "Far", Strength: 10},
	}
	if !slices.Equal(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestNMStateConnected(t *testing.T) {
	for n, want := range map[uint32]bool{0: false, 10: false, 20: false, 30: false, 40: false, 50: false, 60: false, 70: true} {
		if got := nmConnected(n); got != want {
			t.Errorf("state %d: connected %v, want %v", n, got, want)
		}
	}
}

func TestSecuredFlags(t *testing.T) {
	for _, c := range []struct {
		flags, wpa, rsn uint32
		want            bool
	}{{0, 0, 0, false}, {1, 0, 0, true}, {0, 1, 0, true}, {0, 0, 2, true}} {
		if got := apSecured(c.flags, c.wpa, c.rsn); got != c.want {
			t.Errorf("%+v: %v", c, got)
		}
	}
}

func TestWifiSettingsSecured(t *testing.T) {
	s := wifiSettings("Home", "hunter22", false, "wpa-psk")
	w := s["802-11-wireless"]
	if got, _ := w["ssid"].Value().([]byte); !bytes.Equal(got, []byte("Home")) {
		t.Errorf("ssid %v", w["ssid"])
	}
	if w["mode"].Value() != "infrastructure" {
		t.Errorf("mode %v", w["mode"])
	}
	if _, ok := w["hidden"]; ok {
		t.Error("hidden set for a visible network")
	}
	sec := s["802-11-wireless-security"]
	if sec["key-mgmt"].Value() != "wpa-psk" || sec["psk"].Value() != "hunter22" {
		t.Errorf("security %v", sec)
	}
	if s["connection"]["type"].Value() != "802-11-wireless" {
		t.Errorf("connection %v", s["connection"])
	}
}

func TestWifiSettingsOpenAndHidden(t *testing.T) {
	s := wifiSettings("Cafe", "", true, "wpa-psk")
	if _, ok := s["802-11-wireless-security"]; ok {
		t.Error("security section for an open network")
	}
	if s["802-11-wireless"]["hidden"] != dbus.MakeVariant(true) {
		t.Errorf("hidden %v", s["802-11-wireless"]["hidden"])
	}
}

func TestConnectFailureWording(t *testing.T) {
	if err := connectFailure("Home", "pw"); err != errWrongPassword {
		t.Errorf("with password: %v", err)
	}
	if err := connectFailure("Cafe", ""); err == nil || err.Error() != "Could not connect to Cafe" {
		t.Errorf("open network: %v", err)
	}
}

// M9: a WPA3-only (SAE) network needs key-mgmt "sae"; WPA2 and the mixed
// WPA2/WPA3 mode stay "wpa-psk".
func TestKeyMgmt(t *testing.T) {
	for _, c := range []struct {
		rsn  uint32
		want string
	}{
		{0, "wpa-psk"},
		{0x100, "wpa-psk"}, // PSK
		{0x500, "wpa-psk"}, // PSK and SAE
		{0x400, "sae"},     // SAE only
		{0x488, "sae"},     // SAE with its ciphers
	} {
		if got := keyMgmt(c.rsn); got != c.want {
			t.Errorf("rsn %#x: %s, want %s", c.rsn, got, c.want)
		}
	}
	s := wifiSettings("Home", "hunter22", false, "sae")
	if s["802-11-wireless-security"]["key-mgmt"].Value() != "sae" {
		t.Errorf("security %v", s["802-11-wireless-security"])
	}
}
