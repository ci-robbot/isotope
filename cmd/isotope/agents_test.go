package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"
)

func v(x any) dbus.Variant { return dbus.MakeVariant(x) }

func vpnConn() map[string]map[string]dbus.Variant {
	return map[string]map[string]dbus.Variant{
		"connection": {"id": v("Work"), "type": v("vpn")},
		"vpn": {
			"service-type": v("org.freedesktop.NetworkManager.openconnect"),
			"user-name":    v("ada"),
			"data":         v(map[string]string{"gateway": "vpn.example.com", "protocol": "gp", "cookie-flags": "2"}),
		},
	}
}

func TestSecretAgentVPN(t *testing.T) {
	var got []string
	var gotLogin ocLogin
	a := &secretAgent{ask: func(args ...string) (map[string]any, error) {
		got = args
		return map[string]any{"password": "pw", "secondary_password": "123"}, nil
	}, login: func(_ context.Context, l ocLogin) (map[string]string, error) {
		gotLogin = l
		code, err := l.Second(context.Background(), "Second Password:")
		if err != nil || code != "123" {
			t.Errorf("the typed code: %q, %v", code, err)
		}
		return map[string]string{"cookie": "c", "gateway": "https://vpn", "gwcert": "pin-sha256:x"}, nil
	}}
	res, derr := a.GetSecrets(vpnConn(), "/c/1", "vpn", []string{"vpn.secrets.secondary_password", "x-vpn-message:Enter the code", "nototp-text"}, nmAllowInteraction)
	if derr != nil {
		t.Fatal(derr)
	}
	want := []string{"vpn-secret", "--name", "Work", "--fields", "password,secondary_password", "--message", "Enter the code"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("asked %q", got)
	}
	if gotLogin.Password != "pw" || gotLogin.Gateway != "vpn.example.com" || gotLogin.Protocol != "gp" || gotLogin.User != "ada" {
		t.Fatalf("login %+v", gotLogin)
	}
	secrets, ok := res["vpn"]["secrets"].Value().(map[string]string)
	if !ok || !reflect.DeepEqual(secrets, map[string]string{"cookie": "c", "gateway": "https://vpn", "gwcert": "pin-sha256:x"}) {
		t.Fatalf("reply %v", res)
	}
}

func TestSecretAgentWireGuard(t *testing.T) {
	a := &secretAgent{ask: func(args ...string) (map[string]any, error) {
		return map[string]any{"private-key": "k"}, nil
	}}
	conn := map[string]map[string]dbus.Variant{"connection": {"id": v("wg"), "type": v("wireguard")}}
	res, derr := a.GetSecrets(conn, "/c/2", "wireguard", nil, nmAllowInteraction)
	if derr != nil || res["wireguard"]["private-key"].Value() != "k" {
		t.Fatalf("%v, %v", res, derr)
	}
}

func TestSecretAgentIgnoresWifi(t *testing.T) {
	asked := false
	a := &secretAgent{ask: func(...string) (map[string]any, error) { asked = true; return nil, nil }}
	wifi := map[string]map[string]dbus.Variant{"connection": {"id": v("Home"), "type": v("802-11-wireless")}}
	cases := []struct {
		name    string
		conn    map[string]map[string]dbus.Variant
		setting string
		flags   uint32
	}{
		{"wifi", wifi, "802-11-wireless-security", nmAllowInteraction},
		{"no interaction", vpnConn(), "vpn", 0},
		{"other vpn plugin", map[string]map[string]dbus.Variant{
			"connection": {"id": v("x"), "type": v("vpn")},
			"vpn":        {"service-type": v("org.freedesktop.NetworkManager.openvpn")}}, "vpn", nmAllowInteraction},
	}
	for _, c := range cases {
		_, derr := a.GetSecrets(c.conn, "/c/3", c.setting, nil, c.flags)
		if derr == nil || derr.Name != "org.freedesktop.NetworkManager.SecretAgent.NoSecrets" {
			t.Errorf("%s: %v", c.name, derr)
		}
	}
	if asked {
		t.Fatal("a dialog opened")
	}
}

func TestSecretAgentCancel(t *testing.T) {
	a := &secretAgent{ask: func(...string) (map[string]any, error) { return nil, errAskCanceled }}
	_, derr := a.GetSecrets(vpnConn(), "/c/1", "vpn", nil, nmAllowInteraction)
	if derr == nil || derr.Name != "org.freedesktop.NetworkManager.SecretAgent.UserCanceled" {
		t.Fatalf("%v", derr)
	}
}

func TestSecretAgentCancelGetSecretsUnblocks(t *testing.T) {
	release := make(chan struct{})
	set := make(chan struct{}, 1)
	a := &secretAgent{ask: func(...string) (map[string]any, error) { <-release; return nil, errAskCanceled },
		pendingSet: func() { set <- struct{}{} }}
	done := make(chan *dbus.Error, 1)
	go func() {
		_, derr := a.GetSecrets(vpnConn(), "/c/1", "vpn", nil, nmAllowInteraction)
		done <- derr
	}()
	<-set
	if derr := a.CancelGetSecrets("/c/1", "vpn"); derr != nil {
		t.Fatal(derr)
	}
	select {
	case derr := <-done:
		if derr == nil || derr.Name != "org.freedesktop.NetworkManager.SecretAgent.UserCanceled" {
			t.Fatalf("%v", derr)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("GetSecrets still blocked")
	}
	close(release)
	if a.SaveSecrets(vpnConn(), "/c/1") != nil || a.DeleteSecrets(vpnConn(), "/c/1") != nil {
		t.Fatal("Save or Delete failed")
	}
}

func obexFake(dir string, ask Asker) *obexAgent {
	return &obexAgent{ask: ask, downloads: dir, timeout: time.Second,
		watch: func(dbus.ObjectPath) func() (string, string) { return func() (string, string) { return "", "" } },
		info: func(dbus.ObjectPath) (transferInfo, error) {
			return transferInfo{Name: "photo.jpg", Size: 2048, Device: "Phone"}, nil
		}}
}

func TestObexAgentAccept(t *testing.T) {
	var got []string
	a := obexFake(t.TempDir(), func(args ...string) (map[string]any, error) {
		got = args
		return map[string]any{"accept": true}, nil
	})
	// obexd only writes in its own folder: the reply is the bare name.
	name, derr := a.AuthorizePush("/org/bluez/obex/server/session0/transfer0")
	if derr != nil || name != "photo.jpg" {
		t.Fatalf("%q, %v", name, derr)
	}
	want := []string{"bluetooth-file", "--device", "Phone", "--file", "photo.jpg", "--size", "2048"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("asked %q", got)
	}
}

func TestSafeName(t *testing.T) {
	for in, want := range map[string]string{
		"photo.jpg": "photo.jpg", ".bashrc": "bashrc", "../../etc/x": "x", "...": "file", "": "file", "a/b.txt": "b.txt",
	} {
		if got := safeName(in); got != want {
			t.Errorf("safeName(%q) = %q, want %q", in, got, want)
		}
	}
}

// settleWith runs one accepted transfer to its end: the transfer ends with
// status and the file at file in obexd's folder.
func settleWith(t *testing.T, downloads, status, file string) {
	t.Helper()
	a := obexFake(downloads, func(...string) (map[string]any, error) { return map[string]any{"accept": true}, nil })
	settled := make(chan struct{})
	a.settled = func() { close(settled) }
	a.watch = func(dbus.ObjectPath) func() (string, string) {
		return func() (string, string) { return status, file }
	}
	if _, derr := a.AuthorizePush("/t"); derr != nil {
		t.Fatal(derr)
	}
	select {
	case <-settled:
	case <-time.After(2 * time.Second):
		t.Fatal("the transfer was never settled")
	}
}

func TestObexCompleteMovesToDownloads(t *testing.T) {
	root, dl := t.TempDir(), filepath.Join(t.TempDir(), "Downloads") // Downloads is missing
	file := filepath.Join(root, "photo.jpg")
	os.WriteFile(file, []byte("data"), 0o600)
	settleWith(t, dl, "complete", file)
	if b, err := os.ReadFile(filepath.Join(dl, "photo.jpg")); err != nil || string(b) != "data" {
		t.Fatalf("%q, %v", b, err)
	}
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Fatalf("the file is still in obexd's folder: %v", err)
	}
}

func TestObexCompleteClashSuffixAtMoveTime(t *testing.T) {
	root, dl := t.TempDir(), t.TempDir()
	// The name was free at AuthorizePush; it is taken by the time the file
	// is complete.
	os.WriteFile(filepath.Join(dl, "photo.jpg"), []byte("old"), 0o600)
	os.WriteFile(filepath.Join(dl, "photo (1).jpg"), []byte("old"), 0o600)
	file := filepath.Join(root, "photo.jpg")
	os.WriteFile(file, []byte("new"), 0o600)
	settleWith(t, dl, "complete", file)
	if b, _ := os.ReadFile(filepath.Join(dl, "photo (2).jpg")); string(b) != "new" {
		t.Fatalf("photo (2).jpg holds %q", b)
	}
	if b, _ := os.ReadFile(filepath.Join(dl, "photo.jpg")); string(b) != "old" {
		t.Fatalf("an old file was replaced: %q", b)
	}
}

func TestObexErrorRemovesPartialFile(t *testing.T) {
	root, dl := t.TempDir(), t.TempDir()
	file := filepath.Join(root, "photo.jpg")
	os.WriteFile(file, []byte("par"), 0o600)
	settleWith(t, dl, "error", file)
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Fatalf("the partial file stays: %v", err)
	}
	if ents, _ := os.ReadDir(dl); len(ents) != 0 {
		t.Fatalf("Downloads has %v", ents)
	}
}

func TestObexTimeoutCountsFromEntry(t *testing.T) {
	if obexAskTimeout > 50*time.Second || askTimeouts["bluetooth-file"] > 50*time.Second {
		t.Fatalf("the file timeouts must stay under obexd's 60 s: %v, %v", obexAskTimeout, askTimeouts["bluetooth-file"])
	}
	block := make(chan struct{})
	defer close(block)
	a := obexFake(t.TempDir(), func(...string) (map[string]any, error) { <-block; return nil, nil })
	a.timeout = 250 * time.Millisecond
	a.info = func(dbus.ObjectPath) (transferInfo, error) {
		time.Sleep(200 * time.Millisecond)
		return transferInfo{Name: "x"}, nil
	}
	start := time.Now()
	_, derr := a.AuthorizePush("/t")
	if derr == nil || time.Since(start) > 400*time.Millisecond {
		t.Fatalf("%v after %v: the timer started after info", derr, time.Since(start))
	}
}

func TestReregisterOnNewOwner(t *testing.T) {
	ch := make(chan *dbus.Signal, 8)
	n := make(chan struct{}, 8)
	go reregister(ch, "org.bluez.obex", func() error { n <- struct{}{}; return nil })
	sig := func(name, old, owner string) *dbus.Signal {
		return &dbus.Signal{Name: "org.freedesktop.DBus.NameOwnerChanged", Body: []any{name, old, owner}}
	}
	ch <- sig("org.bluez.obex", "", ":1.5") // appears: register
	ch <- sig("org.bluez.obex", ":1.5", "") // gone: nothing
	ch <- sig("other.name", "", ":1.9")     // another name: nothing
	ch <- sig("org.bluez.obex", "", ":1.6") // back: register
	for i := 0; i < 2; i++ {
		select {
		case <-n:
		case <-time.After(2 * time.Second):
			t.Fatalf("registered %d times, want 2", i)
		}
	}
	select {
	case <-n:
		t.Fatal("registered a third time")
	case <-time.After(50 * time.Millisecond):
	}
	close(ch)
}

func TestSecretAgentNoDialogIsNoSecrets(t *testing.T) {
	a := &secretAgent{ask: func(...string) (map[string]any, error) { return nil, errors.New("exec: isotope-system: not found") }}
	_, derr := a.GetSecrets(vpnConn(), "/c/1", "vpn", nil, nmAllowInteraction)
	if derr == nil || derr.Name != "org.freedesktop.NetworkManager.SecretAgent.NoSecrets" {
		t.Fatalf("%v", derr)
	}
}

func TestObexAgentRefuses(t *testing.T) {
	dir := t.TempDir()
	for name, ask := range map[string]Asker{
		"decline": func(...string) (map[string]any, error) { return map[string]any{"accept": false}, nil },
		"cancel":  func(...string) (map[string]any, error) { return nil, errAskCanceled },
	} {
		_, derr := obexFake(dir, ask).AuthorizePush("/t")
		if derr == nil || derr.Name != "org.bluez.obex.Error.Rejected" {
			t.Errorf("%s: %v", name, derr)
		}
	}
	// A name that climbs out of Downloads never leaves it.
	a := obexFake(dir, func(...string) (map[string]any, error) { return map[string]any{"accept": true}, nil })
	a.info = func(dbus.ObjectPath) (transferInfo, error) { return transferInfo{Name: "../../etc/x"}, nil }
	p, derr := a.AuthorizePush("/t")
	if derr != nil || p != "x" {
		t.Fatalf("%q, %v", p, derr)
	}
}

func TestObexAgentTimeout(t *testing.T) {
	block := make(chan struct{})
	defer close(block)
	a := obexFake(t.TempDir(), func(...string) (map[string]any, error) { <-block; return nil, nil })
	a.timeout = 50 * time.Millisecond
	start := time.Now()
	_, derr := a.AuthorizePush("/t")
	if derr == nil || derr.Name != "org.bluez.obex.Error.Rejected" {
		t.Fatalf("%v", derr)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatal("did not time out")
	}
}

func TestExecAskerTimesOutAndParses(t *testing.T) {
	run := func(out string, wait bool) func(ctx askCtx, args []string) ([]byte, error) {
		return func(ctx askCtx, args []string) ([]byte, error) {
			if wait {
				<-ctx.Done()
				return nil, ctx.Err()
			}
			return []byte(out), nil
		}
	}
	ask := newExecAsker(run(`{"accept":true}`, false), map[string]time.Duration{"bluetooth-file": time.Second})
	m, err := ask("bluetooth-file", "--file", "x")
	if err != nil || m["accept"] != true {
		t.Fatalf("%v, %v", m, err)
	}
	ask = newExecAsker(run("", true), map[string]time.Duration{"vpn-secret": 30 * time.Millisecond})
	if _, err := ask("vpn-secret"); err == nil || !strings.Contains(err.Error(), "deadline") {
		t.Fatalf("%v", err)
	}
	ask = newExecAsker(run("not json", false), nil)
	if _, err := ask("vpn-secret"); err == nil {
		t.Fatal("bad JSON accepted")
	}
	// The output can hold a password: it stays out of the error.
	ask = newExecAsker(run("hunter2 not json", false), nil)
	if _, err := ask("vpn-secret"); err == nil || strings.Contains(err.Error(), "hunter2") {
		t.Fatalf("the error shows the output: %v", err)
	}
}

func transferSignal(sender string, tr dbus.ObjectPath, changed map[string]any) *dbus.Signal {
	m := map[string]dbus.Variant{}
	for k, v := range changed {
		m[k] = dbus.MakeVariant(v)
	}
	return &dbus.Signal{Sender: sender, Path: tr, Name: "org.freedesktop.DBus.Properties.PropertiesChanged",
		Body: []any{transferIf, m, []string{}}}
}

func TestFollowTransferIdleResetsOnProgress(t *testing.T) {
	const tr = dbus.ObjectPath("/org/bluez/obex/server/session0/transfer0")
	const obexd = ":1.7"
	ch := make(chan *dbus.Signal)
	done := make(chan [2]string, 1)
	go func() {
		st, f := followTransfer(ch, tr, obexd, 150*time.Millisecond, func() string { return "/cache/a.jpg" })
		done <- [2]string{st, f}
	}()
	// Progress every 50 ms for 600 ms: four idle periods in all, never one
	// without a change.
	for i := 1; i <= 12; i++ {
		time.Sleep(50 * time.Millisecond)
		ch <- transferSignal(obexd, tr, map[string]any{"Transferred": uint64(i * 1000)})
	}
	ch <- transferSignal(obexd, tr, map[string]any{"Status": "complete"})
	if got := <-done; got != [2]string{"complete", "/cache/a.jpg"} {
		t.Fatalf("got %v", got)
	}
	// No progress: it gives up after the idle time.
	start := time.Now()
	st, _ := followTransfer(make(chan *dbus.Signal), tr, obexd, 50*time.Millisecond, func() string { return "" })
	if st != "" || time.Since(start) > 5*time.Second {
		t.Fatalf("idle: %q after %v", st, time.Since(start))
	}
}

func TestFollowTransferIgnoresOtherSenders(t *testing.T) {
	const tr = dbus.ObjectPath("/org/bluez/obex/server/session0/transfer0")
	ch := make(chan *dbus.Signal, 4)
	// A spoofed signal names /home/u/.ssh/id_ed25519 and says complete.
	ch <- transferSignal(":1.99", tr, map[string]any{"Filename": "/home/u/.ssh/id_ed25519", "Status": "complete"})
	ch <- transferSignal(":1.7", "/other", map[string]any{"Status": "error"})
	ch <- transferSignal(":1.7", tr, map[string]any{"Filename": "/cache/b.txt", "Status": "complete"})
	st, f := followTransfer(ch, tr, ":1.7", time.Minute, func() string { return "" })
	if st != "complete" || f != "/cache/b.txt" {
		t.Fatalf("got %q %q", st, f)
	}
}
