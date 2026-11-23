package main

import "testing"

// M4: one whole flow per user. A second instance does not get the lock
// while the first holds it; it does once the first lets go.
func TestLockInstance(t *testing.T) {
	dir := t.TempDir()
	release, ok := lockInstance(dir)
	if !ok {
		t.Fatal("first instance must get the lock")
	}
	if _, ok := lockInstance(dir); ok {
		t.Fatal("second instance got the lock")
	}
	release()
	release2, ok := lockInstance(dir)
	if !ok {
		t.Fatal("lock not free after release")
	}
	release2()
	// No runtime directory: start anyway.
	if r, ok := lockInstance(""); !ok {
		t.Fatal("no XDG_RUNTIME_DIR must not stop the flow")
	} else {
		r()
	}
}
