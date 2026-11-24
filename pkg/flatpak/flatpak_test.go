package flatpak

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

// The fixtures in testdata/ are flatpak 1.16's own output, recorded with
// FLATPAK_FANCY_OUTPUT unset and stdout not a terminal (so plain): an
// Alpine container with flatpak 1.16.1 and a user flathub remote, on
// 2026-11-24. appstream.xml.gz is a subset of that user's Flathub catalog
// (a few apps, runtimes and add-ons; translations other than German and
// releases after the third left out). list-app.txt, list-runtime-eol.txt
// and remote-ls-updates.txt are written in the same format by hand, for
// apps the container did not install (runtimes of several hundred MB):
// the columns, the tabs, the no-break space before a size's unit and the
// Options column's "eol=" are flatpak's (app/flatpak-builtins-list.c,
// flatpak-builtins-remote-ls.c, flatpak-table-printer.c).

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// fakeRunner answers each command line with an output; the key is the
// arguments joined by spaces.
type fakeRunner struct {
	mu      sync.Mutex
	outs    map[string][]byte
	errs    map[string]error
	calls   []string
	started []string
	lines   map[string][]string
	exit    map[string]error
}

func (f *fakeRunner) Output(_ context.Context, name string, args ...string) ([]byte, error) {
	k := strings.Join(args, " ")
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, k)
	if err := f.errs[k]; err != nil {
		return nil, err
	}
	out, ok := f.outs[k]
	if !ok {
		return nil, errors.New("unexpected: " + k)
	}
	return out, nil
}

func (f *fakeRunner) Start(name string, args ...string) (Process, error) {
	k := strings.Join(args, " ")
	f.mu.Lock()
	defer f.mu.Unlock()
	f.started = append(f.started, k)
	p := &fakeProc{lines: make(chan string, len(f.lines[k])+1), err: f.exit[k]}
	for _, l := range f.lines[k] {
		p.lines <- l
	}
	close(p.lines)
	return p, nil
}

func (f *fakeRunner) Launch(name string, args ...string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.started = append(f.started, "launch "+strings.Join(args, " "))
	return nil
}

type fakeProc struct {
	lines chan string
	err   error
}

func (p *fakeProc) Lines() <-chan string { return p.lines }
func (p *fakeProc) Wait() error          { return p.err }
func (p *fakeProc) Stop()                {}

func testClient(f *fakeRunner) *Client {
	return &Client{R: f, Bin: "flatpak", DataDir: "/home/anna/.local/share/flatpak", Arch: "x86_64"}
}

func TestFlathubReady(t *testing.T) {
	for _, c := range []struct {
		file string
		want bool
	}{{"remotes.txt", true}, {"remotes-disabled.txt", false}} {
		f := &fakeRunner{outs: map[string][]byte{"--user remotes --columns=name,options": fixture(t, c.file)}}
		if got := testClient(f).FlathubReady(context.Background()); got != c.want {
			t.Errorf("%s: %v, want %v", c.file, got, c.want)
		}
	}
	// A remote flatpak lists but marks disabled (an older flatpak, or
	// --show-disabled).
	f := &fakeRunner{outs: map[string][]byte{"--user remotes --columns=name,options": []byte("flathub\tdisabled\n")}}
	if testClient(f).FlathubReady(context.Background()) {
		t.Error("a disabled flathub is ready")
	}
	f = &fakeRunner{errs: map[string]error{"--user remotes --columns=name,options": ErrNoFlatpak}}
	if testClient(f).FlathubReady(context.Background()) {
		t.Error("no flatpak is ready")
	}
}

func TestInstalled(t *testing.T) {
	f := &fakeRunner{outs: map[string][]byte{
		"--user list --app --columns=" + listColumns:     fixture(t, "list-app.txt"),
		"--user list --runtime --columns=" + listColumns: fixture(t, "list-runtime-eol.txt"),
	}}
	got, err := testClient(f).Installed(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 6 {
		t.Fatalf("%d refs: %+v", len(got), got)
	}
	ff := got[0]
	if ff.Ref.String() != "app/org.mozilla.firefox/x86_64/stable" || ff.Name != "Firefox" || ff.Version != "146.0" ||
		ff.SizeBytes != 350_400_000 || ff.Runtime != "org.freedesktop.Platform/x86_64/25.08" || ff.Origin != "flathub" || ff.EOL != "" {
		t.Errorf("firefox: %+v", ff)
	}
	if got[1].Name != "Image Viewer" || got[2].Name != "Papers" {
		t.Errorf("apps by name: %q %q", got[1].Name, got[2].Name)
	}
	var gnome46 Installed
	for _, in := range got {
		if in.Ref.Partial() == "org.gnome.Platform/x86_64/46" {
			gnome46 = in
		}
	}
	if gnome46.Ref.Kind != KindRuntime || !strings.HasPrefix(gnome46.EOL, "The GNOME 46 runtime is no longer supported as of April 17, 2025.") {
		t.Errorf("GNOME 46: %+v", gnome46)
	}
}

func TestInstalledRecordedRuntimeRow(t *testing.T) {
	// The recorded row: no runtime and no options, so flatpak leaves out
	// the trailing empty cell.
	got := parseList(fixture(t, "list-runtime.txt"), KindRuntime)
	if len(got) != 1 || got[0].Ref.String() != "runtime/org.freedesktop.Platform.openh264/x86_64/2.2.0" ||
		got[0].Version != "2.1.0" || got[0].SizeBytes != 790_000 || got[0].Origin != "flathub" {
		t.Fatalf("%+v", got)
	}
}

func TestUpdates(t *testing.T) {
	f := &fakeRunner{outs: map[string][]byte{
		"--user remote-ls flathub --updates --columns=ref,name,version,download-size,commit": fixture(t, "remote-ls-updates.txt"),
	}}
	got, err := testClient(f).Updates(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	ids := []string{}
	for _, u := range got {
		ids = append(ids, u.Ref.ID)
	}
	if !reflect.DeepEqual(ids, []string{"org.mozilla.firefox", "org.gimp.GIMP", "org.freedesktop.Platform.openh264"}) {
		t.Fatalf("apps first: %v", ids)
	}
	if got[0].Version != "160.0.1" || got[0].DownloadBytes != 130_800_000 || got[0].Commit != "3ab15a895b6a" {
		t.Errorf("firefox: %+v", got[0])
	}
	// Recorded without a catalog: no version.
	rec := parseUpdates(fixture(t, "remote-ls-updates-no-appstream.txt"))
	if len(rec) != 1 || rec[0].Version != "" || rec[0].DownloadBytes != 944_300 || rec[0].Commit != "bf24f23f3ba3" {
		t.Errorf("recorded: %+v", rec)
	}
}

func TestRemoteInfo(t *testing.T) {
	ri := parseRemoteInfo(fixture(t, "remote-info-gimp.txt"))
	want := RemoteInfo{Ref: Ref{KindApp, "org.gimp.GIMP", "x86_64", "stable"}, Version: "3.2.8", License: "GPL-3.0+ AND LGPL-3.0+",
		Download: "102.5 MB", Installed: "268.6 MB", DownloadBytes: 102_500_000, Runtime: "org.gnome.Platform/x86_64/51"}
	if !reflect.DeepEqual(ri, want) {
		t.Fatalf("%+v\nwant %+v", ri, want)
	}
	eol := parseRemoteInfo([]byte("        ID: org.gnome.Platform\n End-of-life: The GNOME 46 runtime is no longer supported.\n"))
	if eol.EOL != "The GNOME 46 runtime is no longer supported." {
		t.Errorf("eol: %+v", eol)
	}
}

func TestSizes(t *testing.T) {
	for in, want := range map[string]int64{"944.3 kB": 944_300, "1.5 MB": 1_500_000, "117 bytes": 117, "1 byte": 1, "1.1 GB": 1_100_000_000} {
		if got, ok := ParseSize(in); !ok || got != want {
			t.Errorf("%q: %d %v, want %d", in, got, ok, want)
		}
	}
	for _, bad := range []string{"", "-", "12", "1.5 parsecs"} {
		if _, ok := ParseSize(bad); ok {
			t.Errorf("%q parsed", bad)
		}
	}
	for n, want := range map[int64]string{1: "1 byte", 999: "999 bytes", 1000: "1 kB", 210_000_000: "210 MB", 1_234_000_000: "1.2 GB"} {
		if got := FormatSize(n); got != want {
			t.Errorf("%d: %q, want %q", n, got, want)
		}
	}
}

func TestOptions(t *testing.T) {
	eol, rebase := options("current,eol=Renamed, sorry,eol-rebase=app/org.new.App/x86_64/stable")
	if eol != "Renamed, sorry" || rebase != "app/org.new.App/x86_64/stable" {
		t.Errorf("%q %q", eol, rebase)
	}
	if eol, rebase := options("current"); eol != "" || rebase != "" {
		t.Errorf("%q %q", eol, rebase)
	}
}

func TestProgressFromRecordedOutput(t *testing.T) {
	read := func(file string) []Progress {
		r := NewProgressReader()
		var ps []Progress
		for _, l := range strings.Split(string(fixture(t, file)), "\n") {
			ps = append(ps, r.Line(l))
		}
		return ps
	}
	one := read("install-one.txt")
	var pcts []int
	for _, p := range one {
		if len(pcts) == 0 || pcts[len(pcts)-1] != p.Percent {
			pcts = append(pcts, p.Percent)
		}
	}
	if !reflect.DeepEqual(pcts, []int{-1, 0, 26, 36, 100}) {
		t.Errorf("one install: %v", pcts)
	}
	if last := one[len(one)-1]; last.Line != "Installation complete." {
		t.Errorf("last line %q", last.Line)
	}
	two := read("install-two.txt")
	pcts = nil
	for _, p := range two {
		if len(pcts) == 0 || pcts[len(pcts)-1] != p.Percent {
			pcts = append(pcts, p.Percent)
		}
	}
	if !reflect.DeepEqual(pcts, []int{-1, 0, 6, 18, 50, 57, 68, 100}) {
		t.Errorf("two installs: %v", pcts)
	}
	if p := two[len(two)-3]; p.Step != 2 || p.Steps != 2 || p.Line != "Installing 2/2…" {
		t.Errorf("step: %+v", p)
	}
	up := read("update-one.txt")
	if up[len(up)-3].Percent != 100 || up[len(up)-3].Line != "Updating…" {
		t.Errorf("update: %+v", up[len(up)-3])
	}
	if up[len(up)-2].Line != "Updates complete." {
		t.Errorf("update's end: %+v", up[len(up)-2])
	}
}

func TestMetadataPermissions(t *testing.T) {
	gimp := ParseMetadata(fixture(t, "remote-info-gimp-metadata.txt"))
	if gimp.Runtime() != "org.gnome.Platform/x86_64/51" {
		t.Errorf("runtime %q", gimp.Runtime())
	}
	p := gimp.Permissions()
	if !reflect.DeepEqual(p.Shared, []string{"ipc", "network"}) || !reflect.DeepEqual(p.Sockets, []string{"fallback-x11", "wayland"}) ||
		!reflect.DeepEqual(p.Devices, []string{"all"}) {
		t.Errorf("context: %+v", p)
	}
	if !reflect.DeepEqual(p.Filesystems, []string{"xdg-config/GIMP", "xdg-config/gtk-3.0:ro", "/tmp", "xdg-run/gvfsd", "host", "xdg-run/gvfs"}) {
		t.Errorf("filesystems: %q", p.Filesystems)
	}
	if len(p.SessionBus) != 5 || p.SessionBus["org.gtk.vfs.*"] != "talk" {
		t.Errorf("session bus: %v", p.SessionBus)
	}
	s := Summarize(p)
	titles := []string{}
	for _, r := range s.Items {
		titles = append(titles, r.Title)
	}
	if !reflect.DeepEqual(titles, []string{"All your files", "Network", "Display", "All devices"}) || !s.Items[0].Warn {
		t.Errorf("summary: %+v", s)
	}
	// Five other filesystems and five D-Bus names.
	if s.Others != 10 {
		t.Errorf("others %d, want 10", s.Others)
	}
	// The installed file's "key = value" spelling.
	h := ParseMetadata(fixture(t, "info-openh264-metadata.txt"))
	if h.Groups["Runtime"]["runtime"] != "org.freedesktop.Platform/x86_64/20.08" || h.Groups["Extra Data"]["NoRuntime"] != "true" {
		t.Errorf("installed metadata: %v", h.Groups)
	}
	if s := Summarize(h.Permissions()); len(s.Items) != 0 || s.Others != 0 {
		t.Errorf("a runtime reaches %+v", s)
	}
}

func TestFirefoxSummary(t *testing.T) {
	s := Summarize(ParseMetadata(fixture(t, "remote-info-firefox-metadata.txt")).Permissions())
	titles := []string{}
	for _, r := range s.Items {
		titles = append(titles, r.Title)
	}
	if !reflect.DeepEqual(titles, []string{"Network", "Display", "Sound", "All devices"}) {
		t.Errorf("summary: %v", titles)
	}
	// 5 filesystems, pcsc and cups, devel, 5 session and 1 system names.
	if s.Others != 14 {
		t.Errorf("others %d, want 14", s.Others)
	}
}

func TestAddedPermissions(t *testing.T) {
	gimp := ParseMetadata(fixture(t, "remote-info-gimp-metadata.txt")).Permissions()
	if a := Added(gimp, gimp); len(a) != 0 {
		t.Fatalf("the same metadata adds %v", a)
	}
	old := ParseMetadata([]byte(`[Context]
shared=ipc;
sockets=wayland;x11;
devices=dri;
filesystems=xdg-download;home:ro;!host;

[Session Bus Policy]
org.gtk.vfs.*=talk
org.freedesktop.FileManager1=see
`)).Permissions()
	got := Added(old, gimp)
	want := []string{"the network", "all devices: cameras, microphones, USB and more", "all your files",
		"the service com.canonical.AppMenu.Registrar", "the service org.freedesktop.FileManager1",
		"the service org.gnome.Shell.Screenshot", "the service org.kde.kwin.Screenshot"}
	// xdg-config/GIMP and gtk-3.0 are in the home folder, which old reads
	// (gtk-3.0 read-only is covered, GIMP read-write is not); /tmp and the
	// gvfs sockets are new folders.
	for _, w := range []string{"the folder xdg-config/GIMP", "the folder /tmp", "the folder xdg-run/gvfsd", "the folder xdg-run/gvfs"} {
		want = append(want, w)
	}
	if !sameSet(got, want) {
		t.Errorf("added:\n%q\nwant\n%q", got, want)
	}
	// The other way: host covers Downloads and the home folder, and GIMP
	// had the D-Bus names; only X11 always (not just as a fallback) is new.
	if a := Added(gimp, old); !sameSet(a, []string{"the display"}) {
		t.Errorf("added %q", a)
	}
}

func sameSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	m := map[string]int{}
	for _, x := range a {
		m[x]++
	}
	for _, x := range b {
		m[x]--
	}
	for _, v := range m {
		if v != 0 {
			return false
		}
	}
	return true
}

func TestCatalog(t *testing.T) {
	c, err := LoadCatalog("testdata")
	if err != nil {
		t.Fatal(err)
	}
	// Desktop and console apps only: no runtime or add-on.
	if len(c.Apps) != 9 {
		names := []string{}
		for _, a := range c.Apps {
			names = append(names, a.ID)
		}
		t.Fatalf("%d apps: %v", len(c.Apps), names)
	}
	g, ok := c.Get("org.gimp.GIMP")
	if !ok {
		t.Fatal("no GIMP")
	}
	if g.Name != "GNU Image Manipulation Program" || g.Summary != "High-end image creation and manipulation" ||
		g.Developer != "The GIMP team" || g.License != "GPL-3.0+ AND LGPL-3.0+" || !g.Verified ||
		g.Release.Version != "3.2.8" || g.Ref.String() != "app/org.gimp.GIMP/x86_64/stable" ||
		g.Runtime != "org.gnome.Platform/x86_64/51" || g.Icon != filepath.Join("testdata", "icons", "64x64", "org.gimp.GIMP.png") {
		t.Errorf("GIMP: %+v", g)
	}
	if !strings.HasPrefix(g.Description, "GIMP is an acronym for GNU Image Manipulation Program. It is Community-driven Free Software for high-end") ||
		!strings.Contains(g.Description, "\n\nIt has many capabilities.") {
		t.Errorf("description: %q", g.Description)
	}
	if !reflect.DeepEqual(g.Categories, []string{"Graphics", "2DGraphics", "RasterGraphics"}) {
		t.Errorf("categories %v", g.Categories)
	}
	// Firefox is an old-style "desktop" component; Flatseal names its
	// developer in both spellings.
	ff, _ := c.Get("org.mozilla.firefox")
	if ff.Name != "Firefox" || ff.Ref.ID != "org.mozilla.firefox" {
		t.Errorf("firefox: %+v", ff)
	}
	fs, _ := c.Get("com.github.tchx84.Flatseal")
	if fs.Developer == "" || !fs.Verified || fs.Release.Version != "2.4.1" {
		t.Errorf("flatseal: %+v", fs)
	}
	// Papers' newest stable release, not its beta.
	pp, _ := c.Get("org.gnome.Papers")
	if pp.Release.Version != "51.1" || pp.Release.Description == "" {
		t.Errorf("papers release: %+v", pp.Release)
	}
}

func TestSearchAndCategories(t *testing.T) {
	c, err := LoadCatalog("testdata")
	if err != nil {
		t.Fatal(err)
	}
	ids := func(as []App) []string {
		out := []string{}
		for _, a := range as {
			out = append(out, a.ID)
		}
		return out
	}
	// A name match comes before a summary or keyword match.
	got := ids(c.Search("IMAGE"))
	if len(got) < 2 || got[0] != "org.gnome.Loupe" && got[0] != "org.gimp.GIMP" {
		t.Errorf("image: %v", got)
	}
	if g := ids(c.Search("firefox")); len(g) == 0 || g[0] != "org.mozilla.firefox" {
		t.Errorf("firefox: %v", g)
	}
	if g := ids(c.Search("  ")); len(g) != 0 {
		t.Errorf("blank: %v", g)
	}
	if g := ids(c.Search("nothing-like-this")); len(g) != 0 {
		t.Errorf("nothing: %v", g)
	}
	// Every word must match.
	if g := ids(c.Search("gimp image")); !reflect.DeepEqual(g, []string{"org.gimp.GIMP"}) {
		t.Errorf("gimp image: %v", g)
	}
	office := ids(c.InCategory("Office"))
	if !contains(office, "org.libreoffice.LibreOffice") || !contains(office, "org.gnome.Papers") || contains(office, "org.gimp.GIMP") {
		t.Errorf("office: %v", office)
	}
	if g := ids(c.InCategory("Audio & video")); !contains(g, "org.videolan.VLC") {
		t.Errorf("audio & video: %v", g)
	}
	var nilCat *Catalog
	if nilCat.Search("x") != nil || nilCat.InCategory("Office") != nil {
		t.Error("a nil catalog finds apps")
	}
}

func contains(xs []string, x string) bool {
	for _, y := range xs {
		if y == x {
			return true
		}
	}
	return false
}

func TestReadPicks(t *testing.T) {
	ids, err := ReadPicks("../../rootfs/common/usr/share/isotope/first-run-apps.list")
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) < 4 || ids[0] != "org.mozilla.firefox" {
		t.Errorf("picks: %v", ids)
	}
}

func TestAutoSetting(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if !AutoEnabled() {
		t.Fatal("no file is not on")
	}
	if err := os.MkdirAll(filepath.Dir(SettingFile()), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(SettingFile(), []byte("# mine\nother=1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !AutoEnabled() {
		t.Fatal("a file without the key is not on")
	}
	if err := SetAuto(false); err != nil {
		t.Fatal(err)
	}
	if AutoEnabled() {
		t.Fatal("still on")
	}
	b, _ := os.ReadFile(SettingFile())
	if string(b) != "# mine\nother=1\nauto=off\n" {
		t.Errorf("file %q", b)
	}
	if err := SetAuto(true); err != nil || !AutoEnabled() {
		t.Fatal("not on again", err)
	}
}

func TestLastRun(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	if _, ok := ReadLastRun(); ok {
		t.Fatal("a run before any")
	}
	r := LastRun{Time: time.Date(2026, 11, 24, 9, 12, 0, 0, time.UTC).Unix(), Result: RunUpdated, Updated: []string{"Firefox"},
		Held: []Held{{Ref: "app/org.gimp.GIMP/x86_64/stable", Name: "GIMP", Why: "it asks for more: the network"}}}
	if err := WriteLastRun(r); err != nil {
		t.Fatal(err)
	}
	got, ok := ReadLastRun()
	if !ok || !reflect.DeepEqual(got, r) {
		t.Fatalf("%+v %v", got, ok)
	}
}

func TestReviewHoldsWhatAsksForMore(t *testing.T) {
	gimp := fixture(t, "remote-info-gimp-metadata.txt")
	ff := fixture(t, "remote-info-firefox-metadata.txt")
	f := &fakeRunner{outs: map[string][]byte{
		"--user info --show-metadata app/org.mozilla.firefox/x86_64/stable":                ff,
		"--user remote-info --show-metadata flathub app/org.mozilla.firefox/x86_64/stable": ff,
		"--user info --show-metadata app/org.gimp.GIMP/x86_64/stable":                      []byte("[Context]\nshared=ipc;network;\nsockets=wayland;fallback-x11;\nfilesystems=host;\n"),
		"--user remote-info --show-metadata flathub app/org.gimp.GIMP/x86_64/stable":       gimp,
		"--user remote-info --show-metadata flathub app/org.gnome.Loupe/x86_64/stable":     []byte("[Context]\n"),
	}, errs: map[string]error{"--user info --show-metadata app/org.gnome.Loupe/x86_64/stable": errors.New("org.gnome.Loupe not installed")}}
	ups := parseUpdates(fixture(t, "remote-ls-updates.txt"))
	ups = append(ups, Update{Ref: Ref{KindApp, "org.gnome.Loupe", "x86_64", "stable"}, Name: "Loupe"})
	ps := testClient(f).Review(context.Background(), ups)
	ready, held := Split(ps)
	var rd, hd []string
	for _, p := range ready {
		rd = append(rd, p.Ref.ID)
	}
	for _, p := range held {
		hd = append(hd, p.Ref.ID)
	}
	if !reflect.DeepEqual(rd, []string{"org.mozilla.firefox", "org.freedesktop.Platform.openh264"}) ||
		!reflect.DeepEqual(hd, []string{"org.gimp.GIMP", "org.gnome.Loupe"}) {
		t.Fatalf("ready %v held %v", rd, hd)
	}
	if !contains(held[0].Added, "all devices: cameras, microphones, USB and more") {
		t.Errorf("GIMP adds %q", held[0].Added)
	}
	if !strings.HasPrefix(HeldWhy(held[0]), "it asks for more: ") || !strings.Contains(HeldWhy(held[1]), "could not be compared") {
		t.Errorf("why: %q / %q", HeldWhy(held[0]), HeldWhy(held[1]))
	}
}

func TestSetKey(t *testing.T) {
	ups := parseUpdates(fixture(t, "remote-ls-updates.txt"))
	a := SetKey(ups)
	b := SetKey([]Update{ups[2], ups[0], ups[1]})
	if a != b || !strings.Contains(a, "app/org.mozilla.firefox/x86_64/stable@3ab15a895b6a") {
		t.Errorf("%q vs %q", a, b)
	}
	ups[0].Commit = "000000000000"
	if SetKey(ups) == a {
		t.Error("a new commit is the same set")
	}
}

func TestJobsRunFlatpak(t *testing.T) {
	f := &fakeRunner{lines: map[string][]string{}}
	c := testClient(f)
	_, _ = c.Install("org.gimp.GIMP")
	_, _ = c.Update("app/org.gimp.GIMP/x86_64/stable", "runtime/org.gnome.Platform/x86_64/51")
	_, _ = c.Uninstall("org.gimp.GIMP", true)
	_, _ = c.Uninstall("org.gimp.GIMP", false)
	_ = c.Run("org.gimp.GIMP")
	want := []string{
		"--user install -y flathub org.gimp.GIMP",
		"--user update -y app/org.gimp.GIMP/x86_64/stable runtime/org.gnome.Platform/x86_64/51",
		"--user uninstall -y --delete-data org.gimp.GIMP",
		"--user uninstall -y org.gimp.GIMP",
		"launch run org.gimp.GIMP",
	}
	if !reflect.DeepEqual(f.started, want) {
		t.Errorf("%q", f.started)
	}
}

func TestAppstreamPaths(t *testing.T) {
	dir := t.TempDir()
	c := &Client{DataDir: dir, Arch: "x86_64"}
	if c.AppstreamDir() != filepath.Join(dir, "appstream", "flathub", "x86_64", "active") {
		t.Errorf("dir %s", c.AppstreamDir())
	}
	if _, ok := c.AppstreamAge(time.Now()); ok {
		t.Error("an age with no catalog")
	}
	ts := filepath.Join(dir, "appstream", "flathub", "x86_64", ".timestamp")
	if err := os.MkdirAll(filepath.Dir(ts), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ts, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-30 * time.Hour)
	if err := os.Chtimes(ts, old, old); err != nil {
		t.Fatal(err)
	}
	if age, ok := c.AppstreamAge(time.Now()); !ok || age < 29*time.Hour {
		t.Errorf("age %v %v", age, ok)
	}
}

func TestExecRunnerStartFollowsAFile(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	p, err := ExecRunner{}.Start("sh", "-c", `printf 'Installing…\n'; printf 'Installing… ██ 50%%\r'; echo 'error: No remote refs found for x' >&2; exit 1`)
	if err != nil {
		t.Fatal(err)
	}
	var lines []string
	for l := range p.Lines() {
		lines = append(lines, l)
	}
	err = p.Wait()
	if err == nil || err.Error() != "No remote refs found for x" {
		t.Errorf("err %v", err)
	}
	if len(lines) != 3 || lines[1] != "Installing… ██ 50%" {
		t.Errorf("lines %q", lines)
	}
	if _, err := (ExecRunner{}).Start("isotope-no-such-flatpak"); !errors.Is(err, ErrNoFlatpak) {
		t.Errorf("missing command: %v", err)
	}
	if _, err := (ExecRunner{}).Output(context.Background(), "isotope-no-such-flatpak"); !errors.Is(err, ErrNoFlatpak) {
		t.Errorf("missing command: %v", err)
	}
}
