package main

// isotope efi-boot-entry: give the installed disk a UEFI boot entry.
//
// kairos-agent puts shim and GRUB on the ESP only at the removable-media path,
// \EFI\BOOT\BOOTX64.EFI, and writes no Boot#### variable. A firmware boots
// that path only when its boot list does not stop it first. On a machine that
// had another system before, the boot list still names that system's entries
// (an "ubuntu" entry on a partition the install just erased, a Windows Boot
// Manager on another disk), and many firmwares try those, fail or boot the
// other system, and never look at the new disk. QEMU's OVMF does the same: after
// an install from the live ISO it went to its EFI shell.
//
// So, at the end of an install (the after-install stage in
// /system/oem/11_isotope_efi_boot_entry.yaml), this writes a Boot#### variable
// for the loader on the ESP kairos-agent just wrote, and puts it first in
// BootOrder. It needs nothing but the kernel's efivarfs: the image has no
// efibootmgr.
//
// It touches only variables it can prove are its own: the entry for this exact
// partition and file, and older "Isotope" entries whose partition no longer
// exists on any disk (the ones a reinstall leaves behind). Every other entry,
// another system's included, stays as it was.

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"unicode/utf16"

	"golang.org/x/sys/unix"
)

// efiGlobalVariable is EFI_GLOBAL_VARIABLE, the vendor GUID of Boot#### and
// BootOrder.
const efiGlobalVariable = "8be4df61-93ca-11d2-aa0d-00e098032b8c"

// efiBootDescription is the name the firmware's boot menu shows.
const efiBootDescription = "Isotope"

// Variable attributes: NON_VOLATILE | BOOTSERVICE_ACCESS | RUNTIME_ACCESS.
const efiVarAttrs = 0x7

// loadOptionActive is LOAD_OPTION_ACTIVE.
const loadOptionActive = 0x1

// efiBootEnv holds every path the command reads or writes, so a test can point
// all of them at a temporary directory.
type efiBootEnv struct {
	espMount   string // where kairos-agent mounted the ESP for the install
	mountinfo  string // /proc/self/mountinfo
	sysBlock   string // /sys/class/block
	efivars    string // the efivarfs mount
	byPartUUID string // /dev/disk/by-partuuid, to find stale entries; may be absent
	arch       string // runtime.GOARCH
}

func defaultEFIBootEnv() efiBootEnv {
	return efiBootEnv{
		espMount:   "/run/cos/efi", // kairos-agent's constants.EfiDir
		mountinfo:  "/proc/self/mountinfo",
		sysBlock:   "/sys/class/block",
		efivars:    "/sys/firmware/efi/efivars",
		byPartUUID: "/dev/disk/by-partuuid",
		arch:       runtime.GOARCH,
	}
}

func cmdEFIBootEntry(args []string, stdout, stderr io.Writer) int {
	fs := newFlags("efi-boot-entry", stderr)
	esp := fs.String("esp", "/run/cos/efi", "mount point of the ESP the install wrote")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 {
		return exitUsage
	}
	env := defaultEFIBootEnv()
	env.espMount = *esp
	if err := writeEFIBootEntry(env, stdout); err != nil {
		fmt.Fprintf(stderr, "isotope efi-boot-entry: %v\n", err)
		return exitError
	}
	return exitOK
}

// espPartition is what a hard drive device path node needs to name the ESP.
type espPartition struct {
	disk      string // block device of the whole disk, e.g. /dev/nvme0n1
	number    uint32 // partition number, 1-based
	startLBA  uint64 // in the disk's logical blocks
	sizeLBA   uint64
	signature [16]byte // the GPT unique partition GUID, as stored on disk
}

func writeEFIBootEntry(env efiBootEnv, stdout io.Writer) error {
	if fi, err := os.Stat(env.efivars); err != nil || !fi.IsDir() {
		return fmt.Errorf("no %s: this machine did not boot with UEFI", env.efivars)
	}
	loader, err := removableLoader(env.arch)
	if err != nil {
		return err
	}
	if err := findOnFAT(env.espMount, loader); err != nil {
		return err
	}
	dev, err := mountSource(env.mountinfo, env.espMount)
	if err != nil {
		return err
	}
	part, err := readESPPartition(env.sysBlock, dev)
	if err != nil {
		return err
	}

	devicePath := hardDriveDevicePath(part, loader)
	option := loadOption(efiBootDescription, devicePath)

	entries, err := bootEntries(env.efivars)
	if err != nil {
		return err
	}
	num := -1
	var stale []int
	for _, e := range entries {
		switch {
		case num < 0 && bytes.Equal(e.devicePath, devicePath):
			num = e.num
		case e.description == efiBootDescription && e.partitionGone(env.byPartUUID):
			stale = append(stale, e.num)
		}
	}
	if num < 0 {
		num = freeBootNumber(entries, stale)
	}

	for _, n := range stale {
		if n == num {
			continue
		}
		if err := removeVar(env.efivars, bootVarName(n)); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "removed the stale boot entry Boot%04X\n", n)
	}
	if err := writeVar(env.efivars, bootVarName(num), option); err != nil {
		return err
	}

	order, err := readBootOrder(env.efivars)
	if err != nil {
		return err
	}
	if err := writeVar(env.efivars, "BootOrder", encodeBootOrder(bootFirst(order, num, stale))); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "Boot%04X %q: %s partition %d %s, first in BootOrder\n",
		num, efiBootDescription, part.disk, part.number, loader)
	return nil
}

// removableLoader is the removable-media path of the UEFI spec for this
// architecture. It is where kairos-agent installs shim.
func removableLoader(arch string) (string, error) {
	switch arch {
	case "amd64":
		return `\EFI\BOOT\BOOTX64.EFI`, nil
	case "arm64":
		return `\EFI\BOOT\BOOTAA64.EFI`, nil
	}
	return "", fmt.Errorf("no removable-media loader name for %s", arch)
}

// findOnFAT checks that the loader exists on the ESP. FAT ignores case and
// kairos-agent writes efi/boot/bootx64.efi in lower case, so match each name
// without case.
func findOnFAT(root, loader string) error {
	dir := root
	for _, want := range strings.Split(strings.TrimPrefix(loader, `\`), `\`) {
		ents, err := os.ReadDir(dir)
		if err != nil {
			return fmt.Errorf("reading the ESP: %w", err)
		}
		found := ""
		for _, e := range ents {
			if strings.EqualFold(e.Name(), want) {
				found = e.Name()
				break
			}
		}
		if found == "" {
			return fmt.Errorf("%s is not on the ESP at %s", loader, root)
		}
		dir = filepath.Join(dir, found)
	}
	return nil
}

// mountSource returns the device mounted at mountpoint, from mountinfo.
func mountSource(mountinfo, mountpoint string) (string, error) {
	f, err := os.Open(mountinfo)
	if err != nil {
		return "", err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	src := ""
	for sc.Scan() {
		// ID PARENT MAJ:MIN ROOT MOUNTPOINT OPTIONS [OPTIONAL...] - FSTYPE SOURCE SUPER
		fields := strings.Fields(sc.Text())
		if len(fields) < 5 || unescapeMountinfo(fields[4]) != mountpoint {
			continue
		}
		for i, f := range fields {
			if f == "-" && i+2 < len(fields) {
				src = fields[i+2] // the last mount on the point wins
			}
		}
	}
	if err := sc.Err(); err != nil {
		return "", err
	}
	if !filepath.IsAbs(src) {
		return "", fmt.Errorf("nothing is mounted at %s", mountpoint)
	}
	return src, nil
}

func unescapeMountinfo(s string) string {
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

// readESPPartition finds the partition behind dev in sysfs and reads its GPT
// entry from the disk.
func readESPPartition(sysBlock, dev string) (espPartition, error) {
	var p espPartition
	if real, err := filepath.EvalSymlinks(dev); err == nil {
		dev = real
	}
	name := filepath.Base(dev)
	sysPart, err := filepath.EvalSymlinks(filepath.Join(sysBlock, name))
	if err != nil {
		return p, fmt.Errorf("%s has no sysfs entry: %w", dev, err)
	}
	num, err := readSysUint(filepath.Join(sysPart, "partition"))
	if err != nil {
		return p, fmt.Errorf("%s is not a partition: %w", dev, err)
	}
	diskName := filepath.Base(filepath.Dir(sysPart))
	lbs, err := readSysUint(filepath.Join(sysBlock, diskName, "queue", "logical_block_size"))
	if err != nil || lbs == 0 {
		lbs = 512
	}
	p.disk = filepath.Join(filepath.Dir(dev), diskName)
	p.number = uint32(num)

	f, err := os.Open(p.disk)
	if err != nil {
		return p, err
	}
	defer f.Close()
	ent, err := readGPTEntry(f, lbs, p.number)
	if err != nil {
		return p, fmt.Errorf("%s: %w", p.disk, err)
	}
	p.startLBA, p.sizeLBA, p.signature = ent.first, ent.last-ent.first+1, ent.unique

	// sysfs counts in 512-byte sectors whatever the block size. The GPT is
	// the source of truth; the check only catches reading the wrong disk.
	if start, err := readSysUint(filepath.Join(sysPart, "start")); err == nil && start*512 != p.startLBA*lbs {
		return p, fmt.Errorf("%s: the GPT puts partition %d at LBA %d, the kernel at sector %d", p.disk, p.number, p.startLBA, start)
	}
	return p, nil
}

func readSysUint(path string) (uint64, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	return strconv.ParseUint(strings.TrimSpace(string(b)), 10, 64)
}

type gptEntry struct {
	first, last uint64
	unique      [16]byte
}

var errNoGPT = errors.New("no GPT")

// readGPTEntry reads partition entry num (1-based) of the primary GPT.
func readGPTEntry(r io.ReaderAt, lbs uint64, num uint32) (gptEntry, error) {
	var e gptEntry
	hdr := make([]byte, 92)
	if _, err := r.ReadAt(hdr, int64(lbs)); err != nil {
		return e, err
	}
	if string(hdr[:8]) != "EFI PART" {
		return e, errNoGPT
	}
	entriesLBA := binary.LittleEndian.Uint64(hdr[72:])
	count := binary.LittleEndian.Uint32(hdr[80:])
	size := binary.LittleEndian.Uint32(hdr[84:])
	if num == 0 || num > count || size < 128 {
		return e, fmt.Errorf("no GPT entry %d", num)
	}
	buf := make([]byte, 128)
	off := int64(entriesLBA*lbs) + int64(num-1)*int64(size)
	if _, err := r.ReadAt(buf, off); err != nil {
		return e, err
	}
	copy(e.unique[:], buf[16:32])
	e.first = binary.LittleEndian.Uint64(buf[32:])
	e.last = binary.LittleEndian.Uint64(buf[40:])
	if e.first == 0 || e.last < e.first {
		return e, fmt.Errorf("GPT entry %d is empty", num)
	}
	return e, nil
}

// hardDriveDevicePath is HD(part,GPT,guid,start,size)/File(loader)/End, the
// device path efibootmgr writes for a GPT ESP.
func hardDriveDevicePath(p espPartition, loader string) []byte {
	var b bytes.Buffer
	// Hard drive media device path: type 4, subtype 1, length 42.
	b.Write([]byte{0x04, 0x01, 42, 0})
	binary.Write(&b, binary.LittleEndian, p.number)
	binary.Write(&b, binary.LittleEndian, p.startLBA)
	binary.Write(&b, binary.LittleEndian, p.sizeLBA)
	b.Write(p.signature[:])
	b.Write([]byte{0x02, 0x02}) // GPT partition format, GUID signature
	// File path media device path: type 4, subtype 4.
	path := utf16z(loader)
	b.Write([]byte{0x04, 0x04})
	binary.Write(&b, binary.LittleEndian, uint16(4+len(path)))
	b.Write(path)
	// End of the whole device path.
	b.Write([]byte{0x7f, 0xff, 4, 0})
	return b.Bytes()
}

// loadOption is an EFI_LOAD_OPTION: attributes, the device path length, the
// description and the device path. No optional data: shim needs none to start
// GRUB from the same directory.
func loadOption(description string, devicePath []byte) []byte {
	var b bytes.Buffer
	binary.Write(&b, binary.LittleEndian, uint32(loadOptionActive))
	binary.Write(&b, binary.LittleEndian, uint16(len(devicePath)))
	b.Write(utf16z(description))
	b.Write(devicePath)
	return b.Bytes()
}

func utf16z(s string) []byte {
	u := utf16.Encode([]rune(s))
	b := make([]byte, 2*len(u)+2)
	for i, c := range u {
		binary.LittleEndian.PutUint16(b[2*i:], c)
	}
	return b
}

type bootEntry struct {
	num         int
	description string
	devicePath  []byte
}

// partitionGone reports whether the entry names a GPT partition that no disk
// has any more. Without a by-partuuid directory nothing can be proven gone.
func (e bootEntry) partitionGone(byPartUUID string) bool {
	if len(e.devicePath) < 42 || e.devicePath[0] != 0x04 || e.devicePath[1] != 0x01 || e.devicePath[41] != 0x02 {
		return false
	}
	if _, err := os.Stat(byPartUUID); err != nil {
		return false
	}
	var sig [16]byte
	copy(sig[:], e.devicePath[24:40])
	_, err := os.Stat(filepath.Join(byPartUUID, guidString(sig)))
	return errors.Is(err, os.ErrNotExist)
}

// guidString formats an on-disk (mixed-endian) GUID the way udev names it.
func guidString(g [16]byte) string {
	return fmt.Sprintf("%08x-%04x-%04x-%x-%x",
		binary.LittleEndian.Uint32(g[0:]), binary.LittleEndian.Uint16(g[4:]),
		binary.LittleEndian.Uint16(g[6:]), g[8:10], g[10:16])
}

func bootVarName(n int) string { return fmt.Sprintf("Boot%04X", n) }

// bootEntries reads every Boot#### variable it can parse.
func bootEntries(efivars string) ([]bootEntry, error) {
	ents, err := os.ReadDir(efivars)
	if err != nil {
		return nil, err
	}
	var out []bootEntry
	for _, d := range ents {
		name, ok := strings.CutSuffix(d.Name(), "-"+efiGlobalVariable)
		if !ok || len(name) != 8 || !strings.HasPrefix(name, "Boot") {
			continue
		}
		n, err := strconv.ParseUint(name[4:], 16, 16)
		if err != nil || strings.ToUpper(name[4:]) != name[4:] {
			continue
		}
		data, err := readVar(efivars, name)
		if err != nil {
			continue
		}
		e, ok := parseLoadOption(data)
		if !ok {
			continue
		}
		e.num = int(n)
		out = append(out, e)
	}
	return out, nil
}

func parseLoadOption(b []byte) (bootEntry, bool) {
	var e bootEntry
	if len(b) < 8 {
		return e, false
	}
	pathLen := int(binary.LittleEndian.Uint16(b[4:]))
	rest := b[6:]
	var desc []uint16
	i := 0
	for ; i+1 < len(rest); i += 2 {
		c := binary.LittleEndian.Uint16(rest[i:])
		if c == 0 {
			break
		}
		desc = append(desc, c)
	}
	start := i + 2
	if start+pathLen > len(rest) {
		return e, false
	}
	e.description = string(utf16.Decode(desc))
	e.devicePath = rest[start : start+pathLen]
	return e, true
}

// freeBootNumber is the lowest Boot#### number no entry uses, reusing a stale
// one first.
func freeBootNumber(entries []bootEntry, stale []int) int {
	if len(stale) > 0 {
		s := append([]int(nil), stale...)
		sort.Ints(s)
		return s[0]
	}
	used := map[int]bool{}
	for _, e := range entries {
		used[e.num] = true
	}
	n := 0
	for used[n] {
		n++
	}
	return n
}

func readBootOrder(efivars string) ([]uint16, error) {
	data, err := readVar(efivars, "BootOrder")
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	order := make([]uint16, 0, len(data)/2)
	for i := 0; i+1 < len(data); i += 2 {
		order = append(order, binary.LittleEndian.Uint16(data[i:]))
	}
	return order, nil
}

// bootFirst puts num first, and drops num's old place and every removed entry.
func bootFirst(order []uint16, num int, removed []int) []uint16 {
	drop := map[uint16]bool{uint16(num): true}
	for _, n := range removed {
		drop[uint16(n)] = true
	}
	out := []uint16{uint16(num)}
	for _, n := range order {
		if !drop[n] {
			out = append(out, n)
		}
	}
	return out
}

func encodeBootOrder(order []uint16) []byte {
	b := make([]byte, 2*len(order))
	for i, n := range order {
		binary.LittleEndian.PutUint16(b[2*i:], n)
	}
	return b
}

func varPath(efivars, name string) string {
	return filepath.Join(efivars, name+"-"+efiGlobalVariable)
}

// readVar returns a variable's data, without the four attribute bytes
// efivarfs puts in front.
func readVar(efivars, name string) ([]byte, error) {
	b, err := os.ReadFile(varPath(efivars, name))
	if err != nil {
		return nil, err
	}
	if len(b) < 4 {
		return nil, fmt.Errorf("%s: short variable", name)
	}
	return b[4:], nil
}

// writeVar sets a variable. efivarfs takes the attributes and the data in one
// write, and marks most existing variables immutable, so clear that first.
func writeVar(efivars, name string, data []byte) error {
	p := varPath(efivars, name)
	clearImmutable(p)
	buf := make([]byte, 4+len(data))
	binary.LittleEndian.PutUint32(buf, efiVarAttrs)
	copy(buf[4:], data)
	f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE, 0o644)
	if err != nil {
		return fmt.Errorf("writing %s: %w", name, err)
	}
	if _, err := f.Write(buf); err != nil {
		f.Close()
		return fmt.Errorf("writing %s: %w", name, err)
	}
	// efivarfs replaces the variable on write and has no size to cut. A
	// plain file (a test) keeps old bytes past the end without this.
	if fi, err := f.Stat(); err == nil && fi.Size() > int64(len(buf)) {
		_ = f.Truncate(int64(len(buf)))
	}
	return f.Close()
}

func removeVar(efivars, name string) error {
	p := varPath(efivars, name)
	clearImmutable(p)
	if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("removing %s: %w", name, err)
	}
	return nil
}

// clearImmutable drops FS_IMMUTABLE_FL, as efibootmgr does. Best effort: a
// file system without inode flags (a test's temporary directory) needs none.
func clearImmutable(path string) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	flags, err := unix.IoctlGetUint32(int(f.Fd()), unix.FS_IOC_GETFLAGS)
	if err != nil || flags&fsImmutableFL == 0 {
		return
	}
	_ = unix.IoctlSetPointerInt(int(f.Fd()), unix.FS_IOC_SETFLAGS, int(flags&^fsImmutableFL))
}

// fsImmutableFL is FS_IMMUTABLE_FL from linux/fs.h; x/sys does not name it.
const fsImmutableFL = 0x00000010
