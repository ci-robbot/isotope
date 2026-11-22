package main

// The backup jobs of Isotope System's Backup page and Isotope Welcome's
// restore: list the backups in a target, export the backup to a file,
// cancel a backup, and restore a whole backup on a machine that is still
// new. They run the code of `isotope backup list`, `isotope backup
// --export` and `isotope restore`. Secrets come in only as a pipe fd. See
// "Daemon API" and "Welcome" in
// docs/superpowers/specs/2026-11-20-isotope-welcome-design.md.

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/godbus/dbus/v5"
	"golang.org/x/sys/unix"
)

var (
	errMachineInUse        = errors.New("This machine is already in use; restore parts of a backup with isotope restore --only")
	errExportNeedsPassword = errors.New("Set up a backup first; the file is encrypted with its password")
)

// sentenceErr is an error of the kind is, with its own sentence.
type sentenceErr struct {
	msg string
	is  error
}

func (e sentenceErr) Error() string { return e.msg }
func (e sentenceErr) Unwrap() error { return e.is }

var (
	errRestoreNotCancellable error = sentenceErr{"the restore is writing the backup and cannot be cancelled", ErrNotCancellable}
	errRestoring             error = sentenceErr{"a backup is being restored; the machine restarts at the end", ErrBusy}
)

// restoring is set from the start of a restore job; it stays set once the
// restore asked for the restart. The users methods and SetLocale write
// /oem, which the restore replaces, so they refuse while it is set. It is
// written with usersMu and settingsMu both held, and read with either.
var restoring bool

// notRestoring refuses a change to the users or the settings while a
// restore runs.
func (s *service) notRestoring() *dbus.Error {
	if s.d.Status().State == StateRestore {
		return busError(errRestoring)
	}
	return nil
}

// backupJobRunning says whether a job that writes the backup settings runs:
// a backup or an export writes the config when it ends, a restore replaces
// it.
func backupJobRunning(s State) bool { return s == StateBackup || s == StateRestore }

// startBackupJobLocked takes the job slot for fn, in state with detail. The
// outcome goes to backupResult and backupDetail when it ends: done is the
// word for a run that left out some files ("Backed up, but ..."). The
// caller holds mu and has checked that the slot is free.
func (d *Daemon) startBackupJobLocked(state State, detail, done string, fn func(ctx context.Context, progress func(string)) (string, error)) {
	ctx, cancel := context.WithCancel(context.Background())
	j := &job{cancel: cancel, prev: d.state, prevDetail: d.detail}
	d.job = j
	d.backupResult, d.backupDetail = "", ""
	d.setLocked(state, detail)
	d.wg.Add(1)
	go func() {
		defer d.wg.Done()
		defer cancel()
		msg, err := fn(ctx, d.progress)
		d.mu.Lock()
		defer d.mu.Unlock()
		d.job = nil
		switch {
		case j.cancelled && err != nil:
			d.backupResult, d.backupDetail = backupResultFailed, "Cancelled"
		case errors.Is(err, errPartial):
			d.backupResult, d.backupDetail = backupResultPartial, done+", but "+err.Error()+"."
		case err != nil:
			d.backupResult, d.backupDetail = backupResultFailed, err.Error()
		default:
			d.backupResult, d.backupDetail = backupResultOK, msg
		}
		if state == StateRestore && err == nil {
			// The restore asked for the restart: nothing may start an
			// install now.
			d.rebooting = true
		}
		prev, prevDetail := j.prev, j.prevDetail
		if prev == StateError {
			prev, prevDetail = StateIdle, ""
		}
		d.setLocked(prev, prevDetail)
	}()
}

// slotFreeLocked refuses a job while another one runs or the system
// restarts.
func (d *Daemon) slotFreeLocked() error {
	switch {
	case d.stopping:
		return ErrStopping
	case d.rebooting:
		return ErrRebooting
	case d.busyLocked() || d.job != nil:
		return ErrBusy
	}
	return nil
}

// BackupExport writes the backup of this machine to file, as a job like
// BackupNow. The file is encrypted with the machine's backup password, so
// there must be one, and it is given to uid:gid, the caller.
func (d *Daemon) BackupExport(file string, uid, gid int) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.export == nil {
		return errBackupUnavailable
	}
	if err := d.slotFreeLocked(); err != nil {
		return err
	}
	if !exists(defaultBackupPaths().password()) {
		return errExportNeedsPassword
	}
	export := d.export
	d.startBackupJobLocked(StateBackup, "Exporting to "+file, "Exported",
		func(ctx context.Context, progress func(string)) (string, error) {
			return export(ctx, progress, file, uid, gid)
		})
	return nil
}

// Stop is the daemon's answer to SIGTERM, SIGINT or SIGHUP. A backup or an
// export is cancelled as BackupCancel does; a restore and an install cannot
// be, but systemd stops their child processes with the same signal. Then it
// waits up to wait for the jobs to end, so their deferred cleanup (work
// folders, a .partial file, secrets in /run) runs. It says whether they
// ended.
func (d *Daemon) Stop(wait time.Duration) bool {
	d.mu.Lock()
	// First refuse new jobs, so a call already in progress cannot start one
	// after the running one is cancelled.
	d.stopping = true
	if d.state == StateBackup && d.job != nil {
		d.job.cancelled = true
		d.job.cancel()
	}
	d.mu.Unlock()
	ended := make(chan struct{})
	go func() {
		d.wg.Wait()
		close(ended)
	}()
	select {
	case <-ended:
		return true
	case <-time.After(wait):
		return false
	}
}

// BackupCancel stops a backup or an export: restic is stopped, and the
// outcome is "Cancelled". A restore cannot be stopped.
func (d *Daemon) BackupCancel() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	switch {
	case d.state == StateRestore:
		return errRestoreNotCancellable
	case d.state != StateBackup || d.job == nil:
		return errNothingToCancel
	}
	d.job.cancelled = true
	d.job.cancel()
	return nil
}

// restoreRequest is what Restore puts back: the backup in Source ("" is
// this machine's target), the snapshot ("" is the latest) of Host, with
// its password and its target's credentials (NAME=value lines). HostKey and
// Port are the SFTP server's, as for BackupInit.
type restoreRequest struct {
	Source, Snapshot, Host string
	Password               string
	Env                    []string
	HostKey                string
	Port                   uint32
}

// Restore puts a whole backup back, as a job, and ends in the restart. It
// is refused unless the machine is new.
//
// The check and the start of the job hold usersMu and settingsMu, so no
// user or setting changes between them, and none after: restoring is set.
func (d *Daemon) Restore(req restoreRequest) error {
	usersMu.Lock()
	defer usersMu.Unlock()
	settingsMu.Lock()
	defer settingsMu.Unlock()
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.restore == nil {
		return errBackupUnavailable
	}
	if err := d.slotFreeLocked(); err != nil {
		return err
	}
	if restoring {
		return errRestoring
	}
	if ok, _ := machineIsNew(defaultBackupPaths()); !ok {
		return errMachineInUse
	}
	restoring = true
	restore := d.restore
	d.startBackupJobLocked(StateRestore, "Restoring a backup", "Restored",
		func(ctx context.Context, progress func(string)) (string, error) {
			msg, err := restore(ctx, progress, req)
			if err != nil {
				// No restart comes: the users and settings may change
				// again. Taken before d.mu, in the order Restore takes them.
				usersMu.Lock()
				settingsMu.Lock()
				restoring = false
				settingsMu.Unlock()
				usersMu.Unlock()
			}
			return msg, err
		})
	return nil
}

// primaryGID is the primary group of the user with uid. A variable for the
// tests.
var primaryGID = func(uid uint32) (int, error) {
	u, err := user.LookupId(strconv.FormatUint(uint64(uid), 10))
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(u.Gid)
}

// daemonExport is the daemon's export: `isotope backup --export FILE` as
// root with the machine's password, into a folder of uid's, and the file is
// given to uid:gid, so the user can copy it.
func daemonExport(ctx context.Context, progress func(string), file string, uid, gid int) (string, error) {
	if !exists(restic()) {
		return "", errBackupUnavailable
	}
	p := defaultBackupPaths()
	c, err := loadBackupConfig(p)
	if err != nil {
		return "", err
	}
	// The password goes in as the CLI's --password-stdin: without one,
	// exportTo would make a new password and print it to every client.
	b, err := os.ReadFile(p.password())
	pw := strings.TrimSpace(string(b))
	clear(b)
	if err != nil || pw == "" {
		return "", errExportNeedsPassword
	}
	w := &lineWriter{line: progress}
	defer w.Flush()
	err = exportTo(ctx, p, file, &exportOwner{uid: uid, gid: gid}, nil, c.Containers, true,
		bufio.NewReader(strings.NewReader(pw+"\n")), w, w)
	if err != nil && !errors.Is(err, errPartial) {
		return "", err
	}
	return "Exported to " + file + ".", err
}

// tempSecret writes s to a new 0600 file in dir and returns its name.
func tempSecret(dir, pattern, s string) (string, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	f, err := os.CreateTemp(dir, pattern)
	if err != nil {
		return "", err
	}
	_, err = f.WriteString(s + "\n")
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(f.Name())
		return "", err
	}
	return f.Name(), nil
}

// daemonRestore is the daemon's restore: `isotope restore --from SOURCE
// --yes` as root. The password goes in where the CLI reads it, and the
// credentials in a 0600 file in p.Run, removed at the end. It ends in the
// restart, as the CLI does. A restore cannot be stopped, so ctx is not
// used.
func daemonRestore(_ context.Context, progress func(string), req restoreRequest) (string, error) {
	if !exists(restic()) {
		return "", errBackupUnavailable
	}
	p := defaultBackupPaths()
	if err := confirmSFTP(p, req.Source, req.HostKey, req.Port); err != nil {
		return "", err
	}
	envFile, err := tempSecret(p.Run, "env-restore-*", strings.Join(req.Env, "\n"))
	if err != nil {
		return "", err
	}
	defer os.Remove(envFile)
	w := &lineWriter{line: progress}
	defer w.Flush()
	snap := req.Snapshot
	if snap == "" {
		snap = "latest"
	}
	r := restorer{p: p, snap: snap, host: req.Host, users: userMap{}, envFile: envFile,
		in: bufio.NewReader(strings.NewReader(req.Password + "\n")), stdout: w, stderr: w}
	if err := r.run(req.Source, true); err != nil {
		return "", err
	}
	return "Restored. The machine restarts.", nil
}

// listSnapshots is the backups in source, newest first, as BackupList
// returns them. source "" is this machine's target with its password and
// credentials; any other source is a target or an exported file, with the
// password in secrets[0] and the credentials in secrets[1:].
func listSnapshots(ctx context.Context, p backupPaths, source string, opt initOptions, secrets []string) ([]map[string]dbus.Variant, error) {
	var target, passFile, envFile string
	if source == "" {
		c, err := loadBackupConfig(p)
		if err != nil {
			return nil, err
		}
		if c.Target == "" {
			return nil, errors.New("there is no backup target; set one up first")
		}
		target, passFile, envFile = c.Target, p.password(), p.envFile()
	} else {
		if len(secrets) == 0 || secrets[0] == "" {
			return nil, errors.New("the password is empty")
		}
		if err := checkEnv(secrets[1:]); err != nil {
			return nil, err
		}
		var err error
		if target, err = normalizeTarget(source); err != nil {
			return nil, err
		}
		if err := confirmSFTP(p, target, opt.HostKey, opt.Port); err != nil {
			return nil, err
		}
		if passFile, err = tempSecret(p.Run, "password-list-*", backupPassword(secrets[0])); err != nil {
			return nil, err
		}
		defer os.Remove(passFile)
		if envFile, err = tempSecret(p.Run, "env-list-*", strings.Join(secrets[1:], "\n")); err != nil {
			return nil, err
		}
		defer os.Remove(envFile)
		// A file from an export is unpacked where only root can write, as
		// the restore does.
		if fi, err := os.Stat(target); err == nil && fi.Mode().IsRegular() {
			dir, doneWork, err := makeWorkDir(p)
			if err != nil {
				return nil, err
			}
			defer doneWork()
			if err := unpackFile(target, dir); err != nil {
				return nil, err
			}
			target = dir
		}
	}
	repo, release, err := resolveTarget(target)
	if err != nil {
		return nil, err
	}
	defer release()
	var out bytes.Buffer
	snapshots := func() (string, error) {
		cmd, err := resticCmdEnv(p, repo, passFile, envFile, "snapshots", "--json", "--tag", "isotope")
		if err != nil {
			return "", err
		}
		var stderr bytes.Buffer
		out.Reset()
		cmd.Stdout, cmd.Stderr = &out, &stderr
		err = runCtx(ctx, cmd)
		return stderr.String(), err
	}
	var stderr string
	if source == "" {
		// This machine's password file is never rewritten.
		stderr, err = snapshots()
	} else {
		stderr, err = withTypedPassword(passFile, secrets[0], snapshots)
	}
	if err != nil {
		if s := lastLine(stderr); s != "" {
			return nil, fmt.Errorf("could not read the backup (wrong password?): %s", s)
		}
		return nil, fmt.Errorf("could not read the backup (wrong password?): %w", err)
	}
	var snaps []struct {
		ID       string    `json:"id"`
		ShortID  string    `json:"short_id"`
		Hostname string    `json:"hostname"`
		Time     time.Time `json:"time"`
		Summary  *struct {
			TotalBytesProcessed uint64 `json:"total_bytes_processed"`
		} `json:"summary"`
	}
	if err := json.Unmarshal(out.Bytes(), &snaps); err != nil {
		return nil, fmt.Errorf("restic snapshots: %w", err)
	}
	sort.SliceStable(snaps, func(i, j int) bool { return snaps[i].Time.After(snaps[j].Time) })
	list := make([]map[string]dbus.Variant, 0, len(snaps))
	for _, s := range snaps {
		id := s.ShortID
		if id == "" {
			id = s.ID[:min(8, len(s.ID))]
		}
		var size uint64
		if s.Summary != nil {
			size = s.Summary.TotalBytesProcessed
		}
		list = append(list, map[string]dbus.Variant{
			"id":   dbus.MakeVariant(id),
			"time": dbus.MakeVariant(s.Time.Unix()),
			"host": dbus.MakeVariant(s.Hostname),
			"size": dbus.MakeVariant(size),
		})
	}
	return list, nil
}

// welcomeStamps are the stamps of a user's first login, under
// ~/.local/state: Isotope Welcome's, the old app picker's, and their
// copies from before the rename.
var welcomeStamps = []string{
	"isotope/welcome", "isotope/first-run-apps", "isotope/welcomed",
	"hadron/welcome", "hadron/first-run-apps", "hadron/welcomed",
}

// machineIsNew is the Welcome design's rule: one human user (uid ≥ 1000),
// no backup target and no backup ever, no restore pending or finished, and
// that user's welcome stamp not written. reason is the first rule that fails.
func machineIsNew(p backupPaths) (ok bool, reason string) {
	pw, err := readPasswd()
	if err != nil {
		return false, "the users cannot be read"
	}
	var humans []passwdEntry
	for _, e := range pw {
		if isUserUID(e.uid) {
			humans = append(humans, e)
		}
	}
	switch {
	case len(humans) == 0:
		return false, "there is no user"
	case len(humans) > 1:
		return false, "there is more than one user"
	}
	c, err := loadBackupConfig(p)
	switch {
	case err != nil:
		return false, "the backup settings cannot be read"
	case c.Target != "":
		return false, "a backup target is set up"
	case c.Last != 0 || c.Result != "" || exists(p.manifest()):
		// Every backup and export writes the manifest first.
		return false, "a backup was made on this machine"
	}
	if exists(p.pending()) || exists(p.finishResult()) || exists(filepath.Join(p.State, "oem-before-restore")) {
		return false, "a backup was restored on this machine"
	}
	u := humans[0]
	for _, s := range welcomeStamps {
		// Lstat: a link counts as a stamp; an answer other than "not
		// there" also counts.
		if _, err := os.Lstat(filepath.Join(u.home, ".local/state", s)); !errors.Is(err, os.ErrNotExist) {
			return false, "the first login of " + u.name + " is over"
		}
	}
	return true, ""
}

// listOptionsFrom reads the options of BackupList and Restore: host_key and
// port, for an SFTP target.
func listOptionsFrom(m map[string]dbus.Variant) (initOptions, error) {
	for k := range m {
		if k != "host_key" && k != "port" {
			return initOptions{}, fmt.Errorf("unknown option %q", k)
		}
	}
	return initOptionsFrom(m)
}

// checkBusSource allows the sources Isotope System and Welcome read: the
// targets checkBusTarget allows (an exported file is an absolute path),
// without a password in a URL.
func checkBusSource(source string) error {
	if urlHasPassword(source) {
		return errors.New("the target has a password in it; send it with the credentials")
	}
	return checkBusTarget(source)
}

// checkSnapshotArgs refuses a snapshot that is not a restic ID and a host
// that restic could read as an option.
func checkSnapshotArgs(snapshot, host string) error {
	if snapshot != "" && (len(snapshot) > 64 || strings.Trim(snapshot, "0123456789abcdef") != "") {
		return fmt.Errorf("%q is not a snapshot ID", oneLine(snapshot))
	}
	if strings.HasPrefix(host, "-") || strings.ContainsFunc(host, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
		return fmt.Errorf("%q is not a host name", oneLine(host))
	}
	return nil
}

// listTimeout bounds BackupList; the client waits 120 s.
var listTimeout = 110 * time.Second

// BackupList returns the backups in source, newest first. The fd holds the
// password and the credentials; for source "" it may be empty.
func (a busAPI) BackupList(sender dbus.Sender, source string, options map[string]dbus.Variant, fd dbus.UnixFD) ([]map[string]dbus.Variant, *dbus.Error) {
	done, derr := a.s.enter()
	if derr != nil {
		closeFD(fd)
		return nil, derr
	}
	defer done()
	if err := a.s.authorize(sender, actionBackup); err != nil {
		closeFD(fd)
		return nil, err
	}
	secrets, err := readSecrets(fd)
	if err != nil {
		return nil, busError(err)
	}
	defer clear(secrets)
	opt, err := listOptionsFrom(options)
	if err != nil {
		return nil, busError(err)
	}
	if source != "" {
		if err := checkBusSource(source); err != nil {
			return nil, busError(err)
		}
	}
	if !exists(restic()) {
		return nil, busError(errBackupUnavailable)
	}
	ctx, cancel := context.WithTimeout(context.Background(), listTimeout)
	defer cancel()
	list, err := listSnapshots(ctx, defaultBackupPaths(), source, opt, secrets)
	if err != nil {
		return nil, busError(err)
	}
	return list, nil
}

// BackupExport writes the backup of this machine to path, as a job, and
// gives the file to the caller.
func (a busAPI) BackupExport(sender dbus.Sender, path string) *dbus.Error {
	done, derr := a.s.enter()
	if derr != nil {
		return derr
	}
	defer done()
	if err := a.s.authorize(sender, actionBackup); err != nil {
		return err
	}
	if a.s.uidOf == nil {
		return errNotAuthorized
	}
	uid, err := a.s.uidOf(sender)
	if err != nil {
		fmt.Fprintf(os.Stderr, "isotope: refusing BackupExport for %s: %v\n", sender, err)
		return errNotAuthorized
	}
	gid, err := primaryGID(uid)
	if err != nil {
		return busError(fmt.Errorf("the primary group of uid %d: %w", uid, err))
	}
	file, err := checkExportFile(path)
	if err != nil {
		return busError(err)
	}
	// Refused now, not only when the job runs: the folder must be the
	// caller's (see openExportDir).
	dirfd, err := openExportDir(filepath.Dir(file), &exportOwner{uid: int(uid), gid: gid})
	if err != nil {
		return busError(err)
	}
	unix.Close(dirfd)
	return busError(a.s.d.BackupExport(file, int(uid), gid))
}

// BackupCancel stops a backup or an export.
func (a busAPI) BackupCancel(sender dbus.Sender) *dbus.Error {
	done, derr := a.s.enter()
	if derr != nil {
		return derr
	}
	defer done()
	if err := a.s.authorize(sender, actionBackup); err != nil {
		return err
	}
	return busError(a.s.d.BackupCancel())
}

// Restore puts a whole backup back and restarts. It is refused on a machine
// that is not new before the fd is read.
func (a busAPI) Restore(sender dbus.Sender, source, snapshot, host string, options map[string]dbus.Variant, fd dbus.UnixFD) *dbus.Error {
	done, derr := a.s.enter()
	if derr != nil {
		closeFD(fd)
		return derr
	}
	defer done()
	if err := a.s.authorize(sender, actionRestore); err != nil {
		closeFD(fd)
		return err
	}
	if ok, _ := machineIsNew(defaultBackupPaths()); !ok {
		closeFD(fd)
		return busError(errMachineInUse)
	}
	secrets, err := readSecrets(fd)
	if err != nil {
		return busError(err)
	}
	defer clear(secrets)
	opt, err := listOptionsFrom(options)
	if err != nil {
		return busError(err)
	}
	if source != "" {
		if err := checkBusSource(source); err != nil {
			return busError(err)
		}
	}
	if err := checkEnv(secrets[1:]); err != nil {
		return busError(err)
	}
	if err := checkSnapshotArgs(snapshot, host); err != nil {
		return busError(err)
	}
	if !exists(restic()) {
		return busError(errBackupUnavailable)
	}
	return busError(a.s.d.Restore(restoreRequest{
		Source: source, Snapshot: snapshot, Host: host,
		Password: secrets[0], Env: append([]string(nil), secrets[1:]...),
		HostKey: opt.HostKey, Port: opt.Port,
	}))
}
