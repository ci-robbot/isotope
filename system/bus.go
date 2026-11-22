package main

// The client of isotope-updated (io.kairos.IsotopeUpdate1 on the system
// bus): the same methods the isotope CLI and the tray call. The CLI's client
// is in package main of update/ and cannot be imported; this one is its
// twin for a GUI, as welcome/locale.go is for SetLocale. Every privileged
// job happens in the daemon, behind polkit.

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/godbus/dbus/v5"
)

const (
	busName      = "io.kairos.IsotopeUpdate1"
	busPath      = dbus.ObjectPath("/io/kairos/IsotopeUpdate1")
	busInterface = "io.kairos.IsotopeUpdate1"
	errPrefix    = busInterface + ".Error."
)

// The daemon's states (update/daemon.go): part of the D-Bus interface.
const (
	StateIdle            = "idle"
	StateChecking        = "checking"
	StateUpToDate        = "up-to-date"
	StateUpdateAvailable = "update-available"
	StateInspecting      = "inspecting"
	StateApplying        = "applying"
	StateRebootPending   = "reboot-pending"
	StateError           = "error"
	StateUnmanaged       = "unmanaged"
	StateExtension       = "extension"
	StateBackup          = "backup"
)

// Status.Pending: how the install behind reboot-pending ended.
const (
	pendingInstalled = "installed"
	pendingFailed    = "failed"
	pendingStarted   = "started"
)

// Status is the daemon's Status.
type Status struct {
	State, Installed, Latest, Pin, Detail string
	Advanced                              bool
	Pending                               string
	ExtResult, ExtDetail                  string
	BackupResult, BackupDetail            string
}

// Busy says whether the daemon runs a job.
func (s Status) Busy() bool {
	switch s.State {
	case StateChecking, StateInspecting, StateApplying, StateExtension, StateBackup:
		return true
	}
	return false
}

// InspectInfo is what the daemon says about an image before an install.
type InspectInfo struct {
	Ref, Digest, Flavor, Version, GPU string
	Trusted, Isotope                  bool
	Refusal                           string
	Overridable                       bool
}

// Extension is an installed system extension.
type Extension struct {
	Name, File, Source string
	Enabled, Merged    bool
}

// CatalogEntry is an extension a catalog offers.
type CatalogEntry struct {
	Name, Title, Description, Latest, Catalog string
	Versions                                  []string
	Installed, Enabled                        bool
}

// BackupStatus is the daemon's BackupStatus.
type BackupStatus struct {
	Available                bool
	Target, Schedule         string
	Containers, Present      bool
	Last                     int64
	Result, Warning, Restore string
}

// Container is one LUKS container of RecoveryStatus.
type Container struct {
	Label, Device             string
	TPM, Recovery, Passphrase int32
	TPMWorks                  bool
}

// RecoveryStatus is the daemon's RecoveryStatus.
type RecoveryStatus struct {
	Edition    string
	Containers []Container
}

// Event is a StateChanged (Status) or a Progress line (Line).
type Event struct {
	Status *Status
	Line   string
}

// Daemon is isotope-updated as the pages use it. Every method blocks on
// D-Bus; the pages call them off the UI goroutine.
type Daemon interface {
	Status() (Status, error)
	Check() error
	Releases() ([]string, error)
	Inspect(ref string, advanced bool) (InspectInfo, error)
	Apply(ref, digest string, advanced bool) error
	Cancel() error
	Reboot() error
	RebootPrevious() error
	Extensions() ([]Extension, error)
	Catalog() ([]CatalogEntry, error)
	InstallExtension(ref, version string, force bool) error
	SetExtensionEnabled(name string, on, force bool) error
	RemoveExtension(name string) error
	RecoveryStatus() (RecoveryStatus, error)
	BackupStatus() (BackupStatus, error)
	BackupNow() error
	SetBackupSchedule(schedule string) error
	// Watch sends the daemon's events until stop is called.
	Watch() (events <-chan Event, stop func(), err error)
}

// DaemonError is a refusal or failure the daemon named: Name is what
// follows "io.kairos.IsotopeUpdate1.Error." (Busy, NotAuthorized, ...).
type DaemonError struct{ Name, Msg string }

func (e *DaemonError) Error() string { return e.Msg }

// errName is the daemon's name for err, "" when it is not the daemon's.
func errName(err error) string {
	var de *DaemonError
	if errors.As(err, &de) {
		return de.Name
	}
	return ""
}

func wrapErr(err error) error {
	var be dbus.Error
	if errors.As(err, &be) && strings.HasPrefix(be.Name, errPrefix) {
		msg := be.Name
		if len(be.Body) > 0 {
			if s, ok := be.Body[0].(string); ok {
				msg = s
			}
		}
		return &DaemonError{Name: strings.TrimPrefix(be.Name, errPrefix), Msg: msg}
	}
	var pbe *dbus.Error
	if errors.As(err, &pbe) && pbe != nil {
		return wrapErr(*pbe)
	}
	return err
}

// Describe is the sentence a page shows for a daemon error.
func Describe(err error) string {
	switch errName(err) {
	case "NotAuthorized":
		return "Only members of the admin group can do this."
	case "RebootPending":
		return "The last update waits for a restart; restart first."
	case "Busy":
		return "The update service is busy with another job; try again when it ends."
	case "Exiting":
		return "The update service was stopping; try again."
	}
	var dbe dbus.Error
	if errors.As(err, &dbe) && strings.HasPrefix(dbe.Name, "org.freedesktop.DBus.Error.") {
		return "The update service (isotope-updated) is not available."
	}
	if err == nil {
		return ""
	}
	return err.Error()
}

// busDaemon is Daemon over the system bus: one connection for the life of
// the window.
type busDaemon struct{ conn *dbus.Conn }

func dialDaemon() (*busDaemon, error) {
	c, err := dbus.ConnectSystemBus()
	if err != nil {
		return nil, err
	}
	return &busDaemon{conn: c}, nil
}

func (d *busDaemon) call(flags dbus.Flags, method string, out []any, args ...any) error {
	call := d.conn.Object(busName, busPath).Call(busInterface+"."+method, flags, args...)
	err := call.Err
	if err == nil && len(out) > 0 {
		err = call.Store(out...)
	}
	// The service exits when idle; a call that lands during the exit is
	// refused once with Exiting, and the next one starts a new instance.
	if errName(wrapErr(err)) == "Exiting" {
		call = d.conn.Object(busName, busPath).Call(busInterface+"."+method, flags, args...)
		err = call.Err
		if err == nil && len(out) > 0 {
			err = call.Store(out...)
		}
	}
	return wrapErr(err)
}

func str(m map[string]dbus.Variant, k string) string {
	s, _ := m[k].Value().(string)
	return s
}

func boolean(m map[string]dbus.Variant, k string) bool {
	b, _ := m[k].Value().(bool)
	return b
}

func i32(m map[string]dbus.Variant, k string) int32 {
	n, _ := m[k].Value().(int32)
	return n
}

func statusFromDict(m map[string]dbus.Variant) Status {
	return Status{
		State: str(m, "state"), Installed: str(m, "installed"), Latest: str(m, "latest"),
		Pin: str(m, "pin"), Detail: str(m, "detail"), Advanced: boolean(m, "advanced"),
		Pending: str(m, "pending"), ExtResult: str(m, "ext_result"), ExtDetail: str(m, "ext_detail"),
		BackupResult: str(m, "backup_result"), BackupDetail: str(m, "backup_detail"),
	}
}

func (d *busDaemon) Status() (Status, error) { return d.status(0) }

// pollStatus does not start the service: a window left open must not keep
// a root process alive.
func (d *busDaemon) pollStatus() (Status, error) { return d.status(dbus.FlagNoAutoStart) }

func (d *busDaemon) status(flags dbus.Flags) (Status, error) {
	var m map[string]dbus.Variant
	if err := d.call(flags, "Status", []any{&m}); err != nil {
		return Status{}, err
	}
	return statusFromDict(m), nil
}

func (d *busDaemon) Check() error { return retryBusy(func() error { return d.call(0, "Check", nil) }) }

func (d *busDaemon) Releases() ([]string, error) {
	var tags []string
	err := retryBusy(func() error { return d.call(0, "Releases", []any{&tags}) })
	return tags, err
}

func (d *busDaemon) Inspect(ref string, advanced bool) (InspectInfo, error) {
	var m map[string]dbus.Variant
	if err := retryBusy(func() error { return d.call(0, "Inspect", []any{&m}, ref, advanced) }); err != nil {
		return InspectInfo{}, err
	}
	return InspectInfo{
		Ref: str(m, "ref"), Digest: str(m, "digest"), Flavor: str(m, "flavor"), Version: str(m, "version"),
		GPU: str(m, "gpu"), Trusted: boolean(m, "trusted"), Isotope: boolean(m, "isotope"),
		Refusal: str(m, "refusal"), Overridable: boolean(m, "overridable"),
	}, nil
}

func (d *busDaemon) Apply(ref, digest string, advanced bool) error {
	return d.call(0, "Apply", nil, ref, digest, advanced)
}
func (d *busDaemon) Cancel() error         { return d.call(0, "Cancel", nil) }
func (d *busDaemon) Reboot() error         { return d.call(0, "Reboot", nil) }
func (d *busDaemon) RebootPrevious() error { return d.call(0, "RebootPrevious", nil) }

func (d *busDaemon) Extensions() ([]Extension, error) {
	var list []map[string]dbus.Variant
	if err := d.call(0, "Extensions", []any{&list}); err != nil {
		return nil, err
	}
	out := make([]Extension, 0, len(list))
	for _, m := range list {
		out = append(out, Extension{Name: str(m, "name"), File: str(m, "file"), Source: str(m, "source"),
			Enabled: boolean(m, "enabled"), Merged: boolean(m, "merged")})
	}
	return out, nil
}

func (d *busDaemon) Catalog() ([]CatalogEntry, error) {
	var list []map[string]dbus.Variant
	if err := retryBusy(func() error { return d.call(0, "Catalog", []any{&list}) }); err != nil {
		return nil, err
	}
	out := make([]CatalogEntry, 0, len(list))
	for _, m := range list {
		vs, _ := m["versions"].Value().([]string)
		out = append(out, CatalogEntry{Name: str(m, "name"), Title: str(m, "title"), Description: str(m, "description"),
			Latest: str(m, "latest"), Catalog: str(m, "catalog"), Versions: vs,
			Installed: boolean(m, "installed"), Enabled: boolean(m, "enabled")})
	}
	return out, nil
}

func (d *busDaemon) InstallExtension(ref, version string, force bool) error {
	return d.call(0, "InstallExtension", nil, ref, version, force)
}
func (d *busDaemon) SetExtensionEnabled(name string, on, force bool) error {
	return d.call(0, "SetExtensionEnabled", nil, name, on, force)
}
func (d *busDaemon) RemoveExtension(name string) error {
	return d.call(0, "RemoveExtension", nil, name)
}

func (d *busDaemon) RecoveryStatus() (RecoveryStatus, error) {
	var m map[string]dbus.Variant
	if err := d.call(0, "RecoveryStatus", []any{&m}); err != nil {
		return RecoveryStatus{}, err
	}
	return recoveryFromDict(m), nil
}

func recoveryFromDict(m map[string]dbus.Variant) RecoveryStatus {
	st := RecoveryStatus{Edition: str(m, "edition")}
	cs, _ := m["containers"].Value().([]map[string]dbus.Variant)
	for _, c := range cs {
		st.Containers = append(st.Containers, Container{
			Label: str(c, "label"), Device: str(c, "device"), TPM: i32(c, "tpm"),
			Recovery: i32(c, "recovery"), Passphrase: i32(c, "passphrase"), TPMWorks: boolean(c, "tpm_works"),
		})
	}
	return st
}

func (d *busDaemon) BackupStatus() (BackupStatus, error) {
	var m map[string]dbus.Variant
	if err := d.call(0, "BackupStatus", []any{&m}); err != nil {
		return BackupStatus{}, err
	}
	return backupFromDict(m), nil
}

func backupFromDict(m map[string]dbus.Variant) BackupStatus {
	last, _ := m["last"].Value().(int64)
	return BackupStatus{
		Available: boolean(m, "available"), Target: str(m, "target"), Schedule: str(m, "schedule"),
		Containers: boolean(m, "containers"), Present: boolean(m, "present"), Last: last,
		Result: str(m, "result"), Warning: str(m, "warning"), Restore: str(m, "restore"),
	}
}

func (d *busDaemon) BackupNow() error { return d.call(0, "BackupNow", nil) }
func (d *busDaemon) SetBackupSchedule(s string) error {
	return d.call(0, "SetBackupSchedule", nil, s)
}

// retryBusy repeats a network call while the daemon's one network slot is
// taken (the tray's own check), as the CLI does.
func retryBusy(f func() error) error {
	err := f()
	for i := 0; i < 5 && errName(err) == "Busy"; i++ {
		time.Sleep(time.Second)
		err = f()
	}
	return err
}

// eventFromSignal decodes a StateChanged or Progress signal.
func eventFromSignal(s *dbus.Signal) (Event, bool) {
	if len(s.Body) != 1 {
		return Event{}, false
	}
	switch s.Name {
	case busInterface + ".StateChanged":
		m, ok := s.Body[0].(map[string]dbus.Variant)
		if !ok {
			return Event{}, false
		}
		st := statusFromDict(m)
		return Event{Status: &st}, true
	case busInterface + ".Progress":
		line, ok := s.Body[0].(string)
		return Event{Line: line}, ok
	}
	return Event{}, false
}

// watchPoll backs up a missed signal while a job runs.
var watchPoll = 3 * time.Second

func (d *busDaemon) Watch() (<-chan Event, func(), error) {
	opts := []dbus.MatchOption{dbus.WithMatchSender(busName), dbus.WithMatchObjectPath(busPath), dbus.WithMatchInterface(busInterface)}
	if err := d.conn.AddMatchSignal(opts...); err != nil {
		return nil, nil, fmt.Errorf("subscribing to the update service: %w", err)
	}
	sigs := make(chan *dbus.Signal, 64)
	d.conn.Signal(sigs)
	out := make(chan Event, 64)
	done := make(chan struct{})
	go func() {
		defer close(out)
		tick := time.NewTicker(watchPoll)
		defer tick.Stop()
		for {
			var ev Event
			select {
			case <-done:
				return
			case <-tick.C:
				st, err := d.pollStatus()
				if err != nil || !st.Busy() {
					continue
				}
				ev = Event{Status: &st}
			case s, ok := <-sigs:
				if !ok {
					return
				}
				if ev, ok = eventFromSignal(s); !ok {
					continue
				}
			}
			select {
			case out <- ev:
			case <-done:
				return
			}
		}
	}()
	var once sync.Once
	return out, func() {
		once.Do(func() {
			close(done)
			d.conn.RemoveSignal(sigs)
			_ = d.conn.RemoveMatchSignal(opts...)
		})
	}, nil
}
