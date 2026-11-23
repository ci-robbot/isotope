package main

import (
	"bufio"
	"fmt"
	"os"
	"strings"
)

// Desktop is the session variant Isotope was built for.
type Desktop string

const (
	Sway   Desktop = "sway"
	I3     Desktop = "i3"
	Cosmic Desktop = "cosmic"
	Niri   Desktop = "niri"
)

// SessionFile is where the image records its desktop.
const SessionFile = "/etc/isotope/session"

// ReadDesktop reads DESKTOP_NAME from the session file.
func ReadDesktop(sessionFile string) (Desktop, error) {
	f, err := os.Open(sessionFile)
	if err != nil {
		return "", err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		k, v, ok := strings.Cut(strings.TrimSpace(sc.Text()), "=")
		if !ok || strings.TrimSpace(k) != "DESKTOP_NAME" {
			continue
		}
		v = strings.Trim(strings.TrimSpace(v), `"'`)
		switch strings.ToLower(v) {
		case "cosmic":
			return Cosmic, nil
		case "sway":
			return Sway, nil
		case "i3":
			return I3, nil
		case "niri":
			return Niri, nil
		}
		return "", fmt.Errorf("%s: unknown DESKTOP_NAME %q", sessionFile, v)
	}
	if err := sc.Err(); err != nil {
		return "", err
	}
	return "", fmt.Errorf("%s: no DESKTOP_NAME", sessionFile)
}
