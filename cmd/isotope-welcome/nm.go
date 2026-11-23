package main

import (
	"fmt"
	"sort"
	"time"

	"github.com/godbus/dbus/v5"
)

// NetState is what the Network page needs to know about the machine.
type NetState struct {
	Connected bool   // NetworkManager says it is online
	Name      string // the active connection, when Connected
	HasWifi   bool   // a Wi-Fi device exists
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
	return out, nil
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
var errWrongPassword = fmt.Errorf("Wrong password")

// connectFailure is the error of a connection that NetworkManager dropped
// while activating. Only a network joined with a WPA-PSK password can say
// the password was wrong.
func connectFailure(ssid, password string) error {
	if password != "" {
		return errWrongPassword
	}
	return fmt.Errorf("Could not connect to %s", ssid)
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
		return fmt.Errorf("No Wi-Fi device")
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
	return fmt.Errorf("Timed out connecting to %s", ssid)
}
