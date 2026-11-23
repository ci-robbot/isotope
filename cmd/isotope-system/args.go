package main

// Page is one page of the window, and the argument that opens on it:
// `isotope system PAGE`.
type Page string

const (
	PageUpdates    Page = "updates"
	PageDesktop    Page = "desktop"
	PageExtensions Page = "extensions"
	PageBackup     Page = "backup"
	PageSecurity   Page = "security"
	PageNetwork    Page = "network"
	PageBluetooth  Page = "bluetooth"
	PageUsers      Page = "users"
	PageDisks      Page = "disks"
	PageCompat     Page = "compatibility"
	PageAbout      Page = "about"
)

// Pages is the sidebar, in order.
var Pages = []Page{PageUpdates, PageDesktop, PageExtensions, PageBackup, PageSecurity, PageNetwork, PageBluetooth, PageUsers, PageDisks, PageCompat, PageAbout}

// later are the pages listed but not there yet: none today.
var later = map[Page]bool{}

// Title is the page's name in the sidebar and its heading.
func (p Page) Title() string {
	switch p {
	case PageUpdates:
		return "Updates"
	case PageDesktop:
		return "Desktop & image"
	case PageExtensions:
		return "Extensions"
	case PageBackup:
		return "Backup"
	case PageSecurity:
		return "Security"
	case PageNetwork:
		return "Network"
	case PageBluetooth:
		return "Bluetooth"
	case PageUsers:
		return "Users"
	case PageDisks:
		return "Disks"
	case PageCompat:
		return "Compatibility"
	case PageAbout:
		return "About"
	}
	return string(p)
}

// index is p's place in Pages, or 0.
func (p Page) index() int {
	for i, q := range Pages {
		if q == p {
			return i
		}
	}
	return 0
}

const usage = "usage: isotope-system [updates | desktop | extensions | backup | security | network | bluetooth | users | disks | compatibility | about]"

// ParseArgs maps the command line to the page to open on; ok is false for
// anything else. No argument is Updates.
func ParseArgs(args []string) (Page, bool) {
	if len(args) == 0 {
		return PageUpdates, true
	}
	if len(args) > 1 {
		return "", false
	}
	for _, p := range Pages {
		if string(p) == args[0] {
			return p, true
		}
	}
	return "", false
}
