package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/godbus/dbus/v5"
	"gopkg.in/yaml.v3"
)

// Automatic updates: when an administrator turns them on, the daily
// isotope-autoupdate.timer runs `isotope update --auto` as root, which asks
// the daemon (RunAutoUpdate) to install the newest release of this
// machine's own flavor and line into the other A/B slot, the way "Install
// the latest" does. Nothing restarts: the tray and Isotope System say
// "Restart to finish". Off by default. See
// docs/superpowers/specs/2026-11-24-auto-updates-design.md.

// The files of the feature. Variables so the tests can point them into a
// temporary directory.
var (
	// autoOEM keeps the setting across reboots: /oem survives them, and
	// yip ignores the isotope_autoupdate key.
	autoOEM = "/oem/96_isotope_autoupdate.yaml"
	// autoRunFile is the last automatic run, next to the install history
	// (Kairos rebuilds /var at every boot).
	autoRunFile = "/usr/local/isotope-update/autoupdate"
	// autoBootMarkers are immucore's markers of the boots that must never
	// install: the live media, an install, the Trusted Boot medium. The
	// firewall skips the same boots.
	autoBootMarkers = []string{"/run/cos/live_mode", "/run/cos/install_mode", "/run/cos/uki_install_mode"}
	// autoPassiveMarker and autoRecoveryMarker are immucore's boot-mode
	// sentinels (the same directory as bootModeDir's): this boot runs the
	// previous system (a rollback, or the boot loader's fallback after the
	// new system failed to boot), or the recovery system. Neither installs
	// on its own: it would undo the rollback, or reinstall the release that
	// did not boot.
	autoPassiveMarker  = "/run/cos/passive_mode"
	autoRecoveryMarker = "/run/cos/recovery_mode"
	// autoHoldFile names the release the user rolled back from: the
	// automatic run never installs it again. A newer release is fine, and
	// installing the held release by hand lifts the hold.
	autoHoldFile = "/usr/local/isotope-update/autoupdate-hold"
)

// autoRunnerUID is the only caller RunAutoUpdate accepts: root, which
// isotope-autoupdate.service runs as. Atomic so a test can stand in for
// root while the service runs.
var autoRunnerUID atomic.Uint32

// autoBatteryMin is the battery level, in percent, under which a machine
// on battery does not install.
const autoBatteryMin = 30.0

// The words of an automatic run: what RunAutoUpdate answers, and the
// result kept as the last run (all but autoOff and autoStarted, which the
// job's own end replaces with autoInstalled or autoFailed).
const (
	autoOff       = "off"
	autoStarted   = "started"
	autoInstalled = "installed"
	autoUpToDate  = "up-to-date"
	autoSkipped   = "skipped"
	autoFailed    = "failed"
)

// autoOutcome is one automatic run: a word above, the version it is about
// (the newest release, or the installed one), and a sentence.
type autoOutcome struct {
	Result, Version, Detail string
}

// autoRun is the last run as kept in autoRunFile.
type autoRun struct {
	Time int64
	autoOutcome
}

// autoFile is the cloud-config in autoOEM.
type autoFile struct {
	Name string `yaml:"name"`
	Auto *struct {
		Enabled bool `yaml:"enabled"`
	} `yaml:"isotope_autoupdate"`
}

// loadAutoUpdate reads the setting. No file is off. A file that cannot be
// read or parsed is off too, with the error: nothing installs on a setting
// nobody can confirm.
func loadAutoUpdate() (bool, error) {
	b, err := os.ReadFile(autoOEM)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var f autoFile
	if err := yaml.Unmarshal(b, &f); err != nil {
		return false, fmt.Errorf("%s: %w", autoOEM, err)
	}
	if f.Auto == nil {
		return false, fmt.Errorf("%s has no isotope_autoupdate key", autoOEM)
	}
	return f.Auto.Enabled, nil
}

// renderAutoOEM is the file setAutoUpdate writes.
func renderAutoOEM(on bool) []byte {
	var b bytes.Buffer
	b.WriteString("# Written by isotope-updated (`isotope update auto on|off`, Isotope System).\n")
	b.WriteString("name: \"Isotope automatic updates\"\n")
	b.WriteString("isotope_autoupdate:\n")
	fmt.Fprintf(&b, "  enabled: %t\n", on)
	return b.Bytes()
}

// setAutoUpdate writes the setting. It shares settingsMu with SetLocale:
// both write /oem, which a restore replaces.
func setAutoUpdate(on bool) error {
	settingsMu.Lock()
	defer settingsMu.Unlock()
	if restoring {
		return errRestoring
	}
	return writeRunFile(autoOEM, string(renderAutoOEM(on)))
}

// oneField makes s fit one tab-separated field of one line.
func oneField(s string) string {
	s = strings.Map(func(r rune) rune {
		if r == '\t' || r == '\n' || r == '\r' {
			return ' '
		}
		return r
	}, s)
	if len(s) > historyFieldMax {
		s = s[:historyFieldMax]
	}
	return s
}

// readAutoRun is the last run; ok is false when there is none.
func readAutoRun() (autoRun, bool) {
	b, err := os.ReadFile(autoRunFile)
	if err != nil {
		return autoRun{}, false
	}
	f := strings.SplitN(strings.TrimRight(string(b), "\n"), "\t", 4)
	if len(f) != 4 {
		return autoRun{}, false
	}
	t, err := strconv.ParseInt(f[0], 10, 64)
	if err != nil {
		return autoRun{}, false
	}
	return autoRun{Time: t, autoOutcome: autoOutcome{Result: f[1], Version: f[2], Detail: f[3]}}, true
}

func writeAutoRun(r autoRun) error {
	if err := os.MkdirAll(filepath.Dir(autoRunFile), 0o755); err != nil {
		return err
	}
	line := strings.Join([]string{strconv.FormatInt(r.Time, 10), oneField(r.Result), oneField(r.Version), oneField(r.Detail)}, "\t")
	return writeRunFile(autoRunFile, line+"\n")
}

// autoBootSkip is why this boot never installs on its own, or "".
func autoBootSkip() string {
	for _, m := range autoBootMarkers {
		if _, err := os.Stat(m); err == nil {
			return "this is a live, install or Trusted Boot medium boot (" + filepath.Base(m) + ")"
		}
	}
	return ""
}

// autoSlotSkip is why this boot of the previous or the recovery system
// never installs on its own, or "".
func autoSlotSkip() string {
	if _, err := os.Stat(autoRecoveryMarker); err == nil {
		return "this is the recovery system"
	}
	if _, err := os.Stat(autoPassiveMarker); err == nil {
		return "booted into the previous version (rollback)"
	}
	return ""
}

// heldVersion is the release an automatic run must not install, or "".
func heldVersion() string {
	b, err := os.ReadFile(autoHoldFile)
	if err != nil {
		return ""
	}
	if v := strings.TrimSpace(string(b)); strictTag.MatchString(v) {
		return v
	}
	return ""
}

// holdVersion keeps v from being installed automatically again. A failed
// write is logged: the passive boot still skips.
func holdVersion(v string) {
	if !strictTag.MatchString(v) {
		return
	}
	err := os.MkdirAll(filepath.Dir(autoHoldFile), 0o755)
	if err == nil {
		err = writeRunFile(autoHoldFile, v+"\n")
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "isotope: could not hold back", v+":", err)
	}
}

// releaseHold lifts the hold on v (an install of v by hand).
func releaseHold(v string) {
	if v != "" && heldVersion() == v {
		if err := os.Remove(autoHoldFile); err != nil && !errors.Is(err, os.ErrNotExist) {
			fmt.Fprintln(os.Stderr, "isotope: could not lift the hold on", v+":", err)
		}
	}
}

// lastInstalled is the version of the newest finished install in the
// history: on a passive boot, the system the machine fell back from.
func lastInstalled() string {
	h, _ := readHistory()
	for _, e := range h { // newest first
		if e.Kind == "install" && e.Result == "installed" {
			return e.Version
		}
	}
	return ""
}

// autoEnvironment answers the questions an automatic run asks the machine.
// The real one asks NetworkManager and UPower on the system bus; tests use
// a fake.
type autoEnvironment interface {
	// Metered says whether the connection may cost money.
	Metered() (bool, error)
	// Battery says whether the machine runs on battery, and its level.
	Battery() (onBattery bool, percent float64, err error)
}

// recordAuto keeps o as the last run and logs it. A failure to write it
// only costs the line on the Updates page.
func (d *Daemon) recordAuto(o autoOutcome) {
	fmt.Fprintf(os.Stderr, "isotope: automatic update: %s %s %s\n", o.Result, o.Version, o.Detail)
	if err := writeAutoRun(autoRun{Time: d.clock().Unix(), autoOutcome: o}); err != nil {
		fmt.Fprintln(os.Stderr, "isotope: could not record the automatic update:", err)
	}
}

func (d *Daemon) clock() time.Time {
	if d.now != nil {
		return d.now()
	}
	return time.Now()
}

// AutoUpdate is one automatic run: if the setting is on and nothing says to
// wait, it starts the install of the newest release of this machine's own
// flavor and line, as "Install the latest" would, and returns autoStarted.
// The job's end is recorded by run. Every other outcome is recorded here,
// except autoOff, which does nothing.
func (d *Daemon) AutoUpdate(ctx context.Context) autoOutcome {
	on, err := loadAutoUpdate()
	if err != nil {
		fmt.Fprintln(os.Stderr, "isotope: automatic updates are off:", err)
	}
	if !on {
		return autoOutcome{Result: autoOff, Detail: "Automatic updates are off."}
	}
	o := d.autoDecide(ctx)
	if o.Result != autoStarted {
		d.recordAuto(o)
	}
	return o
}

func skip(why string) autoOutcome {
	return autoOutcome{Result: autoSkipped, Detail: "Not now: " + why + "."}
}

func (d *Daemon) autoDecide(ctx context.Context) autoOutcome {
	if !d.managed {
		return skip("this system has no Isotope release file")
	}
	if why := autoBootSkip(); why != "" {
		return skip(why)
	}
	if why := autoSlotSkip(); why != "" {
		// A fallback leaves no rollback entry: hold the release it fell
		// back from, as RebootPrevious does for a rollback.
		if _, err := os.Stat(autoPassiveMarker); err == nil && heldVersion() == "" {
			if v := lastInstalled(); Newer(v, d.rel.Version) {
				holdVersion(v)
			}
		}
		return skip(why)
	}
	d.mu.Lock()
	var why string
	switch {
	case d.stopping:
		why = "the update service is exiting"
	case d.rebooting:
		why = "the system is restarting"
	case d.state == StateRebootPending && d.pending == pendingInstalled:
		why = "an update is installed and waits for a restart"
	case d.state == StateRebootPending:
		why = "the last update did not finish; restart before trying again"
	case d.busyLocked() || d.job != nil:
		why = "another job is running"
	}
	d.mu.Unlock()
	if why != "" {
		return skip(why)
	}
	if why := d.autoEnvSkip(); why != "" {
		return skip(why)
	}

	if err := d.Check(ctx); err != nil {
		if errors.Is(err, ErrBusy) || errors.Is(err, ErrStopping) {
			return skip("another job is running")
		}
		return autoOutcome{Result: autoFailed, Detail: "Could not check for updates: " + err.Error()}
	}
	st := d.Status()
	switch {
	case st.State == StateRebootPending:
		return skip("an update is installed and waits for a restart")
	case st.Pin != "":
		// The user installed this image on purpose; "Install the latest"
		// would remove the pin, an automatic run does not.
		return skip("the system is pinned to " + st.Pin + "; install the latest release once to unpin it")
	case !Newer(st.Latest, d.rel.Version):
		return autoOutcome{Result: autoUpToDate, Version: d.rel.Version, Detail: "Isotope " + d.rel.Version + " is up to date."}
	}
	if held := heldVersion(); held != "" && !Newer(st.Latest, held) {
		return autoOutcome{Result: autoSkipped, Version: st.Latest,
			Detail: "Not now: you rolled back from Isotope " + held + ", so it is not installed automatically. A newer release will be; install it by hand to undo this."}
	}
	latest := st.Latest

	ref, err := ReleaseRef(d.rel, d.rel.Flavor, latest)
	if err != nil {
		return autoOutcome{Result: autoFailed, Version: latest, Detail: err.Error()}
	}
	info, v, _, err := d.evaluate(ctx, ref, false)
	if err == nil {
		err = autoRefusal(d.rel, v, latest)
	}
	if err != nil {
		return autoOutcome{Result: autoFailed, Version: latest, Detail: "Isotope " + latest + " was not installed: " + err.Error()}
	}
	err = d.apply(ref.String(), info.Digest, false, true)
	switch {
	case err == nil:
		return autoOutcome{Result: autoStarted, Version: latest, Detail: "Installing Isotope " + latest + "."}
	case errors.Is(err, ErrBusy), errors.Is(err, ErrRebootPending), errors.Is(err, ErrRebooting), errors.Is(err, ErrStopping):
		return skip(err.Error())
	}
	return autoOutcome{Result: autoFailed, Version: latest, Detail: "Isotope " + latest + " was not installed: " + err.Error()}
}

// autoRefusal is the check an automatic install adds to Classify's: only a
// published release, of this flavor and line, the newest, and newer than
// the running one. Classify alone allows another flavor (a switch).
func autoRefusal(rel Release, v Verdict, latest string) error {
	switch {
	case !v.Trusted:
		return ErrNotRelease
	case v.Flavor != rel.Flavor:
		return fmt.Errorf("the image is the %s flavor, not %s", v.Flavor, rel.Flavor)
	case v.GPU != rel.Line():
		return fmt.Errorf("the image is on the %s line, not %s", v.GPU, rel.Line())
	case v.Version != latest:
		return fmt.Errorf("the image is %s, not the newest release %s", v.Version, latest)
	case !Newer(v.Version, rel.Version):
		return fmt.Errorf("%s is not newer than the installed %s", v.Version, rel.Version)
	}
	return nil
}

// autoEnvSkip asks NetworkManager and UPower. "" means go ahead.
func (d *Daemon) autoEnvSkip() string {
	if d.autoEnv == nil {
		return ""
	}
	return envSkip(d.autoEnv)
}

// envSkip is why an automatic job should wait for a better moment: a
// metered connection, or a battery below autoBatteryMin while on battery.
// "" means go ahead. Automatic system updates and automatic app updates
// (apps.go) share it.
func envSkip(env autoEnvironment) string {
	metered, err := env.Metered()
	switch {
	case err != nil:
		return "could not tell whether the connection is metered (" + err.Error() + ")"
	case metered:
		return "the connection is metered"
	}
	onBattery, pct, err := env.Battery()
	switch {
	case err != nil:
		return "could not read the battery (" + err.Error() + ")"
	case onBattery && pct < autoBatteryMin:
		return fmt.Sprintf("the battery is at %.0f%% (below %.0f%%) and not charging", pct, autoBatteryMin)
	}
	return ""
}

// busAutoEnv is the real autoEnvironment.
type busAutoEnv struct{ conn *dbus.Conn }

// absent says whether err means the service is not on the bus at all: a
// machine without it (UPower on the server).
func absent(err error) bool {
	var de dbus.Error
	if errors.As(err, &de) {
		switch de.Name {
		case "org.freedesktop.DBus.Error.ServiceUnknown", "org.freedesktop.DBus.Error.NameHasNoOwner":
			return true
		}
	}
	return false
}

func (e busAutoEnv) prop(dest string, path dbus.ObjectPath, name string) (dbus.Variant, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var v dbus.Variant
	err := e.conn.Object(dest, path).CallWithContext(ctx, "org.freedesktop.DBus.Properties.Get", 0,
		name[:strings.LastIndexByte(name, '.')], name[strings.LastIndexByte(name, '.')+1:]).Store(&v)
	return v, err
}

// nmMetered: NM_METERED_YES and NM_METERED_GUESS_YES.
func nmMetered(v uint32) bool { return v == 1 || v == 3 }

// Metered reads NetworkManager's Metered (NMMetered). No NetworkManager is
// not metered.
func (e busAutoEnv) Metered() (bool, error) {
	v, err := e.prop("org.freedesktop.NetworkManager", "/org/freedesktop/NetworkManager", "org.freedesktop.NetworkManager.Metered")
	if absent(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	m, ok := v.Value().(uint32)
	if !ok {
		return false, fmt.Errorf("NetworkManager's Metered is a %s", v.Signature())
	}
	return nmMetered(m), nil
}

// Battery reads UPower's OnBattery, and the display device's Percentage
// when on battery. No UPower (the server) is not on battery.
func (e busAutoEnv) Battery() (bool, float64, error) {
	v, err := e.prop("org.freedesktop.UPower", "/org/freedesktop/UPower", "org.freedesktop.UPower.OnBattery")
	if absent(err) {
		return false, 0, nil
	}
	if err != nil {
		return false, 0, err
	}
	on, _ := v.Value().(bool)
	if !on {
		return false, 0, nil
	}
	v, err = e.prop("org.freedesktop.UPower", "/org/freedesktop/UPower/devices/DisplayDevice", "org.freedesktop.UPower.Device.Percentage")
	if err != nil {
		return true, 0, err
	}
	pct, ok := v.Value().(float64)
	if !ok {
		return true, 0, fmt.Errorf("UPower's Percentage is a %s", v.Signature())
	}
	return true, pct, nil
}

// stagedFile names the version a finished install waits to boot, and
// whether the automatic run installed it: "<version> auto|manual". In the
// run dir, so the reboot clears it with the reboot marker.
func stagedFile() string { return filepath.Join(runDir(), "staged") }

func writeStaged(version string, auto bool) error {
	how := "manual"
	if auto {
		how = "auto"
	}
	return writeRunFile(stagedFile(), version+" "+how+"\n")
}

func readStaged() (version string, auto bool) {
	b, err := os.ReadFile(stagedFile())
	if err != nil {
		return "", false
	}
	v, how, _ := strings.Cut(strings.TrimSpace(string(b)), " ")
	if !strictTag.MatchString(v) {
		return "", false
	}
	return v, how == "auto"
}

// The bus methods.

// AutoUpdateStatus is open to every caller: the setting, the last run and
// the staged version say nothing secret. "enabled", "last_time" (unix,
// 0 for never), "last_result", "last_version", "last_detail", "staged",
// "staged_auto".
func (a busAPI) AutoUpdateStatus() (map[string]dbus.Variant, *dbus.Error) {
	done, derr := a.s.enter()
	if derr != nil {
		return nil, derr
	}
	defer done()
	on, err := loadAutoUpdate()
	broken := ""
	if err != nil {
		broken = err.Error()
	}
	r, _ := readAutoRun()
	st := a.s.d.Status()
	return map[string]dbus.Variant{
		"enabled":      dbus.MakeVariant(on),
		"broken":       dbus.MakeVariant(broken),
		"last_time":    dbus.MakeVariant(r.Time),
		"last_result":  dbus.MakeVariant(r.Result),
		"last_version": dbus.MakeVariant(r.Version),
		"last_detail":  dbus.MakeVariant(r.Detail),
		"staged":       dbus.MakeVariant(st.Staged),
		"staged_auto":  dbus.MakeVariant(st.StagedAuto),
	}, nil
}

// SetAutoUpdate turns automatic updates on or off: the settings action
// (administrators, never a Flatpak app).
func (a busAPI) SetAutoUpdate(sender dbus.Sender, on bool) *dbus.Error {
	done, derr := a.s.enter()
	if derr != nil {
		return derr
	}
	defer done()
	if err := a.s.authorize(sender, actionSettings); err != nil {
		return err
	}
	if derr := a.s.notRestoring(); derr != nil {
		return derr
	}
	if err := setAutoUpdate(on); err != nil {
		fmt.Fprintf(os.Stderr, "isotope: SetAutoUpdate %t: %v\n", on, err)
		return busError(err)
	}
	return nil
}

// RunAutoUpdate is one automatic run, for isotope-autoupdate.service only:
// the caller must be root. It returns at once once an install has started
// (result "started"); the install ends as any other one.
func (a busAPI) RunAutoUpdate(sender dbus.Sender) (string, string, string, *dbus.Error) {
	done, derr := a.s.enter()
	if derr != nil {
		return "", "", "", derr
	}
	defer done()
	if a.s.uidOf == nil {
		return "", "", "", errNotAuthorized
	}
	uid, err := a.s.uidOf(sender)
	if err != nil || uid != autoRunnerUID.Load() {
		fmt.Fprintf(os.Stderr, "isotope: refusing RunAutoUpdate for %s (uid %d, %v)\n", sender, uid, err)
		return "", "", "", errNotAuthorized
	}
	free, derr := a.s.network()
	if derr != nil {
		return "", "", "", derr
	}
	defer free()
	o := a.s.d.AutoUpdate(context.Background())
	return o.Result, o.Version, o.Detail, nil
}
