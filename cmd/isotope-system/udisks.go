package main

// udisks2 on the system bus, as the user: polkit decides. An active local
// session may mount, unmount, eject and power off a removable disk
// (50-isotope-udisks.rules); a local active admin may also format it. The
// system disk needs the *-system actions, which no Isotope rule grants.

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/godbus/dbus/v5"
)

// Disks is udisks2, as far as the Disks page uses it. Every method blocks
// on D-Bus; the page calls them off the UI thread.
type Disks interface {
	Drives() ([]Drive, error)
	// Mount mounts a filesystem block (an object path) and returns where.
	Mount(block string) (mountPoint string, err error)
	Unmount(block string) error
	Eject(drive string) error
	PowerOff(drive string) error
	// Format makes one partition on the whole drive with a fsType
	// ("exfat" or "ext4") filesystem named label. It refuses a system
	// drive, and a drive with a mounted filesystem: the caller unmounts
	// first.
	Format(drive, fsType, label string) error
	// Watch sends on changed when a drive or block comes or goes, or a
	// filesystem is mounted or unmounted. stop ends it.
	Watch() (changed <-chan struct{}, stop func(), err error)
}

// udisks is the Disks of the real udisks2.
type udisks struct{ conn *dbus.Conn }

// dialDisks connects to udisks2; an error when it is not on the bus.
func dialDisks() (*udisks, error) {
	c, err := dbus.ConnectSystemBus()
	if err != nil {
		return nil, err
	}
	// Ping starts udisks2 if it is activatable and fails when it is
	// missing.
	if err := c.Object(udisksName, udisksPath).Call("org.freedesktop.DBus.Peer.Ping", 0).Err; err != nil {
		c.Close()
		return nil, fmt.Errorf("udisks2: %w", err)
	}
	return &udisks{conn: c}, nil
}

// noOptions is udisks2's empty a{sv}.
var noOptions = map[string]dbus.Variant{}

// formatTimeout bounds the partitioning and mkfs of a large disk.
const formatTimeout = 10 * time.Minute

func (u *udisks) Drives() ([]Drive, error) {
	var objs managedObjects
	err := u.conn.Object(udisksName, udisksPath).
		Call("org.freedesktop.DBus.ObjectManager.GetManagedObjects", 0).Store(&objs)
	if err != nil {
		return nil, fmt.Errorf("listing the disks: %w", err)
	}
	ds := drivesFrom(objs)
	fillUsage(ds)
	return ds, nil
}

func (u *udisks) Mount(block string) (string, error) {
	var mp string
	err := u.conn.Object(udisksName, dbus.ObjectPath(block)).Call(udisksFilesystem+".Mount", 0, noOptions).Store(&mp)
	return mp, err
}

func (u *udisks) Unmount(block string) error {
	return u.conn.Object(udisksName, dbus.ObjectPath(block)).Call(udisksFilesystem+".Unmount", 0, noOptions).Err
}

func (u *udisks) Eject(drive string) error {
	return u.conn.Object(udisksName, dbus.ObjectPath(drive)).Call(udisksDrive+".Eject", 0, noOptions).Err
}

func (u *udisks) PowerOff(drive string) error {
	return u.conn.Object(udisksName, dbus.ObjectPath(drive)).Call(udisksDrive+".PowerOff", 0, noOptions).Err
}

// mbrType is the MBR partition type of each filesystem Format makes.
var mbrType = map[string]string{"exfat": "0x07", "ext4": "0x83"}

// formatCheck finds the whole-disk block of drive in ds and refuses what
// Format must never do: a system drive, a mounted filesystem on the drive
// or on a device stacked on it (above: LVM, dm, md, LUKS; nil skips that
// check), another filesystem than exFAT or ext4.
func formatCheck(ds []Drive, drive, fsType string, roots []string, above func(devs []string) (string, error)) (whole Block, err error) {
	if mbrType[fsType] == "" {
		return Block{}, fmt.Errorf("cannot format as %q, only as exfat or ext4", fsType)
	}
	for _, d := range ds {
		if d.ID != drive {
			continue
		}
		switch driveReason(d, roots) {
		case reasonIsotope:
			return Block{}, errors.New("this is Isotope's system disk, it cannot be formatted")
		case reasonInternal:
			return Block{}, errors.New("this is an internal disk, it cannot be formatted here")
		}
		var devs []string
		for _, b := range d.Blocks {
			if len(b.MountPoints) > 0 {
				return Block{}, fmt.Errorf("%s is mounted: unmount it first", b.Device)
			}
			devs = append(devs, b.Device)
		}
		if above != nil {
			what, err := above(devs)
			if err != nil {
				return Block{}, fmt.Errorf("cannot tell what is mounted on the disk: %w", err)
			}
			if what != "" {
				return Block{}, fmt.Errorf("%s is mounted: unmount it first", what)
			}
		}
		if len(d.Blocks) == 0 || d.Blocks[0].IsPartition || d.Blocks[0].cleartext {
			return Block{}, errors.New("the disk has no block device")
		}
		return d.Blocks[0], nil
	}
	return Block{}, errors.New("the disk is not connected")
}

// The partition Format makes starts at 1 MiB, the alignment every
// partitioning tool uses, and takes the rest of the disk: udisks2 reads a
// size of 0 as the maximal size.
const (
	partOffset = 1 << 20
	partSize   = 0
)

func (u *udisks) Format(drive, fsType, label string) error {
	roots, err := rootDevices()
	if err != nil {
		return err
	}
	ds, err := u.Drives()
	if err != nil {
		return err
	}
	whole, err := formatCheck(ds, drive, fsType, roots, host.mountedAbove)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), formatTimeout)
	defer cancel()
	o := u.conn.Object(udisksName, dbus.ObjectPath(whole.ID))
	if err := o.CallWithContext(ctx, udisksBlock+".Format", 0, "dos", noOptions).Err; err != nil {
		return fmt.Errorf("making the partition table: %w", err)
	}
	var part dbus.ObjectPath
	err = o.CallWithContext(ctx, udisksPTable+".CreatePartitionAndFormat", 0,
		uint64(partOffset), uint64(partSize), mbrType[fsType], "", noOptions,
		fsType, map[string]dbus.Variant{
			"label": dbus.MakeVariant(label),
			// the caller owns the new filesystem (udisks2 >= 2.7.2), so an
			// ext4 disk is writable without root
			"take-ownership": dbus.MakeVariant(true),
		}).Store(&part)
	if err != nil {
		return fmt.Errorf("making the %s filesystem: %w", fsType, err)
	}
	return nil
}

func (u *udisks) Watch() (<-chan struct{}, func(), error) {
	matches := [][]dbus.MatchOption{
		{dbus.WithMatchSender(udisksName), dbus.WithMatchObjectPath(udisksPath),
			dbus.WithMatchInterface("org.freedesktop.DBus.ObjectManager"), dbus.WithMatchMember("InterfacesAdded")},
		{dbus.WithMatchSender(udisksName), dbus.WithMatchObjectPath(udisksPath),
			dbus.WithMatchInterface("org.freedesktop.DBus.ObjectManager"), dbus.WithMatchMember("InterfacesRemoved")},
		// a mount or unmount, from this window or elsewhere
		{dbus.WithMatchSender(udisksName), dbus.WithMatchPathNamespace(udisksPath),
			dbus.WithMatchInterface("org.freedesktop.DBus.Properties"), dbus.WithMatchMember("PropertiesChanged"),
			dbus.WithMatchArg(0, udisksFilesystem)},
	}
	for i, m := range matches {
		if err := u.conn.AddMatchSignal(m...); err != nil {
			for _, done := range matches[:i] {
				_ = u.conn.RemoveMatchSignal(done...)
			}
			return nil, nil, fmt.Errorf("watching the disks: %w", err)
		}
	}
	sigs := make(chan *dbus.Signal, 64)
	u.conn.Signal(sigs)
	out := make(chan struct{}, 1)
	done := make(chan struct{})
	go func() {
		defer close(out)
		for {
			select {
			case <-done:
				return
			case s, ok := <-sigs:
				if !ok {
					return
				}
				if !udisksSignal(s) {
					continue
				}
				// one pending change is enough: the page reads all
				select {
				case out <- struct{}{}:
				default:
				}
			}
		}
	}()
	var once sync.Once
	return out, func() {
		once.Do(func() {
			close(done)
			u.conn.RemoveSignal(sigs)
			for _, m := range matches {
				_ = u.conn.RemoveMatchSignal(m...)
			}
		})
	}, nil
}

// udisksSignal tells the signals Watch subscribed to from others on the
// same connection.
func udisksSignal(s *dbus.Signal) bool {
	switch s.Name {
	case "org.freedesktop.DBus.ObjectManager.InterfacesAdded", "org.freedesktop.DBus.ObjectManager.InterfacesRemoved":
		return s.Path == udisksPath
	case "org.freedesktop.DBus.Properties.PropertiesChanged":
		return len(s.Path) > len(udisksPath) && string(s.Path[:len(udisksPath)]) == udisksPath
	}
	return false
}
