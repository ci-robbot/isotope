package printing

import (
	"strings"
	"testing"
)

func TestCheckName(t *testing.T) {
	for _, ok := range []string{"Office", "HP_LaserJet_M404", "a.b-c_1", strings.Repeat("x", 127)} {
		if err := CheckName(ok); err != nil {
			t.Errorf("CheckName(%q) = %v, want nil", ok, err)
		}
	}
	for _, bad := range []string{"", "-x", ".hidden", "with space", "a/b", "a#b", "a'b", `a"b`, "a\\b", "née", "a\nb", strings.Repeat("x", 128)} {
		if err := CheckName(bad); err == nil {
			t.Errorf("CheckName(%q) = nil, want an error", bad)
		}
	}
}

func TestCheckURI(t *testing.T) {
	for _, ok := range []string{
		"ipp://192.168.1.20/ipp/print",
		"ipps://printer.local:631/ipp/print",
		"dnssd://HP%20LaserJet._ipp._tcp.local/?uuid=1234",
		"ipp://[fe80::1%25eth0]:631/ipp/print",
	} {
		if err := CheckURI(ok); err != nil {
			t.Errorf("CheckURI(%q) = %v, want nil", ok, err)
		}
	}
	for _, bad := range []string{
		"", "usb://HP/LaserJet", "file:///dev/null", "http://x/", "socket://10.0.0.2",
		"ipp://", "ipp:///ipp/print", "ipp://host/a b", "ipp://host/\x01",
		"ipp://" + strings.Repeat("h", 1100),
		"-v",
	} {
		if err := CheckURI(bad); err == nil {
			t.Errorf("CheckURI(%q) = nil, want an error", bad)
		}
	}
}

func TestCheckText(t *testing.T) {
	if err := CheckText("Second floor, by the kitchen"); err != nil {
		t.Errorf("plain text: %v", err)
	}
	if err := CheckText("Büro 3"); err != nil {
		t.Errorf("UTF-8 text: %v", err)
	}
	for _, bad := range []string{"a\nb", "tab\there", strings.Repeat("x", 128), "\xff"} {
		if err := CheckText(bad); err == nil {
			t.Errorf("CheckText(%q) = nil, want an error", bad)
		}
	}
}

func TestQueueName(t *testing.T) {
	cases := map[string]string{
		"HP LaserJet Pro M404 [A1B2C3]": "HP_LaserJet_Pro_M404_A1B2C3",
		"Brother HL-L2350DW series":     "Brother_HL-L2350DW_series",
		"  Canon  ":                     "Canon",
		"Épson Büro":                    "pson_B_ro",
		"???":                           "Printer",
		"":                              "Printer",
		".lead":                         "lead",
	}
	for in, want := range cases {
		got := QueueName(in)
		if got != want {
			t.Errorf("QueueName(%q) = %q, want %q", in, got, want)
		}
		if err := CheckName(got); err != nil {
			t.Errorf("QueueName(%q) = %q, which CheckName refuses: %v", in, got, err)
		}
	}
	if got := QueueName(strings.Repeat("a", 300)); len(got) != 127 {
		t.Errorf("long name: %d characters, want 127", len(got))
	}
}

func TestDNSSDURI(t *testing.T) {
	got := DNSSDURI("HP LaserJet (Office) 100%", "_ipps._tcp", "e3248000-80ce-11db-8000-30cda7b3f0aa")
	want := "dnssd://HP%20LaserJet%20%28Office%29%20100%25._ipps._tcp.local/?uuid=e3248000-80ce-11db-8000-30cda7b3f0aa"
	if got != want {
		t.Errorf("DNSSDURI = %q\nwant      %q", got, want)
	}
	if err := CheckURI(got); err != nil {
		t.Errorf("CheckURI(DNSSDURI) = %v", err)
	}
	if got := DNSSDURI("Büro", "_ipp._tcp", ""); got != "dnssd://B%C3%BCro._ipp._tcp.local/" {
		t.Errorf("no uuid, UTF-8: %q", got)
	}
}

func TestAddressURI(t *testing.T) {
	for _, c := range []struct {
		typ, addr string
		port      uint16
		rp, want  string
	}{
		{"_ipp._tcp", "192.168.68.7", 631, "ipp/print", "ipp://192.168.68.7:631/ipp/print"},
		{"_ipps._tcp", "192.168.1.20", 443, "", "ipps://192.168.1.20:443/ipp/print"},
		{"_ipp._tcp", "127.0.0.1", 60000, "/ipp/print/", "ipp://127.0.0.1:60000/ipp/print"},
		{"_ipp._tcp", "2001:db8::5", 631, "printers/Office Jet", "ipp://[2001:db8::5]:631/printers/Office%20Jet"},
		{"_ipp._tcp", "fe80::1", 631, "ipp/print", ""},
		{"_ipp._tcp", "printer.local", 631, "ipp/print", ""},
		{"_ipp._tcp", "", 631, "ipp/print", ""},
		{"_ipp._tcp", "10.0.0.2", 0, "ipp/print", ""},
	} {
		got := AddressURI(c.typ, c.addr, c.port, c.rp)
		if got != c.want {
			t.Errorf("AddressURI(%q, %q, %d, %q) = %q, want %q", c.typ, c.addr, c.port, c.rp, got, c.want)
		}
		if got != "" {
			if err := CheckURI(got); err != nil {
				t.Errorf("CheckURI(%q) = %v", got, err)
			}
		}
	}
}
