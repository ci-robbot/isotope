package main

import (
	"fmt"
	"io"
	"os"
	"syscall"
)

// welcomeBinary is Isotope Welcome (cmd/isotope-welcome/), the graphical first-login
// flow. The desktop images have it; the agent and the server do not.
const welcomeBinary = "/usr/bin/isotope-welcome"

// execWelcome replaces this process; a test replaces it.
var execWelcome = syscall.Exec

// cmdWelcome is `isotope welcome [PAGE]`: it runs isotope-welcome with the
// same arguments, so its pages and exit codes are the command's. With no
// page it asks for "all", the whole flow: a bare isotope-welcome is the
// login hook, which opens nothing once the user went through it.
func cmdWelcome(args []string, stderr io.Writer) int {
	if len(args) == 0 {
		args = []string{"all"}
	}
	argv := append([]string{"isotope-welcome"}, args...)
	if err := execWelcome(welcomeBinary, argv, os.Environ()); err != nil {
		fmt.Fprintf(stderr, "isotope: cannot run %s: %v\n", welcomeBinary, err)
		return exitError
	}
	return exitOK
}
