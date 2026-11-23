package installer

import (
	"strings"
	"testing"
)

// --- keyboard and timezone steps ---------------------------------------------

// A detected zone that the list does not have must not become the choice:
// the user would see the cursor on UTC and get something else.
func TestNewWizardWithIgnoresAnUnlistedDetectedZone(t *testing.T) {
	m := NewWizardWith(testDisks(), CuratedKeyboards, []string{"UTC", "Europe/Rome"}, "Mars/Olympus")
	if m.Choice.Timezone != "UTC" {
		t.Errorf("timezone = %q, want UTC", m.Choice.Timezone)
	}
	m = NewWizardWith(testDisks(), CuratedKeyboards, []string{"UTC", "Europe/Rome"}, "Europe/Rome")
	if m.Choice.Timezone != "Europe/Rome" {
		t.Errorf("timezone = %q, want the detected Europe/Rome", m.Choice.Timezone)
	}
}

func TestChoiceFromEnvLocaleDefaultsAndValidation(t *testing.T) {
	t.Setenv("ISOTOPE_INSTALL_NONINTERACTIVE", "1")
	t.Setenv("ISOTOPE_USER", "ada")
	t.Setenv("ISOTOPE_PASS", "hunter2")
	t.Setenv("ISOTOPE_DISK", "/dev/vda")
	t.Setenv("ISOTOPE_KEYBOARD_LAYOUT", "")
	t.Setenv("ISOTOPE_KEYBOARD_VARIANT", "")
	t.Setenv("ISOTOPE_TIMEZONE", "")
	c, _, err := choiceFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if c.KeyboardLayout != "us" || c.KeyboardVariant != "" || c.Timezone != "UTC" {
		t.Errorf("defaults = %q/%q/%q, want us, no variant, UTC", c.KeyboardLayout, c.KeyboardVariant, c.Timezone)
	}

	t.Setenv("ISOTOPE_KEYBOARD_LAYOUT", "de")
	t.Setenv("ISOTOPE_KEYBOARD_VARIANT", "nodeadkeys")
	t.Setenv("ISOTOPE_TIMEZONE", "Europe/Berlin")
	c, _, err = choiceFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if c.KeyboardLayout != "de" || c.KeyboardVariant != "nodeadkeys" || c.Timezone != "Europe/Berlin" {
		t.Errorf("choice = %+v", c)
	}

	for _, tc := range []struct{ env, value string }{
		{"ISOTOPE_KEYBOARD_LAYOUT", "de\nstages:"},
		{"ISOTOPE_KEYBOARD_VARIANT", "a b"},
		{"ISOTOPE_TIMEZONE", "../../etc/shadow"},
	} {
		t.Run(tc.env, func(t *testing.T) {
			t.Setenv(tc.env, tc.value)
			if _, _, err := choiceFromEnv(); err == nil || !strings.Contains(err.Error(), tc.env) {
				t.Errorf("err = %v, want an error naming %s", err, tc.env)
			}
		})
	}
}

// --- rendering -----------------------------------------------------------------

func TestRenderCloudConfigWritesKeyboardAndTimezone(t *testing.T) {
	c := Choice{Hostname: "h", Username: "u", Disk: "/dev/sda",
		KeyboardLayout: "de", KeyboardVariant: "nodeadkeys", ConsoleKeymap: "de-latin1-nodeadkeys", Timezone: "Europe/Rome"}
	out := RenderCloudConfig(c, "x")
	for _, want := range []string{
		"        - path: /etc/isotope/keyboard\n",
		"            XKB_LAYOUT=de\n            XKB_VARIANT=nodeadkeys\n",
		"        - path: /etc/X11/xorg.conf.d/00-keyboard.conf\n",
		"                Option \"XkbLayout\" \"de\"\n",
		"                Option \"XkbVariant\" \"nodeadkeys\"\n",
		"        - \"ln -sfn /usr/share/zoneinfo/Europe/Rome /etc/localtime\"\n",
		"echo 'KEYMAP=de-latin1-nodeadkeys' >> /etc/vconsole.conf",
		// The initramfs, where the disk passphrase is typed, never sees that
		// vconsole.conf; it gets the keymap from the command line.
		"  grub_options:\n    extra_cmdline: \"rd.vconsole.keymap=de-latin1-nodeadkeys\"\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("rendered config missing %q\n---\n%s", want, out)
		}
	}
	// In the initramfs stage, like save.ini, so /etc has it before
	// systemd-vconsole-setup and the display manager start.
	kb, boot := strings.Index(out, "/etc/isotope/keyboard"), strings.Index(out, "\n  boot:")
	if kb < 0 || boot < kb {
		t.Errorf("keyboard file must be written in the initramfs stage:\n%s", out)
	}
	// grub_options belongs to install:, which ends where hostname: starts.
	if g, h := strings.Index(out, "  grub_options:"), strings.Index(out, "\nhostname:"); g < 0 || h < g {
		t.Errorf("grub_options must be under install:\n%s", out)
	}
	if n := topLevelStages(out); n != 1 {
		t.Errorf("%d top-level stages: keys, want 1", n)
	}
}

// An encrypted install is the one that needs the keymap in the initramfs,
// where the passphrase is typed. grub_options must stay under install:, not
// under the kcrypt: key that follows.
func TestRenderCloudConfigEncryptedKeepsTheKeymapUnderInstall(t *testing.T) {
	c := Choice{Hostname: "h", Username: "u", Disk: "/dev/vda", Encrypt: true, Passphrase: "correct horse battery",
		KeyboardLayout: "de", ConsoleKeymap: "de-latin1", Timezone: "UTC"}
	out := RenderCloudConfig(c, "x")
	want := "install:\n  device: \"/dev/vda\"\n  auto: true\n" +
		"  grub_options:\n    extra_cmdline: \"rd.vconsole.keymap=de-latin1\"\n  reboot: false\n"
	if !strings.Contains(out, want) {
		t.Errorf("config is missing\n%s\nin\n%s", want, out)
	}
}

func TestRenderCloudConfigLocaleDefaults(t *testing.T) {
	out := RenderCloudConfig(Choice{Hostname: "h", Username: "u", Disk: "/dev/sda"}, "x")
	for _, want := range []string{
		"            XKB_LAYOUT=us\n            XKB_VARIANT=\n",
		"Option \"XkbLayout\" \"us\"\n",
		"/usr/share/zoneinfo/UTC /etc/localtime",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("rendered config missing %q\n---\n%s", want, out)
		}
	}
	if strings.Contains(out, "XkbVariant") {
		t.Errorf("an empty variant must not be written to xorg.conf.d:\n%s", out)
	}
	if strings.Contains(out, "KEYMAP=") {
		t.Errorf("no console keymap was resolved, so vconsole.conf must not be touched:\n%s", out)
	}
	if strings.Contains(out, "grub_options") || strings.Contains(out, "vconsole.keymap") {
		t.Errorf("no console keymap was resolved, so the command line must not change:\n%s", out)
	}
}

// The renderer returns no error. A value the input steps should have refused
// must still not reach the YAML or the shell commands.
func TestRenderCloudConfigReplacesInvalidLocaleValues(t *testing.T) {
	c := Choice{Hostname: "h", Username: "u", Disk: "/dev/sda",
		KeyboardLayout: "de\nstages:\n  network:", KeyboardVariant: "x",
		ConsoleKeymap: "de'; rm -rf / #", Timezone: "../../etc/shadow"}
	out := RenderCloudConfig(c, "x")
	for _, bad := range []string{"network:", "rm -rf", "shadow"} {
		if strings.Contains(out, bad) {
			t.Errorf("invalid value %q reached the config:\n%s", bad, out)
		}
	}
	if !strings.Contains(out, "XKB_LAYOUT=us\n") || !strings.Contains(out, "zoneinfo/UTC ") {
		t.Errorf("invalid values were not replaced by the defaults:\n%s", out)
	}
	if n := topLevelStages(out); n != 1 {
		t.Errorf("%d top-level stages: keys, want 1", n)
	}
}
