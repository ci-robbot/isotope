package main

import (
	"strings"

	"github.com/kairos-community/isotope/pkg/guikit"
	"github.com/kairos-community/isotope/pkg/i18n"
)

// The install flow of the Updates and the Desktop & image pages, as the
// CLI's installFlow: inspect the image, show what it is, ask, apply, follow
// the job, offer the restart. The daemon does every step; polkit decides.

// install is the install the window started and follows, under u.mu.
type install struct {
	active bool
	job    installJob
	// preparing is set while the image is inspected, before the question.
	preparing string
}

// startInstall inspects ref and asks before installing it. what names it
// in the question ("Isotope v1.2.0", "the niri desktop"); warning, when
// set, is said first (another desktop, another image line). advanced is
// set only for an image the user typed: only then may an overridable
// refusal be lifted, and only by its own question.
func (u *UI) startInstall(what, ref string, advanced bool, warning string) {
	d := u.hooks.Daemon
	if d == nil {
		u.tell(i18n.T("Cannot install"), i18n.T("The update service (isotope-updated) is not available."))
		return
	}
	u.mu.Lock()
	if u.inst.active || u.inst.preparing != "" {
		u.mu.Unlock()
		return
	}
	u.inst.preparing = i18n.Tf("Checking %s…", what)
	u.mu.Unlock()
	u.async(func() {
		// Always without the override, so the daemon reports the refusal
		// it would make and the user is asked about it.
		info, err := d.Inspect(ref, false)
		u.mu.Lock()
		u.inst.preparing = ""
		u.mu.Unlock()
		if err != nil {
			u.tell(i18n.Tf("Cannot install %s", what), Describe(err))
			return
		}
		q := overrideQuestion(info, advanced)
		if info.Refusal != "" && q == "" {
			u.tell(i18n.Tf("Cannot install %s", what), info.Refusal)
			return
		}
		var b strings.Builder
		if warning != "" {
			b.WriteString(warning + "\n\n")
		}
		if info.GPU != "" && info.GPU != u.sys.Release.Line() && warning == "" {
			b.WriteString(lineWarning(info.GPU) + "\n\n")
		}
		b.WriteString(strings.Join(InspectReport(info), "\n"))
		b.WriteString("\n\n" + i18n.T("The running system is not touched; the new one is used from the next boot."))
		cancel := choice{Label: i18n.T("Cancel"), Kind: guikit.ButtonSecondary}
		if q != "" {
			u.ask(q, info.Refusal+"\n\n"+b.String(), cancel,
				choice{Label: i18n.T("Install anyway"), Kind: guikit.ButtonDanger, Do: func() { u.apply(ref, info.Digest, true) }})
			return
		}
		u.ask(i18n.Tf("Install %s?", what), b.String(), cancel,
			choice{Label: i18n.T("Install"), Kind: guikit.ButtonPrimary, Do: func() { u.apply(ref, info.Digest, false) }})
	})
}

func (u *UI) apply(ref, digest string, override bool) {
	d := u.hooks.Daemon
	u.mu.Lock()
	u.inst.active = true
	// The daemon accepted nothing yet, but a job that ends before Apply
	// returns must still count as this one.
	u.inst.job = installJob{started: true}
	u.mu.Unlock()
	u.async(func() {
		if err := d.Apply(ref, digest, override); err != nil {
			u.mu.Lock()
			u.inst.active = false
			u.mu.Unlock()
			u.tell(i18n.T("The update did not start"), Describe(err))
		}
	})
}

// installEnded is the job watcher of the install flow.
func (u *UI) installEnded(_ string, st Status) {
	u.mu.Lock()
	if !u.inst.active {
		u.mu.Unlock()
		return
	}
	end := u.inst.job.Step(st)
	if end.Done {
		u.inst.active = false
	}
	u.mu.Unlock()
	if !end.Done {
		return
	}
	if !end.OK {
		u.tell(i18n.T("The update did not install"), end.Message)
		return
	}
	u.askRestart(end.Message)
}

// askRestart offers the restart into an installed update.
func (u *UI) askRestart(msg string) {
	u.ask(i18n.T("Restart now?"), msg, choice{Label: i18n.T("Later"), Kind: guikit.ButtonSecondary},
		choice{Label: i18n.T("Restart now"), Kind: guikit.ButtonPrimary, Do: u.reboot})
}

func (u *UI) reboot() {
	d := u.hooks.Daemon
	if d == nil {
		return
	}
	if u.writingFirmware() {
		u.tell(i18n.T("Cannot restart"), i18n.T(firmwareWritingText))
		return
	}
	u.async(func() {
		if err := d.Reboot(); err != nil {
			u.tell(i18n.T("Cannot restart"), Describe(err))
		}
	})
}

// firmwareWritingText is why a restart waits for a firmware write; the
// daemon gives the same sentence when another program's write blocks it.
var firmwareWritingText = i18n.Mark("A firmware update is being written. Restarting now could damage the device. Try again when it is done.")

// writingFirmware says whether this window has fwupd write firmware now.
func (u *UI) writingFirmware() bool {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.firmwareWriting
}

// installing is what the install flow is doing now, "" when nothing.
func (u *UI) installing() string {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.inst.preparing != "" {
		return u.inst.preparing
	}
	return ""
}
