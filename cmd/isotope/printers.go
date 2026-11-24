package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/godbus/dbus/v5"

	"github.com/kairos-community/isotope/pkg/printing"
)

// Printers: the CUPS queues an administrator adds, removes and makes the
// default in Isotope System. isotope-updated runs CUPS's own lpadmin as
// root, behind polkit's io.kairos.isotope-update.printers: lpadmin's
// `-m everywhere` asks the printer what it can do and makes the queue's PPD,
// which a pure-Go IPP client cannot. Reading the queues needs no daemon:
// every user may ask CUPS for them, and Isotope System does so itself.
//
// The queues live in /var/lib/cups (cups-files.conf's ServerRoot), which
// 94_isotope_state.yaml keeps on the persistent partition, so nothing here
// writes /oem: a queue added in a terminal or by cups-browsed survives the
// reboot the same way.

var (
	// printersRun runs lpadmin until ctx ends. A variable for the tests.
	printersRun fwRunner = execCtx
	// printersTimeout bounds one lpadmin call. `-m everywhere` waits for
	// the printer to answer, which a sleeping printer can take a while to
	// do; CUPS itself gives up on it after about a minute.
	printersTimeout = 90 * time.Second
	printersMu      sync.Mutex
)

// errPrinterRefused wraps the checks of pkg/printing, so the bus calls the
// refusal Refused (the caller asked for something that cannot be done)
// rather than Failed.
type errPrinterRefused struct{ err error }

func (e errPrinterRefused) Error() string { return e.err.Error() }

func (e errPrinterRefused) Is(target error) bool { return target == errPrinterInput }

var errPrinterInput = errors.New("bad printer input")

// lpadmin runs lpadmin with args and turns a failure into lpadmin's own
// sentence, which says what CUPS refused ("The printer or class does not
// exist.", "Unable to connect to printer").
func lpadmin(args ...string) error {
	printersMu.Lock()
	defer printersMu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), printersTimeout)
	defer cancel()
	out, err := printersRun(ctx, "lpadmin", args...)
	if ctx.Err() == context.DeadlineExceeded {
		return fmt.Errorf("the printer did not answer in %v", printersTimeout)
	}
	if err != nil {
		var ee *exec.Error
		if errors.As(err, &ee) {
			return fmt.Errorf("lpadmin cannot run (%v): is CUPS installed?", ee.Err)
		}
		msg := strings.TrimSpace(string(out))
		msg = strings.TrimPrefix(msg, "lpadmin: ")
		if msg == "" {
			msg = err.Error()
		}
		return errors.New(msg)
	}
	return nil
}

// addPrinter makes a queue for the driverless printer at uri, enabled,
// accepting jobs and not shared with the network.
func addPrinter(name, uri, info, location string) error {
	for _, err := range []error{printing.CheckName(name), printing.CheckURI(uri),
		printing.CheckText(info), printing.CheckText(location)} {
		if err != nil {
			return errPrinterRefused{err}
		}
	}
	args := []string{"-p", name, "-E", "-v", uri, "-m", "everywhere", "-o", "printer-is-shared=false"}
	if info != "" {
		args = append(args, "-D", info)
	}
	if location != "" {
		args = append(args, "-L", location)
	}
	return lpadmin(args...)
}

func removePrinter(name string) error {
	if err := printing.CheckName(name); err != nil {
		return errPrinterRefused{err}
	}
	return lpadmin("-x", name)
}

func setDefaultPrinter(name string) error {
	if err := printing.CheckName(name); err != nil {
		return errPrinterRefused{err}
	}
	return lpadmin("-d", name)
}

// printersCall is the frame of the printer methods: the printers action
// (administrators, never a Flatpak app), not during a restore.
func (a busAPI) printersCall(sender dbus.Sender, method string, f func() error) *dbus.Error {
	done, derr := a.s.enter()
	if derr != nil {
		return derr
	}
	defer done()
	if err := a.s.authorize(sender, actionPrinters); err != nil {
		return err
	}
	if derr := a.s.notRestoring(); derr != nil {
		return derr
	}
	if err := f(); err != nil {
		fmt.Fprintf(os.Stderr, "isotope: %s: %v\n", method, err)
		return busError(err)
	}
	return nil
}

// AddPrinter adds a driverless printer as the queue name.
func (a busAPI) AddPrinter(sender dbus.Sender, name, uri, info, location string) *dbus.Error {
	return a.printersCall(sender, "AddPrinter", func() error { return addPrinter(name, uri, info, location) })
}

// RemovePrinter deletes the queue name and its jobs.
func (a busAPI) RemovePrinter(sender dbus.Sender, name string) *dbus.Error {
	return a.printersCall(sender, "RemovePrinter", func() error { return removePrinter(name) })
}

// SetDefaultPrinter makes name the system's default printer.
func (a busAPI) SetDefaultPrinter(sender dbus.Sender, name string) *dbus.Error {
	return a.printersCall(sender, "SetDefaultPrinter", func() error { return setDefaultPrinter(name) })
}
