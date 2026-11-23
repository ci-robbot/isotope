package main

// VPN connections of NetworkManager: the saved ones, activate, deactivate,
// delete, and the WireGuard import.

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/godbus/dbus/v5"
)

// VPN is one saved VPN connection.
type VPN struct {
	ID, UUID string
	Kind     string // "WireGuard", "OpenConnect" or "VPN"
	Active   bool
}

// OpenConnect is a company VPN that NetworkManager-openconnect connects:
// Cisco AnyConnect, Palo Alto GlobalProtect, Fortinet, Juniper or Pulse
// (Ivanti).
// Protocol is an ID from OpenConnectProtocols.
type OpenConnect struct {
	Name, Gateway, Protocol, User string
}

// vpnFromSettings is the VPN a connection's settings describe; ok is false
// for any other kind of connection.
func vpnFromSettings(s settings, active map[string]bool) (VPN, bool) {
	c := s["connection"]
	t, _ := c["type"].Value().(string)
	v := VPN{Active: false}
	v.ID, _ = c["id"].Value().(string)
	v.UUID, _ = c["uuid"].Value().(string)
	switch t {
	case "wireguard":
		v.Kind = "WireGuard"
	case "vpn":
		svc, _ := s["vpn"]["service-type"].Value().(string)
		if svc == openConnectService {
			v.Kind = "OpenConnect"
		} else {
			v.Kind = "VPN"
		}
	default:
		return VPN{}, false
	}
	v.Active = active[v.UUID]
	return v, true
}

// vpnsFromConnections keeps the VPN connections, sorted by name.
func vpnsFromConnections(all []settings, active map[string]bool) []VPN {
	var out []VPN
	for _, s := range all {
		if v, ok := vpnFromSettings(s, active); ok {
			out = append(out, v)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].ID != out[j].ID {
			return out[i].ID < out[j].ID
		}
		return out[i].UUID < out[j].UUID
	})
	return out
}

const nmSettingsPath = nmPath + "/Settings"

// activeUUIDs maps the UUID of each active connection to its object path.
func activeUUIDs(c *dbus.Conn) map[string]dbus.ObjectPath {
	out := map[string]dbus.ObjectPath{}
	acs, err := prop[[]dbus.ObjectPath](c.Object(nmName, nmPath), nmIface+".ActiveConnections")
	if err != nil {
		return out
	}
	for _, ac := range acs {
		if u, err := prop[string](c.Object(nmName, ac), nmIface+".Connection.Active.Uuid"); err == nil {
			out[u] = ac
		}
	}
	return out
}

func (n nmNetwork) VPNs() ([]VPN, error) {
	c, err := n.conn()
	if err != nil {
		return nil, err
	}
	defer c.Close()
	var paths []dbus.ObjectPath
	if err := c.Object(nmName, nmSettingsPath).Call(nmIface+".Settings.ListConnections", 0).Store(&paths); err != nil {
		return nil, err
	}
	var all []settings
	for _, p := range paths {
		var s settings
		if err := c.Object(nmName, p).Call(nmIface+".Settings.Connection.GetSettings", 0).Store(&s); err == nil {
			all = append(all, s)
		}
	}
	active := map[string]bool{}
	for u := range activeUUIDs(c) {
		active[u] = true
	}
	return vpnsFromConnections(all, active), nil
}

func connByUUID(c *dbus.Conn, uuid string) (dbus.ObjectPath, error) {
	var p dbus.ObjectPath
	if err := c.Object(nmName, nmSettingsPath).Call(nmIface+".Settings.GetConnectionByUuid", 0, uuid).Store(&p); err != nil {
		return "", fmt.Errorf("No such VPN connection: %w", err)
	}
	return p, nil
}

// activeReason is NM_ACTIVE_CONNECTION_STATE_REASON in words, so the user
// can tell a bad key from an unreachable server.
func activeReason(r uint32) string {
	switch r {
	case 2:
		return "it was disconnected"
	case 3:
		return "the network device disconnected"
	case 4:
		return "the VPN service stopped"
	case 5:
		return "the VPN settings are not valid"
	case 6:
		return "the connection timed out: the server may be unreachable"
	case 7:
		return "the VPN service did not start in time"
	case 8:
		return "the VPN service could not start"
	case 9:
		return "no password or key was given, or the login failed"
	case 10:
		return "the login failed: check the key or password"
	case 11:
		return "the connection was removed"
	case 12:
		return "a connection it needs failed"
	case 13, 14:
		return "the network device is not available"
	}
	return "NetworkManager gave no reason"
}

func (n nmNetwork) ActivateVPN(uuid string) error {
	c, err := n.conn()
	if err != nil {
		return err
	}
	defer c.Close()
	p, err := connByUUID(c, uuid)
	if err != nil {
		return err
	}
	// Listen first: a fast failure ends before the call returns.
	if err := c.AddMatchSignal(dbus.WithMatchInterface(nmIface+".Connection.Active"), dbus.WithMatchMember("StateChanged")); err != nil {
		return err
	}
	sig := make(chan *dbus.Signal, 32)
	c.Signal(sig)
	defer c.RemoveSignal(sig)
	kind := ""
	var cs settings
	if err := c.Object(nmName, p).Call(nmIface+".Settings.Connection.GetSettings", 0).Store(&cs); err == nil {
		kind, _ = cs["connection"]["type"].Value().(string)
	}
	var active dbus.ObjectPath
	// "/" as the device and the specific object: NetworkManager picks them.
	if err := c.Object(nmName, nmPath).Call(nmIface+".ActivateConnection", 0, p, dbus.ObjectPath("/"), dbus.ObjectPath("/")).Store(&active); err != nil {
		return err
	}
	state := func() (uint32, error) {
		return prop[uint32](c.Object(nmName, active), nmIface+".Connection.Active.State")
	}
	err = waitVPN(sig, active, state, vpnWaitBound(kind), time.Second)
	if errors.Is(err, errVPNTimeout) {
		_ = c.Object(nmName, nmPath).Call(nmIface+".DeactivateConnection", 0, active).Err
	}
	return err
}

// errVPNTimeout is waitVPN's answer when NetworkManager settled nothing in
// time.
var errVPNTimeout = errors.New("Timed out connecting the VPN")

// vpnWaitBound is how long the page waits for NetworkManager to settle a
// VPN of type kind (the connection.type). An OpenConnect login runs through
// the tray: a password dialog (2 minutes), openconnect --authenticate, then
// a second-factor dialog, and NetworkManager ends a login that fails or is
// refused itself, with Deactivated and a reason. So the page waits for that
// answer, and the bound is only for a NetworkManager that never answers. A
// WireGuard tunnel asks nothing and comes up at once, so a minute is plenty.
func vpnWaitBound(kind string) time.Duration {
	if kind == "wireguard" {
		return vpnWaitWireGuard
	}
	return vpnWaitLogin
}

var (
	vpnWaitWireGuard = 60 * time.Second
	vpnWaitLogin     = 300 * time.Second
)

// waitVPN waits until the active connection active is Activated (nil) or
// Deactivated (the reason as an error), from its StateChanged signals on sig
// and, every poll, its State property (in case a signal was missed). After
// bound it gives up with errVPNTimeout.
func waitVPN(sig <-chan *dbus.Signal, active dbus.ObjectPath, state func() (uint32, error), bound, poll time.Duration) error {
	failed := func(reason uint32) error {
		return fmt.Errorf("The VPN did not connect: %s", activeReason(reason))
	}
	deadline := time.NewTimer(bound)
	defer deadline.Stop()
	tick := time.NewTicker(poll)
	defer tick.Stop()
	for {
		select {
		case s, ok := <-sig:
			if !ok {
				sig = nil // the connection closed; the poll goes on
				continue
			}
			if s.Path != active || len(s.Body) < 2 {
				continue
			}
			st, _ := s.Body[0].(uint32)
			reason, _ := s.Body[1].(uint32)
			switch st {
			case nmActivated:
				return nil
			case nmDeactivated:
				return failed(reason)
			}
		case <-tick.C:
			st, err := state()
			if err != nil {
				return failed(0) // the object went away: deactivated
			}
			switch st {
			case nmActivated:
				return nil
			case nmDeactivated:
				return failed(0)
			}
		case <-deadline.C:
			return errVPNTimeout
		}
	}
}

func (n nmNetwork) DeactivateVPN(uuid string) error {
	c, err := n.conn()
	if err != nil {
		return err
	}
	defer c.Close()
	ac, ok := activeUUIDs(c)[uuid]
	if !ok {
		return nil // already off
	}
	return c.Object(nmName, nmPath).Call(nmIface+".DeactivateConnection", 0, ac).Err
}

func (n nmNetwork) RemoveVPN(uuid string) error {
	c, err := n.conn()
	if err != nil {
		return err
	}
	defer c.Close()
	p, err := connByUUID(c, uuid)
	if err != nil {
		return err
	}
	if ac, ok := activeUUIDs(c)[uuid]; ok {
		_ = c.Object(nmName, nmPath).Call(nmIface+".DeactivateConnection", 0, ac).Err
	}
	return c.Object(nmName, p).Call(nmIface+".Settings.Connection.Delete", 0).Err
}

// wireguardIfaces is the interface names of the saved WireGuard connections.
func wireguardIfaces(c *dbus.Conn) map[string]bool {
	used := map[string]bool{}
	var paths []dbus.ObjectPath
	if err := c.Object(nmName, nmSettingsPath).Call(nmIface+".Settings.ListConnections", 0).Store(&paths); err != nil {
		return used
	}
	for _, p := range paths {
		var s settings
		if err := c.Object(nmName, p).Call(nmIface+".Settings.Connection.GetSettings", 0).Store(&s); err != nil {
			continue
		}
		if t, _ := s["connection"]["type"].Value().(string); t == "wireguard" {
			if i, ok := s["connection"]["interface-name"].Value().(string); ok {
				used[i] = true
			}
		}
	}
	return used
}

func (n nmNetwork) ImportWireGuard(name string, conf []byte) error {
	s, err := ParseWireGuard(conf)
	if err != nil {
		return err
	}
	c, err := n.conn()
	if err != nil {
		return err
	}
	defer c.Close()
	nameWireGuard(s, name, wireguardIfaces(c))
	var p dbus.ObjectPath
	return c.Object(nmName, nmSettingsPath).Call(nmIface+".Settings.AddConnection", 0, s).Store(&p)
}

// openConnectService is NetworkManager-openconnect's VPN service type.
const openConnectService = "org.freedesktop.NetworkManager.openconnect"

// OpenConnectProtocols are the protocols the form offers: the value of the
// "protocol" data item (openconnect's --protocol) and its label.
var OpenConnectProtocols = [...]struct{ ID, Label string }{
	{"anyconnect", "Cisco AnyConnect"},
	{"gp", "Palo Alto GlobalProtect"},
	{"fortinet", "Fortinet"},
	{"nc", "Juniper"},
	{"pulse", "Pulse / Ivanti"},
}

func openConnectProtocol(id string) bool {
	for _, p := range OpenConnectProtocols {
		if p.ID == id {
			return true
		}
	}
	return false
}

// openConnectSettings is the NetworkManager connection for a company VPN.
//
// NetworkManager-openconnect's service takes only the cookie, gateway,
// gwcert and resolve secrets, which change at every login: the "-flags" of
// 2 (NOT_SAVED) make NetworkManager ask the secret agent each time and keep
// nothing. The tray's agent (cmd/isotope/openconnect.go) logs in with
// `openconnect --authenticate` and returns them. The user name is the
// standard vpn.user-name, where the agent reads it; the service ignores it.
// authtype=password is what the GNOME editor writes for a password login.
func openConnectSettings(o OpenConnect) (settings, error) {
	gw := strings.TrimSpace(o.Gateway)
	user := strings.TrimSpace(o.User)
	name := strings.TrimSpace(o.Name)
	switch {
	case gw == "":
		return nil, fmt.Errorf("Type the VPN server")
	case strings.HasPrefix(gw, "-") || strings.ContainsAny(gw, " \t\r\n"):
		return nil, fmt.Errorf("%q is not a server address", gw)
	case !openConnectProtocol(o.Protocol):
		return nil, fmt.Errorf("Choose the type of VPN")
	case user == "":
		return nil, fmt.Errorf("Type your user name")
	case strings.ContainsAny(user, "\r\n"):
		return nil, fmt.Errorf("The user name cannot have line breaks")
	}
	if name == "" {
		name = gw
	}
	data := map[string]string{
		"gateway":       gw,
		"protocol":      o.Protocol,
		"authtype":      "password",
		"cookie-flags":  "2",
		"gateway-flags": "2",
		"gwcert-flags":  "2",
		"resolve-flags": "2",
	}
	return settings{
		"connection": {
			"id":          dbus.MakeVariant(name),
			"type":        dbus.MakeVariant("vpn"),
			"uuid":        dbus.MakeVariant(newUUID()),
			"autoconnect": dbus.MakeVariant(false),
		},
		"vpn": {
			"service-type": dbus.MakeVariant(openConnectService),
			"user-name":    dbus.MakeVariant(user),
			"data":         dbus.MakeVariant(data),
		},
		"ipv4": {"method": dbus.MakeVariant("auto")},
		"ipv6": {"method": dbus.MakeVariant("auto")},
	}, nil
}

func (n nmNetwork) AddOpenConnect(o OpenConnect) error {
	s, err := openConnectSettings(o)
	if err != nil {
		return err
	}
	c, err := n.conn()
	if err != nil {
		return err
	}
	defer c.Close()
	var p dbus.ObjectPath
	return c.Object(nmName, nmSettingsPath).Call(nmIface+".Settings.AddConnection", 0, s).Store(&p)
}
