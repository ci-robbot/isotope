package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"
)

// ---- the words ----

func TestFingerWords(t *testing.T) {
	if got := fingerLabel("right-index-finger"); got != "Right index finger" {
		t.Errorf("fingerLabel: %q", got)
	}
	if got := fingerShort("left-thumb"); got != "Thumb" {
		t.Errorf("fingerShort: %q", got)
	}
	if got := fingerPhrase("left-little-finger"); got != "your left little finger" {
		t.Errorf("fingerPhrase: %q", got)
	}
	got := sortFingers([]string{"right-thumb", "weird", "left-index-finger", "right-thumb"})
	if want := []string{"left-index-finger", "right-thumb", "weird"}; !reflect.DeepEqual(got, want) {
		t.Errorf("sortFingers: %q", got)
	}
}

func TestEnrollMessages(t *testing.T) {
	for _, c := range []struct {
		result   string
		swipe    bool
		done     bool
		fails    bool
		contains string
	}{
		{"enroll-stage-passed", false, false, false, "touch the reader again"},
		{"enroll-stage-passed", true, false, false, "swipe the reader again"},
		{"enroll-retry-scan", false, false, false, "Touch the reader again"},
		{"enroll-completed", false, true, false, ""},
		{"enroll-duplicate", false, true, true, ""},
		{"enroll-data-full", false, true, true, ""},
		{"enroll-disconnected", false, true, true, ""},
		{"enroll-something-new", false, true, true, ""},
	} {
		msg, done, err := enrollMessage(c.result, c.swipe)
		if done != c.done || (err != nil) != c.fails || !strings.Contains(msg, c.contains) {
			t.Errorf("%s (swipe %v): %q %v %v", c.result, c.swipe, msg, done, err)
		}
	}
}

// ---- fprintd on a private bus ----

// fakeFprintDevice is fprintd's Device object: one user's prints, a claim,
// and an enrollment that sends the statuses in script.
type fakeFprintDevice struct {
	conn   *dbus.Conn
	path   dbus.ObjectPath
	mu     sync.Mutex
	claim  string
	prints []string
	script []string
	calls  []string
}

func (d *fakeFprintDevice) log(s string) {
	d.mu.Lock()
	d.calls = append(d.calls, s)
	d.mu.Unlock()
}

func (d *fakeFprintDevice) Claim(user string) *dbus.Error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.claim != "" {
		return dbus.NewError("net.reactivated.Fprint.Error.AlreadyInUse", []any{"in use"})
	}
	d.claim = "me"
	d.calls = append(d.calls, "Claim "+user)
	return nil
}

func (d *fakeFprintDevice) Release() *dbus.Error {
	d.mu.Lock()
	d.claim = ""
	d.mu.Unlock()
	d.log("Release")
	return nil
}

func (d *fakeFprintDevice) ListEnrolledFingers(user string) ([]string, *dbus.Error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.prints) == 0 {
		return nil, dbus.NewError(fprintNoPrints, []any{"none"})
	}
	return append([]string(nil), d.prints...), nil
}

func (d *fakeFprintDevice) EnrollStart(finger string) *dbus.Error {
	d.log("EnrollStart " + finger)
	d.mu.Lock()
	script := append([]string(nil), d.script...)
	d.mu.Unlock()
	go func() {
		for _, s := range script {
			time.Sleep(5 * time.Millisecond)
			d.conn.Emit(d.path, fprintDeviceIf+".EnrollStatus", s, s != "enroll-stage-passed" && s != "enroll-retry-scan")
		}
		if len(script) > 0 && script[len(script)-1] == "enroll-completed" {
			d.mu.Lock()
			d.prints = append(d.prints, finger)
			d.mu.Unlock()
		}
	}()
	return nil
}

func (d *fakeFprintDevice) EnrollStop() *dbus.Error { d.log("EnrollStop"); return nil }

func (d *fakeFprintDevice) DeleteEnrolledFinger(finger string) *dbus.Error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.calls = append(d.calls, "DeleteEnrolledFinger "+finger)
	for i, p := range d.prints {
		if p == finger {
			d.prints = append(d.prints[:i], d.prints[i+1:]...)
			return nil
		}
	}
	return dbus.NewError(fprintNoPrints, []any{"none"})
}

// set sets the prints and the enrollment's statuses (nil keeps them).
func (d *fakeFprintDevice) set(prints, script []string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if prints != nil {
		d.prints = prints
	}
	if script != nil {
		d.script = script
	}
}

func (d *fakeFprintDevice) called() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.calls...)
}

type fakeFprintProps struct{}

func (fakeFprintProps) Get(iface, name string) (dbus.Variant, *dbus.Error) {
	switch name {
	case "name":
		return dbus.MakeVariant("Synaptics Sensors"), nil
	case "scan-type":
		return dbus.MakeVariant("press"), nil
	case "num-enroll-stages":
		return dbus.MakeVariant(int32(3)), nil
	}
	return dbus.Variant{}, dbus.NewError("org.freedesktop.DBus.Error.UnknownProperty", nil)
}

type fakeFprintManager struct{ dev dbus.ObjectPath }

func (m fakeFprintManager) GetDefaultDevice() (dbus.ObjectPath, *dbus.Error) {
	if m.dev == "" {
		return "", dbus.NewError(fprintNoDevice, []any{"no devices"})
	}
	return m.dev, nil
}

// startFakeFprintd owns fprintd's name on a private bus, with a device or
// none, and returns a client on another connection.
func startFakeFprintd(t *testing.T, withDevice bool) (*fprintd, *fakeFprintDevice, string) {
	t.Helper()
	addr := privateBus(t)
	srv := dial(t, addr)
	dev := &fakeFprintDevice{conn: srv, path: "/net/reactivated/Fprint/Device/0"}
	m := fakeFprintManager{}
	if withDevice {
		m.dev = dev.path
		srv.Export(dev, dev.path, fprintDeviceIf)
		srv.Export(fakeFprintProps{}, dev.path, "org.freedesktop.DBus.Properties")
	}
	srv.Export(m, fprintManager, fprintManagerIf)
	if r, err := srv.RequestName(fprintName, dbus.NameFlagDoNotQueue); err != nil || r != dbus.RequestNameReplyPrimaryOwner {
		t.Fatalf("own %s: %v %v", fprintName, r, err)
	}
	cli := dial(t, addr)
	return &fprintd{conn: cli, user: "anna", uid: 1000}, dev, addr
}

func TestFprintdStateNotInstalled(t *testing.T) {
	f := &fprintd{conn: dial(t, privateBus(t))}
	st, err := f.State()
	if err != nil || st.Installed {
		t.Fatalf("%+v %v", st, err)
	}
}

func TestFprintdStateNoReader(t *testing.T) {
	f, _, _ := startFakeFprintd(t, false)
	st, err := f.State()
	if err != nil || !st.Installed || st.Reader != "" {
		t.Fatalf("%+v %v", st, err)
	}
}

func TestFprintdStateListsTheFingers(t *testing.T) {
	f, dev, _ := startFakeFprintd(t, true)
	st, err := f.State()
	if err != nil || st.Reader != "Synaptics Sensors" || st.Stages != 3 || st.Swipe || len(st.Enrolled) != 0 {
		t.Fatalf("%+v %v", st, err)
	}
	dev.set([]string{"right-thumb", "left-index-finger"}, nil)
	st, _ = f.State()
	if want := []string{"left-index-finger", "right-thumb"}; !reflect.DeepEqual(st.Enrolled, want) {
		t.Fatalf("enrolled %q", st.Enrolled)
	}
}

func TestFprintdEnrollFollowsTheStatuses(t *testing.T) {
	f, dev, addr := startFakeFprintd(t, true)
	dev.set(nil, []string{"enroll-stage-passed", "enroll-retry-scan", "enroll-stage-passed", "enroll-stage-passed", "enroll-completed"})
	// A signal with the same name from another connection is not fprintd's.
	other := dial(t, addr)
	go func() {
		time.Sleep(2 * time.Millisecond)
		other.Emit(dev.path, fprintDeviceIf+".EnrollStatus", "enroll-completed", true)
	}()
	var steps []EnrollStep
	var mu sync.Mutex
	err := f.Enroll(context.Background(), "right-index-finger", nil, func(s EnrollStep) {
		mu.Lock()
		steps = append(steps, s)
		mu.Unlock()
	})
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(steps) != 5 || steps[4].Passed != 3 || !steps[4].Done || steps[1].Message == "" {
		t.Fatalf("steps %+v", steps)
	}
	eventually(t, "the release", func() bool {
		c := strings.Join(dev.called(), ",")
		return c == "Claim ,EnrollStart right-index-finger,EnrollStop,Release"
	})
}

func TestFprintdEnrollReportsAFailure(t *testing.T) {
	f, dev, _ := startFakeFprintd(t, true)
	dev.set(nil, []string{"enroll-stage-passed", "enroll-duplicate"})
	err := f.Enroll(context.Background(), "left-thumb", nil, func(EnrollStep) {})
	if err == nil || !strings.Contains(err.Error(), "already enrolled") {
		t.Fatalf("err %v", err)
	}
}

func TestFprintdEnrollStopsWhenCancelled(t *testing.T) {
	f, dev, _ := startFakeFprintd(t, true)
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(20 * time.Millisecond); cancel() }()
	if err := f.Enroll(ctx, "left-thumb", nil, func(EnrollStep) {}); !errors.Is(err, context.Canceled) {
		t.Fatalf("err %v", err)
	}
	eventually(t, "stop and release", func() bool {
		c := strings.Join(dev.called(), ",")
		return strings.HasSuffix(c, "EnrollStop,Release")
	})
}

func TestFprintdDeleteClaimsAndReleases(t *testing.T) {
	f, dev, _ := startFakeFprintd(t, true)
	dev.set([]string{"left-thumb"}, nil)
	if err := f.Delete(context.Background(), "left-thumb", nil); err != nil {
		t.Fatal(err)
	}
	if c := strings.Join(dev.called(), ","); c != "Claim ,DeleteEnrolledFinger left-thumb,Release" {
		t.Fatalf("calls %s", c)
	}
	err := f.Delete(context.Background(), "left-thumb", nil)
	if dbusErrName(err) != fprintNoPrints || !strings.Contains(describeFprint(err), "not enrolled") {
		t.Fatalf("err %v", err)
	}
}

// ---- the polkit agent ----

// fakeHelper writes a stand-in for polkit-agent-helper-1: it checks its
// argument and the cookie, says what pam_fprintd would say, asks a
// password and answers SUCCESS for "right".
func fakeHelper(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "helper")
	script := `#!/bin/sh
[ "$1" = anna ] || { echo FAILURE; exit 1; }
read -r cookie
[ "$cookie" = c00k1e ] || { echo FAILURE; exit 1; }
printf '%s\n' 'PAM_TEXT_INFO Place your finger on the reader\nor type the password'
printf '%s\n' 'PAM_PROMPT_ECHO_OFF Password: '
read -r pw
if [ "$pw" = right ]; then echo SUCCESS; else echo 'PAM_ERROR_MSG Authentication failure'; echo FAILURE; fi
`
	if err := os.WriteFile(p, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestRunHelperSpeaksPolkitsProtocol(t *testing.T) {
	h := fakeHelper(t)
	var asked []AuthAsk
	ask := func(pw string, ok bool) asker {
		return func(_ context.Context, q AuthAsk) (string, bool) { asked = append(asked, q); return pw, ok }
	}
	res, err := runHelper(context.Background(), h, "anna", "c00k1e", "msg", false, ask("right", true))
	if err != nil || res != helperSuccess {
		t.Fatalf("right: %v %v", res, err)
	}
	if len(asked) != 1 || asked[0].Prompt != "Password: " || asked[0].Info != "Place your finger on the reader\nor type the password" {
		t.Fatalf("asked %+v", asked)
	}
	if res, err := runHelper(context.Background(), h, "anna", "c00k1e", "msg", false, ask("wrong", true)); err != nil || res != helperFailure {
		t.Fatalf("wrong: %v %v", res, err)
	}
	if res, _ := runHelper(context.Background(), h, "anna", "c00k1e", "msg", false, ask("", false)); res != helperCancelled {
		t.Fatalf("cancel: %v", res)
	}
}

func TestAgentAnswersOnlyForItsOwnUser(t *testing.T) {
	tries := 0
	a := &polkitAgent{user: "anna", uid: 1000, helper: fakeHelper(t), cancels: map[string]context.CancelFunc{},
		ask: func(_ context.Context, q AuthAsk) (string, bool) {
			tries++
			if tries < 3 {
				if tries == 2 && !q.Retry {
					t.Error("the second try is not marked as a retry")
				}
				return "wrong", true
			}
			return "right", true
		}}
	type ident = struct {
		Kind    string
		Details map[string]dbus.Variant
	}
	admin := []ident{{"unix-group", map[string]dbus.Variant{"gid": dbus.MakeVariant(uint32(900))}}, {"unix-user", map[string]dbus.Variant{"uid": dbus.MakeVariant(uint32(0))}}}
	if err := a.BeginAuthentication("x", "m", "", nil, "c00k1e", admin); err == nil || err.Name != polkitFailed {
		t.Fatalf("another identity: %v", err)
	}
	if tries != 0 {
		t.Fatal("asked for another identity's password")
	}
	own := []ident{{"unix-user", map[string]dbus.Variant{"uid": dbus.MakeVariant(uint32(1000))}}}
	if err := a.BeginAuthentication("x", "m", "", nil, "c00k1e", own); err != nil {
		t.Fatalf("third try: %v", err)
	}
	if tries != 3 {
		t.Fatalf("%d tries", tries)
	}
	a.ask = func(context.Context, AuthAsk) (string, bool) { return "", false }
	if err := a.BeginAuthentication("x", "m", "", nil, "c00k1e", own); err == nil || err.Name != polkitCancelled {
		t.Fatalf("cancel: %v", err)
	}
}

func TestProcessStartTime(t *testing.T) {
	if n, err := processStartTime(); err != nil || n == 0 {
		t.Fatalf("%d %v", n, err)
	}
	if unescapeC(`a\nb\\c`) != "a\nb\\c" || unescapeC("plain") != "plain" {
		t.Fatal("unescapeC")
	}
}

// ---- the section ----

// fakeFingers is Fingerprints for the page.
type fakeFingers struct {
	mu      sync.Mutex
	st      FingerState
	calls   []string
	pw      string // the password the last ask answered
	askPass bool   // Enroll and Delete ask for a password first
	steps   []EnrollStep
	err     error
	hold    chan struct{} // Enroll waits on it before its steps
}

func (f *fakeFingers) State() (FingerState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	st := f.st
	st.Enrolled = append([]string(nil), f.st.Enrolled...)
	return st, nil
}

func (f *fakeFingers) asked(ctx context.Context, ask asker) bool {
	if !f.askPass {
		return true
	}
	pw, ok := ask(ctx, AuthAsk{Prompt: "Password: ", Info: "Place your finger on the reader"})
	f.mu.Lock()
	f.pw = pw
	f.mu.Unlock()
	return ok
}

func (f *fakeFingers) Enroll(ctx context.Context, finger string, ask asker, progress func(EnrollStep)) error {
	f.mu.Lock()
	f.calls = append(f.calls, "Enroll "+finger)
	f.mu.Unlock()
	if !f.asked(ctx, ask) {
		return dbus.NewError("net.reactivated.Fprint.Error.PermissionDenied", nil)
	}
	if f.hold != nil {
		select {
		case <-f.hold:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	for _, s := range f.steps {
		progress(s)
	}
	if f.err != nil {
		return f.err
	}
	f.mu.Lock()
	f.st.Enrolled = sortFingers(append(f.st.Enrolled, finger))
	f.mu.Unlock()
	return nil
}

func (f *fakeFingers) Delete(ctx context.Context, finger string, ask asker) error {
	f.mu.Lock()
	f.calls = append(f.calls, "Delete "+finger)
	f.mu.Unlock()
	if !f.asked(ctx, ask) {
		return dbus.NewError("net.reactivated.Fprint.Error.PermissionDenied", nil)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	var keep []string
	for _, x := range f.st.Enrolled {
		if x != finger {
			keep = append(keep, x)
		}
	}
	f.st.Enrolled = keep
	return nil
}

func (f *fakeFingers) called() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return strings.Join(f.calls, ",")
}

func readerState(fingers ...string) FingerState {
	return FingerState{Installed: true, Reader: "Synaptics Sensors", Stages: 3, Enrolled: fingers}
}

// fingersUI is the Users page with f as fprintd, loaded.
func fingersUI(t *testing.T, admin bool, f Fingerprints) (*UI, *usersPage) {
	t.Helper()
	u, _, _ := testUI(t, admin)
	u.hooks.Fingers = f
	p := usersPageOf(t, u)
	eventually(t, "the fingerprints", func() bool { u.mu.Lock(); defer u.mu.Unlock(); return p.fp.loaded })
	return u, p
}

func TestFingerprintSectionOffersTheExtension(t *testing.T) {
	for _, f := range []Fingerprints{nil, &fakeFingers{}} {
		u, p := fingersUI(t, true, f)
		st, _, _ := p.fp.state()
		if st.Installed {
			t.Fatal("installed")
		}
		u.show(PageUsers)
		u.Layout(gtxOf(1100, 720))
		// The button opens the Extensions page.
		p.fp.install.Click()
		u.Layout(gtxOf(1100, 720))
		if u.page != PageExtensions {
			t.Fatalf("on %s", u.page)
		}
	}
}

func TestFingerprintSectionAddsAFinger(t *testing.T) {
	f := &fakeFingers{st: readerState("left-thumb"), askPass: true,
		steps: []EnrollStep{{Passed: 1, Message: "Good."}, {Passed: 2}, {Passed: 3, Done: true}}, hold: make(chan struct{})}
	u, p := fingersUI(t, false, f)
	p.fp.openAdd()
	q := waitQuestion(t, u, fpPickTitle)
	if p.fp.picked != "right-index-finger" {
		t.Fatalf("picked %q", p.fp.picked)
	}
	u.pick(q, 1)
	// polkit asks for the password over the enrollment.
	pq := waitQuestion(t, u, fpPassTitle)
	if !strings.Contains(pq.body, "Place your finger on the reader") {
		t.Fatalf("body %q", pq.body)
	}
	if pq.choices[1].enabled() {
		t.Fatal("OK is on with no password")
	}
	u.mu.Lock()
	field := p.fp.pass
	u.mu.Unlock()
	typeInto(field, "s3cret")
	u.pick(pq, 1)
	// The enrollment's dialog comes back and follows the steps.
	eq := waitQuestion(t, u, fpEnrollHead)
	do, _ := p.fp.enrollText(readerState())
	if !strings.Contains(do, "Touch the reader with your right index finger") {
		t.Fatalf("do %q", do)
	}
	close(f.hold)
	waitQuestion(t, u, "Fingerprint added")
	_ = eq
	f.mu.Lock()
	pw := f.pw
	f.mu.Unlock()
	if pw != "s3cret" || f.called() != "Enroll right-index-finger" {
		t.Fatalf("pw %q calls %s", pw, f.called())
	}
	if field.Editor.Text() != "" {
		t.Fatal("the password stays in the field")
	}
	eventually(t, "the new row", func() bool {
		st, _, _ := p.fp.state()
		return reflect.DeepEqual(st.Enrolled, []string{"left-thumb", "right-index-finger"})
	})
}

func TestFingerprintSectionCancelsAnEnrollment(t *testing.T) {
	f := &fakeFingers{st: readerState(), hold: make(chan struct{})}
	u, p := fingersUI(t, false, f)
	p.fp.enroll("left-index-finger", readerState())
	q := waitQuestion(t, u, fpEnrollHead)
	u.pick(q, 0)
	eventually(t, "the enrollment stops", func() bool { u.mu.Lock(); defer u.mu.Unlock(); return p.fp.enrolling == "" })
	time.Sleep(20 * time.Millisecond)
	if d := u.question(); d != nil {
		t.Fatalf("a dialog after Cancel: %q", d.title)
	}
}

func TestFingerprintSectionSaysWhyAnEnrollmentFailed(t *testing.T) {
	f := &fakeFingers{st: readerState(), err: errors.New("this finger is already enrolled, for you or another user")}
	u, p := fingersUI(t, false, f)
	p.fp.enroll("left-index-finger", readerState())
	q := waitQuestion(t, u, "Could not add the fingerprint")
	if !strings.Contains(q.body, "already enrolled") {
		t.Fatalf("body %q", q.body)
	}
}

func TestFingerprintSectionRemovesAFinger(t *testing.T) {
	f := &fakeFingers{st: readerState("left-thumb", "right-thumb")}
	u, p := fingersUI(t, false, f)
	p.fp.openRemove("right-thumb")
	q := waitQuestion(t, u, "Remove Right thumb?")
	u.pick(q, 1)
	eventually(t, "Delete", func() bool { return f.called() == "Delete right-thumb" })
	eventually(t, "the row goes", func() bool {
		st, _, _ := p.fp.state()
		return reflect.DeepEqual(st.Enrolled, []string{"left-thumb"})
	})
}

func TestFingerprintSectionStates(t *testing.T) {
	for _, st := range []FingerState{
		{Installed: true},
		{Installed: true, NeedsRestart: true},
		readerState(),
		readerState("left-thumb", "right-index-finger"),
	} {
		u, _ := fingersUI(t, false, &fakeFingers{st: st})
		u.show(PageUsers)
		for _, sz := range [][2]int{{1100, 720}, {700, 500}} {
			if d := u.Layout(gtxOf(sz[0], sz[1])); d.Size.X != sz[0] {
				t.Errorf("%+v: %v", st, d.Size)
			}
		}
	}
}
