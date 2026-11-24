package main

import (
	"strings"
	"testing"

	"github.com/godbus/dbus/v5"
)

func TestModemState(t *testing.T) {
	if got := modemState(20); !strings.Contains(got, "SIM") {
		t.Errorf("unavailable: %q", got)
	}
	if modemState(100) != "Connected" || modemState(30) != "Disconnected" {
		t.Errorf("other states: %q %q", modemState(100), modemState(30))
	}
}

func TestModemSettingsAndAPN(t *testing.T) {
	s := newModemSettings("internet.example")
	if s["connection"]["type"].Value() != "gsm" || s["gsm"]["apn"].Value() != "internet.example" || s["gsm"]["auto-config"].Value() != false {
		t.Fatalf("manual: %v", s)
	}
	if !mobileConnection(s) || modemAPN(s) != "internet.example" {
		t.Fatalf("read back: %v", s)
	}
	auto := newModemSettings("")
	if _, ok := auto["gsm"]["apn"]; ok || auto["gsm"]["auto-config"].Value() != true || modemAPN(auto) != "" {
		t.Fatalf("automatic: %v", auto)
	}
	// Back to automatic drops the APN.
	withAPN(s, "")
	if _, ok := s["gsm"]["apn"]; ok || modemAPN(s) != "" {
		t.Fatalf("reset: %v", s)
	}
	cdma := settings{"connection": {"type": dbus.MakeVariant("cdma")}}
	wifi := settings{"connection": {"type": dbus.MakeVariant("802-11-wireless")}}
	if !mobileConnection(cdma) || mobileConnection(wifi) {
		t.Fatal("connection types")
	}
}

func TestCheckAPN(t *testing.T) {
	for _, ok := range []string{"", "internet", "web.vodafone.de", "three_uk-1"} {
		if err := checkAPN(ok); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
	}
	for _, bad := range []string{"has space", "a;b", strings.Repeat("a", 101), "ü"} {
		if err := checkAPN(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestModemSub(t *testing.T) {
	m := Modem{State: "Connected", Connection: "Mobile broadband", Address: "10.64.1.2", Operator: "22210", APN: "web.omnitel.it"}
	if got := modemSub(m); got != "Connected · Mobile broadband · 10.64.1.2 · network 22210 · APN web.omnitel.it" {
		t.Errorf("connected: %q", got)
	}
	if got := modemSub(Modem{State: "Disconnected"}); got != "Disconnected · APN automatic" {
		t.Errorf("disconnected: %q", got)
	}
	if modemTitle(Modem{Iface: "cdc-wdm0"}) != "Mobile broadband (cdc-wdm0)" || modemTitle(Modem{}) != "Mobile broadband" {
		t.Error("titles")
	}
}

const modemID = "/org/freedesktop/NetworkManager/Devices/7"

func modemUI(t *testing.T, m Modem) (*UI, *fakeNet, *networkPage) {
	t.Helper()
	u, _, n := testUI(t, true)
	n.st.Modems = []Modem{m}
	p := u.view(PageNetwork).(*networkPage)
	u.show(PageNetwork)
	eventually(t, "the modem", func() bool { u.mu.Lock(); defer u.mu.Unlock(); return len(p.state.Modems) == 1 })
	u.Layout(gtxOf(1100, 720))
	return u, n, p
}

func TestNetworkPageConnectsAndDisconnectsAModem(t *testing.T) {
	_, n, p := modemUI(t, Modem{ID: modemID, Iface: "cdc-wdm0", State: "Disconnected", Ready: true})
	clickAndUpdate(p, &p.modemClicksFor(modemID).toggle)
	eventually(t, "ConnectModem", func() bool { return strings.Contains(strings.Join(n.called(), ","), "ConnectModem "+modemID) })

	_, n, p = modemUI(t, Modem{ID: modemID, Iface: "cdc-wdm0", State: "Connected", Connected: true})
	clickAndUpdate(p, &p.modemClicksFor(modemID).toggle)
	eventually(t, "DisconnectModem", func() bool { return strings.Contains(strings.Join(n.called(), ","), "DisconnectModem "+modemID) })
}

func TestNetworkPageSetsTheAPN(t *testing.T) {
	u, n, p := modemUI(t, Modem{ID: modemID, Iface: "cdc-wdm0", State: "Disconnected", Ready: true, APN: "old.apn"})
	clickAndUpdate(p, &p.modemClicksFor(modemID).apn)
	d := waitQuestion(t, u, "Mobile broadband APN")
	if got := p.apn.Editor.Text(); got != "old.apn" {
		t.Fatalf("the dialog starts with %q", got)
	}
	p.apn.Editor.SetText("bad apn")
	if d.choices[1].Enabled() {
		t.Fatal("Save is on for an APN with a space")
	}
	p.apn.Editor.SetText(" internet.example ")
	if !d.choices[1].Enabled() {
		t.Fatal("Save is off for a good APN")
	}
	u.pick(d, 1)
	eventually(t, "SetModemAPN", func() bool {
		return strings.Contains(strings.Join(n.called(), ","), "SetModemAPN "+modemID+" internet.example")
	})
}

func TestNetworkPageWithoutAModemIsUnchanged(t *testing.T) {
	u, _, _ := testUI(t, true)
	p := u.view(PageNetwork).(*networkPage)
	u.show(PageNetwork)
	eventually(t, "the state", func() bool { u.mu.Lock(); defer u.mu.Unlock(); return len(p.state.Devices) == 1 })
	u.Layout(gtxOf(1100, 720))
	if len(p.modemClicks) != 0 {
		t.Fatal("modem buttons without a modem")
	}
}

func TestPickModemConnection(t *testing.T) {
	conn := func(path, typ, iface, apn string) savedConn {
		s := settings{"connection": {"type": dbus.MakeVariant(typ)}}
		if iface != "" {
			s["connection"]["interface-name"] = dbus.MakeVariant(iface)
		}
		if typ == "gsm" {
			withAPN(s, apn)
		}
		return savedConn{dbus.ObjectPath(path), s}
	}
	wifi := conn("/c/1", "802-11-wireless", "", "")
	other := conn("/c/2", "gsm", "cdc-wdm1", "other.apn")
	mine := conn("/c/3", "gsm", "", "my.apn")
	// Available first.
	if c, ok := pickModemConnection([]savedConn{wifi, mine}, nil, "cdc-wdm0"); !ok || c.path != "/c/3" {
		t.Fatalf("available: %v %v", c.path, ok)
	}
	// Not ready: none available, so the saved one, never another
	// interface's; its APN shows on the row.
	c, ok := pickModemConnection(nil, []savedConn{wifi, other, mine}, "cdc-wdm0")
	if !ok || c.path != "/c/3" || modemAPN(c.s) != "my.apn" {
		t.Fatalf("saved: %v %v", c.path, ok)
	}
	if c, ok := pickModemConnection(nil, []savedConn{wifi, other}, "cdc-wdm1"); !ok || c.path != "/c/2" {
		t.Fatalf("bound to the interface: %v %v", c.path, ok)
	}
	if _, ok := pickModemConnection(nil, []savedConn{wifi, other}, "cdc-wdm0"); ok {
		t.Fatal("picked another modem's connection")
	}
}
