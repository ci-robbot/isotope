package shares

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Saved is one share in the user's list: never a password, which lives
// only in the keyring.
type Saved struct {
	URL       string `json:"url"`
	Name      string `json:"name"`
	User      string `json:"user,omitempty"`
	Domain    string `json:"domain,omitempty"`
	AutoMount bool   `json:"auto_mount"`
}

// Check checks a saved share as the daemon would check it.
func (s Saved) Check() error {
	l, err := Parse(s.URL)
	if err != nil {
		return err
	}
	if err := CheckName(s.Name); err != nil {
		return err
	}
	if l.Scheme == NFS && (s.User != "" || s.Domain != "") {
		return errors.New("an NFS share takes no user or domain")
	}
	if err := CheckCredential("user name", s.User); err != nil {
		return err
	}
	return CheckCredential("domain", s.Domain)
}

// file is shares.json.
type file struct {
	Shares []Saved `json:"shares"`
}

// Path is $XDG_CONFIG_HOME/isotope/shares.json, or ~/.config/... when
// XDG_CONFIG_HOME is not set.
func Path() (string, error) {
	dir := os.Getenv("XDG_CONFIG_HOME")
	if dir == "" || !filepath.IsAbs(dir) {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		dir = filepath.Join(home, ".config")
	}
	return filepath.Join(dir, "isotope", "shares.json"), nil
}

// Load reads the list. A missing file is an empty list. An entry that does
// not pass Check is left out, so one bad line does not hide the others.
func Load(path string) ([]Saved, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var f file
	if err := json.Unmarshal(b, &f); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	out := make([]Saved, 0, len(f.Shares))
	for _, s := range f.Shares {
		if s.Check() == nil {
			out = append(out, s)
		}
	}
	return out, nil
}

// Store writes the list: 0600, through a temporary file in the same
// folder and a rename, so a crash leaves the old list or the new one.
func Store(path string, list []Saved) error {
	if list == nil {
		list = []Saved{}
	}
	b, err := json.MarshalIndent(file{Shares: list}, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".shares-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(append(b, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// Find is the index of the saved share at the same place as l, -1 when
// there is none.
func Find(list []Saved, l Location) int {
	for i, s := range list {
		if sl, err := Parse(s.URL); err == nil && sl.Same(l) {
			return i
		}
	}
	return -1
}

// Put adds s, or replaces the saved share at the same place.
func Put(list []Saved, s Saved) []Saved {
	out := append([]Saved(nil), list...)
	if l, err := Parse(s.URL); err == nil {
		if i := Find(out, l); i >= 0 {
			out[i] = s
			return out
		}
	}
	return append(out, s)
}

// Remove drops the saved share at the same place as l.
func Remove(list []Saved, l Location) []Saved {
	var out []Saved
	for _, s := range list {
		if sl, err := Parse(s.URL); err == nil && sl.Same(l) {
			continue
		}
		out = append(out, s)
	}
	return out
}

// Options are the mount options a session may choose: the folder name and
// read-only. Everything else (nosuid, nodev, noexec, the owner) the daemon
// sets.
type Options struct {
	Name     string
	ReadOnly bool
}

// Credentials go to the daemon through a pipe fd, as three lines: user,
// domain, password. Empty for a guest or an NFS share.
type Credentials struct {
	User, Domain, Password string
}

// Lines are the pipe's lines.
func (c Credentials) Lines() []string { return []string{c.User, c.Domain, c.Password} }

// Mounter is isotope-updated's MountShare as the session calls it.
type Mounter interface {
	MountShare(url string, o Options, c Credentials) (mountpoint string, err error)
}

// Keyring keeps share passwords in the user's keyring, keyed by the
// share's address and the user name.
type Keyring interface {
	Password(url, user string) (password string, found bool, err error)
	StorePassword(url, user, password string) error
	ForgetPassword(url, user string) error
}

// AutoMount mounts the saved shares marked auto_mount, at login. The
// network may still be coming up, so a share that fails for a network
// reason (Transient) is tried again, Tries times in all, Wait apart. Any
// other failure, above all a wrong password, stops at once: trying the same
// password again can lock a domain account. The password is read from the
// keyring in each round until it is found, since the keyring may start
// after the tray. When a share's last try fails, Notify is called once,
// with the share and the reason.
type AutoMount struct {
	Mounter Mounter
	Keyring Keyring // nil: no passwords
	Tries   int
	Wait    time.Duration
	Sleep   func(time.Duration)
	Log     func(format string, args ...any)
	// Notify tells the user that a share could not be mounted; nil tells
	// no one.
	Notify func(summary, body string)
	// Transient says whether an error is worth another try; nil is the
	// package's Transient.
	Transient func(error) bool
}

// transientWords are the network failures that may clear by themselves
// while the network comes up: mount.cifs's and mount.nfs's messages and
// the errno numbers mount.cifs prints (101 ENETUNREACH, 110 ETIMEDOUT,
// 111 ECONNREFUSED, 112 EHOSTDOWN, 113 EHOSTUNREACH), the daemon's own
// timeout, and a daemon that was restarting.
var transientWords = []string{
	"timed out", "timeout", "did not finish",
	"host is down", "no route to host", "network is unreachable", "host is unreachable",
	"connection refused", "could not resolve", "name or service not known",
	"temporary failure in name resolution", "resolve address",
	"error(101)", "error(110)", "error(111)", "error(112)", "error(113)",
	"service was stopping", "is exiting",
}

// Transient says whether err is a network failure that another try may
// clear. An authentication or permission error is not one.
func Transient(err error) bool {
	if err == nil || AuthFailure(err) {
		return false
	}
	msg := strings.ToLower(err.Error())
	for _, w := range transientWords {
		if strings.Contains(msg, w) {
			return true
		}
	}
	return false
}

// AuthFailure says whether err is the server refusing the user name or
// password.
func AuthFailure(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	for _, w := range []string{"error(13)", "permission denied", "status_logon_failure", "logon failure", "access denied", "error(126)"} {
		if strings.Contains(msg, w) {
			return true
		}
	}
	return false
}

var errNoPassword = errors.New("no saved password")

// Run mounts list's auto_mount shares and returns the mount points by URL.
func (a AutoMount) Run(list []Saved) map[string]string {
	logf := a.Log
	if logf == nil {
		logf = func(string, ...any) {}
	}
	sleep := a.Sleep
	if sleep == nil {
		sleep = time.Sleep
	}
	transient := a.Transient
	if transient == nil {
		transient = Transient
	}
	tries := max(a.Tries, 1)
	type job struct {
		s       Saved
		c       Credentials
		havePw  bool
		lastErr error
	}
	var todo []*job
	for _, s := range list {
		if s.AutoMount && s.Check() == nil {
			todo = append(todo, &job{s: s, c: Credentials{User: s.User, Domain: s.Domain}, havePw: s.User == ""})
		}
	}
	done := map[string]string{}
	failed := func(j *job, err error) {
		logf("shares: %s: %v", j.s.URL, err)
		if a.Notify == nil {
			return
		}
		why := err.Error()
		switch {
		case errors.Is(err, errNoPassword):
			why = "Its password is not in your keyring."
		case AuthFailure(err):
			why = "The server refused the user name or password."
		}
		why = strings.TrimSpace(why)
		if why != "" {
			why = strings.ToUpper(why[:1]) + why[1:]
		}
		if !strings.HasSuffix(why, ".") {
			why += "."
		}
		a.Notify("Could not mount "+j.s.Name, why+" Open Isotope System's Disks page to mount it.")
	}
	for try := 1; try <= tries && len(todo) > 0; try++ {
		if try > 1 {
			sleep(a.Wait)
		}
		var again []*job
		for _, j := range todo {
			if !j.havePw {
				if a.Keyring == nil {
					failed(j, errNoPassword)
					continue
				}
				pw, found, err := a.Keyring.Password(j.s.URL, j.s.User)
				switch {
				case err != nil:
					// The keyring may not be up yet.
					j.lastErr = fmt.Errorf("the keyring: %w", err)
					again = append(again, j)
					continue
				case !found:
					failed(j, errNoPassword)
					continue
				}
				j.c.Password, j.havePw = pw, true
			}
			mp, err := a.Mounter.MountShare(j.s.URL, Options{Name: j.s.Name}, j.c)
			if err == nil {
				done[j.s.URL] = mp
				continue
			}
			j.lastErr = err
			if !transient(err) {
				failed(j, err)
				continue
			}
			logf("shares: %s: %v (trying again)", j.s.URL, err)
			again = append(again, j)
		}
		todo = again
	}
	for _, j := range todo {
		failed(j, j.lastErr)
	}
	return done
}
