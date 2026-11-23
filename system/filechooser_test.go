package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"
)

// fakePortal is a session bus with (or without) a FileChooser portal.
type fakePortal struct {
	mu      sync.Mutex
	subs    map[dbus.ObjectPath]chan *dbus.Signal
	title   string
	opts    map[string]dbus.Variant
	callErr error // OpenFile fails with it
	hang    bool  // OpenFile never answers
	oldPath bool  // the handle ignores handle_token
	// failResub makes every Responses after the first fail.
	failResub bool
	nsubs     int
	// gone, when set, is what Gone returns; silent means no Response ever.
	gone   chan struct{}
	silent bool
	// answer is the Response the portal sends, once the call returned.
	code    uint32
	results map[string]dbus.Variant
}

func (f *fakePortal) UniqueName() string { return ":1.42" }

func (f *fakePortal) Responses(p dbus.ObjectPath) (<-chan *dbus.Signal, func(), error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.subs == nil {
		f.subs = map[dbus.ObjectPath]chan *dbus.Signal{}
	}
	if f.nsubs++; f.failResub && f.nsubs > 1 {
		return nil, nil, errors.New("AddMatch refused")
	}
	ch := make(chan *dbus.Signal, 4)
	f.subs[p] = ch
	return ch, func() { f.mu.Lock(); delete(f.subs, p); f.mu.Unlock() }, nil
}

func (f *fakePortal) Gone() (<-chan struct{}, func(), error) {
	if f.gone == nil {
		return make(chan struct{}), func() {}, nil
	}
	return f.gone, func() {}, nil
}

func (f *fakePortal) OpenFile(ctx context.Context, title string, opts map[string]dbus.Variant) (dbus.ObjectPath, error) {
	if f.hang {
		<-ctx.Done()
		return "", ctx.Err()
	}
	if f.callErr != nil {
		return "", f.callErr
	}
	f.mu.Lock()
	f.title, f.opts = title, opts
	f.mu.Unlock()
	token, _ := opts["handle_token"].Value().(string)
	handle := requestPath(f.UniqueName(), token)
	if f.oldPath {
		handle = "/org/freedesktop/portal/desktop/request/1_42/t7"
	}
	if f.silent {
		return handle, nil
	}
	go func() {
		// The user answers a little later, on the handle's path.
		time.Sleep(10 * time.Millisecond)
		f.mu.Lock()
		ch := f.subs[handle]
		f.mu.Unlock()
		if ch != nil {
			ch <- &dbus.Signal{Path: "/elsewhere", Name: portalRequest + ".Response", Body: []any{uint32(0), map[string]dbus.Variant{}}}
			ch <- &dbus.Signal{Path: handle, Name: portalRequest + ".Response", Body: []any{f.code, f.results}}
		}
	}()
	return handle, nil
}

func fakeFiles(f *fakePortal) portalFiles {
	return portalFiles{dial: func() (portalBus, func(), error) { return f, func() {}, nil }}
}

func uris(u ...string) map[string]dbus.Variant {
	return map[string]dbus.Variant{"uris": dbus.MakeVariant(u)}
}

func TestPortalOpenFile(t *testing.T) {
	f := &fakePortal{results: uris("file:///home/anna/My%20VPNs/office%20berlin.conf")}
	got, err := fakeFiles(f).OpenFile(wgFileRequest)
	if err != nil || got != "/home/anna/My VPNs/office berlin.conf" {
		t.Fatalf("got %q, %v", got, err)
	}
	// What the dialog was asked for.
	if f.title != "Import a WireGuard file" {
		t.Errorf("title %q", f.title)
	}
	if v := f.opts["multiple"].Value(); v != false {
		t.Errorf("multiple %v", v)
	}
	if v := f.opts["modal"].Value(); v != true {
		t.Errorf("modal %v", v)
	}
	if v := f.opts["accept_label"].Value(); v != "Import" {
		t.Errorf("accept_label %v", v)
	}
	if sig := f.opts["filters"].Signature().String(); sig != "a(sa(us))" {
		t.Errorf("filters signature %s", sig)
	}
	fl, _ := f.opts["filters"].Value().([]portalFilter)
	if len(fl) != 1 || len(fl[0].Patterns) != 1 || fl[0].Patterns[0] != (portalPattern{0, "*.conf"}) {
		t.Errorf("filters %+v", fl)
	}
	// A portal that ignores handle_token answers on its own handle.
	f = &fakePortal{oldPath: true, results: uris("file:///tmp/a.conf")}
	if got, err := fakeFiles(f).OpenFile(wgFileRequest); err != nil || got != "/tmp/a.conf" {
		t.Fatalf("old portal: %q, %v", got, err)
	}
}

func TestPortalOpenFileCancelAndFailures(t *testing.T) {
	if _, err := fakeFiles(&fakePortal{code: 1}).OpenFile(wgFileRequest); !errors.Is(err, errPickCanceled) {
		t.Errorf("cancel: %v", err)
	}
	// Response 2: the backend failed; the caller asks for a typed path.
	if _, err := fakeFiles(&fakePortal{code: 2}).OpenFile(wgFileRequest); !errors.Is(err, errNoPortal) {
		t.Errorf("code 2: %v", err)
	}
	noPortal := dbus.Error{Name: "org.freedesktop.DBus.Error.ServiceUnknown", Body: []any{"The name is not activatable"}}
	if _, err := fakeFiles(&fakePortal{callErr: noPortal}).OpenFile(wgFileRequest); !errors.Is(err, errNoPortal) {
		t.Errorf("ServiceUnknown: %v", err)
	}
	noBus := portalFiles{dial: func() (portalBus, func(), error) { return nil, nil, errors.New("no session bus") }}
	if _, err := noBus.OpenFile(wgFileRequest); !errors.Is(err, errNoPortal) {
		t.Errorf("no bus: %v", err)
	}
	defer func(d time.Duration) { portalCallLimit = d }(portalCallLimit)
	portalCallLimit = 100 * time.Millisecond
	start := time.Now()
	if _, err := fakeFiles(&fakePortal{hang: true}).OpenFile(wgFileRequest); !errors.Is(err, errNoPortal) {
		t.Errorf("hung portal: %v", err)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("hung portal took %v", d)
	}
}

func TestFileURIPath(t *testing.T) {
	for in, want := range map[string]string{
		"file:///home/anna/a.conf":                 "/home/anna/a.conf",
		"file:///home/anna/My%20Files/b%2Bc.conf":  "/home/anna/My Files/b+c.conf",
		"file://localhost/tmp/x":                   "/tmp/x",
		"file:///run/user/1000/doc/ab12/caf%C3%A9": "/run/user/1000/doc/ab12/café",
	} {
		if got, err := fileURIPath(in); err != nil || got != want {
			t.Errorf("%s: %q, %v; want %q", in, got, err, want)
		}
	}
	for _, in := range []string{"https://example.com/a.conf", "file://server/share/a.conf", "file:relative", "%zz"} {
		if got, err := fileURIPath(in); err == nil {
			t.Errorf("%s: %q, want an error", in, got)
		}
	}
	if _, err := portalResult(0, map[string]dbus.Variant{}); !errors.Is(err, errPickCanceled) {
		t.Errorf("no uris: %v", err)
	}
}

func TestRequestPath(t *testing.T) {
	if got := requestPath(":1.42", "isotope_ab"); got != "/org/freedesktop/portal/desktop/request/1_42/isotope_ab" {
		t.Fatal(got)
	}
}

// pickFiles is a FileChooser with a fixed answer.
type pickFiles struct {
	path string
	err  error
}

func (p pickFiles) OpenFile(FileRequest) (string, error) { return p.path, p.err }

func TestImportWireGuardThroughThePicker(t *testing.T) {
	home := t.TempDir()
	conf := filepath.Join(home, "office.conf")
	if err := os.WriteFile(conf, []byte(fullConf), 0o600); err != nil {
		t.Fatal(err)
	}
	u, _, n := testUI(t, true)
	u.hooks.Files = pickFiles{path: conf}
	p := u.view(PageNetwork).(*networkPage)
	p.Shown()
	clickAndUpdate(p, &p.importWG)
	// The chosen file is filled in; only the name is left to type.
	q := waitQuestion(t, u, "Import a WireGuard file")
	if p.wgFile.Editor.Text() != conf || !q.choices[1].enabled() {
		t.Fatalf("file %q, Import on %v", p.wgFile.Editor.Text(), q.choices[1].enabled())
	}
	u.pick(q, 1)
	eventually(t, "ImportWireGuard", func() bool { return strings.Contains(strings.Join(n.called(), ","), "ImportWireGuard office ") })

	// A cancel opens nothing.
	u.closeDialog()
	u.hooks.Files = pickFiles{err: errPickCanceled}
	clickAndUpdate(p, &p.importWG)
	time.Sleep(50 * time.Millisecond)
	if q := u.question(); q != nil {
		t.Fatalf("cancel opened %q", q.title)
	}
	// No portal: the typed-path dialog.
	u.hooks.Files = pickFiles{err: errNoPortal}
	clickAndUpdate(p, &p.importWG)
	q = waitQuestion(t, u, "Import a WireGuard file")
	if p.wgFile.Editor.Text() != "" || q.choices[1].enabled() {
		t.Fatal("the typed-path dialog is not empty")
	}
}

func TestSendFileThroughThePicker(t *testing.T) {
	paired := BTDevice{Path: "/org/bluez/hci0/dev_BB", Name: "Phone", Icon: "phone", Paired: true, Battery: -1}
	u, f, p := btTestUI(t, BTState{Adapter: true, Powered: true, Devices: []BTDevice{paired}})
	u.hooks.Files = pickFiles{path: "/home/anna/photo one.jpg"}
	u.Layout(gtxOf(1100, 720))
	p.click(paired.Path + "|Send file…").Click()
	p.Update(gtxOf(1100, 720))
	eventually(t, "SendFile", func() bool {
		return strings.Contains(strings.Join(f.called(), ","), "SendFile "+paired.Path+" /home/anna/photo one.jpg")
	})
	// No portal: the typed-path dialog.
	u.hooks.Files = pickFiles{err: errNoPortal}
	eventually(t, "the send to end", func() bool { u.mu.Lock(); defer u.mu.Unlock(); return p.working == "" })
	p.click(paired.Path + "|Send file…").Click()
	p.Update(gtxOf(1100, 720))
	waitQuestion(t, u, "Send a file to Phone")
}

func TestPortalOldHandleResubscribeFails(t *testing.T) {
	// The portal ignores handle_token and the second subscription fails:
	// no portal, and no panic from a nil stop.
	f := &fakePortal{oldPath: true, failResub: true, results: uris("file:///tmp/a.conf")}
	if _, err := fakeFiles(f).OpenFile(wgFileRequest); !errors.Is(err, errNoPortal) {
		t.Fatalf("got %v", err)
	}
}

func TestPortalDiesWithTheDialogOpen(t *testing.T) {
	f := &fakePortal{silent: true, gone: make(chan struct{})}
	done := make(chan error, 1)
	go func() { _, err := fakeFiles(f).OpenFile(wgFileRequest); done <- err }()
	time.Sleep(20 * time.Millisecond)
	close(f.gone) // org.freedesktop.portal.Desktop lost its owner
	select {
	case err := <-done:
		if !errors.Is(err, errNoPortal) {
			t.Fatalf("got %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("OpenFile still waits after the portal left")
	}

	// Through the page: the picker is not left busy, and the next click
	// works (here it gets the typed-path dialog).
	u, _, _ := testUI(t, true)
	f = &fakePortal{silent: true, gone: make(chan struct{})}
	u.hooks.Files = fakeFiles(f)
	p := u.view(PageNetwork).(*networkPage)
	p.Shown()
	clickAndUpdate(p, &p.importWG)
	eventually(t, "the picker to open", func() bool { u.mu.Lock(); defer u.mu.Unlock(); return u.choosing })
	close(f.gone)
	eventually(t, "the picker to be free", func() bool { u.mu.Lock(); defer u.mu.Unlock(); return !u.choosing })
	waitQuestion(t, u, "Import a WireGuard file")
	u.closeDialog()
	u.hooks.Files = pickFiles{err: errNoPortal}
	clickAndUpdate(p, &p.importWG)
	waitQuestion(t, u, "Import a WireGuard file")
}
