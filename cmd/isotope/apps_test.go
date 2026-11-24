package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kairos-community/isotope/pkg/flatpak"
)

// fakeFlatpak is a flatpak script on a temporary PATH that answers from
// pkg/flatpak's recorded outputs and logs its calls.
const fakeFlatpak = `#!/bin/sh
echo "$*" >> "$FAKE_FLATPAK_LOG"
case "$*" in
"--user remotes --columns=name,options") [ -f "$FAKE_FLATPAK_DIR/noflathub" ] || echo flathub ;;
"--user update --appstream flathub") exit 0 ;;
"--user remote-ls flathub --updates --columns=ref,name,version,download-size,commit")
	[ -f "$FAKE_FLATPAK_DIR/nolist" ] && { echo "error: Unable to load summary from remote flathub" >&2; exit 1; }
	cat "$FAKE_FLATPAK_FIX/remote-ls-updates.txt" ;;
"--user info --show-metadata app/org.mozilla.firefox/x86_64/stable"|"--user remote-info --show-metadata flathub app/org.mozilla.firefox/x86_64/stable")
	cat "$FAKE_FLATPAK_FIX/remote-info-firefox-metadata.txt" ;;
"--user info --show-metadata app/org.gimp.GIMP/x86_64/stable")
	printf '[Context]\nshared=ipc;network;\nsockets=wayland;fallback-x11;\nfilesystems=host;\n' ;;
"--user remote-info --show-metadata flathub app/org.gimp.GIMP/x86_64/stable")
	cat "$FAKE_FLATPAK_FIX/remote-info-gimp-metadata.txt" ;;
"--user update -y "*)
	cat "$FAKE_FLATPAK_FIX/update-one.txt"
	[ -f "$FAKE_FLATPAK_DIR/updatefail" ] && { echo "error: While pulling app/org.mozilla.firefox/x86_64/stable from remote flathub: Server returned status 503" >&2; exit 1; }
	exit 0 ;;
"--user uninstall --unused --noninteractive -y") echo "Nothing unused to uninstall" ;;
*) echo "error: unexpected call: $*" >&2; exit 1 ;;
esac
`

type appsFake struct {
	dir, log string
	env      *fakeAutoEnv
}

func newAppsFake(t *testing.T) *appsFake {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "flatpak"), []byte(fakeFlatpak), 0o755); err != nil {
		t.Fatal(err)
	}
	fix, err := filepath.Abs("../../pkg/flatpak/testdata")
	if err != nil {
		t.Fatal(err)
	}
	f := &appsFake{dir: dir, log: filepath.Join(dir, "calls"), env: &fakeAutoEnv{}}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("FAKE_FLATPAK_LOG", f.log)
	t.Setenv("FAKE_FLATPAK_DIR", dir)
	t.Setenv("FAKE_FLATPAK_FIX", fix)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(dir, "state"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(dir, "data"))
	t.Setenv("XDG_RUNTIME_DIR", dir)
	oldLive, oldEnv, oldNow := appsLiveMarker, appsEnv, appsNow
	appsLiveMarker = filepath.Join(dir, "live-desktop")
	appsEnv = func() (autoEnvironment, func(), error) { return f.env, func() {}, nil }
	at := time.Date(2026, 11, 24, 9, 12, 0, 0, time.Local)
	appsNow = func() time.Time { return at }
	t.Cleanup(func() { appsLiveMarker, appsEnv, appsNow = oldLive, oldEnv, oldNow })
	return f
}

func (f *appsFake) touch(t *testing.T, name string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(f.dir, name), nil, 0o644); err != nil {
		t.Fatal(err)
	}
}

func (f *appsFake) calls() []string {
	b, _ := os.ReadFile(f.log)
	return strings.Split(strings.TrimSpace(string(b)), "\n")
}

func runApps(args ...string) (int, string, string) {
	var out, errOut bytes.Buffer
	code := run(append([]string{"apps"}, args...), strings.NewReader(""), &out, &errOut)
	return code, out.String(), errOut.String()
}

func TestAppsAutoRunHoldsWhatAsksForMore(t *testing.T) {
	f := newAppsFake(t)
	code, out, errOut := runApps("update", "--auto")
	if code != exitOK {
		t.Fatalf("exit %d: %s%s", code, out, errOut)
	}
	r, ok := flatpak.ReadLastRun()
	if !ok || r.Result != flatpak.RunUpdated || r.Time != appsNow().Unix() {
		t.Fatalf("run %+v %v", r, ok)
	}
	if strings.Join(r.Updated, ",") != "Firefox,openh264" {
		t.Errorf("updated %v", r.Updated)
	}
	if len(r.Held) != 1 || r.Held[0].Name != "GNU Image Manipulation Program" || !strings.Contains(r.Held[0].Why, "all devices") {
		t.Errorf("held %+v", r.Held)
	}
	var update string
	for _, c := range f.calls() {
		if strings.HasPrefix(c, "--user update -y ") {
			update = c
		}
	}
	if update != "--user update -y app/org.mozilla.firefox/x86_64/stable runtime/org.freedesktop.Platform.openh264/x86_64/2.2.0" {
		t.Errorf("updated with %q", update)
	}
	c := f.calls()
	if c[len(c)-1] != "--user uninstall --unused --noninteractive -y" {
		t.Errorf("unused runtimes stay: %q", c)
	}
	if !strings.Contains(out, "Held back GNU Image Manipulation Program: it asks for more:") {
		t.Errorf("out %q", out)
	}
	// The status shows the run.
	_, st, _ := runApps("status")
	if !strings.Contains(st, "Automatic app updates: on") || !strings.Contains(st, "Last run 24 November 2026, 09:12: updated Firefox, openh264.") {
		t.Errorf("status %q", st)
	}
}

func TestAppsAutoRunSkips(t *testing.T) {
	cases := []struct {
		name, why string
		setup     func(t *testing.T, f *appsFake)
	}{
		{"live", "this is the live system", func(t *testing.T, f *appsFake) { f.touch(t, "live-desktop") }},
		{"off", "automatic app updates are off", func(t *testing.T, f *appsFake) {
			if err := flatpak.SetAuto(false); err != nil {
				t.Fatal(err)
			}
		}},
		{"metered", "the connection is metered", func(t *testing.T, f *appsFake) { f.env.metered = true }},
		{"battery", "the battery is at 12% (below 30%) and not charging", func(t *testing.T, f *appsFake) {
			f.env.onBattery, f.env.percent = true, 12
		}},
		{"no flathub", "Flathub is not set up for this user", func(t *testing.T, f *appsFake) { f.touch(t, "noflathub") }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newAppsFake(t)
			c.setup(t, f)
			if code, out, errOut := runApps("update", "--auto"); code != exitOK {
				t.Fatalf("exit %d: %s%s", code, out, errOut)
			}
			r, ok := flatpak.ReadLastRun()
			if !ok || r.Result != flatpak.RunSkipped || r.Detail != c.why {
				t.Fatalf("run %+v, want skipped: %s", r, c.why)
			}
			for _, call := range f.calls() {
				if strings.HasPrefix(call, "--user update") {
					t.Errorf("updated anyway: %q", call)
				}
			}
		})
	}
	// On battery above 30% goes ahead.
	f := newAppsFake(t)
	f.env.onBattery, f.env.percent = true, 64
	if code, _, _ := runApps("update", "--auto"); code != exitOK {
		t.Fatal("exit")
	}
	if r, _ := flatpak.ReadLastRun(); r.Result != flatpak.RunUpdated {
		t.Errorf("run %+v", r)
	}
}

func TestAppsAutoRunFailures(t *testing.T) {
	f := newAppsFake(t)
	f.touch(t, "updatefail")
	if code, _, _ := runApps("update", "--auto"); code != exitError {
		t.Fatalf("exit %d", code)
	}
	r, _ := flatpak.ReadLastRun()
	if r.Result != flatpak.RunFailed || !strings.Contains(r.Detail, "Server returned status 503") {
		t.Errorf("run %+v", r)
	}
	f = newAppsFake(t)
	f.touch(t, "nolist")
	if code, _, _ := runApps("update", "--auto"); code != exitError {
		t.Fatalf("exit %d", code)
	}
	if r, _ := flatpak.ReadLastRun(); r.Result != flatpak.RunFailed || !strings.Contains(r.Detail, "Unable to load summary") {
		t.Errorf("run %+v", r)
	}
}

func TestAppsAuto(t *testing.T) {
	newAppsFake(t)
	if _, out, _ := runApps("auto"); !strings.HasPrefix(out, "Automatic app updates: on") {
		t.Errorf("default %q", out)
	}
	if code, _, _ := runApps("auto", "off"); code != exitOK || flatpak.AutoEnabled() {
		t.Fatal("not off")
	}
	b, _ := os.ReadFile(flatpak.SettingFile())
	if string(b) != "auto=off\n" {
		t.Errorf("file %q", b)
	}
	if _, out, _ := runApps("auto", "status"); out != "Automatic app updates: off\n" {
		t.Errorf("status %q", out)
	}
	if code, _, _ := runApps("auto", "on"); code != exitOK || !flatpak.AutoEnabled() {
		t.Fatal("not on")
	}
	for _, bad := range [][]string{{"auto", "maybe"}, {"auto", "on", "now"}, {"update", "--now"}, {"nonsense"}} {
		if code, _, _ := runApps(bad...); code != exitUsage {
			t.Errorf("%q: exit %d", bad, code)
		}
	}
}

func TestAppsWithoutFlatpak(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	code, _, errOut := runApps("update", "--auto")
	if code != exitError || errOut != "isotope: Flatpak is not on this system.\n" {
		t.Fatalf("exit %d %q", code, errOut)
	}
	if code, out, _ := runApps("help"); code != exitOK || !strings.Contains(out, "update --auto") {
		t.Errorf("help %d %q", code, out)
	}
}

func TestAppsWatchNotifiesEachSetOnce(t *testing.T) {
	newAppsFake(t)
	c := appsClient()
	var notes []string
	var opened []string
	w := appsWatch{
		updates: c.Updates,
		review:  c.Review,
		auto:    flatpak.AutoEnabled,
		notify: func(summary, body, label string, invoked func()) error {
			notes = append(notes, summary+" | "+body+" | "+label)
			invoked()
			return nil
		},
		open:   func(page string) error { opened = append(opened, page); return nil },
		record: filepath.Join(t.TempDir(), "notified"),
		log:    log.New(io.Discard, "", 0),
	}
	// On: only the update that asks for more.
	w.once(context.Background())
	w.once(context.Background())
	if len(notes) != 1 || notes[0] != "An app update needs your OK | GNU Image Manipulation Program asks for more permissions. Isotope System's Apps page shows what it asks for. | Open Isotope System" {
		t.Fatalf("notes %q", notes)
	}
	if len(opened) != 1 || opened[0] != "apps updates" {
		t.Errorf("opened %q", opened)
	}
	// Off: every pending update, a new set.
	if err := flatpak.SetAuto(false); err != nil {
		t.Fatal(err)
	}
	w.once(context.Background())
	w.once(context.Background())
	if len(notes) != 2 || !strings.HasPrefix(notes[1], "3 app updates | Firefox, GNU Image Manipulation Program and 1 more can be updated.") {
		t.Errorf("notes %q", notes)
	}
	// A metered connection does not look.
	w.metered = func() bool { return true }
	w.updates = func(context.Context) ([]flatpak.Update, error) { t.Fatal("looked"); return nil, nil }
	w.once(context.Background())
	// A failure is logged, not shown.
	var logged bytes.Buffer
	w.metered, w.log = nil, log.New(&logged, "", 0)
	w.updates = func(context.Context) ([]flatpak.Update, error) { return nil, errors.New("Unable to load summary") }
	w.once(context.Background())
	if len(notes) != 2 || !strings.Contains(logged.String(), "Unable to load summary") {
		t.Errorf("notes %q, log %q", notes, logged.String())
	}
}
