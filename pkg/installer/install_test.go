package installer

import (
	"errors"
	"slices"
	"strings"
	"testing"
)

func TestInstallCommandDefault(t *testing.T) {
	t.Setenv("ISOTOPE_INSTALL_CMD", "")
	cmd := installCommand("")
	got := strings.Join(cmd.Args, " ")
	if got != "/usr/bin/kairos-agent install" {
		t.Errorf("default command = %q, want kairos-agent install", got)
	}
}

// An encrypted install must not end in kairos-agent's "press enter" prompt,
// which `install` shows when the config says reboot: false.
func TestInstallCommandForEncryptedInstall(t *testing.T) {
	t.Setenv("ISOTOPE_INSTALL_CMD", "")
	got := strings.Join(installCommand("/oem/99_isotope-user.yaml").Args, " ")
	if got != "/usr/bin/kairos-agent manual-install --use-default-dirs /oem/99_isotope-user.yaml" {
		t.Errorf("command = %q", got)
	}
	t.Setenv("ISOTOPE_INSTALL_CMD", "/bin/true --fake")
	if got := strings.Join(installCommand("/oem/x.yaml").Args, " "); !strings.Contains(got, "/bin/true --fake") {
		t.Errorf("the ISOTOPE_INSTALL_CMD seam lost to the encrypted install: %q", got)
	}
}

func TestInstallCommandHonoursOverride(t *testing.T) {
	t.Setenv("ISOTOPE_INSTALL_CMD", "/bin/true --fake")
	cmd := installCommand("")
	got := strings.Join(cmd.Args, " ")
	if !strings.Contains(got, "/bin/true --fake") {
		t.Errorf("override command = %q, want it to carry /bin/true --fake", got)
	}
}

// errReader fails partway through, the way a pipe to a dying child does.
type errReader struct{ done bool }

func (e *errReader) Read(p []byte) (int, error) {
	if e.done {
		return 0, errors.New("read: broken pipe")
	}
	e.done = true
	return copy(p, "Partitioning device /dev/vda\n"), nil
}

// TestStreamLinesReportsScanErrors: if the scan stops silently, the install
// run goes on to cmd.Wait() with the pipe undrained: the child blocks
// writing, Wait blocks on the child, and the progress screen has no quit key.
// That is a hang, not a stall, so the error has to reach the UI.
func TestStreamLinesReportsScanErrors(t *testing.T) {
	var events []Event
	streamLines(&errReader{}, func(e Event) { events = append(events, e) })

	if len(events) == 0 {
		t.Fatal("streamLines sent nothing at all")
	}
	last := events[len(events)-1]
	if last.Kind != EventFailed {
		t.Fatalf("last event is %v, want EventFailed carrying the scan error", last.Kind)
	}
	if last.Err == nil {
		t.Fatal("EventFailed carries a nil error; the failure would render as success")
	}
	if !strings.Contains(last.Err.Error(), "broken pipe") {
		t.Errorf("error %q does not carry the underlying cause", last.Err)
	}
}

// TestStreamLinesSendsNoDoneEventAtCleanEOF: a clean EOF is the install run's
// job to report via cmd.Wait(); a spurious EventFailed or EventFinished here
// would flag a good install as failed, or finish it early.
func TestStreamLinesSendsNoDoneEventAtCleanEOF(t *testing.T) {
	var events []Event
	streamLines(strings.NewReader("one\ntwo\n"), func(e Event) { events = append(events, e) })

	if len(events) != 2 {
		t.Fatalf("got %d events, want 2 lines and nothing else: %v", len(events), events)
	}
	for _, e := range events {
		if e.Kind != EventLine {
			t.Errorf("clean EOF produced a %v event: %v", e.Kind, e)
		}
	}
}

// runInstall runs a fake install through ISOTOPE_INSTALL_CMD and returns
// what it reported.
func runInstall(t *testing.T, script string, post PostInstall) []Event {
	t.Helper()
	t.Setenv("ISOTOPE_INSTALL_CMD", script)
	in, err := NewInstall("", post)
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	var events []Event
	in.Run(func(e Event) { events = append(events, e) })
	return events
}

func kinds(events []Event, k EventKind) []Event {
	var out []Event
	for _, e := range events {
		if e.Kind == k {
			out = append(out, e)
		}
	}
	return out
}

// The run reports every line, a step event each time a line moves the
// progress on (never back), and on success the last step and EventFinished.
func TestInstallRunReportsLinesStepsAndTheEnd(t *testing.T) {
	var logged bool
	events := runInstall(t, "echo 'Partitioning device /dev/vda'; echo noise; echo 'Running stage: before-install' >&2",
		func(log func(string)) error { logged = true; log("isotope: checking the disk passphrase"); return nil })
	if !logged {
		t.Fatal("post did not run after a successful install")
	}
	lines := kinds(events, EventLine)
	if len(lines) != 4 || lines[2].Line != "Running stage: before-install" {
		t.Errorf("lines = %v, want the three of the child (stderr merged) and post's", lines)
	}
	var steps []int
	for _, e := range kinds(events, EventStep) {
		steps = append(steps, e.Step)
	}
	if want := []int{1, 2, 8, len(Steps) - 1}; !slices.Equal(steps, want) {
		t.Errorf("steps = %v, want %v", steps, want)
	}
	if last := events[len(events)-1]; last.Kind != EventFinished {
		t.Errorf("last event = %v, want EventFinished", last)
	}
	if len(kinds(events, EventFailed)) != 0 {
		t.Errorf("a good install reported a failure: %v", events)
	}
}

func TestInstallRunReportsAFailedChild(t *testing.T) {
	var ran bool
	events := runInstall(t, "echo 'Partitioning device /dev/vda'; exit 3",
		func(func(string)) error { ran = true; return nil })
	if ran {
		t.Error("post ran after a failed install")
	}
	last := events[len(events)-1]
	if last.Kind != EventFailed || last.Err == nil {
		t.Fatalf("last event = %v, want EventFailed with an error", last)
	}
	if len(kinds(events, EventFinished)) != 0 {
		t.Error("a failed install reported EventFinished")
	}
}

func TestInstallRunReportsAFailedPostStep(t *testing.T) {
	events := runInstall(t, "true", func(func(string)) error { return errors.New("the disk check failed") })
	last := events[len(events)-1]
	if last.Kind != EventFailed || last.Err == nil || last.Err.Error() != "the disk check failed" {
		t.Fatalf("last event = %v, want the post step's error", last)
	}
}
