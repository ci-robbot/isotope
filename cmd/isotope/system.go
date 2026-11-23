package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"syscall"
)

// systemBinary is Isotope System (cmd/isotope-system/), the graphical app for
// the jobs of this command: updates, desktop, extensions, backup, the
// recovery key, network. The desktop images have it; the agent and the
// server do not.
var systemBinary = "/usr/bin/isotope-system"

// execSystem replaces this process; a test replaces it.
var execSystem = syscall.Exec

// cmdSystem is `isotope system [PAGE]`: it runs isotope-system with
// the same arguments, so its pages and exit codes are the command's.
func cmdSystem(args []string, stderr io.Writer) int {
	argv := append([]string{"isotope-system"}, args...)
	if err := execSystem(systemBinary, argv, os.Environ()); err != nil {
		fmt.Fprintf(stderr, "isotope: cannot run %s: %v\n", systemBinary, err)
		return exitError
	}
	return exitOK
}

// systemAppInstalled says whether the tray can open Isotope System instead
// of a terminal. A variable so the tests can choose.
var systemAppInstalled = func() bool {
	_, err := os.Stat(systemBinary)
	return err == nil
}

// openSystem starts Isotope System on page and does not wait for it.
func openSystem(page string) error {
	cmd := exec.Command(systemBinary, page)
	if err := cmd.Start(); err != nil {
		return err
	}
	go cmd.Wait()
	return nil
}
