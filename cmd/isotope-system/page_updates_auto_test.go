package main

import (
	"image"
	"strings"
	"testing"
	"time"
)

var autoNow = time.Date(2026, 11, 24, 10, 0, 0, 0, time.Local)

func TestAutoViewStates(t *testing.T) {
	ago := autoNow.Add(-2 * time.Hour).Unix()
	for _, c := range []struct {
		name    string
		in      autoInput
		want    []string
		toggle  string
		enabled bool
	}{
		{"loading", autoInput{Admin: true}, []string{"Loading"}, "", false},
		{"no daemon", autoInput{Admin: true, Loaded: true, Err: "Could not read the automatic update setting"}, []string{"Could not read"}, "", false},
		{"off", autoInput{Admin: true, Loaded: true}, []string{"Off.", "you install them"}, "Turn on", true},
		{"on, never ran", autoInput{Admin: true, Loaded: true, Auto: AutoStatus{Enabled: true}},
			[]string{"On.", "starts at the next restart", "nothing restarts by itself"}, "Turn off", true},
		{"on, installed", autoInput{Admin: true, Loaded: true, Auto: AutoStatus{Enabled: true, LastTime: ago, LastResult: "installed", LastVersion: "v1.1.0"}},
			[]string{"Last run 2 hours ago: installed Isotope v1.1.0."}, "Turn off", true},
		{"on, skipped", autoInput{Admin: true, Loaded: true, Auto: AutoStatus{Enabled: true, LastTime: ago, LastResult: "skipped", LastDetail: "Not now: the connection is metered."}},
			[]string{"Last run 2 hours ago: Not now: the connection is metered."}, "Turn off", true},
		{"on, failed", autoInput{Admin: true, Loaded: true, Auto: AutoStatus{Enabled: true, LastTime: ago, LastResult: "failed", LastDetail: "Could not check for updates: offline"}},
			[]string{"failed. Could not check for updates: offline"}, "Turn off", true},
		{"broken", autoInput{Admin: true, Loaded: true, Auto: AutoStatus{Broken: "yaml: bad"}},
			[]string{"cannot be read", "yaml: bad"}, "Turn on", true},
		{"not an admin", autoInput{Loaded: true, Auto: AutoStatus{Enabled: true}},
			[]string{"On.", "Only an administrator"}, "Turn off", false},
	} {
		c.in.Now = autoNow
		v := autoViewOf(c.in)
		text := v.Sub + "|" + v.Last + "|" + v.Hint
		for _, w := range c.want {
			if !strings.Contains(text, w) {
				t.Errorf("%s: %q is not in %q", c.name, w, text)
			}
		}
		if v.Toggle != c.toggle || v.Enabled != c.enabled {
			t.Errorf("%s: toggle %q enabled %v, want %q %v", c.name, v.Toggle, v.Enabled, c.toggle, c.enabled)
		}
	}
}

func TestHeadlineSaysTheStagedVersion(t *testing.T) {
	st := Status{State: StateRebootPending, Pending: pendingInstalled, Staged: "v1.1.0"}
	if got := headline(st); got != "Version v1.1.0 is installed and starts at the next restart" {
		t.Fatalf("%q", got)
	}
	st.Staged = ""
	if got := headline(st); got != "The update is installed; restart to use it" {
		t.Fatalf("%q", got)
	}
}

func TestOfferHidesTheStagedVersion(t *testing.T) {
	st := Status{State: StateRebootPending, Pending: pendingInstalled, Installed: "v1.0.0", Latest: "v1.1.0", Staged: "v1.1.0"}
	o := OfferFor(st, []string{"v1.1.0", "v1.0.0", "v0.9.0"}, "full")
	if o.Latest != "" || len(o.Others) != 1 || o.Others[0] != "v0.9.0" || !o.Restart {
		t.Fatalf("offer %+v", o)
	}
}

func TestUpdatesPageTurnsAutoUpdatesOnAndOff(t *testing.T) {
	u, d, _ := testUI(t, true)
	u.show(PageUpdates)
	p := u.view(PageUpdates).(*updatesPage)
	eventually(t, "the setting", func() bool { u.mu.Lock(); defer u.mu.Unlock(); return p.auto.Loaded })
	for _, sz := range []image.Point{{1100, 720}, {700, 500}} {
		if dims := u.Layout(gtxOf(sz.X, sz.Y)); dims.Size != sz {
			t.Errorf("at %v: %v", sz, dims.Size)
		}
	}
	p.autoToggle.Click()
	p.Update(gtxOf(1100, 720))
	eventually(t, "SetAutoUpdate true", func() bool { return contains(d.called(), "SetAutoUpdate true") })
	eventually(t, "on", func() bool { u.mu.Lock(); defer u.mu.Unlock(); return p.auto.Auto.Enabled })
	p.autoToggle.Click()
	p.Update(gtxOf(1100, 720))
	eventually(t, "SetAutoUpdate false", func() bool { return contains(d.called(), "SetAutoUpdate false") })
	eventually(t, "off", func() bool { u.mu.Lock(); defer u.mu.Unlock(); return !p.auto.Auto.Enabled })
}

func TestUpdatesPageAutoUpdatesNeedAnAdmin(t *testing.T) {
	u, d, _ := testUI(t, false)
	u.show(PageUpdates)
	p := u.view(PageUpdates).(*updatesPage)
	eventually(t, "the setting", func() bool { u.mu.Lock(); defer u.mu.Unlock(); return p.auto.Loaded })
	p.autoToggle.Click()
	p.Update(gtxOf(1100, 720))
	time.Sleep(20 * time.Millisecond)
	if contains(d.called(), "SetAutoUpdate true") {
		t.Fatal("a non-admin's click reached the daemon")
	}
}

func TestUpdatesPageReloadsAutoUpdatesAfterAJob(t *testing.T) {
	u, d, _ := testUI(t, true)
	u.show(PageUpdates)
	p := u.view(PageUpdates).(*updatesPage)
	eventually(t, "the setting", func() bool { u.mu.Lock(); defer u.mu.Unlock(); return p.auto.Loaded })
	d.mu.Lock()
	d.auto = AutoStatus{Enabled: true, LastTime: time.Now().Unix(), LastResult: "installed", LastVersion: "v1.1.0", Staged: "v1.1.0", StagedAuto: true}
	d.mu.Unlock()
	u.setStatus(Status{State: StateApplying, Installed: "v1.0.0"})
	u.setStatus(Status{State: StateRebootPending, Pending: pendingInstalled, Installed: "v1.0.0", Staged: "v1.1.0", StagedAuto: true})
	eventually(t, "the reload", func() bool { u.mu.Lock(); defer u.mu.Unlock(); return p.auto.Auto.LastResult == "installed" })
}
