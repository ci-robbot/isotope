package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/kairos-community/isotope/pkg/i18n"
)

// trayView is everything the tray shows at one moment.
type trayView struct {
	Icon    string // one of iconKinds
	Tooltip string
	Menu    []MenuItem
}

// trayUI shows a view. The systray glue implements it; `tray --no-icon`
// (and so `notify`) uses a UI that shows nothing, and tests record the views.
type trayUI interface{ Show(trayView) }

type noUI struct{}

func (noUI) Show(trayView) {}

// trayRetry is how long the tray waits before dialling an unreachable
// update service again.
const trayRetry = 30 * time.Second

// unavailableText is the tooltip and menu line while the service cannot be
// reached.
var unavailableText = i18n.Mark("Update service not available")

// trayApp is the tray without the tray library: it follows the daemon,
// checks on a schedule, announces new releases and acts on menu clicks. All
// its state lives in the one goroutine that runs run.
type trayApp struct {
	rel  Release
	ui   trayUI
	dial func() (daemonClient, error)
	log  *log.Logger
	// clicks carries menu item IDs from the tray library to run.
	clicks chan string
	// notify and launch are seams: a desktop notification, and a terminal
	// running `isotope <args>`.
	notify func(summary, body string) error
	// notifyAction is a notification with one action button: the
	// "Restart to finish" of an automatic update.
	notifyAction func(summary, body, label string, invoked func()) error
	launch       func(args ...string) error
	// openMenu opens an entry of isotope-menu, the same one Super+Escape
	// shows: "Extensions" is one list on every variant.
	openMenu func(entry string) error
	// system opens a page of Isotope System; nil (the app is not
	// installed) keeps the terminal and the menu above.
	system func(page string) error
	// noIcon makes the notification point at the CLI: there is no menu.
	noIcon bool

	firstCheck, interval, retry time.Duration

	st       Status
	releases []Latest
	line     string    // the last progress line of the running job
	down     bool      // the service was unreachable at the last try
	due      time.Time // when the next Check runs
}

func newTrayApp(rel Release, ui trayUI, dial func() (daemonClient, error), logOut io.Writer) *trayApp {
	a := &trayApp{
		rel: rel, ui: ui, dial: dial,
		log:          log.New(logOut, "isotope: ", 0),
		clicks:       make(chan string, 8),
		notify:       Notify,
		notifyAction: NotifyAction,
		firstCheck:   notifyDelay, interval: notifyInterval, retry: trayRetry,
	}
	a.launch = func(args ...string) error { return openTerminal(args, logOut) }
	a.openMenu = openIsotopeMenu
	if systemAppInstalled() {
		a.system = openSystem
	}
	return a
}

// run follows the service until ctx ends. It never gives up: an unreachable
// service shows the error icon and is dialled again every a.retry.
func (a *trayApp) run(ctx context.Context) {
	for ctx.Err() == nil {
		c, err := a.dial()
		if err == nil {
			err = a.session(ctx, c)
			c.Close()
		}
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			// Log the loss once, not at every retry.
			if !a.down {
				a.log.Printf("the update service is not available: %v", err)
			}
			a.down = true
			a.ui.Show(trayView{Icon: "error", Tooltip: i18n.T(unavailableText), Menu: []MenuItem{{ID: "status", Label: i18n.T(unavailableText)}}})
		}
		a.idle(ctx, a.retry)
	}
}

// idle waits d, still answering clicks: a terminal item needs no service.
func (a *trayApp) idle(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			return
		case id := <-a.clicks:
			a.click(nil, id)
		}
	}
}

// session follows one connection. It returns nil when ctx ends and an error
// when the service cannot be reached, so run shows the error and dials again.
func (a *trayApp) session(ctx context.Context, c daemonClient) error {
	st, err := c.Status()
	if err != nil {
		return err
	}
	events, stop, err := c.Watch()
	if err != nil {
		return err
	}
	a.down = false
	a.setStatus(st)
	defer func() { stop() }()

	// An unmanaged system has nothing to check against, so no timer runs.
	var timer <-chan time.Time
	var t *time.Timer
	if st.State != StateUnmanaged {
		if a.due.IsZero() {
			a.due = time.Now().Add(a.firstCheck)
		}
		t = time.NewTimer(time.Until(a.due))
		defer t.Stop()
		timer = t.C
	}

	for {
		select {
		case <-ctx.Done():
			return nil
		case id := <-a.clicks:
			a.click(c, id)
		case <-timer:
			if busyState(a.st.State) {
				// The service is busy with a job: Check would only answer
				// Busy, after retryBusy held this goroutine for seconds.
				t.Reset(a.retry)
				continue
			}
			if err := a.check(c); err != nil && daemonErrName(err) == "" {
				// The bus or the activation failed, not the check: keep
				// the schedule, and try again once the service is back.
				return err
			}
			a.due = time.Now().Add(a.interval)
			t.Reset(a.interval)
		case ev, ok := <-events:
			if !ok {
				a.serviceGone()
				stop()
				if events, stop, err = c.Watch(); err != nil {
					return err
				}
				continue
			}
			if ev.Status != nil {
				a.setStatus(*ev.Status)
			} else {
				a.line = ev.Line
				a.render()
			}
		}
	}
}

// serviceGone handles the service's name losing its owner. After an idle
// exit nothing changes. During a job it means the service died with it: the
// job is lost, and the menu must not keep promising it.
func (a *trayApp) serviceGone() {
	if busyState(a.st.State) {
		st := a.st
		st.State, st.Detail = StateError, "the update service stopped during the update"
		a.setStatus(st)
	}
}

// check runs one scheduled check, refreshes the menu and announces a new
// release once.
func (a *trayApp) check(c daemonClient) error {
	if err := retryBusy(c.Check); err != nil {
		a.log.Printf("checking for updates: %v", err)
		return err
	}
	st, err := c.Status()
	if err != nil {
		return err
	}
	var tags []string
	if err := retryBusy(func() (err error) { tags, err = c.Releases(); return }); err == nil {
		a.releases = a.releases[:0]
		for _, t := range tags {
			a.releases = append(a.releases, Latest{Tag: t})
		}
	} else {
		a.log.Printf("listing releases: %v", err)
	}
	a.setStatus(st)
	if err := announce(c, a.rel, st, a.noIcon, a.notify); err != nil {
		a.log.Print(err)
	}
	return nil
}

func (a *trayApp) setStatus(st Status) {
	if st.State != StateInspecting && st.State != StateApplying {
		a.line = ""
	}
	a.st = st
	a.render()
	a.announceStaged(st)
}

// announceStaged says once per version that an automatic update is
// installed and waits for a restart, with a Restart button that does what
// the menu's "Restart now" does. An install the user started gets no
// notification: they asked for it and were told. A failed automatic run
// notifies nothing; the Updates page shows it, so a daily retry is not a
// daily notification.
func (a *trayApp) announceStaged(st Status) {
	if st.State != StateRebootPending || st.Pending != pendingInstalled || st.Staged == "" || !st.StagedAuto {
		return
	}
	if stagedNotified(st.Staged) {
		return
	}
	body := i18n.Tf("Isotope %s was installed in the background. It starts at the next restart.", st.Staged)
	if a.noIcon {
		body = i18n.Tf("Isotope %s was installed in the background. It starts at the next restart. Run `isotope reboot` to restart now.", st.Staged)
	}
	err := a.notifyAction(i18n.T("Restart to finish the update"), body, i18n.T("Restart"), func() {
		select {
		case a.clicks <- "restart":
		default:
		}
	})
	if err != nil {
		a.log.Printf("showing a notification: %v", err)
		return
	}
	if err := markStagedNotified(st.Staged); err != nil {
		a.log.Printf("recording that %s was announced: %v", st.Staged, err)
	}
}

func (a *trayApp) render() {
	tip := statusLine(a.st)
	if a.line != "" {
		tip = a.line
	}
	menu := BuildMenu(a.st, a.releases, a.rel.Flavor, a.rel.Line())
	if a.system != nil {
		// Every job of the menu, and the ones it has no room for.
		menu = append(menu, MenuItem{ID: "system", Label: "Isotope System…", Enabled: true})
	}
	a.ui.Show(trayView{Icon: iconFor(a.st.State), Tooltip: tip, Menu: menu})
}

// click acts on a menu item. Anything that needs details or an answer opens
// a page of Isotope System or, on a system without it, a terminal running
// the CLI, which waits for Enter before closing over an error
// (--pause-on-error). Cancel, Restart and Restart into the previous system
// are the click itself, and polkit decides. c is nil while the service is
// unreachable.
func (a *trayApp) click(c daemonClient, id string) {
	var err error
	page, toApp := systemPage(id)
	switch {
	case toApp && a.system != nil:
		if err = a.system(page); err != nil {
			err = fmt.Errorf(i18n.T("could not open Isotope System: %w"), err)
		}
	case id == "system":
		// Only offered when the app is installed (BuildMenu's caller).
		return
	case id == "install-latest":
		err = a.launch("upgrade", "--pause-on-error")
	case strings.HasPrefix(id, "releases/"):
		err = a.launch("upgrade", "--pause-on-error", "--version", strings.TrimPrefix(id, "releases/"))
	case strings.HasPrefix(id, "switch/"):
		err = a.launch("switch", strings.TrimPrefix(id, "switch/"), "--pause-on-error")
	case strings.HasPrefix(id, "gpu/"):
		err = a.launch("switch", "--gpu", strings.TrimPrefix(id, "gpu/"), "--pause-on-error")
	case id == "advanced":
		err = a.launch("upgrade", "--pause-on-error", "--image", "-")
	case id == "extensions":
		err = a.openMenu("Extensions")
	case id == "recovery":
		err = a.openMenu("Recovery key")
	case c == nil && (id == "cancel" || id == "restart" || id == "rollback"):
		err = errors.New(i18n.T("the update service is not available"))
	case id == "cancel":
		err = c.Cancel()
	case id == "restart":
		err = c.Reboot()
	case id == "rollback":
		err = c.RebootPrevious()
	default:
		return
	}
	if err != nil {
		a.log.Printf("%s: %v", id, err)
		msg := describeDaemonError(err)
		if id == "rollback" {
			msg = describeRollbackError(err, a.st.Pending)
		}
		if nerr := a.notify(i18n.T("Isotope update"), msg); nerr != nil {
			a.log.Printf("showing a notification: %v", nerr)
		}
	}
}

// announce shows one notification per new release, only while the daemon
// says an update is available (a pinned system says up-to-date), and only
// once the release's image can be installed: a release published before its
// image, or one whose package is not public yet, is announced on a later
// check instead.
func announce(c daemonClient, rel Release, st Status, noIcon bool, notify func(summary, body string) error) error {
	if st.State != StateUpdateAvailable || st.Latest == "" || alreadyNotified(st.Latest) {
		return nil
	}
	ref, err := ReleaseRef(rel, rel.Flavor, st.Latest)
	if err != nil {
		return err
	}
	var info InspectInfo
	err = retryBusy(func() (err error) { info, err = c.Inspect(ref.String(), false); return })
	if err == nil && info.Refusal != "" {
		err = errors.New(info.Refusal)
	}
	if err != nil {
		return fmt.Errorf("%s is out but not installable yet: %w", st.Latest, err)
	}
	installed := st.Installed
	if installed == "" {
		installed = i18n.T("unknown")
	}
	body := i18n.Tf("You are on %s. Install it from the update icon's menu in the panel.", installed)
	if noIcon {
		body = i18n.Tf("You are on %s. Run `isotope upgrade` in a terminal to install it.", installed)
	}
	if err := notify(i18n.Tf("Isotope %s is available", st.Latest), body); err != nil {
		return err
	}
	if err := markNotified(st.Latest); err != nil {
		// The user was told; only the record failed, so the next session
		// announces it again.
		return fmt.Errorf("recording that %s was announced: %w", st.Latest, err)
	}
	return nil
}

// busyState reports whether the service is running a job.
func busyState(s State) bool {
	return s == StateChecking || s == StateInspecting || s == StateApplying || s == StateExtension || s == StateBackup || s == StateRestore
}

// defaultSessionFile names the variant's session settings, among them
// TERMINAL_EXEC, the terminal the tray opens the CLI in.
const defaultSessionFile = "/etc/isotope/session"

func sessionFile() string {
	if p := os.Getenv("ISOTOPE_UPDATE_SESSION_FILE"); p != "" {
		return p
	}
	return defaultSessionFile
}

// terminalArgv is the command that runs `self args...` in a terminal window.
// TERMINAL_EXEC is split on spaces, never through a shell. Without it, foot
// and then xterm are tried, and the fallback is logged.
func terminalArgv(self string, args []string, logOut io.Writer) ([]string, error) {
	kv, _ := readKV(sessionFile())
	if te := strings.Fields(kv["TERMINAL_EXEC"]); len(te) > 0 {
		return append(append(te, self), args...), nil
	}
	for _, t := range []struct {
		bin string
		pre []string
	}{{"foot", nil}, {"xterm", []string{"-e"}}} {
		if p, err := exec.LookPath(t.bin); err == nil {
			fmt.Fprintf(logOut, "isotope: %s sets no TERMINAL_EXEC; using %s\n", sessionFile(), p)
			return append(append(append([]string{p}, t.pre...), self), args...), nil
		}
	}
	return nil, fmt.Errorf(i18n.T("no terminal to open: %s sets no TERMINAL_EXEC, and neither foot nor xterm is installed"), sessionFile())
}

// openIsotopeMenu runs `isotope-menu ENTRY` and does not wait for it.
func openIsotopeMenu(entry string) error {
	cmd := exec.Command("isotope-menu", entry)
	if err := cmd.Start(); err != nil {
		return err
	}
	go cmd.Wait()
	return nil
}

// openTerminal starts the CLI in a terminal and does not wait for it.
func openTerminal(args []string, logOut io.Writer) error {
	self, err := os.Executable()
	if err != nil {
		self = "isotope"
	}
	argv, err := terminalArgv(self, args, logOut)
	if err != nil {
		return err
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	if err := cmd.Start(); err != nil {
		return err
	}
	go cmd.Wait() // reap it; the terminal's exit status is not ours to report
	return nil
}

// systemPage is the page of Isotope System a tray item opens when the
// app is installed; ok is false for the items that are a click on the
// service (cancel, restart, rollback).
func systemPage(id string) (page string, ok bool) {
	switch {
	case id == "system", id == "install-latest", id == "advanced", strings.HasPrefix(id, "releases/"):
		return "updates", true
	case strings.HasPrefix(id, "switch/"), strings.HasPrefix(id, "gpu/"):
		return "desktop", true
	case id == "extensions":
		return "extensions", true
	case id == "recovery":
		return "security", true
	}
	return "", false
}
