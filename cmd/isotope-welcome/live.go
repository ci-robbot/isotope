package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/kairos-community/isotope/pkg/installer"
)

// The live desktop: the session of the USB stick, before Isotope is
// installed. See docs/superpowers/specs/2026-11-23-live-desktop-design.md,
// section 4.

// LiveMarker exists only on a live desktop boot (isotope-live-boot writes it
// in the tmpfs /run). A variable, so the tests can point it elsewhere.
var LiveMarker = "/run/isotope/live-desktop"

// OnLiveDesktop reports whether marker exists.
func OnLiveDesktop(marker string) bool {
	_, err := os.Stat(marker)
	return err == nil
}

// LiveChoices are the language and keyboard picked in the live welcome. The
// installer reads them from LiveChoicesPath, so the installed system gets
// the same values.
type LiveChoices struct {
	Lang    string `json:"lang"`
	Layout  string `json:"layout"`
	Variant string `json:"variant"`
}

// LiveChoicesPath is $XDG_RUNTIME_DIR/isotope/live-choices.json, "" when
// there is no runtime directory.
func LiveChoicesPath(runtimeDir string) string {
	if runtimeDir == "" {
		return ""
	}
	return filepath.Join(runtimeDir, "isotope", "live-choices.json")
}

// WriteLiveChoices writes c to path through a temporary file, so a reader
// never sees half of it.
func WriteLiveChoices(path string, c LiveChoices) error {
	b, err := json.Marshal(c)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// liveChoicesNow is what the session uses now: LANG (else the system's),
// and the layout the session started with (XKB_DEFAULT_LAYOUT, from
// /etc/profile.d/isotope-keyboard.sh), else us.
func liveChoicesNow(getenv func(string) string, systemLang string) LiveChoices {
	first := func(csv string) string {
		s, _, _ := strings.Cut(csv, ",")
		return strings.TrimSpace(s)
	}
	c := LiveChoices{Lang: getenv("LANG"), Layout: first(getenv("XKB_DEFAULT_LAYOUT")), Variant: first(getenv("XKB_DEFAULT_VARIANT"))}
	if c.Lang == "" {
		c.Lang = systemLang
	}
	if c.Layout == "" {
		c.Layout, c.Variant = "us", ""
	}
	return c
}

// liveVersionLine is "Isotope v0.0.25 · COSMIC": VERSION from the release
// file, and the variant's own name (DESKTOP_NAME) from the session file.
func liveVersionLine(releaseFile, sessionFile string) string {
	version := readKey(releaseFile, "VERSION")
	name := readKey(sessionFile, "DESKTOP_NAME")
	s := "Isotope"
	if version != "" {
		s += " " + version
	}
	if name != "" {
		s += " · " + name
	}
	return s
}

// readKey is the value of KEY=value in a file, unquoted; "" when missing.
func readKey(path, key string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	v := ""
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		k, val, ok := strings.Cut(strings.TrimSpace(sc.Text()), "=")
		if ok && strings.TrimSpace(k) == key {
			v = strings.Trim(strings.TrimSpace(val), `"'`)
		}
	}
	return v
}

// HelpItem is one item of the live welcome's help line: "Wi-Fi" and the
// keys that open it.
type HelpItem struct {
	Label string
	Keys  []string
	// Where names a place instead of keys ("top bar"), for a variant
	// whose shell has no key for it.
	Where string
}

// HelpItems picks the Wi-Fi, apps and help keys from the variant's shortcut
// sheet, by the sheet's own words, so the line never names a key the
// variant does not bind. A key the sheet does not have is left out. Of a
// cap with alternatives ("D / Space") the first is shown.
//
//   - Wi-Fi: the row "Wi-Fi" (sway, i3), else the control center that opens
//     Wi-Fi first (niri); the Isotope menu row lists Wi-Fi too, but it is a
//     menu, not the Wi-Fi key.
//   - Apps: "Launch an application".
//   - Help: "This help" (Super+F1, isotope-help).
func HelpItems(sheet []Shortcut) []HelpItem {
	find := func(match func(string) bool) []string {
		for _, s := range sheet {
			if match(s.Action) {
				var keys []string
				for _, k := range s.Keys {
					alt, _, _ := strings.Cut(k, " / ")
					keys = append(keys, alt)
				}
				return keys
			}
		}
		return nil
	}
	wifi := find(func(a string) bool { return a == "Wi-Fi" || strings.HasPrefix(a, "Wi-Fi ") })
	if wifi == nil {
		wifi = find(func(a string) bool { return strings.HasPrefix(a, "Control center: Wi-Fi") })
	}
	var out []HelpItem
	for _, it := range []HelpItem{
		{Label: "Wi-Fi", Keys: wifi},
		{Label: "Apps", Keys: find(func(a string) bool { return a == "Launch an application" })},
		{Label: "Help", Keys: find(func(a string) bool { return a == "This help" })},
	} {
		if it.Keys != nil {
			out = append(out, it)
		}
	}
	return out
}

// LiveHelp is the help line of desktop d: HelpItems, and on COSMIC, whose
// sheet binds no Wi-Fi key, the top bar's quick settings in its place.
func LiveHelp(d Desktop, sheet []Shortcut) []HelpItem {
	items := HelpItems(sheet)
	if d == Cosmic && (len(items) == 0 || items[0].Label != "Wi-Fi") {
		items = append([]HelpItem{{Label: "Wi-Fi", Where: "top bar"}}, items...)
	}
	return items
}

// tryText is the line of "Try it out": where the installer is found later
// on desktop d. COSMIC has a dock; the tiling variants have the Isotope
// menu and the app list.
func tryText(d Desktop) string {
	if d == Cosmic {
		return "Look around first. You can install at any time from the dock or the app launcher."
	}
	return "Look around first. You can install at any time from the Isotope menu (Super+Esc) or the app list."
}

// ErrNotLive refuses the live welcome off the live desktop.
var ErrNotLive = errors.New("This window is for the live system.")

// UpdateLiveChoices changes the choices file at path: what it holds, or
// base when there is none yet, changed by change.
func UpdateLiveChoices(path string, base LiveChoices, change func(*LiveChoices)) error {
	c := base
	if b, err := os.ReadFile(path); err == nil {
		var old LiveChoices
		if json.Unmarshal(b, &old) == nil {
			c = old
		}
	}
	change(&c)
	return WriteLiveChoices(path, c)
}

// ApplySessionKeymap switches the running session to an XKB layout, the
// compositor's own way:
//
//   - sway: `swaymsg input type:keyboard xkb_layout`. The variant is
//     cleared first, as the old one may not exist for the new layout, and
//     set after.
//   - i3: setxkbmap (built in the Dockerfile x11-desktop-tools stage).
//   - niri: an `input` block in ~/.config/niri/isotope-keyboard.kdl, which
//     /etc/niri/config.kdl includes; niri reloads it when it changes (niri
//     msg can only switch between the layouts the config already lists).
//   - COSMIC: cosmic-comp's xkb_config key (com.system76.CosmicComp), which
//     cosmic-comp watches, written as the Appearance and Layout pages write
//     COSMIC's keys (writeCosmic).
func ApplySessionKeymap(r Runner, home string, d Desktop, layout, variant string) error {
	if err := installer.ValidateKeyboard(layout, variant); err != nil {
		return err
	}
	switch d {
	case Sway:
		steps := [][]string{{"xkb_variant", `""`}, {"xkb_layout", layout}}
		if variant != "" {
			steps = append(steps, []string{"xkb_variant", variant})
		}
		for _, s := range steps {
			if _, err := run(r, "swaymsg", append([]string{"input", "type:keyboard"}, s...)...); err != nil {
				return fmt.Errorf("swaymsg %s: %w", s[0], err)
			}
		}
		return nil
	case I3:
		args := []string{"-layout", layout}
		if variant != "" {
			args = append(args, "-variant", variant)
		}
		if _, err := run(r, "setxkbmap", args...); err != nil {
			return fmt.Errorf("setxkbmap: %w", err)
		}
		return nil
	case Niri:
		return writeNiriKeyboard(filepath.Join(home, ".config", "niri", "isotope-keyboard.kdl"), layout, variant)
	case Cosmic:
		return writeCosmic(home, "com.system76.CosmicComp", 1, "xkb_config", cosmicXkb(layout, variant))
	}
	return fmt.Errorf("no keyboard layout for desktop %q", d)
}

// writeNiriKeyboard writes the layout as a niri config file.
func writeNiriKeyboard(path, layout, variant string) error {
	var sb strings.Builder
	sb.WriteString("// The keyboard layout, set by Isotope Welcome on the live desktop.\n")
	sb.WriteString("// /etc/niri/config.kdl includes this file; niri reloads it when it changes.\n\n")
	sb.WriteString("input {\n    keyboard {\n        xkb {\n")
	fmt.Fprintf(&sb, "            layout %q\n", layout)
	if variant != "" {
		fmt.Fprintf(&sb, "            variant %q\n", variant)
	}
	sb.WriteString("        }\n    }\n}\n")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(sb.String()), 0o644)
}

// cosmicXkb is cosmic-comp's XkbConfig in RON (cosmic-comp-config
// src/lib.rs), with its defaults for everything but the layout.
func cosmicXkb(layout, variant string) string {
	return fmt.Sprintf("(\n    rules: \"\",\n    model: \"\",\n    layout: %q,\n    variant: %q,\n    options: None,\n    repeat_delay: 600,\n    repeat_rate: 25,\n)", layout, variant)
}

// localeCode is what a LANG value looks like ("de_DE.UTF-8", "sr_RS@latin").
var localeCode = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_.@-]{0,63}$`)

// SetSessionLocale sets LANG for the apps started from now on: in the
// user's systemd manager and the D-Bus activation environment, as sway's
// and i3's configs push it at session start. On the live desktop that is
// all: /etc is tmpfs, and the installer takes the language from
// LiveChoices, so nothing goes through isotope-updated or /oem.
func SetSessionLocale(r Runner, lang string) error {
	if !localeCode.MatchString(lang) {
		return errors.New("not a locale name: " + lang)
	}
	if _, err := run(r, "dbus-update-activation-environment", "--systemd", "LANG="+lang); err != nil {
		return fmt.Errorf("dbus-update-activation-environment: %w", err)
	}
	return nil
}
