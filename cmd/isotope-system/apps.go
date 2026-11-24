package main

// The Apps page talks to the user's Flatpak installation through
// pkg/flatpak (shared with `isotope apps`): flatpak --user, no daemon, no
// polkit. See docs/superpowers/specs/2026-11-24-apps-page-design.md.

import (
	"context"
	"fmt"
	"image"
	_ "image/png" // the catalog's icons
	"os"
	"sort"
	"strings"
	"time"

	"github.com/kairos-community/isotope/pkg/flatpak"
)

// Apps is the user's Flatpak installation, as the Apps page needs it.
type Apps interface {
	// Available says whether flatpak is on this system.
	Available() bool
	FlathubReady() bool
	Installed() ([]flatpak.Installed, error)
	Updates() ([]flatpak.Update, error)
	Review(ups []flatpak.Update) []flatpak.Pending
	// Catalog reads the catalog flatpak keeps; CatalogAge says how old it
	// is (ok false: never downloaded); RefreshCatalog downloads it again.
	Catalog() (*flatpak.Catalog, error)
	CatalogAge() (time.Duration, bool)
	RefreshCatalog() error
	// Picks are the IDs of Isotope Welcome's app list.
	Picks() []string
	RemoteInfo(id string) (flatpak.RemoteInfo, error)
	RemoteMetadata(ref string) (flatpak.Metadata, error)
	InstalledMetadata(ref string) (flatpak.Metadata, error)
	Running() (map[string]bool, error)
	Install(id string) (flatpak.Process, error)
	Update(refs ...string) (flatpak.Process, error)
	Uninstall(id string, deleteData bool) (flatpak.Process, error)
	RemoveUnused() error
	Run(id string) error
	// DataSize is the size of ~/.var/app/ID.
	DataSize(id string) int64
	// Icon is the decoded PNG at path, nil when it cannot be read.
	Icon(path string) image.Image
	Auto() bool
	SetAuto(on bool) error
	LastRun() (flatpak.LastRun, bool)
}

// flatpakApps is Apps over pkg/flatpak.
type flatpakApps struct{ c *flatpak.Client }

func newFlatpakApps() flatpakApps { return flatpakApps{c: flatpak.New()} }

func bg() context.Context { return context.Background() }

func (a flatpakApps) Available() bool                         { return a.c.Available() }
func (a flatpakApps) FlathubReady() bool                      { return a.c.FlathubReady(bg()) }
func (a flatpakApps) Installed() ([]flatpak.Installed, error) { return a.c.Installed(bg()) }
func (a flatpakApps) Updates() ([]flatpak.Update, error)      { return a.c.Updates(bg()) }
func (a flatpakApps) Review(ups []flatpak.Update) []flatpak.Pending {
	return a.c.Review(bg(), ups)
}
func (a flatpakApps) Catalog() (*flatpak.Catalog, error) { return a.c.LoadCatalog() }
func (a flatpakApps) CatalogAge() (time.Duration, bool) {
	return a.c.AppstreamAge(time.Now())
}
func (a flatpakApps) RefreshCatalog() error { return a.c.RefreshAppstream(bg()) }
func (a flatpakApps) Picks() []string {
	ids, _ := flatpak.ReadPicks(flatpak.PicksFile)
	return ids
}
func (a flatpakApps) RemoteInfo(id string) (flatpak.RemoteInfo, error) {
	return a.c.RemoteInfo(bg(), id)
}
func (a flatpakApps) RemoteMetadata(ref string) (flatpak.Metadata, error) {
	return a.c.RemoteMetadata(bg(), ref)
}
func (a flatpakApps) InstalledMetadata(ref string) (flatpak.Metadata, error) {
	return a.c.InstalledMetadata(bg(), ref)
}
func (a flatpakApps) Running() (map[string]bool, error)              { return a.c.Running(bg()) }
func (a flatpakApps) Install(id string) (flatpak.Process, error)     { return a.c.Install(id) }
func (a flatpakApps) Update(refs ...string) (flatpak.Process, error) { return a.c.Update(refs...) }
func (a flatpakApps) Uninstall(id string, del bool) (flatpak.Process, error) {
	return a.c.Uninstall(id, del)
}
func (a flatpakApps) RemoveUnused() error      { return a.c.RemoveUnused(bg()) }
func (a flatpakApps) Run(id string) error      { return a.c.Run(id) }
func (a flatpakApps) DataSize(id string) int64 { return flatpak.DataSize(flatpak.DataPath(id)) }
func (a flatpakApps) Icon(path string) image.Image {
	return decodeIcon(path)
}
func (a flatpakApps) Auto() bool                       { return flatpak.AutoEnabled() }
func (a flatpakApps) SetAuto(on bool) error            { return flatpak.SetAuto(on) }
func (a flatpakApps) LastRun() (flatpak.LastRun, bool) { return flatpak.ReadLastRun() }

// decodeIcon reads a PNG icon; nil when it cannot.
func decodeIcon(path string) image.Image {
	if path == "" {
		return nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	img, _, err := image.Decode(f)
	if err != nil {
		return nil
	}
	return img
}

// catalogMaxAge: the page refreshes an older catalog in the background.
const catalogMaxAge = 24 * time.Hour

// The tabs of the Apps page.
const (
	tabBrowse    = "browse"
	tabInstalled = "installed"
	tabUpdates   = "updates"
)

// appTabs are the tabs, in order: `isotope system apps TAB` names one.
var appTabs = []string{tabBrowse, tabInstalled, tabUpdates}

// appsUpdatesTab is the Updates tab's label: "Updates · 4" with updates.
func appsUpdatesTab(n int) string {
	if n > 0 {
		return fmt.Sprintf("Updates · %d", n)
	}
	return "Updates"
}

// appLine is one app as a row shows it.
type appLine struct {
	ID, Name, Sub, Icon string
	Verified, Installed bool
}

// appSub is "summary · developer", the Browse and Search rows' line.
func appSub(a flatpak.App, installed bool) string {
	parts := []string{a.Summary, a.Developer}
	if installed {
		parts = append(parts, "Installed")
	}
	return joinDots(parts...)
}

func lineOf(a flatpak.App, installed map[string]bool) appLine {
	return appLine{ID: a.ID, Name: a.Name, Sub: appSub(a, installed[a.ID]), Icon: a.Icon, Verified: a.Verified, Installed: installed[a.ID]}
}

// pickLines are the Isotope picks found in the catalog, in the list's order.
func pickLines(cat *flatpak.Catalog, picks []string, installed map[string]bool) []appLine {
	var out []appLine
	for _, id := range picks {
		if a, ok := cat.Get(id); ok {
			out = append(out, lineOf(a, installed))
		}
	}
	return out
}

// extMatch is a system extension that a search found.
type extMatch struct {
	Name, Title, Sub string
}

// extMatches are the extensions whose name, title or description contains
// every word of q.
func extMatches(cat []CatalogEntry, installed []Extension, q string) []extMatch {
	words := strings.Fields(strings.ToLower(q))
	if len(words) == 0 {
		return nil
	}
	inst := map[string]bool{}
	for _, x := range installed {
		inst[x.Name] = true
	}
	var out []extMatch
	for _, e := range cat {
		hay := strings.ToLower(e.Name + " " + e.Title + " " + e.Description)
		all := true
		for _, w := range words {
			if !strings.Contains(hay, w) {
				all = false
			}
		}
		if !all {
			continue
		}
		title := e.Title
		if title == "" {
			title = e.Name
		}
		state := "in Isotope's extension catalog · needs an administrator"
		if inst[e.Name] || e.Installed {
			state = "installed · managed in Extensions"
		}
		out = append(out, extMatch{Name: e.Name, Title: title, Sub: joinDots(e.Description, state)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// installedExtsLine is the Installed tab's last line: "Also installed: 2
// system extensions (gphoto2, modemmanager)." "" for none.
func installedExtsLine(installed []Extension) string {
	if len(installed) == 0 {
		return ""
	}
	names := make([]string, len(installed))
	for i, x := range installed {
		names[i] = x.Name
	}
	sort.Strings(names)
	what := "1 system extension"
	if len(names) > 1 {
		what = fmt.Sprintf("%d system extensions", len(names))
	}
	return "Also installed: " + what + " (" + strings.Join(names, ", ") + ")."
}

// installedRow is one installed app.
type installedRow struct {
	ID, Name, Sub, Icon string
}

// installedRows are the installed apps (not the runtimes), by name.
func installedRows(list []flatpak.Installed, cat *flatpak.Catalog, running map[string]bool) []installedRow {
	var out []installedRow
	for _, in := range list {
		if !in.Ref.IsApp() {
			continue
		}
		r := installedRow{ID: in.Ref.ID, Name: orUnknownName(in.Name, in.Ref.ID)}
		if a, ok := cat.Get(in.Ref.ID); ok {
			r.Icon = a.Icon
			if in.Name == "" {
				r.Name = a.Name
			}
		}
		parts := []string{in.Version, sizeText(in.Size)}
		if running[in.Ref.ID] {
			parts = append(parts, "running")
		}
		r.Sub = joinDots(parts...)
		out = append(out, r)
	}
	return out
}

func orUnknownName(name, id string) string {
	if name != "" {
		return name
	}
	return id
}

// sizeText is flatpak's size with a plain space.
func sizeText(s string) string { return strings.ReplaceAll(s, " ", " ") }

// updateRow is one pending update on the Updates tab.
type updateRow struct {
	Ref, ID, Name, Icon string
	// Change is "146.0 → 146.0.1 · 92 MB · running, restarts later".
	Change string
	// Notes is the newest release's text, or what an update asks for.
	Notes string
	// NeedsOK: it asks for more; Added says what.
	NeedsOK bool
	Added   []string
}

// updatesView is the Updates tab.
type updatesView struct {
	Ready, Held []updateRow
	// ReadyTitle is "READY TO UPDATE · 3, 210 MB".
	ReadyTitle string
	// EOL are the end-of-life lines, one per runtime in use.
	EOL []string
}

// firstLine is the first paragraph line of a release text, for a row.
func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(strings.TrimPrefix(s, "• "))
}

// runtimeName is a person's name for a runtime ID/ARCH/BRANCH:
// "GNOME 46", "Freedesktop 25.08", "KDE 6.8".
func runtimeName(pref string) string {
	f := strings.Split(pref, "/")
	id, branch := f[0], ""
	if len(f) == 3 {
		branch = f[2]
	}
	name := id
	switch {
	case strings.HasPrefix(id, "org.gnome."):
		name = "GNOME"
	case strings.HasPrefix(id, "org.kde."):
		name = "KDE"
	case strings.HasPrefix(id, "org.freedesktop."):
		name = "Freedesktop"
	case strings.HasPrefix(id, "io.elementary."):
		name = "elementary"
	}
	return strings.TrimSpace(name + " " + branch)
}

// updatesViewOf builds the Updates tab from the review of the pending
// updates, the installed refs (old versions, the runtimes in use, their
// end-of-life marks), the catalog and the running apps.
func updatesViewOf(ps []flatpak.Pending, installed []flatpak.Installed, cat *flatpak.Catalog, running map[string]bool) updatesView {
	var v updatesView
	byRef := map[string]flatpak.Installed{}
	users := map[string][]string{}
	for _, in := range installed {
		byRef[in.Ref.String()] = in
		if in.Ref.IsApp() && in.Runtime != "" {
			users[in.Runtime] = append(users[in.Runtime], orUnknownName(in.Name, in.Ref.ID))
		}
	}
	var total int64
	for _, p := range ps {
		in := byRef[p.Ref.String()]
		r := updateRow{Ref: p.Ref.String(), ID: p.Ref.ID, Name: orUnknownName(p.Name, orUnknownName(in.Name, p.Ref.ID))}
		if !p.Ref.IsApp() {
			r.Name = runtimeName(p.Ref.Partial()) + " runtime"
		}
		var parts []string
		if in.Version != "" && p.Version != "" && in.Version != p.Version {
			parts = append(parts, in.Version+" → "+p.Version)
		} else if p.Version != "" {
			parts = append(parts, p.Version)
		}
		if p.NeedsOK() {
			r.NeedsOK, r.Added = true, p.Added
			if p.Err != nil {
				r.Notes = "Its permissions could not be compared. Automatic updates skip it until you update it here."
			} else {
				r.Notes = "Asks for more: " + strings.Join(p.Added, ", ") + ". Automatic updates skip it until you agree."
			}
		} else {
			parts = append(parts, sizeText(p.Download))
			total += p.DownloadBytes
		}
		if running[p.Ref.ID] {
			parts = append(parts, "running, restarts later")
		}
		r.Change = joinDots(parts...)
		if a, ok := cat.Get(p.Ref.ID); ok {
			r.Icon = a.Icon
			if !p.NeedsOK() {
				r.Notes = firstLine(a.Release.Description)
			}
		}
		if !p.Ref.IsApp() && !p.NeedsOK() {
			if us := users[p.Ref.Partial()]; len(us) > 0 {
				sort.Strings(us)
				r.Notes = "Used by " + joinAnd(us) + "."
			} else {
				r.Notes = "A runtime or extension your apps use."
			}
		}
		if r.NeedsOK {
			v.Held = append(v.Held, r)
		} else {
			v.Ready = append(v.Ready, r)
		}
	}
	if len(v.Ready) > 0 {
		v.ReadyTitle = fmt.Sprintf("READY TO UPDATE · %d", len(v.Ready))
		if total > 0 {
			v.ReadyTitle += ", " + flatpak.FormatSize(total)
		}
	}
	eol := map[string]string{}
	for _, in := range installed {
		if !in.Ref.IsApp() && in.EOL != "" {
			eol[in.Ref.Partial()] = in.EOL
		}
	}
	var lines []string
	for rt := range eol {
		us := users[rt]
		if len(us) == 0 {
			continue
		}
		sort.Strings(us)
		lines = append(lines, joinAnd(us)+" "+uses(len(us))+" the "+runtimeName(rt)+" runtime, which no longer gets security fixes. Only "+its(len(us))+" developer can move "+them(len(us))+" to a newer one.")
	}
	sort.Strings(lines)
	v.EOL = lines
	return v
}

func joinAnd(xs []string) string {
	switch len(xs) {
	case 0:
		return ""
	case 1:
		return xs[0]
	}
	return strings.Join(xs[:len(xs)-1], ", ") + " and " + xs[len(xs)-1]
}

func uses(n int) string {
	if n == 1 {
		return "uses"
	}
	return "use"
}

func its(n int) string {
	if n == 1 {
		return "its"
	}
	return "their"
}

func them(n int) string {
	if n == 1 {
		return "it"
	}
	return "them"
}

// autoAppsView is the "Update apps automatically" row.
type autoAppsView struct {
	Sub, Toggle string
}

func autoAppsViewOf(on bool, last flatpak.LastRun, hasLast bool, now time.Time) autoAppsView {
	v := autoAppsView{Sub: "Off. You are told when updates are ready, and you install them here.", Toggle: "Turn on"}
	if on {
		v = autoAppsView{Sub: "On. Once a day, not on a metered connection or a low battery. An app that is open updates the next time it starts.", Toggle: "Turn off"}
	}
	if hasLast {
		v.Sub += " Last run " + ago(now, time.Unix(last.Time, 0)) + ": " + lastRunShort(last)
	}
	return v
}

// lastRunShort is a run in a few words; what it held back is listed under
// "Needs your OK".
func lastRunShort(r flatpak.LastRun) string {
	switch r.Result {
	case flatpak.RunUpdated:
		if len(r.Updated) == 1 {
			return "updated " + r.Updated[0] + "."
		}
		return fmt.Sprintf("updated %d apps and runtimes.", len(r.Updated))
	case flatpak.RunUpToDate:
		return "everything was up to date."
	}
	return flatpak.LastRun{Result: r.Result, Detail: r.Detail}.Text()
}

// detailFacts are the details view's VERSION, DOWNLOAD, LICENCE, SOURCE.
type detailFact struct{ Label, Value string }

func detailFacts(a flatpak.App, ri flatpak.RemoteInfo, inst *flatpak.Installed) []detailFact {
	version := ri.Version
	if version == "" {
		version = a.Release.Version
	}
	var fs []detailFact
	if inst != nil && inst.Version != "" {
		fs = append(fs, detailFact{"INSTALLED", inst.Version})
		if version != "" && version != inst.Version {
			fs = append(fs, detailFact{"NEWEST", version})
		}
	} else if version != "" {
		fs = append(fs, detailFact{"VERSION", version})
	}
	if inst != nil && inst.Size != "" {
		fs = append(fs, detailFact{"SIZE", sizeText(inst.Size)})
	} else if ri.Download != "" {
		fs = append(fs, detailFact{"DOWNLOAD", sizeText(ri.Download)})
	}
	lic := ri.License
	if lic == "" {
		lic = a.License
	}
	if lic != "" {
		fs = append(fs, detailFact{"LICENCE", licenceText(lic)})
	}
	fs = append(fs, detailFact{"SOURCE", "Flathub"})
	return fs
}

// licenceText shortens appstream's proprietary licence reference.
func licenceText(l string) string {
	if strings.HasPrefix(l, "LicenseRef-proprietary") {
		return "Proprietary"
	}
	return l
}

// removeBody is the Remove dialog's text.
func removeBody(name, size string) string {
	what := "The app goes"
	if size != "" {
		what += ", and " + size + " comes back"
	}
	return what + ". Its settings and files stay, so installing it again picks up where you left off."
}
