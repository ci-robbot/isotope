package installer

import (
	"bytes"
	"slices"
	"strings"
	"testing"
)

// These drive Wizard directly, as a front end does, with no terminal: the
// rules each page applies and the order of the pages.

// atGithub is a wizard that has taken a keyboard, a timezone, a hostname, a
// username and a password.
func atGithub(t *testing.T, disks []Disk) Wizard {
	t.Helper()
	w := NewWizard(disks)
	w.SetKeyboard(0)
	w.SetTimezone(0)
	w.SetHostname("box")
	if err := w.SetUsername("ada"); err != nil {
		t.Fatal(err)
	}
	if err := w.SetPassword("hunter2"); err != nil {
		t.Fatal(err)
	}
	if w.Page != PageGithub {
		t.Fatalf("page = %v, want PageGithub", w.Page)
	}
	return w
}

// atDisk is a wizard on the disk page.
func atDisk(t *testing.T) Wizard {
	t.Helper()
	w := atGithub(t, testDisks())
	if err := w.SetGithub(""); err != nil {
		t.Fatal(err)
	}
	return w
}

func TestWizardStartsOnTheKeyboardWithDefaults(t *testing.T) {
	w := NewWizard(testDisks())
	if w.Page != PageKeyboard {
		t.Errorf("page = %v, want PageKeyboard", w.Page)
	}
	if w.Choice.KeyboardLayout != DefaultLayout || w.Choice.Timezone != DefaultTimezone {
		t.Errorf("choice = %+v, want the default layout and timezone", w.Choice)
	}
	if w.KeyboardIndex() != 0 || w.TimezoneIndex() != 0 {
		t.Errorf("indexes = %d, %d, want 0, 0", w.KeyboardIndex(), w.TimezoneIndex())
	}
	if w.Back() || w.Page != PageKeyboard {
		t.Error("Back on the first page must stay there")
	}
}

func TestWizardSetKeyboardLoadsTheConsoleKeymap(t *testing.T) {
	dir := t.TempDir()
	writeZone(t, dir, "i386/qwertz/de-latin1-nodeadkeys.map.gz")
	w := NewWizard(testDisks())
	w.KeymapsDir = dir
	var loaded []string
	w.ApplyKeymap = func(k string) { loaded = append(loaded, k) }
	i := slices.IndexFunc(w.Keyboards, func(k Keyboard) bool { return k.Layout == "de" && k.Variant == "nodeadkeys" })
	w.SetKeyboard(i)
	if w.Choice.KeyboardLayout != "de" || w.Choice.KeyboardVariant != "nodeadkeys" || w.Choice.ConsoleKeymap != "de-latin1-nodeadkeys" {
		t.Errorf("choice = %+v", w.Choice)
	}
	if !slices.Equal(loaded, []string{"de-latin1-nodeadkeys"}) {
		t.Errorf("loaded = %v", loaded)
	}
	if w.Page != PageTimezone || w.KeyboardIndex() != i {
		t.Errorf("page = %v, index = %d", w.Page, w.KeyboardIndex())
	}
	w.SetTimezone(slices.Index(w.Zones, "Europe/Rome"))
	if w.Choice.Timezone != "Europe/Rome" || w.Page != PageHostname {
		t.Errorf("timezone = %q, page = %v", w.Choice.Timezone, w.Page)
	}
}

func TestWizardTextPages(t *testing.T) {
	w := NewWizard(testDisks())
	w.Page = PageHostname
	if w.HostnameValue() != DefaultHostname {
		t.Errorf("hostname field starts with %q", w.HostnameValue())
	}
	w.SetHostname("   ")
	if w.Choice.Hostname != DefaultHostname || w.Page != PageUser {
		t.Errorf("empty hostname: %q, page %v", w.Choice.Hostname, w.Page)
	}

	if err := w.SetUsername("Ada"); err == nil || err.Error() != usernameRule || w.Page != PageUser || w.Choice.Username != "" {
		t.Errorf("an invalid username: err %v, page %v, choice %+v", err, w.Page, w.Choice)
	}
	if err := w.SetUsername(" ada "); err != nil || w.Choice.Username != "ada" || w.Page != PagePass {
		t.Errorf("a valid username: err %v, choice %q", err, w.Choice.Username)
	}

	if err := w.SetPassword(" "); err == nil || err.Error() != "password is required" || w.Page != PagePass {
		t.Errorf("an empty password: err %v, page %v", err, w.Page)
	}
	if err := w.SetPassword("hunter2"); err != nil || w.Page != PageGithub {
		t.Errorf("a password: err %v, page %v", err, w.Page)
	}

	if err := w.SetGithub("-ada"); err == nil || err.Error() != githubRule || w.Choice.Github != "" {
		t.Errorf("an invalid GitHub name: err %v, choice %q", err, w.Choice.Github)
	}
	if err := w.SetGithub("adalovelace"); err != nil || w.Choice.Github != "adalovelace" || w.Page != PageDisk {
		t.Errorf("a GitHub name: err %v, page %v", err, w.Page)
	}
}

// With no disk there is no next page: the wizard keeps the name and stays.
func TestWizardNoDiskStaysOnGithub(t *testing.T) {
	w := atGithub(t, nil)
	err := w.SetGithub("adalovelace")
	if err == nil || err.Error() != "no installable disk found" || w.Page != PageGithub {
		t.Errorf("err %v, page %v", err, w.Page)
	}
}

func TestWizardPagesWithoutEncryption(t *testing.T) {
	w := atDisk(t)
	w.SetDisk(1)
	if w.Choice.Disk != "/dev/vdb" || w.Page != PageEncrypt {
		t.Fatalf("disk %q, page %v", w.Choice.Disk, w.Page)
	}
	w.SetEncrypt(false)
	if w.Page != PageReview {
		t.Fatalf("page = %v, want PageReview", w.Page)
	}
	want := []Page{PageKeyboard, PageTimezone, PageHostname, PageUser, PagePass, PageGithub, PageDisk, PageEncrypt, PageReview}
	if got := w.Pages(); !slices.Equal(got, want) {
		t.Errorf("pages = %v, want %v", got, want)
	}
	for _, back := range []Page{PageEncrypt, PageDisk, PageGithub} {
		if !w.Back() || w.Page != back {
			t.Fatalf("back to %v, want %v", w.Page, back)
		}
	}
}

func TestWizardPassphrasePages(t *testing.T) {
	w := atDisk(t)
	key, _ := GenerateRecoveryKey(bytes.NewReader(bytes.Repeat([]byte{0x9a, 0xbc}, 16)))
	w.NewKey = func() (string, error) { return key, nil }
	w.PickGroup = func() int { return 7 }
	w.SetDisk(0)
	w.SetEncrypt(true)
	if w.Page != PagePassphrase || !w.Choice.Encrypt {
		t.Fatalf("page %v, choice %+v", w.Page, w.Choice)
	}
	if err := w.SetPassphrase("short"); err == nil || w.Page != PagePassphrase {
		t.Fatalf("a short passphrase: err %v, page %v", err, w.Page)
	}
	if err := w.SetPassphrase(testPassphrase); err != nil || w.Page != PagePassphraseConfirm {
		t.Fatalf("err %v, page %v", err, w.Page)
	}
	err := w.ConfirmPassphrase("correct horse battery stapler")
	if err == nil || !strings.Contains(err.Error(), "do not match") || w.Choice.Passphrase != "" || w.Page != PagePassphraseConfirm {
		t.Fatalf("a mismatch: err %v, page %v, choice %+v", err, w.Page, w.Choice)
	}
	if err := w.ConfirmPassphrase(testPassphrase); err != nil || w.Choice.Passphrase != testPassphrase || w.Page != PageRecovery {
		t.Fatalf("err %v, page %v", err, w.Page)
	}
	if err := w.SetRecoveryKey(true); err != nil || w.Page != PageRecoveryShow || w.Choice.RecoveryKeyValue != key {
		t.Fatalf("err %v, page %v", err, w.Page)
	}
	w.KeySaved()
	if w.Page != PageRecoveryConfirm || w.ConfirmGroup != 7 {
		t.Fatalf("page %v, group %d", w.Page, w.ConfirmGroup)
	}
	if err := w.ConfirmRecoveryKey("cccccccc"); err == nil || err.Error() != "that is not group 8: check what you wrote down, or esc to see the key again" {
		t.Errorf("a wrong group: %v", err)
	}
	if err := w.ConfirmRecoveryKey(RecoveryKeyGroups(key)[7]); err != nil || w.Page != PageReview {
		t.Fatalf("err %v, page %v", err, w.Page)
	}
	want := []Page{PageKeyboard, PageTimezone, PageHostname, PageUser, PagePass, PageGithub, PageDisk, PageEncrypt,
		PagePassphrase, PagePassphraseConfirm, PageRecovery, PageRecoveryShow, PageRecoveryConfirm, PageReview}
	if got := w.Pages(); !slices.Equal(got, want) {
		t.Errorf("pages = %v, want %v", got, want)
	}
	// Back from the review skips the key, and from the recovery question
	// starts the passphrase over.
	for _, back := range []Page{PageRecovery, PagePassphrase} {
		if w.Back(); w.Page != back {
			t.Fatalf("back to %v, want %v", w.Page, back)
		}
	}
	// No to encryption drops the passphrase and the key.
	w.Page = PageEncrypt
	w.SetEncrypt(false)
	if w.Choice.Encrypt || w.Choice.Passphrase != "" || w.Choice.RecoveryKey || w.Choice.RecoveryKeyValue != "" {
		t.Errorf("choice = %+v", w.Choice)
	}
}

func TestWizardTrustedBootPages(t *testing.T) {
	w := atDisk(t)
	w.TrustedBoot = true
	w.SetDisk(0)
	if w.Page != PageRecovery {
		t.Fatalf("page = %v, want PageRecovery", w.Page)
	}
	if err := w.SetRecoveryKey(false); err != nil || w.Page != PageReview || w.Choice.RecoveryKey {
		t.Fatalf("err %v, page %v", err, w.Page)
	}
	want := []Page{PageKeyboard, PageTimezone, PageHostname, PageUser, PagePass, PageGithub, PageDisk, PageRecovery, PageReview}
	if got := w.Pages(); !slices.Equal(got, want) {
		t.Errorf("pages = %v, want %v", got, want)
	}
	for _, back := range []Page{PageRecovery, PageDisk} {
		if w.Back(); w.Page != back {
			t.Fatalf("back to %v, want %v", w.Page, back)
		}
	}
}
