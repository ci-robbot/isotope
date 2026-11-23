package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"

	"github.com/kairos-community/isotope/pkg/firewall"
)

// fwFake is the machine under the firewall methods: temporary files, a
// fake unit and a fake `nft list table`. The unit loads the table when the
// flag file says on, as isotope-firewall load does, unless broken is set
// (nft missing, a ruleset that does not parse).
type fwFake struct {
	mu     sync.Mutex
	hang   map[string]bool // commands that never end by themselves
	ran    []string
	loaded bool
	broken bool
	dir    string
	oem    string
	flag   string
	rules  string
}

func (f *fwFake) runs() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.ran...)
}

func (f *fwFake) set(fn func(*fwFake)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fn(f)
}

// boot writes a boot marker (live_mode, install_mode, uki_install_mode).
func (f *fwFake) boot(t *testing.T, name string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(f.dir, "run", "cos", name), nil, 0o644); err != nil {
		t.Fatal(err)
	}
}

func firewallEnv(t *testing.T) *fwFake {
	t.Helper()
	dir := t.TempDir()
	f := &fwFake{
		dir:   dir,
		oem:   filepath.Join(dir, "oem", "94_isotope_firewall.yaml"),
		flag:  filepath.Join(dir, "etc", "isotope", "firewall"),
		rules: filepath.Join(dir, "etc", "isotope", "firewall.nft"),
		hang:  map[string]bool{},
	}
	if err := os.MkdirAll(filepath.Join(dir, "run", "cos"), 0o755); err != nil {
		t.Fatal(err)
	}
	oo, of, or, orun, ob := firewallOEM, firewallFlag, firewallRules, firewallRun, firewallBootMarkers
	ort, ost := firewallReloadTimeout, firewallStatusTimeout
	firewallOEM, firewallFlag, firewallRules = f.oem, f.flag, f.rules
	firewallBootMarkers = []string{
		filepath.Join(dir, "run", "cos", "live_mode"),
		filepath.Join(dir, "run", "cos", "install_mode"),
		filepath.Join(dir, "run", "cos", "uki_install_mode"),
	}
	firewallRun = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		cmd := name + " " + strings.Join(args, " ")
		f.mu.Lock()
		hang := f.hang[cmd]
		f.mu.Unlock()
		if hang {
			// A stuck job: it ends only when the daemon gives up on it.
			<-ctx.Done()
			return nil, ctx.Err()
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		f.ran = append(f.ran, cmd)
		switch cmd {
		case "nft list table inet isotope":
			if !f.loaded {
				return nil, errFakeExit
			}
		case "systemctl reload-or-restart isotope-firewall.service":
			b, _ := os.ReadFile(f.flag)
			f.loaded = string(b) != "off\n" && !f.broken
		}
		return nil, nil
	}
	t.Cleanup(func() {
		firewallOEM, firewallFlag, firewallRules, firewallRun, firewallBootMarkers = oo, of, or, orun, ob
		firewallReloadTimeout, firewallStatusTimeout = ort, ost
	})
	return f
}

type fakeExit struct{}

func (fakeExit) Error() string { return "exit status 1" }

var errFakeExit error = fakeExit{}

// fwStatus calls FirewallStatus over the bus.
func fwStatus(t *testing.T, sv *busFixture) firewall.Status {
	t.Helper()
	var m map[string]dbus.Variant
	var rules []FirewallRule
	if err := sv.call("FirewallStatus").Store(&m, &rules); err != nil {
		t.Fatal(err)
	}
	st := firewallStatusFromDict(m)
	st.Rules = rules
	return st
}

func TestFirewallYAML(t *testing.T) {
	st := firewallState{On: false, Allow: []FirewallRule{
		{Port: 51820, Proto: "udp"},
		{Port: 8080, Proto: "tcp", Note: "dev server"},
		{Port: 8080, Proto: "udp", Note: "Ünïcode ok"},
	}}
	b, err := renderFirewallOEM(st)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "94_isotope_firewall.yaml")
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := loadFirewallState(path)
	if err != nil {
		t.Fatal(err)
	}
	// Loaded back sorted by port, then protocol.
	want := firewallState{On: false, Allow: []FirewallRule{st.Allow[1], st.Allow[2], st.Allow[0]}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("round trip:\n got %+v\nwant %+v", got, want)
	}
	// The boot stage writes the two files the unit reads, with the same bytes
	// the daemon writes now.
	s := string(b)
	for _, w := range []string{
		"name: Isotope firewall",
		"initramfs:",
		"path: /etc/isotope/firewall\n",
		"path: /etc/isotope/firewall.nft\n",
		"permissions: 420",
	} {
		if !strings.Contains(s, w) {
			t.Errorf("%q is not in the OEM file:\n%s", w, s)
		}
	}
	nft := renderFirewallRules(want.Allow)
	for _, w := range []string{
		"table inet isotope {",
		"\t\tchain input {",
		"\t\ttcp dport 8080 accept comment \"dev server\"",
		"\t\tudp dport 8080 accept comment \"Ünïcode ok\"",
		"\t\tudp dport 51820 accept\n",
	} {
		if !strings.Contains(nft, strings.TrimPrefix(w, "\t")) {
			t.Errorf("%q is not in the rules:\n%s", w, nft)
		}
	}
}

func TestAllowPortValidates(t *testing.T) {
	for _, c := range []struct {
		port        int
		proto, note string
		ok          bool
	}{
		{8080, "tcp", "dev server", true},
		{1, "udp", "", true},
		{65535, "tcp", "", true},
		{0, "tcp", "", false},
		{65536, "tcp", "", false},
		{-1, "tcp", "", false},
		{22, "icmp", "", false},
		{22, "TCP", "", false},
		{22, "", "", false},
		{22, "tcp", `a "quote"`, false},
		{22, "tcp", `back\slash`, false},
		{22, "tcp", "two\nlines", false},
		{22, "tcp", "tab\there", false},
		{22, "tcp", strings.Repeat("x", 65), false},
		{22, "tcp", strings.Repeat("x", 64), true},
	} {
		err := checkFirewallRule(c.port, c.proto, c.note)
		if (err == nil) != c.ok {
			t.Errorf("%d/%s %q: err %v, want ok=%v", c.port, c.proto, c.note, err, c.ok)
		}
	}
	// Over the bus: refused, and nothing written or applied.
	f := firewallEnv(t)
	sv := startService(t, localeDaemon(t), &fakeAuth{allow: true})
	for _, args := range [][]any{{uint16(0), "tcp", ""}, {uint16(22), "icmp", ""}, {uint16(80), "tcp", "a\"b"}} {
		if c := sv.call("AllowPort", args...); c.Err == nil {
			t.Errorf("AllowPort %v was accepted", args)
		}
	}
	if c := sv.call("DenyPort", uint16(22), "sctp"); c.Err == nil {
		t.Error("DenyPort sctp was accepted")
	}
	noFile(t, f.oem, f.flag, f.rules)
	if r := f.runs(); len(r) != 0 {
		t.Fatalf("ran %q", r)
	}
}

func TestFirewallRefusedWithoutAction(t *testing.T) {
	f := firewallEnv(t)
	auth := &fakeAuth{allow: false}
	sv := startService(t, localeDaemon(t), auth)
	for _, c := range []*dbus.Call{
		sv.call("SetFirewall", false),
		sv.call("AllowPort", uint16(8080), "tcp", ""),
		sv.call("DenyPort", uint16(8080), "tcp"),
	} {
		if got := errName(c.Err); got != busInterface+".Error.NotAuthorized" {
			t.Fatalf("got %s (%v), want NotAuthorized", got, c.Err)
		}
	}
	me := sv.client.Names()[0]
	for _, q := range auth.questions() {
		if q != me+" "+actionFirewall {
			t.Fatalf("asked %q, want the firewall action", q)
		}
	}
	noFile(t, f.oem, f.flag, f.rules)
	// The status is open to every caller, as the ruleset is to `nft list`.
	if st := fwStatus(t, sv); !st.On || len(st.Rules) != 1 {
		t.Fatalf("status %+v", st)
	}
}

func TestFirewallRefusesFlatpakCaller(t *testing.T) {
	f := firewallEnv(t)
	fakeProc(t, uint32(os.Getpid()), true)
	auth := &fakeAuth{allow: true}
	sv := startService(t, localeDaemon(t), auth)
	if got := errName(sv.call("SetFirewall", false).Err); got != busInterface+".Error.NotAuthorized" {
		t.Fatalf("from a sandbox: %s", got)
	}
	if len(auth.questions()) != 0 {
		t.Fatal("polkit was asked for a sandboxed caller")
	}
	noFile(t, f.oem, f.flag, f.rules)
}

func readFile(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// The default allowed list is SSH, the same rules the image ships for a
// machine with no saved settings.
func TestFirewallDefaultsMatchTheImage(t *testing.T) {
	st, err := loadFirewallState(filepath.Join(t.TempDir(), "missing.yaml"))
	if err != nil || !st.On || !reflect.DeepEqual(st.Allow, firewall.Defaults) {
		t.Fatalf("default %+v, %v", st, err)
	}
	b, err := os.ReadFile("../../rootfs/common/usr/share/isotope/firewall-default.nft")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := nftBody(string(b)), nftBody(renderFirewallRules(firewall.Defaults)); got != want {
		t.Fatalf("firewall-default.nft:\n%s\nwant:\n%s", got, want)
	}
}

// nftBody is a ruleset without its comment lines.
func nftBody(s string) string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if !strings.HasPrefix(strings.TrimSpace(l), "#") {
			out = append(out, l)
		}
	}
	return strings.Join(out, "\n")
}

// testdata/firewall-rules.nft is what the daemon writes for notes that are
// hard on nft's parser; check-firewall.sh IMAGE runs the image's nft -c on
// it. Regenerate with ISOTOPE_UPDATE_GOLDEN=1.
func TestFirewallRulesGolden(t *testing.T) {
	got := renderFirewallRules([]FirewallRule{
		{Port: 22, Proto: "tcp", Note: "SSH"},
		{Port: 443, Proto: "tcp", Note: "a } # ; $x { flush ruleset"},
		{Port: 5000, Proto: "udp", Note: strings.Repeat("é", 64)},
		{Port: 8080, Proto: "tcp", Note: strings.Repeat("😀", 32)},
		{Port: 51820, Proto: "udp"},
	})
	path := "testdata/firewall-rules.nft"
	if os.Getenv("ISOTOPE_UPDATE_GOLDEN") != "" {
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil || string(want) != got {
		t.Fatalf("%s differs (%v); run with ISOTOPE_UPDATE_GOLDEN=1:\n%s", path, err, got)
	}
}

func TestFirewallChangesWriteAndApply(t *testing.T) {
	f := firewallEnv(t)
	f.set(func(f *fwFake) { f.loaded = true }) // as booted with no settings
	sv := startService(t, localeDaemon(t), &fakeAuth{allow: true})
	if st := fwStatus(t, sv); !st.On || !st.Active || !reflect.DeepEqual(st.Rules, firewall.Defaults) || st.Reason != "" {
		t.Fatalf("first status %+v", st)
	}
	if c := sv.call("AllowPort", uint16(8080), "tcp", "dev server"); c.Err != nil {
		t.Fatal(c.Err)
	}
	if c := sv.call("AllowPort", uint16(51820), "udp", ""); c.Err != nil {
		t.Fatal(c.Err)
	}
	// Allowing a port again replaces its note.
	if c := sv.call("AllowPort", uint16(8080), "tcp", "web"); c.Err != nil {
		t.Fatal(c.Err)
	}
	ssh := firewall.Defaults[0]
	want := []FirewallRule{ssh, {Port: 8080, Proto: "tcp", Note: "web"}, {Port: 51820, Proto: "udp"}}
	if st := fwStatus(t, sv); !st.On || !st.Active || !reflect.DeepEqual(st.Rules, want) {
		t.Fatalf("status %+v", st)
	}
	if got := readFile(t, f.flag); got != "on\n" {
		t.Fatalf("flag %q", got)
	}
	nft := readFile(t, f.rules)
	if !strings.Contains(nft, `tcp dport 8080 accept comment "web"`) || strings.Contains(nft, "dev server") ||
		!strings.Contains(nft, `tcp dport 22 accept comment "SSH"`) {
		t.Fatalf("rules:\n%s", nft)
	}
	// SSH is a default, not a fixed rule: denying it removes it, and that
	// is kept.
	if c := sv.call("DenyPort", uint16(22), "tcp"); c.Err != nil {
		t.Fatal(c.Err)
	}
	if strings.Contains(readFile(t, f.rules), "dport 22") {
		t.Fatal("22/tcp is still in the rules")
	}
	if st, _ := loadFirewallState(f.oem); !reflect.DeepEqual(st, firewallState{On: true, Allow: want[1:]}) {
		t.Fatalf("oem %+v", st)
	}
	if c := sv.call("DenyPort", uint16(22), "tcp"); c.Err == nil {
		t.Fatal("denying a port that is not allowed did not fail")
	}
	if c := sv.call("SetFirewall", false); c.Err != nil {
		t.Fatal(c.Err)
	}
	if got := readFile(t, f.flag); got != "off\n" {
		t.Fatalf("flag %q", got)
	}
	if st := fwStatus(t, sv); st.On || st.Active || st.Summary() != "Off" {
		t.Fatalf("status %+v", st)
	}
	// Every change reloads the unit (one nft transaction, never a stop and
	// a start) and then checks what is loaded.
	var reloads int
	for _, x := range f.runs() {
		switch x {
		case "systemctl reload-or-restart isotope-firewall.service":
			reloads++
		case "nft list table inet isotope":
		default:
			t.Fatalf("ran %q", x)
		}
	}
	if reloads != 5 { // the refused deny does not reload
		t.Fatalf("%d reloads in %q", reloads, f.runs())
	}
}

// A change whose reload leaves the table in another state than the
// setting fails: systemctl returns 0 for a unit that its conditions skip
// or whose load failed.
func TestFirewallChangeChecksWhatLoaded(t *testing.T) {
	f := firewallEnv(t)
	f.set(func(f *fwFake) { f.broken = true })
	sv := startService(t, localeDaemon(t), &fakeAuth{allow: true})
	c := sv.call("AllowPort", uint16(8080), "tcp", "")
	if c.Err == nil || !strings.Contains(c.Err.Error(), "not loaded") || !strings.Contains(c.Err.Error(), "systemctl status isotope-firewall") {
		t.Fatalf("apply error: %v", c.Err)
	}
	// The choice is saved for the next boot.
	if st, _ := loadFirewallState(f.oem); len(st.Allow) != 2 {
		t.Fatalf("oem %+v", st)
	}
	st := fwStatus(t, sv)
	if !st.On || st.Active || st.Summary() != "On, but not running" || !strings.Contains(st.Reason, "systemctl status isotope-firewall") {
		t.Fatalf("status %+v", st)
	}
}

// On a live or install boot the unit never runs: the status says so, and
// changes are refused with a sentence, before anything is written.
func TestFirewallRefusedOnTheISO(t *testing.T) {
	// One subtest per marker: its cleanup stops the service before the next
	// firewallEnv rewrites the globals the handler reads. In one loop the
	// previous service's handler goroutine had read them, and the race
	// detector does not see the D-Bus round trip as an ordering.
	for _, marker := range []string{"live_mode", "install_mode", "uki_install_mode"} {
		t.Run(marker, func(t *testing.T) { firewallRefusedOnTheISO(t, marker) })
	}
}

func firewallRefusedOnTheISO(t *testing.T, marker string) {
	f := firewallEnv(t)
	f.boot(t, marker)
	sv := startService(t, localeDaemon(t), &fakeAuth{allow: true})
	st := fwStatus(t, sv)
	if !st.Skipped || st.Active || st.Summary() != "Not running on this boot" || !strings.Contains(st.Reason, "live system") {
		t.Fatalf("%s: status %+v", marker, st)
	}
	for _, c := range []*dbus.Call{
		sv.call("SetFirewall", true),
		sv.call("AllowPort", uint16(8080), "tcp", ""),
		sv.call("DenyPort", uint16(22), "tcp"),
	} {
		if errName(c.Err) != busInterface+".Error.Refused" || !strings.Contains(c.Err.Error(), "after the install") {
			t.Fatalf("%s: %v", marker, c.Err)
		}
	}
	noFile(t, f.oem, f.flag, f.rules)
	for _, x := range f.runs() {
		if strings.HasPrefix(x, "systemctl") {
			t.Fatalf("%s: ran %q", marker, x)
		}
	}
}

// writeOEM puts text in the settings file.
func writeOEM(t *testing.T, f *fwFake, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(f.oem), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.oem, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

// A settings file that does not parse is a state, not an error: the
// status says so and lists the ports that can still be read; changes are
// refused until ResetFirewall, which keeps those ports.
func TestFirewallBrokenSettingsAreReportedAndReset(t *testing.T) {
	f := firewallEnv(t)
	good, err := renderFirewallOEM(firewallState{On: true, Allow: []FirewallRule{{Port: 8080, Proto: "tcp", Note: "web"}, {Port: 51820, Proto: "udp"}}})
	if err != nil {
		t.Fatal(err)
	}
	// SSH was removed; then the file was cut short in the middle of the
	// settings, so YAML cannot read it, but the nft lines are whole.
	broken := strings.Replace(string(good), "name: Isotope firewall\n", "name: [Isotope firewall\n", 1)
	writeOEM(t, f, broken)
	if _, err := loadFirewallState(f.oem); err == nil {
		t.Fatal("the broken file parses")
	}
	sv := startService(t, localeDaemon(t), &fakeAuth{allow: true})
	st := fwStatus(t, sv)
	want := []FirewallRule{{Port: 8080, Proto: "tcp", Note: "web"}, {Port: 51820, Proto: "udp"}}
	if !st.Broken || !strings.Contains(st.Reason, "could not be read") || !reflect.DeepEqual(st.Rules, want) {
		t.Fatalf("status %+v", st)
	}
	for _, c := range []*dbus.Call{
		sv.call("SetFirewall", true),
		sv.call("AllowPort", uint16(9000), "tcp", ""),
		sv.call("DenyPort", uint16(8080), "tcp"),
	} {
		if errName(c.Err) != busInterface+".Error.Refused" || !strings.Contains(c.Err.Error(), "reset") {
			t.Fatalf("change on a broken file: %v", c.Err)
		}
	}
	if readFile(t, f.oem) != broken {
		t.Fatal("a refused change wrote the file")
	}
	// The reset keeps what could be read, and does not bring SSH back.
	if c := sv.call("ResetFirewall"); c.Err != nil {
		t.Fatal(c.Err)
	}
	got, err := loadFirewallState(f.oem)
	if err != nil || !got.On || !reflect.DeepEqual(got.Allow, want) {
		t.Fatalf("after reset %+v %v", got, err)
	}
	if st := fwStatus(t, sv); st.Broken || !st.Active {
		t.Fatalf("status after reset %+v", st)
	}
	// ResetFirewall on a good file changes nothing.
	if c := sv.call("ResetFirewall"); errName(c.Err) != busInterface+".Error.Refused" {
		t.Fatalf("reset of a good file: %v", c.Err)
	}
}

// Nothing can be read: the reset gives the defaults, and the status lists
// no rule (the window then says the saved ports are lost).
func TestFirewallResetWithNothingReadable(t *testing.T) {
	f := firewallEnv(t)
	writeOEM(t, f, "\x00\x01 not yaml: [\n")
	sv := startService(t, localeDaemon(t), &fakeAuth{allow: true})
	if st := fwStatus(t, sv); !st.Broken || len(st.Rules) != 0 {
		t.Fatalf("status %+v", st)
	}
	if c := sv.call("ResetFirewall"); c.Err != nil {
		t.Fatal(c.Err)
	}
	got, err := loadFirewallState(f.oem)
	if err != nil || !reflect.DeepEqual(got, defaultFirewallState()) {
		t.Fatalf("after reset %+v %v", got, err)
	}
}

func TestFirewallResetNeedsTheAction(t *testing.T) {
	f := firewallEnv(t)
	writeOEM(t, f, "[")
	auth := &fakeAuth{allow: false}
	sv := startService(t, localeDaemon(t), auth)
	if errName(sv.call("ResetFirewall").Err) != busInterface+".Error.NotAuthorized" {
		t.Fatal("reset without the action")
	}
	if readFile(t, f.oem) != "[" {
		t.Fatal("written")
	}
}

// A reload that never ends is given up after firewallReloadTimeout, with a
// sentence, and the lock is free again: the status answers at once.
func TestFirewallCommandsTimeOut(t *testing.T) {
	f := firewallEnv(t)
	firewallReloadTimeout, firewallStatusTimeout = 200*time.Millisecond, 100*time.Millisecond
	f.set(func(f *fwFake) { f.hang["systemctl reload-or-restart isotope-firewall.service"] = true })
	sv := startService(t, localeDaemon(t), &fakeAuth{allow: true})
	start := time.Now()
	c := sv.call("AllowPort", uint16(8080), "tcp", "")
	if c.Err == nil || !strings.Contains(c.Err.Error(), "did not finish") || !strings.Contains(c.Err.Error(), "systemctl status isotope-firewall") {
		t.Fatalf("stuck reload: %v", c.Err)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("took %v", d)
	}
	st := fwStatus(t, sv) // would block on firewallMu if the lock were held
	if st.Active {
		t.Fatalf("status %+v", st)
	}
	// A stuck nft read: the status says the state is unknown, in time.
	f.set(func(f *fwFake) { f.hang["nft list table inet isotope"] = true })
	start = time.Now()
	st = fwStatus(t, sv)
	if st.Active || !strings.Contains(st.Reason, "did not answer") || time.Since(start) > 5*time.Second {
		t.Fatalf("stuck nft: %+v after %v", st, time.Since(start))
	}
}

// fakeFirewallClient is the daemon for the CLI tests.
type fakeFirewallClient struct {
	st    firewall.Status
	calls []string
	err   error
}

func (f *fakeFirewallClient) FirewallStatus() (firewall.Status, error) { return f.st, f.err }
func (f *fakeFirewallClient) SetFirewall(on bool) error {
	f.calls = append(f.calls, "SetFirewall "+boolStr(on))
	return f.err
}
func (f *fakeFirewallClient) AllowPort(port uint16, proto, note string) error {
	f.calls = append(f.calls, strings.Join([]string{"AllowPort", strconv.Itoa(int(port)), proto, note}, "|"))
	return f.err
}
func (f *fakeFirewallClient) DenyPort(port uint16, proto string) error {
	f.calls = append(f.calls, strings.Join([]string{"DenyPort", strconv.Itoa(int(port)), proto}, "|"))
	return f.err
}
func (f *fakeFirewallClient) ResetFirewall() error {
	f.calls = append(f.calls, "ResetFirewall")
	return f.err
}
func (f *fakeFirewallClient) Close() {}

func runFirewall(t *testing.T, fc *fakeFirewallClient, args ...string) (int, string, string) {
	t.Helper()
	old := dialFirewall
	dialFirewall = func() (firewallClient, error) { return fc, nil }
	t.Cleanup(func() { dialFirewall = old })
	var out, errb bytes.Buffer
	code := run(append([]string{"firewall"}, args...), strings.NewReader(""), &out, &errb)
	return code, out.String(), errb.String()
}

func TestFirewallCLI(t *testing.T) {
	fc := &fakeFirewallClient{st: firewall.Status{On: true, Active: true}}
	if code, _, e := runFirewall(t, fc, "allow", "8080/tcp", "dev server"); code != exitOK {
		t.Fatalf("exit %d: %s", code, e)
	}
	if code, _, e := runFirewall(t, fc, "allow", "51820/udp"); code != exitOK {
		t.Fatalf("exit %d: %s", code, e)
	}
	if code, _, e := runFirewall(t, fc, "deny", "22/tcp"); code != exitOK {
		t.Fatalf("exit %d: %s", code, e)
	}
	runFirewall(t, fc, "off")
	runFirewall(t, fc, "on")
	runFirewall(t, fc, "reset")
	want := []string{"AllowPort|8080|tcp|dev server", "AllowPort|51820|udp|", "DenyPort|22|tcp", "SetFirewall false", "SetFirewall true", "ResetFirewall"}
	if !reflect.DeepEqual(fc.calls, want) {
		t.Fatalf("calls %q, want %q", fc.calls, want)
	}
	// Bad arguments never reach the daemon.
	fc.calls = nil
	for _, bad := range [][]string{
		{"allow", "8080"}, {"allow", "8080/icmp"}, {"allow", "0/tcp"}, {"allow", "65536/tcp"},
		{"allow", "x/tcp"}, {"allow", "80/tcp", "a\"b"}, {"allow"}, {"deny", "80/tcp", "note"},
		{"on", "now"}, {"open"},
	} {
		if code, _, _ := runFirewall(t, fc, bad...); code != exitUsage {
			t.Errorf("%q: exit %d, want usage", bad, code)
		}
	}
	if len(fc.calls) != 0 {
		t.Fatalf("calls %q", fc.calls)
	}
}

func TestFirewallCLIStatus(t *testing.T) {
	rules := []FirewallRule{{Port: 22, Proto: "tcp", Note: "SSH"}, {Port: 51820, Proto: "udp"}}
	for _, c := range []struct {
		st   firewall.Status
		want []string
	}{
		{firewall.Status{On: true, Active: true, Rules: rules}, []string{"Firewall: on", "22/tcp", "SSH", "51820/udp"}},
		{firewall.Status{On: false}, []string{"Firewall: off", "No port is allowed"}},
		{firewall.Status{On: true, Reason: "isotope-firewall.service did not load it"},
			[]string{"Firewall: on, but not running", "did not load it"}},
		{firewall.Status{On: true, Skipped: true, Reason: "The firewall does not run on the live system"},
			[]string{"Firewall: not running on this boot", "live system"}},
		{firewall.Status{On: true, Active: true, Broken: true, Reason: "The firewall settings could not be read: x", Rules: rules[:1]},
			[]string{"settings could not be read", "isotope firewall reset", "22/tcp"}},
	} {
		code, out, _ := runFirewall(t, &fakeFirewallClient{st: c.st})
		if code != exitOK {
			t.Fatalf("exit %d", code)
		}
		for _, w := range c.want {
			if !strings.Contains(out, w) {
				t.Errorf("%q is not in %q", w, out)
			}
		}
		if (c.st.On && c.st.Active) != strings.Contains(out, "blocked unless allowed") {
			t.Errorf("%+v claims protection wrongly: %q", c.st, out)
		}
	}
	// A refusal names the admin group; the daemon's sentence is shown for
	// a refusal on the ISO.
	fc := &fakeFirewallClient{err: dbus.Error{Name: busInterface + ".Error.NotAuthorized"}}
	code, _, e := runFirewall(t, fc, "off")
	if code != exitError || !strings.Contains(e, "admin group") {
		t.Fatalf("exit %d: %q", code, e)
	}
	fc = &fakeFirewallClient{err: dbus.Error{Name: busInterface + ".Error.Refused", Body: []any{"the firewall does not run on the live system; change it after the install"}}}
	code, out, e := runFirewall(t, fc, "on")
	if code != exitError || !strings.Contains(e, "after the install") || strings.Contains(out, "is on") {
		t.Fatalf("exit %d: %q %q", code, out, e)
	}
}
