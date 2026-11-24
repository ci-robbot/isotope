package main

// Sending files over Bluetooth with obexd, which runs on the session bus
// (org.bluez.obex). The sequence is Client1.CreateSession (Target "opp"),
// ObjectPush1.SendFile, a watch on Transfer1 PropertiesChanged until Status
// is complete or error, then Client1.RemoveSession, always.

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/godbus/dbus/v5"

	"github.com/kairos-community/isotope/pkg/i18n"
)

const (
	obexName       = "org.bluez.obex"
	obexClientPath = "/org/bluez/obex"
	obexClient     = "org.bluez.obex.Client1"
	obexPush       = "org.bluez.obex.ObjectPush1"
	obexTransfer   = "org.bluez.obex.Transfer1"

	// sendIdle is how long a send may go without progress.
	sendIdle = 60 * time.Second
)

var (
	errNoObex       error = nmTextError(i18n.Mark("File transfer is not available"))
	errSendCanceled       = errors.New("sending canceled")
)

// obexChange is one Transfer1 PropertiesChanged signal.
type obexChange struct {
	Path  dbus.ObjectPath
	Props map[string]dbus.Variant
}

// obexBus is the slice of the session bus a send uses; the tests fake it.
type obexBus interface {
	CreateSession(dest string) (dbus.ObjectPath, error)
	SendFile(session dbus.ObjectPath, file string) (dbus.ObjectPath, map[string]dbus.Variant, error)
	// Changes subscribes to the Transfer1 changes under session; stop ends
	// it. The channel is closed when the connection drops.
	Changes(session dbus.ObjectPath) (ch <-chan obexChange, stop func(), err error)
	Cancel(transfer dbus.ObjectPath) error
	RemoveSession(session dbus.ObjectPath) error
	Close()
}

type sessObex struct{ c *dbus.Conn }

func dialObex() (obexBus, error) {
	c, err := dbus.ConnectSessionBus()
	if err != nil {
		return nil, err
	}
	return sessObex{c}, nil
}

func (s sessObex) CreateSession(dest string) (dbus.ObjectPath, error) {
	var sess dbus.ObjectPath
	err := s.c.Object(obexName, obexClientPath).Call(obexClient+".CreateSession", 0, dest,
		map[string]dbus.Variant{"Target": dbus.MakeVariant("opp")}).Store(&sess)
	return sess, err
}

func (s sessObex) SendFile(sess dbus.ObjectPath, file string) (dbus.ObjectPath, map[string]dbus.Variant, error) {
	var tr dbus.ObjectPath
	var props map[string]dbus.Variant
	err := s.c.Object(obexName, sess).Call(obexPush+".SendFile", 0, file).Store(&tr, &props)
	return tr, props, err
}

func (s sessObex) Changes(session dbus.ObjectPath) (<-chan obexChange, func(), error) {
	opts := []dbus.MatchOption{dbus.WithMatchSender(obexName), dbus.WithMatchPathNamespace(session),
		dbus.WithMatchInterface("org.freedesktop.DBus.Properties"), dbus.WithMatchMember("PropertiesChanged")}
	sig := make(chan *dbus.Signal, 64)
	out := make(chan obexChange, 64)
	done := make(chan struct{})
	s.c.Signal(sig)
	if err := s.c.AddMatchSignal(opts...); err != nil {
		s.c.RemoveSignal(sig)
		return nil, nil, err
	}
	go func() {
		for {
			select {
			case <-done:
				return
			case g, ok := <-sig:
				if !ok { // the connection dropped
					close(out)
					return
				}
				if len(g.Body) < 2 {
					continue
				}
				if i, _ := g.Body[0].(string); i != obexTransfer {
					continue
				}
				props, _ := g.Body[1].(map[string]dbus.Variant)
				select {
				case out <- obexChange{g.Path, props}:
				case <-done:
					return
				}
			}
		}
	}()
	return out, func() {
		close(done)
		_ = s.c.RemoveMatchSignal(opts...)
		s.c.RemoveSignal(sig)
	}, nil
}

func (s sessObex) Cancel(tr dbus.ObjectPath) error {
	return s.c.Object(obexName, tr).Call(obexTransfer+".Cancel", 0).Err
}

func (s sessObex) RemoveSession(sess dbus.ObjectPath) error {
	return s.c.Object(obexName, obexClientPath).Call(obexClient+".RemoveSession", 0, sess).Err
}

func (s sessObex) Close() { _ = s.c.Close() }

// obexMissing is true when err says obexd cannot be reached: no service,
// no owner, or no way to start it.
func obexMissing(err error) bool {
	var de dbus.Error
	if errors.As(err, &de) {
		return de.Name == "org.freedesktop.DBus.Error.ServiceUnknown" ||
			de.Name == "org.freedesktop.DBus.Error.NameHasNoOwner" ||
			strings.HasPrefix(de.Name, "org.freedesktop.DBus.Error.Spawn")
	}
	return false
}

// CancelSend cancels the send in progress; it does nothing when none runs.
func (b *bluezBluetooth) CancelSend() {
	b.mu.Lock()
	c := b.cancelSend
	b.cancelSend = nil
	b.mu.Unlock()
	if c != nil {
		close(c)
	}
}

// deviceAddress is the Address of the device at path, from BlueZ.
func (b *bluezBluetooth) deviceAddress(path string) (addr string, err error) {
	err = b.withBus(func(c btBus) error {
		objs, err := c.Objects()
		if err != nil {
			return err
		}
		a, _ := objs[dbus.ObjectPath(path)][bluezDevice]["Address"].Value().(string)
		if a == "" {
			return fmt.Errorf(i18n.T("the device %s has no address"), path)
		}
		addr = a
		return nil
	})
	return
}

// sendQueueWait is how long a transfer may stay queued: the phone asks its
// user to accept, and that takes time.
const sendQueueWait = 3 * time.Minute

// SendFile sends file to the device at path, which is its BlueZ object
// path. progress is called with the bytes sent and the size (0 when
// unknown); sent is -1 while the device has not accepted yet. It returns
// when the transfer ends, fails, is canceled with CancelSend, or goes b.idle
// without progress. Only one send runs at a time.
func (b *bluezBluetooth) SendFile(path, file string, progress func(sent, total int64)) error {
	cancel := make(chan struct{})
	b.mu.Lock()
	if b.sending {
		b.mu.Unlock()
		return errors.New(i18n.T("A file is already being sent."))
	}
	b.sending = true
	b.cancelSend = cancel
	b.mu.Unlock()
	defer func() {
		b.mu.Lock()
		b.sending = false
		b.cancelSend = nil
		b.mu.Unlock()
	}()

	addr, err := b.deviceAddress(path)
	if err != nil {
		return err
	}
	dial := b.dialObex
	if dial == nil {
		dial = dialObex
	}
	idle, queued := b.idle, b.queueWait
	if idle <= 0 {
		idle = sendIdle
	}
	if queued <= 0 {
		queued = sendQueueWait
	}
	bus, err := dial()
	if err != nil {
		return errNoObex
	}
	defer bus.Close()

	// CreateSession and SendFile block, so they run beside the cancel.
	type created struct {
		sess dbus.ObjectPath
		err  error
	}
	cr := make(chan created, 1)
	go func() {
		s, err := bus.CreateSession(addr)
		cr <- created{s, err}
	}()
	var sess dbus.ObjectPath
	select {
	case c := <-cr:
		if c.err != nil {
			if obexMissing(c.err) {
				return errNoObex
			}
			return fmt.Errorf(i18n.T("cannot connect to the device: %w"), c.err)
		}
		sess = c.sess
	case <-cancel:
		go func() { // the session may still come; it must not be left
			if c := <-cr; c.err == nil {
				_ = bus.RemoveSession(c.sess)
			}
		}()
		return errSendCanceled
	}
	// The session always goes, whatever the outcome.
	defer func() { _ = bus.RemoveSession(sess) }()

	changes, stop, err := bus.Changes(sess)
	if err != nil {
		return fmt.Errorf(i18n.T("cannot watch the transfer: %w"), err)
	}
	defer stop()

	type sent struct {
		tr    dbus.ObjectPath
		props map[string]dbus.Variant
		err   error
	}
	sr := make(chan sent, 1)
	go func() {
		tr, props, err := bus.SendFile(sess, file)
		sr <- sent{tr, props, err}
	}()
	var tr dbus.ObjectPath
	var props map[string]dbus.Variant
	select {
	case r := <-sr:
		if r.err != nil {
			return fmt.Errorf(i18n.T("cannot send the file: %w"), r.err)
		}
		tr, props = r.tr, r.props
	case <-cancel:
		return errSendCanceled // RemoveSession aborts the transfer
	}
	var total int64
	if v, ok := props["Size"]; ok {
		if s, ok := v.Value().(uint64); ok {
			total = int64(s)
		}
	}
	status := ""
	if v, ok := props["Status"]; ok {
		status, _ = v.Value().(string)
	}
	var done int64
	report := func() {
		if progress == nil {
			return
		}
		if status == "queued" {
			progress(-1, total)
		} else {
			progress(done, total)
		}
	}
	report()
	limit := func() time.Duration {
		if status == "queued" {
			return queued
		}
		return idle
	}
	timer := time.NewTimer(limit())
	defer timer.Stop()
	rearm := func() {
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		timer.Reset(limit())
	}
	for {
		switch status {
		case "complete":
			if progress != nil && total > 0 {
				progress(total, total)
			}
			return nil
		case "error":
			return errors.New(i18n.T("the device did not accept the file"))
		}
		select {
		case <-cancel:
			_ = bus.Cancel(tr)
			return errSendCanceled
		case <-timer.C:
			_ = bus.Cancel(tr)
			return errors.New(i18n.T("the device stopped answering"))
		case ch, ok := <-changes:
			if !ok {
				return errors.New(i18n.T("The connection to file transfer was lost."))
			}
			if ch.Path != tr {
				continue
			}
			if v, ok := ch.Props["Size"]; ok {
				if s, ok := v.Value().(uint64); ok {
					total = int64(s)
				}
			}
			if v, ok := ch.Props["Transferred"]; ok {
				if n, ok := v.Value().(uint64); ok {
					done = int64(n)
				}
			}
			if v, ok := ch.Props["Status"]; ok {
				status, _ = v.Value().(string)
			}
			report()
			rearm()
		}
	}
}
