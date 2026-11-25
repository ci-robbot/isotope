// Package printing is what the isotope command (cmd/isotope/, whose
// isotope-updated adds and removes CUPS queues with lpadmin) and Isotope
// System (cmd/isotope-system/, which asks for them) share about a printer
// queue: the checks on its name, its device URI and its texts, so the window
// refuses what the daemon would refuse, and the names and URIs the window
// makes for a printer it found on the network.
package printing

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// MaxName is CUPS's limit on a queue name (IPP_MAX_NAME - 1).
const MaxName = 127

// MaxText is the limit on a queue's description or location.
const MaxText = 127

// maxURI is CUPS's limit on a device URI (HTTP_MAX_URI - 1).
const maxURI = 1023

// Schemes are the device URIs a queue may have: driverless IPP printers,
// found on the network or behind ipp-usb. No usb://, socket:// or lpd://:
// those printers need a driver, which Isotope does not ship, and `lpadmin
// -m everywhere` asks the printer itself for what it can do over IPP.
var Schemes = []string{"ipp", "ipps", "dnssd"}

// CheckName refuses a queue name CUPS or lpadmin would trip on. Stricter
// than CUPS: ASCII letters, digits, '.', '_' and '-', and not starting with
// '-' or '.', so it can never be read as an option.
func CheckName(name string) error {
	if name == "" {
		return errors.New("a printer needs a name")
	}
	if len(name) > MaxName {
		return fmt.Errorf("a printer name has at most %d characters", MaxName)
	}
	if name[0] == '-' || name[0] == '.' {
		return errors.New("a printer name starts with a letter or a digit")
	}
	for _, r := range name {
		if !nameChar(r) {
			return fmt.Errorf("a printer name has only letters, digits, '.', '_' and '-', not %q", r)
		}
	}
	return nil
}

func nameChar(r rune) bool {
	return r < utf8.RuneSelf && (r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '.' || r == '_' || r == '-')
}

// CheckURI refuses a device URI that is not a driverless IPP printer.
func CheckURI(uri string) error {
	if len(uri) > maxURI {
		return fmt.Errorf("a printer address has at most %d characters", maxURI)
	}
	for _, r := range uri {
		if r <= ' ' || r == 0x7f {
			return errors.New("a printer address has no spaces or control characters")
		}
	}
	scheme, rest, found := strings.Cut(uri, "://")
	ok := false
	for _, s := range Schemes {
		ok = ok || scheme == s
	}
	if !found || !ok {
		return fmt.Errorf("only driverless printers (%s) can be added", strings.Join(Schemes, ", "))
	}
	// Not url.Parse: a dnssd:// host is a percent-encoded service name,
	// which Go refuses in a host.
	host, _, _ := strings.Cut(rest, "/")
	if host == "" || strings.HasPrefix(host, ":") {
		return errors.New("the printer address has no host")
	}
	if _, err := url.PathUnescape(host); err != nil {
		return fmt.Errorf("not a printer address: %v", err)
	}
	return nil
}

// CheckText refuses a description or a location that is not one line of
// text.
func CheckText(s string) error {
	if len(s) > MaxText {
		return fmt.Errorf("at most %d characters", MaxText)
	}
	if !utf8.ValidString(s) {
		return errors.New("not valid text")
	}
	for _, r := range s {
		if unicode.IsControl(r) {
			return errors.New("one line, with no control characters")
		}
	}
	return nil
}

// QueueName makes a queue name from a printer's DNS-SD name: every run of
// characters CheckName refuses becomes one '_', cut to MaxName. A name with
// nothing left is "Printer".
func QueueName(service string) string {
	var b strings.Builder
	under := false
	for _, r := range service {
		if nameChar(r) {
			b.WriteRune(r)
			under = false
		} else if !under {
			b.WriteByte('_')
			under = true
		}
	}
	s := strings.TrimLeft(strings.Trim(b.String(), "_"), "-.")
	if len(s) > MaxName {
		s = strings.TrimRight(s[:MaxName], "_")
	}
	if s == "" {
		return "Printer"
	}
	return s
}

// DNSSDURI is the dnssd:// device URI CUPS resolves through Avahi, at every
// job, to the printer's .local host name. CUPS then looks that name up with
// getaddrinfo, which on musl fails for most printers (see AddressURI), so
// the page uses it only when AddressURI has nothing.
// svcType is "_ipp._tcp" or "_ipps._tcp"; uuid may be empty.
func DNSSDURI(instance, svcType, uuid string) string {
	u := "dnssd://" + escapeHost(instance) + "." + svcType + ".local/"
	if uuid != "" {
		u += "?uuid=" + url.QueryEscape(uuid)
	}
	return u
}

// AddressURI is the ipp:// or ipps:// device URI of a printer at address
// and port, with rp (its DNS-SD "rp" TXT key, the resource path; "ipp/print"
// when empty). svcType is "_ipp._tcp" or "_ipps._tcp". It is "" for an
// address that is not an IP, for IPv6 link-local (a URI cannot carry the
// zone CUPS would need) and for port 0. If the printer's address changes
// (a new DHCP lease), the queue must be added again: a reserved address in
// the router avoids that.
func AddressURI(svcType, address string, port uint16, rp string) string {
	ip := net.ParseIP(address)
	if ip == nil || port == 0 || ip.IsLinkLocalUnicast() || ip.IsUnspecified() {
		return ""
	}
	scheme := "ipp"
	if svcType == "_ipps._tcp" {
		scheme = "ipps"
	}
	rp = strings.Trim(rp, "/")
	if rp == "" {
		rp = "ipp/print"
	}
	var path strings.Builder
	for _, seg := range strings.Split(rp, "/") {
		path.WriteString("/" + url.PathEscape(seg))
	}
	return scheme + "://" + net.JoinHostPort(ip.String(), strconv.Itoa(int(port))) + path.String()
}

// escapeHost percent-encodes everything but the unreserved characters, as
// CUPS's own dnssd URIs do.
func escapeHost(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == '~' {
			b.WriteByte(c)
		} else {
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}
