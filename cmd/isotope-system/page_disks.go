package main

import (
	"fmt"
	"strings"
	"unicode/utf16"

	"gioui.org/layout"
	"gioui.org/widget"

	"github.com/kairos-community/isotope/pkg/guikit"
)

func init() { pageMakers[PageDisks] = func(u *UI) pageView { return newDisksPage(u) } }

const onlyAdminFormat = "Only an administrator can format a disk."

// disksPage: the drives udisks2 sees, with their partitions and the space
// used. A removable disk may be mounted, unmounted, ejected and powered
// off by anyone at the screen, and formatted by an admin. The system disk
// (and every disk, when the app cannot tell which one it is) has no
// buttons. udisks2 and polkit decide again on every call.
type disksPage struct {
	u    *UI
	list widget.List
	// Under u.mu.
	// roots reads the devices of the running system (rootDevices; the
	// tests give their own).
	roots    func() ([]string, error)
	drives   []Drive
	rootDevs []string
	rootsErr string
	loaded   bool
	loadErr  string
	working  string
	watching bool

	fmtForm formatForm

	driveClicks map[string]*driveClicks
	blockClicks map[string]*blockClicks
}

type driveClicks struct{ eject, powerOff, format widget.Clickable }
type blockClicks struct{ mount, unmount widget.Clickable }

// formatForm is the form of the Format dialog: the filesystem and the
// label.
type formatForm struct {
	fs          string // "exfat" or "ext4"
	exfat, ext4 widget.Clickable
	label       *guikit.Field
	focused     bool
}

func newDisksPage(u *UI) *disksPage {
	return &disksPage{u: u, roots: rootDevices,
		driveClicks: map[string]*driveClicks{}, blockClicks: map[string]*blockClicks{},
		fmtForm: formatForm{fs: "exfat", label: guikit.NewField(false)}}
}

func (p *disksPage) Shown() {
	p.watch()
	p.load()
}

// watch reloads the page on every change udisks2 signals, for the life of
// the window.
func (p *disksPage) watch() {
	u, ds := p.u, p.u.hooks.Disks
	if ds == nil {
		return
	}
	u.mu.Lock()
	if p.watching {
		u.mu.Unlock()
		return
	}
	p.watching = true
	u.mu.Unlock()
	go func() {
		changed, stop, err := ds.Watch()
		if err != nil {
			u.mu.Lock()
			p.watching = false
			u.mu.Unlock()
			return
		}
		defer stop()
		for range changed {
			p.load()
		}
	}()
}

func (p *disksPage) load() {
	u, ds := p.u, p.u.hooks.Disks
	if ds == nil {
		return
	}
	u.mu.Lock()
	read := p.roots
	u.mu.Unlock()
	u.async(func() {
		roots, rerr := read()
		list, err := ds.Drives()
		u.mu.Lock()
		defer u.mu.Unlock()
		if err != nil {
			p.loadErr = "Could not read the disks: " + err.Error()
			return
		}
		p.drives, p.rootDevs, p.loaded, p.loadErr = list, roots, true, ""
		p.rootsErr = ""
		if rerr != nil {
			p.rootsErr = rerr.Error()
		}
	})
}

func (p *disksPage) setRoots(f func() ([]string, error)) {
	p.u.mu.Lock()
	p.roots = f
	p.u.mu.Unlock()
}

func (p *disksPage) snapshot() []Drive {
	p.u.mu.Lock()
	defer p.u.mu.Unlock()
	return append([]Drive(nil), p.drives...)
}

func (p *disksPage) rootsNow() []string {
	p.u.mu.Lock()
	defer p.u.mu.Unlock()
	return p.rootDevs
}

func (p *disksPage) drive(id string) (Drive, bool) {
	for _, d := range p.snapshot() {
		if d.ID == id {
			return d, true
		}
	}
	return Drive{}, false
}

// changeable: d may be mounted, ejected, powered off and formatted from
// here. Only a removable disk that is no system disk.
func (p *disksPage) changeable(d Drive) bool {
	return d.Removable && !systemDrive(d, p.rootsNow())
}

// reasonText is what the page says of a system disk, "" for another.
func (p *disksPage) reasonText(d Drive) string {
	switch driveReason(d, p.rootsNow()) {
	case reasonIsotope:
		return "Isotope's system disk"
	case reasonInternal:
		return "Internal disk"
	}
	return ""
}

// formatNote is the line under the buttons of a removable disk for a
// non-admin.
func (p *disksPage) formatNote() string {
	if p.u.sys.Admin {
		return ""
	}
	return onlyAdminFormat
}

// ---- the calls ----

// do runs one udisks2 call off the UI thread and tells its error.
func (p *disksPage) do(working, failed string, call func(Disks) error) {
	u, ds := p.u, p.u.hooks.Disks
	if ds == nil {
		return
	}
	u.mu.Lock()
	p.working = working
	u.mu.Unlock()
	u.async(func() {
		err := call(ds)
		u.mu.Lock()
		p.working = ""
		u.mu.Unlock()
		if err != nil {
			u.tell(failed, err.Error())
		}
		p.load()
	})
}

// unmountAll unmounts every mounted filesystem of drive, as udisks2 sees
// it now, the last block first. It stops at the first failure.
func unmountAll(ds Disks, drive string) error {
	list, err := ds.Drives()
	if err != nil {
		return err
	}
	for _, d := range list {
		if d.ID != drive {
			continue
		}
		for i := len(d.Blocks) - 1; i >= 0; i-- {
			b := d.Blocks[i]
			if len(b.MountPoints) == 0 {
				continue
			}
			if err := ds.Unmount(b.ID); err != nil {
				return fmt.Errorf("cannot unmount %s: %w", b.Device, err)
			}
		}
	}
	return nil
}

func (p *disksPage) mount(block string) {
	p.do("Mounting…", "Could not mount the disk", func(ds Disks) error { _, err := ds.Mount(block); return err })
}

func (p *disksPage) unmount(block string) {
	p.do("Unmounting…", "Could not unmount the disk", func(ds Disks) error { return ds.Unmount(block) })
}

// eject and powerOff unmount the disk first, as a file manager does.
func (p *disksPage) eject(drive string) {
	p.do("Ejecting…", "Could not eject the disk", func(ds Disks) error {
		if err := unmountAll(ds, drive); err != nil {
			return err
		}
		return ds.Eject(drive)
	})
}

func (p *disksPage) powerOff(drive string) {
	p.do("Powering off…", "Could not power off the disk", func(ds Disks) error {
		if err := unmountAll(ds, drive); err != nil {
			return err
		}
		return ds.PowerOff(drive)
	})
}

// ---- format ----

// labelFits: label is not longer than fs allows. An exFAT label has at
// most 11 UTF-16 characters, an ext4 label at most 16 bytes.
func labelFits(fs, label string) bool {
	if fs == "ext4" {
		return len(label) <= 16
	}
	return len(utf16.Encode([]rune(label))) <= 11
}

func labelRule(fs string) string {
	if fs == "ext4" {
		return "An ext4 label has at most 16 bytes (fewer with accented letters)."
	}
	return "An exFAT label has at most 11 characters."
}

func (f *formatForm) valid() bool { return labelFits(f.fs, f.label.Editor.Text()) }

func (f *formatForm) clear() {
	f.fs, f.focused = "exfat", false
	f.label.Editor.SetText("")
	f.label.Err = ""
}

// driveName is the vendor and model, "Disk" when udisks2 knows neither.
func driveName(d Drive) string {
	if n := strings.TrimSpace(d.Vendor + " " + d.Model); n != "" {
		return n
	}
	return "Disk"
}

// wholeDevice is the device of the whole disk (/dev/sdb).
func wholeDevice(d Drive) string {
	if len(d.Blocks) > 0 && !d.Blocks[0].IsPartition {
		return d.Blocks[0].Device
	}
	return ""
}

// contents are the blocks shown under a drive: its partitions and what
// they hold, or the whole disk when it has a filesystem of its own.
func contents(d Drive) []Block {
	var out []Block
	for _, b := range d.Blocks {
		if b.IsPartition || b.IsFilesystem || b.cleartext {
			out = append(out, b)
		}
	}
	return out
}

func fsName(b Block) string {
	if b.FSType == "" {
		return "no filesystem"
	}
	return b.FSType
}

func labelOf(b Block) string {
	if b.Label == "" {
		return "No label"
	}
	return b.Label
}

// usedOf is "4.0 GB used of 32.0 GB" for a mounted block, its size for
// another.
func usedOf(b Block) string {
	if len(b.MountPoints) > 0 {
		return humanSize(b.Used) + " used of " + humanSize(b.Size)
	}
	return humanSize(b.Size)
}

// formatBody names the disk, lists what is on it and says what Format
// does.
func formatBody(d Drive) string {
	lines := []string{driveName(d) + ", " + humanSize(d.Size) + ", " + wholeDevice(d) + "."}
	mounted := false
	for _, b := range contents(d) {
		lines = append(lines, labelOf(b)+" ("+fsName(b)+"), "+usedOf(b))
		mounted = mounted || len(b.MountPoints) > 0
	}
	if mounted {
		lines = append(lines, "It is unmounted first.")
	}
	lines = append(lines, "Everything on this disk will be erased. This cannot be undone.")
	return strings.Join(lines, "\n")
}

func (p *disksPage) openFormat(drive string) {
	u := p.u
	d, ok := p.drive(drive)
	if !u.sys.Admin || !ok || !p.changeable(d) {
		return
	}
	f := &p.fmtForm
	f.clear()
	name := driveName(d)
	u.askForm("Erase and format "+name+"?", formatBody(d), p.formatFormView, 0,
		choice{Label: "Cancel", Kind: guikit.ButtonSecondary, Do: f.clear},
		choice{Label: "Erase and format", Kind: guikit.ButtonDanger, Enabled: f.valid, Do: func() {
			fs, label := f.fs, f.label.Editor.Text()
			f.clear()
			p.do("Formatting "+name+"…", "Could not format "+name, func(ds Disks) error {
				if err := unmountAll(ds, drive); err != nil {
					return err
				}
				return ds.Format(drive, fs, label)
			})
		}})
}

func (p *disksPage) formatFormView(gtx layout.Context) layout.Dimensions {
	u, f := p.u, &p.fmtForm
	if f.exfat.Clicked(gtx) {
		f.fs = "exfat"
	}
	if f.ext4.Clicked(gtx) {
		f.fs = "ext4"
	}
	if !f.focused {
		f.focused = true
		gtx.Execute(focus(&f.label.Editor))
	}
	f.label.Err = ""
	if !f.valid() {
		f.label.Err = labelRule(f.fs)
	}
	var c col
	c.add(func(gtx layout.Context) layout.Dimensions {
		return u.ChoiceCard(gtx, &f.exfat, f.fs == "exfat", "exFAT", "Works on Windows, macOS and Linux")
	})
	c.gap(10)
	c.add(func(gtx layout.Context) layout.Dimensions {
		return u.ChoiceCard(gtx, &f.ext4, f.fs == "ext4", "ext4", "Linux only")
	})
	c.gap(14)
	c.add(func(gtx layout.Context) layout.Dimensions {
		return fieldsCol(gtx, u, fieldRow{title: "Label", f: f.label})
	})
	return c.layout(gtx)
}

// ---- the page ----

func (p *disksPage) dClicks(id string) *driveClicks {
	k, ok := p.driveClicks[id]
	if !ok {
		k = &driveClicks{}
		p.driveClicks[id] = k
	}
	return k
}

func (p *disksPage) bClicks(id string) *blockClicks {
	k, ok := p.blockClicks[id]
	if !ok {
		k = &blockClicks{}
		p.blockClicks[id] = k
	}
	return k
}

// driveActions are the buttons of a drive: Eject, Power off, and Format…
// for an admin. None for a system disk or a disk that is not removable.
func (p *disksPage) driveActions(d Drive) []guikit.Action {
	if !p.changeable(d) {
		return nil
	}
	k := p.dClicks(d.ID)
	var as []guikit.Action
	if d.Ejectable {
		as = append(as, guikit.Action{Click: &k.eject, Kind: guikit.ButtonSecondary, Label: "Eject"})
	}
	if d.CanPowerOff {
		as = append(as, guikit.Action{Click: &k.powerOff, Kind: guikit.ButtonSecondary, Label: "Power off"})
	}
	if p.u.sys.Admin {
		as = append(as, guikit.Action{Click: &k.format, Kind: guikit.ButtonGhost, Label: "Format…"})
	}
	return as
}

// blockActions are the buttons of a filesystem of d: Mount or Unmount.
func (p *disksPage) blockActions(d Drive, b Block) []guikit.Action {
	if !p.changeable(d) || !b.IsFilesystem {
		return nil
	}
	k := p.bClicks(b.ID)
	if len(b.MountPoints) > 0 {
		return []guikit.Action{{Click: &k.unmount, Kind: guikit.ButtonSecondary, Label: "Unmount"}}
	}
	return []guikit.Action{{Click: &k.mount, Kind: guikit.ButtonSecondary, Label: "Mount"}}
}

func (p *disksPage) Update(gtx layout.Context) {
	for _, d := range p.snapshot() {
		if !p.changeable(d) {
			continue
		}
		k := p.dClicks(d.ID)
		if k.eject.Clicked(gtx) {
			p.eject(d.ID)
		}
		if k.powerOff.Clicked(gtx) {
			p.powerOff(d.ID)
		}
		if k.format.Clicked(gtx) {
			p.openFormat(d.ID)
		}
		for _, b := range d.Blocks {
			bk := p.bClicks(b.ID)
			if bk.mount.Clicked(gtx) {
				p.mount(b.ID)
			}
			if bk.unmount.Clicked(gtx) {
				p.unmount(b.ID)
			}
		}
	}
}

func (p *disksPage) Footer() (leading, trailing []guikit.Action) { return nil, nil }

func (p *disksPage) Body(gtx layout.Context) layout.Dimensions {
	u := p.u
	u.mu.Lock()
	loaded, loadErr, rootsErr, working := p.loaded, p.loadErr, p.rootsErr, p.working
	u.mu.Unlock()
	drives := p.snapshot()
	return u.scrolled(gtx, &p.list, func(gtx layout.Context) layout.Dimensions {
		var c col
		c.add(u.Heading("Disks", "The disks of this computer and the ones plugged in: space used, mount, eject and format USB disks."))
		c.gap(20)
		switch {
		case u.hooks.Disks == nil:
			c.add(u.errorBox("The disk service (udisks2) is not available."))
			return c.layout(gtx)
		case loadErr != "":
			c.add(u.errorBox(loadErr))
			return c.layout(gtx)
		case !loaded:
			c.add(u.spinnerLine("Reading the disks…"))
			return c.layout(gtx)
		}
		if rootsErr != "" {
			c.add(u.errorBox(rootsErr + ". Every disk is shown read-only."))
			c.gap(14)
		}
		if working != "" {
			c.add(u.spinnerLine(working))
			c.gap(14)
		}
		if len(drives) == 0 {
			c.add(u.dim("No disks."))
		}
		for i, d := range drives {
			if i > 0 {
				c.gap(12)
			}
			c.add(p.driveCard(d))
		}
		return c.layout(gtx)
	})
}

func (p *disksPage) driveCard(d Drive) layout.Widget {
	u := p.u
	sub := humanSize(d.Size)
	if dev := wholeDevice(d); dev != "" {
		sub += " · " + dev
	}
	if d.Removable {
		sub += " · Removable"
	}
	reason := p.reasonText(d)
	as := p.driveActions(d)
	note := ""
	if p.changeable(d) {
		note = p.formatNote()
	}
	return u.panel(func(gtx layout.Context) layout.Dimensions {
		var c col
		c.add(u.row(driveName(d), sub, func(gtx layout.Context) layout.Dimensions {
			if reason == "" {
				return layout.Dimensions{}
			}
			return u.Label(gtx, guikit.StSmall, guikit.ColText2, reason)
		}))
		for _, b := range contents(d) {
			c.gap(10)
			c.add(p.blockRow(d, b))
		}
		if len(as) > 0 {
			c.gap(12)
			c.add(func(gtx layout.Context) layout.Dimensions { return wrapButtons(gtx, u, as) })
		}
		if note != "" {
			c.gap(8)
			c.add(u.dim(note))
		}
		return c.layout(gtx)
	})
}

func (p *disksPage) blockRow(d Drive, b Block) layout.Widget {
	u := p.u
	sub := fsName(b) + " · " + usedOf(b)
	if b.Device != "" {
		sub += " · " + b.Device
	}
	if len(b.MountPoints) > 0 {
		sub += " · " + b.MountPoints[0]
	}
	as := p.blockActions(d, b)
	return func(gtx layout.Context) layout.Dimensions {
		return layout.Inset{Left: 14}.Layout(gtx, u.row(labelOf(b), sub, func(gtx layout.Context) layout.Dimensions {
			if len(as) == 0 {
				return layout.Dimensions{}
			}
			return u.buttons(as...)(gtx)
		}))
	}
}

// humanSize is a size in decimal units, as disks are sold: "32.0 GB".
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
