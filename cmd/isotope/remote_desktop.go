package main

// `isotope remote-desktop`: a VNC server for the user's own session, wayvnc
// from the remote-desktop system extension, on Sway and niri. Off by
// default; turned on per user (here or in Isotope System's Network page)
// and only for the session: it listens while it runs, and the firewall
// allows its port only while it runs. Every connection is encrypted (TLS
// through VeNCrypt, or RSA-AES) and needs the user's name and a password of
// its own. See docs/superpowers/specs/2026-11-24-remote-desktop-design.md.
//
// The server runs as the user unit isotope-remote-desktop.service (in the
// image, never enabled), whose ExecStart is `isotope remote-desktop serve`
// and whose ExecStopPost is `isotope remote-desktop stopped`, so the port
// closes however the server ends: turned off, crashed, or at logout.

import (
	"bufio"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/kairos-community/isotope/pkg/firewall"
	"github.com/kairos-community/isotope/pkg/remotedesktop"
)

const remoteDesktopUsage = `Usage: isotope remote-desktop [command]

Let another computer see and control this desktop with a VNC viewer
(TigerVNC, Remmina, RealVNC Viewer). Sway and niri only; it needs the
remote-desktop system extension ("isotope ext install remote-desktop").

It is off until you turn it on, and it stays on only until you turn it off
or log out. While it is on, it listens on port 5900 and the firewall lets
connections in on that port. A viewer logs in with your user name and the
remote desktop password (not your login password), and every connection is
encrypted: TLS (VeNCrypt) or RSA-AES.

Commands:
  status [--json]     on or off, the addresses to connect to, the user
                      name and the TLS certificate's fingerprint (default)
  on                  start the server and allow port 5900/tcp in the
                      firewall (an administrator's job while the firewall
                      is on)
  off                 stop the server and close the port again
  password            set the remote desktop password, read from the
                      standard input (8 to 64 characters)
  password --generate set a random password and print it
`

// rdUnit is the user unit that runs the server.
const rdUnit = "isotope-remote-desktop.service"

// The seams of the command, variables for the tests.
var (
	rdSessionFile = "/etc/isotope/session"
	rdLiveMarker  = "/run/isotope/live-desktop"
	rdKeyboard    = "/etc/isotope/keyboard"
	rdWayvnc      = "/usr/bin/wayvnc"
	// rdSystemctl runs `systemctl --user ARGS`.
	rdSystemctl = func(args ...string) ([]byte, error) {
		return exec.Command("systemctl", append([]string{"--user"}, args...)...).CombinedOutput()
	}
	// rdJournal is the server's last lines, for an error.
	rdJournal = func() string {
		out, _ := exec.Command("journalctl", "--user", "-u", rdUnit, "-n", "3", "-o", "cat", "--no-pager").Output()
		return strings.TrimSpace(string(out))
	}
	rdFirewall = func() (firewallClient, error) { return dialFirewall() }
	rdUser     = func() string {
		if u, err := user.Current(); err == nil {
			return u.Username
		}
		return ""
	}
	rdAddresses = ipv4Addresses
	// rdListening says the server accepts connections on its port.
	rdListening = func() bool {
		c, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", remotedesktop.Port), time.Second)
		if err != nil {
			return false
		}
		c.Close()
		return true
	}
	rdExec      = syscall.Exec
	rdStartWait = 5 * time.Second
	rdRSABits   = 3072
)

// rdConfigDir is $XDG_CONFIG_HOME/isotope/remote-desktop: the server's
// configuration (with the password), its keys and its certificate, 0700.
func rdConfigDir() string {
	dir := os.Getenv("XDG_CONFIG_HOME")
	if dir == "" {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".config")
	}
	return filepath.Join(dir, "isotope", "remote-desktop")
}

func rdConfigFile() string { return filepath.Join(rdConfigDir(), "wayvnc.conf") }

// rdPasswordFile keeps the password alone, so the configuration can be
// written again (a new user name, new keys) without asking for it.
func rdPasswordFile() string { return filepath.Join(rdConfigDir(), "password") }

// rdAvailability says why the server cannot run in this session, "" when it
// can. needsExt is true when only the extension is missing.
func rdAvailability() (reason string, needsExt bool) {
	if _, err := os.Stat(rdLiveMarker); err == nil {
		return "Remote desktop is not available on the live system. Install Isotope first.", false
	}
	if rel, err := LoadRelease(releaseFile()); err == nil {
		switch rel.Flavor {
		case flavorAgent:
			return "Remote desktop is not available on the agent.", false
		case flavorServer:
			return "Remote desktop is not available on Isotope Server: it has no desktop.", false
		}
	}
	switch rdSession() {
	case "sway", "niri":
	case "cosmic":
		return "COSMIC has no remote desktop server yet. Sway and niri have one.", false
	case "i3":
		return "Remote desktop needs a Wayland desktop: Sway or niri. i3 runs on X11.", false
	default:
		return "Remote desktop works on the Sway and niri desktops.", false
	}
	if _, err := os.Stat(rdWayvnc); err != nil {
		return "Remote desktop needs the remote-desktop system extension: isotope ext install remote-desktop", true
	}
	return "", false
}

// rdSession is SESSION from the session file: sway, i3, niri, cosmic.
func rdSession() string {
	b, err := os.ReadFile(rdSessionFile)
	if err != nil {
		return ""
	}
	for _, l := range strings.Split(string(b), "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(l), "SESSION="); ok {
			return strings.Trim(v, `"'`)
		}
	}
	return ""
}

// ipv4Addresses are the addresses of the interfaces that are up, without
// loopback and link-local ones.
func ipv4Addresses() []string {
	var out []string
	ifs, err := net.Interfaces()
	if err != nil {
		return nil
	}
	for _, i := range ifs {
		if i.Flags&net.FlagUp == 0 || i.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, _ := i.Addrs()
		for _, a := range addrs {
			n, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			if ip := n.IP.To4(); ip != nil && !ip.IsLoopback() && !ip.IsLinkLocalUnicast() {
				out = append(out, ip.String())
			}
		}
	}
	return out
}

func rdRunning() bool {
	out, _ := rdSystemctl("is-active", rdUnit)
	return strings.TrimSpace(string(out)) == "active"
}

func cmdRemoteDesktop(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	sub := "status"
	if len(args) > 0 {
		sub, args = args[0], args[1:]
	}
	usageErr := func(msg string) int {
		fmt.Fprintf(stderr, "isotope remote-desktop: %s\n\n%s", msg, remoteDesktopUsage)
		return exitUsage
	}
	fail := func(err error) int {
		fmt.Fprintf(stderr, "isotope remote-desktop: %v\n", err)
		return exitError
	}
	switch sub {
	case "help", "-h", "--help":
		fmt.Fprint(stdout, remoteDesktopUsage)
		return exitOK
	case "status":
		asJSON := false
		for _, a := range args {
			if a != "--json" {
				return usageErr(fmt.Sprintf("unknown argument %q", a))
			}
			asJSON = true
		}
		return rdPrintStatus(asJSON, stdout, stderr)
	case "on", "off", "serve", "stopped", "cleanup":
		if len(args) != 0 {
			return usageErr(sub + " takes no arguments")
		}
	case "password":
		if len(args) > 1 || (len(args) == 1 && args[0] != "--generate") {
			return usageErr("password takes only --generate")
		}
	default:
		return usageErr(fmt.Sprintf("unknown command %q", sub))
	}
	switch sub {
	case "on":
		if err := rdOn(); err != nil {
			return fail(err)
		}
		st := rdStatus()
		fmt.Fprintf(stdout, "Remote desktop is on. Connect a VNC viewer to %s as %s.\n", rdConnectText(st), st.User)
		if st.Firewall == remotedesktop.FirewallClosed {
			fmt.Fprintf(stdout, "The firewall blocks port %d/%s: nobody can connect from another computer.\n", remotedesktop.Port, remotedesktop.Proto)
		}
	case "off":
		if err := rdOff(); err != nil {
			return fail(err)
		}
		fmt.Fprintln(stdout, "Remote desktop is off.")
	case "password":
		var pw string
		if len(args) == 1 {
			p, err := remotedesktop.GeneratePassword()
			if err != nil {
				return fail(err)
			}
			pw = p
		} else {
			line, err := bufio.NewReader(io.LimitReader(stdin, 1024)).ReadString('\n')
			if err != nil && !(errors.Is(err, io.EOF) && line != "") {
				return fail(errors.New("type the new password on the standard input"))
			}
			pw = strings.TrimRight(line, "\r\n")
		}
		if err := rdSetPassword(pw); err != nil {
			return fail(err)
		}
		if len(args) == 1 {
			fmt.Fprintf(stdout, "The remote desktop password is %s\n", pw)
		} else {
			fmt.Fprintln(stdout, "The remote desktop password is set.")
		}
	case "serve":
		return rdServe(stderr)
	case "stopped":
		// The unit's ExecStopPost: never a failure of its own, so a
		// refused firewall change only leaves a line in the journal.
		if err := rdClosePort(); err != nil {
			fmt.Fprintf(stderr, "isotope remote-desktop: closing port %d: %v\n", remotedesktop.Port, err)
		}
	case "cleanup":
		rdCleanup(stderr)
	}
	return exitOK
}

// rdConnectText is "192.168.1.20:5900" or "192.168.1.20:5900 or 10.0.0.4:5900".
func rdConnectText(st remotedesktop.Status) string {
	if len(st.Addresses) == 0 {
		return fmt.Sprintf("this computer's address, port %d", st.Port)
	}
	var out []string
	for _, a := range st.Addresses {
		out = append(out, fmt.Sprintf("%s:%d", a, st.Port))
	}
	return strings.Join(out, " or ")
}

// rdStatus gathers the status; the firewall is asked only while the server
// runs.
func rdStatus() remotedesktop.Status {
	st := remotedesktop.Status{User: rdUser(), Port: remotedesktop.Port, Addresses: rdAddresses()}
	if st.Addresses == nil {
		st.Addresses = []string{}
	}
	st.Reason, st.NeedsExtension = rdAvailability()
	st.Available = st.Reason == ""
	if _, err := os.Stat(rdPasswordFile()); err == nil {
		st.PasswordSet = true
	}
	st.Fingerprint = rdFingerprint()
	if st.Available {
		st.Running = rdRunning()
	}
	if st.Running {
		st.Firewall = rdFirewallState(st.User)
	}
	return st
}

func rdPrintStatus(asJSON bool, stdout, stderr io.Writer) int {
	st := rdStatus()
	if asJSON {
		b, err := json.Marshal(st)
		if err != nil {
			fmt.Fprintf(stderr, "isotope remote-desktop: %v\n", err)
			return exitError
		}
		fmt.Fprintln(stdout, string(b))
		return exitOK
	}
	switch {
	case !st.Available:
		fmt.Fprintln(stdout, st.Reason)
		return exitOK
	case st.Running:
		fmt.Fprintf(stdout, "Remote desktop: on\nConnect to:     %s\nUser name:      %s\n", rdConnectText(st), st.User)
		switch st.Firewall {
		case remotedesktop.FirewallClosed:
			fmt.Fprintf(stdout, "Firewall:       blocks port %d/%s\n", st.Port, remotedesktop.Proto)
		case remotedesktop.FirewallOff:
			fmt.Fprintln(stdout, "Firewall:       off")
		case remotedesktop.FirewallOpen, remotedesktop.FirewallAllowed:
			fmt.Fprintf(stdout, "Firewall:       allows port %d/%s\n", st.Port, remotedesktop.Proto)
		}
	default:
		fmt.Fprintln(stdout, "Remote desktop: off")
	}
	if !st.PasswordSet {
		fmt.Fprintln(stdout, "Password:       not set (isotope remote-desktop password)")
	}
	if st.Fingerprint != "" {
		fmt.Fprintf(stdout, "Certificate:    SHA-256 %s\n", st.Fingerprint)
	}
	return exitOK
}

// rdSetPassword saves pw and writes the configuration. A running server is
// started again, so the new password counts at once.
func rdSetPassword(pw string) error {
	if err := remotedesktop.CheckPassword(pw); err != nil {
		return err
	}
	if err := rdEnsureDir(); err != nil {
		return err
	}
	if err := rdWriteFile(rdPasswordFile(), []byte(pw+"\n"), 0o600); err != nil {
		return err
	}
	if err := rdWriteConfig(); err != nil {
		return err
	}
	if reason, _ := rdAvailability(); reason == "" && rdRunning() {
		if err := rdOff(); err != nil {
			return err
		}
		return rdOn()
	}
	return nil
}

func rdReadPassword() (string, error) {
	b, err := os.ReadFile(rdPasswordFile())
	if errors.Is(err, os.ErrNotExist) {
		return "", errors.New("set a remote desktop password first: isotope remote-desktop password")
	}
	if err != nil {
		return "", err
	}
	pw := strings.TrimRight(string(b), "\n")
	if err := remotedesktop.CheckPassword(pw); err != nil {
		return "", fmt.Errorf("the saved password cannot be used (%v): set it again", err)
	}
	return pw, nil
}

func rdEnsureDir() error {
	dir := rdConfigDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	return os.Chmod(dir, 0o700)
}

// rdWriteFile writes path through a temporary file and a rename.
func rdWriteFile(path string, b []byte, mode os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// The key and certificate files, next to the configuration.
const (
	rdRSAKey  = "rsa-key.pem"
	rdTLSKey  = "tls-key.pem"
	rdTLSCert = "tls-cert.pem"
)

// rdEnsureKeys makes the RSA key (RSA-AES) and the TLS key and self-signed
// certificate (VeNCrypt) once. They stay, so a viewer that trusted the
// certificate's fingerprint keeps trusting it.
func rdEnsureKeys() error {
	dir := rdConfigDir()
	if _, err := os.Stat(filepath.Join(dir, rdRSAKey)); errors.Is(err, os.ErrNotExist) {
		k, err := rsa.GenerateKey(rand.Reader, rdRSABits)
		if err != nil {
			return err
		}
		// neatvnc reads only PKCS #1 ("RSA PRIVATE KEY").
		b := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(k)})
		if err := rdWriteFile(filepath.Join(dir, rdRSAKey), b, 0o600); err != nil {
			return err
		}
	}
	_, errKey := os.Stat(filepath.Join(dir, rdTLSKey))
	_, errCert := os.Stat(filepath.Join(dir, rdTLSCert))
	if errKey == nil && errCert == nil {
		return nil
	}
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
	if err != nil {
		return err
	}
	host, _ := os.Hostname()
	if host == "" {
		host = "isotope"
	}
	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: host, Organization: []string{"Isotope remote desktop"}},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.AddDate(10, 0, 0),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		DNSNames:              []string{host},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &k.PublicKey, k)
	if err != nil {
		return err
	}
	kb, err := x509.MarshalECPrivateKey(k)
	if err != nil {
		return err
	}
	if err := rdWriteFile(filepath.Join(dir, rdTLSKey), pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kb}), 0o600); err != nil {
		return err
	}
	return rdWriteFile(filepath.Join(dir, rdTLSCert), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600)
}

// rdFingerprint is the certificate's SHA-256, as viewers show it
// (AB:CD:...); empty when there is none yet.
func rdFingerprint() string {
	b, err := os.ReadFile(filepath.Join(rdConfigDir(), rdTLSCert))
	if err != nil {
		return ""
	}
	blk, _ := pem.Decode(b)
	if blk == nil || blk.Type != "CERTIFICATE" {
		return ""
	}
	sum := sha256.Sum256(blk.Bytes)
	parts := make([]string, len(sum))
	for i, c := range sum {
		parts[i] = fmt.Sprintf("%02X", c)
	}
	return strings.Join(parts, ":")
}

// rdWriteConfig writes wayvnc's configuration: authentication always on,
// encryption always required (no relax_encryption, no allow_broken_crypto,
// so no plain VNC password and no unencrypted session), the user's own name.
func rdWriteConfig() error {
	pw, err := rdReadPassword()
	if err != nil {
		return err
	}
	name := rdUser()
	if name == "" {
		return errors.New("cannot tell who you are")
	}
	if err := rdEnsureKeys(); err != nil {
		return err
	}
	conf := fmt.Sprintf(`# Written by "isotope remote-desktop"; it writes this file again.
use_relative_paths=true
address=0.0.0.0
port=%d
enable_auth=true
username=%s
password=%s
rsa_private_key_file=%s
private_key_file=%s
certificate_file=%s
`, remotedesktop.Port, name, pw, rdRSAKey, rdTLSKey, rdTLSCert)
	return rdWriteFile(rdConfigFile(), []byte(conf), 0o600)
}

// rdOn opens the port and starts the server, and waits until it listens.
func rdOn() error {
	if reason, _ := rdAvailability(); reason != "" {
		return errors.New(reason)
	}
	if err := rdEnsureDir(); err != nil {
		return err
	}
	if err := rdWriteConfig(); err != nil {
		return err
	}
	if rdRunning() {
		return nil
	}
	if err := rdOpenPort(); err != nil {
		return err
	}
	if out, err := rdSystemctl("start", rdUnit); err != nil {
		rdClosePort()
		return fmt.Errorf("the server did not start: %s", strings.TrimSpace(string(out)))
	}
	deadline := time.Now().Add(rdStartWait)
	for {
		if rdRunning() && rdListening() {
			return nil
		}
		if time.Now().After(deadline) {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	why := rdJournal()
	rdSystemctl("stop", rdUnit)
	rdClosePort()
	if why == "" {
		why = "it does not listen on its port"
	}
	return fmt.Errorf("the server did not start: %s", why)
}

// rdOff stops the server; its ExecStopPost closes the port, and so does
// this, in case the unit was never started.
func rdOff() error {
	if out, err := rdSystemctl("stop", rdUnit); err != nil {
		return fmt.Errorf("stopping the server: %s", strings.TrimSpace(string(out)))
	}
	return rdClosePort()
}

// rdFindRule is the rule for the port, if any.
func rdFindRule(rules []firewall.Rule) (firewall.Rule, bool) {
	for _, r := range rules {
		if int(r.Port) == remotedesktop.Port && r.Proto == remotedesktop.Proto {
			return r, true
		}
	}
	return firewall.Rule{}, false
}

// rdOpenPort allows the port with this user's note while the firewall is
// on. A rule that already allows the port stays as it is.
func rdOpenPort() error {
	c, err := rdFirewall()
	if err != nil {
		return errors.New("the update service is not available (isotope-updated), so the firewall cannot be asked")
	}
	defer c.Close()
	fw, err := c.FirewallStatus()
	if err != nil {
		return fmt.Errorf("the firewall: %s", describeFirewallError(err))
	}
	if !fw.On || fw.Skipped {
		return nil
	}
	if _, ok := rdFindRule(fw.Rules); ok {
		return nil
	}
	err = c.AllowPort(remotedesktop.Port, remotedesktop.Proto, remotedesktop.Note(rdUser()))
	if err != nil {
		if daemonErrName(wrapBusErr(err)) == "NotAuthorized" {
			return fmt.Errorf("only an administrator can turn on remote desktop while the firewall is on: it allows port %d/%s", remotedesktop.Port, remotedesktop.Proto)
		}
		return fmt.Errorf("the firewall: %s", describeFirewallError(err))
	}
	return nil
}

// rdClosePort removes this user's rule for the port, and no other.
func rdClosePort() error {
	c, err := rdFirewall()
	if err != nil {
		return err
	}
	defer c.Close()
	fw, err := c.FirewallStatus()
	if err != nil {
		return errors.New(describeFirewallError(err))
	}
	r, ok := rdFindRule(fw.Rules)
	if !ok || r.Note != remotedesktop.Note(rdUser()) {
		return nil
	}
	if err := c.DenyPort(remotedesktop.Port, remotedesktop.Proto); err != nil {
		return errors.New(describeFirewallError(err))
	}
	return nil
}

// rdFirewallState is the Status.Firewall of a running server.
func rdFirewallState(name string) string {
	c, err := rdFirewall()
	if err != nil {
		return ""
	}
	defer c.Close()
	fw, err := c.FirewallStatus()
	if err != nil {
		return ""
	}
	if !fw.On || !fw.Active {
		return remotedesktop.FirewallOff
	}
	r, ok := rdFindRule(fw.Rules)
	switch {
	case !ok:
		return remotedesktop.FirewallClosed
	case r.Note == remotedesktop.Note(name):
		return remotedesktop.FirewallOpen
	}
	return remotedesktop.FirewallAllowed
}

// rdCleanup removes this user's rule when no server runs: one left by a
// session that ended without its ExecStopPost (a power cut). The tray runs
// it at login; a refusal (not an administrator) is only logged.
func rdCleanup(stderr io.Writer) {
	if reason, _ := rdAvailability(); reason != "" || rdRunning() {
		return
	}
	if err := rdClosePort(); err != nil {
		fmt.Fprintf(stderr, "isotope remote-desktop: closing a port left open: %v\n", err)
	}
}

// rdServe is the unit's ExecStart: the checks, then wayvnc in its place.
func rdServe(stderr io.Writer) int {
	if reason, _ := rdAvailability(); reason != "" {
		fmt.Fprintln(stderr, "isotope remote-desktop:", reason)
		return exitError
	}
	conf := rdConfigFile()
	fi, err := os.Stat(conf)
	if err != nil {
		fmt.Fprintln(stderr, "isotope remote-desktop: no configuration: turn it on with isotope remote-desktop on")
		return exitError
	}
	if fi.Mode().Perm()&0o077 != 0 {
		fmt.Fprintf(stderr, "isotope remote-desktop: %s can be read by others; turn remote desktop on again\n", conf)
		return exitError
	}
	if os.Getenv("WAYLAND_DISPLAY") == "" {
		fmt.Fprintln(stderr, "isotope remote-desktop: no Wayland session (WAYLAND_DISPLAY is not set)")
		return exitError
	}
	env := os.Environ()
	if os.Getenv("XKB_DEFAULT_LAYOUT") == "" {
		env = append(env, rdKeyboardEnv()...)
	}
	argv := []string{rdWayvnc, "--config=" + conf, "--render-cursor"}
	if dir := os.Getenv("XDG_RUNTIME_DIR"); dir != "" {
		argv = append(argv, "--socket="+filepath.Join(dir, "isotope", "wayvncctl"))
		os.MkdirAll(filepath.Join(dir, "isotope"), 0o700)
	}
	if err := rdExec(rdWayvnc, argv, env); err != nil {
		fmt.Fprintf(stderr, "isotope remote-desktop: %s: %v\n", rdWayvnc, err)
		return exitError
	}
	return exitOK
}

// rdKeyboardEnv is the installer's keyboard layout as libxkbcommon's
// variables, so keys a viewer sends map onto the layout the desktop uses.
func rdKeyboardEnv() []string {
	b, err := os.ReadFile(rdKeyboard)
	if err != nil {
		return nil
	}
	var layout, variant string
	for _, l := range strings.Split(string(b), "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(l), "=")
		if !ok {
			continue
		}
		switch k {
		case "XKB_LAYOUT":
			layout = v
		case "XKB_VARIANT":
			variant = v
		}
	}
	if layout == "" {
		return nil
	}
	env := []string{"XKB_DEFAULT_LAYOUT=" + layout}
	if variant != "" {
		env = append(env, "XKB_DEFAULT_VARIANT="+variant)
	}
	return env
}
