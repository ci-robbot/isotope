package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/kairos-community/isotope/pkg/installer"
)

// keyedWizard is at the recovery key step of a Trusted Boot install, with a
// known key and group.
func keyedWizard(t *testing.T) (wizardModel, string) {
	t.Helper()
	key, _ := installer.GenerateRecoveryKey(bytes.NewReader(bytes.Repeat([]byte{0xab, 0xcd}, 16)))
	m := atDisk(t)
	m.TrustedBoot = true
	m.NewKey = func() (string, error) { return key, nil }
	m.PickGroup = func() int { return 2 }
	return keys(t, m, "enter"), key
}

func TestWizardRecoveryKeyDefaultsToOff(t *testing.T) {
	m, _ := keyedWizard(t)
	if m.Page != installer.PageRecovery {
		t.Fatalf("step = %v, want installer.PageRecovery", m.Page)
	}
	m = keys(t, m, "enter")
	if m.Page != installer.PageReview || m.Choice.RecoveryKey || m.Choice.RecoveryKeyValue != "" {
		t.Fatalf("enter on the default: step %v, choice %+v", m.Page, m.Choice)
	}
	cfg := installer.RenderCloudConfig(m.Choice, "$6$hash")
	if strings.Contains(cfg, "kcrypt:") || strings.Contains(cfg, "passphrase_fallback") || !strings.Contains(cfg, "reboot: true") {
		t.Errorf("a TPM-only install must not configure the fallback:\n%s", cfg)
	}
}

func TestWizardRecoveryKeyFlow(t *testing.T) {
	m, key := keyedWizard(t)
	m = keys(t, m, "down", "enter")
	if m.Page != installer.PageRecoveryShow {
		t.Fatalf("step = %v, want installer.PageRecoveryShow", m.Page)
	}
	if !strings.Contains(m.View(), key) {
		t.Fatal("the key screen does not show the key")
	}
	m = keys(t, m, "enter")
	if m.Page != installer.PageRecoveryConfirm || !strings.Contains(m.View(), "group 3 of 8") {
		t.Fatalf("step = %v, view:\n%s", m.Page, m.View())
	}
	m = keys(t, m, "cccccccc", "enter")
	if m.Page != installer.PageRecoveryConfirm || m.err == "" {
		t.Fatalf("a wrong group was accepted: step %v", m.Page)
	}
	m = keys(t, m, installer.RecoveryKeyGroups(key)[2], "enter")
	if m.Page != installer.PageReview {
		t.Fatalf("the right group: step = %v, err %q", m.Page, m.err)
	}
	if !m.Choice.RecoveryKey || m.Choice.RecoveryKeyValue != key {
		t.Fatalf("choice = %+v", m.Choice)
	}
	if strings.Contains(m.View(), key) {
		t.Error("the review shows the key again")
	}
	// Back from the review goes to the yes/no step, not to the key.
	if m = keys(t, m, "esc"); m.Page != installer.PageRecovery {
		t.Errorf("esc from review = %v, want installer.PageRecovery", m.Page)
	}

	cfg := installer.RenderCloudConfig(m.Choice, "$6$hash")
	for _, g := range installer.RecoveryKeyGroups(key) {
		if strings.Contains(cfg, g) {
			t.Fatalf("the cloud-config, which goes to OEM, carries the key:\n%s", cfg)
		}
	}
	for _, want := range []string{"reboot: false", "passphrase_fallback: true", "mode=tpm-recovery-key"} {
		if !strings.Contains(cfg, want) {
			t.Errorf("cloud-config lacks %q:\n%s", want, cfg)
		}
	}
	if strings.Contains(cfg, "method:") || strings.Contains(cfg, "encrypted_partitions") {
		t.Errorf("the recovery key must not change how Trusted Boot encrypts:\n%s", cfg)
	}
}

// The normal edition: after the disk passphrase, "Also create a recovery
// key?" and the same key screen and check.
func TestWizardRecoveryKeyAfterThePassphrase(t *testing.T) {
	key, _ := installer.GenerateRecoveryKey(bytes.NewReader(bytes.Repeat([]byte{0x9a, 0xbc}, 16)))
	m := atDisk(t)
	m.NewKey = func() (string, error) { return key, nil }
	m.PickGroup = func() int { return 7 }
	m = keys(t, m, "enter", "down", "enter", testPassphrase, "enter", testPassphrase, "enter")
	if m.Page != installer.PageRecovery {
		t.Fatalf("step = %v, want installer.PageRecovery", m.Page)
	}
	if v := m.View(); !strings.Contains(v, "Also create a recovery key?") || !strings.Contains(v, "forget the passphrase") {
		t.Fatalf("view:\n%s", v)
	}
	m = keys(t, m, "down", "enter")
	if m.Page != installer.PageRecoveryShow || !strings.Contains(m.View(), "7  "+spaced(installer.RecoveryKeyGroups(key)[6])) {
		t.Fatalf("key screen:\n%s", m.View())
	}
	m = keys(t, m, "enter", installer.RecoveryKeyGroups(key)[7], "enter")
	if m.Page != installer.PageReview || !m.Choice.RecoveryKey || !m.Choice.Encrypt {
		t.Fatalf("step %v, choice %+v", m.Page, m.Choice)
	}
	cfg := installer.RenderCloudConfig(m.Choice, "$6$hash")
	if strings.Contains(cfg, installer.RecoveryKeyGroups(key)[0]) || !strings.Contains(cfg, "method: passphrase") {
		t.Errorf("cloud-config:\n%s", cfg)
	}
	// Turning encryption off drops the key too.
	m = keys(t, m, "esc", "esc", "esc", "up", "enter")
	if m.Choice.Encrypt || m.Choice.RecoveryKey || m.Choice.RecoveryKeyValue != "" {
		t.Errorf("choice after turning encryption off = %+v", m.Choice)
	}
}

// spaced is how the key screen spells a group: a space between letters.
func spaced(s string) string { return strings.Join(strings.Split(s, ""), " ") }
