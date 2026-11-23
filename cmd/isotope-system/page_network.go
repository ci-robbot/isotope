package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"gioui.org/layout"
	"gioui.org/widget"

	"github.com/kairos-community/isotope/pkg/guikit"
)

func init() {
	pageMakers[PageNetwork] = func(u *UI) pageView {
		return &networkPage{u: u, apClicks: map[string]*apClicks{}, vpnClicks: map[string]*vpnClicks{}, fwClicks: map[string]*fwClicks{}}
	}
}

// networkPage: the devices, Wi-Fi on and off, the networks in range with
// connect, and the saved ones with forget, through NetworkManager. The VPN
// section is NetworkManager's too; the Firewall section is isotope-updated's
// (firewall.go).
type networkPage struct {
	u    *UI
	list widget.List

	// Under u.mu.
	state   NetState
	vpns    []VPN
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
	importWG                       widget.Clickable
	wgFile, wgName                 *guikit.Field // of the open import dialog
	addOC                          widget.Clickable
	oc                             *ocForm // the open "Add company VPN" dialog
	vpnClicks                      map[string]*vpnClicks

	// The firewall (firewall.go). fw, fwLoaded and fwErr are under u.mu.
	fw              Firewall
	fwLoaded        bool
	fwErr           string
	fwToggle, fwAdd widget.Clickable
	fwReset         widget.Clickable
	fwClicks        map[string]*fwClicks
	fwForm          *fwForm // the open "Allow incoming" dialog
}

type apClicks struct{ connect, forget widget.Clickable }

type vpnClicks struct{ toggle, remove widget.Clickable }

func (p *networkPage) Shown() {
	if p.password == nil {
		p.password = guikit.NewSecret()
	}
	p.load()
	p.loadFirewall()
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
		var vpns []VPN
		if err == nil {
			vpns, _ = n.VPNs() // the rest of the page works without it
		}
		u.mu.Lock()
		defer u.mu.Unlock()
		p.loading = false
		if err != nil {
			p.err = "NetworkManager is not available: " + err.Error()
			return
		}
		p.state, p.aps, p.vpns, p.err = st, aps, vpns, ""
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
	st, aps, chosen, vpns := p.state, p.aps, p.chosen, p.vpns
	u.mu.Unlock()
	if p.importWG.Clicked(gtx) {
		u.chooseFile(wgFileRequest, p.importDialog, func() { p.importDialog("") })
	}
	if p.addOC.Clicked(gtx) {
		p.openConnectDialog()
	}
	for _, v := range vpns {
		v, k := v, p.vpnClicksFor(v.UUID)
		if k.toggle.Clicked(gtx) {
			if v.Active {
				p.do("Disconnecting "+v.ID+"…", func(n Network) error { return n.DeactivateVPN(v.UUID) })
			} else {
				p.do("Connecting "+v.ID+"…", func(n Network) error { return n.ActivateVPN(v.UUID) })
			}
		}
		if k.remove.Clicked(gtx) {
			u.ask("Remove "+v.ID+"?", "The VPN connection and its keys are deleted from this computer.",
				choice{Label: "Cancel", Kind: guikit.ButtonSecondary},
				choice{Label: "Remove", Kind: guikit.ButtonDanger, Do: func() {
					p.do("Removing "+v.ID+"…", func(n Network) error { return n.RemoveVPN(v.UUID) })
				}})
		}
	}
	p.updateFirewall(gtx)
	if p.refresh.Clicked(gtx) {
		p.load()
		p.loadFirewall()
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

func (p *networkPage) vpnClicksFor(uuid string) *vpnClicks {
	k, ok := p.vpnClicks[uuid]
	if !ok {
		k = &vpnClicks{}
		p.vpnClicks[uuid] = k
	}
	return k
}

// wgName is the name of an imported connection: the one typed, else the
// file's name without .conf.
func wgName(typed, path string) string {
	if t := strings.TrimSpace(typed); t != "" {
		return t
	}
	return strings.TrimSuffix(filepath.Base(path), ".conf")
}

const wgMaxSize = 64 << 10

// importWireGuardFile reads the file at path and saves it as a connection.
func importWireGuardFile(n Network, name, path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	conf, err := io.ReadAll(io.LimitReader(f, wgMaxSize+1))
	if err != nil {
		return err
	}
	if len(conf) > wgMaxSize {
		return fmt.Errorf("%s is too big for a WireGuard file (the limit is 64 KiB)", filepath.Base(path))
	}
	if err := n.ImportWireGuard(name, conf); err != nil {
		return fmt.Errorf("%s: %w", filepath.Base(path), err)
	}
	return nil
}

// wgFileRequest is the file picker's question for "Import WireGuard…".
var wgFileRequest = FileRequest{
	Title: "Import a WireGuard file", Accept: "Import",
	Filters: []FileFilter{{Name: "WireGuard files", Patterns: []string{"*.conf"}}},
}

// importDialog asks for a name and saves the connection. chosen is the
// file the picker chose; without one (no picker) it asks for the path of a
// wg-quick file too.
func (p *networkPage) importDialog(chosen string) {
	u := p.u
	file, name := guikit.NewField(true), guikit.NewField(true)
	file.Editor.SetText(chosen)
	p.wgFile, p.wgName = file, name
	focused := false
	var dlg *dialog
	path := func() string { return sendPath(file.Editor.Text()) }
	dlg = newDialog("Import a WireGuard file", "", func(gtx layout.Context) layout.Dimensions {
		if file.Submitted(gtx) || name.Submitted(gtx) {
			u.pick(dlg, 1) // ignored while Import is off
		}
		if !focused {
			focused = true
			if chosen != "" {
				gtx.Execute(focus(&name.Editor))
			} else {
				gtx.Execute(focus(&file.Editor))
			}
		}
		var c col
		c.add(func(gtx layout.Context) layout.Dimensions {
			return u.Labelled(gtx, "File", file, "The path of a .conf file, for example ~/Downloads/work.conf.", func(gtx layout.Context) layout.Dimensions {
				return u.Input(gtx, file, guikit.StBody, 38, "", nil)
			})
		})
		c.gap(12)
		c.add(func(gtx layout.Context) layout.Dimensions {
			return u.Labelled(gtx, "Name", name, "Optional. The file name is the default.", func(gtx layout.Context) layout.Dimensions {
				return u.Input(gtx, name, guikit.StBody, 38, "", nil)
			})
		})
		return c.layout(gtx)
	}, 1,
		choice{Label: "Cancel", Kind: guikit.ButtonSecondary},
		choice{Label: "Import", Kind: guikit.ButtonPrimary, Enabled: func() bool { return path() != "" }, Do: func() {
			f, nm := path(), wgName(name.Editor.Text(), path())
			p.do("Importing "+nm+"…", func(n Network) error { return importWireGuardFile(n, nm, f) })
		}})
	u.open(dlg)
}

// ocForm is the "Add company VPN" dialog's state.
type ocForm struct {
	name, server, user *guikit.Field
	protocol           string // an OpenConnectProtocols ID
	pick               [len(OpenConnectProtocols)]widget.Clickable
}

func (f *ocForm) value() OpenConnect {
	return OpenConnect{Name: f.name.Editor.Text(), Gateway: f.server.Editor.Text(), Protocol: f.protocol, User: f.user.Editor.Text()}
}

// openConnectDialog asks for a company VPN's name, server, type and user
// name, and saves it. The password, and a second factor when the server
// asks for one, are asked for at each connect by the tray's secret agent.
func (p *networkPage) openConnectDialog() {
	u := p.u
	f := &ocForm{name: guikit.NewField(true), server: guikit.NewField(true), user: guikit.NewField(true), protocol: OpenConnectProtocols[0].ID}
	p.oc = f
	focused := false
	var dlg *dialog
	ok := func() bool { _, err := openConnectSettings(f.value()); return err == nil }
	input := func(fl *guikit.Field, title, hint string) layout.Widget {
		return func(gtx layout.Context) layout.Dimensions {
			return u.Labelled(gtx, title, fl, hint, func(gtx layout.Context) layout.Dimensions {
				return u.Input(gtx, fl, guikit.StBody, 38, "", nil)
			})
		}
	}
	dlg = newDialog("Add a company VPN", "Your password is asked for each time you connect.", func(gtx layout.Context) layout.Dimensions {
		if f.name.Submitted(gtx) || f.server.Submitted(gtx) || f.user.Submitted(gtx) {
			u.pick(dlg, 1) // ignored while Add is off
		}
		for i := range f.pick {
			if f.pick[i].Clicked(gtx) {
				f.protocol = OpenConnectProtocols[i].ID
			}
		}
		if !focused {
			focused = true
			gtx.Execute(focus(&f.server.Editor))
		}
		var c col
		c.add(input(f.server, "Server", "The address your company gave you, for example vpn.example.com."))
		c.gap(12)
		c.add(func(gtx layout.Context) layout.Dimensions {
			var rows col
			rows.add(func(gtx layout.Context) layout.Dimensions {
				return u.Theme.Label(gtx, guikit.StBody, guikit.ColDim, "Type")
			})
			for i, pr := range OpenConnectProtocols {
				rows.gap(6)
				on := f.protocol == pr.ID
				label := pr.Label
				rows.add(func(gtx layout.Context) layout.Dimensions {
					return f.pick[i].Layout(gtx, func(gtx layout.Context) layout.Dimensions {
						return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
							layout.Rigid(func(gtx layout.Context) layout.Dimensions { return guikit.Radio(gtx, on) }),
							layout.Rigid(layout.Spacer{Width: 10}.Layout),
							layout.Rigid(func(gtx layout.Context) layout.Dimensions {
								return u.Theme.Label(gtx, guikit.StBody, guikit.ColText, label)
							}))
					})
				})
			}
			return rows.layout(gtx)
		})
		c.gap(12)
		c.add(input(f.user, "User name", ""))
		c.gap(12)
		c.add(input(f.name, "Name", "Optional. The server is the default."))
		return c.layout(gtx)
	}, 1,
		choice{Label: "Cancel", Kind: guikit.ButtonSecondary},
		choice{Label: "Add", Kind: guikit.ButtonPrimary, Enabled: ok, Do: func() {
			o := f.value()
			label := strings.TrimSpace(o.Name)
			if label == "" {
				label = strings.TrimSpace(o.Gateway)
			}
			p.do("Adding "+label+"…", func(n Network) error { return n.AddOpenConnect(o) })
		}})
	u.open(dlg)
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
	st, aps, err, working, chosen, loading, vpns := p.state, p.aps, p.err, p.working, p.chosen, p.loading, p.vpns
	fw := p.fwInput()
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
		c.gap(26)
		c.add(u.caps("VPN"))
		c.gap(10)
		c.add(u.panel(func(gtx layout.Context) layout.Dimensions {
			var rows col
			for _, v := range vpns {
				k := p.vpnClicksFor(v.UUID)
				label, sub := "Connect", v.Kind
				if v.Active {
					label, sub = "Disconnect", v.Kind+" · connected"
				}
				rows.add(u.row(v.ID, sub, u.buttons(
					guikit.Action{Click: &k.toggle, Kind: guikit.ButtonSecondary, Label: label, Disabled: working != ""},
					guikit.Action{Click: &k.remove, Kind: guikit.ButtonGhost, Label: "Remove", Disabled: working != ""},
				)))
				rows.gap(8)
			}
			if len(vpns) == 0 {
				rows.add(u.dim("No VPN is set up."))
				rows.gap(8)
			}
			rows.add(u.buttons(
				guikit.Action{Click: &p.importWG, Kind: guikit.ButtonSecondary, Label: "Import WireGuard…", Disabled: working != ""},
				guikit.Action{Click: &p.addOC, Kind: guikit.ButtonSecondary, Label: "Add company VPN…", Disabled: working != ""},
			))
			return rows.layout(gtx)
		}))
		c.gap(26)
		c.add(func(gtx layout.Context) layout.Dimensions { return p.firewallSection(gtx, fw, working) })
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
