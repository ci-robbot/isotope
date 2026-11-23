package main

import (
	"strings"
	"testing"

	"github.com/kairos-community/isotope/pkg/firewall"
)

var fwDev = firewall.Rule{Port: 8080, Proto: "tcp", Note: "dev server"}
var fwWG = firewall.Rule{Port: 51820, Proto: "udp"}

func fwText(v fwView) string {
	text := v.Sub + "|" + v.Toggle + "|" + v.Reset + "|" + v.Hint + "|" + v.Empty
	for _, r := range v.Rows {
		text += "|" + r.Title + "|" + r.Sub
	}
	return text
}

func TestFirewallViewStates(t *testing.T) {
	for _, c := range []struct {
		name   string
		in     fwInput
		want   []string
		toggle string
		add    bool
	}{
		{"loading", fwInput{Admin: true}, []string{"Loading"}, "", false},
		{"on", fwInput{Admin: true, Loaded: true, FW: Firewall{On: true, Active: true, Rules: []firewall.Rule{fwDev, fwWG}}},
			[]string{"On", "blocked", "8080/tcp", "dev server", "51820/udp"}, "Turn off", true},
		{"on, nothing allowed", fwInput{Admin: true, Loaded: true, FW: Firewall{On: true, Active: true}},
			[]string{"On", "No port is allowed"}, "Turn off", true},
		{"off", fwInput{Admin: true, Loaded: true, FW: Firewall{On: false, Rules: []firewall.Rule{fwDev}}},
			[]string{"Off", "Every port is open", "8080/tcp"}, "Turn on", true},
		{"on, but not running", fwInput{Admin: true, Loaded: true, FW: Firewall{On: true, Reason: "isotope-firewall.service did not load the rules"}},
			[]string{"On, but not running", "Nothing is blocked", "did not load the rules"}, "Turn off", true},
		{"off, but still loaded", fwInput{Admin: true, Loaded: true, FW: Firewall{Active: true, Reason: "The rules are still loaded"}},
			[]string{"Off, but still loaded"}, "Turn on", true},
		{"skipped boot", fwInput{Admin: true, Loaded: true, FW: Firewall{On: true, Skipped: true, Reason: "The firewall does not run on the live system or during an install"}},
			[]string{"Not running on this boot", "live system"}, "", false},
		{"live", fwInput{Admin: true, Live: true}, []string{"Off on the live system"}, "", false},
		{"no daemon", fwInput{Admin: true, Loaded: true, Err: "The update service (isotope-updated) is not available."},
			[]string{"not available"}, "", false},
		{"not an admin", fwInput{Loaded: true, FW: Firewall{On: true, Active: true}},
			[]string{"On", "Only an administrator can change the firewall."}, "Turn off", true},
	} {
		v := firewallViewOf(c.in)
		text := fwText(v)
		for _, w := range c.want {
			if !strings.Contains(text, w) {
				t.Errorf("%s: %q is not in %q", c.name, w, text)
			}
		}
		if v.Toggle != c.toggle || v.CanAdd != c.add {
			t.Errorf("%s: toggle %q add %v, want %q %v", c.name, v.Toggle, v.CanAdd, c.toggle, c.add)
		}
		if v.Enabled != (c.in.Admin && c.add) {
			t.Errorf("%s: enabled %v", c.name, v.Enabled)
		}
	}
}

// firewallPage opens the Network page and waits for the firewall status.
func firewallPage(t *testing.T, admin bool) (*UI, *fakeDaemon, *networkPage) {
	t.Helper()
	u, d, _ := testUI(t, admin)
	d.mu.Lock()
	d.fw = Firewall{On: true, Rules: []firewall.Rule{fwDev}}
	d.mu.Unlock()
	p := u.view(PageNetwork).(*networkPage)
	p.Shown()
	eventually(t, "the firewall status", func() bool { u.mu.Lock(); defer u.mu.Unlock(); return p.fwLoaded })
	return u, d, p
}

func TestNetworkPageFirewallSection(t *testing.T) {
	u, d, p := firewallPage(t, true)
	u.show(PageNetwork)
	// It draws, large and small, with the section.
	for _, sz := range [][2]int{{1100, 720}, {700, 500}} {
		u.Layout(gtxOf(sz[0], sz[1]))
	}
	// Turning it off asks first; on does not.
	clickAndUpdate(p, &p.fwToggle)
	u.pick(waitQuestion(t, u, "Turn the firewall off?"), 1)
	eventually(t, "SetFirewall", func() bool { return strings.Contains(strings.Join(d.called(), ","), "SetFirewall false") })
	eventually(t, "the new status", func() bool { u.mu.Lock(); defer u.mu.Unlock(); return !p.fw.On })
	clickAndUpdate(p, &p.fwToggle)
	eventually(t, "SetFirewall true", func() bool { return strings.Contains(strings.Join(d.called(), ","), "SetFirewall true") })
	eventually(t, "on again", func() bool { u.mu.Lock(); defer u.mu.Unlock(); return p.fw.On })

	// Remove asks first.
	clickAndUpdate(p, &p.fwClicksFor(fwDev).remove)
	q := waitQuestion(t, u, "Stop allowing 8080/tcp?")
	u.pick(q, 1)
	eventually(t, "DenyPort", func() bool { return strings.Contains(strings.Join(d.called(), ","), "DenyPort 8080 tcp") })
	eventually(t, "the empty list", func() bool { u.mu.Lock(); defer u.mu.Unlock(); return len(p.fw.Rules) == 0 })
}

func TestNetworkPageAllowIncomingDialog(t *testing.T) {
	u, d, p := firewallPage(t, true)
	clickAndUpdate(p, &p.fwAdd)
	q := waitQuestion(t, u, "Allow incoming connections")
	u.Layout(gtxOf(1100, 720)) // draws with the dialog
	allow := q.choices[1]
	if allow.enabled() {
		t.Fatal("Allow is on with no port")
	}
	f := p.fwForm
	for _, bad := range []string{"0", "65536", "http", "-1", "80/tcp"} {
		f.port.Editor.SetText(bad)
		if allow.enabled() {
			t.Errorf("Allow is on for the port %q", bad)
		}
	}
	f.port.Editor.SetText("8443")
	if !allow.enabled() {
		t.Fatal("Allow is off for a valid port")
	}
	f.note.Editor.SetText(`say "hi"`)
	if allow.enabled() {
		t.Fatal("Allow is on for a note with a quote")
	}
	f.note.Editor.SetText("web app")
	if f.proto != "tcp" {
		t.Fatalf("the default protocol is %q", f.proto)
	}
	f.proto = "udp" // what a click on UDP sets
	u.pick(q, 1)
	eventually(t, "AllowPort", func() bool { return strings.Contains(strings.Join(d.called(), ","), "AllowPort 8443 udp web app") })
	eventually(t, "the new row", func() bool { u.mu.Lock(); defer u.mu.Unlock(); return len(p.fw.Rules) == 2 })
}

func TestNetworkPageFirewallRefusedForNonAdmin(t *testing.T) {
	u, d, p := firewallPage(t, false)
	u.show(PageNetwork)
	u.Layout(gtxOf(1100, 720))
	// The buttons are off; a click that gets through anyway is refused by
	// the daemon and nothing changes.
	clickAndUpdate(p, &p.fwToggle)
	clickAndUpdate(p, &p.fwAdd)
	for _, c := range d.called() {
		if strings.HasPrefix(c, "SetFirewall") || strings.HasPrefix(c, "AllowPort") {
			t.Fatalf("called %q as a non-admin", c)
		}
	}
	if q := u.question(); q != nil {
		t.Fatalf("opened %q", q.title)
	}
}

func TestNetworkPageFirewallOnLive(t *testing.T) {
	u, d, _ := testUI(t, true)
	u.hooks.Live = true
	p := u.view(PageNetwork).(*networkPage)
	p.Shown()
	u.show(PageNetwork)
	u.Layout(gtxOf(1100, 720))
	eventually(t, "the network state", func() bool { u.mu.Lock(); defer u.mu.Unlock(); return !p.loading })
	for _, c := range d.called() {
		if strings.HasPrefix(c, "FirewallStatus") {
			t.Fatal("asked the daemon on the live system")
		}
	}
}

// Only "On" with the table loaded claims protection.
func TestFirewallViewNeverClaimsBlockingWhenNothingIsLoaded(t *testing.T) {
	for _, fw := range []Firewall{{On: true}, {On: true, Skipped: true}, {}, {Active: true}} {
		v := firewallViewOf(fwInput{Admin: true, Loaded: true, FW: fw})
		if strings.Contains(v.Sub, "are blocked") {
			t.Errorf("%+v: %q", fw, v.Sub)
		}
	}
}

func TestNetworkPageShowsAFailedFirewall(t *testing.T) {
	u, d, _ := testUI(t, true)
	d.mu.Lock()
	d.fw, d.fwBroken = Firewall{On: true, Rules: []firewall.Rule{fwDev}}, true
	d.mu.Unlock()
	p := u.view(PageNetwork).(*networkPage)
	p.Shown()
	eventually(t, "the firewall status", func() bool { u.mu.Lock(); defer u.mu.Unlock(); return p.fwLoaded })
	u.mu.Lock()
	v := firewallViewOf(p.fwInput())
	u.mu.Unlock()
	if !strings.HasPrefix(v.Sub, "On, but not running") || !strings.Contains(v.Sub, "systemctl status isotope-firewall") {
		t.Fatalf("sub %q", v.Sub)
	}
}

func TestFirewallViewBrokenSettings(t *testing.T) {
	fw := Firewall{On: true, Active: true, Broken: true, Reason: "The firewall settings could not be read (yaml: x). Reset them to repair the file.",
		Rules: []firewall.Rule{fwDev}}
	v := firewallViewOf(fwInput{Admin: true, Loaded: true, FW: fw})
	if !strings.HasPrefix(v.Sub, "The firewall settings could not be read") || v.Reset != "Reset to defaults" ||
		v.Toggle != "" || v.CanAdd || !v.Enabled || len(v.Rows) != 1 {
		t.Fatalf("%+v", v)
	}
	// Not an admin: the button is there but off.
	if v := firewallViewOf(fwInput{Loaded: true, FW: fw}); v.Reset == "" || v.Enabled {
		t.Fatalf("non-admin %+v", v)
	}
}

func TestNetworkPageResetsBrokenSettings(t *testing.T) {
	for _, c := range []struct {
		rules []firewall.Rule
		body  string
	}{
		{[]firewall.Rule{fwDev}, "8080/tcp"},
		{nil, "The ports saved in the file are lost"},
	} {
		u, d, _ := testUI(t, true)
		d.mu.Lock()
		d.fw = Firewall{On: true, Broken: true, Reason: "The firewall settings could not be read.", Rules: c.rules}
		d.mu.Unlock()
		p := u.view(PageNetwork).(*networkPage)
		p.Shown()
		eventually(t, "the firewall status", func() bool { u.mu.Lock(); defer u.mu.Unlock(); return p.fwLoaded })
		u.show(PageNetwork)
		u.Layout(gtxOf(1100, 720))
		clickAndUpdate(p, &p.fwReset)
		q := waitQuestion(t, u, "Reset the firewall settings?")
		if !strings.Contains(q.body, c.body) {
			t.Fatalf("body %q, want %q", q.body, c.body)
		}
		u.pick(q, 1)
		eventually(t, "ResetFirewall", func() bool { return strings.Contains(strings.Join(d.called(), ","), "ResetFirewall") })
		eventually(t, "the repaired status", func() bool { u.mu.Lock(); defer u.mu.Unlock(); return !p.fw.Broken })
	}
}
