package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/godbus/dbus/v5"
)

// Input methods for Chinese, Japanese and Korean come from the fcitx5 system
// extension (extensions/fcitx5): fcitx5 with Pinyin (fcitx5-chinese-addons),
// Anthy and Hangul. The image itself only has the hooks, which do nothing
// while the extension is not merged:
//
//   - /etc/profile.d/isotope-input-method.sh exports XMODIFIERS and the other
//     IM variables when /usr/bin/fcitx5 exists;
//   - every session runs `isotope input-method start` (sway and i3 config.d,
//     niri spawn-at-startup, COSMIC /etc/xdg/autostart), which exits at once
//     without fcitx5, and otherwise gives a user with no fcitx5 profile one
//     (the keyboard layout, then the input method of the language, or all
//     three), hands the IM variables to D-Bus activated services, and starts
//     fcitx5.
//
// `isotope input-method set NAME...` chooses the input methods: through
// fcitx5's own D-Bus controller while it runs (it saves the profile itself),
// in its profile file otherwise. See
// docs/superpowers/specs/2026-11-24-input-methods-design.md.

const inputMethodUsage = `Usage: isotope input-method [command]

Typing Chinese, Japanese and Korean through fcitx5. It comes as a system
extension: isotope ext install fcitx5, then log out and in.

Commands:
  status            whether fcitx5 is installed and running, and the input
                    methods you switch between (default)
  set NAME...       the input methods to switch between, after the keyboard
                    layout: pinyin (Chinese), anthy (Japanese), hangul
                    (Korean), or any other fcitx5 input method; for example
                    "isotope input-method set pinyin hangul"
  help              this text

Started by the desktop session, not by hand:
  start             start fcitx5 when the extension is installed; does
                    nothing otherwise

Ctrl+Space turns the input method on and off. The fcitx5 icon in the panel
switches between the input methods.
`

// The extension's files. Variables so tests can move them.
var (
	imFcitx5       = "/usr/bin/fcitx5"
	imMethodDir    = "/usr/share/fcitx5/inputmethod"
	imKeyboardFile = "/etc/isotope/keyboard"
)

// imRun runs a program and waits for it. fcitx5 -d forks and returns. A
// variable so tests can record the calls.
var imRun = func(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
	return cmd.Run()
}

// imVars are the variables isotope-input-method.sh exports, handed to
// D-Bus activated services (the portals' file chooser) and user units.
var imVars = []string{"XMODIFIERS", "GTK_IM_MODULE", "QT_IM_MODULE", "QT_IM_MODULES", "SDL_IM_MODULE"}

// imItem is one input method of a group: its name (keyboard-us, pinyin)
// and a layout that is usually empty (the group's).
type imItem struct{ Name, Layout string }

// imGroup is one fcitx5 input method group.
type imGroup struct {
	Name   string
	Layout string
	Items  []imItem
}

// imController is fcitx5's D-Bus controller, an interface so tests need no
// fcitx5.
type imController interface {
	CurrentGroup() (imGroup, error)
	SetGroup(imGroup) error
	Available() ([]string, error)
	Close()
}

// imConnect returns the running fcitx5's controller, or an error when fcitx5
// does not own its name on the session bus.
var imConnect = func() (imController, error) {
	conn, err := dbus.ConnectSessionBus()
	if err != nil {
		return nil, err
	}
	var has bool
	if err := conn.BusObject().Call("org.freedesktop.DBus.NameHasOwner", 0, "org.fcitx.Fcitx5").Store(&has); err != nil || !has {
		conn.Close()
		if err == nil {
			err = errors.New("fcitx5 is not running")
		}
		return nil, err
	}
	return busFcitx{conn: conn, obj: conn.Object("org.fcitx.Fcitx5", "/controller")}, nil
}

type busFcitx struct {
	conn *dbus.Conn
	obj  dbus.BusObject
}

const fcitxController = "org.fcitx.Fcitx.Controller1."

func (b busFcitx) Close() { b.conn.Close() }

func (b busFcitx) CurrentGroup() (imGroup, error) {
	var g imGroup
	if err := b.obj.Call(fcitxController+"CurrentInputMethodGroup", 0).Store(&g.Name); err != nil {
		return g, err
	}
	var raw []struct{ Name, Layout string }
	if err := b.obj.Call(fcitxController+"InputMethodGroupInfo", 0, g.Name).Store(&g.Layout, &raw); err != nil {
		return g, err
	}
	for _, r := range raw {
		g.Items = append(g.Items, imItem{r.Name, r.Layout})
	}
	return g, nil
}

func (b busFcitx) SetGroup(g imGroup) error {
	entries := make([]struct{ Name, Layout string }, 0, len(g.Items))
	for _, it := range g.Items {
		entries = append(entries, struct{ Name, Layout string }{it.Name, it.Layout})
	}
	return b.obj.Call(fcitxController+"SetInputMethodGroupInfo", 0, g.Name, g.Layout, entries).Err
}

func (b busFcitx) Available() ([]string, error) {
	var raw []struct {
		Unique, Name, Native, Icon, Label, Lang string
		Configurable                            bool
	}
	if err := b.obj.Call(fcitxController+"AvailableInputMethods", 0).Store(&raw); err != nil {
		return nil, err
	}
	names := make([]string, 0, len(raw))
	for _, r := range raw {
		names = append(names, r.Unique)
	}
	return names, nil
}

func cmdInputMethod(args []string, stdout, stderr io.Writer) int {
	sub := "status"
	if len(args) > 0 {
		sub, args = args[0], args[1:]
	}
	switch sub {
	case "status":
		if len(args) != 0 {
			fmt.Fprint(stderr, inputMethodUsage)
			return exitUsage
		}
		return imStatus(stdout)
	case "set":
		if len(args) == 0 {
			fmt.Fprintf(stderr, "isotope input-method set: name at least one input method\n\n%s", inputMethodUsage)
			return exitUsage
		}
		return imSet(args, stdout, stderr)
	case "start":
		return imStart(stderr)
	case "help", "-h", "--help":
		fmt.Fprint(stdout, inputMethodUsage)
		return exitOK
	default:
		fmt.Fprintf(stderr, "isotope input-method: unknown command %q\n\n%s", sub, inputMethodUsage)
		return exitUsage
	}
}

func imInstalled() bool {
	fi, err := os.Stat(imFcitx5)
	return err == nil && fi.Mode().IsRegular() && fi.Mode()&0o111 != 0
}

const imNotInstalled = "Input methods are not installed. Install them with \"isotope ext install fcitx5\", then log out and in."

func imStatus(stdout io.Writer) int {
	if !imInstalled() {
		fmt.Fprintln(stdout, imNotInstalled)
		return exitOK
	}
	var g imGroup
	var err error
	if c, cerr := imConnect(); cerr == nil {
		defer c.Close()
		fmt.Fprintln(stdout, "fcitx5: running")
		g, err = c.CurrentGroup()
	} else {
		fmt.Fprintln(stdout, "fcitx5: installed, not running (it starts with the desktop session)")
		g, err = imReadProfile(imProfilePath())
		if errors.Is(err, os.ErrNotExist) {
			fmt.Fprintln(stdout, "Input methods: chosen at the next login")
			return exitOK
		}
	}
	if err != nil {
		fmt.Fprintln(stdout, "Input methods: cannot be read:", err)
		return exitError
	}
	names := make([]string, 0, len(g.Items))
	for _, it := range g.Items {
		names = append(names, it.Name)
	}
	fmt.Fprintln(stdout, "Input methods:", strings.Join(names, ", "))
	return exitOK
}

func imSet(names []string, stdout, stderr io.Writer) int {
	if !imInstalled() {
		fmt.Fprintln(stderr, imNotInstalled)
		return exitError
	}
	c, cerr := imConnect()
	if cerr == nil {
		defer c.Close()
	}
	// The names fcitx5 knows: its own list while it runs, the input
	// method files otherwise.
	known := func(n string) bool {
		if strings.HasPrefix(n, "keyboard-") {
			return true
		}
		_, err := os.Stat(filepath.Join(imMethodDir, n+".conf"))
		return err == nil
	}
	if c != nil {
		if avail, err := c.Available(); err == nil {
			set := map[string]bool{}
			for _, a := range avail {
				set[a] = true
			}
			known = func(n string) bool { return set[n] }
		}
	}
	var current imGroup
	var err error
	if c != nil {
		current, err = c.CurrentGroup()
	} else {
		current, err = imReadProfile(imProfilePath())
		if errors.Is(err, os.ErrNotExist) {
			current, err = imDefaultGroup(nil), nil
		}
	}
	if err != nil {
		fmt.Fprintln(stderr, "isotope input-method set: cannot read the current input methods:", err)
		return exitError
	}
	g, err := imReplaceMethods(current, names, known)
	if err != nil {
		fmt.Fprintln(stderr, "isotope input-method set:", err)
		return exitUsage
	}
	if c != nil {
		err = c.SetGroup(g)
	} else {
		err = imWriteProfile(imProfilePath(), g)
	}
	if err != nil {
		fmt.Fprintln(stderr, "isotope input-method set:", err)
		return exitError
	}
	list := make([]string, 0, len(g.Items))
	for _, it := range g.Items {
		list = append(list, it.Name)
	}
	fmt.Fprintln(stdout, "Input methods:", strings.Join(list, ", "))
	return exitOK
}

// imReplaceMethods keeps the group's keyboard layout first, unless the names
// bring their own keyboard-*, then the named input methods in order, each
// once. An unknown name is an error that lists it.
func imReplaceMethods(g imGroup, names []string, known func(string) bool) (imGroup, error) {
	var unknown []string
	for _, n := range names {
		if !known(n) {
			unknown = append(unknown, n)
		}
	}
	if len(unknown) > 0 {
		return g, fmt.Errorf("no such input method: %s (installed: the ones in %s)", strings.Join(unknown, ", "), imMethodDir)
	}
	out := imGroup{Name: g.Name, Layout: g.Layout}
	if out.Name == "" {
		out.Name = "Default"
	}
	seen := map[string]bool{}
	hasKeyboard := false
	for _, n := range names {
		if strings.HasPrefix(n, "keyboard-") {
			hasKeyboard = true
		}
	}
	if !hasKeyboard {
		for _, it := range g.Items {
			if strings.HasPrefix(it.Name, "keyboard-") {
				out.Items = append(out.Items, it)
				seen[it.Name] = true
				break
			}
		}
	}
	for _, n := range names {
		if !seen[n] {
			out.Items = append(out.Items, imItem{Name: n})
			seen[n] = true
		}
	}
	return out, nil
}

// imStart is the session hook: nothing without the extension; otherwise a
// first profile, the IM variables for D-Bus activated services, and fcitx5.
func imStart(stderr io.Writer) int {
	if !imInstalled() {
		return exitOK
	}
	p := imProfilePath()
	if _, err := os.Stat(p); errors.Is(err, os.ErrNotExist) {
		if err := imWriteProfile(p, imDefaultGroup(imMethodExists)); err != nil {
			fmt.Fprintln(stderr, "isotope input-method start: cannot write the first fcitx5 profile:", err)
		}
	}
	var set []string
	for _, v := range imVars {
		if os.Getenv(v) != "" {
			set = append(set, v)
		}
	}
	if len(set) > 0 {
		if err := imRun("dbus-update-activation-environment", append([]string{"--systemd"}, set...)...); err != nil {
			fmt.Fprintln(stderr, "isotope input-method start: dbus-update-activation-environment:", err)
		}
	}
	// -d: fork into the background once ready; -r: replace a fcitx5 left
	// from an earlier session of this user.
	if err := imRun(imFcitx5, "-d", "-r"); err != nil {
		fmt.Fprintln(stderr, "isotope input-method start: fcitx5:", err)
		return exitError
	}
	return exitOK
}

func imMethodExists(n string) bool {
	_, err := os.Stat(filepath.Join(imMethodDir, n+".conf"))
	return err == nil
}

// imProfilePath is fcitx5's profile: $XDG_CONFIG_HOME/fcitx5/profile.
func imProfilePath() string {
	dir := os.Getenv("XDG_CONFIG_HOME")
	if dir == "" || !filepath.IsAbs(dir) {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".config")
	}
	return filepath.Join(dir, "fcitx5", "profile")
}

// imLayout is the keyboard layout and variant of the session: the
// XKB_DEFAULT_* variables (isotope-keyboard.sh), else /etc/isotope/keyboard
// (the installer's choice), else us. Only the first of several layouts.
func imLayout() (layout, variant string) {
	layout, variant = os.Getenv("XKB_DEFAULT_LAYOUT"), os.Getenv("XKB_DEFAULT_VARIANT")
	if layout == "" {
		if f, err := os.Open(imKeyboardFile); err == nil {
			s := bufio.NewScanner(f)
			for s.Scan() {
				k, v, _ := strings.Cut(s.Text(), "=")
				switch k {
				case "XKB_LAYOUT":
					layout = v
				case "XKB_VARIANT":
					variant = v
				}
			}
			f.Close()
		}
	}
	layout, _, _ = strings.Cut(layout, ",")
	variant, _, _ = strings.Cut(variant, ",")
	if !imSafeName(layout) {
		layout, variant = "us", ""
	}
	if !imSafeName(variant) {
		variant = ""
	}
	return layout, variant
}

func imSafeName(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-') {
			return false
		}
	}
	return true
}

// imLanguageMethods is the input method for the language of LANG, or all
// three when it is none of Chinese, Japanese and Korean: whoever installs
// the extension wants at least one, and the panel icon switches.
func imLanguageMethods() []string {
	lang := os.Getenv("LC_ALL")
	if lang == "" {
		lang = os.Getenv("LANG")
	}
	switch {
	case strings.HasPrefix(lang, "zh"):
		return []string{"pinyin"}
	case strings.HasPrefix(lang, "ja"):
		return []string{"anthy"}
	case strings.HasPrefix(lang, "ko"):
		return []string{"hangul"}
	}
	return []string{"pinyin", "anthy", "hangul"}
}

// imDefaultGroup is a new user's group: the keyboard layout, then the
// language's input methods that exist (exists nil: all of them).
func imDefaultGroup(exists func(string) bool) imGroup {
	layout, variant := imLayout()
	kb := layout
	if variant != "" {
		kb += "-" + variant
	}
	g := imGroup{Name: "Default", Layout: kb, Items: []imItem{{Name: "keyboard-" + kb}}}
	for _, m := range imLanguageMethods() {
		if exists == nil || exists(m) {
			g.Items = append(g.Items, imItem{Name: m})
		}
	}
	return g
}

// imReadProfile reads the first group of an fcitx5 profile (the one in
// GroupOrder 0, else Groups/0).
func imReadProfile(path string) (imGroup, error) {
	f, err := os.Open(path)
	if err != nil {
		return imGroup{}, err
	}
	defer f.Close()
	sections := map[string]map[string]string{}
	var cur map[string]string
	s := bufio.NewScanner(f)
	for s.Scan() {
		line := strings.TrimSpace(s.Text())
		switch {
		case line == "" || strings.HasPrefix(line, "#"):
		case strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]"):
			cur = map[string]string{}
			sections[line[1:len(line)-1]] = cur
		case cur != nil:
			if k, v, ok := strings.Cut(line, "="); ok {
				cur[strings.TrimSpace(k)] = strings.TrimSpace(v)
			}
		}
	}
	if err := s.Err(); err != nil {
		return imGroup{}, err
	}
	want := sections["GroupOrder"]["0"]
	idx := -1
	for i := 0; ; i++ {
		sec, ok := sections["Groups/"+strconv.Itoa(i)]
		if !ok {
			break
		}
		if idx < 0 && (want == "" || sec["Name"] == want) {
			idx = i
		}
	}
	if idx < 0 {
		return imGroup{}, fmt.Errorf("%s has no input method group", path)
	}
	prefix := "Groups/" + strconv.Itoa(idx)
	g := imGroup{Name: sections[prefix]["Name"], Layout: sections[prefix]["Default Layout"]}
	for i := 0; ; i++ {
		it, ok := sections[prefix+"/Items/"+strconv.Itoa(i)]
		if !ok {
			break
		}
		g.Items = append(g.Items, imItem{Name: it["Name"], Layout: it["Layout"]})
	}
	return g, nil
}

// imWriteProfile writes a profile with one group, as fcitx5 writes it, by a
// rename so fcitx5 never reads half a file.
func imWriteProfile(path string, g imGroup) error {
	var b strings.Builder
	def := ""
	for _, it := range g.Items {
		if !strings.HasPrefix(it.Name, "keyboard-") {
			def = it.Name
			break
		}
	}
	if def == "" && len(g.Items) > 0 {
		def = g.Items[0].Name
	}
	fmt.Fprintf(&b, "[Groups/0]\nName=%s\nDefault Layout=%s\nDefaultIM=%s\n\n", g.Name, g.Layout, def)
	for i, it := range g.Items {
		fmt.Fprintf(&b, "[Groups/0/Items/%d]\nName=%s\nLayout=%s\n\n", i, it.Name, it.Layout)
	}
	fmt.Fprintf(&b, "[GroupOrder]\n0=%s\n", g.Name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".profile-*")
	if err != nil {
		return err
	}
	if _, err := tmp.WriteString(b.String()); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o644); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return os.Rename(tmp.Name(), path)
}
