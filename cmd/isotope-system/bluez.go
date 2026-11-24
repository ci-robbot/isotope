package main

// BlueZ on the system bus, for the Bluetooth page. State comes from one
// ObjectManager.GetManagedObjects call; stateFromObjects turns that tree
// into the page's model and needs no D-Bus, so the tests feed it a fake
// tree.

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/godbus/dbus/v5"

	"github.com/kairos-community/isotope/pkg/i18n"
)

// BTDevice is one Bluetooth device BlueZ knows.
type BTDevice struct {
	Path      string // D-Bus object path, the identity
	Address   string // Device1.Address, AA:BB:CC:DD:EE:FF; the key of its PipeWire card
	Name      string
	Icon      string // BlueZ Icon: audio-headset, input-keyboard, phone, ...
	Paired    bool
	Connected bool
	Battery   int // 0-100, -1 unknown
}

// BTState is what the Bluetooth page shows.
type BTState struct {
	Adapter     bool // an adapter exists
	Powered     bool
	Discovering bool
	Devices     []BTDevice // paired first, then by name
}

// Bluetooth is BlueZ, as far as the page uses it. Every method blocks on
// D-Bus; the page calls them off the UI thread.
type Bluetooth interface {
	State() (BTState, error)
	SetPowered(on bool) error
	Discover(d time.Duration) error // StartDiscovery, StopDiscovery after d
	// SetPairPrompt sets who answers BlueZ's PIN, passkey and confirm
	// requests during Pair. Without one, Pair refuses all of them.
	SetPairPrompt(p PairPrompt)
	Pair(path string) error // runs the page's Agent1 for prompts
	Connect(path string) error
	Disconnect(path string) error
	Forget(path string) error // Adapter1.RemoveDevice
	// SendFile sends file to the device with obexd (Client1 + ObjectPush1);
	// progress is called as bytes go out.
	SendFile(path, file string, progress func(sent, total int64)) error
	// CancelSend stops the send in progress; SendFile then returns.
	CancelSend()
}

// PairPrompt is how the page answers BlueZ's Agent1 while pairing. The
// bool is false when the user refuses or cancels.
type PairPrompt interface {
	PinCode(dev BTDevice) (string, bool)
	Passkey(dev BTDevice) (uint32, bool)
	Confirm(dev BTDevice, passkey uint32) bool // a real 6-digit code to compare
	Authorize(dev BTDevice) bool               // RequestAuthorization: no code
	Display(dev BTDevice, code string)         // the code the user types on the device; "" clears it
	Cancel()                                   // Agent1.Cancel, and when Pair returns
}

const (
	bluezName      = "org.bluez"
	bluezAdapter   = "org.bluez.Adapter1"
	bluezDevice    = "org.bluez.Device1"
	bluezBattery   = "org.bluez.Battery1"
	bluezAgentMgr  = "org.bluez.AgentManager1"
	bluezAgentIf   = "org.bluez.Agent1"
	bluezAgentPath = dbus.ObjectPath("/io/kairos/IsotopeSystem/agent")
)

func variant[T any](m map[string]dbus.Variant, k string) (T, bool) {
	var zero T
	x, ok := m[k]
	if !ok {
		return zero, false
	}
	t, ok := x.Value().(T)
	return t, ok
}

// pickAdapter is the first adapter in path order, "" for none.
func pickAdapter(objs map[dbus.ObjectPath]map[string]map[string]dbus.Variant) dbus.ObjectPath {
	adapter := dbus.ObjectPath("")
	for p, ifs := range objs {
		if _, ok := ifs[bluezAdapter]; ok && (adapter == "" || p < adapter) {
			adapter = p
		}
	}
	return adapter
}

// stateFromObjects builds the page's model from BlueZ's managed objects.
// The first adapter in path order counts; devices of other adapters are
// left out.
func stateFromObjects(objs map[dbus.ObjectPath]map[string]map[string]dbus.Variant) BTState {
	var st BTState
	adapter := pickAdapter(objs)
	if adapter == "" {
		return st
	}
	a := objs[adapter][bluezAdapter]
	st.Adapter = true
	st.Powered, _ = variant[bool](a, "Powered")
	st.Discovering, _ = variant[bool](a, "Discovering")
	for p, ifs := range objs {
		d, ok := ifs[bluezDevice]
		if !ok {
			continue
		}
		if ap, ok := variant[dbus.ObjectPath](d, "Adapter"); ok && ap != adapter {
			continue
		}
		dev := BTDevice{Path: string(p), Battery: -1}
		dev.Address, _ = variant[string](d, "Address")
		dev.Name, _ = variant[string](d, "Alias")
		if dev.Name == "" {
			dev.Name, _ = variant[string](d, "Name")
		}
		if dev.Name == "" {
			dev.Name = dev.Address
		}
		dev.Icon, _ = variant[string](d, "Icon")
		dev.Paired, _ = variant[bool](d, "Paired")
		dev.Connected, _ = variant[bool](d, "Connected")
		if b, ok := ifs[bluezBattery]; ok {
			if pct, ok := variant[uint8](b, "Percentage"); ok {
				dev.Battery = int(pct)
			}
		}
		st.Devices = append(st.Devices, dev)
	}
	sort.Slice(st.Devices, func(i, j int) bool {
		a, b := st.Devices[i], st.Devices[j]
		if a.Paired != b.Paired {
			return a.Paired
		}
		if la, lb := strings.ToLower(a.Name), strings.ToLower(b.Name); la != lb {
			return la < lb
		}
		if a.Name != b.Name {
			return a.Name < b.Name
		}
		return a.Path < b.Path
	})
	return st
}

// btBus is the slice of D-Bus the model uses; the tests fake it.
type btBus interface {
	Objects() (map[dbus.ObjectPath]map[string]map[string]dbus.Variant, error)
	// Call calls an org.bluez method; method is "interface.Name".
	Call(path dbus.ObjectPath, method string, args ...any) error
	SetProp(path dbus.ObjectPath, iface, name string, val any) error
	Export(val any, path dbus.ObjectPath, iface string) error
	Unexport(path dbus.ObjectPath, iface string)
	Close()
}

type sysBTBus struct{ c *dbus.Conn }

func dialBluez() (btBus, error) {
	c, err := dbus.ConnectSystemBus()
	if err != nil {
		return nil, err
	}
	return sysBTBus{c}, nil
}

func (b sysBTBus) Objects() (map[dbus.ObjectPath]map[string]map[string]dbus.Variant, error) {
	var out map[dbus.ObjectPath]map[string]map[string]dbus.Variant
	err := b.c.Object(bluezName, "/").Call("org.freedesktop.DBus.ObjectManager.GetManagedObjects", 0).Store(&out)
	return out, err
}
func (b sysBTBus) Call(p dbus.ObjectPath, method string, args ...any) error {
	return b.c.Object(bluezName, p).Call(method, 0, args...).Err
}
func (b sysBTBus) SetProp(p dbus.ObjectPath, iface, name string, val any) error {
	return b.c.Object(bluezName, p).SetProperty(iface+"."+name, dbus.MakeVariant(val))
}
func (b sysBTBus) Export(val any, p dbus.ObjectPath, iface string) error {
	return b.c.Export(val, p, iface)
}
func (b sysBTBus) Unexport(p dbus.ObjectPath, iface string) {
	_ = b.c.Export(nil, p, iface)
}
func (b sysBTBus) Close() { _ = b.c.Close() }

// bluezBluetooth is Bluetooth over org.bluez on the system bus.
type bluezBluetooth struct {
	dial func() (btBus, error) // dialBluez; the tests swap it

	// after arms a timer; the tests swap it.
	after func(d time.Duration, f func()) btTimer

	dialObex  func() (obexBus, error) // dialObex; the tests swap it
	idle      time.Duration           // a send's idle limit; sendIdle when 0
	queueWait time.Duration           // how long a send may stay queued; sendQueueWait when 0

	mu         sync.Mutex
	sending    bool
	cancelSend chan struct{} // closed by CancelSend; set while a send runs
	prompt     PairPrompt
	disc       *discovery
}

// btTimer is the part of *time.Timer Discover uses.
type btTimer interface {
	Stop() bool
	Reset(d time.Duration) bool
}

// discovery is a running scan. BlueZ ends a client's scan when its
// connection closes, so the connection stays open until the scan stops.
type discovery struct {
	conn    btBus
	adapter dbus.ObjectPath
	timer   btTimer
	rearm   time.Duration // set when Discover extended a timer that had fired
}

func newBluez() *bluezBluetooth {
	return &bluezBluetooth{dial: dialBluez, after: func(d time.Duration, f func()) btTimer { return time.AfterFunc(d, f) }}
}

func (b *bluezBluetooth) SetPairPrompt(p PairPrompt) {
	b.mu.Lock()
	b.prompt = p
	b.mu.Unlock()
}

// withBus dials, runs f and closes.
func (b *bluezBluetooth) withBus(f func(btBus) error) error {
	c, err := b.dial()
	if err != nil {
		return err
	}
	defer c.Close()
	return f(c)
}

// adapterOf is the adapter path: the first one in the tree.
func adapterOf(c btBus) (dbus.ObjectPath, error) {
	objs, err := c.Objects()
	if err != nil {
		return "", err
	}
	adapter := pickAdapter(objs)
	if adapter == "" {
		return "", errors.New(i18n.T("no Bluetooth adapter"))
	}
	return adapter, nil
}

func (b *bluezBluetooth) State() (st BTState, err error) {
	err = b.withBus(func(c btBus) error {
		objs, err := c.Objects()
		st = stateFromObjects(objs)
		return err
	})
	return st, err
}

func (b *bluezBluetooth) SetPowered(on bool) error {
	return b.withBus(func(c btBus) error {
		a, err := adapterOf(c)
		if err != nil {
			return err
		}
		return c.SetProp(a, bluezAdapter, "Powered", on)
	})
}

// Discover starts discovery and returns. It stops after d, and a second
// call while it runs moves that deadline to d from now.
func (b *bluezBluetooth) Discover(d time.Duration) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if ds := b.disc; ds != nil {
		if ds.timer.Stop() {
			ds.timer.Reset(d)
		} else {
			ds.rearm = d // the timer func is about to run; it re-arms
		}
		return nil
	}
	c, err := b.dial()
	if err != nil {
		return err
	}
	adapter, err := adapterOf(c)
	if err == nil {
		err = c.Call(adapter, bluezAdapter+".StartDiscovery")
	}
	if err != nil {
		c.Close()
		return err
	}
	ds := &discovery{conn: c, adapter: adapter}
	ds.timer = b.after(d, func() { b.stopDiscovery(ds) })
	b.disc = ds
	return nil
}

func (b *bluezBluetooth) stopDiscovery(ds *discovery) {
	b.mu.Lock()
	if ds.rearm > 0 {
		d := ds.rearm
		ds.rearm = 0
		ds.timer.Reset(d)
		b.mu.Unlock()
		return
	}
	if b.disc == ds {
		b.disc = nil
	}
	b.mu.Unlock()
	_ = ds.conn.Call(ds.adapter, bluezAdapter+".StopDiscovery")
	ds.conn.Close()
}

func (b *bluezBluetooth) Connect(path string) error {
	return b.withBus(func(c btBus) error { return c.Call(dbus.ObjectPath(path), bluezDevice+".Connect") })
}

func (b *bluezBluetooth) Disconnect(path string) error {
	return b.withBus(func(c btBus) error { return c.Call(dbus.ObjectPath(path), bluezDevice+".Disconnect") })
}

// Forget removes the device (and its pairing) from its adapter.
func (b *bluezBluetooth) Forget(path string) error {
	return b.withBus(func(c btBus) error {
		objs, err := c.Objects()
		if err != nil {
			return err
		}
		adapter, ok := variant[dbus.ObjectPath](objs[dbus.ObjectPath(path)][bluezDevice], "Adapter")
		if !ok {
			if adapter, err = adapterOf(c); err != nil {
				return err
			}
		}
		return c.Call(adapter, bluezAdapter+".RemoveDevice", dbus.ObjectPath(path))
	})
}

// Pair pairs with the device and trusts it, so a headset reconnects by
// itself. The Agent1 is on the bus only while Pair runs.
func (b *bluezBluetooth) Pair(path string) error {
	b.mu.Lock()
	prompt := b.prompt
	b.mu.Unlock()
	return b.withBus(func(c btBus) error {
		devs := map[string]BTDevice{}
		if objs, err := c.Objects(); err == nil {
			for _, d := range stateFromObjects(objs).Devices {
				devs[d.Path] = d
			}
		}
		if prompt != nil {
			// No dialog outlives the pairing.
			defer prompt.Cancel()
		}
		if err := c.Export(&btAgent{prompt: prompt, devs: devs}, bluezAgentPath, bluezAgentIf); err != nil {
			return err
		}
		defer c.Unexport(bluezAgentPath, bluezAgentIf)
		if err := c.Call("/org/bluez", bluezAgentMgr+".RegisterAgent", bluezAgentPath, "KeyboardDisplay"); err != nil {
			return err
		}
		defer func() { _ = c.Call("/org/bluez", bluezAgentMgr+".UnregisterAgent", bluezAgentPath) }()
		if err := c.Call("/org/bluez", bluezAgentMgr+".RequestDefaultAgent", bluezAgentPath); err != nil {
			return err
		}
		if err := c.Call(dbus.ObjectPath(path), bluezDevice+".Pair"); err != nil {
			return err
		}
		// The device is paired; a failure to trust it is not a pairing error.
		_ = c.SetProp(dbus.ObjectPath(path), bluezDevice, "Trusted", true)
		return nil
	})
}

// btAgent is org.bluez.Agent1. A nil prompt refuses every request and
// ignores Display and Cancel. devs is the device list at the start of Pair.
type btAgent struct {
	prompt PairPrompt
	devs   map[string]BTDevice
}

var errRejected = dbus.NewError("org.bluez.Error.Rejected", []any{"rejected"})

// dev is the device for path; one BlueZ no longer lists gets the path as
// its name.
func (a *btAgent) dev(p dbus.ObjectPath) BTDevice {
	if d, ok := a.devs[string(p)]; ok {
		return d
	}
	return BTDevice{Path: string(p), Name: string(p), Battery: -1}
}

func (a *btAgent) Release() *dbus.Error { return nil }

func (a *btAgent) Cancel() *dbus.Error {
	if a.prompt != nil {
		a.prompt.Cancel()
	}
	return nil
}

func (a *btAgent) RequestPinCode(dev dbus.ObjectPath) (string, *dbus.Error) {
	if a.prompt != nil {
		if pin, ok := a.prompt.PinCode(a.dev(dev)); ok {
			return pin, nil
		}
	}
	return "", errRejected
}

func (a *btAgent) RequestPasskey(dev dbus.ObjectPath) (uint32, *dbus.Error) {
	if a.prompt != nil {
		if k, ok := a.prompt.Passkey(a.dev(dev)); ok {
			return k, nil
		}
	}
	return 0, errRejected
}

func (a *btAgent) RequestConfirmation(dev dbus.ObjectPath, passkey uint32) *dbus.Error {
	if a.prompt != nil && a.prompt.Confirm(a.dev(dev), passkey) {
		return nil
	}
	return errRejected
}

// RequestAuthorization is a pairing request with no code to compare.
func (a *btAgent) RequestAuthorization(dev dbus.ObjectPath) *dbus.Error {
	if a.prompt != nil && a.prompt.Authorize(a.dev(dev)) {
		return nil
	}
	return errRejected
}

// AuthorizeService always refuses: a service request after pairing is the
// device asking for more access, and Isotope grants none on its own.
func (a *btAgent) AuthorizeService(dev dbus.ObjectPath, uuid string) *dbus.Error {
	return errRejected
}

func (a *btAgent) DisplayPinCode(dev dbus.ObjectPath, pin string) *dbus.Error {
	if a.prompt != nil {
		a.prompt.Display(a.dev(dev), pin)
	}
	return nil
}

func (a *btAgent) DisplayPasskey(dev dbus.ObjectPath, passkey uint32, entered uint16) *dbus.Error {
	if a.prompt != nil {
		a.prompt.Display(a.dev(dev), fmt.Sprintf("%06d", passkey))
	}
	return nil
}
