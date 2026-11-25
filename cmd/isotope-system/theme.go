package main

import (
	"errors"
	"io/fs"
	"os"
	"strings"
	"time"
	"unsafe"

	"github.com/godbus/dbus/v5"
	"golang.org/x/sys/unix"

	"github.com/kairos-community/isotope/pkg/cosmictheme"
	"github.com/kairos-community/isotope/pkg/guikit"
)

// The window takes its colours from its desktop: on COSMIC the running
// COSMIC theme, elsewhere Isotope's palette in the dark or light mode the
// desktop portal reports. It follows a change while it is open.

// paletteSource is where the window's palette comes from.
type paletteSource struct {
	// cosmic reads the COSMIC theme from dirs with read.
	cosmic bool
	dirs   []string
	read   func(string) ([]byte, error)
	// scheme is the portal's color-scheme: 1 dark, 2 light, 0 no
	// preference; ok is false without a portal.
	scheme func() (v uint32, ok bool)
}

// isCosmic says whether the session is COSMIC.
func isCosmic(getenv func(string) string) bool {
	for _, d := range strings.Split(getenv("XDG_CURRENT_DESKTOP"), ":") {
		if strings.EqualFold(d, "COSMIC") {
			return true
		}
	}
	return false
}

// palette is the palette to draw with now.
func (s paletteSource) palette() guikit.Palette {
	if s.cosmic {
		return guikit.CosmicPalette(cosmictheme.Load(s.dirs, s.read))
	}
	return guikit.IsotopePalette(schemeDark(s.scheme))
}

// schemeDark is the mode for a color-scheme answer: light only when the
// desktop prefers light (2); dark, Isotope's default, otherwise.
func schemeDark(scheme func() (uint32, bool)) bool {
	if scheme == nil {
		return true
	}
	v, ok := scheme()
	return !ok || v != 2
}

// The portal's Settings interface, on the object of filechooser.go.
const (
	settingsIface = "org.freedesktop.portal.Settings"
	appearance    = "org.freedesktop.appearance"
	colorScheme   = "color-scheme"
)

// portalScheme reads color-scheme from the desktop portal on conn.
func portalScheme(conn *dbus.Conn) func() (uint32, bool) {
	return func() (uint32, bool) {
		if conn == nil {
			return 0, false
		}
		var v dbus.Variant
		err := conn.Object(portalName, portalPath).Call(settingsIface+".ReadOne", dbus.FlagNoAutoStart, appearance, colorScheme).Store(&v)
		if err != nil {
			return 0, false
		}
		return schemeValue(v)
	}
}

// schemeValue unwraps color-scheme: a uint32, in a variant (ReadOne) or in
// a variant in a variant (the older Read).
func schemeValue(v dbus.Variant) (uint32, bool) {
	for range 3 {
		switch x := v.Value().(type) {
		case uint32:
			return x, true
		case dbus.Variant:
			v = x
			continue
		}
		break
	}
	return 0, false
}

// watchPortal calls changed each time the portal says color-scheme
// changed. It returns at once; the signals arrive on conn's goroutine.
func watchPortal(conn *dbus.Conn, changed func()) error {
	if conn == nil {
		return errors.New("no session bus")
	}
	if err := conn.AddMatchSignal(dbus.WithMatchInterface(settingsIface), dbus.WithMatchMember("SettingChanged")); err != nil {
		return err
	}
	ch := make(chan *dbus.Signal, 8)
	conn.Signal(ch)
	go func() {
		for sig := range ch {
			if sig.Name != settingsIface+".SettingChanged" || len(sig.Body) < 2 {
				continue
			}
			if ns, _ := sig.Body[0].(string); ns != appearance {
				continue
			}
			if key, _ := sig.Body[1].(string); key == colorScheme {
				changed()
			}
		}
	}()
	return nil
}

// watchCosmic calls changed after each change in dirs (the COSMIC mode
// and theme directories, cosmictheme.WatchDirs), until stop is closed.
// cosmic-config writes a key by renaming a new file over it, so a theme
// switch is a burst of events; changed runs once the burst settles. A
// directory that does not exist yet is created, as COSMIC Settings would,
// so its first key is seen.
func watchCosmic(dirs []string, changed func(), stop <-chan struct{}) error {
	fd, err := unix.InotifyInit1(unix.IN_CLOEXEC | unix.IN_NONBLOCK)
	if err != nil {
		return err
	}
	const mask = unix.IN_CLOSE_WRITE | unix.IN_MOVED_TO | unix.IN_CREATE | unix.IN_DELETE |
		unix.IN_MOVED_FROM | unix.IN_DELETE_SELF | unix.IN_MOVE_SELF
	add := func() error {
		for _, d := range dirs {
			if err := os.MkdirAll(d, 0o755); err != nil {
				return err
			}
			if _, err := unix.InotifyAddWatch(fd, d, mask); err != nil && !errors.Is(err, fs.ErrNotExist) {
				return err
			}
		}
		return nil
	}
	if err := add(); err != nil {
		unix.Close(fd)
		return err
	}
	go func() {
		defer unix.Close(fd)
		buf := make([]byte, 4096)
		for {
			select {
			case <-stop:
				return
			default:
			}
			fds := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
			n, err := unix.Poll(fds, 500)
			if err != nil && err != unix.EINTR {
				return
			}
			if n <= 0 {
				continue
			}
			got, err := unix.Read(fd, buf)
			if err != nil {
				if err == unix.EAGAIN || err == unix.EINTR {
					continue
				}
				return
			}
			if lostWatch(buf[:got]) {
				_ = add()
			}
			time.Sleep(80 * time.Millisecond) // let the burst settle
			// Drop what came meanwhile: one reload covers it.
			for {
				if _, err := unix.Read(fd, buf); err != nil {
					break
				}
			}
			changed()
		}
	}()
	return nil
}

// lostWatch says whether the events report a watched directory removed or
// moved: its watch is dead.
func lostWatch(buf []byte) bool {
	for len(buf) >= unix.SizeofInotifyEvent {
		ev := (*unix.InotifyEvent)(unsafe.Pointer(&buf[0]))
		if ev.Mask&(unix.IN_IGNORED|unix.IN_DELETE_SELF|unix.IN_MOVE_SELF) != 0 {
			return true
		}
		size := unix.SizeofInotifyEvent + int(ev.Len)
		if size > len(buf) {
			break
		}
		buf = buf[size:]
	}
	return false
}

// followTheme sets u's palette from src now and after every change of the
// desktop's theme, for the life of the process.
func followTheme(src paletteSource, session *dbus.Conn, set func(guikit.Palette)) {
	set(src.palette())
	reload := func() { set(src.palette()) }
	if src.cosmic {
		_ = watchCosmic(cosmictheme.WatchDirs(src.dirs), reload, nil)
		return
	}
	_ = watchPortal(session, reload)
}
