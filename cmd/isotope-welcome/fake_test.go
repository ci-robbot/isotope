package main

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"sync"
)

// fakeRunner answers commands from a table and records them. A command
// whose line contains a key of fail exits 1.
type fakeRunner struct {
	mu    sync.Mutex
	out   map[string]string // the whole command line -> stdout
	fail  []string
	calls []string
}

func (f *fakeRunner) line(name string, args []string) string {
	return strings.Join(append([]string{name}, args...), " ")
}

func (f *fakeRunner) failed(line string) bool {
	for _, s := range f.fail {
		if strings.Contains(line, s) {
			return true
		}
	}
	return false
}

func (f *fakeRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	l := f.line(name, args)
	f.mu.Lock()
	f.calls = append(f.calls, l)
	f.mu.Unlock()
	if f.failed(l) {
		return nil, &exec.ExitError{}
	}
	out, ok := f.out[l]
	if !ok && name == "nm-online" {
		return nil, nil
	}
	return []byte(out), nil
}

func (f *fakeRunner) Start(name string, args ...string) (Process, error) {
	l := f.line(name, args)
	f.mu.Lock()
	f.calls = append(f.calls, l)
	f.mu.Unlock()
	lines := make(chan string, 1)
	lines <- "Installing…"
	close(lines)
	var err error
	if f.failed(l) {
		err = fmt.Errorf("exit status 1")
	}
	return fakeProc{lines, err}, nil
}

func (f *fakeRunner) called() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

type fakeProc struct {
	lines chan string
	err   error
}

func (p fakeProc) Lines() <-chan string { return p.lines }
func (p fakeProc) Wait() error          { return p.err }

const (
	flathubOK     = "flatpak --user remotes --columns=name,options"
	installedList = "flatpak --user list --app --columns=application"
	nmOnline      = "nm-online -q -t 5"
)

// setProgress makes the installer look like it is installing: done of total.
func (in *Installer) setProgress(done, total int, current string) {
	in.mu.Lock()
	defer in.mu.Unlock()
	in.running, in.started = true, true
	in.done, in.total, in.current = done, total, current
}

// setFinished makes the installer look like its batch has ended.
func (in *Installer) setFinished() {
	in.mu.Lock()
	defer in.mu.Unlock()
	in.running, in.started = false, true
	in.done, in.current = in.total, ""
}
