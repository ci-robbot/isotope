package main

import (
	"slices"

	"github.com/kairos-community/isotope/pkg/i18n"
)

// PageID names one page of the welcome flow.
type PageID string

const (
	PWelcome       PageID = "welcome"
	PLanguage      PageID = "language"
	PNetwork       PageID = "network"
	PAppearance    PageID = "appearance"
	PLayout        PageID = "layout"
	PWorkflow      PageID = "workflow"
	PLauncher      PageID = "launcher"
	PAccessibility PageID = "accessibility"
	PApps          PageID = "apps"
	PShortcuts     PageID = "shortcuts"
	PReady         PageID = "ready"

	// The steps of "Restore from a backup", after Welcome.
	PRestoreWhere    PageID = "restore-where"
	PRestorePassword PageID = "restore-password"
	PRestoreBackup   PageID = "restore-backup"
	PRestoreRun      PageID = "restore-run"

	// PageLive is the live desktop's one page: install Isotope or try it,
	// with the session's language and keyboard.
	PageLive PageID = "live"
)

// Pages returns the pages for a desktop, in order. Layout, Workflow and
// Launcher exist only on COSMIC.
func Pages(d Desktop) []PageID {
	p := []PageID{PWelcome, PLanguage, PNetwork, PAppearance}
	if d == Cosmic {
		p = append(p, PLayout, PWorkflow, PLauncher)
	}
	return append(p, PAccessibility, PApps, PShortcuts, PReady)
}

// RestorePages are the steps of "Restore from a backup": Welcome, where
// the backup is, its password, which backup, then the restore and the
// restart. The confirmation is a question over the Backup step.
func RestorePages() []PageID {
	return []PageID{PWelcome, PRestoreWhere, PRestorePassword, PRestoreBackup, PRestoreRun}
}

// PageTitle is the sidebar name of a page.
func PageTitle(p PageID) string {
	switch p {
	case PWelcome:
		return i18n.T("Welcome")
	case PLanguage:
		return i18n.T("Language")
	case PNetwork:
		return i18n.T("Network")
	case PAppearance:
		return i18n.T("Appearance")
	case PLayout:
		return i18n.X("page", "Layout")
	case PWorkflow:
		return i18n.X("page", "Workflow")
	case PLauncher:
		return i18n.X("page", "Launcher")
	case PAccessibility:
		return i18n.T("Accessibility")
	case PApps:
		return i18n.T("Apps")
	case PShortcuts:
		return i18n.T("Shortcuts")
	case PReady:
		return i18n.X("page", "Ready")
	case PRestoreWhere:
		return i18n.X("page", "Where")
	case PRestorePassword:
		return i18n.X("page", "Password")
	case PRestoreBackup:
		return i18n.X("page", "Backup")
	case PRestoreRun:
		return i18n.X("page", "Restore")
	case PageLive:
		return i18n.T("Welcome")
	}
	return string(p)
}

// Outcome is what the Ready page shows.
type Outcome struct {
	Applied, Skipped []PageID
	LanguageChanged  string
	AppsInstalled    []string
	AppsFailed       []string
}

// Flow is the user's position in the pages.
type Flow struct {
	pages   []PageID
	idx     int
	done    bool
	Outcome Outcome
}

// NewFlow starts the full flow for a desktop.
func NewFlow(d Desktop) *Flow { return &Flow{pages: Pages(d)} }

// NewLoginFlow is the flow of the login hook: the full flow, without the
// Language page when the language is already set (LanguageSet), which is so
// after an install that carried the live session's language over.
func NewLoginFlow(d Desktop, lang string) *Flow {
	f := NewFlow(d)
	if LanguageSet(lang) {
		f.pages = slices.DeleteFunc(f.pages, func(p PageID) bool { return p == PLanguage })
	}
	return f
}

// NewRestoreFlow is the flow of "Restore from a backup".
func NewRestoreFlow() *Flow { return &Flow{pages: RestorePages()} }

// NewLiveFlow is the live welcome: one page, and no stamp.
func NewLiveFlow() *Flow { return &Flow{pages: []PageID{PageLive}} }

// Single is a flow of one page, for `isotope welcome apps`.
func Single(p PageID) *Flow { return &Flow{pages: []PageID{p}} }

// Current is the page being shown.
func (f *Flow) Current() PageID { return f.pages[f.idx] }

// Index is the position of the current page.
func (f *Flow) Index() int { return f.idx }

// Pages lists the pages of this flow.
func (f *Flow) Pages() []PageID { return append([]PageID(nil), f.pages...) }

// Done reports that Next was called on the last page.
func (f *Flow) Done() bool { return f.done }

// Next advances; on the last page it ends the flow.
func (f *Flow) Next() {
	if f.idx+1 >= len(f.pages) {
		f.done = true
		return
	}
	f.idx++
}

// Back goes to the previous page and stays at the first.
func (f *Flow) Back() {
	f.done = false
	if f.idx > 0 {
		f.idx--
	}
}

// Skip marks the current page skipped, then moves on.
func (f *Flow) Skip() {
	p := f.Current()
	f.Outcome.Applied = without(f.Outcome.Applied, p)
	f.Outcome.Skipped = with(f.Outcome.Skipped, p)
	f.Next()
}

// Apply marks the current page applied.
func (f *Flow) Apply() {
	p := f.Current()
	f.Outcome.Skipped = without(f.Outcome.Skipped, p)
	f.Outcome.Applied = with(f.Outcome.Applied, p)
}

func with(l []PageID, p PageID) []PageID {
	for _, x := range l {
		if x == p {
			return l
		}
	}
	return append(l, p)
}

func without(l []PageID, p PageID) []PageID {
	var out []PageID
	for _, x := range l {
		if x != p {
			out = append(out, x)
		}
	}
	return out
}
