package main

import (
	"io"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/godbus/dbus/v5"
)

func TestUSBTargetIsTheUUIDAndTheFolder(t *testing.T) {
	got, err := usbTarget(USBDisk{UUID: "1234-ABCD"}, " /isotope-backup/ ")
	if err != nil || got != "uuid:1234-ABCD/isotope-backup" {
		t.Fatalf("%q, %v", got, err)
	}
	for _, bad := range []string{"", "/", "a/../b", "a\nb"} {
		if _, err := usbTarget(USBDisk{UUID: "1"}, bad); err == nil {
			t.Fatalf("folder %q accepted", bad)
		}
	}
}

// The secrets of a remote target go in the credentials, never in the
// target string.
func TestRemoteTargetsKeepTheSecretsOut(t *testing.T) {
	target, env, err := restTarget("https://nas:8000/anna/", "anna", "s3cret")
	if err != nil || target != "rest:https://nas:8000/anna/" || strings.Contains(target, "s3cret") {
		t.Fatalf("%q, %v", target, err)
	}
	if !slices.Equal(env, []string{"RESTIC_REST_USERNAME=anna", "RESTIC_REST_PASSWORD=s3cret"}) {
		t.Fatalf("env %v", env)
	}
	if _, _, err := restTarget("https://anna:pw@nas/", "", ""); err == nil {
		t.Fatal("a password in the address was accepted")
	}
	if _, _, err := restTarget("https://nas/", "anna", "a\nb"); err == nil {
		t.Fatal("a line break in a credential was accepted")
	}

	target, env, err = s3Target("https://s3.example.com/", "bucket", "/isotope/", "KEY", "SECRET", "")
	if err != nil || target != "s3:https://s3.example.com/bucket/isotope" || strings.Contains(target, "SECRET") {
		t.Fatalf("%q, %v", target, err)
	}
	if !slices.Equal(env, []string{"AWS_ACCESS_KEY_ID=KEY", "AWS_SECRET_ACCESS_KEY=SECRET"}) {
		t.Fatalf("env %v", env)
	}
	if _, _, err := s3Target("https://s3.example.com", "bucket", "", "KEY", "", ""); err == nil {
		t.Fatal("an S3 target without a secret was accepted")
	}
}

// Every credential name the page sends is one the daemon takes
// (allowedEnv in update/backup_target.go).
func TestCredentialNamesAreTheDaemons(t *testing.T) {
	allowed := []string{"RESTIC_REST_USERNAME", "RESTIC_REST_PASSWORD", "AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "AWS_DEFAULT_REGION"}
	_, a, _ := restTarget("https://nas/", "u", "p")
	_, b, _ := s3Target("https://e", "b", "", "k", "s", "r")
	for _, l := range append(a, b...) {
		name, _, _ := strings.Cut(l, "=")
		if !slices.Contains(allowed, name) {
			t.Fatalf("%s is not a name the daemon takes", name)
		}
	}
}

func TestSFTPTarget(t *testing.T) {
	target, host, port, err := sftpTarget("anna@nas.local", "22", "/srv/backup")
	if err != nil || target != "sftp:anna@nas.local:/srv/backup" || host != "nas.local" || port != 22 {
		t.Fatalf("%q %q %d %v", target, host, port, err)
	}
	target, _, port, err = sftpTarget("anna@nas.local", "2222", "/srv/backup")
	if err != nil || target != "sftp://anna@nas.local:2222//srv/backup" || port != 2222 {
		t.Fatalf("%q %d %v", target, port, err)
	}
	for _, uh := range []string{"", "-oProxy@x", "anna@", "anna@ho st"} {
		if _, _, _, err := sftpTarget(uh, "22", "/srv"); err == nil {
			t.Fatalf("%q accepted", uh)
		}
	}
	if _, _, _, err := sftpTarget("a@h", "70000", "/srv"); err == nil {
		t.Fatal("port 70000 accepted")
	}
}

// BackupHostKey sends "TYPE SHA256:…": every key is shown with its type,
// ED25519 first, and the option is the bare fingerprint.
func TestHostKeyChoices(t *testing.T) {
	got := hostKeyChoices([]string{"RSA SHA256:rrr", "ED25519 SHA256:eee", "ECDSA SHA256:ccc", "SHA256:bare"})
	want := []hostKey{{"ED25519", "SHA256:eee"}, {"RSA", "SHA256:rrr"}, {"ECDSA", "SHA256:ccc"}, {"Key", "SHA256:bare"}}
	if !slices.Equal(got, want) {
		t.Fatalf("%v", got)
	}
}

// optionsDict sends only what is set: BackupList and Restore refuse a key
// they do not take.
func TestOptionsDict(t *testing.T) {
	if m := optionsDict(TargetOptions{}); len(m) != 0 {
		t.Fatalf("%v", m)
	}
	m := optionsDict(TargetOptions{HostKey: "SHA256:eee", Port: 2222})
	if len(m) != 2 || m["host_key"].Value() != "SHA256:eee" || m["port"].Value() != uint32(2222) {
		t.Fatalf("%v", m)
	}
}

// The secrets reach the daemon as the lines of a pipe whose read end is
// the fd: the password first, then NAME=value.
func TestWithSecretsWritesThePipe(t *testing.T) {
	var got string
	err := withSecrets(Credentials{Password: "pw one", Env: []string{"AWS_ACCESS_KEY_ID=K"}}, func(fd dbus.UnixFD) error {
		b, err := io.ReadAll(os.NewFile(uintptr(fd), "r"))
		got = string(b)
		return err
	})
	if err != nil || got != "pw one\nAWS_ACCESS_KEY_ID=K\n" {
		t.Fatalf("%q, %v", got, err)
	}
	long := strings.Repeat("x", 5000)
	if err := withSecrets(Credentials{Password: long}, func(dbus.UnixFD) error { return nil }); err == nil {
		t.Fatal("a 5000-byte secret was sent")
	}
}

// The daemon's error is shown in its own words; NotAuthorized is the
// usual sentence.
func TestBackupErrorKeepsTheDaemonsWords(t *testing.T) {
	refusal := "This machine is already in use; restore parts of a backup with isotope restore --only"
	err := mapBackupErr(dbus.Error{Name: updateBus + ".Error.Refused", Body: []any{refusal}})
	if err == nil || err.Error() != refusal {
		t.Fatalf("%v", err)
	}
	if err := mapBackupErr(dbus.Error{Name: updateNotAuthed, Body: []any{"no"}}); err != ErrNotAuthorized {
		t.Fatalf("%v", err)
	}
}

func TestStatusDicts(t *testing.T) {
	st := backupStateFrom(map[string]dbus.Variant{
		"available": dbus.MakeVariant(true), "can_restore": dbus.MakeVariant(false),
		"restore_refusal": dbus.MakeVariant("there is more than one user"),
	})
	if !st.Available || st.CanRestore || st.RestoreRefusal != "there is more than one user" {
		t.Fatalf("%+v", st)
	}
	js := jobStatusFrom(map[string]dbus.Variant{
		"state": dbus.MakeVariant("idle"), "backup_result": dbus.MakeVariant("failed"),
		"backup_detail": dbus.MakeVariant("wrong password"),
	})
	if js != (JobStatus{State: "idle", Result: "failed", Detail: "wrong password"}) {
		t.Fatalf("%+v", js)
	}
	snaps := snapshotsFrom([]map[string]dbus.Variant{{
		"id": dbus.MakeVariant("abcd1234"), "host": dbus.MakeVariant("anna-pc"),
		"time": dbus.MakeVariant(int64(100)), "size": dbus.MakeVariant(uint64(5)),
	}})
	if len(snaps) != 1 || snaps[0] != (Snapshot{ID: "abcd1234", Host: "anna-pc", Time: 100, Size: 5}) {
		t.Fatalf("%+v", snaps)
	}
}

// The snapshots are grouped by host, the host with the newest backup
// first, and newest first in a host.
func TestSnapshotsByHost(t *testing.T) {
	in := []Snapshot{
		{ID: "a1", Host: "a", Time: 50}, {ID: "b1", Host: "b", Time: 90},
		{ID: "a2", Host: "a", Time: 80}, {ID: "b2", Host: "b", Time: 10},
	}
	var got []string
	for _, s := range byHost(in) {
		got = append(got, s.ID)
	}
	if !slices.Equal(got, []string{"b1", "b2", "a2", "a1"}) {
		t.Fatalf("%v", got)
	}
}

func TestUSBDisksFromUdisks(t *testing.T) {
	v := dbus.MakeVariant
	objs := managedObjects{
		"/drives/usb": {udisksDrive: {"Removable": v(true)}},
		"/drives/ssd": {udisksDrive: {"Removable": v(false)}},
		"/block/sdb1": {
			udisksBlock: {"Drive": v(dbus.ObjectPath("/drives/usb")), "IdUUID": v("1234-ABCD"),
				"IdLabel": v("BACKUP"), "IdType": v("exfat"), "Size": v(uint64(64e9))},
			udisksFilesystem: {},
		},
		"/block/sdb2": { // no file system Isotope backs up to
			udisksBlock:      {"Drive": v(dbus.ObjectPath("/drives/usb")), "IdUUID": v("x"), "IdType": v("swap")},
			udisksFilesystem: {},
		},
		"/block/sda1": { // not removable
			udisksBlock:      {"Drive": v(dbus.ObjectPath("/drives/ssd")), "IdUUID": v("y"), "IdType": v("ext4")},
			udisksFilesystem: {},
		},
		"/block/sdb3": { // ignored by the udev rules
			udisksBlock: {"Drive": v(dbus.ObjectPath("/drives/usb")), "IdUUID": v("z"), "IdType": v("ext4"),
				"HintIgnore": v(true)},
			udisksFilesystem: {},
		},
		// Isotope run from a removable drive: none of its file systems,
		// not even one without a Kairos label.
		"/drives/live": {udisksDrive: {"Removable": v(true)}},
		"/block/sdc1": {
			udisksBlock:      {"Drive": v(dbus.ObjectPath("/drives/live")), "IdUUID": v("p"), "IdType": v("ext4"), "IdLabel": v("COS_PERSISTENT")},
			udisksFilesystem: {},
		},
		"/block/sdc2": {
			udisksBlock:      {"Drive": v(dbus.ObjectPath("/drives/live")), "IdUUID": v("q"), "IdType": v("vfat"), "IdLabel": v("DATA")},
			udisksFilesystem: {},
		},
		// A file system that is a system mount, and a Kairos partition name.
		"/drives/usb2": {udisksDrive: {"Removable": v(true)}},
		"/block/sdd1": {
			udisksBlock:      {"Drive": v(dbus.ObjectPath("/drives/usb2")), "IdUUID": v("m"), "IdType": v("ext4")},
			udisksFilesystem: {"MountPoints": v([][]byte{[]byte("/usr/local\x00")})},
		},
		"/block/sdd2": {
			udisksBlock:      {"Drive": v(dbus.ObjectPath("/drives/usb2")), "IdUUID": v("n"), "IdType": v("ext4")},
			udisksPartition:  {"Name": v("oem")},
			udisksFilesystem: {},
		},
	}
	got := usbDisksFrom(objs)
	if len(got) != 1 || got[0] != (USBDisk{UUID: "1234-ABCD", Label: "BACKUP", FSType: "exfat", Size: 64e9}) {
		t.Fatalf("%+v", got)
	}
}
