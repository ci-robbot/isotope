package installer

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/kairos-community/isotope/pkg/i18n"
)

// What is on a disk now, for the graphical installer's disk page: the model
// and bus from sysfs, the partitions from lsblk, and a warning that names
// what installing would erase.

// Partition is one partition of a disk, as lsblk reports it.
type Partition struct {
	Name      string
	SizeBytes int64
	FSType    string
	Label     string
	PartLabel string
}

// DiskInfo is what is known about a disk beyond its path and size.
type DiskInfo struct {
	// Model is the device's model string ("Samsung SSD 980 PRO 1TB"), or
	// "" when sysfs has none (virtual disks often have none).
	Model string
	// Bus is how the disk is attached: "NVMe", "SATA", "USB", "SD card",
	// "Virtio", "SCSI" or "".
	Bus        string
	SizeBytes  int64
	Partitions []Partition
	// Read says the partitions are known: lsblk answered. When it did
	// not, nothing is claimed about what is on the disk.
	Read bool
}

// lsblkSize is lsblk's SIZE, which -b prints as a number, or as a string on
// older util-linux.
type lsblkSize int64

func (s *lsblkSize) UnmarshalJSON(b []byte) error {
	str := strings.Trim(string(b), `"`)
	if str == "null" || str == "" {
		*s = 0
		return nil
	}
	n, err := strconv.ParseInt(str, 10, 64)
	if err != nil {
		return err
	}
	*s = lsblkSize(n)
	return nil
}

type lsblkDev struct {
	Name      string     `json:"name"`
	Size      lsblkSize  `json:"size"`
	FSType    *string    `json:"fstype"`
	Label     *string    `json:"label"`
	PartLabel *string    `json:"partlabel"`
	Children  []lsblkDev `json:"children"`
}

func str(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// LsblkArgs are the arguments ParseLsblk expects the output of, followed by
// the disk's path.
var LsblkArgs = []string{"-J", "-b", "-o", "NAME,SIZE,FSTYPE,LABEL,PARTLABEL"}

// ParseLsblk reads `lsblk -J -b -o NAME,SIZE,FSTYPE,LABEL,PARTLABEL <disk>`
// for one disk: its size and its partitions, in order.
func ParseLsblk(out []byte) (DiskInfo, error) {
	var doc struct {
		Blockdevices []lsblkDev `json:"blockdevices"`
	}
	if err := json.Unmarshal(out, &doc); err != nil {
		return DiskInfo{}, fmt.Errorf("lsblk: %w", err)
	}
	if len(doc.Blockdevices) == 0 {
		return DiskInfo{}, fmt.Errorf("lsblk: no device")
	}
	d := doc.Blockdevices[0]
	info := DiskInfo{SizeBytes: int64(d.Size), Read: true}
	for _, c := range d.Children {
		info.Partitions = append(info.Partitions, Partition{
			Name: c.Name, SizeBytes: int64(c.Size),
			FSType: str(c.FSType), Label: str(c.Label), PartLabel: str(c.PartLabel),
		})
	}
	return info, nil
}

// DiskBus says how the disk name (sda, nvme0n1) under sysBlock is attached,
// from where its sysfs entry points.
func DiskBus(sysBlock, name string) string {
	target, err := filepath.EvalSymlinks(filepath.Join(sysBlock, name))
	if err != nil {
		target = filepath.Join(sysBlock, name)
	}
	t := filepath.ToSlash(target)
	switch {
	case strings.HasPrefix(name, "nvme") || strings.Contains(t, "/nvme/"):
		return "NVMe"
	case strings.Contains(t, "/usb"):
		return "USB"
	case strings.HasPrefix(name, "mmcblk") || strings.Contains(t, "/mmc_host/"):
		return i18n.Mark("SD card")
	case strings.HasPrefix(name, "vd") || strings.Contains(t, "/virtio"):
		return "Virtio"
	case strings.Contains(t, "/ata"):
		return "SATA"
	case strings.HasPrefix(name, "sd"):
		return "SCSI"
	}
	return ""
}

// DiskModel is the model string sysfs has for name, or "".
func DiskModel(sysBlock, name string) string {
	for _, f := range []string{"device/model", "device/name"} {
		if b, err := os.ReadFile(filepath.Join(sysBlock, name, f)); err == nil {
			if m := strings.Join(strings.Fields(string(b)), " "); m != "" {
				return m
			}
		}
	}
	return ""
}

// DescribeDisk is DiskInfo for the disk at path (/dev/sda): sysfs from
// sysBlock, partitions from lsblk. A failing lsblk leaves the partitions
// unknown, not the disk.
func DescribeDisk(sysBlock, path string) DiskInfo {
	name := filepath.Base(path)
	var info DiskInfo
	if out, err := exec.Command("lsblk", append(LsblkArgs, path)...).Output(); err == nil {
		if i, err := ParseLsblk(out); err == nil {
			info = i
		}
	}
	info.Model = DiskModel(sysBlock, name)
	info.Bus = DiskBus(sysBlock, name)
	return info
}

// Kind is what a partition holds, in words for the disk bar: "EFI",
// "Windows (NTFS)", "Linux (ext4)", "Kairos (COS_STATE)", or its file
// system or label.
func (p Partition) Kind() string {
	label := p.Label
	if label == "" {
		label = p.PartLabel
	}
	lower := strings.ToLower(label + " " + p.PartLabel)
	switch {
	case strings.HasPrefix(p.Label, "COS_") || strings.HasPrefix(p.PartLabel, "COS_") ||
		p.FSType == "crypto_LUKS" && strings.HasPrefix(label, "COS_"):
		return "Kairos (" + label + ")"
	case p.FSType == "vfat" && (strings.Contains(lower, "efi") || strings.Contains(lower, "esp")):
		return "EFI"
	case p.FSType == "ntfs" && (strings.Contains(lower, "windows") || strings.Contains(lower, "basic data")):
		return "Windows (NTFS)"
	case p.FSType == "ntfs":
		return "NTFS"
	case p.FSType == "ext4" || p.FSType == "btrfs" || p.FSType == "xfs" || p.FSType == "f2fs":
		return "Linux (" + p.FSType + ")"
	case p.FSType == "swap":
		return i18n.Mark("Swap")
	case p.FSType == "crypto_LUKS":
		return i18n.Mark("Encrypted")
	case p.FSType != "":
		if label != "" {
			return label + " (" + p.FSType + ")"
		}
		return p.FSType
	case label != "":
		return label
	}
	return i18n.Mark("Unknown")
}

// ErasureWarning names what installing on the disk would erase, as far as
// its partitions tell: Windows, a Kairos system, a Linux system, or, when it
// recognises nothing, everything on it.
func (d DiskInfo) ErasureWarning() string {
	var windows, kairos, linux bool
	for _, p := range d.Partitions {
		k := p.Kind()
		switch {
		case strings.HasPrefix(k, "Windows"):
			windows = true
		case strings.HasPrefix(k, "Kairos"):
			kairos = true
		case strings.HasPrefix(k, "Linux"):
			linux = true
		}
	}
	switch {
	case windows && linux:
		return i18n.T("Windows, a Linux system and their files on this disk will be erased.")
	case windows:
		return i18n.T("Windows and its files on this disk will be erased.")
	case kairos:
		return i18n.T("The Kairos system on this disk (Isotope or another) and its files will be erased.")
	case linux:
		return i18n.T("The Linux system and its files on this disk will be erased.")
	case d.Read && len(d.Partitions) == 0 && d.SizeBytes > 0:
		return i18n.T("This disk has no partitions. Anything on it will be erased.")
	}
	return i18n.T("Everything on this disk will be erased.")
}

// Segment is one piece of the disk bar.
type Segment struct {
	Kind      string
	SizeBytes int64
	Free      bool
}

// Segments are the disk's partitions in order and, when they leave room,
// the free space after them.
func (d DiskInfo) Segments() []Segment {
	if !d.Read {
		return nil
	}
	var out []Segment
	var used int64
	for _, p := range d.Partitions {
		out = append(out, Segment{Kind: p.Kind(), SizeBytes: p.SizeBytes})
		used += p.SizeBytes
	}
	// Less than 1% left is alignment and the GPT backup, not free space.
	if free := d.SizeBytes - used; free > 0 && free*100 >= d.SizeBytes {
		out = append(out, Segment{Kind: i18n.Mark("Free"), SizeBytes: free, Free: true})
	}
	return out
}

// HumanSize is a size as the installer shows it: "512 MB", "620 GB",
// "1 TB", in powers of 1000 like the disk's label.
func HumanSize(b int64) string {
	const k = 1000
	units := []string{"B", "KB", "MB", "GB", "TB", "PB"}
	f := float64(b)
	i := 0
	for f >= k && i < len(units)-1 {
		f /= k
		i++
	}
	if s := fmt.Sprintf("%.1f", f); f < 10 && !strings.HasSuffix(s, ".0") {
		return s + " " + units[i]
	}
	return fmt.Sprintf("%.0f %s", f, units[i])
}
