package main

import (
	"errors"
	"image"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kairos-community/isotope/pkg/guikit"
)

// fakeGlibc is `isotope glibc` for the page: a status, and the calls.
type fakeGlibc struct {
	mu    sync.Mutex
	st    GlibcStatus
	err   error
	calls []string
	// gate holds Create and Reset until it is closed; nil does not wait.
	gate     chan struct{}
	lines    []string
	jobErr   error
	autoErr  error
	statuses int
}

func (f *fakeGlibc) Status() (GlibcStatus, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.statuses++
	return f.st, f.err
}

func (f *fakeGlibc) SetAuto(on bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "auto "+map[bool]string{true: "on", false: "off"}[on])
	if f.autoErr != nil {
		return f.autoErr
	}
	f.st.Auto = on
	return nil
}

func (f *fakeGlibc) job(name string, line func(string)) error {
	f.mu.Lock()
	f.calls = append(f.calls, name)
	gate, lines := f.gate, f.lines
	f.mu.Unlock()
	for _, l := range lines {
		line(l)
	}
	if gate != nil {
		<-gate
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.jobErr != nil {
		return f.jobErr
	}
	f.st.Exists, f.st.SizeBytes = true, 1_234_000_000
	return nil
}

func (f *fakeGlibc) Create(line func(string)) error { return f.job("create", line) }
func (f *fakeGlibc) Pull(line func(string)) error {
	f.mu.Lock()
	f.calls = append(f.calls, "pull")
	lines, err := f.lines, f.jobErr
	f.mu.Unlock()
	for _, l := range lines {
		line(l)
	}
	if err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.st.ImagePresent, f.st.ImageSizeBytes = true, 78_000_000
	return nil
}
func (f *fakeGlibc) Reset(line func(string)) error { return f.job("reset", line) }

func (f *fakeGlibc) called() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

const inUseText = "The glibc container is in use: a program built for glibc is running in it, or it is being set up. Close those programs, then try again."

func compatUI(t *testing.T, g *fakeGlibc, live bool) (*UI, *compatPage) {
	t.Helper()
	u := newUI(testSystem(true), guikit.NewShaper(), Hooks{Glibc: g, Live: live, Start: func(...string) error { return nil }}, PageCompat)
	if live {
		return u, nil
	}
	p := u.view(PageCompat).(*compatPage)
	eventually(t, "the status", func() bool { return p.state().loaded })
	return u, p
}

func newFakeGlibc(exists bool) *fakeGlibc {
	st := GlibcStatus{Image: "docker.io/library/ubuntu:26.04", Auto: true}
	if exists {
		st.Exists, st.SizeBytes, st.ImagePresent = true, 1_234_000_000, true
	}
	return &fakeGlibc{st: st}
}

func TestCompatPageIsListedAndOpensByName(t *testing.T) {
	if p, ok := ParseArgs([]string{"compatibility"}); !ok || p != PageCompat {
		t.Fatalf("compatibility: %q %v", p, ok)
	}
	if PageCompat.Title() != "Compatibility" {
		t.Fatalf("title %q", PageCompat.Title())
	}
	if !strings.Contains(usage, "compatibility") {
		t.Fatalf("usage %q", usage)
	}
}

func TestCompatStatusText(t *testing.T) {
	cases := []struct {
		st          GlibcStatus
		title, sub  string
		setup, rset bool
	}{
		{GlibcStatus{Image: "docker.io/library/ubuntu:26.04", Auto: true}, "Not set up yet",
			"Isotope sets it up the first time you start such a program, or now. It downloads Ubuntu 26.04 (docker.io/library/ubuntu:26.04) and can take a few minutes.", true, false},
		{GlibcStatus{Exists: true, Image: "docker.io/library/ubuntu:26.04", SizeBytes: 1_234_000_000}, "Ready",
			"Ubuntu 26.04 (docker.io/library/ubuntu:26.04) · 1.2 GB on disk", false, true},
		{GlibcStatus{Exists: true, Image: "quay.io/x/box:1"}, "Ready", "quay.io/x/box:1", false, true},
		{GlibcStatus{Error: "Docker did not answer about the glibc container: Cannot connect."}, "Not available",
			"Docker did not answer about the glibc container: Cannot connect.", false, false},
		{GlibcStatus{Exists: true, Busy: true, Image: "docker.io/library/ubuntu:26.04"}, "Being set up",
			"Another window or a program's first run is setting up or resetting it. Open this page again to see the result.", false, false},
	}
	for _, c := range cases {
		v := compatViewOf(c.st)
		if v.Title != c.title || v.Sub != c.sub || v.Setup != c.setup || v.Reset != c.rset {
			t.Errorf("%+v:\n got %+v\nwant %q %q %v %v", c.st, v, c.title, c.sub, c.setup, c.rset)
		}
	}
}

func TestCompatSwitchWritesTheSetting(t *testing.T) {
	g := newFakeGlibc(false)
	_, p := compatUI(t, g, false)
	if !p.state().st.Auto {
		t.Fatal("not on")
	}
	p.toggleAuto()
	eventually(t, "auto off", func() bool { return !p.state().st.Auto && !p.state().autoBusy })
	p.toggleAuto()
	eventually(t, "auto on", func() bool { return p.state().st.Auto && !p.state().autoBusy })
	eventually(t, "the setup", func() bool { s := p.state(); return s.working == "" && s.st.Exists })
	if got := strings.Join(g.called(), ","); got != "auto off,auto on,create" {
		t.Fatalf("calls %s", got)
	}
	// A failure is said on the page and the switch stays.
	g.mu.Lock()
	g.autoErr = errors.New("isotope glibc: permission denied")
	g.mu.Unlock()
	p.toggleAuto()
	eventually(t, "the failure", func() bool { return p.state().fail != "" })
	if s := p.state(); !s.st.Auto || !strings.Contains(s.fail, "permission denied") {
		t.Fatalf("state %+v", s)
	}
}

func TestCompatEnableSetsUpTheBoxWhenMissing(t *testing.T) {
	g := newFakeGlibc(false)
	g.st.Auto = false
	g.gate = make(chan struct{})
	g.lines = []string{"Pulling docker.io/library/ubuntu:26.04"}
	_, p := compatUI(t, g, false)
	p.toggleAuto()
	eventually(t, "the progress", func() bool { s := p.state(); return s.working == jobSetup && len(s.lines) == 1 })
	close(g.gate)
	eventually(t, "ready", func() bool { s := p.state(); return s.working == "" && s.st.Exists })
	if got := strings.Join(g.called(), ","); got != "auto on,create" {
		t.Fatalf("calls %s", got)
	}
	if s := p.state(); !s.st.Auto || s.fail != "" {
		t.Fatalf("state %+v", s)
	}
}

func TestCompatEnableWithABoxOnlyWritesTheSetting(t *testing.T) {
	g := newFakeGlibc(true)
	g.st.Auto = false
	_, p := compatUI(t, g, false)
	p.toggleAuto()
	eventually(t, "auto on", func() bool { return p.state().st.Auto && !p.state().autoBusy })
	time.Sleep(50 * time.Millisecond)
	if got := strings.Join(g.called(), ","); got != "auto on" {
		t.Fatalf("calls %s", got)
	}
}

func TestCompatDisableNeverCreatesOrResets(t *testing.T) {
	for _, exists := range []bool{false, true} {
		g := newFakeGlibc(exists)
		_, p := compatUI(t, g, false)
		p.toggleAuto()
		eventually(t, "auto off", func() bool { return !p.state().st.Auto && !p.state().autoBusy })
		time.Sleep(50 * time.Millisecond)
		if got := strings.Join(g.called(), ","); got != "auto off" {
			t.Fatalf("exists=%v calls %s", exists, got)
		}
	}
}

func TestCompatEnableSetupFailureKeepsTheSetting(t *testing.T) {
	g := newFakeGlibc(false)
	g.st.Auto = false
	g.jobErr = errors.New("The glibc container could not be created: no network.")
	_, p := compatUI(t, g, false)
	p.toggleAuto()
	eventually(t, "the failure", func() bool { return p.state().fail != "" })
	if s := p.state(); s.working != "" || !s.st.Auto || !strings.Contains(s.fail, "no network") {
		t.Fatalf("state %+v", s)
	}
	if got := strings.Join(g.called(), ","); got != "auto on,create" {
		t.Fatalf("calls %s", got)
	}
}

func TestCompatSetUpNow(t *testing.T) {
	g := newFakeGlibc(false)
	g.gate = make(chan struct{})
	g.lines = []string{"Pulling docker.io/library/ubuntu:26.04"}
	_, p := compatUI(t, g, false)
	p.setupNow()
	eventually(t, "the progress", func() bool { s := p.state(); return s.working == jobSetup && len(s.lines) == 1 })
	p.setupNow() // a second click while it runs does nothing
	// Opening the page again during the job does not read the half-made
	// container's status.
	g.mu.Lock()
	before := g.statuses
	g.mu.Unlock()
	p.Shown()
	time.Sleep(20 * time.Millisecond)
	g.mu.Lock()
	after := g.statuses
	g.mu.Unlock()
	if after != before {
		t.Fatalf("status read during the job: %d -> %d", before, after)
	}
	close(g.gate)
	eventually(t, "ready", func() bool { s := p.state(); return s.working == "" && s.st.Exists })
	if got := strings.Join(g.called(), ","); got != "create" {
		t.Fatalf("calls %s", got)
	}
	if s := p.state(); s.fail != "" || s.lines != nil {
		t.Fatalf("state %+v", s)
	}

	// A failure stays on the page.
	g = newFakeGlibc(false)
	g.jobErr = errors.New("The glibc container could not be created: no network.")
	_, p = compatUI(t, g, false)
	p.setupNow()
	eventually(t, "the failure", func() bool { return p.state().fail != "" })
	if s := p.state(); s.working != "" || !strings.Contains(s.fail, "no network") {
		t.Fatalf("state %+v", s)
	}
}

func TestCompatImageMissingOffersDownload(t *testing.T) {
	ub := "docker.io/library/ubuntu:26.04"
	cases := []struct {
		name string
		st   GlibcStatus
		pull bool
		set  bool
		rst  bool
	}{
		{"no box, no image", GlibcStatus{Image: ub}, false, true, false},
		{"no box, image", GlibcStatus{Image: ub, ImagePresent: true}, false, true, false},
		{"box, image", GlibcStatus{Exists: true, Image: ub, ImagePresent: true}, false, false, true},
		{"box, image pruned", GlibcStatus{Exists: true, Image: ub}, true, false, true},
	}
	for _, c := range cases {
		v := compatViewOf(c.st)
		if v.Pull != c.pull || v.Setup != c.set || v.Reset != c.rst {
			t.Errorf("%s: %+v", c.name, v)
		}
		if c.pull && v.Note == "" || !c.pull && v.Note != "" {
			t.Errorf("%s: note %q", c.name, v.Note)
		}
	}
	if v := compatViewOf(cases[3].st); !strings.HasPrefix(v.Note, "The Ubuntu image is not on this computer") {
		t.Errorf("note %q", v.Note)
	}

	// The button runs pull, with progress, and not create or reset.
	g := newFakeGlibc(true)
	g.st.ImagePresent = false
	g.gate = nil
	g.lines = []string{"26.04: Pulling from library/ubuntu"}
	_, p := compatUI(t, g, false)
	p.pullNow()
	eventually(t, "downloaded", func() bool { s := p.state(); return s.working == "" && s.st.ImagePresent })
	if got := strings.Join(g.called(), ","); got != "pull" {
		t.Fatalf("calls %s", got)
	}
	// A failure is shown.
	g = newFakeGlibc(true)
	g.st.ImagePresent = false
	g.jobErr = errors.New("The glibc image could not be downloaded: no network.")
	_, p = compatUI(t, g, false)
	p.pullNow()
	eventually(t, "the failure", func() bool { return p.state().fail != "" })
	if s := p.state(); s.working != "" || s.st.ImagePresent || !strings.Contains(s.fail, "no network") {
		t.Fatalf("state %+v", s)
	}
	// Enabling with no box and no image runs create only: it pulls.
	g = newFakeGlibc(false)
	g.st.Auto = false
	_, p = compatUI(t, g, false)
	p.toggleAuto()
	eventually(t, "ready", func() bool { s := p.state(); return s.working == "" && s.st.Exists })
	if got := strings.Join(g.called(), ","); got != "auto on,create" {
		t.Fatalf("calls %s", got)
	}
}

func TestCompatResetConfirmsThenRuns(t *testing.T) {
	g := newFakeGlibc(true)
	u, p := compatUI(t, g, false)
	p.askReset()
	d := waitQuestion(t, u, "Reset the glibc environment?")
	u.pick(d, 0) // Cancel
	if len(g.called()) != 0 {
		t.Fatalf("calls %q", g.called())
	}
	p.askReset()
	d = waitQuestion(t, u, "Reset the glibc environment?")
	if !strings.Contains(d.body, "your home folder is not touched") || d.choices[1].Label != "Reset" {
		t.Fatalf("dialog %q %+v", d.body, d.choices)
	}
	u.pick(d, 1)
	eventually(t, "the reset", func() bool { s := p.state(); return s.working == "" && len(g.called()) == 1 })
	if got := g.called(); got[0] != "reset" {
		t.Fatalf("calls %q", got)
	}
}

// The CLI's in-use refusal is a message on the page, not an error dialog.
func TestCompatResetInUseIsAMessage(t *testing.T) {
	g := newFakeGlibc(true)
	g.jobErr = glibcInUse{msg: inUseText}
	u, p := compatUI(t, g, false)
	p.askReset()
	u.pick(waitQuestion(t, u, "Reset the glibc environment?"), 1)
	eventually(t, "the message", func() bool { return p.state().inUse != "" })
	if s := p.state(); s.inUse != inUseText || s.fail != "" {
		t.Fatalf("state %+v", s)
	}
	if u.question() != nil {
		t.Fatalf("a dialog: %q", u.question().title)
	}
	// It goes when the next job starts.
	g.mu.Lock()
	g.jobErr = nil
	g.mu.Unlock()
	p.askReset()
	u.pick(waitQuestion(t, u, "Reset the glibc environment?"), 1)
	eventually(t, "the reset", func() bool { s := p.state(); return s.working == "" && len(g.called()) == 2 })
	if s := p.state(); s.inUse != "" {
		t.Fatalf("state %+v", s)
	}
}

func TestCompatNotAvailableLive(t *testing.T) {
	g := newFakeGlibc(false)
	u, _ := compatUI(t, g, true)
	if _, ok := u.view(PageCompat).(*livePage); !ok {
		t.Fatalf("live view %T", u.view(PageCompat))
	}
	if g.statuses != 0 {
		t.Fatal("the live page asked isotope")
	}
}

// Without isotope (no hook) the page says so, and does not crash.
func TestCompatWithoutTheCLI(t *testing.T) {
	u := newUI(testSystem(true), guikit.NewShaper(), Hooks{Start: func(...string) error { return nil }}, PageCompat)
	p := u.view(PageCompat).(*compatPage)
	if s := p.state(); s.loadErr == "" {
		t.Fatalf("state %+v", s)
	}
	gtx := gtxOf(1100, 720)
	u.Layout(gtx)
}

// The page's states, against the PNGs in testdata/.
func TestCompatScreenshots(t *testing.T) {
	needGPU(t)
	sz := image.Pt(1100, 720)
	at := time.Date(2026, 11, 23, 10, 0, 0, 250_000_000, time.UTC)
	still := func(u *UI) { u.now = func() time.Time { return at }; u.pageAt = time.Time{} }

	t.Run("compat-not-set-up", func(t *testing.T) {
		u, _ := compatUI(t, newFakeGlibc(false), false)
		still(u)
		compare(t, "compat-not-set-up", render(t, u, sz))
	})
	t.Run("compat-ready", func(t *testing.T) {
		u, _ := compatUI(t, newFakeGlibc(true), false)
		still(u)
		compare(t, "compat-ready", render(t, u, sz))
	})
	t.Run("compat-image-missing", func(t *testing.T) {
		g := newFakeGlibc(true)
		g.st.ImagePresent = false
		u, _ := compatUI(t, g, false)
		still(u)
		compare(t, "compat-image-missing", render(t, u, sz))
	})
	t.Run("compat-setting-up", func(t *testing.T) {
		g := newFakeGlibc(false)
		g.gate = make(chan struct{})
		defer close(g.gate)
		g.lines = []string{
			"Setting up the glibc environment, first run only: creating the container isotope-glibc from docker.io/library/ubuntu:26.04. This can take a few minutes.",
			"Image docker.io/library/ubuntu:26.04 not found.",
			"26.04: Pulling from library/ubuntu",
			"Digest: sha256:4f0c4f8c1d3e9b0c2a7d5e6f8a9b0c1d2e3f4a5b6c7d8e9f0a1b2c3d4e5f6a7b",
		}
		u, p := compatUI(t, g, false)
		p.setupNow()
		eventually(t, "the progress", func() bool { return len(p.state().lines) == 4 })
		still(u)
		compare(t, "compat-setting-up", render(t, u, sz))
	})
	t.Run("compat-reset-confirm", func(t *testing.T) {
		u, p := compatUI(t, newFakeGlibc(true), false)
		p.askReset()
		waitQuestion(t, u, "Reset the glibc environment?")
		still(u)
		compare(t, "compat-reset-confirm", render(t, u, sz))
	})
	t.Run("compat-in-use", func(t *testing.T) {
		g := newFakeGlibc(true)
		g.jobErr = glibcInUse{msg: inUseText}
		u, p := compatUI(t, g, false)
		p.askReset()
		u.pick(waitQuestion(t, u, "Reset the glibc environment?"), 1)
		eventually(t, "the message", func() bool { return p.state().inUse != "" })
		still(u)
		compare(t, "compat-in-use", render(t, u, sz))
	})
	t.Run("compat-live", func(t *testing.T) {
		u, _ := compatUI(t, newFakeGlibc(false), true)
		still(u)
		compare(t, "compat-live", render(t, u, sz))
	})
}
