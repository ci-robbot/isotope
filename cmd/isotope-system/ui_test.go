package main

import (
	"errors"
	"fmt"
	"image"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"

	"github.com/kairos-community/isotope/pkg/firewall"
	"github.com/kairos-community/isotope/pkg/guikit"
	"github.com/kairos-community/isotope/pkg/isorelease"
	"github.com/kairos-community/isotope/pkg/recoverykey"
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
	users    []User
	snaps    []Snapshot
	history  []HistoryEntry
	sshKey   string
	hostKeys []string
	// err is the error a method returns, by method name. It overrides
	// the refusals below.
	err map[string]error
	// nonAdmin: the caller is not an administrator, so the methods behind
	// polkit refuse with NotAuthorized. caller is the caller's user name:
	// SetPassword for it is allowed without admin, as in the daemon.
	nonAdmin bool
	caller   string
	// recovery is what RecoveryStatus answers; recReads counts the calls.
	// recCurrent, recNew and recReseal are the secrets the last recovery
	// calls got, for the tests to check (the call strings never hold one).
	recovery                      RecoveryStatus
	recReads                      int
	recCurrent, recNew, recReseal string
	// fw is what FirewallStatus answers; the changes edit it. Active
	// follows On unless fwBroken (the unit fails to load).
	fw       Firewall
	fwBroken bool
	// creds are the credentials of the backup calls, by call, for the
	// tests to check (the call strings never hold one).
	creds []Credentials
}

func newFakeDaemon() *fakeDaemon {
	return &fakeDaemon{
		st:       Status{State: StateUpToDate, Installed: "v1.0.0", Latest: "v1.1.0"},
		tags:     []string{"v1.1.0", "v1.0.0", "v0.9.0"},
		info:     InspectInfo{Ref: "quay.io/x/isotope:sway-v1.1.0", Digest: "sha256:abc", Flavor: "sway", Version: "v1.1.0", Trusted: true},
		events:   make(chan Event, 16),
		caller:   "anna",
		recovery: RecoveryStatus{Edition: "normal", Containers: []Container{{Label: "COS_PERSISTENT_LUKS", Passphrase: 1}}},
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
	f.mu.Lock()
	defer f.mu.Unlock()
	f.recReads++
	return f.recovery, nil
}
func (f *fakeDaemon) BackupStatus() (BackupStatus, error) { return f.backup, nil }
func (f *fakeDaemon) BackupNow() error                    { f.rec("BackupNow"); return nil }
func (f *fakeDaemon) SetBackupSchedule(s string) error {
	f.rec("SetBackupSchedule " + s)
	return nil
}

// secret is how a call string shows a secret, so a test can check that no
// secret is ever in one.
const secret = "<secret>"

// The daemon's refusals, with its sentences (cmd/isotope/dbus.go, daemon.go,
// backup_jobs.go).
var (
	errFakeNotAuthorized = &DaemonError{Name: "NotAuthorized", Msg: "not authorized: only an administrator may change this system"}
	errFakeBusy          = &DaemonError{Name: "Busy", Msg: "another update job is running"}
	errFakeRestoring     = &DaemonError{Name: "Busy", Msg: "a backup is being restored; the machine restarts at the end"}
	errFakeNotCancel     = &DaemonError{Name: "NotCancellable", Msg: "the restore is writing the backup and cannot be cancelled"}
	errFakeNoJob         = &DaemonError{Name: "Failed", Msg: "no job to cancel"}
	errFakeInUse         = &DaemonError{Name: "Refused", Msg: "This machine is already in use; restore parts of a backup with isotope restore --only"}
)

// fakeOpen are the methods the daemon answers without polkit.
var fakeOpen = map[string]bool{"Users": true, "UpdateHistory": true, "FirewallStatus": true}

// refusalLocked is the daemon's answer to method before it does anything,
// in the daemon's order: polkit, then the machine-in-use check, then the
// job slot. f.mu is held.
func (f *fakeDaemon) refusalLocked(method string, self bool) error {
	if err, ok := f.err[method]; ok {
		return err
	}
	if f.nonAdmin && !fakeOpen[method] && !self {
		return errFakeNotAuthorized
	}
	state := f.st.State
	switch method {
	case "AddUser", "RemoveUser", "SetPassword", "SetAdmin":
		// They write /oem, which a restore replaces; other jobs do not
		// touch it.
		if state == StateRestore {
			return errFakeRestoring
		}
	case "BackupInit":
		// A backup or a restore writes the backup settings.
		if state == StateBackup || state == StateRestore {
			return errFakeBusy
		}
	case "Restore":
		if !f.backup.CanRestore {
			return errFakeInUse
		}
		if f.st.Busy() {
			return errFakeBusy
		}
	case "BackupExport":
		if f.st.Busy() {
			return errFakeBusy
		}
	case "BackupCancel":
		if state == StateRestore {
			return errFakeNotCancel
		}
		if state != StateBackup {
			return errFakeNoJob
		}
	}
	return nil
}

// do records a call and returns the daemon's answer to it.
func (f *fakeDaemon) do(method string, args ...string) error {
	f.rec(strings.Join(append([]string{method}, args...), " "))
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.refusalLocked(method, false)
}

// change is do for a method that changes f.users: apply runs, with f.mu
// held, only when the call is not refused.
func (f *fakeDaemon) change(method string, self bool, apply func() error, args ...string) error {
	f.rec(strings.Join(append([]string{method}, args...), " "))
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.refusalLocked(method, self); err != nil {
		return err
	}
	return apply()
}

// userLocked is the index of name in f.users, or -1.
func (f *fakeDaemon) userLocked(name string) int {
	for i, u := range f.users {
		if u.Name == name {
			return i
		}
	}
	return -1
}

func fmtOpts(o TargetOptions) string { return fmt.Sprintf("%+v", o) }

// fmtCred shows one secret per line of the pipe, never their text.
func fmtCred(c Credentials) string {
	return strings.TrimSpace(strings.Repeat(secret+" ", 1+len(c.Env)))
}

func (f *fakeDaemon) Users() ([]User, error) {
	err := f.do("Users")
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]User(nil), f.users...), err
}
func (f *fakeDaemon) AddUser(name, fullName string, admin bool, password string) error {
	return f.change("AddUser", false, func() error {
		if f.userLocked(name) >= 0 {
			return &DaemonError{Name: "Refused", Msg: "the user " + name + " exists"}
		}
		uid := uint32(1000)
		for _, u := range f.users {
			uid = max(uid, u.UID+1)
		}
		f.users = append(f.users, User{Name: name, FullName: fullName, Home: "/home/" + name, UID: uid, Admin: admin})
		return nil
	}, name, fullName, fmt.Sprint(admin), secret)
}
func (f *fakeDaemon) RemoveUser(name string, keepHome bool) error {
	return f.change("RemoveUser", false, func() error {
		i := f.userLocked(name)
		if i < 0 {
			return &DaemonError{Name: "Refused", Msg: "there is no user " + name}
		}
		f.users = append(f.users[:i:i], f.users[i+1:]...)
		return nil
	}, name, fmt.Sprint(keepHome))
}
func (f *fakeDaemon) SetPassword(name, current, password string) error {
	f.mu.Lock()
	self := f.caller != "" && name == f.caller
	f.mu.Unlock()
	return f.change("SetPassword", self, func() error { return nil }, name, secret, secret)
}
func (f *fakeDaemon) SetAdmin(name string, on bool) error {
	return f.change("SetAdmin", false, func() error {
		i := f.userLocked(name)
		if i < 0 {
			return &DaemonError{Name: "Refused", Msg: "there is no user " + name}
		}
		f.users[i].Admin = on
		return nil
	}, name, fmt.Sprint(on))
}
func (f *fakeDaemon) FirewallStatus() (Firewall, error) {
	err := f.do("FirewallStatus")
	f.mu.Lock()
	defer f.mu.Unlock()
	fw := f.fw
	fw.Rules = append([]firewall.Rule(nil), f.fw.Rules...)
	if !fw.Skipped && !fw.Broken {
		fw.Active = fw.On && !f.fwBroken
		if fw.On && !fw.Active {
			fw.Reason = "isotope-firewall.service did not load the rules: see `systemctl status isotope-firewall`."
		}
	}
	return fw, err
}
func (f *fakeDaemon) ResetFirewall() error {
	return f.change("ResetFirewall", false, func() error {
		if len(f.fw.Rules) == 0 {
			f.fw.Rules = append([]firewall.Rule(nil), firewall.Defaults...)
		}
		f.fw.On, f.fw.Broken, f.fw.Reason = true, false, ""
		return nil
	})
}
func (f *fakeDaemon) SetFirewall(on bool) error {
	return f.change("SetFirewall", false, func() error { f.fw.On = on; return nil }, fmt.Sprint(on))
}
func (f *fakeDaemon) AllowPort(port uint16, proto, note string) error {
	return f.change("AllowPort", false, func() error {
		f.fw.Rules = append(f.fw.Rules, firewall.Rule{Port: port, Proto: proto, Note: note})
		return nil
	}, fmt.Sprint(port), proto, note)
}
func (f *fakeDaemon) DenyPort(port uint16, proto string) error {
	return f.change("DenyPort", false, func() error {
		for i, r := range f.fw.Rules {
			if r.Port == port && r.Proto == proto {
				f.fw.Rules = append(f.fw.Rules[:i], f.fw.Rules[i+1:]...)
				return nil
			}
		}
		return &DaemonError{Name: "Failed", Msg: "not allowed"}
	}, fmt.Sprint(port), proto)
}
func (f *fakeDaemon) keepCred(c Credentials) {
	f.mu.Lock()
	f.creds = append(f.creds, Credentials{Password: c.Password, Env: append([]string(nil), c.Env...)})
	f.mu.Unlock()
}

func (f *fakeDaemon) lastCred() Credentials {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.creds) == 0 {
		return Credentials{}
	}
	return f.creds[len(f.creds)-1]
}

func (f *fakeDaemon) BackupInit(target string, opt TargetOptions, cred Credentials) error {
	f.keepCred(cred)
	return f.do("BackupInit", target, fmtOpts(opt), fmtCred(cred))
}
func (f *fakeDaemon) BackupSSHKey() (string, error) {
	err := f.do("BackupSSHKey")
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.sshKey, err
}
func (f *fakeDaemon) BackupHostKey(host string, port uint32) ([]string, error) {
	err := f.do("BackupHostKey", host, fmt.Sprint(port))
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.hostKeys...), err
}
func (f *fakeDaemon) BackupList(source string, opt TargetOptions, cred Credentials) ([]Snapshot, error) {
	f.keepCred(cred)
	err := f.do("BackupList", source, fmtOpts(opt), fmtCred(cred))
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Snapshot(nil), f.snaps...), err
}
func (f *fakeDaemon) BackupExport(path string) error { return f.do("BackupExport", path) }
func (f *fakeDaemon) BackupCancel() error            { return f.do("BackupCancel") }
func (f *fakeDaemon) Restore(source, snapshot, host string, opt TargetOptions, cred Credentials) error {
	f.keepCred(cred)
	return f.do("Restore", source, snapshot, host, fmtOpts(opt), fmtCred(cred))
}
func (f *fakeDaemon) RecoveryNewKey(current, newKey string) error {
	f.mu.Lock()
	f.recCurrent, f.recNew = current, newKey
	f.mu.Unlock()
	return f.do("RecoveryNewKey", secret, secret)
}
func (f *fakeDaemon) RecoveryReseal(key string) error {
	f.mu.Lock()
	f.recReseal = key
	f.mu.Unlock()
	return f.do("RecoveryReseal", secret)
}
func (f *fakeDaemon) UpdateHistory() ([]HistoryEntry, error) {
	err := f.do("UpdateHistory")
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]HistoryEntry(nil), f.history...), err
}

func (f *fakeDaemon) Watch() (<-chan Event, func(), error) {
	return f.events, func() {}, nil
}

type fakeNet struct {
	mu    sync.Mutex
	st    NetState
	aps   []AccessPoint
	vpns  []VPN
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
func (n *fakeNet) VPNs() ([]VPN, error)     { return n.vpns, nil }
func (n *fakeNet) ActivateVPN(u string) error {
	n.rec("ActivateVPN " + u)
	return nil
}
func (n *fakeNet) DeactivateVPN(u string) error {
	n.rec("DeactivateVPN " + u)
	return nil
}
func (n *fakeNet) RemoveVPN(u string) error { n.rec("RemoveVPN " + u); return nil }
func (n *fakeNet) ImportWireGuard(name string, conf []byte) error {
	n.rec("ImportWireGuard " + name + " " + strconv.Itoa(len(conf)))
	return nil
}
func (n *fakeNet) AddOpenConnect(o OpenConnect) error {
	n.rec("AddOpenConnect " + o.Name + "|" + o.Gateway + "|" + o.Protocol + "|" + o.User)
	return nil
}

// fakeDisks is udisks2 for the page tests. It refuses a Format as the
// client does (formatCheck): a system drive, a mounted filesystem.
type fakeDisks struct {
	mu     sync.Mutex
	drives []Drive
	calls  []string
	// errs is the error a call returns, by the whole call ("Unmount
	// /org/...") or by method name ("Unmount").
	errs map[string]error
	// roots are the root devices of the fake system.
	roots []string
	// stacked: a device (/dev/sdb1) and what is mounted on a device
	// stacked on it ("/dev/dm-1 at /mnt"), as hostView.mountedAbove says.
	stacked map[string]string
	changed chan struct{}
}

const fakeUSB = "/org/freedesktop/UDisks2/drives/SanDisk_Cruzer"

// newFakeDisks has a USB stick with one mounted exFAT partition and the
// internal Kairos disk.
func newFakeDisks() *fakeDisks {
	bd := "/org/freedesktop/UDisks2/block_devices/"
	return &fakeDisks{
		drives: []Drive{
			{ID: fakeUSB, Model: "Cruzer", Vendor: "SanDisk", Serial: "4C53", Size: 32e9, Removable: true, Ejectable: true, CanPowerOff: true,
				Blocks: []Block{
					{ID: bd + "sdb", Device: "/dev/sdb", Size: 32e9},
					{ID: bd + "sdb1", Device: "/dev/sdb1", Label: "USB", FSType: "exfat", UUID: "1234-ABCD", Size: 32e9, Used: 4e9,
						MountPoints: []string{"/run/media/anna/USB"}, IsFilesystem: true, IsPartition: true},
				}},
			{ID: "/org/freedesktop/UDisks2/drives/Samsung_SSD", Model: "Samsung SSD", Size: 512e9,
				Blocks: []Block{
					{ID: bd + "nvme0n1", Device: "/dev/nvme0n1", Size: 512e9, HintSystem: true},
					{ID: bd + "nvme0n1p1", Device: "/dev/nvme0n1p1", Label: "COS_GRUB", FSType: "vfat", UUID: "A", Size: 64e6, IsFilesystem: true, IsPartition: true, HintSystem: true},
					{ID: bd + "nvme0n1p2", Device: "/dev/nvme0n1p2", Label: "COS_STATE", FSType: "ext4", UUID: "B", Size: 8e9, IsFilesystem: true, IsPartition: true, HintSystem: true},
					{ID: bd + "nvme0n1p3", Device: "/dev/nvme0n1p3", Label: "COS_PERSISTENT", FSType: "ext4", UUID: "C", Size: 500e9, IsFilesystem: true, IsPartition: true, HintSystem: true,
						MountPoints: []string{"/usr/local"}},
				}},
		},
		roots:   []string{"/dev/nvme0n1p3"},
		changed: make(chan struct{}, 1),
	}
}

// call records c and returns its error.
func (f *fakeDisks) call(method, c string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, c)
	if err, ok := f.errs[c]; ok {
		return err
	}
	return f.errs[method]
}

func (f *fakeDisks) called() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

// block runs do on the block id, under the lock.
func (f *fakeDisks) block(id string, do func(b *Block)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := range f.drives {
		for j := range f.drives[i].Blocks {
			if f.drives[i].Blocks[j].ID == id {
				do(&f.drives[i].Blocks[j])
			}
		}
	}
	select {
	case f.changed <- struct{}{}:
	default:
	}
}

func (f *fakeDisks) Drives() ([]Drive, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]Drive, len(f.drives))
	for i, d := range f.drives {
		d.Blocks = append([]Block(nil), d.Blocks...)
		for j := range d.Blocks {
			d.Blocks[j].MountPoints = append([]string(nil), d.Blocks[j].MountPoints...)
		}
		out[i] = d
	}
	return out, f.errs["Drives"]
}

// systemBlock: block is on a system drive. udisks2 asks for
// filesystem-mount-system there, which no Isotope rule grants.
func (f *fakeDisks) systemBlock(block string) bool {
	ds, _ := f.Drives()
	for _, d := range ds {
		for _, b := range d.Blocks {
			if b.ID == block && systemDrive(d, f.roots) {
				return true
			}
		}
	}
	return false
}

func (f *fakeDisks) Mount(block string) (string, error) {
	if err := f.call("Mount", "Mount "+block); err != nil {
		return "", err
	}
	if f.systemBlock(block) {
		return "", errors.New("Not authorized to perform operation")
	}
	var mp string
	f.block(block, func(b *Block) {
		mp = "/run/media/anna/" + b.Label
		b.MountPoints = []string{mp}
	})
	return mp, nil
}
func (f *fakeDisks) Unmount(block string) error {
	if err := f.call("Unmount", "Unmount "+block); err != nil {
		return err
	}
	f.block(block, func(b *Block) { b.MountPoints = nil; b.Used = 0 })
	return nil
}
func (f *fakeDisks) Eject(drive string) error    { return f.call("Eject", "Eject "+drive) }
func (f *fakeDisks) PowerOff(drive string) error { return f.call("PowerOff", "PowerOff "+drive) }
func (f *fakeDisks) Format(drive, fsType, label string) error {
	if err := f.call("Format", "Format "+drive+" "+fsType+" "+label); err != nil {
		return err
	}
	ds, err := f.Drives()
	if err != nil {
		return err
	}
	above := func(devs []string) (string, error) {
		f.mu.Lock()
		defer f.mu.Unlock()
		for _, d := range devs {
			if w := f.stacked[d]; w != "" {
				return w, nil
			}
		}
		return "", nil
	}
	if _, err := formatCheck(ds, drive, fsType, f.roots, above); err != nil {
		return err
	}
	f.mu.Lock()
	for i := range f.drives {
		if f.drives[i].ID == drive {
			whole := f.drives[i].Blocks[0]
			f.drives[i].Blocks = []Block{whole, {ID: whole.ID + "1", Device: whole.Device + "1", Label: label, FSType: fsType,
				UUID: "NEW-UUID", Size: whole.Size, IsFilesystem: true, IsPartition: true}}
		}
	}
	f.mu.Unlock()
	return nil
}
func (f *fakeDisks) Watch() (<-chan struct{}, func(), error) {
	return f.changed, func() {}, nil
}

func testSystem(admin bool) System {
	return System{
		Release: isorelease.Release{Flavor: "sway", Version: "v1.0.0", HadronVersion: "v0.6.3",
			ImageRepo: "quay.io/x/isotope", GitHubRepo: "kairos-community/isotope", GPU: isorelease.GPUFull},
		Desktop: "Sway", Admin: admin, Encrypted: true, Kernel: "6.12.0", Hostname: "anna-laptop", User: "anna",
	}
}

func testUI(t *testing.T, admin bool) (*UI, *fakeDaemon, *fakeNet) {
	t.Helper()
	d := newFakeDaemon()
	d.nonAdmin = !admin
	d.users = []User{
		{Name: "anna", FullName: "Anna Rossi", Home: "/home/anna", UID: 1000, Admin: admin},
		{Name: "bob", FullName: "Bob", Home: "/home/bob", UID: 1001},
		{Name: "carla", FullName: "", Home: "/home/carla", UID: 1002, Admin: true},
	}
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
	// Long, for loaded builds: it returns as soon as cond holds.
	deadline := time.Now().Add(30 * time.Second)
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
	// Wait for this install itself: other calls (the pages' first loads) can
	// come first, and the forced install must not overtake it.
	eventually(t, "the install", func() bool {
		d.mu.Lock()
		defer d.mu.Unlock()
		return len(d.forceSet) == 1
	})
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
	p.started = jobBackup
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

func TestAskFormFocusesTheGivenChoice(t *testing.T) {
	u, _, _ := testUI(t, true)
	u.askForm("Erase?", "body", nil, 0, choice{Label: "Cancel"}, choice{Label: "Erase and format", Kind: guikit.ButtonDanger})
	d := waitQuestion(t, u, "Erase?")
	if d.focus != 0 {
		t.Fatalf("focus %d, want 0 (Cancel)", d.focus)
	}
}

func TestPickIgnoresADisabledChoice(t *testing.T) {
	u, _, _ := testUI(t, true)
	ran := false
	u.askForm("Q", "", nil, 0, choice{Label: "Cancel"}, choice{Label: "Go", Do: func() { ran = true }, Enabled: func() bool { return false }})
	d := waitQuestion(t, u, "Q")
	u.pick(d, 1)
	if ran || u.question() == nil {
		t.Fatal("a disabled choice was picked")
	}
}

// usersPageOf opens the Users page and waits for its list.
func usersPageOf(t *testing.T, u *UI) *usersPage {
	t.Helper()
	p := u.view(PageUsers).(*usersPage)
	p.Shown()
	eventually(t, "the users", func() bool { u.mu.Lock(); defer u.mu.Unlock(); return p.loaded })
	return p
}

func typeInto(f *guikit.Field, s string) { f.Editor.SetText(s) }

func TestUsersPageAddsAUser(t *testing.T) {
	u, d, _ := testUI(t, true)
	p := usersPageOf(t, u)
	p.openAdd()
	q := waitQuestion(t, u, "Add a user")
	typeInto(p.add.name, "bob2")
	typeInto(p.add.full, "Bob")
	typeInto(p.add.pw, "hunter2")
	typeInto(p.add.pw2, "hunter2")
	p.add.admin = true
	u.pick(q, len(q.choices)-1)
	eventually(t, "AddUser", func() bool { return strings.Contains(strings.Join(d.called(), ","), "AddUser bob2 Bob true "+secret) })
	eventually(t, "the new row", func() bool {
		u.mu.Lock()
		defer u.mu.Unlock()
		return len(p.users) == 4
	})
	if p.add.pw.Editor.Text() != "" || p.add.pw2.Editor.Text() != "" {
		t.Fatal("the password stays in the form")
	}
}

func TestUsersPageAddIsOffUntilTheFormIsValid(t *testing.T) {
	u, d, _ := testUI(t, true)
	p := usersPageOf(t, u)
	p.openAdd()
	q := waitQuestion(t, u, "Add a user")
	add := q.choices[len(q.choices)-1]
	if add.enabled() {
		t.Fatal("Add is on for an empty form")
	}
	typeInto(p.add.name, "bob2")
	typeInto(p.add.pw, "a")
	typeInto(p.add.pw2, "b")
	if add.enabled() {
		t.Fatal("Add is on for passwords that differ")
	}
	typeInto(p.add.pw2, "a")
	if !add.enabled() {
		t.Fatal("Add is off for a valid form")
	}
	for _, bad := range []string{"Bob", "1bob", "bo b", "bob.x", strings.Repeat("a", 33)} {
		typeInto(p.add.name, bad)
		if add.enabled() {
			t.Errorf("Add is on for the name %q", bad)
		}
	}
	typeInto(p.add.name, "bob")
	if add.enabled() {
		t.Error("Add is on for a name that exists")
	}
	typeInto(p.add.name, "ok")
	// A single-line field drops line breaks itself; the rule still holds.
	if passwordProblem("a\nb") == "" || passwordProblem(strings.Repeat("x", 257)) == "" || passwordProblem(strings.Repeat("x", 256)) != "" {
		t.Error("passwordProblem")
	}
	typeInto(p.add.pw, "")
	typeInto(p.add.pw2, "")
	u.pick(q, len(q.choices)-1)
	if len(d.called()) != 0 && strings.Contains(strings.Join(d.called(), ","), "AddUser") {
		t.Fatal("a disabled Add called the daemon")
	}
}

func TestUsersPageChangesYourOwnPassword(t *testing.T) {
	u, d, _ := testUI(t, false)
	p := usersPageOf(t, u)
	p.openPassword("anna")
	q := waitQuestion(t, u, "Change your password")
	if p.pw.cur == nil || !p.pw.own {
		t.Fatal("no Current password field on your own row")
	}
	last := q.choices[len(q.choices)-1]
	typeInto(p.pw.newPw, "n1")
	typeInto(p.pw.again, "n1")
	if last.enabled() {
		t.Fatal("Change is on without the current password")
	}
	typeInto(p.pw.cur, "old")
	u.pick(q, len(q.choices)-1)
	eventually(t, "SetPassword", func() bool {
		return strings.Contains(strings.Join(d.called(), ","), "SetPassword anna "+secret+" "+secret)
	})
	eventually(t, "the form is cleared", func() bool { return p.pw.newPw.Editor.Text() == "" && p.pw.cur.Editor.Text() == "" })
}

func TestUsersPageOtherRowPasswordHasNoCurrentField(t *testing.T) {
	u, _, _ := testUI(t, true)
	p := usersPageOf(t, u)
	p.openPassword("bob")
	waitQuestion(t, u, "Change the password of bob")
	if p.pw.own {
		t.Fatal("Current password asked for another user")
	}
}

func TestUsersPageRemoveKeepsTheHomeByDefault(t *testing.T) {
	u, d, _ := testUI(t, true)
	p := usersPageOf(t, u)
	p.openRemove("bob")
	q := waitQuestion(t, u, "Remove bob")
	if last := q.choices[len(q.choices)-1]; last.Label != "Remove bob" || last.Kind != guikit.ButtonDanger {
		t.Fatalf("last choice %+v", last)
	}
	u.pick(q, len(q.choices)-1)
	eventually(t, "RemoveUser", func() bool { return strings.Contains(strings.Join(d.called(), ","), "RemoveUser bob true") })
	eventually(t, "the row goes", func() bool { u.mu.Lock(); defer u.mu.Unlock(); return len(p.users) == 2 })
}

func TestUsersPageRemoveCanDropTheHome(t *testing.T) {
	u, d, _ := testUI(t, true)
	p := usersPageOf(t, u)
	p.openRemove("bob")
	q := waitQuestion(t, u, "Remove bob")
	p.rm.keep = false
	u.pick(q, len(q.choices)-1)
	eventually(t, "RemoveUser", func() bool { return strings.Contains(strings.Join(d.called(), ","), "RemoveUser bob false") })
}

func TestUsersPageNonAdminSeesNoAdminButtons(t *testing.T) {
	u, _, _ := testUI(t, false)
	p := usersPageOf(t, u)
	_, trailing := p.Footer()
	for _, a := range trailing {
		if !a.Disabled {
			t.Fatalf("footer action %q is on for a non-admin", a.Label)
		}
	}
	if got := p.actionsOf(User{Name: "bob"}); len(got) != 0 {
		t.Fatalf("%d actions on another user's row", len(got))
	}
	got := p.actionsOf(User{Name: "anna"})
	if len(got) != 1 || got[0].Label != "Change your password" {
		t.Fatalf("own row: %+v", got)
	}
	// Even called directly, the admin dialogs do not open.
	p.openRemove("bob")
	p.openAdd()
	time.Sleep(20 * time.Millisecond)
	if u.question() != nil {
		t.Fatal("a non-admin opened an admin dialog")
	}
}

func TestUsersPageAdminRowActions(t *testing.T) {
	u, _, _ := testUI(t, true)
	p := usersPageOf(t, u)
	own := p.actionsOf(User{Name: "anna", Admin: true})
	for _, a := range own {
		if a.Label == "Remove…" && !a.Disabled {
			t.Fatal("Remove is on for your own row")
		}
	}
	if got := p.actionsOf(User{Name: "bob"}); len(got) != 3 {
		t.Fatalf("%d actions on another row", len(got))
	}
}

func TestUsersPageShowsTheDaemonsRefusal(t *testing.T) {
	u, d, _ := testUI(t, true)
	d.mu.Lock()
	d.err = map[string]error{"SetAdmin": &DaemonError{Name: "Refused", Msg: "this is the last administrator; make someone else one first"}}
	d.mu.Unlock()
	p := usersPageOf(t, u)
	p.setAdmin("carla", true) // already one: no ask, goes through
	q := waitQuestion(t, u, "Could not change")
	if !strings.Contains(q.body, "this is the last administrator") {
		t.Fatalf("body %q", q.body)
	}
}

func TestUsersPageTurningAdminOffAsksFirst(t *testing.T) {
	u, d, _ := testUI(t, true)
	p := usersPageOf(t, u)
	p.setAdmin("carla", false)
	q := waitQuestion(t, u, "Remove carla from the administrators?")
	if q.focus != 0 {
		t.Fatalf("focus %d, want 0 (Cancel)", q.focus)
	}
	if strings.Contains(strings.Join(d.called(), ","), "SetAdmin") {
		t.Fatal("called before the answer")
	}
	u.pick(q, len(q.choices)-1)
	eventually(t, "SetAdmin", func() bool { return strings.Contains(strings.Join(d.called(), ","), "SetAdmin carla false") })
}

func TestUsersPageListsFullNamesAndYou(t *testing.T) {
	u, _, _ := testUI(t, true)
	p := usersPageOf(t, u)
	rows := p.rowTexts()
	want := []string{"Anna Rossi|anna · Administrator|You", "Bob|bob|", "carla|carla · Administrator|"}
	if strings.Join(rows, ";") != strings.Join(want, ";") {
		t.Fatalf("rows %q", rows)
	}
}

func TestUsersDialogsDrawAtBothSizes(t *testing.T) {
	for _, sz := range []image.Point{{1100, 720}, {700, 500}} {
		u, _, _ := testUI(t, true)
		p := usersPageOf(t, u)
		for _, open := range []func(){p.openAdd, func() { p.openPassword("anna") }, func() { p.openPassword("bob") }, func() { p.openRemove("bob") }} {
			open()
			if dims := u.Layout(gtxOf(sz.X, sz.Y)); dims.Size != sz {
				t.Errorf("at %v: %v", sz, dims.Size)
			}
			u.closeDialog()
		}
	}
}

func TestUsersPageSelfDemotionWarnsInTheSecondPerson(t *testing.T) {
	u, _, _ := testUI(t, true)
	p := usersPageOf(t, u)
	p.setAdmin("anna", false)
	q := waitQuestion(t, u, "Remove anna from the administrators?")
	if !strings.Contains(q.body, "You will no longer be able to change this system.") {
		t.Fatalf("body %q", q.body)
	}
}

func TestUsersPageCancelClearsTheTypedPasswords(t *testing.T) {
	u, d, _ := testUI(t, true)
	p := usersPageOf(t, u)
	p.openAdd()
	q := waitQuestion(t, u, "Add a user")
	typeInto(p.add.pw, "x")
	typeInto(p.add.pw2, "x")
	u.pick(q, 0)
	if p.add.pw.Editor.Text() != "" || p.add.pw2.Editor.Text() != "" {
		t.Fatal("Add form keeps the password after Cancel")
	}
	p.openPassword("anna")
	q = waitQuestion(t, u, "Change your password")
	typeInto(p.pw.cur, "x")
	typeInto(p.pw.newPw, "y")
	typeInto(p.pw.again, "y")
	u.pick(q, 0)
	if p.pw.cur.Editor.Text() != "" || p.pw.newPw.Editor.Text() != "" || p.pw.again.Editor.Text() != "" {
		t.Fatal("Change password form keeps the password after Cancel")
	}
	if c := strings.Join(d.called(), ","); strings.Contains(c, "SetPassword") || strings.Contains(c, "AddUser") {
		t.Fatalf("calls %v", c)
	}
}

// securityPageOf opens the Security page and waits for the disk's keys.
func securityPageOf(t *testing.T, u *UI) *securityPage {
	t.Helper()
	p := u.view(PageSecurity).(*securityPage)
	p.Shown()
	eventually(t, "the disk's keys", func() bool { u.mu.Lock(); defer u.mu.Unlock(); return p.loaded })
	return p
}

func (f *fakeDaemon) recoverySecrets() (current, newKey, reseal string, reads int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.recCurrent, f.recNew, f.recReseal, f.recReads
}

var trustedBootTPM = RecoveryStatus{Edition: "trusted-boot", Containers: []Container{
	{Label: "COS_OEM_LUKS", TPM: 1, Recovery: 1, TPMWorks: true},
	{Label: "COS_PERSISTENT_LUKS", TPM: 1, Recovery: 1, TPMWorks: true},
}}

// trustedBootStale is Trusted Boot after a firmware update: the TPM no
// longer unlocks the persistent container.
var trustedBootStale = RecoveryStatus{Edition: "trusted-boot", Containers: []Container{
	{Label: "COS_OEM_LUKS", TPM: 1, Recovery: 1, TPMWorks: true},
	{Label: "COS_PERSISTENT_LUKS", TPM: 1, Recovery: 1},
}}

// shownKey passes the key dialog's first step and returns the key it showed
// and its second step.
func shownKey(t *testing.T, u *UI) (string, *dialog) {
	t.Helper()
	return shownKeyTitled(t, u, "Your new recovery key")
}

func shownKeyTitled(t *testing.T, u *UI, title string) (string, *dialog) {
	t.Helper()
	q := waitQuestion(t, u, title)
	if q.key == nil {
		t.Fatal("not a key dialog")
	}
	key := q.key.secret
	if key == "" || recoverykey.Canonical(key) != key {
		t.Fatalf("shown key %q", key)
	}
	u.pick(q, len(q.choices)-1) // I saved it
	c := u.question()
	if c == nil || c == q || c.key != q.key {
		t.Fatal("no second step")
	}
	if c.focus != 0 {
		t.Fatalf("focus %d, want 0 (Cancel)", c.focus)
	}
	if g := c.key.group; g < 2 || g > 7 || !strings.Contains(c.body, fmt.Sprintf("group %d of 8", g)) {
		t.Fatalf("group %d, body %q", g, c.body)
	}
	return key, c
}

func groupOf(key string, n int) string { return strings.Split(key, "-")[n-1] }

func TestNewRecoveryKeyAsksTheCurrentSecretThenConfirms(t *testing.T) {
	u, d, _ := testUI(t, true)
	p := securityPageOf(t, u)
	p.openNewKey()
	q := waitQuestion(t, u, "New recovery key")
	if q.focus != 0 {
		t.Fatalf("focus %d, want 0 (Cancel)", q.focus)
	}
	if q.choices[len(q.choices)-1].enabled() {
		t.Fatal("Continue is on with nothing typed")
	}
	typeInto(p.cur, "old pass")
	u.pick(q, len(q.choices)-1)
	if p.cur.Editor.Text() != "" {
		t.Fatal("the current secret stays in the field")
	}
	key, c := shownKey(t, u)
	k := c.key
	typeInto(k.typed, strings.ToUpper(groupOf(key, k.group)))
	u.pick(c, len(c.choices)-1)
	eventually(t, "RecoveryNewKey", func() bool {
		return strings.Contains(strings.Join(d.called(), ","), "RecoveryNewKey "+secret+" "+secret)
	})
	cur, nk, _, _ := d.recoverySecrets()
	if cur != "old pass" || nk != key {
		t.Fatalf("sent %q %q, want the typed secret and %q", cur, nk, key)
	}
	if k.secret != "" || k.typed.Editor.Text() != "" {
		t.Fatal("the key stays in the dialog")
	}
	waitQuestion(t, u, "Recovery key replaced")
	eventually(t, "the keys read again", func() bool { _, _, _, n := d.recoverySecrets(); return n >= 2 })
}

// A failed RecoveryNewKey says which key works. A refusal before the daemon
// changed the disk: the old one. After the new key went to part of the disk
// (PartlyChanged), or after a failure the app cannot place: keep both.
func TestNewRecoveryKeyFailureSaysWhichKeyWorks(t *testing.T) {
	const old = "The key you wrote down does not work; your old key still does.\n"
	const both = "Keep both your old key and the key you wrote down: some parts of the disk may already use the new key.\n"
	for _, c := range []struct {
		err  error
		want string
	}{
		{&DaemonError{Name: "Refused", Msg: "what you typed does not open COS_OEM_LUKS"}, old},
		{&DaemonError{Name: "NotAuthorized", Msg: "not authorized"}, old},
		{&DaemonError{Name: "Busy", Msg: "another update job is running"}, old},
		{&DaemonError{Name: "PartlyChanged", Msg: "COS_PERSISTENT_LUKS: boom; the new key was already added to the disk, keep both keys"}, both},
		{&DaemonError{Name: "Failed", Msg: "cryptsetup failed"}, both},
		{errors.New("no reply"), both},
	} {
		u, d, _ := testUI(t, true)
		d.mu.Lock()
		d.err = map[string]error{"RecoveryNewKey": c.err}
		d.mu.Unlock()
		p := securityPageOf(t, u)
		p.openNewKey()
		q := waitQuestion(t, u, "New recovery key")
		typeInto(p.cur, "old pass")
		u.pick(q, len(q.choices)-1)
		key, k := shownKey(t, u)
		typeInto(k.key.typed, groupOf(key, k.key.group))
		u.pick(k, len(k.choices)-1)
		f := waitQuestion(t, u, "The recovery key was not replaced")
		if !strings.HasPrefix(f.body, c.want) {
			t.Errorf("%v: body %q, want it to start with %q", c.err, f.body, c.want)
		}
	}
}

func TestNewRecoveryKeyWrongGroupDoesNothing(t *testing.T) {
	u, d, _ := testUI(t, true)
	p := securityPageOf(t, u)
	p.openNewKey()
	q := waitQuestion(t, u, "New recovery key")
	typeInto(p.cur, "old pass")
	u.pick(q, len(q.choices)-1)
	key, c := shownKey(t, u)
	k := c.key
	wrong := "zzzzzzzz"
	if other := groupOf(key, 1); other != groupOf(key, k.group) {
		wrong = other
	}
	typeInto(k.typed, wrong)
	if c.choices[len(c.choices)-1].enabled() {
		t.Fatal("Confirm is on for a wrong group")
	}
	u.pick(c, len(c.choices)-1)
	if u.question() != c {
		t.Fatal("a wrong group closed the dialog")
	}
	u.pick(c, 0) // Cancel
	time.Sleep(20 * time.Millisecond)
	if strings.Contains(strings.Join(d.called(), ","), "RecoveryNewKey") {
		t.Fatal("RecoveryNewKey called")
	}
	if k.secret != "" || k.typed.Editor.Text() != "" || p.cur.Editor.Text() != "" {
		t.Fatal("Cancel left a secret behind")
	}
	// Cancel at the first step runs nothing either.
	p.openNewKey()
	q = waitQuestion(t, u, "New recovery key")
	typeInto(p.cur, "old pass")
	u.pick(q, 0)
	if p.cur.Editor.Text() != "" || u.question() != nil {
		t.Fatal("Cancel left the secret or a dialog")
	}
}

func TestNewRecoveryKeySkipsTheSecretWhenTheTPMUnlocks(t *testing.T) {
	u, d, _ := testUI(t, true)
	d.recovery = trustedBootTPM
	p := securityPageOf(t, u)
	p.openNewKey()
	key, c := shownKey(t, u)
	typeInto(c.key.typed, groupOf(key, c.key.group))
	u.pick(c, len(c.choices)-1)
	eventually(t, "RecoveryNewKey", func() bool { return strings.Contains(strings.Join(d.called(), ","), "RecoveryNewKey") })
	if cur, nk, _, _ := d.recoverySecrets(); cur != "" || nk != key {
		t.Fatalf("sent %q %q", cur, nk)
	}

	// The TPM fails on one container: the recovery key is asked for.
	u2, d2, _ := testUI(t, true)
	d2.recovery = trustedBootStale
	p2 := securityPageOf(t, u2)
	p2.openNewKey()
	q := waitQuestion(t, u2, "New recovery key")
	if q.key != nil {
		t.Fatal("no secret asked while the TPM fails")
	}
}

func TestResealOnlyWhenTheTPMFails(t *testing.T) {
	for _, c := range []struct {
		st    RecoveryStatus
		offer bool
		note  string
	}{
		{RecoveryStatus{Edition: "normal", Containers: []Container{{Label: "COS_PERSISTENT_LUKS", Passphrase: 1}}}, false, ""},
		{trustedBootTPM, false, "The TPM unlocks the disk; nothing to reseal"},
		{trustedBootStale, true, ""},
	} {
		if offer, note := resealView(c.st); offer != c.offer || note != c.note {
			t.Errorf("%+v: %v %q", c.st, offer, note)
		}
	}
	if !tpmUnlocksAll(trustedBootTPM) || tpmUnlocksAll(trustedBootStale) || tpmUnlocksAll(RecoveryStatus{Edition: "trusted-boot"}) {
		t.Error("tpmUnlocksAll")
	}

	// Not offered: no dialog.
	u, d, _ := testUI(t, true)
	d.recovery = trustedBootTPM
	p := securityPageOf(t, u)
	p.openReseal()
	time.Sleep(20 * time.Millisecond)
	if u.question() != nil {
		t.Fatal("reseal asked while the TPM works")
	}

	u, d, _ = testUI(t, true)
	d.recovery = trustedBootStale
	p = securityPageOf(t, u)
	p.openReseal()
	q := waitQuestion(t, u, "Seal to this computer's TPM again")
	if q.focus != 0 {
		t.Fatalf("focus %d, want 0 (Cancel)", q.focus)
	}
	last := q.choices[len(q.choices)-1]
	typeInto(p.resealKey, "not a key")
	if last.enabled() {
		t.Fatal("Reseal is on for something that is not a key")
	}
	key, _ := recoverykey.New()
	typeInto(p.resealKey, strings.ToUpper(strings.ReplaceAll(key, "-", " ")))
	u.pick(q, len(q.choices)-1)
	eventually(t, "RecoveryReseal", func() bool { return strings.Contains(strings.Join(d.called(), ","), "RecoveryReseal "+secret) })
	if _, _, got, _ := d.recoverySecrets(); got != key {
		t.Fatalf("resealed with %q, want %q", got, key)
	}
	if p.resealKey.Editor.Text() != "" {
		t.Fatal("the key stays in the field")
	}
}

func TestSecurityPageNonAdminHasTheButtonsOff(t *testing.T) {
	u, d, _ := testUI(t, false)
	d.recovery = trustedBootStale
	p := u.view(PageSecurity).(*securityPage)
	p.Shown()
	for _, a := range p.actions() {
		if !a.Disabled {
			t.Fatalf("%q is on for a non-admin", a.Label)
		}
	}
	p.openNewKey()
	p.openReseal()
	time.Sleep(20 * time.Millisecond)
	if u.question() != nil {
		t.Fatal("a non-admin opened a key dialog")
	}
}

func TestSecurityDialogsDrawAtBothSizes(t *testing.T) {
	for _, admin := range []bool{true, false} {
		for _, sz := range []image.Point{{1100, 720}, {700, 500}} {
			u, d, _ := testUI(t, admin)
			d.recovery = trustedBootStale
			u.show(PageSecurity)
			p := u.view(PageSecurity).(*securityPage)
			if admin {
				p = securityPageOf(t, u)
			}
			draw := func(what string) {
				if dims := u.Layout(gtxOf(sz.X, sz.Y)); dims.Size != sz {
					t.Errorf("%s (admin %v) at %v: %v", what, admin, sz, dims.Size)
				}
			}
			draw("page")
			if !admin {
				continue
			}
			p.openNewKey()
			draw("secret form")
			u.pick(u.question(), len(u.question().choices)-1) // disabled: stays
			typeInto(p.cur, "x")
			u.pick(u.question(), len(u.question().choices)-1)
			draw("key shown")
			u.pick(u.question(), len(u.question().choices)-1)
			draw("group asked")
			u.closeDialog()
			p.openReseal()
			draw("reseal form")
			u.closeDialog()
		}
	}
}

// disksUI is the window with the fake udisks2, open on the Disks page with
// its drives read. The page reads the fake's root devices.
func disksUI(t *testing.T, admin bool) (*UI, *fakeDisks, *disksPage) {
	t.Helper()
	u, _, _ := testUI(t, admin)
	f := newFakeDisks()
	u.hooks.Disks = f
	p := u.view(PageDisks).(*disksPage)
	p.setRoots(func() ([]string, error) { return f.roots, nil })
	p.Shown()
	eventually(t, "the drives", func() bool { u.mu.Lock(); defer u.mu.Unlock(); return p.loaded })
	return u, f, p
}

const (
	fakeSSD  = "/org/freedesktop/UDisks2/drives/Samsung_SSD"
	fakeSDB1 = "/org/freedesktop/UDisks2/block_devices/sdb1"
)

func (p *disksPage) driveOf(t *testing.T, id string) Drive {
	t.Helper()
	for _, d := range p.snapshot() {
		if d.ID == id {
			return d
		}
	}
	t.Fatalf("no drive %s", id)
	return Drive{}
}

func labels(as []guikit.Action) []string {
	var out []string
	for _, a := range as {
		out = append(out, a.Label)
	}
	return out
}

func TestDisksPageSystemDriveHasNoButtons(t *testing.T) {
	u, _, p := disksUI(t, true)
	ssd := p.driveOf(t, fakeSSD)
	if got := p.driveActions(ssd); len(got) != 0 {
		t.Fatalf("system drive actions %q", labels(got))
	}
	for _, b := range ssd.Blocks {
		if got := p.blockActions(ssd, b); len(got) != 0 {
			t.Fatalf("system block %s actions %q", b.Device, labels(got))
		}
	}
	if got := p.reasonText(ssd); got != "Isotope's system disk" {
		t.Fatalf("reason %q", got)
	}
	// A disk udisks2 calls a system disk, not Isotope's: "Internal disk".
	other := Drive{ID: "x", Model: "HDD", Blocks: []Block{{ID: "y", Device: "/dev/sdc", HintSystem: true}}}
	if got := p.reasonText(other); got != "Internal disk" {
		t.Fatalf("reason %q", got)
	}
	if got := p.driveActions(other); len(got) != 0 {
		t.Fatalf("internal drive actions %q", labels(got))
	}
	p.openFormat(fakeSSD)
	time.Sleep(20 * time.Millisecond)
	if u.question() != nil {
		t.Fatal("Format opened on the system disk")
	}

	// The system disk cannot be told: every disk is a system disk, and
	// the page says why.
	p.setRoots(func() ([]string, error) {
		return []string{everyDevice}, errors.New("cannot tell which disk holds the system: no mountinfo")
	})
	p.load()
	eventually(t, "the error", func() bool { u.mu.Lock(); defer u.mu.Unlock(); return p.rootsErr != "" })
	if !strings.Contains(p.rootsErr, "no mountinfo") {
		t.Fatalf("rootsErr %q", p.rootsErr)
	}
	usb := p.driveOf(t, fakeUSB)
	if got := p.driveActions(usb); len(got) != 0 {
		t.Fatalf("USB actions with no root devices %q", labels(got))
	}
	if got := p.reasonText(usb); got != "Isotope's system disk" {
		t.Fatalf("reason %q", got)
	}
}

func TestDisksPageFormatDialogNamesTheDiskAndFocusesCancel(t *testing.T) {
	u, f, p := disksUI(t, true)
	p.openFormat(fakeUSB)
	q := waitQuestion(t, u, "Erase and format SanDisk Cruzer?")
	for _, want := range []string{
		"SanDisk Cruzer, 32.0 GB, /dev/sdb.",
		"USB (exfat), 4.0 GB used of 32.0 GB",
		"It is unmounted first.",
		"Everything on this disk will be erased. This cannot be undone.",
	} {
		if !strings.Contains(q.body, want) {
			t.Errorf("body %q lacks %q", q.body, want)
		}
	}
	if q.focus != 0 || q.choices[0].Label != "Cancel" {
		t.Fatalf("focus %d on %q, want Cancel", q.focus, q.choices[q.focus].Label)
	}
	if q.choices[1].Label != "Erase and format" || q.choices[1].Kind != guikit.ButtonDanger {
		t.Fatalf("choice %+v", q.choices[1])
	}
	if p.fmtForm.fs != "exfat" {
		t.Fatalf("default filesystem %q", p.fmtForm.fs)
	}
	for _, sz := range []image.Point{{1100, 720}, {700, 500}} {
		if dims := u.Layout(gtxOf(sz.X, sz.Y)); dims.Size != sz {
			t.Errorf("at %v: %v", sz, dims.Size)
		}
	}
	// exFAT labels have at most 11 characters, ext4 labels 16 bytes.
	typeInto(p.fmtForm.label, "BACKUPS2026X")
	if q.choices[1].enabled() {
		t.Fatal("a 12-character exFAT label is accepted")
	}
	p.fmtForm.fs = "ext4"
	if !q.choices[1].enabled() {
		t.Fatal("a 12-character ext4 label is refused")
	}
	typeInto(p.fmtForm.label, "BACKUPS-2026-XYZ1")
	if q.choices[1].enabled() {
		t.Fatal("a 17-character ext4 label is accepted")
	}
	// An ext4 label counts bytes: 9 accented letters are 18 bytes.
	typeInto(p.fmtForm.label, "ééééééééé")
	if q.choices[1].enabled() {
		t.Fatal("an 18-byte ext4 label is accepted")
	}
	if r := labelRule("ext4"); r != "An ext4 label has at most 16 bytes (fewer with accented letters)." {
		t.Fatalf("ext4 rule %q", r)
	}
	typeInto(p.fmtForm.label, "Backups")
	if len(f.called()) != 0 {
		t.Fatalf("called before the answer: %q", f.called())
	}
	u.pick(q, 1)
	eventually(t, "Format", func() bool { return contains(f.called(), "Format "+fakeUSB+" ext4 Backups") })
	if c := f.called(); c[0] != "Unmount "+fakeSDB1 {
		t.Fatalf("calls %q, want the unmount first", c)
	}
}

// Review focus 3: a mounted disk is unmounted first, and a failed unmount
// stops the format.
func TestFormatUnmountsFirstAndStopsOnFailure(t *testing.T) {
	u, f, p := disksUI(t, true)
	f.mu.Lock()
	f.errs = map[string]error{"Unmount": errors.New("target is busy")}
	f.mu.Unlock()
	p.openFormat(fakeUSB)
	u.pick(waitQuestion(t, u, "Erase and format"), 1)
	q := waitQuestion(t, u, "Could not format")
	if !strings.Contains(q.body, "target is busy") {
		t.Fatalf("body %q", q.body)
	}
	c := f.called()
	if !contains(c, "Unmount "+fakeSDB1) {
		t.Fatalf("calls %q: no unmount", c)
	}
	for _, x := range c {
		if strings.HasPrefix(x, "Format") {
			t.Fatalf("calls %q: formatted after a failed unmount", c)
		}
	}
}

func TestDisksPageNonAdminCannotFormat(t *testing.T) {
	u, _, p := disksUI(t, false)
	usb := p.driveOf(t, fakeUSB)
	got := labels(p.driveActions(usb))
	if strings.Join(got, ",") != "Eject,Power off" {
		t.Fatalf("non-admin actions %q", got)
	}
	if p.formatNote() != "Only an administrator can format a disk." {
		t.Fatalf("note %q", p.formatNote())
	}
	p.openFormat(fakeUSB)
	time.Sleep(20 * time.Millisecond)
	if u.question() != nil {
		t.Fatal("a non-admin opened the format dialog")
	}
	_, _, ap := disksUI(t, true)
	if got := labels(ap.driveActions(ap.driveOf(t, fakeUSB))); strings.Join(got, ",") != "Eject,Power off,Format…" {
		t.Fatalf("admin actions %q", got)
	}
	if ap.formatNote() != "" {
		t.Fatalf("admin note %q", ap.formatNote())
	}
}

func TestDisksPageMountEjectPowerOff(t *testing.T) {
	u, f, p := disksUI(t, false)
	usb := p.driveOf(t, fakeUSB)
	if got := labels(p.blockActions(usb, usb.Blocks[1])); strings.Join(got, ",") != "Unmount" {
		t.Fatalf("mounted block actions %q", got)
	}
	p.unmount(fakeSDB1)
	eventually(t, "Unmount", func() bool { return contains(f.called(), "Unmount "+fakeSDB1) })
	// the page reloads on the change udisks2 signals
	eventually(t, "the unmounted block", func() bool {
		b := p.driveOf(t, fakeUSB).Blocks[1]
		return len(b.MountPoints) == 0 && strings.Join(labels(p.blockActions(usb, b)), ",") == "Mount"
	})
	p.mount(fakeSDB1)
	eventually(t, "Mount", func() bool { return contains(f.called(), "Mount "+fakeSDB1) })
	eventually(t, "the mounted block", func() bool { return len(p.driveOf(t, fakeUSB).Blocks[1].MountPoints) == 1 })
	p.eject(fakeUSB)
	eventually(t, "Eject", func() bool { return contains(f.called(), "Eject "+fakeUSB) })
	p.powerOff(fakeUSB)
	eventually(t, "PowerOff", func() bool { return contains(f.called(), "PowerOff "+fakeUSB) })
	// Eject unmounted the stick first.
	c := strings.Join(f.called(), ",")
	if !strings.Contains(c, "Mount "+fakeSDB1+",Unmount "+fakeSDB1+",Eject "+fakeUSB) {
		t.Fatalf("calls %q", c)
	}
	if u.question() != nil {
		t.Fatalf("a dialog: %q %q", u.question().title, u.question().body)
	}
	f.mu.Lock()
	f.errs = map[string]error{"Eject": errors.New("Not authorized to perform operation")}
	f.mu.Unlock()
	p.eject(fakeUSB)
	q := waitQuestion(t, u, "Could not eject")
	if !strings.Contains(q.body, "Not authorized") {
		t.Fatalf("body %q", q.body)
	}
}

func TestDisksPageDrawsAtBothSizes(t *testing.T) {
	for _, admin := range []bool{true, false} {
		u, _, p := disksUI(t, admin)
		u.show(PageDisks)
		for _, sz := range []image.Point{{1100, 720}, {700, 500}} {
			if dims := u.Layout(gtxOf(sz.X, sz.Y)); dims.Size != sz {
				t.Errorf("admin %v at %v: %v", admin, sz, dims.Size)
			}
		}
		p.setRoots(func() ([]string, error) { return []string{everyDevice}, errors.New("no mountinfo") })
		p.load()
		eventually(t, "the error", func() bool { u.mu.Lock(); defer u.mu.Unlock(); return p.rootsErr != "" })
		u.Layout(gtxOf(700, 500))
	}
	// without udisks2
	u, _, _ := testUI(t, true)
	u.show(PageDisks)
	u.Layout(gtxOf(1100, 720))
}

func TestHumanSize(t *testing.T) {
	for in, want := range map[uint64]string{0: "0 bytes", 999: "999 bytes", 64e6: "64.0 MB", 4e9: "4.0 GB", 32e9: "32.0 GB", 2e12: "2.0 TB", 1536e6: "1.5 GB"} {
		if got := humanSize(in); got != want {
			t.Errorf("humanSize(%d) = %q, want %q", in, got, want)
		}
	}
}

func TestLabelFits(t *testing.T) {
	for _, c := range []struct {
		fs, label string
		ok        bool
	}{
		{"exfat", "", true}, {"exfat", "ABCDEFGHIJK", true}, {"exfat", "ABCDEFGHIJKL", false},
		{"exfat", "Café", true}, {"ext4", "ABCDEFGHIJKLMNOP", true}, {"ext4", "ABCDEFGHIJKLMNOPQ", false},
		{"ext4", "ééééééééé", false}, // 18 bytes
	} {
		if got := labelFits(c.fs, c.label); got != c.ok {
			t.Errorf("labelFits(%s, %q) = %v", c.fs, c.label, got)
		}
	}
}

// ---- the Backup page, phase 2 ----

// backupUI is the window open on the Backup page with st from the daemon
// and the fake udisks2, the status and the disks read.
func backupUI(t *testing.T, admin bool, st BackupStatus) (*UI, *fakeDaemon, *fakeDisks, *backupPage) {
	t.Helper()
	u, d, _ := testUI(t, admin)
	d.backup = st
	f := newFakeDisks()
	u.hooks.Disks = f
	p := u.view(PageBackup).(*backupPage)
	p.setRoots(func() ([]string, error) { return f.roots, nil })
	u.show(PageBackup)
	eventually(t, "the backup status", func() bool { u.mu.Lock(); defer u.mu.Unlock(); return p.loaded })
	if admin {
		eventually(t, "the disks", func() bool { u.mu.Lock(); defer u.mu.Unlock(); return p.disksLoaded })
	}
	return u, d, f, p
}

func hasPrefix(list []string, prefix string) bool {
	for _, s := range list {
		if strings.HasPrefix(s, prefix) {
			return true
		}
	}
	return false
}

// confirmKey types the asked group of the shown password and confirms it.
func confirmKey(t *testing.T, u *UI, d *fakeDaemon) string {
	t.Helper()
	key, c := shownKeyTitled(t, u, "The password of this backup")
	if hasPrefix(d.called(), "BackupInit") {
		t.Fatal("BackupInit before the password was confirmed")
	}
	typeInto(c.key.typed, groupOf(key, c.key.group))
	u.pick(c, len(c.choices)-1)
	return key
}

var setupNew = BackupStatus{Available: true, RestoreRefusal: "there is more than one user"}

// A removable drive that runs Isotope offers none of its file systems, not
// even a data partition (welcome's usbDisksFrom has the same rule).
func TestBackupDisksSkipADriveThatRunsIsotope(t *testing.T) {
	_, _, f, p := backupUI(t, true, setupNew)
	eventually(t, "the USB disk", func() bool { return len(p.backupDisks()) == 1 })
	f.mu.Lock()
	f.drives[0].Blocks = append(f.drives[0].Blocks, Block{ID: "/org/freedesktop/UDisks2/block_devices/sdb2", Device: "/dev/sdb2",
		Label: "COS_STATE", FSType: "ext4", UUID: "D", Size: 8e9, IsFilesystem: true, IsPartition: true})
	f.mu.Unlock()
	select {
	case f.changed <- struct{}{}:
	default:
	}
	eventually(t, "no disk", func() bool { return len(p.backupDisks()) == 0 })
}

func TestBackupSetupUSBInitsByUUIDAfterTheKeyIsConfirmed(t *testing.T) {
	u, d, f, p := backupUI(t, true, setupNew)
	f.block(fakeSDB1, func(b *Block) { b.MountPoints = nil })
	eventually(t, "the unmounted disk", func() bool {
		ds := p.backupDisks()
		return len(ds) == 1 && len(ds[0].MountPoints) == 0
	})
	if p.form.folder.Editor.Text() != "isotope-backup" {
		t.Fatalf("folder %q", p.form.folder.Editor.Text())
	}
	p.pickDisk(fakeSDB1)
	p.cont()
	q := waitQuestion(t, u, "Set up the backup")
	if !contains(f.called(), "Mount "+fakeSDB1) {
		t.Fatalf("not mounted first: %q", f.called())
	}
	if labels := []string{q.choices[1].Label, q.choices[2].Label}; labels[0] != "Use an existing backup" || labels[1] != "Create a new backup" {
		t.Fatalf("choices %q", labels)
	}
	u.pick(q, 2)
	key := confirmKey(t, u, d)
	want := "BackupInit uuid:1234-ABCD/isotope-backup {Containers:false Existing:false Label:USB HostKey: Port:0} <secret>"
	eventually(t, "BackupInit", func() bool { return contains(d.called(), want) })
	if c := d.lastCred(); c.Password != key || len(c.Env) != 0 {
		t.Fatalf("credentials %+v, want the shown key", c)
	}
}

func TestBackupSetupCancelledKeyInitsNothing(t *testing.T) {
	u, d, _, p := backupUI(t, true, setupNew)
	p.pickDisk(fakeSDB1)
	p.cont()
	u.pick(waitQuestion(t, u, "Set up the backup"), 2)
	q := waitQuestion(t, u, "The password of this backup")
	u.pick(q, 0)
	time.Sleep(20 * time.Millisecond)
	if hasPrefix(d.called(), "BackupInit") {
		t.Fatalf("calls %q", d.called())
	}
}

func TestBackupSetupExistingAsksThePassword(t *testing.T) {
	u, d, f, p := backupUI(t, true, setupNew)
	p.pickDisk(fakeSDB1)
	p.form.containers = true
	p.cont()
	u.pick(waitQuestion(t, u, "Set up the backup"), 1)
	q := waitQuestion(t, u, "Use an existing backup")
	if q.choices[len(q.choices)-1].enabled() {
		t.Fatal("Use this backup is on with no password")
	}
	typeInto(p.form.existing, "xbcd-efgh")
	u.pick(q, len(q.choices)-1)
	want := "BackupInit uuid:1234-ABCD/isotope-backup {Containers:true Existing:true Label:USB HostKey: Port:0} <secret>"
	eventually(t, "BackupInit", func() bool { return contains(d.called(), want) })
	if c := d.lastCred(); c.Password != "xbcd-efgh" {
		t.Fatalf("password %q", c.Password)
	}
	if p.form.existing.Editor.Text() != "" {
		t.Fatal("the typed password stays in the field")
	}
	if contains(f.called(), "Mount "+fakeSDB1) {
		t.Fatal("mounted a disk that is mounted")
	}
}

func TestBackupSetupS3SendsEnvLinesNotStrings(t *testing.T) {
	u, d, _, p := backupUI(t, true, setupNew)
	p.pickRemote("s3")
	typeInto(p.form.s3Endpoint, "https://s3.example.com")
	typeInto(p.form.s3Bucket, "bucket")
	typeInto(p.form.s3Path, "isotope")
	typeInto(p.form.s3Key, "AKIAKEY")
	typeInto(p.form.s3Secret, "VERYSECRET")
	typeInto(p.form.s3Region, "eu-west-1")
	p.cont()
	u.pick(waitQuestion(t, u, "Set up the backup"), 2)
	confirmKey(t, u, d)
	eventually(t, "BackupInit", func() bool { return hasPrefix(d.called(), "BackupInit") })
	call := strings.Join(d.called(), "\n")
	if !strings.Contains(call, "BackupInit s3:https://s3.example.com/bucket/isotope {Containers:false Existing:false Label: HostKey: Port:0} <secret> <secret> <secret> <secret>") {
		t.Fatalf("calls %q", call)
	}
	if strings.Contains(call, "VERYSECRET") || strings.Contains(call, "AKIAKEY") {
		t.Fatalf("a secret in a call string: %q", call)
	}
	want := []string{"AWS_ACCESS_KEY_ID=AKIAKEY", "AWS_SECRET_ACCESS_KEY=VERYSECRET", "AWS_DEFAULT_REGION=eu-west-1"}
	if c := d.lastCred(); fmt.Sprint(c.Env) != fmt.Sprint(want) {
		t.Fatalf("env %q", c.Env)
	}
}

func TestBackupSetupSFTPConfirmsTheHostKey(t *testing.T) {
	u, d, _, p := backupUI(t, true, setupNew)
	d.sshKey = "ssh-ed25519 AAAAC3Nz isotope-backup@anna-laptop"
	d.hostKeys = []string{"RSA SHA256:rsarsarsa", "ECDSA SHA256:ecdsaecdsa", "ED25519 SHA256:edededed"}
	p.pickRemote("sftp")
	eventually(t, "the public key", func() bool { return p.publicKey() == d.sshKey })
	typeInto(p.form.sftpUserHost, "anna@nas.local")
	typeInto(p.form.sftpPort, "2222")
	typeInto(p.form.sftpPath, "/srv/backup")
	if p.ready() {
		t.Fatal("Continue is on before the host key is confirmed")
	}
	p.connect()
	q := waitQuestion(t, u, "Is this the server's key?")
	if !strings.Contains(q.body, "/etc/ssh/ssh_host_*_key.pub") || q.focus != 0 || q.form == nil {
		t.Fatalf("body %q, focus %d", q.body, q.focus)
	}
	rows, chosen := p.hostKeyRows()
	want := []string{"ED25519 SHA256:edededed", "RSA SHA256:rsarsarsa", "ECDSA SHA256:ecdsaecdsa"}
	if fmt.Sprint(rows) != fmt.Sprint(want) || chosen != 0 {
		t.Fatalf("rows %q chosen %d, want %q with ED25519 chosen", rows, chosen, want)
	}
	if !contains(d.called(), "BackupHostKey nas.local 2222") {
		t.Fatalf("calls %q", d.called())
	}
	u.Layout(gtxOf(700, 500))
	p.chooseHostKey(2) // the user compared the ECDSA key
	u.pick(q, len(q.choices)-1)
	if !p.ready() {
		t.Fatal("Continue is off after the host key is confirmed")
	}
	p.cont()
	u.pick(waitQuestion(t, u, "Set up the backup"), 2)
	confirmKey(t, u, d)
	want1 := "BackupInit sftp://anna@nas.local:2222//srv/backup {Containers:false Existing:false Label: HostKey:SHA256:ecdsaecdsa Port:2222} <secret>"
	eventually(t, "BackupInit", func() bool { return contains(d.called(), want1) })

	// Another server needs its own key confirmed.
	typeInto(p.form.sftpUserHost, "anna@other")
	if p.ready() {
		t.Fatal("the host key of nas.local counts for another host")
	}
}

func TestBackupSetupSFTPYesTakesED25519ByDefault(t *testing.T) {
	u, d, _, p := backupUI(t, true, setupNew)
	d.hostKeys = []string{"RSA SHA256:rsa", "ED25519 SHA256:ed"}
	p.pickRemote("sftp")
	typeInto(p.form.sftpUserHost, "anna@nas.local")
	typeInto(p.form.sftpPath, "/srv/backup")
	p.connect()
	u.pick(waitQuestion(t, u, "Is this the server's key?"), 1)
	if tg, problem := p.formTarget(); problem != "" || tg.opt.HostKey != "SHA256:ed" || tg.opt.Port != 22 {
		t.Fatalf("target %+v, %q", tg, problem)
	}
}

func TestBackupSecretsAreClearedOnCancelAndRestore(t *testing.T) {
	u, _, _, p := backupUI(t, true, BackupStatus{Available: true, CanRestore: true})
	fill := func() {
		typeInto(p.form.restURL, "https://nas/")
		typeInto(p.form.restPw, "restpw")
		typeInto(p.form.s3Secret, "s3secret")
	}
	empty := func(what string) {
		t.Helper()
		if p.form.restPw.Editor.Text() != "" || p.form.s3Secret.Editor.Text() != "" {
			t.Fatalf("%s: secrets stay in the form", what)
		}
	}
	p.pickRemote("rest")
	fill()
	p.cont()
	u.pick(waitQuestion(t, u, "Set up the backup"), 0)
	empty("Cancel of Set up the backup")

	fill()
	p.cont()
	u.pick(waitQuestion(t, u, "Set up the backup"), 1)
	u.pick(waitQuestion(t, u, "Use an existing backup"), 0)
	empty("Cancel of the existing backup's password")

	fill()
	p.openRestore()
	q := waitQuestion(t, u, "Restore this machine from a backup")
	typeInto(p.restore.pw, "pw")
	u.pick(q, 1)
	empty("the restore's submit")
	// The list runs off the UI goroutine and ends in its own message (the
	// fake has no backups): answer it before the next question, which it
	// would replace.
	u.pick(waitQuestion(t, u, "There is no backup there"), 0)
	eventually(t, "the list to end", func() bool { u.mu.Lock(); defer u.mu.Unlock(); return p.working == "" })

	fill()
	p.openRestore()
	u.pick(waitQuestion(t, u, "Restore this machine from a backup"), 0)
	empty("Cancel of the restore")
}

func TestBackupExportOffWhenTheDiskIsUnplugged(t *testing.T) {
	st := targetUSB
	st.Present = false
	u, _, _, p := backupUI(t, true, st)
	if a := p.exportAction(Status{State: StateIdle}); !a.Disabled {
		t.Fatal("Export is on with the disk unplugged")
	}
	p.openExport()
	time.Sleep(20 * time.Millisecond)
	if q := u.question(); q != nil {
		t.Fatalf("a question: %q", q.title)
	}
	u2, _, _, p2 := backupUI(t, true, targetUSB)
	if a := p2.exportAction(Status{State: StateIdle}); a.Disabled {
		t.Fatal("Export is off with the disk there")
	}
	if a := p2.exportAction(Status{State: StateBackup}); !a.Disabled {
		t.Fatal("Export is on while a backup runs")
	}
	u2.Layout(gtxOf(700, 500))
}

func TestBackupSetupSFTPNoIsNotConfirmed(t *testing.T) {
	u, d, _, p := backupUI(t, true, setupNew)
	d.hostKeys = []string{"ED25519 SHA256:abc"}
	p.pickRemote("sftp")
	typeInto(p.form.sftpUserHost, "anna@nas.local")
	typeInto(p.form.sftpPath, "/srv/backup")
	p.connect()
	u.pick(waitQuestion(t, u, "Is this the server's key?"), 0)
	if p.ready() {
		t.Fatal("No confirmed the key")
	}
}

func TestBackupSetupREST(t *testing.T) {
	u, d, _, p := backupUI(t, true, setupNew)
	p.pickRemote("rest")
	typeInto(p.form.restURL, "https://backup.example.com/anna/")
	typeInto(p.form.restUser, "anna")
	typeInto(p.form.restPw, "restpw")
	p.cont()
	u.pick(waitQuestion(t, u, "Set up the backup"), 2)
	confirmKey(t, u, d)
	want := "BackupInit rest:https://backup.example.com/anna/ {Containers:false Existing:false Label: HostKey: Port:0} <secret> <secret> <secret>"
	eventually(t, "BackupInit", func() bool { return contains(d.called(), want) })
}

func TestBackupSetupShowsTheDaemonsRefusal(t *testing.T) {
	u, d, _, p := backupUI(t, true, setupNew)
	d.err = map[string]error{"BackupInit": &DaemonError{Name: "Failed", Msg: "could not use uuid:1234-ABCD/isotope-backup: permission denied"}}
	p.pickDisk(fakeSDB1)
	p.cont()
	u.pick(waitQuestion(t, u, "Set up the backup"), 2)
	confirmKey(t, u, d)
	q := waitQuestion(t, u, "The backup was not set up")
	if !strings.Contains(q.body, "permission denied") {
		t.Fatalf("body %q", q.body)
	}
}

func TestBackupSetupListsOnlyUsableRemovableDisks(t *testing.T) {
	_, _, f, p := backupUI(t, true, setupNew)
	ds := p.backupDisks()
	if len(ds) != 1 || ds[0].ID != fakeSDB1 {
		t.Fatalf("disks %+v", ds)
	}
	f.mu.Lock()
	f.drives = f.drives[1:]
	f.mu.Unlock()
	f.block("", func(*Block) {}) // signal a change
	eventually(t, "no disk", func() bool { return len(p.backupDisks()) == 0 })
	if p.ready() {
		t.Fatal("Continue is on with no disk")
	}
}

var targetUSB = BackupStatus{Available: true, Target: "uuid:1234-ABCD/isotope-backup", Label: "USB", Kind: "disk",
	Schedule: "weekly", Present: true, RestoreRefusal: "a backup target is set up"}

func TestBackupPlugInDisablesBackUpNow(t *testing.T) {
	st := targetUSB
	st.Present = false
	u, d, _, p := backupUI(t, true, st)
	_, trail := p.Footer()
	if len(trail) != 1 || trail[0].Label != "Back up now" || !trail[0].Disabled {
		t.Fatalf("footer %+v", trail)
	}
	if v := BackupViewOf(st, u.now()); !contains(v.Lines, "Plug in USB to back up") {
		t.Fatalf("lines %q", v.Lines)
	}
	time.Sleep(20 * time.Millisecond)
	if hasPrefix(d.called(), "BackupList") {
		t.Fatal("listed the backups of a disk that is not there")
	}
	u.Layout(gtxOf(700, 500))
}

func TestBackupListShowsNewestFirst(t *testing.T) {
	u, d, _, p := backupUI(t, true, BackupStatus{})
	now := time.Now()
	d.snaps = []Snapshot{
		{ID: "aaaa", Host: "old-box", Time: now.AddDate(0, 0, -20).Unix(), Size: 1e9},
		{ID: "cccc", Host: "anna-laptop", Time: now.Add(-time.Minute).Unix(), Size: 3e9},
		{ID: "bbbb", Host: "anna-laptop", Time: now.AddDate(0, 0, -3).Unix(), Size: 2e9},
	}
	d.mu.Lock()
	d.backup = targetUSB
	d.mu.Unlock()
	p.Shown()
	eventually(t, "the backups", func() bool { return len(p.snapshotRows()) == 3 })
	rows := p.snapshotRows()
	if !strings.Contains(rows[0], "anna-laptop · 3.0 GB") || !strings.Contains(rows[2], "old-box") {
		t.Fatalf("rows %q", rows)
	}
	if !contains(d.called(), "BackupList  {Containers:false Existing:false Label: HostKey: Port:0} <secret>") {
		t.Fatalf("calls %q", d.called())
	}
	u.Layout(gtxOf(1100, 720))
}

func TestBackupExportPathOnTheChosenDisk(t *testing.T) {
	u, d, f, p := backupUI(t, true, targetUSB)
	u.now = func() time.Time { return time.Date(2026, 11, 22, 10, 0, 0, 0, time.Local) }
	p.openExport()
	q := waitQuestion(t, u, "Export to a file")
	if got := p.export.name.Editor.Text(); got != "isotope-anna-laptop-2026-11-22.isotope-backup" {
		t.Fatalf("name %q", got)
	}
	u.pick(q, len(q.choices)-1)
	want := "BackupExport /run/media/anna/USB/isotope-anna-laptop-2026-11-22.isotope-backup"
	eventually(t, "BackupExport", func() bool { return contains(d.called(), want) })

	// An unmounted disk is mounted first; the daemon's refusal is shown.
	f.block(fakeSDB1, func(b *Block) { b.MountPoints = nil })
	eventually(t, "the unmounted disk", func() bool { ds := p.backupDisks(); return len(ds) == 1 && len(ds[0].MountPoints) == 0 })
	d.err = map[string]error{"BackupExport": &DaemonError{Name: "Failed", Msg: "/run/media/anna/USB is not your folder"}}
	p.openExport()
	typeInto(p.export.name, "mine.isotope-backup")
	u.pick(waitQuestion(t, u, "Export to a file"), 1)
	q = waitQuestion(t, u, "The export did not start")
	if !strings.Contains(q.body, "is not your folder") || !contains(f.called(), "Mount "+fakeSDB1) {
		t.Fatalf("body %q, disk calls %q", q.body, f.called())
	}
	for _, bad := range []string{"", "a/b", ".hidden", "..", "a\nb"} {
		if exportNameProblem(bad) == "" {
			t.Errorf("name %q accepted", bad)
		}
	}
	if exportNameProblem("mine.isotope-backup") != "" {
		t.Error("a good name refused")
	}
}

func TestBackupCancelOnlyForThisWindowsJob(t *testing.T) {
	u, d, _, p := backupUI(t, true, targetUSB)
	u.setStatus(Status{State: StateBackup})
	if lead, _ := p.Footer(); len(lead) != 0 {
		t.Fatalf("Cancel for a backup another started: %q", labels(lead))
	}
	u.mu.Lock()
	p.started = jobBackup
	u.mu.Unlock()
	lead, _ := p.Footer()
	if len(lead) != 1 || lead[0].Label != "Cancel" {
		t.Fatalf("leading %q", labels(lead))
	}
	p.cancelJob()
	eventually(t, "BackupCancel", func() bool { return contains(d.called(), "BackupCancel") })
}

func TestRestoreHiddenOnAMachineInUse(t *testing.T) {
	for _, st := range []BackupStatus{targetUSB, setupNew} {
		u, d, _, p := backupUI(t, true, st)
		if p.restoreOffered() {
			t.Fatalf("%+v: restore offered", st)
		}
		p.pickDisk(fakeSDB1)
		p.openRestore()
		time.Sleep(20 * time.Millisecond)
		if q := u.question(); q != nil {
			t.Fatalf("a question: %q", q.title)
		}
		lines := strings.Join(p.restoreLines(), "\n")
		if !strings.Contains(lines, "isotope restore --only homes,apps,extensions") || !strings.Contains(lines, st.RestoreRefusal) {
			t.Fatalf("lines %q", lines)
		}
		if hasPrefix(d.called(), "Restore") {
			t.Fatal("Restore called")
		}
	}
}

func TestRestoreOfferedOnANewMachine(t *testing.T) {
	u, d, _, p := backupUI(t, true, BackupStatus{Available: true, CanRestore: true})
	now := time.Now()
	d.snaps = []Snapshot{
		{ID: "aaaa1111", Host: "old-box", Time: now.AddDate(0, 0, -20).Unix()},
		{ID: "cccc3333", Host: "anna-laptop", Time: now.Add(-time.Hour).Unix(), Size: 3e9},
	}
	if !p.restoreOffered() || len(p.restoreLines()) != 0 {
		t.Fatalf("offered %v, lines %q", p.restoreOffered(), p.restoreLines())
	}
	p.pickDisk(fakeSDB1)
	p.openRestore()
	q := waitQuestion(t, u, "Restore this machine from a backup")
	if q.choices[len(q.choices)-1].enabled() {
		t.Fatal("on with no password")
	}
	typeInto(p.restore.pw, "pass-word")
	u.pick(q, len(q.choices)-1)
	q = waitQuestion(t, u, "Which backup?")
	if !contains(d.called(), "BackupList uuid:1234-ABCD/isotope-backup {Containers:false Existing:false Label: HostKey: Port:0} <secret>") {
		t.Fatalf("calls %q", d.called())
	}
	u.pick(q, len(q.choices)-1) // the newest
	q = waitQuestion(t, u, "Restore this machine?")
	if q.focus != 0 || q.choices[len(q.choices)-1].Kind != guikit.ButtonDanger {
		t.Fatalf("focus %d, kinds %+v", q.focus, q.choices)
	}
	for _, w := range []string{"anna-laptop", "the users and their passwords", "hostname", "SSH keys", "Flatpak apps", "restarts"} {
		if !strings.Contains(q.body, w) {
			t.Errorf("body without %q: %q", w, q.body)
		}
	}
	if hasPrefix(d.called(), "Restore") {
		t.Fatal("Restore before the confirmation")
	}
	u.pick(q, len(q.choices)-1)
	want := "Restore uuid:1234-ABCD/isotope-backup cccc3333 anna-laptop {Containers:false Existing:false Label: HostKey: Port:0} <secret>"
	eventually(t, "Restore", func() bool { return contains(d.called(), want) })
	if c := d.lastCred(); c.Password != "pass-word" {
		t.Fatalf("password %q", c.Password)
	}
	if p.restore.pw.Editor.Text() != "" {
		t.Fatal("the password stays in the field")
	}
}

func TestBackupPageNonAdminChangesNothing(t *testing.T) {
	for _, st := range []BackupStatus{setupNew, targetUSB, {Available: true, CanRestore: true}} {
		u, d, f, p := backupUI(t, false, st)
		p.pickDisk(fakeSDB1)
		p.cont()
		p.openExport()
		p.openRestore()
		p.connect()
		time.Sleep(20 * time.Millisecond)
		if q := u.question(); q != nil {
			t.Fatalf("%+v: a question %q", st, q.title)
		}
		if c := d.called(); hasPrefix(c, "Backup") || hasPrefix(c, "Restore") || hasPrefix(c, "SetBackup") {
			t.Fatalf("%+v: daemon calls %q", st, c)
		}
		if c := f.called(); len(c) != 0 {
			t.Fatalf("%+v: disk calls %q", st, c)
		}
		if p.restoreOffered() {
			t.Fatal("restore offered to a non-admin")
		}
		lead, trail := p.Footer()
		for _, a := range append(lead, trail...) {
			if !a.Disabled {
				t.Fatalf("%+v: %q is on", st, a.Label)
			}
		}
	}
}

func TestBackupPageDrawsAtBothSizes(t *testing.T) {
	states := []BackupStatus{setupNew, targetUSB, {Available: true, CanRestore: true},
		{Available: true, Target: "uuid:1234-ABCD/x", Label: "USB", Kind: "disk"}}
	for _, admin := range []bool{true, false} {
		for _, st := range states {
			for _, sz := range []image.Point{{1100, 720}, {700, 500}} {
				u, d, _, p := backupUI(t, admin, st)
				d.snaps = []Snapshot{{ID: "aaaa", Host: "anna-laptop", Time: time.Now().Unix(), Size: 1e9}}
				d.sshKey = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIBlahBlahBlahBlahBlahBlahBlahBlah isotope-backup@anna-laptop"
				draw := func(what string) {
					t.Helper()
					if dims := u.Layout(gtxOf(sz.X, sz.Y)); dims.Size != sz {
						t.Errorf("%s (admin %v, %+v) at %v: %v", what, admin, st, sz, dims.Size)
					}
				}
				draw("page")
				for _, k := range []string{"rest", "s3", "sftp"} {
					p.pickRemote(k)
					time.Sleep(5 * time.Millisecond)
					draw(k)
				}
				p.pickDisk(fakeSDB1)
				draw("usb")
				if !admin {
					continue
				}
				if st.Target == "" {
					p.cont()
					waitQuestion(t, u, "Set up the backup")
					draw("new or existing")
					u.closeDialog()
				} else if st.Present {
					p.openExport()
					waitQuestion(t, u, "Export to a file")
					draw("export")
					u.closeDialog()
				}
				if st.CanRestore {
					p.openRestore()
					q := waitQuestion(t, u, "Restore this machine from a backup")
					draw("restore password")
					typeInto(p.restore.pw, "x")
					u.pick(q, 1)
					waitQuestion(t, u, "Which backup?")
					draw("which backup")
					u.closeDialog()
				}
				u.setStatus(Status{State: StateBackup})
				draw("running")
			}
		}
	}
}

func TestUpdatesPageShowsHistory(t *testing.T) {
	for _, admin := range []bool{true, false} {
		u, d, _ := testUI(t, admin)
		d.mu.Lock()
		d.history = []HistoryEntry{
			{Time: time.Now().Add(-72 * time.Hour).Unix(), Kind: "install", Version: "v1.0.0", Result: "installed"},
			{Time: time.Now().Add(-time.Hour).Unix(), Kind: "install", Result: "failed"},
		}
		d.mu.Unlock()
		u.show(PageUpdates)
		p := u.view(PageUpdates).(*updatesPage)
		eventually(t, "UpdateHistory", func() bool { return contains(d.called(), "UpdateHistory") })
		eventually(t, "the history", func() bool {
			u.mu.Lock()
			defer u.mu.Unlock()
			return len(p.history) == 2
		})
		for _, sz := range []image.Point{{1100, 720}, {700, 500}} {
			if dims := u.Layout(gtxOf(sz.X, sz.Y)); dims.Size != sz {
				t.Errorf("admin %v at %v: %v", admin, sz, dims.Size)
			}
		}
		// A job that ends reloads it.
		d.mu.Lock()
		d.history = append(d.history, HistoryEntry{Time: time.Now().Unix(), Kind: "rollback"})
		d.mu.Unlock()
		u.setStatus(Status{State: StateApplying, Installed: "v1.0.0"})
		u.setStatus(Status{State: StateUpToDate, Installed: "v1.0.0"})
		eventually(t, "the reload", func() bool {
			u.mu.Lock()
			defer u.mu.Unlock()
			return len(p.history) == 3
		})
	}
}
