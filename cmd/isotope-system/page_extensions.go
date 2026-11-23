package main

import (
	"strings"

	"gioui.org/layout"
	"gioui.org/widget"

	"github.com/kairos-community/isotope/pkg/guikit"
)

func init() {
	pageMakers[PageExtensions] = func(u *UI) pageView {
		p := &extPage{u: u, clicks: map[string]*extClicks{}, filter: guikit.NewField(false)}
		u.onJobEnd(p.jobEnded)
		return p
	}
}

// extPage: the installed system extensions and the catalogs' ones, with
// install, enable, disable and remove, as `isotope ext` does. Each change
// is a job in isotope-updated (polkit's extensions action).
type extPage struct {
	u      *UI
	list   widget.List
	filter *guikit.Field

	// Under u.mu.
	installed []Extension
	catalog   []CatalogEntry
	loading   bool
	loadErr   string
	// retry is the change that started the running job, to repeat it with
	// force after a conflict; what names it.
	retry func(force bool) error
	what  string

	clicks map[string]*extClicks
	reload widget.Clickable
}

type extClicks struct{ toggle, remove, install widget.Clickable }

func (p *extPage) Shown() { p.load() }

func (p *extPage) load() {
	u, d := p.u, p.u.hooks.Daemon
	if d == nil {
		return
	}
	u.mu.Lock()
	if p.loading {
		u.mu.Unlock()
		return
	}
	p.loading, p.loadErr = true, ""
	u.mu.Unlock()
	u.async(func() {
		inst, ierr := d.Extensions()
		cat, cerr := d.Catalog()
		u.mu.Lock()
		defer u.mu.Unlock()
		p.loading = false
		if ierr == nil {
			p.installed = inst
		}
		if cerr == nil {
			p.catalog = cat
		}
		switch {
		case ierr != nil:
			p.loadErr = "Could not list the installed extensions: " + Describe(ierr)
		case cerr != nil:
			p.loadErr = "Could not read the catalogs (is the network up?): " + Describe(cerr)
		}
	})
}

func (p *extPage) clicksFor(name string) *extClicks {
	c, ok := p.clicks[name]
	if !ok {
		c = &extClicks{}
		p.clicks[name] = c
	}
	return c
}

// change starts an extension job; the outcome comes in jobEnded.
func (p *extPage) change(what string, f func(force bool) error) {
	u := p.u
	if u.hooks.Daemon == nil {
		return
	}
	u.mu.Lock()
	p.retry, p.what = f, what
	u.mu.Unlock()
	u.async(func() {
		if err := f(false); err != nil {
			u.mu.Lock()
			p.retry = nil
			u.mu.Unlock()
			u.tell("Could not "+what, Describe(err))
		}
	})
}

func (p *extPage) jobEnded(prev string, st Status) {
	if prev != StateExtension {
		return
	}
	u := p.u
	u.mu.Lock()
	retry, what := p.retry, p.what
	p.retry = nil
	u.mu.Unlock()
	switch st.ExtResult {
	case "conflict":
		if retry == nil {
			break
		}
		u.ask("Replace system files?", st.ExtDetail+"\n\nInstalling it anyway replaces those files of the running system while it is enabled.",
			choice{Label: "Cancel", Kind: guikit.ButtonSecondary},
			choice{Label: "Install anyway", Kind: guikit.ButtonDanger, Do: func() {
				u.mu.Lock()
				p.retry, p.what = retry, what
				u.mu.Unlock()
				u.async(func() {
					if err := retry(true); err != nil {
						u.tell("Could not "+what, Describe(err))
					}
				})
			}})
	case "failed":
		u.tell("Could not "+what, st.ExtDetail)
	}
	p.load()
}

func (p *extPage) Update(gtx layout.Context) {
	d := p.u.hooks.Daemon
	if p.reload.Clicked(gtx) {
		p.load()
	}
	p.filter.Submitted(gtx)
	for name, c := range p.clicks {
		name := name
		if c.install.Clicked(gtx) {
			p.change("install "+name, func(force bool) error { return d.InstallExtension(name, "", force) })
		}
		if c.remove.Clicked(gtx) {
			p.u.ask("Remove "+name+"?", "The extension is turned off and deleted. You can install it again from the catalog.",
				choice{Label: "Cancel", Kind: guikit.ButtonSecondary},
				choice{Label: "Remove", Kind: guikit.ButtonDanger, Do: func() {
					p.change("remove "+name, func(bool) error { return d.RemoveExtension(name) })
				}})
		}
		if c.toggle.Clicked(gtx) {
			on := true
			p.u.mu.Lock()
			for _, x := range p.installed {
				if x.Name == name {
					on = !x.Enabled
				}
			}
			p.u.mu.Unlock()
			verb := "turn on "
			if !on {
				verb = "turn off "
			}
			p.change(verb+name, func(force bool) error { return d.SetExtensionEnabled(name, on, force) })
		}
	}
}

func (p *extPage) Footer() (leading, trailing []guikit.Action) {
	p.u.mu.Lock()
	loading := p.loading
	p.u.mu.Unlock()
	return nil, []guikit.Action{{Click: &p.reload, Kind: guikit.ButtonSecondary, Label: "Refresh", Disabled: loading || p.u.hooks.Daemon == nil}}
}

func (p *extPage) Body(gtx layout.Context) layout.Dimensions {
	u := p.u
	st, ok, why := u.status()
	u.mu.Lock()
	inst, avail := ExtRows(p.installed, p.catalog, p.filter.Editor.Text())
	loading, loadErr := p.loading, p.loadErr
	u.mu.Unlock()
	busy := !ok || st.Busy() || !u.sys.Admin
	lines := u.progress()
	return u.scrolled(gtx, &p.list, func(gtx layout.Context) layout.Dimensions {
		var c col
		c.add(u.Heading("Extensions", "System extensions add programs to the read-only system: drivers, tools and services that are not in the image. They are checked against the running system before they are turned on."))
		c.gap(20)
		if !ok {
			c.add(u.errorBox(orUnknown(why)))
			c.gap(12)
		}
		if !u.sys.Admin {
			c.add(u.notice("Only an administrator (a member of the admin group) can change extensions."))
			c.gap(12)
		}
		if loadErr != "" {
			c.add(u.errorBox(loadErr))
			c.gap(12)
		}
		if st.State == StateExtension {
			c.add(u.spinnerLine(st.Detail))
			c.gap(10)
			c.add(u.log(lines, 6))
			c.gap(16)
		}
		c.add(func(gtx layout.Context) layout.Dimensions {
			return u.Input(gtx, p.filter, guikit.StBody, 38, "Search  ", nil)
		})
		c.gap(22)
		c.add(u.caps("INSTALLED"))
		c.gap(10)
		if len(inst) == 0 {
			c.add(u.dim("No extension is installed."))
		} else {
			c.add(u.panel(func(gtx layout.Context) layout.Dimensions {
				var rows col
				for i, r := range inst {
					if i > 0 {
						rows.gap(10)
					}
					k := p.clicksFor(r.Name)
					toggle := "Turn on"
					if r.Enabled {
						toggle = "Turn off"
					}
					sub := r.Description
					if !r.Enabled {
						sub = strings.TrimSpace("Off. " + sub)
					}
					rows.add(u.row(extTitle(r), sub, u.buttons(
						guikit.Action{Click: &k.toggle, Kind: guikit.ButtonSecondary, Label: toggle, Disabled: busy},
						guikit.Action{Click: &k.remove, Kind: guikit.ButtonGhost, Label: "Remove", Disabled: busy},
					)))
				}
				return rows.layout(gtx)
			}))
		}
		c.gap(26)
		c.add(u.caps("AVAILABLE"))
		c.gap(10)
		switch {
		case loading && len(avail) == 0:
			c.add(u.spinnerLine("Reading the catalogs…"))
		case len(avail) == 0:
			c.add(u.dim("Nothing more in the catalogs."))
		default:
			c.add(u.panel(func(gtx layout.Context) layout.Dimensions {
				var rows col
				for i, r := range avail {
					if i > 0 {
						rows.gap(10)
					}
					k := p.clicksFor(r.Name)
					rows.add(u.row(extTitle(r), r.Description, u.buttons(
						guikit.Action{Click: &k.install, Kind: guikit.ButtonSecondary, Label: "Install", Disabled: busy},
					)))
				}
				return rows.layout(gtx)
			}))
		}
		return c.layout(gtx)
	})
}

func extTitle(r ExtRow) string {
	t := r.Name
	if r.Title != "" && !strings.EqualFold(r.Title, r.Name) {
		t = r.Title + " (" + r.Name + ")"
	}
	if r.Latest != "" {
		t += " " + r.Latest
	}
	return t
}
