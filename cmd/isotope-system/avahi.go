package main

// The printers on the network, as Avahi finds them: browse _ipp._tcp and
// _ipps._tcp over Avahi's D-Bus API, resolve each one, and read its TXT
// record (RFC 6763, and the IPP Everywhere and AirPrint keys). As the user:
// avahi-daemon answers every client on the system bus. Not CUPS's
// CUPS-Get-Devices, which CUPS allows to administrators only and which runs
// every backend for many seconds.

import (
	"context"
	"errors"
	"net"
	"sort"
	"strings"
	"time"

	"github.com/godbus/dbus/v5"

	"github.com/kairos-community/isotope/pkg/printing"
)

const (
	avahiName    = "org.freedesktop.Avahi"
	avahiServer  = "org.freedesktop.Avahi.Server"
	avahiBrowser = "org.freedesktop.Avahi.ServiceBrowser"
	// avahiUnspec is AVAHI_IF_UNSPEC and AVAHI_PROTO_UNSPEC.
	avahiUnspec = int32(-1)
)

// ippTypes are the DNS-SD types of an IPP printer; a printer that has both
// gets the secure one (mergeFound).
var ippTypes = []string{"_ipps._tcp", "_ipp._tcp"}

// resolveTimeout bounds one ResolveService: a printer that left the network
// is not waited for longer.
const resolveTimeout = 5 * time.Second

// errNoAvahi is the page's "printers cannot be searched for".
var errNoAvahi = errors.New("the network printer search (Avahi) is not running")

// findWithAvahi browses on the system bus.
func findWithAvahi(ctx context.Context) ([]FoundPrinter, error) {
	conn, err := dbus.ConnectSystemBus()
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	return findAvahi(ctx, conn)
}

type avahiItem struct {
	iface, proto      int32
	name, typ, domain string
}

// findAvahi browses for IPP printers until ctx ends or every browser said
// AllForNow, then resolves what it found.
func findAvahi(ctx context.Context, conn *dbus.Conn) ([]FoundPrinter, error) {
	// The signals come before ServiceBrowserNew returns the browser's
	// path, so the channel is there first and they are sorted by path
	// after.
	if err := conn.AddMatchSignal(dbus.WithMatchSender(avahiName), dbus.WithMatchInterface(avahiBrowser)); err != nil {
		return nil, err
	}
	defer conn.RemoveMatchSignal(dbus.WithMatchSender(avahiName), dbus.WithMatchInterface(avahiBrowser))
	sigs := make(chan *dbus.Signal, 256)
	conn.Signal(sigs)
	defer conn.RemoveSignal(sigs)

	server := conn.Object(avahiName, "/")
	browsers := map[dbus.ObjectPath]bool{} // path -> AllForNow seen
	for _, typ := range ippTypes {
		var path dbus.ObjectPath
		err := server.CallWithContext(ctx, avahiServer+".ServiceBrowserNew", 0,
			avahiUnspec, avahiUnspec, typ, "local", uint32(0)).Store(&path)
		if err != nil {
			var de dbus.Error
			if errors.As(err, &de) && strings.HasPrefix(de.Name, "org.freedesktop.DBus.Error.") {
				return nil, errNoAvahi
			}
			return nil, err
		}
		browsers[path] = false
		defer conn.Object(avahiName, path).Call(avahiBrowser+".Free", 0)
	}

	var items []avahiItem
	seen := map[string]bool{}
	done := func() bool {
		for _, all := range browsers {
			if !all {
				return false
			}
		}
		return true
	}
wait:
	for !done() {
		select {
		case <-ctx.Done():
			break wait
		case s := <-sigs:
			if _, ok := browsers[s.Path]; !ok {
				continue
			}
			switch s.Name {
			case avahiBrowser + ".ItemNew":
				var it avahiItem
				var flags uint32
				if dbus.Store(s.Body, &it.iface, &it.proto, &it.name, &it.typ, &it.domain, &flags) != nil {
					continue
				}
				// One answer per network and protocol: resolve each
				// printer once.
				if k := it.typ + "\x00" + it.name; !seen[k] {
					seen[k] = true
					items = append(items, it)
				}
			case avahiBrowser + ".AllForNow", avahiBrowser + ".Failure":
				browsers[s.Path] = true
			}
		}
	}

	var found []FoundPrinter
	for _, it := range items {
		rctx, cancel := context.WithTimeout(context.Background(), resolveTimeout)
		var r avahiResolved
		err := server.CallWithContext(rctx, avahiServer+".ResolveService", 0,
			it.iface, it.proto, it.name, it.typ, it.domain, avahiUnspec, uint32(0)).
			Store(&r.iface, &r.proto, &r.name, &r.typ, &r.domain, &r.host, &r.aproto, &r.address, &r.port, &r.txt, &r.flags)
		cancel()
		if err != nil {
			continue // gone, or it does not answer: not shown
		}
		found = append(found, r.printer())
	}
	return mergeFound(found), nil
}

// avahiResolved is ResolveService's answer.
type avahiResolved struct {
	iface, proto, aproto int32
	name, typ, domain    string
	host, address        string
	port                 uint16
	txt                  [][]byte
	flags                uint32
}

// printer reads the answer: the TXT keys ty (make and model), note
// (location), UUID, and pdl and URF for what the printer takes.
func (r avahiResolved) printer() FoundPrinter {
	txt := map[string]string{}
	for _, kv := range r.txt {
		k, v, _ := strings.Cut(string(kv), "=")
		txt[strings.ToLower(k)] = v
	}
	p := FoundPrinter{
		Service:  r.name,
		Queue:    printing.QueueName(r.name),
		Model:    txt["ty"],
		Location: txt["note"],
		UUID:     txt["uuid"],
	}
	p.URI = printing.DNSSDURI(r.name, r.typ, p.UUID)
	p.Address, p.Port = r.address, r.port
	if ip := net.ParseIP(r.address); ip != nil && ip.IsLoopback() {
		p.USB = true
	}
	_, urf := txt["urf"]
	pdl := strings.ToLower(txt["pdl"])
	p.Driverless = urf || strings.Contains(pdl, "image/pwg-raster") ||
		strings.Contains(pdl, "image/urf") || strings.Contains(pdl, "application/pdf")
	return p
}

// mergeFound keeps one entry per printer (by UUID, else by name): the
// secure one (_ipps) when the printer offers both. Sorted by name.
func mergeFound(in []FoundPrinter) []FoundPrinter {
	in = append([]FoundPrinter(nil), in...)
	secure := func(p FoundPrinter) bool { return strings.Contains(p.URI, "._ipps._tcp.") }
	sort.SliceStable(in, func(i, j int) bool { return secure(in[i]) && !secure(in[j]) })
	var out []FoundPrinter
	seen := map[string]bool{}
	for _, p := range in {
		k := "name:" + p.Service
		if p.UUID != "" {
			k = "uuid:" + strings.ToLower(p.UUID)
		}
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, p)
	}
	sort.SliceStable(out, func(i, j int) bool { return strings.ToLower(out[i].Service) < strings.ToLower(out[j].Service) })
	return out
}
