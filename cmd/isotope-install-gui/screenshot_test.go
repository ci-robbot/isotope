package main

import (
	"errors"
	"flag"
	"fmt"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	_ "time/tzdata" // the zone clock, the same on every test machine

	"gioui.org/gpu/headless"
	"gioui.org/io/input"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"

	"github.com/kairos-community/isotope/pkg/guikit"
	"github.com/kairos-community/isotope/pkg/installer"
)

// The screenshot tests render every page at the two sizes of the design
// note and compare them with the PNGs in testdata/, within a small
// tolerance: llvmpipe's anti-aliasing moves a little between Mesa releases.
// `go test -update` rewrites the PNGs; a change to a page shows in its PR.
//
// They need an EGL with a surfaceless platform (Mesa), and skip without one
// or with ISOTOPE_GUI_SKIP_SCREENSHOTS=1 (the image build, whose Mesa is not
// the one the PNGs were made with).
var update = flag.Bool("update", false, "rewrite the golden screenshots in testdata/")

var sizes = []image.Point{{1440, 900}, {1024, 768}}

// tolerance: a pixel differs when a channel is off by more than pixelTol,
// and a page fails when more than pageTol of its pixels differ.
const (
	pixelTol = 24
	pageTol  = 0.004
)

func TestMain(m *testing.M) {
	if os.Getenv("EGL_PLATFORM") == "" {
		os.Setenv("EGL_PLATFORM", "surfaceless")
	}
	os.Exit(m.Run())
}

// fixtureDisks are what lsblk and sysfs say about the fixture's disks: a
// laptop disk with Windows, a Linux data disk, a USB stick.
var fixtureDisks = map[string]installer.DiskInfo{
	"/dev/nvme0n1": {Model: "Samsung SSD 980 PRO 1TB", Bus: "NVMe", SizeBytes: 1000204886016, Read: true, Partitions: []installer.Partition{
		{Name: "nvme0n1p1", SizeBytes: 536870912, FSType: "vfat", Label: "SYSTEM", PartLabel: "EFI system partition"},
		{Name: "nvme0n1p2", SizeBytes: 16777216, PartLabel: "Microsoft reserved partition"},
		{Name: "nvme0n1p3", SizeBytes: 620000000000, FSType: "ntfs", Label: "Windows", PartLabel: "Basic data partition"},
	}},
	"/dev/sda": {Model: "WDC WD20EZAZ-00G", Bus: "SATA", SizeBytes: 2000398934016, Read: true, Partitions: []installer.Partition{
		{Name: "sda1", SizeBytes: 2000397885440, FSType: "ext4", Label: "data"},
	}},
	"/dev/sdb": {Model: "SanDisk Ultra", Bus: "USB", SizeBytes: 64023257088, Read: true, Partitions: []installer.Partition{
		{Name: "sdb1", SizeBytes: 64022208512, FSType: "exfat", Label: "SANDISK"},
	}},
}

var testKey = "cbdefghi-jklnrtuv-vutrnlkj-ihgfedcb-bcdtrnhe-gkulvfij-nrdcebtv-lhjkfgiu"

// fixture is a UI on a wizard with three disks, the curated lists and fixed
// time, key and group, with nothing that touches the machine.
func fixture(t *testing.T, trustedBoot bool) *UI {
	t.Helper()
	return fixtureMode(t, trustedBoot, nil)
}

// fixtureMode is fixture with mode applied to the UI before it gets the
// wizard: window mode sets its fields there, as main does.
func fixtureMode(t *testing.T, trustedBoot bool, mode func(*UI)) *UI {
	t.Helper()
	disks := []installer.Disk{{Path: "/dev/nvme0n1", SizeGB: 1000}, {Path: "/dev/sda", SizeGB: 2000}, {Path: "/dev/sdb", SizeGB: 64}}
	w := installer.NewWizardWith(disks, installer.CuratedKeyboards, installer.CuratedTimezones, "Europe/Rome")
	w.TrustedBoot = trustedBoot
	w.NewKey = func() (string, error) { return testKey, nil }
	w.PickGroup = func() int { return 2 }
	u := newUI(nil, guikit.NewShaper(), hooks{
		confirm:  func(installer.Choice) {},
		relaunch: func(installer.GUIState) { t.Fatal("relaunch") },
		poweroff: func() {},
		reboot:   func() {},
		describe: func(path string) installer.DiskInfo { return fixtureDisks[path] },
		saveLog:  func() (string, error) { return "/media/STICK/isotope-install.log", nil },
		terminal: func() {},
	})
	if mode != nil {
		mode(u)
	}
	u.setWizard(&w)
	f, err := os.Open("testdata/zone.tab")
	if err != nil {
		t.Fatal(err)
	}
	u.zoneCoords = installer.ParseZoneTab(f)
	f.Close()
	at := time.Date(2026, 11, 17, 12, 4, 0, 0, time.UTC)
	u.now = func() time.Time { return at }
	u.version = "Isotope v0.0.20 · sway"
	return u
}

// mustNext presses Continue and checks the page it lands on.
func mustNext(t *testing.T, u *UI, want gpage) {
	t.Helper()
	u.next()
	if u.page != want {
		errs := []string{}
		for _, f := range []*guikit.Field{u.user, u.pw, u.pw2, u.gh, u.pp, u.pp2, u.rk, u.tzField} {
			if f.Err != "" {
				errs = append(errs, f.Err)
			}
		}
		t.Fatalf("Continue went to page %d, want %d (errors: %s %s)", u.page, want, strings.Join(errs, "; "), u.encErr)
	}
}

func set(f *guikit.Field, s string) { f.Editor.SetText(s) }

func toKeyboard(t *testing.T, u *UI) {
	mustNext(t, u, pKeyboard)
	set(u.kbSearch, "ital")
	u.filterKeyboards()
	u.kbSel = u.kbShown[0]
	set(u.kbTry, "perché però è già")
}

func toAccount(t *testing.T, u *UI) {
	toKeyboard(t, u)
	mustNext(t, u, pAccount)
	set(u.host, "isotope-laptop")
	set(u.user, "alex")
	set(u.pw, "hunter22hunter")
	set(u.pw2, "hunter22hunter")
	set(u.gh, "alex")
	// GitHub answered: three keys.
	u.ghState.asked, u.ghState.answered, u.ghState.n = "alex", "alex", 3
}

func toEncrypt(t *testing.T, u *UI) {
	toAccount(t, u)
	mustNext(t, u, pDisk)
	mustNext(t, u, pEncrypt)
	u.encOn = true
	set(u.pp, "correct horse battery")
	set(u.pp2, "correct horse battery")
	u.recKey = true
}

func toRecovery(t *testing.T, u *UI) {
	toEncrypt(t, u)
	mustNext(t, u, pRecovery)
	set(u.rk, "vutrn")
}

func toSummary(t *testing.T, u *UI) {
	toRecovery(t, u)
	set(u.rk, "vutrnlkj")
	mustNext(t, u, pSummary)
}

func toInstalling(t *testing.T, u *UI) {
	toSummary(t, u)
	mustNext(t, u, pInstalling)
	for _, l := range []string{
		"INFO Partitioning device /dev/nvme0n1",
		"INFO Running stage: before-install",
		"INFO Creating file system image /run/cos/state/cOS/active.img",
		"INFO Copying /run/rootfsbase to /run/cos/active",
		"INFO Finished copying /run/rootfsbase into /run/cos/active",
	} {
		u.apply(installer.Event{Kind: installer.EventLine, Line: l})
		if s := installer.AdvanceStep(l, u.install.step); s != u.install.step {
			u.apply(installer.Event{Kind: installer.EventStep, Step: s})
		}
	}
	u.install.show = true
	// Four minutes in, ninety seconds into the active system.
	u.install.started = u.now().Add(-4 * time.Minute)
	u.install.activeAt = u.now().Add(-90 * time.Second)
}

var pages = []struct {
	name  string
	tb    bool
	setup func(*testing.T, *UI)
}{
	{"1-welcome", false, func(*testing.T, *UI) {}},
	{"2-keyboard", false, toKeyboard},
	{"3-account", false, toAccount},
	{"4-disk", false, func(t *testing.T, u *UI) { toAccount(t, u); mustNext(t, u, pDisk) }},
	{"5-encryption", false, toEncrypt},
	{"5-encryption-trusted-boot", true, func(t *testing.T, u *UI) {
		toAccount(t, u)
		mustNext(t, u, pDisk)
		mustNext(t, u, pEncrypt)
		u.recKey = true
	}},
	{"6-recovery-key", false, toRecovery},
	{"7-summary", false, toSummary},
	{"8-installing", false, toInstalling},
	{"8-installing-failed", false, func(t *testing.T, u *UI) {
		toInstalling(t, u)
		u.apply(installer.Event{Kind: installer.EventLine, Line: "ERROR failed copying /run/rootfsbase: no space left on device"})
		u.apply(installer.Event{Kind: installer.EventFailed, Err: errors.New("exit status 1")})
	}},
	{"9-done", false, func(t *testing.T, u *UI) {
		toInstalling(t, u)
		u.apply(installer.Event{Kind: installer.EventStep, Step: len(installer.Steps) - 1})
		u.apply(installer.Event{Kind: installer.EventFinished})
	}},
	{"2-keyboard-zone-search", false, func(t *testing.T, u *UI) {
		toKeyboard(t, u)
		set(u.tzField, "new")
		u.filterZones()
	}},
	{"3-account-github-unreachable", false, func(t *testing.T, u *UI) {
		toAccount(t, u)
		u.ghState.err = errors.New("dial tcp: no route to host")
	}},
	{"4-disk-usb", false, func(t *testing.T, u *UI) {
		toAccount(t, u)
		mustNext(t, u, pDisk)
		u.diskSel = 2
	}},
	{"6-recovery-key-qr", false, func(t *testing.T, u *UI) {
		toRecovery(t, u)
		u.showQR = true
	}},
	{"8-installing-early", false, func(t *testing.T, u *UI) {
		toInstalling(t, u)
		u.install.activeAt = u.now().Add(-20 * time.Second)
		u.install.show = false
	}},
	{"scaled-150", false, func(t *testing.T, u *UI) {
		toAccount(t, u)
		u.scale = 1.5
	}},
	{"account-errors", false, func(t *testing.T, u *UI) {
		toAccount(t, u)
		set(u.user, "Alex")
		set(u.gh, "-alex")
		u.next()
	}},
}

func TestScreenshots(t *testing.T) {
	needGPU(t)
	for _, p := range pages {
		for _, sz := range sizes {
			name := fmt.Sprintf("%s-%dx%d", p.name, sz.X, sz.Y)
			t.Run(name, func(t *testing.T) {
				u := fixture(t, p.tb)
				p.setup(t, u)
				u.pageAt = time.Time{}
				img := render(t, u, sz)
				compare(t, name, img)
			})
		}
	}
}

// needGPU skips a screenshot test without a headless GPU context, or with
// ISOTOPE_GUI_SKIP_SCREENSHOTS=1.
func needGPU(t *testing.T) {
	t.Helper()
	if os.Getenv("ISOTOPE_GUI_SKIP_SCREENSHOTS") == "1" {
		t.Skip("ISOTOPE_GUI_SKIP_SCREENSHOTS=1")
	}
	probe, err := headless.NewWindow(8, 8)
	if err != nil {
		// CI sets ISOTOPE_GUI_REQUIRE_SCREENSHOTS, so a runner without
		// Mesa fails rather than passing with nothing compared.
		if os.Getenv("ISOTOPE_GUI_REQUIRE_SCREENSHOTS") == "1" {
			t.Fatalf("no headless GPU context: %v", err)
		}
		t.Skipf("no headless GPU context (EGL with Mesa's surfaceless platform): %v", err)
	}
	probe.Release()
}

func render(t *testing.T, u *UI, sz image.Point) *image.RGBA {
	t.Helper()
	win, err := headless.NewWindow(sz.X, sz.Y)
	if err != nil {
		t.Fatal(err)
	}
	defer win.Release()
	var r input.Router
	var ops op.Ops
	// Two frames: the first runs the focus request of the page, the
	// second draws the focus ring.
	for i := 0; i < 2; i++ {
		ops.Reset()
		gtx := layout.Context{
			Ops:         &ops,
			Metric:      unit.Metric{PxPerDp: 1, PxPerSp: 1},
			Constraints: layout.Exact(sz),
			Now:         u.now(),
			Source:      r.Source(),
		}
		u.Layout(gtx)
		r.Frame(&ops)
	}
	if err := win.Frame(&ops); err != nil {
		t.Fatal(err)
	}
	img := image.NewRGBA(image.Rectangle{Max: sz})
	if err := win.Screenshot(img); err != nil {
		t.Fatal(err)
	}
	return img
}

func compare(t *testing.T, name string, got *image.RGBA) {
	t.Helper()
	path := filepath.Join("testdata", name+".png")
	if *update {
		if err := writePNG(path, got); err != nil {
			t.Fatal(err)
		}
		return
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("%v (run go test -update to make it)", err)
	}
	defer f.Close()
	want, err := png.Decode(f)
	if err != nil {
		t.Fatal(err)
	}
	if want.Bounds() != got.Bounds() {
		t.Fatalf("size %v, golden %v", got.Bounds(), want.Bounds())
	}
	diff := 0
	b := got.Bounds()
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			r1, g1, b1, _ := got.At(x, y).RGBA()
			r2, g2, b2, _ := want.At(x, y).RGBA()
			if far(r1, r2) || far(g1, g2) || far(b1, b2) {
				diff++
			}
		}
	}
	if frac := float64(diff) / float64(b.Dx()*b.Dy()); frac > pageTol {
		out := filepath.Join(t.TempDir(), name+".png")
		_ = writePNG(out, got)
		t.Errorf("%.2f%% of the pixels differ from %s (limit %.2f%%); this run's image: %s", frac*100, path, pageTol*100, out)
	}
}

func far(a, b uint32) bool {
	a, b = a>>8, b>>8
	if a > b {
		return a-b > pixelTol
	}
	return b-a > pixelTol
}

func writePNG(path string, img image.Image) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	if err := (&png.Encoder{CompressionLevel: png.BestCompression}).Encode(f, img); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}
