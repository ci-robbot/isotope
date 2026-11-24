package main

// The client of isotope-updated (io.kairos.IsotopeUpdate1 on the system
// bus): the same methods the isotope CLI and the tray call. The CLI's client
// is in package main of cmd/isotope/ and cannot be imported; this one is its
// twin for a GUI, as cmd/isotope-welcome/locale.go is for SetLocale. Every privileged
// job happens in the daemon, behind polkit.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/godbus/dbus/v5"

	"github.com/kairos-community/isotope/pkg/firewall"
	"github.com/kairos-community/isotope/pkg/i18n"
	"github.com/kairos-community/isotope/pkg/shares"
)

const (
	busName      = "io.kairos.IsotopeUpdate1"
	busPath      = dbus.ObjectPath("/io/kairos/IsotopeUpdate1")
	busInterface = "io.kairos.IsotopeUpdate1"
	errPrefix    = busInterface + ".Error."
)

// The daemon's states (cmd/isotope/daemon.go): part of the D-Bus interface.
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
	StateRestore         = "restore"
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
	// Staged is the version a finished install waits to boot; StagedAuto
	// says the automatic update installed it.
	Staged     string
	StagedAuto bool
}

// Busy says whether the daemon runs a job.
func (s Status) Busy() bool {
	switch s.State {
	case StateChecking, StateInspecting, StateApplying, StateExtension, StateBackup, StateRestore:
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

// BackupStatus is the daemon's BackupStatus. CanRestore is false on a
// machine in use; RestoreRefusal then says why.
type BackupStatus struct {
	Available                bool
	Target, Label, Kind      string
	Schedule                 string
	Containers, Present      bool
	Last                     int64
	Result, Warning, Restore string
	CanRestore               bool
	RestoreRefusal           string
}

// User is one row of the daemon's Users.
type User struct {
	Name, FullName, Home string
	UID                  uint32
	Admin                bool
}

// Firewall is the daemon's FirewallStatus: the saved setting, what is
// loaded now, and the ports an administrator allowed.
type Firewall = firewall.Status

// AutoStatus is the daemon's AutoUpdateStatus: the setting (Broken: it
// cannot be read, and so is off), the last automatic run and the staged
// version.
type AutoStatus struct {
	Enabled                                     bool
	Broken                                      string
	LastTime                                    int64
	LastResult, LastVersion, LastDetail, Staged string
	StagedAuto                                  bool
}

// Credentials go to the daemon through a pipe, never as a D-Bus string:
// the backup password, then the target's NAME=value lines.
type Credentials struct {
	Password string
	Env      []string // NAME=value
}

// TargetOptions are the a{sv} options of BackupInit, BackupList and
// Restore. A zero value is not sent.
type TargetOptions struct {
	Containers, Existing bool
	Label, HostKey       string
	Port                 uint32
}

// Snapshot is one backup of BackupList.
type Snapshot struct {
	ID, Host string
	Time     int64
	Size     uint64
}

// HistoryEntry is one install or rollback of UpdateHistory.
type HistoryEntry struct {
	Time                               int64
	Kind, Version, Ref, Digest, Result string
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
	Users() ([]User, error)
	AddUser(name, fullName string, admin bool, password string) error
	RemoveUser(name string, keepHome bool) error
	SetPassword(name, current, password string) error
	SetAdmin(name string, on bool) error
	FirewallStatus() (Firewall, error)
	SetFirewall(on bool) error
	AllowPort(port uint16, proto, note string) error
	DenyPort(port uint16, proto string) error
	ResetFirewall() error
	AutoUpdateStatus() (AutoStatus, error)
	SetAutoUpdate(on bool) error
	// AddPrinter, RemovePrinter and SetDefaultPrinter change the CUPS
	// queues through lpadmin, behind polkit's printers action.
	AddPrinter(name, uri, info, location string) error
	RemovePrinter(name string) error
	SetDefaultPrinter(name string) error
	BackupInit(target string, opt TargetOptions, cred Credentials) error
	BackupSSHKey() (string, error)
	BackupHostKey(host string, port uint32) ([]string, error)
	BackupList(source string, opt TargetOptions, cred Credentials) ([]Snapshot, error)
	BackupExport(path string) error
	BackupCancel() error
	Restore(source, snapshot, host string, opt TargetOptions, cred Credentials) error
	RecoveryNewKey(current, newKey string) error
	RecoveryReseal(key string) error
	UpdateHistory() ([]HistoryEntry, error)
	// MountShare mounts a network share for the caller (shares.go).
	MountShare(url string, o shares.Options, c shares.Credentials) (string, error)
	UnmountShare(mountpoint string) error
	MountedShares() ([]MountedShare, error)
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
		return i18n.T("Only members of the admin group can do this.")
	case "RebootPending":
		return i18n.T("The last update waits for a restart; restart first.")
	case "Busy":
		return i18n.T("The update service is busy with another job; try again when it ends.")
	case "Exiting":
		return i18n.T("The update service was stopping; try again.")
	case "Refused":
		// The daemon's own sentence says what may not be done.
		return err.Error()
	}
	var dbe dbus.Error
	if errors.As(err, &dbe) && strings.HasPrefix(dbe.Name, "org.freedesktop.DBus.Error.") {
		return i18n.T("The update service (isotope-updated) is not available.")
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
	return d.callCtx(context.Background(), flags, method, out, args...)
}

// Timeouts of the calls that wait for network or disk work, longer than
// D-Bus's default 25 s. BackupInit runs a whole restic init, which takes
// long on a slow remote; the daemon bounds BackupList at 110 s.
const (
	initTimeout = 10 * time.Minute
	longTimeout = 120 * time.Second
)

// callCtx is call with a context: its deadline replaces D-Bus's default
// timeout.
func (d *busDaemon) callCtx(ctx context.Context, flags dbus.Flags, method string, out []any, args ...any) error {
	once := func() error {
		call := d.conn.Object(busName, busPath).CallWithContext(ctx, busInterface+"."+method, flags, args...)
		err := call.Err
		if err == nil && len(out) > 0 {
			err = call.Store(out...)
		}
		return err
	}
	err := once()
	// The service exits when idle; a call that lands during the exit is
	// refused once with Exiting, and the next one starts a new instance.
	// A fd argument is still open here: withSecrets closes it after call.
	if errName(wrapErr(err)) == "Exiting" {
		err = once()
	}
	return wrapErr(err)
}

func (d *busDaemon) callTimeout(timeout time.Duration, method string, out []any, args ...any) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return d.callCtx(ctx, 0, method, out, args...)
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
		Staged: str(m, "staged"), StagedAuto: boolean(m, "staged_auto"),
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
		Available: boolean(m, "available"), Target: str(m, "target"), Label: str(m, "label"),
		Kind: str(m, "kind"), Schedule: str(m, "schedule"),
		Containers: boolean(m, "containers"), Present: boolean(m, "present"), Last: last,
		Result: str(m, "result"), Warning: str(m, "warning"), Restore: str(m, "restore"),
		CanRestore: boolean(m, "can_restore"), RestoreRefusal: str(m, "restore_refusal"),
	}
}

func (d *busDaemon) BackupNow() error { return d.call(0, "BackupNow", nil) }
func (d *busDaemon) SetBackupSchedule(s string) error {
	return d.call(0, "SetBackupSchedule", nil, s)
}

func (d *busDaemon) Users() ([]User, error) {
	var list []map[string]dbus.Variant
	if err := d.call(0, "Users", []any{&list}); err != nil {
		return nil, err
	}
	out := make([]User, 0, len(list))
	for _, m := range list {
		out = append(out, userFromDict(m))
	}
	return out, nil
}

func userFromDict(m map[string]dbus.Variant) User {
	uid, _ := m["uid"].Value().(uint32)
	return User{Name: str(m, "name"), FullName: str(m, "full_name"), Home: str(m, "home"),
		UID: uid, Admin: boolean(m, "admin")}
}

func (d *busDaemon) AddUser(name, fullName string, admin bool, password string) error {
	return withSecrets([]string{password}, func(fd dbus.UnixFD) error {
		return d.call(0, "AddUser", nil, name, fullName, admin, fd)
	})
}

func (d *busDaemon) RemoveUser(name string, keepHome bool) error {
	return d.call(0, "RemoveUser", nil, name, keepHome)
}

// SetPassword sends the current password first, then the new one. The
// daemon reads current only when it must (your own account from a session
// you did not open).
func (d *busDaemon) SetPassword(name, current, password string) error {
	return withSecrets([]string{current, password}, func(fd dbus.UnixFD) error {
		return d.call(0, "SetPassword", nil, name, fd)
	})
}

func (d *busDaemon) SetAdmin(name string, on bool) error {
	return d.call(0, "SetAdmin", nil, name, on)
}

func (d *busDaemon) FirewallStatus() (Firewall, error) {
	var m map[string]dbus.Variant
	var rules []firewall.Rule
	if err := d.call(0, "FirewallStatus", []any{&m, &rules}); err != nil {
		return Firewall{}, err
	}
	return firewallFromDict(m, rules), nil
}

func firewallFromDict(m map[string]dbus.Variant, rules []firewall.Rule) Firewall {
	return Firewall{On: boolean(m, "on"), Active: boolean(m, "active"), Skipped: boolean(m, "skipped"),
		Broken: boolean(m, "broken"), Reason: str(m, "reason"), Rules: rules}
}

func (d *busDaemon) SetFirewall(on bool) error { return d.call(0, "SetFirewall", nil, on) }

func (d *busDaemon) AllowPort(port uint16, proto, note string) error {
	return d.call(0, "AllowPort", nil, port, proto, note)
}

func (d *busDaemon) ResetFirewall() error { return d.call(0, "ResetFirewall", nil) }

func (d *busDaemon) DenyPort(port uint16, proto string) error {
	return d.call(0, "DenyPort", nil, port, proto)
}

func (d *busDaemon) AutoUpdateStatus() (AutoStatus, error) {
	var m map[string]dbus.Variant
	if err := d.call(0, "AutoUpdateStatus", []any{&m}); err != nil {
		return AutoStatus{}, err
	}
	return autoFromDict(m), nil
}

func autoFromDict(m map[string]dbus.Variant) AutoStatus {
	t, _ := m["last_time"].Value().(int64)
	return AutoStatus{Enabled: boolean(m, "enabled"), Broken: str(m, "broken"), LastTime: t,
		LastResult: str(m, "last_result"), LastVersion: str(m, "last_version"), LastDetail: str(m, "last_detail"),
		Staged: str(m, "staged"), StagedAuto: boolean(m, "staged_auto")}
}

func (d *busDaemon) SetAutoUpdate(on bool) error { return d.call(0, "SetAutoUpdate", nil, on) }

// printerTimeout covers AddPrinter, which waits while lpadmin asks the
// printer what it can do (the daemon gives up after 90 s).
const printerTimeout = 100 * time.Second

func (d *busDaemon) AddPrinter(name, uri, info, location string) error {
	return d.callTimeout(printerTimeout, "AddPrinter", nil, name, uri, info, location)
}

func (d *busDaemon) RemovePrinter(name string) error {
	return d.callTimeout(printerTimeout, "RemovePrinter", nil, name)
}

func (d *busDaemon) SetDefaultPrinter(name string) error {
	return d.callTimeout(printerTimeout, "SetDefaultPrinter", nil, name)
}

// optionsDict is TargetOptions as the daemon's a{sv}, without the zero
// values: BackupList and Restore refuse a key they do not take.
func optionsDict(o TargetOptions) map[string]dbus.Variant {
	m := map[string]dbus.Variant{}
	if o.Containers {
		m["containers"] = dbus.MakeVariant(true)
	}
	if o.Existing {
		m["existing"] = dbus.MakeVariant(true)
	}
	if o.Label != "" {
		m["label"] = dbus.MakeVariant(o.Label)
	}
	if o.HostKey != "" {
		m["host_key"] = dbus.MakeVariant(o.HostKey)
	}
	if o.Port != 0 {
		m["port"] = dbus.MakeVariant(o.Port)
	}
	return m
}

func credLines(c Credentials) []string {
	return append([]string{c.Password}, c.Env...)
}

func (d *busDaemon) BackupInit(target string, opt TargetOptions, cred Credentials) error {
	return withSecrets(credLines(cred), func(fd dbus.UnixFD) error {
		return d.callTimeout(initTimeout, "BackupInit", nil, target, optionsDict(opt), fd)
	})
}

func (d *busDaemon) BackupSSHKey() (string, error) {
	var key string
	err := d.call(0, "BackupSSHKey", []any{&key})
	return key, err
}

func (d *busDaemon) BackupHostKey(host string, port uint32) ([]string, error) {
	var fps []string
	err := d.callTimeout(longTimeout, "BackupHostKey", []any{&fps}, host, port)
	return fps, err
}

// BackupList lists the backups in source, newest first. Source "" is this
// machine's target, with the daemon's own password; cred may then be empty.
func (d *busDaemon) BackupList(source string, opt TargetOptions, cred Credentials) ([]Snapshot, error) {
	var list []map[string]dbus.Variant
	err := withSecrets(credLines(cred), func(fd dbus.UnixFD) error {
		return d.callTimeout(longTimeout, "BackupList", []any{&list}, source, optionsDict(opt), fd)
	})
	if err != nil {
		return nil, err
	}
	return snapshotsFromDicts(list), nil
}

func snapshotsFromDicts(list []map[string]dbus.Variant) []Snapshot {
	out := make([]Snapshot, 0, len(list))
	for _, m := range list {
		t, _ := m["time"].Value().(int64)
		size, _ := m["size"].Value().(uint64)
		out = append(out, Snapshot{ID: str(m, "id"), Host: str(m, "host"), Time: t, Size: size})
	}
	return out
}

func (d *busDaemon) BackupExport(path string) error { return d.call(0, "BackupExport", nil, path) }
func (d *busDaemon) BackupCancel() error            { return d.call(0, "BackupCancel", nil) }

// Restore starts a restore as a job; the daemon refuses it on a machine in
// use.
func (d *busDaemon) Restore(source, snapshot, host string, opt TargetOptions, cred Credentials) error {
	return withSecrets(credLines(cred), func(fd dbus.UnixFD) error {
		return d.call(0, "Restore", nil, source, snapshot, host, optionsDict(opt), fd)
	})
}

// RecoveryNewKey sends the current secret (passphrase or recovery key),
// then the new key.
func (d *busDaemon) RecoveryNewKey(current, newKey string) error {
	return withSecrets([]string{current, newKey}, func(fd dbus.UnixFD) error {
		return d.call(0, "RecoveryNewKey", nil, fd)
	})
}

func (d *busDaemon) RecoveryReseal(key string) error {
	return withSecrets([]string{key}, func(fd dbus.UnixFD) error {
		return d.call(0, "RecoveryReseal", nil, fd)
	})
}

func (d *busDaemon) UpdateHistory() ([]HistoryEntry, error) {
	var list []map[string]dbus.Variant
	if err := d.call(0, "UpdateHistory", []any{&list}); err != nil {
		return nil, err
	}
	return historyFromDicts(list), nil
}

func historyFromDicts(list []map[string]dbus.Variant) []HistoryEntry {
	out := make([]HistoryEntry, 0, len(list))
	for _, m := range list {
		t, _ := m["time"].Value().(int64)
		out = append(out, HistoryEntry{Time: t, Kind: str(m, "kind"), Version: str(m, "version"),
			Ref: str(m, "ref"), Digest: str(m, "digest"), Result: str(m, "result")})
	}
	return out
}

// withSecrets passes secrets to the daemon through a pipe: the D-Bus
// message carries the read end as an fd, never the text. It is the twin of
// withSecrets in cmd/isotope/recovery_cli.go: lines joined by newlines, at most
// 4096 bytes.
func withSecrets(secrets []string, call func(dbus.UnixFD) error) error {
	r, w, err := os.Pipe()
	if err != nil {
		return err
	}
	defer r.Close()
	payload := []byte(strings.Join(secrets, "\n") + "\n")
	if len(payload) > 4096 {
		clear(payload)
		w.Close()
		return errors.New(i18n.T("the secret is too long"))
	}
	// A pipe buffer holds at least 4096 bytes (PIPE_BUF; 64 KiB by default
	// on Linux), so this write never blocks.
	_, werr := w.Write(payload)
	clear(payload)
	w.Close()
	if werr != nil {
		return werr
	}
	return call(dbus.UnixFD(r.Fd()))
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
		return nil, nil, fmt.Errorf(i18n.T("subscribing to the update service: %w"), err)
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
