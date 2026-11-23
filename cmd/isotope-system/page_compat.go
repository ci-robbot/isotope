package main

import (
	"errors"
	"strings"

	"gioui.org/layout"
	"gioui.org/widget"

	"github.com/kairos-community/isotope/pkg/guikit"
)

func init() {
	pageMakers[PageCompat] = func(u *UI) pageView { return &compatPage{u: u} }
}

// The jobs of the Compatibility page.
const (
	jobSetup = "setup"
	jobReset = "reset"
	jobPull  = "pull"
)

// compatPage: running programs made for other Linux systems. Its first
// section, Glibc compatibility, is `isotope glibc` (compat.go): the switch
// that sends programs built for glibc to the container when they start
// (`auto on|off`), the container's state, "Set up now" and "Reset…".
type compatPage struct {
	u    *UI
	list widget.List

	auto, setup, reset, pull widget.Clickable

	// Under u.mu.
	s compatState
}

// compatState is what the page shows; state returns a copy.
type compatState struct {
	st      GlibcStatus
	loaded  bool
	loadErr string
	// working is the running job, jobSetup or jobReset, or "".
	working string
	// lines are the job's last lines of progress.
	lines []string
	// fail is the last failure; inUse is reset's in-use refusal, a
	// message rather than an error.
	fail, inUse string
	autoBusy    bool
}

func (p *compatPage) state() compatState {
	p.u.mu.Lock()
	defer p.u.mu.Unlock()
	s := p.s
	s.lines = append([]string(nil), p.s.lines...)
	if len(s.lines) == 0 {
		s.lines = nil
	}
	return s
}

func (p *compatPage) Shown() { p.load() }

func (p *compatPage) Footer() (leading, trailing []guikit.Action) { return nil, nil }

// load reads the status again, unless a job is running (the container is
// half made then; the job reads it when it ends).
func (p *compatPage) load() {
	u, g := p.u, p.u.hooks.Glibc
	u.mu.Lock()
	if g == nil {
		p.s.loadErr = "The isotope command cannot be reached, so this page cannot show the glibc environment."
	}
	working := p.s.working
	u.mu.Unlock()
	if g == nil || working != "" {
		return
	}
	u.async(func() {
		st, err := g.Status()
		u.mu.Lock()
		defer u.mu.Unlock()
		if err != nil {
			p.s.loadErr = "Could not read the glibc environment: " + err.Error()
			return
		}
		p.s.st, p.s.loaded, p.s.loadErr = st, true, ""
	})
}

// toggleAuto flips the switch through `isotope glibc auto`, then reads the
// setting back. Turned on with no environment yet, it starts the setup
// "Set up now" runs, so the image is pulled with progress and not at the
// first program's start. Turned off, it removes nothing; Reset… does. A
// failed setup leaves the setting on: the next program tries again.
func (p *compatPage) toggleAuto() {
	u, g := p.u, p.u.hooks.Glibc
	u.mu.Lock()
	if g == nil || !p.s.loaded || p.s.autoBusy {
		u.mu.Unlock()
		return
	}
	on := !p.s.st.Auto
	p.s.autoBusy, p.s.fail, p.s.inUse = true, "", ""
	u.mu.Unlock()
	u.async(func() {
		err := g.SetAuto(on)
		st, serr := g.Status()
		u.mu.Lock()
		p.s.autoBusy = false
		if err != nil {
			p.s.fail = "Could not change the setting: " + err.Error()
		}
		if serr == nil {
			p.s.st = st
		}
		u.mu.Unlock()
		if on && err == nil && serr == nil && !st.Exists && !st.Busy && st.Error == "" {
			p.setupNow()
		}
	})
}

func (p *compatPage) setupNow() {
	p.run(jobSetup, func(g Glibc, line func(string)) error { return g.Create(line) })
}

func (p *compatPage) pullNow() {
	p.run(jobPull, func(g Glibc, line func(string)) error { return g.Pull(line) })
}

// askReset confirms, then resets.
func (p *compatPage) askReset() {
	st := p.state().st
	p.u.ask("Reset the glibc environment?",
		"Isotope removes the container isotope-glibc and creates it again from "+imageName(st.Image)+
			". What you installed in it is lost, and so are the unpacked AppImages; your home folder is not touched. This can take a few minutes.",
		choice{Label: "Cancel", Kind: guikit.ButtonSecondary},
		choice{Label: "Reset", Kind: guikit.ButtonDanger, Do: func() {
			p.run(jobReset, func(g Glibc, line func(string)) error { return g.Reset(line) })
		}})
}

// run starts a job that can take minutes. Its lines show under a spinner;
// a failure stays on the page, and reset's in-use refusal shows as a
// message.
func (p *compatPage) run(job string, f func(Glibc, func(string)) error) {
	u, g := p.u, p.u.hooks.Glibc
	u.mu.Lock()
	if g == nil || p.s.working != "" {
		u.mu.Unlock()
		return
	}
	p.s.working, p.s.lines, p.s.fail, p.s.inUse = job, nil, "", ""
	u.mu.Unlock()
	line := func(l string) {
		u.mu.Lock()
		p.s.lines = append(p.s.lines, l)
		if len(p.s.lines) > maxLines {
			p.s.lines = p.s.lines[len(p.s.lines)-maxLines:]
		}
		u.mu.Unlock()
		u.hooks.Invalidate()
	}
	u.async(func() {
		err := f(g, line)
		st, serr := g.Status()
		u.mu.Lock()
		defer u.mu.Unlock()
		p.s.working, p.s.lines = "", nil
		if serr == nil {
			p.s.st, p.s.loaded, p.s.loadErr = st, true, ""
		}
		var inUse glibcInUse
		switch {
		case err == nil:
		case errors.As(err, &inUse):
			p.s.inUse = inUse.msg
		default:
			p.s.fail = err.Error()
		}
	})
}

func (p *compatPage) Update(gtx layout.Context) {
	if p.auto.Clicked(gtx) {
		p.toggleAuto()
	}
	if p.setup.Clicked(gtx) {
		p.setupNow()
	}
	if p.pull.Clicked(gtx) {
		p.pullNow()
	}
	if p.reset.Clicked(gtx) {
		p.askReset()
	}
}

// compatView is what the Glibc environment row says and offers.
type compatView struct {
	Title, Sub   string
	Setup, Reset bool
	// Pull offers "Download now" with Note: the container is there, its
	// image is not (pruned).
	Pull bool
	Note string
}

func compatViewOf(st GlibcStatus) compatView {
	switch {
	case st.Error != "":
		return compatView{Title: "Not available", Sub: st.Error}
	case st.Busy:
		return compatView{Title: "Being set up",
			Sub: "Another window or a program's first run is setting up or resetting it. Open this page again to see the result."}
	case !st.Exists:
		return compatView{Title: "Not set up yet", Setup: true,
			Sub: "Isotope sets it up the first time you start such a program, or now. It downloads " + imageName(st.Image) + " and can take a few minutes."}
	}
	sub := imageName(st.Image)
	if st.SizeBytes > 0 {
		sub += " · " + humanSize(uint64(st.SizeBytes)) + " on disk"
	}
	v := compatView{Title: "Ready", Sub: sub, Reset: true}
	if !st.ImagePresent {
		v.Pull = true
		v.Note = "The " + imageFamily(st.Image) + " image is not on this computer. The container works without it; downloading it now means a reset does not have to."
	}
	return v
}

// imageFamily is "Ubuntu" for Docker Hub's ubuntu, "image X" otherwise, so
// that "The X image" reads right.
func imageFamily(ref string) string {
	if strings.HasPrefix(imageName(ref), "Ubuntu ") {
		return "Ubuntu"
	}
	return ref
}

// imageName names an image for people: "Ubuntu 26.04 (ref)" for Docker
// Hub's ubuntu, the reference itself otherwise.
func imageName(ref string) string {
	repo, tag, ok := strings.Cut(ref[strings.LastIndex(ref, "/")+1:], ":")
	if ok && repo == "ubuntu" && (ref == "ubuntu:"+tag || strings.HasSuffix(ref, "/library/ubuntu:"+tag)) {
		return "Ubuntu " + tag + " (" + ref + ")"
	}
	return ref
}

func (p *compatPage) Body(gtx layout.Context) layout.Dimensions {
	u := p.u
	s := p.state()
	return u.scrolled(gtx, &p.list, func(gtx layout.Context) layout.Dimensions {
		var c col
		c.add(u.Heading("Compatibility", "Run programs made for other Linux systems on Isotope."))
		c.gap(24)
		c.add(u.caps("GLIBC COMPATIBILITY"))
		c.gap(10)
		c.add(u.para("Isotope is built on musl. Most Linux programs you download, and AppImages, are built for glibc instead; Isotope runs them in a container for your user, an Ubuntu system that shares your home folder."))
		c.gap(14)
		if !s.loaded {
			if s.loadErr != "" {
				c.add(u.errorBox(s.loadErr))
			} else {
				c.add(u.spinnerLine("Reading the glibc environment…"))
			}
			return c.layout(gtx)
		}
		c.add(func(gtx layout.Context) layout.Dimensions {
			return u.CheckCard(gtx, &p.auto, s.st.Auto, "Run programs built for other Linux systems automatically",
				"When you start a program built for glibc, it runs in the container. Turned off, it does not start; `isotope glibc run` still runs it.")
		})
		c.gap(14)
		v := compatViewOf(s.st)
		c.add(u.panel(func(gtx layout.Context) layout.Dimensions {
			var rows col
			var right layout.Widget
			busy := s.working != ""
			switch {
			case v.Setup:
				right = u.buttons(guikit.Action{Click: &p.setup, Kind: guikit.ButtonPrimary, Label: "Set up now", Disabled: busy})
			case v.Reset:
				acts := []guikit.Action{{Click: &p.reset, Kind: guikit.ButtonSecondary, Label: "Reset…", Disabled: busy}}
				if v.Pull {
					acts = append([]guikit.Action{{Click: &p.pull, Kind: guikit.ButtonPrimary, Label: "Download now", Disabled: busy}}, acts...)
				}
				right = u.buttons(acts...)
			}
			rows.add(u.row("Glibc environment", v.Title, right))
			rows.gap(6)
			rows.add(u.dim(v.Sub))
			if v.Note != "" {
				rows.gap(6)
				rows.add(u.dim(v.Note))
			}
			if busy {
				rows.gap(14)
				txt := "Setting up the glibc environment… This can take a few minutes; it goes on if you close this window."
				if s.working == jobPull {
					txt = "Downloading the image… This can take a few minutes; it goes on if you close this window."
				}
				if s.working == jobReset {
					txt = "Resetting the glibc environment… This can take a few minutes; it goes on if you close this window."
				}
				rows.add(u.spinnerLine(txt))
				rows.gap(10)
				rows.add(u.log(s.lines, 6))
			}
			return rows.layout(gtx)
		}))
		if s.inUse != "" {
			c.gap(14)
			c.add(u.notice(s.inUse))
		}
		if s.fail != "" {
			c.gap(14)
			c.add(u.errorBox(s.fail))
		}
		return c.layout(gtx)
	})
}
