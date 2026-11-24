package main

import (
	"sync"
	"time"

	"gioui.org/io/event"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/text"
	"gioui.org/unit"
	"gioui.org/widget"

	"github.com/kairos-community/isotope/pkg/guikit"
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

	nav    guikit.Nav
	page   Page
	pageAt time.Time
	pages  map[Page]pageView
	scale  float32
	window guikit.Window

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
}

const maxLines = 200

func newUI(sys System, sh *text.Shaper, h Hooks, first Page) *UI {
	u := &UI{
		Theme: &guikit.Theme{Shaper: sh},
		sys:   sys,
		hooks: h,
		now:   time.Now,
		pages: map[Page]pageView{},
		scale: 1,
	}
	if u.hooks.Invalidate == nil {
		u.hooks.Invalidate = func() {}
	}
	if u.hooks.Start == nil {
		u.hooks.Start = start
	}
	u.nav.Title = "Isotope System"
	u.nav.Note = sys.Note()
	for _, p := range Pages {
		u.nav.Items = append(u.nav.Items, guikit.NavItem{Name: p.Title(), Later: later[p]})
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
		u.stErr = "The update service (isotope-updated) is not available: the system bus cannot be reached."
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

// Layout draws one frame over the whole OS window.
func (u *UI) Layout(gtx layout.Context) layout.Dimensions {
	gtx = guikit.Scaled(gtx, u.scale)
	event.Op(gtx.Ops, u)
	u.mu.Lock()
	dlg, want, tab := u.dlg, u.toShow, u.toTab
	u.toShow, u.toTab = "", ""
	u.mu.Unlock()
	if want != "" {
		u.showTab(want, tab)
	}
	guikit.WindowKeys(gtx, &u.scale, func() {
		if dlg != nil && dlg.cancel >= 0 {
			u.pick(dlg, dlg.cancel)
		}
	})
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
	dims := u.window.Layout(gtx, nil, func(gtx layout.Context) layout.Dimensions {
		return layout.Flex{}.Layout(gtx,
			layout.Rigid(func(gtx layout.Context) layout.Dimensions { return u.NavSidebar(gtx, &u.nav) }),
			layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
				gtx.Constraints.Min = gtx.Constraints.Max
				body := func(gtx layout.Context) layout.Dimensions {
					gtx.Constraints.Min = gtx.Constraints.Max
					return guikit.Slide(gtx, u.pageAt, u.now(), v.Body)
				}
				if footer == nil {
					return body(gtx)
				}
				return layout.Flex{Axis: layout.Vertical}.Layout(gtx, layout.Flexed(1, body), layout.Rigid(footer))
			}),
		)
	})
	if dlg != nil {
		dlg.layout(gtx, u)
	}
	if u.now().Sub(u.pageAt) < guikit.SlideTime {
		gtx.Execute(op.InvalidateCmd{})
	}
	return dims
}

// scrolled lays w in the page's padding, in a list that scrolls when w is
// taller than the column.
func (u *UI) scrolled(gtx layout.Context, l *widget.List, w layout.Widget) layout.Dimensions {
	l.Axis = layout.Vertical
	return guikit.Scrolled(gtx, l, func(gtx layout.Context) layout.Dimensions {
		return guikit.Pad(gtx, func(gtx layout.Context) layout.Dimensions {
			gtx.Constraints.Min.X = gtx.Constraints.Max.X
			return w(gtx)
		})
	})
}

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
		return u.LabelLines(gtx, guikit.StBody, guikit.ColText2, 1.5, txt)
	}
}

// dim is a small line in the dim colour.
func (u *UI) dim(txt string) layout.Widget {
	return func(gtx layout.Context) layout.Dimensions {
		return u.LabelLines(gtx, guikit.StSmall, guikit.ColDim, 1.4, txt)
	}
}

// caps is a section title.
func (u *UI) caps(txt string) layout.Widget {
	return func(gtx layout.Context) layout.Dimensions {
		return u.Label(gtx, guikit.StCaps, guikit.ColDim, txt)
	}
}

// lead is a status line, larger.
func (u *UI) lead(txt string) layout.Widget {
	return func(gtx layout.Context) layout.Dimensions {
		return u.Label(gtx, guikit.StCard, guikit.ColText, txt)
	}
}

// notice is an amber note: a warning that is not an error.
func (u *UI) notice(txt string) layout.Widget {
	return func(gtx layout.Context) layout.Dimensions {
		b := guikit.Box{Radius: 10, Fill: guikit.Over(guikit.ColWindow, guikit.ColWarn, 0.1),
			Border: guikit.Over(guikit.ColWindow, guikit.ColWarn, 0.35), BorderWidth: 1}
		return b.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			gtx.Constraints.Min.X = gtx.Constraints.Max.X
			return layout.Inset{Top: 12, Bottom: 12, Left: 16, Right: 16}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
					layout.Rigid(func(gtx layout.Context) layout.Dimensions { return guikit.WarnIcon(gtx, 18, guikit.ColWarn) }),
					layout.Rigid(layout.Spacer{Width: 12}.Layout),
					layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
						return u.LabelLines(gtx, guikit.StBody, guikit.ColText, 1.4, txt)
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
		return guikit.Panel(gtx, 10, layout.UniformInset(unit.Dp(14)), func(gtx layout.Context) layout.Dimensions {
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
				return u.LabelMax(gtx, guikit.StMono12, guikit.ColText2, 1, l)
			})
		}
		if len(lines) == 0 {
			c.add(u.dim("Waiting for the first lines…"))
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
					return u.Label(gtx, guikit.StBody600, guikit.ColText, title)
				})
				if sub != "" {
					c.gap(2)
					c.add(func(gtx layout.Context) layout.Dimensions {
						return u.LabelMax(gtx, guikit.StSmall, guikit.ColDim, 3, sub)
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
			layout.Rigid(func(gtx layout.Context) layout.Dimensions { return guikit.Spinner(gtx, 18, phase) }),
			layout.Rigid(layout.Spacer{Width: 10}.Layout),
			layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
				return u.Label(gtx, guikit.StBody, guikit.ColText2, txt)
			}),
		)
	}
}
