package main

// The OpenConnect login, done by the tray's secret agent.
//
// NetworkManager-openconnect's service connects with a WebVPN cookie, the
// gateway's URL and its certificate's fingerprint (the "cookie", "gateway"
// and "gwcert" secrets); it never sees a password. On other desktops its GTK
// auth dialog logs in and hands those over, but Isotope builds the plugin
// without it. So the agent logs in itself: it runs
//
//	openconnect --authenticate --non-inter --passwd-on-stdin --protocol=P --user=U -- GATEWAY
//
// as the user, with the password as the first line on stdin (never in argv),
// and returns what openconnect prints (COOKIE=, CONNECT_URL=, FINGERPRINT=,
// RESOLVE=). A server that wants a second factor (a one-time code) shows
// another form; openconnect prints its prompt's label on stderr without a
// newline and reads the answer from stdin (fgets). The agent sees a prompt
// when stderr has been quiet for a moment and either ends in an unfinished
// line (the label) or openconnect is blocked reading fd 0 (/proc/PID/syscall:
// a prompt with an empty label). It asks the user and writes the answer. One answer is
// allowed: a further prompt ends the login, and so does a prompt labelled
// "Password", which is the server asking for the password again. --non-inter
// makes openconnect refuse instead of asking about a server certificate
// that does not verify.

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// ocLogin is one login.
type ocLogin struct {
	Gateway, Protocol, User, Password string
	// Second asks the user to answer a further prompt of the server (a
	// one-time code). prompt is openconnect's label for it; ctx ends with
	// the login (a timeout, a cancel), and the dialog must close then.
	Second func(ctx context.Context, prompt string) (string, error)
}

// ocRunner runs the openconnect CLI.
type ocRunner struct {
	bin     string        // the openconnect binary
	quiet   time.Duration // stderr quiet this long, and stdin read: a prompt
	timeout time.Duration // the whole login
}

var defaultOCRunner = ocRunner{bin: "openconnect", quiet: 400 * time.Millisecond, timeout: 4 * time.Minute}

// ocProtocol is an openconnect --protocol name.
var ocProtocol = regexp.MustCompile(`^[a-z0-9-]{1,32}$`)

// ocFailed is a line of openconnect's or the server's that says the login
// did not work. It picks the line a failed login shows; it never decides
// that a login failed, since a banner can say "access is denied".
var ocFailed = regexp.MustCompile(`(?i)fail|invalid|incorrect|denied|wrong|refused|expired|locked`)

// LoginError is a login that openconnect or the server refused. Its text is
// what openconnect printed, never the password.
type LoginError struct{ Reason string }

func (e *LoginError) Error() string { return "the VPN login failed: " + e.Reason }

func (l ocLogin) check() error {
	switch {
	case l.Gateway == "" || strings.HasPrefix(l.Gateway, "-") || strings.ContainsAny(l.Gateway, " \t\r\n"):
		return fmt.Errorf("the connection has no valid gateway")
	case l.Protocol != "" && !ocProtocol.MatchString(l.Protocol):
		return fmt.Errorf("the connection's protocol %q is not valid", l.Protocol)
	case strings.ContainsAny(l.User, "\r\n"):
		return fmt.Errorf("the user name has a line break")
	case l.Password == "" || strings.ContainsAny(l.Password, "\r\n"):
		return fmt.Errorf("the password is empty or has a line break")
	}
	return nil
}

// args is openconnect's command line. The password is not in it.
func (l ocLogin) args() []string {
	a := []string{"--authenticate", "--non-inter", "--passwd-on-stdin"}
	if l.Protocol != "" {
		a = append(a, "--protocol="+l.Protocol)
	}
	if l.User != "" {
		a = append(a, "--user="+l.User)
	}
	return append(a, "--", l.Gateway)
}

// parseAuthenticate reads `openconnect --authenticate`'s KEY='value' lines.
func parseAuthenticate(out []byte) map[string]string {
	m := map[string]string{}
	sc := bufio.NewScanner(bytes.NewReader(out))
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for sc.Scan() {
		k, v, ok := strings.Cut(strings.TrimSpace(sc.Text()), "=")
		if !ok || k == "" {
			continue
		}
		if len(v) >= 2 && v[0] == '\'' && v[len(v)-1] == '\'' {
			v = v[1 : len(v)-1]
		}
		m[k] = v
	}
	return m
}

// secretsFrom is NetworkManager-openconnect's secrets from openconnect's
// output: what its GTK auth dialog returns.
func secretsFrom(out map[string]string, gateway string) (map[string]string, error) {
	s := map[string]string{"cookie": out["COOKIE"], "gwcert": out["FINGERPRINT"], "gateway": out["CONNECT_URL"]}
	if s["gateway"] == "" {
		s["gateway"] = gateway
	}
	if r := out["RESOLVE"]; r != "" {
		s["resolve"] = r
	}
	// Without either, NetworkManager asks again at once: fail instead.
	if s["cookie"] == "" || s["gwcert"] == "" {
		return nil, &LoginError{Reason: "openconnect gave no login cookie"}
	}
	return s, nil
}

// reason is the line to show for a failed login: a certificate that does
// not verify, else the last line that says it failed, else the last one,
// without openconnect's own closing line.
func reason(lines []string) string {
	// A certificate that does not verify (a private CA, a self-signed
	// server) is the likely one; its own line says why.
	for _, l := range lines {
		if why, ok := strings.CutPrefix(l, "Server certificate verify failed: "); ok {
			return "the server's certificate is not trusted (" + why + ")"
		}
	}
	var last string
	for i := len(lines) - 1; i >= 0; i-- {
		l := lines[i]
		if l == "Failed to complete authentication" {
			continue
		}
		if ocFailed.MatchString(l) {
			return l
		}
		if last == "" {
			last = l
		}
	}
	if last == "" {
		return "openconnect stopped"
	}
	return last
}

// passwordPrompt is a prompt that asks for the password again: the
// password form, shown again after a failed login. The first password field
// is answered by --passwd-on-stdin without a prompt, so its label is the
// common one, "Password"; a second factor has its own ("Second Password",
// "Verification code", "Response").
func passwordPrompt(label string) bool {
	return strings.EqualFold(strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(label), ":")), "password")
}

// readSyscalls are the read(2) and readv(2) numbers in /proc/PID/syscall:
// musl's stdio, under openconnect's fgets, reads with readv.
var readSyscalls = map[string][]string{"amd64": {"0", "19"}, "arm64": {"63", "65"}}[runtime.GOARCH]

// readingStdin says whether pid is blocked in read(0, ...) or readv(0, ...).
// ok is false when /proc cannot tell (no permission, another architecture).
func readingStdin(pid int) (reading, ok bool) {
	if readSyscalls == nil {
		return false, false
	}
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/syscall")
	if err != nil {
		return false, false
	}
	f := strings.Fields(string(b))
	if len(f) == 0 {
		return false, false
	}
	return len(f) >= 2 && slices.Contains(readSyscalls, f[0]) && f[1] == "0x0", true
}

// login logs in and returns the secrets for NetworkManager.
func (r ocRunner) login(ctx context.Context, l ocLogin) (map[string]string, error) {
	if err := l.check(); err != nil {
		return nil, err
	}
	if r.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, r.timeout)
		defer cancel()
	}
	run, kill := context.WithCancel(ctx)
	defer kill()
	cmd := exec.CommandContext(run, r.bin, l.args()...)
	// English messages, for the failure reasons.
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	// Its own process group, killed whole: nothing it starts outlives the
	// login. Wait closes the pipes at the latest a second after the kill.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = time.Second
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("running openconnect: %w", err)
	}

	// The reader stops at the pipe's end or when done closes; it never
	// blocks on a send that nobody receives.
	chunks := make(chan []byte, 16)
	done := make(chan struct{})
	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		defer close(chunks)
		buf := make([]byte, 4096)
		for {
			n, err := stderr.Read(buf)
			if n > 0 {
				select {
				case chunks <- append([]byte(nil), buf[:n]...):
				case <-done:
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()
	// stop ends the run early, reaps openconnect and the reader, and
	// returns err.
	stop := func(err error) (map[string]string, error) {
		kill()
		close(done)
		stdin.Close()
		_ = cmd.Wait() // closes stderr, so the reader returns
		<-readerDone
		return nil, err
	}
	if _, err := io.WriteString(stdin, l.Password+"\n"); err != nil {
		return stop(fmt.Errorf("giving openconnect the password: %w", err))
	}

	var all []string // stderr's lines
	partial := ""    // stderr's unfinished line: a prompt's label
	answered := 0
	// quiet fires r.quiet after the last stderr byte, and again every
	// r.quiet while stderr stays quiet.
	quiet := time.NewTimer(r.quiet)
	defer quiet.Stop()
	for open := true; open; {
		select {
		case b, ok := <-chunks:
			if !ok {
				open = false
				break
			}
			parts := strings.Split(partial+string(b), "\n")
			partial = parts[len(parts)-1]
			for _, ln := range parts[:len(parts)-1] {
				if ln = strings.TrimSpace(ln); ln != "" {
					all = append(all, ln)
				}
			}
			quiet.Reset(r.quiet)
		case <-quiet.C:
			quiet.Reset(r.quiet)
			// A label waiting on stderr is a prompt; an empty label is one
			// only when /proc shows openconnect in read(0).
			reading, _ := readingStdin(cmd.Process.Pid)
			if !reading && strings.TrimSpace(partial) == "" {
				continue
			}
			prompt := strings.TrimSpace(partial)
			partial = ""
			switch {
			case passwordPrompt(prompt):
				return stop(&LoginError{Reason: reason(append(all, prompt))})
			case answered > 0 || l.Second == nil:
				return stop(&LoginError{Reason: "the server asked again for: " + strings.TrimSuffix(prompt, ":")})
			}
			ans, err := l.Second(run, prompt)
			if err != nil {
				return stop(err)
			}
			if strings.ContainsAny(ans, "\r\n") {
				return stop(fmt.Errorf("the answer has a line break"))
			}
			if _, err := io.WriteString(stdin, ans+"\n"); err != nil {
				return stop(fmt.Errorf("giving openconnect the answer: %w", err))
			}
			answered++
			quiet.Reset(r.quiet)
		case <-run.Done():
			_, err := stop(run.Err())
			if ctx.Err() != nil {
				err = ctx.Err() // the timeout, or the caller's cancel
			}
			return nil, err
		}
	}
	close(done)
	stdin.Close()
	err = cmd.Wait()
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return nil, &LoginError{Reason: reason(all)}
		}
		return nil, err
	}
	return secretsFrom(parseAuthenticate(stdout.Bytes()), l.Gateway)
}
