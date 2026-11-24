package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"

	"github.com/godbus/dbus/v5"
)

// The page's own polkit authentication agent, registered for this process
// only (polkit's "unix-process" scope) while the Users page adds or removes
// a fingerprint: fprintd asks polkit for net.reactivated.fprint.device.enroll,
// which is the user's own password (auth_self_keep), and sway, i3 and niri
// run no polkit agent. A process agent comes before the session's own
// (COSMIC's cosmic-osd), so the question is asked the same way on every
// desktop, in this window. The password goes to polkit's setuid helper
// (polkit-agent-helper-1), which runs the polkit-1 PAM service and tells
// polkitd; this process never checks it.

const (
	polkitAgentPath = dbus.ObjectPath("/io/kairos/IsotopeSystem/PolkitAgent")
	polkitAgentIf   = "org.freedesktop.PolicyKit1.AuthenticationAgent"
	polkitCancelled = "org.freedesktop.PolicyKit1.Error.Cancelled"
	polkitFailed    = "org.freedesktop.PolicyKit1.Error.Failed"
)

// polkitHelpers are where polkit's helper may be: Isotope builds polkit with
// --libexecdir=lib.
var polkitHelpers = []string{
	"/usr/lib/polkit-1/polkit-agent-helper-1",
	"/usr/libexec/polkit-agent-helper-1",
	"/usr/lib/polkit/polkit-agent-helper-1",
}

// AuthAsk is one question of the helper, for the page: Prompt is PAM's
// prompt ("Password: "), Info what PAM said before it (the fingerprint
// module's "Place your finger on the reader", or "wrong password" after a
// failed try), Action polkit's message for the action.
type AuthAsk struct {
	Action, Prompt, Info string
	Retry                bool
}

// asker answers a question with the password, or false to cancel. It runs
// on the agent's goroutine and may block until the person answers.
type asker func(ctx context.Context, q AuthAsk) (string, bool)

// helperResult is how one run of the helper ended.
type helperResult int

const (
	helperFailure helperResult = iota
	helperSuccess
	helperCancelled
)

// runHelper drives one run of polkit's helper: it writes the cookie, then
// answers each PAM prompt through ask, until SUCCESS or FAILURE. The
// protocol is polkit's own (polkitagenthelper-pam.c): one line per message,
// "PAM_PROMPT_ECHO_OFF <prompt>", "PAM_PROMPT_ECHO_ON <prompt>",
// "PAM_ERROR_MSG <text>", "PAM_TEXT_INFO <text>", the text escaped as C.
func runHelper(ctx context.Context, helper, user, cookie, action string, retry bool, ask asker) (helperResult, error) {
	cmd := exec.CommandContext(ctx, helper, user)
	cmd.Env = []string{"PATH=/usr/bin:/bin", "LC_ALL=C.UTF-8"}
	in, err := cmd.StdinPipe()
	if err != nil {
		return helperFailure, err
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return helperFailure, err
	}
	if err := cmd.Start(); err != nil {
		return helperFailure, err
	}
	defer cmd.Wait()
	defer in.Close()
	if _, err := io.WriteString(in, cookie+"\n"); err != nil {
		return helperFailure, err
	}
	var info []string
	if retry {
		info = append(info, "That was not right. Try again.")
	}
	r := bufio.NewScanner(out)
	for r.Scan() {
		kind, text, _ := strings.Cut(r.Text(), " ")
		text = unescapeC(text)
		switch kind {
		case "PAM_PROMPT_ECHO_OFF", "PAM_PROMPT_ECHO_ON":
			answer, ok := ask(ctx, AuthAsk{Action: action, Prompt: text, Info: strings.Join(info, "\n"), Retry: retry})
			info = nil
			if !ok {
				_ = cmd.Process.Kill()
				return helperCancelled, nil
			}
			if _, err := io.WriteString(in, answer+"\n"); err != nil {
				return helperFailure, err
			}
		case "PAM_ERROR_MSG", "PAM_TEXT_INFO":
			if text != "" {
				info = append(info, text)
			}
		case "SUCCESS":
			return helperSuccess, nil
		case "FAILURE":
			return helperFailure, nil
		}
	}
	if err := r.Err(); err != nil {
		return helperFailure, err
	}
	return helperFailure, errors.New("polkit's helper stopped without an answer")
}

// unescapeC undoes g_strescape for the helper's lines.
func unescapeC(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	if u, err := strconv.Unquote(`"` + strings.ReplaceAll(s, `"`, `\"`) + `"`); err == nil {
		return u
	}
	return s
}

// polkitAgent is the exported AuthenticationAgent object.
type polkitAgent struct {
	user   string
	uid    uint32
	helper string
	ask    asker

	mu      sync.Mutex
	cancels map[string]context.CancelFunc
}

// identityIsUser says whether polkit's identity is the unix-user uid.
func identityIsUser(kind string, details map[string]dbus.Variant, uid uint32) bool {
	if kind != "unix-user" {
		return false
	}
	switch v := details["uid"].Value().(type) {
	case uint32:
		return v == uid
	case int32:
		return v >= 0 && uint32(v) == uid
	}
	return false
}

// BeginAuthentication asks for the user's own password, up to three times.
// Only the user's own identity is answered: an action that wants an
// administrator is left to the session's agent or refused.
func (a *polkitAgent) BeginAuthentication(action, message, icon string, details map[string]string, cookie string, identities []struct {
	Kind    string
	Details map[string]dbus.Variant
}) *dbus.Error {
	own := false
	for _, id := range identities {
		if identityIsUser(id.Kind, id.Details, a.uid) {
			own = true
		}
	}
	if !own {
		return dbus.NewError(polkitFailed, []any{"Isotope System answers only for its own user"})
	}
	ctx, cancel := context.WithCancel(context.Background())
	a.mu.Lock()
	a.cancels[cookie] = cancel
	a.mu.Unlock()
	defer func() {
		a.mu.Lock()
		delete(a.cancels, cookie)
		a.mu.Unlock()
		cancel()
	}()
	for try := 0; try < 3; try++ {
		res, err := runHelper(ctx, a.helper, a.user, cookie, message, try > 0, a.ask)
		switch {
		case res == helperSuccess:
			return nil
		case res == helperCancelled || ctx.Err() != nil:
			return dbus.NewError(polkitCancelled, []any{"cancelled"})
		case err != nil:
			return dbus.NewError(polkitFailed, []any{err.Error()})
		}
	}
	return dbus.NewError(polkitFailed, []any{"wrong password"})
}

func (a *polkitAgent) CancelAuthentication(cookie string) *dbus.Error {
	a.mu.Lock()
	if c := a.cancels[cookie]; c != nil {
		c()
	}
	a.mu.Unlock()
	return nil
}

// processStartTime is field 22 of /proc/self/stat, which polkit compares.
func processStartTime() (uint64, error) {
	b, err := os.ReadFile("/proc/self/stat")
	if err != nil {
		return 0, err
	}
	s := string(b)
	i := strings.LastIndexByte(s, ')')
	if i < 0 {
		return 0, errors.New("cannot read /proc/self/stat")
	}
	f := strings.Fields(s[i+1:])
	// f[0] is field 3 (state); the start time is field 22.
	if len(f) < 20 {
		return 0, errors.New("short /proc/self/stat")
	}
	return strconv.ParseUint(f[19], 10, 64)
}

// startPolkitAgent registers the agent for this process on conn and returns
// the function that unregisters it. With no helper on the system it does
// nothing: the session's agent, if any, answers.
func startPolkitAgent(conn *dbus.Conn, user string, uid uint32, ask asker) (func(), error) {
	helper := ""
	for _, h := range polkitHelpers {
		if st, err := os.Stat(h); err == nil && st.Mode().IsRegular() {
			helper = h
			break
		}
	}
	if helper == "" {
		return func() {}, errors.New("polkit's helper is not on this system")
	}
	start, err := processStartTime()
	if err != nil {
		return func() {}, err
	}
	a := &polkitAgent{user: user, uid: uid, helper: helper, ask: ask, cancels: map[string]context.CancelFunc{}}
	if err := conn.Export(a, polkitAgentPath, polkitAgentIf); err != nil {
		return func() {}, err
	}
	subject := struct {
		Kind    string
		Details map[string]dbus.Variant
	}{"unix-process", map[string]dbus.Variant{
		"pid":        dbus.MakeVariant(uint32(os.Getpid())),
		"start-time": dbus.MakeVariant(start),
		"uid":        dbus.MakeVariant(int32(uid)),
	}}
	locale := os.Getenv("LANG")
	if locale == "" {
		locale = "C.UTF-8"
	}
	auth := conn.Object(polkitName, polkitAuthority)
	if err := auth.Call(polkitAuthIf+".RegisterAuthenticationAgent", 0, subject, locale, string(polkitAgentPath)).Err; err != nil {
		conn.Export(nil, polkitAgentPath, polkitAgentIf)
		return func() {}, fmt.Errorf("register the polkit agent: %w", err)
	}
	return func() {
		auth.Call(polkitAuthIf+".UnregisterAuthenticationAgent", 0, subject, string(polkitAgentPath))
		conn.Export(nil, polkitAgentPath, polkitAgentIf)
	}, nil
}
