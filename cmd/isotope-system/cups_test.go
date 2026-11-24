package main

import (
	"bytes"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"

	"github.com/OpenPrinting/goipp"
)

// fakeCUPS answers IPP on a Unix socket, as cupsd does on
// /run/cups/cups.sock: the queues and the default it holds, and the jobs
// it was sent.
type fakeCUPS struct {
	mu       sync.Mutex
	printers []Printer
	def      string
	jobs     []fakeJob
	hosts    []string
	// refuse answers Print-Job with this status.
	refuse goipp.Status
}

type fakeJob struct {
	path, printerURI, user, name, format string
	doc                                  []byte
}

func startFakeCUPS(t *testing.T, f *fakeCUPS) string {
	t.Helper()
	sock := filepath.Join(t.TempDir(), "cups.sock")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: f}
	go srv.Serve(l)
	t.Cleanup(func() { srv.Close() })
	return sock
}

func (f *fakeCUPS) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.hosts = append(f.hosts, r.Host)
	if r.Header.Get("Content-Type") != goipp.ContentType {
		http.Error(w, "not ipp", http.StatusBadRequest)
		return
	}
	var req goipp.Message
	if err := req.Decode(r.Body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	resp := goipp.NewResponse(goipp.DefaultVersion, goipp.StatusOk, req.RequestID)
	resp.Operation.Add(goipp.MakeAttribute("attributes-charset", goipp.TagCharset, goipp.String("utf-8")))
	resp.Operation.Add(goipp.MakeAttribute("attributes-natural-language", goipp.TagLanguage, goipp.String("en")))
	switch goipp.Op(req.Code) {
	case goipp.OpCupsGetPrinters:
		if len(f.printers) == 0 {
			resp.Code = goipp.Code(goipp.StatusErrorNotFound)
			break
		}
		resp.Groups = goipp.Groups{{Tag: goipp.TagOperationGroup, Attrs: resp.Operation}}
		for _, p := range f.printers {
			var g goipp.Attributes
			g.Add(goipp.MakeAttribute("printer-name", goipp.TagName, goipp.String(p.Name)))
			g.Add(goipp.MakeAttribute("printer-info", goipp.TagText, goipp.String(p.Info)))
			g.Add(goipp.MakeAttribute("printer-location", goipp.TagText, goipp.String(p.Location)))
			g.Add(goipp.MakeAttribute("printer-make-and-model", goipp.TagText, goipp.String(p.Model)))
			g.Add(goipp.MakeAttribute("printer-state", goipp.TagEnum, goipp.Integer(p.State)))
			g.Add(goipp.MakeAttribute("printer-state-message", goipp.TagText, goipp.String(p.StateMessage)))
			g.Add(goipp.MakeAttribute("printer-is-accepting-jobs", goipp.TagBoolean, goipp.Boolean(p.Accepting)))
			g.Add(goipp.MakeAttribute("device-uri", goipp.TagURI, goipp.String(p.DeviceURI)))
			resp.Groups = append(resp.Groups, goipp.Group{Tag: goipp.TagPrinterGroup, Attrs: g})
		}
	case goipp.OpCupsGetDefault:
		if f.def == "" {
			resp.Code = goipp.Code(goipp.StatusErrorNotFound)
			break
		}
		resp.Printer.Add(goipp.MakeAttribute("printer-name", goipp.TagName, goipp.String(f.def)))
	case goipp.OpPrintJob:
		if f.refuse != 0 {
			resp.Code = goipp.Code(f.refuse)
			resp.Operation.Add(goipp.MakeAttribute("status-message", goipp.TagText, goipp.String("The printer is not accepting jobs.")))
			break
		}
		doc, _ := io.ReadAll(r.Body)
		f.jobs = append(f.jobs, fakeJob{path: r.URL.Path, printerURI: attrString(req.Operation, "printer-uri"),
			user: attrString(req.Operation, "requesting-user-name"), name: attrString(req.Operation, "job-name"),
			format: attrString(req.Operation, "document-format"), doc: doc})
		resp.Job.Add(goipp.MakeAttribute("job-id", goipp.TagInteger, goipp.Integer(41+len(f.jobs))))
	default:
		resp.Code = goipp.Code(goipp.StatusErrorOperationNotSupported)
	}
	w.Header().Set("Content-Type", goipp.ContentType)
	b, _ := resp.EncodeBytes()
	w.Write(b)
}

func TestCUPSPrinters(t *testing.T) {
	f := &fakeCUPS{def: "Office", printers: []Printer{
		{Name: "Office", Info: "Office printer", Location: "2nd floor", Model: "HP LaserJet M404 - IPP Everywhere",
			DeviceURI: "dnssd://Office._ipps._tcp.local/", State: printerIdle, Accepting: true},
		{Name: "Hall", Model: "Brother HL-L2350DW", DeviceURI: "ipp://10.0.0.9/ipp/print",
			State: printerStopped, StateMessage: "Out of paper"},
	}}
	c := newCUPSClient(startFakeCUPS(t, f))
	got, err := c.Printers()
	if err != nil {
		t.Fatal(err)
	}
	want := []Printer{
		{Name: "Hall", Model: "Brother HL-L2350DW", DeviceURI: "ipp://10.0.0.9/ipp/print",
			State: printerStopped, StateMessage: "Out of paper"},
		{Name: "Office", Info: "Office printer", Location: "2nd floor", Model: "HP LaserJet M404 - IPP Everywhere",
			DeviceURI: "dnssd://Office._ipps._tcp.local/", State: printerIdle, Accepting: true, Default: true},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got\n%+v\nwant\n%+v", got, want)
	}
	for _, h := range f.hosts {
		if h != "localhost" {
			t.Fatalf("Host %q, CUPS wants localhost", h)
		}
	}
}

func TestCUPSNoPrinters(t *testing.T) {
	c := newCUPSClient(startFakeCUPS(t, &fakeCUPS{}))
	got, err := c.Printers()
	if err != nil || len(got) != 0 {
		t.Fatalf("got %v, %v; want none", got, err)
	}
}

func TestCUPSNotRunning(t *testing.T) {
	c := newCUPSClient(filepath.Join(t.TempDir(), "missing.sock"))
	if _, err := c.Printers(); !errors.Is(err, errNoCUPS) {
		t.Fatalf("err %v, want errNoCUPS", err)
	}
}

func TestCUPSTestPage(t *testing.T) {
	f := &fakeCUPS{}
	c := newCUPSClient(startFakeCUPS(t, f))
	c.user = "anna"
	c.testPage = filepath.Join(t.TempDir(), "testprint")
	page := []byte("#CUPS-BANNER\nTemplate default-testpage.pdf\n")
	if err := os.WriteFile(c.testPage, page, 0o644); err != nil {
		t.Fatal(err)
	}
	id, err := c.PrintTestPage("Office")
	if err != nil {
		t.Fatal(err)
	}
	if id != 42 {
		t.Fatalf("job %d", id)
	}
	j := f.jobs[0]
	want := fakeJob{path: "/printers/Office", printerURI: "ipp://localhost/printers/Office", user: "anna",
		name: "Test page", format: "application/vnd.cups-banner", doc: page}
	if !reflect.DeepEqual(j, want) {
		t.Fatalf("job %+v\nwant %+v", j, want)
	}
	if !bytes.Equal(j.doc, page) {
		t.Fatal("the document did not follow the request")
	}
	f.refuse = goipp.StatusErrorNotAcceptingJobs
	if _, err := c.PrintTestPage("Office"); err == nil || err.Error() != "The printer is not accepting jobs." {
		t.Fatalf("refused job: %v", err)
	}
	c.testPage = filepath.Join(t.TempDir(), "missing")
	if _, err := c.PrintTestPage("Office"); err == nil {
		t.Fatal("no test page file, no error")
	}
}
