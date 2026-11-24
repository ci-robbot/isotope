package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/godbus/dbus/v5"
)

// oldIMConnect is the real imConnect, kept before any fixture replaces it.
var oldIMConnect = imConnect

// imFixture moves the extension's paths into a temporary tree: fcitx5 (an
// executable file when installed is true), the input method files, the
// keyboard file and $XDG_CONFIG_HOME, clears the IM and layout variables,
// and records what imStart runs.
func imFixture(t *testing.T, installed bool, methods ...string) (calls *[][]string, profile string) {
	t.Helper()
	dir := t.TempDir()
	oldF, oldM, oldK, oldRun, oldConnect := imFcitx5, imMethodDir, imKeyboardFile, imRun, imConnect
	t.Cleanup(func() {
		imFcitx5, imMethodDir, imKeyboardFile, imRun, imConnect = oldF, oldM, oldK, oldRun, oldConnect
	})
	imFcitx5 = filepath.Join(dir, "bin", "fcitx5")
	imMethodDir = filepath.Join(dir, "share", "fcitx5", "inputmethod")
	imKeyboardFile = filepath.Join(dir, "etc", "keyboard")
	if installed {
		if err := os.MkdirAll(filepath.Dir(imFcitx5), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(imFcitx5, []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(imMethodDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, m := range methods {
		if err := os.WriteFile(filepath.Join(imMethodDir, m+".conf"), []byte("[InputMethod]\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "config"))
	for _, v := range append([]string{"XKB_DEFAULT_LAYOUT", "XKB_DEFAULT_VARIANT", "LANG", "LC_ALL"}, imVars...) {
		t.Setenv(v, "")
	}
	var rec [][]string
	imRun = func(name string, args ...string) error {
		rec = append(rec, append([]string{name}, args...))
		return nil
	}
	imConnect = func() (imController, error) { return nil, errors.New("fcitx5 is not running") }
	return &rec, filepath.Join(dir, "config", "fcitx5", "profile")
}

func TestInputMethodStartWithoutTheExtensionDoesNothing(t *testing.T) {
	calls, profile := imFixture(t, false)
	t.Setenv("XMODIFIERS", "@im=fcitx")
	var stderr bytes.Buffer
	if code := cmdInputMethod([]string{"start"}, &bytes.Buffer{}, &stderr); code != exitOK {
		t.Fatalf("start without fcitx5: exit %d, %s", code, stderr.String())
	}
	if len(*calls) != 0 {
		t.Fatalf("start without fcitx5 ran %v", *calls)
	}
	if _, err := os.Stat(profile); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("start without fcitx5 wrote a profile: %v", err)
	}
	if stderr.Len() != 0 {
		t.Fatalf("start without fcitx5 must be silent, said %q", stderr.String())
	}
}

func TestInputMethodStartSeedsAProfileAndStartsFcitx5(t *testing.T) {
	calls, profile := imFixture(t, true, "pinyin", "anthy", "hangul")
	t.Setenv("XKB_DEFAULT_LAYOUT", "de,us")
	t.Setenv("XKB_DEFAULT_VARIANT", "nodeadkeys,")
	t.Setenv("XMODIFIERS", "@im=fcitx")
	t.Setenv("QT_IM_MODULES", "wayland;fcitx")
	if code := cmdInputMethod([]string{"start"}, &bytes.Buffer{}, &bytes.Buffer{}); code != exitOK {
		t.Fatalf("start: exit %d", code)
	}
	want := [][]string{
		{"dbus-update-activation-environment", "--systemd", "XMODIFIERS", "QT_IM_MODULES"},
		{imFcitx5, "-d", "-r"},
	}
	if !reflect.DeepEqual(*calls, want) {
		t.Fatalf("start ran\n%v\nwant\n%v", *calls, want)
	}
	g, err := imReadProfile(profile)
	if err != nil {
		t.Fatal(err)
	}
	wantG := imGroup{Name: "Default", Layout: "de-nodeadkeys", Items: []imItem{
		{Name: "keyboard-de-nodeadkeys"}, {Name: "pinyin"}, {Name: "anthy"}, {Name: "hangul"}}}
	if !reflect.DeepEqual(g, wantG) {
		t.Fatalf("seeded group %+v, want %+v", g, wantG)
	}
	b, _ := os.ReadFile(profile)
	if !strings.Contains(string(b), "DefaultIM=pinyin\n") || !strings.Contains(string(b), "[GroupOrder]\n0=Default\n") {
		t.Fatalf("profile is not in fcitx5's shape:\n%s", b)
	}
}

func TestInputMethodStartKeepsAnExistingProfile(t *testing.T) {
	_, profile := imFixture(t, true, "pinyin")
	mine := imGroup{Name: "Mine", Layout: "fr", Items: []imItem{{Name: "keyboard-fr"}, {Name: "pinyin"}}}
	if err := imWriteProfile(profile, mine); err != nil {
		t.Fatal(err)
	}
	t.Setenv("LANG", "ja_JP.UTF-8")
	if code := cmdInputMethod([]string{"start"}, &bytes.Buffer{}, &bytes.Buffer{}); code != exitOK {
		t.Fatalf("start: exit %d", code)
	}
	g, err := imReadProfile(profile)
	if err != nil || !reflect.DeepEqual(g, mine) {
		t.Fatalf("start changed the user's profile: %+v, %v", g, err)
	}
}

func TestInputMethodStartWithNoVariablesSkipsTheActivationEnvironment(t *testing.T) {
	calls, _ := imFixture(t, true, "pinyin")
	cmdInputMethod([]string{"start"}, &bytes.Buffer{}, &bytes.Buffer{})
	if len(*calls) != 1 || (*calls)[0][0] != imFcitx5 {
		t.Fatalf("with no IM variable set, start must only run fcitx5, ran %v", *calls)
	}
}

func TestInputMethodDefaultGroupFollowsTheLanguage(t *testing.T) {
	imFixture(t, true, "pinyin", "anthy", "hangul")
	if err := os.MkdirAll(filepath.Dir(imKeyboardFile), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(imKeyboardFile, []byte("XKB_LAYOUT=jp\nXKB_VARIANT=\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for lang, want := range map[string][]string{
		"zh_CN.UTF-8": {"keyboard-jp", "pinyin"},
		"zh_TW.UTF-8": {"keyboard-jp", "pinyin"},
		"ja_JP.UTF-8": {"keyboard-jp", "anthy"},
		"ko_KR.UTF-8": {"keyboard-jp", "hangul"},
		"en_US.UTF-8": {"keyboard-jp", "pinyin", "anthy", "hangul"},
		"":            {"keyboard-jp", "pinyin", "anthy", "hangul"},
	} {
		t.Setenv("LANG", lang)
		g := imDefaultGroup(imMethodExists)
		var got []string
		for _, it := range g.Items {
			got = append(got, it.Name)
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("LANG=%q: %v, want %v", lang, got, want)
		}
	}
}

func TestInputMethodDefaultGroupLeavesOutMissingEngines(t *testing.T) {
	imFixture(t, true, "hangul")
	g := imDefaultGroup(imMethodExists)
	if len(g.Items) != 2 || g.Items[0].Name != "keyboard-us" || g.Items[1].Name != "hangul" {
		t.Fatalf("with only hangul installed and no layout: %+v", g.Items)
	}
}

func TestInputMethodLayoutRefusesOddNames(t *testing.T) {
	imFixture(t, true)
	t.Setenv("XKB_DEFAULT_LAYOUT", "us]\n[Groups/1")
	if l, v := imLayout(); l != "us" || v != "" {
		t.Fatalf("a layout that is not a name must fall back to us, got %q %q", l, v)
	}
	t.Setenv("XKB_DEFAULT_LAYOUT", "gb")
	t.Setenv("XKB_DEFAULT_VARIANT", "x=y")
	if l, v := imLayout(); l != "gb" || v != "" {
		t.Fatalf("an odd variant must be dropped, got %q %q", l, v)
	}
}

func TestInputMethodSetOfflineWritesTheProfile(t *testing.T) {
	_, profile := imFixture(t, true, "pinyin", "anthy", "hangul")
	t.Setenv("XKB_DEFAULT_LAYOUT", "us")
	var out, errb bytes.Buffer
	if code := cmdInputMethod([]string{"set", "hangul", "pinyin", "hangul"}, &out, &errb); code != exitOK {
		t.Fatalf("set: exit %d: %s", code, errb.String())
	}
	g, err := imReadProfile(profile)
	if err != nil {
		t.Fatal(err)
	}
	want := []imItem{{Name: "keyboard-us"}, {Name: "hangul"}, {Name: "pinyin"}}
	if !reflect.DeepEqual(g.Items, want) {
		t.Fatalf("set wrote %+v, want %+v", g.Items, want)
	}
	if !strings.Contains(out.String(), "keyboard-us, hangul, pinyin") {
		t.Fatalf("set said %q", out.String())
	}
	b, _ := os.ReadFile(profile)
	if !strings.Contains(string(b), "DefaultIM=hangul\n") {
		t.Fatalf("DefaultIM must be the first input method:\n%s", b)
	}
}

func TestInputMethodSetKeepsTheUsersKeyboard(t *testing.T) {
	_, profile := imFixture(t, true, "pinyin", "anthy")
	if err := imWriteProfile(profile, imGroup{Name: "Default", Layout: "fr", Items: []imItem{{Name: "keyboard-fr"}, {Name: "pinyin"}}}); err != nil {
		t.Fatal(err)
	}
	if code := cmdInputMethod([]string{"set", "anthy"}, &bytes.Buffer{}, &bytes.Buffer{}); code != exitOK {
		t.Fatalf("set: exit %d", code)
	}
	g, _ := imReadProfile(profile)
	if want := []imItem{{Name: "keyboard-fr"}, {Name: "anthy"}}; g.Layout != "fr" || !reflect.DeepEqual(g.Items, want) {
		t.Fatalf("set lost the keyboard: %+v", g)
	}
}

func TestInputMethodSetRefusesUnknownNames(t *testing.T) {
	_, profile := imFixture(t, true, "pinyin")
	var errb bytes.Buffer
	if code := cmdInputMethod([]string{"set", "pinyin", "mozc"}, &bytes.Buffer{}, &errb); code != exitUsage {
		t.Fatalf("an unknown input method must be a usage error, got %d", code)
	}
	if !strings.Contains(errb.String(), "no such input method: mozc") {
		t.Fatalf("the refusal must name it: %q", errb.String())
	}
	if _, err := os.Stat(profile); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("a refused set must write nothing")
	}
}

func TestInputMethodSetWithoutTheExtension(t *testing.T) {
	imFixture(t, false)
	var errb bytes.Buffer
	if code := cmdInputMethod([]string{"set", "pinyin"}, &bytes.Buffer{}, &errb); code != exitError {
		t.Fatalf("set without fcitx5: exit %d", code)
	}
	if !strings.Contains(errb.String(), "isotope ext install fcitx5") {
		t.Fatalf("set without fcitx5 must say how to install it: %q", errb.String())
	}
}

// fakeFcitx is the running fcitx5's controller.
type fakeFcitx struct {
	group imGroup
	avail []string
	set   []imGroup
}

func (f *fakeFcitx) CurrentGroup() (imGroup, error) { return f.group, nil }
func (f *fakeFcitx) SetGroup(g imGroup) error       { f.set = append(f.set, g); f.group = g; return nil }
func (f *fakeFcitx) Available() ([]string, error)   { return f.avail, nil }
func (f *fakeFcitx) Close()                         {}

func TestInputMethodSetWhileRunningGoesThroughFcitx5(t *testing.T) {
	_, profile := imFixture(t, true)
	fake := &fakeFcitx{
		group: imGroup{Name: "Default", Layout: "us", Items: []imItem{{Name: "keyboard-us"}, {Name: "pinyin"}}},
		avail: []string{"keyboard-us", "pinyin", "anthy", "hangul", "shuangpin"},
	}
	imConnect = func() (imController, error) { return fake, nil }
	if code := cmdInputMethod([]string{"set", "shuangpin"}, &bytes.Buffer{}, &bytes.Buffer{}); code != exitOK {
		t.Fatalf("set: exit %d", code)
	}
	want := imGroup{Name: "Default", Layout: "us", Items: []imItem{{Name: "keyboard-us"}, {Name: "shuangpin"}}}
	if len(fake.set) != 1 || !reflect.DeepEqual(fake.set[0], want) {
		t.Fatalf("SetInputMethodGroupInfo got %+v, want %+v", fake.set, want)
	}
	if _, err := os.Stat(profile); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("while fcitx5 runs it saves the profile itself; set must not write it")
	}
	var out bytes.Buffer
	cmdInputMethod(nil, &out, &bytes.Buffer{})
	if !strings.Contains(out.String(), "fcitx5: running") || !strings.Contains(out.String(), "keyboard-us, shuangpin") {
		t.Fatalf("status while running: %q", out.String())
	}
}

func TestInputMethodStatus(t *testing.T) {
	imFixture(t, false)
	var out bytes.Buffer
	if code := cmdInputMethod([]string{"status"}, &out, &bytes.Buffer{}); code != exitOK || !strings.Contains(out.String(), "isotope ext install fcitx5") {
		t.Fatalf("status without the extension: %d %q", code, out.String())
	}
	_, profile := imFixture(t, true, "pinyin")
	out.Reset()
	cmdInputMethod(nil, &out, &bytes.Buffer{})
	if !strings.Contains(out.String(), "not running") || !strings.Contains(out.String(), "next login") {
		t.Fatalf("status before the first login: %q", out.String())
	}
	if err := imWriteProfile(profile, imGroup{Name: "Default", Layout: "us", Items: []imItem{{Name: "keyboard-us"}, {Name: "pinyin"}}}); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	cmdInputMethod(nil, &out, &bytes.Buffer{})
	if !strings.Contains(out.String(), "Input methods: keyboard-us, pinyin") {
		t.Fatalf("status from the profile: %q", out.String())
	}
}

func TestInputMethodReadsAProfileFcitx5Wrote(t *testing.T) {
	// The shape fcitx5 5.1 writes, with its comments and a second group.
	p := filepath.Join(t.TempDir(), "profile")
	body := `[Groups/0]
# Group Name
Name=Other
# Layout
Default Layout=us
# Default Input Method
DefaultIM=keyboard-us

[Groups/0/Items/0]
# Name
Name=keyboard-us
# Layout
Layout=

[Groups/1]
# Group Name
Name=Default
# Layout
Default Layout=de
# Default Input Method
DefaultIM=pinyin

[Groups/1/Items/0]
# Name
Name=keyboard-de
# Layout
Layout=

[Groups/1/Items/1]
# Name
Name=pinyin
# Layout
Layout=

[GroupOrder]
0=Default
1=Other
`
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	g, err := imReadProfile(p)
	if err != nil {
		t.Fatal(err)
	}
	want := imGroup{Name: "Default", Layout: "de", Items: []imItem{{Name: "keyboard-de"}, {Name: "pinyin"}}}
	if !reflect.DeepEqual(g, want) {
		t.Fatalf("read %+v, want %+v", g, want)
	}
}

func TestInputMethodIsACommand(t *testing.T) {
	imFixture(t, false)
	var out bytes.Buffer
	if code := run([]string{"input-method", "help"}, nil, &out, &bytes.Buffer{}); code != exitOK || !strings.Contains(out.String(), "set NAME...") {
		t.Fatalf("isotope input-method help: %d %q", code, out.String())
	}
	if !strings.Contains(usage, "input-method [command]") {
		t.Fatal("the usage text must list input-method")
	}
}

// busFcitxServer is fcitx5's controller on a private bus: the four methods
// isotope input-method calls, with fcitx5's signatures.
type busFcitxServer struct {
	layout  string
	entries []struct{ Name, Layout string }
}

func (s *busFcitxServer) CurrentInputMethodGroup() (string, *dbus.Error) { return "Default", nil }

func (s *busFcitxServer) InputMethodGroupInfo(name string) (string, []struct{ Name, Layout string }, *dbus.Error) {
	if name != "Default" {
		return "", nil, dbus.MakeFailedError(errors.New("no such group"))
	}
	return s.layout, s.entries, nil
}

func (s *busFcitxServer) SetInputMethodGroupInfo(name, layout string, entries []struct{ Name, Layout string }) *dbus.Error {
	s.layout, s.entries = layout, entries
	return nil
}

func (s *busFcitxServer) AvailableInputMethods() ([]struct {
	Unique, Name, Native, Icon, Label, Lang string
	Configurable                            bool
}, *dbus.Error) {
	out := []struct {
		Unique, Name, Native, Icon, Label, Lang string
		Configurable                            bool
	}{}
	for _, n := range []string{"keyboard-us", "pinyin", "hangul"} {
		out = append(out, struct {
			Unique, Name, Native, Icon, Label, Lang string
			Configurable                            bool
		}{n, n, n, "", "", "", false})
	}
	return out, nil
}

func TestInputMethodTalksToFcitx5OverDBus(t *testing.T) {
	addr := privateBus(t)
	imFixture(t, true)
	imConnect = oldIMConnect
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", addr)

	// Not running: no owner of org.fcitx.Fcitx5.
	if c, err := imConnect(); err == nil {
		c.Close()
		t.Fatal("imConnect must fail while nobody owns org.fcitx.Fcitx5")
	}

	srv := &busFcitxServer{layout: "us", entries: []struct{ Name, Layout string }{{"keyboard-us", ""}, {"pinyin", ""}}}
	conn := busConn(t, addr)
	if err := conn.Export(srv, "/controller", "org.fcitx.Fcitx.Controller1"); err != nil {
		t.Fatal(err)
	}
	if reply, err := conn.RequestName("org.fcitx.Fcitx5", dbus.NameFlagDoNotQueue); err != nil || reply != dbus.RequestNameReplyPrimaryOwner {
		t.Fatalf("RequestName: %v %v", reply, err)
	}
	var out, errb bytes.Buffer
	if code := cmdInputMethod([]string{"set", "hangul"}, &out, &errb); code != exitOK {
		t.Fatalf("set over D-Bus: exit %d: %s", code, errb.String())
	}
	want := []struct{ Name, Layout string }{{"keyboard-us", ""}, {"hangul", ""}}
	if srv.layout != "us" || !reflect.DeepEqual(srv.entries, want) {
		t.Fatalf("fcitx5 got %q %+v, want us %+v", srv.layout, srv.entries, want)
	}
	if code := cmdInputMethod([]string{"set", "anthy"}, &bytes.Buffer{}, &errb); code != exitUsage {
		t.Fatalf("a method fcitx5 does not list must be refused, got %d", code)
	}
	out.Reset()
	cmdInputMethod(nil, &out, &bytes.Buffer{})
	if !strings.Contains(out.String(), "Input methods: keyboard-us, hangul") {
		t.Fatalf("status over D-Bus: %q", out.String())
	}
}
