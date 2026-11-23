package main

import (
	"errors"
	"sync"
	"testing"
)

// fakeNet is a Network with canned answers.
type fakeNet struct {
	mu         sync.Mutex
	state      NetState
	aps        []AccessPoint
	err        error // Connect fails with it
	stateErr   error // State fails with it
	apsErr     error // AccessPoints fails with it
	connected  []string
	passwords  []string
	hiddenSeen []bool
	lag        bool // State still says offline after Connect
}

func (f *fakeNet) State() (NetState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.state, f.stateErr
}

func (f *fakeNet) AccessPoints() ([]AccessPoint, error) { return f.aps, f.apsErr }

func (f *fakeNet) Connect(ssid, password string, hidden bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.connected = append(f.connected, ssid)
	f.passwords = append(f.passwords, password)
	f.hiddenSeen = append(f.hiddenSeen, hidden)
	if f.err != nil {
		return f.err
	}
	if !f.lag {
		f.state = NetState{Connected: true, Name: ssid, HasWifi: true}
	}
	return nil
}

var homeAPs = []AccessPoint{
	{SSID: "Home", Strength: 82, Secured: true},
	{SSID: "Neighbour", Strength: 55, Secured: true},
	{SSID: "Cafe Guest", Strength: 38},
}

// networkAt is the Network page, loaded from n.
func networkAt(u *UI, n Network) *networkPage {
	u.hooks.Network = n
	to(u, PNetwork)
	p := u.page().(*networkPage)
	p.started = true
	p.load()
	return p
}

func TestNetworkConnectedShowsName(t *testing.T) {
	u, _ := newRecorded(NewFlow(Sway), Sway)
	p := networkAt(u, &fakeNet{state: NetState{Connected: true, Name: "Home", HasWifi: true}})
	if p.view() != netConnected {
		t.Fatalf("view %v", p.view())
	}
}

func TestNetworkNoDevice(t *testing.T) {
	u, _ := newRecorded(NewFlow(Sway), Sway)
	if v := networkAt(u, &fakeNet{}).view(); v != netNoDevice {
		t.Fatalf("view %v", v)
	}
}

func TestNetworkConnectFailureKeepsPasswordAndShowsError(t *testing.T) {
	u, _ := newRecorded(NewFlow(Sway), Sway)
	n := &fakeNet{state: NetState{HasWifi: true}, aps: homeAPs, err: errors.New("Wrong password")}
	p := networkAt(u, n)
	p.choose("Home")
	p.pass.Editor.SetText("nope")
	p.connect()
	p.wait()
	if p.pass.Err != "Wrong password" || p.view() != netList {
		t.Fatalf("err %q view %v", p.pass.Err, p.view())
	}
	if n.passwords[0] != "nope" || n.hiddenSeen[0] {
		t.Fatalf("connect args %v %v", n.passwords, n.hiddenSeen)
	}
}

func TestNetworkConnectSuccess(t *testing.T) {
	u, _ := newRecorded(NewFlow(Sway), Sway)
	n := &fakeNet{state: NetState{HasWifi: true}, aps: homeAPs}
	p := networkAt(u, n)
	p.choose("Cafe Guest") // open: no password needed
	p.connect()
	p.wait()
	if p.view() != netConnected {
		t.Fatalf("view %v", p.view())
	}
	p.Continue()
	if u.flow.Current() == PNetwork || len(u.flow.Outcome.Applied) == 0 {
		t.Fatal("Continue did not apply and move on")
	}
}

func TestNetworkHiddenSendsFlag(t *testing.T) {
	u, _ := newRecorded(NewFlow(Sway), Sway)
	n := &fakeNet{state: NetState{HasWifi: true}, aps: homeAPs}
	p := networkAt(u, n)
	p.hiddenMode()
	p.ssid.Editor.SetText("Secret")
	p.pass.Editor.SetText("pw")
	p.connect()
	p.wait()
	if len(n.hiddenSeen) != 1 || !n.hiddenSeen[0] || n.connected[0] != "Secret" {
		t.Fatalf("%v %v", n.connected, n.hiddenSeen)
	}
}

func TestNetworkContinueWorksOffline(t *testing.T) {
	u, _ := newRecorded(NewFlow(Sway), Sway)
	p := networkAt(u, &fakeNet{})
	p.Continue()
	if u.flow.Current() == PNetwork {
		t.Fatal("Continue stayed")
	}
}

func TestNetworkConnectedWithoutWaitingForGlobalState(t *testing.T) {
	u, _ := newRecorded(NewFlow(Sway), Sway)
	n := &fakeNet{state: NetState{HasWifi: true}, aps: homeAPs, lag: true, stateErr: nil}
	p := networkAt(u, n)
	p.choose("Cafe Guest")
	n.stateErr = errors.New("bus gone") // State must not be needed after Connect
	p.connect()
	p.wait()
	if p.view() != netConnected || p.st.Name != "Cafe Guest" || !p.st.HasWifi {
		t.Fatalf("view %v state %+v", p.view(), p.st)
	}
}

func TestNetworkFirstLoadErrorOffersTryAgain(t *testing.T) {
	u, _ := newRecorded(NewFlow(Sway), Sway)
	n := &fakeNet{stateErr: errors.New("no bus")}
	p := networkAt(u, n)
	if p.view() != netError {
		t.Fatalf("view %v", p.view())
	}
	n.mu.Lock()
	n.stateErr, n.state, n.aps = nil, NetState{HasWifi: true}, homeAPs
	n.mu.Unlock()
	p.load()
	if p.view() != netList {
		t.Fatalf("after retry view %v", p.view())
	}
}

func TestNetworkRescanErrorKeepsTheList(t *testing.T) {
	u, _ := newRecorded(NewFlow(Sway), Sway)
	n := &fakeNet{state: NetState{HasWifi: true}, aps: homeAPs}
	p := networkAt(u, n)
	n.apsErr = errors.New("scan failed")
	p.load()
	if p.view() != netList || len(p.aps) != len(homeAPs) {
		t.Fatalf("view %v aps %d", p.view(), len(p.aps))
	}
	n.stateErr = errors.New("no bus")
	p.load()
	if p.view() != netList || !p.st.HasWifi {
		t.Fatalf("view %v state %+v", p.view(), p.st)
	}
}
