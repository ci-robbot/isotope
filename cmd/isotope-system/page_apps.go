package main

import (
	"hash/fnv"
	"image"
	"image/color"
	"strconv"
	"strings"

	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/unit"
	"gioui.org/widget"

	"github.com/kairos-community/isotope/pkg/flatpak"
	"github.com/kairos-community/isotope/pkg/guikit"
	"github.com/kairos-community/isotope/pkg/i18n"
)

func init() {
	pageMakers[PageApps] = func(u *UI) pageView { return newAppsPage(u) }
}

// The most rows a list shows: a search or a category of Flathub can have
// hundreds; typing more narrows it.
const appsMaxRows = 30

// The job kinds: one at a time per window.
const (
	jobInstall = "install"
	jobUpdate  = "update"
	jobRemove  = "remove"
)

// appsJob is the install, update or removal that runs.
type appsJob struct {
	Kind, ID, Name, Icon string
	Progress             flatpak.Progress
	proc                 flatpak.Process
	cancelled            bool
}

// appDetail is the details view of one app.
type appDetail struct {
	ID      string
	Info    flatpak.RemoteInfo
	Summary flatpak.Summary
	// Loaded: the permissions are read; FromInstalled: from the installed
	// app's metadata, not Flathub's.
	Loaded, FromInstalled bool
	Err                   string
}

// appsState is what the page shows, under u.mu.
type appsState struct {
	checked, available, flathub bool
	tab, chip                   string
	// detail is the app whose details are open, "" for none.
	detail *appDetail

	cat                    *flatpak.Catalog
	catErr                 string
	catLoaded, refreshing  bool
	picks                  []string
	installed              []flatpak.Installed
	instLoaded             bool
	instErr                string
	pending                []flatpak.Pending
	upLoaded, upLoading    bool
	upErr                  string
	running                map[string]bool
	exts                   []Extension
	extCat                 []CatalogEntry
	auto, hasLast, autoErr bool
	last                   flatpak.LastRun
	job                    *appsJob
}

// appClicks are one row's buttons.
type appClicks struct {
	row                  guikit.Row
	main, open, remove   widget.Clickable
	review, update, more widget.Clickable
}

// appsPage: Flathub's apps for the user, in three tabs (Browse, Installed,
// Updates), a search that also finds system extensions, and an app's
// details with what it can reach.
type appsPage struct {
	u      *UI
	list   widget.List
	search *guikit.Field

	tabs                        [3]widget.Clickable
	chips                       []widget.Clickable
	back, autoToggle, updateAll widget.Clickable
	extLink, detailMain         widget.Clickable
	detailOpen, detailRemove    widget.Clickable
	cancel                      widget.Clickable
	rows                        map[string]*appClicks
	icons                       map[string]*appIcon
	query                       string
	results                     []flatpak.App
	resultsOf                   *flatpak.Catalog
	removeData                  widget.Clickable
	removeDataOn                bool
	extOpen                     map[string]*widget.Clickable
	refreshStarted              bool
	starting                    bool

	// st is under u.mu.
	st appsState
}

// appIcon is a decoded icon, or none.
type appIcon struct {
	op paint.ImageOp
	ok bool
}

func newAppsPage(u *UI) *appsPage {
	p := &appsPage{u: u, search: guikit.NewField(false), rows: map[string]*appClicks{}, icons: map[string]*appIcon{},
		chips: make([]widget.Clickable, len(flatpak.Categories)), extOpen: map[string]*widget.Clickable{}}
	p.s().tab = tabBrowse
	return p
}

// s is the state; callers hold u.mu, or own the page before it is shown.
func (p *appsPage) s() *appsState { return &p.st }

// SetTab opens a tab (`isotope system apps updates`), closing the details.
func (p *appsPage) SetTab(tab string) {
	p.u.mu.Lock()
	s := p.s()
	for _, t := range appTabs {
		if t == tab {
			s.tab, s.detail = tab, nil
		}
	}
	p.u.mu.Unlock()
}

func (p *appsPage) Footer() (leading, trailing []guikit.Action) { return nil, nil }

func (p *appsPage) Shown() {
	u, a := p.u, p.u.hooks.Apps
	if a == nil {
		return
	}
	u.mu.Lock()
	first := !p.s().checked
	starting := p.starting
	p.starting = true
	u.mu.Unlock()
	if first && starting {
		return
	}
	if !first {
		p.loadInstalled()
		p.loadUpdates()
		p.loadAuto()
		p.loadExtensions()
		return
	}
	u.async(func() {
		avail := a.Available()
		ready := avail && a.FlathubReady()
		picks := a.Picks()
		u.mu.Lock()
		s := p.s()
		s.checked, s.available, s.flathub, s.picks = true, avail, ready, picks
		u.mu.Unlock()
		if !avail {
			return
		}
		p.loadAuto()
		p.loadInstalled()
		p.loadExtensions()
		if ready {
			p.loadCatalog()
			p.loadUpdates()
		}
	})
}

// loadCatalog reads the catalog, and refreshes it in the background when
// it is missing or older than a day.
func (p *appsPage) loadCatalog() {
	u, a := p.u, p.u.hooks.Apps
	u.async(func() {
		cat, err := a.Catalog()
		age, ok := a.CatalogAge()
		u.mu.Lock()
		s := p.s()
		s.catLoaded = true
		if err == nil {
			s.cat, s.catErr = cat, ""
		} else if s.cat == nil {
			s.catErr = err.Error()
		}
		stale := (!ok || age > catalogMaxAge || err != nil) && !p.refreshStarted
		if stale {
			p.refreshStarted, s.refreshing = true, true
		}
		u.mu.Unlock()
		if !stale {
			return
		}
		rerr := a.RefreshCatalog()
		cat, err = a.Catalog()
		u.mu.Lock()
		s = p.s()
		s.refreshing = false
		switch {
		case err == nil:
			s.cat, s.catErr = cat, ""
		case rerr != nil && s.cat == nil:
			s.catErr = i18n.Tf("Could not download Flathub's catalog: %s", rerr.Error())
		}
		u.mu.Unlock()
	})
}

func (p *appsPage) loadInstalled() {
	u, a := p.u, p.u.hooks.Apps
	u.async(func() {
		list, err := a.Installed()
		running, _ := a.Running()
		u.mu.Lock()
		defer u.mu.Unlock()
		s := p.s()
		s.instLoaded = true
		if err != nil {
			s.instErr = i18n.Tf("Could not list your apps: %s", err.Error())
			return
		}
		s.installed, s.instErr, s.running = list, "", running
	})
}

// loadUpdates lists the pending updates and reviews what each asks for.
func (p *appsPage) loadUpdates() {
	u, a := p.u, p.u.hooks.Apps
	u.mu.Lock()
	s := p.s()
	if !s.flathub || s.upLoading {
		u.mu.Unlock()
		return
	}
	s.upLoading = true
	u.mu.Unlock()
	u.async(func() {
		ups, err := a.Updates()
		var ps []flatpak.Pending
		if err == nil {
			ps = a.Review(ups)
		}
		running, _ := a.Running()
		u.mu.Lock()
		defer u.mu.Unlock()
		s := p.s()
		s.upLoading, s.upLoaded = false, true
		if err != nil {
			s.upErr = i18n.Tf("Could not look for updates: %s", err.Error())
			return
		}
		s.pending, s.upErr = ps, ""
		if running != nil {
			s.running = running
		}
		u.appUpdates, u.appUpdatesKnown = len(ps), true
	})
}

func (p *appsPage) loadAuto() {
	u, a := p.u, p.u.hooks.Apps
	u.async(func() {
		on := a.Auto()
		last, has := a.LastRun()
		u.mu.Lock()
		s := p.s()
		s.auto, s.last, s.hasLast = on, last, has
		u.mu.Unlock()
	})
}

// loadExtensions reads the Extensions page's data: the installed ones and
// the catalogs, for the search and the Installed tab's last line.
func (p *appsPage) loadExtensions() {
	u, d := p.u, p.u.hooks.Daemon
	if d == nil {
		return
	}
	u.async(func() {
		inst, ierr := d.Extensions()
		cat, cerr := d.Catalog()
		u.mu.Lock()
		defer u.mu.Unlock()
		s := p.s()
		if ierr == nil {
			s.exts = inst
		}
		if cerr == nil {
			s.extCat = cat
		}
	})
}

// openDetail shows an app's details and reads what it can reach: the
// installed metadata for an installed app, Flathub's otherwise.
func (p *appsPage) openDetail(id string) {
	u, a := p.u, p.u.hooks.Apps
	u.mu.Lock()
	s := p.s()
	inst := p.installedLocked(id)
	s.detail = &appDetail{ID: id, FromInstalled: inst != nil}
	flathub := s.flathub
	d := s.detail
	u.mu.Unlock()
	p.list.Position = layout.Position{}
	u.async(func() {
		var info flatpak.RemoteInfo
		var ierr error
		if flathub {
			info, ierr = a.RemoteInfo(id)
		}
		var md flatpak.Metadata
		var merr error
		if inst != nil {
			md, merr = a.InstalledMetadata(inst.Ref.String())
		} else if flathub {
			md, merr = a.RemoteMetadata(id)
		}
		u.mu.Lock()
		defer u.mu.Unlock()
		if ierr == nil {
			d.Info = info
		}
		d.Loaded = true
		switch {
		case merr != nil:
			d.Err = i18n.Tf("Could not read what it can reach: %s", merr.Error())
		case inst == nil && !flathub:
			d.Err = i18n.T("Flathub is not set up for this user.")
		default:
			d.Summary = flatpak.Summarize(md.Permissions())
		}
	})
}

// installedLocked is the installed app id, nil when it is not.
func (p *appsPage) installedLocked(id string) *flatpak.Installed {
	for i, in := range p.s().installed {
		if in.Ref.IsApp() && in.Ref.ID == id {
			return &p.s().installed[i]
		}
	}
	return nil
}

func (p *appsPage) installedSet() map[string]bool {
	m := map[string]bool{}
	for _, in := range p.s().installed {
		if in.Ref.IsApp() {
			m[in.Ref.ID] = true
		}
	}
	return m
}

// nameOf is an app's name: the catalog's, the installed one's, or its ID.
func (p *appsPage) nameOfLocked(id string) (name, icon string) {
	s := p.s()
	if a, ok := s.cat.Get(id); ok {
		return a.Name, a.Icon
	}
	if in := p.installedLocked(id); in != nil && in.Name != "" {
		return in.Name, ""
	}
	return id, ""
}

// run starts a job, follows its progress and, when it ends, reloads the
// lists; after runs then too (the removal's unused runtimes).
func (p *appsPage) run(job *appsJob, start func(Apps) (flatpak.Process, error), failed string, after func(Apps)) {
	u, a := p.u, p.u.hooks.Apps
	u.mu.Lock()
	s := p.s()
	if s.job != nil {
		u.mu.Unlock()
		return
	}
	job.Progress = flatpak.Progress{Percent: -1, Step: 1, Steps: 1}
	s.job = job
	u.mu.Unlock()
	u.async(func() {
		proc, err := start(a)
		if err == nil {
			u.mu.Lock()
			job.proc = proc
			u.mu.Unlock()
			r := flatpak.NewProgressReader()
			for l := range proc.Lines() {
				pr := r.Line(l)
				u.mu.Lock()
				job.Progress = pr
				u.mu.Unlock()
				u.hooks.Invalidate()
			}
			err = proc.Wait()
		}
		if err == nil && after != nil {
			after(a)
		}
		u.mu.Lock()
		cancelled := job.cancelled
		p.s().job = nil
		u.mu.Unlock()
		if err != nil && !cancelled {
			u.tell(failed, capitalize(err.Error()))
		}
		p.loadInstalled()
		p.loadUpdates()
	})
}

func (p *appsPage) install(id string) {
	p.u.mu.Lock()
	name, icon := p.nameOfLocked(id)
	flathub := p.s().flathub
	p.u.mu.Unlock()
	if !flathub {
		return
	}
	p.run(&appsJob{Kind: jobInstall, ID: id, Name: name, Icon: icon},
		func(a Apps) (flatpak.Process, error) { return a.Install(id) }, i18n.Tf("Could not install %s", name), nil)
}

func (p *appsPage) update(name, icon string, refs ...string) {
	id := ""
	if len(refs) == 1 {
		if r, ok := flatpak.ParseRef(refs[0]); ok {
			id = r.ID
		}
	}
	p.run(&appsJob{Kind: jobUpdate, ID: id, Name: name, Icon: icon},
		func(a Apps) (flatpak.Process, error) { return a.Update(refs...) }, i18n.Tf("Could not update %s", name), nil)
}

func (p *appsPage) open(id string) {
	if err := p.u.hooks.Apps.Run(id); err != nil {
		p.u.tell(i18n.T("Could not open the app"), capitalize(err.Error()))
	}
}

// askRemove asks "Remove NAME?" with the data's size; the data stays
// unless "Also delete its data" is checked.
func (p *appsPage) askRemove(id string) {
	u, a := p.u, p.u.hooks.Apps
	u.mu.Lock()
	name, icon := p.nameOfLocked(id)
	size := ""
	if in := p.installedLocked(id); in != nil {
		size = sizeText(in.Size)
	}
	u.mu.Unlock()
	p.removeDataOn = false
	dataSize := a.DataSize(id)
	path := "~/.var/app/" + id
	form := func(gtx layout.Context) layout.Dimensions {
		if p.removeData.Clicked(gtx) {
			p.removeDataOn = !p.removeDataOn
		}
		sub := path
		if dataSize > 0 {
			sub += " · " + flatpak.FormatSize(dataSize)
		} else {
			// TRANSLATORS: the app's data folder has nothing in it.
			sub += " · " + i18n.X("data folder", "empty")
		}
		return u.CheckCard(gtx, &p.removeData, p.removeDataOn, i18n.T("Also delete its data"), sub)
	}
	u.askForm(i18n.Tf("Remove %s?", name), removeBody(name, size), form, 0,
		choice{Label: i18n.T("Keep"), Kind: guikit.ButtonSecondary},
		choice{Label: i18n.T("Remove"), Kind: guikit.ButtonDanger, Do: func() {
			del := p.removeDataOn
			u.mu.Lock()
			if s := p.s(); s.detail != nil && s.detail.ID == id {
				s.detail = nil
			}
			u.mu.Unlock()
			p.run(&appsJob{Kind: jobRemove, ID: id, Name: name, Icon: icon},
				func(a Apps) (flatpak.Process, error) { return a.Uninstall(id, del) }, i18n.Tf("Could not remove %s", name),
				func(a Apps) { _ = a.RemoveUnused() })
		}})
}

// askReview shows what a held-back update adds, and updates on an OK.
func (p *appsPage) askReview(r updateRow) {
	body := i18n.Tf("The new version asks for more than the version you have:\n\n• %s\n\nAutomatic updates skip it until you update it here. Flatseal can take permissions away later.", strings.Join(r.Added, "\n• "))
	if len(r.Added) == 0 {
		body = i18n.T("Its permissions could not be compared with the version you have. Automatic updates skip it until you update it here.")
	}
	p.u.ask(i18n.Tf("Update %s?", r.Name), body,
		choice{Label: i18n.T("Cancel"), Kind: guikit.ButtonSecondary},
		choice{Label: i18n.T("Update"), Kind: guikit.ButtonPrimary, Do: func() { p.update(r.Name, r.Icon, r.Ref) }})
}

func (p *appsPage) toggleAuto() {
	u, a := p.u, p.u.hooks.Apps
	u.mu.Lock()
	on := !p.s().auto
	u.mu.Unlock()
	u.async(func() {
		err := a.SetAuto(on)
		if err != nil {
			u.tell(i18n.T("Could not change the setting"), capitalize(err.Error()))
		}
		u.mu.Lock()
		p.s().auto = a.Auto()
		u.mu.Unlock()
	})
}

func (p *appsPage) clicks(key string) *appClicks {
	k, ok := p.rows[key]
	if !ok {
		k = &appClicks{}
		p.rows[key] = k
	}
	return k
}

func (p *appsPage) extClick(name string) *widget.Clickable {
	k, ok := p.extOpen[name]
	if !ok {
		k = new(widget.Clickable)
		p.extOpen[name] = k
	}
	return k
}

// appsSnap is a copy of the state for one frame.
type appsSnap struct {
	appsState
	installedIDs map[string]bool
	job          *appsJob
	detail       *appDetail
}

func (p *appsPage) snap() appsSnap {
	p.u.mu.Lock()
	defer p.u.mu.Unlock()
	s := p.s()
	sn := appsSnap{appsState: *s, installedIDs: p.installedSet()}
	if s.job != nil {
		j := *s.job
		sn.job = &j
	}
	if s.detail != nil {
		d := *s.detail
		sn.detail = &d
	}
	return sn
}

func (p *appsPage) Update(gtx layout.Context) {
	u := p.u
	if u.hooks.Apps == nil {
		return
	}
	for i := range p.tabs {
		if p.tabs[i].Clicked(gtx) {
			p.SetTab(appTabs[i])
		}
	}
	for i := range p.chips {
		if p.chips[i].Clicked(gtx) {
			u.mu.Lock()
			s := p.s()
			if s.chip == flatpak.Categories[i].Name {
				s.chip = ""
			} else {
				s.chip = flatpak.Categories[i].Name
			}
			s.tab = tabBrowse
			u.mu.Unlock()
		}
	}
	for {
		_, ok := p.search.Editor.Update(gtx)
		if !ok {
			break
		}
		u.mu.Lock()
		s := p.s()
		s.detail = nil
		if strings.TrimSpace(p.search.Editor.Text()) != "" {
			s.tab = tabBrowse
		}
		u.mu.Unlock()
	}
	sn := p.snap()
	if p.back.Clicked(gtx) {
		u.mu.Lock()
		p.s().detail = nil
		u.mu.Unlock()
	}
	if p.autoToggle.Clicked(gtx) {
		p.toggleAuto()
	}
	if p.extLink.Clicked(gtx) {
		u.show(PageExtensions)
	}
	if p.cancel.Clicked(gtx) {
		u.mu.Lock()
		if j := p.s().job; j != nil && j.proc != nil {
			j.cancelled = true
			j.proc.Stop()
		}
		u.mu.Unlock()
	}
	busy := sn.job != nil
	if d := sn.detail; d != nil {
		if p.detailMain.Clicked(gtx) && !busy && sn.flathub {
			p.install(d.ID)
		}
		if p.detailOpen.Clicked(gtx) {
			p.open(d.ID)
		}
		if p.detailRemove.Clicked(gtx) && !busy {
			p.askRemove(d.ID)
		}
	}
	uv := updatesViewOf(sn.pending, sn.installed, sn.cat, sn.running)
	if p.updateAll.Clicked(gtx) && !busy && len(uv.Ready) > 0 {
		refs := make([]string, len(uv.Ready))
		for i, r := range uv.Ready {
			refs[i] = r.Ref
		}
		name := i18n.T("your apps")
		if len(uv.Ready) == 1 {
			name = uv.Ready[0].Name
		}
		p.update(name, "", refs...)
	}
	for _, r := range append(append([]updateRow{}, uv.Ready...), uv.Held...) {
		k := p.clicks("u:" + r.Ref)
		if k.update.Clicked(gtx) && !busy {
			p.update(r.Name, r.Icon, r.Ref)
		}
		if k.review.Clicked(gtx) && !busy {
			p.askReview(r)
		}
	}
	for key, k := range p.rows {
		if strings.HasPrefix(key, "u:") {
			continue
		}
		id := strings.TrimPrefix(strings.TrimPrefix(key, "i:"), "a:")
		if k.row.Clicked(gtx) {
			p.openDetail(id)
		}
		if k.main.Clicked(gtx) && !busy && sn.flathub {
			p.install(id)
		}
		if k.open.Clicked(gtx) {
			p.open(id)
		}
		if k.remove.Clicked(gtx) && !busy {
			p.askRemove(id)
		}
	}
	for _, k := range p.extOpen {
		if k.Clicked(gtx) {
			u.show(PageExtensions)
		}
	}
}

// searchResults is the catalog's search, kept until the query or the
// catalog changes.
func (p *appsPage) searchResults(cat *flatpak.Catalog, q string) []flatpak.App {
	if q != p.query || cat != p.resultsOf {
		p.query, p.resultsOf, p.results = q, cat, cat.Search(q)
	}
	return p.results
}

var appsIntro = i18n.Mark("Apps from Flathub. They install for you only and update without a restart.")

func (p *appsPage) Body(gtx layout.Context) layout.Dimensions {
	u := p.u
	sn := p.snap()
	return u.scrolled(gtx, &p.list, func(gtx layout.Context) layout.Dimensions {
		var c col
		if u.hooks.Apps == nil || (sn.checked && !sn.available) {
			c.add(u.Heading(i18n.T("Apps"), i18n.T(appsIntro)))
			c.gap(20)
			c.add(u.errorBox(i18n.T("Flatpak is not on this system, so apps from Flathub cannot be installed here.")))
			return c.layout(gtx)
		}
		if !sn.checked {
			c.add(u.Heading(i18n.T("Apps"), i18n.T(appsIntro)))
			c.gap(20)
			c.add(u.spinnerLine(i18n.T("Reading your apps…")))
			return c.layout(gtx)
		}
		if sn.detail != nil {
			return p.detailBody(gtx, sn)
		}
		c.add(u.Heading(i18n.T("Apps"), i18n.T(appsIntro)))
		c.gap(18)
		c.add(func(gtx layout.Context) layout.Dimensions { return p.searchRow(gtx, sn) })
		if !sn.flathub {
			c.gap(14)
			c.add(u.notice(i18n.T("Flathub is not set up for this user, so apps cannot be installed or updated here. The apps you have still open and can be removed.")))
		}
		c.gap(16)
		q := strings.TrimSpace(p.search.Editor.Text())
		switch {
		case q != "" && sn.flathub:
			c.add(p.searchBody(sn, q))
		case sn.tab == tabInstalled || (sn.tab == tabBrowse && !sn.flathub):
			c.add(p.installedBody(sn))
		case sn.tab == tabUpdates:
			c.add(p.updatesBody(sn))
		default:
			c.add(p.browseBody(sn))
		}
		return c.layout(gtx)
	})
}

// searchRow is the search field and the tabs.
func (p *appsPage) searchRow(gtx layout.Context, sn appsSnap) layout.Dimensions {
	u := p.u
	n := 0
	if sn.upLoaded {
		n = len(sn.pending)
	}
	labels := []string{i18n.T("Browse"), i18n.X("apps tab", "Installed"), appsUpdatesTab(n)}
	return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
		layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
			hint := i18n.T("Search Flathub")
			if !sn.flathub {
				hint = i18n.T("Search needs Flathub")
			}
			return layout.Stack{Alignment: layout.W}.Layout(gtx,
				layout.Stacked(func(gtx layout.Context) layout.Dimensions {
					gtx.Constraints.Min.X = gtx.Constraints.Max.X
					return u.Input(gtx, p.search, guikit.StBody, 42, "", nil)
				}),
				layout.Stacked(func(gtx layout.Context) layout.Dimensions {
					if p.search.Editor.Len() > 0 {
						return layout.Dimensions{}
					}
					return layout.Inset{Left: 13}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
						return u.Label(gtx, guikit.StBody, guikit.ColDim, hint)
					})
				}),
			)
		}),
		layout.Rigid(layout.Spacer{Width: 10}.Layout),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			b := guikit.Box{Radius: 10, Fill: guikit.ColSidebar}
			return b.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				return layout.UniformInset(4).Layout(gtx, func(gtx layout.Context) layout.Dimensions {
					var kids []layout.FlexChild
					for i := range labels {
						i := i
						if i > 0 {
							kids = append(kids, layout.Rigid(layout.Spacer{Width: 4}.Layout))
						}
						on := appTabs[i] == sn.tab && strings.TrimSpace(p.search.Editor.Text()) == ""
						kids = append(kids, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
							return p.tab(gtx, &p.tabs[i], labels[i], on)
						}))
					}
					return layout.Flex{}.Layout(gtx, kids...)
				})
			})
		}),
	)
}

func (p *appsPage) tab(gtx layout.Context, c *widget.Clickable, label string, on bool) layout.Dimensions {
	u := p.u
	return c.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		pointer.CursorPointer.Add(gtx.Ops)
		b := guikit.Box{Radius: 8}
		fg := guikit.ColDim
		if on {
			b.Fill, fg = guikit.ColField, guikit.ColText
		}
		if gtx.Focused(c) {
			b.Ring, b.RingWidth = guikit.FocusRingStrong, 2
		}
		return b.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			return layout.Inset{Top: 8, Bottom: 8, Left: 14, Right: 14}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				return u.Label(gtx, guikit.StButton, fg, label)
			})
		})
	})
}

func (p *appsPage) chip(gtx layout.Context, c *widget.Clickable, label string, on bool) layout.Dimensions {
	u := p.u
	return c.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		pointer.CursorPointer.Add(gtx.Ops)
		b := guikit.Box{Radius: 16, Border: guikit.Over(guikit.ColWindow, guikit.ColText, 0.18), BorderWidth: 1}
		fg := guikit.ColText2
		if on {
			b.Fill, b.Border, fg = guikit.Over(guikit.ColWindow, guikit.ColAccent, 0.14), guikit.ColAccent, guikit.ColText
		}
		if gtx.Focused(c) {
			b.Ring, b.RingWidth = guikit.FocusRingStrong, 2
		}
		return b.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			return layout.Inset{Top: 7, Bottom: 7, Left: 12, Right: 12}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				return u.Label(gtx, guikit.StSmall500, fg, label)
			})
		})
	})
}

// browseBody is the chips, then the picks or the chip's category.
func (p *appsPage) browseBody(sn appsSnap) layout.Widget {
	u := p.u
	return func(gtx layout.Context) layout.Dimensions {
		var c col
		c.add(func(gtx layout.Context) layout.Dimensions {
			ws := make([]layout.Widget, len(flatpak.Categories))
			for i, cat := range flatpak.Categories {
				i, cat := i, cat
				ws[i] = func(gtx layout.Context) layout.Dimensions {
					return p.chip(gtx, &p.chips[i], cat.Name, sn.chip == cat.Name)
				}
			}
			return flow(gtx, 8, 8, ws)
		})
		c.gap(18)
		if sn.job != nil {
			c.add(p.jobPanel(sn.job))
			c.gap(18)
		}
		switch {
		case sn.cat == nil && sn.catErr != "":
			c.add(u.errorBox(sn.catErr))
			return c.layout(gtx)
		case sn.cat == nil:
			c.add(u.spinnerLine(i18n.T("Downloading Flathub's catalog… The first time takes a minute.")))
			return c.layout(gtx)
		}
		if sn.chip != "" {
			apps := sn.cat.InCategory(sn.chip)
			c.add(u.caps(strings.ToUpper(sn.chip) + " · " + countApps(len(apps))))
			c.gap(10)
			c.add(p.appList(sn, apps, sn.job != nil))
			if len(apps) > appsMaxRows {
				c.gap(10)
				c.add(u.dim(i18n.Tf("The first %d, verified apps first. Search to find the others.", appsMaxRows)))
			}
			return c.layout(gtx)
		}
		c.add(u.caps(i18n.T("ISOTOPE PICKS")))
		c.gap(10)
		lines := pickLines(sn.cat, sn.picks, sn.installedIDs)
		c.add(u.panel(func(gtx layout.Context) layout.Dimensions {
			var rows col
			for i := 0; i < len(lines); i += 2 {
				i := i
				if i > 0 {
					rows.gap(14)
				}
				rows.add(func(gtx layout.Context) layout.Dimensions {
					right := func(gtx layout.Context) layout.Dimensions { return layout.Dimensions{} }
					if i+1 < len(lines) {
						right = p.appRow(lines[i+1], sn.job != nil, sn.flathub, true)
					}
					return guikit.TwoCols(gtx, p.appRow(lines[i], sn.job != nil, sn.flathub, true), right)
				})
			}
			if len(lines) == 0 {
				rows.add(u.dim(i18n.T("None of Isotope's picks is in the catalog.")))
			}
			return rows.layout(gtx)
		}))
		c.gap(10)
		c.add(u.dim(i18n.T("The picks are Isotope Welcome's list. Search reaches all of Flathub.")))
		if sn.refreshing {
			c.gap(10)
			c.add(u.spinnerLine(i18n.T("Refreshing Flathub's catalog…")))
		}
		return c.layout(gtx)
	}
}

func countApps(n int) string {
	return i18n.Nf("%d APP", "%d APPS", n, n)
}

func itoa(n int) string { return strconv.Itoa(n) }

// flow lays ws left to right, gap dp apart, on as many lines as they need,
// rowGap dp apart: the chips wrap in a narrow window.
func flow(gtx layout.Context, gap, rowGap float32, ws []layout.Widget) layout.Dimensions {
	maxW := gtx.Constraints.Max.X
	g, rg := guikit.Dp(gtx, gap), guikit.Dp(gtx, rowGap)
	type placed struct {
		call op.CallOp
		size image.Point
	}
	items := make([]placed, len(ws))
	for i, w := range ws {
		m := op.Record(gtx.Ops)
		cgtx := gtx
		cgtx.Constraints = layout.Constraints{Max: image.Pt(maxW, gtx.Constraints.Max.Y)}
		d := w(cgtx)
		items[i] = placed{m.Stop(), d.Size}
	}
	x, y, lineH, width := 0, 0, 0, 0
	for _, it := range items {
		if x > 0 && x+it.size.X > maxW {
			x, y, lineH = 0, y+lineH+rg, 0
		}
		off := op.Offset(image.Pt(x, y)).Push(gtx.Ops)
		it.call.Add(gtx.Ops)
		off.Pop()
		x += it.size.X + g
		width = max(width, x-g)
		lineH = max(lineH, it.size.Y)
	}
	return layout.Dimensions{Size: image.Pt(width, y+lineH)}
}

// appList is a panel of catalog apps, at most appsMaxRows.
func (p *appsPage) appList(sn appsSnap, apps []flatpak.App, busy bool) layout.Widget {
	u := p.u
	if len(apps) > appsMaxRows {
		apps = apps[:appsMaxRows]
	}
	return u.panel(func(gtx layout.Context) layout.Dimensions {
		var rows col
		for i, a := range apps {
			if i > 0 {
				rows.gap(12)
			}
			rows.add(p.appRow(lineOf(a, sn.installedIDs), busy, sn.flathub, false))
		}
		return rows.layout(gtx)
	})
}

// appRow is an app: its icon, name (and Verified), line, and Install or
// Open. A click on the icon or the name opens the details.
func (p *appsPage) appRow(l appLine, busy, flathub, compact bool) layout.Widget {
	u := p.u
	k := p.clicks("a:" + l.ID)
	return func(gtx layout.Context) layout.Dimensions {
		gtx.Constraints.Min.X = gtx.Constraints.Max.X
		var btn guikit.Action
		if l.Installed {
			btn = guikit.Action{Click: &k.open, Kind: guikit.ButtonSecondary, Label: i18n.T("Open")}
		} else {
			btn = guikit.Action{Click: &k.main, Kind: guikit.ButtonPrimary, Label: i18n.T("Install"), Disabled: busy || !flathub}
		}
		return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
			layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
				return k.row.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
					return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
						layout.Rigid(func(gtx layout.Context) layout.Dimensions { return p.icon(gtx, l.ID, l.Name, l.Icon, 44) }),
						layout.Rigid(layout.Spacer{Width: 12}.Layout),
						layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
							var c col
							c.add(func(gtx layout.Context) layout.Dimensions { return p.nameLine(gtx, l.Name, l.Verified && !compact, "") })
							c.gap(2)
							maxLines := 2
							if compact {
								maxLines = 1
							}
							c.add(func(gtx layout.Context) layout.Dimensions {
								return u.LabelMax(gtx, guikit.StSmall, guikit.ColDim, maxLines, l.Sub)
							})
							return c.layout(gtx)
						}),
					)
				})
			}),
			layout.Rigid(layout.Spacer{Width: 12}.Layout),
			layout.Rigid(u.buttons(btn)),
		)
	}
}

// nameLine is a name in the row's style, with a Verified badge and a dim
// suffix ("146.0 → 146.0.1 · 92 MB").
func (p *appsPage) nameLine(gtx layout.Context, name string, verified bool, suffix string) layout.Dimensions {
	u := p.u
	kids := []layout.FlexChild{layout.Rigid(func(gtx layout.Context) layout.Dimensions {
		if verified || suffix != "" {
			gtx.Constraints.Max.X = max(gtx.Constraints.Max.X-guikit.Dp(gtx, 90), gtx.Constraints.Max.X/2)
		}
		return u.LabelMax(gtx, guikit.StBody600, guikit.ColText, 1, name)
	})}
	if verified {
		kids = append(kids, layout.Rigid(layout.Spacer{Width: 8}.Layout), layout.Rigid(p.verified))
	}
	if suffix != "" {
		kids = append(kids, layout.Rigid(layout.Spacer{Width: 8}.Layout), layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
			return u.LabelMax(gtx, guikit.StSmall, guikit.ColDim, 1, suffix)
		}))
	}
	return layout.Flex{Alignment: layout.Middle}.Layout(gtx, kids...)
}

// colVerified is the Verified badge's green.
var colVerified = guikit.RGB(0x37be95)

func (p *appsPage) verified(gtx layout.Context) layout.Dimensions {
	// Never wrapped: it overflows a too narrow row rather than stacking.
	gtx.Constraints.Max.X = max(gtx.Constraints.Max.X, guikit.Dp(gtx, 80))
	b := guikit.Box{Radius: 9, Border: guikit.Over(guikit.ColWindow, colVerified, 0.45), BorderWidth: 1}
	return b.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		return layout.Inset{Top: 1, Bottom: 1, Left: 7, Right: 7}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			return p.u.Label(gtx, guikit.Style{Face: guikit.StSmall600.Face, Weight: guikit.StSmall600.Weight, Size: 11}, colVerified, i18n.T("Verified"))
		})
	})
}

// tileColours are the letter tiles of an app without an icon.
var tileColours = []color.NRGBA{
	guikit.RGB(0xff9a52), guikit.RGB(0x89dae3), guikit.RGB(0xea8a18), guikit.RGB(0x79b8ed),
	guikit.RGB(0xd896db), guikit.RGB(0x37be95), guikit.RGB(0x9a9ab0),
}

// icon draws an app's icon, size dp square with round corners, or a tile
// with its first letter.
func (p *appsPage) icon(gtx layout.Context, id, name, path string, size float32) layout.Dimensions {
	px := guikit.Dp(gtx, size)
	sz := image.Pt(px, px)
	rad := guikit.Dp(gtx, size/4)
	ic := p.iconOf(path)
	if ic.ok {
		defer clip.UniformRRect(image.Rectangle{Max: sz}, rad).Push(gtx.Ops).Pop()
		gtx.Constraints = layout.Exact(sz)
		return widget.Image{Src: ic.op, Fit: widget.Contain, Position: layout.Center}.Layout(gtx)
	}
	h := fnv.New32a()
	_, _ = h.Write([]byte(id))
	guikit.FillRRect(gtx, image.Rectangle{Max: sz}, rad, tileColours[h.Sum32()%uint32(len(tileColours))])
	letter := "?"
	if r := []rune(strings.TrimSpace(name)); len(r) > 0 {
		letter = strings.ToUpper(string(r[0]))
	}
	gtx.Constraints = layout.Exact(sz)
	layout.Center.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		st := guikit.Style{Face: guikit.StTitle.Face, Weight: guikit.StTitle.Weight, Size: unit.Sp(size * 0.4)}
		return p.u.Label(gtx, st, guikit.ColOnAccent, letter)
	})
	return layout.Dimensions{Size: sz}
}

func (p *appsPage) iconOf(path string) *appIcon {
	if path == "" {
		return &appIcon{}
	}
	ic, ok := p.icons[path]
	if !ok {
		ic = &appIcon{}
		if img := p.u.hooks.Apps.Icon(path); img != nil {
			ic.op, ic.ok = paint.NewImageOp(img), true
			ic.op.Filter = paint.FilterLinear
		}
		p.icons[path] = ic
	}
	return ic
}

// searchBody is Flathub's results, then the system extensions that match.
func (p *appsPage) searchBody(sn appsSnap, q string) layout.Widget {
	u := p.u
	return func(gtx layout.Context) layout.Dimensions {
		var c col
		if sn.job != nil {
			c.add(p.jobPanel(sn.job))
			c.gap(18)
		}
		switch {
		case sn.cat == nil && sn.catErr != "":
			c.add(u.errorBox(sn.catErr))
		case sn.cat == nil:
			c.add(u.spinnerLine(i18n.T("Downloading Flathub's catalog… The first time takes a minute.")))
		default:
			res := p.searchResults(sn.cat, q)
			title := i18n.Nf("FLATHUB · %d RESULT", "FLATHUB · %d RESULTS", len(res), len(res))
			c.add(u.caps(title))
			c.gap(10)
			if len(res) == 0 {
				c.add(u.panel(u.dim(i18n.Tf("No app on Flathub matches “%s”.", q))))
			} else {
				c.add(p.appList(sn, res, sn.job != nil))
				if len(res) > appsMaxRows {
					c.gap(10)
					c.add(u.dim(i18n.Tf("The first %d. Type more to narrow the search.", appsMaxRows)))
				}
			}
		}
		if exts := extMatches(sn.extCat, sn.exts, q); len(exts) > 0 {
			c.gap(22)
			c.add(u.caps(i18n.T("SYSTEM EXTENSIONS")))
			c.gap(10)
			c.add(u.panel(func(gtx layout.Context) layout.Dimensions {
				var rows col
				for i, e := range exts {
					if i > 0 {
						rows.gap(12)
					}
					e := e
					rows.add(func(gtx layout.Context) layout.Dimensions {
						return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
							layout.Rigid(func(gtx layout.Context) layout.Dimensions { return p.extTile(gtx) }),
							layout.Rigid(layout.Spacer{Width: 12}.Layout),
							layout.Flexed(1, u.row(e.Title, e.Sub, u.buttons(guikit.Action{Click: p.extClick(e.Name), Kind: guikit.ButtonSecondary, Label: i18n.T("Open in Extensions")}))),
						)
					})
				}
				return rows.layout(gtx)
			}))
		}
		return c.layout(gtx)
	}
}

// extTile is a system extension's tile: the hexagon of the Isotope mark.
func (p *appsPage) extTile(gtx layout.Context) layout.Dimensions {
	px := guikit.Dp(gtx, 44)
	sz := image.Pt(px, px)
	guikit.FillRRect(gtx, image.Rectangle{Max: sz}, guikit.Dp(gtx, 11), guikit.ColDisabled)
	off := op.Offset(image.Pt((px-guikit.Dp(gtx, 26))/2, (px-guikit.Dp(gtx, 26))/2)).Push(gtx.Ops)
	guikit.Mark(gtx, 26, true, 1, 0)
	off.Pop()
	return layout.Dimensions{Size: sz}
}

// jobPanel is the IN PROGRESS panel: the app, its progress and Cancel.
func (p *appsPage) jobPanel(j *appsJob) layout.Widget {
	u := p.u
	title := j.Name
	switch j.Kind {
	case jobInstall:
		title = i18n.Tf("Installing %s", j.Name)
	case jobUpdate:
		title = i18n.Tf("Updating %s", j.Name)
	case jobRemove:
		title = i18n.Tf("Removing %s", j.Name)
	}
	sub := i18n.T("Starting…")
	if j.Progress.Percent >= 0 {
		sub = itoa(j.Progress.Percent) + "%"
		if j.Progress.Steps > 1 {
			sub += " · " + i18n.Tf("step %[1]d of %[2]d", j.Progress.Step, j.Progress.Steps)
		}
	}
	return func(gtx layout.Context) layout.Dimensions {
		var c col
		c.add(u.caps(i18n.T("IN PROGRESS")))
		c.gap(10)
		c.add(u.panel(func(gtx layout.Context) layout.Dimensions {
			return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
				layout.Rigid(func(gtx layout.Context) layout.Dimensions { return p.icon(gtx, j.ID, j.Name, j.Icon, 40) }),
				layout.Rigid(layout.Spacer{Width: 12}.Layout),
				layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
					var rows col
					rows.add(func(gtx layout.Context) layout.Dimensions {
						return u.Label(gtx, guikit.StBody600, guikit.ColText, title)
					})
					rows.gap(2)
					rows.add(u.dim(sub))
					rows.gap(8)
					rows.add(func(gtx layout.Context) layout.Dimensions {
						return guikit.ProgressBar(gtx, float32(max(j.Progress.Percent, 0))/100)
					})
					return rows.layout(gtx)
				}),
				layout.Rigid(layout.Spacer{Width: 16}.Layout),
				layout.Rigid(u.buttons(guikit.Action{Click: &p.cancel, Kind: guikit.ButtonSecondary, Label: i18n.T("Cancel")})),
			)
		}))
		return c.layout(gtx)
	}
}

// installedBody is the job, the installed apps, and the extensions line.
func (p *appsPage) installedBody(sn appsSnap) layout.Widget {
	u := p.u
	return func(gtx layout.Context) layout.Dimensions {
		var c col
		if sn.job != nil {
			c.add(p.jobPanel(sn.job))
			c.gap(18)
		}
		rows := installedRows(sn.installed, sn.cat, sn.running)
		switch {
		case sn.instErr != "":
			c.add(u.errorBox(sn.instErr))
		case !sn.instLoaded:
			c.add(u.spinnerLine(i18n.T("Reading your apps…")))
		default:
			c.add(u.caps(i18n.Tf("INSTALLED · %s", countApps(len(rows)))))
			c.gap(10)
			if len(rows) == 0 {
				c.add(u.panel(u.dim(i18n.T("No app yet. Browse or search Flathub to install one."))))
			} else {
				c.add(u.panel(func(gtx layout.Context) layout.Dimensions {
					var list col
					for i, r := range rows {
						if i > 0 {
							list.gap(12)
						}
						list.add(p.installedRow(r, sn.job != nil))
					}
					return list.layout(gtx)
				}))
			}
		}
		if l := installedExtsLine(sn.exts); l != "" {
			c.gap(14)
			c.add(func(gtx layout.Context) layout.Dimensions {
				return layout.Flex{Alignment: layout.Baseline}.Layout(gtx,
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						return u.Label(gtx, guikit.StSmall, guikit.ColDim, l+" ")
					}),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						return u.Link(gtx, &p.extLink, i18n.T("Manage them in Extensions"))
					}),
				)
			})
		}
		return c.layout(gtx)
	}
}

func (p *appsPage) installedRow(r installedRow, busy bool) layout.Widget {
	u := p.u
	k := p.clicks("i:" + r.ID)
	return func(gtx layout.Context) layout.Dimensions {
		gtx.Constraints.Min.X = gtx.Constraints.Max.X
		return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
			layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
				return k.row.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
					return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
						layout.Rigid(func(gtx layout.Context) layout.Dimensions { return p.icon(gtx, r.ID, r.Name, r.Icon, 40) }),
						layout.Rigid(layout.Spacer{Width: 12}.Layout),
						layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
							var c col
							c.add(func(gtx layout.Context) layout.Dimensions {
								return u.LabelMax(gtx, guikit.StBody600, guikit.ColText, 1, r.Name)
							})
							c.gap(2)
							c.add(func(gtx layout.Context) layout.Dimensions {
								return u.LabelMax(gtx, guikit.StSmall, guikit.ColDim, 1, r.Sub)
							})
							return c.layout(gtx)
						}),
					)
				})
			}),
			layout.Rigid(layout.Spacer{Width: 12}.Layout),
			layout.Rigid(u.buttons(
				guikit.Action{Click: &k.open, Kind: guikit.ButtonSecondary, Label: i18n.T("Open")},
				guikit.Action{Click: &k.remove, Kind: guikit.ButtonGhost, Label: i18n.T("Remove…"), Disabled: busy},
			)),
		)
	}
}

// updatesBody is the automatic setting, the ready updates, the ones that
// need an OK, and the end-of-life runtimes.
func (p *appsPage) updatesBody(sn appsSnap) layout.Widget {
	u := p.u
	uv := updatesViewOf(sn.pending, sn.installed, sn.cat, sn.running)
	av := autoAppsViewOf(sn.auto, sn.last, sn.hasLast, u.now())
	busy := sn.job != nil
	return func(gtx layout.Context) layout.Dimensions {
		var c col
		c.add(u.panel(u.row(i18n.T("Update apps automatically"), av.Sub,
			u.buttons(guikit.Action{Click: &p.autoToggle, Kind: guikit.ButtonSecondary, Label: av.Toggle}))))
		c.gap(18)
		if sn.job != nil {
			c.add(p.jobPanel(sn.job))
			c.gap(18)
		}
		switch {
		case sn.upErr != "":
			c.add(u.errorBox(sn.upErr))
			return c.layout(gtx)
		case !sn.upLoaded:
			c.add(u.spinnerLine(i18n.T("Looking for updates…")))
			return c.layout(gtx)
		}
		if len(uv.Ready) == 0 && len(uv.Held) == 0 {
			c.add(u.panel(u.dim(i18n.T("Your apps are up to date."))))
		}
		if len(uv.Ready) > 0 {
			c.add(func(gtx layout.Context) layout.Dimensions {
				return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
					layout.Flexed(1, u.caps(uv.ReadyTitle)),
					layout.Rigid(u.buttons(guikit.Action{Click: &p.updateAll, Kind: guikit.ButtonPrimary, Label: i18n.T("Update all"), Disabled: busy})),
				)
			})
			c.gap(10)
			c.add(p.updateList(uv.Ready, busy))
		}
		if len(uv.Held) > 0 {
			c.gap(20)
			c.add(u.caps(i18n.T("NEEDS YOUR OK")))
			c.gap(10)
			c.add(p.updateList(uv.Held, busy))
		}
		for _, l := range uv.EOL {
			c.gap(14)
			c.add(func(gtx layout.Context) layout.Dimensions {
				gtx.Constraints.Max.X = min(gtx.Constraints.Max.X, guikit.Dp(gtx, 700))
				return u.LabelLines(gtx, guikit.StSmall, guikit.ColWarn, 1.4, l)
			})
		}
		return c.layout(gtx)
	}
}

func (p *appsPage) updateList(rows []updateRow, busy bool) layout.Widget {
	u := p.u
	return u.panel(func(gtx layout.Context) layout.Dimensions {
		var list col
		for i, r := range rows {
			if i > 0 {
				list.gap(12)
			}
			r := r
			k := p.clicks("u:" + r.Ref)
			btn := guikit.Action{Click: &k.update, Kind: guikit.ButtonSecondary, Label: i18n.T("Update"), Disabled: busy}
			if r.NeedsOK {
				btn = guikit.Action{Click: &k.review, Kind: guikit.ButtonSecondary, Label: i18n.T("Review…"), Disabled: busy}
			}
			list.add(func(gtx layout.Context) layout.Dimensions {
				gtx.Constraints.Min.X = gtx.Constraints.Max.X
				return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
					layout.Rigid(func(gtx layout.Context) layout.Dimensions { return p.icon(gtx, r.ID, r.Name, r.Icon, 36) }),
					layout.Rigid(layout.Spacer{Width: 12}.Layout),
					layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
						var c col
						c.add(func(gtx layout.Context) layout.Dimensions { return p.nameLine(gtx, r.Name, false, r.Change) })
						if r.Notes != "" {
							c.gap(2)
							notes := r.Notes
							c.add(func(gtx layout.Context) layout.Dimensions {
								if r.NeedsOK {
									lead := i18n.T("Asks for more:")
									return p.warnLead(gtx, lead, strings.TrimPrefix(notes, lead))
								}
								return u.LabelMax(gtx, guikit.StSmall, guikit.ColDim, 2, notes)
							})
						}
						return c.layout(gtx)
					}),
					layout.Rigid(layout.Spacer{Width: 12}.Layout),
					layout.Rigid(u.buttons(btn)),
				)
			})
		}
		return list.layout(gtx)
	})
}

// warnLead is a small line whose first words are amber.
func (p *appsPage) warnLead(gtx layout.Context, lead, rest string) layout.Dimensions {
	u := p.u
	if !strings.HasPrefix(rest, " ") {
		return u.LabelMax(gtx, guikit.StSmall, guikit.ColWarn, 2, lead+rest)
	}
	return layout.Flex{Alignment: layout.Baseline}.Layout(gtx,
		layout.Rigid(func(gtx layout.Context) layout.Dimensions { return u.Label(gtx, guikit.StSmall, guikit.ColWarn, lead) }),
		layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
			return u.LabelMax(gtx, guikit.StSmall, guikit.ColDim, 2, rest)
		}),
	)
}

// detailBody is one app: its head, the facts, the description and what it
// can reach.
func (p *appsPage) detailBody(gtx layout.Context, sn appsSnap) layout.Dimensions {
	u := p.u
	d := sn.detail
	a, inCat := sn.cat.Get(d.ID)
	var inst *flatpak.Installed
	for i, in := range sn.installed {
		if in.Ref.IsApp() && in.Ref.ID == d.ID {
			inst = &sn.installed[i]
		}
	}
	name := a.Name
	if !inCat {
		name = d.ID
		if inst != nil && inst.Name != "" {
			name = inst.Name
		}
	}
	busy := sn.job != nil
	var c col
	back := i18n.T("Back to results")
	if strings.TrimSpace(p.search.Editor.Text()) == "" {
		back = i18n.T("Back")
	}
	c.add(func(gtx layout.Context) layout.Dimensions { return u.Link(gtx, &p.back, "‹ "+back) })
	c.gap(16)
	c.add(func(gtx layout.Context) layout.Dimensions {
		var btns []guikit.Action
		if inst != nil {
			btns = []guikit.Action{
				{Click: &p.detailOpen, Kind: guikit.ButtonSecondary, Label: i18n.T("Open")},
				{Click: &p.detailRemove, Kind: guikit.ButtonGhost, Label: i18n.T("Remove…"), Disabled: busy},
			}
		} else {
			btns = []guikit.Action{{Click: &p.detailMain, Kind: guikit.ButtonPrimary, Label: i18n.T("Install"), Disabled: busy || !sn.flathub}}
		}
		return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
			layout.Rigid(func(gtx layout.Context) layout.Dimensions { return p.icon(gtx, d.ID, name, a.Icon, 72) }),
			layout.Rigid(layout.Spacer{Width: 18}.Layout),
			layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
				var h col
				h.add(func(gtx layout.Context) layout.Dimensions {
					return u.LabelMax(gtx, guikit.StTitle, guikit.ColText, 1, name)
				})
				h.gap(4)
				h.add(func(gtx layout.Context) layout.Dimensions {
					kids := []layout.FlexChild{layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						return u.Label(gtx, guikit.StSmall, guikit.ColDim, orUnknown(a.Developer))
					})}
					if a.Verified {
						kids = append(kids, layout.Rigid(layout.Spacer{Width: 8}.Layout), layout.Rigid(p.verified))
					}
					return layout.Flex{Alignment: layout.Middle}.Layout(gtx, kids...)
				})
				if a.Summary != "" {
					h.gap(6)
					h.add(func(gtx layout.Context) layout.Dimensions {
						return u.LabelMax(gtx, guikit.StBody, guikit.ColText2, 2, a.Summary)
					})
				}
				return h.layout(gtx)
			}),
			layout.Rigid(layout.Spacer{Width: 12}.Layout),
			layout.Rigid(u.buttons(btns...)),
		)
	})
	c.gap(20)
	facts := detailFacts(a, d.Info, inst)
	c.add(func(gtx layout.Context) layout.Dimensions {
		var kids []layout.FlexChild
		for i, f := range facts {
			f := f
			if i > 0 {
				kids = append(kids, layout.Rigid(layout.Spacer{Width: 40}.Layout))
			}
			kids = append(kids, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				var fc col
				fc.add(u.caps(f.Label))
				fc.gap(4)
				fc.add(func(gtx layout.Context) layout.Dimensions {
					gtx.Constraints.Max.X = min(gtx.Constraints.Max.X, guikit.Dp(gtx, 260))
					return u.LabelMax(gtx, guikit.StBody600, guikit.ColText, 1, f.Value)
				})
				return fc.layout(gtx)
			}))
		}
		return layout.Flex{}.Layout(gtx, kids...)
	})
	if a.Description != "" {
		c.gap(18)
		c.add(func(gtx layout.Context) layout.Dimensions {
			gtx.Constraints.Max.X = min(gtx.Constraints.Max.X, guikit.Dp(gtx, 700))
			return u.LabelLines(gtx, guikit.StBody, guikit.ColText2, 1.5, a.Description)
		})
	}
	if a.Release.Description != "" && inst != nil && a.Release.Version != inst.Version {
		c.gap(18)
		c.add(u.caps(i18n.Tf("WHAT'S NEW IN %s", strings.ToUpper(a.Release.Version))))
		c.gap(6)
		c.add(u.dim(a.Release.Description))
	}
	c.gap(22)
	c.add(u.caps(i18n.T("WHAT IT CAN REACH")))
	c.gap(10)
	switch {
	case !d.Loaded:
		c.add(u.spinnerLine(i18n.T("Reading its permissions…")))
	case d.Err != "":
		c.add(u.errorBox(d.Err))
	default:
		c.add(p.reachPanel(d.Summary))
		c.gap(10)
		note := i18n.T("From the app's metadata on Flathub, before you install it. Flatseal changes them later.")
		if d.FromInstalled {
			note = i18n.T("From the installed app's metadata. Flatseal changes them.")
		}
		c.add(u.dim(note))
	}
	if eol := d.Info.EOL; eol != "" {
		c.gap(14)
		c.add(u.notice(i18n.Tf("This app is no longer maintained on Flathub: %s", eol)))
	}
	return c.layout(gtx)
}

// reachPanel is the permissions in two columns, the files in amber.
func (p *appsPage) reachPanel(s flatpak.Summary) layout.Widget {
	u := p.u
	items := append([]flatpak.Reach(nil), s.Items...)
	if s.Others > 0 {
		t := i18n.Nf("%d other permission", "%d other permissions", s.Others, s.Others)
		items = append(items, flatpak.Reach{Title: t, Sub: i18n.T("Folders, services and devices it names one by one")})
	}
	if len(items) == 0 {
		items = append(items, flatpak.Reach{Title: i18n.T("Nothing outside its sandbox"), Sub: i18n.T("It asks for no files, network, display or devices")})
	}
	cell := func(r flatpak.Reach) layout.Widget {
		return func(gtx layout.Context) layout.Dimensions {
			title := guikit.ColText
			if r.Warn {
				title = guikit.ColWarn
			}
			return layout.Flex{}.Layout(gtx,
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return layout.Inset{Top: 2}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
						if r.Warn {
							return guikit.WarnIcon(gtx, 16, guikit.ColWarn)
						}
						return guikit.RingIcon(gtx, 16)
					})
				}),
				layout.Rigid(layout.Spacer{Width: 10}.Layout),
				layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
					var c col
					c.add(func(gtx layout.Context) layout.Dimensions { return u.Label(gtx, guikit.StBody600, title, r.Title) })
					c.gap(2)
					c.add(func(gtx layout.Context) layout.Dimensions {
						return u.LabelMax(gtx, guikit.StSmall, guikit.ColDim, 2, r.Sub)
					})
					return c.layout(gtx)
				}),
			)
		}
	}
	return u.panel(func(gtx layout.Context) layout.Dimensions {
		var rows col
		for i := 0; i < len(items); i += 2 {
			if i > 0 {
				rows.gap(14)
			}
			left := cell(items[i])
			right := func(gtx layout.Context) layout.Dimensions { return layout.Dimensions{} }
			if i+1 < len(items) {
				right = cell(items[i+1])
			}
			rows.add(func(gtx layout.Context) layout.Dimensions { return guikit.TwoCols(gtx, left, right) })
		}
		return rows.layout(gtx)
	})
}
