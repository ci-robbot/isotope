package main

import (
	"bufio"
	"fmt"
	"io"
	"strings"
)

// AppListFile is the curated list of apps, one row per app.
const AppListFile = "/usr/share/isotope/first-run-apps.list"

// App is one row of the list: a Flatpak from Flathub.
type App struct {
	// Group is "browser" (one choice, or none) or "extra" (any number).
	Group       string
	ID, Label   string
	Preselected bool
}

// Short is the name of the app without its description: "Loupe" for
// "Image viewer (Loupe)". The Ready page and the notifications use it.
func (a App) Short() string {
	if i := strings.LastIndex(a.Label, "("); i >= 0 && strings.HasSuffix(a.Label, ")") {
		return a.Label[i+1 : len(a.Label)-1]
	}
	return a.Label
}

// ParseAppList reads the list: group|Flatpak ID|label|preselected, with
// preselected "on" or "off". Empty lines and lines that start with "#" are
// skipped.
func ParseAppList(r io.Reader) ([]App, error) {
	var out []App
	sc := bufio.NewScanner(r)
	n := 0
	for sc.Scan() {
		n++
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		f := strings.Split(line, "|")
		if len(f) != 4 {
			return nil, fmt.Errorf("apps list line %d: want group|id|label|on or off", n)
		}
		a := App{Group: f[0], ID: strings.TrimSpace(f[1]), Label: strings.TrimSpace(f[2])}
		if a.Group != "browser" && a.Group != "extra" {
			return nil, fmt.Errorf("apps list line %d: group %q is not browser or extra", n, a.Group)
		}
		if a.ID == "" || a.Label == "" {
			return nil, fmt.Errorf("apps list line %d: empty id or label", n)
		}
		switch f[3] {
		case "on":
			a.Preselected = true
		case "off":
		default:
			return nil, fmt.Errorf("apps list line %d: %q is not on or off", n, f[3])
		}
		out = append(out, a)
	}
	return out, sc.Err()
}
