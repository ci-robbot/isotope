package main

import (
	"errors"
	"image"
	"image/color"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kairos-community/isotope/pkg/flatpak"
	"github.com/kairos-community/isotope/pkg/guikit"
)

// The Apps page against fakeApps, a user installation in memory with
// Flathub's catalog from pkg/flatpak's recorded fixture.

// appsFixtures is pkg/flatpak's recorded flatpak output.
const appsFixtures = "../../pkg/flatpak/testdata"

// appsCatalog is the recorded catalog subset.
func appsCatalog(t *testing.T) *flatpak.Catalog {
	t.Helper()
	c, err := flatpak.LoadCatalog(appsFixtures)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func appsMetadata(t *testing.T, name string) flatpak.Metadata {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(appsFixtures, name))
	if err != nil {
		t.Fatal(err)
	}
	return flatpak.ParseMetadata(b)
}

// fakeProc is a flatpak job: its lines, then its end when released.
type appsFakeProc struct {
	lines   chan string
	release chan error
	stopped bool
	mu      sync.Mutex
}

func (p *appsFakeProc) Lines() <-chan string { return p.lines }
func (p *appsFakeProc) Wait() error          { return <-p.release }
func (p *appsFakeProc) Stop() {
	p.mu.Lock()
	p.stopped = true
	p.mu.Unlock()
}

type fakeApps struct {
	mu                       sync.Mutex
	available, flathub       bool
	installed                []flatpak.Installed
	updates                  []flatpak.Update
	pending                  []flatpak.Pending
	cat                      *flatpak.Catalog
	catErr                   error
	age                      time.Duration
	hasAge                   bool
	refreshed                int
	picks                    []string
	info                     map[string]flatpak.RemoteInfo
	remoteMeta, installedMet map[string]flatpak.Metadata
	running                  map[string]bool
	auto, hasLast            bool
	last                     flatpak.LastRun
	dataSize                 int64
	calls                    []string
	// lines are what each job prints; hold keeps the job running until
	// release is closed.
	lines   []string
	hold    bool
	jobErr  error
	proc    *appsFakeProc
	release chan struct{}
	// icons are the IDs whose icon decodes (a generated picture).
	icons map[string]bool
}

func (f *fakeApps) rec(s string) {
	f.mu.Lock()
	f.calls = append(f.calls, s)
	f.mu.Unlock()
}

func (f *fakeApps) called() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

func (f *fakeApps) Available() bool    { return f.available }
func (f *fakeApps) FlathubReady() bool { return f.flathub }
func (f *fakeApps) Installed() ([]flatpak.Installed, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]flatpak.Installed(nil), f.installed...), nil
}
func (f *fakeApps) Updates() ([]flatpak.Update, error) {
	f.rec("Updates")
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]flatpak.Update(nil), f.updates...), nil
}
func (f *fakeApps) Review(ups []flatpak.Update) []flatpak.Pending {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.pending != nil {
		return append([]flatpak.Pending(nil), f.pending...)
	}
	out := make([]flatpak.Pending, len(ups))
	for i, u := range ups {
		out[i] = flatpak.Pending{Update: u}
	}
	return out
}
func (f *fakeApps) Catalog() (*flatpak.Catalog, error) { return f.cat, f.catErr }
func (f *fakeApps) CatalogAge() (time.Duration, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.age, f.hasAge
}
func (f *fakeApps) RefreshCatalog() error {
	f.rec("RefreshCatalog")
	f.mu.Lock()
	f.refreshed++
	f.age, f.hasAge = 0, true
	f.mu.Unlock()
	return nil
}
func (f *fakeApps) Picks() []string { return f.picks }
func (f *fakeApps) RemoteInfo(id string) (flatpak.RemoteInfo, error) {
	return f.info[id], nil
}
func (f *fakeApps) RemoteMetadata(ref string) (flatpak.Metadata, error) {
	m, ok := f.remoteMeta[ref]
	if !ok {
		return flatpak.Metadata{}, errors.New(ref + " not found")
	}
	return m, nil
}
func (f *fakeApps) InstalledMetadata(ref string) (flatpak.Metadata, error) {
	m, ok := f.installedMet[ref]
	if !ok {
		return flatpak.Metadata{}, errors.New(ref + " not installed")
	}
	return m, nil
}
func (f *fakeApps) Running() (map[string]bool, error) { return f.running, nil }

func (f *fakeApps) job(call string) (flatpak.Process, error) {
	f.rec(call)
	f.mu.Lock()
	defer f.mu.Unlock()
	p := &appsFakeProc{lines: make(chan string, len(f.lines)+1), release: make(chan error, 1)}
	for _, l := range f.lines {
		p.lines <- l
	}
	f.proc = p
	err, hold, rel := f.jobErr, f.hold, f.release
	go func() {
		if hold {
			<-rel
		}
		close(p.lines)
		p.release <- err
	}()
	return p, nil
}

func (f *fakeApps) Install(id string) (flatpak.Process, error) { return f.job("Install " + id) }
func (f *fakeApps) Update(refs ...string) (flatpak.Process, error) {
	return f.job("Update " + strings.Join(refs, " "))
}
func (f *fakeApps) Uninstall(id string, del bool) (flatpak.Process, error) {
	if del {
		return f.job("Uninstall --delete-data " + id)
	}
	return f.job("Uninstall " + id)
}
func (f *fakeApps) RemoveUnused() error { f.rec("RemoveUnused"); return nil }
func (f *fakeApps) Run(id string) error { f.rec("Run " + id); return nil }
func (f *fakeApps) DataSize(string) int64 {
	return f.dataSize
}
func (f *fakeApps) Icon(path string) image.Image {
	id := strings.TrimSuffix(filepath.Base(path), ".png")
	if !f.icons[id] {
		return nil
	}
	return appsTestIcon(id)
}
func (f *fakeApps) Auto() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.auto
}
func (f *fakeApps) SetAuto(on bool) error {
	f.rec("SetAuto " + map[bool]string{true: "on", false: "off"}[on])
	f.mu.Lock()
	f.auto = on
	f.mu.Unlock()
	return nil
}
func (f *fakeApps) LastRun() (flatpak.LastRun, bool) { return f.last, f.hasLast }

// appsTestIcon is a generated 64x64 icon: a ring in a colour from the ID,
// so the tests draw an icon without shipping an app's artwork.
func appsTestIcon(id string) image.Image {
	img := image.NewNRGBA(image.Rect(0, 0, 64, 64))
	c := tileColours[len(id)%len(tileColours)]
	for y := 0; y < 64; y++ {
		for x := 0; x < 64; x++ {
			dx, dy := x-32, y-32
			d := dx*dx + dy*dy
			switch {
			case d < 14*14:
				img.Set(x, y, guikit.ColWindow)
			case d < 30*30:
				img.Set(x, y, c)
			default:
				img.Set(x, y, color.NRGBA{})
			}
		}
	}
	return img
}

func inst(kind, id, branch, name, version, size, runtime string) flatpak.Installed {
	return flatpak.Installed{Ref: flatpak.Ref{Kind: kind, ID: id, Arch: "x86_64", Branch: branch}, Name: name, Version: version,
		Size: size, Runtime: runtime, Origin: "flathub"}
}

// newFakeApps is a user with Firefox, Loupe and Papers (on the end-of-life
// GNOME 46 runtime), three updates (GIMP's asks for more) and Flathub set
// up.
func newFakeApps(t *testing.T) *fakeApps {
	t.Helper()
	gnome46 := inst(flatpak.KindRuntime, "org.gnome.Platform", "46", "GNOME Application Platform version 46", "", "951.3 MB", "")
	gnome46.EOL = "The GNOME 46 runtime is no longer supported as of April 17, 2025."
	f := &fakeApps{
		available: true, flathub: true, cat: appsCatalog(t), hasAge: true, age: time.Hour, auto: true,
		picks: []string{"org.mozilla.firefox", "org.gnome.Loupe", "org.videolan.VLC", "org.gnome.Papers", "org.libreoffice.LibreOffice", "com.github.tchx84.Flatseal"},
		installed: []flatpak.Installed{
			inst(flatpak.KindApp, "org.mozilla.firefox", "stable", "Firefox", "146.0", "350.4 MB", "org.freedesktop.Platform/x86_64/25.08"),
			inst(flatpak.KindApp, "org.gnome.Loupe", "stable", "Image Viewer", "51.1", "12.3 MB", "org.gnome.Platform/x86_64/51"),
			inst(flatpak.KindApp, "org.gnome.Papers", "stable", "Papers", "46.2", "18.0 MB", "org.gnome.Platform/x86_64/46"),
			inst(flatpak.KindApp, "org.gimp.GIMP", "stable", "GNU Image Manipulation Program", "3.0.6", "268.6 MB", "org.gnome.Platform/x86_64/51"),
			inst(flatpak.KindRuntime, "org.freedesktop.Platform", "25.08", "Freedesktop Platform", "25.08.6", "620.1 MB", ""),
			inst(flatpak.KindRuntime, "org.gnome.Platform", "51", "GNOME Application Platform version 51", "", "1.1 GB", ""),
			gnome46,
		},
		running: map[string]bool{"org.mozilla.firefox": true},
		icons:   map[string]bool{"org.mozilla.firefox": true, "org.gnome.Loupe": true, "org.gimp.GIMP": true},
		info: map[string]flatpak.RemoteInfo{
			"org.gimp.GIMP": {Version: "3.2.8", License: "GPL-3.0+ AND LGPL-3.0+", Download: "102.5 MB", DownloadBytes: 102_500_000},
		},
		remoteMeta: map[string]flatpak.Metadata{
			"org.gimp.GIMP":       appsMetadata(t, "remote-info-gimp-metadata.txt"),
			"org.mozilla.firefox": appsMetadata(t, "remote-info-firefox-metadata.txt"),
		},
		installedMet: map[string]flatpak.Metadata{
			"app/org.mozilla.firefox/x86_64/stable": appsMetadata(t, "remote-info-firefox-metadata.txt"),
		},
		dataSize: 1_200_000_000,
		release:  make(chan struct{}),
	}
	ff := flatpak.Update{Ref: flatpak.Ref{Kind: flatpak.KindApp, ID: "org.mozilla.firefox", Arch: "x86_64", Branch: "stable"}, Name: "Firefox",
		Version: "146.0.1", Download: "92.0 MB", DownloadBytes: 92_000_000}
	rt := flatpak.Update{Ref: flatpak.Ref{Kind: flatpak.KindRuntime, ID: "org.gnome.Platform", Arch: "x86_64", Branch: "51"},
		Name: "GNOME Application Platform version 51", Download: "84.1 MB", DownloadBytes: 84_100_000}
	gimp := flatpak.Update{Ref: flatpak.Ref{Kind: flatpak.KindApp, ID: "org.gimp.GIMP", Arch: "x86_64", Branch: "stable"},
		Name: "GNU Image Manipulation Program", Version: "3.2.8", Download: "102.5 MB", DownloadBytes: 102_500_000}
	f.updates = []flatpak.Update{ff, gimp, rt}
	f.pending = []flatpak.Pending{{Update: ff}, {Update: gimp, Added: []string{"all devices: cameras, microphones, USB and more"}}, {Update: rt}}
	return f
}

// appsUI is the window on the Apps page with fake apps, after its loads.
func appsUI(t *testing.T, f *fakeApps) (*UI, *appsPage, *fakeDaemon) {
	t.Helper()
	u, d, _ := testUI(t, true)
	d.mu.Lock()
	d.ext = []Extension{{Name: "gphoto2", Enabled: true}, {Name: "modemmanager", Enabled: true}}
	d.cat = []CatalogEntry{
		{Name: "gphoto2", Title: "gphoto2", Description: "Camera tethering and photo transfer for the whole system", Latest: "2.5.32", Installed: true, Enabled: true},
		{Name: "modemmanager", Title: "ModemManager", Description: "Mobile broadband modems", Latest: "1.24.0", Installed: true, Enabled: true},
		{Name: "tailscale", Title: "Tailscale", Description: "A mesh VPN", Latest: "1.78.1"},
	}
	d.mu.Unlock()
	u.hooks.Apps = f
	u.show(PageApps)
	p := u.view(PageApps).(*appsPage)
	appsWait(t, u, p, f)
	return u, p, d
}

// appsWait waits for the page's reads.
func appsWait(t *testing.T, u *UI, p *appsPage, f *fakeApps) {
	t.Helper()
	eventually(t, "the Apps page's reads", func() bool {
		u.mu.Lock()
		defer u.mu.Unlock()
		s := p.s()
		if !s.checked {
			return false
		}
		if !s.available {
			return true
		}
		ok := s.instLoaded && len(s.exts) > 0
		if s.flathub {
			ok = ok && s.catLoaded && !s.refreshing && s.upLoaded
		}
		return ok
	})
}

func TestParseTargetOpensAnAppsTab(t *testing.T) {
	for _, tab := range appTabs {
		if p, got, ok := ParseTarget([]string{"apps", tab}); !ok || p != PageApps || got != tab {
			t.Errorf("apps %s: %s %s %v", tab, p, got, ok)
		}
	}
	if p, tab, ok := ParseTarget([]string{"apps"}); !ok || p != PageApps || tab != "" {
		t.Errorf("apps: %s %q %v", p, tab, ok)
	}
	for _, bad := range [][]string{{"apps", "nonsense"}, {"updates", "updates"}, {"apps", "updates", "x"}} {
		if _, _, ok := ParseTarget(bad); ok {
			t.Errorf("%q accepted", bad)
		}
	}
	if Pages[0] != PageApps {
		t.Errorf("the sidebar starts with %s", Pages[0])
	}
}

func TestAppsShowTabOverTheBus(t *testing.T) {
	f := newFakeApps(t)
	u, p, _ := appsUI(t, f)
	u.ShowPage(PageUpdates)
	u.Layout(gtxOf(1100, 720))
	if err := (instance{u}).ShowPage("apps updates"); err != nil {
		t.Fatal(err)
	}
	u.Layout(gtxOf(1100, 720))
	u.mu.Lock()
	tab := p.s().tab
	u.mu.Unlock()
	if u.page != PageApps || tab != tabUpdates {
		t.Fatalf("on %s, tab %s", u.page, tab)
	}
	if err := (instance{u}).ShowPage("apps nonsense"); err == nil {
		t.Error("a bad tab accepted")
	}
}

func TestAppsInstallFollowsProgressThenReloads(t *testing.T) {
	f := newFakeApps(t)
	b, err := os.ReadFile(filepath.Join(appsFixtures, "install-two.txt"))
	if err != nil {
		t.Fatal(err)
	}
	f.lines = strings.Split(string(b), "\n")
	f.hold = true
	u, p, _ := appsUI(t, f)
	p.install("org.videolan.VLC")
	eventually(t, "the progress", func() bool {
		u.mu.Lock()
		defer u.mu.Unlock()
		j := p.s().job
		return j != nil && j.Progress.Percent == 100
	})
	u.mu.Lock()
	j := p.s().job
	u.mu.Unlock()
	if j.Name != "VLC" || j.Kind != jobInstall {
		t.Errorf("job %+v", j)
	}
	// A second job waits for the first.
	p.install("org.gimp.GIMP")
	u.Layout(gtxOf(1100, 720))
	close(f.release)
	eventually(t, "the job's end", func() bool {
		u.mu.Lock()
		defer u.mu.Unlock()
		return p.s().job == nil
	})
	calls := strings.Join(f.called(), "\n")
	if !strings.Contains(calls, "Install org.videolan.VLC") || strings.Contains(calls, "Install org.gimp.GIMP") {
		t.Errorf("calls %s", calls)
	}
	if u.question() != nil {
		t.Errorf("a question after a good install: %q", u.question().title)
	}
}

func TestAppsJobFailureIsTold(t *testing.T) {
	f := newFakeApps(t)
	f.jobErr = errors.New("no remote refs found similar to 'org.example.Gone'")
	u, p, _ := appsUI(t, f)
	p.install("org.gimp.GIMP")
	q := waitQuestion(t, u, "Could not install GNU Image Manipulation Program")
	if q.body != "No remote refs found similar to 'org.example.Gone'" {
		t.Errorf("body %q", q.body)
	}
}

func TestAppsCancelStopsTheJob(t *testing.T) {
	f := newFakeApps(t)
	f.hold = true
	u, p, _ := appsUI(t, f)
	p.install("org.videolan.VLC")
	eventually(t, "the job", func() bool {
		u.mu.Lock()
		defer u.mu.Unlock()
		return p.s().job != nil && p.s().job.proc != nil
	})
	u.mu.Lock()
	p.s().job.cancelled = true
	p.s().job.proc.Stop()
	u.mu.Unlock()
	f.mu.Lock()
	f.jobErr = errors.New("killed")
	f.mu.Unlock()
	close(f.release)
	eventually(t, "the end", func() bool {
		u.mu.Lock()
		defer u.mu.Unlock()
		return p.s().job == nil
	})
	f.mu.Lock()
	stopped := f.proc.stopped
	f.mu.Unlock()
	if !stopped {
		t.Error("not stopped")
	}
	if q := u.question(); q != nil {
		t.Errorf("a cancelled job is an error: %q", q.title)
	}
}

func TestAppsRemoveKeepsTheDataUnlessAsked(t *testing.T) {
	for _, del := range []bool{false, true} {
		f := newFakeApps(t)
		u, p, _ := appsUI(t, f)
		p.askRemove("org.mozilla.firefox")
		q := waitQuestion(t, u, "Remove Firefox?")
		if q.body != "The app goes, and 350.4 MB comes back. Its settings and files stay, so installing it again picks up where you left off." {
			t.Errorf("body %q", q.body)
		}
		p.removeDataOn = del
		u.pick(q, 1)
		eventually(t, "the removal", func() bool {
			return strings.Contains(strings.Join(f.called(), "\n"), "RemoveUnused")
		})
		want := "Uninstall org.mozilla.firefox"
		if del {
			want = "Uninstall --delete-data org.mozilla.firefox"
		}
		if !strings.Contains(strings.Join(f.called(), "\n"), want) {
			t.Errorf("delete %v: calls %q", del, f.called())
		}
	}
}

func TestAppsDetailsReadWhatItCanReach(t *testing.T) {
	f := newFakeApps(t)
	f.installed = f.installed[:3] // GIMP is not installed
	u, p, _ := appsUI(t, f)
	p.openDetail("org.gimp.GIMP")
	eventually(t, "the details", func() bool {
		u.mu.Lock()
		defer u.mu.Unlock()
		return p.s().detail != nil && p.s().detail.Loaded
	})
	u.mu.Lock()
	d := *p.s().detail
	u.mu.Unlock()
	if d.FromInstalled || d.Err != "" || len(d.Summary.Items) == 0 || d.Summary.Items[0].Title != "All your files" || !d.Summary.Items[0].Warn {
		t.Fatalf("detail %+v", d)
	}
	a, _ := appsCatalog(t).Get("org.gimp.GIMP")
	facts := detailFacts(a, d.Info, nil)
	got := []string{}
	for _, f := range facts {
		got = append(got, f.Label+"="+f.Value)
	}
	if strings.Join(got, " ") != "Version=3.2.8 Download=102.5 MB Licence=GPL-3.0+ AND LGPL-3.0+ Source=Flathub" {
		t.Errorf("facts %q", got)
	}
	// An installed app reads its installed metadata.
	p.openDetail("org.mozilla.firefox")
	eventually(t, "the details", func() bool {
		u.mu.Lock()
		defer u.mu.Unlock()
		return p.s().detail.Loaded
	})
	u.mu.Lock()
	d = *p.s().detail
	u.mu.Unlock()
	if !d.FromInstalled || d.Err != "" {
		t.Errorf("firefox %+v", d)
	}
}

func TestAppsUpdatesView(t *testing.T) {
	f := newFakeApps(t)
	v := updatesViewOf(f.pending, f.installed, f.cat, f.running)
	if len(v.Ready) != 2 || len(v.Held) != 1 {
		t.Fatalf("ready %d held %d", len(v.Ready), len(v.Held))
	}
	if v.ReadyTitle != "Ready to update · 2, 176.1 MB" {
		t.Errorf("title %q", v.ReadyTitle)
	}
	if v.Ready[0].Change != "146.0 → 146.0.1 · 92.0 MB · running, restarts later" {
		t.Errorf("firefox %q", v.Ready[0].Change)
	}
	if v.Ready[1].Name != "GNOME 51 runtime" || v.Ready[1].Notes != "Used by GNU Image Manipulation Program and Image Viewer." {
		t.Errorf("runtime %q", v.Ready[1].Notes)
	}
	if h := v.Held[0]; h.Change != "3.0.6 → 3.2.8" || !strings.HasPrefix(h.Notes, "Asks for more: all devices") {
		t.Errorf("held %+v", h)
	}
	if len(v.EOL) != 1 || v.EOL[0] != "Papers uses the GNOME 46 runtime, which no longer gets security fixes. Only its developer can move it to a newer one." {
		t.Errorf("eol %q", v.EOL)
	}
	if appsUpdatesTab(0) != "Updates" || appsUpdatesTab(4) != "Updates · 4" {
		t.Error("tab labels")
	}
}

func TestAppsReviewUpdatesOnTheOK(t *testing.T) {
	f := newFakeApps(t)
	u, p, _ := appsUI(t, f)
	v := updatesViewOf(f.pending, f.installed, f.cat, f.running)
	p.askReview(v.Held[0])
	q := waitQuestion(t, u, "Update GNU Image Manipulation Program?")
	if !strings.Contains(q.body, "• all devices: cameras, microphones, USB and more") {
		t.Errorf("body %q", q.body)
	}
	u.pick(q, 1)
	eventually(t, "the update", func() bool {
		return strings.Contains(strings.Join(f.called(), "\n"), "Update app/org.gimp.GIMP/x86_64/stable")
	})
}

func TestAppsAutoToggle(t *testing.T) {
	f := newFakeApps(t)
	u, p, _ := appsUI(t, f)
	p.toggleAuto()
	eventually(t, "off", func() bool {
		u.mu.Lock()
		defer u.mu.Unlock()
		return !p.s().auto
	})
	if c := f.called(); c[len(c)-1] != "SetAuto off" {
		t.Errorf("calls %q", c)
	}
	at := time.Date(2026, 11, 24, 9, 12, 0, 0, time.Local)
	v := autoAppsViewOf(true, flatpak.LastRun{Time: at.Unix(), Result: flatpak.RunUpdated, Updated: []string{"Firefox"}}, true, at.Add(time.Hour))
	if v.Toggle != "Turn off" || !strings.HasSuffix(v.Sub, "Last run today at 09:12: updated Firefox.") {
		t.Errorf("view %+v", v)
	}
}

func TestAppsRefreshesAnOldCatalog(t *testing.T) {
	f := newFakeApps(t)
	f.age = 30 * time.Hour
	appsUI(t, f)
	if f.refreshed != 1 {
		t.Errorf("refreshed %d times", f.refreshed)
	}
	f = newFakeApps(t)
	appsUI(t, f)
	if f.refreshed != 0 {
		t.Errorf("a fresh catalog refreshed %d times", f.refreshed)
	}
}

func TestAppsWithoutFlathubOrFlatpak(t *testing.T) {
	f := newFakeApps(t)
	f.flathub = false
	u, p, _ := appsUI(t, f)
	u.Layout(gtxOf(1100, 720))
	for _, c := range f.called() {
		if c == "Updates" || c == "RefreshCatalog" {
			t.Errorf("asked Flathub: %s", c)
		}
	}
	p.install("org.gimp.GIMP")
	time.Sleep(20 * time.Millisecond)
	for _, c := range f.called() {
		if strings.HasPrefix(c, "Install") {
			t.Errorf("installed without Flathub: %s", c)
		}
	}
	f = newFakeApps(t)
	f.available = false
	u, _, _ = appsUI(t, f)
	u.Layout(gtxOf(1100, 720))
}

func TestAppsSearchFindsExtensions(t *testing.T) {
	cat := []CatalogEntry{
		{Name: "gphoto2", Title: "gphoto2", Description: "Camera tethering and photo transfer"},
		{Name: "tailscale", Title: "Tailscale", Description: "A mesh VPN"},
	}
	got := extMatches(cat, []Extension{{Name: "gphoto2"}}, "PHOTO")
	if len(got) != 1 || got[0].Name != "gphoto2" || !strings.Contains(got[0].Sub, "installed") {
		t.Errorf("%+v", got)
	}
	got = extMatches(cat, nil, "vpn")
	if len(got) != 1 || !strings.Contains(got[0].Sub, "needs an administrator") {
		t.Errorf("%+v", got)
	}
	if installedExtsLine([]Extension{{Name: "modemmanager"}, {Name: "gphoto2"}}) != "Also installed: 2 system extensions (gphoto2, modemmanager)." {
		t.Error(installedExtsLine([]Extension{{Name: "modemmanager"}, {Name: "gphoto2"}}))
	}
}

func TestAppsLiveSaysInstallFirst(t *testing.T) {
	u := newUI(testSystem(true), guikit.NewShaper(), Hooks{Live: true, Apps: newFakeApps(t), Start: func(...string) error { return nil }}, PageApps)
	if _, ok := u.view(PageApps).(*livePage); !ok {
		t.Fatalf("the Apps page on the live system is %T", u.view(PageApps))
	}
}

func TestUpdatesPageNamesTheAppUpdates(t *testing.T) {
	if appUpdatesLine(0) != "" || appUpdatesLine(1) != "1 app update" || appUpdatesLine(4) != "4 app updates" {
		t.Error("lines")
	}
	f := newFakeApps(t)
	u, _, _ := testUI(t, true)
	u.hooks.Apps = f
	u.show(PageUpdates)
	eventually(t, "the count", func() bool {
		u.mu.Lock()
		defer u.mu.Unlock()
		return u.appUpdatesKnown && u.appUpdates == 3
	})
	p := u.view(PageUpdates).(*updatesPage)
	u.Layout(gtxOf(1100, 720))
	u.showTab(PageApps, tabUpdates)
	if u.page != PageApps {
		t.Fatal("not on Apps")
	}
	_ = p
}
