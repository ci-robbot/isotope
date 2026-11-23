package main

import (
	"github.com/godbus/dbus/v5"
)

// The desktop's light or dark preference, read from the XDG settings portal
// (org.freedesktop.appearance color-scheme), which every variant runs:
// COSMIC's own portal follows its dark mode, the GTK portal on sway, i3 and
// niri follows gsettings' color-scheme. The tray icon needs it because the
// panel gets a finished pixmap and cannot recolour it.

const (
	portalDest      = "org.freedesktop.portal.Desktop"
	portalPath      = "/org/freedesktop/portal/desktop"
	portalSettings  = "org.freedesktop.portal.Settings"
	appearanceNS    = "org.freedesktop.appearance"
	colorSchemeName = "color-scheme"
)

// schemeIsDark reads a color-scheme value: 1 prefers dark, 2 prefers light,
// 0 is no preference. Isotope's panels are dark unless the user chose light,
// so everything but an explicit 2 (or a value that cannot be read) is dark.
// Read returns the value wrapped in a second variant; ReadOne does not.
func schemeIsDark(v dbus.Variant) bool {
	for {
		inner, ok := v.Value().(dbus.Variant)
		if !ok {
			break
		}
		v = inner
	}
	n, ok := v.Value().(uint32)
	return !ok || n != 2
}

// watchColorScheme calls set with the desktop's preference (true is dark),
// now and on every change, until the session bus goes away. Without a bus or
// a portal it calls nothing, and the tray keeps the dark icons.
func watchColorScheme(conn *dbus.Conn, set func(dark bool)) {
	ch := make(chan *dbus.Signal, 8)
	conn.Signal(ch)
	_ = conn.AddMatchSignal(
		dbus.WithMatchObjectPath(portalPath),
		dbus.WithMatchInterface(portalSettings),
		dbus.WithMatchMember("SettingChanged"),
		dbus.WithMatchArg(0, appearanceNS),
		dbus.WithMatchArg(1, colorSchemeName),
	)
	obj := conn.Object(portalDest, portalPath)
	var v dbus.Variant
	if obj.Call(portalSettings+".ReadOne", 0, appearanceNS, colorSchemeName).Store(&v) == nil ||
		obj.Call(portalSettings+".Read", 0, appearanceNS, colorSchemeName).Store(&v) == nil {
		set(schemeIsDark(v))
	}
	for s := range ch {
		if s.Name != portalSettings+".SettingChanged" || len(s.Body) != 3 {
			continue
		}
		ns, _ := s.Body[0].(string)
		key, _ := s.Body[1].(string)
		val, ok := s.Body[2].(dbus.Variant)
		if ns == appearanceNS && key == colorSchemeName && ok {
			set(schemeIsDark(val))
		}
	}
}
