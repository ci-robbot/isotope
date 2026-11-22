package main

import (
	"gioui.org/layout"
	"gioui.org/widget"

	"github.com/kairos-community/isotope/guikit"
)

func init() { pageMakers[PageSecurity] = func(u *UI) pageView { return &securityPage{u: u} } }

// securityPage: whether the disk is encrypted, and on an encrypted disk its
// keys (RecoveryStatus, polkit's recovery action). Replacing the recovery
// key stays `isotope recovery new-key` until phase 2.
type securityPage struct {
	u    *UI
	list widget.List

	// Under u.mu.
	rec     RecoveryStatus
	loaded  bool
	loadErr string
}

func (p *securityPage) Shown() {
	u, d := p.u, p.u.hooks.Daemon
	if d == nil || !u.sys.Encrypted || !u.sys.Admin {
		return
	}
	u.async(func() {
		st, err := d.RecoveryStatus()
		u.mu.Lock()
		defer u.mu.Unlock()
		if err != nil {
			p.loadErr = "Could not read the disk's keys: " + Describe(err)
			return
		}
		p.rec, p.loaded, p.loadErr = st, true, ""
	})
}

func (p *securityPage) Update(layout.Context) {}

func (p *securityPage) Footer() (leading, trailing []guikit.Action) { return nil, nil }

func (p *securityPage) Body(gtx layout.Context) layout.Dimensions {
	u := p.u
	u.mu.Lock()
	rec, loaded, loadErr := p.rec, p.loaded, p.loadErr
	u.mu.Unlock()
	return u.scrolled(gtx, &p.list, func(gtx layout.Context) layout.Dimensions {
		var c col
		c.add(u.Heading("Security", "Disk encryption keeps your files unreadable without the passphrase, the recovery key or this computer's TPM."))
		c.gap(24)
		if !u.sys.Encrypted {
			c.add(u.lead("The disk is not encrypted"))
			c.gap(6)
			c.add(u.para("Encryption is chosen when Isotope is installed (\"Encrypt your files?\"). To encrypt this machine, back it up, install again with encryption on, and restore the backup."))
			return c.layout(gtx)
		}
		c.add(u.lead("The disk is encrypted"))
		c.gap(12)
		switch {
		case !u.sys.Admin:
			c.add(u.notice("Only an administrator (a member of the admin group) can see and change the disk's keys."))
		case loadErr != "":
			c.add(u.errorBox(loadErr))
		case !loaded:
			c.add(u.spinnerLine("Reading the disk's keys…"))
		default:
			c.add(u.panel(func(gtx layout.Context) layout.Dimensions {
				var rows col
				for i, l := range RecoveryLines(rec) {
					if i > 0 {
						rows.gap(6)
					}
					rows.add(u.dim(l))
				}
				return rows.layout(gtx)
			}))
		}
		c.gap(28)
		c.add(u.caps("RECOVERY KEY"))
		c.gap(10)
		c.add(u.para("A recovery key opens the disk when the passphrase is lost or the TPM refuses (after a firmware update). To create a new one, which replaces the old one, run this in a terminal:"))
		c.gap(10)
		c.add(u.panel(func(gtx layout.Context) layout.Dimensions {
			return u.Label(gtx, guikit.StMono13, guikit.ColText, "isotope recovery new-key")
		}))
		c.gap(8)
		c.add(u.dim("Creating it here is coming in a later version of this page."))
		return c.layout(gtx)
	})
}
