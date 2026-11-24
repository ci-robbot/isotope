package flatpak

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Automatic updates of the user's apps: the setting, the review of what an
// update would add, and the record of the last automatic run. The run
// itself is `isotope apps update --auto` (cmd/isotope/apps.go), from the
// isotope-app-updates.timer user unit.

// configDir is $XDG_CONFIG_HOME/isotope; stateDir $XDG_STATE_HOME/isotope.
func configDir() string {
	if d := os.Getenv("XDG_CONFIG_HOME"); d != "" {
		return filepath.Join(d, "isotope")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "isotope")
}

func stateDir() string {
	if d := os.Getenv("XDG_STATE_HOME"); d != "" {
		return filepath.Join(d, "isotope")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "state", "isotope")
}

// SettingFile is the user's setting, "auto=on" or "auto=off".
func SettingFile() string { return filepath.Join(configDir(), "apps") }

// LastRunFile is the record of the last automatic run.
func LastRunFile() string { return filepath.Join(stateDir(), "app-updates") }

// AutoEnabled reads the setting: on unless the file says auto=off. No file
// is on, so is a file without the key.
func AutoEnabled() bool {
	b, err := os.ReadFile(SettingFile())
	if err != nil {
		return true
	}
	on := true
	for _, l := range strings.Split(string(b), "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(l), "=")
		if ok && strings.TrimSpace(k) == "auto" {
			on = strings.TrimSpace(v) != "off"
		}
	}
	return on
}

// SetAuto writes the setting, keeping the file's other lines, through a
// rename.
func SetAuto(on bool) error {
	path := SettingFile()
	val := "auto=off"
	if on {
		val = "auto=on"
	}
	var lines []string
	if b, err := os.ReadFile(path); err == nil {
		for _, l := range strings.Split(strings.TrimRight(string(b), "\n"), "\n") {
			if k, _, ok := strings.Cut(strings.TrimSpace(l), "="); ok && strings.TrimSpace(k) == "auto" {
				continue
			}
			if l != "" {
				lines = append(lines, l)
			}
		}
	}
	lines = append(lines, val)
	return writeFile(path, []byte(strings.Join(lines, "\n")+"\n"))
}

func writeFile(path string, b []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o644); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// The results of an automatic run.
const (
	RunUpdated  = "updated"
	RunUpToDate = "up-to-date"
	RunSkipped  = "skipped"
	RunFailed   = "failed"
)

// LastRun is the record of an automatic run.
type LastRun struct {
	Time   int64  `json:"time"`
	Result string `json:"result"`
	// Detail says why it skipped or failed.
	Detail string `json:"detail,omitempty"`
	// Updated are the names of what it updated.
	Updated []string `json:"updated,omitempty"`
	// Held are the updates it left for the user.
	Held []Held `json:"held,omitempty"`
}

// Held is an update the automatic run did not install, and why.
type Held struct {
	Ref  string `json:"ref"`
	Name string `json:"name"`
	Why  string `json:"why"`
}

// Text says the run's result in a sentence: "updated Firefox, VLC.",
// "skipped: the connection is metered.", with what it held back.
func (r LastRun) Text() string {
	var s string
	switch r.Result {
	case RunUpdated:
		s = "updated " + strings.Join(r.Updated, ", ") + "."
	case RunUpToDate:
		s = "everything was up to date."
	case RunSkipped:
		s = "skipped: " + r.Detail + "."
	case RunFailed:
		s = "failed: " + r.Detail
		if !strings.HasSuffix(s, ".") {
			s += "."
		}
	default:
		s = r.Result + "."
	}
	for _, h := range r.Held {
		s += " Held back " + h.Name + ": " + h.Why + "."
	}
	return s
}

// ReadLastRun reads the record; ok is false when there is none.
func ReadLastRun() (LastRun, bool) {
	b, err := os.ReadFile(LastRunFile())
	if err != nil {
		return LastRun{}, false
	}
	var r LastRun
	if json.Unmarshal(b, &r) != nil || r.Time == 0 {
		return LastRun{}, false
	}
	return r, true
}

// WriteLastRun keeps r as the last run.
func WriteLastRun(r LastRun) error {
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	return writeFile(LastRunFile(), append(b, '\n'))
}

// Pending is a pending update and what it would add.
type Pending struct {
	Update
	// Added are the permissions the new version asks for that the
	// installed one does not have (apps only).
	Added []string
	// Err: the permissions could not be compared. Such an update is held
	// like one that adds some.
	Err error
}

// NeedsOK says whether the update waits for the user: it asks for more,
// or what it asks for could not be read.
func (p Pending) NeedsOK() bool { return len(p.Added) > 0 || p.Err != nil }

// Review compares, for each app update, the permissions of Flathub's new
// commit with the installed ones. Runtimes have no permissions of their
// own.
func (c *Client) Review(ctx context.Context, ups []Update) []Pending {
	out := make([]Pending, len(ups))
	for i, u := range ups {
		out[i] = Pending{Update: u}
		if !u.Ref.IsApp() {
			continue
		}
		old, err := c.InstalledMetadata(ctx, u.Ref.String())
		if err != nil {
			out[i].Err = err
			continue
		}
		newer, err := c.RemoteMetadata(ctx, u.Ref.String())
		if err != nil {
			out[i].Err = err
			continue
		}
		out[i].Added = Added(old.Permissions(), newer.Permissions())
	}
	return out
}

// Split is the pending updates the automatic run installs, and the ones
// it holds back.
func Split(ps []Pending) (ready, held []Pending) {
	for _, p := range ps {
		if p.NeedsOK() {
			held = append(held, p)
		} else {
			ready = append(ready, p)
		}
	}
	return ready, held
}

// HeldWhy says why an update waits, in a sentence's end: "it asks for
// more: the network, your home folder".
func HeldWhy(p Pending) string {
	if p.Err != nil {
		return "its permissions could not be compared (" + p.Err.Error() + ")"
	}
	return "it asks for more: " + strings.Join(p.Added, ", ")
}

// SetKey names a set of pending updates (the refs and their new commits,
// or versions): the tray announces each set once.
func SetKey(ups []Update) string {
	keys := make([]string, 0, len(ups))
	for _, u := range ups {
		k := u.Ref.String()
		if u.Commit != "" {
			k += "@" + u.Commit
		} else if u.Version != "" {
			k += "@" + u.Version
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return strings.Join(keys, " ")
}
