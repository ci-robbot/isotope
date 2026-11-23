package installer

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestChoiceFromEnvRequiresMandatoryFields(t *testing.T) {
	t.Setenv("ISOTOPE_INSTALL_NONINTERACTIVE", "1")
	t.Setenv("ISOTOPE_USER", "")
	t.Setenv("ISOTOPE_PASS", "")
	t.Setenv("ISOTOPE_DISK", "")
	if _, _, err := choiceFromEnv(); err == nil {
		t.Fatal("expected an error when ISOTOPE_USER/PASS/DISK are unset")
	}
}

// TestChoiceFromEnvRequiresEachFieldIndividually: checking only the all-empty
// case cannot tell that all three are enforced — with two checks in place the
// third could be dropped and the suite would stay green. An unenforced
// ISOTOPE_DISK in particular would install to device "".
func TestChoiceFromEnvRequiresEachFieldIndividually(t *testing.T) {
	full := map[string]string{
		"ISOTOPE_USER": "ada",
		"ISOTOPE_PASS": "hunter2",
		"ISOTOPE_DISK": "/dev/vda",
	}
	for blank := range full {
		t.Run(blank, func(t *testing.T) {
			t.Setenv("ISOTOPE_INSTALL_NONINTERACTIVE", "1")
			for k, v := range full {
				if k == blank {
					v = ""
				}
				t.Setenv(k, v)
			}
			_, _, err := choiceFromEnv()
			if err == nil {
				t.Fatalf("expected an error when %s is unset", blank)
			}
			if !strings.Contains(err.Error(), blank) {
				t.Errorf("error %q does not name the missing variable %s", err, blank)
			}
		})
	}
}

func TestChoiceFromEnvPopulatesAndDefaultsHostname(t *testing.T) {
	t.Setenv("ISOTOPE_INSTALL_NONINTERACTIVE", "1")
	t.Setenv("ISOTOPE_USER", "ada")
	t.Setenv("ISOTOPE_PASS", "hunter2")
	t.Setenv("ISOTOPE_DISK", "/dev/vda")
	t.Setenv("ISOTOPE_HOSTNAME", "")
	t.Setenv("ISOTOPE_GITHUB", "adalovelace")

	c, ok, err := choiceFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("expected non-interactive mode to be active")
	}
	if c.Username != "ada" || c.Password != "hunter2" || c.Disk != "/dev/vda" {
		t.Errorf("unexpected choice: %+v", c)
	}
	if c.Hostname != "isotope" {
		t.Errorf("Hostname = %q, want default %q", c.Hostname, "isotope")
	}
	if c.Github != "adalovelace" {
		t.Errorf("Github = %q, want adalovelace", c.Github)
	}
}

func TestChoiceFromEnvInactiveWhenFlagUnset(t *testing.T) {
	t.Setenv("ISOTOPE_INSTALL_NONINTERACTIVE", "0")
	if _, ok, _ := choiceFromEnv(); ok {
		t.Error("non-interactive mode should be inactive when the flag is not 1")
	}
}

// TestChoiceFromEnvRejectsInvalidUsername guards the one interpolation
// RenderCloudConfig cannot defend: Username goes raw into the /etc/ly/save.ini
// block scalar, so a newline there terminates the YAML block and yields an
// invalid cloud-config on a machine that has already been wiped.
func TestChoiceFromEnvRejectsInvalidUsername(t *testing.T) {
	t.Setenv("ISOTOPE_INSTALL_NONINTERACTIVE", "1")
	t.Setenv("ISOTOPE_PASS", "hunter2")
	t.Setenv("ISOTOPE_DISK", "/dev/vda")
	t.Setenv("ISOTOPE_USER", "ada\nsession_index = 9")
	if _, _, err := choiceFromEnv(); err == nil {
		t.Fatal("expected an error for a username containing a newline")
	}
}

// topLevelStages counts unindented `stages:` keys in a rendered cloud-config.
// A second one is the signature of a successful injection.
func topLevelStages(cfg string) int {
	var n int
	for _, line := range strings.Split(cfg, "\n") {
		if line == "stages:" {
			n++
		}
	}
	return n
}

// TestChoiceFromEnvRejectsInjectedGithub guards the worse half of the same
// defect: Github goes into an *unquoted* YAML scalar, so this payload does not
// corrupt the document, it extends it — the rendered config ends up with two
// top-level `stages:` keys and the injected one displaces /etc/ly/save.ini.
// Valid YAML, wrong machine, disk already wiped.
func TestChoiceFromEnvRejectsInjectedGithub(t *testing.T) {
	t.Setenv("ISOTOPE_INSTALL_NONINTERACTIVE", "1")
	t.Setenv("ISOTOPE_USER", "ada")
	t.Setenv("ISOTOPE_PASS", "hunter2")
	t.Setenv("ISOTOPE_DISK", "/dev/vda")
	t.Setenv("ISOTOPE_GITHUB", "ada\nstages:\n  network:\n    - name: pwn")

	_, _, err := choiceFromEnv()
	if err == nil {
		t.Fatal("expected an error for a ISOTOPE_GITHUB containing a newline")
	}
	if !strings.Contains(err.Error(), "ISOTOPE_GITHUB") {
		t.Errorf("error %q does not name the offending variable", err)
	}
}

// TestChoiceFromEnvGithubValidity pins both edges at once: a rejection-only
// test would pass with the field hardwired to always fail, and an
// acceptance-only test would pass with no validation at all. The empty case is
// load-bearing — ISOTOPE_GITHUB is optional and must stay skippable.
func TestChoiceFromEnvGithubValidity(t *testing.T) {
	cases := []struct {
		name  string
		value string
		ok    bool
	}{
		{"ordinary handle", "adalovelace", true},
		{"hyphen and digits", "ada-l0velace", true},
		{"empty stays optional", "", true},
		{"39 chars", strings.Repeat("a", 39), true},
		{"newline injects a stages block", "ada\nstages:\n  network:\n    - name: pwn", false},
		{"leading hyphen", "-ada", false},
		{"trailing hyphen", "ada-", false},
		{"space", "ada lovelace", false},
		{"underscore", "ada_lovelace", false},
		{"colon", "ada:x", false},
		{"40 chars", strings.Repeat("a", 40), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("ISOTOPE_INSTALL_NONINTERACTIVE", "1")
			t.Setenv("ISOTOPE_USER", "ada")
			t.Setenv("ISOTOPE_PASS", "hunter2")
			t.Setenv("ISOTOPE_DISK", "/dev/vda")
			t.Setenv("ISOTOPE_GITHUB", tc.value)

			c, _, err := choiceFromEnv()
			if tc.ok {
				if err != nil {
					t.Fatalf("ISOTOPE_GITHUB=%q rejected: %v", tc.value, err)
				}
				if c.Github != tc.value {
					t.Errorf("Github = %q, want %q", c.Github, tc.value)
				}
				// The point of the check is the rendered document: whatever
				// survives validation must still yield exactly one top-level
				// `stages:` key, i.e. the one the installer wrote.
				if n := topLevelStages(RenderCloudConfig(c, "$6$fake")); n != 1 {
					t.Errorf("rendered config has %d top-level stages: keys, want 1", n)
				}
				return
			}
			if err == nil {
				t.Fatalf("ISOTOPE_GITHUB=%q accepted, rendered config:\n%s",
					tc.value, RenderCloudConfig(c, "$6$fake"))
			}
		})
	}
}

func TestWriteChoiceProducesReadableConfig(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "99_isotope-user.yaml")
	c := Choice{Hostname: "box", Username: "ada", Disk: "/dev/vda"}

	if err := writeChoice(out, c, "$6$fake"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "hostname: \"box\"") {
		t.Errorf("config missing hostname:\n%s", data)
	}
	// The file carries a password hash; it must not be world-readable.
	info, err := os.Stat(out)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm&0o077 != 0 {
		t.Errorf("permissions = %o, want no group/other access", perm)
	}
}

// TestWriteChoiceMatchesRenderCloudConfig pins that writeChoice writes the
// renderer's bytes unmodified — the shell installer's output shape is verified
// against RenderCloudConfig in config_test.go, and this is the only step
// between it and the disk.
func TestWriteChoiceMatchesRenderCloudConfig(t *testing.T) {
	out := filepath.Join(t.TempDir(), "nested", "99_isotope-user.yaml")
	c := Choice{Hostname: "box", Username: "ada", Github: "adalovelace", Disk: "/dev/vda"}

	if err := writeChoice(out, c, "$6$fake"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if want := RenderCloudConfig(c, "$6$fake"); string(data) != want {
		t.Errorf("written config differs from RenderCloudConfig\n got:\n%s\nwant:\n%s", data, want)
	}
}

// --- wizard flow -----------------------------------------------------------

func testDisks() []Disk {
	return []Disk{{Path: "/dev/sda", SizeGB: 120}, {Path: "/dev/vdb", SizeGB: 500}}
}

// TestWizardGithubPayloadIsRejectedAtTheFunction pins the raw payload against
// ValidateGithub directly, because the interactive test of the text front end
// (TestWizardGithubValidation in package main) cannot: bubbles' textinput
// rewrites newlines to spaces before commitField ever sees the value
// (measured — "ada\nstages:..." arrives as "ada stages:..."). So the widget's
// sanitisation, not our check, is what stops a *typed* newline today, and that
// is a third-party implementation detail we do not control. This asserts the
// check would hold on its own if textinput ever stopped scrubbing.
func TestWizardGithubPayloadIsRejectedAtTheFunction(t *testing.T) {
	if err := ValidateGithub("ada\nstages:\n  network:\n    - name: pwn"); err == nil {
		t.Fatal("ValidateGithub accepted a newline-injected handle")
	}
	if err := ValidateGithub(""); err != nil {
		t.Errorf("ValidateGithub rejected the empty (optional) value: %v", err)
	}
	if err := ValidateGithub("adalovelace"); err != nil {
		t.Errorf("ValidateGithub rejected an ordinary handle: %v", err)
	}
}

// --- RunWizard end to end --------------------------------------------------

func TestRunWizardNonInteractiveWritesConfig(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("ISOTOPE_OEM_DIR", dir)
	t.Setenv("ISOTOPE_INSTALL_NONINTERACTIVE", "1")
	t.Setenv("ISOTOPE_USER", "ada")
	t.Setenv("ISOTOPE_PASS", "hunter2")
	t.Setenv("ISOTOPE_DISK", "/dev/vda")
	t.Setenv("ISOTOPE_HOSTNAME", "box")
	t.Setenv("ISOTOPE_GITHUB", "adalovelace")

	if _, err := RunWizard(noAsk(t)); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "99_isotope-user.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	got := string(data)
	for _, want := range []string{"#cloud-config", "hostname: \"box\"", "name: \"ada\"", "$6$", "github:adalovelace", "/etc/ly/save.ini"} {
		if !strings.Contains(got, want) {
			t.Errorf("config missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "hunter2") {
		t.Errorf("plaintext password reached disk:\n%s", got)
	}
}

// TestRunWizardSkipsWhenConfigPresent preserves the shell installer's unattended
// behaviour: a seeded /oem must install without prompting. If this regressed the
// wizard would block a seeded install forever on a headless machine.
func TestRunWizardSkipsWhenConfigPresent(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "10_seed.yaml"), []byte("users:\n  - name: seeded\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ISOTOPE_OEM_DIR", dir)
	// Non-interactive mode is deliberately NOT set: if the short-circuit failed,
	// runWizard would try to open a terminal here rather than return nil.
	t.Setenv("ISOTOPE_INSTALL_NONINTERACTIVE", "")

	if _, err := RunWizard(noAsk(t)); err != nil {
		t.Fatalf("seeded /oem should install unattended, got: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "99_isotope-user.yaml")); !os.IsNotExist(err) {
		t.Errorf("wizard wrote its own config despite a seeded one being present (stat err: %v)", err)
	}
}

func TestRunWizardReportsMissingEnv(t *testing.T) {
	t.Setenv("ISOTOPE_OEM_DIR", t.TempDir())
	t.Setenv("ISOTOPE_INSTALL_NONINTERACTIVE", "1")
	t.Setenv("ISOTOPE_USER", "")
	t.Setenv("ISOTOPE_PASS", "")
	t.Setenv("ISOTOPE_DISK", "")
	_, err := RunWizard(noAsk(t))
	if err == nil {
		t.Fatal("expected an error when the non-interactive env is incomplete")
	}
	if !strings.Contains(err.Error(), "ISOTOPE_USER") {
		t.Errorf("error %q does not name the missing variable", err)
	}
}

func TestOemDirDefaultsToOem(t *testing.T) {
	t.Setenv("ISOTOPE_OEM_DIR", "")
	if got := OEMDir(); got != "/oem" {
		t.Errorf("oemDir() = %q, want /oem", got)
	}
	t.Setenv("ISOTOPE_OEM_DIR", "/tmp/fake-oem")
	if got := OEMDir(); got != "/tmp/fake-oem" {
		t.Errorf("oemDir() = %q, want /tmp/fake-oem", got)
	}
}

func TestConfigOutFollowsOemDirUnlessSet(t *testing.T) {
	t.Setenv("ISOTOPE_OEM_DIR", "/tmp/fake-oem")
	t.Setenv("ISOTOPE_CONFIG_OUT", "")
	if got := ConfigOut(); got != "/tmp/fake-oem/99_isotope-user.yaml" {
		t.Errorf("configOut() = %q, want /tmp/fake-oem/99_isotope-user.yaml", got)
	}
	t.Setenv("ISOTOPE_CONFIG_OUT", "/etc/kairos/99_isotope-user.yaml")
	if got := ConfigOut(); got != "/etc/kairos/99_isotope-user.yaml" {
		t.Errorf("configOut() = %q, want /etc/kairos/99_isotope-user.yaml", got)
	}
}

// noAsk is the interactive front end of a test that must not need one.
func noAsk(t *testing.T) func(Wizard) (Choice, error) {
	return func(Wizard) (Choice, error) {
		t.Fatal("the interactive wizard was asked for")
		return Choice{}, nil
	}
}
