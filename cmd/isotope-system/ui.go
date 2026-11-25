package main

import (
	"image"
	"sync"
	"time"

	"gioui.org/io/event"
	"gioui.org/io/system"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/text"
	"gioui.org/unit"
	"gioui.org/widget"

	"github.com/kairos-community/isotope/pkg/guikit"
	"github.com/kairos-community/isotope/pkg/i18n"
)

// Hooks are the UI's ways out: main wires them to the session, the tests
// pass fakes.
type Hooks struct {
	// Invalidate asks for a frame from another goroutine.
	Invalidate func()
	// Daemon is isotope-updated; nil when the system bus cannot be
	// reached (every page that needs it then says so).
	Daemon Daemon
	// Network is NetworkManager; nil shows no network device.
	Network Network
	// Bluetooth is BlueZ; nil shows "No Bluetooth adapter".
	Bluetooth Bluetooth
	// Disks is udisks2; nil when it is missing (the Disks page then says
	// so).
	Disks Disks
	// Glibc is `isotope glibc`, run as the user (compat.go); nil when the
	// Compatibility page cannot reach it.
	Glibc Glibc
	// Printing is CUPS and Avahi, as the user. main always sets it (a CUPS
	// that is not running is errNoCUPS); the tests may leave it nil.
	Printing Printing
	// Files is the file picker; nil asks for a typed path.
	Files FileChooser
	// Start runs a program and does not wait (the desktop's own settings,
	// xdg-open).
	Start func(argv ...string) error
	// Live is true on a live desktop session (the marker exists): the
	// pages that need an installed system then say to install first.
	Live bool
	// Shares is the user's saved network shares and their keyring; nil
	// keeps no list (the Disks page still connects).
	Shares ShareStore
	// Firmware is fwupd; nil leaves the Updates page without its FIRMWARE
	// section (no system bus).
	Firmware Firmware
	// Apps is the user's Flatpak installation (apps.go); nil says Flatpak
	// is not on this system.
	Apps Apps
	// Fingers is fprintd for the user's own fingerprints (fprint.go); nil
	// when the system bus cannot be reached (the Users page then offers the
	// fingerprint extension, as when fprintd is not installed).
	Fingers Fingerprints
	// Window performs a window action the header bar asked for (move,
	// close, maximize); nil ignores them.
	Window func(system.Action)
	// RemoteDesktop is `isotope remote-desktop`, run as the user
	// (remote.go); nil leaves the Network page without its REMOTE
	// DESKTOP section.
	RemoteDesktop RemoteDesktop
}

// pageView is one page. Body draws it above the footer; Footer returns the
// footer's buttons (none for no footer); Update handles its input before it
// is drawn; Shown is called each time the user opens the page, to refresh
// what it shows.
type pageView interface {
	Body(gtx layout.Context) layout.Dimensions
	Footer() (leading, trailing []guikit.Action)
	Update(gtx layout.Context)
	Shown()
}

// pageMakers builds each page; each page_*.go file adds itself.
var pageMakers = map[Page]func(u *UI) pageView{}

// UI is the Isotope System window.
type UI struct {
	*guikit.Theme
	sys   System
	hooks Hooks
	now   func() time.Time

	nav    guikit.NavList
	page   Page
	pageAt time.Time
	pages  map[Page]pageView
	scale  float32
	// menus are the rows' "⋯" menus, by row key (actionRow).
	menus map[string]*guikit.Overflow
	// bar is the header bar the window draws on COSMIC; nil elsewhere,
	// where the window manager draws the title bar.
	bar *guikit.HeaderBar
	// navOpen shows the navigation over the page on a narrow window;
	// navBtn shows it where there is no header bar.
	navOpen  bool
	navBtn   widget.Clickable
	navScrim widget.Clickable
	// pal is the palette the theme draws with.
	pal guikit.Palette

	// mu guards everything below: the goroutines that call the services
	// write it, the frame reads it.
	mu sync.Mutex
	// firmwareWriting is set while this window has fwupd write firmware
	// (firmware.go): every restart waits for it.
	firmwareWriting bool
	// st is the daemon's last status; stErr why it could not be read.
	st    Status
	stOK  bool
	stErr string
	// lines are the running job's progress lines, the last maxLines.
	lines []string
	// dlg is the question over the window, nil for none.
	dlg *dialog
	// jobWatch are told when the daemon's job ends: each page that started
	// one adds itself.
	jobWatch []func(prev string, st Status)
	// inst is the install the window follows.
	inst install
	// toShow is a page asked for by a second `isotope system PAGE`.
	toShow Page
	// choosing is true while the file picker is open.
	choosing bool
	// appUpdates is how many app updates the Apps page found, for the
	// Updates page's line; appUpdatesKnown once it has looked.
	appUpdates      int
	appUpdatesKnown bool
	// toTab is the tab of toShow ("apps updates").
	toTab string
	// newPal is a palette set from another goroutine, applied at the
	// next frame.
	newPal *guikit.Palette
}

// StPageTitle is a page's title: Inter, semibold, as COSMIC's page titles.
var StPageTitle = guikit.Style{Face: guikit.FaceBody, Weight: guikit.StBody600.Weight, Size: 24}

// narrowWidth is the window width, in dp, below which the navigation hides.
const narrowWidth = 760

// pageIcons is each page's icon in the navigation.
var pageIcons = map[Page]guikit.IconName{
	PageApps: guikit.IconApps, PageUpdates: guikit.IconUpdates, PageDesktop: guikit.IconDesktop,
	PageExtensions: guikit.IconExtensions, PageBackup: guikit.IconBackup, PageSecurity: guikit.IconSecurity,
	PageNetwork: guikit.IconNetwork, PageBluetooth: guikit.IconBluetooth, PagePrinters: guikit.IconPrinters,
	PageUsers: guikit.IconUsers, PageDisks: guikit.IconDisks, PageCompat: guikit.IconCompat, PageAbout: guikit.IconAbout,
}

// SetPalette changes the window's colours, from any goroutine: the
// desktop's theme changed.
func (u *UI) SetPalette(p guikit.Palette) {
	u.mu.Lock()
	u.newPal = &p
	u.mu.Unlock()
	u.hooks.Invalidate()
}

const maxLines = 200

func newUI(sys System, sh *text.Shaper, h Hooks, first Page) *UI {
	u := &UI{
		Theme: &guikit.Theme{Shaper: sh, TitleStyle: StPageTitle},
		sys:   sys,
		hooks: h,
		now:   time.Now,
		pages: map[Page]pageView{},
		menus: map[string]*guikit.Overflow{},
		scale: 1,
		pal:   guikit.IsotopePalette(true),
	}
	u.Pal = &u.pal
	if u.hooks.Invalidate == nil {
		u.hooks.Invalidate = func() {}
	}
	if u.hooks.Start == nil {
		u.hooks.Start = start
	}
	for _, p := range Pages {
		u.nav.Items = append(u.nav.Items, guikit.NavEntry{Name: p.Title(), Icon: pageIcons[p]})
	}
	u.jobWatch = append(u.jobWatch, u.installEnded)
	u.show(first)
	return u
}

// show makes p the page on screen.
func (u *UI) show(p Page) {
	if u.page != p || u.pageAt.IsZero() {
		u.pageAt = u.now()
	}
	u.page = p
	u.nav.Current = p.index()
	u.navOpen = false
	if u.bar != nil {
		u.bar.Title = p.Title()
	}
	u.view(p).Shown()
}

// ShowPage is the request of another `isotope system PAGE`, from any
// goroutine.
func (u *UI) ShowPage(p Page) { u.ShowTab(p, "") }

// ShowTab is ShowPage with a tab of the page ("apps updates").
func (u *UI) ShowTab(p Page, tab string) {
	u.mu.Lock()
	u.toShow, u.toTab = p, tab
	u.mu.Unlock()
	u.hooks.Invalidate()
}

// tabbed is a page with tabs.
type tabbed interface{ SetTab(tab string) }

// showTab shows p on its tab, on the UI goroutine.
func (u *UI) showTab(p Page, tab string) {
	if tab != "" {
		if t, ok := u.view(p).(tabbed); ok {
			t.SetTab(tab)
		}
	}
	u.show(p)
}

func (u *UI) view(p Page) pageView {
	v, ok := u.pages[p]
	if !ok {
		if u.hooks.Live && liveOnly[p] {
			v = &livePage{u: u, page: p}
		} else {
			v = pageMakers[p](u)
		}
		u.pages[p] = v
	}
	return v
}

// async runs f off the UI goroutine and asks for a frame when it returns.
func (u *UI) async(f func()) {
	go func() {
		f()
		u.hooks.Invalidate()
	}()
}

// status is the daemon's last status, and whether there is one.
func (u *UI) status() (Status, bool, string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.st, u.stOK, u.stErr
}

// progress is a copy of the job's last lines.
func (u *UI) progress() []string {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]string(nil), u.lines...)
}

// onJobEnd registers f, called (without mu held) when a job ends.
func (u *UI) onJobEnd(f func(prev string, st Status)) {
	u.mu.Lock()
	u.jobWatch = append(u.jobWatch, f)
	u.mu.Unlock()
}

// setStatus records a status from the daemon and tells the pages when a
// job ended. Called by follow, from its goroutine.
func (u *UI) setStatus(st Status) {
	u.mu.Lock()
	prev := u.st
	u.st, u.stOK, u.stErr = st, true, ""
	if !prev.Busy() && st.Busy() {
		u.lines = nil
	}
	watch := append([]func(string, Status){}, u.jobWatch...)
	u.mu.Unlock()
	if prev.Busy() && !st.Busy() {
		for _, f := range watch {
			f(prev.State, st)
		}
	}
	u.hooks.Invalidate()
}

func (u *UI) addLine(l string) {
	u.mu.Lock()
	u.lines = append(u.lines, l)
	if len(u.lines) > maxLines {
		u.lines = u.lines[len(u.lines)-maxLines:]
	}
	u.mu.Unlock()
	u.hooks.Invalidate()
}

// follow keeps the status current for the life of the window: the
// daemon's signals, and a new subscription when the daemon restarts.
func (u *UI) follow() {
	d := u.hooks.Daemon
	if d == nil {
		u.mu.Lock()
		u.stErr = i18n.T("The update service (isotope-updated) is not available: the system bus cannot be reached.")
		u.mu.Unlock()
		return
	}
	for {
		if st, err := d.Status(); err == nil {
			u.setStatus(st)
		} else {
			u.mu.Lock()
			u.stOK, u.stErr = false, Describe(err)
			u.mu.Unlock()
			u.hooks.Invalidate()
		}
		events, stop, err := d.Watch()
		if err == nil {
			for ev := range events {
				if ev.Status != nil {
					u.setStatus(*ev.Status)
				} else if ev.Line != "" {
					u.addLine(ev.Line)
				}
			}
			stop()
		}
		time.Sleep(3 * time.Second)
	}
}

// Layout draws one frame over the whole OS window: the header bar on
// COSMIC, the navigation at the left (over the page on a narrow window),
// the page and its footer. The window is the app: nothing is drawn around
// it.
func (u *UI) Layout(gtx layout.Context) layout.Dimensions {
	gtx = guikit.Scaled(gtx, u.scale)
	event.Op(gtx.Ops, u)
	u.mu.Lock()
	dlg, want, tab := u.dlg, u.toShow, u.toTab
	u.toShow, u.toTab = "", ""
	if u.newPal != nil {
		u.pal, u.newPal = *u.newPal, nil
	}
	u.mu.Unlock()
	if want != "" {
		u.showTab(want, tab)
	}
	guikit.WindowKeys(gtx, &u.scale, func() {
		switch {
		case dlg != nil && dlg.cancel >= 0:
			u.pick(dlg, dlg.cancel)
		case u.navOpen:
			u.navOpen = false
		}
	})
	narrow := gtx.Constraints.Max.X < gtx.Dp(narrowWidth)
	if !narrow {
		u.navOpen = false
	}
	if u.bar != nil {
		u.bar.ShowNavButton = narrow
		acts, nav := u.bar.Update(gtx)
		if acts != 0 && u.hooks.Window != nil {
			u.hooks.Window(acts)
		}
		if nav && dlg == nil {
			u.navOpen = !u.navOpen
		}
	}
	if dlg != nil {
		dlg.update(gtx, u)
	} else {
		if i := u.nav.Clicked(gtx); i >= 0 {
			u.show(Pages[i])
		}
	}
	v := u.view(u.page)
	if dlg == nil {
		v.Update(gtx)
	}
	lead, trail := v.Footer()
	var footer layout.Widget
	if len(lead)+len(trail) > 0 {
		footer = func(gtx layout.Context) layout.Dimensions { return u.Footer(gtx, lead, nil, trail) }
	}
	size := gtx.Constraints.Max
	guikit.FillRect(gtx, image.Rectangle{Max: size}, u.pal.Window)
	top := 0
	if u.bar != nil {
		top = u.bar.Layout(gtx, u.Theme).Size.Y
	}
	body := gtx
	body.Constraints = layout.Exact(image.Pt(size.X, size.Y-top))
	o := op.Offset(image.Pt(0, top)).Push(gtx.Ops)
	page := func(gtx layout.Context) layout.Dimensions {
		gtx.Constraints.Min = gtx.Constraints.Max
		w := func(gtx layout.Context) layout.Dimensions {
			gtx.Constraints.Min = gtx.Constraints.Max
			return guikit.Slide(gtx, u.pageAt, u.now(), v.Body)
		}
		if footer == nil {
			return w(gtx)
		}
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx, layout.Flexed(1, w), layout.Rigid(footer))
	}
	if narrow {
		if u.bar == nil {
			// No header bar to hold the navigation button: it gets a
			// strip of its own above the page.
			strip := gtx.Dp(48)
			pg := body
			pg.Constraints = layout.Exact(image.Pt(size.X, max(0, size.Y-top-strip)))
			po := op.Offset(image.Pt(0, strip)).Push(gtx.Ops)
			page(pg)
			po.Pop()
			u.navToggle(body)
		} else {
			page(body)
		}
		if u.navOpen {
			u.navOverlay(body)
		}
	} else {
		layout.Flex{}.Layout(body,
			layout.Rigid(func(gtx layout.Context) layout.Dimensions { return u.NavList(gtx, &u.nav) }),
			layout.Flexed(1, page),
		)
	}
	o.Pop()
	if dlg != nil {
		dlg.layout(gtx, u)
	}
	if u.now().Sub(u.pageAt) < guikit.SlideTime {
		gtx.Execute(op.InvalidateCmd{})
	}
	return layout.Dimensions{Size: size}
}

// navOverlay draws the navigation over a narrow page, and the page
// dimmed beside it; a click there closes the navigation.
func (u *UI) navOverlay(gtx layout.Context) {
	for u.navScrim.Clicked(gtx) {
		u.navOpen = false
	}
	size := gtx.Constraints.Max
	u.navScrim.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		guikit.FillRect(gtx, image.Rectangle{Max: size}, u.pal.Shade)
		return layout.Dimensions{Size: size}
	})
	u.NavList(gtx, &u.nav)
}

// navToggle is the button at the top left of a narrow page that shows the
// navigation, where there is no header bar to hold it.
func (u *UI) navToggle(gtx layout.Context) {
	for u.navBtn.Clicked(gtx) {
		u.navOpen = !u.navOpen
	}
	o := op.Offset(image.Pt(gtx.Dp(8), gtx.Dp(8))).Push(gtx.Ops)
	u.navBtn.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		d := gtx.Dp(32)
		r := image.Rectangle{Max: image.Pt(d, d)}
		guikit.FillRRect(gtx, r, gtx.Dp(8), u.pal.Group)
		if gtx.Focused(&u.navBtn) {
			guikit.StrokeRRect(gtx, r, gtx.Dp(8), guikit.Fdp(gtx, 2), u.pal.Focus)
		}
		io := op.Offset(image.Pt((d-gtx.Dp(16))/2, (d-gtx.Dp(16))/2)).Push(gtx.Ops)
		guikit.Icon(gtx, guikit.IconSidebar, 16, u.pal.Text)
		io.Pop()
		return layout.Dimensions{Size: r.Max}
	})
	o.Pop()
}

// scrolled lays w in the page's padding, in a list that scrolls when w is
// taller than the column.
func (u *UI) scrolled(gtx layout.Context, l *widget.List, w layout.Widget) layout.Dimensions {
	l.Axis = layout.Vertical
	return guikit.Scrolled(gtx, l, func(gtx layout.Context) layout.Dimensions {
		gtx.Constraints.Min.X = gtx.Constraints.Max.X
		return layout.N.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			gtx.Constraints.Max.X = min(gtx.Constraints.Max.X, gtx.Dp(maxColumn+2*40))
			gtx.Constraints.Min.X = gtx.Constraints.Max.X
			return guikit.Pad(gtx, func(gtx layout.Context) layout.Dimensions {
				gtx.Constraints.Min.X = gtx.Constraints.Max.X
				return w(gtx)
			})
		})
	})
}

// maxColumn is the widest a page's content gets, in dp: a wider window
// adds margin, not wider rows, as COSMIC Settings does.
const maxColumn = 720

// col is a vertical stack being built.
type col []layout.FlexChild

func (c *col) add(w layout.Widget) { *c = append(*c, layout.Rigid(w)) }
func (c *col) gap(h float32)       { *c = append(*c, guikit.VGap(h)) }
func (c col) layout(gtx layout.Context) layout.Dimensions {
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx, c...)
}

// para is a paragraph in the body colour.
func (u *UI) para(txt string) layout.Widget {
	return func(gtx layout.Context) layout.Dimensions {
		gtx.Constraints.Max.X = min(gtx.Constraints.Max.X, guikit.Dp(gtx, 620))
		return u.LabelLines(gtx, guikit.StBody, u.Colors().Text2, 1.5, txt)
	}
}

// dim is a small line in the dim colour.
func (u *UI) dim(txt string) layout.Widget {
	return func(gtx layout.Context) layout.Dimensions {
		return u.LabelLines(gtx, guikit.StSmall, u.Colors().Dim, 1.4, txt)
	}
}

// caps is a section's heading, over its group of rows.
func (u *UI) caps(txt string) layout.Widget {
	return func(gtx layout.Context) layout.Dimensions {
		return u.Label(gtx, guikit.StBody600, u.Colors().Text, txt)
	}
}

// lead is a status line, larger.
func (u *UI) lead(txt string) layout.Widget {
	return func(gtx layout.Context) layout.Dimensions {
		return u.Label(gtx, guikit.StCard, u.Colors().Text, txt)
	}
}

// notice is an amber note: a warning that is not an error.
func (u *UI) notice(txt string) layout.Widget {
	return func(gtx layout.Context) layout.Dimensions {
		b := guikit.Box{Radius: 10, Fill: guikit.Over(u.Colors().Window, u.Colors().Warning, 0.1),
			Border: guikit.Over(u.Colors().Window, u.Colors().Warning, 0.35), BorderWidth: 1}
		return b.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			gtx.Constraints.Min.X = gtx.Constraints.Max.X
			return layout.Inset{Top: 12, Bottom: 12, Left: 16, Right: 16}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
					layout.Rigid(func(gtx layout.Context) layout.Dimensions { return guikit.WarnIcon(gtx, 18, u.Colors().Warning) }),
					layout.Rigid(layout.Spacer{Width: 12}.Layout),
					layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
						return u.LabelLines(gtx, guikit.StBody, u.Colors().Text, 1.4, txt)
					}),
				)
			})
		})
	}
}

// errorBox is th.Warning as a widget.
func (u *UI) errorBox(txt string) layout.Widget {
	return func(gtx layout.Context) layout.Dimensions { return u.Warning(gtx, txt) }
}

// buttons is a row of buttons, 10 dp apart.
func (u *UI) buttons(as ...guikit.Action) layout.Widget {
	return func(gtx layout.Context) layout.Dimensions {
		var kids []layout.FlexChild
		for i, a := range as {
			if i > 0 {
				kids = append(kids, layout.Rigid(layout.Spacer{Width: 10}.Layout))
			}
			kids = append(kids, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return u.Button(gtx, a.Click, a.Kind, a.Label, !a.Disabled)
			}))
		}
		return layout.Flex{Alignment: layout.Middle}.Layout(gtx, kids...)
	}
}

// panel is a dark box the full width, for a list or a log.
func (u *UI) panel(w layout.Widget) layout.Widget {
	return func(gtx layout.Context) layout.Dimensions {
		gtx.Constraints.Min.X = gtx.Constraints.Max.X
		return u.Panel(gtx, u.Colors().RadiusS, layout.UniformInset(unit.Dp(14)), func(gtx layout.Context) layout.Dimensions {
			gtx.Constraints.Min.X = gtx.Constraints.Max.X
			return w(gtx)
		})
	}
}

// log shows the job's last lines in mono, the newest at the bottom.
func (u *UI) log(lines []string, n int) layout.Widget {
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return u.panel(func(gtx layout.Context) layout.Dimensions {
		var c col
		for _, l := range lines {
			c.add(func(gtx layout.Context) layout.Dimensions {
				return u.LabelMax(gtx, guikit.StMono12, u.Colors().Text2, 1, l)
			})
		}
		if len(lines) == 0 {
			c.add(u.dim(i18n.T("Waiting for the first lines…")))
		}
		return c.layout(gtx)
	})
}

// row lays a label (title over sub) at the left and w at the right.
func (u *UI) row(title, sub string, right layout.Widget) layout.Widget {
	return func(gtx layout.Context) layout.Dimensions {
		gtx.Constraints.Min.X = gtx.Constraints.Max.X
		return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
			layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
				var c col
				c.add(func(gtx layout.Context) layout.Dimensions {
					return u.Label(gtx, guikit.StBody600, u.Colors().Text, title)
				})
				if sub != "" {
					c.gap(2)
					c.add(func(gtx layout.Context) layout.Dimensions {
						return u.LabelMax(gtx, guikit.StSmall, u.Colors().Dim, 3, sub)
					})
				}
				return c.layout(gtx)
			}),
			layout.Rigid(layout.Spacer{Width: 12}.Layout),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				if right == nil {
					return layout.Dimensions{}
				}
				return right(gtx)
			}),
		)
	}
}

// spinnerLine is a spinner and a line of text, for a running job.
func (u *UI) spinnerLine(txt string) layout.Widget {
	return func(gtx layout.Context) layout.Dimensions {
		gtx.Execute(op.InvalidateCmd{})
		phase := float32(u.now().UnixMilli()%1000) / 1000
		return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
			layout.Rigid(func(gtx layout.Context) layout.Dimensions { return u.Spinner(gtx, 18, phase) }),
			layout.Rigid(layout.Spacer{Width: 10}.Layout),
			layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
				return u.Label(gtx, guikit.StBody, u.Colors().Text2, txt)
			}),
		)
	}
}

// group is a section's rows in one rounded container, split by lines.
func (u *UI) group(rows ...layout.Widget) layout.Widget {
	return func(gtx layout.Context) layout.Dimensions { return u.Group(gtx, rows...) }
}

// actionRow is a settings row: title and sub at the left, extra (may be
// nil) and the actions at the right, the ones that do not fit in a "⋯"
// menu. key names the row for its menu: unique on the page and stable
// while the row is shown (a printer's name, a device's path).
func (u *UI) actionRow(key, title, sub string, extra layout.Widget, as ...guikit.Action) layout.Widget {
	o := u.menus[key]
	if o == nil {
		o = new(guikit.Overflow)
		u.menus[key] = o
	}
	return func(gtx layout.Context) layout.Dimensions {
		return u.ActionRow(gtx, title, sub, extra, as, o)
	}
}

// rowButtons is a row of row-sized buttons, for a row whose actions are
// one choice (a codec) and must all show.
func (u *UI) rowButtons(as ...guikit.Action) layout.Widget {
	return func(gtx layout.Context) layout.Dimensions {
		var kids []layout.FlexChild
		for i, a := range as {
			if i > 0 {
				kids = append(kids, layout.Rigid(layout.Spacer{Width: 8}.Layout))
			}
			kids = append(kids, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return u.RowButton(gtx, a.Click, a.Kind, a.Label, !a.Disabled)
			}))
		}
		return layout.Flex{Alignment: layout.Middle}.Layout(gtx, kids...)
	}
}

// note is a row of a group that is a sentence in the dim colour: the
// group's empty state.
func (u *UI) note(txt string) layout.Widget { return u.dim(txt) }

// section is a section's heading with its actions at the right of it
// ("Search again").
func (u *UI) section(title string, as ...guikit.Action) layout.Widget {
	if len(as) == 0 {
		return u.caps(title)
	}
	return func(gtx layout.Context) layout.Dimensions {
		gtx.Constraints.Min.X = gtx.Constraints.Max.X
		kids := []layout.FlexChild{layout.Flexed(1, u.caps(title))}
		for _, a := range as {
			kids = append(kids, layout.Rigid(layout.Spacer{Width: 8}.Layout), layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return u.RowButton(gtx, a.Click, a.Kind, a.Label, !a.Disabled)
			}))
		}
		return layout.Flex{Alignment: layout.Middle}.Layout(gtx, kids...)
	}
}
