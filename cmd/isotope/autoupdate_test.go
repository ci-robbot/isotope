package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"
	"gopkg.in/yaml.v3"
)

// autoFiles points the setting, the last run and the boot markers into a
// temporary directory.
func autoFiles(t *testing.T) (dir string) {
	t.Helper()
	dir = t.TempDir()
	oldOEM, oldRun, oldMarkers, oldUID := autoOEM, autoRunFile, autoBootMarkers, autoRunnerUID.Load()
	oldPassive, oldRecovery, oldHold := autoPassiveMarker, autoRecoveryMarker, autoHoldFile
	autoOEM = filepath.Join(dir, "oem", "96_isotope_autoupdate.yaml")
	autoRunFile = filepath.Join(dir, "usr-local", "autoupdate")
	autoHoldFile = filepath.Join(dir, "usr-local", "autoupdate-hold")
	autoBootMarkers = []string{filepath.Join(dir, "live_mode"), filepath.Join(dir, "install_mode"), filepath.Join(dir, "uki_install_mode")}
	autoPassiveMarker, autoRecoveryMarker = filepath.Join(dir, "passive_mode"), filepath.Join(dir, "recovery_mode")
	t.Cleanup(func() {
		autoOEM, autoRunFile, autoBootMarkers = oldOEM, oldRun, oldMarkers
		autoPassiveMarker, autoRecoveryMarker, autoHoldFile = oldPassive, oldRecovery, oldHold
		autoRunnerUID.Store(oldUID)
	})
	return dir
}

// fakeAutoEnv is NetworkManager and UPower.
type fakeAutoEnv struct {
	mu         sync.Mutex
	metered    bool
	meteredErr error
	onBattery  bool
	percent    float64
	batteryErr error
	asked      int
}

func (f *fakeAutoEnv) Metered() (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.asked++
	return f.metered, f.meteredErr
}

func (f *fakeAutoEnv) Battery() (bool, float64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.onBattery, f.percent, f.batteryErr
}

var autoClock = time.Date(2026, 11, 24, 3, 17, 0, 0, time.UTC)

// autoDaemon is a daemon on installed with automatic updates set to on,
// the fake environment and a fixed clock.
func autoDaemon(t *testing.T, installed string, on bool, env *fakeAutoEnv, b *fakeBackend) (*Daemon, daemonDigests) {
	t.Helper()
	dg := daemonEnv(t, installed)
	autoFiles(t)
	if on {
		if err := setAutoUpdate(true); err != nil {
			t.Fatal(err)
		}
	}
	d := NewDaemon(b)
	if env != nil {
		d.autoEnv = env
	}
	d.now = func() time.Time { return autoClock }
	return d, dg
}

func lastRun(t *testing.T) autoRun {
	t.Helper()
	r, ok := readAutoRun()
	if !ok {
		t.Fatal("no last run recorded")
	}
	return r
}

func TestAutoUpdateSettingIsOffByDefault(t *testing.T) {
	autoFiles(t)
	if on, err := loadAutoUpdate(); on || err != nil {
		t.Fatalf("no file: %v %v, want off", on, err)
	}
	if err := setAutoUpdate(true); err != nil {
		t.Fatal(err)
	}
	if on, err := loadAutoUpdate(); !on || err != nil {
		t.Fatalf("after on: %v %v", on, err)
	}
	// The file is a cloud-config yip can read: a name, and a key it
	// ignores; no stage runs anything.
	b, err := os.ReadFile(autoOEM)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := yaml.Unmarshal(b, &doc); err != nil {
		t.Fatalf("%s does not parse: %v", b, err)
	}
	if doc["name"] != "Isotope automatic updates" || doc["stages"] != nil {
		t.Fatalf("file %s", b)
	}
	if err := setAutoUpdate(false); err != nil {
		t.Fatal(err)
	}
	if on, _ := loadAutoUpdate(); on {
		t.Fatal("still on after off")
	}
}

func TestAutoUpdateBrokenSettingIsOff(t *testing.T) {
	autoFiles(t)
	for _, body := range []string{"isotope_autoupdate: [\n", "name: x\n", "isotope_autoupdate:\n  enabled: maybe\n"} {
		if err := os.MkdirAll(filepath.Dir(autoOEM), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(autoOEM, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		if on, err := loadAutoUpdate(); on || err == nil {
			t.Errorf("%q: %v %v, want off with an error", body, on, err)
		}
	}
}

func TestAutoUpdateOffDoesNothing(t *testing.T) {
	env := &fakeAutoEnv{}
	b := &fakeBackend{}
	d, _ := autoDaemon(t, "v0.0.5", false, env, b)
	o := d.AutoUpdate(context.Background())
	if o.Result != autoOff {
		t.Fatalf("outcome %+v", o)
	}
	if len(b.calls()) != 0 || env.asked != 0 {
		t.Fatal("an off setting asked or installed something")
	}
	if _, ok := readAutoRun(); ok {
		t.Fatal("an off run was recorded")
	}
	if s := d.Status(); s.State != StateIdle {
		t.Fatalf("state %s: an off run must not even check", s.State)
	}
}

func TestAutoUpdateInstallsTheLatestOfItsFlavorAndLine(t *testing.T) {
	b := &fakeBackend{lines: []string{"done"}}
	d, dg := autoDaemon(t, "v0.0.5", true, &fakeAutoEnv{}, b)
	o := d.AutoUpdate(context.Background())
	if o.Result != autoStarted || o.Version != "v0.0.6" {
		t.Fatalf("outcome %+v", o)
	}
	s := waitState(t, d, StateRebootPending)
	want := "oci:" + ghcrRepo + "@" + dg[ghcrRepo+":sway-v0.0.6"]
	if got := b.calls(); len(got) != 1 || got[0] != want {
		t.Fatalf("sources %q, want [%q]", got, want)
	}
	if s.Staged != "v0.0.6" || !s.StagedAuto || s.Pending != pendingInstalled {
		t.Fatalf("status %+v", s)
	}
	d.wg.Wait()
	r := lastRun(t)
	if r.Result != autoInstalled || r.Version != "v0.0.6" || r.Time != autoClock.Unix() {
		t.Fatalf("last run %+v", r)
	}
	// The daemon exits when idle: the next one still knows what is staged.
	d2 := NewDaemon(b)
	if s := d2.Status(); s.Staged != "v0.0.6" || !s.StagedAuto {
		t.Fatalf("restarted daemon %+v", s)
	}
	// The next run waits for the restart.
	o = d2.AutoUpdate(context.Background())
	if o.Result != autoSkipped || !strings.Contains(o.Detail, "waits for a restart") || len(b.calls()) != 1 {
		t.Fatalf("second run %+v, %d upgrades", o, len(b.calls()))
	}
}

func TestAutoUpdateStaysOnTheFlavor(t *testing.T) {
	// The newest release is the same for every flavor; niri installs niri.
	b := &fakeBackend{}
	dg := daemonEnv(t, "v0.0.5")
	autoFiles(t)
	t.Setenv("ISOTOPE_UPDATE_RELEASE_FILE", writeRelease(t, strings.Replace(strings.Replace(goodRelease, `"v0.1.0"`, "v0.0.5", 1), "FLAVOR=sway", "FLAVOR=niri", 1)))
	if err := setAutoUpdate(true); err != nil {
		t.Fatal(err)
	}
	d := NewDaemon(b)
	if o := d.AutoUpdate(context.Background()); o.Result != autoStarted {
		t.Fatalf("outcome %+v", o)
	}
	waitState(t, d, StateRebootPending)
	if got := b.calls(); len(got) != 1 || got[0] != "oci:"+ghcrRepo+"@"+dg[ghcrRepo+":niri-v0.0.6"] {
		t.Fatalf("sources %q, want the niri image", got)
	}
}

func TestAutoUpdateRefusesWhatInstallLatestWouldNot(t *testing.T) {
	rel := Release{Flavor: "sway", Version: "v0.0.5", GPU: gpuFull}
	for _, c := range []struct {
		name string
		v    Verdict
		want string
	}{
		{"not a release", Verdict{Flavor: "sway", Version: "v0.0.6", GPU: gpuFull}, "not a published"},
		{"another flavor", Verdict{Flavor: "niri", Version: "v0.0.6", GPU: gpuFull, Trusted: true}, "niri flavor"},
		{"another line", Verdict{Flavor: "sway", Version: "v0.0.6", GPU: gpuVM, Trusted: true}, "vm line"},
		{"not the newest", Verdict{Flavor: "sway", Version: "v0.0.4", GPU: gpuFull, Trusted: true}, "not the newest"},
	} {
		err := autoRefusal(rel, c.v, "v0.0.6")
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want %q", c.name, err, c.want)
		}
	}
	if err := autoRefusal(Release{Flavor: "sway", Version: "v0.0.7", GPU: gpuFull}, Verdict{Flavor: "sway", Version: "v0.0.6", GPU: gpuFull, Trusted: true}, "v0.0.6"); err == nil {
		t.Error("a downgrade was allowed")
	}
	if err := autoRefusal(rel, Verdict{Flavor: "sway", Version: "v0.0.6", GPU: gpuFull, Trusted: true}, "v0.0.6"); err != nil {
		t.Errorf("the newest release of this flavor and line: %v", err)
	}
}

func TestAutoUpdateNeverDowngrades(t *testing.T) {
	// Installed v0.0.7 is newer than every published release.
	b := &fakeBackend{}
	d, _ := autoDaemon(t, "v0.0.7", true, &fakeAutoEnv{}, b)
	o := d.AutoUpdate(context.Background())
	if o.Result != autoUpToDate || len(b.calls()) != 0 {
		t.Fatalf("outcome %+v, upgrades %q", o, b.calls())
	}
	if r := lastRun(t); r.Result != autoUpToDate {
		t.Fatalf("last run %+v", r)
	}
}

func TestAutoUpdateSkips(t *testing.T) {
	for _, c := range []struct {
		name  string
		env   *fakeAutoEnv
		setup func(t *testing.T, d *Daemon, dir string)
		want  string
	}{
		{"metered", &fakeAutoEnv{metered: true}, nil, "metered"},
		{"metered unknown", &fakeAutoEnv{meteredErr: errors.New("NetworkManager timed out")}, nil, "metered"},
		{"low battery", &fakeAutoEnv{onBattery: true, percent: 29.5}, nil, "battery is at 30%"},
		{"battery unreadable", &fakeAutoEnv{onBattery: true, batteryErr: errors.New("no display device")}, nil, "battery"},
		{"pinned", &fakeAutoEnv{}, func(t *testing.T, d *Daemon, _ string) {
			if err := SavePin(ghcrRepo + ":sway-v0.0.5"); err != nil {
				t.Fatal(err)
			}
		}, "pinned"},
		{"another job", &fakeAutoEnv{}, func(t *testing.T, d *Daemon, _ string) {
			d.mu.Lock()
			d.state = StateBackup
			d.mu.Unlock()
		}, "another job"},
		{"a failed install waits for a restart", &fakeAutoEnv{}, func(t *testing.T, d *Daemon, _ string) {
			d.mu.Lock()
			d.state, d.pending = StateRebootPending, pendingFailed
			d.mu.Unlock()
		}, "did not finish"},
		{"rebooting", &fakeAutoEnv{}, func(t *testing.T, d *Daemon, _ string) {
			d.mu.Lock()
			d.rebooting = true
			d.mu.Unlock()
		}, "restarting"},
		{"live boot", &fakeAutoEnv{}, func(t *testing.T, d *Daemon, dir string) {
			if err := os.WriteFile(filepath.Join(dir, "live_mode"), nil, 0o644); err != nil {
				t.Fatal(err)
			}
		}, "live_mode"},
		{"Trusted Boot medium", &fakeAutoEnv{}, func(t *testing.T, d *Daemon, dir string) {
			if err := os.WriteFile(filepath.Join(dir, "uki_install_mode"), nil, 0o644); err != nil {
				t.Fatal(err)
			}
		}, "uki_install_mode"},
	} {
		t.Run(c.name, func(t *testing.T) {
			b := &fakeBackend{}
			d, _ := autoDaemon(t, "v0.0.5", true, c.env, b)
			if c.setup != nil {
				c.setup(t, d, filepath.Dir(autoBootMarkers[0]))
			}
			o := d.AutoUpdate(context.Background())
			if o.Result != autoSkipped || !strings.Contains(o.Detail, c.want) {
				t.Fatalf("outcome %+v, want skipped with %q", o, c.want)
			}
			d.wg.Wait()
			if len(b.calls()) != 0 {
				t.Fatalf("installed %q", b.calls())
			}
			if r := lastRun(t); r.Result != autoSkipped || r.Detail != o.Detail || r.Time != autoClock.Unix() {
				t.Fatalf("last run %+v", r)
			}
		})
	}
}

func touch(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestAutoUpdateSkipsAPassiveBootAndHoldsWhatItFellBackFrom(t *testing.T) {
	b := &fakeBackend{}
	d, _ := autoDaemon(t, "v0.0.5", true, &fakeAutoEnv{}, b)
	// v0.0.6 was installed, did not boot (or the user rolled back without
	// the daemon): the boot loader fell back to v0.0.5 in the passive slot.
	recordHistory(historyEntry{Kind: "install", Version: "v0.0.6", Result: "installed"})
	touch(t, autoPassiveMarker)
	o := d.AutoUpdate(context.Background())
	if o.Result != autoSkipped || !strings.Contains(o.Detail, "booted into the previous version (rollback)") {
		t.Fatalf("outcome %+v", o)
	}
	if r := lastRun(t); r.Result != autoSkipped || !strings.Contains(r.Detail, "rollback") {
		t.Fatalf("last run %+v", r)
	}
	if len(b.calls()) != 0 {
		t.Fatalf("installed %q", b.calls())
	}
	if h := heldVersion(); h != "v0.0.6" {
		t.Fatalf("held %q, want the release it fell back from", h)
	}
	// Back on the active slot with v0.0.5 (reinstalled by hand, say), the
	// held v0.0.6 is still not installed.
	if err := os.Remove(autoPassiveMarker); err != nil {
		t.Fatal(err)
	}
	o = d.AutoUpdate(context.Background())
	if o.Result != autoSkipped || !strings.Contains(o.Detail, "rolled back from Isotope v0.0.6") || len(b.calls()) != 0 {
		t.Fatalf("outcome %+v, upgrades %q", o, b.calls())
	}
}

func TestAutoUpdateSkipsTheRecoverySystem(t *testing.T) {
	b := &fakeBackend{}
	d, _ := autoDaemon(t, "v0.0.5", true, &fakeAutoEnv{}, b)
	touch(t, autoRecoveryMarker)
	o := d.AutoUpdate(context.Background())
	if o.Result != autoSkipped || !strings.Contains(o.Detail, "recovery system") || len(b.calls()) != 0 {
		t.Fatalf("outcome %+v, upgrades %q", o, b.calls())
	}
	if heldVersion() != "" {
		t.Fatal("a recovery boot held a release")
	}
}

func TestAutoUpdateInstallsAReleaseNewerThanTheHeldOne(t *testing.T) {
	b := &fakeBackend{}
	d, dg := autoDaemon(t, "v0.0.4", true, &fakeAutoEnv{}, b)
	holdVersion("v0.0.5")
	if o := d.AutoUpdate(context.Background()); o.Result != autoStarted || o.Version != "v0.0.6" {
		t.Fatalf("outcome %+v", o)
	}
	waitState(t, d, StateRebootPending)
	d.wg.Wait()
	if got := b.calls(); len(got) != 1 || got[0] != "oci:"+ghcrRepo+"@"+dg[ghcrRepo+":sway-v0.0.6"] {
		t.Fatalf("sources %q", got)
	}
}

func TestRollbackHoldsTheRunningRelease(t *testing.T) {
	b := &fakeBackend{}
	d, dg := autoDaemon(t, "v0.0.6", true, nil, b)
	if err := d.RebootPrevious(); err != nil {
		t.Fatal(err)
	}
	if h := heldVersion(); h != "v0.0.6" {
		t.Fatalf("held %q after a rollback from v0.0.6", h)
	}
	// Installing it again by hand lifts the hold.
	d2 := NewDaemon(b)
	ref := ghcrRepo + ":sway-v0.0.6"
	if err := d2.Apply(context.Background(), ref, dg[ref], false); err != nil {
		t.Fatal(err)
	}
	waitState(t, d2, StateRebootPending)
	d2.wg.Wait()
	if h := heldVersion(); h != "" {
		t.Fatalf("still held %q after a manual install", h)
	}
}

func TestAutoUpdateGoesOnBatteryAboveTheFloorOrOnAC(t *testing.T) {
	for _, env := range []*fakeAutoEnv{{onBattery: true, percent: 30}, {onBattery: false, percent: 5}} {
		b := &fakeBackend{}
		d, _ := autoDaemon(t, "v0.0.5", true, env, b)
		if o := d.AutoUpdate(context.Background()); o.Result != autoStarted {
			t.Fatalf("battery %v %v: outcome %+v", env.onBattery, env.percent, o)
		}
		waitState(t, d, StateRebootPending)
		d.wg.Wait()
	}
}

func TestAutoUpdateWithoutAnEnvironmentGoesAhead(t *testing.T) {
	// No NetworkManager or UPower answer (the daemon's env is nil): as on
	// a server without UPower, nothing says to wait.
	b := &fakeBackend{}
	d, _ := autoDaemon(t, "v0.0.5", true, nil, b)
	if o := d.AutoUpdate(context.Background()); o.Result != autoStarted {
		t.Fatalf("outcome %+v", o)
	}
	waitState(t, d, StateRebootPending)
	d.wg.Wait()
}

func TestAutoUpdateRecordsAFailedInstallOnce(t *testing.T) {
	b := &fakeBackend{err: errors.New("disk full")}
	d, _ := autoDaemon(t, "v0.0.5", true, &fakeAutoEnv{}, b)
	if o := d.AutoUpdate(context.Background()); o.Result != autoStarted {
		t.Fatalf("outcome %+v", o)
	}
	s := waitState(t, d, StateRebootPending)
	d.wg.Wait()
	if s.Staged != "" || s.StagedAuto {
		t.Fatalf("a failed install is staged: %+v", s)
	}
	r := lastRun(t)
	if r.Result != autoFailed || r.Version != "v0.0.6" || !strings.Contains(r.Detail, "disk full") {
		t.Fatalf("last run %+v", r)
	}
	// The next day's run does not install over it: the slots are unknown
	// until a restart.
	if o := d.AutoUpdate(context.Background()); o.Result != autoSkipped || len(b.calls()) != 1 {
		t.Fatalf("retry %+v, %d upgrades", o, len(b.calls()))
	}
}

func TestAutoUpdateRecordsAFailureBeforeTheWrite(t *testing.T) {
	// The release list says v0.0.6, but the registry has no i3 image.
	b := &fakeBackend{}
	daemonEnv(t, "v0.0.5")
	autoFiles(t)
	t.Setenv("ISOTOPE_UPDATE_RELEASE_FILE", writeRelease(t, strings.Replace(strings.Replace(goodRelease, `"v0.1.0"`, "v0.0.5", 1), "FLAVOR=sway", "FLAVOR=i3", 1)))
	if err := setAutoUpdate(true); err != nil {
		t.Fatal(err)
	}
	d := NewDaemon(b)
	o := d.AutoUpdate(context.Background())
	if o.Result != autoFailed || len(b.calls()) != 0 {
		t.Fatalf("outcome %+v, upgrades %q", o, b.calls())
	}
	if r := lastRun(t); r.Result != autoFailed || !strings.Contains(r.Detail, "v0.0.6 was not installed") {
		t.Fatalf("last run %+v", r)
	}
}

func TestManualInstallIsStagedButNotAuto(t *testing.T) {
	b := &fakeBackend{}
	d, dg := autoDaemon(t, "v0.0.5", false, nil, b)
	ref := ghcrRepo + ":sway-v0.0.6"
	if err := d.Apply(context.Background(), ref, dg[ref], false); err != nil {
		t.Fatal(err)
	}
	s := waitState(t, d, StateRebootPending)
	if s.Staged != "v0.0.6" || s.StagedAuto {
		t.Fatalf("status %+v", s)
	}
	d.wg.Wait()
	if _, ok := readAutoRun(); ok {
		t.Fatal("a manual install was recorded as an automatic run")
	}
}

func TestAutoRunFileHoldsOneLine(t *testing.T) {
	autoFiles(t)
	r := autoRun{Time: 42, autoOutcome: autoOutcome{Result: autoFailed, Version: "v1.0.0", Detail: "a\tb\nc"}}
	if err := writeAutoRun(r); err != nil {
		t.Fatal(err)
	}
	got, ok := readAutoRun()
	if !ok || got.Detail != "a b c" || got.Time != 42 || got.Version != "v1.0.0" {
		t.Fatalf("read %+v %v", got, ok)
	}
}

// The bus.

func TestBusAutoUpdateSettingNeedsTheSettingsAction(t *testing.T) {
	d, _ := autoDaemon(t, "v0.0.5", false, nil, &fakeBackend{})
	auth := &fakeAuth{allow: false}
	f := startService(t, d, auth)
	if c := f.call("SetAutoUpdate", true); errName(c.Err) != busInterface+".Error.NotAuthorized" {
		t.Fatalf("got %v, want NotAuthorized", c.Err)
	}
	if on, _ := loadAutoUpdate(); on {
		t.Fatal("turned on without the action")
	}
	if q := auth.questions(); len(q) != 1 || !strings.HasSuffix(q[0], " "+actionSettings) {
		t.Fatalf("asked %q, want the settings action", q)
	}
	auth.mu.Lock()
	auth.allow = true
	auth.mu.Unlock()
	if c := f.call("SetAutoUpdate", true); c.Err != nil {
		t.Fatal(c.Err)
	}
	if on, _ := loadAutoUpdate(); !on {
		t.Fatal("not on")
	}
	var m map[string]dbus.Variant
	if err := f.call("AutoUpdateStatus").Store(&m); err != nil {
		t.Fatal(err)
	}
	if st := autoStatusFromDict(m); !st.Enabled || st.LastTime != 0 || st.Staged != "" {
		t.Fatalf("status %+v", st)
	}
}

func TestBusRunAutoUpdateIsForRootOnly(t *testing.T) {
	d, _ := autoDaemon(t, "v0.0.5", false, nil, &fakeBackend{})
	// The test's own uid stands in for root; any other uid is refused.
	autoRunnerUID.Store(uint32(os.Getuid()) + 1)
	f := startService(t, d, &fakeAuth{allow: true})
	if c := f.call("RunAutoUpdate"); errName(c.Err) != busInterface+".Error.NotAuthorized" {
		t.Fatalf("got %v, want NotAuthorized", c.Err)
	}
	autoRunnerUID.Store(uint32(os.Getuid()))
	var result, version, detail string
	if err := f.call("RunAutoUpdate").Store(&result, &version, &detail); err != nil {
		t.Fatal(err)
	}
	if result != autoOff {
		t.Fatalf("result %q %q", result, detail)
	}
}

func TestBusStatusCarriesTheStagedVersion(t *testing.T) {
	m := statusDict(Status{State: StateRebootPending, Pending: pendingInstalled, Staged: "v0.0.6", StagedAuto: true})
	if st := statusFromDict(m); st.Staged != "v0.0.6" || !st.StagedAuto {
		t.Fatalf("round trip %+v", st)
	}
}

// The CLI.

type fakeAutoClient struct {
	st     AutoStatus
	setErr error
	calls  []string
	result [3]string
}

func (f *fakeAutoClient) AutoUpdateStatus() (AutoStatus, error) {
	f.calls = append(f.calls, "status")
	return f.st, nil
}
func (f *fakeAutoClient) SetAutoUpdate(on bool) error {
	f.calls = append(f.calls, map[bool]string{true: "on", false: "off"}[on])
	return f.setErr
}
func (f *fakeAutoClient) RunAutoUpdate() (string, string, string, error) {
	f.calls = append(f.calls, "run")
	return f.result[0], f.result[1], f.result[2], nil
}
func (f *fakeAutoClient) Close() {}

func withAutoClient(t *testing.T, f *fakeAutoClient) {
	t.Helper()
	old := dialAuto
	dialAuto = func() (autoClient, error) {
		if f == nil {
			t.Fatal("dialled the service")
		}
		return f, nil
	}
	t.Cleanup(func() { dialAuto = old })
}

func TestCLIAutoOnOffStatus(t *testing.T) {
	t.Setenv("ISOTOPE_UPDATE_RELEASE_FILE", writeRelease(t, goodRelease))
	f := &fakeAutoClient{st: AutoStatus{Enabled: true, LastTime: autoClock.Unix(), LastResult: autoSkipped,
		LastDetail: "Not now: the connection is metered.", Staged: "v0.2.0", StagedAuto: true}}
	withAutoClient(t, f)
	var out, errb bytes.Buffer
	if code := run([]string{"update", "auto", "on"}, nil, &out, &errb); code != exitOK || !strings.Contains(out.String(), "on") {
		t.Fatalf("on: %d %q %q", code, out.String(), errb.String())
	}
	out.Reset()
	if code := run([]string{"update", "auto", "off"}, nil, &out, &errb); code != exitOK || !strings.Contains(out.String(), "off") {
		t.Fatalf("off: %d %q", code, out.String())
	}
	out.Reset()
	if code := run([]string{"update", "auto"}, nil, &out, &errb); code != exitOK {
		t.Fatalf("status: %d %q", code, errb.String())
	}
	for _, w := range []string{"Automatic updates: on", "skipped. Not now: the connection is metered.", "Isotope v0.2.0 is installed and starts at the next restart"} {
		if !strings.Contains(out.String(), w) {
			t.Errorf("status %q lacks %q", out.String(), w)
		}
	}
	if got := strings.Join(f.calls, ","); got != "on,off,status" {
		t.Fatalf("calls %s", got)
	}
	if code := run([]string{"update", "auto", "maybe"}, nil, &out, &errb); code != exitUsage {
		t.Fatalf("bad subcommand: %d", code)
	}
	f.setErr = &daemonError{Name: "NotAuthorized", Msg: "no"}
	errb.Reset()
	if code := run([]string{"upgrade", "auto", "on"}, nil, &out, &errb); code != exitError || !strings.Contains(errb.String(), "admin group") {
		t.Fatalf("refused: %d %q", code, errb.String())
	}
}

func TestAutoStatusLines(t *testing.T) {
	never := autoStatusLines(AutoStatus{}, autoClock)
	if strings.Join(never, "\n") != "Automatic updates: off\nLast run:          never" {
		t.Fatalf("%q", never)
	}
	broken := autoStatusLines(AutoStatus{Broken: "yaml: bad"}, autoClock)
	if !strings.Contains(broken[0], "cannot be read") {
		t.Fatalf("%q", broken)
	}
	inst := autoStatusLines(AutoStatus{Enabled: true, LastTime: autoClock.Unix(), LastResult: autoInstalled, LastVersion: "v0.2.0"}, autoClock)
	if inst[1] != "Last run:          2026-11-24 03:17, installed Isotope v0.2.0" {
		t.Fatalf("%q", inst)
	}
}

func TestCLIAutoRun(t *testing.T) {
	autoFiles(t)
	t.Setenv("ISOTOPE_UPDATE_RELEASE_FILE", writeRelease(t, goodRelease))
	oldRoot := needRoot
	t.Cleanup(func() { needRoot = oldRoot })
	needRoot = func() bool { return false }
	var out, errb bytes.Buffer

	// Off: nothing, not even the service.
	withAutoClient(t, nil)
	if code := run([]string{"update", "--auto"}, nil, &out, &errb); code != exitOK || !strings.Contains(out.String(), "off") {
		t.Fatalf("off: %d %q %q", code, out.String(), errb.String())
	}

	if err := setAutoUpdate(true); err != nil {
		t.Fatal(err)
	}
	f := &fakeAutoClient{result: [3]string{autoStarted, "v0.2.0", "Installing Isotope v0.2.0."}}
	withAutoClient(t, f)
	out.Reset()
	if code := run([]string{"update", "--auto"}, nil, &out, &errb); code != exitOK || !strings.Contains(out.String(), "v0.2.0") {
		t.Fatalf("started: %d %q", code, out.String())
	}
	f.result = [3]string{autoFailed, "v0.2.0", "Could not check for updates: offline"}
	errb.Reset()
	if code := run([]string{"update", "--auto"}, nil, &out, &errb); code != exitError || !strings.Contains(errb.String(), "offline") {
		t.Fatalf("failed: %d %q", code, errb.String())
	}
	if code := run([]string{"update", "--auto", "--yes"}, nil, &out, &errb); code != exitUsage {
		t.Fatalf("--auto with another flag: %d", code)
	}
	// Not as root.
	needRoot = func() bool { return true }
	if code := run([]string{"update", "--auto"}, nil, &out, &errb); code != exitError {
		t.Fatalf("not root: %d", code)
	}
}

// The tray.

func TestTrayStatusLineSaysRestartToFinish(t *testing.T) {
	st := Status{State: StateRebootPending, Pending: pendingInstalled, Staged: "v0.2.0"}
	if got := statusLine(st); got != "Isotope v0.2.0 is installed: restart to finish" {
		t.Fatalf("%q", got)
	}
	st.Staged = ""
	if got := statusLine(st); got != "Update installed; restart to use it" {
		t.Fatalf("%q", got)
	}
}

func TestTrayAnnouncesAStagedAutoUpdateOncePerVersion(t *testing.T) {
	staged := Status{State: StateRebootPending, Pending: pendingInstalled, Installed: "v0.1.0", Staged: "v0.2.0", StagedAuto: true}
	f := &trayFake{st: staged}
	h := startTray(t, dialFake(f), time.Hour, time.Hour)
	h.ui.waitView(t, "restart to finish", func(v trayView) bool {
		it, ok := find(v.Menu, "status")
		return ok && it.Label == "Isotope v0.2.0 is installed: restart to finish"
	})
	eventually(t, "the notification", func() bool {
		h.mu.Lock()
		defer h.mu.Unlock()
		return len(h.actions) == 1
	})
	h.mu.Lock()
	note, invoke := h.actions[0], h.invoke
	h.mu.Unlock()
	if !strings.Contains(note, "Restart to finish") || !strings.Contains(note, "v0.2.0") || !strings.HasSuffix(note, "| Restart") {
		t.Fatalf("notification %q", note)
	}
	// The button restarts, as the menu's Restart now does.
	invoke()
	eventually(t, "the reboot", func() bool { return f.count("Reboot") == 1 })
	// The same status again: no second notification.
	f.push(Event{Status: &staged})
	time.Sleep(20 * time.Millisecond)
	h.mu.Lock()
	n := len(h.actions)
	h.mu.Unlock()
	if n != 1 {
		t.Fatalf("%d notifications for one version", n)
	}
	if notes := h.notified(); len(notes) != 0 {
		t.Fatalf("plain notifications %q", notes)
	}
}

func TestTrayDoesNotAnnounceAManualInstall(t *testing.T) {
	staged := Status{State: StateRebootPending, Pending: pendingInstalled, Installed: "v0.1.0", Staged: "v0.2.0"}
	f := &trayFake{st: staged}
	h := startTray(t, dialFake(f), time.Hour, time.Hour)
	h.ui.waitView(t, "restart to finish", func(v trayView) bool { return v.Tooltip == "Isotope v0.2.0 is installed: restart to finish" })
	time.Sleep(20 * time.Millisecond)
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.actions) != 0 {
		t.Fatalf("notified a manual install: %q", h.actions)
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
