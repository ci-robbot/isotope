package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const gsettingsSchema = "org.gnome.desktop.interface"

func gsettings(r Runner, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return r.Run(ctx, "gsettings", args...)
}

// SetColorScheme makes apps dark or light. On COSMIC only the theme mode is
// written: cosmic-settings-daemon watches it and sets the gsettings keys
// (and GTK's settings) itself. On the other variants it is the portal's
// colour scheme and the GTK theme name (gsettings, which needs the user's
// session bus; the image's schema override sets gtk-theme to Adwaita-dark,
// and GTK 3 on Wayland, and Flatpak GTK 3 through the portal, follow that
// key rather than color-scheme), and GTK 3's setting file.
func SetColorScheme(r Runner, home string, d Desktop, dark bool) error {
	if d == Cosmic {
		return writeCosmic(home, "com.system76.CosmicTheme.Mode", 1, "is_dark", fmt.Sprint(dark))
	}
	scheme, theme := "prefer-light", "Adwaita"
	if dark {
		scheme, theme = "prefer-dark", "Adwaita-dark"
	}
	for _, kv := range [][2]string{{"color-scheme", scheme}, {"gtk-theme", theme}} {
		if _, err := gsettings(r, "set", gsettingsSchema, kv[0], kv[1]); err != nil {
			return fmt.Errorf("gsettings: %w", err)
		}
	}
	ini := filepath.Join(home, ".config", "gtk-3.0", "settings.ini")
	return setIniKey(ini, "Settings", "gtk-application-prefer-dark-theme", fmt.Sprint(dark))
}

// ReadColorScheme is what is set now. Dark is the answer when nothing says
// otherwise: it is Isotope's look.
func ReadColorScheme(r Runner, home string, d Desktop) (dark bool) {
	if d == Cosmic {
		b, err := os.ReadFile(cosmicKey(home, "com.system76.CosmicTheme.Mode", 1, "is_dark"))
		if err == nil {
			return strings.TrimSpace(string(b)) != "false"
		}
	}
	if r != nil {
		if out, err := gsettings(r, "get", gsettingsSchema, "color-scheme"); err == nil {
			return !strings.Contains(string(out), "prefer-light")
		}
	}
	return true
}

// cosmicKey is the file cosmic-config keeps one key in:
// ~/.config/cosmic/<id>/v<version>/<key>, holding the key's RON.
func cosmicKey(home, id string, version int, key string) string {
	return filepath.Join(home, ".config", "cosmic", id, fmt.Sprintf("v%d", version), key)
}

func writeCosmic(home, id string, version int, key, ron string) error {
	p := cosmicKey(home, id, version, key)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	return os.WriteFile(p, []byte(ron), 0o644)
}

// setIniKey sets key=value in [section] of an INI file, keeping every other
// line, and creates the file or the section when it is missing.
func setIniKey(path, section, key, value string) error {
	b, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	var lines []string
	if s := strings.TrimRight(string(b), "\n"); s != "" {
		lines = strings.Split(s, "\n")
	}
	line := key + "=" + value
	in, done, secEnd := false, false, -1
	for i, l := range lines {
		t := strings.TrimSpace(l)
		if strings.HasPrefix(t, "[") {
			if in {
				secEnd = i
			}
			in = t == "["+section+"]"
			if in {
				secEnd = len(lines)
			}
			continue
		}
		if in {
			k, _, ok := strings.Cut(t, "=")
			if ok && strings.TrimSpace(k) == key {
				lines[i], done = line, true
			}
		}
	}
	if !done {
		switch {
		case secEnd >= 0:
			lines = append(lines[:secEnd], append([]string{line}, lines[secEnd:]...)...)
		default:
			lines = append(lines, "["+section+"]", line)
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644)
}
