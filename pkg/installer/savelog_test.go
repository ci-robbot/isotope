package installer

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUSBMountPoints(t *testing.T) {
	block := sysfsFixture(t, map[string]string{
		"sda": "pci0000:00/0000:00:17.0/ata1/host0/target0:0:0/0:0:0:0",
		"sdb": "pci0000:00/0000:00:14.0/usb2/2-1/2-1:1.0/host3/target3:0:0/3:0:0:0",
		"sdc": "pci0000:00/0000:00:14.0/usb2/2-2/2-2:1.0/host4/target4:0:0/4:0:0:0",
	})
	for _, p := range []string{"sda/sda1", "sdb/sdb1", "sdc/sdc1"} {
		if err := os.MkdirAll(filepath.Join(block, p), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	mounts := strings.Join([]string{
		"/dev/sda1 /mnt/data ext4 rw,relatime 0 0",
		"/dev/sdb1 /media/MY\\040STICK vfat rw,relatime 0 0",
		"/dev/sdc1 /run/initramfs/live iso9660 ro,relatime 0 0",
		"/dev/sdc1 /media/ro vfat ro,relatime 0 0",
		"tmpfs /run tmpfs rw 0 0",
	}, "\n")
	got := USBMountPoints(strings.NewReader(mounts), block)
	if len(got) != 1 || got[0] != "/media/MY STICK" {
		t.Fatalf("%q", got)
	}
}

func TestSaveInstallLog(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "install.log")
	if err := os.WriteFile(src, []byte("INFO one\nERROR two\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	stick := filepath.Join(dir, "stick")
	if err := os.Mkdir(stick, 0o755); err != nil {
		t.Fatal(err)
	}
	where, err := SaveInstallLog(src, []string{filepath.Join(dir, "gone"), stick})
	if err != nil || where != filepath.Join(stick, "isotope-install.log") {
		t.Fatalf("%q %v", where, err)
	}
	if b, _ := os.ReadFile(where); string(b) != "INFO one\nERROR two\n" {
		t.Fatalf("copied %q", b)
	}
	if where, err := SaveInstallLog(src, nil); err != nil || where != src {
		t.Fatalf("no stick: %q %v", where, err)
	}
	if _, err := SaveInstallLog(filepath.Join(dir, "missing"), nil); err == nil {
		t.Fatal("a missing log saved")
	}
}
