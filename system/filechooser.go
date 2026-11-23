package main

// The file picker: xdg-desktop-portal's FileChooser, which every variant's
// portal routes to a backend that draws a dialog (the GTK backend on sway,
// i3 and niri, COSMIC's own on COSMIC). guikit has no file dialog of its
// own; when no portal answers, the pages fall back to a typed path.

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"github.com/godbus/dbus/v5"
)

// FileFilter is one choice of the picker's file type list: a name and
// glob patterns such as "*.conf".
type FileFilter struct {
	Name     string
	Patterns []string
}

// FileRequest is what the picker asks for.
type FileRequest struct {
	Title, Accept string // the window title and the open button's label
	Filters       []FileFilter
}

// FileChooser picks one existing file. It returns its path,
// errPickCanceled when the user closed the dialog, or errNoPortal (wrapped)
// when no picker could be shown, so the caller asks for a typed path.
type FileChooser interface {
	OpenFile(req FileRequest) (string, error)
}

var (
	errPickCanceled = errors.New("no file was chosen")
	errNoPortal     = errors.New("no file chooser portal")
)

const (
	portalName    = "org.freedesktop.portal.Desktop"
	portalPath    = dbus.ObjectPath("/org/freedesktop/portal/desktop")
	portalChooser = "org.freedesktop.portal.FileChooser"
	portalRequest = "org.freedesktop.portal.Request"
)

// portalCallLimit is how long OpenFile may take to return its handle.
var portalCallLimit = 5 * time.Second

// portalBus is the D-Bus a portalFiles talks to; a test fakes it.
type portalBus interface {
	// UniqueName is the connection's own name, such as ":1.42".
	UniqueName() string
	// Responses delivers the Response signals of the request object path.
	// Call stop when done.
	Responses(path dbus.ObjectPath) (ch <-chan *dbus.Signal, stop func(), err error)
	// OpenFile calls FileChooser.OpenFile and returns the request handle.
	OpenFile(ctx context.Context, title string, options map[string]dbus.Variant) (dbus.ObjectPath, error)
	// Gone is closed when org.freedesktop.portal.Desktop loses its owner
	// (the portal died with the dialog open). Call stop when done.
	Gone() (gone <-chan struct{}, stop func(), err error)
}

// portalFiles is FileChooser over the portal.
type portalFiles struct {
	dial func() (portalBus, func(), error) // a bus and how to close it
}

func newPortalFiles() portalFiles {
	return portalFiles{dial: func() (portalBus, func(), error) {
		c, err := dbus.ConnectSessionBus()
		if err != nil {
			return nil, nil, err
		}
		return sessionPortal{c}, func() { c.Close() }, nil
	}}
}

// portalFilter is the portal's a(sa(us)) filter: a name and (kind,
// pattern) pairs, kind 0 for a glob.
type portalFilter struct {
	Name     string
	Patterns []portalPattern
}

type portalPattern struct {
	Kind    uint32
	Pattern string
}

// requestPath is the handle the portal gives a request made with token by
// the connection named sender: the documented
// /org/freedesktop/portal/desktop/request/SENDER/TOKEN, with SENDER the
// unique name without its ':' and with '.' as '_'.
func requestPath(sender, token string) dbus.ObjectPath {
	s := strings.ReplaceAll(strings.TrimPrefix(sender, ":"), ".", "_")
	return dbus.ObjectPath(string(portalPath) + "/request/" + s + "/" + token)
}

func portalToken() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return "isotope_" + hex.EncodeToString(b)
}

func (f portalFiles) OpenFile(req FileRequest) (string, error) {
	bus, closeBus, err := f.dial()
	if err != nil {
		return "", fmt.Errorf("%w: %v", errNoPortal, err)
	}
	defer closeBus()
	token := portalToken()
	// Listen before the call: the Response can come before the handle.
	want := requestPath(bus.UniqueName(), token)
	ch, stop, err := bus.Responses(want)
	if err != nil {
		return "", fmt.Errorf("%w: %v", errNoPortal, err)
	}
	defer func() { stop() }()
	// A portal that dies with the dialog open sends no Response; without
	// this the picker would wait, and stay busy, until the window closes.
	gone, stopGone, err := bus.Gone()
	if err != nil {
		return "", fmt.Errorf("%w: %v", errNoPortal, err)
	}
	defer stopGone()
	filters := make([]portalFilter, 0, len(req.Filters))
	for _, ff := range req.Filters {
		pf := portalFilter{Name: ff.Name}
		for _, p := range ff.Patterns {
			pf.Patterns = append(pf.Patterns, portalPattern{0, p})
		}
		filters = append(filters, pf)
	}
	opts := map[string]dbus.Variant{
		"handle_token": dbus.MakeVariant(token),
		"modal":        dbus.MakeVariant(true),
		"multiple":     dbus.MakeVariant(false),
	}
	if req.Accept != "" {
		opts["accept_label"] = dbus.MakeVariant(req.Accept)
	}
	if len(filters) > 0 {
		opts["filters"] = dbus.MakeVariant(filters)
		opts["current_filter"] = dbus.MakeVariant(filters[0])
	}
	// The call returns at once with the handle; only the dialog waits for
	// the user. A portal that does not answer the call in a few seconds
	// (none on the bus: ServiceUnknown; one that hangs) is no portal.
	ctx, cancel := context.WithTimeout(context.Background(), portalCallLimit)
	handle, err := bus.OpenFile(ctx, req.Title, opts)
	cancel()
	if err != nil {
		return "", fmt.Errorf("%w: %v", errNoPortal, err)
	}
	if handle != want {
		// A portal older than handle_token (before 0.9): listen on its
		// handle instead. Its Response cannot come before the user picks.
		// The first subscription stays until this one exists, so stop is
		// never nil.
		ch2, stop2, err := bus.Responses(handle)
		if err != nil {
			return "", fmt.Errorf("%w: %v", errNoPortal, err)
		}
		stop()
		ch, stop = ch2, stop2
	}
	for {
		select {
		case <-gone:
			return "", fmt.Errorf("%w: the portal left the session bus", errNoPortal)
		case s, ok := <-ch:
			if !ok {
				return "", fmt.Errorf("%w: the session bus closed", errNoPortal)
			}
			if s.Path != handle || s.Name != portalRequest+".Response" || len(s.Body) < 2 {
				continue
			}
			code, _ := s.Body[0].(uint32)
			results, _ := s.Body[1].(map[string]dbus.Variant)
			return portalResult(code, results)
		}
	}
}

// portalResult reads a Response: 0 is a choice, 1 a cancel, 2 anything
// else (the backend failed), which the caller handles as no portal.
func portalResult(code uint32, results map[string]dbus.Variant) (string, error) {
	switch code {
	case 0:
		uris, _ := results["uris"].Value().([]string)
		if len(uris) == 0 {
			return "", errPickCanceled
		}
		return fileURIPath(uris[0])
	case 1:
		return "", errPickCanceled
	}
	return "", fmt.Errorf("%w: the file chooser ended with code %d", errNoPortal, code)
}

// fileURIPath is the local path of a file:// URI, percent-decoded.
func fileURIPath(uri string) (string, error) {
	u, err := url.Parse(uri)
	if err != nil {
		return "", fmt.Errorf("the file chooser gave a bad address: %v", err)
	}
	if u.Scheme != "file" || (u.Host != "" && u.Host != "localhost") || !filepath.IsAbs(u.Path) {
		return "", fmt.Errorf("the file chooser gave %s, which is not a local file", uri)
	}
	return filepath.Clean(u.Path), nil
}

// sessionPortal is portalBus over a session bus connection.
type sessionPortal struct{ c *dbus.Conn }

func (s sessionPortal) UniqueName() string { return s.c.Names()[0] }

func (s sessionPortal) Responses(path dbus.ObjectPath) (<-chan *dbus.Signal, func(), error) {
	match := []dbus.MatchOption{dbus.WithMatchObjectPath(path), dbus.WithMatchInterface(portalRequest), dbus.WithMatchMember("Response")}
	if err := s.c.AddMatchSignal(match...); err != nil {
		return nil, nil, err
	}
	ch := make(chan *dbus.Signal, 4)
	s.c.Signal(ch)
	return ch, func() {
		s.c.RemoveSignal(ch)
		_ = s.c.RemoveMatchSignal(match...)
	}, nil
}

func (s sessionPortal) Gone() (<-chan struct{}, func(), error) {
	match := []dbus.MatchOption{dbus.WithMatchSender("org.freedesktop.DBus"), dbus.WithMatchInterface("org.freedesktop.DBus"),
		dbus.WithMatchMember("NameOwnerChanged"), dbus.WithMatchArg(0, portalName)}
	if err := s.c.AddMatchSignal(match...); err != nil {
		return nil, nil, err
	}
	sig := make(chan *dbus.Signal, 8)
	s.c.Signal(sig)
	gone, done := make(chan struct{}), make(chan struct{})
	go func() {
		for {
			select {
			case <-done:
				return
			case v, ok := <-sig:
				if !ok {
					return
				}
				if v.Name != "org.freedesktop.DBus.NameOwnerChanged" || len(v.Body) < 3 {
					continue
				}
				if name, _ := v.Body[0].(string); name != portalName {
					continue
				}
				if owner, _ := v.Body[2].(string); owner == "" {
					close(gone)
					return
				}
			}
		}
	}()
	return gone, func() {
		s.c.RemoveSignal(sig)
		_ = s.c.RemoveMatchSignal(match...)
		close(done)
	}, nil
}

func (s sessionPortal) OpenFile(ctx context.Context, title string, options map[string]dbus.Variant) (dbus.ObjectPath, error) {
	var handle dbus.ObjectPath
	err := s.c.Object(portalName, portalPath).CallWithContext(ctx, portalChooser+".OpenFile", 0, "", title, options).Store(&handle)
	return handle, err
}

// chooseFile asks for a file off the UI goroutine. picked gets the chosen
// path; typed runs instead when there is no picker, to ask for a typed
// path. A cancel does nothing. One picker at a time.
func (u *UI) chooseFile(req FileRequest, picked func(path string), typed func()) {
	fc := u.hooks.Files
	if fc == nil {
		typed()
		return
	}
	u.mu.Lock()
	if u.choosing {
		u.mu.Unlock()
		return
	}
	u.choosing = true
	u.mu.Unlock()
	u.async(func() {
		path, err := fc.OpenFile(req)
		u.mu.Lock()
		u.choosing = false
		u.mu.Unlock()
		switch {
		case err == nil:
			picked(path)
		case errors.Is(err, errPickCanceled):
		case errors.Is(err, errNoPortal):
			typed()
		default:
			u.tell(req.Title, err.Error())
		}
	})
}
