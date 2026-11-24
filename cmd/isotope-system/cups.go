package main

// The Printers page's reads, as the user: the CUPS queues over IPP on CUPS's
// own socket (CUPS-Get-Printers, CUPS-Get-Default), CUPS's test page
// (Print-Job), and the printers Avahi finds on the network (avahi.go).
// Adding, removing and choosing the default go through isotope-updated
// (AddPrinter, RemovePrinter, SetDefaultPrinter), behind polkit.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/user"
	"sort"
	"strings"
	"time"

	"github.com/OpenPrinting/goipp"

	"github.com/kairos-community/isotope/pkg/i18n"
)

// Printer is one CUPS queue.
type Printer struct {
	Name, Info, Location, Model, DeviceURI string
	// State is IPP's printer-state: 3 idle, 4 printing, 5 stopped.
	State        int
	StateMessage string
	Accepting    bool
	Default      bool
}

// The printer-state values.
const (
	printerIdle       = 3
	printerProcessing = 4
	printerStopped    = 5
)

// Automatic says cups-browsed made the queue and makes it again while the
// printer is there, so removing it does not last: an IPP-over-USB printer
// that ipp-usb publishes on loopback (cups-browsed.conf has
// CreateIPPPrinterQueues LocalOnly), or one of cups-browsed's clusters.
func (p Printer) Automatic() bool {
	if strings.HasPrefix(p.DeviceURI, "implicitclass:") {
		return true
	}
	u, err := url.Parse(p.DeviceURI)
	if err != nil {
		return false
	}
	h := u.Hostname()
	if h == "localhost" {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

// Title is the name the page shows: the description when there is one.
func (p Printer) Title() string {
	if p.Info != "" {
		return p.Info
	}
	return p.Name
}

// FoundPrinter is a printer Avahi found: an IPP printer on the network, or
// a USB printer that ipp-usb publishes on this computer.
type FoundPrinter struct {
	// Service is its DNS-SD name, Queue the queue name it would get.
	Service, Queue        string
	Model, Location, UUID string
	// URI is the dnssd:// URI the queue gets.
	URI string
	// Address and Port are where it answers, to match a queue that
	// prints to it by address (cups-browsed's, or one added by hand).
	Address string
	Port    uint16
	// USB is true for a printer behind ipp-usb (published on loopback).
	USB bool
	// Driverless says it takes a format CUPS can make without a driver
	// (PDF, PWG or Apple raster). Without it, no queue can be made.
	Driverless bool
}

// Printing is CUPS and Avahi as the page uses them. Every method blocks;
// the page calls them off the UI goroutine.
type Printing interface {
	Printers() ([]Printer, error)
	// Find browses the network for IPP printers until ctx ends, or until
	// Avahi has said it has no more for now.
	Find(ctx context.Context) ([]FoundPrinter, error)
	// PrintTestPage sends CUPS's test page to the queue and returns the
	// job's ID.
	PrintTestPage(name string) (int, error)
}

// errNoCUPS is the page's "the printing service is not running".
var errNoCUPS error = nmTextError(i18n.Mark("the printing service (CUPS) is not running"))

// cupsPrinting is Printing over CUPS's socket and the system bus.
type cupsPrinting struct {
	// sock is CUPS's domain socket.
	sock string
	// testPage is CUPS's test page, a banner file the filters render.
	testPage string
	// user is requesting-user-name.
	user string
	// find browses with Avahi.
	find func(ctx context.Context) ([]FoundPrinter, error)
	http *http.Client
}

func newCUPSPrinting() *cupsPrinting {
	name := "anonymous"
	if u, err := user.Current(); err == nil {
		name = u.Username
	}
	c := newCUPSClient("/run/cups/cups.sock")
	c.user = name
	c.find = findWithAvahi
	return c
}

func newCUPSClient(sock string) *cupsPrinting {
	c := &cupsPrinting{sock: sock, testPage: "/usr/share/cups/data/testprint", user: "anonymous"}
	c.http = &http.Client{
		Timeout: 30 * time.Second,
		Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", c.sock)
		}},
	}
	return c
}

// do sends one IPP request (and a document after it, when doc is not nil)
// to path on CUPS and returns the answer.
func (c *cupsPrinting) do(path string, req *goipp.Message, doc io.Reader) (*goipp.Message, error) {
	body, err := req.EncodeBytes()
	if err != nil {
		return nil, err
	}
	var r io.Reader = bytes.NewReader(body)
	if doc != nil {
		r = io.MultiReader(r, doc)
	}
	// CUPS checks the Host header; on its socket it must be localhost.
	hr, err := http.NewRequest(http.MethodPost, "http://localhost"+path, r)
	if err != nil {
		return nil, err
	}
	hr.Header.Set("Content-Type", goipp.ContentType)
	resp, err := c.http.Do(hr)
	if err != nil {
		var oe *net.OpError
		if errors.As(err, &oe) && oe.Op == "dial" {
			return nil, errNoCUPS
		}
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf(i18n.T("CUPS answered %s"), resp.Status)
	}
	var m goipp.Message
	if err := m.Decode(resp.Body); err != nil {
		return nil, fmt.Errorf(i18n.T("CUPS's answer cannot be read: %v"), err)
	}
	return &m, nil
}

// request is an IPP request with the attributes every request starts with.
func (c *cupsPrinting) request(op goipp.Op) *goipp.Message {
	m := goipp.NewRequest(goipp.DefaultVersion, op, 1)
	m.Operation.Add(goipp.MakeAttribute("attributes-charset", goipp.TagCharset, goipp.String("utf-8")))
	m.Operation.Add(goipp.MakeAttribute("attributes-natural-language", goipp.TagLanguage, goipp.String("en")))
	return m
}

// statusErr is the request's IPP status as an error, nil when it is OK.
func statusErr(m *goipp.Message) error {
	st := goipp.Status(m.Code)
	if st < 0x0100 { // successful-ok and its "with ..." variants
		return nil
	}
	msg := attrString(m.Operation, "status-message")
	if msg == "" {
		msg = st.String()
	}
	return errors.New(msg)
}

var printerAttrs = []string{
	"printer-name", "printer-info", "printer-location", "printer-make-and-model",
	"printer-state", "printer-state-message", "printer-is-accepting-jobs", "device-uri",
}

// Printers is CUPS-Get-Printers and CUPS-Get-Default, sorted by title.
func (c *cupsPrinting) Printers() ([]Printer, error) {
	req := c.request(goipp.OpCupsGetPrinters)
	req.Operation.Add(goipp.MakeAttribute("requesting-user-name", goipp.TagName, goipp.String(c.user)))
	var want goipp.Values
	for _, a := range printerAttrs {
		want.Add(goipp.TagKeyword, goipp.String(a))
	}
	req.Operation.Add(goipp.Attribute{Name: "requested-attributes", Values: want})
	resp, err := c.do("/", req, nil)
	if err != nil {
		return nil, err
	}
	var out []Printer
	// No printer at all is client-error-not-found.
	if goipp.Status(resp.Code) != goipp.StatusErrorNotFound {
		if err := statusErr(resp); err != nil {
			return nil, err
		}
		for _, g := range resp.Groups {
			if g.Tag != goipp.TagPrinterGroup {
				continue
			}
			out = append(out, Printer{
				Name:         attrString(g.Attrs, "printer-name"),
				Info:         attrString(g.Attrs, "printer-info"),
				Location:     attrString(g.Attrs, "printer-location"),
				Model:        attrString(g.Attrs, "printer-make-and-model"),
				DeviceURI:    attrString(g.Attrs, "device-uri"),
				State:        attrInt(g.Attrs, "printer-state"),
				StateMessage: attrString(g.Attrs, "printer-state-message"),
				Accepting:    attrBool(g.Attrs, "printer-is-accepting-jobs"),
			})
		}
	}
	def, err := c.defaultPrinter()
	if err != nil {
		return nil, err
	}
	for i := range out {
		out[i].Default = out[i].Name == def
	}
	sort.SliceStable(out, func(i, j int) bool {
		return strings.ToLower(out[i].Title()) < strings.ToLower(out[j].Title())
	})
	return out, nil
}

// defaultPrinter is the system's default queue, "" for none.
func (c *cupsPrinting) defaultPrinter() (string, error) {
	req := c.request(goipp.OpCupsGetDefault)
	var want goipp.Values
	want.Add(goipp.TagKeyword, goipp.String("printer-name"))
	req.Operation.Add(goipp.Attribute{Name: "requested-attributes", Values: want})
	resp, err := c.do("/", req, nil)
	if err != nil {
		return "", err
	}
	if goipp.Status(resp.Code) == goipp.StatusErrorNotFound {
		return "", nil
	}
	if err := statusErr(resp); err != nil {
		return "", err
	}
	return attrString(resp.Printer, "printer-name"), nil
}

// PrintTestPage is what CUPS's web page does: Print-Job with CUPS's test
// page, a banner file that the filters (bannertopdf) turn into a page with
// the printer's name and details.
func (c *cupsPrinting) PrintTestPage(name string) (int, error) {
	f, err := os.Open(c.testPage)
	if err != nil {
		return 0, fmt.Errorf(i18n.T("CUPS's test page is missing: %v"), err)
	}
	defer f.Close()
	req := c.request(goipp.OpPrintJob)
	req.Operation.Add(goipp.MakeAttribute("printer-uri", goipp.TagURI,
		goipp.String("ipp://localhost/printers/"+url.PathEscape(name))))
	req.Operation.Add(goipp.MakeAttribute("requesting-user-name", goipp.TagName, goipp.String(c.user)))
	req.Operation.Add(goipp.MakeAttribute("job-name", goipp.TagName, goipp.String("Test page")))
	req.Operation.Add(goipp.MakeAttribute("document-format", goipp.TagMimeType, goipp.String("application/vnd.cups-banner")))
	resp, err := c.do("/printers/"+url.PathEscape(name), req, f)
	if err != nil {
		return 0, err
	}
	if err := statusErr(resp); err != nil {
		return 0, err
	}
	return attrInt(resp.Job, "job-id"), nil
}

func (c *cupsPrinting) Find(ctx context.Context) ([]FoundPrinter, error) {
	if c.find == nil {
		return nil, errors.New(i18n.T("no printer search on this system"))
	}
	return c.find(ctx)
}

func attr(as goipp.Attributes, name string) (goipp.Values, bool) {
	for _, a := range as {
		if a.Name == name && len(a.Values) > 0 {
			return a.Values, true
		}
	}
	return nil, false
}

func attrString(as goipp.Attributes, name string) string {
	if v, ok := attr(as, name); ok {
		return v[0].V.String()
	}
	return ""
}

func attrInt(as goipp.Attributes, name string) int {
	if v, ok := attr(as, name); ok {
		if i, ok := v[0].V.(goipp.Integer); ok {
			return int(i)
		}
	}
	return 0
}

func attrBool(as goipp.Attributes, name string) bool {
	if v, ok := attr(as, name); ok {
		if b, ok := v[0].V.(goipp.Boolean); ok {
			return bool(b)
		}
	}
	return false
}
