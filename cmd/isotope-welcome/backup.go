package main

// The backup methods of isotope-updated that "Restore from a backup" uses,
// and the targets it describes. The target functions are twins of those in
// cmd/isotope-system/backup_setup.go (Welcome does not import cmd/isotope-system/); a change to the
// target format changes both. Secrets go to the daemon only through a pipe
// fd (withSecrets), never in a D-Bus string or a target.

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/godbus/dbus/v5"
)

// BackupState is what BackupStatus says about restoring. CanRestore is the
// daemon's machine-is-new rule; RestoreRefusal says why not.
type BackupState struct {
	Available      bool // restic is on the image
	CanRestore     bool
	RestoreRefusal string
}

// JobStatus is the part of the daemon's Status a restore needs: the state
// ("restore" while it runs), and how the last backup job ended.
type JobStatus struct {
	State, Result, Detail string
}

// Credentials go to the daemon through a pipe: the backup password, then
// the target's NAME=value lines.
type Credentials struct {
	Password string
	Env      []string
}

// TargetOptions are the a{sv} options of BackupList and Restore: an SFTP
// server's key (the bare SHA256: fingerprint) and port.
type TargetOptions struct {
	HostKey string
	Port    uint32
}

// Snapshot is one backup of BackupList.
type Snapshot struct {
	ID, Host string
	Time     int64
	Size     uint64
}

// USBDisk is a file system on a removable drive that can hold a backup.
type USBDisk struct {
	UUID, Label, FSType string
	Size                uint64
}

// Backup is the backup methods of isotope-updated. Every method blocks on
// D-Bus; the pages call them off the UI thread.
type Backup interface {
	BackupStatus() (BackupState, error)
	JobStatus() (JobStatus, error)
	BackupSSHKey() (string, error)
	BackupHostKey(host string, port uint32) ([]string, error)
	BackupList(source string, opt TargetOptions, cred Credentials) ([]Snapshot, error)
	Restore(source, snapshot, host string, opt TargetOptions, cred Credentials) error
}

// busBackup is Backup over the system bus.
type busBackup struct{}

// longCall bounds BackupList and BackupHostKey: the daemon gives up after
// 110 s.
const longCall = 120 * time.Second

func (busBackup) call(flags dbus.Flags, timeout time.Duration, method string, out []any, args ...any) error {
	conn, err := dbus.ConnectSystemBus()
	if err != nil {
		return err
	}
	defer conn.Close()
	ctx := context.Background()
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	c := conn.Object(updateBus, updatePath).CallWithContext(ctx, updateBus+"."+method, flags, args...)
	if c.Err != nil {
		return mapBackupErr(c.Err)
	}
	if len(out) > 0 {
		return c.Store(out...)
	}
	return nil
}

func (b busBackup) BackupStatus() (BackupState, error) {
	var m map[string]dbus.Variant
	if err := b.call(0, 0, "BackupStatus", []any{&m}); err != nil {
		return BackupState{}, err
	}
	return backupStateFrom(m), nil
}

// JobStatus does not start the service: a restart may be under way.
func (b busBackup) JobStatus() (JobStatus, error) {
	var m map[string]dbus.Variant
	if err := b.call(dbus.FlagNoAutoStart, 0, "Status", []any{&m}); err != nil {
		return JobStatus{}, err
	}
	return jobStatusFrom(m), nil
}

func (b busBackup) BackupSSHKey() (string, error) {
	var key string
	err := b.call(0, 0, "BackupSSHKey", []any{&key})
	return key, err
}

func (b busBackup) BackupHostKey(host string, port uint32) ([]string, error) {
	var fps []string
	err := b.call(0, longCall, "BackupHostKey", []any{&fps}, host, port)
	return fps, err
}

func (b busBackup) BackupList(source string, opt TargetOptions, cred Credentials) ([]Snapshot, error) {
	var list []map[string]dbus.Variant
	err := withSecrets(cred, func(fd dbus.UnixFD) error {
		return b.call(0, longCall, "BackupList", []any{&list}, source, optionsDict(opt), fd)
	})
	if err != nil {
		return nil, err
	}
	return snapshotsFrom(list), nil
}

func (b busBackup) Restore(source, snapshot, host string, opt TargetOptions, cred Credentials) error {
	return withSecrets(cred, func(fd dbus.UnixFD) error {
		return b.call(0, 0, "Restore", nil, source, snapshot, host, optionsDict(opt), fd)
	})
}

// mapBackupErr is mapSettingsErr: NotAuthorized, or the daemon's own words.
func mapBackupErr(err error) error { return mapSettingsErr(err) }

func str(m map[string]dbus.Variant, k string) string {
	s, _ := m[k].Value().(string)
	return s
}

func boolean(m map[string]dbus.Variant, k string) bool {
	b, _ := m[k].Value().(bool)
	return b
}

func backupStateFrom(m map[string]dbus.Variant) BackupState {
	return BackupState{Available: boolean(m, "available"), CanRestore: boolean(m, "can_restore"),
		RestoreRefusal: str(m, "restore_refusal")}
}

func jobStatusFrom(m map[string]dbus.Variant) JobStatus {
	return JobStatus{State: str(m, "state"), Result: str(m, "backup_result"), Detail: str(m, "backup_detail")}
}

func snapshotsFrom(list []map[string]dbus.Variant) []Snapshot {
	out := make([]Snapshot, 0, len(list))
	for _, m := range list {
		t, _ := m["time"].Value().(int64)
		size, _ := m["size"].Value().(uint64)
		out = append(out, Snapshot{ID: str(m, "id"), Host: str(m, "host"), Time: t, Size: size})
	}
	return out
}

// optionsDict is TargetOptions as the daemon's a{sv}, without the zero
// values: BackupList and Restore refuse a key they do not take.
func optionsDict(o TargetOptions) map[string]dbus.Variant {
	m := map[string]dbus.Variant{}
	if o.HostKey != "" {
		m["host_key"] = dbus.MakeVariant(o.HostKey)
	}
	if o.Port != 0 {
		m["port"] = dbus.MakeVariant(o.Port)
	}
	return m
}

// withSecrets passes the credentials to the daemon through a pipe: the
// D-Bus message carries the read end as an fd, never the text. Lines
// joined by newlines, at most 4096 bytes, as the daemon reads them.
func withSecrets(c Credentials, call func(dbus.UnixFD) error) error {
	payload := []byte(strings.Join(append([]string{c.Password}, c.Env...), "\n") + "\n")
	defer clear(payload)
	if len(payload) > 4096 {
		return errors.New("the secret is too long")
	}
	r, w, err := os.Pipe()
	if err != nil {
		return err
	}
	defer r.Close()
	// A pipe buffer holds at least 4096 bytes, so this write never blocks.
	_, werr := w.Write(payload)
	w.Close()
	if werr != nil {
		return werr
	}
	return call(dbus.UnixFD(r.Fd()))
}

// byHost groups the snapshots by host, the host with the newest backup
// first, and puts each host's newest first. The first is the default.
func byHost(snaps []Snapshot) []Snapshot {
	newest := map[string]int64{}
	for _, s := range snaps {
		newest[s.Host] = max(newest[s.Host], s.Time)
	}
	out := append([]Snapshot(nil), snaps...)
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Host != b.Host {
			if newest[a.Host] != newest[b.Host] {
				return newest[a.Host] > newest[b.Host]
			}
			return a.Host < b.Host
		}
		return a.Time > b.Time
	})
	return out
}

// ---- targets: twins of cmd/isotope-system/backup_setup.go ----

const (
	whereUSB    = "usb"
	whereRemote = "remote"

	remoteREST = "rest"
	remoteS3   = "s3"
	remoteSFTP = "sftp"

	defaultFolder = "isotope-backup"
)

var remoteKinds = []struct{ ID, Name, Sub string }{
	{remoteREST, "REST server", "restic's rest-server, over HTTP or HTTPS"},
	{remoteS3, "S3", "Amazon S3, or a service that works like it"},
	{remoteSFTP, "SFTP", "A server you reach with SSH"},
}

func hasControl(s string) bool { return strings.ContainsFunc(s, unicode.IsControl) }

// cleanFolder is a folder on the disk without the slashes around it.
func cleanFolder(folder string) (string, error) {
	f := strings.TrimSpace(folder)
	if hasControl(f) {
		return "", errors.New("The folder name has a control character.")
	}
	for _, part := range strings.Split(f, "/") {
		if part == "." || part == ".." {
			return "", errors.New("The folder cannot have . or .. in it.")
		}
	}
	f = strings.Trim(path.Clean("/"+f), "/")
	if f == "" {
		return "", errors.New("Type the folder on the disk where the backup is.")
	}
	return f, nil
}

// usbTarget is uuid:<UUID>/<folder>: the daemon finds the disk by the UUID
// of its file system and mounts it.
func usbTarget(d USBDisk, folder string) (string, error) {
	f, err := cleanFolder(folder)
	if err != nil {
		return "", err
	}
	return "uuid:" + d.UUID + "/" + f, nil
}

// envLine is NAME=value, refused when the value would break the lines of
// the credentials pipe.
func envLine(name, value string) (string, error) {
	if strings.ContainsAny(value, "\r\n\x00") {
		return "", fmt.Errorf("The value of %s has a line break in it.", name)
	}
	return name + "=" + value, nil
}

func envLines(kvs [][2]string) ([]string, error) {
	var env []string
	for _, kv := range kvs {
		if kv[1] == "" {
			continue
		}
		l, err := envLine(kv[0], kv[1])
		if err != nil {
			return nil, err
		}
		env = append(env, l)
	}
	return env, nil
}

// restTarget is rest:<url>, with the user and the password as credentials.
func restTarget(raw, user, password string) (string, []string, error) {
	u := strings.TrimSpace(raw)
	pu, err := url.Parse(u)
	if u == "" || hasControl(u) || err != nil || (pu.Scheme != "http" && pu.Scheme != "https") || pu.Host == "" {
		return "", nil, errors.New("Type the address of the server, starting with http:// or https://.")
	}
	if pu.User != nil {
		return "", nil, errors.New("Put the user and the password in their fields, not in the address.")
	}
	env, err := envLines([][2]string{{"RESTIC_REST_USERNAME", user}, {"RESTIC_REST_PASSWORD", password}})
	if err != nil {
		return "", nil, err
	}
	return "rest:" + u, env, nil
}

// s3Target is s3:<endpoint>/<bucket>/<path>, with the keys and the region
// as credentials.
func s3Target(endpoint, bucket, folder, keyID, secret, region string) (string, []string, error) {
	e := strings.TrimRight(strings.TrimSpace(endpoint), "/")
	b := strings.TrimSpace(bucket)
	p := strings.Trim(strings.TrimSpace(folder), "/")
	switch {
	case e == "" || hasControl(e) || strings.ContainsAny(e, "@ "):
		return "", nil, errors.New("Type the endpoint of the service, such as https://s3.amazonaws.com.")
	case b == "" || hasControl(b) || strings.ContainsAny(b, "/ "):
		return "", nil, errors.New("Type the name of the bucket.")
	case hasControl(p):
		return "", nil, errors.New("The path has a control character.")
	case strings.TrimSpace(keyID) == "" || secret == "":
		return "", nil, errors.New("Type the access key ID and the secret access key.")
	}
	t := "s3:" + e + "/" + b
	if p != "" {
		t += "/" + p
	}
	env, err := envLines([][2]string{{"AWS_ACCESS_KEY_ID", strings.TrimSpace(keyID)}, {"AWS_SECRET_ACCESS_KEY", secret}, {"AWS_DEFAULT_REGION", strings.TrimSpace(region)}})
	if err != nil {
		return "", nil, err
	}
	return t, env, nil
}

const hostChars = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789.-_"

// sftpTarget is sftp:user@host:path on port 22, and
// sftp://user@host:port/path on another port. host and port are for
// BackupHostKey.
func sftpTarget(userHost, portText, folder string) (target, host string, port uint32, err error) {
	uh := strings.TrimSpace(userHost)
	user, host := "", uh
	if i := strings.LastIndex(uh, "@"); i >= 0 {
		user, host = uh[:i], uh[i+1:]
		if user == "" || strings.HasPrefix(user, "-") || hasControl(user) || strings.ContainsAny(user, " :/@") {
			return "", "", 0, errors.New("Type the user and the server as user@server.")
		}
	}
	if host == "" || strings.HasPrefix(host, "-") || strings.Trim(host, hostChars) != "" {
		return "", "", 0, errors.New("Type the user and the server as user@server.")
	}
	n := uint64(22)
	if ps := strings.TrimSpace(portText); ps != "" {
		n, err = strconv.ParseUint(ps, 10, 16)
		if err != nil || n == 0 {
			return "", "", 0, errors.New("The port is a number from 1 to 65535.")
		}
	}
	p := strings.TrimSpace(folder)
	if p == "" || hasControl(p) {
		return "", "", 0, errors.New("Type the folder on the server where the backup is.")
	}
	if n == 22 {
		return "sftp:" + uh + ":" + p, host, 22, nil
	}
	return "sftp://" + uh + ":" + strconv.FormatUint(n, 10) + "/" + p, host, uint32(n), nil
}

func hostPort(host string, port uint32) string { return fmt.Sprintf("%s:%d", host, port) }

// hostKey is one key BackupHostKey sent: "TYPE SHA256:...".
type hostKey struct{ typ, fingerprint string }

// hostKeyChoices are the keys BackupHostKey sent, ED25519 first. An entry
// without a type is "Key".
func hostKeyChoices(fps []string) []hostKey {
	var out []hostKey
	for _, s := range fps {
		f := strings.Fields(s)
		switch len(f) {
		case 0:
			continue
		case 1:
			out = append(out, hostKey{typ: "Key", fingerprint: f[0]})
		default:
			out = append(out, hostKey{typ: f[0], fingerprint: f[len(f)-1]})
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].typ == "ED25519" && out[j].typ != "ED25519" })
	return out
}

// ---- USB disks, from udisks2 ----

const (
	udisksName       = "org.freedesktop.UDisks2"
	udisksPath       = dbus.ObjectPath("/org/freedesktop/UDisks2")
	udisksDrive      = "org.freedesktop.UDisks2.Drive"
	udisksBlock      = "org.freedesktop.UDisks2.Block"
	udisksFilesystem = "org.freedesktop.UDisks2.Filesystem"
	udisksPartition  = "org.freedesktop.UDisks2.Partition"
)

type managedObjects = map[dbus.ObjectPath]map[string]map[string]dbus.Variant

// backupFS are the file systems a backup may be on. Twin of backupFS in
// cmd/isotope-system/disks_model.go.
var backupFS = map[string]bool{"ext4": true, "exfat": true, "vfat": true, "ntfs": true, "btrfs": true, "xfs": true}

func get[T any](m map[string]dbus.Variant, k string) T {
	v, _ := m[k].Value().(T)
	return v
}

// systemMounts and kairosPartNames: twins of cmd/isotope-system/disks_model.go.
var (
	systemMounts    = []string{"/", "/oem", "/usr/local"}
	kairosPartNames = map[string]bool{"oem": true, "recovery": true, "state": true, "persistent": true}
)

// udisksBlockInfo is what the backup rule reads of one udisks2 block.
type udisksBlockInfo struct {
	disk                   USBDisk
	partName               string
	mountPoints            []string
	hintSystem, hintIgnore bool
	isFilesystem           bool
}

// isotopeBlock is the reasonIsotope part of systemReason in
// cmd/isotope-system/disks_model.go (a twin), without its walk of /sys for the devices
// behind /, /oem and /usr/local: a Kairos label or partition name, a
// *_LUKS volume, or a system mount point. usbDisksFrom refuses every file
// system of a drive that has such a block, which covers what that walk
// finds (a cleartext device on a COS_*_LUKS partition, for one).
func isotopeBlock(b udisksBlockInfo) bool {
	l := strings.ToUpper(b.disk.Label)
	if strings.HasPrefix(l, "COS_") || strings.HasSuffix(l, "_LUKS") || kairosPartNames[strings.ToLower(b.partName)] {
		return true
	}
	for _, m := range b.mountPoints {
		for _, s := range systemMounts {
			if m == s {
				return true
			}
		}
	}
	return false
}

// usbDisksFrom are the file systems a backup may be on, by the rule of
// System's Backup page (backupDisks and usableForBackupOn in cmd/isotope-system/,
// twins): a file system on a removable drive, of a type in backupFS, with
// a UUID, and not a system device (HintSystem and HintIgnore count too). A
// drive with any Isotope block (Isotope run from a USB disk) offers none of
// its file systems. A cleartext device
// belongs to the drive of its backing device.
func usbDisksFrom(objs managedObjects) []USBDisk {
	driveOf := func(path dbus.ObjectPath) dbus.ObjectPath {
		p := objs[path][udisksBlock]
		for range 4 {
			if d := get[dbus.ObjectPath](p, "Drive"); d != "/" && d != "" {
				return d
			}
			back := get[dbus.ObjectPath](p, "CryptoBackingDevice")
			if back == "/" || back == "" {
				return ""
			}
			p = objs[back][udisksBlock]
		}
		return ""
	}
	byDrive := map[dbus.ObjectPath][]udisksBlockInfo{}
	systemDrive := map[dbus.ObjectPath]bool{}
	for path, ifs := range objs {
		b, ok := ifs[udisksBlock]
		if !ok {
			continue
		}
		d := driveOf(path)
		if d == "" {
			continue
		}
		info := udisksBlockInfo{
			disk: USBDisk{UUID: get[string](b, "IdUUID"), Label: get[string](b, "IdLabel"),
				FSType: get[string](b, "IdType"), Size: get[uint64](b, "Size")},
			partName:   get[string](ifs[udisksPartition], "Name"),
			hintSystem: get[bool](b, "HintSystem"), hintIgnore: get[bool](b, "HintIgnore"),
		}
		if fs, ok := ifs[udisksFilesystem]; ok {
			info.isFilesystem = true
			for _, m := range get[[][]byte](fs, "MountPoints") {
				info.mountPoints = append(info.mountPoints, strings.TrimRight(string(m), "\x00"))
			}
		}
		if isotopeBlock(info) {
			systemDrive[d] = true
		}
		byDrive[d] = append(byDrive[d], info)
	}
	var out []USBDisk
	for d, blocks := range byDrive {
		if systemDrive[d] || !get[bool](objs[d][udisksDrive], "Removable") {
			continue
		}
		for _, b := range blocks {
			if b.isFilesystem && backupFS[b.disk.FSType] && b.disk.UUID != "" && !b.hintSystem && !b.hintIgnore {
				out = append(out, b.disk)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Label+out[i].UUID < out[j].Label+out[j].UUID })
	return out
}

// USBDisksOnBus lists the USB disks through udisks2, as the user (reading
// needs no polkit).
func USBDisksOnBus() ([]USBDisk, error) {
	conn, err := dbus.ConnectSystemBus()
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	var objs managedObjects
	if err := conn.Object(udisksName, udisksPath).
		Call("org.freedesktop.DBus.ObjectManager.GetManagedObjects", 0).Store(&objs); err != nil {
		return nil, fmt.Errorf("udisks2: %w", err)
	}
	return usbDisksFrom(objs), nil
}

// humanSize is a size in bytes as people read it (cmd/isotope-system/page_disks.go).
func humanSize(n uint64) string {
	units := []string{"kB", "MB", "GB", "TB", "PB"}
	if n < 1000 {
		return fmt.Sprintf("%d bytes", n)
	}
	v := float64(n) / 1000
	i := 0
	for v >= 1000 && i < len(units)-1 {
		v /= 1000
		i++
	}
	return fmt.Sprintf("%.1f %s", v, units[i])
}

// ago is a date the way a person says it (cmd/isotope-system/model.go).
func ago(now, t time.Time) string {
	y1, m1, d1 := now.Date()
	y2, m2, d2 := t.Date()
	switch {
	case y1 == y2 && m1 == m2 && d1 == d2:
		return "today at " + t.Format("15:04")
	case now.AddDate(0, 0, -1).Format("2006-01-02") == t.Format("2006-01-02"):
		return "yesterday at " + t.Format("15:04")
	}
	return t.Format("2 January 2006, 15:04")
}
