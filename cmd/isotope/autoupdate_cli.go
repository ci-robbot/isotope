package main

import (
	"fmt"
	"io"
	"time"

	"github.com/godbus/dbus/v5"
)

const autoUsage = `Usage: isotope update auto [command]

Automatic updates install a new release of this system's own desktop and
image line in the background, once a day, into the other system slot. The
new release starts at the next restart; nothing restarts by itself. They
skip a metered connection, a laptop on battery below 30%, and a machine
with an update already waiting for a restart. Off by default.

Commands:
  status   on or off, the last run and a waiting update (default)
  on       turn automatic updates on
  off      turn them off

Only members of the admin group can turn them on or off.
`

// AutoStatus is the daemon's AutoUpdateStatus.
type AutoStatus struct {
	Enabled                                     bool
	Broken                                      string
	LastTime                                    int64
	LastResult, LastVersion, LastDetail, Staged string
	StagedAuto                                  bool
}

// autoClient is the CLI's view of the automatic update half of the service.
type autoClient interface {
	AutoUpdateStatus() (AutoStatus, error)
	SetAutoUpdate(on bool) error
	RunAutoUpdate() (result, version, detail string, err error)
	Close()
}

var dialAuto = func() (autoClient, error) { return dial() }

func (c *client) AutoUpdateStatus() (AutoStatus, error) {
	var m map[string]dbus.Variant
	if err := c.call("AutoUpdateStatus", []any{&m}); err != nil {
		return AutoStatus{}, err
	}
	return autoStatusFromDict(m), nil
}

func autoStatusFromDict(m map[string]dbus.Variant) AutoStatus {
	t, _ := m["last_time"].Value().(int64)
	return AutoStatus{Enabled: boolean(m, "enabled"), Broken: str(m, "broken"), LastTime: t,
		LastResult: str(m, "last_result"), LastVersion: str(m, "last_version"), LastDetail: str(m, "last_detail"),
		Staged: str(m, "staged"), StagedAuto: boolean(m, "staged_auto")}
}

func (c *client) SetAutoUpdate(on bool) error { return c.call("SetAutoUpdate", nil, on) }

func (c *client) RunAutoUpdate() (result, version, detail string, err error) {
	err = c.call("RunAutoUpdate", []any{&result, &version, &detail})
	return
}

func describeAutoError(err error) string {
	err = wrapBusErr(err)
	if daemonErrName(err) == "NotAuthorized" {
		return "Only members of the admin group can turn automatic updates on or off."
	}
	return describeDaemonError(err)
}

// cmdAuto is `isotope update auto`.
func cmdAuto(args []string, stdout, stderr io.Writer) int {
	sub := "status"
	if len(args) > 0 {
		sub, args = args[0], args[1:]
	}
	switch sub {
	case "help", "-h", "--help":
		fmt.Fprint(stdout, autoUsage)
		return exitOK
	case "status", "on", "off":
		if len(args) != 0 {
			fmt.Fprintf(stderr, "isotope update auto: %s takes no arguments\n\n%s", sub, autoUsage)
			return exitUsage
		}
	default:
		fmt.Fprintf(stderr, "isotope update auto: unknown command %q\n\n%s", sub, autoUsage)
		return exitUsage
	}
	if rel, err := LoadRelease(releaseFile()); err == nil && rel.Flavor == flavorAgent {
		fmt.Fprintln(stderr, "isotope: automatic updates are not available on the agent flavor.")
		return exitUsage
	}
	c, err := dialAuto()
	if err != nil {
		fmt.Fprintln(stderr, "isotope: The update service is not available (isotope-updated).")
		return exitError
	}
	defer c.Close()
	if sub != "status" {
		if err := c.SetAutoUpdate(sub == "on"); err != nil {
			fmt.Fprintf(stderr, "isotope: %s\n", describeAutoError(err))
			return exitError
		}
		if sub == "on" {
			fmt.Fprintln(stdout, "Automatic updates are on. A new release installs in the background and starts at the next restart.")
		} else {
			fmt.Fprintln(stdout, "Automatic updates are off.")
		}
		return exitOK
	}
	st, err := c.AutoUpdateStatus()
	if err != nil {
		fmt.Fprintf(stderr, "isotope: %s\n", describeAutoError(err))
		return exitError
	}
	for _, l := range autoStatusLines(st, time.Now()) {
		fmt.Fprintln(stdout, l)
	}
	return exitOK
}

// autoStatusLines is `isotope update auto status`.
func autoStatusLines(st AutoStatus, now time.Time) []string {
	var out []string
	switch {
	case st.Broken != "":
		out = append(out, "Automatic updates: off (the setting cannot be read: "+st.Broken+"; turn them on or off again to repair it)")
	case st.Enabled:
		out = append(out, "Automatic updates: on (once a day; a new release starts at the next restart)")
	default:
		out = append(out, "Automatic updates: off")
	}
	if st.LastTime != 0 {
		when := time.Unix(st.LastTime, 0).In(now.Location()).Format("2006-01-02 15:04")
		out = append(out, "Last run:          "+when+", "+autoResultLine(st.LastResult, st.LastVersion, st.LastDetail))
	} else {
		out = append(out, "Last run:          never")
	}
	if st.Staged != "" {
		out = append(out, "Waiting:           Isotope "+st.Staged+" is installed and starts at the next restart (`isotope reboot`).")
	}
	return out
}

// autoResultLine says a last run in one phrase.
func autoResultLine(result, version, detail string) string {
	switch result {
	case autoInstalled:
		return "installed Isotope " + version
	case autoUpToDate:
		return "up to date"
	case autoSkipped:
		return "skipped. " + detail
	case autoFailed:
		return "failed. " + detail
	}
	return result + ". " + detail
}

// cmdAutoRun is `isotope update --auto`, which isotope-autoupdate.service
// runs as root once a day. Off, it does nothing, not even start the
// service.
func cmdAutoRun(stdout, stderr io.Writer) int {
	if needRoot() {
		fmt.Fprintln(stderr, "isotope update --auto: must run as root; isotope-autoupdate.timer runs it. Use `isotope update auto on` to turn automatic updates on.")
		return exitError
	}
	on, err := loadAutoUpdate()
	if err != nil {
		fmt.Fprintln(stderr, "isotope: automatic updates are off:", err)
	}
	if !on {
		fmt.Fprintln(stdout, "Automatic updates are off; nothing to do.")
		return exitOK
	}
	if rel, err := LoadRelease(releaseFile()); err == nil && rel.Flavor == flavorAgent {
		return exitOK
	}
	c, err := dialAuto()
	if err != nil {
		fmt.Fprintln(stderr, "isotope: The update service is not available (isotope-updated).")
		return exitError
	}
	defer c.Close()
	var result, version, detail string
	err = retryBusy(func() (err error) { result, version, detail, err = c.RunAutoUpdate(); return })
	if err != nil {
		return netFail(stderr, "the automatic update did not run: ", err)
	}
	switch result {
	case autoStarted:
		fmt.Fprintf(stdout, "Installing Isotope %s in the background; it starts at the next restart.\n", version)
	case autoFailed:
		fmt.Fprintln(stderr, "isotope: the automatic update failed:", detail)
		return exitError
	default:
		fmt.Fprintln(stdout, detail)
	}
	return exitOK
}
