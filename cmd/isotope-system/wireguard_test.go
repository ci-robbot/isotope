package main

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"gioui.org/widget"
	"github.com/godbus/dbus/v5"
)

const (
	k1 = "yAnz5TF+lXXJte14tji3zlMNq+hd2rYUIgJBgB3fBmk="
	k2 = "xTIBA5rboUvnH4htodjb6e697QjLERt1NAB4mZqp8Dg="
	k3 = "HIgo9xNzJMWLKASShiTqIybxZ0U3wGLiUeJ1PKf8ykw="
)

const fullConf = `# a work tunnel
[Interface]
PrivateKey = ` + k1 + `
Address = 10.0.0.2/24, fd00::2/64
DNS = 10.0.0.1, fd00::1, corp.example
MTU = 1380
ListenPort = 51820
FwMark = 0x1234

[Peer]
PublicKey = ` + k2 + `
PresharedKey = ` + k3 + `
Endpoint = vpn.example.com:51820
AllowedIPs = 0.0.0.0/0, ::/0
PersistentKeepalive = 25

[Peer]
PublicKey = ` + k3 + `
Endpoint = [2001:db8::1]:51821
AllowedIPs = 192.168.5.0/24 # lan
`

func TestParseWireGuard(t *testing.T) {
	s, err := ParseWireGuard([]byte(fullConf))
	if err != nil {
		t.Fatal(err)
	}
	c := s["connection"]
	if c["type"].Value() != "wireguard" || c["interface-name"].Value() != "wg0" || len(c["uuid"].Value().(string)) != 36 {
		t.Errorf("connection: %v", c)
	}
	wg := s["wireguard"]
	if wg["private-key"].Value() != k1 || wg["listen-port"].Value() != uint32(51820) ||
		wg["mtu"].Value() != uint32(1380) || wg["fwmark"].Value() != uint32(0x1234) || wg["peer-routes"].Value() != true {
		t.Errorf("wireguard: %v", wg)
	}
	if got := wg["peers"].Signature().String(); got != "aa{sv}" {
		t.Fatalf("peers signature %s", got)
	}
	peers := wg["peers"].Value().([]map[string]dbus.Variant)
	if len(peers) != 2 {
		t.Fatalf("peers: %v", peers)
	}
	p := peers[0]
	if p["public-key"].Value() != k2 || p["preshared-key"].Value() != k3 || p["preshared-key-flags"].Value() != uint32(0) ||
		p["endpoint"].Value() != "vpn.example.com:51820" || p["persistent-keepalive"].Value() != uint32(25) ||
		!reflect.DeepEqual(p["allowed-ips"].Value(), []string{"0.0.0.0/0", "::/0"}) {
		t.Errorf("peer 0: %v", p)
	}
	if peers[1]["endpoint"].Value() != "[2001:db8::1]:51821" || !reflect.DeepEqual(peers[1]["allowed-ips"].Value(), []string{"192.168.5.0/24"}) {
		t.Errorf("peer 1: %v", peers[1])
	}
	v4, v6 := s["ipv4"], s["ipv6"]
	if v4["method"].Value() != "manual" || v6["method"].Value() != "manual" {
		t.Errorf("methods: %v %v", v4, v6)
	}
	if v4["address-data"].Signature().String() != "aa{sv}" {
		t.Errorf("address-data signature %s", v4["address-data"].Signature())
	}
	a4 := v4["address-data"].Value().([]map[string]dbus.Variant)
	a6 := v6["address-data"].Value().([]map[string]dbus.Variant)
	if a4[0]["address"].Value() != "10.0.0.2" || a4[0]["prefix"].Value() != uint32(24) ||
		a6[0]["address"].Value() != "fd00::2" || a6[0]["prefix"].Value() != uint32(64) {
		t.Errorf("addresses: %v %v", a4, a6)
	}
	if !reflect.DeepEqual(v4["dns-data"].Value(), []string{"10.0.0.1"}) || !reflect.DeepEqual(v6["dns-data"].Value(), []string{"fd00::1"}) ||
		!reflect.DeepEqual(v4["dns-search"].Value(), []string{"corp.example"}) {
		t.Errorf("dns: %v %v", v4, v6)
	}
	// A full tunnel must not set never-default.
	for _, k := range []string{"ipv4", "ipv6"} {
		if _, ok := s[k]["never-default"]; ok {
			t.Errorf("%s sets never-default", k)
		}
	}
}

func TestParseWireGuardCRLFAndComments(t *testing.T) {
	conf := "# top\r\n[interface]\r\nprivatekey = " + k1 + " # secret\r\nAddress=10.1.1.1\r\n\r\n; not a comment\r\n"
	if _, err := ParseWireGuard([]byte(conf)); err == nil || !strings.Contains(err.Error(), "line 6") {
		t.Fatalf("; is not a wg-quick comment, want line 6 error, got %v", err)
	}
	conf = "# top\r\n[interface]\r\nprivatekey = " + k1 + " # secret\r\nAddress=10.1.1.1\r\n\r\n[PEER]\r\npublickey=" + k2 + "\r\nAllowedIPs=10.1.1.0/24\r\n"
	s, err := ParseWireGuard([]byte(conf))
	if err != nil {
		t.Fatal(err)
	}
	if s["wireguard"]["private-key"].Value() != k1 {
		t.Errorf("key %q", s["wireguard"]["private-key"].Value())
	}
	a := s["ipv4"]["address-data"].Value().([]map[string]dbus.Variant)
	if a[0]["prefix"].Value() != uint32(32) {
		t.Errorf("bare address prefix: %v", a)
	}
	if s["ipv6"]["method"].Value() != "disabled" {
		t.Errorf("ipv6 without addresses: %v", s["ipv6"])
	}
}

func TestParseWireGuardErrors(t *testing.T) {
	cases := []struct{ name, conf, want string }{
		{"no private key", "[Interface]\nAddress = 10.0.0.2/24\n", "line 1"},
		{"bad key", "[Interface]\nPrivateKey = abc\n", "line 2"},
		{"short key", "[Interface]\nPrivateKey = " + k1[:20] + "\n", "line 2"},
		{"peer without public key", "[Interface]\nPrivateKey = " + k1 + "\n\n[Peer]\nAllowedIPs = 0.0.0.0/0\n", "line 4"},
		{"second interface", "[Interface]\nPrivateKey = " + k1 + "\n[Interface]\n", "line 3"},
		{"unknown section", "[Interface]\nPrivateKey = " + k1 + "\n[Pier]\n", "line 3"},
		{"bad endpoint", "[Interface]\nPrivateKey = " + k1 + "\n[Peer]\nPublicKey = " + k2 + "\nEndpoint = nohost\n", "line 5"},
		{"bad allowed ip", "[Interface]\nPrivateKey = " + k1 + "\n[Peer]\nPublicKey = " + k2 + "\nAllowedIPs = lan\n", "line 5"},
		{"key outside a section", "PrivateKey = " + k1 + "\n", "line 1"},
		{"not key value", "[Interface]\nnonsense\n", "line 2"},
		{"bare key", "[Interface]\n" + k1 + "\n", "line 2"},
		{"key before a section", "PrivateKey = " + k1 + "\n", "line 1"},
		{"key as address", "[Interface]\nPrivateKey = " + k1 + "\nAddress = " + k2 + "\n", "line 3"},
		{"key as endpoint", "[Interface]\nPrivateKey = " + k1 + "\n[Peer]\nPublicKey = " + k2 + "\nEndpoint = " + k3 + "\n", "line 5"},
		{"key as number", "[Interface]\nPrivateKey = " + k1 + "\nMTU = " + k2 + "\n", "line 3"},
		{"key as table", "[Interface]\nPrivateKey = " + k1 + "\nTable = " + k2 + "\n", "line 3"},
		{"key as unknown key name", "[Interface]\nPrivateKey = " + k1 + "\n" + k2 + " = x\n", "line 3"},
		{"numeric table", "[Interface]\nPrivateKey = " + k1 + "\nTable = 100\n", "line 3"},
		{"PreUp", "[Interface]\nPrivateKey = " + k1 + "\nPreUp = echo hi\n", "line 3"},
		{"PostUp", "[Interface]\nPrivateKey = " + k1 + "\nPostUp = iptables -A x\n", "line 3"},
		{"PreDown", "[Interface]\nPrivateKey = " + k1 + "\nPreDown = x\n", "line 3"},
		{"PostDown", "[Interface]\nPrivateKey = " + k1 + "\nPostDown = x\n", "line 3"},
		{"SaveConfig", "[Interface]\nPrivateKey = " + k1 + "\nSaveConfig = true\n", "line 3"},
		{"unknown interface key", "[Interface]\nPrivateKey = " + k1 + "\nAdress = 10.0.0.2/24\n", "line 3"},
		{"unknown peer key", "[Interface]\nPrivateKey = " + k1 + "\n[Peer]\nPublicKey = " + k2 + "\nAllowedIP = 1.2.3.4\n", "line 5"},
		{"duplicate PrivateKey", "[Interface]\nPrivateKey = " + k1 + "\nPrivateKey = " + k1 + "\n", "line 3"},
		{"duplicate MTU", "[Interface]\nPrivateKey = " + k1 + "\nMTU = 1300\nMTU = 1400\n", "line 4"},
		{"duplicate Endpoint", "[Interface]\nPrivateKey = " + k1 + "\n[Peer]\nPublicKey = " + k2 + "\nEndpoint = a:1\nEndpoint = b:2\n", "line 6"},
		{"IPv4 full tunnel with no IPv4 address", "[Interface]\nPrivateKey = " + k1 + "\nAddress = fd00::2/64\n[Peer]\nPublicKey = " + k2 + "\nAllowedIPs = 0.0.0.0/0, ::/0\n", "line 6"},
		{"IPv4 DNS with no IPv4 address", "[Interface]\nPrivateKey = " + k1 + "\nAddress = fd00::2/64\nDNS = 10.0.0.1\n", "line 4"},
	}
	for _, c := range cases {
		_, err := ParseWireGuard([]byte(c.conf))
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: got %v, want an error naming %s", c.name, err, c.want)
			continue
		}
		// An error never shows the file: it can hold a key.
		for _, secret := range []string{k1, k2, k3, k1[:16], k2[:16], k3[:16], "echo hi", "iptables"} {
			if strings.Contains(err.Error(), secret) {
				t.Errorf("%s: the error shows %q: %v", c.name, secret, err)
			}
		}
	}
	if _, err := ParseWireGuard(nil); err == nil {
		t.Error("an empty file must fail")
	}
}

func wgConfWith(extra string) []byte {
	return []byte("[Interface]\nPrivateKey = " + k1 + "\nAddress = 10.0.0.2/24\n" + extra)
}

func TestParseWireGuardTable(t *testing.T) {
	for _, c := range []struct {
		table  string
		routes bool
	}{{"", true}, {"Table = auto\n", true}, {"Table = off\n", false}, {"table = OFF\n", false}} {
		s, err := ParseWireGuard(wgConfWith(c.table))
		if err != nil {
			t.Fatalf("%q: %v", c.table, err)
		}
		if got := s["wireguard"]["peer-routes"].Value(); got != c.routes {
			t.Errorf("%q: peer-routes %v, want %v", c.table, got, c.routes)
		}
	}
}

func TestParseWireGuardRepeatedLines(t *testing.T) {
	conf := string(wgConfWith("Address = fd00::2/64\nDNS = 10.0.0.1\nDNS = fd00::1\n")) +
		"[Peer]\nPublicKey = " + k2 + "\nAllowedIPs = 10.1.0.0/16\nAllowedIPs = 0.0.0.0/0, ::/0\n"
	s, err := ParseWireGuard([]byte(conf))
	if err != nil {
		t.Fatal(err)
	}
	peers := s["wireguard"]["peers"].Value().([]map[string]dbus.Variant)
	if got := peers[0]["allowed-ips"].Value(); !reflect.DeepEqual(got, []string{"10.1.0.0/16", "0.0.0.0/0", "::/0"}) {
		t.Errorf("AllowedIPs lines must add up: %v", got)
	}
	if got := len(s["ipv4"]["address-data"].Value().([]map[string]dbus.Variant)); got != 1 {
		t.Errorf("v4 addresses %d", got)
	}
	if !reflect.DeepEqual(s["ipv4"]["dns-data"].Value(), []string{"10.0.0.1"}) || !reflect.DeepEqual(s["ipv6"]["dns-data"].Value(), []string{"fd00::1"}) {
		t.Errorf("DNS lines must add up: %v %v", s["ipv4"], s["ipv6"])
	}
}

func TestNameWireGuard(t *testing.T) {
	s, _ := ParseWireGuard([]byte(fullConf))
	nameWireGuard(s, "Work VPN (Berlin) long name", nil)
	c := s["connection"]
	if c["id"].Value() != "Work VPN (Berlin) long name" {
		t.Errorf("connection: %v", c)
	}
	if got := c["interface-name"].Value().(string); got != "WorkVPNBerlinlo" {
		t.Errorf("interface %q", got)
	}
	for name, want := range map[string]string{"日本": "wg0", ".": "wg0", "..": "wg0", "a/b": "ab"} {
		nameWireGuard(s, name, nil)
		if got := c["interface-name"].Value(); got != want {
			t.Errorf("%q: interface %v, want %s", name, got, want)
		}
	}
	used := map[string]bool{"work": true, "work1": true}
	nameWireGuard(s, "work", used)
	if got := c["interface-name"].Value(); got != "work2" {
		t.Errorf("a used name: %v", got)
	}
	used = map[string]bool{"WorkVPNBerlinlo": true}
	nameWireGuard(s, "Work VPN (Berlin) long name", used)
	if got := c["interface-name"].Value().(string); got != "WorkVPNBerlinl1" || len(got) > 15 {
		t.Errorf("a used long name: %v", got)
	}
}

func TestVPNsFromConnections(t *testing.T) {
	mk := func(typ, id, uuid string, extra map[string]map[string]dbus.Variant) settings {
		s := settings{"connection": {
			"type": dbus.MakeVariant(typ), "id": dbus.MakeVariant(id), "uuid": dbus.MakeVariant(uuid)}}
		for k, v := range extra {
			s[k] = v
		}
		return s
	}
	all := []settings{
		mk("802-11-wireless", "Cafe", "u0", nil),
		mk("wireguard", "Work", "u1", nil),
		mk("vpn", "Corp", "u2", map[string]map[string]dbus.Variant{"vpn": {"service-type": dbus.MakeVariant("org.freedesktop.NetworkManager.openconnect")}}),
		mk("vpn", "Other", "u3", map[string]map[string]dbus.Variant{"vpn": {"service-type": dbus.MakeVariant("org.freedesktop.NetworkManager.openvpn")}}),
		mk("802-3-ethernet", "Wired", "u4", nil),
	}
	got := vpnsFromConnections(all, map[string]bool{"u1": true, "u4": true})
	want := []VPN{
		{ID: "Corp", UUID: "u2", Kind: "OpenConnect"},
		{ID: "Other", UUID: "u3", Kind: "VPN"},
		{ID: "Work", UUID: "u1", Kind: "WireGuard", Active: true},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v\nwant %+v", got, want)
	}
}

func TestImportWireGuardFile(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "work.conf")
	if err := os.WriteFile(f, []byte(fullConf), 0o600); err != nil {
		t.Fatal(err)
	}
	n := &fakeNet{}
	if got := wgName(" ", f); got != "work" {
		t.Errorf("default name %q", got)
	}
	if got := wgName("Mine", f); got != "Mine" {
		t.Errorf("typed name %q", got)
	}
	if err := importWireGuardFile(n, "work", f); err != nil {
		t.Fatal(err)
	}
	if got := n.called(); len(got) != 1 || !strings.HasPrefix(got[0], "ImportWireGuard work ") {
		t.Errorf("calls %v", got)
	}
	if err := importWireGuardFile(n, "x", filepath.Join(dir, "missing.conf")); err == nil {
		t.Error("a missing file must fail")
	}
}

func TestImportWireGuardFileTooBig(t *testing.T) {
	f := filepath.Join(t.TempDir(), "big.conf")
	if err := os.WriteFile(f, append([]byte(fullConf), make([]byte, wgMaxSize)...), 0o600); err != nil {
		t.Fatal(err)
	}
	n := &fakeNet{}
	if err := importWireGuardFile(n, "big", f); err == nil || !strings.Contains(err.Error(), "too big") {
		t.Fatalf("got %v", err)
	}
	if len(n.called()) != 0 {
		t.Errorf("calls %v", n.called())
	}
}

func TestActiveReason(t *testing.T) {
	if got := activeReason(10); !strings.Contains(got, "login") {
		t.Errorf("10: %q", got)
	}
	if got := activeReason(6); !strings.Contains(got, "unreachable") {
		t.Errorf("6: %q", got)
	}
	if got := activeReason(99); got == "" {
		t.Error("unknown reason has no words")
	}
}

// clickAndUpdate presses a button and lets the page see it.
func clickAndUpdate(p *networkPage, c *widget.Clickable) {
	c.Click()
	p.Update(gtxOf(1100, 720))
}

func TestNetworkPageVPNWiring(t *testing.T) {
	u, _, n := testUI(t, true)
	n.vpns = []VPN{{ID: "Work", UUID: "u1", Kind: "WireGuard"}, {ID: "Corp", UUID: "u2", Kind: "OpenConnect", Active: true}}
	p := u.view(PageNetwork).(*networkPage)
	p.Shown()
	eventually(t, "the VPN list", func() bool { u.mu.Lock(); defer u.mu.Unlock(); return len(p.vpns) == 2 })
	u.Layout(gtxOf(1100, 720)) // draws with the list

	clickAndUpdate(p, &p.vpnClicksFor("u1").toggle)
	eventually(t, "ActivateVPN", func() bool { return strings.Contains(strings.Join(n.called(), ","), "ActivateVPN u1") })
	clickAndUpdate(p, &p.vpnClicksFor("u2").toggle)
	eventually(t, "DeactivateVPN", func() bool { return strings.Contains(strings.Join(n.called(), ","), "DeactivateVPN u2") })

	// Remove asks first, and does nothing on Cancel.
	clickAndUpdate(p, &p.vpnClicksFor("u1").remove)
	q := waitQuestion(t, u, "Remove Work")
	u.pick(q, 0)
	if strings.Contains(strings.Join(n.called(), ","), "RemoveVPN") {
		t.Fatal("Cancel removed the VPN")
	}
	clickAndUpdate(p, &p.vpnClicksFor("u1").remove)
	q = waitQuestion(t, u, "Remove Work")
	u.pick(q, 1)
	eventually(t, "RemoveVPN", func() bool { return strings.Contains(strings.Join(n.called(), ","), "RemoveVPN u1") })
}

func TestNetworkPageImportDialog(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.WriteFile(filepath.Join(home, "office.conf"), []byte(fullConf), 0o600); err != nil {
		t.Fatal(err)
	}
	u, _, n := testUI(t, true)
	p := u.view(PageNetwork).(*networkPage)
	p.Shown()
	clickAndUpdate(p, &p.importWG)
	q := waitQuestion(t, u, "Import a WireGuard file")
	u.Layout(gtxOf(1100, 720)) // draws with the dialog
	// Import is off until the path names a file.
	if q.choices[1].enabled() {
		t.Fatal("Import is on with no file")
	}
	p.wgFile.Editor.SetText("~/missing.conf")
	if q.choices[1].enabled() {
		t.Fatal("Import is on for a missing file")
	}
	p.wgFile.Editor.SetText("~/office.conf")
	if !q.choices[1].enabled() {
		t.Fatal("Import is off for a real file")
	}
	u.pick(q, 1)
	eventually(t, "ImportWireGuard", func() bool { return strings.Contains(strings.Join(n.called(), ","), "ImportWireGuard office ") })
	// A typed name wins over the file name.
	clickAndUpdate(p, &p.importWG)
	q = waitQuestion(t, u, "Import a WireGuard file")
	p.wgFile.Editor.SetText(filepath.Join(home, "office.conf"))
	p.wgName.Editor.SetText("Berlin")
	u.pick(q, 1)
	eventually(t, "the named import", func() bool { return strings.Contains(strings.Join(n.called(), ","), "ImportWireGuard Berlin ") })
}

func TestParseWireGuardIPv6WithoutAddress(t *testing.T) {
	peer := func(ips string) string {
		return "[Peer]\nPublicKey = " + k2 + "\nAllowedIPs = " + ips + "\n"
	}
	for _, c := range []struct{ name, extra, want string }{
		{"::/0", peer("0.0.0.0/0, ::/0"), "link-local"},
		{"another v6 prefix", peer("fd00::/8"), "link-local"},
		{"no v6 AllowedIPs", peer("0.0.0.0/0"), "disabled"},
		{"no peers", "", "disabled"},
		{"Table off", "Table = off\n" + peer("::/0"), "disabled"},
	} {
		s, err := ParseWireGuard(wgConfWith(c.extra))
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if got := s["ipv6"]["method"].Value(); got != c.want {
			t.Errorf("%s: ipv6.method %v, want %s", c.name, got, c.want)
		}
		if _, ok := s["ipv6"]["address-data"]; ok {
			t.Errorf("%s: ipv6 has addresses", c.name)
		}
	}
}

func TestParseWireGuardFullTunnelDNSPriority(t *testing.T) {
	peer := func(ips string) string {
		return "[Peer]\nPublicKey = " + k2 + "\nAllowedIPs = " + ips + "\n"
	}
	const both = "[Interface]\nPrivateKey = " + k1 + "\nAddress = 10.0.0.2/24, fd00::2/64\n"
	for _, c := range []struct {
		name, conf string
		v4, v6     bool // a dns-priority of -50 on the family
	}{
		{"full v4 and v6, DNS on both", both + "DNS = 10.0.0.1, fd00::1\n" + peer("0.0.0.0/0, ::/0"), true, true},
		{"full v4 only, v4 DNS", both + "DNS = 10.0.0.1\n" + peer("0.0.0.0/0"), true, false},
		{"::/0 only, v4 DNS", both + "DNS = 10.0.0.1\n" + peer("10.0.0.0/8, ::/0"), true, false},
		{"full on a second peer", both + "DNS = fd00::1\n" + peer("10.9.0.0/16") + peer("0.0.0.0/0"), false, true},
		{"split tunnel", both + "DNS = 10.0.0.1, fd00::1\n" + peer("10.0.0.0/8, fd00::/8"), false, false},
		{"full, no DNS", both + peer("0.0.0.0/0, ::/0"), false, false},
		{"Table off", both + "Table = off\nDNS = 10.0.0.1\n" + peer("0.0.0.0/0"), false, false},
	} {
		s, err := ParseWireGuard([]byte(c.conf))
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		for fam, want := range map[string]bool{"ipv4": c.v4, "ipv6": c.v6} {
			got, ok := s[fam]["dns-priority"]
			if !want {
				if ok {
					t.Errorf("%s: %s.dns-priority %v, want none", c.name, fam, got)
				}
				continue
			}
			if p, isInt := got.Value().(int32); !ok || !isInt || p != -50 || got.Signature().String() != "i" {
				t.Errorf("%s: %s.dns-priority %v, want int32 -50", c.name, fam, got)
			}
		}
	}
}
