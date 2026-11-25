package main

import (
	"bufio"
	"context"
	"fmt"
	"os/exec"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"
)

// privateBus starts a dbus-daemon of its own and returns its address.
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

func dial(t *testing.T, addr string) *dbus.Conn {
	t.Helper()
	c, err := dbus.Connect(addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

// fakeService is one printer Avahi knows, on one DNS-SD type.
type fakeService struct {
	name, typ, address string
	txt                []string
	gone               bool // ResolveService fails
}

// fakeAvahi is Avahi's Server and ServiceBrowser objects: each browser
// sends one ItemNew per service of its type (one of them before
// ServiceBrowserNew has even returned, as the real one may), twice when
// the printer is on IPv4 and IPv6, then AllForNow.
type fakeAvahi struct {
	conn     *dbus.Conn
	services []fakeService
	mu       sync.Mutex
	n        int
	freed    []dbus.ObjectPath
	resolved []string
}

func (a *fakeAvahi) ServiceBrowserNew(iface, proto int32, typ, domain string, flags uint32) (dbus.ObjectPath, *dbus.Error) {
	a.mu.Lock()
	a.n++
	path := dbus.ObjectPath(fmt.Sprintf("/Client1/ServiceBrowser%d", a.n))
	a.mu.Unlock()
	a.conn.Export(browserObj{a, path}, path, avahiBrowser)
	var mine []fakeService
	for _, s := range a.services {
		if s.typ == typ {
			mine = append(mine, s)
		}
	}
	emit := func(s fakeService, proto int32) {
		a.conn.Emit(path, avahiBrowser+".ItemNew", int32(2), proto, s.name, s.typ, "local", uint32(0))
	}
	if len(mine) > 0 {
		emit(mine[0], 0) // before the reply
	}
	go func() {
		time.Sleep(20 * time.Millisecond)
		for i, s := range mine {
			if i > 0 {
				emit(s, 0)
			}
			emit(s, 1)
		}
		a.conn.Emit(path, avahiBrowser+".AllForNow")
	}()
	return path, nil
}

func (a *fakeAvahi) ResolveService(iface, proto int32, name, typ, domain string, aproto int32, flags uint32) (
	int32, int32, string, string, string, string, int32, string, uint16, [][]byte, uint32, *dbus.Error) {
	a.mu.Lock()
	a.resolved = append(a.resolved, typ+" "+name)
	a.mu.Unlock()
	for _, s := range a.services {
		if s.name == name && s.typ == typ && !s.gone {
			var txt [][]byte
			for _, kv := range s.txt {
				txt = append(txt, []byte(kv))
			}
			return iface, proto, name, typ, domain, strings.ReplaceAll(name, " ", "-") + ".local", 0, s.address, 631, txt, 0, nil
		}
	}
	return 0, 0, "", "", "", "", 0, "", 0, nil, 0, &dbus.Error{Name: "org.freedesktop.Avahi.TimeoutError", Body: []any{"Timeout reached"}}
}

type browserObj struct {
	a    *fakeAvahi
	path dbus.ObjectPath
}

func (b browserObj) Free() *dbus.Error {
	b.a.mu.Lock()
	b.a.freed = append(b.a.freed, b.path)
	b.a.mu.Unlock()
	return nil
}

func startFakeAvahi(t *testing.T, services []fakeService) (*fakeAvahi, *dbus.Conn) {
	t.Helper()
	addr := privateBus(t)
	srv := dial(t, addr)
	a := &fakeAvahi{conn: srv, services: services}
	if err := srv.Export(a, "/", avahiServer); err != nil {
		t.Fatal(err)
	}
	if r, err := srv.RequestName(avahiName, 0); err != nil || r != dbus.RequestNameReplyPrimaryOwner {
		t.Fatalf("name: %v %v", r, err)
	}
	return a, dial(t, addr)
}

func TestFindAvahi(t *testing.T) {
	a, client := startFakeAvahi(t, []fakeService{
		{name: "HP LaserJet M404 [A1B2C3]", typ: "_ipp._tcp", address: "192.168.1.20",
			txt: []string{"ty=HP LaserJet Pro M404", "note=Office", "UUID=1234-ab", "pdl=application/pdf,image/urf", "URF=W8,SRGB24"}},
		{name: "HP LaserJet M404 [A1B2C3]", typ: "_ipps._tcp", address: "192.168.1.20",
			txt: []string{"ty=HP LaserJet Pro M404", "note=Office", "UUID=1234-ab", "pdl=application/pdf", "rp=ipp/print"}},
		{name: "Canon G3010 (USB)", typ: "_ipp._tcp", address: "127.0.0.1",
			txt: []string{"ty=Canon G3010 series", "pdl=image/pwg-raster,image/jpeg"}},
		{name: "Old PostScript", typ: "_ipp._tcp", address: "fe80::1",
			txt: []string{"ty=Old LaserWriter", "pdl=application/postscript"}},
		{name: "Left the network", typ: "_ipp._tcp", gone: true},
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	start := time.Now()
	got, err := findAvahi(ctx, client)
	if err != nil {
		t.Fatal(err)
	}
	if time.Since(start) > 3*time.Second {
		t.Error("did not stop at AllForNow")
	}
	want := []FoundPrinter{
		{Service: "Canon G3010 (USB)", Queue: "Canon_G3010_USB", Model: "Canon G3010 series",
			URI: "ipp://127.0.0.1:631/ipp/print", Address: "127.0.0.1", Port: 631, USB: true, Driverless: true},
		{Service: "HP LaserJet M404 [A1B2C3]", Queue: "HP_LaserJet_M404_A1B2C3", Model: "HP LaserJet Pro M404",
			Location: "Office", UUID: "1234-ab",
			URI: "ipps://192.168.1.20:631/ipp/print", Address: "192.168.1.20", Port: 631, Driverless: true},
		{Service: "Old PostScript", Queue: "Old_PostScript", Model: "Old LaserWriter",
			URI: "dnssd://Old%20PostScript._ipp._tcp.local/", Address: "fe80::1", Port: 631},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got\n%+v\nwant\n%+v", got, want)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.freed) != 2 {
		t.Errorf("freed %v, want both browsers", a.freed)
	}
	// One resolve per printer and type, not one per network and protocol;
	// a printer that does not answer is asked once more for any address.
	if len(a.resolved) != 6 {
		t.Errorf("resolved %q", a.resolved)
	}
}

func TestFindAvahiNotRunning(t *testing.T) {
	client := dial(t, privateBus(t))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := findAvahi(ctx, client); err != errNoAvahi {
		t.Fatalf("err %v, want errNoAvahi", err)
	}
}

func TestFindAvahiStopsAtTheDeadline(t *testing.T) {
	_, client := startFakeAvahi(t, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	got, err := findAvahi(ctx, client)
	if err != nil || len(got) != 0 {
		t.Fatalf("got %v, %v", got, err)
	}
}

// fakeUpdated is isotope-updated's printer methods, recording their
// arguments, to check what the window's client sends.
type fakeUpdated struct {
	mu    sync.Mutex
	calls []string
}

func (f *fakeUpdated) AddPrinter(name, uri, info, location string) *dbus.Error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, strings.Join([]string{"AddPrinter", name, uri, info, location}, "|"))
	return nil
}
func (f *fakeUpdated) RemovePrinter(name string) *dbus.Error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "RemovePrinter|"+name)
	return dbus.NewError(busInterface+".Error.Refused", []any{"a printer name has only letters"})
}
func (f *fakeUpdated) SetDefaultPrinter(name string) *dbus.Error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "SetDefaultPrinter|"+name)
	return nil
}

func TestBusDaemonPrinterMethods(t *testing.T) {
	addr := privateBus(t)
	srv := dial(t, addr)
	f := &fakeUpdated{}
	if err := srv.Export(f, busPath, busInterface); err != nil {
		t.Fatal(err)
	}
	if _, err := srv.RequestName(busName, 0); err != nil {
		t.Fatal(err)
	}
	d := &busDaemon{conn: dial(t, addr)}
	if err := d.AddPrinter("Office", "dnssd://Office._ipp._tcp.local/", "Office printer", "2nd floor"); err != nil {
		t.Fatal(err)
	}
	if err := d.SetDefaultPrinter("Office"); err != nil {
		t.Fatal(err)
	}
	err := d.RemovePrinter("a b")
	if errName(err) != "Refused" || Describe(err) != "a printer name has only letters" {
		t.Fatalf("refusal: %v (%s)", err, errName(err))
	}
	want := []string{"AddPrinter|Office|dnssd://Office._ipp._tcp.local/|Office printer|2nd floor", "SetDefaultPrinter|Office", "RemovePrinter|a b"}
	f.mu.Lock()
	defer f.mu.Unlock()
	if !reflect.DeepEqual(f.calls, want) {
		t.Fatalf("calls %q", f.calls)
	}
}
