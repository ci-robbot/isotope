package flatpak

import (
	"bufio"
	"sort"
	"strings"
)

// Metadata is an app's metadata keyfile: the [Context] and bus policy
// groups say what the sandbox lets it reach. `flatpak remote-info
// --show-metadata` prints the commit's copy ("key=value"); `flatpak info
// --show-metadata` the installed file ("key = value").
type Metadata struct {
	Groups map[string]map[string]string
}

// ParseMetadata reads a keyfile. Comments and lines outside a group are
// skipped.
func ParseMetadata(b []byte) Metadata {
	m := Metadata{Groups: map[string]map[string]string{}}
	var g map[string]string
	sc := bufio.NewScanner(strings.NewReader(string(b)))
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		l := strings.TrimSpace(sc.Text())
		switch {
		case l == "" || strings.HasPrefix(l, "#"):
		case strings.HasPrefix(l, "[") && strings.HasSuffix(l, "]"):
			name := l[1 : len(l)-1]
			if m.Groups[name] == nil {
				m.Groups[name] = map[string]string{}
			}
			g = m.Groups[name]
		case g != nil:
			if k, v, ok := strings.Cut(l, "="); ok {
				g[strings.TrimSpace(k)] = strings.TrimSpace(v)
			}
		}
	}
	return m
}

// Runtime is the runtime the metadata names, ID/ARCH/BRANCH.
func (m Metadata) Runtime() string {
	if g := m.Groups["Application"]; g != nil {
		return g["runtime"]
	}
	return ""
}

// Permissions are the parts of the metadata that open the sandbox.
type Permissions struct {
	Shared, Sockets, Devices, Features, Filesystems []string
	// SessionBus and SystemBus are the names the app may talk to or own.
	SessionBus, SystemBus map[string]string
}

// list splits a "a;b;c;" value, without the negated entries ("!x" takes a
// permission away).
func list(v string) []string {
	var out []string
	for _, x := range strings.Split(v, ";") {
		if x = strings.TrimSpace(x); x != "" && !strings.HasPrefix(x, "!") {
			out = append(out, x)
		}
	}
	return out
}

// normFS is a filesystem entry with its default mode left out: "home:rw"
// is "home", "xdg-config/X:create" is read and write, so "xdg-config/X".
func normFS(s string) string {
	s = strings.TrimSuffix(s, "/")
	if p, mode, ok := strings.Cut(s, ":"); ok {
		switch mode {
		case "rw", "create":
			return strings.TrimSuffix(p, "/")
		case "ro":
			return strings.TrimSuffix(p, "/") + ":ro"
		}
	}
	return s
}

// Permissions reads the [Context] and bus policy groups.
func (m Metadata) Permissions() Permissions {
	c := m.Groups["Context"]
	p := Permissions{SessionBus: map[string]string{}, SystemBus: map[string]string{}}
	if c != nil {
		p.Shared, p.Sockets, p.Devices, p.Features = list(c["shared"]), list(c["sockets"]), list(c["devices"]), list(c["features"])
		for _, f := range list(c["filesystems"]) {
			p.Filesystems = append(p.Filesystems, normFS(f))
		}
	}
	for group, into := range map[string]map[string]string{"Session Bus Policy": p.SessionBus, "System Bus Policy": p.SystemBus} {
		for name, policy := range m.Groups[group] {
			if policy == "talk" || policy == "own" {
				into[name] = policy
			}
		}
	}
	return p
}

func has(xs []string, x string) bool {
	for _, y := range xs {
		if y == x {
			return true
		}
	}
	return false
}

// homeFS says whether a filesystem entry is in the home folder.
func homeFS(f string) bool {
	f = strings.TrimSuffix(f, ":ro")
	if strings.HasPrefix(f, "~/") || f == "~" {
		return true
	}
	if !strings.HasPrefix(f, "xdg-") {
		return false
	}
	return !strings.HasPrefix(f, "xdg-run")
}

// fsCovered says whether old already lets the app reach f.
func fsCovered(old []string, f string) bool {
	if has(old, f) {
		return true
	}
	ro := strings.HasSuffix(f, ":ro")
	bare := strings.TrimSuffix(f, ":ro")
	if ro && has(old, bare) {
		return true
	}
	covers := func(all string) bool { return has(old, all) || (ro && has(old, all+":ro")) }
	switch {
	case bare == "home":
		return covers("host")
	case homeFS(bare):
		return covers("home") || covers("host")
	case strings.HasPrefix(bare, "/") && !strings.HasPrefix(bare, "/run") && !strings.HasPrefix(bare, "/tmp"):
		return covers("host")
	}
	return false
}

// busCovered says whether the old policy already lets the app talk to name:
// the same name, or a wildcard ("org.gtk.vfs.*") that matches it.
func busCovered(old map[string]string, name, policy string) bool {
	ok := func(p string) bool { return p == "own" || p == policy }
	if p, found := old[name]; found && ok(p) {
		return true
	}
	for n, p := range old {
		if strings.HasSuffix(n, ".*") && strings.HasPrefix(name, strings.TrimSuffix(n, "*")) && ok(p) {
			return true
		}
	}
	return false
}

// Added is what newer asks for that old did not have, in words for a
// person: the filesystems, devices, sockets, features, the network, and
// the D-Bus names it may talk to or own. Empty means the update asks for
// nothing new.
func Added(old, newer Permissions) []string {
	var out []string
	add := func(s string) {
		if s != "" && !has(out, s) {
			out = append(out, s)
		}
	}
	if has(newer.Shared, "network") && !has(old.Shared, "network") {
		add("the network")
	}
	for _, s := range newer.Sockets {
		switch {
		case has(old.Sockets, s):
		case s == "fallback-x11" && has(old.Sockets, "x11"):
		default:
			add(socketWords(s))
		}
	}
	for _, d := range newer.Devices {
		if !has(old.Devices, d) && !has(old.Devices, "all") {
			add(deviceWords(d))
		}
	}
	for _, f := range newer.Features {
		if !has(old.Features, f) {
			add(featureWords(f))
		}
	}
	for _, f := range newer.Filesystems {
		if !fsCovered(old.Filesystems, f) {
			add(fsWords(f))
		}
	}
	for _, bus := range []struct {
		old, newer map[string]string
		what       string
	}{{old.SessionBus, newer.SessionBus, "the service "}, {old.SystemBus, newer.SystemBus, "the system service "}} {
		names := make([]string, 0, len(bus.newer))
		for n := range bus.newer {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			if !busCovered(bus.old, n, bus.newer[n]) {
				add(bus.what + n)
			}
		}
	}
	return out
}

func socketWords(s string) string {
	switch s {
	case "x11", "fallback-x11", "wayland":
		return "the display"
	case "pulseaudio":
		return "sound and the microphone"
	case "ssh-auth":
		return "your SSH keys"
	case "gpg-agent":
		return "your GnuPG keys"
	case "pcsc":
		return "smart cards"
	case "cups":
		return "the printers"
	case "session-bus":
		return "every service of your session"
	case "system-bus":
		return "every service of the system"
	}
	return "the " + s + " socket"
}

func deviceWords(d string) string {
	switch d {
	case "all":
		return "all devices: cameras, microphones, USB and more"
	case "dri":
		return "graphics acceleration"
	case "kvm":
		return "virtual machines (KVM)"
	case "usb":
		return "USB devices"
	case "input":
		return "input devices"
	case "shm":
		return "shared memory"
	}
	return "the device " + d
}

func featureWords(f string) string {
	switch f {
	case "bluetooth":
		return "Bluetooth"
	case "devel":
		return "debugging other programs"
	case "canbus":
		return "the CAN bus"
	case "multiarch":
		return "32-bit programs"
	case "per-app-dev-shm":
		return "shared memory"
	}
	return "the feature " + f
}

// xdgNames are the folders of the xdg-* filesystem entries.
var xdgNames = map[string]string{
	"xdg-desktop": "Desktop", "xdg-documents": "Documents", "xdg-download": "Downloads",
	"xdg-music": "Music", "xdg-pictures": "Pictures", "xdg-public-share": "Public",
	"xdg-videos": "Videos", "xdg-templates": "Templates",
}

func fsWords(f string) string {
	ro := strings.HasSuffix(f, ":ro")
	f = strings.TrimSuffix(f, ":ro")
	var s string
	switch {
	case f == "host":
		s = "all your files"
	case f == "home":
		s = "your home folder"
	case f == "host-os":
		s = "the system's programs and libraries"
	case f == "host-etc":
		s = "the system's settings (/etc)"
	case xdgNames[f] != "":
		s = "your " + xdgNames[f] + " folder"
	default:
		s = "the folder " + f
	}
	if ro {
		s += " (read only)"
	}
	return s
}

// Reach is one line of "What it can reach".
type Reach struct {
	Title, Sub string
	// Warn: the app reaches your files.
	Warn bool
}

// Summary is what an app can reach in the details view: your files (a
// warning), the network, the display, sound, devices, and the rest as a
// count.
type Summary struct {
	Items  []Reach
	Others int
}

// Summarize is the details view of p.
func Summarize(p Permissions) Summary {
	var s Summary
	counted := map[string]bool{}
	fs := func(x string) bool { return has(p.Filesystems, x) || has(p.Filesystems, x+":ro") }
	switch {
	case fs("host"):
		sub := "Reads and changes your files and the other files on this computer"
		if !has(p.Filesystems, "host") {
			sub = "Reads your files and the other files on this computer"
		}
		s.Items = append(s.Items, Reach{"All your files", sub, true})
	case fs("home"):
		sub := "Reads and changes everything in your home folder"
		if !has(p.Filesystems, "home") {
			sub = "Reads everything in your home folder"
		}
		s.Items = append(s.Items, Reach{"All your files", sub, true})
	}
	for _, x := range []string{"host", "home"} {
		counted[x], counted[x+":ro"] = true, true
	}
	if fs("host-os") || fs("host-etc") {
		s.Items = append(s.Items, Reach{"System files", "Reads the system's programs or settings", true})
		for _, x := range []string{"host-os", "host-etc"} {
			counted[x], counted[x+":ro"] = true, true
		}
	}
	if has(p.Shared, "network") {
		s.Items = append(s.Items, Reach{"Network", "Can connect to the internet", false})
	}
	wl, x11 := has(p.Sockets, "wayland"), has(p.Sockets, "x11") || has(p.Sockets, "fallback-x11")
	switch {
	case wl && x11:
		s.Items = append(s.Items, Reach{"Display", "Wayland and X11 windows", false})
	case wl:
		s.Items = append(s.Items, Reach{"Display", "Wayland windows", false})
	case x11:
		s.Items = append(s.Items, Reach{"Display", "X11 windows", false})
	}
	if has(p.Sockets, "pulseaudio") {
		s.Items = append(s.Items, Reach{"Sound", "Plays sound, and can record from the microphone", false})
	}
	if has(p.Devices, "all") {
		s.Items = append(s.Items, Reach{"All devices", "Cameras, microphones, USB and every other device", false})
	} else {
		var ds []string
		for _, d := range p.Devices {
			if d != "shm" {
				ds = append(ds, deviceWords(d))
			}
		}
		if len(ds) > 0 {
			s.Items = append(s.Items, Reach{"Devices", capitalize(strings.Join(ds, ", ")), false})
		}
	}
	for _, f := range p.Filesystems {
		if !counted[f] {
			s.Others++
		}
	}
	for _, x := range p.Sockets {
		switch x {
		case "wayland", "x11", "fallback-x11", "pulseaudio", "inherit-wayland-socket":
		default:
			s.Others++
		}
	}
	for _, f := range p.Features {
		if f != "multiarch" && f != "per-app-dev-shm" {
			s.Others++
		}
	}
	s.Others += len(p.SessionBus) + len(p.SystemBus)
	return s
}

func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}
