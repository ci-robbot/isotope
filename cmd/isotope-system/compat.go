package main

// The Compatibility page talks to `isotope glibc` (cmd/isotope/glibc.go),
// run as the user: the setting file, the container and the AppImage cache
// stay one implementation, in the CLI. isotope-system cannot import
// cmd/isotope (package main), so it runs the binary. See
// docs/superpowers/specs/2026-11-23-glibc-programs-design.md.

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

// Glibc is `isotope glibc`, for the Compatibility page.
type Glibc interface {
	// Status is `isotope glibc status --json`.
	Status() (GlibcStatus, error)
	// SetAuto is `isotope glibc auto on|off`.
	SetAuto(on bool) error
	// Create is `isotope glibc create --progress`: it returns when the
	// container is ready, and gives each line of progress to line, from
	// another goroutine.
	Create(line func(string)) error
	// Pull is `isotope glibc pull --progress`: the image only.
	Pull(line func(string)) error
	// Reset is `isotope glibc reset --yes --progress`. A container in use
	// is a glibcInUse error.
	Reset(line func(string)) error
}

// GlibcStatus is the JSON of `isotope glibc status --json`
// (glibcStatusJSON in cmd/isotope/glibc.go).
type GlibcStatus struct {
	Exists    bool   `json:"exists"`
	Image     string `json:"image"`
	SizeBytes int64  `json:"size_bytes"`
	Auto      bool   `json:"auto"`
	Error     string `json:"error"`
	// Busy: being created or reset now, by another window or by a
	// program's first run.
	Busy bool `json:"busy"`
	// ImagePresent: the image is in Docker; ImageSizeBytes is its size.
	ImagePresent   bool  `json:"image_present"`
	ImageSizeBytes int64 `json:"image_size_bytes"`
}

// glibcExitInUse is the exit code of `isotope glibc reset` while programs
// run in the container (glibcExitInUse in cmd/isotope/glibc.go).
const glibcExitInUse = 75

// glibcInUse is reset's refusal while programs run in the container (exit
// glibcExitInUse): the CLI's sentence, which the page shows as a message.
type glibcInUse struct{ msg string }

func (e glibcInUse) Error() string { return e.msg }

// glibcCLI runs bin (isotope) as the user.
type glibcCLI struct{ bin string }

// glibcStatusTimeout bounds status --json: Docker can hang.
const glibcStatusTimeout = time.Minute

func (c glibcCLI) Status() (GlibcStatus, error) {
	ctx, cancel := context.WithTimeout(context.Background(), glibcStatusTimeout)
	defer cancel()
	var out, errOut bytes.Buffer
	cmd := exec.CommandContext(ctx, c.bin, "glibc", "status", "--json")
	cmd.Stdout, cmd.Stderr = &out, &errOut
	// A docker child that hangs keeps the pipes open after isotope is
	// killed; stop waiting for them too.
	cmd.WaitDelay = 5 * time.Second
	if err := cmd.Run(); err != nil {
		return GlibcStatus{}, cliError("isotope glibc status", errOut.String(), err)
	}
	var st GlibcStatus
	if err := json.Unmarshal(out.Bytes(), &st); err != nil {
		return GlibcStatus{}, fmt.Errorf("isotope glibc status gave no status (%v); is isotope up to date?", err)
	}
	return st, nil
}

func (c glibcCLI) SetAuto(on bool) error {
	arg := "off"
	if on {
		arg = "on"
	}
	var errOut bytes.Buffer
	cmd := exec.Command(c.bin, "glibc", "auto", arg)
	cmd.Stderr = &errOut
	if err := cmd.Run(); err != nil {
		return cliError("isotope glibc auto", errOut.String(), err)
	}
	return nil
}

func (c glibcCLI) Create(line func(string)) error {
	_, err := c.job(line, "create", "--progress")
	return err
}

func (c glibcCLI) Pull(line func(string)) error {
	_, err := c.job(line, "pull", "--progress")
	return err
}

func (c glibcCLI) Reset(line func(string)) error {
	code, err := c.job(line, "reset", "--yes", "--progress")
	if code == glibcExitInUse {
		return glibcInUse{msg: err.Error()}
	}
	return err
}

// job runs `isotope glibc ARGS...`, which can take minutes, and passes its
// output to line as it comes. The program runs in its own session and
// writes to a file, not a pipe, so closing the window does not stop it: it
// finishes the setup (or the reset), and the page shows the result when it
// is opened again. A pipe would kill it at its next line once the window
// is gone. The file is unlinked at once; the open descriptors keep it until
// both sides are done. When the window is gone at the end, the CLI notices
// its new parent and sends the end as a notification (glibcReaderGone in
// cmd/isotope/glibc.go). A failure is the CLI's last line, its message;
// code is its exit code.
func (c glibcCLI) job(line func(string), args ...string) (code int, err error) {
	dir := os.Getenv("XDG_RUNTIME_DIR")
	if dir == "" {
		dir = os.TempDir()
	}
	w, err := os.CreateTemp(dir, "isotope-glibc-*.log")
	if err != nil {
		return -1, err
	}
	r, err := os.Open(w.Name())
	os.Remove(w.Name())
	if err != nil {
		w.Close()
		return -1, err
	}
	defer r.Close()
	cmd := exec.Command(c.bin, append([]string{"glibc"}, args...)...)
	cmd.Stdout, cmd.Stderr = w, w
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	err = cmd.Start()
	w.Close()
	if err != nil {
		return -1, err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	t := lineTail{line: line}
	tick := time.NewTicker(200 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case err := <-done:
			t.read(r)
			t.flush()
			if err != nil {
				return cmd.ProcessState.ExitCode(), cliError("isotope glibc "+args[0], t.last, err)
			}
			return 0, nil
		case <-tick.C:
			t.read(r)
		}
	}
}

// lineTail cuts what a file has gained into lines.
type lineTail struct {
	line    func(string)
	partial []byte
	last    string
}

func (t *lineTail) read(r io.Reader) {
	// A regular file: a read error is EOF or a vanished file system, and
	// the next tick or the end tries again.
	b, _ := io.ReadAll(r)
	t.partial = append(t.partial, b...)
	for {
		i := bytes.IndexByte(t.partial, '\n')
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
	l = strings.TrimRight(l, "\r")
	if strings.TrimSpace(l) == "" {
		return
	}
	t.last = l
	t.line(l)
}

// cliError is what a failed isotope call said: its last line, or how it
// failed when it said nothing.
func cliError(what, out string, err error) error {
	var last string
	sc := bufio.NewScanner(strings.NewReader(out))
	for sc.Scan() {
		if l := strings.TrimSpace(sc.Text()); l != "" {
			last = l
		}
	}
	if last != "" {
		return errors.New(last)
	}
	if errors.Is(err, exec.ErrNotFound) || errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("%s: the isotope command is not installed", what)
	}
	return fmt.Errorf("%s: %v", what, err)
}
