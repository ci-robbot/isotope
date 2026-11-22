package main

// Backup targets that Isotope System sets up: a USB disk by the UUID of its
// file system, and remote targets (REST, S3, SFTP) with their credentials.
// The daemon methods BackupInit, BackupSSHKey and BackupHostKey run the
// code `isotope backup init` runs. Secrets come in only as a pipe fd. See
// "Disks", "Remote targets" and "Daemon API" in
// docs/superpowers/specs/2026-11-20-isotope-welcome-design.md.

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/godbus/dbus/v5"
)

// byUUIDDir is where udev links each file system by its UUID. A variable
// for the tests.
var byUUIDDir = "/dev/disk/by-uuid"

// parseUUIDTarget splits uuid:<UUID>/<path>. The path is not empty and has
// no "..".
func parseUUIDTarget(t string) (uuid, sub string, ok bool) {
	rest, found := strings.CutPrefix(t, "uuid:")
	if !found {
		return "", "", false
	}
	uuid, sub, _ = strings.Cut(rest, "/")
	if uuid == "" || strings.Trim(uuid, "0123456789abcdefABCDEF-") != "" {
		return "", "", false
	}
	for _, part := range strings.Split(sub, "/") {
		if part == ".." || part == "." {
			return "", "", false
		}
	}
	sub = strings.Trim(filepath.Clean("/"+sub), "/")
	if sub == "" {
		return "", "", false
	}
	return uuid, sub, true
}

// diskPresent says whether a file system with this UUID is plugged in.
func diskPresent(uuid string) bool { return exists(filepath.Join(byUUIDDir, uuid)) }

// errDiskMissing is a uuid: target whose disk is not plugged in.
var errDiskMissing = errors.New("the backup disk is not plugged in")

// realPath follows the links of a device path when it can.
func realPath(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return filepath.Clean(p)
}

// uuidMountPoint is where the file system with this UUID is mounted now,
// or "". Only a mount of the whole file system counts, not a bind mount of
// a folder in it.
func uuidMountPoint(uuid string) string {
	link := filepath.Join(byUUIDDir, uuid)
	dest, err := os.Readlink(link)
	if err != nil {
		return ""
	}
	if !filepath.IsAbs(dest) {
		dest = filepath.Join(byUUIDDir, dest)
	}
	dev := realPath(dest)
	for _, m := range readMounts() {
		if m.root == "/" && m.src != "" && realPath(m.src) == dev {
			return m.point
		}
	}
	return ""
}

// mountByUUID mounts the file system with this UUID through udisks2 and
// returns where; unmount undoes it. As root udisks2 allows it without a
// question. A variable for the tests.
var mountByUUID = func(uuid string) (dir string, unmount func(), err error) {
	conn, err := dbus.ConnectSystemBus()
	if err != nil {
		return "", nil, err
	}
	const dest = "org.freedesktop.UDisks2"
	none := map[string]dbus.Variant{}
	opts := map[string]dbus.Variant{"auth.no_user_interaction": dbus.MakeVariant(true)}
	var paths []dbus.ObjectPath
	err = conn.Object(dest, "/org/freedesktop/UDisks2/Manager").Call(dest+".Manager.ResolveDevice", 0,
		map[string]dbus.Variant{"uuid": dbus.MakeVariant(uuid)}, none).Store(&paths)
	if err == nil && len(paths) == 0 {
		err = errDiskMissing
	}
	if err != nil {
		conn.Close()
		return "", nil, err
	}
	fs := conn.Object(dest, paths[0])
	if err := fs.Call(dest+".Filesystem.Mount", 0, opts).Store(&dir); err != nil {
		conn.Close()
		return "", nil, fmt.Errorf("could not mount the backup disk: %w", err)
	}
	return dir, func() {
		_ = fs.Call(dest+".Filesystem.Unmount", 0, opts).Err
		conn.Close()
	}, nil
}

// resolveTarget turns a target into what restic opens. A uuid: target is
// looked up in mountinfo; when it is not mounted, it is mounted through
// udisks2 (as root) and release unmounts it again. A disk that is not
// plugged in is errDiskMissing: the target is never a bare mount point.
func resolveTarget(target string) (repo string, release func(), err error) {
	uuid, sub, ok := parseUUIDTarget(target)
	if !ok {
		return target, func() {}, nil
	}
	if !diskPresent(uuid) {
		return "", nil, errDiskMissing
	}
	if dir := uuidMountPoint(uuid); dir != "" {
		repo = filepath.Join(dir, sub)
		if err := checkOnMount(dir, repo); err != nil {
			return "", nil, err
		}
		return repo, func() {}, nil
	}
	dir, unmount, err := mountByUUID(uuid)
	if err != nil {
		return "", nil, err
	}
	if dir == "" {
		unmount()
		return "", nil, errors.New("the backup disk was mounted nowhere")
	}
	repo = filepath.Join(dir, sub)
	if err := checkOnMount(dir, repo); err != nil {
		unmount()
		return "", nil, err
	}
	var once sync.Once
	return repo, func() { once.Do(unmount) }, nil
}

// checkOnMount refuses a folder that a link takes off the disk mounted at
// mount: a disk with a cloned UUID could hold a link to the internal disk,
// and the backup would then write there. The deepest part of path that
// exists is resolved; the rest is made later as real folders.
func checkOnMount(mount, path string) error {
	realMount, err := filepath.EvalSymlinks(mount)
	if err != nil {
		return err
	}
	p := path
	for {
		if _, err := os.Lstat(p); err == nil {
			break
		}
		parent := filepath.Dir(p)
		if parent == p || !under(parent, mount) {
			return fmt.Errorf("%s is not on the backup disk", path)
		}
		p = parent
	}
	real, err := filepath.EvalSymlinks(p)
	if err != nil {
		return fmt.Errorf("%s: %w", p, err)
	}
	if !under(real, realMount) {
		return fmt.Errorf("%s is a link off the backup disk; refusing it", p)
	}
	return nil
}

// targetKind is BackupStatus's kind of a target.
func targetKind(t string) string {
	switch {
	case t == "":
		return ""
	case strings.HasPrefix(t, "uuid:"):
		return "disk"
	case strings.HasPrefix(t, "rest:"):
		return "rest"
	case strings.HasPrefix(t, "s3:"):
		return "s3"
	case strings.HasPrefix(t, "sftp:"):
		return "sftp"
	case filepath.IsAbs(t):
		return "dir"
	}
	return "other"
}

// targetName is a target as the user knows it: a USB disk by its label.
func targetName(c backupConfig) string {
	if strings.HasPrefix(c.Target, "uuid:") {
		name := c.Label
		if name == "" {
			name = c.Target
		}
		return name + " (USB disk)"
	}
	return c.Target
}

// diskName is how a message names the disk of a uuid: target.
func diskName(c backupConfig) string {
	if c.Label != "" {
		return c.Label
	}
	return "The backup disk"
}

// sshDir holds the backup's own SSH key and the hosts it trusts:
// id_ed25519, id_ed25519.pub, known_hosts. On the persistent partition,
// not in /root.
func sshDir(p backupPaths) string { return filepath.Join(p.State, "ssh") }

// sftpArgs make restic's ssh use only the backup's key and known_hosts,
// with no question: a host key that is not known is an error.
func sftpArgs(p backupPaths) []string {
	return sftpArgsWith(p, filepath.Join(sshDir(p), "known_hosts"))
}

// sftpArgsWith is sftpArgs with the hosts in knownHosts.
func sftpArgsWith(p backupPaths, knownHosts string) []string {
	return []string{"-o", "sftp.args=-i " + filepath.Join(sshDir(p), "id_ed25519") +
		" -o UserKnownHostsFile=" + knownHosts +
		" -o StrictHostKeyChecking=yes -o BatchMode=yes"}
}

// ensureSSHKey makes the backup's SSH key the first time and returns its
// public half, to add to the server's authorized_keys.
func ensureSSHKey(p backupPaths) (pub string, err error) {
	key := filepath.Join(sshDir(p), "id_ed25519")
	if !exists(key) {
		if err := os.MkdirAll(sshDir(p), 0o700); err != nil {
			return "", err
		}
		host, _ := os.Hostname()
		cmd := sysCommand("ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-C", "isotope-backup@"+host, "-f", key)
		if out, err := cmd.CombinedOutput(); err != nil {
			return "", fmt.Errorf("ssh-keygen: %v: %s", err, oneLine(string(out)))
		}
	}
	b, err := os.ReadFile(key + ".pub")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}

// checkHost refuses a host that ssh could read as an option or that would
// not make a file name.
func checkHost(host string, port uint32) error {
	if host == "" || strings.HasPrefix(host, "-") || port > 65535 ||
		strings.Trim(host, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789.-_:") != "" {
		return fmt.Errorf("%q is not a host name", host)
	}
	return nil
}

func hostKeysFile(p backupPaths, host string, port uint32) string {
	return filepath.Join(p.Run, fmt.Sprintf("hostkeys-%s-%d", host, port))
}

// hostKeyFingerprint is ssh-keygen -l's SHA256 fingerprint of a known_hosts
// line, or "".
func hostKeyFingerprint(line string) string {
	f := strings.Fields(line)
	if len(f) < 3 || strings.HasPrefix(f[0], "#") {
		return ""
	}
	blob, err := base64.StdEncoding.DecodeString(f[2])
	if err != nil || len(blob) == 0 {
		return ""
	}
	sum := sha256.Sum256(blob)
	return "SHA256:" + base64.RawStdEncoding.EncodeToString(sum[:])
}

// hostKeyType is ssh-keygen -l's name of the key type of a known_hosts
// line: ED25519, ECDSA, RSA, ...
func hostKeyType(line string) string {
	f := strings.Fields(line)
	if len(f) < 2 {
		return ""
	}
	t := f[1]
	sk := strings.HasPrefix(t, "sk-")
	t = strings.TrimPrefix(t, "sk-")
	switch {
	case strings.HasPrefix(t, "ssh-ed25519"):
		t = "ED25519"
	case strings.HasPrefix(t, "ecdsa-"):
		t = "ECDSA"
	case t == "ssh-rsa":
		t = "RSA"
	case t == "ssh-dss":
		t = "DSA"
	default:
		t = strings.ToUpper(strings.TrimPrefix(t, "ssh-"))
	}
	if sk {
		t += "-SK"
	}
	return t
}

// hostKeyRank orders the key types as the user most likely checks them:
// ED25519 first.
func hostKeyRank(typed string) int {
	switch {
	case strings.HasPrefix(typed, "ED25519 "):
		return 0
	case strings.HasPrefix(typed, "ECDSA "):
		return 1
	case strings.HasPrefix(typed, "RSA "):
		return 2
	}
	return 3
}

// bareFingerprint is the SHA256:... part of "TYPE SHA256:...", so that a
// line copied whole from the list is taken too.
func bareFingerprint(s string) string {
	f := strings.Fields(s)
	if len(f) == 0 {
		return ""
	}
	return f[len(f)-1]
}

// scanHostKey fetches the server's host keys with ssh-keyscan, keeps them in
// p.Run for acceptHostKey, and returns them as "TYPE SHA256:..." (ED25519
// first) for the user to compare with the server's
// (ssh-keygen -lf /etc/ssh/ssh_host_*_key.pub). acceptHostKey takes the
// bare SHA256:... fingerprint.
func scanHostKey(p backupPaths, host string, port uint32) (fingerprints []string, err error) {
	if port == 0 {
		port = 22
	}
	if err := checkHost(host, port); err != nil {
		return nil, err
	}
	var stderr bytes.Buffer
	cmd := sysCommand("ssh-keyscan", "-T", "10", "-p", strconv.FormatUint(uint64(port), 10), host)
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("ssh-keyscan %s: %v: %s", host, err, oneLine(stderr.String()))
	}
	var keep []string
	for _, line := range strings.Split(string(out), "\n") {
		if fp := hostKeyFingerprint(line); fp != "" {
			keep = append(keep, strings.TrimSpace(line))
			fingerprints = append(fingerprints, hostKeyType(line)+" "+fp)
		}
	}
	sort.SliceStable(fingerprints, func(i, j int) bool { return hostKeyRank(fingerprints[i]) < hostKeyRank(fingerprints[j]) })
	if len(keep) == 0 {
		return nil, fmt.Errorf("%s:%d sent no host key", host, port)
	}
	if err := os.MkdirAll(p.Run, 0o700); err != nil {
		return nil, err
	}
	if err := os.WriteFile(hostKeysFile(p, host, port), []byte(strings.Join(keep, "\n")+"\n"), 0o600); err != nil {
		return nil, err
	}
	return fingerprints, nil
}

// acceptHostKey adds the scanned line whose fingerprint matches to the
// backup's known_hosts.
func acceptHostKey(p backupPaths, host string, port uint32, fingerprint string) error {
	line, err := confirmedHostLine(p, host, port, fingerprint)
	if err != nil {
		return err
	}
	return addKnownHost(filepath.Join(sshDir(p), "known_hosts"), line)
}

// confirmedHostLine is the scanned known_hosts line of host:port whose
// fingerprint matches.
func confirmedHostLine(p backupPaths, host string, port uint32, fingerprint string) (string, error) {
	if port == 0 {
		port = 22
	}
	if err := checkHost(host, port); err != nil {
		return "", err
	}
	fingerprint = bareFingerprint(fingerprint)
	b, err := os.ReadFile(hostKeysFile(p, host, port))
	if errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("the host key of %s:%d was not fetched; fetch it first", host, port)
	}
	if err != nil {
		return "", err
	}
	for _, l := range strings.Split(string(b), "\n") {
		if fingerprint != "" && hostKeyFingerprint(l) == fingerprint {
			return strings.TrimSpace(l), nil
		}
	}
	return "", fmt.Errorf("%s is not a host key of %s:%d", fingerprint, host, port)
}

// addKnownHost adds line to the known_hosts file kh, once.
func addKnownHost(kh, line string) error {
	old, err := os.ReadFile(kh)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	for _, l := range strings.Split(string(old), "\n") {
		if strings.TrimSpace(l) == line {
			return nil
		}
	}
	if err := os.MkdirAll(filepath.Dir(kh), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(kh, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.WriteString(line + "\n"); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// parseSFTPTarget is the host and port of sftp://user@host:port//path or
// sftp:user@host:/path (port 22).
func parseSFTPTarget(t string) (host string, port uint32, ok bool) {
	if rest, found := strings.CutPrefix(t, "sftp://"); found {
		hp, _, _ := strings.Cut(rest, "/")
		if i := strings.LastIndex(hp, "@"); i >= 0 {
			hp = hp[i+1:]
		}
		h, ps, err := net.SplitHostPort(hp)
		if err != nil {
			h, ps = strings.Trim(hp, "[]"), "22"
		}
		n, err := strconv.ParseUint(ps, 10, 16)
		if err != nil || h == "" || n == 0 {
			return "", 0, false
		}
		return h, uint32(n), true
	}
	rest, found := strings.CutPrefix(t, "sftp:")
	if !found {
		return "", 0, false
	}
	hp, _, found := strings.Cut(rest, ":")
	if i := strings.LastIndex(hp, "@"); i >= 0 {
		hp = hp[i+1:]
	}
	if !found || hp == "" {
		return "", 0, false
	}
	return hp, 22, true
}

// initOptions are BackupInit's options. Existing opens a backup that is
// there already, with its password; otherwise a new one is made. HostKey is
// the SHA256 fingerprint the user confirmed for an SFTP server. Port, when
// given, must be the SFTP target's.
type initOptions struct {
	Containers, Existing bool
	Label, HostKey       string
	Port                 uint32
}

// allowedEnv are the names a target's credentials may set.
var allowedEnv = []string{"RESTIC_REST_USERNAME", "RESTIC_REST_PASSWORD", "AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "AWS_DEFAULT_REGION"}

// checkEnv refuses a credential line that is not NAME=value with a NAME of
// allowedEnv. The message never holds the value.
func checkEnv(env []string) error {
	for i, line := range env {
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			return fmt.Errorf("credential line %d is not NAME=value", i+1)
		}
		if !contains(allowedEnv, k) {
			// Only a name is shown: the text before "=" may be part of a
			// secret.
			if k == "" || strings.Trim(k, "ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_") != "" {
				return fmt.Errorf("credential line %d does not start with a NAME= this backup takes (%s)", i+1, strings.Join(allowedEnv, ", "))
			}
			return fmt.Errorf("%s is not a credential a backup target takes (%s)", k, strings.Join(allowedEnv, ", "))
		}
		if strings.ContainsAny(v, "\r\x00") {
			return fmt.Errorf("the value of %s has a control character", k)
		}
	}
	return nil
}

// errBackupThere is a new backup asked for where one is already.
type errBackupThere string

func (e errBackupThere) Error() string {
	return fmt.Sprintf("%s already holds a backup; use it as an existing backup, with its password", string(e))
}

// initTarget makes target this machine's backup target: it creates the
// backup there with password (or, with Existing, opens it), and only then
// stores the password, the credentials and the config. env nil keeps the
// credentials file as it is (the CLI, where people write it by hand); a
// non-nil env replaces it.
func initTarget(p backupPaths, target string, opt initOptions, password string, env []string) error {
	target, err := normalizeTarget(target)
	if err != nil {
		return err
	}
	if password == "" {
		return errors.New("the password is empty")
	}
	typed := password
	password = backupPassword(password)
	if err := checkEnv(env); err != nil {
		return err
	}
	// The host key goes to the backup's known_hosts only when restic
	// accepts the target: before that, an old target on the same host and
	// port would start to use the backup's own key.
	hostLine, err := sftpHostLine(p, target, opt.HostKey, opt.Port)
	if err != nil {
		return err
	}

	repo, release, err := resolveTarget(target)
	if err != nil {
		return err
	}
	defer release()
	if !isURLTarget(repo) {
		// Only the last folder of a directory is made: a missing parent is
		// most likely a disk that is not mounted. On a disk by UUID, the
		// mount point is checked and the folders in it are made.
		parent, mkdir := filepath.Dir(repo), os.Mkdir
		if _, sub, ok := parseUUIDTarget(target); ok {
			parent, mkdir = strings.TrimSuffix(repo, "/"+sub), os.MkdirAll
		}
		if err := checkOnDisk(parent); err != nil {
			return err
		}
		if err := mkdir(repo, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
			return err
		}
		if strings.HasPrefix(target, "uuid:") {
			if err := checkOnMount(parent, repo); err != nil {
				return err
			}
		}
		if !opt.Existing && exists(filepath.Join(repo, "config")) {
			return errBackupThere(repo)
		}
	}

	// The password and the credentials go to new files first: an init that
	// fails must not replace those of a target that works.
	tmpPass := p.password() + ".new"
	if err := writeSecret(tmpPass, password); err != nil {
		return err
	}
	defer os.Remove(tmpPass)
	envFile := p.envFile()
	if env != nil {
		envFile = filepath.Join(p.Run, "env.new")
		if err := os.MkdirAll(p.Run, 0o700); err != nil {
			return err
		}
		if err := writeSecret(envFile, strings.Join(env, "\n")); err != nil {
			return err
		}
		defer os.Remove(envFile)
	}
	args := []string{"init"}
	if opt.Existing {
		// Opening, not creating: `cat config` fails on a wrong password.
		args = []string{"cat", "config"}
	}
	knownHosts := ""
	if hostLine != "" {
		if knownHosts, err = stageKnownHosts(p, hostLine); err != nil {
			return err
		}
		defer os.Remove(knownHosts)
	}
	run := func() (string, error) {
		cmd, err := resticCmdHosts(p, repo, tmpPass, envFile, knownHosts, args...)
		if err != nil {
			return "", err
		}
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		err = cmd.Run()
		return stderr.String(), err
	}
	var stderr string
	if opt.Existing {
		// The password that works is the one stored.
		stderr, err = withTypedPassword(tmpPass, typed, run)
	} else {
		stderr, err = run()
	}
	if err != nil {
		if s := lastLine(stderr); s != "" {
			return fmt.Errorf("could not use %s: %s", target, s)
		}
		return fmt.Errorf("could not use %s: %w", target, err)
	}

	if hostLine != "" {
		if err := addKnownHost(filepath.Join(sshDir(p), "known_hosts"), hostLine); err != nil {
			return err
		}
	}

	if env != nil {
		// /run is another file system: copy, then the rename in writeSecret.
		if err := writeSecret(p.envFile(), strings.Join(env, "\n")); err != nil {
			return err
		}
	}
	if err := os.Rename(tmpPass, p.password()); err != nil {
		return err
	}
	c, _ := loadBackupConfig(p)
	c.Target, c.Label, c.Last, c.Result = target, oneLine(opt.Label), 0, ""
	c.Containers = c.Containers || opt.Containers
	return saveBackupConfig(p, c)
}

// confirmSFTP makes restic reach an sftp: target with the backup's own SSH
// key, and trust the host key the user confirmed (hostKey, a SHA256
// fingerprint). port, when not 0, must be the target's. Other targets need
// nothing.
func confirmSFTP(p backupPaths, target, hostKey string, port uint32) error {
	line, err := sftpHostLine(p, target, hostKey, port)
	if err != nil || line == "" {
		return err
	}
	return addKnownHost(filepath.Join(sshDir(p), "known_hosts"), line)
}

// sftpHostLine checks an sftp: target as confirmSFTP does, makes the
// backup's SSH key, and returns the known_hosts line of the confirmed key;
// "" for a target that is not SFTP.
func sftpHostLine(p backupPaths, target, hostKey string, port uint32) (string, error) {
	host, tport, ok := parseSFTPTarget(target)
	if !ok {
		if strings.HasPrefix(target, "sftp:") {
			return "", fmt.Errorf("%q is not sftp://user@host:port//path or sftp:user@host:/path", target)
		}
		return "", nil
	}
	if port != 0 && port != tport {
		return "", fmt.Errorf("port %d is not the port of %s", port, target)
	}
	if hostKey == "" {
		return "", fmt.Errorf("the host key of %s is not confirmed", host)
	}
	if _, err := ensureSSHKey(p); err != nil {
		return "", err
	}
	return confirmedHostLine(p, host, tport, hostKey)
}

// stageKnownHosts writes a copy of the backup's known_hosts with line to a
// new file in p.Run, for restic to use before the line is kept. It returns
// its path; the caller removes it. Each call has its own file, so two setups
// at the same time do not share one.
func stageKnownHosts(p backupPaths, line string) (string, error) {
	old, err := os.ReadFile(filepath.Join(sshDir(p), "known_hosts"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	if err := os.MkdirAll(p.Run, 0o700); err != nil {
		return "", err
	}
	f, err := os.CreateTemp(p.Run, "known_hosts-*") // mode 0600
	if err != nil {
		return "", err
	}
	kh := f.Name()
	if len(old) > 0 && !bytes.HasSuffix(old, []byte("\n")) {
		old = append(old, '\n')
	}
	_, err = f.Write(old)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = addKnownHost(kh, line)
	}
	if err != nil {
		os.Remove(kh)
		return "", err
	}
	return kh, nil
}

// lastLine is the last line of restic's errors that says something.
func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return oneLine(lines[len(lines)-1])
}

// urlHasPassword says whether a URL target carries a password
// (rest:https://user:password@host/), which must not travel as a D-Bus
// string.
func urlHasPassword(target string) bool {
	_, rest, ok := strings.Cut(target, ":")
	if !ok || !isURLTarget(target) {
		return false
	}
	u, err := url.Parse(rest)
	if err != nil {
		return strings.Contains(rest, "@")
	}
	_, set := u.User.Password()
	return set
}

// checkBusTarget allows over the bus only the targets Isotope System sets
// up: a disk by UUID, a directory, REST, S3 and SFTP. The other restic
// backends stay with the CLI; rclone: in particular passes options to a
// program that runs as root.
func checkBusTarget(target string) error {
	t, err := normalizeTarget(target)
	if err != nil {
		return err
	}
	switch targetKind(t) {
	case "disk", "dir", "rest", "s3", "sftp":
		return nil
	}
	return fmt.Errorf("%q is not a USB disk, a directory, or a rest:, s3: or sftp: target", oneLine(target))
}

// backupInitMu makes BackupInit calls wait for each other.
var backupInitMu sync.Mutex

// initOptionsFrom reads BackupInit's a{sv}. An unknown key or a wrong type
// is refused.
func initOptionsFrom(m map[string]dbus.Variant) (initOptions, error) {
	var o initOptions
	for k, v := range m {
		var ok bool
		switch k {
		case "containers":
			o.Containers, ok = v.Value().(bool)
		case "existing":
			o.Existing, ok = v.Value().(bool)
		case "label":
			o.Label, ok = v.Value().(string)
		case "host_key":
			o.HostKey, ok = v.Value().(string)
		case "port":
			o.Port, ok = v.Value().(uint32)
		default:
			return o, fmt.Errorf("unknown option %q", k)
		}
		if !ok {
			return o, fmt.Errorf("option %q has the wrong type %s", k, v.Signature())
		}
	}
	return o, nil
}

// BackupInit reads the backup password (line 1) and the target's
// credentials (NAME=value lines) from fd and makes target the backup
// target. The password never goes back over the bus: for a new backup the
// app made it and had it confirmed.
func (a busAPI) BackupInit(sender dbus.Sender, target string, options map[string]dbus.Variant, fd dbus.UnixFD) *dbus.Error {
	done, derr := a.s.enter()
	if derr != nil {
		closeFD(fd)
		return derr
	}
	defer done()
	if err := a.s.authorize(sender, actionBackup); err != nil {
		closeFD(fd)
		return err
	}
	secrets, err := readSecrets(fd)
	if err != nil {
		return busError(err)
	}
	defer clear(secrets)
	opt, err := initOptionsFrom(options)
	if err != nil {
		return busError(err)
	}
	if urlHasPassword(target) {
		return busError(errors.New("the target has a password in it; send it with the credentials"))
	}
	if err := checkBusTarget(target); err != nil {
		return busError(err)
	}
	if !exists(restic()) {
		return busError(errBackupUnavailable)
	}
	// The backup job writes the config when it ends, and a restore
	// replaces it.
	if backupJobRunning(a.s.d.Status().State) {
		return busError(ErrBusy)
	}
	backupInitMu.Lock()
	defer backupInitMu.Unlock()
	// secrets[1:] is never nil: the credentials file is replaced, also by
	// none.
	return busError(initTarget(defaultBackupPaths(), target, opt, secrets[0], secrets[1:]))
}

// BackupSSHKey returns the public half of the backup's SSH key, made the
// first time.
func (a busAPI) BackupSSHKey(sender dbus.Sender) (string, *dbus.Error) {
	done, derr := a.s.enter()
	if derr != nil {
		return "", derr
	}
	defer done()
	if err := a.s.authorize(sender, actionBackup); err != nil {
		return "", err
	}
	backupInitMu.Lock()
	defer backupInitMu.Unlock()
	pub, err := ensureSSHKey(defaultBackupPaths())
	if err != nil {
		return "", busError(err)
	}
	return pub, nil
}

// BackupHostKey fetches the host keys of an SFTP server and returns their
// fingerprints, for the user to confirm before BackupInit.
func (a busAPI) BackupHostKey(sender dbus.Sender, host string, port uint32) ([]string, *dbus.Error) {
	done, derr := a.s.enter()
	if derr != nil {
		return nil, derr
	}
	defer done()
	if err := a.s.authorize(sender, actionBackup); err != nil {
		return nil, err
	}
	fps, err := scanHostKey(defaultBackupPaths(), host, port)
	if err != nil {
		return nil, busError(err)
	}
	return fps, nil
}

// usesBackupSSH says whether restic reaches an SFTP target with the
// backup's own key: the key exists and the backup's known_hosts has the
// target's host and port, which only acceptHostKey writes.
func usesBackupSSH(p backupPaths, target string) bool {
	host, port, ok := parseSFTPTarget(target)
	if !ok || !exists(filepath.Join(sshDir(p), "id_ed25519")) {
		return false
	}
	want := host
	if port != 22 {
		want = fmt.Sprintf("[%s]:%d", host, port)
	}
	b, _ := os.ReadFile(filepath.Join(sshDir(p), "known_hosts"))
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) < 3 || strings.HasPrefix(f[0], "#") || strings.HasPrefix(f[0], "@") {
			continue
		}
		for _, h := range strings.Split(f[0], ",") {
			if h == want {
				return true
			}
		}
	}
	return false
}
