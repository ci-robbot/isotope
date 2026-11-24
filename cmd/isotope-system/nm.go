package main

// NetworkManager on the system bus. The connect half is Isotope Welcome's
// (cmd/isotope-welcome/nm.go); this page adds the devices, Wi-Fi on and off,
// disconnect and forget. NetworkManager runs with auth-polkit=false on the
// desktops, so the user's own session may change it, as from the Wi-Fi
// menus.

import (
	"encoding/binary"
	"errors"
	"fmt"
	"net/netip"
	"sort"
	"time"

	"github.com/godbus/dbus/v5"

	"github.com/kairos-community/isotope/pkg/i18n"
)

// NetState is what the Network page needs to know about the machine.
type NetState struct {
	Connected bool   // NetworkManager says it is online
	Name      string // the active connection, when Connected
	HasWifi   bool   // a Wi-Fi device exists
	// WifiOn is NetworkManager's WirelessEnabled.
	WifiOn bool
	// Devices are the Ethernet and Wi-Fi devices.
	Devices []Device
	// Saved are the SSIDs of the saved Wi-Fi connections.
	Saved []string
	// Modems are the mobile broadband devices (the modemmanager extension).
	Modems []Modem
}

// Device is one network device.
type Device struct {
	Iface, Kind, State, Connection, Address string
}

// AccessPoint is one Wi-Fi network in range.
type AccessPoint struct {
	SSID     string
	Strength uint8 // percent
	Secured  bool
}

// Network is NetworkManager, as far as the page uses it. Every method
// blocks on D-Bus; the page calls them off the UI thread.
type Network interface {
	State() (NetState, error)
	AccessPoints() ([]AccessPoint, error)
	// Connect joins the network; a non-empty password means WPA-PSK.
	Connect(ssid, password string, hidden bool) error
	// Disconnect takes the Wi-Fi device off its network.
	Disconnect() error
	// Forget deletes the saved connections of ssid.
	Forget(ssid string) error
	// SetWifi turns the Wi-Fi radio on or off.
	SetWifi(on bool) error

	// VPNs lists the saved VPN connections (WireGuard and OpenConnect).
	VPNs() ([]VPN, error)
	ActivateVPN(uuid string) error
	DeactivateVPN(uuid string) error
	// RemoveVPN deletes the connection, after taking it down.
	RemoveVPN(uuid string) error
	// ImportWireGuard saves a wg-quick file as a connection named name.
	ImportWireGuard(name string, conf []byte) error
	// AddOpenConnect saves a company VPN.
	AddOpenConnect(c OpenConnect) error

	// ConnectModem connects a modem (a Modem's ID) with its saved
	// connection, or a new automatic one.
	ConnectModem(id string) error
	DisconnectModem(id string) error
	// SetModemAPN sets the APN of the modem's connection, "" for
	// automatic, and saves the connection when there is none.
	SetModemAPN(id, apn string) error
}

const (
	nmDeviceEthernet = 1
)

// deviceKind names a NetworkManager device type the page shows, "" for the
// others (bridges, loopback, ...).
func deviceKind(t uint32) string {
	switch t {
	case nmDeviceEthernet:
		return i18n.Mark("Wired")
	case nmDeviceWifi:
		return "Wi-Fi"
	}
	return ""
}

// deviceState is NM_DEVICE_STATE in words.
func deviceState(s uint32) string {
	switch {
	case s == 100:
		return i18n.Mark("Connected")
	case s >= 40 && s < 100:
		return i18n.Mark("Connecting")
	case s == 30:
		return i18n.Mark("Disconnected")
	case s == 20:
		return i18n.Mark("Cable unplugged")
	case s == 110 || s == 120:
		return i18n.Mark("Disconnecting")
	case s == 10:
		return i18n.Mark("Not managed")
	}
	return i18n.Mark("Unavailable")
}

// ipv4 is NetworkManager's uint32 address (network byte order, read as
// little-endian by D-Bus) as text.
func ipv4(n uint32) string {
	var b [4]byte
	binary.LittleEndian.PutUint32(b[:], n)
	return netip.AddrFrom4(b).String()
}

// dedupeAPs keeps the strongest access point of each SSID, drops hidden
// ones (empty SSID), and sorts by strength, strongest first.
func dedupeAPs(in []AccessPoint) []AccessPoint {
	best := map[string]AccessPoint{}
	for _, a := range in {
		if a.SSID == "" {
			continue
		}
		if b, ok := best[a.SSID]; !ok || a.Strength > b.Strength {
			best[a.SSID] = a
		}
	}
	out := make([]AccessPoint, 0, len(best))
	for _, a := range best {
		out = append(out, a)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Strength != out[j].Strength {
			return out[i].Strength > out[j].Strength
		}
		return out[i].SSID < out[j].SSID
	})
	return out
}

const (
	nmConnectedGlobal = 70 // NM_STATE_CONNECTED_GLOBAL
	nmDeviceWifi      = 2  // NM_DEVICE_TYPE_WIFI
	nmActivated       = 2  // NM_ACTIVE_CONNECTION_STATE_ACTIVATED
	nmDeactivated     = 4  // NM_ACTIVE_CONNECTION_STATE_DEACTIVATED
)

// nmConnected maps NetworkManager's global state to "online".
func nmConnected(state uint32) bool { return state == nmConnectedGlobal }

// apSecured: the access point needs a key (privacy flag, WPA or RSN).
func apSecured(flags, wpa, rsn uint32) bool { return flags&1 != 0 || wpa != 0 || rsn != 0 }

type settings = map[string]map[string]dbus.Variant

const (
	nmKeyMgmtPSK = 0x100 // NM_802_11_AP_SEC_KEY_MGMT_PSK
	nmKeyMgmtSAE = 0x400 // NM_802_11_AP_SEC_KEY_MGMT_SAE
)

// keyMgmt is the key-mgmt for a password network whose access point
// advertises the RSN flags rsn: "sae" for WPA3-only, else "wpa-psk" (WPA2,
// the mixed WPA2/WPA3 mode, and a network not in the list).
func keyMgmt(rsn uint32) string {
	if rsn&nmKeyMgmtSAE != 0 && rsn&nmKeyMgmtPSK == 0 {
		return "sae"
	}
	return "wpa-psk"
}

// wifiSettings is the connection AddAndActivateConnection takes. A
// password makes it a WPA network with key-mgmt km ("wpa-psk" or "sae");
// none makes it an open network.
func wifiSettings(ssid, password string, hidden bool, km string) settings {
	wireless := map[string]dbus.Variant{
		"ssid": dbus.MakeVariant([]byte(ssid)),
		"mode": dbus.MakeVariant("infrastructure"),
	}
	if hidden {
		wireless["hidden"] = dbus.MakeVariant(true)
	}
	s := settings{
		"connection": {
			"type":        dbus.MakeVariant("802-11-wireless"),
			"id":          dbus.MakeVariant(ssid),
			"autoconnect": dbus.MakeVariant(true),
		},
		"802-11-wireless": wireless,
		"ipv4":            {"method": dbus.MakeVariant("auto")},
		"ipv6":            {"method": dbus.MakeVariant("auto")},
	}
	if password != "" {
		wireless["security"] = dbus.MakeVariant("802-11-wireless-security")
		s["802-11-wireless-security"] = map[string]dbus.Variant{
			"key-mgmt": dbus.MakeVariant(km),
			"psk":      dbus.MakeVariant(password),
		}
	}
	return s
}

// nmNetwork is Network over org.freedesktop.NetworkManager on the system
// bus. The user's own polkit rights decide what it may do.
type nmNetwork struct{}

const (
	nmName  = "org.freedesktop.NetworkManager"
	nmPath  = "/org/freedesktop/NetworkManager"
	nmIface = nmName
)

func (nmNetwork) conn() (*dbus.Conn, error) { return dbus.ConnectSystemBus() }

func prop[T any](o dbus.BusObject, name string) (T, error) {
	var zero T
	v, err := o.GetProperty(name)
	if err != nil {
		return zero, err
	}
	t, ok := v.Value().(T)
	if !ok {
		return zero, fmt.Errorf("%s: unexpected type %T", name, v.Value())
	}
	return t, nil
}

// wifiDevice is the first Wi-Fi device, or "" when there is none.
func wifiDevice(c *dbus.Conn) (dbus.ObjectPath, error) {
	var devs []dbus.ObjectPath
	if err := c.Object(nmName, nmPath).Call(nmIface+".GetDevices", 0).Store(&devs); err != nil {
		return "", err
	}
	for _, d := range devs {
		if t, err := prop[uint32](c.Object(nmName, d), nmIface+".Device.DeviceType"); err == nil && t == nmDeviceWifi {
			return d, nil
		}
	}
	return "", nil
}

func (n nmNetwork) State() (NetState, error) {
	c, err := n.conn()
	if err != nil {
		return NetState{}, err
	}
	defer c.Close()
	nm := c.Object(nmName, nmPath)
	st, err := prop[uint32](nm, nmIface+".State")
	if err != nil {
		return NetState{}, err
	}
	out := NetState{Connected: nmConnected(st)}
	if dev, err := wifiDevice(c); err == nil && dev != "" {
		out.HasWifi = true
	}
	if out.Connected {
		if ac, err := prop[dbus.ObjectPath](nm, nmIface+".PrimaryConnection"); err == nil && ac != "/" {
			out.Name, _ = prop[string](c.Object(nmName, ac), nmIface+".Connection.Active.Id")
		}
	}
	out.WifiOn, _ = prop[bool](nm, nmIface+".WirelessEnabled")
	var devs []dbus.ObjectPath
	if err := nm.Call(nmIface+".GetDevices", 0).Store(&devs); err == nil {
		for _, p := range devs {
			o := c.Object(nmName, p)
			t, _ := prop[uint32](o, nmIface+".Device.DeviceType")
			if t == nmDeviceModem {
				out.Modems = append(out.Modems, modem(c, p))
				continue
			}
			kind := deviceKind(t)
			if kind == "" {
				continue
			}
			d := Device{Kind: kind}
			d.Iface, _ = prop[string](o, nmIface+".Device.Interface")
			s, _ := prop[uint32](o, nmIface+".Device.State")
			d.State = deviceState(s)
			if ac, err := prop[dbus.ObjectPath](o, nmIface+".Device.ActiveConnection"); err == nil && ac != "/" {
				d.Connection, _ = prop[string](c.Object(nmName, ac), nmIface+".Connection.Active.Id")
			}
			if a, err := prop[uint32](o, nmIface+".Device.Ip4Address"); err == nil && a != 0 {
				d.Address = ipv4(a)
			}
			out.Devices = append(out.Devices, d)
		}
	}
	out.Saved = savedWifi(c)
	return out, nil
}

// savedWifi is the SSIDs of the saved Wi-Fi connections, sorted, once each.
func savedWifi(c *dbus.Conn) []string {
	conns := wifiConnections(c)
	seen := map[string]bool{}
	var out []string
	for _, w := range conns {
		if !seen[w.ssid] {
			seen[w.ssid] = true
			out = append(out, w.ssid)
		}
	}
	sort.Strings(out)
	return out
}

type wifiConn struct {
	path dbus.ObjectPath
	ssid string
}

// wifiConnections lists the saved connections that are Wi-Fi, with their
// SSID.
func wifiConnections(c *dbus.Conn) []wifiConn {
	var paths []dbus.ObjectPath
	if err := c.Object(nmName, nmPath+"/Settings").Call(nmIface+".Settings.ListConnections", 0).Store(&paths); err != nil {
		return nil
	}
	var out []wifiConn
	for _, p := range paths {
		var s settings
		if err := c.Object(nmName, p).Call(nmIface+".Settings.Connection.GetSettings", 0).Store(&s); err != nil {
			continue
		}
		if ssid, ok := wifiSSID(s); ok {
			out = append(out, wifiConn{p, ssid})
		}
	}
	return out
}

// wifiSSID is the SSID of a Wi-Fi connection's settings; ok is false for
// any other kind of connection.
func wifiSSID(s settings) (string, bool) {
	if t, _ := s["connection"]["type"].Value().(string); t != "802-11-wireless" {
		return "", false
	}
	b, ok := s["802-11-wireless"]["ssid"].Value().([]byte)
	return string(b), ok
}

func (n nmNetwork) Forget(ssid string) error {
	c, err := n.conn()
	if err != nil {
		return err
	}
	defer c.Close()
	found := false
	for _, w := range wifiConnections(c) {
		if w.ssid != ssid {
			continue
		}
		found = true
		if err := c.Object(nmName, w.path).Call(nmIface+".Settings.Connection.Delete", 0).Err; err != nil {
			return err
		}
	}
	if !found {
		return fmt.Errorf(i18n.T("%s is not a saved network"), ssid)
	}
	return nil
}

func (n nmNetwork) Disconnect() error {
	c, err := n.conn()
	if err != nil {
		return err
	}
	defer c.Close()
	dev, err := wifiDevice(c)
	if err != nil {
		return err
	}
	if dev == "" {
		return errors.New(i18n.T("No Wi-Fi device"))
	}
	return c.Object(nmName, dev).Call(nmIface+".Device.Disconnect", 0).Err
}

func (n nmNetwork) SetWifi(on bool) error {
	c, err := n.conn()
	if err != nil {
		return err
	}
	defer c.Close()
	return c.Object(nmName, nmPath).SetProperty(nmIface+".WirelessEnabled", dbus.MakeVariant(on))
}

func (n nmNetwork) AccessPoints() ([]AccessPoint, error) {
	c, err := n.conn()
	if err != nil {
		return nil, err
	}
	defer c.Close()
	dev, err := wifiDevice(c)
	if err != nil || dev == "" {
		return nil, err
	}
	w := c.Object(nmName, dev)
	_ = w.Call(nmIface+".Device.Wireless.RequestScan", 0, map[string]dbus.Variant{}).Err // may be throttled
	var paths []dbus.ObjectPath
	if err := w.Call(nmIface+".Device.Wireless.GetAccessPoints", 0).Store(&paths); err != nil {
		return nil, err
	}
	var aps []AccessPoint
	for _, p := range paths {
		o := c.Object(nmName, p)
		ssid, err := prop[[]byte](o, nmIface+".AccessPoint.Ssid")
		if err != nil {
			continue
		}
		strength, _ := prop[byte](o, nmIface+".AccessPoint.Strength")
		flags, _ := prop[uint32](o, nmIface+".AccessPoint.Flags")
		wpa, _ := prop[uint32](o, nmIface+".AccessPoint.WpaFlags")
		rsn, _ := prop[uint32](o, nmIface+".AccessPoint.RsnFlags")
		aps = append(aps, AccessPoint{SSID: string(ssid), Strength: strength, Secured: apSecured(flags, wpa, rsn)})
	}
	return dedupeAPs(aps), nil
}

// apRSN is the RSN flags of the access point named ssid on device dev, 0
// when it is not in range (a hidden network).
func apRSN(c *dbus.Conn, dev dbus.ObjectPath, ssid string) uint32 {
	var paths []dbus.ObjectPath
	if err := c.Object(nmName, dev).Call(nmIface+".Device.Wireless.GetAccessPoints", 0).Store(&paths); err != nil {
		return 0
	}
	for _, p := range paths {
		o := c.Object(nmName, p)
		if s, err := prop[[]byte](o, nmIface+".AccessPoint.Ssid"); err == nil && string(s) == ssid {
			rsn, _ := prop[uint32](o, nmIface+".AccessPoint.RsnFlags")
			return rsn
		}
	}
	return 0
}

// errWrongPassword is a secured connection that NetworkManager dropped
// while activating: nearly always a wrong key.
var errWrongPassword error = nmTextError(i18n.Mark("Wrong password"))

// nmTextError is an error whose English message is translated when shown.
type nmTextError string

func (e nmTextError) Error() string { return i18n.T(string(e)) }

// connectFailure is the error of a connection that NetworkManager dropped
// while activating. Only a network joined with a WPA-PSK password can say
// the password was wrong.
func connectFailure(ssid, password string) error {
	if password != "" {
		return errWrongPassword
	}
	return fmt.Errorf(i18n.T("Could not connect to %s"), ssid)
}

func (n nmNetwork) Connect(ssid, password string, hidden bool) error {
	c, err := n.conn()
	if err != nil {
		return err
	}
	defer c.Close()
	dev, err := wifiDevice(c)
	if err != nil {
		return err
	}
	if dev == "" {
		return errors.New(i18n.T("No Wi-Fi device"))
	}
	var settingsPath, active dbus.ObjectPath
	err = c.Object(nmName, nmPath).Call(nmIface+".AddAndActivateConnection", 0,
		wifiSettings(ssid, password, hidden, keyMgmt(apRSN(c, dev, ssid))), dev, dbus.ObjectPath("/")).Store(&settingsPath, &active)
	if err != nil {
		return err
	}
	deadline := time.Now().Add(45 * time.Second)
	for time.Now().Before(deadline) {
		st, err := prop[uint32](c.Object(nmName, active), nmIface+".Connection.Active.State")
		if err != nil {
			st = nmDeactivated // the object went away
		}
		switch st {
		case nmActivated:
			return nil
		case nmDeactivated:
			// Do not keep a profile that does not work.
			_ = c.Object(nmName, settingsPath).Call(nmIface+".Settings.Connection.Delete", 0).Err
			return connectFailure(ssid, password)
		}
		time.Sleep(500 * time.Millisecond)
	}
	_ = c.Object(nmName, nmPath).Call(nmIface+".DeactivateConnection", 0, active).Err
	_ = c.Object(nmName, settingsPath).Call(nmIface+".Settings.Connection.Delete", 0).Err
	return fmt.Errorf(i18n.T("Timed out connecting to %s"), ssid)
}
