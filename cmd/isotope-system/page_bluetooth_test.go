package main

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kairos-community/isotope/pkg/guikit"
)

// fakeBlue is a Bluetooth for the page tests.
type fakeBlue struct {
	mu     sync.Mutex
	st     BTState
	calls  []string
	states int
	prompt PairPrompt
	// pairing, when set, runs inside Pair (it asks the prompt things).
	pairing func(PairPrompt) error
}

func (f *fakeBlue) rec(s string) { f.mu.Lock(); f.calls = append(f.calls, s); f.mu.Unlock() }
func (f *fakeBlue) called() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}
func (f *fakeBlue) State() (BTState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.states++
	return f.st, nil
}
func (f *fakeBlue) SetPowered(on bool) error {
	f.rec("SetPowered " + map[bool]string{true: "on", false: "off"}[on])
	return nil
}
func (f *fakeBlue) Discover(time.Duration) error { f.rec("Discover"); return nil }
func (f *fakeBlue) SetPairPrompt(p PairPrompt)   { f.mu.Lock(); f.prompt = p; f.mu.Unlock() }
func (f *fakeBlue) Connect(p string) error       { f.rec("Connect " + p); return nil }
func (f *fakeBlue) Disconnect(p string) error    { f.rec("Disconnect " + p); return nil }
func (f *fakeBlue) Forget(p string) error        { f.rec("Forget " + p); return nil }
func (f *fakeBlue) SendFile(p, file string, _ func(int64, int64)) error {
	f.rec("SendFile " + p + " " + file)
	return nil
}
func (f *fakeBlue) CancelSend() { f.rec("CancelSend") }
func (f *fakeBlue) Pair(p string) error {
	f.rec("Pair " + p)
	f.mu.Lock()
	prompt, fn := f.prompt, f.pairing
	f.mu.Unlock()
	if fn != nil {
		return fn(prompt)
	}
	return nil
}

var btHeadset = BTDevice{Path: "/org/bluez/hci0/dev_AA", Address: "38:18:4C:AA:BB:CC", Name: "WH-1000XM4", Icon: "audio-headset", Paired: true, Connected: true, Battery: 80}
var btMouse = BTDevice{Path: "/org/bluez/hci0/dev_ZZ", Name: "Zebra mouse", Icon: "input-mouse", Battery: -1}

func btTestUI(t *testing.T, st BTState) (*UI, *fakeBlue, *btPage) {
	t.Helper()
	u, _, _ := testUI(t, true)
	f := &fakeBlue{st: st}
	u.hooks.Bluetooth = f
	p := u.view(PageBluetooth).(*btPage)
	p.Shown()
	eventually(t, "the first load", func() bool { u.mu.Lock(); defer u.mu.Unlock(); return p.loaded })
	return u, f, p
}

func TestBluetoothPageStates(t *testing.T) {
	withTools(t)
	for _, c := range []struct {
		name string
		st   BTState
		want []string
	}{
		{"no adapter", BTState{}, []string{"No Bluetooth adapter"}},
		{"off", BTState{Adapter: true}, []string{"Bluetooth is off"}},
		{"on", BTState{Adapter: true, Powered: true, Devices: []BTDevice{btHeadset, btMouse}},
			[]string{"Bluetooth is on", "WH-1000XM4", "Zebra mouse", "Connected", "Battery 80%", "Not paired"}},
	} {
		u, _, p := btTestUI(t, c.st)
		u.show(PageBluetooth)
		u.mu.Lock()
		v := btViewOf(true, p.st, p.profiles)
		u.mu.Unlock()
		text := v.Headline
		for _, r := range v.Rows {
			text += "|" + r.Dev.Name + "|" + r.Sub + "|" + strings.Join(r.Actions, ",")
		}
		for _, w := range c.want {
			if !strings.Contains(text, w) {
				t.Errorf("%s: %q is not in %q", c.name, w, text)
			}
		}
		// It draws, large and small.
		for _, sz := range [][2]int{{1100, 720}, {700, 500}} {
			u.Layout(gtxOf(sz[0], sz[1]))
		}
	}
	// With no Bluetooth hook at all.
	u, _, _ := testUI(t, true)
	u.show(PageBluetooth)
	u.Layout(gtxOf(1100, 720))
	if v := btViewOf(false, BTState{}, nil); v.Headline != "No Bluetooth adapter" {
		t.Fatalf("headline %q", v.Headline)
	}
}

func TestBluetoothRowActions(t *testing.T) {
	v := btViewOf(true, BTState{Adapter: true, Powered: true, Devices: []BTDevice{btHeadset, btMouse,
		{Path: "p", Name: "Old", Paired: true, Battery: -1}}}, nil)
	want := [][]string{{"Send file…", "Disconnect", "Forget"}, {"Pair"}, {"Connect", "Send file…", "Forget"}}
	for i, r := range v.Rows {
		if strings.Join(r.Actions, ",") != strings.Join(want[i], ",") {
			t.Errorf("%s: %v, want %v", r.Dev.Name, r.Actions, want[i])
		}
	}
}

func TestCodecChoiceOnlySBC(t *testing.T) {
	f := withTools(t)
	// The second card has only SBC (and the headset profile, which is not
	// a sound-quality choice).
	sbc := btHeadset
	sbc.Path, sbc.Address = "/org/bluez/hci0/dev_SB", "00:11:22:33:44:55"
	_, _, p := btTestUI(t, BTState{Adapter: true, Powered: true, Devices: []BTDevice{btHeadset, sbc}})
	p.u.mu.Lock()
	v := btViewOf(true, p.st, p.profiles)
	p.u.mu.Unlock()
	if len(v.Rows) != 2 || len(v.Rows[0].Codecs) != 5 || len(v.Rows[1].Codecs) != 0 {
		t.Fatalf("codecs: %+v", v.Rows)
	}
	for _, c := range f.called() {
		if strings.HasPrefix(c, "wpctl set-profile") {
			t.Fatalf("SetProfile ran: %q", c)
		}
	}
	// A draw with a single profile has no choice control and sets nothing.
	p.u.Layout(gtxOf(1100, 720))
	for _, c := range f.called() {
		if strings.HasPrefix(c, "wpctl set-profile") {
			t.Fatalf("SetProfile ran: %q", c)
		}
	}
}

func TestCodecChoiceSetsTheProfile(t *testing.T) {
	f := withTools(t)
	_, _, p := btTestUI(t, BTState{Adapter: true, Powered: true, Devices: []BTDevice{btHeadset}})
	p.setCodec(btHeadset, Profile{Name: "a2dp-sink-ldac", Label: "LDAC"})
	eventually(t, "set-profile", func() bool {
		for _, c := range f.called() {
			if c == "wpctl set-profile 52 4" {
				return true
			}
		}
		return false
	})
}

func TestBluetoothPageActions(t *testing.T) {
	withTools(t)
	u, f, p := btTestUI(t, BTState{Adapter: true, Powered: true, Devices: []BTDevice{btHeadset, btMouse}})
	p.do("x", func(bt Bluetooth) error { return bt.SetPowered(false) })
	p.scan()
	p.pair(btMouse)
	eventually(t, "the calls", func() bool { return len(f.called()) >= 4 })
	got := strings.Join(f.called(), ",")
	for _, w := range []string{"SetPowered off", "Discover", "Pair " + btMouse.Path, "Connect " + btMouse.Path} {
		if !strings.Contains(got, w) {
			t.Errorf("%q is not in %q", w, got)
		}
	}
	// Forget asks first.
	p.forget(btHeadset)
	q := waitQuestion(t, u, "Forget WH-1000XM4?")
	u.pick(q, 0)
	if strings.Contains(strings.Join(f.called(), ","), "Forget ") {
		t.Fatal("Cancel forgot the device")
	}
	p.forget(btHeadset)
	u.pick(waitQuestion(t, u, "Forget WH-1000XM4?"), 1)
	eventually(t, "Forget", func() bool { return strings.Contains(strings.Join(f.called(), ","), "Forget "+btHeadset.Path) })
}

func TestPairPromptConfirm(t *testing.T) {
	u, _, _ := testUI(t, true)
	pr := newBTPrompt(u)
	res := make(chan bool, 1)
	go func() { res <- pr.Confirm(btMouse, 123456) }()
	d := waitQuestion(t, u, "Pair with Zebra mouse?")
	if !strings.Contains(d.body, "123 456") {
		t.Fatalf("body %q", d.body)
	}
	u.pick(d, 1) // Yes
	select {
	case ok := <-res:
		if !ok {
			t.Fatal("Yes returned false")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Confirm did not return")
	}
	// No refuses.
	go func() { res <- pr.Authorize(btMouse) }()
	u.pick(waitQuestion(t, u, "Pair with"), 0)
	if ok := <-res; ok {
		t.Fatal("No returned true")
	}
}

func TestPairPromptCancelUnblocksAndIsIdempotent(t *testing.T) {
	u, _, _ := testUI(t, true)
	pr := newBTPrompt(u)
	res := make(chan bool, 1)
	go func() { res <- pr.Confirm(btMouse, 1) }()
	waitQuestion(t, u, "Pair with")
	pr.Cancel()
	pr.Cancel() // BlueZ, then Pair returning
	if ok := <-res; ok {
		t.Fatal("a cancelled question said yes")
	}
	if u.question() != nil {
		t.Fatal("the dialog stayed open")
	}
	// Another dialog is not closed by a late Cancel.
	u.tell("Other", "x")
	pr.Cancel()
	if u.question() == nil {
		t.Fatal("Cancel closed a dialog that is not the prompt's")
	}
}

func TestPairPromptPinAndDisplay(t *testing.T) {
	u, _, _ := testUI(t, true)
	pr := newBTPrompt(u)
	type pin struct {
		s  string
		ok bool
	}
	res := make(chan pin, 1)
	go func() { s, ok := pr.PinCode(btMouse); res <- pin{s, ok} }()
	d := waitQuestion(t, u, "Pair with Zebra mouse")
	if d.choices[1].enabled() {
		t.Fatal("Pair is on with no PIN")
	}
	u.pick(d, 0)
	if r := <-res; r.ok {
		t.Fatalf("Cancel gave %+v", r)
	}
	pr.Display(btMouse, "004242")
	if q := u.question(); q == nil || !strings.Contains(q.body, "004242") {
		t.Fatalf("display dialog: %+v", q)
	}
	pr.Display(btMouse, "")
	if u.question() != nil {
		t.Fatal("an empty code did not clear the display")
	}
	_ = guikit.ButtonPrimary
}

// Pair blocks on the prompt; the page must stay responsive meanwhile.
func TestPairingDoesNotBlockTheUI(t *testing.T) {
	withTools(t)
	u, f, p := btTestUI(t, BTState{Adapter: true, Powered: true, Devices: []BTDevice{btMouse}})
	answered := make(chan bool, 1)
	f.mu.Lock()
	f.pairing = func(pr PairPrompt) error { answered <- pr.Confirm(btMouse, 654321); return nil }
	f.mu.Unlock()
	p.pair(btMouse)
	q := waitQuestion(t, u, "Pair with Zebra mouse?") // the UI goroutine is free to see it
	u.Layout(gtxOf(1100, 720))                        // and to draw with it open
	u.pick(q, 1)
	if !<-answered {
		t.Fatal("Yes was not passed to Pair")
	}
	eventually(t, "Connect after Pair", func() bool { return strings.Contains(strings.Join(f.called(), ","), "Connect "+btMouse.Path) })
}

func TestScanKeepsRefreshingUntilTheDeadline(t *testing.T) {
	withTools(t)
	// BlueZ says Discovering false right after StartDiscovery.
	_, f, p := btTestUI(t, BTState{Adapter: true, Powered: true})
	p.poll, p.window = time.Millisecond, 60*time.Millisecond
	p.u.mu.Lock()
	p.u.page = PageBluetooth
	p.u.mu.Unlock()
	f.mu.Lock()
	before := f.states
	f.mu.Unlock()
	p.scan()
	eventually(t, "several refreshes", func() bool { f.mu.Lock(); defer f.mu.Unlock(); return f.states >= before+4 })
}

func TestSendPath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.WriteFile(filepath.Join(home, "photo.jpg"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(home, "dir"), 0o700); err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(home, "photo.jpg")
	for _, in := range []string{"~/photo.jpg", "photo.jpg", want, "  ~/photo.jpg "} {
		if got := sendPath(in); got != want {
			t.Errorf("%q: %q, want %q", in, got, want)
		}
	}
	for _, in := range []string{"", "~/nothing", "dir", "~", "/nonexistent/file"} {
		if got := sendPath(in); got != "" {
			t.Errorf("%q: %q, want none", in, got)
		}
	}
}

func TestSendDialogEnablesSendOnlyForAFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	_, _, p := btTestUI(t, BTState{Adapter: true, Powered: true, Devices: []BTDevice{btHeadset}})
	p.sendFile(btHeadset)
	d := waitQuestion(t, p.u, "Send a file")
	if d.choices[1].enabled() {
		t.Fatal("Send is on with no file")
	}
}

func TestPairPromptCancelWhileBuildingNeverOpens(t *testing.T) {
	u, _, _ := testUI(t, true)
	pr := newBTPrompt(u)
	_, ok := pr.ask(func(answer func(any, bool)) *dialog {
		pr.Cancel() // BlueZ cancels after the question began, before it opened
		return newDialog("Pair with X?", "", nil, 0, choice{Label: "No"})
	})
	if ok || u.question() != nil {
		t.Fatalf("ok %v, dialog %v", ok, u.question())
	}
}

func TestBTSendText(t *testing.T) {
	for _, c := range []struct {
		sent, total int64
		want        string
	}{{250, 1000, "Sending… 25%"}, {0, 1000, "Sending… 0%"}, {-1, 1000, "Waiting for the device to accept…"}, {5000, 0, "Sending… 5 KB sent"}, {2500000, 0, "Sending… 2.5 MB sent"}} {
		if got := btSendText(c.sent, c.total); got != c.want {
			t.Errorf("%d/%d: %q, want %q", c.sent, c.total, got, c.want)
		}
	}
}
