package main

import (
	"bytes"
	"context"
	"errors"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"
)

// fakeOpenConnect is an `openconnect --authenticate` that reads the password
// from stdin, and in some modes asks for a code on stderr as openconnect
// does: a label with no newline, then a line from stdin. It writes its argv
// to $FAKE_OC_ARGV.
const fakeOpenConnect = `#!/bin/sh
printf '%s\n' "$@" > "$FAKE_OC_ARGV"
echo $$ > "$FAKE_OC_PID"
IFS= read -r pw || exit 1
echo "POST https://vpn.example.com/" >&2
case "$FAKE_OC_MODE" in
otp|otp-wrong)
	printf 'Verification code:' >&2
	IFS= read -r code || exit 1
	[ "$code" = 654321 ] || { echo "Invalid code" >&2; echo "Failed to complete authentication" >&2; exit 1; } ;;
reprompt)
	if [ "$pw" != secret ]; then
		echo "Login failed." >&2
		printf 'Password:' >&2
		IFS= read -r pw || exit 1
	fi ;;
banner)
	echo "Unauthorized access is denied. All logins are recorded." >&2
	printf 'Verification code:' >&2
	IFS= read -r code || exit 1
	[ "$code" = 654321 ] || exit 1 ;;
empty)
	IFS= read -r code || exit 1
	[ "$code" = 654321 ] || exit 1 ;;
slow)
	sleep 1
	echo "Connected to 192.0.2.7:443" >&2
	sleep 1 ;;
hang)
	sleep 30 ;;
twice)
	printf 'Verification code:' >&2; IFS= read -r code || exit 1
	printf 'Verification code:' >&2; IFS= read -r code || exit 1 ;;
nocookie)
	exit 0 ;;
esac
if [ "$pw" != secret ]; then
	echo "Login failed." >&2
	echo "Failed to complete authentication" >&2
	exit 1
fi
echo "COOKIE='abc123'"
echo "HOST='192.0.2.7'"
echo "CONNECT_URL='https://vpn.example.com/eng'"
echo "FINGERPRINT='pin-sha256:AAAA'"
echo "RESOLVE='vpn.example.com:192.0.2.7'"
`

// fakeOC installs the fake and returns a runner for it and its argv file.
func fakeOC(t *testing.T, mode string) (ocRunner, string) {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "openconnect")
	if err := os.WriteFile(bin, []byte(fakeOpenConnect), 0o755); err != nil {
		t.Fatal(err)
	}
	argv := filepath.Join(dir, "argv")
	t.Setenv("FAKE_OC_ARGV", argv)
	t.Setenv("FAKE_OC_PID", filepath.Join(dir, "pid"))
	t.Setenv("FAKE_OC_MODE", mode)
	return ocRunner{bin: bin, quiet: 100 * time.Millisecond, timeout: 10 * time.Second}, argv
}

var wantOCSecrets = map[string]string{
	"cookie":  "abc123",
	"gateway": "https://vpn.example.com/eng",
	"gwcert":  "pin-sha256:AAAA",
	"resolve": "vpn.example.com:192.0.2.7",
}

func TestOpenConnectLogin(t *testing.T) {
	r, argvFile := fakeOC(t, "ok")
	asked := 0
	got, err := r.login(context.Background(), ocLogin{Gateway: "vpn.example.com", Protocol: "gp", User: "ada", Password: "secret",
		Second: func(context.Context, string) (string, error) { asked++; return "", nil }})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, wantOCSecrets) {
		t.Fatalf("secrets %v", got)
	}
	if asked != 0 {
		t.Fatal("asked for a code the server did not want")
	}
	argv, _ := os.ReadFile(argvFile)
	want := "--authenticate\n--non-inter\n--passwd-on-stdin\n--protocol=gp\n--user=ada\n--\nvpn.example.com\n"
	if string(argv) != want {
		t.Fatalf("argv %q", argv)
	}
}

func TestOpenConnectLoginAnswersTheCodePrompt(t *testing.T) {
	r, argvFile := fakeOC(t, "otp")
	var prompts []string
	got, err := r.login(context.Background(), ocLogin{Gateway: "vpn.example.com", Protocol: "anyconnect", User: "ada", Password: "secret",
		Second: func(_ context.Context, p string) (string, error) { prompts = append(prompts, p); return "654321", nil }})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(prompts, []string{"Verification code:"}) {
		t.Fatalf("prompts %q", prompts)
	}
	if !reflect.DeepEqual(got, wantOCSecrets) {
		t.Fatalf("secrets %v", got)
	}
	if argv, _ := os.ReadFile(argvFile); strings.Contains(string(argv), "secret") || strings.Contains(string(argv), "654321") {
		t.Fatalf("a secret is in argv: %q", argv)
	}
}

func TestOpenConnectLoginFailures(t *testing.T) {
	for _, c := range []struct {
		mode, password, code, reason string
	}{
		{"ok", "wrong", "", "Login failed."},
		// The server asks for the password again: no second dialog.
		{"reprompt", "wrong", "", "Login failed."},
		{"otp-wrong", "secret", "000000", "Invalid code"},
		// One second step only.
		{"twice", "secret", "654321", "the server asked again for: Verification code"},
		{"nocookie", "secret", "", "openconnect gave no login cookie"},
	} {
		t.Run(c.mode, func(t *testing.T) {
			r, _ := fakeOC(t, c.mode)
			asked := 0
			got, err := r.login(context.Background(), ocLogin{Gateway: "vpn.example.com", Password: c.password,
				Second: func(context.Context, string) (string, error) { asked++; return c.code, nil }})
			var le *LoginError
			if !errors.As(err, &le) || le.Reason != c.reason {
				t.Fatalf("err %v", err)
			}
			if got != nil {
				t.Fatalf("secrets %v on a failed login", got)
			}
			if c.mode == "reprompt" && asked != 0 {
				t.Fatal("a failed password was taken for a second step")
			}
			if strings.Contains(err.Error(), c.password) && c.password != "" && c.password != "wrong" {
				t.Fatalf("the password is in the error: %v", err)
			}
		})
	}
}

func TestOpenConnectLoginRefusesBadInput(t *testing.T) {
	r, _ := fakeOC(t, "ok")
	for _, l := range []ocLogin{
		{Gateway: "--script=/tmp/x", Password: "secret"},
		{Gateway: "", Password: "secret"},
		{Gateway: "vpn.example.com", Protocol: "gp --x", Password: "secret"},
		{Gateway: "vpn.example.com", Password: ""},
		{Gateway: "vpn.example.com", Password: "a\nb"},
	} {
		if _, err := r.login(context.Background(), l); err == nil {
			t.Errorf("%+v was accepted", l.Gateway)
		}
	}
}

func TestParseAuthenticate(t *testing.T) {
	m := parseAuthenticate([]byte("COOKIE='a=b;c'\nHOST='h'\nRESOLVE=\nnoise\n"))
	if m["COOKIE"] != "a=b;c" || m["HOST"] != "h" || m["RESOLVE"] != "" {
		t.Fatalf("%v", m)
	}
	// Lines the real openconnect 9.21 printed (see the task report).
	cert := []string{"POST https://self-signed.badssl.com/", "Server certificate verify failed: self-signed certificate",
		"Failed to open HTTPS connection to self-signed.badssl.com", "Failed to complete authentication"}
	if got := reason(cert); got != "the server's certificate is not trusted (self-signed certificate)" {
		t.Errorf("reason %q", got)
	}
	if got := reason([]string{"Got HTTP response: HTTP/1.1 405 Method Not Allowed", "Invalid credentials; try again.", "Password:"}); got != "Invalid credentials; try again." {
		t.Errorf("reason %q", got)
	}
	s, err := secretsFrom(map[string]string{"COOKIE": "c", "FINGERPRINT": "f"}, "vpn.example.com")
	if err != nil || !reflect.DeepEqual(s, map[string]string{"cookie": "c", "gwcert": "f", "gateway": "vpn.example.com"}) {
		t.Fatalf("%v %v", s, err)
	}
}

// The agent's whole OpenConnect path with the fake binary: the password
// dialog, the second dialog for the code, and the a{ss} reply.
func TestSecretAgentOpenConnect(t *testing.T) {
	r, _ := fakeOC(t, "otp")
	var mu sync.Mutex
	var asks [][]string
	a := &secretAgent{login: r.login, ask: func(args ...string) (map[string]any, error) {
		mu.Lock()
		asks = append(asks, args)
		mu.Unlock()
		if strings.Contains(strings.Join(args, " "), "--fields otp") {
			return map[string]any{"otp": "654321"}, nil
		}
		return map[string]any{"password": "secret"}, nil
	}, notify: func(s, b string) error { t.Errorf("notified %q %q", s, b); return nil }}
	res, derr := a.GetSecrets(vpnConn(), "/c/9", "vpn", nil, nmAllowInteraction)
	if derr != nil {
		t.Fatal(derr)
	}
	sec := res["vpn"]["secrets"]
	if sig := sec.Signature().String(); sig != "a{ss}" {
		t.Fatalf("secrets are %s, not a{ss}", sig)
	}
	if !reflect.DeepEqual(sec.Value(), wantOCSecrets) {
		t.Fatalf("secrets %v", sec.Value())
	}
	want := [][]string{
		{"vpn-secret", "--name", "Work", "--fields", "password", "--message", "Log in to vpn.example.com as ada."},
		{"vpn-secret", "--name", "Work", "--fields", "otp", "--message", "The server asks for a second step to log in. It asks for: Verification code"},
	}
	if !reflect.DeepEqual(asks, want) {
		t.Fatalf("asked %q", asks)
	}
}

func TestSecretAgentOpenConnectWrongPassword(t *testing.T) {
	r, _ := fakeOC(t, "ok")
	var logs bytes.Buffer
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	var note string
	a := &secretAgent{login: r.login,
		ask:    func(...string) (map[string]any, error) { return map[string]any{"password": "hunter2-wrong"}, nil },
		notify: func(s, b string) error { note = s + ": " + b; return nil }}
	res, derr := a.GetSecrets(vpnConn(), "/c/9", "vpn", nil, nmAllowInteraction)
	if derr == nil || derr.Name != errNoSecrets || res != nil {
		t.Fatalf("%v, %v", res, derr)
	}
	if note != "VPN Work: The login failed: Login failed." {
		t.Fatalf("notification %q", note)
	}
	if strings.Contains(logs.String(), "hunter2") || strings.Contains(note, "hunter2") {
		t.Fatalf("the password was logged: %q", logs.String())
	}
}

func TestSecretAgentOpenConnectCancelStopsTheLogin(t *testing.T) {
	r, _ := fakeOC(t, "otp")
	set := make(chan struct{}, 1)
	release := make(chan struct{})
	defer close(release)
	a := &secretAgent{login: r.login, pendingSet: func() { set <- struct{}{} },
		ask: func(args ...string) (map[string]any, error) {
			if strings.Contains(strings.Join(args, " "), "--fields otp") {
				<-release // the code dialog stays open
				return nil, errAskCanceled
			}
			return map[string]any{"password": "secret"}, nil
		},
		notify: func(s, b string) error { t.Errorf("notified %q", b); return nil }}
	done := make(chan *dbus.Error, 1)
	go func() { _, derr := a.GetSecrets(vpnConn(), "/c/9", "vpn", nil, nmAllowInteraction); done <- derr }()
	<-set
	time.Sleep(400 * time.Millisecond) // the code prompt is up
	a.CancelGetSecrets("/c/9", "vpn")
	select {
	case derr := <-done:
		if derr == nil || derr.Name != errUserCanceled {
			t.Fatalf("%v", derr)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("GetSecrets still blocked")
	}
	if !gone(t, os.Getenv("FAKE_OC_PID")) {
		t.Fatal("openconnect was not killed and reaped")
	}
}

// A banner with a failure word before the code prompt is not a failed
// login; an empty label is still a prompt; a slow server is not a prompt.
func TestOpenConnectLoginPromptShapes(t *testing.T) {
	for _, c := range []struct{ mode, prompt string }{
		{"banner", "Verification code:"},
		{"empty", ""},
		{"slow", "-"},
	} {
		t.Run(c.mode, func(t *testing.T) {
			if c.prompt == "" && !shReadsStdin(t) {
				t.Skip("this sh does not wait in read(0), or /proc/PID/syscall is not readable")
			}
			r, _ := fakeOC(t, c.mode)
			var prompts []string
			got, err := r.login(context.Background(), ocLogin{Gateway: "vpn.example.com", Password: "secret",
				Second: func(_ context.Context, p string) (string, error) { prompts = append(prompts, p); return "654321", nil }})
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, wantOCSecrets) {
				t.Fatalf("secrets %v", got)
			}
			want := []string{c.prompt}
			if c.prompt == "-" {
				want = nil
			}
			if !reflect.DeepEqual(prompts, want) {
				t.Fatalf("prompts %q, want %q", prompts, want)
			}
		})
	}
}

func TestPasswordPrompt(t *testing.T) {
	for label, want := range map[string]bool{"Password:": true, "password": true, " PASSWORD: ": true,
		"Second Password:": false, "Verification code:": false, "": false, "Passcode:": false} {
		if passwordPrompt(label) != want {
			t.Errorf("%q: %v", label, !want)
		}
	}
}

// gone says whether the process pid no longer exists (killed and reaped).
func gone(t *testing.T, pidFile string) bool {
	t.Helper()
	b, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil {
		t.Fatal(err)
	}
	return syscall.Kill(pid, 0) == syscall.ESRCH
}

func TestSecretAgentOpenConnectTimeout(t *testing.T) {
	r, _ := fakeOC(t, "hang")
	r.timeout = 300 * time.Millisecond
	var note string
	a := &secretAgent{login: r.login,
		ask:    func(...string) (map[string]any, error) { return map[string]any{"password": "secret"}, nil },
		notify: func(s, b string) error { note = b; return nil }}
	_, derr := a.GetSecrets(vpnConn(), "/c/9", "vpn", nil, nmAllowInteraction)
	if derr == nil || derr.Name != errNoSecrets {
		t.Fatalf("%v", derr)
	}
	if note != "The server did not answer in time." {
		t.Fatalf("notification %q", note)
	}
	if !gone(t, os.Getenv("FAKE_OC_PID")) {
		t.Fatal("openconnect still runs after the timeout")
	}
}

// shReadsStdin says whether /bin/sh's read waits in read(0) where
// readingStdin sees it, as openconnect's fgets does. BusyBox sh polls first.
func shReadsStdin(t *testing.T) bool {
	cmd := exec.Command("/bin/sh", "-c", "read -r x")
	in, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { in.Close(); _ = cmd.Wait() }()
	for i := 0; i < 20; i++ {
		time.Sleep(25 * time.Millisecond)
		if r, _ := readingStdin(cmd.Process.Pid); r {
			return true
		}
	}
	return false
}
