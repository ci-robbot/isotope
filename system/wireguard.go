package main

// WireGuard import: a wg-quick .conf file as NetworkManager's connection
// map. NetworkManager 1.50 does WireGuard in its core, as the user, so the
// result goes to Settings.AddConnection like any other connection. The keys
// and D-Bus types are those of nm-settings-dbus(5) for 1.50: the property
// "dns-data" (as) replaces the deprecated "dns" and takes the same text for
// IPv4 and IPv6.

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"strings"

	"github.com/godbus/dbus/v5"
)

// wgLine is the error for one line of the file. It never quotes the file:
// a line can hold a private key.
func wgLine(n int, format string, a ...any) error {
	return fmt.Errorf("line %d: %s", n, fmt.Sprintf(format, a...))
}

// wgKey checks a WireGuard key: base64 text of 32 bytes.
func wgKey(v string) bool {
	b, err := base64.StdEncoding.DecodeString(v)
	return err == nil && len(b) == 32
}

// wgPrefix reads an address or a prefix. A bare address is a single host.
func wgPrefix(v string) (netip.Prefix, error) {
	if p, err := netip.ParsePrefix(v); err == nil {
		return p, nil
	}
	a, err := netip.ParseAddr(v)
	if err != nil {
		return netip.Prefix{}, fmt.Errorf("not an address")
	}
	return netip.PrefixFrom(a, a.BitLen()), nil
}

// wgNumber reads a decimal or 0x number that fits in bits; "off" is 0.
func wgNumber(v string, bits int) (uint32, error) {
	if strings.EqualFold(v, "off") {
		return 0, nil
	}
	n, err := strconv.ParseUint(v, 0, bits)
	if err != nil {
		return 0, fmt.Errorf("not a valid number")
	}
	return uint32(n), nil
}

func wgList(v string) []string {
	var out []string
	for _, s := range strings.Split(v, ",") {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// wgKeyName is a key name that is safe to show: short and made of letters.
// Anything else could be a pasted secret.
func wgKeyName(k string) string {
	if k == "" || len(k) > 24 {
		return ""
	}
	for _, r := range k {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z') {
			return ""
		}
	}
	return k + " "
}

func newUUID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}

type wgPeer struct {
	line  int
	m     map[string]dbus.Variant
	seen  map[string]bool
	ips   []string
	has6  bool // an AllowedIPs prefix is IPv6
	full4 int  // the line that routes all IPv4, 0 for none
	full6 bool // an AllowedIPs of ::/0
}

// wgScriptKeys are wg-quick's script and state keys. NetworkManager runs
// none of them.
var wgScriptKeys = map[string]bool{"preup": true, "postup": true, "predown": true, "postdown": true, "saveconfig": true}

// ParseWireGuard reads a wg-quick configuration and returns the connection
// for Settings.AddConnection. The name of the connection is "WireGuard"; see
// nameWireGuard. Isotope runs WireGuard through NetworkManager, which runs
// no PreUp, PostUp, PreDown, PostDown or SaveConfig and has no routing
// table number, so those keys are refused ("Table = off" and "auto" are
// kept). Errors name a line and never quote the file, which holds keys.
func ParseWireGuard(conf []byte) (settings, error) {
	var (
		section                    string
		sawIface                   bool
		ifaceLine                  int
		priv                       string
		listen, fwmark, mtu        uint32
		addrs4, addrs6             []map[string]dbus.Variant
		dns4, dns6, search         []string
		dns4Line, dns6Line         int
		peers                      []*wgPeer
		cur                        *wgPeer
		hasMTU, hasListen, hasMark bool
		noRoutes                   bool
		seen                       = map[string]bool{}
	)
	for i, raw := range strings.Split(string(conf), "\n") {
		n := i + 1
		line := strings.TrimSpace(raw) // also drops the CR of a CRLF file
		if j := strings.IndexByte(line, '#'); j >= 0 {
			line = strings.TrimSpace(line[:j])
		}
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "[") {
			switch strings.ToLower(line) {
			case "[interface]":
				if sawIface {
					return nil, wgLine(n, "a second [Interface] section")
				}
				sawIface, ifaceLine, section, cur = true, n, "interface", nil
			case "[peer]":
				cur = &wgPeer{line: n, m: map[string]dbus.Variant{}, seen: map[string]bool{}}
				peers = append(peers, cur)
				section = "peer"
			default:
				return nil, wgLine(n, "unknown section")
			}
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			return nil, wgLine(n, "not a Key = Value line")
		}
		k, v = strings.ToLower(strings.TrimSpace(k)), strings.TrimSpace(v)
		if section == "" {
			return nil, wgLine(n, "a setting outside a section")
		}
		// Keys that may repeat add up, as in wg(8); the others may not.
		multi := k == "address" || k == "dns" || k == "allowedips"
		dup := seen
		if section == "peer" {
			dup = cur.seen
		}
		if !multi {
			if dup[k] {
				return nil, wgLine(n, "duplicate key")
			}
			dup[k] = true
		}
		switch section {
		case "interface":
			switch {
			case k == "privatekey":
				if !wgKey(v) {
					return nil, wgLine(n, "PrivateKey is not a 32-byte base64 key")
				}
				priv = v
			case k == "address":
				for _, a := range wgList(v) {
					p, err := wgPrefix(a)
					if err != nil {
						return nil, wgLine(n, "Address is %v", err)
					}
					e := map[string]dbus.Variant{
						"address": dbus.MakeVariant(p.Addr().String()),
						"prefix":  dbus.MakeVariant(uint32(p.Bits())),
					}
					if p.Addr().Is4() {
						addrs4 = append(addrs4, e)
					} else {
						addrs6 = append(addrs6, e)
					}
				}
			case k == "dns":
				for _, d := range wgList(v) {
					a, err := netip.ParseAddr(d)
					switch {
					case err != nil:
						search = append(search, d) // a search domain
					case a.Is4():
						dns4, dns4Line = append(dns4, d), n
					default:
						dns6, dns6Line = append(dns6, d), n
					}
				}
			case k == "mtu":
				x, err := wgNumber(v, 32)
				if err != nil {
					return nil, wgLine(n, "MTU is %v", err)
				}
				mtu, hasMTU = x, true
			case k == "listenport":
				x, err := wgNumber(v, 16)
				if err != nil {
					return nil, wgLine(n, "ListenPort is %v", err)
				}
				listen, hasListen = x, true
			case k == "fwmark":
				x, err := wgNumber(v, 32)
				if err != nil {
					return nil, wgLine(n, "FwMark is %v", err)
				}
				fwmark, hasMark = x, true
			case k == "table":
				switch strings.ToLower(v) {
				case "off":
					noRoutes = true
				case "auto":
				default:
					return nil, wgLine(n, "a Table number is not supported: NetworkManager sets the routes. Use Table = off or auto")
				}
			case wgScriptKeys[k]:
				return nil, wgLine(n, "%s is not supported: Isotope runs WireGuard through NetworkManager, which does not run it", wgKeyName(k)+"setting")
			default:
				return nil, wgLine(n, "unknown key %s in [Interface]", strings.TrimSpace(wgKeyName(k)))
			}
		case "peer":
			switch k {
			case "publickey":
				if !wgKey(v) {
					return nil, wgLine(n, "PublicKey is not a 32-byte base64 key")
				}
				cur.m["public-key"] = dbus.MakeVariant(v)
			case "presharedkey":
				if !wgKey(v) {
					return nil, wgLine(n, "PresharedKey is not a 32-byte base64 key")
				}
				cur.m["preshared-key"] = dbus.MakeVariant(v)
				cur.m["preshared-key-flags"] = dbus.MakeVariant(uint32(0)) // saved with the connection
			case "allowedips":
				for _, a := range wgList(v) {
					p, err := wgPrefix(a)
					if err != nil {
						return nil, wgLine(n, "AllowedIPs is %v", err)
					}
					cur.ips = append(cur.ips, p.String())
					if p.Addr().Is6() {
						cur.has6 = true
					}
					switch p.String() {
					case "0.0.0.0/0":
						cur.full4 = n
					case "::/0":
						cur.full6 = true
					}
				}
				cur.m["allowed-ips"] = dbus.MakeVariant(cur.ips)
			case "endpoint":
				host, port, err := net.SplitHostPort(v)
				if _, perr := strconv.ParseUint(port, 10, 16); err != nil || host == "" || perr != nil {
					return nil, wgLine(n, "Endpoint is not host:port")
				}
				cur.m["endpoint"] = dbus.MakeVariant(v)
			case "persistentkeepalive":
				x, err := wgNumber(v, 16)
				if err != nil {
					return nil, wgLine(n, "PersistentKeepalive is %v", err)
				}
				cur.m["persistent-keepalive"] = dbus.MakeVariant(x)
			default:
				return nil, wgLine(n, "unknown key %s in [Peer]", strings.TrimSpace(wgKeyName(k)))
			}
		}
	}
	if !sawIface {
		return nil, fmt.Errorf("no [Interface] section")
	}
	if priv == "" {
		return nil, wgLine(ifaceLine, "the [Interface] section has no PrivateKey")
	}
	plist := []map[string]dbus.Variant{}
	full4, has6, full := 0, false, false
	for _, p := range peers {
		has6 = has6 || p.has6
		full = full || p.full4 != 0 || p.full6
		if _, ok := p.m["public-key"]; !ok {
			return nil, wgLine(p.line, "the [Peer] section has no PublicKey")
		}
		if p.full4 != 0 {
			full4 = p.full4
		}
		plist = append(plist, p.m)
	}
	// Without an IPv4 address on the tunnel, IPv4 traffic would leave
	// outside it.
	if full4 != 0 && len(addrs4) == 0 && !noRoutes {
		return nil, wgLine(full4, "AllowedIPs has 0.0.0.0/0 but the [Interface] has no IPv4 Address: IPv4 traffic would not use the tunnel")
	}
	if len(dns4) > 0 && len(addrs4) == 0 {
		return nil, wgLine(dns4Line, "the IPv4 DNS server needs an IPv4 Address in [Interface]")
	}
	if len(dns6) > 0 && len(addrs6) == 0 {
		return nil, wgLine(dns6Line, "the IPv6 DNS server needs an IPv6 Address in [Interface]")
	}
	wg := map[string]dbus.Variant{
		"private-key":       dbus.MakeVariant(priv),
		"private-key-flags": dbus.MakeVariant(uint32(0)),
		"peer-routes":       dbus.MakeVariant(!noRoutes),
		"peers":             dbus.MakeVariant(plist),
	}
	if hasListen {
		wg["listen-port"] = dbus.MakeVariant(listen)
	}
	if hasMark {
		wg["fwmark"] = dbus.MakeVariant(fwmark)
	}
	if hasMTU {
		wg["mtu"] = dbus.MakeVariant(mtu)
	}
	// An AllowedIPs of 0.0.0.0/0 or ::/0 needs no flag here: never-default
	// stays false, so NetworkManager routes everything through the tunnel.
	ip := func(addrs []map[string]dbus.Variant, dns []string, keep bool) map[string]dbus.Variant {
		if len(addrs) == 0 {
			// "disabled" sets disable_ipv6 on the interface and the kernel
			// then refuses the IPv6 peer routes, so IPv6 would leave outside
			// the tunnel. "link-local" keeps IPv6 on without an address.
			if keep {
				return map[string]dbus.Variant{"method": dbus.MakeVariant("link-local")}
			}
			return map[string]dbus.Variant{"method": dbus.MakeVariant("disabled")}
		}
		m := map[string]dbus.Variant{
			"method":       dbus.MakeVariant("manual"),
			"address-data": dbus.MakeVariant(addrs),
		}
		if len(dns) > 0 {
			m["dns-data"] = dbus.MakeVariant(dns)
			// A full tunnel must also take every DNS query. With dns=default
			// NetworkManager writes one resolv.conf from every connection,
			// and musl asks all its servers at once, so the LAN's server
			// would still see the names. A negative priority makes
			// NetworkManager list only this connection's servers (the
			// property is a D-Bus int32, "i", in NetworkManager 1.50).
			if full && !noRoutes {
				m["dns-priority"] = dbus.MakeVariant(int32(wgFullTunnelDNSPriority))
			}
		}
		if len(search) > 0 {
			m["dns-search"] = dbus.MakeVariant(search)
		}
		return m
	}
	return settings{
		"connection": {
			"id":             dbus.MakeVariant("WireGuard"),
			"type":           dbus.MakeVariant("wireguard"),
			"uuid":           dbus.MakeVariant(newUUID()),
			"interface-name": dbus.MakeVariant("wg0"),
		},
		"wireguard": wg,
		"ipv4":      ip(addrs4, dns4, false),
		"ipv6":      ip(addrs6, dns6, has6 && !noRoutes),
	}, nil
}

// wgFullTunnelDNSPriority is the dns-priority of a full-tunnel WireGuard
// connection's DNS servers: negative, so that no other connection's servers
// are used. -50 leaves room below it for a connection that must win anyway.
const wgFullTunnelDNSPriority = -50

// wgIfaceBase makes an interface name from a connection name: at most 15
// characters of letters, digits and "_=+.-", and not "." or "..". It is
// "wg0" when nothing is left.
func wgIfaceBase(name string, limit int) string {
	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', strings.ContainsRune("_=+.-", r):
			if b.Len() < limit {
				b.WriteRune(r)
			}
		}
	}
	if s := b.String(); s != "." && s != ".." && s != "" {
		return s
	}
	return "wg0"
}

// nameWireGuard gives the parsed connection its name and an interface name
// made from it. A name in used (the interfaces of the existing WireGuard
// connections) gets a number: work1, work2, ...
func nameWireGuard(s settings, name string, used map[string]bool) {
	name = strings.TrimSpace(name)
	if name == "" {
		name = "WireGuard"
	}
	iface := wgIfaceBase(name, 15)
	for i := 1; used[iface]; i++ {
		num := strconv.Itoa(i)
		iface = wgIfaceBase(name, 15-len(num)) + num
	}
	s["connection"]["id"] = dbus.MakeVariant(name)
	s["connection"]["interface-name"] = dbus.MakeVariant(iface)
}
