package main

import (
	"fmt"
	"io"

	"github.com/godbus/dbus/v5"
)

// `isotope unmetered` is fwupd-refresh.service's ExecCondition: the daily
// LVFS metadata refresh skips a metered connection, as automatic updates
// do, with the same NetworkManager check (busAutoEnv.Metered: NM's Metered
// yes or guess-yes; no NetworkManager is not metered). Exit 0 runs the
// refresh; 1 skips it, also when the answer cannot be read (it could be
// metered), which systemd records as a skipped condition, not a failure.

// meteredCheck is the check, a variable so tests need no bus.
var meteredCheck = func() (bool, error) {
	conn, err := dbus.ConnectSystemBus()
	if err != nil {
		return false, err
	}
	defer conn.Close()
	return busAutoEnv{conn: conn}.Metered()
}

func cmdUnmetered(stderr io.Writer) int {
	metered, err := meteredCheck()
	switch {
	case err != nil:
		fmt.Fprintln(stderr, "isotope unmetered: cannot tell whether the connection is metered:", err)
		return 1
	case metered:
		fmt.Fprintln(stderr, "isotope unmetered: the connection is metered")
		return 1
	}
	return exitOK
}
