package main

import (
	"errors"
	"image"
	"strings"
	"sync"
	"testing"
	"time"

	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"

	"github.com/kairos-community/isotope/guikit"
	"github.com/kairos-community/isotope/update/isorelease"
)

// fakeDaemon records the calls and answers from its fields.
type fakeDaemon struct {
	mu       sync.Mutex
	st       Status
	tags     []string
	info     InspectInfo
	ext      []Extension
	cat      []CatalogEntry
	backup   BackupStatus
	extErr   error
	calls    []string
	events   chan Event
	forceSet []bool
}

func newFakeDaemon() *fakeDaemon {
	return &fakeDaemon{
		st:     Status{State: StateUpToDate, Installed: "v1.0.0", Latest: "v1.1.0"},
		tags:   []string{"v1.1.0", "v1.0.0", "v0.9.0"},
		info:   InspectInfo{Ref: "quay.io/x/isotope:sway-v1.1.0", Digest: "sha256:abc", Flavor: "sway", Version: "v1.1.0", Trusted: true},
		events: make(chan Event, 16),
	}
}

func (f *fakeDaemon) rec(s string) {
	f.mu.Lock()
	f.calls = append(f.calls, s)
	f.mu.Unlock()
}

func (f *fakeDaemon) called() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

func (f *fakeDaemon) Status() (Status, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.st, nil
}
func (f *fakeDaemon) Check() error                { f.rec("Check"); return nil }
func (f *fakeDaemon) Releases() ([]string, error) { f.rec("Releases"); return f.tags, nil }
func (f *fakeDaemon) Inspect(ref string, adv bool) (InspectInfo, error) {
	f.rec("Inspect " + ref)
	return f.info, nil
}
func (f *fakeDaemon) Apply(ref, digest string, adv bool) error {
	f.rec("Apply " + ref + " " + digest + map[bool]string{true: " override", false: ""}[adv])
	return nil
}
func (f *fakeDaemon) Cancel() error                    { f.rec("Cancel"); return nil }
func (f *fakeDaemon) Reboot() error                    { f.rec("Reboot"); return nil }
func (f *fakeDaemon) RebootPrevious() error            { f.rec("RebootPrevious"); return nil }
func (f *fakeDaemon) Extensions() ([]Extension, error) { return f.ext, nil }
func (f *fakeDaemon) Catalog() ([]CatalogEntry, error) { return f.cat, nil }
func (f *fakeDaemon) InstallExtension(ref, v string, force bool) error {
	f.rec("InstallExtension " + ref)
	f.mu.Lock()
	f.forceSet = append(f.forceSet, force)
	f.mu.Unlock()
	return f.extErr
}
func (f *fakeDaemon) SetExtensionEnabled(n string, on, force bool) error {
	f.rec("SetExtensionEnabled " + n)
	return nil
}
func (f *fakeDaemon) RemoveExtension(n string) error { f.rec("RemoveExtension " + n); return nil }
func (f *fakeDaemon) RecoveryStatus() (RecoveryStatus, error) {
	return RecoveryStatus{Edition: "normal", Containers: []Container{{Label: "COS_PERSISTENT_LUKS", Passphrase: 1}}}, nil
}
func (f *fakeDaemon) BackupStatus() (BackupStatus, error) { return f.backup, nil }
func (f *fakeDaemon) BackupNow() error                    { f.rec("BackupNow"); return nil }
func (f *fakeDaemon) SetBackupSchedule(s string) error {
	f.rec("SetBackupSchedule " + s)
	return nil
}
func (f *fakeDaemon) Watch() (<-chan Event, func(), error) {
	return f.events, func() {}, nil
}

type fakeNet struct {
	mu    sync.Mutex
	st    NetState
	aps   []AccessPoint
	calls []string
}

func (n *fakeNet) rec(s string) { n.mu.Lock(); n.calls = append(n.calls, s); n.mu.Unlock() }
func (n *fakeNet) called() []string {
	n.mu.Lock()
	defer n.mu.Unlock()
	return append([]string(nil), n.calls...)
}
func (n *fakeNet) State() (NetState, error)             { return n.st, nil }
func (n *fakeNet) AccessPoints() ([]AccessPoint, error) { return n.aps, nil }
func (n *fakeNet) Connect(ssid, pw string, hidden bool) error {
	n.rec("Connect " + ssid + " " + pw)
	return nil
}
func (n *fakeNet) Disconnect() error        { n.rec("Disconnect"); return nil }
func (n *fakeNet) Forget(ssid string) error { n.rec("Forget " + ssid); return nil }
func (n *fakeNet) SetWifi(on bool) error    { n.rec("SetWifi"); return nil }

func testSystem(admin bool) System {
	return System{
		Release: isorelease.Release{Flavor: "sway", Version: "v1.0.0", HadronVersion: "v0.6.3",
			ImageRepo: "quay.io/x/isotope", GitHubRepo: "kairos-community/isotope", GPU: isorelease.GPUFull},
		Desktop: "Sway", Admin: admin, Encrypted: true, Kernel: "6.12.0", Hostname: "anna-laptop",
	}
}

func testUI(t *testing.T, admin bool) (*UI, *fakeDaemon, *fakeNet) {
	t.Helper()
	d := newFakeDaemon()
	n := &fakeNet{
		st:  NetState{Connected: true, Name: "Home", HasWifi: true, WifiOn: true, Saved: []string{"Home"}, Devices: []Device{{Iface: "wlan0", Kind: "Wi-Fi", State: "Connected", Connection: "Home", Address: "192.168.1.20"}}},
		aps: []AccessPoint{{SSID: "Home", Strength: 80, Secured: true}, {SSID: "Cafe", Strength: 50}, {SSID: "Office", Strength: 30, Secured: true}},
	}
	u := newUI(testSystem(admin), guikit.NewShaper(), Hooks{Daemon: d, Network: n, Start: func(...string) error { return nil }}, PageUpdates)
	u.setStatus(d.st)
	return u, d, n
}

func gtxOf(w, h int) layout.Context {
	return layout.Context{
		Ops:         new(op.Ops),
		Metric:      unit.Metric{PxPerDp: 1, PxPerSp: 1},
		Constraints: layout.Exact(image.Pt(w, h)),
		Now:         time.Now(),
	}
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func waitQuestion(t *testing.T, u *UI, title string) *dialog {
	t.Helper()
	var d *dialog
	eventually(t, "the question "+title, func() bool {
		d = u.question()
		return d != nil && strings.Contains(d.title, title)
	})
	return d
}

// Every page draws, for an admin and for anyone else, in a large and a
// small window, without the services too.
func TestEveryPageDraws(t *testing.T) {
	for _, admin := range []bool{true, false} {
		u, _, _ := testUI(t, admin)
		for _, p := range Pages {
			u.show(p)
			time.Sleep(20 * time.Millisecond) // let the page's loads land
			for _, sz := range []image.Point{{1100, 720}, {700, 500}} {
				if dims := u.Layout(gtxOf(sz.X, sz.Y)); dims.Size != sz {
					t.Errorf("%s (admin %v) at %v: %v", p, admin, sz, dims.Size)
				}
			}
		}
	}
	bare := newUI(System{ReleaseErr: "no release file"}, guikit.NewShaper(), Hooks{}, PageUpdates)
	go bare.follow()
	for _, p := range Pages {
		bare.show(p)
		bare.Layout(gtxOf(1100, 720))
	}
}

func TestNavListsEveryPageAndMarksTheLaterOnes(t *testing.T) {
	u, _, _ := testUI(t, true)
	if len(u.nav.Items) != len(Pages) {
		t.Fatalf("%d rows for %d pages", len(u.nav.Items), len(Pages))
	}
	for i, p := range Pages {
		if u.nav.Items[i].Later != later[p] {
			t.Errorf("%s: later %v", p, u.nav.Items[i].Later)
		}
	}
	u.ShowPage(PageBackup)
	u.Layout(gtxOf(1100, 720))
	if u.page != PageBackup || u.nav.Current != PageBackup.index() {
		t.Fatalf("ShowPage: on %s", u.page)
	}
}

// The install flow: inspect, the question with the report, Apply with the
// inspected digest, then the restart question when the job ends.
func TestInstallAsksThenAppliesThenOffersTheRestart(t *testing.T) {
	u, d, _ := testUI(t, true)
	u.startInstall("Isotope v1.1.0", "quay.io/x/isotope:sway-v1.1.0", false, "")
	q := waitQuestion(t, u, "Install Isotope v1.1.0?")
	if !strings.Contains(q.body, "Digest: sha256:abc") || !strings.Contains(q.body, "next boot") {
		t.Fatalf("body %q", q.body)
	}
	u.pick(q, 1)
	eventually(t, "Apply", func() bool {
		c := d.called()
		return contains(c, "Apply quay.io/x/isotope:sway-v1.1.0 sha256:abc")
	})
	u.setStatus(Status{State: StateApplying})
	u.setStatus(Status{State: StateRebootPending, Pending: pendingInstalled})
	q = waitQuestion(t, u, "Restart now?")
	u.pick(q, 1)
	eventually(t, "Reboot", func() bool { return contains(d.called(), "Reboot") })
}

func TestInstallOfAFailedJobSaysSo(t *testing.T) {
	u, _, _ := testUI(t, true)
	u.startInstall("Isotope v1.1.0", "ref", false, "")
	u.pick(waitQuestion(t, u, "Install"), 1)
	eventually(t, "the job to start", func() bool { u.mu.Lock(); defer u.mu.Unlock(); return u.inst.active })
	u.setStatus(Status{State: StateInspecting})
	u.setStatus(Status{State: StateError, Detail: "no such image"})
	q := waitQuestion(t, u, "did not install")
	if !strings.Contains(q.body, "no such image") {
		t.Fatalf("body %q", q.body)
	}
}

// An image that is not a release is asked about with its own question,
// only in advanced mode, and its yes is the override.
func TestAdvancedImageAsksTheOverrideQuestion(t *testing.T) {
	u, d, _ := testUI(t, true)
	d.info = InspectInfo{Ref: "docker.io/me/os:1", Digest: "sha256:def", Refusal: "not an Isotope image", Overridable: true}
	u.startInstall("docker.io/me/os:1", "docker.io/me/os:1", false, "")
	q := waitQuestion(t, u, "Cannot install")
	if !strings.Contains(q.body, "not an Isotope image") {
		t.Fatalf("not advanced: %q", q.body)
	}
	u.pick(q, 0)
	u.startInstall("docker.io/me/os:1", "docker.io/me/os:1", true, "")
	q = waitQuestion(t, u, "This is not an Isotope image. Install it anyway?")
	if q.choices[1].Kind != guikit.ButtonDanger {
		t.Fatal("the override is not the danger button")
	}
	u.pick(q, 1)
	eventually(t, "Apply with the override", func() bool {
		c := d.called()
		return contains(c, "Apply docker.io/me/os:1 sha256:def override")
	})
}

func TestSwitchDesktopInstallsTheNewestOfTheOtherFlavor(t *testing.T) {
	u, d, _ := testUI(t, true)
	p := u.view(PageDesktop).(*desktopPage)
	p.switchTo("niri", isorelease.GPUFull, "the niri desktop", switchWarning)
	q := waitQuestion(t, u, "Install the niri desktop (v1.1.0)?")
	if !strings.HasPrefix(q.body, switchWarning) {
		t.Fatalf("the warning is not said first: %q", q.body)
	}
	if c := d.called(); !strings.Contains(strings.Join(c, ","), "Inspect quay.io/x/isotope:niri-v1.1.0") {
		t.Fatalf("calls %q", c)
	}
}

// A conflict asks before the install is repeated with force.
func TestExtensionConflictAsksBeforeForcing(t *testing.T) {
	u, d, _ := testUI(t, true)
	p := u.view(PageExtensions).(*extPage)
	p.change("install gpg", func(force bool) error { return d.InstallExtension("gpg", "", force) })
	eventually(t, "the install", func() bool { return len(d.called()) > 0 })
	u.setStatus(Status{State: StateExtension})
	u.setStatus(Status{State: StateIdle, ExtResult: "conflict", ExtDetail: "gpg ships /usr/lib/libgcrypt.so.20, which differs"})
	q := waitQuestion(t, u, "Replace system files?")
	u.pick(q, 1)
	eventually(t, "the forced install", func() bool {
		d.mu.Lock()
		defer d.mu.Unlock()
		return len(d.forceSet) == 2 && !d.forceSet[0] && d.forceSet[1]
	})
}

func TestBackupNowReportsTheEnd(t *testing.T) {
	u, d, _ := testUI(t, true)
	d.backup = BackupStatus{Available: true, Target: "/run/media/anna/usb", Schedule: "weekly", Present: true}
	p := u.view(PageBackup).(*backupPage)
	p.load()
	eventually(t, "the backup status", func() bool { u.mu.Lock(); defer u.mu.Unlock(); return p.loaded })
	u.mu.Lock()
	p.started = true
	u.mu.Unlock()
	u.setStatus(Status{State: StateBackup})
	u.setStatus(Status{State: StateIdle, BackupResult: "failed", BackupDetail: "restic: disk full"})
	q := waitQuestion(t, u, "The backup failed")
	if q.body != "restic: disk full" {
		t.Fatalf("body %q", q.body)
	}
}

func TestNetworkConnectsAndForgets(t *testing.T) {
	u, _, n := testUI(t, true)
	p := u.view(PageNetwork).(*networkPage)
	p.Shown()
	p.do("Connecting to Cafe…", func(nw Network) error { return nw.Connect("Cafe", "", false) })
	p.do("Forgetting Home…", func(nw Network) error { return nw.Forget("Home") })
	eventually(t, "the calls", func() bool { return len(n.called()) == 2 })
	got := strings.Join(n.called(), ",")
	if !strings.Contains(got, "Connect Cafe ") || !strings.Contains(got, "Forget Home") {
		t.Fatalf("calls %q", got)
	}
}

func TestDescribe(t *testing.T) {
	if got := Describe(&DaemonError{Name: "NotAuthorized", Msg: "x"}); !strings.Contains(got, "admin group") {
		t.Errorf("NotAuthorized: %q", got)
	}
	if got := Describe(errors.New("boom")); got != "boom" {
		t.Errorf("plain: %q", got)
	}
}
