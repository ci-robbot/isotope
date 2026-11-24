package main

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakePrinting is CUPS and Avahi for the page: the queues, what a search
// finds, and the test pages it was sent.
type fakePrinting struct {
	mu       sync.Mutex
	printers []Printer
	found    []FoundPrinter
	readErr  error
	findErr  error
	tests    []string
	finds    int
}

func (f *fakePrinting) Printers() ([]Printer, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Printer(nil), f.printers...), f.readErr
}
func (f *fakePrinting) Find(ctx context.Context) ([]FoundPrinter, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.finds++
	return append([]FoundPrinter(nil), f.found...), f.findErr
}
func (f *fakePrinting) PrintTestPage(name string) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tests = append(f.tests, name)
	return 7, nil
}
func (f *fakePrinting) add(p Printer) {
	f.mu.Lock()
	f.printers = append(f.printers, p)
	f.mu.Unlock()
}
func (f *fakePrinting) remove(name string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i, p := range f.printers {
		if p.Name == name {
			f.printers = append(f.printers[:i], f.printers[i+1:]...)
			return
		}
	}
}
func (f *fakePrinting) setDefault(name string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := range f.printers {
		f.printers[i].Default = f.printers[i].Name == name
	}
}

var (
	officeQueue = Printer{Name: "HP_LaserJet_M404_A1B2C3", Info: "HP LaserJet M404 [A1B2C3]", Location: "Office",
		Model: "HP LaserJet Pro M404 - IPP Everywhere", DeviceURI: "dnssd://HP%20LaserJet%20M404%20%5BA1B2C3%5D._ipps._tcp.local/?uuid=1234-ab",
		State: printerIdle, Accepting: true, Default: true}
	hallQueue = Printer{Name: "Hall", Model: "Brother HL-L2350DW", DeviceURI: "ipp://10.0.0.9/ipp/print",
		State: printerStopped, StateMessage: "Out of paper", Accepting: true}
	foundOffice = FoundPrinter{Service: "HP LaserJet M404 [A1B2C3]", Queue: "HP_LaserJet_M404_A1B2C3", Model: "HP LaserJet Pro M404",
		Location: "Office", UUID: "1234-AB", URI: "dnssd://HP%20LaserJet%20M404%20%5BA1B2C3%5D._ipps._tcp.local/?uuid=1234-AB", Driverless: true}
	foundCanon = FoundPrinter{Service: "Canon G3010 (USB)", Queue: "Canon_G3010_USB", Model: "Canon G3010 series",
		URI: "dnssd://Canon%20G3010%20%28USB%29._ipp._tcp.local/", USB: true, Driverless: true}
	foundOld = FoundPrinter{Service: "Old PostScript", Queue: "Old_PostScript", Model: "Old LaserWriter",
		URI: "dnssd://Old%20PostScript._ipp._tcp.local/"}
)

func TestPrinterRows(t *testing.T) {
	rows := printerRows([]Printer{officeQueue, hallQueue, {Name: "Paused", State: printerIdle}}, true)
	want := []printerRow{
		{P: officeQueue, Title: "HP LaserJet M404 [A1B2C3]", Sub: "HP LaserJet Pro M404 - IPP Everywhere · Office · Ready",
			Badge: "Default", CanTest: true, CanRemove: true},
		{P: hallQueue, Title: "Hall", Sub: "Brother HL-L2350DW · Stopped: Out of paper", CanTest: true, CanDefault: true, CanRemove: true},
		{P: Printer{Name: "Paused", State: printerIdle}, Title: "Paused", Sub: "Ready · not accepting jobs", CanDefault: true, CanRemove: true},
	}
	if !reflect.DeepEqual(rows, want) {
		t.Fatalf("got\n%+v\nwant\n%+v", rows, want)
	}
	// Not an administrator: the test page only.
	for _, r := range printerRows([]Printer{officeQueue, hallQueue}, false) {
		if r.CanDefault || r.CanRemove || !r.CanTest {
			t.Fatalf("non-admin row %+v", r)
		}
	}
}

func TestFoundRows(t *testing.T) {
	rows := foundRows([]FoundPrinter{foundCanon, foundOffice, foundOld}, []Printer{officeQueue}, true)
	got := map[string]foundRow{}
	for _, r := range rows {
		got[r.Title] = r
	}
	if r := got["Canon G3010 (USB)"]; !r.CanAdd || r.Status != "" || r.Sub != "Canon G3010 series · USB" {
		t.Errorf("Canon: %+v", r)
	}
	// The queue's dnssd URI holds the same UUID, in another case.
	if r := got["HP LaserJet M404 [A1B2C3]"]; r.CanAdd || r.Status != "Added" || r.Sub != "HP LaserJet Pro M404 · Office · On the network" {
		t.Errorf("Office: %+v", r)
	}
	if r := got["Old PostScript"]; r.CanAdd || r.Status != "Needs a driver" {
		t.Errorf("PostScript: %+v", r)
	}
	for _, r := range foundRows([]FoundPrinter{foundCanon}, nil, false) {
		if r.CanAdd {
			t.Error("a non-admin can add")
		}
	}
}

func TestUniqueQueue(t *testing.T) {
	ps := []Printer{{Name: "Office"}, {Name: "Office_2"}}
	if got := uniqueQueue("Office", ps); got != "Office_3" {
		t.Errorf("got %q", got)
	}
	if got := uniqueQueue("Hall", ps); got != "Hall" {
		t.Errorf("got %q", got)
	}
	long := strings.Repeat("a", 127)
	if got := uniqueQueue(long, []Printer{{Name: long}}); len(got) != 127 || !strings.HasSuffix(got, "_2") {
		t.Errorf("long: %q", got)
	}
}

func TestPrinterState(t *testing.T) {
	for p, want := range map[Printer]string{
		{State: printerIdle, Accepting: true}:                               "Ready",
		{State: printerIdle, Accepting: true, StateMessage: "Sleeping"}:     "Ready",
		{State: printerProcessing, Accepting: true, StateMessage: "Page 2"}: "Printing: Page 2",
		{State: printerStopped, StateMessage: "Paused"}:                     "Stopped: Paused · not accepting jobs",
	} {
		if got := printerState(p); got != want {
			t.Errorf("%+v: %q, want %q", p, got, want)
		}
	}
}

// printersUI is the window on the Printers page with fake CUPS and Avahi.
func printersUI(t *testing.T, admin bool) (*UI, *fakeDaemon, *fakePrinting) {
	t.Helper()
	u, d, _ := testUI(t, admin)
	pr := &fakePrinting{printers: []Printer{officeQueue, hallQueue}, found: []FoundPrinter{foundCanon, foundOffice, foundOld}}
	d.queues = pr
	u.hooks.Printing = pr
	u.show(PagePrinters)
	waitFor(t, func() bool {
		s := u.view(PagePrinters).(*printersPage).snap()
		return s.loaded && s.searched
	})
	return u, d, pr
}

func waitFor(t *testing.T, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatal("timed out")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestPrintersPageAddsAFoundPrinter(t *testing.T) {
	u, d, pr := printersUI(t, true)
	p := u.view(PagePrinters).(*printersPage)
	if pr.finds != 1 {
		t.Fatalf("searched %d times on open, want once", pr.finds)
	}
	p.add(foundCanon)
	waitFor(t, func() bool { return len(p.snap().printers) == 3 && p.snap().working == "" })
	want := "AddPrinter Canon_G3010_USB dnssd://Canon%20G3010%20%28USB%29._ipp._tcp.local/ Canon G3010 (USB) "
	if c := d.called(); c[len(c)-1] != want {
		t.Fatalf("called %q\nwant %q", c[len(c)-1], want)
	}
	// Once it is a queue, the found row says so.
	for _, r := range foundRows(p.snap().found, p.snap().printers, true) {
		if r.F.Service == "Canon G3010 (USB)" && r.Status != "Added" {
			t.Fatalf("after add: %+v", r)
		}
	}
	// A second printer with the same queue name gets another.
	p.add(FoundPrinter{Service: "Hall", Queue: "Hall", URI: "ipp://10.0.0.10/ipp/print", Driverless: true})
	waitFor(t, func() bool { return len(p.snap().printers) == 4 })
	if c := d.called(); !strings.HasPrefix(c[len(c)-1], "AddPrinter Hall_2 ") {
		t.Fatalf("called %q", c[len(c)-1])
	}
}

func TestPrintersPageDefaultRemoveAndTestPage(t *testing.T) {
	u, d, pr := printersUI(t, true)
	p := u.view(PagePrinters).(*printersPage)
	p.change("…", "failed", func(d Daemon) error { return d.SetDefaultPrinter("Hall") })
	waitFor(t, func() bool {
		ps := p.snap().printers
		return len(ps) == 2 && ps[1].Name == "Hall" && ps[1].Default
	})
	p.change("…", "failed", func(d Daemon) error { return d.RemovePrinter("Hall") })
	waitFor(t, func() bool { return len(p.snap().printers) == 1 })
	p.testPage(officeQueue)
	waitFor(t, func() bool { return u.question() != nil })
	if q := u.question(); q.title != "Test page sent" || !strings.Contains(q.body, "Job 7") {
		t.Fatalf("told %q: %q", q.title, q.body)
	}
	if !reflect.DeepEqual(pr.tests, []string{officeQueue.Name}) {
		t.Fatalf("test pages %q", pr.tests)
	}
	c := d.called()
	if !reflect.DeepEqual(c[len(c)-2:], []string{"SetDefaultPrinter Hall", "RemovePrinter Hall"}) {
		t.Fatalf("called %q", c)
	}
}

func TestPrintersPageNonAdmin(t *testing.T) {
	u, d, _ := printersUI(t, false)
	p := u.view(PagePrinters).(*printersPage)
	p.add(foundCanon)
	p.change("…", "failed", func(d Daemon) error { return d.RemovePrinter("Hall") })
	time.Sleep(20 * time.Millisecond)
	for _, c := range d.called() {
		if strings.Contains(c, "Printer") {
			t.Fatalf("a non-admin page called %q", c)
		}
	}
	// It still draws, with the note.
	u.Layout(gtxOf(1100, 720))
}

func TestPrintersPageErrors(t *testing.T) {
	u, d, _ := testUI(t, true)
	pr := &fakePrinting{readErr: errNoCUPS, findErr: errNoAvahi}
	u.hooks.Printing = pr
	u.show(PagePrinters)
	p := u.view(PagePrinters).(*printersPage)
	waitFor(t, func() bool { s := p.snap(); return s.loaded && s.searched })
	s := p.snap()
	if s.loadErr != errNoCUPS.Error() || s.findErr != errNoAvahi.Error() {
		t.Fatalf("errors %q %q", s.loadErr, s.findErr)
	}
	u.Layout(gtxOf(1100, 720))
	// The daemon's refusal is told.
	d.err = map[string]error{"AddPrinter": errors.New("Unable to connect to printer")}
	d.queues = pr
	p.add(foundCanon)
	waitFor(t, func() bool { return u.question() != nil })
	if q := u.question(); !strings.Contains(q.body, "Unable to connect") {
		t.Fatalf("told %q", q.body)
	}
	// No printing on the image: the page says so and does nothing.
	u2, _, _ := testUI(t, true)
	u2.show(PagePrinters)
	u2.Layout(gtxOf(1100, 720))
}

func TestPrintersPageButtonsWhileWorking(t *testing.T) {
	u, _, _ := printersUI(t, true)
	p := u.view(PagePrinters).(*printersPage)
	u.mu.Lock()
	p.working = "Adding…"
	u.mu.Unlock()
	u.Layout(gtxOf(1100, 720))
}

// cups-browsed (CreateIPPPrinterQueues LocalOnly) makes the queue of a
// USB printer behind ipp-usb itself, with an address URI on loopback.
var usbAutoQueue = Printer{Name: "Canon_G3010_series_USB", Model: "Canon G3010 series",
	DeviceURI: "ipp://127.0.0.1:60000/ipp/print", State: printerIdle, Accepting: true}

func TestAutomaticQueue(t *testing.T) {
	for uri, want := range map[string]bool{
		"ipp://127.0.0.1:60000/ipp/print":            true,
		"ipp://[::1]:60000/ipp/print":                true,
		"ipp://localhost:60000/ipp/print":            true,
		"implicitclass://Office/":                    true,
		"ipp://192.168.1.20/ipp/print":               false,
		"dnssd://Office._ipps._tcp.local/?uuid=1234": false,
		"ipps://printer.local:631/ipp/print":         false,
	} {
		if got := (Printer{DeviceURI: uri}).Automatic(); got != want {
			t.Errorf("%s: automatic %v, want %v", uri, got, want)
		}
	}
	// An automatic queue cannot be removed (cups-browsed makes it again
	// while the printer is plugged in); it can be the default and print.
	r := printerRows([]Printer{usbAutoQueue}, true)[0]
	if r.CanRemove || !r.CanDefault || !r.CanTest {
		t.Fatalf("automatic row %+v", r)
	}
	if !strings.Contains(r.Sub, "Set up automatically while it is plugged in") {
		t.Fatalf("sub %q", r.Sub)
	}
}

// A found printer matches a queue by address and port, whatever names the
// queue got, so the page never offers to add a second one.
func TestFoundPrinterMatchesQueueByAddress(t *testing.T) {
	usb := foundCanon
	usb.Address, usb.Port = "127.0.0.1", 60000
	rows := foundRows([]FoundPrinter{usb}, []Printer{usbAutoQueue}, true)
	if rows[0].CanAdd || rows[0].Status != "Added" {
		t.Fatalf("USB printer with a cups-browsed queue: %+v", rows[0])
	}
	// Another port on the same host is another printer.
	usb.Port = 60001
	if rows := foundRows([]FoundPrinter{usb}, []Printer{usbAutoQueue}, true); !rows[0].CanAdd {
		t.Fatalf("other port: %+v", rows[0])
	}
	// A network printer added with an address URI by hand.
	net := FoundPrinter{Service: "Brother HL", Queue: "Brother_HL", URI: "dnssd://Brother%20HL._ipp._tcp.local/",
		Address: "192.168.1.30", Port: 631, Driverless: true}
	byHand := Printer{Name: "hall", DeviceURI: "ipp://192.168.1.30/ipp/print"}
	if rows := foundRows([]FoundPrinter{net}, []Printer{byHand}, true); rows[0].CanAdd {
		t.Fatalf("default port: %+v", rows[0])
	}
}
