package main

import (
	"bytes"
	"context"
	"errors"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"
)

// trayFake is a daemonClient safe to use from the tray's goroutine and the
// test at once. Check moves the status to afterCheck, as the daemon would.
type trayFake struct {
	mu          sync.Mutex
	st          Status
	afterCheck  *Status
	tags        []string
	inspectErr  error
	refusal     string
	rebootErr   error
	previousErr error
	calls       []string
	events      chan Event
	watches     int
}

func (f *trayFake) rec(s string) { f.mu.Lock(); f.calls = append(f.calls, s); f.mu.Unlock() }
func (f *trayFake) count(s string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		if c == s {
			n++
		}
	}
	return n
}
func (f *trayFake) Status() (Status, error) {
	f.rec("Status")
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.st, nil
}
func (f *trayFake) Check() error {
	f.rec("Check")
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.afterCheck != nil {
		f.st = *f.afterCheck
	}
	return nil
}
func (f *trayFake) Releases() ([]string, error) { f.rec("Releases"); return f.tags, nil }
func (f *trayFake) Inspect(ref string, adv bool) (InspectInfo, error) {
	f.rec("Inspect " + ref)
	if f.inspectErr != nil {
		return InspectInfo{}, f.inspectErr
	}
	return InspectInfo{Ref: ref, Digest: digestA, Trusted: true, Refusal: f.refusal}, nil
}
func (f *trayFake) Apply(ref, digest string, adv bool) error { f.rec("Apply"); return nil }
func (f *trayFake) Cancel() error                            { f.rec("Cancel"); return nil }
func (f *trayFake) Reboot() error                            { f.rec("Reboot"); return f.rebootErr }
func (f *trayFake) RebootPrevious() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "RebootPrevious")
	return f.previousErr
}
func (f *trayFake) Watch() (<-chan Event, func(), error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = make(chan Event, 16)
	f.watches++
	return f.events, func() {}, nil
}
func (f *trayFake) Close() {}

// push sends ev on the current watch channel; lose closes it, as when the
// service's name loses its owner.
func (f *trayFake) push(ev Event) { f.mu.Lock(); ch := f.events; f.mu.Unlock(); ch <- ev }
func (f *trayFake) lose()         { f.mu.Lock(); close(f.events); f.mu.Unlock() }

// recUI keeps every view the tray showed.
type recUI struct {
	mu    sync.Mutex
	views []trayView
}

func (u *recUI) Show(v trayView) { u.mu.Lock(); u.views = append(u.views, v); u.mu.Unlock() }

// waitView waits until the latest view satisfies ok.
func (u *recUI) waitView(t *testing.T, what string, ok func(trayView) bool) trayView {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		u.mu.Lock()
		var last trayView
		n := len(u.views)
		if n > 0 {
			last = u.views[n-1]
		}
		u.mu.Unlock()
		if n > 0 && ok(last) {
			return last
		}
		time.Sleep(2 * time.Millisecond)
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	t.Fatalf("never showed %s; views %+v", what, u.views)
	return trayView{}
}

// syncBuf is a log sink the tray goroutine and the test share.
type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) { s.mu.Lock(); defer s.mu.Unlock(); return s.b.Write(p) }
func (s *syncBuf) String() string              { s.mu.Lock(); defer s.mu.Unlock(); return s.b.String() }

// trayHarness runs a trayApp against f until the test ends.
type trayHarness struct {
	app      *trayApp
	ui       *recUI
	log      *syncBuf
	mu       sync.Mutex
	notes    []string
	launches [][]string
	menus    []string
	// actions are the notifications with a button; invoke presses the
	// last one's button.
	actions []string
	invoke  func()
}

func (h *trayHarness) notified() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.notes...)
}

func (h *trayHarness) launched() [][]string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([][]string(nil), h.launches...)
}

func startTray(t *testing.T, dial func() (daemonClient, error), first, every time.Duration, opts ...func(*trayApp)) *trayHarness {
	t.Helper()
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	h := &trayHarness{ui: &recUI{}, log: &syncBuf{}}
	rel := Release{Flavor: "sway", Version: "v0.1.0", ImageRepo: "ghcr.io/kairos-community/isotope", GitHubRepo: "kairos-community/isotope"}
	h.app = newTrayApp(rel, h.ui, dial, h.log)
	h.app.firstCheck, h.app.interval, h.app.retry = first, every, 5*time.Millisecond
	// The terminal path; TestTrayOpensIsotopeSystem sets its own.
	h.app.system = nil
	h.app.notify = func(summary, body string) error {
		h.mu.Lock()
		h.notes = append(h.notes, summary+" | "+body)
		h.mu.Unlock()
		return nil
	}
	h.app.notifyAction = func(summary, body, label string, invoked func()) error {
		h.mu.Lock()
		h.actions = append(h.actions, summary+" | "+body+" | "+label)
		h.invoke = invoked
		h.mu.Unlock()
		return nil
	}
	h.app.launch = func(args ...string) error {
		h.mu.Lock()
		h.launches = append(h.launches, args)
		h.mu.Unlock()
		return nil
	}
	h.app.openMenu = func(entry string) error {
		h.mu.Lock()
		h.menus = append(h.menus, entry)
		h.mu.Unlock()
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	for _, o := range opts {
		o(h.app)
	}
	go func() { h.app.run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	return h
}

func dialFake(f *trayFake) func() (daemonClient, error) {
	return func() (daemonClient, error) { return f, nil }
}

func available() *Status {
	return &Status{State: StateUpdateAvailable, Installed: "v0.1.0", Latest: "v0.2.0", Advanced: true}
}

func TestTrayChecksThenNotifiesOncePerTag(t *testing.T) {
	f := &trayFake{st: Status{State: StateIdle, Installed: "v0.1.0", Advanced: true}, afterCheck: available(), tags: []string{"v0.2.0", "v0.1.0"}}
	h := startTray(t, dialFake(f), time.Millisecond, 5*time.Millisecond)
	v := h.ui.waitView(t, "the update", func(v trayView) bool {
		_, ok := find(v.Menu, "releases/v0.2.0")
		return v.Icon == "update" && ok
	})
	if it, _ := find(v.Menu, "install-latest"); it.Label != "Install v0.2.0" {
		t.Fatalf("menu %+v", v.Menu)
	}
	for f.count("Check") < 3 {
		time.Sleep(time.Millisecond)
	}
	notes := h.notified()
	if len(notes) != 1 || !strings.Contains(notes[0], "Isotope v0.2.0 is available") || !strings.Contains(notes[0], "menu") {
		t.Fatalf("notifications %q, want one pointing at the tray menu", notes)
	}
	if strings.Contains(notes[0], "terminal") {
		t.Fatalf("notification still sends the user to a terminal: %q", notes[0])
	}
	if f.count("Inspect ghcr.io/kairos-community/isotope:sway-v0.2.0") != 1 {
		t.Fatalf("calls %v: the release image must be inspected once before announcing it", f.calls)
	}
}

func TestTrayWaitsBeforeTheFirstCheck(t *testing.T) {
	f := &trayFake{st: Status{State: StateIdle, Installed: "v0.1.0"}}
	h := startTray(t, dialFake(f), time.Hour, time.Hour)
	h.ui.waitView(t, "the idle icon", func(v trayView) bool { return v.Icon == "idle" })
	time.Sleep(20 * time.Millisecond)
	if f.count("Check") != 0 {
		t.Fatal("checked on the login path")
	}
}

func TestTrayQuietWhilePinned(t *testing.T) {
	pinned := &Status{State: StateUpToDate, Installed: "v0.1.0", Latest: "v0.2.0", Pin: "ghcr.io/x/y:z"}
	f := &trayFake{st: Status{State: StateIdle}, afterCheck: pinned}
	h := startTray(t, dialFake(f), time.Millisecond, 5*time.Millisecond)
	v := h.ui.waitView(t, "the pinned status", func(v trayView) bool { return strings.Contains(v.Tooltip, "pinned") })
	if it, _ := find(v.Menu, "install-latest"); it.Label != "Install latest (v0.2.0)" {
		t.Fatalf("menu %+v", v.Menu)
	}
	for f.count("Check") < 2 {
		time.Sleep(time.Millisecond)
	}
	if n := h.notified(); len(n) != 0 {
		t.Fatalf("notified while pinned: %q", n)
	}
}

func TestTrayQuietWhenTheImageIsNotThere(t *testing.T) {
	f := &trayFake{st: Status{State: StateIdle}, afterCheck: available(), inspectErr: errors.New("manifest unknown")}
	h := startTray(t, dialFake(f), time.Millisecond, 5*time.Millisecond)
	for f.count("Check") < 2 {
		time.Sleep(time.Millisecond)
	}
	if n := h.notified(); len(n) != 0 || alreadyNotified("v0.2.0") {
		t.Fatalf("announced an image that cannot be installed: %q", n)
	}
}

func TestTrayUnmanagedNeverChecks(t *testing.T) {
	f := &trayFake{st: Status{State: StateUnmanaged, Advanced: true}}
	h := startTray(t, dialFake(f), time.Millisecond, time.Millisecond)
	v := h.ui.waitView(t, "unmanaged", func(v trayView) bool { return len(v.Menu) > 0 })
	if got := strings.Join(ids(v.Menu), ","); got != "status" {
		t.Fatalf("menu %v", got)
	}
	time.Sleep(20 * time.Millisecond)
	if f.count("Check") != 0 || f.count("Releases") != 0 {
		t.Fatalf("calls %v on an unmanaged system", f.calls)
	}
}

func TestTrayRetriesAnUnavailableService(t *testing.T) {
	f := &trayFake{st: Status{State: StateIdle, Installed: "v0.1.0"}}
	var mu sync.Mutex
	fails := 3
	dial := func() (daemonClient, error) {
		mu.Lock()
		defer mu.Unlock()
		if fails > 0 {
			fails--
			return nil, errors.New("no bus")
		}
		return f, nil
	}
	h := startTray(t, dial, time.Hour, time.Hour)
	h.ui.waitView(t, "the error icon", func(v trayView) bool {
		return v.Icon == "error" && v.Tooltip == "Update service not available"
	})
	h.ui.waitView(t, "recovery", func(v trayView) bool { return v.Icon == "idle" })
	// Three failed dials, one line: the loss is logged, not every retry.
	if n := strings.Count(h.log.String(), "not available"); n != 1 {
		t.Fatalf("logged the outage %d times: %q", n, h.log.String())
	}
}

// A scheduled check while a job runs would only get Busy, after retryBusy
// held the tray for seconds; it waits for the job instead.
func TestTraySkipsTheCheckDuringAJob(t *testing.T) {
	f := &trayFake{st: Status{State: StateApplying, Installed: "v0.1.0"}}
	h := startTray(t, dialFake(f), time.Millisecond, time.Millisecond)
	h.ui.waitView(t, "busy", func(v trayView) bool { return v.Icon == "busy" })
	time.Sleep(30 * time.Millisecond)
	if f.count("Check") != 0 {
		t.Fatal("checked while a job runs")
	}
	f.push(Event{Status: &Status{State: StateRebootPending, Installed: "v0.1.0"}})
	deadline := time.Now().Add(5 * time.Second)
	for f.count("Check") == 0 {
		if time.Now().After(deadline) {
			t.Fatal("no check after the job ended")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestTrayLogsAFailedNotificationRecord(t *testing.T) {
	f := &trayFake{st: Status{State: StateIdle}, afterCheck: available()}
	// A file where the state directory should be: markNotified cannot write.
	blocker := filepath.Join(t.TempDir(), "state")
	os.WriteFile(blocker, nil, 0o644)
	// Set as an option, after startTray's own XDG_STATE_HOME and before the
	// tray starts: set after startTray, a slow test host let the first check
	// record into the working directory and log nothing.
	h := startTray(t, dialFake(f), 0, time.Hour, func(*trayApp) { t.Setenv("XDG_STATE_HOME", blocker) })
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(h.log.String(), "recording that v0.2.0 was announced") {
		if time.Now().After(deadline) {
			t.Fatalf("log %q", h.log.String())
		}
		time.Sleep(time.Millisecond)
	}
}

func TestTrayProgressIsTheTooltip(t *testing.T) {
	f := &trayFake{st: Status{State: StateIdle, Installed: "v0.1.0", Latest: "v0.2.0"}}
	h := startTray(t, dialFake(f), time.Hour, time.Hour)
	h.ui.waitView(t, "idle", func(v trayView) bool { return v.Icon == "idle" })
	f.push(Event{Status: &Status{State: StateApplying, Installed: "v0.1.0", Latest: "v0.2.0"}})
	f.push(Event{Line: "pulling layer 3/7"})
	h.ui.waitView(t, "progress", func(v trayView) bool { return v.Icon == "busy" && v.Tooltip == "pulling layer 3/7" })
	f.push(Event{Status: &Status{State: StateRebootPending, Installed: "v0.1.0"}})
	v := h.ui.waitView(t, "reboot", func(v trayView) bool { return v.Icon == "reboot" })
	if _, ok := find(v.Menu, "restart"); !ok || strings.Contains(v.Tooltip, "pulling") {
		t.Fatalf("view %+v", v)
	}
}

func TestTrayServiceLostMidJob(t *testing.T) {
	f := &trayFake{st: Status{State: StateApplying, Installed: "v0.1.0"}}
	h := startTray(t, dialFake(f), time.Hour, time.Hour)
	h.ui.waitView(t, "busy", func(v trayView) bool { return v.Icon == "busy" })
	f.lose()
	h.ui.waitView(t, "the error", func(v trayView) bool {
		return v.Icon == "error" && strings.Contains(v.Tooltip, "stopped")
	})
	// It watches again, so a restarted service is seen.
	deadline := time.Now().Add(5 * time.Second)
	for {
		f.mu.Lock()
		w := f.watches
		f.mu.Unlock()
		if w >= 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("did not watch again")
		}
		time.Sleep(time.Millisecond)
	}
	f.push(Event{Status: &Status{State: StateIdle, Installed: "v0.1.0"}})
	h.ui.waitView(t, "idle again", func(v trayView) bool { return v.Icon == "idle" })
}

// An idle exit is normal: the menu stays as it was.
func TestTrayServiceIdleExitKeepsTheMenu(t *testing.T) {
	f := &trayFake{st: Status{State: StateUpToDate, Installed: "v0.1.0", Latest: "v0.1.0"}}
	h := startTray(t, dialFake(f), time.Hour, time.Hour)
	h.ui.waitView(t, "up to date", func(v trayView) bool { return v.Icon == "idle" })
	h.ui.mu.Lock()
	n := len(h.ui.views)
	h.ui.mu.Unlock()
	f.lose()
	time.Sleep(20 * time.Millisecond)
	h.ui.mu.Lock()
	defer h.ui.mu.Unlock()
	for _, v := range h.ui.views[n:] {
		if v.Icon != "idle" {
			t.Fatalf("view %+v after an idle exit", v)
		}
	}
}

func TestTrayClicks(t *testing.T) {
	f := &trayFake{st: Status{State: StateRebootPending, Installed: "v0.1.0"}}
	h := startTray(t, dialFake(f), time.Hour, time.Hour)
	h.ui.waitView(t, "reboot", func(v trayView) bool { return v.Icon == "reboot" })
	for _, id := range []string{"install-latest", "releases/v0.2.0", "switch/i3", "gpu/vm", "advanced", "cancel"} {
		h.app.clicks <- id
	}
	want := [][]string{
		{"upgrade", "--pause-on-error"},
		{"upgrade", "--pause-on-error", "--version", "v0.2.0"},
		{"switch", "i3", "--pause-on-error"},
		{"switch", "--gpu", "vm", "--pause-on-error"},
		{"upgrade", "--pause-on-error", "--image", "-"},
	}
	deadline := time.Now().Add(5 * time.Second)
	for f.count("Cancel") == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := h.launched(); !reflect.DeepEqual(got, want) {
		t.Fatalf("launched %q, want %q", got, want)
	}
	f.mu.Lock()
	f.rebootErr = &daemonError{Name: "NotAuthorized", Msg: "no"}
	f.mu.Unlock()
	h.app.clicks <- "restart"
	for len(h.notified()) == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if f.count("Reboot") != 1 || len(h.notified()) != 1 || !strings.Contains(h.notified()[0], "admin group") {
		t.Fatalf("reboot calls %d, notes %q", f.count("Reboot"), h.notified())
	}
}

// TestTrayOpensIsotopeSystem: with Isotope System installed, every item
// that needs an answer opens its page instead of a terminal or the Isotope
// menu; Cancel stays a click on the service; the menu offers the app.
func TestTrayOpensIsotopeSystem(t *testing.T) {
	f := &trayFake{st: Status{State: StateInspecting, Installed: "v0.1.0"}}
	var mu sync.Mutex
	var pages []string
	opened := make(chan struct{}, 16)
	h := startTray(t, dialFake(f), time.Hour, time.Hour, func(a *trayApp) {
		a.system = func(page string) error {
			mu.Lock()
			pages = append(pages, page)
			mu.Unlock()
			opened <- struct{}{}
			return nil
		}
	})
	h.ui.waitView(t, "the system item", func(v trayView) bool {
		return v.Icon == "busy" && len(v.Menu) > 0 && v.Menu[len(v.Menu)-1].ID == "system"
	})
	ids := []string{"install-latest", "releases/v0.2.0", "advanced", "switch/i3", "gpu/vm", "extensions", "recovery", "system"}
	for _, id := range ids {
		h.app.clicks <- id
	}
	for range ids {
		select {
		case <-opened:
		case <-time.After(5 * time.Second):
			t.Fatal("Isotope System was not opened for every item")
		}
	}
	h.app.clicks <- "cancel"
	deadline := time.Now().Add(5 * time.Second)
	for f.count("Cancel") == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	want := []string{"updates", "updates", "updates", "desktop", "desktop", "extensions", "security", "updates"}
	if !reflect.DeepEqual(pages, want) {
		t.Fatalf("pages %q, want %q", pages, want)
	}
	if len(h.launched()) != 0 || len(h.menus) != 0 {
		t.Fatalf("a terminal or the menu opened: %q %q", h.launched(), h.menus)
	}
	if f.count("Cancel") != 1 {
		t.Fatal("Cancel did not reach the service")
	}
}

func TestSystemPage(t *testing.T) {
	for id, want := range map[string]string{
		"install-latest": "updates", "releases/v1.0.0": "updates", "advanced": "updates",
		"switch/niri": "desktop", "gpu/full": "desktop", "extensions": "extensions",
		"recovery": "security", "system": "updates",
	} {
		if got, ok := systemPage(id); !ok || got != want {
			t.Errorf("%s: %q %v, want %q", id, got, ok, want)
		}
	}
	for _, id := range []string{"cancel", "restart", "rollback", "status"} {
		if _, ok := systemPage(id); ok {
			t.Errorf("%s opens Isotope System", id)
		}
	}
}

func TestSystemCommandRunsTheApp(t *testing.T) {
	old := execSystem
	t.Cleanup(func() { execSystem = old })
	var got []string
	execSystem = func(bin string, argv, env []string) error {
		got = append([]string{bin}, argv...)
		return nil
	}
	if code, _, errOut := runCLI("", "system", "backup"); code != exitOK {
		t.Fatalf("code %d, %q", code, errOut)
	}
	if want := []string{systemBinary, "isotope-system", "backup"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("ran %q, want %q", got, want)
	}
	execSystem = func(string, []string, []string) error { return errors.New("no such file") }
	if code, _, errOut := runCLI("", "system"); code != exitError || !strings.Contains(errOut, "isotope-system") {
		t.Fatalf("missing app: code %d, %q", code, errOut)
	}
}

// TestTrayExtensionsOpensTheIsotopeMenuEntry: "Extensions…" opens the same
// list as the Isotope menu, even while the service is unreachable.
func TestTrayExtensionsOpensTheIsotopeMenuEntry(t *testing.T) {
	h := startTray(t, func() (daemonClient, error) { return nil, errors.New("down") }, time.Hour, time.Hour)
	h.ui.waitView(t, "error", func(v trayView) bool { return v.Icon == "error" })
	h.app.clicks <- "extensions"
	deadline := time.Now().Add(5 * time.Second)
	for {
		h.mu.Lock()
		got := strings.Join(h.menus, ",")
		h.mu.Unlock()
		if got == "Extensions" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("menus opened %q", got)
		}
		time.Sleep(time.Millisecond)
	}
	if iconFor(StateExtension) != "busy" || !busyState(StateExtension) {
		t.Fatal("an extension job is not shown as busy")
	}
}

func TestTrayAndNotifyExitAtOnceOnAgent(t *testing.T) {
	setupAgent(t, "v0.1.0", "v0.2.0")
	dialDaemon = func() (daemonClient, error) {
		t.Error("the agent flavor dialled the update service")
		return nil, errors.New("no")
	}
	for _, args := range [][]string{{"tray"}, {"tray", "--no-icon"}, {"notify"}, {"notify", "--once"}} {
		if code, _, errOut := runCLI("", args...); code != exitOK {
			t.Errorf("%v: code %d, stderr %q", args, code, errOut)
		}
	}
}

func TestTerminalArgv(t *testing.T) {
	dir := t.TempDir()
	session := filepath.Join(dir, "session")
	t.Setenv("ISOTOPE_UPDATE_SESSION_FILE", session)
	bin := t.TempDir()
	t.Setenv("PATH", bin)
	var log bytes.Buffer

	os.WriteFile(session, []byte("DESKTOP_NAME=i3\nTERMINAL_EXEC=\"st -c IsotopeFloat -e\"\n"), 0o644)
	got, err := terminalArgv("/usr/bin/isotope-update", []string{"switch", "niri"}, &log)
	if want := []string{"st", "-c", "IsotopeFloat", "-e", "/usr/bin/isotope-update", "switch", "niri"}; err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("argv %q (%v), want %q", got, err, want)
	}

	os.WriteFile(session, []byte("DESKTOP_NAME=i3\n"), 0o644)
	if _, err := terminalArgv("hu", []string{"upgrade"}, &log); err == nil {
		t.Fatal("no terminal at all must be an error")
	}
	for _, name := range []string{"xterm", "foot"} {
		os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\n"), 0o755)
		got, err = terminalArgv("hu", []string{"upgrade"}, &log)
		want := map[string][]string{
			"xterm": {filepath.Join(bin, "xterm"), "-e", "hu", "upgrade"},
			"foot":  {filepath.Join(bin, "foot"), "hu", "upgrade"},
		}[name]
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("with %s: argv %q (%v), want %q", name, got, err, want)
		}
	}
	if !strings.Contains(log.String(), "TERMINAL_EXEC") {
		t.Fatalf("the fallback is not logged: %q", log.String())
	}
}

func TestIcons(t *testing.T) {
	// Points of the 512 box of the mark: on the ring's right side, where the
	// cut around the badge also passes; the badge's centre; and the badge
	// below its glyph.
	type pt struct{ x, y float64 }
	ring, centre, badge := pt{407, 286}, pt{404, 404}, pt{404, 492}
	for _, dark := range []bool{true, false} {
		th := iconThemes[dark]
		for _, kind := range iconKinds {
			for _, size := range []int{22, 44, 88} {
				img, err := png.Decode(bytes.NewReader(iconPNG(kind, dark, size)))
				if err != nil {
					t.Fatalf("%s/%v/%d: %v", kind, dark, size, err)
				}
				if b := img.Bounds(); b.Dx() != size || b.Dy() != size {
					t.Fatalf("%s/%v/%d: bounds %v", kind, dark, size, b)
				}
				if _, _, _, a := img.At(0, 0).RGBA(); a != 0 {
					t.Errorf("%s/%v/%d: corner not transparent", kind, dark, size)
				}
				if size < 44 {
					continue // too coarse for whole pixels inside each part
				}
				at := func(p pt) color.NRGBA {
					return color.NRGBAModel.Convert(img.At(int(p.x*float64(size)/512), int(p.y*float64(size)/512))).(color.NRGBA)
				}
				bg, badged := th.badge[kind]
				if !badged {
					if kind != "idle" {
						t.Fatalf("%s has no badge colour", kind)
					}
					if c := at(ring); c != th.mark {
						t.Errorf("idle/%v/%d: ring %v, want the mark %v", dark, size, c, th.mark)
					}
					if c := at(centre); c.A != 0 {
						t.Errorf("idle/%v/%d: a badge is drawn: %v", dark, size, c)
					}
					continue
				}
				if c := at(ring); c.A != 0 {
					t.Errorf("%s/%v/%d: no gap around the badge: %v", kind, dark, size, c)
				}
				if c := at(badge); c != bg {
					t.Errorf("%s/%v/%d: badge %v, want %v", kind, dark, size, c, bg)
				}
				// Every glyph but the restart arrow crosses the middle.
				if c := at(centre); kind != "reboot" && c != th.glyph {
					t.Errorf("%s/%v/%d: glyph %v, want %v", kind, dark, size, c, th.glyph)
				}
			}
		}
	}
}

func TestSchemeIsDark(t *testing.T) {
	for _, c := range []struct {
		v    dbus.Variant
		dark bool
	}{
		{dbus.MakeVariant(uint32(1)), true},
		{dbus.MakeVariant(uint32(0)), true},
		{dbus.MakeVariant(uint32(2)), false},
		{dbus.MakeVariant(dbus.MakeVariant(uint32(2))), false}, // Read wraps it twice
		{dbus.MakeVariant("light"), true},
	} {
		if got := schemeIsDark(c.v); got != c.dark {
			t.Errorf("%v: dark %v, want %v", c.v, got, c.dark)
		}
	}
}

func TestTrayRollbackClick(t *testing.T) {
	f := &trayFake{st: Status{State: StateUpToDate, Installed: "v0.1.0"}}
	h := startTray(t, dialFake(f), time.Hour, time.Hour)
	h.ui.waitView(t, "rollback", func(v trayView) bool { _, ok := find(v.Menu, "rollback"); return ok })
	h.app.clicks <- "rollback"
	deadline := time.Now().Add(5 * time.Second)
	for f.count("RebootPrevious") == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if f.count("RebootPrevious") != 1 || f.count("Reboot") != 0 || len(h.launched()) != 0 {
		t.Fatalf("RebootPrevious %d, Reboot %d, launched %q", f.count("RebootPrevious"), f.count("Reboot"), h.launched())
	}

	f.mu.Lock()
	f.previousErr = &daemonError{Name: "RebootPending", Msg: "raw"}
	f.mu.Unlock()
	h.app.clicks <- "rollback"
	for len(h.notified()) == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if n := h.notified(); len(n) != 1 || !strings.Contains(n[0], "restart into it first") {
		t.Fatalf("notes %q", n)
	}
}

func TestTrayRollbackWithoutTheService(t *testing.T) {
	h := startTray(t, func() (daemonClient, error) { return nil, errors.New("no bus") }, time.Hour, time.Hour)
	h.ui.waitView(t, "unavailable", func(v trayView) bool { return v.Tooltip == unavailableText })
	h.app.clicks <- "rollback"
	deadline := time.Now().Add(5 * time.Second)
	for len(h.notified()) == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if n := h.notified(); len(n) != 1 || !strings.Contains(n[0], "not available") {
		t.Fatalf("notes %q", n)
	}
}
