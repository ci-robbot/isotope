package main

// The pages' rules as plain Go, tested without a window: what each page
// offers in each daemon state, and the words it shows. They follow the
// CLI's and the tray's rules (update/menu.go, update/main.go).

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/kairos-community/isotope/update/isorelease"
)

// lineLabels are the words for the image lines (update/policy.go).
var lineLabels = map[string]string{
	isorelease.GPUFull: "hardware (Intel and AMD GPU drivers, firmware)",
	isorelease.GPUVM:   "virtual machine (no GPU drivers or firmware)",
	isorelease.LineUKI: "Trusted Boot (signed UKI, hardware image)",
}

// flavorNames are the desktops, in the order the page shows them.
var flavorNames = []struct{ ID, Name string }{
	{"sway", "Sway"}, {"i3", "i3"}, {"cosmic", "COSMIC"}, {"niri", "niri"},
}

func flavorName(id string) string {
	for _, f := range flavorNames {
		if f.ID == id {
			return f.Name
		}
	}
	return id
}

// headline is the first line of the Updates page: the tray's status line.
func headline(st Status) string {
	switch st.State {
	case StateUnmanaged:
		return "This system was not installed from an Isotope release"
	case StateChecking:
		return "Checking for updates…"
	case StateUpToDate:
		if st.Pin != "" {
			return "Isotope " + st.Installed + " (pinned)"
		}
		return "Isotope " + st.Installed + " is up to date"
	case StateUpdateAvailable:
		return "Isotope " + st.Latest + " is available"
	case StateInspecting:
		return "Checking the image…"
	case StateApplying:
		return "Installing the update…"
	case StateExtension:
		return "Changing system extensions…"
	case StateBackup:
		return "Backing up this machine…"
	case StateRebootPending:
		switch st.Pending {
		case pendingFailed:
			return "The update failed; restart before trying again"
		case pendingStarted:
			return "The update was interrupted; restart before trying again"
		}
		return "The update is installed; restart to use it"
	case StateError:
		return "The last check failed"
	}
	if st.Installed == "" {
		return "Isotope"
	}
	return "Isotope " + st.Installed
}

// installsAllowed: never while a job runs or a reboot is pending (the tray's
// rule: a second install would overwrite the known-good passive slot).
func installsAllowed(st Status) bool {
	switch st.State {
	case StateIdle, StateUpToDate, StateUpdateAvailable, StateError:
		return true
	}
	return false
}

// UpdateOffer is what the Updates page offers in a state.
type UpdateOffer struct {
	Headline string
	// Latest is the release "Install" installs, "" when there is none.
	Latest     string
	CanInstall bool
	// Others are the other published releases, newest first.
	Others   []string
	Cancel   bool // only before kairos-agent starts writing
	Restart  bool
	Rollback bool
	// Advanced: "Install an image…" (never on Trusted Boot).
	Advanced bool
}

// OfferFor is the Updates page for st, the published tags and the system's
// image line.
func OfferFor(st Status, tags []string, line string) UpdateOffer {
	o := UpdateOffer{Headline: headline(st)}
	if st.State == StateUnmanaged {
		return o
	}
	o.CanInstall = installsAllowed(st)
	// A pinned system says up to date, but "latest" is how it leaves the pin.
	if st.Latest != "" && (st.Pin != "" || isorelease.Newer(st.Latest, st.Installed)) {
		o.Latest = st.Latest
	}
	for _, t := range tags {
		if isorelease.IsTag(t) && t != st.Installed && t != o.Latest {
			o.Others = append(o.Others, t)
		}
	}
	sort.SliceStable(o.Others, func(i, j int) bool { return isorelease.Newer(o.Others[i], o.Others[j]) })
	o.Cancel = st.State == StateInspecting
	o.Restart = st.State == StateRebootPending
	o.Rollback = o.CanInstall
	o.Advanced = st.Advanced && line != isorelease.LineUKI
	return o
}

// releaseRef is the image of a release for a flavor and line, as the CLI's
// ReleaseRef and LineRef compute it.
func releaseRef(rel isorelease.Release, flavor, line, tag string) (string, error) {
	if rel.ImageRepo == "" {
		return "", fmt.Errorf("the release file names no image repository")
	}
	return rel.ImageRepo + ":" + isorelease.LineTag(flavor, line, tag), nil
}

// newestTag is the highest release tag in tags, "" when there is none.
func newestTag(tags []string) string {
	newest := ""
	for _, t := range tags {
		if isorelease.IsTag(t) && (newest == "" || isorelease.Newer(t, newest)) {
			newest = t
		}
	}
	return newest
}

// InspectReport is what the CLI prints before it asks: the image, what it
// is, and where it comes from.
func InspectReport(info InspectInfo) []string {
	repo, _, _ := strings.Cut(info.Ref, "@")
	lines := []string{
		"Image: " + repo,
		"Digest: " + info.Digest,
		"Flavor: " + orUnknown(info.Flavor),
		"Version: " + orUnknown(info.Version),
	}
	if info.GPU != "" {
		lines = append(lines, "Graphics: "+lineLabels[info.GPU])
	}
	switch {
	case info.Trusted:
		lines = append(lines, "Source: a published Isotope release (trusted)")
	case info.Isotope:
		lines = append(lines, "Source: labelled by its publisher, not a published release")
	default:
		lines = append(lines, "Source: not an Isotope image")
	}
	return lines
}

// overrideQuestion is the one question --yes never answers in the CLI: it
// stops a mistyped image from replacing the system. "" when the refusal
// cannot be lifted (or there is none) for this install.
func overrideQuestion(info InspectInfo, advanced bool) string {
	if info.Refusal == "" || !info.Overridable || !advanced {
		return ""
	}
	if info.Isotope {
		return "This is not a published Isotope release. Install it anyway?"
	}
	return "This is not an Isotope image. Install it anyway?"
}

// lineWarning says what installing an image of line changes (update/main.go).
func lineWarning(line string) string {
	if line == isorelease.GPUVM {
		return "This is the virtual machine image: it has no Intel or AMD GPU drivers and no firmware, so on a real machine Wi-Fi, Bluetooth and graphics may stop working. Later upgrades stay on the virtual machine image."
	}
	return "This is the hardware image: it adds Intel and AMD GPU drivers and firmware, about 600 MB more. Later upgrades stay on the hardware image."
}

const switchWarning = "Your per-user desktop settings may not carry over, and the login screen may change."

func orUnknown(s string) string {
	if s == "" {
		return "unknown"
	}
	return s
}

// installJob follows one install from Apply to its end, by the states the
// daemon reports, as the CLI's installFlow does. A state seen before the
// job's own (a failed check left behind) does not count.
type installJob struct{ started bool }

// JobEnd is how an install ended.
type JobEnd struct {
	Done, OK bool
	Message  string
}

func (j *installJob) Step(st Status) JobEnd {
	switch st.State {
	case StateInspecting, StateApplying:
		j.started = true
		return JobEnd{}
	}
	if !j.started {
		return JobEnd{}
	}
	switch st.State {
	case StateIdle, StateUpToDate, StateUpdateAvailable:
		return JobEnd{Done: true, Message: "The update was cancelled, or the update service restarted."}
	case StateRebootPending:
		if st.Pending == pendingFailed || st.Pending == pendingStarted {
			return JobEnd{Done: true, Message: "The update failed after it started writing. Restart before trying again: " + st.Detail}
		}
		return JobEnd{Done: true, OK: true, Message: "The update is installed and starts on the next boot."}
	case StateError:
		return JobEnd{Done: true, Message: "The update failed: " + st.Detail + ". Nothing changed on the running system; you can retry safely."}
	}
	return JobEnd{}
}

// ExtRow is one row of the Extensions page.
type ExtRow struct {
	Name, Title, Description string
	Installed, Enabled       bool
	Latest                   string
	Versions                 []string
	Source                   string // where an installed one came from
}

// ExtRows merges the installed extensions and the catalog: Installed are
// every installed one (the catalog's words when it lists it), Available the
// catalog's entries not installed. filter keeps the rows whose name, title
// or description contains it, case-insensitively.
func ExtRows(installed []Extension, cat []CatalogEntry, filter string) (inst, avail []ExtRow) {
	byName := map[string]CatalogEntry{}
	for _, e := range cat {
		byName[e.Name] = e
	}
	seen := map[string]bool{}
	for _, x := range installed {
		e := byName[x.Name]
		seen[x.Name] = true
		inst = append(inst, ExtRow{Name: x.Name, Title: e.Title, Description: e.Description, Installed: true,
			Enabled: x.Enabled, Latest: e.Latest, Versions: e.Versions, Source: x.Source})
	}
	for _, e := range cat {
		if seen[e.Name] || e.Installed {
			continue
		}
		avail = append(avail, ExtRow{Name: e.Name, Title: e.Title, Description: e.Description,
			Latest: e.Latest, Versions: e.Versions})
	}
	keep := func(rows []ExtRow) []ExtRow {
		f := strings.ToLower(strings.TrimSpace(filter))
		if f == "" {
			return rows
		}
		var out []ExtRow
		for _, r := range rows {
			if strings.Contains(strings.ToLower(r.Name+" "+r.Title+" "+r.Description), f) {
				out = append(out, r)
			}
		}
		return out
	}
	byNameSort := func(rows []ExtRow) {
		sort.Slice(rows, func(i, j int) bool { return rows[i].Name < rows[j].Name })
	}
	byNameSort(inst)
	byNameSort(avail)
	return keep(inst), keep(avail)
}

// BackupView is the Backup page's words for a BackupStatus.
type BackupView struct {
	Lines []string
	// Warning is a problem to show in amber ("" for none).
	Warning string
	// CanBackUp: a target exists and its disk is there.
	CanBackUp bool
	// Setup: there is no target yet.
	Setup bool
}

func BackupViewOf(b BackupStatus, now time.Time) BackupView {
	var v BackupView
	if !b.Available {
		v.Lines = []string{"Backup is not available on this edition (restic is not installed)."}
		return v
	}
	if b.Restore != "" {
		v.Warning = "After the last restore: " + b.Restore
	}
	if b.Target == "" {
		v.Setup = true
		v.Lines = []string{"This machine has no backup yet."}
		return v
	}
	v.Lines = append(v.Lines, "Backs up to "+b.Target)
	switch b.Schedule {
	case "daily", "weekly":
		v.Lines = append(v.Lines, "Automatic backups: "+b.Schedule)
	default:
		v.Lines = append(v.Lines, "Automatic backups: off")
	}
	if b.Last == 0 {
		v.Lines = append(v.Lines, "Last backup: never")
	} else {
		v.Lines = append(v.Lines, "Last backup: "+ago(now, time.Unix(b.Last, 0)))
	}
	switch {
	case strings.HasPrefix(b.Result, "partial: "):
		v.Lines = append(v.Lines, "Last try: backed up, but "+strings.TrimPrefix(b.Result, "partial: "))
	case b.Result != "" && b.Result != "ok":
		v.Lines = append(v.Lines, "Last try failed: "+b.Result)
	}
	if b.Warning != "" && v.Warning == "" {
		v.Warning = "No backup in the last 14 days."
	}
	v.CanBackUp = b.Present
	if !b.Present {
		v.Lines = append(v.Lines, "The backup disk is not plugged in.")
	}
	return v
}

// ago is a date the way a person says it: "today at 14:05", "yesterday at
// 09:00", or the date.
func ago(now, t time.Time) string {
	y1, m1, d1 := now.Date()
	y2, m2, d2 := t.Date()
	switch {
	case y1 == y2 && m1 == m2 && d1 == d2:
		return "today at " + t.Format("15:04")
	case now.AddDate(0, 0, -1).Format("2006-01-02") == t.Format("2006-01-02"):
		return "yesterday at " + t.Format("15:04")
	}
	return t.Format("2 January 2006, 15:04")
}

// RecoveryLines describe the disk's keys for the Security page.
func RecoveryLines(st RecoveryStatus) []string {
	var out []string
	if st.Edition == "trusted-boot" {
		out = append(out, "Trusted Boot: the disk is sealed to this computer's TPM.")
	}
	for _, c := range st.Containers {
		var keys []string
		if c.Passphrase > 0 {
			keys = append(keys, "passphrase")
		}
		if c.Recovery > 0 {
			keys = append(keys, "recovery key")
		}
		if c.TPM > 0 {
			if c.TPMWorks {
				keys = append(keys, "TPM")
			} else {
				keys = append(keys, "TPM (does not unlock it now)")
			}
		}
		if len(keys) == 0 {
			keys = []string{"no key Isotope knows"}
		}
		out = append(out, c.Label+": "+strings.Join(keys, ", "))
	}
	return out
}
