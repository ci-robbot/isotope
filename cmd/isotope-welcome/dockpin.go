package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// cosmicDefaultFavorites is the dock's list a user starts from.
var cosmicDefaultFavorites = "/usr/share/cosmic/com.system76.CosmicAppList/v1/favorites"

var ronString = regexp.MustCompile(`"((?:[^"\\]|\\.)*)"`)

// ronStrings are the strings of a RON list, in order.
func ronStrings(s string) []string {
	var out []string
	for _, m := range ronString.FindAllStringSubmatch(s, -1) {
		out = append(out, m[1])
	}
	return out
}

// PinToCosmicDock appends appID to the user's dock favorites
// (~/.config/cosmic/com.system76.CosmicAppList/v1/favorites, a RON list).
// A user with no file starts from the system default. An app that is
// already in the list is not added again.
func PinToCosmicDock(home, appID string) error { return pinToCosmicDock(home, appID, false) }

// PinFirstToCosmicDock is PinToCosmicDock, but puts a new app at the start
// of the list (the live session's "Install Isotope"). An app that is
// already in the list stays where it is.
func PinFirstToCosmicDock(home, appID string) error { return pinToCosmicDock(home, appID, true) }

// liveDockPinID is the desktop-file ID of the live launcher.
const liveDockPinID = "isotope-install"

// liveDockPin pins the installer first in COSMIC's dock when the live
// welcome starts. Other desktops have their own launchers and get nothing.
// pin is PinFirstToCosmicDock in the program.
func liveDockPin(d Desktop, home string, pin func(home, id string) error) {
	if d != Cosmic {
		return
	}
	if err := pin(home, liveDockPinID); err != nil {
		fmt.Fprintf(os.Stderr, "isotope-welcome: %v\n", err)
	}
}

func pinToCosmicDock(home, appID string, first bool) error {
	path := filepath.Join(home, ".config", "cosmic", "com.system76.CosmicAppList", "v1", "favorites")
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		b, err = os.ReadFile(cosmicDefaultFavorites)
		if os.IsNotExist(err) {
			b, err = []byte("[]"), nil
		}
	}
	if err != nil {
		return err
	}
	s := string(b)
	for _, id := range ronStrings(s) {
		if id == appID {
			return nil
		}
	}
	var out string
	if start := strings.Index(s, "["); first && start >= 0 {
		out = s[:start+1] + "\n    \"" + appID + "\"," + s[start+1:]
		if !strings.HasSuffix(out, "\n") {
			out += "\n"
		}
	} else {
		end := strings.LastIndex(s, "]")
		if end < 0 {
			s, end = "[]", 1
		}
		head := strings.TrimRight(s[:end], " \t\r\n")
		if !strings.HasSuffix(head, "[") && !strings.HasSuffix(head, ",") {
			head += ","
		}
		out = head + "\n    \"" + appID + "\",\n]\n"
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	// Write a temporary file and rename it over the list, so the dock
	// never reads half a file.
	tmp, err := os.CreateTemp(filepath.Dir(path), ".favorites-*")
	if err != nil {
		return err
	}
	if _, err := tmp.WriteString(out); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Chmod(0o644); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return nil
}
