package main

import (
	"reflect"
	"strings"
	"syscall"
	"testing"

	"github.com/godbus/dbus/v5"
)

func v(x any) dbus.Variant { return dbus.MakeVariant(x) }

func TestUserFromDict(t *testing.T) {
	got := userFromDict(map[string]dbus.Variant{
		"name": v("anna"), "full_name": v("Anna B"), "uid": v(uint32(1000)),
		"admin": v(true), "home": v("/home/anna"),
	})
	want := User{Name: "anna", FullName: "Anna B", Home: "/home/anna", UID: 1000, Admin: true}
	if got != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestSnapshotsFromDicts(t *testing.T) {
	got := snapshotsFromDicts([]map[string]dbus.Variant{
		{"id": v("abcd1234"), "time": v(int64(1700000000)), "host": v("anna-laptop"), "size": v(uint64(4096))},
		{"id": v("00ff00ff")},
	})
	want := []Snapshot{
		{ID: "abcd1234", Host: "anna-laptop", Time: 1700000000, Size: 4096},
		{ID: "00ff00ff"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestHistoryFromDicts(t *testing.T) {
	got := historyFromDicts([]map[string]dbus.Variant{
		{"time": v(int64(1700000000)), "kind": v("install"), "version": v("v1.1.0"),
			"ref": v("quay.io/x/isotope:sway-v1.1.0"), "digest": v("sha256:abc"), "result": v("installed")},
		{"time": v(int64(1690000000)), "kind": v("rollback")},
	})
	want := []HistoryEntry{
		{Time: 1700000000, Kind: "install", Version: "v1.1.0", Ref: "quay.io/x/isotope:sway-v1.1.0", Digest: "sha256:abc", Result: "installed"},
		{Time: 1690000000, Kind: "rollback"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestOptionsDictOmitsZeroValues(t *testing.T) {
	if m := optionsDict(TargetOptions{}); len(m) != 0 {
		t.Fatalf("zero options: %v", m)
	}
	m := optionsDict(TargetOptions{Containers: true, Existing: true, Label: "USB", HostKey: "SHA256:x", Port: 2222})
	want := map[string]dbus.Variant{
		"containers": v(true), "existing": v(true), "label": v("USB"),
		"host_key": v("SHA256:x"), "port": v(uint32(2222)),
	}
	if !reflect.DeepEqual(m, want) {
		t.Fatalf("got %v, want %v", m, want)
	}
	// Only the keys BackupList and Restore take, when only those are set.
	m = optionsDict(TargetOptions{HostKey: "SHA256:x"})
	if len(m) != 1 || m["host_key"].Value() != "SHA256:x" {
		t.Fatalf("host key only: %v", m)
	}
}

func TestWithSecretsJoinsLines(t *testing.T) {
	var got string
	var sent dbus.UnixFD
	err := withSecrets([]string{"pw", "AWS_ACCESS_KEY_ID=x"}, func(fd dbus.UnixFD) error {
		sent = fd
		buf := make([]byte, 8192)
		for {
			n, err := syscall.Read(int(fd), buf)
			if n <= 0 || err != nil {
				return err
			}
			got += string(buf[:n])
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if got != "pw\nAWS_ACCESS_KEY_ID=x\n" {
		t.Fatalf("pipe held %q", got)
	}
	// The read end is closed once the call returns.
	if _, err := syscall.Read(int(sent), make([]byte, 1)); err != syscall.EBADF {
		t.Fatalf("read end still open after withSecrets: %v", err)
	}
	called := false
	err = withSecrets([]string{string(make([]byte, 4096))}, func(dbus.UnixFD) error { called = true; return nil })
	if err == nil || called {
		t.Fatalf("a secret over 4096 bytes was sent: err %v, called %v", err, called)
	}
}

func TestBusyIncludesRestore(t *testing.T) {
	if !(Status{State: StateRestore}).Busy() {
		t.Fatal("a restore is not busy")
	}
}

func TestDescribeKeepsTheRefusal(t *testing.T) {
	msg := "this machine is in use; restore with isotope restore --only"
	err := wrapErr(dbus.Error{Name: errPrefix + "Refused", Body: []any{msg}})
	if got := Describe(err); got != msg {
		t.Fatalf("Describe = %q, want the daemon's sentence", got)
	}
	if errName(err) != "Refused" {
		t.Fatalf("name %q", errName(err))
	}
}

// The fake records every call and never a secret's text.
func TestFakeDaemonRecordsCallsWithoutSecrets(t *testing.T) {
	f := newFakeDaemon()
	f.err = map[string]error{"Restore": &DaemonError{Name: "Refused", Msg: "in use"}}
	cred := Credentials{Password: "hunter2", Env: []string{"AWS_SECRET_ACCESS_KEY=s3cr3t"}}
	_ = f.AddUser("bob", "Bob", true, "hunter2")
	_ = f.SetPassword("bob", "old-pw", "hunter2")
	_ = f.BackupInit("s3:host/bucket", TargetOptions{Label: "S3"}, cred)
	if err := f.Restore("", "abcd1234", "", TargetOptions{}, cred); errName(err) != "Refused" {
		t.Fatalf("Restore error %v", err)
	}
	_ = f.RecoveryNewKey("old-key", "new-key")
	calls := f.called()
	if len(calls) != 5 || calls[0] != "AddUser bob Bob true <secret>" || calls[3] != "Restore  abcd1234  {Containers:false Existing:false Label: HostKey: Port:0} <secret> <secret>" {
		t.Fatalf("calls %q", calls)
	}
	for _, c := range calls {
		for _, s := range []string{"hunter2", "s3cr3t", "old-pw", "old-key", "new-key"} {
			if strings.Contains(c, s) {
				t.Fatalf("call %q shows a secret", c)
			}
		}
	}
}

func TestFakeDaemonRefusesWhileBusy(t *testing.T) {
	f := newFakeDaemon()
	f.backup.CanRestore = true
	f.users = []User{{Name: "bob", UID: 1001}}
	f.st.State = StateApplying
	for name, err := range map[string]error{
		"Restore":      f.Restore("", "abcd1234", "", TargetOptions{}, Credentials{}),
		"BackupExport": f.BackupExport("/home/anna/backup.tar"),
	} {
		if errName(err) != "Busy" {
			t.Errorf("%s during an install: %v", name, err)
		}
	}
	// An install does not touch the backup settings or /oem.
	if err := f.BackupInit("/mnt/b", TargetOptions{}, Credentials{}); err != nil {
		t.Errorf("BackupInit during an install: %v", err)
	}
	if err := f.SetAdmin("bob", true); err != nil {
		t.Errorf("SetAdmin during an install: %v", err)
	}
	f.st.State = StateBackup
	if err := f.BackupInit("/mnt/b", TargetOptions{}, Credentials{}); errName(err) != "Busy" {
		t.Errorf("BackupInit during a backup: %v", err)
	}
	if err := f.BackupCancel(); err != nil {
		t.Errorf("BackupCancel during a backup: %v", err)
	}
	f.st.State = StateRestore
	for name, err := range map[string]error{
		"AddUser":     f.AddUser("carol", "", false, "pw"),
		"RemoveUser":  f.RemoveUser("bob", false),
		"SetPassword": f.SetPassword("bob", "", "pw"),
		"SetAdmin":    f.SetAdmin("bob", false),
	} {
		if errName(err) != "Busy" {
			t.Errorf("%s during a restore: %v", name, err)
		}
	}
	if err := f.BackupCancel(); errName(err) != "NotCancellable" {
		t.Errorf("BackupCancel during a restore: %v", err)
	}
	if len(f.users) != 1 || !f.users[0].Admin {
		t.Fatalf("a refused change changed the users: %+v", f.users)
	}
	f.st.State = StateIdle
	if err := f.BackupCancel(); errName(err) != "Failed" || !strings.Contains(err.Error(), "no job to cancel") {
		t.Errorf("BackupCancel with no job: %v", err)
	}
}

func TestFakeDaemonRefusesRestoreOnAMachineInUse(t *testing.T) {
	f := newFakeDaemon()
	f.backup = BackupStatus{CanRestore: false, RestoreRefusal: "this machine is in use"}
	err := f.Restore("", "abcd1234", "", TargetOptions{}, Credentials{Password: "pw"})
	want := "This machine is already in use; restore parts of a backup with isotope restore --only"
	if errName(err) != "Refused" || Describe(err) != want {
		t.Fatalf("Restore on a machine in use: %v", err)
	}
	f.backup.CanRestore = true
	if err := f.Restore("", "abcd1234", "", TargetOptions{}, Credentials{Password: "pw"}); err != nil {
		t.Fatalf("Restore on a new machine: %v", err)
	}
}

func TestFakeDaemonRefusesANonAdmin(t *testing.T) {
	f := newFakeDaemon()
	f.nonAdmin = true
	f.users = []User{{Name: "anna", UID: 1000}, {Name: "bob", UID: 1001}}
	f.backup.CanRestore = true
	cred := Credentials{Password: "pw"}
	for name, err := range map[string]error{
		"AddUser":        f.AddUser("carol", "", false, "pw"),
		"RemoveUser":     f.RemoveUser("bob", false),
		"SetPassword":    f.SetPassword("bob", "", "pw"),
		"SetAdmin":       f.SetAdmin("bob", true),
		"BackupInit":     f.BackupInit("/mnt/b", TargetOptions{}, cred),
		"BackupList":     func() error { _, err := f.BackupList("", TargetOptions{}, cred); return err }(),
		"BackupSSHKey":   func() error { _, err := f.BackupSSHKey(); return err }(),
		"BackupHostKey":  func() error { _, err := f.BackupHostKey("h", 22); return err }(),
		"BackupExport":   f.BackupExport("/home/anna/b.tar"),
		"BackupCancel":   f.BackupCancel(),
		"Restore":        f.Restore("", "abcd1234", "", TargetOptions{}, cred),
		"RecoveryNewKey": f.RecoveryNewKey("a", "b"),
		"RecoveryReseal": f.RecoveryReseal("a"),
	} {
		if errName(err) != "NotAuthorized" {
			t.Errorf("%s by a non-admin: %v", name, err)
		}
	}
	// Reading the users and the history, and your own password, need no
	// admin.
	if _, err := f.Users(); err != nil {
		t.Errorf("Users: %v", err)
	}
	if _, err := f.UpdateHistory(); err != nil {
		t.Errorf("UpdateHistory: %v", err)
	}
	if err := f.SetPassword("anna", "old", "new"); err != nil {
		t.Errorf("own SetPassword: %v", err)
	}
	if len(f.users) != 2 || f.users[1].Admin {
		t.Fatalf("a refused change changed the users: %+v", f.users)
	}
}

func TestFakeDaemonAppliesUserChanges(t *testing.T) {
	f := newFakeDaemon()
	f.users = []User{{Name: "anna", UID: 1000, Admin: true, Home: "/home/anna"}}
	if err := f.AddUser("bob", "Bob B", false, "pw"); err != nil {
		t.Fatal(err)
	}
	if err := f.SetAdmin("bob", true); err != nil {
		t.Fatal(err)
	}
	us, _ := f.Users()
	want := []User{{Name: "anna", UID: 1000, Admin: true, Home: "/home/anna"}, {Name: "bob", FullName: "Bob B", Home: "/home/bob", UID: 1001, Admin: true}}
	if !reflect.DeepEqual(us, want) {
		t.Fatalf("after add and admin: %+v", us)
	}
	if err := f.RemoveUser("anna", false); err != nil {
		t.Fatal(err)
	}
	if us, _ := f.Users(); len(us) != 1 || us[0].Name != "bob" {
		t.Fatalf("after remove: %+v", us)
	}
	if err := f.AddUser("bob", "", false, "pw"); errName(err) != "Refused" {
		t.Fatalf("adding an existing user: %v", err)
	}
}

func TestFakeDaemonErrOverrides(t *testing.T) {
	f := newFakeDaemon()
	f.backup.CanRestore = true
	f.err = map[string]error{"Restore": &DaemonError{Name: "Failed", Msg: "restic: wrong password"}}
	if err := f.Restore("", "abcd1234", "", TargetOptions{}, Credentials{}); errName(err) != "Failed" {
		t.Fatalf("err override: %v", err)
	}
}
