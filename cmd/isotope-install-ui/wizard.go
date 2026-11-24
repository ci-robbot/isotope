package main

import (
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/kairos-community/isotope/pkg/i18n"
	"github.com/kairos-community/isotope/pkg/installer"
)

// The text front end of the wizard. The pages, their order and every check
// on what is typed are in installer.Wizard; this draws a page and turns keys into
// calls on it.

var stepName = map[installer.Page]string{
	installer.PageKeyboard:          i18n.Mark("keyboard"),
	installer.PageTimezone:          i18n.Mark("timezone"),
	installer.PageHostname:          i18n.Mark("hostname"),
	installer.PageUser:              i18n.Mark("username"),
	installer.PagePass:              i18n.Mark("password"),
	installer.PageGithub:            i18n.Mark("ssh keys"),
	installer.PageDisk:              i18n.Mark("target disk"),
	installer.PageEncrypt:           i18n.Mark("encryption"),
	installer.PagePassphrase:        i18n.Mark("disk passphrase"),
	installer.PagePassphraseConfirm: i18n.Mark("disk passphrase"),
	installer.PageRecovery:          i18n.Mark("recovery key"),
	installer.PageRecoveryShow:      i18n.Mark("recovery key"),
	installer.PageRecoveryConfirm:   i18n.Mark("recovery key"),
	installer.PageReview:            i18n.Mark("review"),
}

// wizardModel is the Bubble Tea model of the wizard: installer.Wizard plus what
// the text screens need to draw it.
type wizardModel struct {
	installer.Wizard

	in      textinput.Model
	diskIdx int
	err     string
	done    bool
	aborted bool

	kbPick picker
	tzPick picker

	// encIdx is the cursor on the encryption step, 0 for "no" (the
	// default) and 1 for "yes".
	encIdx int
	// recIdx is the cursor on the recovery key step, 0 for "no" (the
	// default) and 1 for "yes".
	recIdx int
}

// newWizard is the wizard with the built-in keyboard and timezone lists and
// no console side effects.
func newWizard(disks []installer.Disk) wizardModel {
	return newWizardModel(installer.NewWizard(disks))
}

func newWizardWith(disks []installer.Disk, keyboards []installer.Keyboard, zones []string, tz string) wizardModel {
	return newWizardModel(installer.NewWizardWith(disks, keyboards, zones, tz))
}

func newWizardModel(w installer.Wizard) wizardModel {
	m := wizardModel{Wizard: w}
	kl := make([]string, len(w.Keyboards))
	for i, k := range w.Keyboards {
		kl[i] = k.Description + "  " + k.Code()
	}
	m.kbPick = newPicker(kl)
	m.tzPick = newPicker(w.Zones)
	m.in = textinput.New()
	m.enter(installer.PageKeyboard)
	return m
}

func (m *wizardModel) isList() bool {
	return m.Page == installer.PageKeyboard || m.Page == installer.PageTimezone
}

// enter re-seeds the input for step s from already-collected state, so values
// survive back-navigation instead of being silently cleared.
func (m *wizardModel) enter(s installer.Page) {
	m.Page = s
	m.err = ""
	m.in.EchoMode = textinput.EchoNormal
	m.in.Prompt = "> "
	switch s {
	case installer.PageKeyboard:
		// The list steps use the input as a search filter, empty on entry,
		// with the cursor on the current choice.
		m.in.Prompt = i18n.T("search: ")
		m.in.SetValue("")
		m.kbPick.filter("")
		m.kbPick.selectIndex(m.KeyboardIndex())
	case installer.PageTimezone:
		m.in.Prompt = i18n.T("search: ")
		m.in.SetValue("")
		m.tzPick.filter("")
		m.tzPick.selectIndex(m.TimezoneIndex())
	case installer.PageHostname:
		m.in.SetValue(m.HostnameValue())
	case installer.PageUser:
		m.in.SetValue(m.Choice.Username)
	case installer.PagePass:
		m.in.EchoMode = textinput.EchoPassword
		m.in.SetValue(m.Choice.Password)
	case installer.PageGithub:
		m.in.SetValue(m.Choice.Github)
	case installer.PageEncrypt:
		m.encIdx = 0
		if m.Choice.Encrypt {
			m.encIdx = 1
		}
	case installer.PagePassphrase:
		m.in.EchoMode = textinput.EchoPassword
		m.in.SetValue(m.Choice.Passphrase)
	case installer.PagePassphraseConfirm:
		m.in.EchoMode = textinput.EchoPassword
		m.in.SetValue("")
	case installer.PageRecovery:
		m.recIdx = 0
		if m.Choice.RecoveryKey {
			m.recIdx = 1
		}
	case installer.PageRecoveryConfirm:
		m.in.SetValue("")
	}
	m.in.Focus()
	m.in.CursorEnd()
}

func (m wizardModel) Init() tea.Cmd { return textinput.Blink }

func (m wizardModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	key, ok := msg.(tea.KeyMsg)
	if !ok {
		var cmd tea.Cmd
		m.in, cmd = m.in.Update(msg)
		return m, cmd
	}

	switch key.String() {
	case "ctrl+c":
		m.aborted = true
		return m, tea.Quit
	case "esc":
		if m.Back() {
			m.enter(m.Page)
		}
		return m, nil
	}

	if m.isList() {
		return m.updateList(key)
	}
	if m.Page == installer.PageDisk {
		return m.updateDisk(key)
	}
	if m.Page == installer.PageEncrypt {
		return m.updateEncrypt(key)
	}
	if m.Page == installer.PageRecovery {
		return m.updateRecovery(key)
	}
	if m.Page == installer.PageRecoveryShow {
		if key.String() == "enter" {
			m.KeySaved()
			m.enter(m.Page)
		}
		return m, nil
	}
	if m.Page == installer.PageReview {
		return m.updateReview(key)
	}
	if key.String() == "enter" {
		return m.commitField()
	}
	var cmd tea.Cmd
	m.in, cmd = m.in.Update(msg)
	return m, cmd
}

// commitField hands the text field to the wizard. On an error the page
// stays, and a confirmation field starts over empty.
func (m wizardModel) commitField() (tea.Model, tea.Cmd) {
	v := m.in.Value()
	var err error
	switch m.Page {
	case installer.PageRecoveryConfirm:
		err = m.ConfirmRecoveryKey(v)
	case installer.PagePassphrase:
		err = m.SetPassphrase(v)
	case installer.PagePassphraseConfirm:
		err = m.ConfirmPassphrase(v)
	case installer.PageHostname:
		m.SetHostname(v)
	case installer.PageUser:
		err = m.SetUsername(v)
	case installer.PagePass:
		err = m.SetPassword(v)
	case installer.PageGithub:
		err = m.SetGithub(v)
	default:
		return m, nil
	}
	if err != nil {
		if m.Page == installer.PageRecoveryConfirm || m.Page == installer.PagePassphraseConfirm {
			m.in.SetValue("")
		}
		m.err = err.Error()
		return m, nil
	}
	m.enter(m.Page)
	return m, nil
}

// updateList drives the keyboard and timezone steps: arrows move, enter
// picks the entry under the cursor, anything else edits the search filter.
func (m wizardModel) updateList(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	p := &m.kbPick
	if m.Page == installer.PageTimezone {
		p = &m.tzPick
	}
	switch key.String() {
	case "up":
		p.move(-1)
		return m, nil
	case "down":
		p.move(1)
		return m, nil
	case "pgup":
		p.move(-pickerRows)
		return m, nil
	case "pgdown":
		p.move(pickerRows)
		return m, nil
	case "enter":
		i, ok := p.selected()
		if !ok {
			m.err = i18n.T("nothing matches the search")
			return m, nil
		}
		if m.Page == installer.PageKeyboard {
			m.SetKeyboard(i)
		} else {
			m.SetTimezone(i)
		}
		m.enter(m.Page)
		return m, nil
	}
	var cmd tea.Cmd
	m.in, cmd = m.in.Update(key)
	p.filter(m.in.Value())
	m.err = ""
	return m, cmd
}

func (m wizardModel) updateDisk(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch key.String() {
	case "up", "k":
		if m.diskIdx > 0 {
			m.diskIdx--
		}
	case "down", "j":
		if m.diskIdx < len(m.Disks)-1 {
			m.diskIdx++
		}
	case "enter":
		m.SetDisk(m.diskIdx)
		m.enter(m.Page)
	}
	return m, nil
}

func (m wizardModel) updateEncrypt(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch key.String() {
	case "up", "k":
		m.encIdx = 0
	case "down", "j":
		m.encIdx = 1
	case "enter":
		m.SetEncrypt(m.encIdx == 1)
		m.enter(m.Page)
	}
	return m, nil
}

func (m wizardModel) updateRecovery(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch key.String() {
	case "up", "k":
		m.recIdx = 0
	case "down", "j":
		m.recIdx = 1
	case "enter":
		if err := m.SetRecoveryKey(m.recIdx == 1); err != nil {
			m.err = err.Error()
			return m, nil
		}
		m.enter(m.Page)
	}
	return m, nil
}

// passphraseRecoveryText is shown on the recovery key step after the disk
// passphrase, on the normal edition: lines for an 80-column console.
var passphraseRecoveryText = i18n.Mark(
	"A recovery key is a second, long code that also opens your files.\n" +
		"It is for the day you forget the passphrase: without it, your\n" +
		"files are gone.\n" +
		"\n" +
		"If you create one, keep it somewhere safe and NOT on this computer:\n" +
		"on paper, or in a password manager on another device. Anyone with\n" +
		"the key and the disk can read your files.")

// recoveryText is shown on the recovery key step of a Trusted Boot install:
// lines for an 80-column console.
var recoveryText = i18n.Mark(
	"This computer unlocks its disk with its TPM security chip, without\n" +
		"asking you anything. Without a recovery key, your files cannot be\n" +
		"recovered if:\n" +
		"\n" +
		"  - the TPM is reset or cleared,\n" +
		"  - a firmware (BIOS) update changes what the TPM checks at start,\n" +
		"  - the disk is moved to another computer.\n" +
		"\n" +
		"A recovery key is a long code that the computer asks for only then.\n" +
		"If you create one, keep it somewhere safe and NOT on this computer:\n" +
		"on paper, or in a password manager on another device.")

// encryptText is shown on the encryption step. It says what the passphrase
// protects, what it costs, and what happens if it is lost; see the design
// note for the reasoning. Lines for an 80-column console.
var encryptText = i18n.Mark(
	"Encrypt the partition that holds your files (/home) and /var\n" +
		"with a passphrase you choose.\n" +
		"\n" +
		"This computer asks for the passphrase every time it starts.\n" +
		"\n" +
		"Protects: your files if the laptop or its disk is stolen while it\n" +
		"is switched off. The system itself is not encrypted; it holds\n" +
		"nothing of yours.\n" +
		"\n" +
		"If you forget the passphrase, your files are gone. Nobody can\n" +
		"recover them. Write it down and keep it away from the computer.")

func (m wizardModel) updateReview(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	if key.String() == "enter" {
		m.done = true
		return m, tea.Quit
	}
	return m, nil
}

func (m wizardModel) View() string {
	var b strings.Builder
	b.WriteString("\n  " + tnHot.Render("ISOTOPE") +
		tnDim.Render(" // "+i18n.Tf("desktop install · %s", i18n.T(stepName[m.Page]))) + "\n\n")

	switch m.Page {
	case installer.PageKeyboard, installer.PageTimezone:
		p, labels := m.kbPick, m.kbPick.labels
		if m.Page == installer.PageTimezone {
			p, labels = m.tzPick, m.tzPick.labels
		}
		b.WriteString("  " + prompt(m.Page) + "\n")
		b.WriteString("  " + m.in.View() + "\n\n")
		from, to := p.window()
		for pos := from; pos < to; pos++ {
			line := labels[p.match[pos]]
			if pos == p.cursor {
				b.WriteString("  " + tnAcc.Render("> "+line) + "\n")
			} else {
				b.WriteString("    " + tnDim.Render(line) + "\n")
			}
		}
		if len(p.match) == 0 {
			b.WriteString("    " + tnDim.Render(i18n.T("no match")) + "\n")
		}
		if m.err != "" {
			b.WriteString("\n  " + haltRed.Render(m.err) + "\n")
		}
		hint := i18n.T("type to search · up/down select · enter confirm")
		if m.Page > installer.PageKeyboard {
			hint = i18n.T("type to search · up/down select · enter confirm · esc back")
		}
		b.WriteString("\n  " + tnDim.Render(hint) + "\n")

	case installer.PageDisk:
		b.WriteString("  " + tnDim.Render(i18n.T("the selected disk will be ERASED")) + "\n\n")
		for i, d := range m.Disks {
			line := fmt.Sprintf("%s  %d GB", d.Path, d.SizeGB)
			if i == m.diskIdx {
				b.WriteString("  " + tnAcc.Render("> "+line) + "\n")
			} else {
				b.WriteString("    " + tnDim.Render(line) + "\n")
			}
		}
		b.WriteString("\n  " + tnDim.Render(i18n.T("up/down select · enter confirm · esc back")) + "\n")

	case installer.PageEncrypt:
		b.WriteString("  " + tnHot.Render(i18n.T("Encrypt your files?")) + "\n\n")
		for _, l := range strings.Split(i18n.T(encryptText), "\n") {
			b.WriteString("  " + l + "\n")
		}
		b.WriteString("\n")
		for i, l := range []string{i18n.T("no, do not encrypt"), i18n.T("yes, encrypt my files")} {
			if i == m.encIdx {
				b.WriteString("  " + tnAcc.Render("> "+l) + "\n")
			} else {
				b.WriteString("    " + tnDim.Render(l) + "\n")
			}
		}
		b.WriteString("\n  " + tnDim.Render(i18n.T("up/down select · enter confirm · esc back")) + "\n")

	case installer.PageRecovery:
		title, text, opts := i18n.T("Create a recovery key?"), i18n.T(recoveryText), []string{i18n.T("no, the TPM only"), i18n.T("yes, create a recovery key")}
		if m.Choice.Encrypt {
			title, text, opts = i18n.T("Also create a recovery key?"), i18n.T(passphraseRecoveryText), []string{i18n.T("no, the passphrase only"), i18n.T("yes, also create a recovery key")}
		}
		b.WriteString("  " + tnHot.Render(title) + "\n\n")
		for _, l := range strings.Split(text, "\n") {
			b.WriteString("  " + l + "\n")
		}
		b.WriteString("\n")
		for i, l := range opts {
			if i == m.recIdx {
				b.WriteString("  " + tnAcc.Render("> "+l) + "\n")
			} else {
				b.WriteString("    " + tnDim.Render(l) + "\n")
			}
		}
		if m.err != "" {
			b.WriteString("\n  " + haltRed.Render(m.err) + "\n")
		}
		b.WriteString("\n  " + tnDim.Render(i18n.T("up/down select · enter confirm · esc back")) + "\n")

	case installer.PageRecoveryShow:
		b.WriteString("  " + tnHot.Render(i18n.T("Your recovery key")) + "\n\n")
		for _, l := range installer.RecoveryKeyLines(m.Choice.RecoveryKeyValue) {
			b.WriteString("  " + tnAcc.Render(l) + "\n")
		}
		b.WriteString("\n")
		for _, l := range strings.Split(i18n.T("Write it down or take a photo of it now, and keep it away from\nthis computer. It is shown only this once and saved nowhere."), "\n") {
			b.WriteString("  " + l + "\n")
		}
		b.WriteString("  " + i18n.Tf("The letters are only these: %s", strings.Join(strings.Split(installer.Modhex, ""), " ")) + "\n")
		b.WriteString("\n  " + tnDim.Render(i18n.T("enter when you have saved it · esc back")) + "\n")

	case installer.PageReview:
		b.WriteString("  " + haltRed.Render(i18n.Tf("this ERASES %s and installs isotope", m.Choice.Disk)) + "\n\n")
		b.WriteString(reviewLabel(i18n.T("keyboard")) + tnHot.Render(keyboardLabel(m.Choice)) + "\n")
		b.WriteString(reviewLabel(i18n.T("timezone")) + tnHot.Render(m.Choice.Timezone) + "\n")
		b.WriteString(reviewLabel(i18n.T("hostname")) + tnHot.Render(m.Choice.Hostname) + "\n")
		b.WriteString(reviewLabel(i18n.T("username")) + tnHot.Render(m.Choice.Username) + "\n")
		b.WriteString(reviewLabel(i18n.T("ssh keys")) + tnHot.Render(orDefault(githubLabel(m.Choice.Github), i18n.T("none"))) + "\n")
		b.WriteString(reviewLabel(i18n.T("disk")) + haltRed.Render(m.Choice.Disk) + "\n")
		if m.TrustedBoot || m.Choice.Encrypt {
			rk := i18n.T("no")
			if m.TrustedBoot {
				rk = i18n.T("no (the TPM only)")
			}
			if m.Choice.RecoveryKey {
				rk = i18n.T("yes (you saved it)")
			}
			b.WriteString(reviewLabel(i18n.T("recovery")) + tnHot.Render(rk) + "\n")
		}
		if !m.TrustedBoot {
			enc := i18n.T("off")
			if m.Choice.Encrypt {
				enc = i18n.T("on (passphrase at every start)")
			}
			b.WriteString(reviewLabel(i18n.T("encrypt")) + tnHot.Render(enc) + "\n")
		}
		b.WriteString("\n  " + tnDim.Render(i18n.T("enter to INSTALL · esc back")) + "\n")

	default:
		if m.Page == installer.PageRecoveryConfirm {
			b.WriteString("  " + m.recoveryPrompt() + "\n")
		} else {
			b.WriteString("  " + prompt(m.Page) + "\n")
		}
		b.WriteString("  " + m.in.View() + "\n")
		if m.Page == installer.PagePassphrase {
			if h := installer.PassphraseStrength(m.in.Value()); h != "" {
				b.WriteString("  " + tnDim.Render(i18n.Tf("strength: %s", h)) + "\n")
			}
		}
		if m.err != "" {
			b.WriteString("\n  " + haltRed.Render(m.err) + "\n")
		}
		b.WriteString("\n  " + tnDim.Render(i18n.T("enter continue · esc back")) + "\n")
	}
	return b.String()
}

// reviewLabel is a row's name on the review page, in a column 11 wide.
func reviewLabel(name string) string {
	return "    " + name + strings.Repeat(" ", max(1, 11-utf8.RuneCountInString(name)))
}

func keyboardLabel(c installer.Choice) string {
	return installer.Keyboard{Layout: c.KeyboardLayout, Variant: c.KeyboardVariant}.Code()
}

func githubLabel(g string) string {
	if g == "" {
		return ""
	}
	return "github:" + g
}

func prompt(s installer.Page) string {
	switch s {
	case installer.PageKeyboard:
		return tnDim.Render(i18n.T("keyboard layout"))
	case installer.PageTimezone:
		return tnDim.Render(i18n.T("timezone"))
	case installer.PageHostname:
		return tnDim.Render(i18n.T("hostname for this machine"))
	case installer.PageUser:
		return tnDim.Render(i18n.T("username for the desktop account"))
	case installer.PagePass:
		return tnDim.Render(i18n.T("password for that account"))
	case installer.PageGithub:
		return tnDim.Render(i18n.T("GitHub username to import SSH keys from (blank to skip)"))
	case installer.PagePassphrase:
		return tnDim.Render(i18n.Tf("disk passphrase, %d characters or more (a few words work well)", installer.MinPassphraseLen))
	case installer.PagePassphraseConfirm:
		return tnDim.Render(i18n.T("type the disk passphrase again"))
	}
	return ""
}

// recoveryPrompt asks for one group of the recovery key back.
func (m wizardModel) recoveryPrompt() string {
	n := m.ConfirmGroup + 1
	return tnDim.Render(i18n.Tf("to check you saved it, type the %[1]s group of the recovery key (group %[2]d of 8)", installer.Ordinal(n), n))
}

// --- entry point -----------------------------------------------------------

// runWizard is installer.RunWizard with this text wizard as its interactive front
// end.
func runWizard() (*installer.Choice, error) { return installer.RunWizard(askTUI) }

// askTUI runs the wizard on the terminal and returns what was confirmed.
func askTUI(w installer.Wizard) (installer.Choice, error) {
	i18n.Init()
	final, err := tea.NewProgram(newWizardModel(w), tea.WithAltScreen()).Run()
	if err != nil {
		return installer.Choice{}, err
	}
	wm, ok := final.(wizardModel)
	if !ok || wm.aborted || !wm.done {
		return installer.Choice{}, errors.New("install cancelled")
	}
	return wm.Choice, nil
}

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}
