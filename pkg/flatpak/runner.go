package flatpak

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

// Runner runs the flatpak command: the one exec seam, so the tests replace
// it with recorded output.
type Runner interface {
	// Output runs name to the end and returns its standard output. A
	// failure carries the last line flatpak printed on standard error.
	Output(ctx context.Context, name string, args ...string) ([]byte, error)
	// Start runs name in a session of its own and returns at once. Its
	// output goes to an unlinked file, not a pipe, so the program finishes
	// its job when the window that started it is closed.
	Start(name string, args ...string) (Process, error)
	// Launch starts name in a session of its own, its output thrown away,
	// and does not wait for it (an app the user opens).
	Launch(name string, args ...string) error
}

// Process is a program Start began.
type Process interface {
	// Lines are the lines it writes (standard output and error), in order.
	// The channel closes after the program ended and its last line was
	// read.
	Lines() <-chan string
	// Wait waits for the end; a non-zero exit is an error that carries the
	// program's last line.
	Wait() error
	// Stop asks the program to stop (SIGTERM to its process group).
	Stop()
}

// ExecRunner runs the real programs.
type ExecRunner struct{}

// env is flatpak's environment: plain output whatever the terminal is,
// which is what the parsers read.
func env() []string { return append(os.Environ(), "FLATPAK_FANCY_OUTPUT=0") }

func (ExecRunner) Output(ctx context.Context, name string, args ...string) ([]byte, error) {
	var out, errOut bytes.Buffer
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdout, cmd.Stderr, cmd.Env = &out, &errOut, env()
	cmd.WaitDelay = 5 * time.Second
	if err := cmd.Run(); err != nil {
		return out.Bytes(), cmdError(name, args, errOut.String(), err)
	}
	return out.Bytes(), nil
}

// cmdError is what a failed command said: its last line, or how it failed.
func cmdError(name string, args []string, out string, err error) error {
	if l := lastLine(out); l != "" {
		return errors.New(strings.TrimPrefix(strings.TrimPrefix(l, "error: "), "Error: "))
	}
	if errors.Is(err, exec.ErrNotFound) {
		return ErrNoFlatpak
	}
	what := name
	if len(args) > 0 {
		what += " " + firstVerb(args)
	}
	return fmt.Errorf("%s: %v", what, err)
}

func firstVerb(args []string) string {
	for _, a := range args {
		if !strings.HasPrefix(a, "-") {
			return a
		}
	}
	return ""
}

func lastLine(s string) string {
	var last string
	for _, l := range strings.Split(s, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			last = l
		}
	}
	return last
}

func (ExecRunner) Launch(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return ErrNoFlatpak
		}
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}

func (ExecRunner) Start(name string, args ...string) (Process, error) {
	dir := os.Getenv("XDG_RUNTIME_DIR")
	if dir == "" {
		dir = os.TempDir()
	}
	w, err := os.CreateTemp(dir, "isotope-flatpak-*.log")
	if err != nil {
		return nil, err
	}
	r, err := os.Open(w.Name())
	os.Remove(w.Name())
	if err != nil {
		w.Close()
		return nil, err
	}
	cmd := exec.Command(name, args...)
	cmd.Stdout, cmd.Stderr, cmd.Env = w, w, env()
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	err = cmd.Start()
	w.Close()
	if err != nil {
		r.Close()
		if errors.Is(err, exec.ErrNotFound) {
			return nil, ErrNoFlatpak
		}
		return nil, err
	}
	p := &execProc{cmd: cmd, lines: make(chan string, 256), done: make(chan struct{}), name: name, args: args}
	go p.follow(r)
	return p, nil
}

type execProc struct {
	cmd   *exec.Cmd
	name  string
	args  []string
	lines chan string
	done  chan struct{}
	err   error
}

func (p *execProc) Lines() <-chan string { return p.lines }

func (p *execProc) Wait() error {
	<-p.done
	return p.err
}

func (p *execProc) Stop() {
	if p.cmd.Process != nil {
		_ = syscall.Kill(-p.cmd.Process.Pid, syscall.SIGTERM)
	}
}

// follow reads the file as it grows, until the program has ended.
func (p *execProc) follow(r *os.File) {
	defer r.Close()
	exited := make(chan error, 1)
	go func() { exited <- p.cmd.Wait() }()
	t := lineTail{out: p.lines}
	tick := time.NewTicker(150 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case err := <-exited:
			t.read(r)
			t.flush()
			if err != nil {
				p.err = cmdError(p.name, p.args, t.last, err)
			}
			close(p.lines)
			close(p.done)
			return
		case <-tick.C:
			t.read(r)
		}
	}
}

// lineTail cuts what a file has gained into lines. flatpak's plain progress
// is one line per update; a carriage return also ends a line.
type lineTail struct {
	out     chan<- string
	partial []byte
	last    string
}

func (t *lineTail) read(r io.Reader) {
	b, _ := io.ReadAll(r)
	t.partial = append(t.partial, b...)
	for {
		i := bytes.IndexAny(t.partial, "\r\n")
		if i < 0 {
			return
		}
		t.emit(string(t.partial[:i]))
		t.partial = t.partial[i+1:]
	}
}

func (t *lineTail) flush() {
	if len(t.partial) > 0 {
		t.emit(string(t.partial))
		t.partial = nil
	}
}

func (t *lineTail) emit(l string) {
	if strings.TrimSpace(l) == "" {
		return
	}
	t.last = l
	select {
	case t.out <- l:
	default: // nobody reads: never block the program's job
	}
}
