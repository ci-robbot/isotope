package core

import (
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"
)

// DefaultHostname is the hostname when the person leaves the field empty.
const DefaultHostname = "isotope"

// OEMDir is the directory scanned for a seeded config.
func OEMDir() string {
	if d := os.Getenv("ISOTOPE_OEM_DIR"); d != "" {
		return d
	}
	return "/oem"
}

// ConfigOut is the file the wizard writes. It is normally in OEMDir, but a
// Trusted Boot (UKI) install medium boots with a read-only root: there /oem is
// a read-only directory and only /etc, /var, /srv and /home are writable.
// 90_desktop_installer.yaml sets ISOTOPE_CONFIG_OUT to a file in /etc/kairos,
// which kairos-agent also reads its config from (agent GetUserConfigDirs).
// OEMDir is still the directory scanned for a seeded config.
func ConfigOut() string {
	if p := os.Getenv("ISOTOPE_CONFIG_OUT"); p != "" {
		return p
	}
	return OEMDir() + "/99_isotope-user.yaml"
}

// posixUsername is the conservative intersection of what useradd accepts and
// what is safe to interpolate: lowercase start (letter or underscore), then
// letters, digits, dash or underscore, 32 characters at most.
//
// This is not cosmetic. RenderCloudConfig writes Username raw into the
// /etc/ly/save.ini block scalar and returns no error, so it cannot defend
// itself: a newline there closes the YAML block early and produces a
// cloud-config that fails to parse on a machine whose disk has already been
// wiped. The check has to live here, before the value can reach the renderer.
var posixUsername = regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,31}$`)

const usernameRule = "username must be 1-32 chars: lowercase letters, digits, - or _, starting with a letter or _"

// ValidateUsername checks the name of the desktop account.
func ValidateUsername(v string) error {
	if v == "" {
		return errors.New("username is required")
	}
	if !posixUsername.MatchString(v) {
		return errors.New(usernameRule)
	}
	return nil
}

// githubHandle is GitHub's own account-name shape: ASCII alphanumerics and
// hyphens, never leading or trailing, 39 characters at most.
//
// This matters more than the username check, not less. Github is the one field
// RenderCloudConfig writes into a *plain* YAML scalar with no quoting:
//
//   - github:<value>
//
// A newline there does not break the document, it extends it. The value
// "ada\nstages:\n  network:\n    - name: pwn" renders a cloud-config with two
// top-level `stages:` keys — still valid YAML, in which the injected block
// displaces the /etc/ly/save.ini stage and the machine boots to a login screen
// that was never configured. An invalid username fails loudly; this fails
// silently, and both happen after the disk has been wiped. RenderCloudConfig
// returns no error and so cannot refuse, so the check lives here at the input.
var githubHandle = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,37}[A-Za-z0-9])?$`)

const githubRule = "github username must be 1-39 chars: letters, digits or hyphens, not starting or ending with a hyphen"

// ValidateGithub accepts the empty string: importing SSH keys is optional and
// skipping the step is the documented way to decline.
func ValidateGithub(v string) error {
	if v == "" {
		return nil
	}
	if !githubHandle.MatchString(v) {
		return errors.New(githubRule)
	}
	return nil
}

// choiceFromEnv reads the non-interactive configuration. The second return value
// reports whether non-interactive mode is active at all. These variable names are
// a stable contract with existing tests and automation.
func choiceFromEnv() (Choice, bool, error) {
	if os.Getenv("ISOTOPE_INSTALL_NONINTERACTIVE") != "1" {
		return Choice{}, false, nil
	}
	c := Choice{
		Hostname: os.Getenv("ISOTOPE_HOSTNAME"),
		Username: os.Getenv("ISOTOPE_USER"),
		Password: os.Getenv("ISOTOPE_PASS"),
		Github:   os.Getenv("ISOTOPE_GITHUB"),
		Disk:     os.Getenv("ISOTOPE_DISK"),

		KeyboardLayout:  orDefault(os.Getenv("ISOTOPE_KEYBOARD_LAYOUT"), DefaultLayout),
		KeyboardVariant: os.Getenv("ISOTOPE_KEYBOARD_VARIANT"),
		Timezone:        orDefault(os.Getenv("ISOTOPE_TIMEZONE"), DefaultTimezone),

		Encrypt:    os.Getenv("ISOTOPE_ENCRYPT") == "1",
		Passphrase: os.Getenv("ISOTOPE_DISK_PASSPHRASE"),
	}
	if c.Hostname == "" {
		c.Hostname = DefaultHostname
	}
	var missing []string
	if c.Username == "" {
		missing = append(missing, "ISOTOPE_USER")
	}
	if c.Password == "" {
		missing = append(missing, "ISOTOPE_PASS")
	}
	if c.Disk == "" {
		missing = append(missing, "ISOTOPE_DISK")
	}
	if len(missing) > 0 {
		return c, true, fmt.Errorf("non-interactive mode requires %s", strings.Join(missing, ", "))
	}
	// The interactive path validates at the input step; the env is the other way
	// a username can reach RenderCloudConfig, so it is checked here too.
	if err := ValidateUsername(c.Username); err != nil {
		return c, true, fmt.Errorf("ISOTOPE_USER: %w", err)
	}
	// Same reasoning for the GitHub handle, which reaches an unquoted scalar.
	// Empty stays valid here: ISOTOPE_GITHUB is genuinely optional.
	if err := ValidateGithub(c.Github); err != nil {
		return c, true, fmt.Errorf("ISOTOPE_GITHUB: %w", err)
	}
	// Optional, defaulting to US English and UTC. The renderer would replace
	// a bad value with the default, but a typo here should fail the install,
	// not install a machine with a keyboard the operator did not ask for.
	if err := ValidateKeyboard(c.KeyboardLayout, c.KeyboardVariant); err != nil {
		return c, true, fmt.Errorf("ISOTOPE_KEYBOARD_LAYOUT/ISOTOPE_KEYBOARD_VARIANT: %w", err)
	}
	if err := ValidateTimezone(c.Timezone); err != nil {
		return c, true, fmt.Errorf("ISOTOPE_TIMEZONE: %w", err)
	}
	// Encryption has no default passphrase.
	if c.Encrypt {
		if err := ValidatePassphrase(c.Passphrase); err != nil {
			return c, true, fmt.Errorf("ISOTOPE_DISK_PASSPHRASE: %w", err)
		}
	} else {
		c.Passphrase = ""
	}
	return c, true, nil
}

// writeChoice hashes the password and writes the cloud-config with owner-only
// permissions — the file carries a password hash.
func writeChoice(path string, c Choice, hash string) error {
	if err := os.MkdirAll(dirOf(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(RenderCloudConfig(c, hash)), 0o600)
}

func dirOf(path string) string {
	if i := strings.LastIndex(path, "/"); i > 0 {
		return path[:i]
	}
	return "."
}

// installableDisks is ListDisks for the wizard's purposes. An unreadable
// sysBlock and an empty one mean the same thing to the user — there is nothing
// to install onto — and the wizard already has a clear screen for that case, so
// the error becomes an empty list rather than a bare syscall message printed
// over the branded UI.
func installableDisks(sysBlock string) []Disk {
	disks, err := ListDisks(sysBlock)
	if err != nil {
		return nil
	}
	return disks
}

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

// --- wizard pages ----------------------------------------------------------

// Page is one page of the wizard.
type Page int

// The keyboard comes first: the console switches to the chosen keymap, so the
// password is typed with the layout the installed system will use for it.
const (
	PageKeyboard Page = iota
	PageTimezone
	PageHostname
	PageUser
	PagePass
	PageGithub
	PageDisk
	PageEncrypt
	PagePassphrase
	PagePassphraseConfirm
	PageRecovery
	PageRecoveryShow
	PageRecoveryConfirm
	PageReview
)

// Wizard is the install wizard with no drawing: the page it is on, what has
// been chosen so far, and the rules that move it from page to page. A front
// end shows Page, collects the input and calls the method for that page;
// each method checks the input, stores it in Choice and moves Page on, or
// returns the error to show and leaves Page where it is.
type Wizard struct {
	Page   Page
	Choice Choice

	Disks     []Disk
	Keyboards []Keyboard
	Zones     []string

	// TrustedBoot hides the encryption page: a Trusted Boot install
	// encrypts with its TPM-sealed key instead, and offers only the
	// recovery key.
	TrustedBoot bool

	// KeymapsDir is where the console keymaps are looked up, and
	// ApplyKeymap loads one on the live console. NewWizard makes it a
	// no-op.
	KeymapsDir  string
	ApplyKeymap func(string)

	// ConfirmGroup is the 0-based group of the recovery key the person
	// types back. NewKey and PickGroup are crypto/rand backed; the tests
	// replace them.
	ConfirmGroup int
	NewKey       func() (string, error)
	PickGroup    func() int

	// passphraseFirst is the disk passphrase as first typed, until the
	// second entry matches it.
	passphraseFirst string
}

// NewWizard is the wizard with the built-in keyboard and timezone lists and
// no console side effects. NewSystemWizard uses NewWizardWith and the
// image's lists.
func NewWizard(disks []Disk) Wizard {
	return NewWizardWith(disks, CuratedKeyboards, CuratedTimezones, DefaultTimezone)
}

// NewWizardWith is the wizard on its first page, offering these disks,
// keyboards and zones. tz is the detected timezone; it becomes the default
// only when zones lists it.
func NewWizardWith(disks []Disk, keyboards []Keyboard, zones []string, tz string) Wizard {
	w := Wizard{
		Page:        PageKeyboard,
		Disks:       disks,
		Keyboards:   keyboards,
		Zones:       zones,
		ApplyKeymap: func(string) {},
		NewKey:      newRecoveryKey,
		PickGroup:   func() int { return recoveryGroupToConfirm(rand.Reader) },
	}
	w.Choice.KeyboardLayout = DefaultLayout
	w.Choice.Timezone = DefaultTimezone
	if ValidateTimezone(tz) == nil && indexOf(zones, tz) >= 0 {
		w.Choice.Timezone = tz
	}
	return w
}

// NewSystemWizard is the wizard for this machine: its disks, the image's
// keyboard and timezone lists, the detected timezone, and the live console's
// keymap loader.
func NewSystemWizard(trustedBoot bool) Wizard {
	w := NewWizardWith(installableDisks("/sys/block"),
		LoadKeyboards(XKBRulesList), loadTimezones(zoneinfoDir), detectTimezone("/etc/localtime"))
	w.KeymapsDir = keymapsDir
	w.ApplyKeymap = loadConsoleKeymap
	w.TrustedBoot = trustedBoot
	return w
}

func indexOf(list []string, v string) int {
	for i, s := range list {
		if s == v {
			return i
		}
	}
	return -1
}

// KeyboardIndex is the index in Keyboards of the chosen keyboard, or -1.
func (w *Wizard) KeyboardIndex() int {
	for i, k := range w.Keyboards {
		if k.Layout == w.Choice.KeyboardLayout && k.Variant == w.Choice.KeyboardVariant {
			return i
		}
	}
	return -1
}

// TimezoneIndex is the index in Zones of the chosen timezone, or -1.
func (w *Wizard) TimezoneIndex() int { return indexOf(w.Zones, w.Choice.Timezone) }

// Pages is the list of pages this wizard shows, in order, for this medium
// and the choices made so far: the encryption pages only on a normal medium,
// the passphrase pages only with encryption on, the recovery key page on
// Trusted Boot or with encryption on, and the key pages only with a key.
func (w *Wizard) Pages() []Page {
	pages := []Page{PageKeyboard, PageTimezone, PageHostname, PageUser, PagePass, PageGithub, PageDisk}
	if !w.TrustedBoot {
		pages = append(pages, PageEncrypt)
		if w.Choice.Encrypt {
			pages = append(pages, PagePassphrase, PagePassphraseConfirm)
		}
	}
	if w.TrustedBoot || w.Choice.Encrypt {
		pages = append(pages, PageRecovery)
		if w.Choice.RecoveryKey {
			pages = append(pages, PageRecoveryShow, PageRecoveryConfirm)
		}
	}
	return append(pages, PageReview)
}

// Prev is the page before this one: the page before, skipping the
// encryption pages that were not shown.
func (w *Wizard) Prev() Page {
	switch w.Page {
	case PageReview:
		// Not back to the key: it is shown once, before it is confirmed.
		if w.Choice.Encrypt || w.TrustedBoot {
			return PageRecovery
		}
		return PageEncrypt
	case PageRecovery:
		if w.Choice.Encrypt {
			// The confirmation needs the first entry: start the
			// passphrase over.
			return PagePassphrase
		}
		return PageDisk
	case PageEncrypt:
		return PageDisk
	case PageRecoveryShow:
		return PageRecovery
	}
	return w.Page - 1
}

// Back goes to Prev. It reports false, and stays, on the first page.
func (w *Wizard) Back() bool {
	if w.Page <= PageKeyboard {
		return false
	}
	w.Page = w.Prev()
	return true
}

// SetKeyboard picks Keyboards[i], loads its console keymap on the live
// console, and goes to the timezone.
func (w *Wizard) SetKeyboard(i int) {
	k := w.Keyboards[i]
	w.Choice.KeyboardLayout, w.Choice.KeyboardVariant = k.Layout, k.Variant
	w.Choice.ConsoleKeymap = consoleKeymap(w.KeymapsDir, k.Layout, k.Variant)
	w.ApplyKeymap(w.Choice.ConsoleKeymap)
	w.Page = PageTimezone
}

// SetTimezone picks Zones[i] and goes to the hostname.
func (w *Wizard) SetTimezone(i int) {
	w.Choice.Timezone = w.Zones[i]
	w.Page = PageHostname
}

// HostnameValue is what the hostname field starts with.
func (w *Wizard) HostnameValue() string { return orDefault(w.Choice.Hostname, DefaultHostname) }

// SetHostname takes v, trimmed, or DefaultHostname when it is empty.
func (w *Wizard) SetHostname(v string) {
	w.Choice.Hostname = orDefault(strings.TrimSpace(v), DefaultHostname)
	w.Page = PageUser
}

// SetUsername takes v, trimmed, if it is a valid username.
func (w *Wizard) SetUsername(v string) error {
	v = strings.TrimSpace(v)
	if err := ValidateUsername(v); err != nil {
		return err
	}
	w.Choice.Username = v
	w.Page = PagePass
	return nil
}

// SetPassword takes v, trimmed. It is required.
func (w *Wizard) SetPassword(v string) error {
	v = strings.TrimSpace(v)
	if v == "" {
		return errors.New("password is required")
	}
	w.Choice.Password = v
	w.Page = PageGithub
	return nil
}

// SetGithub takes v, trimmed: empty skips the key import, anything else
// must be a GitHub name. It then goes to the disk, which needs a disk to
// pick: with none, it keeps the name, stays and says so.
func (w *Wizard) SetGithub(v string) error {
	v = strings.TrimSpace(v)
	// Optional, but not arbitrary: an empty value skips key import, a
	// non-empty one must be a real handle before it can reach the renderer.
	if err := ValidateGithub(v); err != nil {
		return err
	}
	w.Choice.Github = v
	if len(w.Disks) == 0 {
		return errors.New("no installable disk found")
	}
	w.Page = PageDisk
	return nil
}

// SetDisk picks Disks[i]. A Trusted Boot medium goes on to the recovery key,
// any other to the encryption question.
func (w *Wizard) SetDisk(i int) {
	w.Choice.Disk = w.Disks[i].Path
	if w.TrustedBoot {
		w.Page = PageRecovery
	} else {
		w.Page = PageEncrypt
	}
}

// SetEncrypt answers the encryption question. No drops the passphrase and
// the recovery key and goes to the review.
func (w *Wizard) SetEncrypt(on bool) {
	if on {
		w.Choice.Encrypt = true
		w.Page = PagePassphrase
		return
	}
	w.Choice.Encrypt = false
	w.Choice.Passphrase = ""
	w.Choice.RecoveryKey = false
	w.Choice.RecoveryKeyValue = ""
	w.Page = PageReview
}

// SetPassphrase takes the first entry of the disk passphrase. It is not
// trimmed: the passphrase is used byte for byte, and ValidatePassphrase
// refuses leading and trailing spaces instead.
func (w *Wizard) SetPassphrase(raw string) error {
	if err := ValidatePassphrase(raw); err != nil {
		return err
	}
	w.passphraseFirst = raw
	w.Page = PagePassphraseConfirm
	return nil
}

// ConfirmPassphrase takes the second entry, which must match the first.
func (w *Wizard) ConfirmPassphrase(raw string) error {
	if raw != w.passphraseFirst {
		return errors.New("the passphrases do not match: type it again, or esc to start over")
	}
	w.Choice.Passphrase = w.passphraseFirst
	w.passphraseFirst = ""
	w.Page = PageRecovery
	return nil
}

// SetRecoveryKey answers the recovery key question. Yes makes a key, once,
// and goes to the page that shows it.
func (w *Wizard) SetRecoveryKey(on bool) error {
	if !on {
		w.Choice.RecoveryKey = false
		w.Choice.RecoveryKeyValue = ""
		w.Page = PageReview
		return nil
	}
	if w.Choice.RecoveryKeyValue == "" {
		k, err := w.NewKey()
		if err != nil {
			return err
		}
		w.Choice.RecoveryKeyValue = k
	}
	w.Choice.RecoveryKey = true
	w.Page = PageRecoveryShow
	return nil
}

// KeySaved leaves the key page: it picks the group to type back.
func (w *Wizard) KeySaved() {
	w.ConfirmGroup = w.PickGroup()
	w.Page = PageRecoveryConfirm
}

// ConfirmRecoveryKey takes the group typed back, or the whole key.
func (w *Wizard) ConfirmRecoveryKey(typed string) error {
	if !confirmsRecoveryKey(w.Choice.RecoveryKeyValue, w.ConfirmGroup, typed) {
		return fmt.Errorf("that is not group %d: check what you wrote down, or esc to see the key again", w.ConfirmGroup+1)
	}
	w.Page = PageReview
	return nil
}

// --- entry point -----------------------------------------------------------

// RunWizard collects the install configuration and writes it to ConfigOut for
// `kairos-agent install` to merge. It is a no-op when a config is already present,
// preserving the shell installer's unattended behaviour for seeded installs.
// It returns what was chosen, or nil when it wrote nothing; the caller needs
// the choice to hand the disk passphrase to kcrypt.
//
// ask is the interactive front end: it is called, only when the
// non-interactive environment is not set, with the wizard for this machine,
// and returns the choice the person confirmed.
func RunWizard(ask func(Wizard) (Choice, error)) (*Choice, error) {
	out := ConfigOut()

	present, err := ConfigPresent(OEMDir(), out)
	if err != nil {
		return nil, err
	}
	if present {
		// Someone already supplied a users/install config: install unattended.
		return nil, nil
	}

	c, nonInteractive, err := choiceFromEnv()
	if err != nil {
		return nil, err
	}

	trustedBoot := trustedBootMedium(ukiInstallMarker)

	if nonInteractive {
		if err := zoneExists(zoneinfoDir, c.Timezone); err != nil {
			return nil, fmt.Errorf("ISOTOPE_TIMEZONE: %w", err)
		}
		if c.Encrypt && trustedBoot {
			return nil, errors.New("ISOTOPE_ENCRYPT: a Trusted Boot install encrypts with its TPM-sealed key; the passphrase method is for the normal edition")
		}
		if os.Getenv("ISOTOPE_RECOVERY_KEY") == "1" {
			return nil, errRecoveryKeyNonInteractive
		}
		c.ConsoleKeymap = consoleKeymap(keymapsDir, c.KeyboardLayout, c.KeyboardVariant)
	} else {
		c, err = ask(NewSystemWizard(trustedBoot))
		if err != nil {
			return nil, err
		}
	}

	hash, err := HashPassword(c.Password)
	if err != nil {
		return nil, err
	}
	if err := writeChoice(out, c, hash); err != nil {
		return nil, err
	}
	return &c, nil
}
