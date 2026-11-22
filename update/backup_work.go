package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

// The work folders of an export, a list of an exported file and a restore
// are on the persistent partition and can hold a whole backup. A job that
// is killed (a power cut, a SIGKILL) leaves them; the daemon removes them
// when it starts (sweepBackupWork). A job holds a flock on its folder while
// it runs, so the sweep never removes one in use, also one of a CLI run
// beside the daemon.

// workPattern is the name of a work folder in backupPaths.State.
const workPattern = "work-*"

// sweepMinAge keeps a folder that was just made: its job may not hold the
// lock yet.
const sweepMinAge = time.Minute

// lockDir takes the flock of dir, and returns the function that gives it
// back.
func lockDir(dir string) (unlock func(), err error) {
	fd, err := unix.Open(dir, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	if err := unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		unix.Close(fd)
		return nil, fmt.Errorf("%s is in use: %w", dir, err)
	}
	var once sync.Once
	return func() { once.Do(func() { unix.Close(fd) }) }, nil
}

// makeWorkDir makes a work folder in p.State that only root can reach, and
// locks it. done removes it and gives the lock back; it may run twice.
func makeWorkDir(p backupPaths) (dir string, done func(), err error) {
	if err := os.MkdirAll(p.State, 0o755); err != nil {
		return "", nil, err
	}
	dir, err = os.MkdirTemp(p.State, "work-")
	if err != nil {
		return "", nil, err
	}
	unlock, err := lockDir(dir)
	if err != nil {
		os.RemoveAll(dir)
		return "", nil, err
	}
	return dir, func() {
		_ = os.RemoveAll(dir)
		unlock()
	}, nil
}

// sweepBackupWork removes the work folders and the restore's stage that a
// killed job left. A folder whose lock is held, or that is younger than
// sweepMinAge, stays.
func sweepBackupWork(p backupPaths, stderr io.Writer) {
	dirs, _ := filepath.Glob(filepath.Join(p.State, workPattern))
	dirs = append(dirs, p.stage())
	for _, dir := range dirs {
		fi, err := os.Lstat(dir)
		if err != nil || !fi.IsDir() || time.Since(fi.ModTime()) < sweepMinAge {
			continue
		}
		unlock, err := lockDir(dir)
		if err != nil {
			continue
		}
		if err := os.RemoveAll(dir); err != nil {
			fmt.Fprintf(stderr, "isotope: removing the old %s: %v\n", dir, err)
		} else {
			fmt.Fprintf(stderr, "isotope: removed %s, left by a job that was stopped\n", dir)
		}
		unlock()
	}
}
