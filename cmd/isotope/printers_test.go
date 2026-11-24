package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"
)

// fakeLpadmin stands in for lpadmin: it records each argv and answers with
// out and err, or hangs until the daemon gives up.
type fakeLpadmin struct {
	mu   sync.Mutex
	ran  [][]string
	out  string
	err  error
	hang bool
}

func lpadminEnv(t *testing.T) *fakeLpadmin {
	t.Helper()
	f := &fakeLpadmin{}
	orun, ot := printersRun, printersTimeout
	printersRun = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		f.mu.Lock()
		f.ran = append(f.ran, append([]string{name}, args...))
		hang, out, err := f.hang, f.out, f.err
		f.mu.Unlock()
		if hang {
			<-ctx.Done()
			return nil, ctx.Err()
		}
		return []byte(out), err
	}
	t.Cleanup(func() { printersRun, printersTimeout = orun, ot })
	return f
}

func (f *fakeLpadmin) runs() [][]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([][]string(nil), f.ran...)
}

func TestAddPrinterRunsLpadmin(t *testing.T) {
	f := lpadminEnv(t)
	if err := addPrinter("Office", "dnssd://Office._ipps._tcp.local/", "Office printer", "Second floor"); err != nil {
		t.Fatal(err)
	}
	if err := addPrinter("Hall", "ipp://192.168.1.9/ipp/print", "", ""); err != nil {
		t.Fatal(err)
	}
	want := [][]string{
		{"lpadmin", "-p", "Office", "-E", "-v", "dnssd://Office._ipps._tcp.local/", "-m", "everywhere",
			"-o", "printer-is-shared=false", "-D", "Office printer", "-L", "Second floor"},
		{"lpadmin", "-p", "Hall", "-E", "-v", "ipp://192.168.1.9/ipp/print", "-m", "everywhere", "-o", "printer-is-shared=false"},
	}
	if got := f.runs(); !reflect.DeepEqual(got, want) {
		t.Fatalf("ran\n%q\nwant\n%q", got, want)
	}
}

func TestRemoveAndDefaultRunLpadmin(t *testing.T) {
	f := lpadminEnv(t)
	if err := removePrinter("Office"); err != nil {
		t.Fatal(err)
	}
	if err := setDefaultPrinter("Hall"); err != nil {
		t.Fatal(err)
	}
	want := [][]string{{"lpadmin", "-x", "Office"}, {"lpadmin", "-d", "Hall"}}
	if got := f.runs(); !reflect.DeepEqual(got, want) {
		t.Fatalf("ran %q, want %q", got, want)
	}
}

func TestPrinterInputIsRefused(t *testing.T) {
	f := lpadminEnv(t)
	for _, err := range []error{
		addPrinter("-x", "ipp://h/", "", ""),
		addPrinter("Office", "usb://HP/LaserJet", "", ""),
		addPrinter("Office", "ipp://h/", "two\nlines", ""),
		addPrinter("Office", "ipp://h/", "", strings.Repeat("x", 200)),
		removePrinter("a b"),
		setDefaultPrinter(""),
	} {
		if !errors.Is(err, errPrinterInput) {
			t.Errorf("err %v, want a refusal", err)
		}
		if name := busError(err).Name; name != busInterface+".Error.Refused" {
			t.Errorf("%v is %s on the bus, want Refused", err, name)
		}
	}
	if r := f.runs(); len(r) != 0 {
		t.Fatalf("lpadmin ran for refused input: %q", r)
	}
}

func TestLpadminErrorsAreItsOwnSentence(t *testing.T) {
	f := lpadminEnv(t)
	f.out, f.err = "lpadmin: The printer or class does not exist.\n", errFakeExit
	err := removePrinter("Gone")
	if err == nil || err.Error() != "The printer or class does not exist." {
		t.Fatalf("err %v", err)
	}
	if name := busError(err).Name; name != busInterface+".Error.Failed" {
		t.Fatalf("bus error %s, want Failed", name)
	}
	f.out, f.err = "", &exec.Error{Name: "lpadmin", Err: exec.ErrNotFound}
	if err := removePrinter("Gone"); err == nil || !strings.Contains(err.Error(), "is CUPS installed") {
		t.Fatalf("missing lpadmin: %v", err)
	}
}

func TestLpadminTimesOut(t *testing.T) {
	f := lpadminEnv(t)
	f.hang = true
	printersTimeout = 50 * time.Millisecond
	start := time.Now()
	err := addPrinter("Sleepy", "ipp://10.0.0.2/ipp/print", "", "")
	if err == nil || !strings.Contains(err.Error(), "did not answer") {
		t.Fatalf("err %v", err)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatal("the timeout did not stop the call")
	}
}

func TestPrintersOverTheBus(t *testing.T) {
	f := lpadminEnv(t)
	auth := &fakeAuth{allow: true}
	sv := startService(t, localeDaemon(t), auth)
	for _, c := range []*dbus.Call{
		sv.call("AddPrinter", "Office", "dnssd://Office._ipp._tcp.local/", "", ""),
		sv.call("SetDefaultPrinter", "Office"),
		sv.call("RemovePrinter", "Office"),
	} {
		if c.Err != nil {
			t.Fatal(c.Err)
		}
	}
	if got := len(f.runs()); got != 3 {
		t.Fatalf("lpadmin ran %d times, want 3", got)
	}
	me := sv.client.Names()[0]
	for _, q := range auth.questions() {
		if q != me+" "+actionPrinters {
			t.Fatalf("asked %q, want the printers action", q)
		}
	}
	if got := errName(sv.call("AddPrinter", "Office", "socket://10.0.0.2", "", "").Err); got != busInterface+".Error.Refused" {
		t.Fatalf("bad URI over the bus: %s", got)
	}
}

func TestPrintersRefusedWithoutAction(t *testing.T) {
	f := lpadminEnv(t)
	sv := startService(t, localeDaemon(t), &fakeAuth{allow: false})
	for _, c := range []*dbus.Call{
		sv.call("AddPrinter", "Office", "ipp://h/", "", ""),
		sv.call("SetDefaultPrinter", "Office"),
		sv.call("RemovePrinter", "Office"),
	} {
		if got := errName(c.Err); got != busInterface+".Error.NotAuthorized" {
			t.Fatalf("got %s (%v), want NotAuthorized", got, c.Err)
		}
	}
	if r := f.runs(); len(r) != 0 {
		t.Fatalf("lpadmin ran: %q", r)
	}
}

func TestPrintersRefuseFlatpakCaller(t *testing.T) {
	f := lpadminEnv(t)
	fakeProc(t, uint32(os.Getpid()), true)
	auth := &fakeAuth{allow: true}
	sv := startService(t, localeDaemon(t), auth)
	if got := errName(sv.call("RemovePrinter", "Office").Err); got != busInterface+".Error.NotAuthorized" {
		t.Fatalf("from a sandbox: %s", got)
	}
	if len(auth.questions()) != 0 || len(f.runs()) != 0 {
		t.Fatal("a sandboxed caller reached polkit or lpadmin")
	}
}

func TestIntrospectionListsPrinterMethods(t *testing.T) {
	for _, m := range []string{"AddPrinter", "RemovePrinter", "SetDefaultPrinter"} {
		if !strings.Contains(busIntrospection, `<method name="`+m+`">`) {
			t.Errorf("introspection lacks %s", m)
		}
	}
}
