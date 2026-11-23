package main

import (
	"errors"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"
)

type btObjs = map[dbus.ObjectPath]map[string]map[string]dbus.Variant

func vv(x any) dbus.Variant { return dbus.MakeVariant(x) }

func btAdapter(powered, discovering bool) map[string]map[string]dbus.Variant {
	return map[string]map[string]dbus.Variant{
		"org.bluez.Adapter1": {"Powered": vv(powered), "Discovering": vv(discovering)},
	}
}

func btDevice(name, icon string, paired, connected bool) map[string]map[string]dbus.Variant {
	return map[string]map[string]dbus.Variant{
		"org.bluez.Device1": {
			"Alias": vv(name), "Icon": vv(icon), "Paired": vv(paired),
			"Connected": vv(connected), "Adapter": vv(dbus.ObjectPath("/org/bluez/hci0")),
		},
	}
}

func fakeTree() btObjs {
	paired := btDevice("Headset", "audio-headset", true, true)
	paired["org.bluez.Device1"]["Address"] = vv("38:18:4C:AA:BB:CC")
	paired["org.bluez.Battery1"] = map[string]dbus.Variant{"Percentage": vv(uint8(80))}
	return btObjs{
		"/org/bluez/hci0":                 btAdapter(true, true),
		"/org/bluez/hci0/dev_ZZ":          btDevice("Zebra mouse", "input-mouse", false, false),
		"/org/bluez/hci0/dev_AA":          paired,
		"/org/bluez/hci0/dev_BB":          btDevice("Another", "phone", false, false),
		"/org/freedesktop/NetworkManager": {"org.freedesktop.NetworkManager": {}},
	}
}

func TestBluetoothState(t *testing.T) {
	st := stateFromObjects(fakeTree())
	if !st.Adapter || !st.Powered || !st.Discovering {
		t.Fatalf("adapter flags: %+v", st)
	}
	want := []BTDevice{
		{Path: "/org/bluez/hci0/dev_AA", Address: "38:18:4C:AA:BB:CC", Name: "Headset", Icon: "audio-headset", Paired: true, Connected: true, Battery: 80},
		{Path: "/org/bluez/hci0/dev_BB", Name: "Another", Icon: "phone", Battery: -1},
		{Path: "/org/bluez/hci0/dev_ZZ", Name: "Zebra mouse", Icon: "input-mouse", Battery: -1},
	}
	if !reflect.DeepEqual(st.Devices, want) {
		t.Fatalf("got %+v\nwant %+v", st.Devices, want)
	}
}

func TestBluetoothNoAdapter(t *testing.T) {
	b := &bluezBluetooth{dial: func() (btBus, error) { return &fakeBTBus{objs: btObjs{}}, nil }}
	st, err := b.State()
	if err != nil || st.Adapter || len(st.Devices) != 0 {
		t.Fatalf("got %+v, %v", st, err)
	}
}

func TestBluetoothPoweredOff(t *testing.T) {
	st := stateFromObjects(btObjs{"/org/bluez/hci0": btAdapter(false, false)})
	if !st.Adapter || st.Powered || st.Discovering {
		t.Fatalf("got %+v", st)
	}
}

type btCall struct {
	path   dbus.ObjectPath
	method string
	args   []any
}

type fakeBTBus struct {
	objs   btObjs
	calls  []btCall
	failOn string
	agent  bool
	closed bool
}

func (f *fakeBTBus) Objects() (btObjs, error) { return f.objs, nil }
func (f *fakeBTBus) Call(p dbus.ObjectPath, m string, args ...any) error {
	f.calls = append(f.calls, btCall{p, m, args})
	if m == f.failOn {
		return errors.New("boom")
	}
	return nil
}
func (f *fakeBTBus) SetProp(p dbus.ObjectPath, iface, name string, val any) error {
	f.calls = append(f.calls, btCall{p, "Set " + iface + "." + name, []any{val}})
	return nil
}
func (f *fakeBTBus) Export(v any, p dbus.ObjectPath, iface string) error {
	f.agent = true
	f.calls = append(f.calls, btCall{p, "Export", nil})
	return nil
}
func (f *fakeBTBus) Unexport(p dbus.ObjectPath, iface string) {
	f.agent = false
	f.calls = append(f.calls, btCall{p, "Unexport", nil})
}
func (f *fakeBTBus) Close() { f.closed = true }

func fakeBT(f *fakeBTBus) *bluezBluetooth {
	return &bluezBluetooth{dial: func() (btBus, error) { return f, nil }}
}

func TestForgetCallsRemoveDevice(t *testing.T) {
	f := &fakeBTBus{objs: fakeTree()}
	if err := fakeBT(f).Forget("/org/bluez/hci0/dev_AA"); err != nil {
		t.Fatal(err)
	}
	want := btCall{"/org/bluez/hci0", "org.bluez.Adapter1.RemoveDevice", []any{dbus.ObjectPath("/org/bluez/hci0/dev_AA")}}
	if len(f.calls) != 1 || !reflect.DeepEqual(f.calls[0], want) {
		t.Fatalf("calls %+v, want %+v", f.calls, want)
	}
}

func TestPairUnregistersAgentWhenPairFails(t *testing.T) {
	f := &fakeBTBus{objs: fakeTree(), failOn: "org.bluez.Device1.Pair"}
	if err := fakeBT(f).Pair("/org/bluez/hci0/dev_BB"); err == nil {
		t.Fatal("want the Pair error")
	}
	if f.agent {
		t.Fatal("agent still exported")
	}
	var names []string
	for _, c := range f.calls {
		names = append(names, c.method)
	}
	want := []string{"Export", "org.bluez.AgentManager1.RegisterAgent", "org.bluez.AgentManager1.RequestDefaultAgent",
		"org.bluez.Device1.Pair", "org.bluez.AgentManager1.UnregisterAgent", "Unexport"}
	if !slices.Equal(names, want) {
		t.Fatalf("calls %v, want %v", names, want)
	}
}

func TestAgentNilPromptRefuses(t *testing.T) {
	a := &btAgent{}
	if _, e := a.RequestPinCode("/d"); e == nil {
		t.Error("PIN not refused")
	}
	if _, e := a.RequestPasskey("/d"); e == nil {
		t.Error("passkey not refused")
	}
	if e := a.RequestConfirmation("/d", 123456); e == nil {
		t.Error("confirm not refused")
	}
	if e := a.RequestAuthorization("/d"); e == nil {
		t.Error("authorization not refused")
	}
	if e := a.AuthorizeService("/d", "uuid"); e == nil {
		t.Error("service not refused")
	}
	a.DisplayPinCode("/d", "1234")
	a.DisplayPasskey("/d", 1, 0)
	a.Cancel()
}

type rec struct {
	ok       bool
	confirms []uint32
	auths    int
	shown    []string
	cancels  int
	dev      BTDevice
}

func (p *rec) PinCode(d BTDevice) (string, bool) { p.dev = d; return "0000", p.ok }
func (p *rec) Passkey(d BTDevice) (uint32, bool) { p.dev = d; return 42, p.ok }
func (p *rec) Confirm(d BTDevice, k uint32) bool { p.confirms = append(p.confirms, k); return p.ok }
func (p *rec) Authorize(d BTDevice) bool         { p.auths++; return p.ok }
func (p *rec) Display(d BTDevice, c string)      { p.shown = append(p.shown, c) }
func (p *rec) Cancel()                           { p.cancels++ }

func TestAgentUsesPrompt(t *testing.T) {
	p := &rec{ok: true}
	a := &btAgent{prompt: p, devs: map[string]BTDevice{"/d": {Path: "/d", Name: "Headset"}}}
	if pin, e := a.RequestPinCode("/d"); e != nil || pin != "0000" || p.dev.Name != "Headset" {
		t.Errorf("pin %q %v dev %+v", pin, e, p.dev)
	}
	if k, e := a.RequestPasskey("/d"); e != nil || k != 42 {
		t.Errorf("passkey %d %v", k, e)
	}
	if e := a.RequestConfirmation("/d", 1); e != nil {
		t.Error(e)
	}
	a = &btAgent{prompt: &rec{}}
	if e := a.RequestConfirmation("/d", 1); e == nil {
		t.Error("rejected confirm accepted")
	}
}

func TestAuthorizeNoCode(t *testing.T) {
	p := &rec{ok: true}
	if e := (&btAgent{prompt: p}).RequestAuthorization("/d"); e != nil {
		t.Fatal(e)
	}
	if p.auths != 1 || len(p.confirms) != 0 {
		t.Fatalf("auths %d, confirms %v", p.auths, p.confirms)
	}
}

func TestDisplayPasskeyFormatsSixDigits(t *testing.T) {
	p := &rec{}
	a := &btAgent{prompt: p}
	a.DisplayPasskey("/d", 123, 0)
	a.DisplayPinCode("/d", "0042")
	if !slices.Equal(p.shown, []string{"000123", "0042"}) {
		t.Fatalf("shown %v", p.shown)
	}
}

func TestCancelReachesPrompt(t *testing.T) {
	p := &rec{}
	(&btAgent{prompt: p}).Cancel()
	if p.cancels != 1 {
		t.Fatalf("cancels %d", p.cancels)
	}
}

func TestPairCancelsPromptOnReturn(t *testing.T) {
	for _, fail := range []string{"", "org.bluez.Device1.Pair"} {
		p := &rec{}
		b := fakeBT(&fakeBTBus{objs: fakeTree(), failOn: fail})
		b.SetPairPrompt(p)
		_ = b.Pair("/org/bluez/hci0/dev_BB")
		if p.cancels != 1 {
			t.Errorf("fail %q: cancels %d", fail, p.cancels)
		}
	}
}

func TestPairTrustsDevice(t *testing.T) {
	f := &fakeBTBus{objs: fakeTree()}
	if err := fakeBT(f).Pair("/org/bluez/hci0/dev_BB"); err != nil {
		t.Fatal(err)
	}
	want := btCall{"/org/bluez/hci0/dev_BB", "Set org.bluez.Device1.Trusted", []any{true}}
	if got := f.calls[len(f.calls)-3]; !reflect.DeepEqual(got, want) {
		t.Fatalf("calls %+v", f.calls)
	}
	f = &fakeBTBus{objs: fakeTree(), failOn: "org.bluez.Device1.Pair"}
	_ = fakeBT(f).Pair("/org/bluez/hci0/dev_BB")
	for _, c := range f.calls {
		if c.method == "Set org.bluez.Device1.Trusted" {
			t.Fatal("trusted after a failed pairing")
		}
	}
}

func TestSortIgnoresCase(t *testing.T) {
	st := stateFromObjects(btObjs{
		"/org/bluez/hci0":       btAdapter(true, false),
		"/org/bluez/hci0/dev_1": btDevice("beta", "", false, false),
		"/org/bluez/hci0/dev_2": btDevice("Alpha", "", false, false),
		"/org/bluez/hci0/dev_3": btDevice("Zed", "", false, false),
	})
	var names []string
	for _, d := range st.Devices {
		names = append(names, d.Name)
	}
	if !slices.Equal(names, []string{"Alpha", "beta", "Zed"}) {
		t.Fatalf("names %v", names)
	}
}

type fakeTimer struct {
	f     func()
	d     time.Duration
	armed bool
}

func (t *fakeTimer) Stop() bool                 { was := t.armed; t.armed = false; return was }
func (t *fakeTimer) Reset(d time.Duration) bool { was := t.armed; t.armed = true; t.d = d; return was }
func (t *fakeTimer) fire()                      { t.armed = false; t.f() }

func discoverFake(f *fakeBTBus) (*bluezBluetooth, *fakeTimer, *int) {
	tm := &fakeTimer{}
	dials := 0
	b := &bluezBluetooth{
		dial: func() (btBus, error) { dials++; return f, nil },
		after: func(d time.Duration, fn func()) btTimer {
			tm.f, tm.d, tm.armed = fn, d, true
			return tm
		},
	}
	return b, tm, &dials
}

func countCalls(f *fakeBTBus, m string) (n int) {
	for _, c := range f.calls {
		if c.method == m {
			n++
		}
	}
	return n
}

func TestDiscoverKeepsConnectionUntilTimer(t *testing.T) {
	f := &fakeBTBus{objs: fakeTree()}
	b, tm, _ := discoverFake(f)
	if err := b.Discover(10 * time.Second); err != nil {
		t.Fatal(err)
	}
	if f.closed || countCalls(f, "org.bluez.Adapter1.StartDiscovery") != 1 || countCalls(f, "org.bluez.Adapter1.StopDiscovery") != 0 {
		t.Fatalf("closed %v calls %+v", f.closed, f.calls)
	}
	tm.fire()
	if !f.closed || countCalls(f, "org.bluez.Adapter1.StopDiscovery") != 1 {
		t.Fatalf("after the timer: closed %v calls %+v", f.closed, f.calls)
	}
}

func TestDiscoverTwiceExtendsDeadline(t *testing.T) {
	f := &fakeBTBus{objs: fakeTree()}
	b, tm, dials := discoverFake(f)
	_ = b.Discover(10 * time.Second)
	_ = b.Discover(30 * time.Second)
	if *dials != 1 || countCalls(f, "org.bluez.Adapter1.StartDiscovery") != 1 {
		t.Fatalf("dials %d calls %+v", *dials, f.calls)
	}
	if tm.d != 30*time.Second || !tm.armed || f.closed {
		t.Fatalf("timer %+v closed %v", tm, f.closed)
	}
	// The first timer firing while the second call raced it must not stop the scan.
	tm.armed = false
	_ = b.Discover(20 * time.Second) // Stop reports "already fired"
	tm.f()
	if f.closed || tm.d != 20*time.Second || !tm.armed {
		t.Fatalf("re-arm: timer %+v closed %v", tm, f.closed)
	}
}

func TestBluetoothDeviceAddress(t *testing.T) {
	// A device with no name is shown by its address.
	d := btDevice("", "", false, false)
	d["org.bluez.Device1"]["Address"] = vv("00:11:22:33:44:55")
	delete(d["org.bluez.Device1"], "Alias")
	st := stateFromObjects(btObjs{"/org/bluez/hci0": btAdapter(true, false), "/org/bluez/hci0/dev_X": d})
	if len(st.Devices) != 1 || st.Devices[0].Address != "00:11:22:33:44:55" || st.Devices[0].Name != "00:11:22:33:44:55" {
		t.Fatalf("got %+v", st.Devices)
	}
}
