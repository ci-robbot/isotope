package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"golang.org/x/sys/unix"
)

// fakeIsotope is a stand-in for `isotope glibc`: it logs its arguments,
// answers status --json from $FAKE_STATUS, and plays create and reset.
const fakeIsotope = `#!/bin/sh
printf '%s\n' "$*" >> "$FAKE_LOG"
awk '{print $6}' /proc/$$/stat > "$FAKE_SID"
case "$*" in
"glibc status --json")
	[ -n "$FAKE_STATUS_EXIT" ] && { echo "isotope: broken" >&2; exit "$FAKE_STATUS_EXIT"; }
	cat "$FAKE_STATUS" ;;
"glibc auto on"|"glibc auto off")
	[ -n "$FAKE_AUTO_FAIL" ] && { echo "isotope glibc: $FAKE_AUTO_FAIL" >&2; exit 1; }
	echo "ok" ;;
"glibc create --progress")
	echo "Setting up the glibc environment, first run only: creating the container isotope-glibc" >&2
	echo "Pulling docker.io/library/ubuntu:26.04"
	if [ -n "$FAKE_FAIL" ]; then echo "$FAKE_FAIL" >&2; exit 1; fi
	echo "The glibc environment is ready." >&2 ;;
"glibc reset --yes --progress")
	if [ -n "$FAKE_INUSE" ]; then
		echo "$FAKE_INUSE" >&2
		exit 75
	fi
	if [ -n "$FAKE_FAIL" ]; then echo "$FAKE_FAIL" >&2; exit 1; fi
	echo "The glibc container isotope-glibc was created again." ;;
*) echo "unexpected: $*" >&2; exit 2 ;;
esac
`

type isotopeFake struct {
	dir, log, status, sid string
}

func newIsotopeFake(t *testing.T) (glibcCLI, *isotopeFake) {
	t.Helper()
	f := &isotopeFake{dir: t.TempDir()}
	f.log, f.status, f.sid = filepath.Join(f.dir, "log"), filepath.Join(f.dir, "status.json"), filepath.Join(f.dir, "sid")
	bin := filepath.Join(f.dir, "isotope")
	if err := os.WriteFile(bin, []byte(fakeIsotope), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FAKE_LOG", f.log)
	t.Setenv("FAKE_STATUS", f.status)
	t.Setenv("FAKE_SID", f.sid)
	t.Setenv("XDG_RUNTIME_DIR", f.dir)
	return glibcCLI{bin: bin}, f
}

func (f *isotopeFake) setStatus(t *testing.T, json string) {
	t.Helper()
	if err := os.WriteFile(f.status, []byte(json), 0o644); err != nil {
		t.Fatal(err)
	}
}

func (f *isotopeFake) calls(t *testing.T) []string {
	t.Helper()
	b, _ := os.ReadFile(f.log)
	return strings.Split(strings.TrimSpace(string(b)), "\n")
}

func TestGlibcCLIStatus(t *testing.T) {
	c, f := newIsotopeFake(t)
	f.setStatus(t, `{"exists":false,"image":"docker.io/library/ubuntu:26.04","size_bytes":0,"auto":true,"error":""}`)
	st, err := c.Status()
	if err != nil || st.Exists || st.Image != "docker.io/library/ubuntu:26.04" || !st.Auto {
		t.Fatalf("absent: %+v %v", st, err)
	}
	f.setStatus(t, `{"exists":true,"image":"docker.io/library/ubuntu:26.04","size_bytes":812345678,"auto":false,"error":""}`)
	st, err = c.Status()
	if err != nil || !st.Exists || st.SizeBytes != 812345678 || st.Auto {
		t.Fatalf("ready: %+v %v", st, err)
	}
	f.setStatus(t, `{"exists":false,"image":"docker.io/library/ubuntu:26.04","size_bytes":0,"auto":true,"error":"Docker did not answer about the glibc container: Cannot connect"}`)
	st, err = c.Status()
	if err != nil || !strings.Contains(st.Error, "Cannot connect") {
		t.Fatalf("error: %+v %v", st, err)
	}
	if got := f.calls(t); got[0] != "glibc status --json" {
		t.Fatalf("calls %q", got)
	}
	// An isotope that cannot answer is an error, with what it said.
	f.setStatus(t, "not json")
	if _, err := c.Status(); err == nil {
		t.Fatal("garbage accepted")
	}
	t.Setenv("FAKE_STATUS_EXIT", "1")
	if _, err := c.Status(); err == nil || !strings.Contains(err.Error(), "broken") {
		t.Fatalf("exit 1: %v", err)
	}
	if _, err := (glibcCLI{bin: filepath.Join(f.dir, "missing")}).Status(); err == nil {
		t.Fatal("a missing isotope is fine")
	}
}

func TestGlibcCLISetAuto(t *testing.T) {
	c, f := newIsotopeFake(t)
	if err := c.SetAuto(false); err != nil {
		t.Fatal(err)
	}
	if err := c.SetAuto(true); err != nil {
		t.Fatal(err)
	}
	if got := f.calls(t); len(got) != 2 || got[0] != "glibc auto off" || got[1] != "glibc auto on" {
		t.Fatalf("calls %q", got)
	}
	t.Setenv("FAKE_AUTO_FAIL", "permission denied")
	if err := c.SetAuto(false); err == nil || !strings.Contains(err.Error(), "permission denied") {
		t.Fatalf("failure: %v", err)
	}
}

func TestGlibcCLICreate(t *testing.T) {
	c, f := newIsotopeFake(t)
	var mu sync.Mutex
	var lines []string
	line := func(l string) { mu.Lock(); lines = append(lines, l); mu.Unlock() }
	if err := c.Create(line); err != nil {
		t.Fatal(err)
	}
	if got := f.calls(t); got[0] != "glibc create --progress" {
		t.Fatalf("calls %q", got)
	}
	if strings.Join(lines, "|") != "Setting up the glibc environment, first run only: creating the container isotope-glibc|Pulling docker.io/library/ubuntu:26.04|The glibc environment is ready." {
		t.Fatalf("lines %q", lines)
	}
	// It runs in its own session, so closing the window (or its terminal)
	// does not stop it half way.
	sid, _ := os.ReadFile(f.sid)
	mine, _ := unix.Getsid(0)
	if s := strings.TrimSpace(string(sid)); s == "" || s == strconv.Itoa(mine) {
		t.Fatalf("session %q, the window's is %d", s, mine)
	}
	// Its log file is gone.
	if left, _ := filepath.Glob(filepath.Join(f.dir, "isotope-glibc-*")); len(left) != 0 {
		t.Fatalf("left %q", left)
	}
	// A failure is the CLI's last line.
	t.Setenv("FAKE_FAIL", "The glibc container could not be created: the first run downloads docker.io/library/ubuntu:26.04, which needs the network. Connect to the network, then try again.")
	err := c.Create(func(string) {})
	if err == nil || !strings.HasPrefix(err.Error(), "The glibc container could not be created: the first run downloads") {
		t.Fatalf("failure: %v", err)
	}
}

func TestGlibcCLIReset(t *testing.T) {
	c, f := newIsotopeFake(t)
	if err := c.Reset(func(string) {}); err != nil {
		t.Fatal(err)
	}
	if got := f.calls(t); got[0] != "glibc reset --yes --progress" {
		t.Fatalf("calls %q", got)
	}
	// The page branches on the exit code (75), not on the words.
	t.Setenv("FAKE_INUSE", "Busy, try later.")
	err := c.Reset(func(string) {})
	var inUse glibcInUse
	if !errors.As(err, &inUse) || err.Error() != "Busy, try later." {
		t.Fatalf("in use: %#v", err)
	}
	os.Unsetenv("FAKE_INUSE")
	t.Setenv("FAKE_FAIL", "The glibc container is in use, says another failure.")
	if err := c.Reset(func(string) {}); err == nil || errors.As(err, &inUse) {
		t.Fatalf("exit 1: %#v", err)
	}
}

// realIsotope builds cmd/isotope into a temp dir, once per test binary.
var realIsotope = sync.OnceValues(func() (string, error) {
	gobin, err := exec.LookPath("go")
	if err != nil {
		return "", errSkip("no go on PATH to build cmd/isotope")
	}
	dir, err := os.MkdirTemp("", "isotope-cli-")
	if err != nil {
		return "", err
	}
	bin := filepath.Join(dir, "isotope")
	build := exec.Command(gobin, "build", "-o", bin, "../isotope")
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := build.CombinedOutput(); err != nil {
		return "", fmt.Errorf("go build ../isotope: %v\n%s", err, out)
	}
	return bin, nil
})

type errSkip string

func (e errSkip) Error() string { return string(e) }

func buildIsotope(t *testing.T) string {
	t.Helper()
	// The image's system-build stage copies only what isotope-system
	// imports (scripts/check-go-stage-copies.sh sees this skip); CI has the
	// whole tree.
	if _, err := os.Stat("../isotope/glibc.go"); err != nil {
		t.Skip("cmd/isotope is not here")
	}
	bin, err := realIsotope()
	var skip errSkip
	if errors.As(err, &skip) {
		t.Skip(string(skip))
	}
	if err != nil {
		t.Fatal(err)
	}
	return bin
}

// The real `isotope glibc reset` refuses a container in use (its lock held
// shared, as by a running program) with the exit code the page maps to the
// in-use message.
func TestGlibcResetInUseThroughTheRealCLI(t *testing.T) {
	c := glibcCLI{bin: buildIsotope(t)}
	dir := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", dir)
	t.Setenv("PATH", filepath.Join(dir, "nothing"))
	if err := os.MkdirAll(filepath.Join(dir, "isotope"), 0o700); err != nil {
		t.Fatal(err)
	}
	lock, err := os.OpenFile(filepath.Join(dir, "isotope", "glibc.lock"), os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if err := unix.Flock(int(lock.Fd()), unix.LOCK_SH); err != nil {
		t.Fatal(err)
	}
	var inUse glibcInUse
	if err := c.Reset(func(string) {}); !errors.As(err, &inUse) || inUse.msg == "" {
		t.Fatalf("reset under a running program: %#v", err)
	}
	// Without it, the answer is some other refusal (no Docker here), not
	// in use.
	_ = unix.Flock(int(lock.Fd()), unix.LOCK_UN)
	if err := c.Reset(func(string) {}); err == nil || errors.As(err, &inUse) {
		t.Fatalf("reset without a running program: %#v", err)
	}
}

// The switch writes the setting through the real `isotope glibc auto`, and
// the page reads it back through the real `status --json`: one
// implementation of the file. A missing file reads as on.
func TestGlibcAutoRoundTripThroughTheRealCLI(t *testing.T) {
	bin := buildIsotope(t)
	dir := t.TempDir()
	cfg := filepath.Join(dir, "config")
	t.Setenv("XDG_CONFIG_HOME", cfg)
	t.Setenv("ISOTOPE_GLIBC_AUTO", "")
	t.Setenv("PATH", filepath.Join(dir, "nothing")) // no docker: status answers with an error, and the setting
	c := glibcCLI{bin: bin}
	file := filepath.Join(cfg, "isotope", "glibc")

	if st, err := c.Status(); err != nil || !st.Auto {
		t.Fatalf("no file: %+v %v", st, err)
	}
	if err := c.SetAuto(false); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(file); string(b) != "auto=off\n" {
		t.Fatalf("file %q", b)
	}
	if st, err := c.Status(); err != nil || st.Auto || st.Error == "" {
		t.Fatalf("off: %+v %v", st, err)
	}
	if err := c.SetAuto(true); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(file); string(b) != "auto=on\n" {
		t.Fatalf("file %q", b)
	}
	if st, _ := c.Status(); !st.Auto {
		t.Fatalf("on: %+v", st)
	}
}
