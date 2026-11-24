package main

import (
	"errors"
	"fmt"
	"sync"

	"gioui.org/layout"
	"gioui.org/widget"

	"github.com/kairos-community/isotope/pkg/guikit"
	"github.com/kairos-community/isotope/pkg/i18n"
)

func init() {
	pageMakers[PNetwork] = func(u *UI) page {
		return &networkPage{basePage: basePage{u}, pass: guikit.NewSecret(), ssid: guikit.NewField(true)}
	}
}

// netView is which of the page's four faces is showing.
type netView int

const (
	netChecking  netView = iota // asking NetworkManager
	netConnected                // online: "Connected to <name>"
	netNoDevice                 // no Wi-Fi and no connection
	netList                     // Wi-Fi networks to pick from
	netError                    // the first look failed: Try again
)

// networkPage shows the connection, or the Wi-Fi networks to join. Every
// call to NetworkManager runs in the background. Continue is always on:
// the machine may go on without a network (Apps then says so).
type networkPage struct {
	basePage
	list widget.List

	mu       sync.Mutex // the background calls write under it
	wg       sync.WaitGroup
	started  bool
	loaded   bool
	st       NetState
	aps      []AccessPoint
	selected string // the SSID picked from the list
	hidden   bool   // the "Hidden network…" form
	busy     bool   // a Connect is running
	loadErr  string // the first load failed

	pass, ssid *guikit.Field // pass.Err holds the connect error
	connectBtn widget.Clickable
	hiddenLink widget.Clickable
	listLink   widget.Clickable
	rescan     widget.Clickable
	retry      widget.Clickable
	rows       map[string]*widget.Clickable
}

// load asks NetworkManager. It blocks; the page runs it in the background.
func (p *networkPage) load() {
	p.mu.Lock()
	first := !p.loaded
	p.mu.Unlock()
	var st NetState
	var aps []AccessPoint
	var err error
	if n := p.u.hooks.Network; n != nil {
		st, err = n.State()
		if err == nil && st.HasWifi && !st.Connected {
			aps, err = n.AccessPoints()
		}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if err != nil {
		// A rescan that fails keeps what the page already shows.
		if first {
			p.loadErr = i18n.Tf("Could not reach NetworkManager: %v", err)
		}
		return
	}
	p.st, p.aps, p.loaded, p.loadErr = st, aps, true, ""
}

// background runs fn without blocking the frame.
func (p *networkPage) background(fn func()) {
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		fn()
		if p.u.hooks.Invalidate != nil {
			p.u.hooks.Invalidate()
		}
	}()
}

// wait blocks until the background calls have ended (tests).
func (p *networkPage) wait() { p.wg.Wait() }

func (p *networkPage) view() netView {
	p.mu.Lock()
	defer p.mu.Unlock()
	switch {
	case !p.loaded && p.loadErr != "":
		return netError
	case !p.loaded:
		return netChecking
	case p.st.Connected:
		return netConnected
	case !p.st.HasWifi:
		return netNoDevice
	}
	return netList
}

// choose picks a network from the list.
func (p *networkPage) choose(ssid string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.selected, p.hidden = ssid, false
	p.pass.Editor.SetText("")
	p.pass.Err = ""
	if p.secured(ssid) {
		p.u.focus = &p.pass.Editor
	}
}

func (p *networkPage) secured(ssid string) bool {
	for _, a := range p.aps {
		if a.SSID == ssid {
			return a.Secured
		}
	}
	return false
}

// hiddenMode opens the form for a network that does not announce itself.
func (p *networkPage) hiddenMode() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.hidden, p.selected = true, ""
	p.pass.Editor.SetText("")
	p.pass.Err = ""
	p.ssid.Err = ""
	p.u.focus = &p.ssid.Editor
}

// connect joins the picked or typed network in the background. A failure
// is shown under the password field.
func (p *networkPage) connect() {
	p.mu.Lock()
	if p.busy {
		p.mu.Unlock()
		return
	}
	ssid, hidden := p.selected, p.hidden
	if hidden {
		ssid = p.ssid.Editor.Text()
	}
	password := p.pass.Editor.Text()
	p.ssid.Err, p.pass.Err = "", ""
	switch {
	case hidden && ssid == "":
		p.ssid.Err = i18n.T("Enter the network name")
	case ssid == "":
		// nothing picked
	case !hidden && p.secured(ssid) && password == "":
		p.pass.Err = i18n.T("Enter the password")
	default:
		p.busy = true
	}
	busy := p.busy
	p.mu.Unlock()
	n := p.u.hooks.Network
	if !busy || n == nil {
		return
	}
	p.background(func() {
		err := n.Connect(ssid, password, hidden)
		p.mu.Lock()
		defer p.mu.Unlock()
		p.busy = false
		if err != nil {
			p.pass.Err = err.Error()
			if errors.Is(err, errWrongPassword) {
				p.pass.Err = i18n.T(errWrongPassword.Error())
			}
			return
		}
		p.st = NetState{Connected: true, HasWifi: true, Name: ssid}
	})
}

func (p *networkPage) row(ssid string) *widget.Clickable {
	if p.rows == nil {
		p.rows = map[string]*widget.Clickable{}
	}
	if p.rows[ssid] == nil {
		p.rows[ssid] = new(widget.Clickable)
	}
	return p.rows[ssid]
}

func (p *networkPage) Update(gtx layout.Context) {
	if !p.started {
		p.started = true
		if p.u.hooks.Network != nil {
			p.background(p.load)
		} else {
			p.load()
		}
	}
	p.mu.Lock()
	aps := append([]AccessPoint(nil), p.aps...)
	p.mu.Unlock()
	var pick string
	for _, a := range aps {
		if p.row(a.SSID).Clicked(gtx) {
			pick = a.SSID
		}
	}
	submitted := p.pass.Submitted(gtx)
	if p.ssid.Submitted(gtx) {
		submitted = true
	}
	if pick != "" {
		p.choose(pick)
	}
	if p.hiddenLink.Clicked(gtx) {
		p.hiddenMode()
	}
	if p.listLink.Clicked(gtx) {
		p.mu.Lock()
		p.hidden = false
		p.mu.Unlock()
	}
	if p.retry.Clicked(gtx) {
		p.mu.Lock()
		p.loadErr = ""
		p.mu.Unlock()
		p.background(p.load)
	}
	if p.rescan.Clicked(gtx) {
		p.background(p.load)
	}
	if p.connectBtn.Clicked(gtx) || submitted {
		p.connect()
	}
}

// Continue goes on, with or without a network.
func (p *networkPage) Continue() {
	if p.view() == netConnected {
		p.u.flow.Apply()
	}
	p.u.flow.Next()
}

func (p *networkPage) Body(gtx layout.Context) layout.Dimensions {
	u := p.u
	return u.scrolled(gtx, &p.list, func(gtx layout.Context) layout.Dimensions {
		switch p.view() {
		case netConnected:
			p.mu.Lock()
			name := p.st.Name
			p.mu.Unlock()
			line := i18n.T("Connected")
			if name != "" {
				line = i18n.Tf("Connected to %s", name)
			}
			return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
				layout.Rigid(u.Heading(i18n.T("Network"), i18n.T("You are online. Apps and updates can use the connection."))),
				guikit.VGap(22),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					gtx.Constraints.Max.X = min(gtx.Constraints.Max.X, guikit.Dp(gtx, 580))
					gtx.Constraints.Min.X = gtx.Constraints.Max.X
					return guikit.Panel(gtx, 10, layout.Inset{Top: 14, Bottom: 14, Left: 16, Right: 16}, func(gtx layout.Context) layout.Dimensions {
						return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
							layout.Rigid(func(gtx layout.Context) layout.Dimensions { return guikit.CheckIcon(gtx, 18) }),
							layout.Rigid(layout.Spacer{Width: 12}.Layout),
							layout.Rigid(func(gtx layout.Context) layout.Dimensions {
								return u.Label(gtx, guikit.StBody600, guikit.ColText, line)
							}))
					})
				}))
		case netNoDevice:
			return u.Heading(i18n.T("Network"), i18n.T("Plug in a network cable, or continue without a network."))(gtx)
		case netList:
			return p.networks(gtx)
		case netError:
			p.mu.Lock()
			msg := p.loadErr
			p.mu.Unlock()
			return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
				layout.Rigid(u.Heading(i18n.T("Network"), i18n.T("Isotope could not look for networks."))),
				guikit.VGap(22),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					gtx.Constraints.Max.X = min(gtx.Constraints.Max.X, guikit.Dp(gtx, 580))
					return u.Warning(gtx, msg)
				}),
				guikit.VGap(16),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return u.Button(gtx, &p.retry, guikit.ButtonSecondary, i18n.T("Try again"), true)
				}))
		}
		return u.Heading(i18n.T("Network"), i18n.T("Looking for networks…"))(gtx)
	})
}

// networks is the list of Wi-Fi networks, with the password field under
// the picked one, and the hidden-network form.
func (p *networkPage) networks(gtx layout.Context) layout.Dimensions {
	u := p.u
	p.mu.Lock()
	defer p.mu.Unlock()
	kids := []layout.FlexChild{
		layout.Rigid(u.Heading(i18n.T("Network"), i18n.T("Pick a Wi-Fi network. You can also continue without one."))),
		guikit.VGap(20),
	}
	col := func(w layout.Widget) layout.Widget {
		return func(gtx layout.Context) layout.Dimensions {
			gtx.Constraints.Max.X = min(gtx.Constraints.Max.X, guikit.Dp(gtx, 580))
			return w(gtx)
		}
	}
	if p.hidden {
		kids = append(kids, layout.Rigid(col(p.hiddenForm)))
		kids = append(kids, guikit.VGap(14), layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return u.Link(gtx, &p.listLink, i18n.T("Show the list of networks"))
		}))
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx, kids...)
	}
	for _, a := range p.aps {
		kids = append(kids, layout.Rigid(col(func(gtx layout.Context) layout.Dimensions { return p.apRow(gtx, a) })), guikit.VGap(8))
	}
	if len(p.aps) == 0 {
		kids = append(kids, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return u.Label(gtx, guikit.StBody, guikit.ColDim, i18n.T("No Wi-Fi networks found."))
		}), guikit.VGap(8))
	}
	kids = append(kids, guikit.VGap(6), layout.Rigid(func(gtx layout.Context) layout.Dimensions {
		return layout.Flex{}.Layout(gtx,
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return u.Link(gtx, &p.hiddenLink, i18n.T("Hidden network…"))
			}),
			layout.Rigid(layout.Spacer{Width: 22}.Layout),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions { return u.Link(gtx, &p.rescan, i18n.T("Scan again")) }))
	}))
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx, kids...)
}

// apRow is one network; the picked one opens its password field.
func (p *networkPage) apRow(gtx layout.Context, a AccessPoint) layout.Dimensions {
	u := p.u
	on := p.selected == a.SSID
	gtx.Constraints.Min.X = gtx.Constraints.Max.X
	return u.Card(gtx, p.row(a.SSID), on, layout.Inset{Top: 12, Bottom: 12, Left: 14, Right: 14}, func(gtx layout.Context) layout.Dimensions {
		gtx.Constraints.Min.X = gtx.Constraints.Max.X
		head := func(gtx layout.Context) layout.Dimensions {
			return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
				layout.Rigid(func(gtx layout.Context) layout.Dimensions { return guikit.Radio(gtx, on) }),
				layout.Rigid(layout.Spacer{Width: 12}.Layout),
				layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
					return u.LabelMax(gtx, guikit.StBody600, guikit.ColText, 1, a.SSID)
				}),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					if !a.Secured {
						return layout.Dimensions{}
					}
					return layout.Inset{Right: 14}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
						return u.Label(gtx, guikit.StTiny, guikit.ColDim, i18n.T("Secured"))
					})
				}),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return u.Label(gtx, guikit.StSmall, guikit.ColDim, fmt.Sprintf("%d%%", a.Strength))
				}))
		}
		if !on {
			return head(gtx)
		}
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
			layout.Rigid(head),
			guikit.VGap(14),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions { return p.connectBlock(gtx, a.Secured) }))
	})
}

// connectBlock is the password field (for a secured network) and Connect.
func (p *networkPage) connectBlock(gtx layout.Context, secured bool) layout.Dimensions {
	u := p.u
	var kids []layout.FlexChild
	if secured {
		kids = append(kids, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return u.Labelled(gtx, i18n.T("Password"), p.pass, "", func(gtx layout.Context) layout.Dimensions {
				return u.Input(gtx, p.pass, guikit.StBody, 38, "", nil)
			})
		}), guikit.VGap(12))
	}
	kids = append(kids, layout.Rigid(p.connectButton))
	if !secured && p.pass.Err != "" {
		kids = append(kids, guikit.VGap(8), layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return u.Label(gtx, guikit.StTiny, guikit.ColError, p.pass.Err)
		}))
	}
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx, kids...)
}

func (p *networkPage) connectButton(gtx layout.Context) layout.Dimensions {
	txt := i18n.T("Connect")
	if p.busy {
		txt = i18n.T("Connecting…")
	}
	return p.u.Button(gtx, &p.connectBtn, guikit.ButtonPrimary, txt, !p.busy)
}

// hiddenForm takes the name and the password of a hidden network.
func (p *networkPage) hiddenForm(gtx layout.Context) layout.Dimensions {
	u := p.u
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return u.Labelled(gtx, i18n.T("Network name"), p.ssid, "", func(gtx layout.Context) layout.Dimensions {
				return u.Input(gtx, p.ssid, guikit.StBody, 38, "", nil)
			})
		}),
		guikit.VGap(14),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return u.Labelled(gtx, i18n.T("Password"), p.pass, i18n.T("Leave empty for an open network."), func(gtx layout.Context) layout.Dimensions {
				return u.Input(gtx, p.pass, guikit.StBody, 38, "", nil)
			})
		}),
		guikit.VGap(14),
		layout.Rigid(p.connectButton),
	)
}
