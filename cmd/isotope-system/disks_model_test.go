package main

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/godbus/dbus/v5"
)

func TestIsSystemDevice(t *testing.T) {
	roots := []string{"/dev/sda2"}
	cases := []struct {
		name string
		b    Block
		want bool
	}{
		{"COS_PERSISTENT", Block{Device: "/dev/sda5", Label: "COS_PERSISTENT", FSType: "ext4", IsFilesystem: true}, true},
		{"COS_LIVE", Block{Device: "/dev/sdb1", Label: "COS_LIVE", FSType: "iso9660", IsFilesystem: true}, true},
		{"COS_OEM_LUKS", Block{Device: "/dev/sda2", Label: "COS_OEM_LUKS", FSType: "crypto_LUKS", IsCrypto: true}, true},
		{"other _LUKS", Block{Device: "/dev/sdb3", Label: "DATA_LUKS", FSType: "crypto_LUKS", IsCrypto: true}, true},
		{"HintSystem", Block{Device: "/dev/nvme0n1p1", FSType: "ext4", HintSystem: true, IsFilesystem: true}, true},
		{"HintIgnore", Block{Device: "/dev/sdb1", FSType: "ext4", HintIgnore: true, IsFilesystem: true}, true},
		{"in rootDevices", Block{Device: "/dev/sda2", Label: "data", FSType: "ext4", IsFilesystem: true}, true},
		{"mounted at /", Block{Device: "/dev/sdc1", FSType: "ext4", IsFilesystem: true, MountPoints: []string{"/"}}, true},
		{"mounted at /usr/local", Block{Device: "/dev/sdc1", FSType: "ext4", IsFilesystem: true, MountPoints: []string{"/usr/local"}}, true},
		{"mounted at /oem", Block{Device: "/dev/sdc1", FSType: "ext4", IsFilesystem: true, MountPoints: []string{"/oem"}}, true},
		{"USB exfat", Block{Device: "/dev/sdb1", Label: "USB", FSType: "exfat", UUID: "1234-ABCD", IsFilesystem: true, IsPartition: true}, false},
		{"no label ext4", Block{Device: "/dev/sdb1", FSType: "ext4", IsFilesystem: true}, false},
		{"lower-case cos_", Block{Device: "/dev/sdb1", Label: "cos_photos", FSType: "ext4", IsFilesystem: true}, true},
		{"lower-case _luks", Block{Device: "/dev/sdb3", Label: "cos_oem_luks", FSType: "crypto_LUKS", IsCrypto: true}, true},
		{"GPT name persistent", Block{Device: "/dev/sdb4", PartName: "persistent", FSType: "ext4", IsPartition: true}, true},
		{"GPT name STATE", Block{Device: "/dev/sdb2", PartName: "STATE", FSType: "ext4", IsPartition: true}, true},
		{"GPT name bios", Block{Device: "/dev/sdb1", PartName: "bios", IsPartition: true}, false},
		{"GPT name EFI", Block{Device: "/dev/sdb1", PartName: "EFI", Label: "EFI", FSType: "vfat", IsFilesystem: true, IsPartition: true}, false},
		{"GPT name photos", Block{Device: "/dev/sdb1", PartName: "photos", FSType: "exfat", IsPartition: true}, false},
		{"root devices unreadable", Block{Device: "/dev/sdb1", Label: "USB", FSType: "exfat", IsFilesystem: true}, true},
		{"USB mounted elsewhere", Block{Device: "/dev/sdb1", Label: "USB", FSType: "exfat", IsFilesystem: true, MountPoints: []string{"/run/media/anna/USB"}}, false},
	}
	for _, c := range cases {
		roots := roots
		if c.name == "root devices unreadable" {
			roots = []string{everyDevice}
		}
		if got := isSystemDevice(c.b, roots); got != c.want {
			t.Errorf("%s: isSystemDevice = %v, want %v", c.name, got, c.want)
		}
	}
}

// udisks2 objects for the tests: a Drive, and Blocks with Partition,
// Filesystem and Encrypted interfaces.
const (
	ifDrive  = "org.freedesktop.UDisks2.Drive"
	ifBlock  = "org.freedesktop.UDisks2.Block"
	ifPart   = "org.freedesktop.UDisks2.Partition"
	ifFS     = "org.freedesktop.UDisks2.Filesystem"
	ifCrypt  = "org.freedesktop.UDisks2.Encrypted"
	ifPTable = "org.freedesktop.UDisks2.PartitionTable"
	udPath   = "/org/freedesktop/UDisks2/"
)

type objs = map[dbus.ObjectPath]map[string]map[string]dbus.Variant

// cstr is a D-Bus "ay" string: the bytes and a NUL.
func cstr(s string) []byte { return append([]byte(s), 0) }

func driveObj(model string, size uint64, removable bool) map[string]map[string]dbus.Variant {
	return map[string]map[string]dbus.Variant{ifDrive: {
		"Model": v(model), "Vendor": v("Vendor"), "Serial": v("S-" + model), "Size": v(size),
		"Removable": v(removable), "Ejectable": v(removable), "CanPowerOff": v(removable),
	}}
}

// blockObj is a Block on drive; num > 0 makes it a partition.
func blockObj(drive, dev, label, fstype, uuid string, size uint64, num uint32, mounts ...string) map[string]map[string]dbus.Variant {
	o := map[string]map[string]dbus.Variant{ifBlock: {
		"Device": v(cstr(dev)), "IdLabel": v(label), "IdType": v(fstype), "IdUUID": v(uuid),
		"Size": v(size), "Drive": v(dbus.ObjectPath(drive)),
		"HintSystem": v(false), "HintIgnore": v(false), "CryptoBackingDevice": v(dbus.ObjectPath("/")),
	}}
	if num > 0 {
		o[ifPart] = map[string]dbus.Variant{"Number": v(num), "Table": v(dbus.ObjectPath("/x")), "Name": v("")}
	} else {
		o[ifPTable] = map[string]dbus.Variant{"Type": v("dos")}
	}
	switch fstype {
	case "crypto_LUKS":
		o[ifCrypt] = map[string]dbus.Variant{}
	case "", "swap":
	default:
		mp := [][]byte{}
		for _, m := range mounts {
			mp = append(mp, cstr(m))
		}
		o[ifFS] = map[string]dbus.Variant{"MountPoints": v(mp)}
	}
	return o
}

func TestDrivesFromManagedObjects(t *testing.T) {
	usb := udPath + "drives/SanDisk"
	o := objs{
		dbus.ObjectPath(usb): driveObj("Cruzer", 32e9, true),
		// partitions first and out of order: the result sorts them
		udPath + "block_devices/sdb2": blockObj(usb, "/dev/sdb2", "", "ext4", "u2", 12e9, 2),
		udPath + "block_devices/sdb1": blockObj(usb, "/dev/sdb1", "USB", "exfat", "1234-ABCD", 20e9, 1, "/run/media/anna/USB"),
		udPath + "block_devices/sdb":  blockObj(usb, "/dev/sdb", "", "", "", 32e9, 0),
		// a loop device has no drive and is left out
		udPath + "block_devices/loop0": blockObj("/", "/dev/loop0", "COS_ACTIVE", "ext2", "u", 3e9, 0, "/"),
	}
	o[udPath+"block_devices/sdb1"][ifPart]["Name"] = v("photos")
	got := drivesFrom(o)
	if len(got) != 1 {
		t.Fatalf("got %d drives, want 1: %+v", len(got), got)
	}
	d := got[0]
	if d.ID != usb || d.Model != "Cruzer" || d.Vendor != "Vendor" || d.Serial != "S-Cruzer" || d.Size != 32e9 || !d.Removable || !d.Ejectable || !d.CanPowerOff {
		t.Errorf("drive = %+v", d)
	}
	var devs []string
	for _, b := range d.Blocks {
		devs = append(devs, b.Device)
	}
	if want := []string{"/dev/sdb", "/dev/sdb1", "/dev/sdb2"}; !reflect.DeepEqual(devs, want) {
		t.Fatalf("blocks %v, want %v", devs, want)
	}
	p1 := d.Blocks[1]
	if p1.ID != udPath+"block_devices/sdb1" || p1.Label != "USB" || p1.FSType != "exfat" || p1.UUID != "1234-ABCD" || p1.Size != 20e9 ||
		!p1.IsFilesystem || !p1.IsPartition || p1.IsCrypto {
		t.Errorf("sdb1 = %+v", p1)
	}
	if p1.PartName != "photos" {
		t.Errorf("sdb1 GPT name %q", p1.PartName)
	}
	if !reflect.DeepEqual(p1.MountPoints, []string{"/run/media/anna/USB"}) {
		t.Errorf("sdb1 mount points %q", p1.MountPoints)
	}
	if d.Blocks[0].IsPartition || d.Blocks[0].IsFilesystem {
		t.Errorf("the whole disk = %+v", d.Blocks[0])
	}
	if d.Blocks[2].MountPoints != nil {
		t.Errorf("sdb2 is not mounted, got %q", d.Blocks[2].MountPoints)
	}
}

func TestDrivesFromIsSortedAndKeepsEmptyDrives(t *testing.T) {
	o := objs{
		udPath + "drives/B": driveObj("B", 1, true),
		udPath + "drives/A": driveObj("A", 1, true),
	}
	got := drivesFrom(o)
	if len(got) != 2 || got[0].Model != "A" || got[1].Model != "B" {
		t.Fatalf("drives %+v", got)
	}
}

// kairosDisk is an internal disk with Kairos on it, its persistent
// partition in a LUKS container. label is the container's label.
func kairosDisk(luksLabel string) objs {
	nvme := udPath + "drives/Samsung"
	return objs{
		dbus.ObjectPath(nvme):              driveObj("Samsung SSD", 512e9, false),
		udPath + "block_devices/nvme0n1":   blockObj(nvme, "/dev/nvme0n1", "", "", "", 512e9, 0),
		udPath + "block_devices/nvme0n1p1": blockObj(nvme, "/dev/nvme0n1p1", "", "vfat", "EF", 1e8, 1),
		udPath + "block_devices/nvme0n1p2": blockObj(nvme, "/dev/nvme0n1p2", luksLabel, "crypto_LUKS", "L", 500e9, 2),
		// the cleartext device: no drive of its own, found through its
		// CryptoBackingDevice
		udPath + "block_devices/dm_2d0": func() map[string]map[string]dbus.Variant {
			b := blockObj("/", "/dev/dm-0", "data", "ext4", "C", 500e9, 0, "/srv")
			b[ifBlock]["CryptoBackingDevice"] = v(dbus.ObjectPath(udPath + "block_devices/nvme0n1p2"))
			delete(b, ifPTable)
			return b
		}(),
	}
}

func TestSystemDriveIfAnyBlockIs(t *testing.T) {
	one := func(o objs) Drive {
		t.Helper()
		ds := drivesFrom(o)
		if len(ds) != 1 {
			t.Fatalf("got %d drives, want 1", len(ds))
		}
		return ds[0]
	}
	stick := udPath + "drives/Stick"
	// Kairos installed on a USB stick: removable, one COS_STATE partition
	kairosStick := one(objs{
		dbus.ObjectPath(stick):        driveObj("Stick", 16e9, true),
		udPath + "block_devices/sdb":  blockObj(stick, "/dev/sdb", "", "", "", 16e9, 0),
		udPath + "block_devices/sdb1": blockObj(stick, "/dev/sdb1", "", "vfat", "A", 1e8, 1),
		udPath + "block_devices/sdb2": blockObj(stick, "/dev/sdb2", "COS_STATE", "ext4", "B", 8e9, 2),
		udPath + "block_devices/sdb3": blockObj(stick, "/dev/sdb3", "photos", "exfat", "C", 7e9, 3),
	})
	if !systemDrive(kairosStick, nil) {
		t.Error("a stick with a COS_STATE partition is not a system drive")
	}

	plain := one(objs{
		dbus.ObjectPath(stick):        driveObj("Stick", 16e9, true),
		udPath + "block_devices/sdb":  blockObj(stick, "/dev/sdb", "", "", "", 16e9, 0),
		udPath + "block_devices/sdb1": blockObj(stick, "/dev/sdb1", "USB", "exfat", "C", 16e9, 1),
	})
	if systemDrive(plain, []string{"/dev/sda2"}) {
		t.Error("a plain USB stick is a system drive")
	}

	// a drive found only through its partition: nothing marks it but the
	// partition behind /usr/local
	if !systemDrive(plain, []string{"/dev/sdb1"}) {
		t.Error("the drive of a root device is not a system drive")
	}
	// ... or through the partition's own mount point
	mounted := one(objs{
		dbus.ObjectPath(stick):        driveObj("Stick", 16e9, true),
		udPath + "block_devices/sdb":  blockObj(stick, "/dev/sdb", "", "", "", 16e9, 0),
		udPath + "block_devices/sdb1": blockObj(stick, "/dev/sdb1", "", "ext4", "C", 16e9, 1, "/usr/local"),
	})
	if !systemDrive(mounted, nil) {
		t.Error("the drive whose partition is mounted at /usr/local is not a system drive")
	}

	// a LUKS container with a plain label, whose cleartext holds /: the
	// cleartext belongs to the container's drive
	luks := one(kairosDisk("secret"))
	var devs []string
	for _, b := range luks.Blocks {
		devs = append(devs, b.Device)
	}
	if want := []string{"/dev/nvme0n1", "/dev/nvme0n1p1", "/dev/nvme0n1p2", "/dev/dm-0"}; !reflect.DeepEqual(devs, want) {
		t.Fatalf("blocks %v, want %v", devs, want)
	}
	if !luks.Blocks[2].IsCrypto || luks.Blocks[2].IsFilesystem {
		t.Errorf("the container = %+v", luks.Blocks[2])
	}
	if systemDrive(luks, nil) {
		t.Error("nothing marks this disk yet, but it is a system drive")
	}
	if !systemDrive(luks, []string{"/dev/dm-0"}) {
		t.Error("the disk whose LUKS cleartext holds / is not a system drive")
	}
	// the Kairos label alone is enough
	if !systemDrive(one(kairosDisk("COS_PERSISTENT_LUKS")), nil) {
		t.Error("a disk with COS_PERSISTENT_LUKS is not a system drive")
	}
}

func TestUsableForBackup(t *testing.T) {
	old := rootDevices
	rootDevices = func() ([]string, error) { return []string{"/dev/sda5"}, nil }
	t.Cleanup(func() { rootDevices = old })
	fs := func(fstype, uuid, label, dev string) Block {
		return Block{Device: dev, Label: label, FSType: fstype, UUID: uuid, IsFilesystem: fstype != "swap" && fstype != "crypto_LUKS", IsCrypto: fstype == "crypto_LUKS"}
	}
	cases := []struct {
		name string
		b    Block
		want bool
	}{
		{"exfat", fs("exfat", "1234-ABCD", "USB", "/dev/sdb1"), true},
		{"ext4", fs("ext4", "u", "", "/dev/sdb1"), true},
		{"vfat", fs("vfat", "u", "", "/dev/sdb1"), true},
		{"ntfs", fs("ntfs", "u", "", "/dev/sdb1"), true},
		{"btrfs", fs("btrfs", "u", "", "/dev/sdb1"), true},
		{"xfs", fs("xfs", "u", "", "/dev/sdb1"), true},
		{"swap", fs("swap", "u", "", "/dev/sdb1"), false},
		{"crypto_LUKS", fs("crypto_LUKS", "u", "", "/dev/sdb1"), false},
		{"iso9660", fs("iso9660", "u", "", "/dev/sdb1"), false},
		{"no UUID", fs("exfat", "", "USB", "/dev/sdb1"), false},
		{"system label", fs("ext4", "u", "COS_PERSISTENT", "/dev/sdb1"), false},
		{"root device", fs("ext4", "u", "", "/dev/sda5"), false},
	}
	for _, c := range cases {
		if got := usableForBackup(c.b); got != c.want {
			t.Errorf("%s: usableForBackup = %v, want %v", c.name, got, c.want)
		}
	}
}

// sysTree is a fake /sys in a temporary directory, with the links of
// /dev and the device of each file.
type sysTree struct {
	t     *testing.T
	root  string
	links map[string]string // /dev/mapper/x -> /dev/dm-0
	devs  map[string]string // a file -> the major:minor of its st_dev
}

func newSysTree(t *testing.T) sysTree {
	return sysTree{t, t.TempDir(),
		map[string]string{"/dev/mapper/vg-lv": "/dev/dm-1"},
		map[string]string{"/run/initramfs/cos-state/cOS/active.img": "8:3"}}
}

// dev makes /sys/dev/block/majmin a link to the device name, and
// /sys/class/block/name/dev.
func (s sysTree) dev(majmin, name string) {
	s.t.Helper()
	s.file("class/block/"+name+"/dev", majmin+"\n")
	if err := os.MkdirAll(filepath.Join(s.root, "dev/block"), 0o755); err != nil {
		s.t.Fatal(err)
	}
	if err := os.Symlink("../../devices/virtual/block/"+name, filepath.Join(s.root, "dev/block", majmin)); err != nil {
		s.t.Fatal(err)
	}
}

func (s sysTree) file(path, content string) {
	s.t.Helper()
	p := filepath.Join(s.root, path)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		s.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		s.t.Fatal(err)
	}
}

// stack puts upper on lower: upper's slaves and lower's holders.
// btrfs lists devs as the devices of btrfs filesystem fsid.
func (s sysTree) btrfs(fsid string, devs ...string) {
	for _, d := range devs {
		s.file("fs/btrfs/"+fsid+"/devices/"+d, "")
	}
}

func (s sysTree) stack(lower, upper string) {
	s.file("class/block/"+upper+"/slaves/"+lower, "")
	s.file("class/block/"+lower+"/holders/"+upper, "")
}

func (s sysTree) view(mountinfo string, err error) hostView {
	return hostView{
		sys:       s.root,
		mountinfo: func() ([]byte, error) { return []byte(mountinfo), err },
		devOf: func(p string) (string, error) {
			if mm, ok := s.devs[p]; ok {
				return mm, nil
			}
			return "", os.ErrNotExist
		},
		evalLink: func(p string) (string, error) {
			if r, ok := s.links[p]; ok {
				return r, nil
			}
			return "", os.ErrNotExist
		},
	}
}

const kairosMounts = `22 1 7:0 / / ro,relatime shared:1 - ext2 /dev/loop0 ro
30 22 8:2 / /oem rw,relatime shared:9 - ext4 /dev/disk/by-label/COS_OEM rw
31 22 253:1 /usr/local /usr/local rw,relatime shared:10 - ext4 /dev/mapper/vg-lv rw
32 22 8:33 / /run/media/anna/USB rw - exfat /dev/sdc1 rw
33 22 0:25 / /tmp rw - tmpfs tmpfs rw
`

func TestRootDevicesFollowLoopsAndStacks(t *testing.T) {
	s := newSysTree(t)
	// / is loop0, its backing file on sda3
	s.dev("7:0", "loop0")
	s.file("class/block/loop0/loop/backing_file", "/run/initramfs/cos-state/cOS/active.img\n")
	s.dev("8:3", "sda3")
	s.dev("8:2", "sda2")
	// /usr/local is an LV (dm-1) in a LUKS container (dm-0) on sdb2
	s.dev("253:1", "dm-1")
	s.dev("253:0", "dm-0")
	s.dev("8:18", "sdb2")
	s.stack("dm-0", "dm-1")
	s.stack("sdb2", "dm-0")
	s.dev("8:33", "sdc1")

	got, err := s.view(kairosMounts, nil).rootDevices()
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"/dev/loop0", "/dev/sda3", "/dev/sda2", "/dev/disk/by-label/COS_OEM", "/dev/dm-1", "/dev/dm-0", "/dev/sdb2", "/dev/mapper/vg-lv"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}

	// drive X holds only the loop's backing file, with no Kairos label
	x := udPath + "drives/X"
	dx := drivesFrom(objs{
		dbus.ObjectPath(x):            driveObj("X", 64e9, true),
		udPath + "block_devices/sda":  blockObj(x, "/dev/sda", "", "", "", 64e9, 0),
		udPath + "block_devices/sda3": blockObj(x, "/dev/sda3", "", "ext4", "u", 60e9, 3),
	})[0]
	if !systemDrive(dx, got) || driveReason(dx, got) != reasonIsotope {
		t.Error("the drive of the loop root's backing file is not Isotope's system disk")
	}
	// the drive under the LV and the LUKS container
	y := udPath + "drives/Y"
	dy := drivesFrom(objs{
		dbus.ObjectPath(y):            driveObj("Y", 64e9, true),
		udPath + "block_devices/sdb":  blockObj(y, "/dev/sdb", "", "", "", 64e9, 0),
		udPath + "block_devices/sdb2": blockObj(y, "/dev/sdb2", "", "crypto_LUKS", "u", 60e9, 2),
	})[0]
	if !systemDrive(dy, got) {
		t.Error("the drive under the LV of /usr/local is not a system drive")
	}
	// the USB stick mounted under /run/media is not
	if isSystemDevice(Block{Device: "/dev/sdc1"}, got) {
		t.Error("a USB stick is a system device")
	}
}

func TestRootDevicesFailClosed(t *testing.T) {
	s := newSysTree(t)
	plain := Drive{Blocks: []Block{{Device: "/dev/sdc"}, {Device: "/dev/sdc1", FSType: "exfat", UUID: "u", IsFilesystem: true, IsPartition: true}}}
	check := func(name string, h hostView) {
		t.Helper()
		roots, err := h.rootDevices()
		if err == nil {
			t.Errorf("%s: no error", name)
		}
		if !systemDrive(plain, roots) || driveReason(plain, roots) != reasonIsotope {
			t.Errorf("%s: a plain USB stick is not a system drive (roots %q)", name, roots)
		}
		old := rootDevices
		rootDevices = h.rootDevices
		defer func() { rootDevices = old }()
		if usableForBackup(plain.Blocks[1]) {
			t.Errorf("%s: usable for backup", name)
		}
	}
	check("mountinfo unreadable", s.view("", os.ErrPermission))
	check("no / in mountinfo", s.view("33 22 0:25 / /tmp rw - tmpfs tmpfs rw\n", nil))
	// / on a loop device whose backing file cannot be read
	s.dev("7:0", "loop0")
	check("loop without a backing file", s.view("22 1 7:0 / / ro - ext2 /dev/loop0 ro\n", nil))
	// / on a device sysfs does not know
	check("unknown root device", s.view("22 1 8:9 / / ro - ext4 /dev/sdz9 ro\n", nil))
	// / on btrfs from a source that is no known device
	check("btrfs root of an unknown source", s.view("22 1 0:31 / / rw - btrfs /dev/mapper/gone rw\n", nil))
	// / on ZFS or overlay: no /dev source at all
	check("zfs root", s.view("22 1 0:32 / / rw - zfs rpool/ROOT rw\n", nil))
	check("overlay root", s.view("22 1 0:33 / / rw - overlay overlay rw\n", nil))
	// / on a loop whose backing file is on NFS
	s.file("class/block/loop0/loop/backing_file", "/mnt/nfs/root.img\n")
	s.devs["/mnt/nfs/root.img"] = "0:60"
	check("loop on NFS", s.view("22 1 7:0 / / ro - ext4 /dev/loop0 ro\n40 22 0:60 / /mnt/nfs rw - nfs4 server:/export rw\n", nil))
}

func TestFormatRefusesAMountedStackedDevice(t *testing.T) {
	s := newSysTree(t)
	s.dev("8:16", "sdb")
	s.dev("8:17", "sdb1")
	s.dev("253:0", "dm-0")
	s.dev("253:1", "dm-1")
	// an LV (dm-1) in a LUKS container (dm-0) on sdb1, mounted
	s.stack("sdb1", "dm-0")
	s.stack("dm-0", "dm-1")
	stick := Drive{ID: fakeUSB, Removable: true, Blocks: []Block{
		{ID: "b/sdb", Device: "/dev/sdb"},
		{ID: "b/sdb1", Device: "/dev/sdb1", FSType: "crypto_LUKS", IsCrypto: true, IsPartition: true, part: 1},
	}}
	ds := []Drive{stick}
	mounted := "22 1 0:20 / / rw - overlay overlay rw\n40 22 253:1 / /run/media/anna/my\\040lv rw - ext4 /dev/mapper/vg-lv rw\n"
	_, err := formatCheck(ds, fakeUSB, "exfat", nil, s.view(mounted, nil).mountedAbove)
	if err == nil || !strings.Contains(err.Error(), "/dev/dm-1 at /run/media/anna/my lv") {
		t.Errorf("Format with a mounted LV on the disk: %v", err)
	}
	whole, err := formatCheck(ds, fakeUSB, "exfat", nil, s.view("22 1 0:20 / / rw - overlay overlay rw\n", nil).mountedAbove)
	if err != nil || whole.Device != "/dev/sdb" {
		t.Errorf("Format with nothing mounted: %v, %+v", err, whole)
	}
	if _, err := formatCheck(ds, fakeUSB, "exfat", nil, s.view("", os.ErrPermission).mountedAbove); err == nil {
		t.Error("Format went on without the mount table")
	}
}

func TestSystemReason(t *testing.T) {
	internal := Drive{Blocks: []Block{{Device: "/dev/sdd", HintSystem: true}, {Device: "/dev/sdd1", FSType: "ext4", HintSystem: true}}}
	if r := driveReason(internal, nil); r != reasonInternal {
		t.Errorf("a second internal disk: %q, want %q", r, reasonInternal)
	}
	if _, err := formatCheck([]Drive{{ID: "d", Blocks: internal.Blocks}}, "d", "exfat", nil, nil); err == nil {
		t.Error("formatCheck accepts an internal disk")
	}
	kairos := Drive{Blocks: []Block{{Device: "/dev/sda", HintSystem: true}, {Device: "/dev/sda2", Label: "COS_STATE", HintSystem: true}}}
	if r := driveReason(kairos, nil); r != reasonIsotope {
		t.Errorf("the Kairos disk: %q, want %q", r, reasonIsotope)
	}
	if r := driveReason(internal, []string{"/dev/sdd1"}); r != reasonIsotope {
		t.Errorf("an internal disk behind /: %q, want %q", r, reasonIsotope)
	}
	if r := systemReason(Block{Device: "/dev/sdc1", Label: "USB"}, nil); r != "" {
		t.Errorf("a USB stick: %q", r)
	}
}

func TestFillUsage(t *testing.T) {
	old := statfs
	statfs = func(p string) (uint64, error) {
		if p == "/run/media/anna/USB" {
			return 5e9, nil
		}
		t.Errorf("statfs(%q)", p)
		return 0, nil
	}
	t.Cleanup(func() { statfs = old })
	ds := []Drive{{Blocks: []Block{{Device: "/dev/sdb"}, {Device: "/dev/sdb1", MountPoints: []string{"/run/media/anna/USB", "/mnt"}}}}}
	fillUsage(ds)
	if ds[0].Blocks[1].Used != 5e9 || ds[0].Blocks[0].Used != 0 {
		t.Fatalf("used = %+v", ds[0].Blocks)
	}
}

func TestFakeDisksRefusesLikeTheClient(t *testing.T) {
	old := rootDevices
	rootDevices = func() ([]string, error) { return nil, nil }
	t.Cleanup(func() { rootDevices = old })
	f := newFakeDisks()
	if err := f.Format(f.drives[1].ID, "exfat", "X"); err == nil {
		t.Error("the fake formats the system disk")
	}
	if err := f.Format(f.drives[0].ID, "exfat", "X"); err == nil {
		t.Error("the fake formats a mounted disk")
	}
	if err := f.Unmount(f.drives[0].Blocks[1].ID); err != nil {
		t.Fatal(err)
	}
	if err := f.Format(f.drives[0].ID, "ntfs", "X"); err == nil {
		t.Error("the fake formats as ntfs")
	}
	if err := f.Format(f.drives[0].ID, "exfat", "BACKUP"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Mount(f.drives[1].Blocks[2].ID); err == nil {
		t.Error("the fake mounts a partition of the system disk")
	}
	if ds, _ := f.Drives(); len(ds[0].Blocks) != 2 || ds[0].Blocks[1].Label != "BACKUP" || !usableForBackup(ds[0].Blocks[1]) {
		t.Errorf("after Format: %+v", ds[0].Blocks)
	}
	want := []string{
		"Format " + f.drives[1].ID + " exfat X",
		"Format " + f.drives[0].ID + " exfat X",
		"Unmount " + f.drives[0].Blocks[1].ID,
		"Format " + f.drives[0].ID + " ntfs X",
		"Format " + f.drives[0].ID + " exfat BACKUP",
		"Mount " + f.drives[1].Blocks[2].ID,
	}
	if got := f.called(); !reflect.DeepEqual(got, want) {
		t.Fatalf("calls %q, want %q", got, want)
	}
}

func TestFakeDisksFormatStopsOnAStackedMountAndADrivesError(t *testing.T) {
	f := newFakeDisks()
	if err := f.Unmount(f.drives[0].Blocks[1].ID); err != nil {
		t.Fatal(err)
	}
	f.stacked = map[string]string{"/dev/sdb1": "/dev/dm-1 at /mnt"}
	if err := f.Format(fakeUSB, "exfat", "X"); err == nil || !strings.Contains(err.Error(), "/dev/dm-1 at /mnt") {
		t.Errorf("Format with a mounted LV: %v", err)
	}
	f.stacked = nil
	f.errs = map[string]error{"Drives": errors.New("no udisks2")}
	if err := f.Format(fakeUSB, "exfat", "X"); err == nil {
		t.Error("Format went on after Drives failed")
	}
}

func TestAnEFIStickIsFormatable(t *testing.T) {
	stick := udPath + "drives/Installer"
	o := objs{
		dbus.ObjectPath(stick):        driveObj("Installer", 8e9, true),
		udPath + "block_devices/sdb":  blockObj(stick, "/dev/sdb", "", "", "", 8e9, 0),
		udPath + "block_devices/sdb1": blockObj(stick, "/dev/sdb1", "EFI", "vfat", "AB12-CD34", 5e8, 1),
		udPath + "block_devices/sdb2": blockObj(stick, "/dev/sdb2", "", "", "", 1e6, 2),
		udPath + "block_devices/sdb3": blockObj(stick, "/dev/sdb3", "Ubuntu", "iso9660", "u", 7e9, 3),
	}
	o[udPath+"block_devices/sdb1"][ifPart]["Name"] = v("EFI")
	o[udPath+"block_devices/sdb2"][ifPart]["Name"] = v("bios")
	ds := drivesFrom(o)
	if r := driveReason(ds[0], []string{"/dev/sda2"}); r != "" {
		t.Fatalf("an installer stick with EFI and bios partitions: reason %q", r)
	}
	whole, err := formatCheck(ds, stick, "exfat", []string{"/dev/sda2"}, func([]string) (string, error) { return "", nil })
	if err != nil || whole.Device != "/dev/sdb" {
		t.Fatalf("formatCheck: %v, %+v", err, whole)
	}
}

func TestRootDevicesOnBtrfs(t *testing.T) {
	s := newSysTree(t)
	s.dev("253:0", "dm-0")
	s.dev("8:2", "sdx2")
	s.stack("sdx2", "dm-0")
	s.links["/dev/mapper/root"] = "/dev/dm-0"
	s.btrfs("1111", "dm-0")
	s.file("fs/btrfs/features/raid1", "")
	x := Drive{Blocks: []Block{{Device: "/dev/sdx"}, {Device: "/dev/sdx2", FSType: "crypto_LUKS", IsCrypto: true, IsPartition: true}}}

	// btrfs on LUKS: mountinfo shows 0:31, the source is the cleartext
	roots, err := s.view("22 1 0:31 / / rw - btrfs /dev/mapper/root rw\n", nil).rootDevices()
	if err != nil {
		t.Fatal(err)
	}
	if !systemDrive(x, roots) {
		t.Errorf("btrfs root on dm-0 over sdx2: X is not system (roots %q)", roots)
	}

	// / on a loop whose backing file is on a btrfs mount of sdx3
	s.dev("8:3", "sdx3")
	s.btrfs("3333", "sdx3")
	s.dev("7:0", "loop0")
	s.file("class/block/loop0/loop/backing_file", "/run/initramfs/cos-state/cOS/active.img\n")
	s.devs["/run/initramfs/cos-state/cOS/active.img"] = "0:44"
	mi := "22 1 7:0 / / ro - ext2 /dev/loop0 ro\n" +
		"40 22 0:44 / /run/initramfs/cos-state rw - btrfs /dev/sdx3 rw\n" +
		"41 22 0:45 / /run rw - tmpfs tmpfs rw\n"
	roots, err = s.view(mi, nil).rootDevices()
	if err != nil {
		t.Fatal(err)
	}
	x3 := Drive{Blocks: []Block{{Device: "/dev/sdx"}, {Device: "/dev/sdx3", FSType: "btrfs", IsFilesystem: true, IsPartition: true}}}
	if !systemDrive(x3, roots) {
		t.Errorf("loop root on a btrfs file of sdx3: X is not system (roots %q)", roots)
	}

	// a loop file in RAM is on no disk, and is no error
	s.devs["/run/initramfs/cos-state/cOS/active.img"] = "0:45"
	mi = "22 1 7:0 / / ro - ext2 /dev/loop0 ro\n41 22 0:45 / /run rw - tmpfs tmpfs rw\n"
	if roots, err = s.view(mi, nil).rootDevices(); err != nil || !reflect.DeepEqual(roots, []string{"/dev/loop0"}) {
		t.Errorf("loop root in RAM: %q, %v", roots, err)
	}
}

func TestFormatRefusesAStackedBtrfsAndAMissingSysfs(t *testing.T) {
	s := newSysTree(t)
	s.dev("8:16", "sdb")
	s.dev("8:17", "sdb1")
	s.dev("253:2", "dm-2")
	s.dev("253:3", "dm-3")
	s.stack("sdb1", "dm-2")
	s.stack("dm-2", "dm-3")
	s.links["/dev/mapper/usb-lv"] = "/dev/dm-3"
	s.btrfs("4444", "dm-3")
	stick := []Drive{{ID: fakeUSB, Removable: true, Blocks: []Block{
		{ID: "b/sdb", Device: "/dev/sdb"},
		{ID: "b/sdb1", Device: "/dev/sdb1", FSType: "LVM2_member", IsPartition: true, part: 1},
	}}}
	mi := "22 1 0:20 / / rw - overlay overlay rw\n50 22 0:50 / /run/media/anna/data rw - btrfs /dev/mapper/usb-lv rw\n"
	_, err := formatCheck(stick, fakeUSB, "exfat", nil, s.view(mi, nil).mountedAbove)
	if err == nil || !strings.Contains(err.Error(), "/dev/dm-3 at /run/media/anna/data") {
		t.Errorf("Format with btrfs mounted on an LV of the disk: %v", err)
	}

	// a device missing from sysfs: cannot tell, so no Format
	gone := []Drive{{ID: fakeUSB, Removable: true, Blocks: []Block{{ID: "b/sdq", Device: "/dev/sdq"}}}}
	if _, err := formatCheck(gone, fakeUSB, "exfat", nil, s.view("22 1 0:20 / / rw - overlay overlay rw\n", nil).mountedAbove); err == nil {
		t.Error("Format went on for a disk sysfs does not know")
	}
}

func TestBtrfsOnTwoDisks(t *testing.T) {
	s := newSysTree(t)
	s.dev("8:2", "sda2")
	s.dev("8:18", "sdb2")
	s.btrfs("5555", "sda2", "sdb2")
	mi := "22 1 0:31 / / rw - btrfs /dev/sda2 rw\n"
	roots, err := s.view(mi, nil).rootDevices()
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{"/dev/sda2", "/dev/sdb2"} {
		if !isSystemDevice(Block{Device: d}, roots) {
			t.Errorf("btrfs RAID1 root: %s is not system (roots %q)", d, roots)
		}
	}
	// a btrfs that /sys/fs/btrfs does not list: fail closed
	s2 := newSysTree(t)
	s2.dev("8:2", "sda2")
	if roots, err := s2.view(mi, nil).rootDevices(); err == nil || !isSystemDevice(Block{Device: "/dev/sdz1"}, roots) {
		t.Errorf("btrfs without /sys/fs/btrfs: %q, %v", roots, err)
	}

	// a mounted btrfs over a partition of stick 1 and stick 2
	s.dev("8:32", "sdc")
	s.dev("8:33", "sdc1")
	s.dev("8:48", "sdd")
	s.dev("8:49", "sdd1")
	s.btrfs("6666", "sdc1", "sdd1")
	mi = "22 1 0:20 / / rw - tmpfs tmpfs rw\n50 22 0:50 / /run/media/anna/pool rw - btrfs /dev/sdc1 rw\n"
	sticks := []Drive{
		{ID: "one", Removable: true, Blocks: []Block{{Device: "/dev/sdc"}, {Device: "/dev/sdc1", FSType: "btrfs", IsFilesystem: true, IsPartition: true, part: 1}}},
		{ID: "two", Removable: true, Blocks: []Block{{Device: "/dev/sdd"}, {Device: "/dev/sdd1", FSType: "btrfs", IsFilesystem: true, IsPartition: true, part: 1}}},
	}
	for _, id := range []string{"one", "two"} {
		if _, err := formatCheck(sticks, id, "exfat", nil, s.view(mi, nil).mountedAbove); err == nil || !strings.Contains(err.Error(), "/run/media/anna/pool") {
			t.Errorf("Format of stick %s under a mounted two-disk btrfs: %v", id, err)
		}
	}
}

func TestLoopFileOnAShadowedMount(t *testing.T) {
	s := newSysTree(t)
	s.dev("8:3", "sdx3")
	s.btrfs("3333", "sdx3")
	s.dev("7:0", "loop0")
	s.file("class/block/loop0/loop/backing_file", "/run/x/root.img\n")
	s.devs["/run/x/root.img"] = "0:44"
	// the btrfs is mounted at /run/x, then a tmpfs over it
	mi := "22 1 7:0 / / ro - ext2 /dev/loop0 ro\n" +
		"40 22 0:44 / /run/x rw - btrfs /dev/sdx3 rw\n" +
		"41 40 0:45 / /run/x rw - tmpfs tmpfs rw\n"
	roots, err := s.view(mi, nil).rootDevices()
	if err != nil {
		t.Fatal(err)
	}
	if !isSystemDevice(Block{Device: "/dev/sdx3"}, roots) {
		t.Errorf("the backing file's disk under a shadowing tmpfs is not system (roots %q)", roots)
	}
	// st_dev of no mount: fail closed
	s.devs["/run/x/root.img"] = "0:99"
	if roots, err := s.view(mi, nil).rootDevices(); err == nil || !isSystemDevice(Block{Device: "/dev/sdz1"}, roots) {
		t.Errorf("a backing file on no mount: %q, %v", roots, err)
	}
}

func TestFormatRefusesAMountedLoopOnTheDisk(t *testing.T) {
	stick := []Drive{{ID: fakeUSB, Removable: true, Blocks: []Block{
		{Device: "/dev/sdb"}, {Device: "/dev/sdb1", FSType: "exfat", IsFilesystem: true, IsPartition: true, part: 1},
	}}}
	base := "22 1 0:20 / / rw - tmpfs tmpfs rw\n"

	// losetup /dev/sdb1, then mount /dev/loop1
	s := newSysTree(t)
	s.dev("8:16", "sdb")
	s.dev("8:17", "sdb1")
	s.dev("7:1", "loop1")
	s.dev("7:2", "loop2") // not bound
	s.file("class/block/loop1/loop/backing_file", "/dev/sdb1\n")
	_, err := formatCheck(stick, fakeUSB, "exfat", nil, s.view(base+"60 22 7:1 / /mnt/img rw - ext4 /dev/loop1 rw\n", nil).mountedAbove)
	if err == nil || !strings.Contains(err.Error(), "/dev/loop1 at /mnt/img") {
		t.Errorf("a mounted loop on /dev/sdb1: %v", err)
	}
	if _, err := formatCheck(stick, fakeUSB, "exfat", nil, s.view(base, nil).mountedAbove); err != nil {
		t.Errorf("an unmounted loop on /dev/sdb1: %v", err)
	}

	// a loop on an image file that lies on sdb1 (when sdb1 is unmounted
	// lazily the loop keeps it open), with a LUKS volume on it, mounted
	s2 := newSysTree(t)
	s2.dev("8:16", "sdb")
	s2.dev("8:17", "sdb1")
	s2.dev("7:3", "loop3")
	s2.dev("253:5", "dm-5")
	s2.stack("loop3", "dm-5")
	s2.file("class/block/loop3/loop/backing_file", "/run/media/anna/USB/disk.img\n")
	s2.devs["/run/media/anna/USB/disk.img"] = "8:17"
	s2.links["/dev/mapper/img"] = "/dev/dm-5"
	_, err = formatCheck(stick, fakeUSB, "exfat", nil, s2.view(base+"61 22 253:5 / /mnt/secret rw - ext4 /dev/mapper/img rw\n", nil).mountedAbove)
	if err == nil || !strings.Contains(err.Error(), "/dev/dm-5 at /mnt/secret") {
		t.Errorf("a mounted volume on a loop file of sdb1: %v", err)
	}
}
