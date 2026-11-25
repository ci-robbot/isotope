package main

// The REMOTE DESKTOP section of the Network page talks to `isotope
// remote-desktop` (cmd/isotope/remote_desktop.go), run as the user, as the
// Compatibility page talks to `isotope glibc`: the server, its keys, its
// password and the firewall rule stay one implementation, in the CLI. See
// docs/superpowers/specs/2026-11-24-remote-desktop-design.md.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"gioui.org/layout"
	"gioui.org/widget"

	"github.com/kairos-community/isotope/pkg/guikit"
	"github.com/kairos-community/isotope/pkg/i18n"
	"github.com/kairos-community/isotope/pkg/remotedesktop"
)

// RemoteDesktop is `isotope remote-desktop`, for the Network page.
type RemoteDesktop interface {
	// Status is `isotope remote-desktop status --json`.
	Status() (remotedesktop.Status, error)
	// SetOn is `isotope remote-desktop on|off`.
	SetOn(on bool) error
	// SetPassword is `isotope remote-desktop password`, the password on
	// its standard input.
	SetPassword(pw string) error
}

// remoteCLI runs bin (isotope) as the user.
type remoteCLI struct{ bin string }

// remoteTimeout bounds a call: `on` waits up to 5 s for the server, and
// makes keys the first time.
const remoteTimeout = time.Minute

func (c remoteCLI) run(stdin string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), remoteTimeout)
	defer cancel()
	var out, errOut bytes.Buffer
	cmd := exec.CommandContext(ctx, c.bin, append([]string{"remote-desktop"}, args...)...)
	cmd.Stdin = strings.NewReader(stdin)
	cmd.Stdout, cmd.Stderr = &out, &errOut
	if err := cmd.Run(); err != nil {
		return nil, cliError("isotope remote-desktop "+args[0], strings.TrimPrefix(errOut.String(), "isotope remote-desktop: "), err)
	}
	return out.Bytes(), nil
}

func (c remoteCLI) Status() (remotedesktop.Status, error) {
	out, err := c.run("", "status", "--json")
	if err != nil {
		return remotedesktop.Status{}, err
	}
	var st remotedesktop.Status
	if err := json.Unmarshal(out, &st); err != nil {
		return st, fmt.Errorf(i18n.T("isotope remote-desktop status gave no status (%v); is isotope up to date?"), err)
	}
	return st, nil
}

func (c remoteCLI) SetOn(on bool) error {
	arg := "off"
	if on {
		arg = "on"
	}
	_, err := c.run("", arg)
	return err
}

func (c remoteCLI) SetPassword(pw string) error {
	_, err := c.run(pw+"\n", "password")
	return err
}

// rdInput is what the section is drawn from.
type rdInput struct {
	Live, Admin, Loaded bool
	Err                 string
	St                  remotedesktop.Status
}

// rdView is the section: the first row's state (Sub) and its button
// (Toggle, empty for none), the rows under it, the warning, the password
// button's label, the hint, and "Open Extensions" (Extension).
type rdView struct {
	Sub, Toggle  string
	Rows         [][2]string
	Warn         string
	Password     string
	Hint         string
	Extension    bool
	ToggleNeedPw bool
}

// rdConnect is "192.168.1.20:5900".
func rdConnect(st remotedesktop.Status) string {
	var out []string
	for _, a := range st.Addresses {
		out = append(out, fmt.Sprintf("%s:%d", a, st.Port))
	}
	if len(out) == 0 {
		return i18n.Tf("No network address yet · port %d", st.Port)
	}
	return strings.Join(out, " · ")
}

func remoteViewOf(in rdInput) rdView {
	var v rdView
	st := in.St
	switch {
	case in.Live:
		v.Sub = i18n.T("Not available on the live system. Install Isotope first.")
		return v
	case in.Err != "":
		v.Sub = in.Err
		return v
	case !in.Loaded:
		v.Sub = i18n.T("Loading…")
		return v
	case !st.Available:
		v.Sub = st.Reason
		v.Extension = st.NeedsExtension
		if st.NeedsExtension {
			v.Sub = i18n.T("Needs the Remote desktop system extension, from the Extensions page.")
		}
		return v
	}
	v.Password = i18n.T("Set password…")
	if st.PasswordSet {
		v.Password = i18n.T("Change password…")
	}
	if st.Running {
		v.Sub, v.Toggle = i18n.T("On · A VNC viewer on another computer can see and control this desktop."), i18n.T("Turn off")
		v.Rows = append(v.Rows,
			[2]string{i18n.T("Connect to"), rdConnect(st)},
			[2]string{i18n.T("User name"), i18n.Tf("%s, with the remote desktop password", st.User)})
		if st.Fingerprint != "" {
			v.Rows = append(v.Rows, [2]string{i18n.T("Certificate (SHA-256)"), st.Fingerprint})
		}
		if st.Firewall == remotedesktop.FirewallClosed {
			v.Warn = i18n.Tf("The firewall blocks port %d/%s, so other computers cannot connect. Turn remote desktop off and on again as an administrator.", st.Port, remotedesktop.Proto)
		}
		v.Hint = i18n.T("It stays on until you turn it off or log out. Every connection is encrypted (TLS or RSA-AES).")
		return v
	}
	v.Sub, v.Toggle = i18n.T("Off · Nothing listens for viewers."), i18n.T("Turn on")
	v.ToggleNeedPw = !st.PasswordSet
	v.Hint = i18n.Tf("Turning it on starts a VNC server for this session on port %d and lets connections in through the firewall until you turn it off or log out. A viewer logs in with your user name and the remote desktop password, not your login password.", st.Port)
	if !in.Admin {
		v.Hint += " " + i18n.T("While the firewall is on, only an administrator can turn it on.")
	}
	return v
}

// rdClicks are the section's buttons.
type rdClicks struct {
	toggle, password, extensions widget.Clickable
}

// loadRemote reads the server's state. Not on the live system.
func (p *networkPage) loadRemote() {
	u, r := p.u, p.u.hooks.RemoteDesktop
	if r == nil || u.hooks.Live {
		return
	}
	u.async(func() {
		st, err := r.Status()
		u.mu.Lock()
		defer u.mu.Unlock()
		p.rdLoaded = true
		if err != nil {
			p.rdErr = err.Error()
			return
		}
		p.rd, p.rdErr = st, ""
	})
}

func (p *networkPage) rdInput() rdInput {
	return rdInput{Live: p.u.hooks.Live, Admin: p.u.sys.Admin, Loaded: p.rdLoaded, Err: p.rdErr, St: p.rd}
}

// rdChange runs a change and reads the state again.
func (p *networkPage) rdChange(working string, f func(RemoteDesktop) error) {
	u, r := p.u, p.u.hooks.RemoteDesktop
	if r == nil {
		return
	}
	u.mu.Lock()
	p.working = working
	u.mu.Unlock()
	u.async(func() {
		err := f(r)
		u.mu.Lock()
		p.working = ""
		u.mu.Unlock()
		if err != nil {
			u.tell(i18n.T("Remote desktop"), err.Error())
		}
		p.loadRemote()
		p.loadFirewall()
	})
}

// updateRemote handles the section's clicks. u.mu is not held.
func (p *networkPage) updateRemote(gtx layout.Context) {
	u := p.u
	u.mu.Lock()
	v, running := remoteViewOf(p.rdInput()), p.rd.Running
	u.mu.Unlock()
	if p.rdk.toggle.Clicked(gtx) && v.Toggle != "" {
		switch {
		case running:
			p.rdChange(i18n.T("Turning remote desktop off…"), func(r RemoteDesktop) error { return r.SetOn(false) })
		case v.ToggleNeedPw:
			p.rdPasswordDialog(true)
		default:
			p.rdChange(i18n.T("Turning remote desktop on…"), func(r RemoteDesktop) error { return r.SetOn(true) })
		}
	}
	if p.rdk.password.Clicked(gtx) && v.Password != "" {
		p.rdPasswordDialog(false)
	}
	if p.rdk.extensions.Clicked(gtx) && v.Extension {
		u.show(PageExtensions)
	}
}

// rdPasswordDialog asks for the remote desktop password twice. With
// thenOn, saving it also turns remote desktop on.
func (p *networkPage) rdPasswordDialog(thenOn bool) {
	u := p.u
	pw, again := guikit.NewSecret(), guikit.NewSecret()
	p.rdPw, p.rdPw2 = pw, again
	focused := false
	var dlg *dialog
	ok := func() bool {
		return remotedesktop.CheckPassword(pw.Editor.Text()) == nil && pw.Editor.Text() == again.Editor.Text()
	}
	label := i18n.T("Save")
	if thenOn {
		label = i18n.T("Save and turn on")
	}
	dlg = newDialog(i18n.T("Remote desktop password"),
		i18n.T("A VNC viewer asks for it with your user name. Use a password of its own, not your login password."),
		func(gtx layout.Context) layout.Dimensions {
			if pw.Submitted(gtx) || again.Submitted(gtx) {
				u.pick(dlg, 1) // ignored while Save is off
			}
			if !focused {
				focused = true
				gtx.Execute(focus(&pw.Editor))
			}
			pw.Err, again.Err = "", ""
			if t := pw.Editor.Text(); t != "" {
				if err := remotedesktop.CheckPassword(t); err != nil {
					pw.Err = strings.ToUpper(err.Error()[:1]) + err.Error()[1:] + "."
				}
			}
			if a := again.Editor.Text(); a != "" && a != pw.Editor.Text() {
				again.Err = i18n.T("The two passwords are not the same.")
			}
			var c col
			c.add(func(gtx layout.Context) layout.Dimensions {
				return u.Labelled(gtx, i18n.T("Password"), pw, i18n.Tf("%d to %d characters.", remotedesktop.MinPassword, remotedesktop.MaxPassword), func(gtx layout.Context) layout.Dimensions {
					return u.Input(gtx, pw, guikit.StBody, 38, "", nil)
				})
			})
			c.gap(12)
			c.add(func(gtx layout.Context) layout.Dimensions {
				return u.Labelled(gtx, i18n.T("Again"), again, "", func(gtx layout.Context) layout.Dimensions {
					return u.Input(gtx, again, guikit.StBody, 38, "", nil)
				})
			})
			return c.layout(gtx)
		}, 1,
		choice{Label: i18n.T("Cancel"), Kind: guikit.ButtonSecondary},
		choice{Label: label, Kind: guikit.ButtonPrimary, Enabled: ok, Do: func() {
			secret := pw.Editor.Text()
			pw.Editor.SetText("")
			again.Editor.SetText("")
			p.rdChange(i18n.T("Saving the remote desktop password…"), func(r RemoteDesktop) error {
				if err := r.SetPassword(secret); err != nil {
					return err
				}
				if thenOn {
					return r.SetOn(true)
				}
				return nil
			})
		}})
	u.open(dlg)
}

// remoteSection is the REMOTE DESKTOP heading and its panel.
func (p *networkPage) remoteSection(gtx layout.Context, in rdInput, working string) layout.Dimensions {
	u := p.u
	v := remoteViewOf(in)
	off := working != ""
	var c col
	c.add(u.caps(i18n.T("Remote desktop")))
	c.gap(10)
	var as []guikit.Action
	switch {
	case v.Toggle != "":
		as = append(as, guikit.Action{Click: &p.rdk.toggle, Kind: guikit.ButtonSecondary, Label: v.Toggle, Disabled: off})
	case v.Extension:
		as = append(as, guikit.Action{Click: &p.rdk.extensions, Kind: guikit.ButtonSecondary, Label: i18n.T("Open Extensions")})
	}
	if v.Password != "" {
		as = append(as, guikit.Action{Click: &p.rdk.password, Kind: guikit.ButtonSecondary, Label: v.Password, Disabled: off})
	}
	rows := []layout.Widget{u.actionRow("rd", i18n.T("Remote desktop"), v.Sub, nil, as...)}
	for _, r := range v.Rows {
		rows = append(rows, u.actionRow("rd:"+r[0], r[0], r[1], nil))
	}
	c.add(u.group(rows...))
	if v.Warn != "" {
		c.gap(10)
		c.add(u.notice(v.Warn))
	}
	if v.Hint != "" {
		c.gap(8)
		c.add(u.dim(v.Hint))
	}
	return c.layout(gtx)
}
