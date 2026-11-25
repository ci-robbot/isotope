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
	"github.com/kairos-community/isotope/pkg/i18n"
	"github.com/kairos-community/isotope/pkg/remotedesktop"
)

func init() {
	pageMakers[PageNetwork] = func(u *UI) pageView {
		return &networkPage{u: u, apClicks: map[string]*apClicks{}, vpnClicks: map[string]*vpnClicks{}, fwClicks: map[string]*fwClicks{}, modemClicks: map[string]*modemClicks{}}
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
	modemClicks                    map[string]*modemClicks
	apn                            *guikit.Field // of the open APN dialog

	// The firewall (firewall.go). fw, fwLoaded and fwErr are under u.mu.
	fw              Firewall
	fwLoaded        bool
	fwErr           string
	fwToggle, fwAdd widget.Clickable
	fwReset         widget.Clickable
	fwClicks        map[string]*fwClicks
	fwForm          *fwForm // the open "Allow incoming" dialog

	// Remote desktop (remote.go). rd, rdLoaded and rdErr are under u.mu.
	rd          remotedesktop.Status
	rdLoaded    bool
	rdErr       string
	rdk         rdClicks
	rdPw, rdPw2 *guikit.Field // of the open password dialog
}

type apClicks struct{ connect, forget widget.Clickable }

type vpnClicks struct{ toggle, remove widget.Clickable }

type modemClicks struct{ toggle, apn widget.Clickable }

func (p *networkPage) modemClicksFor(id string) *modemClicks {
	k, ok := p.modemClicks[id]
	if !ok {
		k = &modemClicks{}
		p.modemClicks[id] = k
	}
	return k
}

// modemTitle names a modem row: "Mobile broadband", with its interface.
func modemTitle(m Modem) string {
	if m.Iface == "" {
		return i18n.T("Mobile broadband")
	}
	return i18n.Tf("Mobile broadband (%s)", m.Iface)
}

// modemSub is a modem row's second line: the state, the connection and
// address, the network and the APN.
func modemSub(m Modem) string {
	parts := []string{i18n.T(m.State)}
	if m.Connection != "" {
		parts = append(parts, m.Connection)
	}
	if m.Address != "" {
		parts = append(parts, m.Address)
	}
	if m.Operator != "" {
		parts = append(parts, i18n.Tf("network %s", m.Operator))
	}
	if m.APN != "" {
		parts = append(parts, i18n.Tf("APN %s", m.APN))
	} else {
		parts = append(parts, i18n.T("APN automatic"))
	}
	return strings.Join(parts, " · ")
}

// apnDialog asks for the modem's APN; empty is automatic (from the
// provider database, for the network the modem registered on).
func (p *networkPage) apnDialog(m Modem) {
	u := p.u
	f := guikit.NewField(true)
	f.Editor.SetText(m.APN)
	p.apn = f
	focused := false
	var dlg *dialog
	dlg = newDialog(i18n.T("Mobile broadband APN"), i18n.Tf("The access point name of %s. Your operator gives it; most connections work without one.", modemTitle(m)), func(gtx layout.Context) layout.Dimensions {
		if f.Submitted(gtx) {
			u.pick(dlg, 1) // ignored while Save is off
		}
		if !focused {
			focused = true
			gtx.Execute(focus(&f.Editor))
		}
		f.Err = ""
		if err := checkAPN(strings.TrimSpace(f.Editor.Text())); err != nil {
			f.Err = err.Error()
		}
		return u.Labelled(gtx, "APN", f, i18n.T("Leave it empty to look it up for the network the modem is on."), func(gtx layout.Context) layout.Dimensions {
			return u.Input(gtx, f, guikit.StBody, 38, "", nil)
		})
	}, 1,
		choice{Label: i18n.T("Cancel"), Kind: guikit.ButtonSecondary},
		choice{Label: i18n.T("Save"), Kind: guikit.ButtonPrimary, Enabled: func() bool { return checkAPN(strings.TrimSpace(f.Editor.Text())) == nil }, Do: func() {
			apn, id := strings.TrimSpace(f.Editor.Text()), m.ID
			p.do(i18n.T("Saving the APN…"), func(n Network) error { return n.SetModemAPN(id, apn) })
		}})
	u.open(dlg)
}

func (p *networkPage) Shown() {
	if p.password == nil {
		p.password = guikit.NewSecret()
	}
	p.load()
	p.loadFirewall()
	p.loadRemote()
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
			p.err = i18n.Tf("NetworkManager is not available: %s", err.Error())
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
			u.tell(i18n.T("Network"), err.Error())
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
				p.do(i18n.Tf("Disconnecting %s…", v.ID), func(n Network) error { return n.DeactivateVPN(v.UUID) })
			} else {
				p.do(i18n.Tf("Connecting %s…", v.ID), func(n Network) error { return n.ActivateVPN(v.UUID) })
			}
		}
		if k.remove.Clicked(gtx) {
			u.ask(i18n.Tf("Remove %s?", v.ID), i18n.T("The VPN connection and its keys are deleted from this computer."),
				choice{Label: i18n.T("Cancel"), Kind: guikit.ButtonSecondary},
				choice{Label: i18n.T("Remove"), Kind: guikit.ButtonDanger, Do: func() {
					p.do(i18n.Tf("Removing %s…", v.ID), func(n Network) error { return n.RemoveVPN(v.UUID) })
				}})
		}
	}
	for _, m := range st.Modems {
		m, k := m, p.modemClicksFor(m.ID)
		if k.toggle.Clicked(gtx) {
			if m.Connected {
				p.do(i18n.T("Disconnecting the modem…"), func(n Network) error { return n.DisconnectModem(m.ID) })
			} else {
				p.do(i18n.T("Connecting the modem…"), func(n Network) error { return n.ConnectModem(m.ID) })
			}
		}
		if k.apn.Clicked(gtx) {
			p.apnDialog(m)
		}
	}
	p.updateFirewall(gtx)
	p.updateRemote(gtx)
	if p.refresh.Clicked(gtx) {
		p.load()
		p.loadFirewall()
		p.loadRemote()
	}
	if p.wifi.Clicked(gtx) {
		on := !st.WifiOn
		p.do(map[bool]string{true: i18n.T("Turning Wi-Fi on…"), false: i18n.T("Turning Wi-Fi off…")}[on], func(n Network) error { return n.SetWifi(on) })
	}
	if p.disconnect.Clicked(gtx) {
		p.do(i18n.T("Disconnecting…"), func(n Network) error { return n.Disconnect() })
	}
	if p.own.Clicked(gtx) {
		if _, argv := otherSettings(u.sys.Desktop); argv != nil {
			if err := u.hooks.Start(argv...); err != nil {
				u.tell(i18n.T("Network"), err.Error())
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
				p.do(i18n.Tf("Connecting to %s…", ssid), func(n Network) error { return n.Connect(ssid, "", false) })
			}
		}
	}
	for _, s := range st.Saved {
		ssid := s
		if p.clicksFor(ssid).forget.Clicked(gtx) {
			u.ask(i18n.Tf("Forget %s?", ssid), i18n.T("Its password is deleted, and this computer no longer joins it by itself."),
				choice{Label: i18n.T("Cancel"), Kind: guikit.ButtonSecondary},
				choice{Label: i18n.T("Forget"), Kind: guikit.ButtonDanger, Do: func() {
					p.do(i18n.Tf("Forgetting %s…", ssid), func(n Network) error { return n.Forget(ssid) })
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
				p.password.Err = i18n.T("A Wi-Fi password has at least 8 characters.")
				return
			}
			u.mu.Lock()
			p.chosen = ""
			u.mu.Unlock()
			p.password.Editor.SetText("")
			p.do(i18n.Tf("Connecting to %s…", chosen), func(n Network) error { return n.Connect(chosen, pw, false) })
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
		return fmt.Errorf(i18n.T("%s is too big for a WireGuard file (the limit is 64 KiB)"), filepath.Base(path))
	}
	if err := n.ImportWireGuard(name, conf); err != nil {
		return fmt.Errorf("%s: %w", filepath.Base(path), err)
	}
	return nil
}

// wgFileRequest is the file picker's question for "Import WireGuard…".
var wgFileRequest = FileRequest{
	Title: i18n.Mark("Import a WireGuard file"), Accept: i18n.Mark("Import"),
	Filters: []FileFilter{{Name: i18n.Mark("WireGuard files"), Patterns: []string{"*.conf"}}},
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
	dlg = newDialog(i18n.T("Import a WireGuard file"), "", func(gtx layout.Context) layout.Dimensions {
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
			return u.Labelled(gtx, i18n.T("File"), file, i18n.T("The path of a .conf file, for example ~/Downloads/work.conf."), func(gtx layout.Context) layout.Dimensions {
				return u.Input(gtx, file, guikit.StBody, 38, "", nil)
			})
		})
		c.gap(12)
		c.add(func(gtx layout.Context) layout.Dimensions {
			return u.Labelled(gtx, i18n.T("Name"), name, i18n.T("Optional. The file name is the default."), func(gtx layout.Context) layout.Dimensions {
				return u.Input(gtx, name, guikit.StBody, 38, "", nil)
			})
		})
		return c.layout(gtx)
	}, 1,
		choice{Label: i18n.T("Cancel"), Kind: guikit.ButtonSecondary},
		choice{Label: i18n.T("Import"), Kind: guikit.ButtonPrimary, Enabled: func() bool { return path() != "" }, Do: func() {
			f, nm := path(), wgName(name.Editor.Text(), path())
			p.do(i18n.Tf("Importing %s…", nm), func(n Network) error { return importWireGuardFile(n, nm, f) })
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
	dlg = newDialog(i18n.T("Add a company VPN"), i18n.T("Your password is asked for each time you connect."), func(gtx layout.Context) layout.Dimensions {
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
		c.add(input(f.server, i18n.T("Server"), i18n.T("The address your company gave you, for example vpn.example.com.")))
		c.gap(12)
		c.add(func(gtx layout.Context) layout.Dimensions {
			var rows col
			rows.add(func(gtx layout.Context) layout.Dimensions {
				return u.Theme.Label(gtx, guikit.StBody, u.Colors().Dim, i18n.T("Type"))
			})
			for i, pr := range OpenConnectProtocols {
				rows.gap(6)
				on := f.protocol == pr.ID
				label := pr.Label
				rows.add(func(gtx layout.Context) layout.Dimensions {
					return f.pick[i].Layout(gtx, func(gtx layout.Context) layout.Dimensions {
						return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
							layout.Rigid(func(gtx layout.Context) layout.Dimensions { return u.Radio(gtx, on) }),
							layout.Rigid(layout.Spacer{Width: 10}.Layout),
							layout.Rigid(func(gtx layout.Context) layout.Dimensions {
								return u.Theme.Label(gtx, guikit.StBody, u.Colors().Text, label)
							}))
					})
				})
			}
			return rows.layout(gtx)
		})
		c.gap(12)
		c.add(input(f.user, i18n.T("User name"), ""))
		c.gap(12)
		c.add(input(f.name, i18n.T("Name"), i18n.T("Optional. The server is the default.")))
		return c.layout(gtx)
	}, 1,
		choice{Label: i18n.T("Cancel"), Kind: guikit.ButtonSecondary},
		choice{Label: i18n.T("Add"), Kind: guikit.ButtonPrimary, Enabled: ok, Do: func() {
			o := f.value()
			label := strings.TrimSpace(o.Name)
			if label == "" {
				label = strings.TrimSpace(o.Gateway)
			}
			p.do(i18n.Tf("Adding %s…", label), func(n Network) error { return n.AddOpenConnect(o) })
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
		leading = append(leading, guikit.Action{Click: &p.own, Kind: guikit.ButtonGhost, Label: i18n.Tf("Open %s", name)})
	}
	p.u.mu.Lock()
	loading := p.loading
	p.u.mu.Unlock()
	return leading, []guikit.Action{{Click: &p.refresh, Kind: guikit.ButtonSecondary, Label: i18n.T("Refresh"), Disabled: loading || p.u.hooks.Network == nil}}
}

func (p *networkPage) Body(gtx layout.Context) layout.Dimensions {
	u := p.u
	u.mu.Lock()
	st, aps, err, working, chosen, loading, vpns := p.state, p.aps, p.err, p.working, p.chosen, p.loading, p.vpns
	fw, rd := p.fwInput(), p.rdInput()
	u.mu.Unlock()
	return u.scrolled(gtx, &p.list, func(gtx layout.Context) layout.Dimensions {
		var c col
		c.add(u.Heading(i18n.T("Network"), i18n.T("Wired and Wi-Fi connections, through NetworkManager. The same connections the panel's network menu shows.")))
		c.gap(24)
		if u.hooks.Network == nil || err != "" {
			c.add(u.errorBox(orUnknown(err)))
			return c.layout(gtx)
		}
		if st.Connected {
			c.add(u.lead(i18n.Tf("Connected to %s", orUnknown(st.Name))))
		} else {
			c.add(u.lead(i18n.T("Not connected")))
		}
		if working != "" {
			c.gap(12)
			c.add(u.spinnerLine(working))
		}
		if len(st.Devices) > 0 {
			c.gap(24)
			c.add(u.caps(i18n.T("Devices")))
			c.gap(10)
			c.add(u.panel(func(gtx layout.Context) layout.Dimensions {
				var rows col
				for i, d := range st.Devices {
					if i > 0 {
						rows.gap(8)
					}
					sub := i18n.T(d.State)
					if d.Connection != "" {
						sub += " · " + d.Connection
					}
					if d.Address != "" {
						sub += " · " + d.Address
					}
					rows.add(u.row(fmt.Sprintf("%s (%s)", i18n.T(d.Kind), d.Iface), sub, nil))
				}
				return rows.layout(gtx)
			}))
		}
		if len(st.Modems) > 0 {
			c.gap(26)
			c.add(u.caps(i18n.T("Mobile broadband")))
			c.gap(10)
			c.add(u.panel(func(gtx layout.Context) layout.Dimensions {
				var rows col
				for i, m := range st.Modems {
					if i > 0 {
						rows.gap(8)
					}
					k := p.modemClicksFor(m.ID)
					label := i18n.T("Connect")
					if m.Connected {
						label = i18n.T("Disconnect")
					}
					rows.add(u.row(modemTitle(m), modemSub(m), u.buttons(
						guikit.Action{Click: &k.toggle, Kind: guikit.ButtonSecondary, Label: label, Disabled: working != "" || !(m.Connected || m.Ready)},
						guikit.Action{Click: &k.apn, Kind: guikit.ButtonGhost, Label: i18n.T("APN…"), Disabled: working != ""},
					)))
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
				label, sub := i18n.T("Connect"), v.Kind
				if v.Active {
					label, sub = i18n.T("Disconnect"), i18n.Tf("%s · connected", v.Kind)
				}
				rows.add(u.row(v.ID, sub, u.buttons(
					guikit.Action{Click: &k.toggle, Kind: guikit.ButtonSecondary, Label: label, Disabled: working != ""},
					guikit.Action{Click: &k.remove, Kind: guikit.ButtonGhost, Label: i18n.T("Remove"), Disabled: working != ""},
				)))
				rows.gap(8)
			}
			if len(vpns) == 0 {
				rows.add(u.dim(i18n.T("No VPN is set up.")))
				rows.gap(8)
			}
			rows.add(u.buttons(
				guikit.Action{Click: &p.importWG, Kind: guikit.ButtonSecondary, Label: i18n.T("Import WireGuard…"), Disabled: working != ""},
				guikit.Action{Click: &p.addOC, Kind: guikit.ButtonSecondary, Label: i18n.T("Add company VPN…"), Disabled: working != ""},
			))
			return rows.layout(gtx)
		}))
		if u.hooks.RemoteDesktop != nil {
			c.gap(26)
			c.add(func(gtx layout.Context) layout.Dimensions { return p.remoteSection(gtx, rd, working) })
		}
		c.gap(26)
		c.add(func(gtx layout.Context) layout.Dimensions { return p.firewallSection(gtx, fw, working) })
		if !st.HasWifi {
			return c.layout(gtx)
		}
		c.gap(26)
		c.add(u.row("Wi-Fi", map[bool]string{true: i18n.T("On"), false: i18n.T("Off")}[st.WifiOn], func(gtx layout.Context) layout.Dimensions {
			label := i18n.T("Turn on")
			if st.WifiOn {
				label = i18n.T("Turn off")
			}
			as := []guikit.Action{{Click: &p.wifi, Kind: guikit.ButtonSecondary, Label: label}}
			for _, d := range st.Devices {
				if d.Kind == "Wi-Fi" && d.State == "Connected" {
					as = append(as, guikit.Action{Click: &p.disconnect, Kind: guikit.ButtonGhost, Label: i18n.T("Disconnect")})
					break
				}
			}
			return u.buttons(as...)(gtx)
		}))
		if chosen != "" {
			c.gap(14)
			c.add(u.panel(func(gtx layout.Context) layout.Dimensions {
				return u.Labelled(gtx, i18n.Tf("Password for %s", chosen), p.password, "", func(gtx layout.Context) layout.Dimensions {
					return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
						layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
							return u.Input(gtx, p.password, guikit.StBody, 38, "", nil)
						}),
						layout.Rigid(layout.Spacer{Width: 10}.Layout),
						layout.Rigid(u.buttons(
							guikit.Action{Click: &p.cancelJoin, Kind: guikit.ButtonGhost, Label: i18n.T("Cancel")},
							guikit.Action{Click: &p.join, Kind: guikit.ButtonPrimary, Label: i18n.T("Connect")},
						)),
					)
				})
			}))
		}
		if st.WifiOn {
			c.gap(18)
			c.add(u.caps(i18n.T("Networks in range")))
			c.gap(10)
			switch {
			case loading && len(aps) == 0:
				c.add(u.spinnerLine(i18n.T("Looking for networks…")))
			case len(aps) == 0:
				c.add(u.dim(i18n.T("No network in range.")))
			default:
				c.add(u.panel(func(gtx layout.Context) layout.Dimensions {
					var rows col
					for i, a := range aps {
						if i > 0 {
							rows.gap(8)
						}
						k := p.clicksFor(a.SSID)
						sub := i18n.Tf("Signal %d%%", a.Strength)
						if a.Secured {
							sub += " · " + i18n.T("secured")
						}
						if contains(st.Saved, a.SSID) {
							sub += " · " + i18n.T("saved")
						}
						title := a.SSID
						var right layout.Widget
						if st.Connected && a.SSID == st.Name {
							title = i18n.Tf("%s (connected)", title)
						} else {
							right = u.buttons(guikit.Action{Click: &k.connect, Kind: guikit.ButtonSecondary, Label: i18n.T("Connect"), Disabled: working != ""})
						}
						rows.add(u.row(title, sub, right))
					}
					return rows.layout(gtx)
				}))
			}
		}
		if len(st.Saved) > 0 {
			c.gap(26)
			c.add(u.caps(i18n.T("Saved networks")))
			c.gap(10)
			c.add(u.panel(func(gtx layout.Context) layout.Dimensions {
				var rows col
				for i, s := range st.Saved {
					if i > 0 {
						rows.gap(8)
					}
					k := p.clicksFor(s)
					rows.add(u.row(s, "", u.buttons(guikit.Action{Click: &k.forget, Kind: guikit.ButtonGhost, Label: i18n.T("Forget"), Disabled: working != ""})))
				}
				return rows.layout(gtx)
			}))
		}
		return c.layout(gtx)
	})
}
