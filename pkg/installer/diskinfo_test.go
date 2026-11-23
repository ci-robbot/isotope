package installer

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// lsblk -J -b -o NAME,SIZE,FSTYPE,LABEL,PARTLABEL of a laptop disk with
// Windows, util-linux 2.39 (sizes as numbers).
const lsblkWindows = `{
   "blockdevices": [
      {"name":"nvme0n1", "size":1000204886016, "fstype":null, "label":null, "partlabel":null,
         "children": [
            {"name":"nvme0n1p1", "size":536870912, "fstype":"vfat", "label":"SYSTEM", "partlabel":"EFI system partition"},
            {"name":"nvme0n1p2", "size":16777216, "fstype":null, "label":null, "partlabel":"Microsoft reserved partition"},
            {"name":"nvme0n1p3", "size":620000000000, "fstype":"ntfs", "label":"Windows", "partlabel":"Basic data partition"}
         ]
      }
   ]
}`

// An installed Kairos disk, older util-linux (sizes as strings).
const lsblkKairos = `{"blockdevices":[{"name":"vda","size":"64424509440","fstype":null,"label":null,"partlabel":null,
 "children":[
  {"name":"vda1","size":"67108864","fstype":"vfat","label":"COS_GRUB","partlabel":"efi"},
  {"name":"vda2","size":"67108864","fstype":"ext4","label":"COS_OEM","partlabel":"oem"},
  {"name":"vda3","size":"8589934592","fstype":"ext4","label":"COS_RECOVERY","partlabel":"recovery"},
  {"name":"vda4","size":"19327352832","fstype":"ext4","label":"COS_STATE","partlabel":"state"},
  {"name":"vda5","size":"36371054592","fstype":"crypto_LUKS","label":"COS_PERSISTENT_LUKS","partlabel":"persistent"}
 ]}]}`

const lsblkLinux = `{"blockdevices":[{"name":"sda","size":2000398934016,"fstype":null,"label":null,"partlabel":null,
 "children":[
  {"name":"sda1","size":1073741824,"fstype":"vfat","label":null,"partlabel":"EFI"},
  {"name":"sda2","size":500000000000,"fstype":"ext4","label":"root","partlabel":null}
 ]}]}`

const lsblkEmpty = `{"blockdevices":[{"name":"sdb","size":64023257088,"fstype":null,"label":null,"partlabel":null}]}`

func mustParse(t *testing.T, s string) DiskInfo {
	t.Helper()
	d, err := ParseLsblk([]byte(s))
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestParseLsblk(t *testing.T) {
	d := mustParse(t, lsblkWindows)
	if d.SizeBytes != 1000204886016 || len(d.Partitions) != 3 {
		t.Fatalf("%+v", d)
	}
	if p := d.Partitions[2]; p.FSType != "ntfs" || p.Label != "Windows" || p.SizeBytes != 620000000000 {
		t.Fatalf("%+v", p)
	}
	k := mustParse(t, lsblkKairos)
	if k.SizeBytes != 64424509440 || len(k.Partitions) != 5 || k.Partitions[4].FSType != "crypto_LUKS" {
		t.Fatalf("string sizes: %+v", k)
	}
	if _, err := ParseLsblk([]byte(`{"blockdevices":[]}`)); err == nil {
		t.Fatal("no device parsed")
	}
	if _, err := ParseLsblk([]byte(`not json`)); err == nil {
		t.Fatal("garbage parsed")
	}
}

func TestErasureWarningNamesWhatItKnows(t *testing.T) {
	for name, c := range map[string]struct{ in, want string }{
		"windows": {lsblkWindows, "Windows and its files"},
		"kairos":  {lsblkKairos, "The Kairos system"},
		"linux":   {lsblkLinux, "The Linux system"},
		"empty":   {lsblkEmpty, "no partitions"},
	} {
		if got := mustParse(t, c.in).ErasureWarning(); !strings.Contains(got, c.want) {
			t.Errorf("%s: %q, want %q in it", name, got, c.want)
		}
	}
	if got := (DiskInfo{SizeBytes: 100}).ErasureWarning(); got != "Everything on this disk will be erased." {
		t.Errorf("unread disk: %q", got)
	}
	unknown := DiskInfo{SizeBytes: 100, Read: true, Partitions: []Partition{{Name: "x1", FSType: "zfs_member"}}}
	if got := unknown.ErasureWarning(); got != "Everything on this disk will be erased." {
		t.Errorf("unknown: %q", got)
	}
}

func TestSegments(t *testing.T) {
	segs := mustParse(t, lsblkWindows).Segments()
	var kinds []string
	for _, s := range segs {
		kinds = append(kinds, s.Kind)
	}
	if got := strings.Join(kinds, ","); got != "EFI,Microsoft reserved partition,Windows (NTFS),Free" {
		t.Fatalf("kinds %s", got)
	}
	if last := segs[len(segs)-1]; !last.Free || last.SizeBytes != 1000204886016-536870912-16777216-620000000000 {
		t.Fatalf("free %+v", last)
	}
	// A full disk has no free segment for its alignment slack.
	full := DiskInfo{SizeBytes: 1000, Read: true, Partitions: []Partition{{SizeBytes: 995}}}
	if s := full.Segments(); len(s) != 1 {
		t.Fatalf("%+v", s)
	}
	if s := (DiskInfo{SizeBytes: 1000}).Segments(); s != nil {
		t.Fatalf("an unread disk has segments: %+v", s)
	}
	if k := mustParse(t, lsblkKairos).Partitions[3].Kind(); k != "Kairos (COS_STATE)" {
		t.Fatal(k)
	}
}

func TestHumanSize(t *testing.T) {
	for b, want := range map[int64]string{536870912: "537 MB", 1000204886016: "1 TB", 64023257088: "64 GB", 1500000000000: "1.5 TB", 512: "512 B"} {
		if got := HumanSize(b); got != want {
			t.Errorf("HumanSize(%d) = %q, want %q", b, got, want)
		}
	}
}

// sysfsFixture makes a /sys/block whose entries link into a device tree,
// as the kernel's do.
func sysfsFixture(t *testing.T, disks map[string]string) string {
	t.Helper()
	root := t.TempDir()
	block := filepath.Join(root, "block")
	if err := os.MkdirAll(block, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, dev := range disks {
		target := filepath.Join(root, "devices", dev, "block", name)
		if err := os.MkdirAll(filepath.Join(target, "device"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, filepath.Join(block, name)); err != nil {
			t.Fatal(err)
		}
	}
	return block
}

func TestDiskBusAndModel(t *testing.T) {
	block := sysfsFixture(t, map[string]string{
		"nvme0n1": "pci0000:00/0000:00:1d.0/0000:3d:00.0/nvme/nvme0",
		"sda":     "pci0000:00/0000:00:17.0/ata1/host0/target0:0:0/0:0:0:0",
		"sdb":     "pci0000:00/0000:00:14.0/usb2/2-1/2-1:1.0/host3/target3:0:0/3:0:0:0",
		"vda":     "pci0000:00/0000:00:04.0/virtio1",
		"mmcblk0": "platform/mmc_host/mmc0/mmc0:0001",
	})
	want := map[string]string{"nvme0n1": "NVMe", "sda": "SATA", "sdb": "USB", "vda": "Virtio", "mmcblk0": "SD card"}
	for name, bus := range want {
		if got := DiskBus(block, name); got != bus {
			t.Errorf("DiskBus(%s) = %q, want %q", name, got, bus)
		}
	}
	if err := os.WriteFile(filepath.Join(block, "sda", "device", "model"), []byte("WDC WD20EZAZ-00G  \n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if m := DiskModel(block, "sda"); m != "WDC WD20EZAZ-00G" {
		t.Errorf("model %q", m)
	}
	if m := DiskModel(block, "vda"); m != "" {
		t.Errorf("virtio model %q", m)
	}
}
