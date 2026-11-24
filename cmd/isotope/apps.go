package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/godbus/dbus/v5"

	"github.com/kairos-community/isotope/pkg/flatpak"
)

// `isotope apps`: the user's Flatpak apps from Flathub, as Isotope System's
// Apps page shows them. The daily isotope-app-updates.timer (a user unit)
// runs `isotope apps update --auto`, which updates every pending app and
// runtime except an update that asks for more permissions, and records the
// run in $XDG_STATE_HOME/isotope/app-updates. On by default;
// $XDG_CONFIG_HOME/isotope/apps says auto=off when the user turned it off.
// See docs/superpowers/specs/2026-11-24-apps-page-design.md.

const appsUsage = `Usage: isotope apps [command]

Your apps from Flathub (flatpak --user). Isotope System's Apps page
installs, updates and removes them.

Commands:
  status            automatic updates and their last run (default)
  update            update every app and runtime now
  update --auto     the daily automatic run: skips the live system, a
                    metered connection and a battery below 30%, and holds
                    back an update that asks for more permissions
  auto [status|on|off]
                    automatic app updates (on unless you turn them off)
`

// The seams of the command, variables for the tests.
var (
	// appsLiveMarker is the live desktop's marker: an app installed there
	// is gone at the restart, so nothing runs.
	appsLiveMarker = "/run/isotope/live-desktop"
	// appsClient is the user's installation.
	appsClient = flatpak.New
	// appsEnv is NetworkManager and UPower on the system bus, as automatic
	// system updates ask them (envSkip).
	appsEnv = func() (autoEnvironment, func(), error) {
		conn, err := dbus.ConnectSystemBus()
		if err != nil {
			return nil, nil, err
		}
		return busAutoEnv{conn: conn}, func() { conn.Close() }, nil
	}
	appsNow = time.Now
)

func cmdApps(args []string, stdout, stderr io.Writer) int {
	sub := "status"
	if len(args) > 0 {
		sub, args = args[0], args[1:]
	}
	switch sub {
	case "help", "-h", "--help":
		fmt.Fprint(stdout, appsUsage)
		return exitOK
	case "status", "update", "auto":
	default:
		fmt.Fprintf(stderr, "isotope apps: unknown command %q\n\n%s", sub, appsUsage)
		return exitUsage
	}
	c := appsClient()
	if !c.Available() {
		fmt.Fprintln(stderr, "isotope: Flatpak is not on this system.")
		return exitError
	}
	switch sub {
	case "status":
		if len(args) != 0 {
			fmt.Fprintf(stderr, "isotope apps: status takes no arguments\n\n%s", appsUsage)
			return exitUsage
		}
		for _, l := range appsStatusLines(appsNow()) {
			fmt.Fprintln(stdout, l)
		}
		return exitOK
	case "auto":
		return cmdAppsAuto(args, stdout, stderr)
	}
	switch {
	case len(args) == 0:
		return appsUpdateAll(c, stdout, stderr)
	case len(args) == 1 && (args[0] == "--auto" || args[0] == "-auto"):
		return appsAutoRun(c, stdout, stderr)
	}
	fmt.Fprintf(stderr, "isotope apps: update takes only --auto\n\n%s", appsUsage)
	return exitUsage
}

func cmdAppsAuto(args []string, stdout, stderr io.Writer) int {
	sub := "status"
	if len(args) > 0 {
		sub, args = args[0], args[1:]
	}
	if len(args) != 0 {
		fmt.Fprintf(stderr, "isotope apps auto: %s takes no arguments\n\n%s", sub, appsUsage)
		return exitUsage
	}
	switch sub {
	case "status":
		fmt.Fprintln(stdout, appsStatusLines(appsNow())[0])
		return exitOK
	case "on", "off":
		if err := flatpak.SetAuto(sub == "on"); err != nil {
			fmt.Fprintln(stderr, "isotope: could not save the setting:", err)
			return exitError
		}
		if sub == "on" {
			fmt.Fprintln(stdout, "Automatic app updates are on. Once a day your apps update in the background; an update that asks for more permissions waits for you.")
		} else {
			fmt.Fprintln(stdout, "Automatic app updates are off. You are told when updates are ready.")
		}
		return exitOK
	}
	fmt.Fprintf(stderr, "isotope apps auto: unknown command %q\n\n%s", sub, appsUsage)
	return exitUsage
}

// appsStatusLines are the setting and the last automatic run.
func appsStatusLines(now time.Time) []string {
	out := []string{"Automatic app updates: off"}
	if flatpak.AutoEnabled() {
		out[0] = "Automatic app updates: on (once a day; an update that asks for more permissions waits for you)"
	}
	r, ok := flatpak.ReadLastRun()
	if !ok {
		return append(out, "Last run: never")
	}
	return append(out, "Last run "+time.Unix(r.Time, 0).In(now.Location()).Format("2 January 2006, 15:04")+": "+r.Text())
}

// appsUpdateAll is `isotope apps update`: everything, the user asked.
func appsUpdateAll(c *flatpak.Client, stdout, stderr io.Writer) int {
	p, err := c.Update()
	if err != nil {
		fmt.Fprintln(stderr, "isotope:", err)
		return exitError
	}
	for l := range p.Lines() {
		fmt.Fprintln(stdout, l)
	}
	if err := p.Wait(); err != nil {
		fmt.Fprintln(stderr, "isotope:", err)
		return exitError
	}
	if err := c.RemoveUnused(context.Background()); err != nil {
		fmt.Fprintln(stderr, "isotope: removing unused runtimes:", err)
	}
	return exitOK
}

// appsSkip is why the automatic run does nothing now, "" to go ahead: the
// live desktop, the setting off, Flathub not set up, a metered connection,
// a low battery.
func appsSkip(c *flatpak.Client) string {
	if _, err := os.Stat(appsLiveMarker); err == nil {
		return "this is the live system"
	}
	if !flatpak.AutoEnabled() {
		return "automatic app updates are off"
	}
	env, done, err := appsEnv()
	if err != nil {
		return "could not reach the system bus (" + err.Error() + ")"
	}
	defer done()
	if why := envSkip(env); why != "" {
		return why
	}
	if !c.FlathubReady(context.Background()) {
		return "Flathub is not set up for this user"
	}
	return ""
}

// appsAutoRun is `isotope apps update --auto`: one automatic run, recorded
// for the Apps page.
func appsAutoRun(c *flatpak.Client, stdout, stderr io.Writer) int {
	r := flatpak.LastRun{Time: appsNow().Unix()}
	record := func() {
		if err := flatpak.WriteLastRun(r); err != nil {
			fmt.Fprintln(stderr, "isotope: could not record the run:", err)
		}
		fmt.Fprintln(stdout, "Automatic app updates: "+r.Text())
	}
	if why := appsSkip(c); why != "" {
		r.Result, r.Detail = flatpak.RunSkipped, why
		record()
		return exitOK
	}
	ctx := context.Background()
	// A failed refresh leaves the old catalog: versions and release notes
	// may lag, the updates do not.
	if err := c.RefreshAppstream(ctx); err != nil {
		fmt.Fprintln(stderr, "isotope: refreshing Flathub's catalog:", err)
	}
	ups, err := c.Updates(ctx)
	if err != nil {
		r.Result, r.Detail = flatpak.RunFailed, "could not list the updates: "+err.Error()
		record()
		return exitError
	}
	ready, held := flatpak.Split(c.Review(ctx, ups))
	for _, h := range held {
		r.Held = append(r.Held, flatpak.Held{Ref: h.Ref.String(), Name: orID(h.Name, h.Ref.ID), Why: flatpak.HeldWhy(h)})
	}
	if len(ready) == 0 {
		r.Result = flatpak.RunUpToDate
		record()
		return exitOK
	}
	refs := make([]string, len(ready))
	for i, p := range ready {
		refs[i] = p.Ref.String()
	}
	p, err := c.Update(refs...)
	if err == nil {
		for range p.Lines() {
		}
		err = p.Wait()
	}
	if err != nil {
		r.Result, r.Detail = flatpak.RunFailed, err.Error()
		record()
		return exitError
	}
	if err := c.RemoveUnused(ctx); err != nil {
		fmt.Fprintln(stderr, "isotope: removing unused runtimes:", err)
	}
	r.Result = flatpak.RunUpdated
	for _, p := range ready {
		r.Updated = append(r.Updated, orID(p.Name, p.Ref.ID))
	}
	record()
	return exitOK
}

func orID(name, id string) string {
	if name != "" {
		return name
	}
	return id
}
