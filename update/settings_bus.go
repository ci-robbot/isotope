package main

import (
	"fmt"
	"os"
	"regexp"
	"strings"
	"sync"

	"github.com/godbus/dbus/v5"
)

// The settings methods change machine-wide settings the Welcome app and the
// settings tools offer to an administrator. They share one polkit action:
// each is a small file write, with nothing to run as root but `locale -a`.

// The files SetLocale writes, and the command it asks. Variables so the
// tests can point them into a temporary directory and fake the command.
var (
	// localeConf is what systemd's localed would write, and what the
	// session reads (/etc/profile.d/isotope-locale.sh).
	localeConf = "/etc/locale.conf"
	// localeOEM writes localeConf again at every boot: on Kairos /etc is
	// rebuilt from the image, so a file written there alone is lost at
	// the next boot. The installer's keyboard layout does the same.
	localeOEM = "/oem/91_isotope_locale.yaml"
	// localeList runs `locale -a` (musl-locales).
	localeList cmdRunner = execCmd
)

// localeName is every character a locale name may have: nothing in it can
// break out of the YAML or of LANG=.
var localeName = regexp.MustCompile(`^[A-Za-z0-9_.@-]{1,64}$`)

var settingsMu sync.Mutex

func errNotLocale(lang string) error {
	return fmt.Errorf("%q is not a locale of this system", lang)
}

// oemLocale is the cloud-config that writes localeConf at every boot. The
// stage and the file style are the installer's keyboard stage
// (install-ui/core/config.go, writeLocaleStage).
func oemLocale(lang string) string {
	var b strings.Builder
	b.WriteString("name: \"Isotope system locale\"\n")
	b.WriteString("stages:\n")
	b.WriteString("  initramfs:\n")
	b.WriteString("    - name: \"Write /etc/locale.conf\"\n")
	b.WriteString("      files:\n")
	b.WriteString("        - path: /etc/locale.conf\n")
	b.WriteString("          permissions: 0644\n")
	b.WriteString("          content: |\n")
	fmt.Fprintf(&b, "            LANG=%s\n", lang)
	return b.String()
}

// setLocale checks lang against the locales the system has, then writes
// the boot-time config first and the live file second: if the second fails
// the next boot still applies the choice.
func setLocale(lang string) error {
	if !localeName.MatchString(lang) {
		return fmt.Errorf("%q is not a locale name", lang)
	}
	out, err := localeList("locale", []string{"-a"}, nil)
	if err != nil {
		return err
	}
	found := false
	for _, l := range strings.Split(string(out), "\n") {
		if strings.TrimSpace(l) == lang {
			found = true
			break
		}
	}
	if !found {
		return errNotLocale(lang)
	}
	settingsMu.Lock()
	defer settingsMu.Unlock()
	if restoring {
		return errRestoring
	}
	if err := writeRunFile(localeOEM, oemLocale(lang)); err != nil {
		return err
	}
	return writeRunFile(localeConf, "LANG="+lang+"\n")
}

// SetLocale sets the system language for every user. It needs the settings
// action: administrators, no password, never a Flatpak app.
func (a busAPI) SetLocale(sender dbus.Sender, lang string) *dbus.Error {
	done, derr := a.s.enter()
	if derr != nil {
		return derr
	}
	defer done()
	if err := a.s.authorize(sender, actionSettings); err != nil {
		return err
	}
	if derr := a.s.notRestoring(); derr != nil {
		return derr
	}
	if err := setLocale(lang); err != nil {
		fmt.Fprintf(os.Stderr, "isotope: SetLocale %q: %v\n", lang, err)
		return busError(err)
	}
	return nil
}
