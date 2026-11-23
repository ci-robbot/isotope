package main

import (
	"reflect"
	"testing"
)

func TestFlathubReady(t *testing.T) {
	for _, c := range []struct {
		name, out string
		want      bool
	}{
		{"enabled", "flathub\t\n", true},
		{"other option", "flathub\tuser\n", true},
		{"disabled", "flathub\tdisabled\n", false},
		{"disabled among", "flathub\tuser,disabled\n", false},
		{"other remote only", "fedora\t\n", false},
		{"none", "", false},
	} {
		r := &fakeRunner{out: map[string]string{flathubOK: c.out}}
		if got := FlathubReady(r); got != c.want {
			t.Errorf("%s: FlathubReady = %v, want %v", c.name, got, c.want)
		}
	}
	if FlathubReady(&fakeRunner{fail: []string{"remotes"}}) {
		t.Error("a failing flatpak is not ready")
	}
}

func TestInstalledApps(t *testing.T) {
	r := &fakeRunner{out: map[string]string{installedList: "org.mozilla.firefox\norg.gnome.Loupe\n\n"}}
	got, err := InstalledApps(r)
	if err != nil || !got["org.mozilla.firefox"] || !got["org.gnome.Loupe"] || len(got) != 2 {
		t.Fatalf("%v %v", got, err)
	}
}

func TestOnline(t *testing.T) {
	if !Online(&fakeRunner{}) {
		t.Error("nm-online exiting 0 is online")
	}
	if Online(&fakeRunner{fail: []string{"nm-online"}}) {
		t.Error("nm-online exiting 1 is offline")
	}
}

var vlc = "org.videolan.VLC"

func TestInstallerReportsFailures(t *testing.T) {
	r := &fakeRunner{fail: []string{vlc}}
	in := NewInstaller(r, loadApps(t))
	var got []string
	in.OnApp = func(id string, ok bool) {
		if ok {
			got = append(got, id)
		}
	}
	ids := []string{"org.mozilla.firefox", "org.gnome.Loupe", vlc, "org.gnome.Papers"}
	in.Start(ids)
	in.Wait()
	ok, failed, finished := in.Result()
	if !finished {
		t.Fatal("not finished after Wait")
	}
	if !reflect.DeepEqual(ok, []string{"org.mozilla.firefox", "org.gnome.Loupe", "org.gnome.Papers"}) {
		t.Errorf("ok = %v", ok)
	}
	if !reflect.DeepEqual(failed, []string{vlc}) {
		t.Errorf("failed = %v", failed)
	}
	if !reflect.DeepEqual(got, ok) {
		t.Errorf("OnApp saw %v, want %v", got, ok)
	}
	done, total, _ := in.Progress()
	if done != 4 || total != 4 {
		t.Errorf("progress %d/%d", done, total)
	}
	want := "flatpak --user install --noninteractive flathub org.mozilla.firefox"
	if c := r.called(); c[0] != want || len(c) != 4 {
		t.Errorf("calls %v, want first %q", c, want)
	}
}

func TestInstallerRunsAgainAfterFailures(t *testing.T) {
	r := &fakeRunner{fail: []string{vlc}}
	in := NewInstaller(r, loadApps(t))
	in.Start([]string{vlc})
	in.Wait()
	r.fail = nil
	in.Start([]string{vlc})
	in.Wait()
	ok, failed, _ := in.Result()
	if len(ok) != 1 || len(failed) != 0 {
		t.Errorf("second run: ok %v failed %v", ok, failed)
	}
}

// TestInstallerDetaches: the processes the real runner starts are in a new
// session, so they outlive the window and the terminal it came from.
func TestInstallerDetaches(t *testing.T) {
	cmd := DetachedCommand("flatpak", "--user", "install", "x")
	if cmd.SysProcAttr == nil || !cmd.SysProcAttr.Setsid {
		t.Fatalf("SysProcAttr = %+v, want Setsid", cmd.SysProcAttr)
	}
}
