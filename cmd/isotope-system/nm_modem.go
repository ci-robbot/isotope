package main

// Mobile broadband through NetworkManager. ModemManager is the `modemmanager`
// extension; once it runs, NetworkManager's WWAN plugin shows each modem as
// a device of type modem. The page talks only to NetworkManager, as for
// Wi-Fi: ModemManager's own D-Bus policy is for root and the admin group.

import (
	"fmt"
	"strings"
	"time"

	"github.com/godbus/dbus/v5"
)

const nmDeviceModem = 8 // NM_DEVICE_TYPE_MODEM

// Modem is one mobile broadband device.
type Modem struct {
	// ID is the NetworkManager device's object path.
	ID         string
	Iface      string
	State      string
	Connection string
	Address    string
	// Operator is the network the modem registered on (MCC and MNC).
	Operator string
	// APN is the saved connection's APN, "" when it is looked up
	// automatically or there is no saved connection.
	APN string
	// Connected and Ready say which button the row shows: Disconnect, or
	// Connect (only when the modem can connect).
	Connected, Ready bool
}

// modemState is the device state in words, for a modem: NetworkManager
// keeps it unavailable until the modem is registered on a network, which
// a locked SIM or no signal prevents.
func modemState(s uint32) string {
	if s == 20 {
		return "Not ready: no signal, or the SIM is locked"
	}
	return deviceState(s)
}

// mobileConnection says whether a connection's settings are mobile
// broadband (gsm for 3GPP, cdma for 3GPP2).
func mobileConnection(s settings) bool {
	t, _ := s["connection"]["type"].Value().(string)
	return t == "gsm" || t == "cdma"
}

// modemAPN is the APN of a gsm connection, "" for an automatic one.
func modemAPN(s settings) string {
	if auto, _ := s["gsm"]["auto-config"].Value().(bool); auto {
		return ""
	}
	apn, _ := s["gsm"]["apn"].Value().(string)
	return apn
}

// withAPN sets the APN of a saved connection's settings, or turns it back
// to automatic (gsm.auto-config: the APN from mobile-broadband-provider-info
// for the network the modem registered on) when apn is empty. It changes s.
func withAPN(s settings, apn string) settings {
	g := s["gsm"]
	if g == nil {
		g = map[string]dbus.Variant{}
		s["gsm"] = g
	}
	if apn == "" {
		delete(g, "apn")
		g["auto-config"] = dbus.MakeVariant(true)
	} else {
		g["apn"] = dbus.MakeVariant(apn)
		g["auto-config"] = dbus.MakeVariant(false)
	}
	return s
}

// newModemSettings is a new mobile broadband connection with the APN, or
// automatic. It is gsm: an APN is a 3GPP thing.
func newModemSettings(apn string) settings {
	return withAPN(settings{
		"connection": {
			"type":        dbus.MakeVariant("gsm"),
			"id":          dbus.MakeVariant("Mobile broadband"),
			"autoconnect": dbus.MakeVariant(true),
		},
		"ipv4": {"method": dbus.MakeVariant("auto")},
		"ipv6": {"method": dbus.MakeVariant("auto")},
	}, apn)
}

// checkAPN refuses what no operator uses as an APN: an APN is a DNS-like
// name (letters, digits, dots, dashes, underscores), at most 100 characters.
func checkAPN(apn string) error {
	if len(apn) > 100 {
		return fmt.Errorf("An APN has at most 100 characters")
	}
	for _, r := range apn {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '.' || r == '-' || r == '_') {
			return fmt.Errorf("An APN has only letters, digits, dots, dashes and underscores")
		}
	}
	return nil
}

// savedConn is a saved connection with its settings.
type savedConn struct {
	path dbus.ObjectPath
	s    settings
}

// pickModemConnection is the modem's saved mobile broadband connection: the
// first the device lists as available, or else (a modem that is not ready,
// for which NetworkManager lists none) the first saved one bound to its
// interface or to none. So the row shows the saved APN, and "APN…" changes
// that connection instead of saving another.
func pickModemConnection(available, all []savedConn, iface string) (savedConn, bool) {
	for _, c := range available {
		if mobileConnection(c.s) {
			return c, true
		}
	}
	for _, c := range all {
		if !mobileConnection(c.s) {
			continue
		}
		if bound, _ := c.s["connection"]["interface-name"].Value().(string); bound == "" || bound == iface {
			return c, true
		}
	}
	return savedConn{}, false
}

func readConns(c *dbus.Conn, paths []dbus.ObjectPath) []savedConn {
	var out []savedConn
	for _, p := range paths {
		var s settings
		if err := c.Object(nmName, p).Call(nmIface+".Settings.Connection.GetSettings", 0).Store(&s); err == nil {
			out = append(out, savedConn{p, s})
		}
	}
	return out
}

// modemConnection is the modem's saved connection with its settings, or
// "" when there is none (pickModemConnection).
func modemConnection(c *dbus.Conn, dev dbus.ObjectPath) (dbus.ObjectPath, settings) {
	o := c.Object(nmName, dev)
	avail, _ := prop[[]dbus.ObjectPath](o, nmIface+".Device.AvailableConnections")
	if sc, ok := pickModemConnection(readConns(c, avail), nil, ""); ok {
		return sc.path, sc.s
	}
	iface, _ := prop[string](o, nmIface+".Device.Interface")
	var all []dbus.ObjectPath
	if err := c.Object(nmName, nmSettingsPath).Call(nmIface+".Settings.ListConnections", 0).Store(&all); err != nil {
		return "", nil
	}
	if sc, ok := pickModemConnection(nil, readConns(c, all), iface); ok {
		return sc.path, sc.s
	}
	return "", nil
}

// modemDevice checks that id is a modem device of NetworkManager's.
func modemDevice(c *dbus.Conn, id string) (dbus.ObjectPath, error) {
	dev := dbus.ObjectPath(id)
	if !dev.IsValid() {
		return "", fmt.Errorf("No such modem")
	}
	if t, err := prop[uint32](c.Object(nmName, dev), nmIface+".Device.DeviceType"); err != nil || t != nmDeviceModem {
		return "", fmt.Errorf("No such modem")
	}
	return dev, nil
}

// modem reads one modem device for State.
func modem(c *dbus.Conn, p dbus.ObjectPath) Modem {
	o := c.Object(nmName, p)
	m := Modem{ID: string(p)}
	m.Iface, _ = prop[string](o, nmIface+".Device.Interface")
	s, _ := prop[uint32](o, nmIface+".Device.State")
	m.State = modemState(s)
	m.Connected = s >= 40 && s <= 100
	m.Ready = s == 30
	if ac, err := prop[dbus.ObjectPath](o, nmIface+".Device.ActiveConnection"); err == nil && ac != "/" {
		m.Connection, _ = prop[string](c.Object(nmName, ac), nmIface+".Connection.Active.Id")
	}
	if a, err := prop[uint32](o, nmIface+".Device.Ip4Address"); err == nil && a != 0 {
		m.Address = ipv4(a)
	}
	m.Operator, _ = prop[string](o, nmIface+".Device.Modem.OperatorCode")
	if _, s := modemConnection(c, p); s != nil {
		m.APN = modemAPN(s)
	}
	return m
}

// modemWait is how long ConnectModem waits for NetworkManager: a modem
// registers, attaches and gets its address slower than Wi-Fi.
const modemWait = 90 * time.Second

func (n nmNetwork) ConnectModem(id string) error {
	c, err := n.conn()
	if err != nil {
		return err
	}
	defer c.Close()
	dev, err := modemDevice(c, id)
	if err != nil {
		return err
	}
	var active dbus.ObjectPath
	if p, _ := modemConnection(c, dev); p != "" {
		err = c.Object(nmName, nmPath).Call(nmIface+".ActivateConnection", 0, p, dev, dbus.ObjectPath("/")).Store(&active)
	} else {
		// No settings: NetworkManager completes a gsm or cdma connection
		// for the modem, with gsm.auto-config, and saves it.
		var saved dbus.ObjectPath
		err = c.Object(nmName, nmPath).Call(nmIface+".AddAndActivateConnection", 0,
			settings{}, dev, dbus.ObjectPath("/")).Store(&saved, &active)
	}
	if err != nil {
		return err
	}
	deadline := time.Now().Add(modemWait)
	for time.Now().Before(deadline) {
		st, err := prop[uint32](c.Object(nmName, active), nmIface+".Connection.Active.State")
		if err != nil {
			st = nmDeactivated // the object went away
		}
		switch st {
		case nmActivated:
			return nil
		case nmDeactivated:
			return errModemConnect
		}
		time.Sleep(500 * time.Millisecond)
	}
	_ = c.Object(nmName, nmPath).Call(nmIface+".DeactivateConnection", 0, active).Err
	return fmt.Errorf("Timed out connecting the modem")
}

// errModemConnect is a modem connection NetworkManager gave up on. The
// saved connection stays: its APN is the user's to fix.
var errModemConnect = fmt.Errorf("The modem did not connect. Check the APN (APN…), the SIM and the signal")

func (n nmNetwork) DisconnectModem(id string) error {
	c, err := n.conn()
	if err != nil {
		return err
	}
	defer c.Close()
	dev, err := modemDevice(c, id)
	if err != nil {
		return err
	}
	return c.Object(nmName, dev).Call(nmIface+".Device.Disconnect", 0).Err
}

func (n nmNetwork) SetModemAPN(id, apn string) error {
	apn = strings.TrimSpace(apn)
	if err := checkAPN(apn); err != nil {
		return err
	}
	c, err := n.conn()
	if err != nil {
		return err
	}
	defer c.Close()
	dev, err := modemDevice(c, id)
	if err != nil {
		return err
	}
	if p, s := modemConnection(c, dev); p != "" {
		if t, _ := s["connection"]["type"].Value().(string); t != "gsm" {
			return fmt.Errorf("This modem's network (CDMA) has no APN")
		}
		return c.Object(nmName, p).Call(nmIface+".Settings.Connection.Update", 0, withAPN(s, apn)).Err
	}
	var p dbus.ObjectPath
	return c.Object(nmName, nmSettingsPath).Call(nmIface+".Settings.AddConnection", 0, newModemSettings(apn)).Store(&p)
}
