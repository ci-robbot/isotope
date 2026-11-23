package main

import (
	"strings"
	"testing"

	"github.com/kairos-community/isotope/pkg/installer"
)

const testPassphrase = "correct horse battery staple"

// --- wizard ----------------------------------------------------------------

// atDisk is the wizard on the disk step.
func atDisk(t *testing.T) wizardModel {
	t.Helper()
	m := atHostname(t, testDisks())
	m = keys(t, m, "box", "enter", "ada", "enter", "hunter2", "enter", "enter")
	if m.Page != installer.PageDisk {
		t.Fatalf("step = %v, want installer.PageDisk", m.Page)
	}
	return m
}

// The step is on every machine: it needs no TPM.
func TestWizardEncryptionDefaultsToOff(t *testing.T) {
	m := keys(t, atDisk(t), "enter")
	if m.Page != installer.PageEncrypt {
		t.Fatalf("step = %v, want installer.PageEncrypt", m.Page)
	}
	v := m.View()
	for _, want := range []string{"Encrypt your files?", "> no, do not encrypt", "every time it starts",
		"stolen", "If you forget the passphrase, your files are gone"} {
		if !strings.Contains(v, want) {
			t.Errorf("encryption step is missing %q:\n%s", want, v)
		}
	}
	if strings.Contains(v, "TPM") {
		t.Errorf("encryption step still talks about a TPM:\n%s", v)
	}
	if n := len(strings.Split(v, "\n")); n > 25 {
		t.Errorf("encryption step is %d lines, more than a 25-row console", n)
	}
	m = keys(t, m, "enter")
	if m.Page != installer.PageReview || m.Choice.Encrypt {
		t.Fatalf("enter on the default = step %v, encrypt %v; want review, off", m.Page, m.Choice.Encrypt)
	}
	if !strings.Contains(m.View(), "encrypt    off") {
		t.Errorf("review does not say encryption is off:\n%s", m.View())
	}
	if m = keys(t, m, "esc"); m.Page != installer.PageEncrypt {
		t.Errorf("esc from review = %v, want installer.PageEncrypt", m.Page)
	}
	if m = keys(t, m, "esc"); m.Page != installer.PageDisk {
		t.Errorf("esc from encryption = %v, want installer.PageDisk", m.Page)
	}
}

func TestWizardDiskPassphrase(t *testing.T) {
	m := keys(t, atDisk(t), "enter", "down", "enter")
	if m.Page != installer.PagePassphrase || !m.Choice.Encrypt {
		t.Fatalf("step = %v encrypt = %v, want installer.PagePassphrase, on", m.Page, m.Choice.Encrypt)
	}
	if !strings.Contains(m.View(), "12 characters or more") {
		t.Errorf("passphrase step does not state the minimum:\n%s", m.View())
	}

	m = keys(t, m, "short", "enter")
	if m.Page != installer.PagePassphrase || m.err == "" {
		t.Fatalf("a short passphrase was accepted (step %v)", m.Page)
	}
	if !strings.Contains(m.View(), "strength: too short") {
		t.Errorf("no strength hint:\n%s", m.View())
	}
	if strings.Contains(m.in.View(), "short") {
		t.Errorf("the passphrase is echoed:\n%s", m.in.View())
	}

	m = clear(t, m, testPassphrase)
	if !strings.Contains(m.View(), "strength: strong") {
		t.Errorf("no strong hint for a four-word passphrase:\n%s", m.View())
	}
	m = keys(t, m, "enter")
	if m.Page != installer.PagePassphraseConfirm {
		t.Fatalf("step = %v, want installer.PagePassphraseConfirm", m.Page)
	}
	if m.in.Value() != "" {
		t.Error("the confirmation field must start empty")
	}

	m = keys(t, m, "correct horse battery stapler", "enter")
	if m.Page != installer.PagePassphraseConfirm || !strings.Contains(m.err, "do not match") {
		t.Fatalf("a mismatch was accepted: step %v err %q", m.Page, m.err)
	}
	if m.Choice.Passphrase != "" {
		t.Error("a mismatched passphrase reached the choice")
	}

	m = keys(t, m, testPassphrase, "enter")
	// Then the recovery key question, off by default.
	if m.Page != installer.PageRecovery || !strings.Contains(m.View(), "Also create a recovery key?") {
		t.Fatalf("step = %v, want installer.PageRecovery", m.Page)
	}
	m = keys(t, m, "enter")
	if m.Page != installer.PageReview {
		t.Fatalf("step = %v, want installer.PageReview", m.Page)
	}
	if m.Choice.Passphrase != testPassphrase || !m.Choice.Encrypt {
		t.Errorf("choice = %+v", m.Choice)
	}
	if v := m.View(); !strings.Contains(v, "on (passphrase at every start)") || strings.Contains(v, testPassphrase) {
		t.Errorf("review is wrong or shows the passphrase:\n%s", v)
	}

	// Back from review goes to the recovery key question, then the
	// passphrase from its first entry.
	if m = keys(t, m, "esc"); m.Page != installer.PageRecovery {
		t.Errorf("esc from review = %v, want installer.PageRecovery", m.Page)
	}
	if m = keys(t, m, "esc"); m.Page != installer.PagePassphrase {
		t.Errorf("esc from the recovery key = %v, want installer.PagePassphrase", m.Page)
	}

	// Turning encryption off again drops the passphrase.
	m = keys(t, m, "esc")
	if m.Page != installer.PageEncrypt {
		t.Fatalf("step = %v, want installer.PageEncrypt", m.Page)
	}
	m = keys(t, m, "up", "enter")
	if m.Choice.Encrypt || m.Choice.Passphrase != "" {
		t.Errorf("choice after turning encryption off = %+v", m.Choice)
	}
}

// A Trusted Boot install medium encrypts with its TPM-sealed key (#61): the
// passphrase step is not offered there.
func TestWizardHidesEncryptionOnTrustedBoot(t *testing.T) {
	m := atDisk(t)
	m.TrustedBoot = true
	// The disk leads to the recovery key step, not to the passphrase.
	m = keys(t, m, "enter")
	if m.Page != installer.PageRecovery {
		t.Fatalf("step = %v, want installer.PageRecovery", m.Page)
	}
	m = keys(t, m, "enter")
	if m.Page != installer.PageReview {
		t.Fatalf("step = %v, want installer.PageReview", m.Page)
	}
	if strings.Contains(m.View(), "encrypt") {
		t.Errorf("review mentions the passphrase choice on Trusted Boot:\n%s", m.View())
	}
	if m = keys(t, m, "esc"); m.Page != installer.PageRecovery {
		t.Errorf("esc from review = %v, want installer.PageRecovery", m.Page)
	}
	if m = keys(t, m, "esc"); m.Page != installer.PageDisk {
		t.Errorf("esc from the recovery key step = %v, want installer.PageDisk", m.Page)
	}
}
