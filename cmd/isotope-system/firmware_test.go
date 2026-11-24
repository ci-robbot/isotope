package main

import (
	"context"
	"errors"
	"image"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"

	"github.com/kairos-community/isotope/pkg/fwupd"
)

// fakeFirmware is fwupd for the page: a device list, the updates, and an
// Install that records the device and, for a capsule, marks the device
// NeedsReboot as fwupd does.
type fakeFirmware struct {
	mu       sync.Mutex
	devices  []fwupd.Device
	updates  []fwupd.Update
	err      error
	failWith error
	reboot   bool // Install stages the update for a restart
	calls    []string
}

func (f *fakeFirmware) Devices() ([]fwupd.Device, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]fwupd.Device(nil), f.devices...), f.err
}

func (f *fakeFirmware) Updates() ([]fwupd.Update, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]fwupd.Update(nil), f.updates...), f.err
}

func (f *fakeFirmware) Install(_ context.Context, up fwupd.Update, progress func(string)) error {
	progress("Writing… 50%")
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "Install "+up.Device.ID+" "+up.Release.Version)
	if f.failWith != nil {
		return f.failWith
	}
	var left []fwupd.Update
	for _, u := range f.updates {
		if u.Device.ID != up.Device.ID {
			left = append(left, u)
		}
	}
	f.updates = left
	for i := range f.devices {
		if f.devices[i].ID == up.Device.ID {
			if f.reboot {
				f.devices[i].Flags |= fwupd.FlagNeedsReboot
			} else {
				f.devices[i].Version = up.Release.Version
			}
		}
	}
	return nil
}

func (f *fakeFirmware) installs() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

var (
	fwSSD  = fwupd.Device{ID: "ssd", Name: "Samsung SSD 990 PRO", Vendor: "Samsung", Version: "4B2QJXD7", Flags: fwupd.FlagUpdatable | fwupd.FlagInternal}
	fwBIOS = fwupd.Device{ID: "bios", Name: "System Firmware", Vendor: "LENOVO", Version: "0.1.42", Flags: fwupd.FlagUpdatable | fwupd.FlagInternal}
	fwDock = fwupd.Device{ID: "dock", Name: "ThinkPad Dock", Vendor: "Lenovo", Version: "1.0.3", Flags: fwupd.FlagUpdatable}
	fwCPU  = fwupd.Device{ID: "cpu", Name: "Intel Core i7", Vendor: "Intel", Version: "0x2c"}
)

func fwFake() *fakeFirmware {
	return &fakeFirmware{
		devices: []fwupd.Device{fwSSD, fwBIOS, fwDock, fwCPU},
		updates: []fwupd.Update{
			{Device: fwBIOS, Release: fwupd.Release{Version: "0.1.45", Summary: "Lenovo ThinkPad T14 System Update"}},
			{Device: fwSSD, Release: fwupd.Release{Version: "4B2QJXD8", Summary: "Firmware for Samsung NVMe SSDs"}},
		},
	}
}

func firmwareUI(t *testing.T, admin bool, f Firmware) (*UI, *updatesPage, *fakeDaemon) {
	t.Helper()
	u, d, _ := testUI(t, admin)
	u.hooks.Firmware = f
	u.show(PageUpdates)
	p := u.view(PageUpdates).(*updatesPage)
	eventually(t, "the firmware read", func() bool { u.mu.Lock(); defer u.mu.Unlock(); return p.firm.in.Loaded })
	return u, p, d
}

func TestFirmwareViewStates(t *testing.T) {
	ups := fwFake().updates
	devs := []fwupd.Device{fwSSD, fwBIOS, fwDock, fwCPU}
	staged := fwBIOS
	staged.Flags |= fwupd.FlagNeedsReboot
	failed := fwDock
	failed.UpdateState, failed.UpdateError = fwupd.StateFailed, "the device went away"
	for _, c := range []struct {
		name    string
		in      firmwareInput
		rows    int
		want    []string
		note    string
		restart bool
		enabled int
	}{
		{"loading", firmwareInput{Admin: true}, 0, nil, "Looking for devices", false, 0},
		{"no fwupd", firmwareInput{Admin: true, Loaded: true, Missing: true}, 0, nil, "not available on this system", false, 0},
		{"error", firmwareInput{Admin: true, Loaded: true, Err: "Could not read"}, 0, nil, "", false, 0},
		{"nothing updatable", firmwareInput{Admin: true, Loaded: true, Devices: []fwupd.Device{fwCPU}}, 0, nil, "No device in this computer", false, 0},
		{"updates", firmwareInput{Admin: true, Loaded: true, Devices: devs, Updates: ups}, 3,
			[]string{"Version 0.1.45 is available: Lenovo ThinkPad T14 System Update", "Version 4B2QJXD8 is available", "Lenovo · Version 1.0.3, up to date."}, "", false, 2},
		{"not an admin", firmwareInput{Loaded: true, Devices: devs, Updates: ups}, 3, []string{"is available"}, "Only an administrator", false, 0},
		{"one running", firmwareInput{Admin: true, Loaded: true, Devices: devs, Updates: ups, Working: "ssd", Line: "Writing… 10%"}, 3, []string{"Writing… 10%"}, "", false, 0},
		{"staged", firmwareInput{Admin: true, Loaded: true, Devices: []fwupd.Device{staged}, Updates: ups[:1]}, 1, []string{"restart to finish it"}, "", true, 0},
		{"failed before", firmwareInput{Admin: true, Loaded: true, Devices: []fwupd.Device{failed}}, 1, []string{"The last update failed: the device went away"}, "", false, 0},
	} {
		v := firmwareViewOf(c.in)
		if len(v.Rows) != c.rows {
			t.Errorf("%s: %d rows, want %d: %+v", c.name, len(v.Rows), c.rows, v.Rows)
		}
		var text []string
		enabled := 0
		for _, r := range v.Rows {
			text = append(text, r.Sub, r.Progress)
			if r.Enabled {
				enabled++
			}
		}
		all := strings.Join(text, "|")
		for _, w := range c.want {
			if !strings.Contains(all, w) {
				t.Errorf("%s: %q is not in %q", c.name, w, all)
			}
		}
		if c.note != "" && !strings.Contains(v.Note, c.note) || c.note == "" && v.Note != "" {
			t.Errorf("%s: note %q, want %q", c.name, v.Note, c.note)
		}
		if v.Restart != c.restart || enabled != c.enabled {
			t.Errorf("%s: restart %v enabled %d, want %v %d", c.name, v.Restart, enabled, c.restart, c.enabled)
		}
		if c.name == "error" && v.Err == "" {
			t.Errorf("error: no error shown")
		}
	}
}

func TestUpdatesPageUpdatesFirmware(t *testing.T) {
	f := fwFake()
	u, p, _ := firmwareUI(t, true, f)
	for _, sz := range []image.Point{{1100, 720}, {700, 500}} {
		if dims := u.Layout(gtxOf(sz.X, sz.Y)); dims.Size != sz {
			t.Errorf("at %v: %v", sz, dims.Size)
		}
	}
	u.mu.Lock()
	b := p.firm.buttons["ssd"]
	u.mu.Unlock()
	if b == nil {
		t.Fatal("no Update button for the SSD")
	}
	b.Click()
	p.Update(gtxOf(1100, 720))
	u.pick(waitQuestion(t, u, "Update Samsung SSD 990 PRO?"), 1)
	eventually(t, "Install", func() bool { return contains(f.installs(), "Install ssd 4B2QJXD8") })
	eventually(t, "the new list", func() bool {
		u.mu.Lock()
		defer u.mu.Unlock()
		return p.firm.in.Working == "" && len(p.firm.in.Updates) == 1
	})
	if q := u.question(); q != nil {
		t.Fatalf("a device that needs no restart asked %q", q.title)
	}
}

func TestUpdatesPageOffersTheRestartAfterACapsule(t *testing.T) {
	f := fwFake()
	f.reboot = true
	u, p, d := firmwareUI(t, true, f)
	p.runFirmware(f.updates[0])
	u.pick(waitQuestion(t, u, "Restart to finish the firmware update?"), 1)
	eventually(t, "Reboot", func() bool { return contains(d.called(), "Reboot") })
	eventually(t, "the staged device", func() bool {
		u.mu.Lock()
		defer u.mu.Unlock()
		return firmwareViewOf(p.firmwareInputLocked()).Restart
	})
	u.Layout(gtxOf(1100, 720))
	p.firm.restart.Click()
	p.Update(gtxOf(1100, 720))
	waitQuestion(t, u, "Restart to finish the firmware update?")
}

func TestUpdatesPageSaysWhyAFirmwareUpdateFailed(t *testing.T) {
	f := fwFake()
	f.failWith = dbus.NewError("org.freedesktop.fwupd.AuthFailed", []any{"Not authorized"})
	u, p, _ := firmwareUI(t, true, f)
	p.runFirmware(f.updates[1])
	q := waitQuestion(t, u, "Could not update Samsung SSD 990 PRO")
	if !strings.Contains(q.body, "administrator password") || !strings.Contains(q.body, "fwupdmgr") {
		t.Fatalf("body %q", q.body)
	}
	if got := firmwareErrorText(dbus.NewError("org.freedesktop.fwupd.Internal", []any{"device went away"})); got != "device went away" {
		t.Fatalf("%q", got)
	}
}

func TestUpdatesPageFirmwareNeedsAnAdmin(t *testing.T) {
	f := fwFake()
	u, p, _ := firmwareUI(t, false, f)
	u.Layout(gtxOf(1100, 720))
	u.mu.Lock()
	b := p.firm.buttons["ssd"]
	u.mu.Unlock()
	b.Click()
	p.Update(gtxOf(1100, 720))
	p.updateFirmware("ssd")
	time.Sleep(20 * time.Millisecond)
	if q := u.question(); q != nil || len(f.installs()) != 0 {
		t.Fatalf("a non-admin's click got through: %v %v", q, f.installs())
	}
}

// firmwareMissingErr is the bus's answer when fwupd is not installed.
var firmwareMissingErr = dbus.Error{Name: "org.freedesktop.DBus.Error.ServiceUnknown", Body: []any{"no fwupd"}}

func TestUpdatesPageWithoutFwupd(t *testing.T) {
	f := &fakeFirmware{err: firmwareMissingErr}
	u, p, _ := firmwareUI(t, true, f)
	u.mu.Lock()
	in := p.firmwareInputLocked()
	u.mu.Unlock()
	if !in.Missing {
		t.Fatalf("%+v", in)
	}
	f.err = errors.New("boom")
	p.loadFirmware()
	eventually(t, "the error", func() bool {
		u.mu.Lock()
		defer u.mu.Unlock()
		return strings.Contains(p.firm.in.Err, "boom")
	})
	u.Layout(gtxOf(700, 500))
}

func TestFirmwareConfirmWarnsAboutSecureBootForTheSystemFirmware(t *testing.T) {
	bios := fwupd.Update{Device: fwupd.Device{Name: "System Firmware", Plugin: "uefi_capsule"}, Release: fwupd.Release{Version: "1.2"}}
	if s := firmwareConfirmText(bios); !strings.Contains(s, "Some firmware updates turn Secure Boot back on. If the computer then does not start Isotope, turn Secure Boot off in the firmware setup.") {
		t.Fatalf("%q", s)
	}
	ssd := fwupd.Update{Device: fwupd.Device{Name: "SSD", Plugin: "nvme"}, Release: fwupd.Release{Version: "2"}}
	if s := firmwareConfirmText(ssd); strings.Contains(s, "Secure Boot") {
		t.Fatalf("an SSD update warns about Secure Boot: %q", s)
	}
}

// While this window has fwupd write firmware, every restart waits: the
// buttons are off, and a restart asked anyway says why and never reaches
// the daemon.
func TestRestartWaitsForAFirmwareWrite(t *testing.T) {
	f := fwFake()
	f.devices[1].Flags |= fwupd.FlagNeedsReboot
	u, p, d := firmwareUI(t, true, f)
	u.setStatus(Status{State: StateRebootPending, Pending: pendingInstalled, Installed: "v1.0.0", Staged: "v1.1.0"})
	u.mu.Lock()
	p.firm.in.Working = "ssd"
	u.firmwareWriting = true
	v := firmwareViewOf(p.firmwareInputLocked())
	u.mu.Unlock()
	if !v.Writing {
		t.Fatal("the view does not say a write runs")
	}
	_, trailing := p.Footer()
	for _, a := range trailing {
		if a.Label == "Restart now" && !a.Disabled {
			t.Fatal("Restart now is on during a firmware write")
		}
	}
	u.reboot()
	q := waitQuestion(t, u, "Cannot restart")
	if !strings.Contains(q.body, "A firmware update is being written. Restarting now could damage the device.") {
		t.Fatalf("%q", q.body)
	}
	time.Sleep(20 * time.Millisecond)
	if contains(d.called(), "Reboot") {
		t.Fatal("the restart reached the daemon")
	}
	u.mu.Lock()
	u.firmwareWriting = false
	u.mu.Unlock()
	u.pick(q, 0)
	u.reboot()
	eventually(t, "Reboot", func() bool { return contains(d.called(), "Reboot") })
}
