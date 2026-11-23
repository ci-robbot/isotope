package main

import (
	"reflect"
	"strings"
	"testing"
)

func TestOpenConnectSettings(t *testing.T) {
	s, err := openConnectSettings(OpenConnect{Name: " Work ", Gateway: " vpn.example.com ", Protocol: "gp", User: "ada"})
	if err != nil {
		t.Fatal(err)
	}
	str := func(group, key string) string { v, _ := s[group][key].Value().(string); return v }
	if str("connection", "type") != "vpn" || str("connection", "id") != "Work" || str("connection", "uuid") == "" {
		t.Errorf("connection = %v", s["connection"])
	}
	if ac, _ := s["connection"]["autoconnect"].Value().(bool); ac {
		t.Error("a company VPN connects by itself")
	}
	if got := str("vpn", "service-type"); got != "org.freedesktop.NetworkManager.openconnect" {
		t.Errorf("service-type = %q", got)
	}
	if got := str("vpn", "user-name"); got != "ada" {
		t.Errorf("user-name = %q", got)
	}
	data, ok := s["vpn"]["data"].Value().(map[string]string) // a{ss}
	if !ok {
		t.Fatalf("vpn.data is %T, not a{ss}", s["vpn"]["data"].Value())
	}
	want := map[string]string{
		"gateway": "vpn.example.com", "protocol": "gp", "authtype": "password",
		// NOT_SAVED: NetworkManager asks the agent at each connect.
		"cookie-flags": "2", "gateway-flags": "2", "gwcert-flags": "2", "resolve-flags": "2",
	}
	if !reflect.DeepEqual(data, want) {
		t.Errorf("vpn.data = %v, want %v", data, want)
	}
	if _, ok := s["vpn"]["secrets"]; ok {
		t.Error("the connection carries secrets")
	}
	if str("ipv4", "method") != "auto" || str("ipv6", "method") != "auto" {
		t.Errorf("ip = %v %v", s["ipv4"], s["ipv6"])
	}
	// The list's kind for it.
	if v, ok := vpnFromSettings(s, nil); !ok || v.Kind != "OpenConnect" || v.ID != "Work" {
		t.Errorf("vpnFromSettings = %+v %v", v, ok)
	}

	// Each protocol of the form; the server names an unnamed connection.
	for _, p := range OpenConnectProtocols {
		s, err := openConnectSettings(OpenConnect{Gateway: "https://vpn.example.com/eng", Protocol: p.ID, User: "ada"})
		if err != nil {
			t.Fatalf("%s: %v", p.ID, err)
		}
		if id, _ := s["connection"]["id"].Value().(string); id != "https://vpn.example.com/eng" {
			t.Errorf("%s: id = %q", p.ID, id)
		}
	}
	var ids []string
	for _, p := range OpenConnectProtocols {
		ids = append(ids, p.ID+"="+p.Label)
	}
	// Juniper (nc) and Pulse / Ivanti (pulse) are two protocols in openconnect.
	if got := strings.Join(ids, ","); got != "anyconnect=Cisco AnyConnect,gp=Palo Alto GlobalProtect,fortinet=Fortinet,nc=Juniper,pulse=Pulse / Ivanti" {
		t.Errorf("protocols %s", got)
	}

	for _, bad := range []OpenConnect{
		{Gateway: "", Protocol: "gp", User: "ada"},
		{Gateway: "--script=/tmp/x", Protocol: "gp", User: "ada"},
		{Gateway: "vpn example.com", Protocol: "gp", User: "ada"},
		{Gateway: "vpn.example.com", Protocol: "openvpn", User: "ada"},
		{Gateway: "vpn.example.com", Protocol: "gp", User: " "},
		{Gateway: "vpn.example.com", Protocol: "gp", User: "ada\nx"},
	} {
		if _, err := openConnectSettings(bad); err == nil {
			t.Errorf("%+v was accepted", bad)
		}
	}
}

func TestNetworkPageOpenConnectDialog(t *testing.T) {
	u, _, n := testUI(t, true)
	p := u.view(PageNetwork).(*networkPage)
	p.Shown()
	clickAndUpdate(p, &p.addOC)
	q := waitQuestion(t, u, "Add a company VPN")
	u.Layout(gtxOf(1100, 720)) // draws with the dialog
	if q.choices[1].enabled() {
		t.Fatal("Add is on with an empty form")
	}
	p.oc.server.Editor.SetText("vpn.example.com")
	if q.choices[1].enabled() {
		t.Fatal("Add is on with no user name")
	}
	p.oc.user.Editor.SetText("ada")
	if !q.choices[1].enabled() {
		t.Fatal("Add is off for a full form")
	}
	if p.oc.protocol != "anyconnect" {
		t.Fatalf("the default type is %q", p.oc.protocol)
	}
	p.oc.protocol = "fortinet" // what a click on the third type sets
	u.pick(q, 1)
	eventually(t, "AddOpenConnect", func() bool {
		return strings.Contains(strings.Join(n.called(), ","), "AddOpenConnect |vpn.example.com|fortinet|ada")
	})
}
