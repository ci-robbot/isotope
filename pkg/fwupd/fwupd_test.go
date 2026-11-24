package fwupd

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"

	"github.com/godbus/dbus/v5"
	"github.com/godbus/dbus/v5/introspect"
	"github.com/godbus/dbus/v5/prop"
)

// privateBus starts a dbus-daemon of its own and returns its address; the
// test skips without one (the golang:alpine build stages have none).
func privateBus(t *testing.T) string {
	t.Helper()
	bin, err := exec.LookPath("dbus-daemon")
	if err != nil {
		t.Skip("dbus-daemon not installed")
	}
	cmd := exec.Command(bin, "--session", "--nofork", "--print-address")
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cmd.Process.Kill(); cmd.Wait() })
	addr, err := bufio.NewReader(out).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(addr)
}

func connect(t *testing.T, addr string) *dbus.Conn {
	t.Helper()
	c, err := dbus.Connect(addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

// fakeFwupd is org.freedesktop.fwupd: devices, upgrades per device, and an
// Install that reads the descriptor it is given.
type fakeFwupd struct {
	mu        sync.Mutex
	devices   []map[string]dbus.Variant
	upgrades  map[string][]map[string]dbus.Variant
	installed map[string]string // device id -> the bytes it was given
	refuse    *dbus.Error
}

func (f *fakeFwupd) GetDevices() ([]map[string]dbus.Variant, *dbus.Error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.devices, nil
}

func (f *fakeFwupd) GetUpgrades(id string) ([]map[string]dbus.Variant, *dbus.Error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r, ok := f.upgrades[id]; ok {
		return r, nil
	}
	return nil, dbus.NewError("org.freedesktop.fwupd.NothingToDo", []any{"No upgrades for " + id})
}

func (f *fakeFwupd) Install(id string, fd dbus.UnixFD, opts map[string]dbus.Variant) *dbus.Error {
	file := os.NewFile(uintptr(fd), "cab")
	defer file.Close()
	b, err := io.ReadAll(file)
	if err != nil {
		return dbus.MakeFailedError(err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.refuse != nil {
		return f.refuse
	}
	if f.installed == nil {
		f.installed = map[string]string{}
	}
	f.installed[id] = string(b)
	return nil
}

func v(x any) dbus.Variant { return dbus.MakeVariant(x) }

// serve exports f on a private bus under the name fwupd uses and returns a
// client for it.
func serve(t *testing.T, f *fakeFwupd) *Client {
	t.Helper()
	addr := privateBus(t)
	srv := connect(t, addr)
	if err := srv.Export(f, ObjPath, Interface); err != nil {
		t.Fatal(err)
	}
	props, err := prop.Export(srv, ObjPath, prop.Map{Interface: {
		"Status":     {Value: uint32(5), Writable: false, Emit: prop.EmitTrue},
		"Percentage": {Value: uint32(42), Writable: false, Emit: prop.EmitTrue},
	}})
	if err != nil {
		t.Fatal(err)
	}
	node := &introspect.Node{Name: string(ObjPath), Interfaces: []introspect.Interface{
		introspect.IntrospectData, prop.IntrospectData,
		{Name: Interface, Methods: introspect.Methods(f), Properties: props.Introspection(Interface)},
	}}
	if err := srv.Export(introspect.NewIntrospectable(node), ObjPath, "org.freedesktop.DBus.Introspectable"); err != nil {
		t.Fatal(err)
	}
	if r, err := srv.RequestName(BusName, dbus.NameFlagDoNotQueue); err != nil || r != dbus.RequestNameReplyPrimaryOwner {
		t.Fatalf("taking %s: %v %v", BusName, r, err)
	}
	return New(connect(t, addr))
}

func dev(id, name, version string, flags uint64) map[string]dbus.Variant {
	return map[string]dbus.Variant{"DeviceId": v(id), "Name": v(name), "Vendor": v("ACME"), "Version": v(version), "Flags": v(flags), "Plugin": v("nvme")}
}

func rel(version string, sums string, locs ...string) map[string]dbus.Variant {
	return map[string]dbus.Variant{"Version": v(version), "Summary": v("Firmware for the " + version), "Checksum": v(sums), "Locations": v(locs), "Size": v(uint64(5)), "Flags": v(ReleaseTrustedPayload | ReleaseIsUpgrade)}
}

func TestUpdatesListsUpdatableDevicesWithTheNewestRelease(t *testing.T) {
	f := &fakeFwupd{
		devices: []map[string]dbus.Variant{
			dev("ssd", "Samsung SSD", "1.0", FlagUpdatable|FlagInternal),
			dev("cpu", "Intel CPU", "0x10", FlagInternal), // not updatable
			dev("dock", "Dock", "2.0", FlagUpdatable),     // no upgrade: NothingToDo
			dev("bios", "System Firmware", "1.2", FlagUpdatable|FlagInternal|FlagNeedsReboot),
		},
		upgrades: map[string][]map[string]dbus.Variant{
			"ssd":  {rel("1.2", "aa,"+strings.Repeat("b", 64), "https://fwupd.org/downloads/x.cab"), rel("1.1", "cc")},
			"bios": {rel("1.3", strings.Repeat("c", 40))},
		},
	}
	c := serve(t, f)
	devs, err := c.Devices()
	if err != nil || len(devs) != 4 {
		t.Fatalf("devices %v %v", devs, err)
	}
	if !devs[3].NeedsReboot() || devs[0].NeedsReboot() || !devs[0].Updatable() || devs[1].Updatable() {
		t.Fatalf("flags read wrong: %+v", devs)
	}
	ups, err := c.Updates()
	if err != nil {
		t.Fatal(err)
	}
	if len(ups) != 2 || ups[0].Device.ID != "ssd" || ups[1].Device.ID != "bios" {
		t.Fatalf("updates %+v", ups)
	}
	r := ups[0].Release
	if r.Version != "1.2" || r.Summary != "Firmware for the 1.2" || len(r.Checksums) != 2 || r.Locations[0] != "https://fwupd.org/downloads/x.cab" || r.Size != 5 {
		t.Fatalf("release %+v", r)
	}
	if rels, err := c.Upgrades("dock"); err != nil || rels != nil {
		t.Fatalf("NothingToDo is no upgrade, not an error: %v %v", rels, err)
	}
}

func TestInstallPassesTheFileDescriptor(t *testing.T) {
	f := &fakeFwupd{}
	c := serve(t, f)
	tmp, err := os.CreateTemp(t.TempDir(), "cab")
	if err != nil {
		t.Fatal(err)
	}
	tmp.WriteString("CABINET")
	tmp.Seek(0, io.SeekStart)
	if err := c.Install(context.Background(), "ssd", tmp); err != nil {
		t.Fatal(err)
	}
	if f.installed["ssd"] != "CABINET" {
		t.Fatalf("fwupd got %q", f.installed["ssd"])
	}
	f.refuse = dbus.NewError("org.freedesktop.fwupd.AuthFailed", []any{"Not authorized"})
	tmp.Seek(0, io.SeekStart)
	err = c.Install(context.Background(), "ssd", tmp)
	if err == nil || Message(err) != "Not authorized" {
		t.Fatalf("refusal: %v", err)
	}
}

func TestProgressReadsTheProperties(t *testing.T) {
	c := serve(t, &fakeFwupd{})
	p, err := c.Progress()
	if err != nil {
		t.Fatal(err)
	}
	if p.Status != 5 || p.Percent != 42 || p.Text() != "Writing… 42%" {
		t.Fatalf("%+v %q", p, p.Text())
	}
	if (Progress{Status: 1}).Text() != "" {
		t.Fatal("idle has no text")
	}
}

func TestMissingDaemon(t *testing.T) {
	addr := privateBus(t)
	c := New(connect(t, addr))
	_, err := c.Devices()
	if err == nil || !Missing(err) {
		t.Fatalf("want a missing service, got %v", err)
	}
	if Missing(errors.New("other")) || NoUpdate(errors.New("other")) {
		t.Fatal("a plain error is neither")
	}
}

func cabServer(t *testing.T, body string) (*httptest.Server, string) {
	t.Helper()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	s := sha256.Sum256([]byte(body))
	return srv, hex.EncodeToString(s[:])
}

func TestDownloadChecksTheChecksumAndUnlinks(t *testing.T) {
	srv, sum := cabServer(t, "CABINET")
	dir := t.TempDir()
	r := Release{Locations: []string{"http://insecure/x.cab", srv.URL + "/x.cab"}, Checksums: []string{strings.Repeat("0", 40), sum}, Size: 7}
	f, err := Download(context.Background(), srv.Client(), r, dir)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	b, _ := io.ReadAll(f)
	if string(b) != "CABINET" {
		t.Fatalf("%q", b)
	}
	if left, _ := os.ReadDir(dir); len(left) != 0 {
		t.Fatalf("left behind: %v", left)
	}

	r.Checksums = []string{strings.Repeat("a", 64)}
	if _, err := Download(context.Background(), srv.Client(), r, dir); err == nil || !strings.Contains(err.Error(), "checksum") {
		t.Fatalf("a wrong checksum: %v", err)
	}
	r.Checksums, r.Size = []string{sum}, 9
	if _, err := Download(context.Background(), srv.Client(), r, dir); err == nil || !strings.Contains(err.Error(), "bytes") {
		t.Fatalf("a wrong size: %v", err)
	}
	if _, err := Download(context.Background(), srv.Client(), Release{Locations: []string{"http://x/y.cab"}, Checksums: []string{sum}}, dir); err == nil || !strings.Contains(err.Error(), "HTTPS") {
		t.Fatalf("plain HTTP: %v", err)
	}
	if _, err := Download(context.Background(), srv.Client(), Release{Locations: []string{srv.URL}}, dir); err == nil || !strings.Contains(err.Error(), "checksum") {
		t.Fatalf("no checksum: %v", err)
	}
	if _, err := Download(context.Background(), srv.Client(), Release{Locations: []string{srv.URL}, Checksums: []string{sum}, Size: MaxSize + 1}, dir); err == nil {
		t.Fatal("an oversized release was fetched")
	}
}

func TestDownloadPrefersSHA256(t *testing.T) {
	want, h := pickChecksum([]string{strings.Repeat("A", 40), strings.Repeat("B", 64)})
	if want != strings.Repeat("b", 64) || h.Size() != 32 {
		t.Fatalf("%s %d", want, h.Size())
	}
	if _, h := pickChecksum([]string{"zz"}); h != nil {
		t.Fatal("not a checksum")
	}
}
