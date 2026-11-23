package main

import (
	"os"
	"path/filepath"
	"syscall"
)

// lockInstance takes the user's lock of the whole flow, in dir (the
// XDG_RUNTIME_DIR), so the login hook and the menu's "Welcome" do not open
// two windows. ok is false when another instance holds it; release lets
// it go. Without dir, or a lock file, it lets the flow start.
func lockInstance(dir string) (release func(), ok bool) {
	none := func() {}
	if dir == "" {
		return none, true
	}
	f, err := os.OpenFile(filepath.Join(dir, "isotope-welcome.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return none, true
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return none, false
	}
	return func() { f.Close() }, true
}
