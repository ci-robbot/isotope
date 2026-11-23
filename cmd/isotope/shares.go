package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/godbus/dbus/v5"

	"github.com/kairos-community/isotope/pkg/shares"
)

// Network shares: isotope-updated mounts an SMB or NFSv4 share for the
// session that asks, at /run/media/<user>/<name> where udisks2 puts disks,
// behind polkit's io.kairos.isotope-update.shares (a local, active session).
// The saved list and the passwords are the session's (pkg/shares): the
// daemon keeps nothing. See
// docs/superpowers/specs/2026-11-24-network-shares-design.md.

// actionShares covers mounting and unmounting the caller's own network
// shares. actionSharesNFS is asked as well for an NFS share: NFS trusts
// this computer by its address (AUTH_SYS), not the user, so attaching an
// export is an administrator's choice.
const (
	actionShares    = "io.kairos.isotope-update.shares"
	actionSharesNFS = "io.kairos.isotope-update.shares-nfs"
)

var errNotAuthorizedNFS = dbus.NewError(busInterface+".Error.NotAuthorized",
	[]any{"not authorized: only an administrator can mount an NFS share, because NFS trusts this computer, not the user"})

// Where and how the shares are mounted. Variables so the tests can point
// them into a temporary directory and at fake mount and umount commands.
var (
	sharesMediaRoot = "/run/media"
	// sharesCredDir holds the CIFS credentials file for the length of one
	// mount call; the unit's RuntimeDirectory is /run/isotope-update.
	sharesCredDir = "/run/isotope-update/shares"
	// sharesPath is where mount and umount are looked for, and the only
	// variable in their environment: mount.cifs reads USER and PASSWD
	// from it, and the daemon's own must never reach it.
	sharesPath    = "/usr/sbin:/usr/bin:/sbin:/bin"
	sharesTimeout = 60 * time.Second
	// sharesGID is the primary group of a user.
	sharesGID = func(name string) (uint32, error) {
		u, err := user.Lookup(name)
		if err != nil {
			return 0, err
		}
		g, err := strconv.ParseUint(u.Gid, 10, 32)
		return uint32(g), err
	}
	sharesChown = os.Chown
	// sharesRootOwned: fi is owned by root.
	sharesRootOwned = func(fi fs.FileInfo) bool {
		st, ok := fi.Sys().(*syscall.Stat_t)
		return ok && st.Uid == 0
	}
)

// maxSharesPerUser bounds a user's mounted shares: every mount is a
// systemd .mount unit, and nothing else bounds them (udisks2 is bounded by
// the disks plugged in).
const maxSharesPerUser = 16

// sharesBusy names the users with a MountShare in progress: one at a time
// per user, so the check for a share that is already mounted, the count,
// the mkdir and the mount do not interleave. No lock is held across the
// mount call itself: a slow server keeps only its own user waiting.
var (
	sharesBusyMu sync.Mutex
	sharesBusy   = map[string]bool{}
)

// beginShareMount takes the user's mount slot, or refuses at once.
func beginShareMount(name string) (func(), error) {
	sharesBusyMu.Lock()
	defer sharesBusyMu.Unlock()
	if sharesBusy[name] {
		return nil, refuseShare("another network share is being mounted for you; try again when it is done")
	}
	sharesBusy[name] = true
	return func() {
		sharesBusyMu.Lock()
		delete(sharesBusy, name)
		sharesBusyMu.Unlock()
	}, nil
}

// errShareRefused marks a request that is not allowed or not valid, as
// opposed to a mount that failed.
var errShareRefused = errors.New("refused")

type shareRefusal struct{ error }

func (shareRefusal) Is(t error) bool { return t == errShareRefused }

func refuseShare(format string, args ...any) error {
	return shareRefusal{fmt.Errorf(format, args...)}
}

// shareFS are the mount table types of a network share.
func shareFS(fstype string) bool {
	switch fstype {
	case "cifs", "smb3", "nfs4", "nfs":
		return true
	}
	return false
}

// mountedShare is one of a user's mounted shares.
type mountedShare struct{ point, fstype, source string }

// userMediaDir is /run/media/<user>.
func userMediaDir(name string) string { return filepath.Join(sharesMediaRoot, name) }

// mountedSharesOf lists the network shares mounted right under the user's
// /run/media folder.
func mountedSharesOf(name string) []mountedShare {
	dir := userMediaDir(name)
	var out []mountedShare
	for _, m := range readMounts() {
		if shareFS(m.fstype) && filepath.Dir(m.point) == dir {
			out = append(out, mountedShare{point: m.point, fstype: m.fstype, source: m.src})
		}
	}
	return out
}

// lookIn finds an executable in the dirs of sharesPath.
func lookIn(name string) (string, error) {
	for _, d := range filepath.SplitList(sharesPath) {
		p := filepath.Join(d, name)
		if fi, err := os.Stat(p); err == nil && fi.Mode().IsRegular() && fi.Mode()&0o111 != 0 {
			return p, nil
		}
	}
	return "", fmt.Errorf("%s is not installed", name)
}

// sharesRun runs mount or umount with PATH alone in its environment, for
// at most sharesTimeout.
func sharesRun(name string, args ...string) ([]byte, error) {
	return sharesRunFor(sharesTimeout, name, args...)
}

func sharesRunFor(d time.Duration, name string, args ...string) ([]byte, error) {
	bin, err := lookIn(name)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Env = []string{"PATH=" + sharesPath}
	cmd.WaitDelay = time.Second
	out, err := cmd.CombinedOutput()
	if ctx.Err() == context.DeadlineExceeded {
		return out, fmt.Errorf("%s did not finish in %v", name, d)
	}
	return out, err
}

// cmdError is the command's own message, or its error.
func cmdError(out []byte, err error) error {
	if msg := strings.TrimSpace(string(out)); msg != "" {
		return errors.New(msg)
	}
	return err
}

// ensureUserDir makes /run/media and /run/media/<user> as udisks2 does
// (root, 0755 and 0750 with the user's group) and refuses them when they
// are links or not root's: a folder the user owned would let them swap the
// mount point for a link between the mkdir and the mount.
func ensureUserDir(name string, gid uint32) (string, error) {
	check := func(p string) error {
		fi, err := os.Lstat(p)
		if err != nil {
			return err
		}
		if !fi.IsDir() || !sharesRootOwned(fi) || fi.Mode().Perm()&0o022 != 0 {
			return fmt.Errorf("%s is not a folder that only root can change", p)
		}
		return nil
	}
	if err := os.Mkdir(sharesMediaRoot, 0o755); err != nil && !errors.Is(err, fs.ErrExist) {
		return "", err
	}
	if err := check(sharesMediaRoot); err != nil {
		return "", err
	}
	dir := userMediaDir(name)
	if err := os.Mkdir(dir, 0o750); err == nil {
		if err := sharesChown(dir, 0, int(gid)); err != nil {
			os.Remove(dir)
			return "", err
		}
	} else if !errors.Is(err, fs.ErrExist) {
		return "", err
	}
	return dir, check(dir)
}

// newMountPoint makes dir/name, or dir/name-2 and so on when it is taken.
func newMountPoint(dir, name string) (string, error) {
	for n := 1; n <= 100; n++ {
		p := filepath.Join(dir, name)
		if n > 1 {
			p += "-" + strconv.Itoa(n)
		}
		err := os.Mkdir(p, 0o700)
		if err == nil {
			return p, nil
		}
		if !errors.Is(err, fs.ErrExist) {
			return "", err
		}
	}
	return "", fmt.Errorf("no free folder name for %s in %s", name, dir)
}

// writeCredentials writes mount.cifs's credentials file, 0600 in a 0700
// root folder; the caller removes it.
func writeCredentials(c shares.Credentials) (string, error) {
	if err := os.MkdirAll(sharesCredDir, 0o700); err != nil {
		return "", err
	}
	if err := os.Chmod(sharesCredDir, 0o700); err != nil {
		return "", err
	}
	f, err := os.CreateTemp(sharesCredDir, "cred-")
	if err != nil {
		return "", err
	}
	b := []byte("username=" + c.User + "\npassword=" + c.Password + "\n")
	if c.Domain != "" {
		b = append(b, "domain="+c.Domain+"\n"...)
	}
	_, werr := f.Write(b)
	clear(b)
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		os.Remove(f.Name())
		return "", werr
	}
	return f.Name(), nil
}

// mountShare mounts raw for the user name (uid, gid) and returns the mount
// point. A share the user already has mounted is not mounted again: its
// mount point is returned.
func mountShare(name string, uid, gid uint32, raw string, opt shares.Options, cred shares.Credentials) (string, error) {
	loc, err := shares.Parse(raw)
	if err != nil {
		return "", shareRefusal{err}
	}
	folder := opt.Name
	if folder == "" {
		folder = loc.DefaultName()
	}
	if err := shares.CheckName(folder); err != nil {
		return "", shareRefusal{err}
	}
	if loc.Scheme == shares.NFS && (cred.User != "" || cred.Domain != "" || cred.Password != "") {
		return "", refuseShare("an NFS share takes no user name or password")
	}
	if err := shares.CheckCredential("user name", cred.User); err != nil {
		return "", shareRefusal{err}
	}
	if err := shares.CheckCredential("domain", cred.Domain); err != nil {
		return "", shareRefusal{err}
	}
	if cred.User == "" && (cred.Domain != "" || cred.Password != "") {
		return "", refuseShare("a password or a domain needs a user name")
	}

	end, err := beginShareMount(name)
	if err != nil {
		return "", err
	}
	defer end()
	mounted := mountedSharesOf(name)
	for _, m := range mounted {
		if l, ok := shares.FromMount(m.fstype, m.source); ok && l.Same(loc) {
			return m.point, nil
		}
	}
	if len(mounted) >= maxSharesPerUser {
		return "", refuseShare("you have %d network shares mounted, the most there can be; unmount one first", len(mounted))
	}
	dir, err := ensureUserDir(name, gid)
	if err != nil {
		return "", err
	}
	point, err := newMountPoint(dir, folder)
	if err != nil {
		return "", err
	}
	common := []string{"nosuid", "nodev", "noexec"}
	if opt.ReadOnly {
		common = append(common, "ro")
	} else {
		common = append(common, "rw")
	}
	if loc.Port != 0 {
		common = append(common, "port="+strconv.Itoa(int(loc.Port)))
	}
	if loc.Scheme == shares.SMB {
		err = mountCIFS(loc, point, uid, gid, common, cred)
	} else {
		err = mountNFS(loc, point, common)
	}
	if err != nil {
		os.Remove(point)
		return "", fmt.Errorf("could not mount %s: %w", loc, err)
	}
	return point, nil
}

func mountCIFS(loc shares.Location, point string, uid, gid uint32, common []string, cred shares.Credentials) error {
	opts := append(common, "uid="+strconv.FormatUint(uint64(uid), 10), "gid="+strconv.FormatUint(uint64(gid), 10),
		"forceuid", "forcegid", "file_mode=0600", "dir_mode=0700")
	if cred.User == "" {
		opts = append(opts, "guest", "user=guest")
	} else {
		file, err := writeCredentials(cred)
		if err != nil {
			return err
		}
		defer os.Remove(file)
		opts = append(opts, "credentials="+file)
	}
	out, err := sharesRun("mount", "-t", "cifs", "-o", strings.Join(opts, ","), loc.Source(), point)
	if err != nil {
		return cmdError(out, err)
	}
	return nil
}

// mountNFS mounts with NFSv4.2, and with 4.1 when the server does not do
// 4.2. NFSv4.0 and v3 need the server to call back in, which the firewall
// drops.
func mountNFS(loc shares.Location, point string, common []string) error {
	var first error
	for _, vers := range []string{"4.2", "4.1"} {
		opts := append([]string{"vers=" + vers}, common...)
		opts = append(opts, "retry=0")
		out, err := sharesRun("mount", "-t", "nfs4", "-o", strings.Join(opts, ","), loc.Source(), point)
		if err == nil {
			return nil
		}
		err = cmdError(out, err)
		if first == nil {
			first = err
		}
		if !strings.Contains(strings.ToLower(err.Error()), "not supported") {
			break
		}
	}
	return first
}

// unmountShare unmounts one of the user's mounted shares and removes its
// folder.
func unmountShare(name, point string) error {
	if point != filepath.Clean(point) || filepath.Dir(point) != userMediaDir(name) {
		return refuseShare("%s is not one of your network shares", point)
	}
	found := false
	for _, m := range mountedSharesOf(name) {
		found = found || m.point == point
	}
	if !found {
		return refuseShare("%s is not one of your mounted network shares", point)
	}
	if out, err := sharesRun("umount", point); err != nil {
		return fmt.Errorf("could not unmount %s: %w", point, cmdError(out, err))
	}
	os.Remove(point)
	return nil
}

// shareCaller is the user behind sender: name, uid and primary group.
func (s *service) shareCaller(sender dbus.Sender, method string) (string, uint32, uint32, *dbus.Error) {
	name, derr := s.callerName(sender, method)
	if derr != nil {
		return "", 0, 0, derr
	}
	uid, err := s.uidOf(sender)
	if err != nil {
		return "", 0, 0, errNotAuthorized
	}
	gid, err := sharesGID(name)
	if err != nil {
		fmt.Fprintf(os.Stderr, "isotope: refusing %s for %s: %v\n", method, sender, err)
		return "", 0, 0, errNotAuthorized
	}
	return name, uid, gid, nil
}

// shareOptions reads MountShare's a{sv}: name (s) and read_only (b).
func shareOptions(m map[string]dbus.Variant) (shares.Options, error) {
	var o shares.Options
	for k, v := range m {
		var ok bool
		switch k {
		case "name":
			o.Name, ok = v.Value().(string)
		case "read_only":
			o.ReadOnly, ok = v.Value().(bool)
		default:
			return o, refuseShare("unknown option %q", k)
		}
		if !ok {
			return o, refuseShare("option %q has the wrong type", k)
		}
	}
	return o, nil
}

// MountShare mounts a share for the caller and returns its mount point.
// The fd holds three lines: user, domain, password (all empty for a guest
// or an NFS share).
func (a busAPI) MountShare(sender dbus.Sender, url string, options map[string]dbus.Variant, fd dbus.UnixFD) (string, *dbus.Error) {
	done, derr := a.s.enter()
	if derr != nil {
		closeFD(fd)
		return "", derr
	}
	defer done()
	if derr := a.s.authorize(sender, actionShares); derr != nil {
		closeFD(fd)
		return "", derr
	}
	// Only the scheme is read before the NFS question; Parse checks the
	// rest after it.
	if scheme, _, ok := strings.Cut(strings.TrimSpace(url), "://"); ok && strings.EqualFold(scheme, shares.NFS) {
		if derr := a.s.authorize(sender, actionSharesNFS); derr != nil {
			closeFD(fd)
			if derr == errNotAuthorized {
				derr = errNotAuthorizedNFS
			}
			return "", derr
		}
	}
	name, uid, gid, derr := a.s.shareCaller(sender, "MountShare")
	if derr != nil {
		closeFD(fd)
		return "", derr
	}
	lines, err := readSecrets(fd)
	if err != nil {
		return "", busError(err)
	}
	defer clear(lines)
	if len(lines) != 3 {
		return "", busError(refuseShare("want three lines: the user name, the domain and the password"))
	}
	opt, err := shareOptions(options)
	if err != nil {
		return "", busError(err)
	}
	point, err := mountShare(name, uid, gid, url, opt, shares.Credentials{User: lines[0], Domain: lines[1], Password: lines[2]})
	if err != nil {
		fmt.Fprintf(os.Stderr, "isotope: MountShare for %s: %v\n", name, err)
		return "", busError(err)
	}
	return point, nil
}

func (a busAPI) UnmountShare(sender dbus.Sender, point string) *dbus.Error {
	done, derr := a.s.enter()
	if derr != nil {
		return derr
	}
	defer done()
	if derr := a.s.authorize(sender, actionShares); derr != nil {
		return derr
	}
	name, derr := a.s.callerName(sender, "UnmountShare")
	if derr != nil {
		return derr
	}
	return busError(unmountShare(name, point))
}

// MountedShares lists the caller's own mounted shares. It needs no polkit
// answer: the mount table is open to every user.
func (a busAPI) MountedShares(sender dbus.Sender) ([]map[string]dbus.Variant, *dbus.Error) {
	done, derr := a.s.enter()
	if derr != nil {
		return nil, derr
	}
	defer done()
	name, derr := a.s.callerName(sender, "MountedShares")
	if derr != nil {
		return nil, derr
	}
	out := []map[string]dbus.Variant{}
	for _, m := range mountedSharesOf(name) {
		out = append(out, map[string]dbus.Variant{
			"mountpoint": dbus.MakeVariant(m.point),
			"fstype":     dbus.MakeVariant(m.fstype),
			"source":     dbus.MakeVariant(m.source),
		})
	}
	return out, nil
}

// sharesLogoutTimeout bounds each umount at logout.
var sharesLogoutTimeout = 15 * time.Second

// unmountSharesAtLogout unmounts every network share under the user's
// /run/media folder and removes the folders: isotope-shares-logout@.service
// runs it as root (`isotope-update daemon --unmount-shares UID`) when the
// user's last session ends and user@UID.service stops. No polkit: the
// session is gone by then. The unmounts are lazy, so a share in use is
// detached at once and let go when its last file closes, and NFS is also
// forced, so a server that is gone (a laptop off its network) cannot hang
// the logout.
func unmountSharesAtLogout(name string, stderr io.Writer) error {
	var first error
	for _, m := range mountedSharesOf(name) {
		args := []string{"-l", m.point}
		if strings.HasPrefix(m.fstype, "nfs") {
			args = []string{"-f", "-l", m.point}
		}
		if out, err := sharesRunFor(sharesLogoutTimeout, "umount", args...); err != nil {
			err = fmt.Errorf("could not unmount %s: %w", m.point, cmdError(out, err))
			fmt.Fprintln(stderr, "isotope:", err)
			if first == nil {
				first = err
			}
			continue
		}
		os.Remove(m.point)
	}
	return first
}

// cmdUnmountShares is `isotope-update daemon --unmount-shares UID`.
func cmdUnmountShares(uid string, stderr io.Writer) int {
	n, err := strconv.ParseUint(uid, 10, 32)
	if err != nil {
		fmt.Fprintf(stderr, "isotope daemon: --unmount-shares wants a uid, not %q\n", uid)
		return exitUsage
	}
	name, err := userName(uint32(n))
	if err != nil || name == "" {
		fmt.Fprintf(stderr, "isotope daemon: no user with uid %d: %v\n", n, err)
		return exitError
	}
	if err := unmountSharesAtLogout(name, stderr); err != nil {
		return exitError
	}
	return exitOK
}
