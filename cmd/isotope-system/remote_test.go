package main

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/kairos-community/isotope/pkg/remotedesktop"
)

// fakeRemote is `isotope remote-desktop` for the page.
type fakeRemote struct {
	mu    sync.Mutex
	st    remotedesktop.Status
	err   error // of SetOn
	calls []string
}

func (f *fakeRemote) Status() (remotedesktop.Status, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.st, nil
}

func (f *fakeRemote) SetOn(on bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, map[bool]string{true: "on", false: "off"}[on])
	if f.err != nil {
		return f.err
	}
	f.st.Running = on
	return nil
}

func (f *fakeRemote) SetPassword(pw string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "password "+pw)
	f.st.PasswordSet = true
	return nil
}

func (f *fakeRemote) called() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return strings.Join(f.calls, ",")
}

const rdTestFingerprint = "3F:A1:9C:07:D2:5E:88:41:B0:6C:2D:F3:91:7A:E4:55:0B:C8:63:1F:9E:D7:24:A9:5B:80:E2:36:CF:14:7D:AB"

func rdAvailable(running, pwSet bool) remotedesktop.Status {
	return remotedesktop.Status{Available: true, Running: running, PasswordSet: pwSet, User: "anna", Port: 5900,
		Addresses: []string{"192.168.1.20"}, Fingerprint: rdTestFingerprint, Firewall: map[bool]string{true: remotedesktop.FirewallOpen}[running]}
}

func remoteTestPage(t *testing.T, admin bool, st remotedesktop.Status) (*UI, *networkPage, *fakeRemote) {
	t.Helper()
	u, _, _ := testUI(t, admin)
	f := &fakeRemote{st: st}
	u.hooks.RemoteDesktop = f
	p := u.view(PageNetwork).(*networkPage)
	p.Shown()
	eventually(t, "the remote desktop state", func() bool { u.mu.Lock(); defer u.mu.Unlock(); return p.rdLoaded })
	return u, p, f
}

func TestRemoteViewOf(t *testing.T) {
	v := remoteViewOf(rdInput{Live: true})
	if !strings.Contains(v.Sub, "live system") || v.Toggle != "" || v.Password != "" {
		t.Errorf("live: %+v", v)
	}
	v = remoteViewOf(rdInput{Loaded: true, St: remotedesktop.Status{Reason: "COSMIC has no remote desktop server yet."}})
	if v.Sub != "COSMIC has no remote desktop server yet." || v.Toggle != "" || v.Extension {
		t.Errorf("cosmic: %+v", v)
	}
	v = remoteViewOf(rdInput{Loaded: true, St: remotedesktop.Status{Reason: "x", NeedsExtension: true}})
	if !v.Extension || v.Toggle != "" || !strings.Contains(v.Sub, "Extensions page") {
		t.Errorf("no extension: %+v", v)
	}
	v = remoteViewOf(rdInput{Loaded: true, Admin: true, St: rdAvailable(false, false)})
	if v.Toggle != "Turn on" || !v.ToggleNeedPw || v.Password != "Set password…" || len(v.Rows) != 0 || strings.Contains(v.Hint, "administrator") {
		t.Errorf("off: %+v", v)
	}
	v = remoteViewOf(rdInput{Loaded: true, St: rdAvailable(false, true)})
	if v.ToggleNeedPw || v.Password != "Change password…" || !strings.Contains(v.Hint, "only an administrator") {
		t.Errorf("off, not admin: %+v", v)
	}
	v = remoteViewOf(rdInput{Loaded: true, St: rdAvailable(true, true)})
	if v.Toggle != "Turn off" || len(v.Rows) != 3 || v.Rows[0][1] != "192.168.1.20:5900" || !strings.HasPrefix(v.Rows[1][1], "anna") || v.Warn != "" {
		t.Errorf("on: %+v", v)
	}
	st := rdAvailable(true, true)
	st.Firewall = remotedesktop.FirewallClosed
	if v := remoteViewOf(rdInput{Loaded: true, St: st}); !strings.Contains(v.Warn, "firewall blocks port 5900/tcp") {
		t.Errorf("closed: %+v", v)
	}
}

// Turn on without a password asks for one first, and saving it turns the
// server on; Turn off stops it; Open Extensions goes to that page.
func TestRemoteSectionWiring(t *testing.T) {
	u, p, f := remoteTestPage(t, true, rdAvailable(false, false))
	u.Layout(gtxOf(1100, 720))
	clickAndUpdate(p, &p.rdk.toggle)
	d := waitQuestion(t, u, "Remote desktop password")
	p.rdPw.Editor.SetText("short")
	p.rdPw2.Editor.SetText("short")
	u.Layout(gtxOf(1100, 720))
	if d.choices[1].enabled() {
		t.Error("a short password can be saved")
	}
	p.rdPw.Editor.SetText("correct horse")
	p.rdPw2.Editor.SetText("correct horsf")
	if d.choices[1].enabled() {
		t.Error("two different passwords can be saved")
	}
	p.rdPw2.Editor.SetText("correct horse")
	if !d.choices[1].enabled() || d.choices[1].Label != "Save and turn on" {
		t.Fatalf("save: %+v", d.choices[1])
	}
	u.pick(d, 1)
	eventually(t, "password then on", func() bool { return f.called() == "password correct horse,on" })
	eventually(t, "the state", func() bool { u.mu.Lock(); defer u.mu.Unlock(); return p.rd.Running && p.working == "" })
	u.Layout(gtxOf(1100, 720))
	clickAndUpdate(p, &p.rdk.toggle)
	eventually(t, "off", func() bool { return strings.HasSuffix(f.called(), ",off") })

	u, p, _ = remoteTestPage(t, true, remotedesktop.Status{Reason: "needs it", NeedsExtension: true})
	u.Layout(gtxOf(1100, 720))
	clickAndUpdate(p, &p.rdk.extensions)
	if u.page != PageExtensions {
		t.Errorf("Open Extensions went to %s", u.page)
	}
}

// A refusal from the CLI (not an administrator) is told, and nothing runs.
func TestRemoteSectionTellsARefusal(t *testing.T) {
	u, p, f := remoteTestPage(t, false, rdAvailable(false, true))
	f.err = errString("only an administrator can turn on remote desktop while the firewall is on: it allows port 5900/tcp")
	u.Layout(gtxOf(1100, 720))
	clickAndUpdate(p, &p.rdk.toggle)
	d := waitQuestion(t, u, "Remote desktop")
	if !strings.Contains(d.body, "only an administrator") {
		t.Errorf("told %q", d.body)
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	if p.rd.Running {
		t.Error("running after a refusal")
	}
}

type errString string

func (e errString) Error() string { return string(e) }

// The page and the real CLI agree on the JSON and on the password's way in.
func TestRemoteCLIRoundTrip(t *testing.T) {
	c := remoteCLI{bin: buildIsotope(t)}
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	st, err := c.Status()
	if err != nil {
		t.Fatal(err)
	}
	// No session file on a build machine: not available, and why.
	if st.Available || st.Reason == "" || st.Port != remotedesktop.Port || st.PasswordSet {
		t.Errorf("status %+v", st)
	}
	if err := c.SetPassword("bad"); err == nil || !strings.Contains(err.Error(), "at least 8") {
		t.Errorf("a short password: %v", err)
	}
	if err := c.SetPassword("correct horse"); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "isotope", "remote-desktop", "password"))
	if err != nil || string(b) != "correct horse\n" {
		t.Errorf("password file %q %v", b, err)
	}
	if st, _ := c.Status(); !st.PasswordSet {
		t.Error("the status does not see the password")
	}
	if err := c.SetOn(true); err == nil {
		t.Error("on worked where it is not available")
	}
}
