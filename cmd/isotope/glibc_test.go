package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// The test binary doubles as the "program built for glibc": when this
// variable names a file, it writes its argv and the loop guard there and
// exits, before the testing package parses any flag.
const glibcHelperEnv = "ISOTOPE_GLIBC_TEST_HELPER_OUT"

func init() {
	if out := os.Getenv(glibcHelperEnv); out != "" {
		b, _ := json.Marshal(map[string]any{
			"argv":  os.Args,
			"guard": os.Getenv(glibcGuardEnv),
			"argv0": os.Getenv(glibcArgv0Env),
		})
		_ = os.WriteFile(out, b, 0o644)
		os.Exit(7)
	}
}

// glibcFake is a test system: fake distrobox and docker on PATH, a temp
// HOME and XDG dirs, a docker "socket", a group file that has the user in
// docker, and notifications recorded instead of sent.
type glibcFake struct {
	dir, home, log, state string
	mu                    sync.Mutex
	notes                 []string
	stderr                *syncBuffer
}

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

const fakeDistrobox = `#!/bin/sh
l=distrobox; for a in "$@"; do l="$l $a"; done; printf '%s\n' "$l" >> "$GLIBC_FAKE_LOG" # one write, so concurrent calls do not interleave
echo "$1 DBX_NON_INTERACTIVE=${DBX_NON_INTERACTIVE-unset} DBX_CONTAINER_MANAGER=${DBX_CONTAINER_MANAGER-unset} DBX_CONTAINER_IMAGE=${DBX_CONTAINER_IMAGE-unset}" >> "$GLIBC_FAKE_LOG.env"
case "$1" in
create)
	if [ -n "$GLIBC_FAKE_CREATE_SLEEP" ]; then sleep "$GLIBC_FAKE_CREATE_SLEEP"; fi
	touch "$GLIBC_FAKE_STATE"
	if [ -n "$GLIBC_FAKE_CREATE_FAIL" ]; then echo "$GLIBC_FAKE_CREATE_FAIL" >&2; exit 1; fi
	;;
enter)
	shift
	[ "$1" = isotope-glibc ] || { echo "Cannot find container $1" >&2; exit 1; }
	shift
	[ "$1" = -- ] && shift
	[ $# -eq 0 ] && exit 0
	if [ "$*" = true ] && [ -n "$GLIBC_FAKE_WARMUP_SLEEP" ]; then
		# distrobox's first-start setup
		sleep "$GLIBC_FAKE_WARMUP_SLEEP"
		echo warmup-done >> "$GLIBC_FAKE_LOG"
	fi
	# The box sees the host's / under /run/host; this fake runs on the host.
	for a in "$@"; do shift; case "$a" in /run/host/*) a="${a#/run/host}" ;; esac; set -- "$@" "$a"; done
	exec "$@"
	;;
esac
`

const fakeDocker = `#!/bin/sh
l=docker; for a in "$@"; do l="$l $a"; done; printf '%s\n' "$l" >> "$GLIBC_FAKE_LOG" # one write, so concurrent calls do not interleave
case "$1" in
inspect)
	if [ -n "$GLIBC_FAKE_DOCKER_DOWN" ]; then echo "Cannot connect to the Docker daemon at unix:///run/docker.sock. Is the docker daemon running?" >&2; exit 1; fi
	if [ -e "$GLIBC_FAKE_STATE" ]; then
		case "$*" in *SizeRootFs*) echo "docker.io/library/ubuntu:26.04 812345678" ;; *) echo "docker.io/library/ubuntu:26.04" ;; esac
		exit 0
	fi
	echo "Error: No such container: isotope-glibc" >&2; exit 1
	;;
image)
	if [ -n "$GLIBC_FAKE_DOCKER_DOWN" ]; then exit 1; fi
	if [ -e "$GLIBC_FAKE_STATE.image" ]; then echo 78123456; exit 0; fi
	echo "Error: No such image: $5" >&2; exit 1
	;;
pull)
	echo "26.04: Pulling from library/ubuntu"
	if [ -n "$GLIBC_FAKE_PULL_FAIL" ]; then echo "$GLIBC_FAKE_PULL_FAIL" >&2; exit 1; fi
	touch "$GLIBC_FAKE_STATE.image"
	;;
rm) rm -f "$GLIBC_FAKE_STATE" ;;
ps) echo "1.2GB (virtual 1.3GB)" ;;
esac
`

func newGlibcFake(t *testing.T) *glibcFake {
	t.Helper()
	f := &glibcFake{dir: t.TempDir(), stderr: &syncBuffer{}}
	f.home = filepath.Join(f.dir, "home", "tester")
	f.log = filepath.Join(f.dir, "log")
	f.state = filepath.Join(f.dir, "box-exists")
	bin := filepath.Join(f.dir, "bin")
	for _, d := range []string{f.home, bin, filepath.Join(f.dir, "run"), filepath.Join(f.dir, "runtime")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for name, body := range map[string]string{"distrobox": fakeDistrobox, "docker": fakeDocker} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write(t, filepath.Join(f.dir, "run", "docker.sock"), "")
	write(t, filepath.Join(f.dir, "group"), "root:x:0:\nwheel:x:10:tester\ndocker:x:999:tester,other\n")
	t.Setenv("PATH", bin+":/usr/bin:/bin")
	t.Setenv("HOME", f.home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(f.home, ".config"))
	t.Setenv("XDG_RUNTIME_DIR", filepath.Join(f.dir, "runtime"))
	t.Setenv("GLIBC_FAKE_LOG", f.log)
	t.Setenv("GLIBC_FAKE_STATE", f.state)
	t.Setenv(glibcArgv0Env, "")
	os.Unsetenv(glibcArgv0Env)
	t.Setenv(glibcGuardEnv, "")
	os.Unsetenv(glibcGuardEnv)
	t.Setenv("ISOTOPE_GLIBC_AUTO", "")
	os.Unsetenv("ISOTOPE_GLIBC_AUTO")

	old := glibc
	t.Cleanup(func() { glibc = old })
	glibc = glibcSys{
		euid:       func() int { return 1000 },
		uid:        func() int { return 1000 },
		egid:       func() int { return 1000 },
		gid:        func() int { return 1000 },
		userName:   func() string { return "tester" },
		getgroups:  func() ([]int, error) { return []int{10, 999}, nil },
		groupFile:  filepath.Join(f.dir, "group"),
		liveMarker: filepath.Join(f.dir, "run", "live-desktop"),
		dockerSock: filepath.Join(f.dir, "run", "docker.sock"),
		confFile:   filepath.Join(f.dir, "glibc.conf"),
		sockWait:   50 * time.Millisecond,
		poll:       5 * time.Millisecond,
		isTerminal: func() bool { return true },
		getppid:    os.Getppid,
		notify: func(summary, body string) error {
			f.mu.Lock()
			defer f.mu.Unlock()
			f.notes = append(f.notes, summary+": "+body)
			return nil
		},
		exec: func(path string, argv, env []string) (int, error) {
			c := exec.Command(path, argv[1:]...)
			c.Env = env
			c.Stdout, c.Stderr = f.stderr, f.stderr
			if err := c.Run(); err != nil {
				var ee *exec.ExitError
				if errors.As(err, &ee) {
					return ee.ExitCode(), nil
				}
				return 0, err
			}
			return 0, nil
		},
		stdin:  strings.NewReader(""),
		stdout: f.stderr,
		stderr: f.stderr,
	}
	return f
}

func (f *glibcFake) calls(t *testing.T) []string {
	t.Helper()
	b, err := os.ReadFile(f.log)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimRight(string(b), "\n"), "\n")
}

func (f *glibcFake) count(t *testing.T, prefix string) int {
	n := 0
	for _, c := range f.calls(t) {
		if strings.HasPrefix(c, prefix) {
			n++
		}
	}
	return n
}

// helperProgram puts the test binary under $HOME as a program and returns
// its path and the file it will write its argv to.
func (f *glibcFake) helperProgram(t *testing.T) (prog, out string) {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	prog = filepath.Join(f.home, "Downloads", "tool")
	if err := os.MkdirAll(filepath.Dir(prog), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(self, prog); err != nil {
		t.Fatal(err)
	}
	out = filepath.Join(f.dir, "helper.json")
	t.Setenv(glibcHelperEnv, out)
	return prog, out
}

type helperResult struct {
	Argv  []string `json:"argv"`
	Guard string   `json:"guard"`
	Argv0 string   `json:"argv0"`
}

func readHelper(t *testing.T, out string) helperResult {
	t.Helper()
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("the program did not run: %v", err)
	}
	var r helperResult
	if err := json.Unmarshal(b, &r); err != nil {
		t.Fatal(err)
	}
	return r
}

func TestRunRefuses(t *testing.T) {
	cases := []struct {
		name, want string
		set        func(f *glibcFake)
	}{
		{"root", "root user", func(f *glibcFake) { glibc.euid, glibc.uid = func() int { return 0 }, func() int { return 0 } }},
		{"setuid", "setuid and setgid programs are not handed", func(f *glibcFake) { glibc.euid = func() int { return 2000 } }},
		{"setuid root", "setuid and setgid programs are not handed", func(f *glibcFake) { glibc.euid = func() int { return 0 }; glibc.uid = func() int { return 1000 } }},
		{"setgid", "setuid and setgid programs are not handed", func(f *glibcFake) { glibc.egid = func() int { return 2000 } }},
		{"live", "live system", func(f *glibcFake) { write(t, glibc.liveMarker, "") }},
		{"no docker socket", "Docker is not running", func(f *glibcFake) { os.Remove(glibc.dockerSock) }},
		{"not in docker in this session", "Log out and back in", func(f *glibcFake) {
			glibc.getgroups = func() ([]int, error) { return []int{10}, nil }
		}},
		{"not in docker at all", "not in the docker group", func(f *glibcFake) {
			glibc.getgroups = func() ([]int, error) { return []int{10}, nil }
			write(t, glibc.groupFile, "docker:x:999:other\n")
		}},
		{"no distrobox", "Distrobox is not installed", func(f *glibcFake) {
			os.Remove(filepath.Join(f.dir, "bin", "distrobox"))
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newGlibcFake(t)
			c.set(f)
			start := time.Now()
			code := glibcRun(runOpts{Loader: true}, "/opt/thing/prog", []string{"a"})
			if code != 127 {
				t.Errorf("exit %d, want 127", code)
			}
			msg := f.stderr.String()
			if !strings.Contains(msg, c.want) || !strings.Contains(msg, "/opt/thing/prog is a program built for glibc") ||
				!strings.Contains(msg, "isotope glibc --help") {
				t.Errorf("message %q lacks %q", msg, c.want)
			}
			if n := f.count(t, "distrobox"); n != 0 {
				t.Errorf("distrobox ran %d times: %v", n, f.calls(t))
			}
			if c.name == "no docker socket" && time.Since(start) < glibc.sockWait {
				t.Errorf("gave up before the wait of %v", glibc.sockWait)
			}
		})
	}
}

func TestRunWaitsForTheDockerSocket(t *testing.T) {
	f := newGlibcFake(t)
	sock := glibc.dockerSock
	os.Remove(sock)
	glibc.sockWait = 5 * time.Second
	go func() {
		time.Sleep(30 * time.Millisecond)
		_ = os.WriteFile(sock, nil, 0o644)
	}()
	write(t, f.state, "")
	if code := glibcRun(runOpts{}, "/bin/true", nil); code != 0 {
		t.Fatalf("exit %d: %s", code, f.stderr.String())
	}
}

func TestCreateThenRun(t *testing.T) {
	f := newGlibcFake(t)
	prog, _ := f.helperProgram(t)
	code := glibcRun(runOpts{Loader: true}, prog, []string{"x", "y"})
	if code != 7 {
		t.Fatalf("exit %d, want the program's 7: %s", code, f.stderr.String())
	}
	want := []string{
		"docker inspect --type container isotope-glibc",
		"docker inspect --type container isotope-glibc",
		"distrobox create --yes --name isotope-glibc --image docker.io/library/ubuntu:26.04",
		"distrobox enter isotope-glibc -- true",
		"docker inspect --type container isotope-glibc",
		"distrobox enter isotope-glibc -- bash -c exec -a \"$0\" \"$@\" tool " + prog + " x y",
	}
	if got := f.calls(t); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("calls:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	// The second run finds the box and only enters it.
	os.Remove(f.log)
	if code := glibcRun(runOpts{}, prog, nil); code != 7 {
		t.Fatalf("second run: exit %d", code)
	}
	if n := f.count(t, "distrobox create"); n != 0 {
		t.Fatalf("second run created the box again: %v", f.calls(t))
	}
}

func TestRunArgvRoundTrip(t *testing.T) {
	f := newGlibcFake(t)
	write(t, f.state, "")
	prog, out := f.helperProgram(t)
	args := []string{"a", "b c", "d\ne", "-x", "--", `q"u'o\te$HOME`, "", "*"}
	t.Setenv(glibcArgv0Env, "my tool")
	if code := glibcRun(runOpts{Loader: true}, prog, args); code != 7 {
		t.Fatalf("exit %d: %s", code, f.stderr.String())
	}
	r := readHelper(t, out)
	if len(r.Argv) != len(args)+1 || r.Argv[0] != "my tool" {
		t.Fatalf("argv %q", r.Argv)
	}
	for i, a := range args {
		if r.Argv[i+1] != a {
			t.Errorf("arg %d = %q, want %q", i+1, r.Argv[i+1], a)
		}
	}
	if r.Argv0 != "" {
		t.Errorf("ISOTOPE_GLIBC_ARGV0 leaked into the program: %q", r.Argv0)
	}

	// Without ISOTOPE_GLIBC_ARGV0, argv[0] is the program's base name.
	os.Unsetenv(glibcArgv0Env)
	if code := glibcRun(runOpts{}, prog, nil); code != 7 {
		t.Fatalf("exit %d", code)
	}
	if r := readHelper(t, out); len(r.Argv) != 1 || r.Argv[0] != "tool" {
		t.Fatalf("argv %q, want [tool]", r.Argv)
	}
}

func TestRunSetsLoaderGuard(t *testing.T) {
	f := newGlibcFake(t)
	write(t, f.state, "")
	prog, out := f.helperProgram(t)
	if code := glibcRun(runOpts{Loader: true}, prog, nil); code != 7 {
		t.Fatalf("exit %d: %s", code, f.stderr.String())
	}
	if r := readHelper(t, out); r.Guard != "1" {
		t.Fatalf("%s = %q in the box, want 1", glibcGuardEnv, r.Guard)
	}
}

func TestPathMapping(t *testing.T) {
	home := "/home/tester"
	for in, want := range map[string]string{
		"/home/tester":                  "/home/tester",
		"/home/tester/bin/tool":         "/home/tester/bin/tool",
		"/home/tester2/tool":            "/run/host/home/tester2/tool",
		"/tmp/x/tool":                   "/tmp/x/tool",
		"/mnt/usb/tool":                 "/mnt/usb/tool",
		"/media/usb/tool":               "/media/usb/tool",
		"/run/media/tester/USB/tool":    "/run/media/tester/USB/tool",
		"/opt/thing/tool":               "/run/host/opt/thing/tool",
		"/usr/local/bin/tool":           "/run/host/usr/local/bin/tool",
		"/run/user/1000/tool":           "/run/host/run/user/1000/tool",
		"/tmpfoo/tool":                  "/run/host/tmpfoo/tool",
		"/home/tester/../other/tool":    "/run/host/home/other/tool",
		"/run/host/opt/already-mapped":  "/run/host/run/host/opt/already-mapped",
		"/media":                        "/media",
		"/home/tester/with space/t ool": "/home/tester/with space/t ool",
	} {
		if got := boxPath(home, in); got != want {
			t.Errorf("boxPath(%q) = %q, want %q", in, got, want)
		}
	}
	// A bare command name is looked up in the box's PATH: left as it is.
	if got := boxPath(home, "node"); got != "node" {
		t.Errorf("boxPath(node) = %q", got)
	}
}

func TestRunMakesARelativePathAbsolute(t *testing.T) {
	f := newGlibcFake(t)
	write(t, f.state, "")
	prog, out := f.helperProgram(t)
	wd, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(wd) })
	if err := os.Chdir(filepath.Dir(prog)); err != nil {
		t.Fatal(err)
	}
	if code := glibcRun(runOpts{}, "./tool", nil); code != 7 {
		t.Fatalf("exit %d: %s", code, f.stderr.String())
	}
	readHelper(t, out)
	if c := f.calls(t); !strings.HasSuffix(c[len(c)-1], " tool "+prog) {
		t.Fatalf("enter call %q does not name %s", c[len(c)-1], prog)
	}
}

func TestCreateIsLocked(t *testing.T) {
	f := newGlibcFake(t)
	t.Setenv("GLIBC_FAKE_CREATE_SLEEP", "0.3")
	var wg sync.WaitGroup
	codes := make([]int, 2)
	for i := range codes {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			codes[i] = glibcRun(runOpts{}, "/bin/true", nil)
		}(i)
	}
	wg.Wait()
	for i, c := range codes {
		if c != 0 {
			t.Errorf("run %d: exit %d", i, c)
		}
	}
	if n := f.count(t, "distrobox create"); n != 1 {
		t.Fatalf("%d creates, want 1: %v", n, f.calls(t))
	}
	if n := f.count(t, "distrobox enter isotope-glibc -- bash"); n != 2 {
		t.Fatalf("%d runs, want 2: %v", n, f.calls(t))
	}
	if _, err := os.Stat(filepath.Join(f.dir, "runtime", "isotope", "glibc.lock")); err != nil {
		t.Fatalf("lock file: %v", err)
	}
}

func TestFailedCreateCleansUp(t *testing.T) {
	f := newGlibcFake(t)
	t.Setenv("GLIBC_FAKE_CREATE_FAIL", "Error: something broke")
	code := glibcRun(runOpts{}, "/bin/true", nil)
	if code == 0 {
		t.Fatal("a failed create ran the program")
	}
	if n := f.count(t, "docker rm --force isotope-glibc"); n != 1 {
		t.Fatalf("half-made box not removed: %v", f.calls(t))
	}
	if _, err := os.Stat(f.state); !os.IsNotExist(err) {
		t.Fatal("the half-made box is still there")
	}
	if msg := f.stderr.String(); !strings.Contains(msg, "could not be created") || !strings.Contains(msg, "something broke") {
		t.Fatalf("message %q", msg)
	}
	if n := f.count(t, "distrobox enter isotope-glibc -- bash"); n != 0 {
		t.Fatalf("ran the program after a failed create: %v", f.calls(t))
	}

	// The next run tries again.
	os.Unsetenv("GLIBC_FAKE_CREATE_FAIL")
	os.Remove(f.log)
	if code := glibcRun(runOpts{}, "/bin/true", nil); code != 0 {
		t.Fatalf("retry: exit %d: %s", code, f.stderr.String())
	}
	if n := f.count(t, "distrobox create"); n != 1 {
		t.Fatalf("retry did not create: %v", f.calls(t))
	}
}

func TestOfflineCreateSaysTheNetwork(t *testing.T) {
	f := newGlibcFake(t)
	t.Setenv("GLIBC_FAKE_CREATE_FAIL", "Error response from daemon: Get \"https://registry-1.docker.io/v2/\": dial tcp: lookup registry-1.docker.io: no such host")
	if code := glibcRun(runOpts{}, "/bin/true", nil); code == 0 {
		t.Fatal("ran")
	}
	if msg := f.stderr.String(); !strings.Contains(msg, "needs the network") {
		t.Fatalf("message %q", msg)
	}
}

func TestPrunedBoxIsRecreated(t *testing.T) {
	f := newGlibcFake(t)
	glibc.isTerminal = func() bool { return false }
	write(t, f.state, "")
	if code := glibcRun(runOpts{}, "/bin/true", nil); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if len(f.notes) != 0 || f.count(t, "distrobox create") != 0 {
		t.Fatalf("an existing box was set up again: %v %v", f.notes, f.calls(t))
	}
	os.Remove(f.state) // docker container prune
	if code := glibcRun(runOpts{}, "/bin/true", nil); code != 0 {
		t.Fatalf("exit %d: %s", code, f.stderr.String())
	}
	if f.count(t, "distrobox create") != 1 {
		t.Fatalf("not recreated: %v", f.calls(t))
	}
	if len(f.notes) != 2 || !strings.Contains(f.notes[0], "first run only") {
		t.Fatalf("notes %q", f.notes)
	}
}

func TestNoTerminalNotifies(t *testing.T) {
	f := newGlibcFake(t)
	glibc.isTerminal = func() bool { return false }
	if code := glibcRun(runOpts{Loader: true}, "/bin/true", nil); code != 0 {
		t.Fatalf("exit %d: %s", code, f.stderr.String())
	}
	if len(f.notes) != 2 || !strings.HasPrefix(f.notes[0], "Setting up the glibc environment") ||
		!strings.Contains(f.notes[1], "ready") {
		t.Fatalf("notes %q", f.notes)
	}

	// A failure says what failed.
	f = newGlibcFake(t)
	glibc.isTerminal = func() bool { return false }
	t.Setenv("GLIBC_FAKE_CREATE_FAIL", "Error: something broke")
	glibcRun(runOpts{Loader: true}, "/bin/true", nil)
	if len(f.notes) != 2 || !strings.Contains(f.notes[1], "could not be created") || !strings.Contains(f.notes[1], "something broke") {
		t.Fatalf("notes %q", f.notes)
	}

	// So does a refusal, which a double-click would otherwise never show.
	os.Unsetenv("GLIBC_FAKE_CREATE_FAIL")
	f = newGlibcFake(t)
	glibc.isTerminal = func() bool { return false }
	glibc.euid, glibc.uid = func() int { return 0 }, func() int { return 0 }
	glibcRun(runOpts{Loader: true}, "/opt/x/prog", nil)
	if len(f.notes) != 1 || !strings.Contains(f.notes[0], "root user") {
		t.Fatalf("notes %q", f.notes)
	}

	// On a terminal it prints and does not notify.
	f = newGlibcFake(t)
	if code := glibcRun(runOpts{}, "/bin/true", nil); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if len(f.notes) != 0 || !strings.Contains(f.stderr.String(), "first run only") {
		t.Fatalf("notes %q, stderr %q", f.notes, f.stderr.String())
	}
}

func TestImageFromConf(t *testing.T) {
	f := newGlibcFake(t)
	write(t, glibc.confFile, "# the box image\nimage = docker.io/library/ubuntu:24.04\n")
	if code := glibcRun(runOpts{}, "/bin/true", nil); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if f.count(t, "distrobox create --yes --name isotope-glibc --image docker.io/library/ubuntu:24.04") != 1 {
		t.Fatalf("conf image not used: %v", f.calls(t))
	}

	f = newGlibcFake(t) // no conf file
	if code := glibcRun(runOpts{}, "/bin/true", nil); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if f.count(t, "distrobox create --yes --name isotope-glibc --image docker.io/library/ubuntu:26.04") != 1 {
		t.Fatalf("default image not used: %v", f.calls(t))
	}
}

func TestAutoOff(t *testing.T) {
	f := newGlibcFake(t)
	write(t, f.state, "")
	if code := cmdGlibc([]string{"auto", "off"}, strings.NewReader(""), f.stderr, f.stderr); code != 0 {
		t.Fatalf("auto off: exit %d", code)
	}
	b, err := os.ReadFile(filepath.Join(f.home, ".config", "isotope", "glibc"))
	if err != nil || strings.TrimSpace(string(b)) != "auto=off" {
		t.Fatalf("config %q, %v", b, err)
	}
	// The loader's hand-off is refused, an explicit run is not.
	if code := glibcRun(runOpts{Loader: true}, "/opt/x/prog", nil); code != 127 {
		t.Fatalf("loader run with auto off: exit %d", code)
	}
	if !strings.Contains(f.stderr.String(), "turned off") {
		t.Fatalf("message %q", f.stderr.String())
	}
	if code := glibcRun(runOpts{}, "/bin/true", nil); code != 0 {
		t.Fatalf("explicit run with auto off: exit %d", code)
	}
	if code := cmdGlibc([]string{"auto", "on"}, strings.NewReader(""), f.stderr, f.stderr); code != 0 {
		t.Fatal("auto on")
	}
	if code := glibcRun(runOpts{Loader: true}, "/bin/true", nil); code != 0 {
		t.Fatalf("loader run with auto on: exit %d", code)
	}
	t.Setenv("ISOTOPE_GLIBC_AUTO", "0")
	if code := glibcRun(runOpts{Loader: true}, "/bin/true", nil); code != 127 {
		t.Fatalf("ISOTOPE_GLIBC_AUTO=0: exit %d", code)
	}
}

func TestGlibcCLI(t *testing.T) {
	f := newGlibcFake(t)
	var out bytes.Buffer
	if code := cmdGlibc([]string{"status"}, strings.NewReader(""), &out, &out); code != 0 {
		t.Fatalf("status: exit %d: %s", code, out.String())
	}
	if !strings.Contains(out.String(), "not created yet") || !strings.Contains(out.String(), "ubuntu:26.04") {
		t.Fatalf("status %q", out.String())
	}

	if code := cmdGlibc([]string{"create"}, strings.NewReader(""), &out, &out); code != 0 {
		t.Fatalf("create: exit %d: %s", code, f.stderr.String())
	}
	out.Reset()
	cmdGlibc([]string{"status"}, strings.NewReader(""), &out, &out)
	if !strings.Contains(out.String(), "created") || !strings.Contains(out.String(), "1.2GB") {
		t.Fatalf("status %q", out.String())
	}

	// reset asks on a terminal; "n" keeps the box.
	os.Remove(f.log)
	if code := cmdGlibc([]string{"reset"}, strings.NewReader("n\n"), &out, &out); code == 0 {
		t.Fatal("reset without a yes succeeded")
	}
	if f.count(t, "docker rm") != 0 {
		t.Fatalf("removed without a yes: %v", f.calls(t))
	}
	if code := cmdGlibc([]string{"reset"}, strings.NewReader("y\n"), &out, &out); code != 0 {
		t.Fatalf("reset: exit %d: %s", code, out.String())
	}
	if f.count(t, "docker rm --force isotope-glibc") != 1 || f.count(t, "distrobox create") != 1 {
		t.Fatalf("reset calls %v", f.calls(t))
	}

	os.Remove(f.log)
	if code := cmdGlibc([]string{"enter"}, strings.NewReader(""), &out, &out); code != 0 {
		t.Fatalf("enter: exit %d", code)
	}
	if c := f.calls(t); c[len(c)-1] != "distrobox enter isotope-glibc" {
		t.Fatalf("enter calls %v", c)
	}

	if code := cmdGlibc([]string{"run"}, strings.NewReader(""), &out, &out); code != exitUsage {
		t.Errorf("run without FILE: exit %d", code)
	}
	out.Reset()
	if code := cmdGlibc([]string{"--help"}, strings.NewReader(""), &out, &out); code != 0 || !strings.Contains(out.String(), "isotope glibc run") {
		t.Errorf("help: %d %q", code, out.String())
	}
	_, mainOut, _ := runCLI("", "help")
	if !strings.Contains(mainOut, "glibc [command]") {
		t.Errorf("isotope help lacks glibc")
	}
}

// A program started while another one is still in distrobox's first-start
// setup waits for it, instead of entering a box whose user does not exist
// yet.
func TestRunWaitsForFirstStartSetup(t *testing.T) {
	f := newGlibcFake(t)
	t.Setenv("GLIBC_FAKE_WARMUP_SLEEP", "0.4")
	first := make(chan int)
	go func() { first <- glibcRun(runOpts{}, "/bin/true", nil) }()
	// Start the second run once the container exists, while the warm-up runs.
	for deadline := time.Now().Add(5 * time.Second); ; {
		if _, err := os.Stat(f.state); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the first run never created the box")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if code := glibcRun(runOpts{}, "/bin/true", nil); code != 0 {
		t.Fatalf("second run: exit %d: %s", code, f.stderr.String())
	}
	if code := <-first; code != 0 {
		t.Fatalf("first run: exit %d", code)
	}
	calls := f.calls(t)
	done, firstRun := -1, -1
	for i, c := range calls {
		if c == "warmup-done" {
			done = i
		}
		if firstRun < 0 && strings.HasPrefix(c, "distrobox enter isotope-glibc -- bash") {
			firstRun = i
		}
	}
	if done < 0 || firstRun < done {
		t.Fatalf("a program entered the box before its setup finished: %v", calls)
	}
	if n := f.count(t, "distrobox create"); n != 1 {
		t.Fatalf("%d creates: %v", n, calls)
	}
}

// reset cannot remove the box under a running program (which holds the
// lock shared), nor between a run's check and its enter.
func TestResetRefusedWhileAProgramRuns(t *testing.T) {
	f := newGlibcFake(t)
	write(t, f.state, "")
	running, release := make(chan struct{}), make(chan struct{})
	inner := glibc.exec
	glibc.exec = func(path string, argv, env []string) (int, error) {
		close(running)
		<-release
		return inner(path, argv, env)
	}
	done := make(chan int)
	go func() { done <- glibcRun(runOpts{}, "/bin/true", nil) }()
	<-running
	var out bytes.Buffer
	code := cmdGlibc([]string{"reset", "--yes"}, strings.NewReader(""), &out, &out)
	close(release)
	if code != glibcExitInUse || !strings.Contains(out.String(), "in use") {
		t.Fatalf("reset under a running program: exit %d, %q", code, out.String())
	}
	if n := f.count(t, "docker rm"); n != 0 {
		t.Fatalf("removed the box under a running program: %v", f.calls(t))
	}
	if c := <-done; c != 0 {
		t.Fatalf("run: exit %d", c)
	}
	// Once the program is done, reset works.
	if code := cmdGlibc([]string{"reset", "--yes"}, strings.NewReader(""), &out, &out); code != 0 {
		t.Fatalf("reset: exit %d: %s", code, out.String())
	}
}

// Every distrobox call runs with DBX_NON_INTERACTIVE=0 (a missing box makes
// it ask, never create one from its own default image) and
// DBX_CONTAINER_MANAGER=docker, and without the user's other DBX_* settings.
func TestDistroboxEnv(t *testing.T) {
	f := newGlibcFake(t)
	t.Setenv("DBX_NON_INTERACTIVE", "1")
	t.Setenv("DBX_CONTAINER_MANAGER", "podman")
	t.Setenv("DBX_CONTAINER_IMAGE", "registry.fedoraproject.org/fedora-toolbox:latest")
	if code := glibcRun(runOpts{}, "/bin/true", nil); code != 0 {
		t.Fatalf("exit %d: %s", code, f.stderr.String())
	}
	b, err := os.ReadFile(f.log + ".env")
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	if len(lines) != 3 { // create, warm-up enter, run enter
		t.Fatalf("distrobox calls %q", lines)
	}
	for _, l := range lines {
		if !strings.HasSuffix(l, " DBX_NON_INTERACTIVE=0 DBX_CONTAINER_MANAGER=docker DBX_CONTAINER_IMAGE=unset") {
			t.Errorf("distrobox env: %q", l)
		}
	}
}

// status --json is what Isotope System's Compatibility page reads.
func TestGlibcStatusJSON(t *testing.T) {
	f := newGlibcFake(t)
	status := func() glibcStatusJSON {
		t.Helper()
		var out bytes.Buffer
		if code := cmdGlibc([]string{"status", "--json"}, strings.NewReader(""), &out, &out); code != 0 {
			t.Fatalf("status --json: exit %d: %s", code, out.String())
		}
		var raw map[string]any
		if err := json.Unmarshal(out.Bytes(), &raw); err != nil {
			t.Fatalf("not JSON: %q: %v", out.String(), err)
		}
		for _, k := range []string{"exists", "image", "size_bytes", "image_present", "image_size_bytes", "auto", "error", "busy"} {
			if _, ok := raw[k]; !ok {
				t.Errorf("no %q in %s", k, out.String())
			}
		}
		var st glibcStatusJSON
		_ = json.Unmarshal(out.Bytes(), &st)
		return st
	}
	// No box: the image a new one is made from; no setting file is on.
	if st := status(); st.Exists || st.Image != glibcDefaultImage || st.SizeBytes != 0 || !st.Auto || st.Error != "" {
		t.Fatalf("no box: %+v", st)
	}
	if st := status(); st.ImagePresent || st.ImageSizeBytes != 0 {
		t.Fatalf("no image: %+v", st)
	}
	write(t, f.state+".image", "")
	if st := status(); !st.ImagePresent || st.ImageSizeBytes != 78123456 {
		t.Fatalf("image: %+v", st)
	}
	os.Remove(f.state + ".image")
	write(t, f.state, "")
	if st := status(); !st.Exists || st.Image != "docker.io/library/ubuntu:26.04" || st.SizeBytes != 812345678 || st.Error != "" || st.Busy {
		t.Fatalf("box: %+v", st)
	}
	// Being set up or reset (the lock held exclusive) is busy; a program
	// running in it (shared) is not.
	l, err := glibcOpenLock()
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		how  int
		busy bool
	}{{unix.LOCK_EX, true}, {unix.LOCK_SH, false}} {
		if err := l.take(c.how, false); err != nil {
			t.Fatal(err)
		}
		if st := status(); st.Busy != c.busy {
			t.Fatalf("lock %d: %+v", c.how, st)
		}
	}
	l.release()
	// auto is the user's setting, the file `isotope glibc auto` writes.
	if code := cmdGlibc([]string{"auto", "off"}, strings.NewReader(""), f.stderr, f.stderr); code != 0 {
		t.Fatal("auto off")
	}
	if st := status(); st.Auto {
		t.Fatalf("auto off: %+v", st)
	}
	// Docker down: an error, and still the setting.
	t.Setenv("GLIBC_FAKE_DOCKER_DOWN", "1")
	if st := status(); st.Exists || st.Error == "" || st.Auto {
		t.Fatalf("docker down: %+v", st)
	}
	// Not in the docker group: the sentence that says what to do.
	write(t, glibc.groupFile, "docker:x:999:other\n")
	glibc.getgroups = func() ([]int, error) { return []int{10}, nil }
	if st := status(); !strings.Contains(st.Error, "not in the docker group") {
		t.Fatalf("not in docker: %+v", st)
	}
}

func TestGlibcPull(t *testing.T) {
	f := newGlibcFake(t)
	var out bytes.Buffer
	if code := cmdGlibc([]string{"pull", "--progress"}, strings.NewReader(""), &out, f.stderr); code != 0 {
		t.Fatalf("pull: exit %d: %s", code, f.stderr.String())
	}
	if f.count(t, "docker pull "+glibcDefaultImage) != 1 || f.count(t, "distrobox") != 0 {
		t.Fatalf("calls %q", f.calls(t))
	}
	if !strings.Contains(f.stderr.String(), "Pulling from library/ubuntu") || !strings.Contains(out.String(), "is on this computer") {
		t.Fatalf("stderr %q stdout %q", f.stderr.String(), out.String())
	}
	if _, err := os.Stat(f.state); err == nil {
		t.Fatal("pull made the box")
	}
	// A failure exits 1 with the message, a bad argument 2.
	t.Setenv("GLIBC_FAKE_PULL_FAIL", "Error: toomanyrequests: rate limit")
	if code := cmdGlibc([]string{"pull"}, strings.NewReader(""), &out, f.stderr); code != exitError || !strings.Contains(f.stderr.String(), "The glibc image could not be downloaded") {
		t.Fatalf("fail: %d %q", code, f.stderr.String())
	}
	if code := cmdGlibc([]string{"pull", "--x"}, strings.NewReader(""), &out, f.stderr); code != exitUsage {
		t.Fatalf("usage: %d", code)
	}
}

// --progress: what a terminal would show, on stderr, and no notification
// (Isotope System shows the lines and the result itself).
func TestCreateAndResetProgress(t *testing.T) {
	f := newGlibcFake(t)
	glibc.isTerminal = func() bool { return false }
	if code := cmdGlibc([]string{"create", "--progress"}, strings.NewReader(""), f.stderr, f.stderr); code != 0 {
		t.Fatalf("create --progress: exit %d: %s", code, f.stderr.String())
	}
	if len(f.notes) != 0 || !strings.Contains(f.stderr.String(), "This can take a few minutes") || !strings.Contains(f.stderr.String(), "is ready") {
		t.Fatalf("notes %q, stderr %q", f.notes, f.stderr.String())
	}
	if code := cmdGlibc([]string{"reset", "--yes", "--progress"}, strings.NewReader(""), f.stderr, f.stderr); code != 0 {
		t.Fatalf("reset --progress: exit %d: %s", code, f.stderr.String())
	}
	if len(f.notes) != 0 || f.count(t, "docker rm --force isotope-glibc") != 1 {
		t.Fatalf("notes %q, calls %v", f.notes, f.calls(t))
	}
	// A failure is the last line, not a notification.
	t.Setenv("GLIBC_FAKE_CREATE_FAIL", "Error: something broke")
	os.Remove(f.state)
	if code := cmdGlibc([]string{"create", "--progress"}, strings.NewReader(""), f.stderr, f.stderr); code == 0 {
		t.Fatal("a failed create succeeded")
	}
	lines := strings.Split(strings.TrimSpace(f.stderr.String()), "\n")
	if len(f.notes) != 0 || !strings.Contains(lines[len(lines)-1], "something broke") {
		t.Fatalf("notes %q, last line %q", f.notes, lines[len(lines)-1])
	}
	// Without --progress the next call notifies again.
	os.Unsetenv("GLIBC_FAKE_CREATE_FAIL")
	if code := cmdGlibc([]string{"create"}, strings.NewReader(""), f.stderr, f.stderr); code != 0 || len(f.notes) != 2 {
		t.Fatalf("create: exit %d, notes %q", code, f.notes)
	}
	if code := cmdGlibc([]string{"create", "--bogus"}, strings.NewReader(""), f.stderr, f.stderr); code != exitUsage {
		t.Fatalf("create --bogus: exit %d", code)
	}
}

// The in-use refusal is its own exit code, which Isotope System branches
// on, and it comes before the other checks: a container in use is one that
// works.
func TestResetInUseExitCode(t *testing.T) {
	f := newGlibcFake(t)
	write(t, f.state, "")
	l, err := glibcOpenLock()
	if err != nil {
		t.Fatal(err)
	}
	if err := l.take(unix.LOCK_SH, false); err != nil {
		t.Fatal(err)
	}
	os.Remove(glibc.dockerSock) // a refusal, if it were asked first
	var out bytes.Buffer
	code := cmdGlibc([]string{"reset", "--yes", "--progress"}, strings.NewReader(""), &out, &out)
	l.release()
	if code != glibcExitInUse || !strings.Contains(out.String(), "Close those programs") || strings.Contains(out.String(), "Docker is not running") {
		t.Fatalf("exit %d, %q", code, out.String())
	}
	if n := f.count(t, "docker rm"); n != 0 {
		t.Fatalf("removed: %v", f.calls(t))
	}
	if !strings.Contains(glibcUsage, fmt.Sprintf("exits with %d", glibcExitInUse)) {
		t.Fatalf("the help does not name exit %d", glibcExitInUse)
	}
}

// A --progress job whose window closed (its parent changed) still says how
// it ended, as a notification; while the window is there, it does not.
func TestDetachedJobNotifiesItsEnd(t *testing.T) {
	parent := 4242
	setup := func(t *testing.T) *glibcFake {
		f := newGlibcFake(t)
		glibc.isTerminal = func() bool { return false }
		parent = 4242
		glibc.getppid = func() int { return parent }
		return f
	}
	// The window stays: no notification.
	f := setup(t)
	if code := cmdGlibc([]string{"create", "--progress"}, strings.NewReader(""), f.stderr, f.stderr); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if len(f.notes) != 0 {
		t.Fatalf("notes %q", f.notes)
	}
	// The window closes during the create: the end is notified, once.
	f = setup(t)
	t.Setenv("GLIBC_FAKE_CREATE_SLEEP", "0.2")
	go func() { time.Sleep(50 * time.Millisecond); f.mu.Lock(); parent = 1; f.mu.Unlock() }()
	glibc.getppid = func() int { f.mu.Lock(); defer f.mu.Unlock(); return parent }
	if code := cmdGlibc([]string{"create", "--progress"}, strings.NewReader(""), f.stderr, f.stderr); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if len(f.notes) != 1 || !strings.HasPrefix(f.notes[0], "The glibc environment is ready") {
		t.Fatalf("notes %q", f.notes)
	}
	// And a failure.
	f = setup(t)
	os.Unsetenv("GLIBC_FAKE_CREATE_SLEEP")
	t.Setenv("GLIBC_FAKE_CREATE_FAIL", "Error: something broke")
	glibc.getppid = func() int { parent++; return parent }
	if code := cmdGlibc([]string{"create", "--progress"}, strings.NewReader(""), f.stderr, f.stderr); code == 0 {
		t.Fatal("a failed create succeeded")
	}
	if len(f.notes) != 1 || !strings.Contains(f.notes[0], "something broke") {
		t.Fatalf("notes %q", f.notes)
	}
	// So does reset's.
	os.Unsetenv("GLIBC_FAKE_CREATE_FAIL")
	f = setup(t)
	write(t, f.state, "")
	glibc.getppid = func() int { parent++; return parent }
	if code := cmdGlibc([]string{"reset", "--yes", "--progress"}, strings.NewReader(""), f.stderr, f.stderr); code != 0 {
		t.Fatalf("reset: exit %d: %s", code, f.stderr.String())
	}
	if len(f.notes) != 1 || !strings.HasPrefix(f.notes[0], "The glibc environment is ready") {
		t.Fatalf("notes %q", f.notes)
	}
}

// Renovate bumps ARG GLIBC_BOX_IMAGE in the Dockerfile, which glibc.conf is
// written from; the Go fallback must move with it.
func TestGlibcDefaultImageIsTheDockerfilePin(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "Dockerfile"))
	if err != nil {
		t.Fatal(err)
	}
	var pins []string
	for _, line := range strings.Split(string(b), "\n") {
		if v, ok := strings.CutPrefix(line, "ARG GLIBC_BOX_IMAGE="); ok {
			pins = append(pins, strings.TrimSpace(v))
		}
	}
	if len(pins) != 1 || pins[0] != glibcDefaultImage {
		t.Fatalf("Dockerfile ARG GLIBC_BOX_IMAGE = %q, glibcDefaultImage = %q: change glibcDefaultImage with the pin",
			pins, glibcDefaultImage)
	}
}
