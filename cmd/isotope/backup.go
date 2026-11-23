package main

// Backup and restore of a whole machine. On Kairos everything that survives
// a reboot is on the persistent partition, mounted at /usr/local (each
// persistent path is a bind mount of /usr/local/.state/<path>.bind), and in
// /oem (the installer's users, hostname and keyboard). A backup is those
// two, without caches, logs and, unless asked, container storage. The
// backups are restic repositories. See
// docs/superpowers/specs/2026-11-16-isotope-cli-backup-design.md.

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"os/signal"
	"os/user"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"

	"github.com/kairos-community/isotope/pkg/recoverykey"
)

// backupPaths are the places a backup reads and a restore writes. A struct,
// so tests can point every one of them into a temporary directory.
type backupPaths struct {
	Persist    string // /usr/local: the persistent partition
	OEM        string // /oem: the installer's cloud-config
	State      string // /usr/local/isotope-backup: config, password, env, manifest
	Run        string // /run/isotope-backup: a restore's typed password (tmpfs)
	Release    string // /etc/isotope/release
	Encryption string // /etc/isotope/encryption, the installer's marker
	ExtDir     string // where each extension came from (extStateDir)
}

// defaultBackupPaths is a variable so tests can move every path. State is on
// the persistent partition itself: /var is rebuilt at every boot.
var defaultBackupPaths = func() backupPaths {
	return backupPaths{
		Persist:    "/usr/local",
		OEM:        "/oem",
		State:      "/usr/local/isotope-backup",
		Run:        "/run/isotope-backup",
		Release:    releaseFile(),
		Encryption: "/etc/isotope/encryption",
		ExtDir:     extStateDir(),
	}
}

// bindDir is where Kairos keeps a persistent path: /home is
// /usr/local/.state/home.bind, /var/lib/docker .state/var-lib-docker.bind.
func (p backupPaths) bindDir(path string) string {
	name := strings.ReplaceAll(strings.Trim(path, "/"), "/", "-")
	return filepath.Join(p.Persist, ".state", name+".bind")
}

// homeState is /home as the persistent partition has it. A restore moves
// files there, not into /home: /home is another mount of it, and a rename
// cannot cross mounts.
func (p backupPaths) homeState() string { return p.bindDir("/home") }

func (p backupPaths) config() string   { return filepath.Join(p.State, "config") }
func (p backupPaths) password() string { return filepath.Join(p.State, "password") }
func (p backupPaths) envFile() string  { return filepath.Join(p.State, "env") }
func (p backupPaths) manifest() string { return filepath.Join(p.State, "manifest") }

// pending is what a whole restore leaves for `isotope restore --finish`;
// finishResult is what that run could not install.
func (p backupPaths) pending() string      { return filepath.Join(p.State, "pending") }
func (p backupPaths) finishResult() string { return filepath.Join(p.State, "restore-result") }

// extOnPersist is kairos-agent's extension directory, /var/lib/kairos/
// extensions, on the persistent partition, relative to it.
func (p backupPaths) extOnPersist() string {
	rel, _ := filepath.Rel(p.Persist, p.bindDir("/var/lib/kairos"))
	return filepath.Join(rel, "extensions")
}

// stage is where a restore puts the files first: on the persistent
// partition, so moving them into place is a rename. Root's, mode 0700.
func (p backupPaths) stage() string { return filepath.Join(p.Persist, ".isotope-restore") }

// mountinfoPath is read to map a path to the persistent partition. A
// variable for the tests.
var mountinfoPath = "/proc/self/mountinfo"

// mountEntry is a mountinfo line: the device number, the folder of the file
// system that is mounted, the mount point and the mount source.
type mountEntry struct{ dev, root, point, src string }

func readMounts() []mountEntry {
	b, err := os.ReadFile(mountinfoPath)
	if err != nil {
		return nil
	}
	var out []mountEntry
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) < 5 {
			continue
		}
		m := mountEntry{dev: f[2], root: unescapeMount(f[3]), point: unescapeMount(f[4])}
		// After the optional fields and "-": the type and the source.
		for i := 5; i+2 < len(f); i++ {
			if f[i] == "-" {
				m.src = unescapeMount(f[i+2])
				break
			}
		}
		out = append(out, m)
	}
	return out
}

// unescapeMount undoes mountinfo's octal escapes (\040 is a space).
func unescapeMount(s string) string {
	if !strings.Contains(s, "\\") {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+3 < len(s) {
			if n, err := strconv.ParseUint(s[i+1:i+4], 8, 8); err == nil {
				b.WriteByte(byte(n))
				i += 3
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

func under(path, dir string) bool {
	return path == dir || strings.HasPrefix(path, strings.TrimSuffix(dir, "/")+"/")
}

// onPersist maps a path the user sees (/home/anna/x, /root/backups) to where
// it is on the persistent partition (/usr/local/.state/home.bind/anna/x), so
// the backup can exclude it. A path on another file system is returned as it
// is: the backup does not read it anyway.
func (p backupPaths) onPersist(path string) string {
	mounts := readMounts()
	var persist *mountEntry
	for i, m := range mounts {
		if m.point == p.Persist {
			persist = &mounts[i]
		}
	}
	if persist == nil || under(path, p.Persist) {
		return path
	}
	// The mount the path is in: the longest mount point above it; the last
	// one listed when the same point is mounted over.
	var best *mountEntry
	for i, m := range mounts {
		if under(path, m.point) && (best == nil || len(m.point) >= len(best.point)) {
			best = &mounts[i]
		}
	}
	if best == nil || best.dev != persist.dev {
		return path
	}
	rel := strings.TrimPrefix(strings.TrimPrefix(path, best.point), "/")
	return filepath.Join(p.Persist, strings.TrimPrefix(best.root, persist.root), rel)
}

// restic is the backup engine. ISOTOPE_RESTIC replaces it, so a test can
// record what would have been run.
func restic() string {
	if p := os.Getenv("ISOTOPE_RESTIC"); p != "" {
		return p
	}
	return "/usr/bin/restic"
}

// sysCommand builds every command a backup or restore runs (restic,
// flatpak, runuser, systemctl). A variable so tests can see them.
var sysCommand = exec.Command

// flatpakPresent says whether this edition has Flatpak (the server has not).
var flatpakPresent = func() bool { _, err := exec.LookPath("flatpak"); return err == nil }

// backupNow is the clock of the schedule. A variable for the tests.
var backupNow = time.Now

// Snapshots older than this many backups are forgotten after each backup.
var keepPolicy = []string{"--keep-daily", "7", "--keep-weekly", "4", "--keep-monthly", "12"}

// The parts of a home that are caches or can be downloaded again. Flatpak
// apps are installed again from the manifest.
var homeExcludes = []string{
	".cache",
	".local/share/Trash",
	".local/share/flatpak",
	".var/app/*/cache",
}

// Container storage: large, and not consistent while it runs. In the backup
// only with --with-containers.
var containerBinds = []string{"/var/lib/docker", "/var/lib/containerd"}

const homeContainers = ".local/share/containers"

// backupConfig is /var/lib/isotope-backup/config. It holds no secret, so
// `isotope status` can read it as any user.
type backupConfig struct {
	Target     string
	Label      string // the name the user knows the target by: a USB disk's label
	Schedule   string // off, daily, weekly
	Containers bool   // also Docker's and Podman's storage
	Last       int64  // Unix time of the last successful backup
	Result     string // ok, or the error of the last attempt
}

func loadBackupConfig(p backupPaths) (backupConfig, error) {
	c := backupConfig{Schedule: "off"}
	b, err := os.ReadFile(p.config())
	if errors.Is(err, os.ErrNotExist) {
		return c, nil
	}
	if err != nil {
		return c, err
	}
	for _, line := range strings.Split(string(b), "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok {
			continue
		}
		switch k {
		case "target":
			c.Target = v
		case "label":
			c.Label = v
		case "schedule":
			if v == "daily" || v == "weekly" || v == "off" {
				c.Schedule = v
			}
		case "containers":
			c.Containers = v == "true"
		case "last":
			c.Last, _ = strconv.ParseInt(v, 10, 64)
		case "result":
			c.Result = v
		}
	}
	return c, nil
}

func saveBackupConfig(p backupPaths, c backupConfig) error {
	if err := os.MkdirAll(p.State, 0o755); err != nil {
		return err
	}
	s := fmt.Sprintf("target=%s\nlabel=%s\nschedule=%s\ncontainers=%t\nlast=%d\nresult=%s\n",
		c.Target, oneLine(c.Label), c.Schedule, c.Containers, c.Last, oneLine(c.Result))
	tmp := p.config() + ".tmp"
	if err := os.WriteFile(tmp, []byte(s), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, p.config())
}

// The restic repository schemes a target may use besides a directory.
var urlSchemes = []string{"sftp:", "rest:", "s3:", "b2:", "azure:", "gs:", "swift:", "rclone:"}

// isURLTarget says whether a target is a restic URL rather than a directory.
func isURLTarget(t string) bool {
	for _, sc := range urlSchemes {
		if strings.HasPrefix(t, sc) {
			return true
		}
	}
	return false
}

// normalizeTarget checks a target the user typed: an absolute directory
// (also written local:/path), a folder on a disk by UUID
// (uuid:<UUID>/<path>) or a restic URL. A relative path is refused: restic
// would put the backup in the current directory, on the internal disk.
func normalizeTarget(t string) (string, error) {
	t = strings.TrimPrefix(t, "local:")
	if strings.ContainsFunc(t, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
		return "", errors.New("the target has a control character")
	}
	if strings.HasPrefix(t, "uuid:") {
		uuid, sub, ok := parseUUIDTarget(t)
		if !ok {
			return "", fmt.Errorf("%q is not uuid:<UUID>/<folder>", t)
		}
		return "uuid:" + uuid + "/" + sub, nil
	}
	if isURLTarget(t) {
		return t, nil
	}
	if !filepath.IsAbs(t) {
		return "", fmt.Errorf("%q is not an absolute path or a restic URL (%s...)", t, strings.Join(urlSchemes, ", "))
	}
	return filepath.Clean(t), nil
}

// onTmpfs says whether a directory is on a tmpfs, such as /run, which is
// empty again after a reboot. A variable for the tests.
var onTmpfs = func(dir string) bool {
	var st unix.Statfs_t
	return unix.Statfs(dir, &st) == nil && st.Type == unix.TMPFS_MAGIC
}

// interruptHook is what a long job does on a signal: onInterrupt for the
// CLI, nil in the daemon. The daemon must never exit in the middle of a
// job; it stops a job through its context, and the job cleans up on the
// way out.
type interruptHook func(clean func()) (stop func())

// install runs h, or nothing when h is nil.
func (h interruptHook) install(clean func()) (stop func()) {
	if h == nil {
		return func() {}
	}
	return h(clean)
}

// signalNotify is signal.Notify. A variable so the tests can see who asks
// for signals.
var signalNotify = signal.Notify

// onInterrupt runs fn and exits on SIGINT, SIGTERM or SIGHUP, until the returned
// stop is called. restic gets the signal too, as it is in the same process
// group, and stops. Only for the CLI: it exits the process.
func onInterrupt(fn func()) (stop func()) {
	ch := make(chan os.Signal, 1)
	done := make(chan struct{})
	// SIGHUP too: a dropped SSH session sends it.
	signalNotify(ch, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	go func() {
		select {
		case <-ch:
			fn()
			os.Exit(130)
		case <-done:
		}
	}()
	return func() { signal.Stop(ch); close(done) }
}

// resticCmd runs restic against target with the password in passFile and
// the credentials in this machine's env file.
func resticCmd(p backupPaths, target, passFile string, args ...string) (*exec.Cmd, error) {
	return resticCmdEnv(p, target, passFile, p.envFile(), args...)
}

// resticCmdEnv is resticCmd with the credentials (NAME=value lines) in
// envFile. An SFTP target whose host the backup's known_hosts has (set up
// by initTarget) uses the backup's own SSH key and known_hosts; any other,
// such as one set up by hand before, root's own ssh setup.
func resticCmdEnv(p backupPaths, target, passFile, envFile string, args ...string) (*exec.Cmd, error) {
	return resticCmdHosts(p, target, passFile, envFile, "", args...)
}

// resticCmdHosts is resticCmdEnv with the SFTP hosts in knownHosts, with
// the backup's own key; knownHosts "" is resticCmdEnv's choice.
func resticCmdHosts(p backupPaths, target, passFile, envFile, knownHosts string, args ...string) (*exec.Cmd, error) {
	switch {
	case knownHosts != "":
		args = append(sftpArgsWith(p, knownHosts), args...)
	case usesBackupSSH(p, target):
		args = append(sftpArgs(p), args...)
	}
	c := sysCommand(restic(), args...)
	env := append(os.Environ(),
		"RESTIC_REPOSITORY="+target,
		"RESTIC_PASSWORD_FILE="+passFile,
		"RESTIC_CACHE_DIR="+filepath.Join(p.State, "cache"))
	b, err := os.ReadFile(envFile)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if k, _, ok := strings.Cut(line, "="); ok && k != "" && !strings.HasPrefix(line, "#") {
			env = append(env, line)
		}
	}
	c.Env = env
	return c, nil
}

const backupUsage = `Usage: isotope backup [command] [flags]

Back up this machine: everything that survives a reboot, which is the
persistent partition (/usr/local: the homes, SSH keys, system extensions,
settings) and /oem (users, hostname, keyboard), without caches, logs and
Flatpak apps (the list of apps is kept; a restore installs them again).
The backup is encrypted. Runs as root through sudo; only members of the
admin group may use it.

Commands:
  now                    back up now to the target (default)
      --export FILE      instead, write the backup to one file, to copy or
                         keep anywhere; needs no target. "isotope restore
                         --from FILE" restores it
      --with-containers  also Docker's and Podman's images and volumes
  status                 the target, the schedule and the last backup
  init TARGET            back up to TARGET from now on: a directory, such as
                         a folder on a USB disk, a folder on a disk by its
                         UUID (uuid:UUID/folder), or a restic URL (sftp:...,
                         rest:..., s3:...). Creates the backup there with a
                         new password, which is shown once
      --password-stdin   read the password instead: for a backup that
                         already exists, or a password of your own
      --with-containers  include Docker's and Podman's storage from now on
      --host-key SHA256:...
                         an SFTP server's host key, as checked with the
                         server. Without it, init shows the fingerprints and
                         the key to add to the server's authorized_keys
  list                   the backups in the target
  schedule daily|weekly|off
                         back up automatically. A USB disk that is not
                         plugged in is skipped, not an error

Restore with "isotope restore".
`

func cmdBackup(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	sub := "now"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		sub, args = args[0], args[1:]
	}
	if sub == "help" || sub == "-h" || sub == "--help" {
		fmt.Fprint(stdout, backupUsage)
		return exitOK
	}
	p := defaultBackupPaths()
	if sub == "status" {
		return backupStatus(p, stdout, stderr)
	}
	if !exists(restic()) {
		fmt.Fprintln(stderr, "isotope: backup is not available on this edition (no restic).")
		return exitError
	}
	if needRoot() {
		if err := asRoot(append([]string{"backup", sub}, args...)...).Run(); err != nil {
			var ee *exec.ExitError
			if errors.As(err, &ee) {
				return ee.ExitCode()
			}
			fmt.Fprintln(stderr, "isotope:", err)
			return exitError
		}
		return exitOK
	}
	fs := newFlags("backup "+sub, stderr)
	passStdin := fs.Bool("password-stdin", false, "")
	scheduled := fs.Bool("scheduled", false, "")
	export := fs.String("export", "", "")
	containers := fs.Bool("with-containers", false, "")
	hostKey := fs.String("host-key", "", "")
	rest, err := parseInterspersed(fs, args)
	if err != nil {
		return exitUsage
	}
	in := bufio.NewReader(stdin)
	switch sub {
	case "now":
		if len(rest) != 0 {
			break
		}
		if *export != "" {
			return backupExport(p, *export, *containers, *passStdin, in, stdout, stderr)
		}
		return backupRun(p, *scheduled, *containers, stdout, stderr)
	case "init":
		if len(rest) != 1 {
			break
		}
		return backupInit(p, rest[0], *hostKey, *passStdin, *containers, in, stdout, stderr)
	case "list":
		if len(rest) != 0 {
			break
		}
		return backupList(p, stdout, stderr)
	case "schedule":
		if len(rest) != 1 {
			break
		}
		return backupSchedule(p, rest[0], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "isotope backup: unknown command %q\n\n%s", sub, backupUsage)
		return exitUsage
	}
	fmt.Fprint(stderr, backupUsage)
	return exitUsage
}

func backupStatus(p backupPaths, stdout, stderr io.Writer) int {
	if r := strings.TrimSpace(readFileString(p.finishResult())); r != "" && r != "ok" {
		fmt.Fprintf(stdout, "Restore:    after the restart, %s\n", r)
	}
	c, err := loadBackupConfig(p)
	if err != nil {
		fmt.Fprintln(stderr, "isotope:", err)
		return exitError
	}
	if c.Target == "" {
		fmt.Fprintln(stdout, "No backup target. Run `isotope backup init TARGET` to set one, for example a folder on a USB disk.")
		return exitOK
	}
	fmt.Fprintf(stdout, "Target:     %s\n", targetName(c))
	fmt.Fprintf(stdout, "Schedule:   %s\n", c.Schedule)
	if c.Last == 0 {
		fmt.Fprintln(stdout, "Last:       never")
	} else {
		fmt.Fprintf(stdout, "Last:       %s\n", time.Unix(c.Last, 0).Format("2006-01-02 15:04"))
	}
	switch {
	case strings.HasPrefix(c.Result, "partial: "):
		fmt.Fprintf(stdout, "Last try:   %s\n", c.Result)
	case c.Result != "" && c.Result != "ok":
		fmt.Fprintf(stdout, "Last try:   failed: %s\n", c.Result)
	}
	if w := backupWarning(c); w != "" {
		fmt.Fprintln(stdout, w)
	}
	return exitOK
}

// backupWarning is the line `isotope status` adds when automatic backups
// have stopped working.
func backupWarning(c backupConfig) string {
	if c.Target == "" || c.Schedule == "off" {
		return ""
	}
	if c.Last == 0 || backupNow().Sub(time.Unix(c.Last, 0)) > 14*24*time.Hour {
		return "Backup:     no backup in the last 14 days; run `isotope backup` to see why."
	}
	return ""
}

func backupSchedule(p backupPaths, s string, stdout, stderr io.Writer) int {
	c, err := setBackupSchedule(p, s)
	var bad errBadSchedule
	if errors.As(err, &bad) {
		fmt.Fprintf(stderr, "isotope backup schedule: %v\n", err)
		return exitUsage
	}
	if err != nil {
		fmt.Fprintln(stderr, "isotope:", err)
		return exitError
	}
	if s == "off" {
		fmt.Fprintln(stdout, "Automatic backups are off.")
	} else {
		fmt.Fprintf(stdout, "This machine backs up %s to %s.\n", s, targetName(c))
	}
	return exitOK
}

// errBadSchedule is a schedule that is not daily, weekly or off.
type errBadSchedule string

func (e errBadSchedule) Error() string {
	return fmt.Sprintf("%q is not daily, weekly or off", string(e))
}

// setBackupSchedule is `isotope backup schedule` and the daemon's
// SetBackupSchedule: it changes the schedule in the config and returns it.
func setBackupSchedule(p backupPaths, s string) (backupConfig, error) {
	if s != "daily" && s != "weekly" && s != "off" {
		return backupConfig{}, errBadSchedule(s)
	}
	c, err := loadBackupConfig(p)
	if err == nil && c.Target == "" && s != "off" {
		err = errors.New("there is no backup target; run `isotope backup init TARGET` first")
	}
	if err == nil {
		c.Schedule = s
		err = saveBackupConfig(p, c)
	}
	return c, err
}

// stopGrace is how long restic has to stop after an interrupt before it is
// killed. On an interrupt restic removes its lock from the repository.
var stopGrace = 10 * time.Second

// runCtx runs cmd and stops it when ctx is cancelled: first an interrupt,
// then a kill after stopGrace. A cancelled run returns ctx's error.
func runCtx(ctx context.Context, cmd *exec.Cmd) error {
	// The output may go through a pipe that a child of restic keeps open.
	cmd.WaitDelay = stopGrace
	if err := cmd.Start(); err != nil {
		return err
	}
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = cmd.Process.Signal(os.Interrupt)
			select {
			case <-time.After(stopGrace):
				_ = cmd.Process.Kill()
			case <-done:
			}
		case <-done:
		}
	}()
	err := cmd.Wait()
	close(done)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}

// targetPresent says whether a directory target holds a restic repository
// now. A USB disk that is not plugged in leaves the mount point missing or
// empty; restic must then not be pointed at it, or it would write to the
// internal disk. A uuid: target is present when its disk is plugged in;
// whether the folder holds a backup is known only once it is mounted.
func targetPresent(target string) bool {
	if uuid, _, ok := parseUUIDTarget(target); ok {
		return diskPresent(uuid)
	}
	return isURLTarget(target) || exists(filepath.Join(target, "config"))
}

// scheduledSkip says why a scheduled backup does nothing, or "" when it is
// due.
func scheduledSkip(c backupConfig) string {
	switch {
	case c.Target == "":
		return "no backup target"
	case c.Schedule == "off":
		return "automatic backups are off"
	case c.Schedule == "weekly" && c.Last != 0 && backupNow().Sub(time.Unix(c.Last, 0)) < 7*24*time.Hour:
		return "the last backup is less than a week old"
	case strings.HasPrefix(c.Target, "uuid:") && !targetPresent(c.Target):
		return diskName(c) + " is not plugged in"
	case !targetPresent(c.Target):
		return c.Target + " is not there (a disk that is not plugged in?)"
	}
	return ""
}

func backupRun(p backupPaths, scheduled, containers bool, stdout, stderr io.Writer) int {
	skip, err := runConfiguredBackup(context.Background(), p, scheduled, containers, stdout, stderr)
	var refused errBackupRefused
	switch {
	case errors.As(err, &refused):
		fmt.Fprintln(stderr, "isotope:", err)
		return exitError
	case errors.Is(err, errPartial):
		fmt.Fprintf(stderr, "isotope: backed up, but %v.\n", err)
		return exitOK
	case err != nil:
		fmt.Fprintln(stderr, "isotope: the backup failed:", err)
		return exitError
	case skip != "":
		fmt.Fprintln(stdout, "No backup:", skip+".")
		return exitOK
	}
	fmt.Fprintln(stdout, "Backed up.")
	return exitOK
}

// errBackupRefused is a backup that did not start: the config cannot be
// read, there is no target, or its disk is not plugged in.
type errBackupRefused struct{ msg string }

func (e errBackupRefused) Error() string { return e.msg }

// runConfiguredBackup is one backup to the configured target, as
// `isotope backup` (as root) and the daemon's BackupNow run it, with the
// outcome recorded in the config. A scheduled run that is not due returns
// why in skip and does nothing. errPartial means the snapshot was written
// without some files. Cancelling ctx stops restic.
func runConfiguredBackup(ctx context.Context, p backupPaths, scheduled, containers bool, stdout, stderr io.Writer) (skip string, err error) {
	c, err := loadBackupConfig(p)
	if err != nil {
		return "", errBackupRefused{err.Error()}
	}
	if scheduled {
		if why := scheduledSkip(c); why != "" {
			return why, nil
		}
	} else {
		if c.Target == "" {
			return "", errBackupRefused{"there is no backup target; run `isotope backup init TARGET` first."}
		}
	}
	// A disk by UUID is mounted when it is not, and only when it is
	// plugged in: never a bare mount point on the internal disk.
	repo, release, err := resolveTarget(c.Target)
	switch {
	case errors.Is(err, errDiskMissing) && scheduled:
		return diskName(c) + " is not plugged in", nil
	case errors.Is(err, errDiskMissing):
		return "", errBackupRefused{diskName(c) + " is not plugged in."}
	case err != nil:
		return "", errBackupRefused{err.Error()}
	}
	defer release()
	if !targetPresent(repo) {
		if scheduled {
			return repo + " is not there (a disk that is not plugged in?)", nil
		}
		return "", errBackupRefused{fmt.Sprintf("there is no backup in %s. Is the disk plugged in?", repo)}
	}
	err = backupOnce(ctx, p, repo, p.password(), c.Containers || containers, true, stdout, stderr)
	switch {
	case errors.Is(err, context.Canceled):
		c.Result = "Cancelled"
	case errors.Is(err, errPartial):
		c.Result, c.Last = "partial: "+err.Error(), backupNow().Unix()
	case err != nil:
		c.Result = err.Error()
	default:
		c.Result, c.Last = "ok", backupNow().Unix()
	}
	if serr := saveBackupConfig(p, c); serr != nil && err == nil {
		err = serr
	}
	return "", err
}

// backupArgs are restic's arguments for one backup of this machine.
func backupArgs(p backupPaths, target string, containers bool) []string {
	host, _ := os.Hostname()
	args := []string{"backup", "--tag", "isotope", "--host", host, "--exclude-caches"}
	ex := func(path string) { args = append(args, "--exclude", path) }
	for _, x := range homeExcludes {
		ex(filepath.Join(p.homeState(), "*", x))
	}
	if !containers {
		ex(filepath.Join(p.homeState(), "*", homeContainers))
		for _, b := range containerBinds {
			ex(p.bindDir(b))
		}
	}
	ex(p.bindDir("/var/log"))
	ex(filepath.Join(p.Persist, "lost+found"))
	// What a backup must not hold: its own cache and secrets, a restore's
	// staging directory, and a target or export on this disk, which would
	// otherwise be backed up into itself.
	for _, f := range []string{"cache", "password", "password.new", "env", "ssh", "work-*", "pending"} {
		ex(filepath.Join(p.State, f))
	}
	ex(p.stage())
	if !isURLTarget(target) {
		ex(p.onPersist(target))
	}
	args = append(args, p.Persist)
	if exists(p.OEM) {
		args = append(args, p.OEM)
	}
	return args
}

// backupOnce writes the manifest and backs up to target. prune forgets the
// old snapshots of this host afterwards. Cancelling ctx stops restic.
func backupOnce(ctx context.Context, p backupPaths, target, passFile string, containers, prune bool, stdout, stderr io.Writer) error {
	if !exists(p.homeState()) {
		return fmt.Errorf("%s is missing: this is not an installed Isotope system", p.homeState())
	}
	if err := writeManifest(p); err != nil {
		return fmt.Errorf("writing the manifest: %w", err)
	}
	cmd, err := resticCmd(p, target, passFile, backupArgs(p, target, containers)...)
	if err != nil {
		return err
	}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	var partial error
	if err := runCtx(ctx, cmd); err != nil {
		// Exit 3: some files could not be read, and the snapshot of the
		// others was still written. Keep it and prune as usual.
		var ee *exec.ExitError
		if !errors.As(err, &ee) || ee.ExitCode() != 3 {
			return fmt.Errorf("restic backup: %w", err)
		}
		partial = errPartial
	}
	if !prune {
		return partial
	}
	host, _ := os.Hostname()
	forget := append([]string{"forget", "--tag", "isotope", "--host", host, "--prune"}, keepPolicy...)
	if cmd, err = resticCmd(p, target, passFile, forget...); err != nil {
		return err
	}
	cmd.Stdout, cmd.Stderr = io.Discard, stderr
	if err := runCtx(ctx, cmd); err != nil {
		return fmt.Errorf("restic forget: %w", err)
	}
	return partial
}

// errPartial is a backup that left out files it could not read.
var errPartial = errors.New("some files could not be read and are not in the backup")

// manifestUser is one line of manifest/users.
type manifestUser struct {
	Name string
	UID  int
}

// homeUsers are the users with a home in /home: every directory there that
// is not hidden, named after its owner.
func homeUsers(home string) ([]manifestUser, error) {
	entries, err := os.ReadDir(home)
	if err != nil {
		return nil, err
	}
	var out []manifestUser
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		u := manifestUser{Name: e.Name(), UID: -1}
		if info, err := e.Info(); err == nil {
			u.UID = fileUID(info)
		}
		out = append(out, u)
	}
	return out, nil
}

func fileUID(info fs.FileInfo) int {
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		return int(st.Uid)
	}
	return -1
}

// diskLayout says how this machine boots and is encrypted: the installer's
// marker, Trusted Boot, and whether the persistent and the OEM partitions
// are LUKS. A whole-machine restore copies /oem, which only works on a disk
// set up the same way. A variable for the tests.
var diskLayout = func(p backupPaths) string {
	uki := strings.Contains(readFileString("/proc/cmdline"), "rd.immucore.uki")
	return fmt.Sprintf("encryption=%s uki=%t persistent-luks=%t oem-luks=%t",
		encryptionMode(p), uki, onLUKS(p.Persist), onLUKS(p.OEM))
}

func readFileString(path string) string {
	b, _ := os.ReadFile(path)
	return string(b)
}

// onLUKS says whether path is on a dm-crypt device.
func onLUKS(path string) bool {
	var st unix.Stat_t
	if unix.Stat(path, &st) != nil {
		return false
	}
	uuid := readFileString(fmt.Sprintf("/sys/dev/block/%d:%d/dm/uuid", unix.Major(st.Dev), unix.Minor(st.Dev)))
	return strings.HasPrefix(uuid, "CRYPT-")
}

// encryptionMode is the mode= of the installer's encryption marker, or
// "none".
func encryptionMode(p backupPaths) string {
	b, err := os.ReadFile(p.Encryption)
	if err != nil {
		return "none"
	}
	for _, line := range strings.Split(string(b), "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), "mode="); ok && v != "" {
			return v
		}
	}
	return "none"
}

// writeManifest writes what a restore needs to know besides the files.
func writeManifest(p backupPaths) error {
	dir := p.manifest()
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if b, err := os.ReadFile(p.Release); err == nil {
		if err := os.WriteFile(filepath.Join(dir, "release"), b, 0o644); err != nil {
			return err
		}
	}
	users, err := homeUsers(p.homeState())
	if err != nil {
		return err
	}
	var ub strings.Builder
	for _, u := range users {
		fmt.Fprintf(&ub, "%s %d\n", u.Name, u.UID)
	}
	if err := os.WriteFile(filepath.Join(dir, "users"), []byte(ub.String()), 0o644); err != nil {
		return err
	}
	// The apps, when this edition has Flatpak. A failed listing leaves the
	// file out rather than failing the backup of the files.
	if flatpakPresent() {
		if out, err := sysCommand("flatpak", "list", "--system", "--app", "--columns=application,origin").Output(); err == nil {
			_ = os.WriteFile(filepath.Join(dir, "flatpak-system"), out, 0o644)
		}
		for _, u := range users {
			out, err := sysCommand("runuser", "-u", u.Name, "--", "flatpak", "list", "--user", "--app", "--columns=application,origin").Output()
			if err == nil && len(strings.TrimSpace(string(out))) > 0 {
				_ = os.WriteFile(filepath.Join(dir, "flatpak-user-"+u.Name), out, 0o644)
			}
		}
	}
	// How the disk boots and is encrypted: a whole-machine restore brings
	// back /oem, which must match the new disk.
	if err := os.WriteFile(filepath.Join(dir, "layout"), []byte(diskLayout(p)+"\n"), 0o644); err != nil {
		return err
	}
	if host, err := os.Hostname(); err == nil {
		if err := os.WriteFile(filepath.Join(dir, "hostname"), []byte(host+"\n"), 0o644); err != nil {
			return err
		}
	}
	// The extensions: the file name, where it came from, and whether it is
	// enabled at boot.
	list, err := listInstalled()
	if err != nil {
		return err
	}
	var xb strings.Builder
	for _, x := range list {
		if x.Source == "" {
			continue
		}
		fmt.Fprintf(&xb, "%s %s %t\n", x.File, x.Source, x.Enabled)
	}
	return os.WriteFile(filepath.Join(dir, "extensions"), []byte(xb.String()), 0o644)
}

func backupList(p backupPaths, stdout, stderr io.Writer) int {
	c, err := loadBackupConfig(p)
	if err == nil && c.Target == "" {
		err = errors.New("there is no backup target; run `isotope backup init TARGET` first")
	}
	if err != nil {
		fmt.Fprintln(stderr, "isotope:", err)
		return exitError
	}
	repo, release, err := resolveTarget(c.Target)
	if err != nil {
		fmt.Fprintln(stderr, "isotope:", err)
		return exitError
	}
	defer release()
	cmd, err := resticCmd(p, repo, p.password(), "snapshots", "--tag", "isotope")
	if err != nil {
		fmt.Fprintln(stderr, "isotope:", err)
		return exitError
	}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	if err := cmd.Run(); err != nil {
		fmt.Fprintln(stderr, "isotope: could not list the backups:", err)
		return exitError
	}
	return exitOK
}

// writeSecret writes a 0600 file that never exists with wider permissions.
func writeSecret(path, s string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	_ = os.Remove(tmp)
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.WriteString(s + "\n"); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func backupInit(p backupPaths, target, hostKey string, passStdin, containers bool, in *bufio.Reader, stdout, stderr io.Writer) int {
	fail := func(err error) int {
		fmt.Fprintln(stderr, "isotope:", err)
		return exitError
	}
	target, err := normalizeTarget(target)
	if err != nil {
		return fail(err)
	}
	opt := initOptions{Containers: containers}
	if host, port, ok := parseSFTPTarget(target); ok {
		if opt.HostKey, err = cliHostKey(p, host, port, hostKey, stdout); err != nil {
			return fail(err)
		}
	}
	// Before the password is asked: whether the disk is there, and whether
	// it holds a backup already. A disk by UUID stays mounted until the end.
	if !isURLTarget(target) {
		repo, release, err := resolveTarget(target)
		if err != nil {
			return fail(err)
		}
		defer release()
		parent := filepath.Dir(repo)
		if _, sub, ok := parseUUIDTarget(target); ok {
			parent = strings.TrimSuffix(repo, "/"+sub)
		}
		if err := checkOnDisk(parent); err != nil {
			return fail(err)
		}
		opt.Existing = targetPresent(repo)
	}
	if opt.Existing && !passStdin {
		fmt.Fprintf(stderr, "isotope: %s already holds a backup; give its password with --password-stdin.\n", target)
		return exitError
	}
	password, err := newPassword(passStdin, in, stdout)
	if err != nil {
		return fail(err)
	}
	if passStdin && isURLTarget(target) {
		// A URL target given a password may hold a backup or not: open it,
		// and create it when that fails.
		opt.Existing = true
		if err = initTarget(p, target, opt, password, nil); err != nil {
			opt.Existing = false
			err = initTarget(p, target, opt, password, nil)
		}
	} else {
		err = initTarget(p, target, opt, password, nil)
	}
	if err != nil {
		return fail(err)
	}
	c, _ := loadBackupConfig(p)
	fmt.Fprintf(stdout, "This machine now backs up to %s. Run `isotope backup now` for the first backup", targetName(c))
	if c.Schedule == "off" {
		fmt.Fprint(stdout, ", and `isotope backup schedule daily` to back up every day")
	}
	fmt.Fprintln(stdout, ".")
	if isURLTarget(target) {
		fmt.Fprintf(stdout, "Credentials for %s go in %s, one NAME=value per line.\n", target, p.envFile())
	}
	return exitOK
}

// cliHostKey makes the backup's SSH key and fetches the server's host keys.
// Without --host-key it shows the public key and the fingerprints and
// stops: the user checks the fingerprint, then runs init again with it.
func cliHostKey(p backupPaths, host string, port uint32, hostKey string, stdout io.Writer) (string, error) {
	pub, err := ensureSSHKey(p)
	if err != nil {
		return "", err
	}
	fps, err := scanHostKey(p, host, port)
	if err != nil {
		return "", err
	}
	if hostKey != "" {
		return hostKey, nil
	}
	fmt.Fprintf(stdout, "Add this key to %s's authorized_keys:\n\n    %s\n\n", host, pub)
	fmt.Fprintf(stdout, "The server's host keys:\n\n")
	for _, fp := range fps {
		fmt.Fprintf(stdout, "    %s\n", fp)
	}
	fmt.Fprintln(stdout, "\nOn the server, ssh-keygen -lf /etc/ssh/ssh_host_*_key.pub shows its keys with their type.")
	fmt.Fprintln(stdout)
	return "", errors.New("compare a key with the server's of the same type, then run this again with --host-key SHA256:... (that key)")
}

// checkOnDisk refuses a directory where a backup would not be safe. Only
// the last component of a target is created: a missing parent is most
// likely a USB disk that is not mounted, and creating its mount point would
// put the backup on the internal disk. And /run/media/anna exists while
// another disk is mounted; without the disk, the folder would go on the
// tmpfs and vanish at the reboot.
func checkOnDisk(dir string) error {
	if !exists(dir) {
		return fmt.Errorf("%s does not exist. Is the disk plugged in?", dir)
	}
	if onTmpfs(dir) {
		return fmt.Errorf("%s is not on a disk (it is in memory, and empty after a reboot). Is the disk plugged in?", dir)
	}
	return nil
}

// newPassword is the password of a backup: read from stdin as typed, or
// made here, shown once and confirmed. A typed password is not put in
// canonical form here: initTarget does that, and tries the typed form too
// for an old backup (withTypedPassword).
func newPassword(passStdin bool, in *bufio.Reader, stdout io.Writer) (string, error) {
	if passStdin {
		line, err := in.ReadString('\n')
		pw := strings.TrimSpace(line)
		if pw == "" {
			if err == nil {
				err = errors.New("the password is empty")
			}
			return "", fmt.Errorf("no password on stdin: %w", err)
		}
		return pw, nil
	}
	key, err := newBackupKey()
	if err != nil {
		return "", err
	}
	if !confirmBackupKey(key, in, stdout) {
		return "", errors.New("the password was not confirmed; nothing changed")
	}
	return key, nil
}

// backupPassword is a typed backup password as it was made. A password in
// the recovery key format is put in its canonical form (lower case, groups
// joined by dashes), whatever spaces or case it was typed with; any other
// password stays as typed, because older backups may use any password.
func backupPassword(pw string) string {
	if c := recoverykey.Canonical(pw); c != "" {
		return c
	}
	return pw
}

// resticExitWrongPassword is restic's exit status for a wrong password
// (restic 0.17 and later).
const resticExitWrongPassword = 12

// wrongPassword says whether restic refused the password.
func wrongPassword(err error, stderr string) bool {
	var ee *exec.ExitError
	if errors.As(err, &ee) && ee.ExitCode() == resticExitWrongPassword {
		return true
	}
	return strings.Contains(stderr, "wrong password")
}

// withTypedPassword runs run, a restic command that reads its password
// from passFile, which holds the canonical form of typed. When restic
// refuses it and typed is different, it runs run once more with typed in
// passFile: an old backup may have a password in the recovery key format
// that was not stored in canonical form. When typed fails too, passFile
// gets the canonical form back and the first error is returned. A failed
// write of passFile is returned as that error, with no stderr, so the
// caller shows it and not restic's "wrong password".
func withTypedPassword(passFile, typed string, run func() (stderr string, err error)) (string, error) {
	stderr, err := run()
	if err == nil || backupPassword(typed) == typed || !wrongPassword(err, stderr) {
		return stderr, err
	}
	if werr := writeSecret(passFile, typed); werr != nil {
		return "", fmt.Errorf("could not try the password as typed: %w", werr)
	}
	if stderr2, err2 := run(); err2 == nil {
		return stderr2, nil
	}
	if werr := writeSecret(passFile, backupPassword(typed)); werr != nil {
		return "", fmt.Errorf("could not put the password back: %w", werr)
	}
	return stderr, err
}

// newBackupKey makes the password of a new backup. A variable for the tests.
var newBackupKey = newRecoveryKey

// confirmBackupKey shows the new password once and has one group of it typed
// back, as the installer does for a recovery key.
func confirmBackupKey(key string, in *bufio.Reader, stdout io.Writer) bool {
	groups := strings.Split(key, "-")
	fmt.Fprintln(stdout, "The password of this backup is:")
	fmt.Fprintln(stdout)
	for i := 0; i < len(groups); i += 2 {
		fmt.Fprintf(stdout, "    %s\n", strings.Join(groups[i:min(i+2, len(groups))], "-"))
	}
	fmt.Fprintln(stdout)
	fmt.Fprintln(stdout, "Write it down and keep it away from this computer. Without it the")
	fmt.Fprintln(stdout, "backup cannot be restored, and it is shown only now.")
	n := int(backupNow().UnixNano()%int64(len(groups))) + 1
	for tries := 0; tries < 3; tries++ {
		fmt.Fprintf(stdout, "Type group %d of the password to confirm: ", n)
		line, err := in.ReadString('\n')
		if strings.TrimSpace(strings.ToLower(line)) == groups[n-1] {
			return true
		}
		if err != nil {
			return false
		}
		fmt.Fprintln(stdout, "That is not the group shown above.")
	}
	return false
}

// sortedKeys is for messages that list a map.
func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// chownTree gives every file under root to uid:gid, without following links
// and without stopping at the first error. Only for a tree no user can
// change while it runs: a restore's stage.
func chownTree(root string, uid, gid int) error {
	var errs []error
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err == nil {
			err = os.Lchown(path, uid, gid)
		}
		if err != nil {
			errs = append(errs, err)
		}
		return nil
	})
	return errors.Join(errs...)
}

// lookupUser is the local account for a name. A variable for the tests.
var lookupUser = func(name string) (uid, gid int, err error) {
	u, err := user.Lookup(name)
	if err != nil {
		return 0, 0, err
	}
	uid, _ = strconv.Atoi(u.Uid)
	gid, _ = strconv.Atoi(u.Gid)
	return uid, gid, nil
}
