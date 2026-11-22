package main

import (
	"fmt"

	"gioui.org/layout"
	"gioui.org/widget"

	"github.com/kairos-community/isotope/guikit"
)

func init() {
	pageMakers[PageNetwork] = func(u *UI) pageView { return &networkPage{u: u, apClicks: map[string]*apClicks{}} }
}

// networkPage: the devices, Wi-Fi on and off, the networks in range with
// connect, and the saved ones with forget, through NetworkManager.
type networkPage struct {
	u    *UI
	list widget.List

	// Under u.mu.
	state   NetState
	aps     []AccessPoint
	loading bool
	err     string
	working string // a connect or forget in progress

	// chosen is the secured network whose password is asked for.
	chosen   string
	password *guikit.Field

	apClicks                       map[string]*apClicks
	refresh, wifi, disconnect, own widget.Clickable
	join, cancelJoin               widget.Clickable
}

type apClicks struct{ connect, forget widget.Clickable }

func (p *networkPage) Shown() {
	if p.password == nil {
		p.password = guikit.NewSecret()
	}
	p.load()
}

func (p *networkPage) load() {
	u, n := p.u, p.u.hooks.Network
	if n == nil {
		return
	}
	u.mu.Lock()
	if p.loading {
		u.mu.Unlock()
		return
	}
	p.loading = true
	u.mu.Unlock()
	u.async(func() {
		st, err := n.State()
		var aps []AccessPoint
		if err == nil && st.HasWifi && st.WifiOn {
			aps, _ = n.AccessPoints()
		}
		u.mu.Lock()
		defer u.mu.Unlock()
		p.loading = false
		if err != nil {
			p.err = "NetworkManager is not available: " + err.Error()
			return
		}
		p.state, p.aps, p.err = st, aps, ""
	})
}

// do runs a NetworkManager change and reloads.
func (p *networkPage) do(working string, f func(Network) error) {
	u, n := p.u, p.u.hooks.Network
	if n == nil {
		return
	}
	u.mu.Lock()
	p.working = working
	u.mu.Unlock()
	u.async(func() {
		err := f(n)
		u.mu.Lock()
		p.working = ""
		u.mu.Unlock()
		if err != nil {
			u.tell("Network", err.Error())
		}
		p.load()
	})
}

func (p *networkPage) Update(gtx layout.Context) {
	u := p.u
	u.mu.Lock()
	st, aps, chosen := p.state, p.aps, p.chosen
	u.mu.Unlock()
	if p.refresh.Clicked(gtx) {
		p.load()
	}
	if p.wifi.Clicked(gtx) {
		on := !st.WifiOn
		p.do("Turning Wi-Fi "+map[bool]string{true: "on", false: "off"}[on]+"…", func(n Network) error { return n.SetWifi(on) })
	}
	if p.disconnect.Clicked(gtx) {
		p.do("Disconnecting…", func(n Network) error { return n.Disconnect() })
	}
	if p.own.Clicked(gtx) {
		if _, argv := otherSettings(u.sys.Desktop); argv != nil {
			if err := u.hooks.Start(argv...); err != nil {
				u.tell("Network", err.Error())
			}
		}
	}
	for _, a := range aps {
		k := p.clicksFor(a.SSID)
		ssid := a.SSID
		if k.connect.Clicked(gtx) {
			if a.Secured && !contains(st.Saved, ssid) {
				u.mu.Lock()
				p.chosen = ssid
				u.mu.Unlock()
				p.password.Editor.SetText("")
				p.password.Err = ""
				gtx.Execute(focus(&p.password.Editor))
			} else {
				p.do("Connecting to "+ssid+"…", func(n Network) error { return n.Connect(ssid, "", false) })
			}
		}
	}
	for _, s := range st.Saved {
		ssid := s
		if p.clicksFor(ssid).forget.Clicked(gtx) {
			u.ask("Forget "+ssid+"?", "Its password is deleted, and this computer no longer joins it by itself.",
				choice{Label: "Cancel", Kind: guikit.ButtonSecondary},
				choice{Label: "Forget", Kind: guikit.ButtonDanger, Do: func() {
					p.do("Forgetting "+ssid+"…", func(n Network) error { return n.Forget(ssid) })
				}})
		}
	}
	if chosen != "" {
		submitted := p.password.Submitted(gtx)
		if p.cancelJoin.Clicked(gtx) {
			u.mu.Lock()
			p.chosen = ""
			u.mu.Unlock()
		} else if p.join.Clicked(gtx) || submitted {
			pw := p.password.Editor.Text()
			if len(pw) < 8 {
				p.password.Err = "A Wi-Fi password has at least 8 characters."
				return
			}
			u.mu.Lock()
			p.chosen = ""
			u.mu.Unlock()
			p.password.Editor.SetText("")
			p.do("Connecting to "+chosen+"…", func(n Network) error { return n.Connect(chosen, pw, false) })
		}
	}
}

func (p *networkPage) clicksFor(ssid string) *apClicks {
	k, ok := p.apClicks[ssid]
	if !ok {
		k = &apClicks{}
		p.apClicks[ssid] = k
	}
	return k
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func (p *networkPage) Footer() (leading, trailing []guikit.Action) {
	if name, argv := otherSettings(p.u.sys.Desktop); argv != nil {
		leading = append(leading, guikit.Action{Click: &p.own, Kind: guikit.ButtonGhost, Label: "Open " + name})
	}
	p.u.mu.Lock()
	loading := p.loading
	p.u.mu.Unlock()
	return leading, []guikit.Action{{Click: &p.refresh, Kind: guikit.ButtonSecondary, Label: "Refresh", Disabled: loading || p.u.hooks.Network == nil}}
}

func (p *networkPage) Body(gtx layout.Context) layout.Dimensions {
	u := p.u
	u.mu.Lock()
	st, aps, err, working, chosen, loading := p.state, p.aps, p.err, p.working, p.chosen, p.loading
	u.mu.Unlock()
	return u.scrolled(gtx, &p.list, func(gtx layout.Context) layout.Dimensions {
		var c col
		c.add(u.Heading("Network", "Wired and Wi-Fi connections, through NetworkManager. The same connections the panel's network menu shows."))
		c.gap(24)
		if u.hooks.Network == nil || err != "" {
			c.add(u.errorBox(orUnknown(err)))
			return c.layout(gtx)
		}
		if st.Connected {
			c.add(u.lead("Connected to " + orUnknown(st.Name)))
		} else {
			c.add(u.lead("Not connected"))
		}
		if working != "" {
			c.gap(12)
			c.add(u.spinnerLine(working))
		}
		if len(st.Devices) > 0 {
			c.gap(24)
			c.add(u.caps("DEVICES"))
			c.gap(10)
			c.add(u.panel(func(gtx layout.Context) layout.Dimensions {
				var rows col
				for i, d := range st.Devices {
					if i > 0 {
						rows.gap(8)
					}
					sub := d.State
					if d.Connection != "" {
						sub += " · " + d.Connection
					}
					if d.Address != "" {
						sub += " · " + d.Address
					}
					rows.add(u.row(fmt.Sprintf("%s (%s)", d.Kind, d.Iface), sub, nil))
				}
				return rows.layout(gtx)
			}))
		}
		if !st.HasWifi {
			return c.layout(gtx)
		}
		c.gap(26)
		c.add(u.row("Wi-Fi", map[bool]string{true: "On", false: "Off"}[st.WifiOn], func(gtx layout.Context) layout.Dimensions {
			label := "Turn on"
			if st.WifiOn {
				label = "Turn off"
			}
			as := []guikit.Action{{Click: &p.wifi, Kind: guikit.ButtonSecondary, Label: label}}
			for _, d := range st.Devices {
				if d.Kind == "Wi-Fi" && d.State == "Connected" {
					as = append(as, guikit.Action{Click: &p.disconnect, Kind: guikit.ButtonGhost, Label: "Disconnect"})
					break
				}
			}
			return u.buttons(as...)(gtx)
		}))
		if chosen != "" {
			c.gap(14)
			c.add(u.panel(func(gtx layout.Context) layout.Dimensions {
				return u.Labelled(gtx, "Password for "+chosen, p.password, "", func(gtx layout.Context) layout.Dimensions {
					return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
						layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
							return u.Input(gtx, p.password, guikit.StBody, 38, "", nil)
						}),
						layout.Rigid(layout.Spacer{Width: 10}.Layout),
						layout.Rigid(u.buttons(
							guikit.Action{Click: &p.cancelJoin, Kind: guikit.ButtonGhost, Label: "Cancel"},
							guikit.Action{Click: &p.join, Kind: guikit.ButtonPrimary, Label: "Connect"},
						)),
					)
				})
			}))
		}
		if st.WifiOn {
			c.gap(18)
			c.add(u.caps("NETWORKS IN RANGE"))
			c.gap(10)
			switch {
			case loading && len(aps) == 0:
				c.add(u.spinnerLine("Looking for networks…"))
			case len(aps) == 0:
				c.add(u.dim("No network in range."))
			default:
				c.add(u.panel(func(gtx layout.Context) layout.Dimensions {
					var rows col
					for i, a := range aps {
						if i > 0 {
							rows.gap(8)
						}
						k := p.clicksFor(a.SSID)
						sub := fmt.Sprintf("Signal %d%%", a.Strength)
						if a.Secured {
							sub += " · secured"
						}
						if contains(st.Saved, a.SSID) {
							sub += " · saved"
						}
						title := a.SSID
						var right layout.Widget
						if st.Connected && a.SSID == st.Name {
							title += " (connected)"
						} else {
							right = u.buttons(guikit.Action{Click: &k.connect, Kind: guikit.ButtonSecondary, Label: "Connect", Disabled: working != ""})
						}
						rows.add(u.row(title, sub, right))
					}
					return rows.layout(gtx)
				}))
			}
		}
		if len(st.Saved) > 0 {
			c.gap(26)
			c.add(u.caps("SAVED NETWORKS"))
			c.gap(10)
			c.add(u.panel(func(gtx layout.Context) layout.Dimensions {
				var rows col
				for i, s := range st.Saved {
					if i > 0 {
						rows.gap(8)
					}
					k := p.clicksFor(s)
					rows.add(u.row(s, "", u.buttons(guikit.Action{Click: &k.forget, Kind: guikit.ButtonGhost, Label: "Forget", Disabled: working != ""})))
				}
				return rows.layout(gtx)
			}))
		}
		return c.layout(gtx)
	})
}
