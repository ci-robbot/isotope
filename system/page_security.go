package main

import (
	"gioui.org/layout"
	"gioui.org/widget"

	"github.com/kairos-community/isotope/guikit"
	"github.com/kairos-community/isotope/update/recoverykey"
)

func init() {
	pageMakers[PageSecurity] = func(u *UI) pageView {
		return &securityPage{u: u, cur: guikit.NewSecret(), resealKey: guikit.NewSecret()}
	}
}

// securityPage: whether the disk is encrypted, and on an encrypted disk its
// keys (RecoveryStatus, polkit's recovery action), a new recovery key
// (RecoveryNewKey) and, on Trusted Boot, the reseal (RecoveryReseal): the
// CLI's `isotope recovery new-key` and `reseal` in dialogs.
type securityPage struct {
	u    *UI
	list widget.List

	// Under u.mu.
	rec     RecoveryStatus
	loaded  bool
	loadErr string
	// busy is the running call, "" for none.
	busy string

	// cur is the current passphrase or recovery key for a new key,
	// resealKey the recovery key for a reseal. Both are cleared after use.
	cur, resealKey            *guikit.Field
	curFocused, resealFocused bool
	newKeyBtn, resealBtn      widget.Clickable
}

// tpmUnlocksAll tells if the TPM unlocks every container, so it can
// authorize a new key alone (the CLI's tpmWorksEverywhere).
func tpmUnlocksAll(st RecoveryStatus) bool {
	if len(st.Containers) == 0 {
		return false
	}
	for _, c := range st.Containers {
		if c.TPM == 0 || !c.TPMWorks {
			return false
		}
	}
	return true
}

// resealView tells if the page offers the reseal (Trusted Boot, and the TPM
// does not unlock some container it is enrolled in), or else the note to
// show in its place.
func resealView(st RecoveryStatus) (offer bool, note string) {
	if st.Edition != "trusted-boot" {
		return false, ""
	}
	for _, c := range st.Containers {
		if c.TPM > 0 && !c.TPMWorks {
			return true, ""
		}
	}
	if tpmUnlocksAll(st) {
		return false, "The TPM unlocks the disk; nothing to reseal"
	}
	return false, ""
}

// keys is the last RecoveryStatus, and whether the page can act on it.
func (p *securityPage) keys() (RecoveryStatus, bool) {
	p.u.mu.Lock()
	defer p.u.mu.Unlock()
	return p.rec, p.loaded && p.busy == "" && p.u.sys.Admin
}

// run makes one recovery call off the UI goroutine with a spinner, then
// tells how it went and reads the keys again. On a failure, failNote (when
// not nil) says what the error means for the user, before the daemon's
// reason.
func (p *securityPage) run(busy, failed string, failNote func(error) string, doneTitle, doneBody string, call func(Daemon) error) {
	u, d := p.u, p.u.hooks.Daemon
	if d == nil {
		return
	}
	u.mu.Lock()
	p.busy = busy
	u.mu.Unlock()
	u.async(func() {
		err := call(d)
		u.mu.Lock()
		p.busy = ""
		u.mu.Unlock()
		if err != nil {
			body := Describe(err)
			if failNote != nil {
				body = failNote(err) + "\n" + body
			}
			u.tell(failed, body)
		} else {
			u.tell(doneTitle, doneBody)
		}
		p.Shown()
	})
}

func (p *securityPage) clearForms() {
	for _, f := range []*guikit.Field{p.cur, p.resealKey} {
		f.Editor.SetText("")
		f.Err = ""
	}
	p.curFocused, p.resealFocused = false, false
}

// openNewKey asks for what unlocks the disk now, unless the TPM unlocks
// every container on Trusted Boot, then shows the new key.
func (p *securityPage) openNewKey() {
	st, ok := p.keys()
	if !ok {
		return
	}
	p.clearForms()
	trusted := st.Edition == "trusted-boot"
	if trusted && tpmUnlocksAll(st) {
		p.showNewKey("")
		return
	}
	title, hint := "The disk passphrase or the current recovery key", ""
	if trusted {
		title, hint = "The current recovery key", "The TPM does not unlock the disk now."
	}
	u := p.u
	u.askForm("New recovery key", "The new key replaces the old one, which then stops working.", func(gtx layout.Context) layout.Dimensions {
		if !p.curFocused {
			p.curFocused = true
			gtx.Execute(focus(&p.cur.Editor))
		}
		return fieldsCol(gtx, u, fieldRow{title: title, f: p.cur, hint: hint})
	}, 0,
		choice{Label: "Cancel", Kind: guikit.ButtonSecondary, Do: p.clearForms},
		choice{Label: "Continue", Kind: guikit.ButtonPrimary, Enabled: func() bool { return p.cur.Editor.Text() != "" }, Do: func() {
			cur := p.cur.Editor.Text()
			p.clearForms()
			p.showNewKey(cur)
		}})
}

// showNewKey makes the key, shows it once and, once a group is typed back,
// sends it with current.
func (p *securityPage) showNewKey(current string) {
	u := p.u
	key, err := recoverykey.New()
	if err != nil {
		u.tell("Could not make a recovery key", err.Error())
		return
	}
	u.keyDialog("Your new recovery key", "Write it down and keep it away from this computer. It is shown only now.", key, func() {
		p.run("Replacing the recovery key…", "The recovery key was not replaced",
			newKeyFailNote,
			"Recovery key replaced", "The new recovery key works; the old one does not any more.",
			func(d Daemon) error { return d.RecoveryNewKey(current, key) })
	})
}

// newKeyFailNote says which recovery key works after RecoveryNewKey failed.
// The daemon's refusals before it changes the disk leave the old key alone.
// After it added the new key to part of the disk ("PartlyChanged"), and
// for any failure the app cannot place (a lost connection, a timeout), both
// keys may be needed.
func newKeyFailNote(err error) string {
	switch errName(err) {
	case "NotAuthorized", "Busy", "Exiting", "RebootPending", "Rebooting", "Refused":
		return "The key you wrote down does not work; your old key still does."
	}
	return "Keep both your old key and the key you wrote down: some parts of the disk may already use the new key."
}

// openReseal asks for the recovery key and seals the disk to the TPM
// again.
func (p *securityPage) openReseal() {
	st, ok := p.keys()
	if offer, _ := resealView(st); !ok || !offer {
		return
	}
	p.clearForms()
	u := p.u
	u.askForm("Seal to this computer's TPM again", "After a firmware update the TPM may no longer unlock the disk. The recovery key opens it, then the disk is sealed to the TPM as it is now.", func(gtx layout.Context) layout.Dimensions {
		if !p.resealFocused {
			p.resealFocused = true
			gtx.Execute(focus(&p.resealKey.Editor))
		}
		return fieldsCol(gtx, u, fieldRow{title: "Recovery key", f: p.resealKey, hint: "8 groups of 8 letters; dashes optional."})
	}, 0,
		choice{Label: "Cancel", Kind: guikit.ButtonSecondary, Do: p.clearForms},
		choice{Label: "Seal again", Kind: guikit.ButtonPrimary, Enabled: func() bool { return recoverykey.Canonical(p.resealKey.Editor.Text()) != "" }, Do: func() {
			key := recoverykey.Canonical(p.resealKey.Editor.Text())
			p.clearForms()
			p.run("Sealing the disk to the TPM…", "The disk was not sealed again", nil,
				"Sealed again", "The next start unlocks the disk without the recovery key.",
				func(d Daemon) error { return d.RecoveryReseal(key) })
		}})
}

// actions are the page's buttons: off for a non-admin, before the keys are
// read and while a call runs.
func (p *securityPage) actions() []guikit.Action {
	st, ok := p.keys()
	as := []guikit.Action{{Click: &p.newKeyBtn, Kind: guikit.ButtonPrimary, Label: "New recovery key…", Disabled: !ok}}
	if offer, _ := resealView(st); offer {
		as = append(as, guikit.Action{Click: &p.resealBtn, Kind: guikit.ButtonSecondary, Label: "Seal to this computer's TPM again", Disabled: !ok})
	}
	return as
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

func (p *securityPage) Update(gtx layout.Context) {
	if p.newKeyBtn.Clicked(gtx) {
		p.openNewKey()
	}
	if p.resealBtn.Clicked(gtx) {
		p.openReseal()
	}
}

func (p *securityPage) Footer() (leading, trailing []guikit.Action) { return nil, nil }

func (p *securityPage) Body(gtx layout.Context) layout.Dimensions {
	u := p.u
	u.mu.Lock()
	rec, loaded, loadErr, busy := p.rec, p.loaded, p.loadErr, p.busy
	u.mu.Unlock()
	acts := p.actions()
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
		c.add(u.para("A recovery key opens the disk when the passphrase is lost or the TPM refuses (after a firmware update). A new one replaces the old one, which then stops working."))
		c.gap(12)
		if !u.sys.Admin {
			c.add(u.dim("Only an administrator can create a recovery key or seal the disk to the TPM again."))
			c.gap(10)
		}
		if busy != "" {
			c.add(u.spinnerLine(busy))
			c.gap(10)
		}
		c.add(u.buttons(acts...))
		if _, note := resealView(rec); note != "" && loaded {
			c.gap(10)
			c.add(u.dim(note))
		}
		return c.layout(gtx)
	})
}
