package main

import "github.com/kairos-community/isotope/pkg/i18n"

// Page is one page of the window, and the argument that opens on it:
// `isotope system PAGE`.
type Page string

const (
	PageApps       Page = "apps"
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
	PagePrinters   Page = "printers"
	PageAbout      Page = "about"
)

// Pages is the sidebar, in order.
var Pages = []Page{PageApps, PageUpdates, PageDesktop, PageExtensions, PageBackup, PageSecurity, PageNetwork, PageBluetooth, PagePrinters, PageUsers, PageDisks, PageCompat, PageAbout}

// Title is the page's name in the sidebar and its heading.
func (p Page) Title() string {
	switch p {
	case PageApps:
		return i18n.T("Apps")
	case PageUpdates:
		return i18n.T("Updates")
	case PageDesktop:
		return i18n.T("Desktop & image")
	case PageExtensions:
		return i18n.T("Extensions")
	case PageBackup:
		return i18n.T("Backup")
	case PageSecurity:
		return i18n.T("Security")
	case PageNetwork:
		return i18n.T("Network")
	case PageBluetooth:
		return i18n.T("Bluetooth")
	case PagePrinters:
		return i18n.T("Printers")
	case PageUsers:
		return i18n.T("Users")
	case PageDisks:
		return i18n.T("Disks")
	case PageCompat:
		return i18n.T("Compatibility")
	case PageAbout:
		return i18n.T("About")
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

const usage = "usage: isotope-system [apps [browse | installed | updates] | updates | desktop | extensions | backup | security | network | bluetooth | printers | users | disks | compatibility | about]"

// ParseTarget is ParseArgs with a tab of the Apps page after it:
// `isotope system apps updates`.
func ParseTarget(args []string) (Page, string, bool) {
	if len(args) == 2 && args[0] == string(PageApps) {
		for _, t := range appTabs {
			if args[1] == t {
				return PageApps, t, true
			}
		}
		return "", "", false
	}
	p, ok := ParseArgs(args)
	return p, "", ok
}

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
