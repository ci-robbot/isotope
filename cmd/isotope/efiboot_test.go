package main

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// efiFixture is a fake machine: a disk image with a GPT whose first
// partition is the ESP, the sysfs and mountinfo that describe it, an ESP
// directory holding the loader, and an efivars directory.
type efiFixture struct {
	env  efiBootEnv
	guid [16]byte
}

const (
	fixLBS   = 512
	fixStart = 2048
	fixSize  = 131072
)

func newEFIFixture(t *testing.T) efiFixture {
	t.Helper()
	root := t.TempDir()
	mk := func(p string) string {
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatal(err)
		}
		return p
	}
	write := func(p, s string) {
		mk(filepath.Dir(p))
		if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	var guid [16]byte
	copy(guid[:], []byte{0x82, 0x2e, 0x44, 0x81, 0xb6, 0x66, 0xd2, 0x5e, 0x96, 0x1b, 0x76, 0x65, 0xf5, 0x12, 0xbe, 0x2e})

	// Disk image: protective MBR, GPT header at LBA 1, entries at LBA 2.
	img := make([]byte, 64*fixLBS)
	hdr := img[fixLBS:]
	copy(hdr, "EFI PART")
	binary.LittleEndian.PutUint64(hdr[72:], 2)
	binary.LittleEndian.PutUint32(hdr[80:], 128)
	binary.LittleEndian.PutUint32(hdr[84:], 128)
	ent := img[2*fixLBS:]
	copy(ent[0:], []byte{0x28, 0x73, 0x2a, 0xc1, 0x1f, 0xf8, 0xd2, 0x11, 0xba, 0x4b, 0x00, 0xa0, 0xc9, 0x3e, 0xc9, 0x3b})
	copy(ent[16:], guid[:])
	binary.LittleEndian.PutUint64(ent[32:], fixStart)
	binary.LittleEndian.PutUint64(ent[40:], fixStart+fixSize-1)
	dev := mk(filepath.Join(root, "dev"))
	if err := os.WriteFile(filepath.Join(dev, "vda"), img, 0o644); err != nil {
		t.Fatal(err)
	}
	write(filepath.Join(dev, "vda1"), "")

	// sysfs: /sys/class/block/vda1 links into the disk's directory.
	devices := filepath.Join(root, "sys", "devices", "virtio0", "block", "vda")
	write(filepath.Join(devices, "queue", "logical_block_size"), "512\n")
	write(filepath.Join(devices, "vda1", "partition"), "1\n")
	write(filepath.Join(devices, "vda1", "start"), "2048\n")
	class := mk(filepath.Join(root, "sys", "class", "block"))
	for _, n := range []string{"vda", "vda/vda1"} {
		if err := os.Symlink(filepath.Join(devices, "..", n), filepath.Join(class, filepath.Base(n))); err != nil {
			t.Fatal(err)
		}
	}

	esp := mk(filepath.Join(root, "run", "cos", "efi"))
	write(filepath.Join(esp, "EFI", "boot", "bootx64.efi"), "shim")
	write(filepath.Join(root, "mountinfo"),
		"25 1 0:22 / /run rw - tmpfs tmpfs rw\n"+
			"99 25 252:1 / "+esp+" rw,relatime - vfat "+filepath.Join(dev, "vda1")+" rw\n")

	parts := mk(filepath.Join(root, "by-partuuid"))
	write(filepath.Join(parts, guidString(guid)), "")

	return efiFixture{
		env: efiBootEnv{
			espMount:   esp,
			mountinfo:  filepath.Join(root, "mountinfo"),
			sysBlock:   class,
			efivars:    mk(filepath.Join(root, "efivars")),
			byPartUUID: parts,
			arch:       "amd64",
		},
		guid: guid,
	}
}

func (f efiFixture) setVar(t *testing.T, name string, data []byte) {
	t.Helper()
	if err := writeVar(f.env.efivars, name, data); err != nil {
		t.Fatal(err)
	}
}

func (f efiFixture) order(t *testing.T) []uint16 {
	t.Helper()
	o, err := readBootOrder(f.env.efivars)
	if err != nil {
		t.Fatal(err)
	}
	return o
}

func otherEntry(desc string, guid byte) []byte {
	p := hardDriveDevicePath(espPartition{number: 1, startLBA: 2048, sizeLBA: 1000, signature: [16]byte{guid}}, `\EFI\ubuntu\shimx64.efi`)
	return loadOption(desc, p)
}

func TestEFIBootEntryWritesTheEntryFirst(t *testing.T) {
	f := newEFIFixture(t)
	f.setVar(t, "Boot0000", loadOption("UiApp", []byte{0x7f, 0xff, 4, 0}))
	f.setVar(t, "Boot0001", otherEntry("ubuntu", 0xaa))
	f.setVar(t, "BootOrder", encodeBootOrder([]uint16{1, 0}))

	var out bytes.Buffer
	if err := writeEFIBootEntry(f.env, &out); err != nil {
		t.Fatal(err)
	}
	if got := f.order(t); len(got) != 3 || got[0] != 2 || got[1] != 1 || got[2] != 0 {
		t.Fatalf("BootOrder = %v, want [2 1 0]", got)
	}
	data, err := readVar(f.env.efivars, "Boot0002")
	if err != nil {
		t.Fatal(err)
	}
	e, ok := parseLoadOption(data)
	if !ok || e.description != "Isotope" {
		t.Fatalf("Boot0002 = %+v", e)
	}
	if binary.LittleEndian.Uint32(data) != loadOptionActive {
		t.Errorf("attributes %#x, want LOAD_OPTION_ACTIVE", binary.LittleEndian.Uint32(data))
	}
	// HD(1,GPT,<guid>,0x800,0x20000)/File(\EFI\BOOT\BOOTX64.EFI)
	dp := e.devicePath
	if dp[0] != 4 || dp[1] != 1 || binary.LittleEndian.Uint32(dp[4:]) != 1 ||
		binary.LittleEndian.Uint64(dp[8:]) != fixStart || binary.LittleEndian.Uint64(dp[16:]) != fixSize ||
		!bytes.Equal(dp[24:40], f.guid[:]) || dp[40] != 2 || dp[41] != 2 {
		t.Errorf("hard drive node % x", dp[:42])
	}
	if !bytes.Equal(dp[46:len(dp)-4], utf16z(`\EFI\BOOT\BOOTX64.EFI`)) || !bytes.Equal(dp[len(dp)-4:], []byte{0x7f, 0xff, 4, 0}) {
		t.Errorf("file node % x", dp[42:])
	}
	// The other system's entry is untouched.
	if _, err := readVar(f.env.efivars, "Boot0001"); err != nil {
		t.Errorf("Boot0001 removed: %v", err)
	}
	if !strings.Contains(out.String(), "Boot0002") {
		t.Errorf("output %q", out.String())
	}
}

func TestEFIBootEntryIsIdempotent(t *testing.T) {
	f := newEFIFixture(t)
	f.setVar(t, "Boot0000", otherEntry("ubuntu", 0xaa))
	f.setVar(t, "BootOrder", encodeBootOrder([]uint16{0}))
	for i := 0; i < 2; i++ {
		if err := writeEFIBootEntry(f.env, &bytes.Buffer{}); err != nil {
			t.Fatal(err)
		}
	}
	if got := f.order(t); len(got) != 2 || got[0] != 1 || got[1] != 0 {
		t.Fatalf("BootOrder = %v, want [1 0]", got)
	}
	ents, _ := bootEntries(f.env.efivars)
	if len(ents) != 2 {
		t.Fatalf("%d entries, want 2", len(ents))
	}
}

// A reinstall leaves an "Isotope" entry for a partition GUID that no disk has
// any more. It is replaced; an "Isotope" entry for a partition that still
// exists (another disk) is kept.
func TestEFIBootEntryReplacesItsStaleEntries(t *testing.T) {
	f := newEFIFixture(t)
	f.setVar(t, "Boot0000", otherEntry("Windows Boot Manager", 0xbb))
	f.setVar(t, "Boot0003", otherEntry("Isotope", 0xcc)) // gone
	f.setVar(t, "Boot0004", otherEntry("Isotope", 0xdd)) // still on a disk
	var kept [16]byte
	kept[0] = 0xdd
	if err := os.WriteFile(filepath.Join(f.env.byPartUUID, guidString(kept)), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	f.setVar(t, "BootOrder", encodeBootOrder([]uint16{3, 0, 4}))

	if err := writeEFIBootEntry(f.env, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if got := f.order(t); len(got) != 3 || got[0] != 3 || got[1] != 0 || got[2] != 4 {
		t.Fatalf("BootOrder = %v, want [3 0 4]", got)
	}
	data, _ := readVar(f.env.efivars, "Boot0003")
	if e, _ := parseLoadOption(data); !bytes.Equal(e.devicePath[24:40], f.guid[:]) {
		t.Errorf("Boot0003 still names the old partition")
	}
	if _, err := readVar(f.env.efivars, "Boot0004"); err != nil {
		t.Errorf("the other disk's Isotope entry was removed: %v", err)
	}
}

func TestEFIBootEntryRefusesWithoutTheLoader(t *testing.T) {
	f := newEFIFixture(t)
	if err := os.RemoveAll(filepath.Join(f.env.espMount, "EFI")); err != nil {
		t.Fatal(err)
	}
	if err := writeEFIBootEntry(f.env, &bytes.Buffer{}); err == nil {
		t.Fatal("wrote an entry for a loader that is not on the ESP")
	}
	if ents, _ := os.ReadDir(f.env.efivars); len(ents) != 0 {
		t.Errorf("wrote %d variables", len(ents))
	}
}

func TestEFIBootEntryNeedsUEFI(t *testing.T) {
	f := newEFIFixture(t)
	f.env.efivars = filepath.Join(f.env.efivars, "missing")
	if err := writeEFIBootEntry(f.env, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "UEFI") {
		t.Fatalf("err = %v", err)
	}
}

func TestGUIDStringMatchesUdev(t *testing.T) {
	// The on-disk bytes of PARTUUID 81442e82-66b6-5ed2-961b-7665f512be2e, the
	// ESP of a Kairos install seen in QEMU.
	g := [16]byte{0x82, 0x2e, 0x44, 0x81, 0xb6, 0x66, 0xd2, 0x5e, 0x96, 0x1b, 0x76, 0x65, 0xf5, 0x12, 0xbe, 0x2e}
	if s := guidString(g); s != "81442e82-66b6-5ed2-961b-7665f512be2e" {
		t.Fatalf("guidString = %s", s)
	}
}
