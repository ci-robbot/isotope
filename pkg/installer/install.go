package installer

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/exec"
)

// EventKind says what an Event reports.
type EventKind int

const (
	// EventLine is one line of the install log, in Line.
	EventLine EventKind = iota
	// EventStep says the install reached Steps[Step].
	EventStep
	// EventFinished says the install worked.
	EventFinished
	// EventFailed says the install failed, with Err.
	EventFailed
)

// Event is what an install run reports to its front end.
type Event struct {
	Kind EventKind
	Line string
	Step int
	Err  error
}

// installCommand builds the child install process. ISOTOPE_INSTALL_CMD is the
// single seam through which a disk wipe happens; tests inject a fake here to
// assert did/didn't install, so it must keep working exactly as in the shell
// installer this replaces.
//
// manualConfig, when set, is the installer's own cloud-config, and the install
// runs as `kairos-agent manual-install` on it. The encrypted install needs
// that: its config says reboot: false so the installer can delete the
// passphrase file and check the disk first, and `kairos-agent install`
// answers reboot: false with a
// "press enter" prompt and a getty on tty1, where this UI is drawing.
// manual-install returns instead. --use-default-dirs makes it read the same
// config directories `install` reads, so the two install the same system.
func installCommand(manualConfig string) *exec.Cmd {
	if c := os.Getenv("ISOTOPE_INSTALL_CMD"); c != "" {
		return exec.Command("/bin/sh", "-c", c)
	}
	if manualConfig != "" {
		return exec.Command("/usr/bin/kairos-agent", "manual-install", "--use-default-dirs", manualConfig)
	}
	return exec.Command("/usr/bin/kairos-agent", "install")
}

// PostInstall runs after a successful `kairos-agent install`, inside the
// progress UI; log adds a line to the install log. nil means nothing to do.
type PostInstall func(log func(string)) error

// RebootCommand restarts the machine after an install that asked kairos-agent
// not to (an encrypted one). ISOTOPE_REBOOT_CMD is the test seam.
func RebootCommand() *exec.Cmd {
	if c := os.Getenv("ISOTOPE_REBOOT_CMD"); c != "" {
		return exec.Command("/bin/sh", "-c", c)
	}
	return exec.Command("reboot")
}

// EncryptionSteps returns what an encrypted install does around kairos-agent:
// pre writes the passphrase where kcrypt reads it, cleanup deletes it again
// (whether or not the install worked), post checks the disk. All three are
// nil for a choice without encryption, for --progress-only (the agent
// appliance) and for a seeded config.
//
// A Trusted Boot install with a recovery key has no passphrase file: pre does
// nothing, post adds the key to the TPM-sealed containers kairos-agent left
// (enrollRecoveryKey writes and deletes its own 0600 file around that), and
// cleanup makes sure that file is gone.
func EncryptionSteps(c *Choice) (pre func() error, cleanup func() error, post PostInstall) {
	if c != nil && c.RecoveryKey && !c.Encrypt {
		disk, key := c.Disk, c.RecoveryKeyValue
		pre = func() error { return nil }
		cleanup = func() error { return removePassphraseFile(recoveryKeyFile) }
		post = func(log func(string)) error { return enrollRecoveryKey(execRunner, disk, key, log) }
		return pre, cleanup, post
	}
	if c == nil || !c.Encrypt {
		return nil, nil, nil
	}
	disk, passphrase := c.Disk, c.Passphrase
	withKey, key := c.RecoveryKey, c.RecoveryKeyValue
	pre = func() error { return writePassphraseFile(passphraseFile, passphrase) }
	cleanup = func() error {
		err := removePassphraseFile(passphraseFile)
		if rerr := removePassphraseFile(recoveryKeyFile); err == nil {
			err = rerr
		}
		return err
	}
	post = func(log func(string)) error {
		if !withKey {
			// The file goes before the check, so it is gone even when
			// the check fails and the person walks away from the
			// failure screen.
			if err := cleanup(); err != nil {
				return fmt.Errorf("deleting %s: %w", passphraseFile, err)
			}
			return verifyEncryption(execRunner, disk, passphrase, log)
		}
		// With a recovery key the passphrase file is the existing key
		// cryptsetup unlocks with to add it. It goes right after, and on
		// any failure.
		err := verifyEncryption(execRunner, disk, passphrase, log)
		if err == nil {
			err = enrollRecoveryKeyWith(execRunner, disk, key, &passphraseFile, log)
		}
		if cerr := cleanup(); cerr != nil && err == nil {
			err = fmt.Errorf("deleting %s: %w", passphraseFile, cerr)
		}
		return err
	}
	return pre, cleanup, post
}

// Install is one run of the installer as a subprocess. kairos-agent has no
// progress API, so its stdout is the only signal available (see steps.go).
type Install struct {
	cmd     *exec.Cmd
	stdout  io.Reader
	devnull *os.File
	post    PostInstall
}

// NewInstall prepares the install: `kairos-agent manual-install` on
// manualConfig when it is set, `kairos-agent install` otherwise, or
// ISOTOPE_INSTALL_CMD. post, when set, runs after a successful install and
// its failure is an install failure. The child never reads the terminal,
// which belongs to the front end. Close releases what NewInstall opened.
func NewInstall(manualConfig string, post PostInstall) (*Install, error) {
	cmd := installCommand(manualConfig)

	devnull, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		return nil, err
	}
	cmd.Stdin = devnull

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		devnull.Close()
		return nil, err
	}
	cmd.Stderr = cmd.Stdout // merge, so failures show up in the log
	return &Install{cmd: cmd, stdout: stdout, devnull: devnull, post: post}, nil
}

// Close releases the null device the child reads from.
func (in *Install) Close() error { return in.devnull.Close() }

// Run starts the install and blocks until it is over, reporting through
// emit: every log line (EventLine), each new step (EventStep), and at the
// end EventFinished or EventFailed. A read error on the log is reported as
// EventFailed when it happens, and the run still waits for the child.
func (in *Install) Run(emit func(Event)) {
	step := 0
	send := func(e Event) {
		emit(e)
		if e.Kind == EventLine {
			if s := AdvanceStep(e.Line, step); s != step {
				step = s
				emit(Event{Kind: EventStep, Step: s})
			}
		}
	}
	if err := in.cmd.Start(); err != nil {
		emit(Event{Kind: EventFailed, Err: err})
		return
	}
	streamLines(in.stdout, send)
	err := in.cmd.Wait()
	if err == nil && in.post != nil {
		err = in.post(func(l string) { send(Event{Kind: EventLine, Line: l}) })
	}
	if err != nil {
		emit(Event{Kind: EventFailed, Err: err})
		return
	}
	emit(Event{Kind: EventStep, Step: len(Steps) - 1})
	emit(Event{Kind: EventFinished})
}

// streamLines reads r line by line and delivers each as an EventLine via
// send. Returns at EOF. Install logs can carry very long lines (device lists,
// kernel cmdlines), so the scanner buffer is enlarged well past bufio's 64KiB
// default.
//
// A scan error is reported as an EventFailed rather than swallowed. Ending the
// scan silently is not merely a stalled progress bar: the caller goes on to
// cmd.Wait() with the pipe undrained, so a child still writing blocks on a full
// pipe while Wait blocks on the child — an unrecoverable hang, and the progress
// screen has no quit key. Surfacing it makes it a visible halt the user can
// leave instead.
func streamLines(r io.Reader, send func(Event)) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		send(Event{Kind: EventLine, Line: sc.Text()})
	}
	if err := sc.Err(); err != nil {
		send(Event{Kind: EventFailed, Err: fmt.Errorf("reading installer output: %w", err)})
	}
}
