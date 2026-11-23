package firewall

import (
	"strings"
	"testing"
)

func TestCheck(t *testing.T) {
	for _, c := range []struct {
		port        int
		proto, note string
		ok          bool
	}{
		{8080, "tcp", "dev server", true},
		{1, "udp", "", true},
		{65535, "tcp", "Ünïcode ok", true},
		{0, "tcp", "", false},
		{65536, "tcp", "", false},
		{22, "icmp", "", false},
		{22, "TCP", "", false},
		{22, "tcp", `a "quote"`, false},
		{22, "tcp", `back\slash`, false},
		{22, "tcp", "two\nlines", false},
		{22, "tcp", strings.Repeat("x", MaxNote+1), false},
		{22, "tcp", strings.Repeat("x", MaxNote), true},
		{22, "tcp", strings.Repeat("é", MaxNote), true},
	} {
		if err := Check(c.port, c.proto, c.note); (err == nil) != c.ok {
			t.Errorf("%d/%s %q: %v, want ok=%v", c.port, c.proto, c.note, err, c.ok)
		}
	}
}

func TestParsePortProto(t *testing.T) {
	port, proto, err := ParsePortProto("8080/tcp")
	if err != nil || port != 8080 || proto != "tcp" {
		t.Fatalf("%d %q %v", port, proto, err)
	}
	for _, bad := range []string{"8080", "8080/", "/tcp", "x/tcp", "0/udp", "65536/udp", "53/icmp", "-1/tcp"} {
		if _, _, err := ParsePortProto(bad); err == nil {
			t.Errorf("%q was accepted", bad)
		}
	}
	if (Rule{Port: 53, Proto: "udp"}).String() != "53/udp" {
		t.Fatal("String")
	}
}

func TestCheckNoteSaysBytesNotCharacters(t *testing.T) {
	err := CheckNote(strings.Repeat("😀", 40)) // 40 characters, 160 bytes
	if err == nil || strings.Contains(err.Error(), "64 characters") {
		t.Fatalf("40 emoji: %v", err)
	}
}

func TestStatusSummary(t *testing.T) {
	for _, c := range []struct {
		s    Status
		want string
	}{
		{Status{On: true, Active: true}, "On"},
		{Status{On: true}, "On, but not running"},
		{Status{}, "Off"},
		{Status{Active: true}, "Off, but still loaded"},
		{Status{On: true, Skipped: true}, "Not running on this boot"},
		{Status{On: true, Active: true, Broken: true}, "The firewall settings could not be read"},
	} {
		if got := c.s.Summary(); got != c.want {
			t.Errorf("%+v: %q, want %q", c.s, got, c.want)
		}
	}
}

func TestDefaultsAllowSSH(t *testing.T) {
	if len(Defaults) != 1 || Defaults[0].String() != "22/tcp" || Check(22, "tcp", Defaults[0].Note) != nil {
		t.Fatalf("%+v", Defaults)
	}
}
