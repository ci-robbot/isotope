package main

import (
	"errors"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"
)

// busUsers is fakeUsers for the service, which runs the commands on its
// own goroutines.
type busUsers struct {
	mu sync.Mutex
	fu *fakeUsers
}

func (b *busUsers) run(name string, args []string, stdin []byte) ([]byte, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.fu.run(name, args, stdin)
}

func (b *busUsers) calls() []usersCall {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]usersCall(nil), b.fu.calls...)
}

func (b *busUsers) ran(name string) bool {
	for _, c := range b.calls() {
		if c.name == name {
			return true
		}
	}
	return false
}

// usersBusEnv is usersFixture behind a bus: the test process's uid is
// anna. allow is polkit's answer.
func usersBusEnv(t *testing.T, allow bool) (*busUsers, *busFixture, *fakeAuth) {
	t.Helper()
	return usersBusEnvAs(t, allow, func(uid uint32) (string, error) {
		if uid == uint32(os.Getuid()) {
			return "anna", nil
		}
		return "", errors.New("unknown uid")
	})
}

// usersBusEnvAs is usersBusEnv with name as userName. The service reads
// the fakes on its own goroutines, so they are set before it starts.
func usersBusEnvAs(t *testing.T, allow bool, name func(uint32) (string, error)) (*busUsers, *busFixture, *fakeAuth) {
	t.Helper()
	fu := &busUsers{fu: usersFixture(t)}
	usersRun = fu.run
	fakeUserName(t, name)
	daemonEnv(t, "v0.0.5")
	auth := &fakeAuth{allow: allow}
	return fu, startService(t, NewDaemon(&fakeBackend{}), auth), auth
}

func fakeUserName(t *testing.T, f func(uint32) (string, error)) {
	t.Helper()
	old := userName
	userName = f
	t.Cleanup(func() { userName = old })
}

// secretCall calls method with args and then an fd that holds lines.
func secretCall(t *testing.T, f *busFixture, method string, lines []string, args ...any) *dbus.Error {
	t.Helper()
	var c *dbus.Call
	if err := withSecrets(lines, func(fd dbus.UnixFD) error {
		c = f.call(method, append(args, fd)...)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return asDBusError(c.Err)
}

func asDBusError(err error) *dbus.Error {
	if err == nil {
		return nil
	}
	var de dbus.Error
	if errors.As(err, &de) {
		return &de
	}
	var dp *dbus.Error
	if errors.As(err, &dp) {
		return dp
	}
	return &dbus.Error{Name: "not a D-Bus error", Body: []any{err.Error()}}
}

func wantBusErr(t *testing.T, what string, err *dbus.Error, name, msg string) {
	t.Helper()
	if err == nil {
		t.Fatalf("%s: no error, want %s", what, name)
	}
	if err.Name != busInterface+".Error."+name {
		t.Fatalf("%s: %s %v, want %s", what, err.Name, err.Body, name)
	}
	if msg != "" && (len(err.Body) != 1 || !strings.Contains(err.Body[0].(string), msg)) {
		t.Fatalf("%s: message %v, want %q", what, err.Body, msg)
	}
}

func TestUsersNeedsNoPolkit(t *testing.T) {
	_, f, auth := usersBusEnv(t, false)
	var users []map[string]dbus.Variant
	if err := f.call("Users").Store(&users); err != nil {
		t.Fatal(err)
	}
	if n := len(auth.questions()); n != 0 {
		t.Fatalf("asked polkit %d times", n)
	}
	if len(users) != 1 {
		t.Fatalf("users %v, want anna only", users)
	}
	u := users[0]
	if u["name"].Value() != "anna" || u["full_name"].Value() != "Anna" ||
		u["uid"].Value() != uint32(1000) || u["admin"].Value() != true || u["home"].Value() != "/home/anna" {
		t.Fatalf("anna is %v", u)
	}
}

func TestUserMethodsAskTheUsersAction(t *testing.T) {
	fu, f, auth := usersBusEnv(t, false)
	before := readLines(t, passwdFile)
	wantBusErr(t, "AddUser", secretCall(t, f, "AddUser", []string{"s3cret"}, "bob", "Bob", false), "NotAuthorized", "")
	wantBusErr(t, "RemoveUser", asDBusError(f.call("RemoveUser", "anna", true).Err), "NotAuthorized", "")
	wantBusErr(t, "SetAdmin", asDBusError(f.call("SetAdmin", "anna", false).Err), "NotAuthorized", "")
	wantBusErr(t, "SetPassword", secretCall(t, f, "SetPassword", []string{"", "newpass"}, "bob"), "NotAuthorized", "")
	q := auth.questions()
	if len(q) != 4 {
		t.Fatalf("asked %q, want 4 questions", q)
	}
	for _, x := range q {
		if !strings.HasSuffix(x, " "+actionUsers) {
			t.Fatalf("asked %q", x)
		}
	}
	if len(fu.calls()) != 0 {
		t.Fatalf("ran %v", fu.calls())
	}
	if after := readLines(t, passwdFile); strings.Join(after, "\n") != strings.Join(before, "\n") {
		t.Fatalf("passwd changed: %q", after)
	}
	noFile(t, usersOEM)
}

func TestAddUserReadsThePasswordFromTheFd(t *testing.T) {
	fu, f, _ := usersBusEnv(t, true)

	// Watch the bus: no message may carry the password as a string.
	mon, err := dbus.Connect(f.addr)
	if err != nil {
		t.Fatal(err)
	}
	// Stop eavesdropping before Close: godbus closes the channel on Close
	// and its reader may still send to it.
	t.Cleanup(func() {
		mon.Eavesdrop(nil)
		mon.Close()
	})
	if err := mon.BusObject().Call("org.freedesktop.DBus.Monitoring.BecomeMonitor", 0, []string{}, uint32(0)).Err; err != nil {
		t.Fatalf("BecomeMonitor: %v", err)
	}
	msgs := make(chan *dbus.Message, 100)
	mon.Eavesdrop(msgs)

	if err := secretCall(t, f, "AddUser", []string{"s3cret"}, "bob", "Bob", false); err != nil {
		t.Fatal(err)
	}
	hashed := false
	for _, c := range fu.calls() {
		if c.name == "openssl" && c.stdin == "s3cret\n" {
			hashed = true
		}
		if strings.Contains(strings.Join(c.args, " "), "s3cret") {
			t.Fatalf("the password is in argv: %v", c)
		}
	}
	if !hashed {
		t.Fatalf("openssl never got the password on stdin: %v", fu.calls())
	}
	if !fu.ran("useradd") || !fu.ran("chpasswd") {
		t.Fatalf("ran %v", fu.calls())
	}

	seen := false
	deadline := time.After(2 * time.Second)
	for !seen {
		select {
		case m := <-msgs:
			if m.Type == dbus.TypeMethodCall && m.Headers[dbus.FieldMember].Value() == "AddUser" {
				seen = true
			}
			for _, b := range m.Body {
				if s, ok := b.(string); ok && strings.Contains(s, "s3cret") {
					t.Fatalf("a message carries the password: %v", m)
				}
			}
		case <-deadline:
			t.Fatal("the monitor never saw the AddUser call")
		}
	}
}

// fakeWrongPasswordWait sets wrongPasswordWait; call it before the service
// starts, which reads it on its own goroutines.
func fakeWrongPasswordWait(t *testing.T, f func()) {
	t.Helper()
	old := wrongPasswordWait
	wrongPasswordWait = f
	t.Cleanup(func() { wrongPasswordWait = old })
}

func TestSetPasswordSelfNeedsTheCurrentPassword(t *testing.T) {
	var waited atomic.Int32
	fakeWrongPasswordWait(t, func() { waited.Add(1) })
	fu, f, auth := usersBusEnv(t, false)
	err := secretCall(t, f, "SetPassword", []string{"wrong", "newpass"}, "anna")
	wantBusErr(t, "wrong current password", err, "Refused", errWrongPassword.Error())
	if fu.ran("chpasswd") {
		t.Fatal("chpasswd ran with a wrong current password")
	}
	if n := waited.Load(); n != 1 {
		t.Fatalf("waited %d times after a wrong password, want 1", n)
	}
	if err := secretCall(t, f, "SetPassword", []string{"annapass", "newpass"}, "anna"); err != nil {
		t.Fatal(err)
	}
	var stdin string
	for _, c := range fu.calls() {
		if c.name == "chpasswd" {
			stdin = c.stdin
		}
	}
	if stdin != "anna:"+fakeHash("fakesalt", "newpass")+"\n" {
		t.Fatalf("chpasswd stdin %q", stdin)
	}
	if q := auth.questions(); len(q) != 0 {
		t.Fatalf("asked polkit %q", q)
	}

	// Another user's password needs polkit, and then not the current one.
	auth.mu.Lock()
	auth.allow = true
	auth.mu.Unlock()
	appendLine(t, passwdFile, "bob:x:1001:1001:Bob:/home/bob:/bin/bash")
	appendLine(t, shadowFile, "bob:"+fakeHash("bobsalt", "bobpass")+":20000:0:99999:7:::")
	if err := secretCall(t, f, "SetPassword", []string{"not bob's", "bobnew"}, "bob"); err != nil {
		t.Fatal(err)
	}
	if q := auth.questions(); len(q) != 1 || !strings.HasSuffix(q[0], " "+actionUsers) {
		t.Fatalf("asked %q", q)
	}
}

// A wrong current password costs a wait, and a user's password is checked
// one call at a time, so parallel calls cannot guess faster.
func TestSetPasswordSelfChecksOneAtATimeAndWaits(t *testing.T) {
	waiting, release := make(chan struct{}, 2), make(chan struct{})
	fakeWrongPasswordWait(t, func() {
		waiting <- struct{}{}
		<-release
	})
	fu, f, _ := usersBusEnv(t, false)
	errs := make(chan *dbus.Error, 2)
	for range 2 {
		go func() {
			var c *dbus.Call
			_ = withSecrets([]string{"wrong", "newpass"}, func(fd dbus.UnixFD) error {
				c = f.call("SetPassword", "anna", fd)
				return nil
			})
			errs <- asDBusError(c.Err)
		}()
	}
	checks := func() int {
		n := 0
		for _, c := range fu.calls() {
			if c.name == "openssl" {
				n++
			}
		}
		return n
	}
	<-waiting
	time.Sleep(200 * time.Millisecond)
	select {
	case <-waiting:
		t.Fatal("two checks waited at once")
	default:
	}
	if n := checks(); n != 1 {
		t.Fatalf("%d checks while the first one waits, want 1", n)
	}
	close(release)
	for range 2 {
		wantBusErr(t, "wrong current password", <-errs, "Refused", errWrongPassword.Error())
	}
	if n := checks(); n != 2 {
		t.Fatalf("%d checks, want 2", n)
	}
	if fu.ran("chpasswd") {
		t.Fatal("chpasswd ran")
	}
}

func TestSetPasswordSelfRefusesAnUnverifiableHash(t *testing.T) {
	fu, f, _ := usersBusEnv(t, true)
	writeLines(t, shadowFile, []string{"anna:$y$j9T$salt$hash:20000:0:99999:7:::"})
	err := secretCall(t, f, "SetPassword", []string{"annapass", "newpass"}, "anna")
	wantBusErr(t, "unverifiable hash", err, "Refused", "change it with passwd in a terminal")
	if fu.ran("chpasswd") {
		t.Fatal("chpasswd ran")
	}
}

func TestSetPasswordSelfFromAFlatpakIsRefused(t *testing.T) {
	fakeProc(t, uint32(os.Getpid()), true)
	fu, f, auth := usersBusEnv(t, true)
	err := secretCall(t, f, "SetPassword", []string{"annapass", "newpass"}, "anna")
	wantBusErr(t, "self from a sandbox", err, "NotAuthorized", "")
	if len(fu.calls()) != 0 || len(auth.questions()) != 0 {
		t.Fatalf("ran %v, asked %q", fu.calls(), auth.questions())
	}
}

func TestUserMethodsRefuseAnUnknownCaller(t *testing.T) {
	for _, name := range []string{"", "error"} {
		t.Run("name "+name, func(t *testing.T) {
			fu, f, _ := usersBusEnvAs(t, true, func(uint32) (string, error) {
				if name == "error" {
					return "", errors.New("no such uid")
				}
				return "", nil
			})
			appendLine(t, passwdFile, "bob:x:1001:1001:Bob:/home/bob:/bin/bash")
			wantBusErr(t, "RemoveUser", asDBusError(f.call("RemoveUser", "bob", true).Err), "NotAuthorized", "")
			wantBusErr(t, "SetPassword", secretCall(t, f, "SetPassword", []string{"", "newpass"}, "anna"), "NotAuthorized", "")
			if len(fu.calls()) != 0 {
				t.Fatalf("ran %v", fu.calls())
			}
		})
	}
}

func TestRemoveUserPassesTheCaller(t *testing.T) {
	_, f, _ := usersBusEnv(t, true)
	wantBusErr(t, "remove self", asDBusError(f.call("RemoveUser", "anna", true).Err), "Refused", errSelfRemove.Error())
}

func TestUserGuardsAreRefused(t *testing.T) {
	fu, f, _ := usersBusEnv(t, true)
	wantBusErr(t, "SetAdmin off for the last admin", asDBusError(f.call("SetAdmin", "anna", false).Err), "Refused", errLastAdmin.Error())
	if err := asDBusError(f.call("SetAdmin", "anna", false).Err); err.Body[0] != errLastAdmin.Error() {
		t.Fatalf("message %v", err.Body)
	}
	if fu.ran("gpasswd") {
		t.Fatal("gpasswd ran")
	}
	wantBusErr(t, "AddUser with a bad name", secretCall(t, f, "AddUser", []string{"pw"}, "Bad Name", "", false), "Refused", errBadUserName.Error())
}

// TestUserMethodsCloseTheFdWhenRefused passes the read end of a pipe and
// closes this process's copy: once the service has closed its copy, a
// write fails with EPIPE.
func TestUserMethodsCloseTheFdWhenRefused(t *testing.T) {
	call := func(t *testing.T, f *busFixture, method string, args ...any) {
		t.Helper()
		r, w, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		defer w.Close()
		c := f.call(method, append(args, dbus.UnixFD(r.Fd()))...)
		r.Close()
		if c.Err == nil {
			t.Fatalf("%s was not refused", method)
		}
		if _, err := w.Write([]byte("x\n")); !errors.Is(err, syscall.EPIPE) {
			t.Fatalf("%s (%v): the service kept the fd open (write: %v)", method, c.Err, err)
		}
	}
	t.Run("polkit says no", func(t *testing.T) {
		_, f, _ := usersBusEnv(t, false)
		call(t, f, "AddUser", "bob", "Bob", false)
		call(t, f, "SetPassword", "bob")
	})
	t.Run("unknown caller", func(t *testing.T) {
		_, f, _ := usersBusEnvAs(t, true, func(uint32) (string, error) { return "", errors.New("no") })
		call(t, f, "SetPassword", "anna")
	})
	t.Run("self from a sandbox", func(t *testing.T) {
		fakeProc(t, uint32(os.Getpid()), true)
		_, f, _ := usersBusEnv(t, true)
		call(t, f, "SetPassword", "anna")
	})
}
