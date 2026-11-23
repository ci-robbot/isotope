package main

import (
	"bufio"
	"context"
	"os"
	"os/exec"
	"syscall"
)

// Runner is how the welcome flow runs other programs: the one exec seam,
// so the pages and their tests never call os/exec themselves.
type Runner interface {
	// Run runs name to the end and returns its standard output.
	Run(ctx context.Context, name string, args ...string) ([]byte, error)
	// Start starts name detached, in a session of its own, so it keeps
	// running when the window closes.
	Start(name string, args ...string) (Process, error)
}

// Process is a program Start began.
type Process interface {
	// Lines are the lines it writes (stdout and stderr). The channel
	// closes when the program ends; lines nobody reads are dropped.
	Lines() <-chan string
	// Wait waits for the program to end; a non-zero exit is an error.
	Wait() error
}

// ExecRunner runs real programs.
type ExecRunner struct{}

func (ExecRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).Output()
}

// DetachedCommand is the command Start runs: a new session, so it is no
// longer in the window's (or the terminal's) process group and ignores
// the hangup that ends them.
func DetachedCommand(name string, args ...string) *exec.Cmd {
	cmd := exec.Command(name, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	return cmd
}

func (ExecRunner) Start(name string, args ...string) (Process, error) {
	pr, pw, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	cmd := DetachedCommand(name, args...)
	cmd.Stdout, cmd.Stderr = pw, pw
	if err := cmd.Start(); err != nil {
		pr.Close()
		pw.Close()
		return nil, err
	}
	pw.Close() // the child holds the write end now
	lines := make(chan string, 64)
	go func() {
		defer close(lines)
		sc := bufio.NewScanner(pr)
		for sc.Scan() {
			select {
			case lines <- sc.Text():
			default: // nobody reads: never stop the program on a full pipe
			}
		}
		pr.Close()
	}()
	return &execProc{cmd: cmd, lines: lines}, nil
}

type execProc struct {
	cmd   *exec.Cmd
	lines chan string
}

func (p *execProc) Lines() <-chan string { return p.lines }
func (p *execProc) Wait() error          { return p.cmd.Wait() }
