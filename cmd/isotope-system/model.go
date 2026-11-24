package main

// The pages' rules as plain Go, tested without a window: what each page
// offers in each daemon state, and the words it shows. They follow the
// CLI's and the tray's rules (cmd/isotope/menu.go, cmd/isotope/main.go).

import (
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/kairos-community/isotope/pkg/i18n"
	"github.com/kairos-community/isotope/pkg/isorelease"
)

// lineLabels are the words for the image lines (cmd/isotope/policy.go).
var lineLabels = map[string]string{
	isorelease.GPUFull: i18n.Mark("hardware (Intel and AMD GPU drivers, firmware)"),
	isorelease.GPUVM:   i18n.Mark("virtual machine (no GPU drivers or firmware)"),
	isorelease.LineUKI: i18n.Mark("Trusted Boot (signed UKI, hardware image)"),
}

// lineLabel is the translated words for an image line, "" for an unknown one.
func lineLabel(line string) string {
	if l := lineLabels[line]; l != "" {
		return i18n.T(l)
	}
	return ""
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
		return i18n.T("This system was not installed from an Isotope release")
	case StateChecking:
		return i18n.T("Checking for updates…")
	case StateUpToDate:
		if st.Pin != "" {
			return i18n.Tf("Isotope %s (pinned)", st.Installed)
		}
		return i18n.Tf("Isotope %s is up to date", st.Installed)
	case StateUpdateAvailable:
		return i18n.Tf("Isotope %s is available", st.Latest)
	case StateInspecting:
		return i18n.T("Checking the image…")
	case StateApplying:
		return i18n.T("Installing the update…")
	case StateExtension:
		return i18n.T("Changing system extensions…")
	case StateBackup:
		return i18n.T("Backing up this machine…")
	case StateRebootPending:
		switch st.Pending {
		case pendingFailed:
			return i18n.T("The update failed; restart before trying again")
		case pendingStarted:
			return i18n.T("The update was interrupted; restart before trying again")
		}
		if st.Staged != "" {
			return i18n.Tf("Version %s is installed and starts at the next restart", st.Staged)
		}
		return i18n.T("The update is installed; restart to use it")
	case StateError:
		return i18n.T("The last check failed")
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
	// A staged version is installed already: it waits for the restart.
	if st.Latest != "" && st.Latest != st.Staged && (st.Pin != "" || isorelease.Newer(st.Latest, st.Installed)) {
		o.Latest = st.Latest
	}
	for _, t := range tags {
		if isorelease.IsTag(t) && t != st.Installed && t != o.Latest && t != st.Staged {
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
		return "", errors.New(i18n.T("the release file names no image repository"))
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
		i18n.Tf("Image: %s", repo),
		i18n.Tf("Digest: %s", info.Digest),
		i18n.Tf("Flavor: %s", orUnknown(info.Flavor)),
		i18n.Tf("Version: %s", orUnknown(info.Version)),
	}
	if info.GPU != "" {
		lines = append(lines, i18n.Tf("Graphics: %s", lineLabel(info.GPU)))
	}
	switch {
	case info.Trusted:
		lines = append(lines, i18n.T("Source: a published Isotope release (trusted)"))
	case info.Isotope:
		lines = append(lines, i18n.T("Source: labelled by its publisher, not a published release"))
	default:
		lines = append(lines, i18n.T("Source: not an Isotope image"))
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
		return i18n.T("This is not a published Isotope release. Install it anyway?")
	}
	return i18n.T("This is not an Isotope image. Install it anyway?")
}

// lineWarning says what installing an image of line changes (cmd/isotope/main.go).
func lineWarning(line string) string {
	if line == isorelease.GPUVM {
		return i18n.T("This is the virtual machine image: it has no Intel or AMD GPU drivers and no firmware, so on a real machine Wi-Fi, Bluetooth and graphics may stop working. Later upgrades stay on the virtual machine image.")
	}
	return i18n.T("This is the hardware image: it adds Intel and AMD GPU drivers and firmware, about 600 MB more. Later upgrades stay on the hardware image.")
}

var switchWarning = i18n.Mark("Your per-user desktop settings may not carry over, and the login screen may change.")

func orUnknown(s string) string {
	if s == "" {
		return i18n.T("unknown")
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
		return JobEnd{Done: true, Message: i18n.T("The update was cancelled, or the update service restarted.")}
	case StateRebootPending:
		if st.Pending == pendingFailed || st.Pending == pendingStarted {
			return JobEnd{Done: true, Message: i18n.Tf("The update failed after it started writing. Restart before trying again: %s", st.Detail)}
		}
		return JobEnd{Done: true, OK: true, Message: i18n.T("The update is installed and starts on the next boot.")}
	case StateError:
		return JobEnd{Done: true, Message: i18n.Tf("The update failed: %s. Nothing changed on the running system; you can retry safely.", st.Detail)}
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
		v.Lines = []string{i18n.T("Backup is not available on this edition (restic is not installed).")}
		return v
	}
	if b.Restore != "" {
		v.Warning = i18n.Tf("After the last restore: %s", b.Restore)
	}
	if b.Target == "" {
		v.Setup = true
		v.Lines = []string{i18n.T("This machine has no backup yet.")}
		return v
	}
	v.Lines = append(v.Lines, i18n.Tf("Backs up to %s", backupTargetName(b)))
	switch b.Schedule {
	case "daily":
		v.Lines = append(v.Lines, i18n.T("Automatic backups: daily"))
	case "weekly":
		v.Lines = append(v.Lines, i18n.T("Automatic backups: weekly"))
	default:
		v.Lines = append(v.Lines, i18n.T("Automatic backups: off"))
	}
	if b.Last == 0 {
		v.Lines = append(v.Lines, i18n.T("Last backup: never"))
	} else {
		v.Lines = append(v.Lines, i18n.Tf("Last backup: %s", ago(now, time.Unix(b.Last, 0))))
	}
	switch {
	case strings.HasPrefix(b.Result, "partial: "):
		v.Lines = append(v.Lines, i18n.Tf("Last try: backed up, but %s", strings.TrimPrefix(b.Result, "partial: ")))
	case b.Result != "" && b.Result != "ok":
		v.Lines = append(v.Lines, i18n.Tf("Last try failed: %s", b.Result))
	}
	if b.Warning != "" && v.Warning == "" {
		v.Warning = i18n.T("No backup in the last 14 days.")
	}
	v.CanBackUp = b.Present
	switch {
	case b.Present:
	case b.Kind == "disk":
		v.Lines = append(v.Lines, i18n.Tf("Plug in %s to back up", backupDiskName(b)))
	default:
		v.Lines = append(v.Lines, i18n.T("The backup disk is not plugged in."))
	}
	return v
}

// backupTargetName is the target as the user knows it: a USB disk by its
// label (the daemon's targetName).
func backupTargetName(b BackupStatus) string {
	if b.Kind != "disk" {
		return b.Target
	}
	if b.Label != "" {
		return i18n.Tf("%s (USB disk)", b.Label)
	}
	return i18n.Tf("%s (USB disk)", b.Target)
}

// backupDiskName names the disk of a uuid: target in a sentence.
func backupDiskName(b BackupStatus) string {
	if b.Label != "" {
		return b.Label
	}
	return i18n.T("the backup disk")
}

// SnapshotRows are the rows of the list of backups, newest first:
// "date · host · size" (no size when restic gave none).
func SnapshotRows(snaps []Snapshot, now time.Time) []string {
	snaps = newestFirst(snaps)
	rows := make([]string, len(snaps))
	for i, s := range snaps {
		rows[i] = snapshotTitle(s, now)
		if s.Size > 0 {
			rows[i] += " · " + humanSize(s.Size)
		}
	}
	return rows
}

// newestFirst is a sorted copy of snaps.
func newestFirst(snaps []Snapshot) []Snapshot {
	out := append([]Snapshot(nil), snaps...)
	sort.SliceStable(out, func(i, j int) bool { return out[i].Time > out[j].Time })
	return out
}

// snapshotTitle is "date · host".
func snapshotTitle(s Snapshot, now time.Time) string {
	t := ago(now, time.Unix(s.Time, 0))
	if s.Host != "" {
		t += " · " + s.Host
	}
	return t
}

// exportName is the default name of an exported backup:
// isotope-<host>-<YYYY-MM-DD>.isotope-backup.
func exportName(host string, now time.Time) string {
	if host != "" {
		host += "-"
	}
	return "isotope-" + host + now.Format("2006-01-02") + ".isotope-backup"
}

// ago is a date the way a person says it: "today at 14:05", "yesterday at
// 09:00", or the date.
func ago(now, t time.Time) string {
	y1, m1, d1 := now.Date()
	y2, m2, d2 := t.Date()
	switch {
	case y1 == y2 && m1 == m2 && d1 == d2:
		return i18n.Tf("today at %s", t.Format("15:04"))
	case now.AddDate(0, 0, -1).Format("2006-01-02") == t.Format("2006-01-02"):
		return i18n.Tf("yesterday at %s", t.Format("15:04"))
	}
	return t.Format("2 January 2006, 15:04")
}

// RecoveryLines describe the disk's keys for the Security page.
func RecoveryLines(st RecoveryStatus) []string {
	var out []string
	if st.Edition == "trusted-boot" {
		out = append(out, i18n.T("Trusted Boot: the disk is sealed to this computer's TPM."))
	}
	for _, c := range st.Containers {
		var keys []string
		if c.Passphrase > 0 {
			keys = append(keys, i18n.T("passphrase"))
		}
		if c.Recovery > 0 {
			keys = append(keys, i18n.T("recovery key"))
		}
		if c.TPM > 0 {
			if c.TPMWorks {
				keys = append(keys, "TPM")
			} else {
				keys = append(keys, i18n.T("TPM (does not unlock it now)"))
			}
		}
		if len(keys) == 0 {
			keys = []string{i18n.T("no key Isotope knows")}
		}
		out = append(out, c.Label+": "+strings.Join(keys, ", "))
	}
	return out
}

// HistoryLines are the newest 10 entries of the install history, newest
// first, one line each. The newest installed entry of the running version
// gets "(running)". An entry may have no version.
func HistoryLines(h []HistoryEntry, installed string, now time.Time) []string {
	if len(h) == 0 {
		return nil
	}
	// Newest first; of two entries with one time, the later one in h.
	idx := make([]int, len(h))
	for i := range idx {
		idx[i] = len(h) - 1 - i
	}
	sort.SliceStable(idx, func(i, j int) bool { return h[idx[i]].Time > h[idx[j]].Time })
	s := make([]HistoryEntry, len(h))
	for i, k := range idx {
		s[i] = h[k]
	}
	running := -1
	for i, e := range s {
		if installed != "" && e.Kind == "install" && e.Result == "installed" && e.Version == installed {
			running = i
			break
		}
	}
	if len(s) > 10 {
		s = s[:10]
	}
	out := make([]string, 0, len(s))
	for i, e := range s {
		var l string
		switch {
		case e.Kind == "rollback":
			l = i18n.T("Restarted into the previous system")
		case e.Version == "":
			l = noVersionPhrase(e.Result)
		case e.Result == "installed":
			l = i18n.Tf("%s installed", e.Version)
		case e.Result == "failed":
			l = i18n.Tf("%s failed", e.Version)
		case e.Result == "cancelled":
			l = i18n.Tf("%s cancelled", e.Version)
		default:
			l = e.Version + " " + e.Result
		}
		if i == running {
			l = i18n.Tf("%s (running)", l)
		}
		out = append(out, l+" · "+since(now, time.Unix(e.Time, 0)))
	}
	return out
}

// since says how long before now t was: "20 minutes ago", "yesterday",
// "3 days ago", or the date after 30 days.
func since(now, t time.Time) string {
	d := now.Sub(t)
	switch {
	case d < time.Minute:
		return i18n.T("just now")
	case d < time.Hour:
		n := int(d / time.Minute)
		return i18n.Nf("%d minute ago", "%d minutes ago", n, n)
	case d < 24*time.Hour && now.YearDay() == t.YearDay() && now.Year() == t.Year():
		n := int(d / time.Hour)
		return i18n.Nf("%d hour ago", "%d hours ago", n, n)
	}
	// Whole local days between the two dates.
	loc := now.Location()
	t = t.In(loc)
	y1, m1, d1 := now.Date()
	y2, m2, d2 := t.Date()
	days := int(time.Date(y1, m1, d1, 12, 0, 0, 0, loc).Sub(time.Date(y2, m2, d2, 12, 0, 0, 0, loc)).Round(24*time.Hour) / (24 * time.Hour))
	switch {
	case days <= 1:
		return i18n.T("yesterday")
	case days < 30:
		return i18n.Nf("%d day ago", "%d days ago", days, days)
	}
	return t.Format("2 January 2006")
}

// noVersionPhrase is the line of an entry without a version.
func noVersionPhrase(result string) string {
	switch result {
	case "failed":
		return i18n.T("An update failed")
	case "cancelled":
		return i18n.T("An update was cancelled")
	case "installed":
		return i18n.T("An update was installed")
	}
	return i18n.Tf("An update: %s", result)
}

// autoInput is what the AUTOMATIC UPDATES section of the Updates page is
// drawn from.
type autoInput struct {
	Admin, Loaded bool
	Err           string
	Auto          AutoStatus
	Now           time.Time
}

// autoView is the section: the setting's line, the button, and the last
// automatic run.
type autoView struct {
	Sub, Toggle, Last, Hint string
	Enabled                 bool
}

func autoViewOf(in autoInput) autoView {
	switch {
	case in.Err != "":
		return autoView{Sub: in.Err}
	case !in.Loaded:
		return autoView{Sub: i18n.T("Loading…")}
	}
	a := in.Auto
	v := autoView{Enabled: in.Admin}
	switch {
	case a.Broken != "":
		v.Sub = i18n.Tf("Off: the setting cannot be read (%s). Turn it on or off to repair it.", a.Broken)
		v.Toggle = i18n.T("Turn on")
	case a.Enabled:
		v.Sub = i18n.T("On. Once a day a new release of this desktop is downloaded and installed in the background. It starts at the next restart; nothing restarts by itself.")
		v.Toggle = i18n.T("Turn off")
	default:
		v.Sub = i18n.T("Off. New releases are announced, and you install them.")
		v.Toggle = i18n.T("Turn on")
	}
	if a.LastTime != 0 {
		v.Last = i18n.Tf("Last run %[1]s: %[2]s", since(in.Now, time.Unix(a.LastTime, 0)), autoResult(a))
	}
	if !in.Admin {
		v.Hint = i18n.T("Only an administrator can turn automatic updates on or off.")
	}
	return v
}

// autoResult says the last automatic run in one phrase.
func autoResult(a AutoStatus) string {
	switch a.LastResult {
	case "installed":
		return i18n.Tf("installed Isotope %s.", a.LastVersion)
	case "up-to-date":
		return i18n.T("up to date.")
	case "skipped":
		return a.LastDetail
	case "failed":
		return i18n.Tf("failed. %s", a.LastDetail)
	}
	return a.LastResult + ". " + a.LastDetail
}
