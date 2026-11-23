package main

// The drives and blocks of the Disks page, and the rule that keeps the
// system disk read-only. udisks2 has the same rule in its own policy
// (a system device needs filesystem-mount-system or modify-device-system,
// which no Isotope rule grants); the app keeps it too, so that it never
// offers a button that changes the system disk.

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"

	"github.com/godbus/dbus/v5"
)

// Block is one udisks2 block device: a whole disk, a partition, or the
// cleartext device of a LUKS container.
type Block struct {
	ID                          string // object path
	Device, Label, FSType, UUID string
	Size, Used                  uint64 // Used from statfs of the first mount point, 0 when not mounted
	MountPoints                 []string
	HintSystem, HintIgnore      bool
	IsFilesystem, IsPartition   bool
	IsCrypto                    bool
	// PartName is the GPT partition name ("" for none, or on MBR).
	PartName string
	// part is the partition number (of the backing device for a
	// cleartext device); cleartext marks a LUKS cleartext device. They
	// only order the blocks.
	part      uint32
	cleartext bool
}

// Drive is one udisks2 drive and its blocks.
type Drive struct {
	ID                                string // object path
	Model, Vendor, Serial             string
	Size                              uint64
	Removable, Ejectable, CanPowerOff bool
	Blocks                            []Block // whole disk first, then partitions by number
}

// systemMounts are the mount points of the running system: a device
// mounted at exactly one of them is a system device whatever its label.
var systemMounts = []string{"/", "/oem", "/usr/local"}

// kairosPartNames are the GPT partition names the Kairos installer sets
// that no other medium uses (kairos-agent's constants). Its "efi" and
// "bios" are left out: bootable USB sticks have them too, and the Kairos
// EFI partition is labelled COS_GRUB.
var kairosPartNames = map[string]bool{"oem": true, "recovery": true, "state": true, "persistent": true}

// everyDevice in the root devices means they could not be read: every
// device is then a system device, so nothing is offered for a change.
const everyDevice = "*"

// Why a device is a system device.
const (
	// reasonIsotope: a Kairos label or partition name, a LUKS volume
	// named *_LUKS, a device behind /, /oem or /usr/local, or the root
	// devices could not be read. The page says "Isotope's system disk".
	reasonIsotope = "isotope"
	// reasonInternal: only udisks2's HintSystem or HintIgnore. The page
	// says "Internal disk".
	reasonInternal = "internal"
)

// systemReason tells why b is a system device: reasonIsotope,
// reasonInternal, or "" when it is not one.
func systemReason(b Block, rootDevices []string) string {
	if strings.HasPrefix(strings.ToUpper(b.Label), "COS_") || strings.HasSuffix(strings.ToUpper(b.Label), "_LUKS") {
		return reasonIsotope
	}
	if kairosPartNames[strings.ToLower(b.PartName)] {
		return reasonIsotope
	}
	for _, r := range rootDevices {
		if r == everyDevice || (r != "" && r == b.Device) {
			return reasonIsotope
		}
	}
	for _, m := range b.MountPoints {
		for _, s := range systemMounts {
			if m == s {
				return reasonIsotope
			}
		}
	}
	if b.HintSystem || b.HintIgnore {
		return reasonInternal
	}
	return ""
}

// isSystemDevice tells whether b must never be changed from the app.
func isSystemDevice(b Block, rootDevices []string) bool {
	return systemReason(b, rootDevices) != ""
}

// driveReason is the strongest reason of the blocks of d: reasonIsotope
// if one block has it, else reasonInternal if one has that, else "".
func driveReason(d Drive, rootDevices []string) string {
	reason := ""
	for _, b := range d.Blocks {
		switch systemReason(b, rootDevices) {
		case reasonIsotope:
			return reasonIsotope
		case reasonInternal:
			reason = reasonInternal
		}
	}
	return reason
}

// systemDrive is true if any block of d is a system device. Such a drive
// offers no Mount, Format or other change.
func systemDrive(d Drive, rootDevices []string) bool {
	return driveReason(d, rootDevices) != ""
}

// backupFS are the filesystems a backup may go to.
var backupFS = map[string]bool{"ext4": true, "exfat": true, "vfat": true, "ntfs": true, "btrfs": true, "xfs": true}

// usableForBackup: b is a filesystem of ext4, exfat, vfat, ntfs, btrfs or
// xfs, with a UUID, and not a system device.
func usableForBackup(b Block) bool {
	roots, _ := rootDevices() // on an error roots is everyDevice
	return usableForBackupOn(b, roots)
}

// usableForBackupOn is usableForBackup with the root devices given.
func usableForBackupOn(b Block, roots []string) bool {
	return b.IsFilesystem && backupFS[b.FSType] && b.UUID != "" && !isSystemDevice(b, roots)
}

// statfs returns the bytes in use on the filesystem mounted at path.
var statfs = func(path string) (used uint64, err error) {
	var s syscall.Statfs_t
	if err := syscall.Statfs(path, &s); err != nil {
		return 0, err
	}
	return (s.Blocks - s.Bfree) * uint64(s.Bsize), nil
}

// fillUsage sets Used of every mounted block from statfs of its first
// mount point.
func fillUsage(ds []Drive) {
	for i := range ds {
		for j := range ds[i].Blocks {
			b := &ds[i].Blocks[j]
			if len(b.MountPoints) == 0 {
				continue
			}
			if u, err := statfs(b.MountPoints[0]); err == nil {
				b.Used = u
			}
		}
	}
}

// hostView reads the running system's mounts and block devices. The
// tests point it at a temporary tree.
type hostView struct {
	sys       string                 // "/sys"
	mountinfo func() ([]byte, error) // /proc/self/mountinfo
	// devOf is the major:minor of the device that holds the file path
	// (its st_dev).
	devOf func(path string) (string, error)
	// evalLink follows the links of a /dev path.
	evalLink func(path string) (string, error)
}

var host = hostView{
	sys:       "/sys",
	mountinfo: func() ([]byte, error) { return os.ReadFile("/proc/self/mountinfo") },
	devOf:     statDev,
	evalLink:  filepath.EvalSymlinks,
}

// rootDevices are the devices behind /, /oem and /usr/local, down to the
// partitions: through dm, LUKS, LVM and md (sysfs "slaves") and loop
// devices (the device of the backing file). On an error it returns
// everyDevice with the error, so that every device counts as system.
var rootDevices = func() ([]string, error) { return host.rootDevices() }

// statDev is the major:minor of st_dev of path.
func statDev(path string) (string, error) {
	var st syscall.Stat_t
	if err := syscall.Stat(path, &st); err != nil {
		return "", err
	}
	dev := uint64(st.Dev)
	major := (dev>>8)&0xfff | (dev>>32)&^uint64(0xfff)
	minor := dev&0xff | (dev>>12)&^uint64(0xff)
	return fmt.Sprintf("%d:%d", major, minor), nil
}

// mountEntry is one line of mountinfo.
type mountEntry struct{ majmin, point, fstype, source string }

func parseMountinfo(b []byte) []mountEntry {
	var out []mountEntry
	for _, line := range strings.Split(string(b), "\n") {
		pre, post, ok := strings.Cut(line, " - ")
		if !ok {
			continue
		}
		f := strings.Fields(pre)
		if len(f) < 5 {
			continue
		}
		e := mountEntry{majmin: f[2], point: unescapeMount(f[4])}
		if g := strings.Fields(post); len(g) >= 2 {
			e.fstype, e.source = g[0], unescapeMount(g[1])
		}
		out = append(out, e)
	}
	return out
}

// unescapeMount undoes mountinfo's octal escapes (\040 for a space).
func unescapeMount(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+3 < len(s) {
			if n, err := strconv.ParseUint(s[i+1:i+4], 8, 8); err == nil {
				b.WriteByte(byte(n))
				i += 3
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// noBlock: a major:minor of major 0 is not a block device (tmpfs,
// overlay, btrfs subvolumes).
func noBlock(majmin string) bool { return strings.HasPrefix(majmin, "0:") }

// nameOf is the kernel's name of block device major:minor (sda2, dm-0).
func (h hostView) nameOf(majmin string) (string, error) {
	l, err := os.Readlink(filepath.Join(h.sys, "dev/block", majmin))
	if err != nil {
		return "", err
	}
	return filepath.Base(l), nil
}

func (h hostView) rootDevices() ([]string, error) {
	fail := func(err error) ([]string, error) {
		return []string{everyDevice}, fmt.Errorf("cannot tell which disk holds the system: %w", err)
	}
	b, err := h.mountinfo()
	if err != nil {
		return fail(err)
	}
	w := rootWalk{h: h, mounts: parseMountinfo(b), seen: map[string]bool{}}
	sawRoot := false
	for _, m := range w.mounts {
		if !isSystemMount(m.point) {
			continue
		}
		if m.point == "/" {
			sawRoot = true
		}
		if err := w.mount(m); err != nil {
			return fail(err)
		}
	}
	if !sawRoot {
		return fail(errors.New("/ is not in the mount table"))
	}
	return w.out, nil
}

// isSystemMount: p is one of systemMounts.
func isSystemMount(p string) bool {
	for _, s := range systemMounts {
		if p == s {
			return true
		}
	}
	return false
}

// inRAM are the filesystems that are on no disk.
var inRAM = map[string]bool{"tmpfs": true, "ramfs": true, "rootfs": true}

// rootWalk collects the devices under the system mounts.
type rootWalk struct {
	h      hostView
	mounts []mountEntry
	out    []string
	seen   map[string]bool
}

// sourceName is the kernel name of a mount's /dev source after its
// links (/dev/mapper/x is dm-0), "" when it is not a known block device.
func (h hostView) sourceName(source string) string {
	if !strings.HasPrefix(source, "/dev/") {
		return ""
	}
	dev := source
	if r, err := h.evalLink(source); err == nil {
		dev = r
	}
	name := filepath.Base(dev)
	if _, err := os.Stat(filepath.Join(h.sys, "class/block", name)); err != nil {
		return ""
	}
	return name
}

// btrfsDevices are all the devices of the btrfs filesystem that has the
// device name: /sys/fs/btrfs/<fsid>/devices lists them. A btrfs on
// several disks (RAID1, a device added later) shows only one of them in
// mountinfo.
func (h hostView) btrfsDevices(name string) ([]string, error) {
	fsids, err := os.ReadDir(filepath.Join(h.sys, "fs/btrfs"))
	if err != nil {
		return nil, fmt.Errorf("the btrfs devices of %s: %w", name, err)
	}
	for _, f := range fsids {
		ents, err := os.ReadDir(filepath.Join(h.sys, "fs/btrfs", f.Name(), "devices"))
		if err != nil {
			continue // "features" and other entries that are no filesystem
		}
		var devs []string
		found := false
		for _, e := range ents {
			devs = append(devs, e.Name())
			found = found || e.Name() == name
		}
		if found {
			return devs, nil
		}
	}
	return nil, fmt.Errorf("the btrfs devices of %s: not in %s", name, filepath.Join(h.sys, "fs/btrfs"))
}

// mountDevices are the kernel names of the block devices mount m is on:
// its device number when major > 0; else (btrfs, ZFS, overlay show an
// anonymous 0:N) its /dev source; for btrfs every device of the
// filesystem. A mount in RAM is on none. A mount on no device found is an
// error.
func (h hostView) mountDevices(m mountEntry) ([]string, error) {
	var names []string
	if !noBlock(m.majmin) {
		name, err := h.nameOf(m.majmin)
		if err != nil {
			return nil, err
		}
		names = append(names, name)
	}
	if name := h.sourceName(m.source); name != "" {
		names = appendNew(names, name)
	}
	if m.fstype == "btrfs" {
		if len(names) == 0 {
			return nil, fmt.Errorf("%s: btrfs on %s, which is no known device", m.point, m.source)
		}
		all, err := h.btrfsDevices(names[0])
		if err != nil {
			return nil, err
		}
		for _, n := range all {
			names = appendNew(names, n)
		}
	}
	if len(names) == 0 && !inRAM[m.fstype] {
		return nil, fmt.Errorf("%s (%s from %s) is on no disk this app can find", m.point, m.fstype, m.source)
	}
	return names, nil
}

// holding is the mount that holds file: among the mounts with device
// number mm (the file's st_dev), the one with the longest mount point
// that is file or a directory above it. A path match alone is not
// enough: a later mount can hide the one the file is on.
func holding(mounts []mountEntry, file, mm string) (mountEntry, error) {
	var best mountEntry
	found := false
	for _, m := range mounts {
		if m.majmin != mm {
			continue
		}
		p := m.point
		if file == p || p == "/" || strings.HasPrefix(file, strings.TrimSuffix(p, "/")+"/") {
			if !found || len(p) >= len(best.point) {
				best, found = m, true
			}
		}
	}
	if !found {
		return mountEntry{}, fmt.Errorf("%s (device %s) is on no mount", file, mm)
	}
	return best, nil
}

// fileDevices are the kernel names of the block devices that hold file:
// the device itself for a /dev path (a loop set up on a partition), the
// device of its st_dev, or the devices of the mount that holds it.
func (h hostView) fileDevices(mounts []mountEntry, file string) ([]string, error) {
	if strings.HasPrefix(file, "/dev/") {
		if name := h.sourceName(file); name != "" {
			return []string{name}, nil
		}
		return nil, fmt.Errorf("%s is no known device", file)
	}
	mm, err := h.devOf(file)
	if err != nil {
		return nil, err
	}
	if !noBlock(mm) {
		name, err := h.nameOf(mm)
		if err != nil {
			return nil, err
		}
		return []string{name}, nil
	}
	m, err := holding(mounts, file, mm)
	if err != nil {
		return nil, err
	}
	return h.mountDevices(m)
}

// loopBacking is the backing file of loop device name, "" when it is not
// bound.
func (h hostView) loopBacking(name string) (string, error) {
	f, err := os.ReadFile(filepath.Join(h.sys, "class/block", name, "loop/backing_file"))
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(f)), nil
}

// mount adds the devices under mount m.
func (w *rootWalk) mount(m mountEntry) error {
	names, err := w.h.mountDevices(m)
	if err != nil {
		return err
	}
	for _, n := range names {
		if err := w.expand(n); err != nil {
			return err
		}
	}
	if strings.HasPrefix(m.source, "/dev/") {
		w.out = appendNew(w.out, m.source)
		if r, err := w.h.evalLink(m.source); err == nil {
			w.out = appendNew(w.out, r)
		}
	}
	return nil
}

// expand adds /dev/name and every device under it: the "slaves" of a dm
// or md device, and the devices that hold a loop device's backing file.
func (w *rootWalk) expand(name string) error {
	if w.seen[name] {
		return nil
	}
	w.seen[name] = true
	w.out = appendNew(w.out, "/dev/"+name)
	dir := filepath.Join(w.h.sys, "class/block", name)
	slaves, err := os.ReadDir(filepath.Join(dir, "slaves"))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	for _, s := range slaves {
		if err := w.expand(s.Name()); err != nil {
			return err
		}
	}
	if !strings.HasPrefix(name, "loop") {
		return nil
	}
	file, err := w.h.loopBacking(name)
	if err == nil && file == "" {
		err = errors.New("not bound")
	}
	if err != nil {
		return fmt.Errorf("the backing file of %s: %w", name, err)
	}
	under, err := w.h.fileDevices(w.mounts, file)
	if err != nil {
		return fmt.Errorf("the backing file of %s: %w", name, err)
	}
	for _, n := range under {
		if err := w.expand(n); err != nil {
			return err
		}
	}
	return nil
}

// mountedAbove looks for a mounted device on devs: devs themselves and
// what is stacked on them, that is the dm, LUKS, LVM and md devices that
// hold them (sysfs "holders", recursively) and the loop devices whose
// backing file is one of them or lies on one of them. A mount matches by
// its device number, by its /dev source, and for btrfs by every device
// of the filesystem. It returns what is mounted ("/dev/dm-1 at
// /run/media/anna/lv"), or "" when nothing is. What it cannot read (the
// mount table, a device missing from sysfs, a btrfs it cannot resolve, a
// loop's backing file) is an error.
func (h hostView) mountedAbove(devs []string) (string, error) {
	b, err := h.mountinfo()
	if err != nil {
		return "", err
	}
	mounts := parseMountinfo(b)
	on := map[string]string{} // a device name -> a mount point on it
	for _, m := range mounts {
		names, err := h.mountDevices(m)
		if err != nil {
			if m.fstype == "btrfs" || !noBlock(m.majmin) {
				return "", err
			}
			continue // proc, sysfs, overlay, NFS: on no block device
		}
		for _, n := range names {
			if _, ok := on[n]; !ok {
				on[n] = m.point
			}
		}
	}

	// stack: devs and everything stacked on them
	stack := map[string]bool{}
	var collect func(name string) error
	collect = func(name string) error {
		if stack[name] {
			return nil
		}
		dir := filepath.Join(h.sys, "class/block", name)
		if _, err := os.ReadFile(filepath.Join(dir, "dev")); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		stack[name] = true
		holders, err := os.ReadDir(filepath.Join(dir, "holders"))
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		for _, hd := range holders {
			if err := collect(hd.Name()); err != nil {
				return err
			}
		}
		return nil
	}
	for _, d := range devs {
		if err := collect(filepath.Base(d)); err != nil {
			return "", err
		}
	}
	// loop devices do not show in "holders": add those whose backing file
	// is on the stack, until no more are found (a loop on a loop)
	all, err := os.ReadDir(filepath.Join(h.sys, "class/block"))
	if err != nil {
		return "", err
	}
	for added := true; added; {
		added = false
		for _, e := range all {
			name := e.Name()
			if !strings.HasPrefix(name, "loop") || stack[name] {
				continue
			}
			file, err := h.loopBacking(name)
			if err != nil {
				return "", fmt.Errorf("the backing file of %s: %w", name, err)
			}
			if file == "" {
				continue
			}
			under, err := h.fileDevices(mounts, file)
			if err != nil {
				return "", fmt.Errorf("the backing file of %s: %w", name, err)
			}
			for _, u := range under {
				if stack[u] {
					if err := collect(name); err != nil {
						return "", err
					}
					added = true
					break
				}
			}
		}
	}
	var names []string
	for n := range stack {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		if p, ok := on[n]; ok {
			return "/dev/" + n + " at " + p, nil
		}
	}
	return "", nil
}

func appendNew(list []string, s string) []string {
	for _, x := range list {
		if x == s {
			return list
		}
	}
	return append(list, s)
}

// udisks2's interfaces.
const (
	udisksName       = "org.freedesktop.UDisks2"
	udisksPath       = "/org/freedesktop/UDisks2"
	udisksDrive      = udisksName + ".Drive"
	udisksBlock      = udisksName + ".Block"
	udisksPartition  = udisksName + ".Partition"
	udisksFilesystem = udisksName + ".Filesystem"
	udisksEncrypted  = udisksName + ".Encrypted"
	udisksPTable     = udisksName + ".PartitionTable"
)

type managedObjects = map[dbus.ObjectPath]map[string]map[string]dbus.Variant

// get reads a property, the zero value when it is missing or of another
// type.
func get[T any](props map[string]dbus.Variant, name string) T {
	var zero T
	if v, ok := props[name]; ok {
		if t, ok := v.Value().(T); ok {
			return t
		}
	}
	return zero
}

// cString is a D-Bus "ay" string without its NUL.
func cString(b []byte) string {
	return strings.TrimRight(string(b), "\x00")
}

// drivesFrom builds the drives from udisks2's GetManagedObjects. A block
// belongs to the drive of its Drive property; a LUKS cleartext device has
// none, and belongs to the drive of its CryptoBackingDevice. Blocks of no
// drive (loop devices) are left out. Drives are sorted by object path.
func drivesFrom(objs managedObjects) []Drive {
	byID := map[dbus.ObjectPath]*Drive{}
	var ids []dbus.ObjectPath
	for path, ifs := range objs {
		p, ok := ifs[udisksDrive]
		if !ok {
			continue
		}
		byID[path] = &Drive{
			ID:          string(path),
			Model:       get[string](p, "Model"),
			Vendor:      get[string](p, "Vendor"),
			Serial:      get[string](p, "Serial"),
			Size:        get[uint64](p, "Size"),
			Removable:   get[bool](p, "Removable"),
			Ejectable:   get[bool](p, "Ejectable"),
			CanPowerOff: get[bool](p, "CanPowerOff"),
		}
		ids = append(ids, path)
	}
	// driveOf is the drive of a block, through its backing device for a
	// cleartext device.
	driveOf := func(path dbus.ObjectPath) (dbus.ObjectPath, uint32, bool) {
		p := objs[path][udisksBlock]
		cleartext := false
		for range 4 { // a cleartext device on a cleartext device, at most a few levels
			if d := get[dbus.ObjectPath](p, "Drive"); d != "/" && d != "" {
				num := get[uint32](objs[path][udisksPartition], "Number")
				return d, num, cleartext
			}
			back := get[dbus.ObjectPath](p, "CryptoBackingDevice")
			if back == "/" || back == "" {
				break
			}
			cleartext = true
			path = back
			p = objs[path][udisksBlock]
		}
		return "", 0, false
	}
	for path, ifs := range objs {
		p, ok := ifs[udisksBlock]
		if !ok {
			continue
		}
		did, num, cleartext := driveOf(path)
		d := byID[did]
		if d == nil {
			continue
		}
		b := Block{
			ID:         string(path),
			Device:     cString(get[[]byte](p, "Device")),
			Label:      get[string](p, "IdLabel"),
			FSType:     get[string](p, "IdType"),
			UUID:       get[string](p, "IdUUID"),
			Size:       get[uint64](p, "Size"),
			HintSystem: get[bool](p, "HintSystem"),
			HintIgnore: get[bool](p, "HintIgnore"),
			part:       num,
			cleartext:  cleartext,
		}
		if pp, ok := ifs[udisksPartition]; ok {
			b.IsPartition = true
			b.PartName = get[string](pp, "Name")
		}
		_, b.IsCrypto = ifs[udisksEncrypted]
		if fs, ok := ifs[udisksFilesystem]; ok {
			b.IsFilesystem = true
			for _, m := range get[[][]byte](fs, "MountPoints") {
				b.MountPoints = append(b.MountPoints, cString(m))
			}
		}
		d.Blocks = append(d.Blocks, b)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	out := make([]Drive, 0, len(ids))
	for _, id := range ids {
		d := byID[id]
		sort.SliceStable(d.Blocks, func(i, j int) bool {
			a, b := d.Blocks[i], d.Blocks[j]
			if a.part != b.part {
				return a.part < b.part
			}
			if a.cleartext != b.cleartext {
				return !a.cleartext
			}
			return a.ID < b.ID
		})
		out = append(out, *d)
	}
	return out
}
