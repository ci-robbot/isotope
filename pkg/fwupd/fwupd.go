// Package fwupd talks to fwupd, the firmware update daemon, over its D-Bus
// API on the system bus (org.freedesktop.fwupd at /). It is pure Go: the
// tray (cmd/isotope) and Isotope System both use it, as the user. Reading
// the devices and their updates needs no polkit; Install does, and
// 50-isotope-fwupd.rules lets only members of admin.
//
// See docs/superpowers/specs/2026-11-24-firmware-updates-design.md.
package fwupd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/godbus/dbus/v5"
)

// The daemon's name, object and interface.
const (
	BusName   = "org.freedesktop.fwupd"
	ObjPath   = dbus.ObjectPath("/")
	Interface = "org.freedesktop.fwupd"
)

// Device flags (libfwupd/fwupd-device.rs) that Isotope reads.
const (
	FlagInternal      uint64 = 1 << 0
	FlagUpdatable     uint64 = 1 << 1
	FlagNeedsReboot   uint64 = 1 << 8
	FlagNeedsShutdown uint64 = 1 << 17
)

// Update states (libfwupd/fwupd-enums.rs FwupdUpdateState).
const (
	StateUnknown uint32 = iota
	StatePending
	StateSuccess
	StateFailed
	StateNeedsReboot
	StateFailedTransient
)

// Release flags (libfwupd/fwupd-release.rs).
const (
	ReleaseTrustedPayload  uint64 = 1 << 0
	ReleaseTrustedMetadata uint64 = 1 << 1
	ReleaseIsUpgrade       uint64 = 1 << 2
)

// Device is one device fwupd knows.
type Device struct {
	ID          string
	Name        string
	Vendor      string
	Version     string
	Plugin      string
	Flags       uint64
	UpdateState uint32
	UpdateError string
}

// Updatable says fwupd can write new firmware to the device.
func (d Device) Updatable() bool { return d.Flags&FlagUpdatable != 0 }

// NeedsReboot says an update is staged and the machine must restart to
// apply it (a UEFI capsule).
func (d Device) NeedsReboot() bool {
	return d.Flags&FlagNeedsReboot != 0 || d.UpdateState == StateNeedsReboot
}

// Release is one firmware release for a device.
type Release struct {
	Version   string
	Name      string
	Summary   string
	Locations []string
	// Checksums are the release's checksums: SHA-1 (40 hex digits) and
	// SHA-256 (64).
	Checksums []string
	Size      uint64
	Flags     uint64
	RemoteID  string
}

// Update is a device with the newest release fwupd offers for it.
type Update struct {
	Device  Device
	Release Release
}

// Bus is the part of a D-Bus connection the client uses; *dbus.Conn is one.
type Bus interface {
	Object(dest string, path dbus.ObjectPath) dbus.BusObject
}

// Client calls fwupd. Name is the bus name, BusName unless a test sets it.
type Client struct {
	Bus  Bus
	Name string
}

// New returns a client on conn.
func New(conn Bus) *Client { return &Client{Bus: conn, Name: BusName} }

// Dial connects to the system bus.
func Dial() (*Client, *dbus.Conn, error) {
	conn, err := dbus.ConnectSystemBus()
	if err != nil {
		return nil, nil, err
	}
	return New(conn), conn, nil
}

func (c *Client) obj() dbus.BusObject { return c.Bus.Object(c.Name, ObjPath) }

// callTimeout bounds the reads; a daemon that is starting enumerates the
// hardware first, which takes seconds.
var callTimeout = 90 * time.Second

func (c *Client) call(method string, out any, args ...any) error {
	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()
	call := c.obj().CallWithContext(ctx, Interface+"."+method, 0, args...)
	if call.Err != nil {
		return call.Err
	}
	if out == nil {
		return nil
	}
	return call.Store(out)
}

// Devices lists every device fwupd knows.
func (c *Client) Devices() ([]Device, error) {
	var raw []map[string]dbus.Variant
	if err := c.call("GetDevices", &raw); err != nil {
		return nil, err
	}
	out := make([]Device, 0, len(raw))
	for _, m := range raw {
		out = append(out, deviceOf(m))
	}
	return out, nil
}

// Upgrades lists the releases newer than what the device runs, newest
// first. A device with none, or with no metadata, gives an empty list, not
// an error.
func (c *Client) Upgrades(deviceID string) ([]Release, error) {
	var raw []map[string]dbus.Variant
	if err := c.call("GetUpgrades", &raw, deviceID); err != nil {
		if NoUpdate(err) {
			return nil, nil
		}
		return nil, err
	}
	out := make([]Release, 0, len(raw))
	for _, m := range raw {
		out = append(out, releaseOf(m))
	}
	return out, nil
}

// Updates lists the updatable devices that have an upgrade, with the newest
// one, ordered by device name.
func (c *Client) Updates() ([]Update, error) {
	devs, err := c.Devices()
	if err != nil {
		return nil, err
	}
	var out []Update
	for _, d := range devs {
		if !d.Updatable() {
			continue
		}
		rels, err := c.Upgrades(d.ID)
		if err != nil {
			return nil, err
		}
		if len(rels) > 0 {
			out = append(out, Update{Device: d, Release: rels[0]})
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Device.Name < out[j].Device.Name })
	return out, nil
}

// installTimeout bounds Install: a slow device (a dock, a hub chain) can
// take many minutes to write.
var installTimeout = 60 * time.Minute

// Install hands the cabinet file to fwupd for the device. fwupd checks its
// signature against the metadata. It returns when fwupd is done: written,
// or staged for the next restart (the device then NeedsReboot).
func (c *Client) Install(ctx context.Context, deviceID string, cab *os.File) error {
	ctx, cancel := context.WithTimeout(ctx, installTimeout)
	defer cancel()
	opts := map[string]dbus.Variant{}
	return c.obj().CallWithContext(ctx, Interface+".Install", 0, deviceID, dbus.UnixFD(cab.Fd()), opts).Err
}

// Progress is fwupd's Status (FwupdStatus) and Percentage.
type Progress struct {
	Status  uint32
	Percent uint32
}

// FwupdStatus values that read as a sentence.
var statusText = map[uint32]string{
	2: "Loading", 3: "Decompressing", 4: "Restarting the device",
	5: "Writing", 6: "Verifying", 7: "Scheduling the update for the next restart",
	8: "Downloading", 9: "Reading", 10: "Erasing", 11: "Waiting for authentication",
	12: "The device is busy",
}

// Text is the progress as one line, "" when there is nothing to say.
func (p Progress) Text() string {
	s := statusText[p.Status]
	if s == "" {
		return ""
	}
	if p.Percent > 0 && p.Percent <= 100 {
		return fmt.Sprintf("%s… %d%%", s, p.Percent)
	}
	return s + "…"
}

// Progress reads the daemon's Status and Percentage.
func (c *Client) Progress() (Progress, error) {
	var p Progress
	o := c.obj()
	v, err := o.GetProperty(Interface + ".Status")
	if err != nil {
		return p, err
	}
	if s, ok := v.Value().(uint32); ok {
		p.Status = s
	}
	v, err = o.GetProperty(Interface + ".Percentage")
	if err != nil {
		return p, err
	}
	if s, ok := v.Value().(uint32); ok {
		p.Percent = s
	}
	return p, nil
}

// ErrorName is the D-Bus error name of err ("" for another error). godbus
// returns a dbus.Error value from a call; a *dbus.Error counts too.
func ErrorName(err error) string {
	var e dbus.Error
	if errors.As(err, &e) {
		return e.Name
	}
	var p *dbus.Error
	if errors.As(err, &p) && p != nil {
		return p.Name
	}
	return ""
}

// NoUpdate says err is fwupd's way of saying there is nothing to install:
// no newer release, no metadata, or the device is not supported.
func NoUpdate(err error) bool {
	switch ErrorName(err) {
	case "org.freedesktop.fwupd.NothingToDo", "org.freedesktop.fwupd.NotSupported", "org.freedesktop.fwupd.NotFound":
		return true
	}
	return false
}

// Missing says err means fwupd is not on the bus: not installed, or its
// activation failed.
func Missing(err error) bool {
	switch ErrorName(err) {
	case "org.freedesktop.DBus.Error.ServiceUnknown", "org.freedesktop.DBus.Error.NameHasNoOwner", "org.freedesktop.systemd1.NoSuchUnit":
		return true
	}
	return false
}

// Message is the text of a D-Bus error, without its name.
func Message(err error) string {
	var body []any
	var e dbus.Error
	var p *dbus.Error
	switch {
	case errors.As(err, &e):
		body = e.Body
	case errors.As(err, &p) && p != nil:
		body = p.Body
	}
	if len(body) > 0 {
		if s, ok := body[0].(string); ok && s != "" {
			return s
		}
	}
	return err.Error()
}

func str(m map[string]dbus.Variant, k string) string {
	if v, ok := m[k]; ok {
		if s, ok := v.Value().(string); ok {
			return s
		}
	}
	return ""
}

func u64(m map[string]dbus.Variant, k string) uint64 {
	if v, ok := m[k]; ok {
		switch n := v.Value().(type) {
		case uint64:
			return n
		case uint32:
			return uint64(n)
		case int64:
			return uint64(n)
		}
	}
	return 0
}

func strs(m map[string]dbus.Variant, k string) []string {
	if v, ok := m[k]; ok {
		switch s := v.Value().(type) {
		case []string:
			return s
		case string:
			if s != "" {
				return []string{s}
			}
		}
	}
	return nil
}

func deviceOf(m map[string]dbus.Variant) Device {
	return Device{
		ID:          str(m, "DeviceId"),
		Name:        str(m, "Name"),
		Vendor:      str(m, "Vendor"),
		Version:     str(m, "Version"),
		Plugin:      str(m, "Plugin"),
		Flags:       u64(m, "Flags"),
		UpdateState: uint32(u64(m, "UpdateState")),
		UpdateError: str(m, "UpdateError"),
	}
}

func releaseOf(m map[string]dbus.Variant) Release {
	r := Release{
		Version:  str(m, "Version"),
		Name:     str(m, "Name"),
		Summary:  str(m, "Summary"),
		Size:     u64(m, "Size"),
		Flags:    u64(m, "Flags"),
		RemoteID: str(m, "RemoteId"),
	}
	r.Locations = strs(m, "Locations")
	if u := str(m, "Uri"); u != "" && len(r.Locations) == 0 {
		r.Locations = []string{u}
	}
	// fwupd joins the checksums with commas in one string.
	for _, c := range strings.Split(str(m, "Checksum"), ",") {
		if c = strings.TrimSpace(c); c != "" {
			r.Checksums = append(r.Checksums, c)
		}
	}
	return r
}
