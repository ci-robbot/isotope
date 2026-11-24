package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/godbus/dbus/v5"

	"github.com/kairos-community/isotope/pkg/i18n"
)

// The tray answers two kinds of question for the session, so they get an
// answer even when Isotope System is closed: NetworkManager asks for a VPN
// password (a SecretAgent), and obexd asks whether to take a Bluetooth file
// (an OBEX Agent1). Each opens `isotope-system ask ...`, a small window.

// Asker runs `isotope-system ask args...` and returns the JSON it printed.
// It fails when the dialog is cancelled or closed (exit 1), when it cannot
// run, and on a timeout.
type Asker func(args ...string) (map[string]any, error)

type askCtx = context.Context

// errAskCanceled is the user's Cancel, or a closed window: exit 1.
var errAskCanceled = errors.New("the dialog was cancelled")

// askTimeouts are how long a dialog may stay open: people type passwords
// slowly, and an unanswered file is refused. obexd gives its agent 60 s and
// then takes the file, so the file dialog must end before that.
var askTimeouts = map[string]time.Duration{
	"bluetooth-file": obexAskTimeout,
	"vpn-secret":     120 * time.Second,
}

// running are the dialogs that are open, by kind, so a cancel can close them.
var running = struct {
	sync.Mutex
	by map[string]map[*func()]struct{}
}{by: map[string]map[*func()]struct{}{}}

// killAsks closes every open dialog of kind.
func killAsks(kind string) {
	running.Lock()
	var fs []func()
	for p := range running.by[kind] {
		fs = append(fs, *p)
	}
	running.Unlock()
	for _, f := range fs {
		f()
	}
}

// newExecAsker is an Asker that runs a dialog through run and gives each
// kind of dialog its timeout (none for a kind not in timeouts).
func newExecAsker(run func(ctx askCtx, args []string) ([]byte, error), timeouts map[string]time.Duration) Asker {
	return func(args ...string) (map[string]any, error) {
		kind := ""
		if len(args) > 0 {
			kind = args[0]
		}
		var ctx context.Context
		var cancel context.CancelFunc
		if d := timeouts[kind]; d > 0 {
			ctx, cancel = context.WithTimeout(context.Background(), d)
		} else {
			ctx, cancel = context.WithCancel(context.Background())
		}
		defer cancel()
		var stop func() = cancel
		running.Lock()
		if running.by[kind] == nil {
			running.by[kind] = map[*func()]struct{}{}
		}
		running.by[kind][&stop] = struct{}{}
		running.Unlock()
		defer func() {
			running.Lock()
			delete(running.by[kind], &stop)
			running.Unlock()
		}()
		out, err := run(ctx, args)
		if err != nil {
			if ctx.Err() != nil {
				return nil, fmt.Errorf("the %s dialog: %w", kind, ctx.Err())
			}
			var ee *exec.ExitError
			if errors.As(err, &ee) && ee.ExitCode() == 1 {
				return nil, errAskCanceled
			}
			return nil, err
		}
		var m map[string]any
		if err := json.Unmarshal(bytes.TrimSpace(out), &m); err != nil {
			// The output is not quoted: it can hold a password.
			return nil, fmt.Errorf("the %s dialog printed something that is not JSON: %w", kind, err)
		}
		return m, nil
	}
}

// systemAsker runs the installed isotope-system.
func systemAsker() Asker {
	return newExecAsker(func(ctx askCtx, args []string) ([]byte, error) {
		return exec.CommandContext(ctx, "isotope-system", append([]string{"ask"}, args...)...).Output()
	}, askTimeouts)
}

// ---- NetworkManager secret agent ----

const (
	nmName            = "org.freedesktop.NetworkManager"
	nmAgentManager    = dbus.ObjectPath("/org/freedesktop/NetworkManager/AgentManager")
	nmAgentManagerIf  = "org.freedesktop.NetworkManager.AgentManager"
	nmSecretAgentIf   = "org.freedesktop.NetworkManager.SecretAgent"
	secretAgentPath   = dbus.ObjectPath("/io/kairos/Isotope/SecretAgent")
	nmSecretAgentPath = dbus.ObjectPath("/org/freedesktop/NetworkManager/SecretAgent")
	secretAgentID     = "io.kairos.isotope"

	nmAllowInteraction = 0x1
	openconnectService = "org.freedesktop.NetworkManager.openconnect"
	errNoSecrets       = nmSecretAgentIf + ".NoSecrets"
	errUserCanceled    = nmSecretAgentIf + ".UserCanceled"
)

// secretAgent answers NetworkManager for OpenConnect and WireGuard secrets.
// For any other connection it says NoSecrets, so other agents and nmcli
// keep working. For OpenConnect it asks for the password and logs in itself
// (openconnect.go), because the service takes only a login cookie.
type secretAgent struct {
	ask Asker
	// login logs in to an OpenConnect server; nil is defaultOCRunner.login.
	login func(ctx context.Context, l ocLogin) (map[string]string, error)
	// notify tells the user why a login failed; nil is Notify.
	notify func(summary, body string) error

	// pendingSet, when set, runs once a request is in pending (for tests).
	pendingSet func()

	mu      sync.Mutex
	pending map[dbus.ObjectPath]chan struct{} // closed by CancelGetSecrets
}

func variantStr(m map[string]map[string]dbus.Variant, group, key string) string {
	s, _ := m[group][key].Value().(string)
	return s
}

// secretFields are the fields to ask for, from NetworkManager's hints
// ("vpn.secrets.password", "wireguard.private-key" or a bare name).
func secretFields(setting string, hints []string) (fields []string, message string) {
	add := func(f string) {
		for _, g := range fields {
			if g == f {
				return
			}
		}
		fields = append(fields, f)
	}
	if setting == "wireguard" {
		add("private-key")
		return fields, ""
	}
	add("password")
	for _, h := range hints {
		if m, ok := strings.CutPrefix(h, "x-vpn-message:"); ok {
			message = m
			continue
		}
		// Only a hint that is a secret's own name adds a field.
		switch strings.TrimPrefix(h, "vpn.secrets.") {
		case "secondary_password":
			add("secondary_password")
		case "otp":
			add("otp")
		}
	}
	return fields, message
}

func (a *secretAgent) GetSecrets(conn map[string]map[string]dbus.Variant, path dbus.ObjectPath, setting string, hints []string, flags uint32) (map[string]map[string]dbus.Variant, *dbus.Error) {
	vpn := setting == "vpn" && variantStr(conn, "connection", "type") == "vpn" && variantStr(conn, "vpn", "service-type") == openconnectService
	wg := setting == "wireguard" && variantStr(conn, "connection", "type") == "wireguard"
	if !(vpn || wg) || flags&nmAllowInteraction == 0 {
		return nil, dbus.NewError(errNoSecrets, nil)
	}
	name := variantStr(conn, "connection", "id")
	fields, message := secretFields(setting, hints)
	if vpn && message == "" {
		message = openConnectMessage(conn)
	}
	args := []string{"vpn-secret", "--name", name, "--fields", strings.Join(fields, ",")}
	if message != "" {
		args = append(args, "--message", message)
	}

	cancel := make(chan struct{})
	a.mu.Lock()
	if a.pending == nil {
		a.pending = map[dbus.ObjectPath]chan struct{}{}
	}
	a.pending[path] = cancel
	a.mu.Unlock()
	if a.pendingSet != nil {
		a.pendingSet()
	}
	defer func() {
		a.mu.Lock()
		if a.pending[path] == cancel {
			delete(a.pending, path)
		}
		a.mu.Unlock()
	}()

	r := a.askUntil(cancel, args)
	if r.canceled || errors.Is(r.err, errAskCanceled) {
		return nil, dbus.NewError(errUserCanceled, nil)
	}
	if r.err != nil {
		// No dialog could run (no app, a timeout): let another agent try.
		log.Printf("isotope: the VPN password dialog: %v", r.err)
		return nil, dbus.NewError(errNoSecrets, nil)
	}
	secrets := map[string]string{}
	for k, val := range r.m {
		if s, ok := val.(string); ok && s != "" {
			secrets[k] = s
		}
	}
	if len(secrets) == 0 {
		return nil, dbus.NewError(errUserCanceled, nil)
	}
	if wg {
		out := map[string]dbus.Variant{}
		for k, s := range secrets {
			out[k] = dbus.MakeVariant(s)
		}
		return map[string]map[string]dbus.Variant{"wireguard": out}, nil
	}
	return a.openConnect(conn, name, secrets, cancel)
}

type askResult struct {
	m        map[string]any
	err      error
	canceled bool // CancelGetSecrets came first
}

// askUntil runs a dialog until it answers or cancel closes.
func (a *secretAgent) askUntil(cancel <-chan struct{}, args []string) askResult {
	ch := make(chan askResult, 1)
	go func() { m, err := a.ask(args...); ch <- askResult{m: m, err: err} }()
	select {
	case r := <-ch:
		return r
	case <-cancel:
		return askResult{canceled: true}
	}
}

// openConnectMessage is the password dialog's text: who logs in where.
func openConnectMessage(conn map[string]map[string]dbus.Variant) string {
	data, _ := conn["vpn"]["data"].Value().(map[string]string)
	user, gw := variantStr(conn, "vpn", "user-name"), data["gateway"]
	switch {
	case user != "" && gw != "":
		return i18n.Tf("Log in to %[1]s as %[2]s.", gw, user)
	case gw != "":
		return i18n.Tf("Log in to %s.", gw)
	}
	return ""
}

// openConnect logs in with the password (and a second factor, asked for
// when the server wants one) and returns the cookie, gateway and gwcert
// secrets that NetworkManager-openconnect connects with. A failed login is
// a notification with openconnect's reason, and NoSecrets.
func (a *secretAgent) openConnect(conn map[string]map[string]dbus.Variant, name string, typed map[string]string, cancel <-chan struct{}) (map[string]map[string]dbus.Variant, *dbus.Error) {
	data, _ := conn["vpn"]["data"].Value().(map[string]string)
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	go func() {
		select {
		case <-cancel:
			stop()
		case <-ctx.Done():
		}
	}()
	// A code typed in the first dialog (a hint asked for it) answers the
	// first prompt without a second dialog.
	code := typed["otp"]
	if code == "" {
		code = typed["secondary_password"]
	}
	second := func(ctx context.Context, prompt string) (string, error) {
		if code != "" {
			c := code
			code = ""
			return c, nil
		}
		msg := i18n.T("The server asks for a second step to log in.")
		if prompt != "" {
			msg = i18n.Tf("The server asks for a second step to log in. It asks for: %s", strings.TrimSuffix(prompt, ":"))
		}
		r := a.askUntil(ctx.Done(), []string{"vpn-secret", "--name", name, "--fields", "otp", "--message", msg})
		if r.canceled {
			// The login ended (a timeout, CancelGetSecrets): close the dialog.
			killAsks("vpn-secret")
			return "", ctx.Err()
		}
		if r.err != nil {
			return "", r.err
		}
		c, _ := r.m["otp"].(string)
		if c == "" {
			return "", errAskCanceled
		}
		return c, nil
	}
	login := a.login
	if login == nil {
		login = defaultOCRunner.login
	}
	secrets, err := login(ctx, ocLogin{Gateway: data["gateway"], Protocol: data["protocol"],
		User: variantStr(conn, "vpn", "user-name"), Password: typed["password"], Second: second})
	if err != nil {
		select {
		case <-cancel:
			return nil, dbus.NewError(errUserCanceled, nil)
		default:
		}
		if errors.Is(err, errAskCanceled) {
			return nil, dbus.NewError(errUserCanceled, nil)
		}
		log.Printf("isotope: logging in to the VPN %s: %v", name, err)
		notify := a.notify
		if notify == nil {
			notify = Notify
		}
		body := i18n.Tf("Could not log in: %s", err.Error())
		var le *LoginError
		switch {
		case errors.As(err, &le):
			body = i18n.Tf("The login failed: %s", le.Reason)
		case errors.Is(err, context.DeadlineExceeded):
			body = i18n.T("The server did not answer in time.")
		}
		if nerr := notify("VPN "+name, body); nerr != nil {
			log.Printf("isotope: the VPN notification: %v", nerr)
		}
		return nil, dbus.NewError(errNoSecrets, nil)
	}
	return map[string]map[string]dbus.Variant{"vpn": {"secrets": dbus.MakeVariant(secrets)}}, nil
}

// CancelGetSecrets closes the dialog of a request NetworkManager gave up.
func (a *secretAgent) CancelGetSecrets(path dbus.ObjectPath, setting string) *dbus.Error {
	a.mu.Lock()
	c := a.pending[path]
	delete(a.pending, path)
	a.mu.Unlock()
	if c != nil {
		close(c)
		killAsks("vpn-secret")
	}
	return nil
}

// SaveSecrets and DeleteSecrets do nothing: NetworkManager keeps the
// secrets it needs itself.
func (a *secretAgent) SaveSecrets(map[string]map[string]dbus.Variant, dbus.ObjectPath) *dbus.Error {
	return nil
}
func (a *secretAgent) DeleteSecrets(map[string]map[string]dbus.Variant, dbus.ObjectPath) *dbus.Error {
	return nil
}

// StartSecretAgent exports the agent on the system bus conn and registers
// it with NetworkManager, and again each time NetworkManager gets a new
// owner. NetworkManager calls the fixed path under its own name, so the
// agent is exported there as well. A failed first registration is returned,
// and the watch still retries it.
func StartSecretAgent(conn *dbus.Conn, ask Asker) error {
	a := &secretAgent{ask: ask}
	for _, p := range []dbus.ObjectPath{secretAgentPath, nmSecretAgentPath} {
		if err := conn.Export(a, p, nmSecretAgentIf); err != nil {
			return err
		}
	}
	register := func() error {
		return conn.Object(nmName, nmAgentManager).Call(nmAgentManagerIf+".Register", 0, secretAgentID).Err
	}
	watchOwner(conn, nmName, register)
	return register()
}

// watchOwner calls register each time name gets a new owner on conn's bus.
func watchOwner(conn *dbus.Conn, name string, register func() error) {
	if err := conn.AddMatchSignal(dbus.WithMatchSender("org.freedesktop.DBus"),
		dbus.WithMatchInterface("org.freedesktop.DBus"), dbus.WithMatchMember("NameOwnerChanged"),
		dbus.WithMatchArg(0, name)); err != nil {
		log.Printf("isotope: watching %s: %v", name, err)
		return
	}
	ch := make(chan *dbus.Signal, 8)
	conn.Signal(ch)
	go reregister(ch, name, register)
}

// reregister runs register for each NameOwnerChanged signal that gives name
// a new owner.
func reregister(ch <-chan *dbus.Signal, name string, register func() error) {
	for sig := range ch {
		if sig.Name != "org.freedesktop.DBus.NameOwnerChanged" || len(sig.Body) < 3 {
			continue
		}
		n, _ := sig.Body[0].(string)
		owner, _ := sig.Body[2].(string)
		if n != name || owner == "" {
			continue
		}
		if err := register(); err != nil {
			log.Printf("isotope: registering with %s: %v", name, err)
		}
	}
}

// ---- OBEX agent ----

const (
	obexName       = "org.bluez.obex"
	obexManager    = dbus.ObjectPath("/org/bluez/obex")
	obexManagerIf  = "org.bluez.obex.AgentManager1"
	obexAgentIf    = "org.bluez.obex.Agent1"
	obexAgentPath  = dbus.ObjectPath("/io/kairos/Isotope/ObexAgent")
	errObexReject  = "org.bluez.obex.Error.Rejected"
	obexAskTimeout = 50 * time.Second
)

// transferInfo is what the dialog shows of an incoming file.
type transferInfo struct {
	Name   string
	Size   uint64
	Device string // the sender's name, or its address
}

// obexAgent asks about each incoming file. obexd only writes inside its own
// root folder, so AuthorizePush returns the bare file name, and the agent
// moves the finished file to downloads.
type obexAgent struct {
	ask       Asker
	downloads string
	timeout   time.Duration
	// info reads the transfer's properties; a seam for the tests.
	info func(transfer dbus.ObjectPath) (transferInfo, error)
	// watch starts following a transfer and returns a function that waits
	// for its end: the final status ("complete" or "error") and the file's
	// path in obexd's folder. A seam for the tests.
	watch func(transfer dbus.ObjectPath) func() (status, file string)
	// settled, when set, runs after a transfer was moved or cleaned up.
	settled func()

	mu      sync.Mutex
	pending map[chan struct{}]struct{}
}

func reject() *dbus.Error { return dbus.NewError(errObexReject, nil) }

// uniquePath is dir/name, or dir/stem (N).ext for the first N that is free.
func uniquePath(dir, name string) string {
	p := filepath.Join(dir, name)
	ext := filepath.Ext(name)
	stem := strings.TrimSuffix(name, ext)
	for i := 1; ; i++ {
		if _, err := os.Lstat(p); errors.Is(err, os.ErrNotExist) {
			return p
		}
		p = filepath.Join(dir, fmt.Sprintf("%s (%d)%s", stem, i, ext))
	}
}

// safeName is the file name from a sender: no folders, no leading dots.
func safeName(n string) string {
	n = filepath.Base(filepath.Clean("/" + n))
	n = strings.TrimLeft(n, ".")
	if n == "" || n == "/" {
		return "file"
	}
	return n
}

func (a *obexAgent) AuthorizePush(transfer dbus.ObjectPath) (string, *dbus.Error) {
	// obexd takes the file after 60 s without an answer; the clock starts here.
	deadline := time.Now().Add(a.timeout)
	ti, err := a.info(transfer)
	if err != nil {
		log.Printf("isotope: reading the incoming Bluetooth file: %v", err)
		return "", reject()
	}
	name := safeName(ti.Name)
	args := []string{"bluetooth-file", "--device", ti.Device, "--file", name, "--size", fmt.Sprint(ti.Size)}

	cancel := make(chan struct{})
	a.mu.Lock()
	if a.pending == nil {
		a.pending = map[chan struct{}]struct{}{}
	}
	a.pending[cancel] = struct{}{}
	a.mu.Unlock()
	defer func() { a.mu.Lock(); delete(a.pending, cancel); a.mu.Unlock() }()

	type result struct {
		m   map[string]any
		err error
	}
	ch := make(chan result, 1)
	go func() { m, err := a.ask(args...); ch <- result{m, err} }()
	timer := time.NewTimer(time.Until(deadline))
	defer timer.Stop()
	var r result
	select {
	case r = <-ch:
	case <-timer.C:
		killAsks("bluetooth-file")
		return "", reject()
	case <-cancel:
		killAsks("bluetooth-file")
		return "", reject()
	}
	if r.err != nil || r.m["accept"] != true {
		return "", reject()
	}
	// Follow the transfer before obexd starts it.
	wait := a.watch(transfer)
	go func() {
		a.settle(wait, name)
		if a.settled != nil {
			a.settled()
		}
	}()
	return name, nil
}

// settle waits for the transfer's end, then moves the file to downloads, or
// removes the partial file after an error.
func (a *obexAgent) settle(wait func() (string, string), name string) {
	status, file := wait()
	if file == "" {
		return
	}
	if status != "complete" {
		os.Remove(file)
		return
	}
	if err := os.MkdirAll(a.downloads, 0o755); err != nil {
		log.Printf("isotope: making %s: %v", a.downloads, err)
		return
	}
	dst := uniquePath(a.downloads, name)
	if err := moveFile(file, dst); err != nil {
		log.Printf("isotope: moving %s to %s: %v", file, dst, err)
	}
}

// moveFile renames src to dst, or copies it when they are on different
// file systems. It never replaces dst.
func moveFile(src, dst string) error {
	if err := os.Link(src, dst); err == nil {
		return os.Remove(src)
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(dst)
		return err
	}
	if err := out.Close(); err != nil {
		os.Remove(dst)
		return err
	}
	return os.Remove(src)
}

// Cancel is obexd giving up on a transfer: the dialog closes.
func (a *obexAgent) Cancel() *dbus.Error {
	a.mu.Lock()
	for c := range a.pending {
		close(c)
		delete(a.pending, c)
	}
	a.mu.Unlock()
	return nil
}

// Release is obexd dropping the agent.
func (a *obexAgent) Release() *dbus.Error { return a.Cancel() }

const transferIf = "org.bluez.obex.Transfer1"

// obexInfo reads an incoming transfer from obexd, and the sender's name
// from BlueZ on the system bus when it is reachable.
func obexInfo(conn *dbus.Conn) func(dbus.ObjectPath) (transferInfo, error) {
	return func(transfer dbus.ObjectPath) (transferInfo, error) {
		var ti transferInfo
		t := conn.Object(obexName, transfer)
		name, err := t.GetProperty(transferIf + ".Name")
		if err != nil {
			return ti, err
		}
		ti.Name, _ = name.Value().(string)
		if size, err := t.GetProperty(transferIf + ".Size"); err == nil {
			ti.Size, _ = size.Value().(uint64)
		}
		if sess, err := t.GetProperty(transferIf + ".Session"); err == nil {
			if sp, ok := sess.Value().(dbus.ObjectPath); ok {
				if dest, err := conn.Object(obexName, sp).GetProperty("org.bluez.obex.Session1.Destination"); err == nil {
					addr, _ := dest.Value().(string)
					ti.Device = bluezAlias(addr)
				}
			}
		}
		return ti, nil
	}
}

// obexIdle is how long a transfer may go without progress (no change of
// its Transferred count) before the watch gives up on it.
var obexIdle = time.Hour

// obexWatch follows a transfer's Status through PropertiesChanged. Only
// obexd's own signals count: the match names it as the sender, and each
// signal's sender is checked against its unique name, so another client on
// the session bus cannot name a file to move or delete.
func obexWatch(conn *dbus.Conn) func(dbus.ObjectPath) func() (string, string) {
	return func(tr dbus.ObjectPath) func() (string, string) {
		opts := []dbus.MatchOption{dbus.WithMatchSender(obexName), dbus.WithMatchObjectPath(tr),
			dbus.WithMatchInterface("org.freedesktop.DBus.Properties"), dbus.WithMatchMember("PropertiesChanged")}
		var owner string
		if err := conn.BusObject().Call("org.freedesktop.DBus.GetNameOwner", 0, obexName).Store(&owner); err != nil {
			return func() (string, string) { return "", "" }
		}
		ch := make(chan *dbus.Signal, 32)
		conn.Signal(ch)
		if err := conn.AddMatchSignal(opts...); err != nil {
			conn.RemoveSignal(ch)
			return func() (string, string) { return "", "" }
		}
		filename := func() string {
			if f, err := conn.Object(obexName, tr).GetProperty(transferIf + ".Filename"); err == nil {
				s, _ := f.Value().(string)
				return s
			}
			return ""
		}
		return func() (string, string) {
			defer func() {
				conn.RemoveMatchSignal(opts...)
				conn.RemoveSignal(ch)
			}()
			return followTransfer(ch, tr, owner, obexIdle, filename)
		}
	}
}

// followTransfer reads the transfer tr's PropertiesChanged signals from ch
// until its Status is complete or error, and returns it with the file
// obexd writes. Signals from anyone but owner (obexd's unique name) are
// ignored. Each change of Transferred restarts the idle timer, so a long
// transfer that still moves is never cut off; after idle without one it
// returns an empty status. filename reads the file when no signal named it.
func followTransfer(ch <-chan *dbus.Signal, tr dbus.ObjectPath, owner string, idle time.Duration, filename func() string) (status, file string) {
	timer := time.NewTimer(idle)
	defer timer.Stop()
	for {
		select {
		case <-timer.C:
			return "", file
		case sig, ok := <-ch:
			if !ok {
				return "", file
			}
			if sig.Sender != owner || sig.Path != tr || sig.Name != "org.freedesktop.DBus.Properties.PropertiesChanged" || len(sig.Body) < 2 {
				continue
			}
			if iface, _ := sig.Body[0].(string); iface != transferIf {
				continue
			}
			changed, _ := sig.Body[1].(map[string]dbus.Variant)
			if _, ok := changed["Transferred"]; ok {
				if !timer.Stop() {
					select {
					case <-timer.C:
					default:
					}
				}
				timer.Reset(idle)
			}
			if f, ok := changed["Filename"]; ok {
				file, _ = f.Value().(string)
			}
			st, ok := changed["Status"]
			if !ok {
				continue
			}
			status, _ = st.Value().(string)
			if status != "complete" && status != "error" {
				continue
			}
			if file == "" {
				file = filename()
			}
			return status, file
		}
	}
}

// bluezAlias is the name BlueZ has for the device at addr, or addr itself.
func bluezAlias(addr string) string {
	sys, err := dbus.SystemBus()
	if err != nil || addr == "" {
		return addr
	}
	var objs map[dbus.ObjectPath]map[string]map[string]dbus.Variant
	if err := sys.Object("org.bluez", "/").Call("org.freedesktop.DBus.ObjectManager.GetManagedObjects", 0).Store(&objs); err != nil {
		return addr
	}
	for _, ifs := range objs {
		d, ok := ifs["org.bluez.Device1"]
		if !ok {
			continue
		}
		if a, _ := d["Address"].Value().(string); strings.EqualFold(a, addr) {
			if alias, _ := d["Alias"].Value().(string); alias != "" {
				return alias
			}
		}
	}
	return addr
}

// StartObexAgent exports the agent on the session bus conn and registers it
// with obexd, and again each time obexd gets a new owner. Accepted files end
// up in downloads.
func StartObexAgent(conn *dbus.Conn, ask Asker, downloads string) error {
	a := &obexAgent{ask: ask, downloads: downloads, timeout: obexAskTimeout, info: obexInfo(conn), watch: obexWatch(conn)}
	if err := conn.Export(a, obexAgentPath, obexAgentIf); err != nil {
		return err
	}
	register := func() error {
		return conn.Object(obexName, obexManager).Call(obexManagerIf+".RegisterAgent", 0, obexAgentPath).Err
	}
	watchOwner(conn, obexName, register)
	return register()
}

// startAgents registers both agents. A failure is logged, not fatal:
// NetworkManager or obexd may be missing, or not running yet.
func startAgents(logger *log.Logger) {
	ask := systemAsker()
	if sys, err := dbus.SystemBus(); err != nil {
		logger.Printf("the NetworkManager secret agent: %v", err)
	} else if err := StartSecretAgent(sys, ask); err != nil {
		logger.Printf("the NetworkManager secret agent: %v", err)
	}
	if ses, err := dbus.SessionBus(); err != nil {
		logger.Printf("the Bluetooth file agent: %v", err)
	} else {
		home, _ := os.UserHomeDir()
		if err := StartObexAgent(ses, ask, filepath.Join(home, "Downloads")); err != nil {
			logger.Printf("the Bluetooth file agent: %v", err)
		}
	}
}
