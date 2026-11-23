package main

import (
	"strings"

	"gioui.org/layout"
	"gioui.org/widget"

	"github.com/kairos-community/isotope/pkg/firewall"
	"github.com/kairos-community/isotope/pkg/guikit"
)

// The Firewall section of the Network page: on or off, and the ports an
// administrator allowed, through isotope-updated (FirewallStatus,
// SetFirewall, AllowPort, DenyPort; polkit io.kairos.isotope-update.firewall).

// fwInput is what the section is drawn from.
type fwInput struct {
	Live, Admin, Loaded bool
	Err                 string
	FW                  Firewall
}

// fwRow is one allowed port.
type fwRow struct {
	Rule       firewall.Rule
	Title, Sub string
}

// fwView is the section: the Firewall row's state (Sub), its button
// (Toggle, empty for none), the allowed ports, and whether "Allow
// incoming…" shows (CanAdd) and the buttons work (Enabled).
type fwView struct {
	// Reset is the label of the repair button for settings that cannot
	// be read, empty otherwise.
	Reset                    string
	Sub, Toggle, Hint, Empty string
	Rows                     []fwRow
	CanAdd, Enabled          bool
}

func firewallViewOf(in fwInput) fwView {
	var v fwView
	fw := in.FW
	switch {
	case in.Live:
		v.Sub = "Off on the live system. It is on after the install."
		return v
	case in.Err != "":
		v.Sub = in.Err
		return v
	case !in.Loaded:
		v.Sub = "Loading…"
		return v
	case fw.Skipped:
		v.Sub = fw.Summary() + " · " + fw.Reason
		return v
	case fw.Broken:
		// Only a reset may change them: the rules below are what could
		// still be read, and the reset keeps them.
		v.Sub = fw.Reason
		if !strings.HasPrefix(v.Sub, "The firewall settings could not be read") {
			v.Sub = strings.TrimSpace("The firewall settings could not be read. " + v.Sub)
		}
		v.Reset, v.Enabled = "Reset to defaults", in.Admin
		v.Empty = "No port could be read from them: a reset gives the default list, SSH (22/tcp)."
		if len(fw.Rules) > 0 {
			v.Empty = "These ports could still be read; a reset keeps them:"
		}
		if !in.Admin {
			v.Hint = "Only an administrator can change the firewall."
		}
		for _, r := range fw.Rules {
			v.Rows = append(v.Rows, fwRow{Rule: r, Title: r.String(), Sub: r.Note})
		}
		return v
	case fw.On && fw.Active:
		v.Sub = "On · Incoming connections are blocked unless their port is allowed."
	case fw.On:
		v.Sub = fw.Summary() + " · Nothing is blocked. " + fw.Reason
	case fw.Active:
		v.Sub = fw.Summary() + " · " + fw.Reason
	default:
		v.Sub = "Off · Every port is open to the network."
	}
	v.Toggle = "Turn on"
	if fw.On {
		v.Toggle = "Turn off"
	}
	v.CanAdd, v.Enabled = true, in.Admin
	if !in.Admin {
		v.Hint = "Only an administrator can change the firewall."
	}
	for _, r := range in.FW.Rules {
		v.Rows = append(v.Rows, fwRow{Rule: r, Title: r.String(), Sub: r.Note})
	}
	if len(v.Rows) == 0 {
		v.Empty = "No port is allowed."
	}
	return v
}

type fwClicks struct{ remove widget.Clickable }

// rulesText is "22/tcp, 8080/tcp".
func rulesText(rs []firewall.Rule) string {
	var out []string
	for _, r := range rs {
		out = append(out, r.String())
	}
	return strings.Join(out, ", ")
}

func (p *networkPage) fwClicksFor(r firewall.Rule) *fwClicks {
	k, ok := p.fwClicks[r.String()]
	if !ok {
		k = &fwClicks{}
		p.fwClicks[r.String()] = k
	}
	return k
}

// loadFirewall reads the firewall's state. Not on the live system, which
// has no firewall.
func (p *networkPage) loadFirewall() {
	u, d := p.u, p.u.hooks.Daemon
	if d == nil || u.hooks.Live {
		return
	}
	u.async(func() {
		fw, err := d.FirewallStatus()
		u.mu.Lock()
		defer u.mu.Unlock()
		p.fwLoaded = true
		if err != nil {
			p.fwErr = Describe(err)
			return
		}
		p.fw, p.fwErr = fw, ""
	})
}

// fwChange runs a firewall change and reads the state again.
func (p *networkPage) fwChange(working string, f func(Daemon) error) {
	u, d := p.u, p.u.hooks.Daemon
	if d == nil || !u.sys.Admin {
		return
	}
	u.mu.Lock()
	p.working = working
	u.mu.Unlock()
	u.async(func() {
		err := f(d)
		u.mu.Lock()
		p.working = ""
		u.mu.Unlock()
		if err != nil {
			u.tell("Firewall", Describe(err))
		}
		p.loadFirewall()
	})
}

func (p *networkPage) fwInput() fwInput {
	return fwInput{Live: p.u.hooks.Live, Admin: p.u.sys.Admin, Loaded: p.fwLoaded, Err: p.fwErr, FW: p.fw}
}

// updateFirewall handles the section's clicks. u.mu is not held.
func (p *networkPage) updateFirewall(gtx layout.Context) {
	u := p.u
	u.mu.Lock()
	v, on, rules := firewallViewOf(p.fwInput()), p.fw.On, p.fw.Rules
	u.mu.Unlock()
	if p.fwToggle.Clicked(gtx) && v.Enabled && v.Toggle != "" {
		if on {
			u.ask("Turn the firewall off?", "Every program that listens on a port can then be reached from the network.",
				choice{Label: "Cancel", Kind: guikit.ButtonSecondary},
				choice{Label: "Turn off", Kind: guikit.ButtonDanger, Do: func() {
					p.fwChange("Turning the firewall off…", func(d Daemon) error { return d.SetFirewall(false) })
				}})
		} else {
			p.fwChange("Turning the firewall on…", func(d Daemon) error { return d.SetFirewall(true) })
		}
	}
	if p.fwReset.Clicked(gtx) && v.Enabled && v.Reset != "" {
		body := "The firewall is turned on with the ports that could still be read: " + rulesText(rules) +
			". Everything else in the file is replaced."
		if len(rules) == 0 {
			body = "None of the saved settings could be read. The ports saved in the file are lost: the firewall is turned on with the default list, SSH (22/tcp)."
		}
		u.ask("Reset the firewall settings?", body,
			choice{Label: "Cancel", Kind: guikit.ButtonSecondary},
			choice{Label: "Reset", Kind: guikit.ButtonDanger, Do: func() {
				p.fwChange("Resetting the firewall settings…", func(d Daemon) error { return d.ResetFirewall() })
			}})
	}
	if p.fwAdd.Clicked(gtx) && v.Enabled && v.CanAdd {
		p.allowDialog()
	}
	for _, r := range rules {
		r := r
		if p.fwClicksFor(r).remove.Clicked(gtx) && v.Enabled && v.CanAdd {
			body := "Incoming connections on this port are blocked again."
			if r.Port == 22 && r.Proto == "tcp" {
				body += " Nobody can then log in to this computer over SSH."
			}
			u.ask("Stop allowing "+r.String()+"?", body,
				choice{Label: "Cancel", Kind: guikit.ButtonSecondary},
				choice{Label: "Remove", Kind: guikit.ButtonDanger, Do: func() {
					p.fwChange("Removing "+r.String()+"…", func(d Daemon) error { return d.DenyPort(r.Port, r.Proto) })
				}})
		}
	}
}

// firewallSection is the FIREWALL heading and its panel.
func (p *networkPage) firewallSection(gtx layout.Context, in fwInput, working string) layout.Dimensions {
	u := p.u
	v := firewallViewOf(in)
	off := !v.Enabled || working != ""
	var c col
	c.add(u.caps("FIREWALL"))
	c.gap(10)
	c.add(u.panel(func(gtx layout.Context) layout.Dimensions {
		var rows col
		var right layout.Widget
		switch {
		case v.Toggle != "":
			right = u.buttons(guikit.Action{Click: &p.fwToggle, Kind: guikit.ButtonSecondary, Label: v.Toggle, Disabled: off})
		case v.Reset != "":
			right = u.buttons(guikit.Action{Click: &p.fwReset, Kind: guikit.ButtonSecondary, Label: v.Reset, Disabled: off})
		}
		rows.add(u.row("Firewall", v.Sub, right))
		if !v.CanAdd {
			// Settings that cannot be read: the ports that can still be
			// read, without buttons.
			if v.Empty != "" {
				rows.gap(8)
				rows.add(u.dim(v.Empty))
			}
			for _, r := range v.Rows {
				rows.gap(8)
				rows.add(u.row(r.Title, r.Sub, nil))
			}
			if v.Hint != "" {
				rows.gap(8)
				rows.add(u.dim(v.Hint))
			}
			return rows.layout(gtx)
		}
		for _, r := range v.Rows {
			rows.gap(8)
			k := p.fwClicksFor(r.Rule)
			rows.add(u.row(r.Title, r.Sub, u.buttons(
				guikit.Action{Click: &k.remove, Kind: guikit.ButtonGhost, Label: "Remove", Disabled: off})))
		}
		if v.Empty != "" {
			rows.gap(8)
			rows.add(u.dim(v.Empty))
		}
		if v.Hint != "" {
			rows.gap(8)
			rows.add(u.dim(v.Hint))
		}
		rows.gap(8)
		rows.add(u.buttons(guikit.Action{Click: &p.fwAdd, Kind: guikit.ButtonSecondary, Label: "Allow incoming…", Disabled: off}))
		return rows.layout(gtx)
	}))
	return c.layout(gtx)
}

// fwForm is the "Allow incoming connections" dialog's state.
type fwForm struct {
	port, note *guikit.Field
	proto      string // tcp or udp
	pick       [2]widget.Clickable
}

var fwProtocols = [2]struct{ ID, Label string }{{"tcp", "TCP"}, {"udp", "UDP"}}

// rule is the form's port, or an error to show under the field.
func (f *fwForm) rule() (firewall.Rule, error) {
	port, err := firewall.ParsePort(f.port.Editor.Text())
	if err != nil {
		return firewall.Rule{}, err
	}
	note := strings.TrimSpace(f.note.Editor.Text())
	if err := firewall.Check(port, f.proto, note); err != nil {
		return firewall.Rule{}, err
	}
	return firewall.Rule{Port: uint16(port), Proto: f.proto, Note: note}, nil
}

// allowDialog asks for a port, its protocol and what it is for, and allows it.
func (p *networkPage) allowDialog() {
	u := p.u
	f := &fwForm{port: guikit.NewField(true), note: guikit.NewField(true), proto: "tcp"}
	p.fwForm = f
	focused := false
	var dlg *dialog
	ok := func() bool { _, err := f.rule(); return err == nil }
	dlg = newDialog("Allow incoming connections", "Programs on other computers can then connect to this one on the port.", func(gtx layout.Context) layout.Dimensions {
		if f.port.Submitted(gtx) || f.note.Submitted(gtx) {
			u.pick(dlg, 1) // ignored while Allow is off
		}
		for i := range f.pick {
			if f.pick[i].Clicked(gtx) {
				f.proto = fwProtocols[i].ID
			}
		}
		if !focused {
			focused = true
			gtx.Execute(focus(&f.port.Editor))
		}
		f.port.Err, f.note.Err = "", ""
		if strings.TrimSpace(f.port.Editor.Text()) != "" {
			if _, err := firewall.ParsePort(f.port.Editor.Text()); err != nil {
				f.port.Err = "Type a port number from 1 to 65535."
			}
		}
		if err := firewall.CheckNote(strings.TrimSpace(f.note.Editor.Text())); err != nil {
			f.note.Err = "Up to 64 characters, with no quotes or backslashes."
		}
		var c col
		c.add(func(gtx layout.Context) layout.Dimensions {
			return u.Labelled(gtx, "Port", f.port, "For example 8080 for a web server, or 51820 for WireGuard.", func(gtx layout.Context) layout.Dimensions {
				return u.Input(gtx, f.port, guikit.StBody, 38, "", nil)
			})
		})
		c.gap(12)
		c.add(func(gtx layout.Context) layout.Dimensions {
			var rows col
			rows.add(func(gtx layout.Context) layout.Dimensions {
				return u.Theme.Label(gtx, guikit.StBody, guikit.ColDim, "Protocol")
			})
			for i, pr := range fwProtocols {
				rows.gap(6)
				on := f.proto == pr.ID
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
		c.add(func(gtx layout.Context) layout.Dimensions {
			return u.Labelled(gtx, "Description", f.note, "Optional. What the port is for.", func(gtx layout.Context) layout.Dimensions {
				return u.Input(gtx, f.note, guikit.StBody, 38, "", nil)
			})
		})
		return c.layout(gtx)
	}, 1,
		choice{Label: "Cancel", Kind: guikit.ButtonSecondary},
		choice{Label: "Allow", Kind: guikit.ButtonPrimary, Enabled: ok, Do: func() {
			r, err := f.rule()
			if err != nil {
				return
			}
			p.fwChange("Allowing "+r.String()+"…", func(d Daemon) error { return d.AllowPort(r.Port, r.Proto, r.Note) })
		}})
	u.open(dlg)
}
