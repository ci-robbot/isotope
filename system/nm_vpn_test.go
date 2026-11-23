package main

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"
)

func stateSignal(path dbus.ObjectPath, st, reason uint32) *dbus.Signal {
	return &dbus.Signal{Path: path, Name: nmIface + ".Connection.Active.StateChanged", Body: []any{st, reason}}
}

// activating is a State property that never settles.
func activating() (uint32, error) { return 1, nil }

func TestWaitVPNWaitsForNetworkManager(t *testing.T) {
	const ac = dbus.ObjectPath("/org/freedesktop/NetworkManager/ActiveConnection/7")
	// A slow login (here 200 ms, on a 2 s bound) still connects: the page
	// waits for Activated, not for a fixed time.
	sig := make(chan *dbus.Signal, 4)
	go func() {
		time.Sleep(200 * time.Millisecond)
		sig <- stateSignal("/other", nmDeactivated, 10) // another connection
		sig <- stateSignal(ac, nmActivated, 0)
	}()
	if err := waitVPN(sig, ac, activating, 2*time.Second, time.Hour); err != nil {
		t.Fatalf("slow login: %v", err)
	}
	// A refused login ends with NetworkManager's reason.
	sig = make(chan *dbus.Signal, 1)
	sig <- stateSignal(ac, nmDeactivated, 10)
	err := waitVPN(sig, ac, activating, time.Second, time.Hour)
	if err == nil || !strings.Contains(err.Error(), "login failed") {
		t.Fatalf("refused login: %v", err)
	}
	// A missed signal: the poll reads the state.
	n := 0
	poll := func() (uint32, error) {
		if n++; n < 3 {
			return 1, nil
		}
		return nmDeactivated, nil
	}
	if err := waitVPN(make(chan *dbus.Signal), ac, poll, time.Second, time.Millisecond); err == nil || errors.Is(err, errVPNTimeout) {
		t.Fatalf("polled Deactivated: %v", err)
	}
	if err := waitVPN(make(chan *dbus.Signal), ac, func() (uint32, error) { return nmActivated, nil }, time.Second, time.Millisecond); err != nil {
		t.Fatalf("polled Activated: %v", err)
	}
	// Nothing settles: the outer bound.
	if err := waitVPN(make(chan *dbus.Signal), ac, activating, 30*time.Millisecond, time.Millisecond); !errors.Is(err, errVPNTimeout) {
		t.Fatalf("never settles: %v", err)
	}
}

func TestVPNWaitBound(t *testing.T) {
	// The tray's password dialog alone waits 2 minutes, then openconnect,
	// then the second factor: the page must not give up first.
	if b := vpnWaitBound("vpn"); b < 300*time.Second {
		t.Errorf("OpenConnect bound %v, want at least 300 s", b)
	}
	if b := vpnWaitBound("wireguard"); b <= 0 || b > vpnWaitBound("vpn") {
		t.Errorf("WireGuard bound %v", b)
	}
}
