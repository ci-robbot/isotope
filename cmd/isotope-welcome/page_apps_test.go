package main

import (
	"reflect"
	"testing"
)

// appsUI is the full flow of sway on the Apps page, with a fake runner and
// the real list. The page has loaded.
func appsUI(t *testing.T, flow *Flow, r *fakeRunner) (*UI, *recorder, *appsPage) {
	t.Helper()
	u, rec := newRecorded(flow, Sway)
	u.hooks.Runner = r
	u.appList = loadApps(t)
	to(u, PApps)
	p := u.page().(*appsPage)
	p.load(true)
	return u, rec, p
}

func online() *fakeRunner {
	return &fakeRunner{out: map[string]string{flathubOK: "flathub\t\n"}}
}

func TestAppsPreselection(t *testing.T) {
	_, _, p := appsUI(t, NewFlow(Sway), online())
	want := []string{"org.mozilla.firefox", "org.gnome.Loupe", "org.videolan.VLC", "org.gnome.TextEditor", "org.gnome.Papers"}
	if got := p.selected(); !reflect.DeepEqual(got, want) {
		t.Fatalf("selected %v, want %v", got, want)
	}
}

func TestAppsBrowserIsOneChoiceOrNone(t *testing.T) {
	_, _, p := appsUI(t, NewFlow(Sway), online())
	p.toggle("org.chromium.Chromium")
	if !p.sel["org.chromium.Chromium"] || p.sel["org.mozilla.firefox"] {
		t.Fatalf("picking Chromium must replace Firefox: %v", p.selected())
	}
	p.clearBrowsers()
	if p.sel["org.chromium.Chromium"] {
		t.Fatal("No browser clears the choice")
	}
	p.toggle("org.gnome.Loupe")
	if p.sel["org.gnome.Loupe"] {
		t.Fatal("an essential toggles off")
	}
}

func TestAppsInstalledCannotBeUnticked(t *testing.T) {
	r := online()
	r.out[installedList] = "org.mozilla.firefox\norg.gnome.Loupe\n"
	_, _, p := appsUI(t, NewFlow(Sway), r)
	if p.sel["org.mozilla.firefox"] || p.sel["org.gnome.Loupe"] {
		t.Errorf("installed apps are not queued again: %v", p.selected())
	}
	p.toggle("org.gnome.Loupe")
	if p.sel["org.gnome.Loupe"] {
		t.Error("an installed app cannot be toggled")
	}
	if !p.installed["org.gnome.Loupe"] {
		t.Error("installed state lost")
	}
}

func TestAppsContinueInstallsInTheBackground(t *testing.T) {
	r := online()
	u, rec, p := appsUI(t, NewFlow(Sway), r)
	u.next()
	if u.flow.Current() != PShortcuts {
		t.Fatalf("Continue goes on, now on %s", u.flow.Current())
	}
	u.installer.Wait()
	calls := r.called()
	if len(calls) < 5 || calls[len(calls)-1] != "flatpak --user install --noninteractive flathub org.gnome.Papers" {
		t.Fatalf("calls %v", calls)
	}
	u.syncApps()
	o := u.flow.Outcome
	if !reflect.DeepEqual(o.AppsInstalled, []string{"Firefox", "Loupe", "VLC", "GNOME Text Editor", "Papers"}) || len(o.AppsFailed) != 0 {
		t.Errorf("outcome %+v", o)
	}
	if len(rec.stamps) != 0 {
		t.Errorf("stamps %v", rec.stamps)
	}
	_ = p
}

func TestAppsFailureReachesTheOutcomeAndRetry(t *testing.T) {
	r := online()
	r.fail = []string{vlc}
	u, _, _ := appsUI(t, NewFlow(Sway), r)
	u.next()
	u.installer.Wait()
	u.syncApps()
	if !reflect.DeepEqual(u.flow.Outcome.AppsFailed, []string{"VLC"}) {
		t.Fatalf("failed %v", u.flow.Outcome.AppsFailed)
	}
	// Ready's Try again goes back to Apps; the page lists the failed app, ticked.
	r.fail = nil
	r.out[installedList] = "org.mozilla.firefox\norg.gnome.Loupe\norg.gnome.TextEditor\norg.gnome.Papers\n"
	u.goBackTo(PApps)
	p := u.page().(*appsPage)
	p.load(false)
	if got := p.selected(); !reflect.DeepEqual(got, []string{vlc}) {
		t.Fatalf("retry selects %v", got)
	}
	u.next()
	u.installer.Wait()
	u.syncApps()
	o := u.flow.Outcome
	if len(o.AppsFailed) != 0 || o.AppsInstalled[len(o.AppsInstalled)-1] != "VLC" {
		t.Errorf("after the retry: %+v", o)
	}
}

func TestAppsSidebarShowsProgress(t *testing.T) {
	u, _, _ := appsUI(t, NewFlow(Sway), online())
	if got := u.steps().Names[indexOf(u.flow.Pages(), PApps)]; got != "Apps" {
		t.Fatalf("idle sidebar says %q", got)
	}
	u.installer = NewInstaller(online(), u.appList)
	u.installer.setProgress(1, 5, "org.videolan.VLC")
	if got := u.steps().Names[indexOf(u.flow.Pages(), PApps)]; got != "Apps 2/5" {
		t.Fatalf("installing sidebar says %q", got)
	}
}

func indexOf(l []PageID, p PageID) int {
	for i, x := range l {
		if x == p {
			return i
		}
	}
	return -1
}

func offline() *fakeRunner {
	return &fakeRunner{fail: []string{"nm-online"}}
}

func TestAppsOfflineState(t *testing.T) {
	_, _, p := appsUI(t, NewFlow(Sway), offline())
	if p.status != appsOffline {
		t.Fatalf("status %v", p.status)
	}
	r := &fakeRunner{out: map[string]string{flathubOK: "flathub\tdisabled\n"}}
	_, _, p = appsUI(t, NewFlow(Sway), r)
	if p.status != appsNoFlathub {
		t.Fatalf("status %v", p.status)
	}
}

// TestOfflineSkipSetsAppsPending: Review Focus 2. Offline, Skip leaves the
// reminder for the next login, and Finish writes it.
func TestOfflineSkipSetsAppsPending(t *testing.T) {
	u, rec, _ := appsUI(t, NewFlow(Sway), offline())
	u.skipPage()
	if !u.appsPending {
		t.Fatal("Skip while offline must mark apps pending")
	}
	if !contains(u.flow.Outcome.Skipped, PApps) {
		t.Error("Apps must be skipped")
	}
	to(u, PReady)
	u.next()
	wantStamps(t, rec, StampAppsPending)
	dir := t.TempDir()
	if err := WriteStamp(dir, StampAppsPending); err != nil {
		t.Fatal(err)
	}
	if ShouldOpenAtLogin(dir) {
		t.Error("apps-pending must not open the flow at login")
	}
	if !ShouldRemindApps(dir) {
		t.Error("apps-pending must send the reminder")
	}
}

func TestOnlineSkipIsAPlainSkip(t *testing.T) {
	u, rec, _ := appsUI(t, NewFlow(Sway), online())
	u.skipPage()
	if u.appsPending {
		t.Fatal("a skip with Flathub reachable is not a reminder")
	}
	to(u, PReady)
	u.next()
	wantStamps(t, rec, StampDone)
}

// A page opened alone (`isotope welcome apps`) closes on Skip and leaves
// no reminder: main writes no stamp for it.
func TestAppsOpenedAloneSkipCloses(t *testing.T) {
	u, rec, _ := appsUI(t, Single(PApps), offline())
	u.skipPage()
	if u.appsPending {
		t.Error("a single page must not ask for a reminder")
	}
	if !rec.closed {
		t.Error("Skip must close the window")
	}
}

func TestAppsOnReadyBarWhileInstalling(t *testing.T) {
	u, _, _ := appsUI(t, NewFlow(Sway), online())
	u.installer = NewInstaller(online(), u.appList)
	u.installer.setProgress(2, 5, "org.videolan.VLC")
	if !u.installing() {
		t.Error("installing() must be true while a batch runs")
	}
}

func contains(l []PageID, p PageID) bool { return indexOf(l, p) >= 0 }

// checking is the Apps page still asking nm-online (up to 5 s offline).
func checking(t *testing.T) (*UI, *recorder, *appsPage) {
	t.Helper()
	u, rec := newRecorded(NewFlow(Sway), Sway)
	u.hooks.Runner = offline()
	u.appList = loadApps(t)
	to(u, PApps)
	p := u.page().(*appsPage)
	p.started = true
	p.status = appsChecking
	return u, rec, p
}

// I2: Skip while the page still says "Checking…" counts as offline.
func TestSkipWhileCheckingSetsAppsPending(t *testing.T) {
	u, rec, _ := checking(t)
	u.skipPage()
	if !u.appsPending {
		t.Fatal("Skip during the network check must mark apps pending")
	}
	to(u, PReady)
	u.next()
	wantStamps(t, rec, StampAppsPending)
}

// I2: so does a close ("Skip setup?" → Skip setup) during the check.
func TestCloseWhileCheckingLeavesAppsPending(t *testing.T) {
	u, rec, _ := checking(t)
	rec.requestClose()
	u.answerSkip(true)
	wantStamps(t, rec, StampAppsPending)
	u, rec, _ = checking(t)
	u.Closed()
	wantStamps(t, rec, StampAppsPending)
}
