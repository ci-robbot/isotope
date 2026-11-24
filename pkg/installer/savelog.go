package installer

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/kairos-community/isotope/pkg/i18n"
)

// InstallLogPath is where the graphical installer keeps the whole install
// log while it runs. /run is a tmpfs, so it never touches a disk.
func InstallLogPath() string { return filepath.Join(RunDir(), "install.log") }

// writableFS are the file systems a USB stick is formatted with that the
// log can be written to.
var writableFS = map[string]bool{"vfat": true, "exfat": true, "ntfs": true, "ntfs3": true, "ext4": true, "ext3": true, "ext2": true, "btrfs": true, "xfs": true, "f2fs": true}

// USBMountPoints are the mount points, read-write, of partitions on USB
// disks: where "Save the log" can put the log so it leaves the machine.
// mounts is /proc/mounts; sysBlock is /sys/block.
func USBMountPoints(mounts io.Reader, sysBlock string) []string {
	var out []string
	sc := bufio.NewScanner(mounts)
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) < 4 || !strings.HasPrefix(f[0], "/dev/") || !writableFS[f[2]] {
			continue
		}
		ro := false
		for _, o := range strings.Split(f[3], ",") {
			if o == "ro" {
				ro = true
			}
		}
		if ro {
			continue
		}
		disk := diskOfPartition(sysBlock, strings.TrimPrefix(f[0], "/dev/"))
		if disk != "" && DiskBus(sysBlock, disk) == "USB" {
			out = append(out, unescapeMount(f[1]))
		}
	}
	return out
}

// diskOfPartition is the whole disk a partition (sdb1) belongs to (sdb), or
// "" when sysBlock has neither.
func diskOfPartition(sysBlock, part string) string {
	entries, err := os.ReadDir(sysBlock)
	if err != nil {
		return ""
	}
	for _, e := range entries {
		if e.Name() == part {
			return part
		}
		if _, err := os.Stat(filepath.Join(sysBlock, e.Name(), part)); err == nil {
			return e.Name()
		}
	}
	return ""
}

// unescapeMount undoes /proc/mounts' octal escapes (\040 for a space).
func unescapeMount(s string) string {
	r := strings.NewReplacer(`\040`, " ", `\011`, "\t", `\012`, "\n", `\134`, `\`)
	return r.Replace(s)
}

// SaveInstallLog copies the install log to the first USB stick in dirs, or
// leaves it where it is when there is none, and says where it is.
func SaveInstallLog(src string, dirs []string) (string, error) {
	for _, d := range dirs {
		dst := filepath.Join(d, "isotope-install.log")
		if err := copyFile(src, dst); err == nil {
			return dst, nil
		}
	}
	if _, err := os.Stat(src); err != nil {
		return "", fmt.Errorf(i18n.T("the install log is gone: %w"), err)
	}
	return src, nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := out.Sync(); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
