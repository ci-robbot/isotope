package main

import (
	"fmt"
	"io"

	"github.com/godbus/dbus/v5"

	"github.com/kairos-community/isotope/pkg/firewall"
)

const firewallUsage = `Usage: isotope firewall [command]

The firewall blocks incoming connections that this computer did not ask
for. Answers to its own connections, the local network's address and name
discovery (DHCP, mDNS) and ping always come in. SSH (22/tcp) is allowed
by default; "isotope firewall deny 22/tcp" removes it.

Commands:
  status                   on or off, and the allowed ports (default)
  on                       turn the firewall on
  off                      turn the firewall off: every port is open
  allow PORT/PROTO [NOTE]  let connections in on a port; PROTO is tcp or
                           udp, NOTE says what the port is for
  deny PORT/PROTO          remove a port from the allowed list
  reset                    repair settings that cannot be read: the firewall
                           is turned on with the ports that can still be read
                           from them, or with the defaults (SSH) when none can

Changes apply at once and are kept across restarts. Only members of the
admin group can change the firewall. The firewall does not run on the live
system or during an install, and cannot be changed there.
`

// firewallClient is the CLI's view of the firewall half of the service.
type firewallClient interface {
	FirewallStatus() (firewall.Status, error)
	SetFirewall(on bool) error
	AllowPort(port uint16, proto, note string) error
	DenyPort(port uint16, proto string) error
	ResetFirewall() error
	Close()
}

var dialFirewall = func() (firewallClient, error) { return dial() }

func (c *client) FirewallStatus() (firewall.Status, error) {
	var m map[string]dbus.Variant
	var rules []FirewallRule
	if err := c.call("FirewallStatus", []any{&m, &rules}); err != nil {
		return firewall.Status{}, err
	}
	st := firewallStatusFromDict(m)
	st.Rules = rules
	return st, nil
}

func (c *client) SetFirewall(on bool) error { return c.call("SetFirewall", nil, on) }

func (c *client) AllowPort(port uint16, proto, note string) error {
	return c.call("AllowPort", nil, port, proto, note)
}

func (c *client) ResetFirewall() error { return c.call("ResetFirewall", nil) }

func (c *client) DenyPort(port uint16, proto string) error {
	return c.call("DenyPort", nil, port, proto)
}

func describeFirewallError(err error) string {
	err = wrapBusErr(err)
	if daemonErrName(err) == "NotAuthorized" {
		return "Only members of the admin group can change the firewall."
	}
	return describeDaemonError(err)
}

// cmdFirewall is `isotope firewall`.
func cmdFirewall(args []string, stdout, stderr io.Writer) int {
	sub := "status"
	if len(args) > 0 {
		sub, args = args[0], args[1:]
	}
	usageErr := func(msg string) int {
		fmt.Fprintf(stderr, "isotope firewall: %s\n\n%s", msg, firewallUsage)
		return exitUsage
	}
	var port int
	var proto, note string
	switch sub {
	case "help", "-h", "--help":
		fmt.Fprint(stdout, firewallUsage)
		return exitOK
	case "status", "on", "off", "reset":
		if len(args) != 0 {
			return usageErr(fmt.Sprintf("%s takes no arguments", sub))
		}
	case "allow", "deny":
		max := 2
		if sub == "deny" {
			max = 1
		}
		if len(args) < 1 || len(args) > max {
			return usageErr(fmt.Sprintf("wrong number of arguments for %s", sub))
		}
		var err error
		if port, proto, err = firewall.ParsePortProto(args[0]); err != nil {
			return usageErr(err.Error())
		}
		if len(args) == 2 {
			note = args[1]
			if err := checkFirewallRule(port, proto, note); err != nil {
				return usageErr(err.Error())
			}
		}
	default:
		return usageErr(fmt.Sprintf("unknown command %q", sub))
	}
	c, err := dialFirewall()
	if err != nil {
		fmt.Fprintln(stderr, "isotope firewall: The update service is not available (isotope-updated).")
		return exitError
	}
	defer c.Close()
	switch sub {
	case "status":
		return firewallStatus(c, stdout, stderr)
	case "on", "off":
		err = c.SetFirewall(sub == "on")
	case "allow":
		err = c.AllowPort(uint16(port), proto, note)
	case "deny":
		err = c.DenyPort(uint16(port), proto)
	case "reset":
		err = c.ResetFirewall()
	}
	if err != nil {
		fmt.Fprintf(stderr, "isotope firewall: %s\n", describeFirewallError(err))
		return exitError
	}
	switch sub {
	case "on":
		fmt.Fprintln(stdout, "The firewall is on.")
	case "off":
		fmt.Fprintln(stdout, "The firewall is off. Every port is open.")
	case "allow":
		fmt.Fprintf(stdout, "Connections on %d/%s are allowed.\n", port, proto)
	case "deny":
		fmt.Fprintf(stdout, "%d/%s is no longer allowed.\n", port, proto)
	case "reset":
		fmt.Fprintln(stdout, "The firewall settings are repaired and the firewall is on. Run `isotope firewall` to see the allowed ports.")
	}
	return exitOK
}

func firewallStatus(c firewallClient, stdout, stderr io.Writer) int {
	st, err := c.FirewallStatus()
	if err != nil {
		fmt.Fprintf(stderr, "isotope firewall: %s\n", describeFirewallError(err))
		return exitError
	}
	if st.Broken && !st.Skipped {
		fmt.Fprintf(stdout, "%s Run `isotope firewall reset` to repair them; the ports listed below are kept.\n", st.Reason)
	}
	switch {
	case st.Skipped:
		fmt.Fprintf(stdout, "Firewall: not running on this boot. %s\n", st.Reason)
	case st.On && st.Active:
		fmt.Fprintln(stdout, "Firewall: on (incoming connections are blocked unless allowed)")
	case st.On:
		fmt.Fprintf(stdout, "Firewall: on, but not running: nothing is blocked. %s\n", reasonUnlessBroken(st))
	case st.Active:
		fmt.Fprintf(stdout, "Firewall: off, but its rules are still loaded. %s\n", reasonUnlessBroken(st))
	default:
		fmt.Fprintln(stdout, "Firewall: off (every port is open)")
	}
	if len(st.Rules) == 0 {
		fmt.Fprintln(stdout, "No port is allowed. Run `isotope firewall allow PORT/PROTO` to add one.")
		return exitOK
	}
	fmt.Fprintln(stdout, "Allowed:")
	for _, r := range st.Rules {
		if r.Note != "" {
			fmt.Fprintf(stdout, "  %-11s %s\n", r.String(), r.Note)
		} else {
			fmt.Fprintf(stdout, "  %s\n", r.String())
		}
	}
	return exitOK
}

// reasonUnlessBroken is st.Reason, unless it is the broken-settings
// sentence, which status prints on its own line.
func reasonUnlessBroken(st firewall.Status) string {
	if st.Broken {
		return ""
	}
	return st.Reason
}
