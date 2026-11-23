package main

import (
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"
)

// fakeObex is obexd on the session bus.
type fakeObex struct {
	mu       sync.Mutex
	calls    []string
	createEr error
	sendEr   error
	status   string // the Status SendFile returns
	size     uint64
	ch       chan obexChange
	closed   bool
	chErr    error         // Changes fails with it
	block    chan struct{} // CreateSession waits for it to close
}

const fakeTransfer = dbus.ObjectPath("/org/bluez/obex/client/session0/transfer0")

func newFakeObex() *fakeObex {
	return &fakeObex{ch: make(chan obexChange, 16), status: "queued", size: 1000}
}

func (f *fakeObex) rec(s string) { f.mu.Lock(); f.calls = append(f.calls, s); f.mu.Unlock() }
func (f *fakeObex) called() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.calls)
}
func (f *fakeObex) CreateSession(dest string) (dbus.ObjectPath, error) {
	f.rec("CreateSession " + dest)
	if f.block != nil {
		<-f.block
	}
	return "/org/bluez/obex/client/session0", f.createEr
}
func (f *fakeObex) SendFile(s dbus.ObjectPath, file string) (dbus.ObjectPath, map[string]dbus.Variant, error) {
	f.rec("SendFile " + file)
	p := map[string]dbus.Variant{"Status": dbus.MakeVariant(f.status)}
	if f.size > 0 {
		p["Size"] = dbus.MakeVariant(f.size)
	}
	return fakeTransfer, p, f.sendEr
}
func (f *fakeObex) Changes(dbus.ObjectPath) (<-chan obexChange, func(), error) {
	return f.ch, func() {}, f.chErr
}
func (f *fakeObex) Cancel(dbus.ObjectPath) error        { f.rec("Cancel"); return nil }
func (f *fakeObex) RemoveSession(dbus.ObjectPath) error { f.rec("RemoveSession"); return nil }
func (f *fakeObex) Close()                              { f.closed = true }

func (f *fakeObex) change(status string, n uint64) {
	p := map[string]dbus.Variant{}
	if status != "" {
		p["Status"] = dbus.MakeVariant(status)
	}
	if n > 0 {
		p["Transferred"] = dbus.MakeVariant(n)
	}
	f.ch <- obexChange{fakeTransfer, p}
}

func obexBT(f *fakeObex) *bluezBluetooth {
	b := fakeBT(&fakeBTBus{objs: fakeTree()})
	b.dialObex = func() (obexBus, error) { return f, nil }
	b.idle = 2 * time.Second
	return b
}

const sendDev = "/org/bluez/hci0/dev_AA"

func TestSendFileProgress(t *testing.T) {
	f := newFakeObex()
	f.change("active", 250)
	f.change("active", 700)
	f.change("complete", 1000)
	var got [][2]int64
	err := obexBT(f).SendFile(sendDev, "/home/u/a.jpg", func(s, n int64) { got = append(got, [2]int64{s, n}) })
	if err != nil {
		t.Fatal(err)
	}
	want := [][2]int64{{-1, 1000}, {250, 1000}, {700, 1000}, {1000, 1000}, {1000, 1000}}
	if !slices.Equal(got, want) {
		t.Fatalf("progress %v, want %v", got, want)
	}
	c := f.called()
	if len(c) != 3 || c[0][:14] != "CreateSession " || c[1] != "SendFile /home/u/a.jpg" || c[2] != "RemoveSession" {
		t.Fatalf("calls %v", c)
	}
	if !f.closed {
		t.Error("session bus left open")
	}
}

func TestSendFileError(t *testing.T) {
	f := newFakeObex()
	f.change("error", 0)
	err := obexBT(f).SendFile(sendDev, "/a", nil)
	if err == nil || err.Error() != "the device did not accept the file" {
		t.Fatalf("err %v", err)
	}
	if c := f.called(); c[len(c)-1] != "RemoveSession" {
		t.Fatalf("calls %v", c)
	}
}

func TestSendFileSendFails(t *testing.T) {
	f := newFakeObex()
	f.sendEr = errors.New("boom")
	if err := obexBT(f).SendFile(sendDev, "/a", nil); err == nil {
		t.Fatal("want an error")
	}
	if c := f.called(); c[len(c)-1] != "RemoveSession" {
		t.Fatalf("calls %v", c)
	}
}

func TestSendFileNoObex(t *testing.T) {
	b := obexBT(nil)
	b.dialObex = func() (obexBus, error) { return nil, errors.New("no session bus") }
	err := b.SendFile(sendDev, "/a", nil)
	if err == nil || err.Error() != "File transfer is not available" {
		t.Fatalf("no bus: %v", err)
	}
	f := newFakeObex()
	f.createEr = dbus.Error{Name: "org.freedesktop.DBus.Error.ServiceUnknown"}
	err = obexBT(f).SendFile(sendDev, "/a", nil)
	if err == nil || err.Error() != "File transfer is not available" {
		t.Fatalf("no service: %v", err)
	}
}

func TestSendFileCancelRemovesSession(t *testing.T) {
	f := newFakeObex()
	b := obexBT(f)
	f.change("active", 100)
	done := make(chan error, 1)
	started := make(chan struct{}, 1)
	go func() {
		done <- b.SendFile(sendDev, "/a", func(s, _ int64) {
			if s == 100 {
				started <- struct{}{}
			}
		})
	}()
	<-started
	b.CancelSend()
	select {
	case err := <-done:
		if !errors.Is(err, errSendCanceled) {
			t.Fatalf("err %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("cancel did not end the send")
	}
	c := f.called()
	if len(c) < 2 || c[len(c)-2] != "Cancel" || c[len(c)-1] != "RemoveSession" {
		t.Fatalf("calls %v", c)
	}
	b.CancelSend() // none running: no panic
}

func TestSendFileIdleTimeout(t *testing.T) {
	f := newFakeObex()
	b := obexBT(f)
	b.idle = 50 * time.Millisecond
	b.queueWait = 50 * time.Millisecond // the fake's transfer stays queued
	err := b.SendFile(sendDev, "/a", nil)
	if err == nil {
		t.Fatal("want a timeout")
	}
	if c := f.called(); c[len(c)-2] != "Cancel" || c[len(c)-1] != "RemoveSession" {
		t.Fatalf("calls %v", c)
	}
}

func TestSendFileConnectionLost(t *testing.T) {
	f := newFakeObex()
	f.change("active", 10)
	close(f.ch)
	err := obexBT(f).SendFile(sendDev, "/a", nil)
	if err == nil || err.Error() != "The connection to file transfer was lost." {
		t.Fatalf("err %v", err)
	}
	if c := f.called(); c[len(c)-1] != "RemoveSession" {
		t.Fatalf("calls %v", c)
	}
}

func TestSendFileWatchFails(t *testing.T) {
	f := newFakeObex()
	f.chErr = errors.New("no match")
	if err := obexBT(f).SendFile(sendDev, "/a", nil); err == nil {
		t.Fatal("want an error")
	}
	c := f.called()
	if slices.Contains(c, "SendFile /a") || c[len(c)-1] != "RemoveSession" {
		t.Fatalf("calls %v", c)
	}
}

func TestSendFileOneAtATime(t *testing.T) {
	f := newFakeObex()
	b := obexBT(f)
	f.change("active", 1)
	started := make(chan struct{}, 1)
	done := make(chan error, 1)
	go func() {
		done <- b.SendFile(sendDev, "/a", func(s, _ int64) {
			if s == 1 {
				started <- struct{}{}
			}
		})
	}()
	<-started
	err := b.SendFile(sendDev, "/b", nil)
	if err == nil || err.Error() != "A file is already being sent." {
		t.Fatalf("err %v", err)
	}
	b.CancelSend()
	<-done
	f2 := newFakeObex()
	f2.change("complete", 0)
	b.dialObex = func() (obexBus, error) { return f2, nil }
	if err := b.SendFile(sendDev, "/c", nil); err != nil {
		t.Fatalf("after the first ended: %v", err)
	}
}

func TestSendFileQueuedWaitsLonger(t *testing.T) {
	f := newFakeObex()
	f.status = "queued"
	b := obexBT(f)
	b.idle = 30 * time.Millisecond
	b.queueWait = 2 * time.Second
	var got []int64
	var mu sync.Mutex
	go func() {
		time.Sleep(200 * time.Millisecond) // longer than idle
		f.change("active", 5)
		f.change("complete", 0)
	}()
	err := b.SendFile(sendDev, "/a", func(s, _ int64) { mu.Lock(); got = append(got, s); mu.Unlock() })
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(got) == 0 || got[0] != -1 {
		t.Fatalf("progress %v: want the first call to say waiting (-1)", got)
	}
}

func TestSendFileCancelDuringCreateSession(t *testing.T) {
	f := newFakeObex()
	f.block = make(chan struct{})
	b := obexBT(f)
	done := make(chan error, 1)
	go func() { done <- b.SendFile(sendDev, "/a", nil) }()
	eventually(t, "CreateSession", func() bool { return len(f.called()) == 1 })
	b.CancelSend()
	if err := <-done; !errors.Is(err, errSendCanceled) {
		t.Fatalf("err %v", err)
	}
	close(f.block)
	eventually(t, "RemoveSession of the late session", func() bool { c := f.called(); return c[len(c)-1] == "RemoveSession" })
}
