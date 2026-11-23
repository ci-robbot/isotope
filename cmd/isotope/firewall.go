package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/godbus/dbus/v5"
	"gopkg.in/yaml.v3"

	"github.com/kairos-community/isotope/pkg/firewall"
)

// The firewall: one nftables table, `inet isotope`, loaded at boot by
// isotope-firewall.service (/usr/libexec/isotope-firewall). Incoming
// connections are dropped unless /usr/share/isotope/firewall.nft (the
// fixed rules) or the allowed list accepts them: /etc/isotope/firewall.nft,
// or /usr/share/isotope/firewall-default.nft (SSH, firewall.Defaults) on a
// machine with no saved settings. The daemon keeps the choice in an /oem
// cloud-config, which writes /etc/isotope/firewall and
// /etc/isotope/firewall.nft again at every boot, as SetLocale does: Kairos
// rebuilds /etc from the image.

// The files the firewall methods write and the command that applies them.
// Variables so the tests can point them into a temporary directory.
var (
	firewallOEM   = "/oem/94_isotope_firewall.yaml"
	firewallFlag  = "/etc/isotope/firewall"
	firewallRules = "/etc/isotope/firewall.nft"
	// firewallRun runs `systemctl reload-or-restart` (the unit's reload is
	// one nft transaction) and `nft list table inet isotope` (what is
	// loaded now), each bounded by a timeout: they run under firewallMu,
	// and a stuck job must not block every status call after it.
	firewallRun fwRunner = execCtx
	// firewallReloadTimeout bounds the reload, firewallStatusTimeout the
	// nft read.
	firewallReloadTimeout = 30 * time.Second
	firewallStatusTimeout = 5 * time.Second
	// firewallBootMarkers are immucore's markers of the boots on which
	// isotope-firewall.service never runs (its ConditionPathExists=!).
	firewallBootMarkers = []string{"/run/cos/live_mode", "/run/cos/install_mode", "/run/cos/uki_install_mode"}
)

// fwRunner runs one command until ctx ends.
type fwRunner func(ctx context.Context, name string, args ...string) ([]byte, error)

func execCtx(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	// A killed command's pipes are not waited for longer than this.
	cmd.WaitDelay = time.Second
	return cmd.CombinedOutput()
}

// runFor runs a command with a timeout. A command that does not end in time
// is killed and named in the error.
func runFor(d time.Duration, name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()
	out, err := firewallRun(ctx, name, args...)
	if ctx.Err() == context.DeadlineExceeded {
		return out, &fwTimeout{cmd: name + " " + strings.Join(args, " "), d: d}
	}
	return out, err
}

type fwTimeout struct {
	cmd string
	d   time.Duration
}

func (e *fwTimeout) Error() string { return fmt.Sprintf("`%s` did not finish in %v", e.cmd, e.d) }

// errFirewallNotThisBoot refuses a change on a boot that never runs the
// firewall: saving it would claim a protection that is not there.
var errFirewallNotThisBoot = errors.New("the firewall does not run on the live system or during an install; set it up after the install")

// firewallSkipped is the reason this boot never runs the firewall, or "".
func firewallSkipped() string {
	for _, m := range firewallBootMarkers {
		if _, err := os.Stat(m); err == nil {
			return "The firewall does not run on the live system or during an install (" + filepath.Base(m) + "); it starts after the install."
		}
	}
	return ""
}

// firewallLoaded says whether table inet isotope is loaded. An error is a
// reason the answer is unknown (no nft).
func firewallLoaded() (bool, error) {
	_, err := runFor(firewallStatusTimeout, "nft", "list", "table", "inet", "isotope")
	var ee *exec.Error
	var te *fwTimeout
	switch {
	case errors.As(err, &ee):
		return false, fmt.Errorf("nft cannot run: %v", ee.Err)
	case errors.As(err, &te):
		return false, fmt.Errorf("nft did not answer in %v, so what is loaded is not known", te.d)
	}
	return err == nil, nil
}

const fwSeeStatus = "see `systemctl status isotope-firewall`"

// readFirewallStatus is the saved setting and what is loaded now. A
// settings file that cannot be read is a state (Broken), with what could
// still be read from it, so the window can offer a reset.
func readFirewallStatus() (firewall.Status, error) {
	st, err := loadFirewallState(firewallOEM)
	var out firewall.Status
	if err != nil {
		b, rerr := os.ReadFile(firewallOEM)
		if rerr != nil {
			return firewall.Status{}, err
		}
		st = salvageFirewallState(b)
		out.Broken = true
		out.Reason = fmt.Sprintf("The firewall settings could not be read (%v). Reset them to repair the file.", err)
	}
	out.On, out.Rules = st.On, st.Allow
	loaded, lerr := firewallLoaded()
	out.Active = loaded
	switch {
	case firewallSkipped() != "":
		out.Skipped, out.Reason = true, firewallSkipped()
	case out.Broken:
	case lerr != nil:
		out.Reason = lerr.Error()
	case st.On && !loaded:
		out.Reason = "isotope-firewall.service did not load the rules: " + fwSeeStatus + "."
	case !st.On && loaded:
		out.Reason = "The rules are still loaded: " + fwSeeStatus + "."
	}
	return out, nil
}

// fwNftRule is one allowed port as the settings file's nft text holds it
// (renderFirewallRules), which stays readable when the YAML around it
// does not.
var fwNftRule = regexp.MustCompile(`(?m)^\s*(tcp|udp) dport (\d+) accept(?: comment "([^"\\]*)")?\s*$`)

// fwOffLine is the saved setting `"on": false`.
var fwOffLine = regexp.MustCompile(`(?m)^\s*"?on"?:\s*false\s*$`)

// salvageFirewallState is what can still be read from a settings file that
// does not parse: the allowed ports of its nft text (each checked as a new
// one would be), and on unless it says off.
func salvageFirewallState(b []byte) firewallState {
	st := firewallState{On: !fwOffLine.Match(b)}
	seen := map[string]bool{}
	for _, m := range fwNftRule.FindAllSubmatch(b, -1) {
		port, err := strconv.Atoi(string(m[2]))
		if err != nil || checkFirewallRule(port, string(m[1]), string(m[3])) != nil {
			continue
		}
		r := FirewallRule{Port: uint16(port), Proto: string(m[1]), Note: string(m[3])}
		if !seen[r.String()] {
			seen[r.String()] = true
			st.Allow = append(st.Allow, r)
		}
	}
	sortRules(st.Allow)
	return st
}

// errFirewallBroken refuses a change while the settings cannot be read: a
// change would have to guess the rest of them.
var errFirewallBroken = errors.New("the firewall settings could not be read; reset them first with `isotope firewall reset` or Reset to defaults in Isotope System")

// errFirewallNotBroken refuses a reset of settings that read fine.
var errFirewallNotBroken = errors.New("the firewall settings can be read; there is nothing to reset")

var firewallMu sync.Mutex

// FirewallRule is one allowed port: the D-Bus struct (qss).
type FirewallRule = firewall.Rule

// firewallState is the firewall's settings, kept in firewallOEM.
type firewallState struct {
	On    bool           `yaml:"on"`
	Allow []FirewallRule `yaml:"allow,omitempty"`
}

// checkFirewallRule is shared with Isotope System, so both refuse the same.
func checkFirewallRule(port int, proto, note string) error { return firewall.Check(port, proto, note) }

func sortRules(rs []FirewallRule) {
	sort.Slice(rs, func(i, j int) bool {
		if rs[i].Port != rs[j].Port {
			return rs[i].Port < rs[j].Port
		}
		return rs[i].Proto < rs[j].Proto
	})
}

// defaultFirewallState is a machine with no saved settings: on, with
// firewall.Defaults allowed (/usr/share/isotope/firewall-default.nft).
func defaultFirewallState() firewallState {
	return firewallState{On: true, Allow: append([]FirewallRule(nil), firewall.Defaults...)}
}

// loadFirewallState reads the settings from path. No file is the default.
func loadFirewallState(path string) (firewallState, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return defaultFirewallState(), nil
	}
	if err != nil {
		return firewallState{}, err
	}
	var f struct {
		Firewall *firewallState `yaml:"isotope_firewall"`
	}
	if err := yaml.Unmarshal(b, &f); err != nil {
		return firewallState{}, fmt.Errorf("%s: %w", path, err)
	}
	if f.Firewall == nil {
		return firewallState{}, fmt.Errorf("%s has no isotope_firewall key", path)
	}
	st := *f.Firewall
	for _, r := range st.Allow {
		if err := checkFirewallRule(int(r.Port), r.Proto, r.Note); err != nil {
			return firewallState{}, fmt.Errorf("%s: %w", path, err)
		}
	}
	sortRules(st.Allow)
	return st, nil
}

func flagText(on bool) string {
	if on {
		return "on\n"
	}
	return "off\n"
}

// renderFirewallRules is /etc/isotope/firewall.nft: the allowed ports, added
// to the input chain that /usr/share/isotope/firewall.nft creates. The unit
// loads both in one nft transaction.
func renderFirewallRules(rs []FirewallRule) string {
	var b strings.Builder
	b.WriteString("# The ports allowed in Isotope System or with `isotope firewall allow`.\n")
	b.WriteString("# Written by isotope-updated from " + "/oem/94_isotope_firewall.yaml" + " at every\n")
	b.WriteString("# boot: a change made here is lost.\n")
	b.WriteString("table inet isotope {\n\tchain input {\n")
	for _, r := range rs {
		fmt.Fprintf(&b, "\t\t%s dport %d accept", r.Proto, r.Port)
		if r.Note != "" {
			fmt.Fprintf(&b, " comment %q", r.Note)
		}
		b.WriteString("\n")
	}
	b.WriteString("\t}\n}\n")
	return b.String()
}

// The cloud-config yip reads. isotope_firewall is ignored by yip and read
// back by the daemon.
type fwOEM struct {
	Name     string        `yaml:"name"`
	Firewall firewallState `yaml:"isotope_firewall"`
	Stages   struct {
		Initramfs []fwStage `yaml:"initramfs"`
	} `yaml:"stages"`
}

type fwStage struct {
	Name  string   `yaml:"name"`
	Files []fwFile `yaml:"files"`
}

// fwFile is a yip file. Permissions are decimal (420 is 0644), so YAML 1.1
// and 1.2 readers agree.
type fwFile struct {
	Path        string `yaml:"path"`
	Permissions uint32 `yaml:"permissions"`
	Content     string `yaml:"content"`
}

// renderFirewallOEM is firewallOEM: the settings, and an initramfs stage
// that writes the two files isotope-firewall.service reads.
func renderFirewallOEM(st firewallState) ([]byte, error) {
	st.Allow = append([]FirewallRule(nil), st.Allow...)
	sortRules(st.Allow)
	f := fwOEM{Name: "Isotope firewall", Firewall: st}
	f.Stages.Initramfs = []fwStage{{Name: "Write the firewall settings", Files: []fwFile{
		{Path: "/etc/isotope/firewall", Permissions: 0o644, Content: flagText(st.On)},
		{Path: "/etc/isotope/firewall.nft", Permissions: 0o644, Content: renderFirewallRules(st.Allow)},
	}}}
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(f); err != nil {
		return nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// changeFirewall loads the settings, lets change edit them, writes the boot
// config first and the live files second (if those fail, the next boot
// still applies the choice), reloads the unit, and checks that what is
// loaded is what was asked for. A settings file that does not parse is
// replaced, starting from the defaults, so the window can repair it.
func changeFirewall(change func(*firewallState) error) error {
	firewallMu.Lock()
	defer firewallMu.Unlock()
	if restoring {
		return errRestoring
	}
	if firewallSkipped() != "" {
		return errFirewallNotThisBoot
	}
	st, err := loadFirewallState(firewallOEM)
	if err != nil {
		if _, serr := os.Stat(firewallOEM); serr == nil {
			fmt.Fprintf(os.Stderr, "isotope: %v\n", err)
			return errFirewallBroken
		}
		return err
	}
	if err := change(&st); err != nil {
		return err
	}
	return applyFirewall(st)
}

// resetFirewall replaces settings that cannot be read: on, with the ports
// that could still be read from them, or the defaults when none could.
func resetFirewall() error {
	firewallMu.Lock()
	defer firewallMu.Unlock()
	if restoring {
		return errRestoring
	}
	if firewallSkipped() != "" {
		return errFirewallNotThisBoot
	}
	if _, err := loadFirewallState(firewallOEM); err == nil {
		return errFirewallNotBroken
	}
	b, err := os.ReadFile(firewallOEM)
	if err != nil {
		return err
	}
	st := salvageFirewallState(b)
	st.On = true
	if len(st.Allow) == 0 {
		st = defaultFirewallState()
	}
	return applyFirewall(st)
}

// applyFirewall writes st and loads it. firewallMu is held.
func applyFirewall(st firewallState) error {
	b, err := renderFirewallOEM(st)
	if err != nil {
		return err
	}
	if err := writeRunFile(firewallOEM, string(b)); err != nil {
		return err
	}
	if err := writeRunFile(firewallRules, renderFirewallRules(st.Allow)); err != nil {
		return err
	}
	if err := writeRunFile(firewallFlag, flagText(st.On)); err != nil {
		return err
	}
	// systemctl answers 0 for a unit that its conditions skip, and the
	// reload can fail inside nft, so the table itself is the answer.
	out, err := runFor(firewallReloadTimeout, "systemctl", "reload-or-restart", "isotope-firewall.service")
	var te *fwTimeout
	if errors.As(err, &te) {
		return fmt.Errorf("the setting is saved and applies at the next boot, but %v: %s", err, fwSeeStatus)
	}
	loaded, lerr := firewallLoaded()
	switch {
	case err != nil:
		return fmt.Errorf("the setting is saved and applies at the next boot, but loading it now failed (%s): %v %s",
			fwSeeStatus, err, strings.TrimSpace(string(out)))
	case lerr != nil:
		return fmt.Errorf("the setting is saved, but %v", lerr)
	case st.On && !loaded:
		return fmt.Errorf("the setting is saved, but the firewall is not loaded: %s", fwSeeStatus)
	case !st.On && loaded:
		return fmt.Errorf("the setting is saved, but the firewall rules are still loaded: %s", fwSeeStatus)
	}
	return nil
}

func setFirewall(on bool) error {
	return changeFirewall(func(st *firewallState) error { st.On = on; return nil })
}

func allowPort(port int, proto, note string) error {
	if err := checkFirewallRule(port, proto, note); err != nil {
		return err
	}
	return changeFirewall(func(st *firewallState) error {
		for i, r := range st.Allow {
			if int(r.Port) == port && r.Proto == proto {
				st.Allow[i].Note = note
				return nil
			}
		}
		st.Allow = append(st.Allow, FirewallRule{Port: uint16(port), Proto: proto, Note: note})
		return nil
	})
}

func denyPort(port int, proto string) error {
	if err := checkFirewallRule(port, proto, ""); err != nil {
		return err
	}
	return changeFirewall(func(st *firewallState) error {
		for i, r := range st.Allow {
			if int(r.Port) == port && r.Proto == proto {
				st.Allow = append(st.Allow[:i], st.Allow[i+1:]...)
				return nil
			}
		}
		return fmt.Errorf("%d/%s is not in the list of allowed ports", port, proto)
	})
}

// FirewallStatus is open to every caller, as `nft list ruleset` shows the
// same to root and a port scan to anyone. The status is a{sv}: "on" (the
// saved setting), "active" (table inet isotope is loaded now), "skipped"
// (this boot never runs it), "reason" (why active is not on, or why the
// boot skips it); the allowed ports follow.
func (a busAPI) FirewallStatus() (map[string]dbus.Variant, []FirewallRule, *dbus.Error) {
	done, derr := a.s.enter()
	if derr != nil {
		return nil, nil, derr
	}
	defer done()
	firewallMu.Lock()
	st, err := readFirewallStatus()
	firewallMu.Unlock()
	if err != nil {
		return nil, nil, busError(err)
	}
	if st.Rules == nil {
		st.Rules = []FirewallRule{}
	}
	return map[string]dbus.Variant{
		"on":      dbus.MakeVariant(st.On),
		"active":  dbus.MakeVariant(st.Active),
		"skipped": dbus.MakeVariant(st.Skipped),
		"broken":  dbus.MakeVariant(st.Broken),
		"reason":  dbus.MakeVariant(st.Reason),
	}, st.Rules, nil
}

// firewallStatusFromDict reads FirewallStatus's a{sv}, for the CLI.
func firewallStatusFromDict(m map[string]dbus.Variant) firewall.Status {
	return firewall.Status{On: boolean(m, "on"), Active: boolean(m, "active"),
		Skipped: boolean(m, "skipped"), Broken: boolean(m, "broken"), Reason: str(m, "reason")}
}

// firewallCall is the frame of the three changing methods: the firewall
// action (administrators, never a Flatpak app), not during a restore.
func (a busAPI) firewallCall(sender dbus.Sender, method string, f func() error) *dbus.Error {
	done, derr := a.s.enter()
	if derr != nil {
		return derr
	}
	defer done()
	if err := a.s.authorize(sender, actionFirewall); err != nil {
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

// ResetFirewall repairs settings that cannot be read (see resetFirewall).
func (a busAPI) ResetFirewall(sender dbus.Sender) *dbus.Error {
	return a.firewallCall(sender, "ResetFirewall", resetFirewall)
}

func (a busAPI) SetFirewall(sender dbus.Sender, on bool) *dbus.Error {
	return a.firewallCall(sender, "SetFirewall", func() error { return setFirewall(on) })
}

func (a busAPI) AllowPort(sender dbus.Sender, port uint16, proto, note string) *dbus.Error {
	return a.firewallCall(sender, "AllowPort", func() error { return allowPort(int(port), proto, note) })
}

func (a busAPI) DenyPort(sender dbus.Sender, port uint16, proto string) *dbus.Error {
	return a.firewallCall(sender, "DenyPort", func() error { return denyPort(int(port), proto) })
}
