package main

import (
	"context"
	"log"
	"time"

	"github.com/godbus/dbus/v5"

	"github.com/kairos-community/isotope/pkg/shares"
)

// The tray mounts the saved shares marked "mount at login" when the session
// starts: it starts with every desktop session, after pam_gnome_keyring has
// unlocked the login keyring, in a session that polkit sees as local and
// active. See docs/superpowers/specs/2026-11-24-network-shares-design.md.

// busMounter is shares.Mounter over isotope-updated's MountShare.
type busMounter struct{ conn *dbus.Conn }

// mountTimeout is longer than the daemon's 60 s per mount call, and NFS
// tries two versions.
const mountTimeout = 150 * time.Second

func (b busMounter) MountShare(url string, o shares.Options, c shares.Credentials) (string, error) {
	opts := map[string]dbus.Variant{}
	if o.Name != "" {
		opts["name"] = dbus.MakeVariant(o.Name)
	}
	if o.ReadOnly {
		opts["read_only"] = dbus.MakeVariant(true)
	}
	var point string
	err := withSecrets(c.Lines(), func(fd dbus.UnixFD) error {
		ctx, cancel := context.WithTimeout(context.Background(), mountTimeout)
		defer cancel()
		return b.conn.Object(busName, busPath).CallWithContext(ctx, busInterface+".MountShare", 0, url, opts, fd).Store(&point)
	})
	return point, wrapBusErr(err)
}

// Login mounts are tried this many times, this far apart: the network may
// still be coming up.
var (
	autoMountTries = 5
	autoMountWait  = 20 * time.Second
)

// mountSavedShares mounts the user's auto-mount shares. Every failure is
// logged; a share that cannot be mounted also gets one notification.
func mountSavedShares(logger *log.Logger) {
	path, err := shares.Path()
	if err != nil {
		return
	}
	list, err := shares.Load(path)
	if err != nil {
		logger.Printf("network shares: %v", err)
		return
	}
	want := false
	for _, s := range list {
		want = want || s.AutoMount
	}
	if !want {
		return
	}
	sys, err := dbus.SystemBus()
	if err != nil {
		logger.Printf("network shares: %v", err)
		return
	}
	a := shares.AutoMount{Mounter: busMounter{sys}, Tries: autoMountTries, Wait: autoMountWait, Log: logger.Printf,
		Notify: func(summary, body string) {
			if err := Notify(summary, body); err != nil {
				logger.Printf("network shares: the notification: %v", err)
			}
		}}
	if ses, err := dbus.SessionBus(); err == nil {
		a.Keyring = shares.SecretService{Conn: ses}
	}
	for url, point := range a.Run(list) {
		logger.Printf("network shares: %s is mounted at %s", url, point)
	}
}
