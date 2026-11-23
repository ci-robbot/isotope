package main

// `isotope backup --export FILE`: the backup of this machine as one file.
// The file is a tar of a restic repository made for it alone, so it is
// encrypted and compressed like any other backup, and `isotope restore
// --from FILE` reads it with the same code.

import (
	"archive/tar"
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"

	"golang.org/x/sys/unix"
)

func backupExport(p backupPaths, file string, containers, passStdin bool, in *bufio.Reader, stdout, stderr io.Writer) int {
	if err := exportTo(context.Background(), p, file, nil, onInterrupt, containers, passStdin, in, stdout, stderr); err != nil {
		if errors.Is(err, errPartial) {
			fmt.Fprintf(stderr, "isotope: exported, but %v.\n", err)
			return exitOK
		}
		fmt.Fprintln(stderr, "isotope: export:", err)
		return exitError
	}
	return exitOK
}

// exportOwner is the user an export is written for: every folder on the
// way to the file must be root's or theirs, and the file becomes theirs.
type exportOwner struct{ uid, gid int }

// fchownFile gives the open export file to its owner. A variable for the
// tests.
var fchownFile = func(f *os.File, uid, gid int) error { return f.Chown(uid, gid) }

// exportTo writes the backup of this machine to file. Cancelling ctx stops
// restic, and no file is made. With owner, the folder of the file is
// checked for that user (see openExportDir) and the file is given to them.
// interrupt is onInterrupt for the CLI and nil in the daemon.
func exportTo(ctx context.Context, p backupPaths, file string, owner *exportOwner, interrupt interruptHook, containers, passStdin bool, in *bufio.Reader, stdout, stderr io.Writer) error {
	file, err := checkExportFile(file)
	if err != nil {
		return err
	}
	// Root works in this folder for minutes. It is opened once, without
	// following a link, and everything after goes through that descriptor:
	// a user who owns a folder on the way cannot swap it for a link to
	// /etc meanwhile.
	dirfd, err := openExportDir(filepath.Dir(file), owner)
	if err != nil {
		return err
	}
	defer unix.Close(dirfd)
	name := filepath.Base(file)
	partial := name + ".partial"
	if err := unix.Fstatat(dirfd, name, &unix.Stat_t{}, unix.AT_SYMLINK_NOFOLLOW); err == nil {
		return fmt.Errorf("%s already exists", file)
	}

	// The machine's backup password when it has one, which the user already
	// keeps; otherwise a new one, shown once.
	var pw string
	if b, err := os.ReadFile(p.password()); err == nil && !passStdin {
		pw = strings.TrimSpace(string(b))
		fmt.Fprintln(stdout, "The file is encrypted with this machine's backup password.")
	} else if pw, err = newPassword(passStdin, in, stdout); err != nil {
		return err
	} else {
		// A new repository: the canonical form, which any way of typing
		// the shown key opens.
		pw = backupPassword(pw)
	}

	// The repository is made in a directory only root can reach, on the
	// persistent partition, then packed into the file. Not next to the file:
	// a user who owns that directory could swap ours for a link.
	work, doneWork, err := makeWorkDir(p)
	if err != nil {
		return err
	}
	passFile := filepath.Join(p.Run, "password.export")
	// Read by clean, which the interrupt hook runs on its own goroutine.
	var madePartial atomic.Bool
	clean := func() {
		doneWork()
		if madePartial.Load() {
			_ = unix.Unlinkat(dirfd, partial, 0)
		}
		_ = os.Remove(passFile)
	}
	stop := interrupt.install(clean)
	defer stop()
	defer clean()
	if err := writeSecret(passFile, pw); err != nil {
		return err
	}
	cmd, err := resticCmd(p, work, passFile, "init")
	if err != nil {
		return err
	}
	cmd.Stdout, cmd.Stderr = io.Discard, stderr
	if err := runCtx(ctx, cmd); err != nil {
		return fmt.Errorf("restic init: %w", err)
	}
	backupErr := backupOnce(ctx, p, work, passFile, containers, false, stdout, stderr)
	if backupErr != nil && !errors.Is(backupErr, errPartial) {
		return backupErr
	}
	// O_EXCL and O_NOFOLLOW also refuse a link a user put at that name.
	fd, err := unix.Openat(dirfd, partial, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	if err != nil {
		return fmt.Errorf("%s.partial: %w", file, err)
	}
	madePartial.Store(true)
	f := os.NewFile(uintptr(fd), file+".partial")
	err = packTo(work, f)
	if err == nil && owner != nil {
		err = giveExport(f, owner)
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		// Cancelled while packing: the file must not get its name.
		err = ctx.Err()
	}
	if err != nil {
		return err
	}
	if err := renameNoReplace(dirfd, partial, name); err != nil {
		return fmt.Errorf("%s: %w", file, err)
	}
	madePartial.Store(false)
	size := ""
	var st unix.Stat_t
	if unix.Fstatat(dirfd, name, &st, unix.AT_SYMLINK_NOFOLLOW) == nil {
		size = fmt.Sprintf(" (%.1f MB)", float64(st.Size)/1e6)
	}
	fmt.Fprintf(stdout, "Wrote %s%s. Restore it with `isotope restore --from %s`.\n", file, size, file)
	return backupErr
}

// giveExport gives the open export file to owner. A vfat or exFAT disk
// sets the owner of every file at mount time and refuses a chown: a file
// that is the owner's already is fine.
func giveExport(f *os.File, owner *exportOwner) error {
	err := fchownFile(f, owner.uid, owner.gid)
	if err == nil {
		return nil
	}
	var st unix.Stat_t
	if unix.Fstat(int(f.Fd()), &st) == nil && int(st.Uid) == owner.uid {
		return nil
	}
	return fmt.Errorf("could not give %s to you: %w", f.Name(), err)
}

// renameNoReplace renames from to to in dirfd, and never over a file that
// is there: a user may have put one there meanwhile. A file system without
// RENAME_NOREPLACE gets a check, then a plain rename.
func renameNoReplace(dirfd int, from, to string) error {
	err := unix.Renameat2(dirfd, from, dirfd, to, unix.RENAME_NOREPLACE)
	if !errors.Is(err, unix.EINVAL) && !errors.Is(err, unix.ENOSYS) {
		return err
	}
	if unix.Fstatat(dirfd, to, &unix.Stat_t{}, unix.AT_SYMLINK_NOFOLLOW) == nil {
		return os.ErrExist
	}
	return unix.Renameat(dirfd, from, dirfd, to)
}

// openExportDir opens dir one folder at a time from /, never through a
// link, and returns its descriptor. With owner, dir itself must belong to
// the owner, and the folders on the way to root or to the owner: /, /home
// and /run/media/<user>, where a USB disk is mounted, are root's. A folder
// of root's is never the target: root runs files in /oem and /etc, and the
// file is given to the owner. No folder may be writable by other users,
// except a root folder with the sticky bit (as /tmp) on the way.
func openExportDir(dir string, owner *exportOwner) (int, error) {
	const flags = unix.O_RDONLY | unix.O_DIRECTORY | unix.O_NOFOLLOW | unix.O_CLOEXEC
	fd, err := unix.Open("/", flags, 0)
	if err != nil {
		return -1, err
	}
	parts := strings.Split(strings.Trim(filepath.Clean(dir), "/"), "/")
	if parts[0] == "" {
		parts = nil
	}
	path := "/"
	for i := 0; ; i++ {
		if err := checkExportFolder(fd, path, owner, i == len(parts)); err != nil {
			unix.Close(fd)
			return -1, err
		}
		if i == len(parts) {
			return fd, nil
		}
		path = filepath.Join(path, parts[i])
		next, err := unix.Openat(fd, parts[i], flags, 0)
		unix.Close(fd)
		switch {
		case errors.Is(err, unix.ELOOP) || errors.Is(err, unix.ENOTDIR):
			return -1, fmt.Errorf("%s is a link or not a folder; refusing it", path)
		case errors.Is(err, unix.ENOENT):
			return -1, fmt.Errorf("%s does not exist. Is the disk plugged in?", path)
		case err != nil:
			return -1, fmt.Errorf("%s: %w", path, err)
		}
		fd = next
	}
}

// checkExportFolder is openExportDir's check of one folder.
func checkExportFolder(fd int, path string, owner *exportOwner, last bool) error {
	if owner == nil {
		return nil
	}
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	if int(st.Uid) != owner.uid && (last || st.Uid != 0) {
		return fmt.Errorf("%s belongs to another user; export into a folder of yours", path)
	}
	sticky := st.Uid == 0 && st.Mode&unix.S_ISVTX != 0 && !last
	if st.Mode&0o022 != 0 && !sticky {
		return fmt.Errorf("%s can be changed by other users; export into a folder only you can change", path)
	}
	return nil
}

// checkExportFile checks the file an export writes: an absolute path, not
// there yet, in a folder on a disk.
func checkExportFile(file string) (string, error) {
	file, err := normalizeTarget(file)
	if err != nil || isURLTarget(file) || !filepath.IsAbs(file) {
		return "", fmt.Errorf("%q is not an absolute file path", file)
	}
	if exists(file) {
		return "", fmt.Errorf("%s already exists", file)
	}
	if err := checkOnDisk(filepath.Dir(file)); err != nil {
		return "", err
	}
	return file, nil
}

// packDir writes the files under dir into a new tar at out, relative to
// dir.
func packDir(dir, out string) error {
	// O_EXCL also refuses a link a user put at that name.
	f, err := os.OpenFile(out, os.O_WRONLY|os.O_CREATE|os.O_EXCL|unix.O_NOFOLLOW, 0o600)
	if err != nil {
		return err
	}
	err = packTo(dir, f)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

// packTo writes the files under dir into a tar in f, relative to dir, and
// syncs it. Only directories and regular files: a restic repository has
// nothing else.
func packTo(dir string, f *os.File) error {
	tw := tar.NewWriter(f)
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || path == dir {
			return err
		}
		rel, _ := filepath.Rel(dir, path)
		info, err := d.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsDir() && !info.Mode().IsRegular() {
			return fmt.Errorf("%s: not a file or a directory", path)
		}
		h, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		h.Name = filepath.ToSlash(rel)
		h.Uname, h.Gname, h.Uid, h.Gid = "", "", 0, 0
		if err := tw.WriteHeader(h); err != nil {
			return err
		}
		if info.Mode().IsDir() {
			return nil
		}
		src, err := os.Open(path)
		if err != nil {
			return err
		}
		defer src.Close()
		_, err = io.Copy(tw, src)
		return err
	})
	if err == nil {
		err = tw.Close()
	}
	if err == nil {
		err = f.Sync()
	}
	return err
}

// unpackFile extracts an exported backup into dir, a new directory under
// /usr/local/isotope-backup, which only root can change. Names that are absolute, climb out with "..", or are
// anything but a directory or a regular file are refused.
func unpackFile(file, dir string) error {
	f, err := os.Open(file)
	if err != nil {
		return err
	}
	defer f.Close()
	tr := tar.NewReader(bufio.NewReader(f))
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("%s is not an exported backup: %w", file, err)
		}
		name := filepath.FromSlash(h.Name)
		if !filepath.IsLocal(name) {
			return fmt.Errorf("%s: refusing the entry %q", file, h.Name)
		}
		path := filepath.Join(dir, name)
		switch h.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(path, 0o700); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				return err
			}
			out, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
			if err != nil {
				return err
			}
			_, err = io.Copy(out, tr)
			if cerr := out.Close(); err == nil {
				err = cerr
			}
			if err != nil {
				return err
			}
		default:
			return fmt.Errorf("%s: refusing the entry %q: not a file or a directory", file, h.Name)
		}
	}
}
