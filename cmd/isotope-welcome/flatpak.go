package main

import (
	"context"
	"errors"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

// InstalledApps are the user's installed Flatpak apps, by ID.
func InstalledApps(r Runner) (map[string]bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	out, err := r.Run(ctx, "flatpak", "--user", "list", "--app", "--columns=application")
	if err != nil {
		return nil, err
	}
	got := map[string]bool{}
	for _, l := range strings.Split(string(out), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			got[l] = true
		}
	}
	return got, nil
}

// FlathubReady: the user's flathub remote exists and is enabled.
// isotope-user-setup disables it when it cannot verify it; that is left
// alone. The rule of the terminal checklist's flathub_ready.
func FlathubReady(r Runner) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	out, err := r.Run(ctx, "flatpak", "--user", "remotes", "--columns=name,options")
	if err != nil {
		return false
	}
	for _, l := range strings.Split(string(out), "\n") {
		name, opts, _ := strings.Cut(l, "\t")
		if name != "flathub" {
			continue
		}
		disabled := false
		for _, o := range strings.Split(opts, ",") {
			if strings.TrimSpace(o) == "disabled" {
				disabled = true
			}
		}
		if !disabled {
			return true
		}
	}
	return false
}

// Online asks NetworkManager whether the machine has a network. Without
// nm-online it assumes it has, and the Flathub check says the rest.
func Online(r Runner) bool { return onlineWithin(r, 5) }

// onlineWithin is Online, waiting up to secs seconds for a connection.
func onlineWithin(r Runner, secs int) bool {
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(secs+10)*time.Second)
	defer cancel()
	_, err := r.Run(ctx, "nm-online", "-q", "-t", strconv.Itoa(secs))
	return err == nil || !isExit(err)
}

// Installer installs apps one after the other, as user Flatpaks from the
// flathub remote, in programs that are detached from the window.
type Installer struct {
	r      Runner
	labels map[string]string
	// OnApp is called, from the installer's goroutine, after each app.
	OnApp func(id string, ok bool)

	mu          sync.Mutex
	done, total int
	current     string
	ok, failed  []string
	running     bool
	started     bool
	finished    chan struct{}
}

// NewInstaller makes an installer that names the apps by the list.
func NewInstaller(r Runner, apps []App) *Installer {
	l := map[string]string{}
	for _, a := range apps {
		l[a.ID] = a.Label
	}
	return &Installer{r: r, labels: l}
}

// Start installs ids, in order. A batch that is still running is left
// alone. A new batch after a finished one starts again from zero.
func (in *Installer) Start(ids []string) {
	in.mu.Lock()
	defer in.mu.Unlock()
	if in.running || len(ids) == 0 {
		return
	}
	in.running, in.started = true, true
	in.done, in.total, in.current = 0, len(ids), ""
	in.ok, in.failed = nil, nil
	in.finished = make(chan struct{})
	go in.run(append([]string(nil), ids...), in.finished)
}

func (in *Installer) run(ids []string, finished chan struct{}) {
	for _, id := range ids {
		in.mu.Lock()
		in.current = id
		in.mu.Unlock()
		ok := in.install(id)
		if in.OnApp != nil {
			in.OnApp(id, ok)
		}
		in.mu.Lock()
		in.done++
		if ok {
			in.ok = append(in.ok, id)
		} else {
			in.failed = append(in.failed, id)
		}
		in.mu.Unlock()
	}
	in.mu.Lock()
	in.running = false
	in.current = ""
	in.mu.Unlock()
	close(finished)
}

func (in *Installer) install(id string) bool {
	p, err := in.r.Start("flatpak", "--user", "install", "--noninteractive", "flathub", id)
	if err != nil {
		return false
	}
	for range p.Lines() { // drain; flatpak's own progress is not shown
	}
	return p.Wait() == nil
}

// Progress is how many apps are done of how many, and the ID of the one
// being installed.
func (in *Installer) Progress() (done, total int, current string) {
	in.mu.Lock()
	defer in.mu.Unlock()
	return in.done, in.total, in.current
}

// Result is the apps that installed and those that did not, so far;
// finished is true when the batch has ended.
func (in *Installer) Result() (ok, failed []string, finished bool) {
	in.mu.Lock()
	defer in.mu.Unlock()
	return append([]string(nil), in.ok...), append([]string(nil), in.failed...), in.started && !in.running
}

// Running reports a batch in progress.
func (in *Installer) Running() bool {
	in.mu.Lock()
	defer in.mu.Unlock()
	return in.running
}

// Started reports that a batch was started at least once.
func (in *Installer) Started() bool {
	in.mu.Lock()
	defer in.mu.Unlock()
	return in.started
}

// Wait blocks until the batch in progress, if any, has ended.
func (in *Installer) Wait() {
	in.mu.Lock()
	ch := in.finished
	in.mu.Unlock()
	if ch != nil {
		<-ch
	}
}

// Label is the list's label of an app, or its ID.
func (in *Installer) Label(id string) string {
	if l, ok := in.labels[id]; ok {
		return l
	}
	return id
}

// isExit: err is a program that ran and exited with a failure, not one that
// could not start.
func isExit(err error) bool {
	var e *exec.ExitError
	return errors.As(err, &e)
}
