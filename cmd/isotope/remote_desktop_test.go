package main

import (
	"bytes"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"

	"github.com/kairos-community/isotope/pkg/firewall"
	"github.com/kairos-community/isotope/pkg/remotedesktop"
)

// rdEnv is a sandbox for `isotope remote-desktop`: a session file, a
// wayvnc that exists, a fake systemctl (units it was asked to start are
// active), the firewall fake, and no real exec.
type rdEnv struct {
	dir      string
	fw       *fakeFirewallClient
	active   bool
	startOK  bool
	listens  bool
	calls    []string
	execArgv []string
	execEnv  []string
}

func newRDEnv(t *testing.T, session string) *rdEnv {
	t.Helper()
	e := &rdEnv{dir: t.TempDir(), startOK: true, listens: true,
		fw: &fakeFirewallClient{st: firewall.Status{On: true, Active: true, Rules: []firewall.Rule{{Port: 22, Proto: "tcp", Note: "SSH"}}}}}
	write := func(name, body string) string {
		p := filepath.Join(e.dir, name)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(e.dir, "config"))
	t.Setenv("XDG_RUNTIME_DIR", filepath.Join(e.dir, "run"))
	t.Setenv("ISOTOPE_UPDATE_RELEASE_FILE", filepath.Join(e.dir, "no-release"))
	t.Setenv("WAYLAND_DISPLAY", "wayland-1")
	t.Setenv("XKB_DEFAULT_LAYOUT", "")
	saved := []any{rdSessionFile, rdLiveMarker, rdKeyboard, rdWayvnc, rdSystemctl, rdJournal, rdFirewall, rdUser, rdAddresses, rdListening, rdExec, rdStartWait, rdRSABits}
	t.Cleanup(func() {
		rdSessionFile, rdLiveMarker, rdKeyboard, rdWayvnc = saved[0].(string), saved[1].(string), saved[2].(string), saved[3].(string)
		rdSystemctl = saved[4].(func(...string) ([]byte, error))
		rdJournal = saved[5].(func() string)
		rdFirewall = saved[6].(func() (firewallClient, error))
		rdUser = saved[7].(func() string)
		rdAddresses = saved[8].(func() []string)
		rdListening = saved[9].(func() bool)
		rdExec = saved[10].(func(string, []string, []string) error)
		rdStartWait = saved[11].(time.Duration)
		rdRSABits = saved[12].(int)
	})
	rdSessionFile = write("session", "# comment\nSESSION="+session+"\nDESKTOP_NAME=X\n")
	rdLiveMarker = filepath.Join(e.dir, "live-desktop")
	rdKeyboard = write("keyboard", "XKB_LAYOUT=de\nXKB_VARIANT=nodeadkeys\n")
	rdWayvnc = write("wayvnc", "")
	rdSystemctl = func(args ...string) ([]byte, error) {
		e.calls = append(e.calls, strings.Join(args, " "))
		switch args[0] {
		case "is-active":
			if e.active {
				return []byte("active\n"), nil
			}
			return []byte("inactive\n"), errExit
		case "start":
			e.active = e.startOK
		case "stop":
			e.active = false
		}
		return nil, nil
	}
	rdJournal = func() string { return "Failed to bind socket" }
	rdFirewall = func() (firewallClient, error) { return e.fw, nil }
	rdUser = func() string { return "anna" }
	rdAddresses = func() []string { return []string{"192.168.1.20"} }
	rdListening = func() bool { return e.listens }
	rdExec = func(path string, argv, env []string) error { e.execArgv, e.execEnv = argv, env; return nil }
	rdStartWait = 0
	rdRSABits = 2048
	return e
}

var errExit = os.ErrProcessDone

func runRD(t *testing.T, stdin string, args ...string) (int, string, string) {
	t.Helper()
	var out, errOut bytes.Buffer
	code := run(append([]string{"remote-desktop"}, args...), strings.NewReader(stdin), &out, &errOut)
	return code, out.String(), errOut.String()
}

func rdStatusJSON(t *testing.T) remotedesktop.Status {
	t.Helper()
	code, out, errOut := runRD(t, "", "status", "--json")
	if code != 0 {
		t.Fatalf("status --json: %d %s", code, errOut)
	}
	var st remotedesktop.Status
	if err := json.Unmarshal([]byte(out), &st); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	return st
}

// Turned on: the password saved, a configuration that always asks for it
// and always encrypts, the keys in the formats neatvnc reads, the port
// allowed with this user's note, the unit started; turned off: the unit
// stopped and that rule, and only that, removed.
func TestRemoteDesktopOnAndOff(t *testing.T) {
	e := newRDEnv(t, "sway")
	if code, _, errOut := runRD(t, "", "on"); code == 0 || !strings.Contains(errOut, "set a remote desktop password first") {
		t.Fatalf("on without a password: %d %q", code, errOut)
	}
	if len(e.fw.calls) != 0 || e.active {
		t.Fatalf("something happened without a password: %v active=%v", e.fw.calls, e.active)
	}
	if code, _, errOut := runRD(t, "short\n", "password"); code == 0 || !strings.Contains(errOut, "at least 8") {
		t.Fatalf("a short password: %d %q", code, errOut)
	}
	if code, out, errOut := runRD(t, "correct horse\n", "password"); code != 0 || !strings.Contains(out, "is set") {
		t.Fatalf("password: %d %q %q", code, out, errOut)
	}
	code, out, errOut := runRD(t, "", "on")
	if code != 0 {
		t.Fatalf("on: %d %s", code, errOut)
	}
	if !strings.Contains(out, "192.168.1.20:5900 as anna") {
		t.Errorf("on said %q", out)
	}
	if got := strings.Join(e.fw.calls, ","); got != "AllowPort|5900|tcp|Remote desktop (anna)" {
		t.Errorf("firewall calls: %s", got)
	}
	if !e.active {
		t.Error("the unit was not started")
	}
	conf, err := os.ReadFile(rdConfigFile())
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"enable_auth=true", "username=anna", "password=correct horse", "address=0.0.0.0", "port=5900",
		"rsa_private_key_file=rsa-key.pem", "private_key_file=tls-key.pem", "certificate_file=tls-cert.pem", "use_relative_paths=true"} {
		if !strings.Contains(string(conf), want+"\n") {
			t.Errorf("the configuration has no %q:\n%s", want, conf)
		}
	}
	for _, never := range []string{"relax_encryption", "allow_broken_crypto", "enable_pam"} {
		if strings.Contains(string(conf), never) {
			t.Errorf("the configuration has %q", never)
		}
	}
	for _, f := range []string{"wayvnc.conf", "password", "rsa-key.pem", "tls-key.pem", "tls-cert.pem"} {
		fi, err := os.Stat(filepath.Join(rdConfigDir(), f))
		if err != nil || fi.Mode().Perm() != 0o600 {
			t.Errorf("%s: %v %v", f, fi.Mode(), err)
		}
	}
	if fi, _ := os.Stat(rdConfigDir()); fi.Mode().Perm() != 0o700 {
		t.Errorf("the directory is %v", fi.Mode())
	}
	pemOf := func(name string) *pem.Block {
		b, _ := os.ReadFile(filepath.Join(rdConfigDir(), name))
		blk, _ := pem.Decode(b)
		if blk == nil {
			t.Fatalf("%s is not PEM", name)
		}
		return blk
	}
	if blk := pemOf("rsa-key.pem"); blk.Type != "RSA PRIVATE KEY" {
		t.Errorf("the RSA key is %q; neatvnc reads only PKCS #1", blk.Type)
	} else if _, err := x509.ParsePKCS1PrivateKey(blk.Bytes); err != nil {
		t.Error(err)
	}
	if blk := pemOf("tls-key.pem"); blk.Type != "EC PRIVATE KEY" {
		t.Errorf("the TLS key is %q", blk.Type)
	}
	cert, err := x509.ParseCertificate(pemOf("tls-cert.pem").Bytes)
	if err != nil {
		t.Fatal(err)
	}
	if cert.NotAfter.Before(cert.NotBefore.AddDate(9, 0, 0)) {
		t.Errorf("the certificate lasts only until %v", cert.NotAfter)
	}

	// The status, with the rule now in the firewall.
	e.fw.st.Rules = append(e.fw.st.Rules, firewall.Rule{Port: 5900, Proto: "tcp", Note: "Remote desktop (anna)"})
	st := rdStatusJSON(t)
	if !st.Available || !st.Running || !st.PasswordSet || st.User != "anna" || st.Port != 5900 || st.Firewall != remotedesktop.FirewallOpen || len(st.Fingerprint) != 95 {
		t.Errorf("status: %+v", st)
	}
	fp := st.Fingerprint

	// A new password restarts the server with it; the keys stay.
	e.fw.calls = nil
	if code, out, _ := runRD(t, "", "password", "--generate"); code != 0 || !strings.Contains(out, "The remote desktop password is ") {
		t.Fatalf("--generate: %d %q", code, out)
	}
	if conf, _ := os.ReadFile(rdConfigFile()); strings.Contains(string(conf), "correct horse") {
		t.Error("the old password stayed")
	}
	if rdFingerprint() != fp {
		t.Error("the certificate changed with the password")
	}
	if !e.active {
		t.Error("the server is not running after a new password")
	}

	e.fw.calls = nil
	if code, out, errOut := runRD(t, "", "off"); code != 0 || !strings.Contains(out, "is off") {
		t.Fatalf("off: %d %q %q", code, out, errOut)
	}
	if e.active {
		t.Error("the unit still runs")
	}
	if got := strings.Join(e.fw.calls, ","); got != "DenyPort|5900|tcp" {
		t.Errorf("firewall calls at off: %s", got)
	}
}

// A rule that allowed the port before (an administrator's, another
// user's) is neither added again nor removed; with the firewall off,
// nothing is asked.
func TestRemoteDesktopLeavesOtherRulesAlone(t *testing.T) {
	e := newRDEnv(t, "niri")
	runRD(t, "correct horse\n", "password")
	e.fw.st.Rules = append(e.fw.st.Rules, firewall.Rule{Port: 5900, Proto: "tcp", Note: "VNC for the lab"})
	if code, _, errOut := runRD(t, "", "on"); code != 0 {
		t.Fatalf("on: %s", errOut)
	}
	if code, _, _ := runRD(t, "", "off"); code != 0 {
		t.Fatal("off")
	}
	if len(e.fw.calls) != 0 {
		t.Errorf("the firewall was changed: %v", e.fw.calls)
	}
	e.fw.st = firewall.Status{On: false}
	if code, _, errOut := runRD(t, "", "on"); code != 0 {
		t.Fatalf("on with the firewall off: %s", errOut)
	}
	if len(e.fw.calls) != 0 {
		t.Errorf("the firewall was changed while off: %v", e.fw.calls)
	}
	if st := rdStatusJSON(t); st.Firewall != remotedesktop.FirewallOff {
		t.Errorf("firewall state %q", st.Firewall)
	}
}

// Someone who is not an administrator cannot open the port; nothing starts.
func TestRemoteDesktopNotAdmin(t *testing.T) {
	e := newRDEnv(t, "sway")
	runRD(t, "correct horse\n", "password")
	denied := &rdDeniedFirewall{e.fw}
	rdFirewall = func() (firewallClient, error) { return denied, nil }
	code, _, errOut := runRD(t, "", "on")
	if code == 0 || !strings.Contains(errOut, "only an administrator can turn on remote desktop while the firewall is on") {
		t.Fatalf("on: %d %q", code, errOut)
	}
	if e.active {
		t.Error("the server started anyway")
	}
}

// A server that does not listen is stopped, its port closed, and the
// journal's reason given.
func TestRemoteDesktopServerThatFails(t *testing.T) {
	e := newRDEnv(t, "sway")
	runRD(t, "correct horse\n", "password")
	e.listens = false
	code, _, errOut := runRD(t, "", "on")
	if code == 0 || !strings.Contains(errOut, "Failed to bind socket") {
		t.Fatalf("on: %d %q", code, errOut)
	}
	if e.active {
		t.Error("the unit was left running")
	}
	// The rule it added was taken back (the fake's list does not change, so
	// the closing finds no rule of anna's; add it and close again).
	e.fw.st.Rules = append(e.fw.st.Rules, firewall.Rule{Port: 5900, Proto: "tcp", Note: "Remote desktop (anna)"})
	e.fw.calls = nil
	if code, _, _ := runRD(t, "", "stopped"); code != 0 {
		t.Fatal("stopped failed")
	}
	if got := strings.Join(e.fw.calls, ","); got != "DenyPort|5900|tcp" {
		t.Errorf("stopped: %s", got)
	}
	// The tray's cleanup at login does the same when no server runs, and
	// nothing while one runs.
	e.fw.calls = nil
	e.active = true
	rdCleanup(&bytes.Buffer{})
	if len(e.fw.calls) != 0 {
		t.Errorf("cleanup with a running server: %v", e.fw.calls)
	}
	e.active = false
	rdCleanup(&bytes.Buffer{})
	if got := strings.Join(e.fw.calls, ","); got != "DenyPort|5900|tcp" {
		t.Errorf("cleanup: %s", got)
	}
}

// Where it is not available, it says why and does nothing.
func TestRemoteDesktopAvailability(t *testing.T) {
	for _, c := range []struct {
		session, want string
		ext, live     bool
	}{
		{"cosmic", "COSMIC has no remote desktop server yet", false, false},
		{"i3", "needs a Wayland desktop", false, false},
		{"", "works on the Sway and niri desktops", false, false},
		{"sway", "isotope ext install remote-desktop", true, false},
		{"niri", "not available on the live system", false, true},
	} {
		e := newRDEnv(t, c.session)
		if c.ext {
			os.Remove(rdWayvnc)
		}
		if c.live {
			os.WriteFile(rdLiveMarker, nil, 0o644)
		}
		runRD(t, "correct horse\n", "password")
		code, _, errOut := runRD(t, "", "on")
		if code == 0 || !strings.Contains(errOut, c.want) {
			t.Errorf("%s: on: %d %q", c.session, code, errOut)
		}
		if len(e.fw.calls) != 0 || e.active {
			t.Errorf("%s: something happened: %v", c.session, e.fw.calls)
		}
		st := rdStatusJSON(t)
		if st.Available || !strings.Contains(st.Reason, c.want) || st.NeedsExtension != c.ext {
			t.Errorf("%s: status %+v", c.session, st)
		}
		if code := rdServe(&bytes.Buffer{}); code == 0 {
			t.Errorf("%s: serve ran", c.session)
		}
	}
	for _, flavor := range []string{"agent", "server"} {
		e := newRDEnv(t, "i3")
		rel := filepath.Join(e.dir, "release")
		os.WriteFile(rel, []byte("FLAVOR="+flavor+"\n"), 0o644)
		t.Setenv("ISOTOPE_UPDATE_RELEASE_FILE", rel)
		if _, err := LoadRelease(rel); err != nil {
			// The loader wants every key; the reason then falls back to
			// the session, which still refuses i3.
			continue
		}
		if st := rdStatusJSON(t); st.Available {
			t.Errorf("%s: available", flavor)
		}
	}
}

// serve runs wayvnc with the configuration, the cursor drawn in, its
// control socket under the runtime directory and the installer's keyboard
// layout; it refuses a configuration others can read.
func TestRemoteDesktopServe(t *testing.T) {
	e := newRDEnv(t, "sway")
	runRD(t, "correct horse\n", "password")
	if err := rdWriteConfig(); err != nil {
		t.Fatal(err)
	}
	if code := rdServe(&bytes.Buffer{}); code != 0 {
		t.Fatal("serve failed")
	}
	want := []string{rdWayvnc, "--config=" + rdConfigFile(), "--render-cursor", "--socket=" + filepath.Join(os.Getenv("XDG_RUNTIME_DIR"), "isotope", "wayvncctl")}
	if strings.Join(e.execArgv, " ") != strings.Join(want, " ") {
		t.Errorf("argv %q", e.execArgv)
	}
	env := strings.Join(e.execEnv, "\n")
	if !strings.Contains(env, "XKB_DEFAULT_LAYOUT=de\n") || !strings.Contains(env, "XKB_DEFAULT_VARIANT=nodeadkeys") {
		t.Errorf("no keyboard layout in the environment")
	}
	os.Chmod(rdConfigFile(), 0o644)
	var errOut bytes.Buffer
	if code := rdServe(&errOut); code == 0 || !strings.Contains(errOut.String(), "can be read by others") {
		t.Errorf("serve with a readable configuration: %d %q", code, errOut.String())
	}
	os.Chmod(rdConfigFile(), 0o600)
	t.Setenv("WAYLAND_DISPLAY", "")
	if code := rdServe(&errOut); code == 0 {
		t.Error("serve without a Wayland session")
	}
}

func TestRemoteDesktopUsage(t *testing.T) {
	newRDEnv(t, "sway")
	if code, out, _ := runRD(t, "", "help"); code != 0 || !strings.Contains(out, "isotope remote-desktop") {
		t.Errorf("help: %d", code)
	}
	for _, args := range [][]string{{"nonsense"}, {"on", "x"}, {"password", "--x"}, {"status", "--yaml"}} {
		if code, _, _ := runRD(t, "", args...); code != exitUsage {
			t.Errorf("%v: %d", args, code)
		}
	}
	var out, errOut bytes.Buffer
	run([]string{"help"}, nil, &out, &errOut)
	if !strings.Contains(out.String(), "remote-desktop [command]") {
		t.Error("isotope help does not list remote-desktop")
	}
}

// rdDeniedFirewall answers the status but refuses every change, as polkit
// does for someone who is not in admin.
type rdDeniedFirewall struct{ *fakeFirewallClient }

func (f *rdDeniedFirewall) AllowPort(port uint16, proto, note string) error {
	return dbus.Error{Name: busInterface + ".Error.NotAuthorized", Body: []any{"not authorized"}}
}
