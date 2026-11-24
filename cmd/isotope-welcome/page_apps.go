package main

import (
	"slices"
	"sync"
	"time"

	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/widget"

	"github.com/kairos-community/isotope/pkg/guikit"
	"github.com/kairos-community/isotope/pkg/i18n"
)

func init() { pageMakers[PApps] = func(u *UI) page { return &appsPage{basePage: basePage{u}} } }

// appsStatus is what the page knows about the network and Flathub.
type appsStatus int

const (
	appsChecking  appsStatus = iota // asking
	appsReady                       // Flathub can be used
	appsOffline                     // no network
	appsNoFlathub                   // a network, but no usable flathub remote
)

// appsPage offers the curated apps: one browser (or none) and any number
// of essentials. Continue starts the installs in the background and goes
// on; the sidebar and Ready show the progress. Without a network, or
// without a usable Flathub, it says so, with Try again and Skip.
type appsPage struct {
	basePage
	list widget.List

	mu        sync.Mutex // the checks run in the background
	status    appsStatus
	installed map[string]bool // already on the system
	sel       map[string]bool // ticked, and not installed
	started   bool            // the first check was started
	seen      int             // the batch the list last took the result of

	rows  map[string]*widget.Clickable
	none  widget.Clickable // "No browser"
	retry widget.Clickable
}

// load asks the system, then makes the selection: the list's defaults
// when reset, else the apps of the last batch that failed. It blocks; the
// page runs it in the background.
func (p *appsPage) load(reset bool) {
	u := p.u
	status, installed := appsReady, map[string]bool{}
	if r := u.hooks.Runner; r != nil {
		switch {
		case !Online(r):
			status = appsOffline
		case !FlathubReady(r):
			status = appsNoFlathub
		}
		if got, err := InstalledApps(r); err == nil {
			installed = got
		}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.status, p.installed = status, installed
	switch {
	case reset || p.sel == nil:
		p.sel = map[string]bool{}
		for _, a := range u.appList {
			p.sel[a.ID] = a.Preselected && !installed[a.ID]
		}
		// An installed browser counts as the browser choice.
		for _, a := range u.appList {
			if a.Group == "browser" && installed[a.ID] {
				p.clearBrowsers()
				break
			}
		}
	case u.installer != nil:
		_, failed, _ := u.installer.Result()
		p.sel = map[string]bool{}
		for _, id := range failed {
			p.sel[id] = !installed[id]
		}
	default:
		for id := range p.sel {
			if installed[id] {
				delete(p.sel, id)
			}
		}
	}
}

// background runs load without blocking the frame.
func (p *appsPage) background(reset bool) {
	p.mu.Lock()
	p.status = appsChecking
	p.mu.Unlock()
	go func() {
		p.load(reset)
		if p.u.hooks.Invalidate != nil {
			p.u.hooks.Invalidate()
		}
	}()
}

// unavailable: no network or no Flathub, so the apps cannot be offered.
func (p *appsPage) unavailable() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.status == appsOffline || p.status == appsNoFlathub
}

// checking: the first answer about the network is not in yet (nm-online
// waits up to 5 s when offline).
func (p *appsPage) checking() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.status == appsChecking
}

// selected are the ticked apps in the list's order, the browser first.
func (p *appsPage) selected() []string {
	var ids []string
	for _, a := range p.u.appList {
		if p.sel[a.ID] && !p.installed[a.ID] {
			ids = append(ids, a.ID)
		}
	}
	return ids
}

func (p *appsPage) group(id string) string {
	for _, a := range p.u.appList {
		if a.ID == id {
			return a.Group
		}
	}
	return ""
}

// toggle flips an app. A browser is a radio button: it replaces the one
// before. An installed app stays as it is.
func (p *appsPage) toggle(id string) {
	if p.installed[id] {
		return
	}
	if p.sel[id] {
		if p.group(id) != "browser" {
			p.sel[id] = false
		}
		return
	}
	if p.group(id) == "browser" {
		p.clearBrowsers()
	}
	p.sel[id] = true
}

// clearBrowsers is "No browser".
func (p *appsPage) clearBrowsers() {
	for _, a := range p.u.appList {
		if a.Group == "browser" {
			p.sel[a.ID] = false
		}
	}
}

// browserChosen: a browser is ticked or installed.
func (p *appsPage) browserChosen() bool {
	for _, a := range p.u.appList {
		if a.Group == "browser" && (p.sel[a.ID] || p.installed[a.ID]) {
			return true
		}
	}
	return false
}

func (p *appsPage) row(id string) *widget.Clickable {
	if p.rows == nil {
		p.rows = map[string]*widget.Clickable{}
	}
	if p.rows[id] == nil {
		p.rows[id] = new(widget.Clickable)
	}
	return p.rows[id]
}

func (p *appsPage) Footer(n nav) (leading, trailing []guikit.Action) {
	if n.Back != nil {
		leading = append(leading, *n.Back)
	}
	if p.unavailable() {
		return leading, []guikit.Action{*n.Skip, {Click: &p.retry, Kind: guikit.ButtonPrimary, Label: i18n.T("Try again")}}
	}
	return leading, []guikit.Action{*n.Skip, *n.Continue}
}

func (p *appsPage) Update(gtx layout.Context) {
	u := p.u
	if !p.started {
		p.started = true
		if u.hooks.Runner != nil {
			p.background(true)
		} else {
			p.load(true)
		}
	}
	if u.installer != nil && !u.installer.Running() && p.seen != u.batches {
		// The batch ended while the list was away: take its result.
		p.seen = u.batches
		p.background(false)
	}
	if p.retry.Clicked(gtx) {
		p.background(true)
		u.focus = &u.cont
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.none.Clicked(gtx) && p.sel != nil {
		p.clearBrowsers()
	}
	for _, a := range u.appList {
		if p.row(a.ID).Clicked(gtx) && p.sel != nil {
			p.toggle(a.ID)
		}
	}
}

// Continue starts the installs of the ticked apps and goes on.
func (p *appsPage) Continue() {
	u := p.u
	p.mu.Lock()
	status, ids := p.status, p.selected()
	p.mu.Unlock()
	switch {
	case u.installing():
		u.flow.Next()
	case status == appsChecking:
		// still asking: Continue waits for the answer
	case status != appsReady || len(ids) == 0 || u.hooks.Runner == nil:
		u.flow.Next()
	default:
		u.startInstall(ids)
		u.flow.Apply()
		u.flow.Next()
	}
}

// startInstall starts the batch ids; the installs outlive the window.
func (u *UI) startInstall(ids []string) {
	if u.installer == nil {
		u.installer = NewInstaller(u.hooks.Runner, u.appList)
		u.installer.OnApp = func(id string, ok bool) {
			if ok && u.hooks.AppInstalled != nil {
				u.hooks.AppInstalled(id)
			}
		}
	}
	u.appsPrior = slices.Clone(u.flow.Outcome.AppsInstalled)
	u.flow.Outcome.AppsFailed = nil
	u.appsPending = false
	u.batches++
	u.installer.Start(ids)
}

// installing: a batch is running.
func (u *UI) installing() bool { return u.installer != nil && u.installer.Running() }

// short is the name of the app id in the Outcome and the notifications.
func (u *UI) short(id string) string {
	for _, a := range u.appList {
		if a.ID == id {
			return a.Short()
		}
	}
	return id
}

func (u *UI) shorts(ids []string) []string {
	var out []string
	for _, id := range ids {
		out = append(out, u.short(id))
	}
	return out
}

// syncApps copies the result of a finished batch to the Outcome, which
// Ready shows: what installed (earlier batches included) and what failed.
func (u *UI) syncApps() {
	if u.installer == nil {
		return
	}
	ok, failed, fin := u.installer.Result()
	if !fin {
		return
	}
	o := &u.flow.Outcome
	o.AppsInstalled = slices.Clone(u.appsPrior)
	for _, n := range u.shorts(ok) {
		if !slices.Contains(o.AppsInstalled, n) {
			o.AppsInstalled = append(o.AppsInstalled, n)
		}
	}
	o.AppsFailed = u.shorts(failed)
}

// AwaitInstalls is for the window going away: it waits for a batch that is
// still running, and returns what installed and what failed, by name.
// running is false when there was nothing to wait for.
func (u *UI) AwaitInstalls() (ok, failed []string, running bool) {
	if !u.installing() {
		return nil, nil, false
	}
	u.installer.Wait()
	o, f, _ := u.installer.Result()
	return u.shorts(o), u.shorts(f), true
}

func (p *appsPage) Body(gtx layout.Context) layout.Dimensions {
	u := p.u
	p.mu.Lock()
	defer p.mu.Unlock()
	return u.scrolled(gtx, &p.list, func(gtx layout.Context) layout.Dimensions {
		switch {
		case u.installing():
			return p.progress(gtx)
		case p.status == appsOffline:
			return p.problem(gtx, i18n.T("No network connection."),
				i18n.T("Connect to a network and try again. If you skip, Isotope reminds you at your next login."))
		case p.status == appsNoFlathub:
			return p.problem(gtx, i18n.T("Flathub is not ready."),
				i18n.T("The Flathub remote is missing or disabled for your user. Try again later. If you skip, Isotope reminds you at your next login."))
		case p.status == appsChecking:
			return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
				layout.Rigid(u.Heading(i18n.T("Install apps"), i18n.T("Checking the network and Flathub…"))))
		}
		return p.choices(gtx)
	})
}

func (p *appsPage) problem(gtx layout.Context, what, how string) layout.Dimensions {
	u := p.u
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(u.Heading(i18n.T("Install apps"), i18n.T("Isotope includes no browser. Its apps come from Flathub."))),
		guikit.VGap(22),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions { return u.Warning(gtx, what+" "+how) }),
	)
}

// progress is the page while a batch runs.
func (p *appsPage) progress(gtx layout.Context) layout.Dimensions {
	u := p.u
	gtx.Execute(op.InvalidateCmd{At: u.now().Add(40 * time.Millisecond)})
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(u.Heading(i18n.T("Installing apps"), i18n.T("The apps install in the background. You can go on; Isotope tells you when they are done."))),
		guikit.VGap(26),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions { return u.installProgress(gtx) }),
	)
}

// installProgress is the bar and the line under it: what is being
// installed and how far the batch is.
func (u *UI) installProgress(gtx layout.Context) layout.Dimensions {
	done, total, cur := u.installer.Progress()
	f := float32(0)
	if total > 0 {
		f = float32(done) / float32(total)
	}
	name := u.installer.Label(cur)
	// TRANSLATORS: %[1]s is an app's name; then its place in the batch and the number of apps.
	line := i18n.Tf("Installing %[1]s (%[2]d of %[3]d)", name, min(done+1, total), total)
	if cur == "" {
		// TRANSLATORS: apps installed of the batch's apps.
		line = i18n.Tf("%[1]d of %[2]d installed", done, total)
	}
	phase := float32(u.now().UnixMilli()%1200) / 1200
	gtx.Constraints.Max.X = min(gtx.Constraints.Max.X, guikit.Dp(gtx, 520))
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(func(gtx layout.Context) layout.Dimensions { return guikit.ProgressBar(gtx, f) }),
		guikit.VGap(12),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
				layout.Rigid(func(gtx layout.Context) layout.Dimensions { return guikit.Spinner(gtx, 16, phase) }),
				layout.Rigid(layout.Spacer{Width: 10}.Layout),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return u.Label(gtx, guikit.StSmall, guikit.ColText2, line)
				}))
		}),
	)
}

// choices is the list: the browsers at the left, the essentials at the right.
func (p *appsPage) choices(gtx layout.Context) layout.Dimensions {
	u := p.u
	kids := []layout.FlexChild{
		layout.Rigid(u.Heading(i18n.T("Install apps"), i18n.T("Isotope includes no browser. These apps come from Flathub and install for your user only."))),
		guikit.VGap(20),
	}
	if u.installer != nil {
		if _, failed, fin := u.installer.Result(); fin && len(failed) > 0 {
			kids = append(kids, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return u.Warning(gtx, i18n.Tf("Could not install %s. They are ticked: Continue tries again.", list(u.shorts(failed))))
			}), guikit.VGap(16))
		}
	}
	kids = append(kids, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
		gtx.Constraints.Min.X = gtx.Constraints.Max.X
		return layout.Flex{}.Layout(gtx,
			layout.Flexed(1, func(gtx layout.Context) layout.Dimensions { return p.column(gtx, "browser") }),
			layout.Rigid(layout.Spacer{Width: 18}.Layout),
			layout.Flexed(1, func(gtx layout.Context) layout.Dimensions { return p.column(gtx, "extra") }),
		)
	}))
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx, kids...)
}

// column is one group: its heading and a card per app.
func (p *appsPage) column(gtx layout.Context, group string) layout.Dimensions {
	u := p.u
	title := i18n.T("Essentials")
	if group == "browser" {
		title = i18n.T("Browser (pick one)")
	}
	kids := []layout.FlexChild{
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return u.Label(gtx, guikit.StSmall600, guikit.ColDim, title)
		}),
		guikit.VGap(10),
	}
	add := func(w layout.Widget) {
		kids = append(kids, layout.Rigid(w), guikit.VGap(8))
	}
	for _, a := range u.appList {
		if a.Group != group {
			continue
		}
		add(func(gtx layout.Context) layout.Dimensions { return p.card(gtx, a) })
	}
	if group == "browser" {
		add(func(gtx layout.Context) layout.Dimensions {
			return p.cardOf(gtx, &p.none, !p.browserChosen(), true, false, i18n.T("No browser"))
		})
	}
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx, kids...)
}

func (p *appsPage) card(gtx layout.Context, a App) layout.Dimensions {
	return p.cardOf(gtx, p.row(a.ID), p.sel[a.ID], a.Group == "browser", p.installed[a.ID], a.Label)
}

// cardOf is one compact card: a radio or a checkbox, and the label. An
// installed app is greyed with a tick, and says so.
func (p *appsPage) cardOf(gtx layout.Context, c *widget.Clickable, on, radio, installed bool, label string) layout.Dimensions {
	u := p.u
	gtx.Constraints.Min.X = gtx.Constraints.Max.X
	return u.Card(gtx, c, on && !installed, layout.Inset{Top: 12, Bottom: 12, Left: 14, Right: 14}, func(gtx layout.Context) layout.Dimensions {
		gtx.Constraints.Min.X = gtx.Constraints.Max.X
		col := guikit.ColText
		if installed {
			col = guikit.ColDim
		}
		return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				switch {
				case installed:
					return guikit.CheckIcon(gtx, 18)
				case radio:
					return guikit.Radio(gtx, on)
				}
				return guikit.Checkbox(gtx, on)
			}),
			layout.Rigid(layout.Spacer{Width: 12}.Layout),
			layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
				return u.Label(gtx, guikit.StBody, col, label)
			}),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				if !installed {
					return layout.Dimensions{}
				}
				return layout.Inset{Left: 10}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
					// TRANSLATORS: an app that is already installed.
					return u.Label(gtx, guikit.StTiny, guikit.ColDim, i18n.X("app", "Installed"))
				})
			}),
		)
	})
}
