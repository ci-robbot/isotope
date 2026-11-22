package main

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/kairos-community/isotope/update/isorelease"
)

func TestOfferFollowsTheTraysRules(t *testing.T) {
	tags := []string{"v0.3.0", "v0.2.0", "v0.1.0", "nightly"}
	o := OfferFor(Status{State: StateUpdateAvailable, Installed: "v0.2.0", Latest: "v0.3.0"}, tags, isorelease.GPUFull)
	if o.Latest != "v0.3.0" || !o.CanInstall || !o.Rollback || o.Cancel || o.Restart {
		t.Fatalf("update available: %+v", o)
	}
	if !reflect.DeepEqual(o.Others, []string{"v0.1.0"}) {
		t.Fatalf("others %q: not the installed, not the latest, not a non-release tag", o.Others)
	}

	// Up to date: nothing to install, but older releases are offered.
	o = OfferFor(Status{State: StateUpToDate, Installed: "v0.3.0", Latest: "v0.3.0"}, tags, isorelease.GPUFull)
	if o.Latest != "" || !reflect.DeepEqual(o.Others, []string{"v0.2.0", "v0.1.0"}) {
		t.Fatalf("up to date: %+v", o)
	}
	// Pinned: "latest" is how the user leaves the pin.
	o = OfferFor(Status{State: StateUpToDate, Installed: "v0.2.0", Latest: "v0.3.0", Pin: "v0.2.0"}, tags, isorelease.GPUFull)
	if o.Latest != "v0.3.0" {
		t.Fatalf("pinned: %+v", o)
	}
	// While a job runs or a restart is pending, nothing may be installed.
	for _, st := range []Status{
		{State: StateApplying}, {State: StateExtension}, {State: StateBackup}, {State: StateChecking},
		{State: StateRebootPending, Pending: pendingInstalled},
	} {
		if o := OfferFor(st, tags, isorelease.GPUFull); o.CanInstall || o.Rollback {
			t.Errorf("%s: installs allowed", st.State)
		}
	}
	if o := OfferFor(Status{State: StateInspecting}, nil, ""); !o.Cancel {
		t.Error("no Cancel while inspecting")
	}
	if o := OfferFor(Status{State: StateRebootPending}, nil, ""); !o.Restart {
		t.Error("no Restart while a reboot is pending")
	}
	// Advanced mode, never on Trusted Boot.
	if o := OfferFor(Status{State: StateIdle, Advanced: true}, nil, isorelease.GPUVM); !o.Advanced {
		t.Error("advanced mode not offered")
	}
	if o := OfferFor(Status{State: StateIdle, Advanced: true}, nil, isorelease.LineUKI); o.Advanced {
		t.Error("advanced mode offered on Trusted Boot")
	}
	// An unmanaged system offers nothing.
	if o := OfferFor(Status{State: StateUnmanaged, Latest: "v9.0.0"}, tags, ""); o.CanInstall || o.Latest != "" || o.Others != nil {
		t.Fatalf("unmanaged: %+v", o)
	}
}

// The image the page installs is the one the CLI would.
func TestReleaseRefIsTheCLIs(t *testing.T) {
	rel := isorelease.Release{Flavor: "sway", ImageRepo: "quay.io/kairos/isotope", GPU: isorelease.GPUVM}
	for _, c := range []struct{ flavor, line, want string }{
		{"sway", rel.Line(), "quay.io/kairos/isotope:sway-vm-v1.0.0"},
		{"niri", isorelease.GPUFull, "quay.io/kairos/isotope:niri-v1.0.0"},
		{"cosmic", isorelease.LineUKI, "quay.io/kairos/isotope:cosmic-uki-v1.0.0"},
	} {
		if got, err := releaseRef(rel, c.flavor, c.line, "v1.0.0"); err != nil || got != c.want {
			t.Errorf("%s/%s: %q %v, want %q", c.flavor, c.line, got, err, c.want)
		}
	}
	if _, err := releaseRef(isorelease.Release{}, "sway", "full", "v1.0.0"); err == nil {
		t.Error("no image repository, no error")
	}
	if got := newestTag([]string{"v0.9.0", "v0.10.0", "dev"}); got != "v0.10.0" {
		t.Errorf("newest %q", got)
	}
}

func TestInspectReportAndTheOverrideQuestion(t *testing.T) {
	info := InspectInfo{Ref: "quay.io/x/isotope@sha256:abc", Digest: "sha256:abc", Flavor: "sway", Version: "v1.0.0", GPU: "vm", Trusted: true}
	want := []string{"Image: quay.io/x/isotope", "Digest: sha256:abc", "Flavor: sway", "Version: v1.0.0",
		"Graphics: " + lineLabels["vm"], "Source: a published Isotope release (trusted)"}
	if got := InspectReport(info); !reflect.DeepEqual(got, want) {
		t.Fatalf("%q\nwant %q", got, want)
	}
	if q := overrideQuestion(info, true); q != "" {
		t.Fatalf("no refusal, but asked %q", q)
	}
	info = InspectInfo{Refusal: "not a release", Overridable: true, Isotope: true}
	if q := overrideQuestion(info, false); q != "" {
		t.Fatal("a refusal was lifted for a release install (not advanced)")
	}
	if q := overrideQuestion(info, true); !strings.Contains(q, "not a published Isotope release") {
		t.Fatalf("question %q", q)
	}
	info.Overridable = false
	if q := overrideQuestion(info, true); q != "" {
		t.Fatal("a final refusal was offered as a question")
	}
}

func TestInstallJobEndsLikeTheCLI(t *testing.T) {
	// A failed check left behind before the job does not end it.
	var j installJob
	if e := j.Step(Status{State: StateError, Detail: "old"}); e.Done {
		t.Fatal("an error before the job started ended it")
	}
	j.Step(Status{State: StateInspecting})
	j.Step(Status{State: StateApplying})
	if e := j.Step(Status{State: StateRebootPending, Pending: pendingInstalled}); !e.Done || !e.OK {
		t.Fatalf("installed: %+v", e)
	}
	j = installJob{}
	j.Step(Status{State: StateApplying})
	if e := j.Step(Status{State: StateRebootPending, Pending: pendingFailed}); !e.Done || e.OK {
		t.Fatalf("failed while writing: %+v", e)
	}
	j = installJob{}
	j.Step(Status{State: StateInspecting})
	if e := j.Step(Status{State: StateError, Detail: "no such image"}); !e.Done || e.OK || !strings.Contains(e.Message, "retry safely") {
		t.Fatalf("failed before writing: %+v", e)
	}
	j = installJob{}
	j.Step(Status{State: StateInspecting})
	if e := j.Step(Status{State: StateUpToDate}); !e.Done || e.OK {
		t.Fatalf("cancelled: %+v", e)
	}
}

func TestExtRowsMergeTheInstalledAndTheCatalog(t *testing.T) {
	installed := []Extension{{Name: "tailscale", Enabled: true}, {Name: "custom", Source: "oci:quay.io/me/custom"}}
	cat := []CatalogEntry{
		{Name: "tailscale", Title: "Tailscale", Description: "VPN mesh", Installed: true, Enabled: true},
		{Name: "gpg", Title: "GnuPG", Description: "Encryption tools", Latest: "2.4"},
		{Name: "android-tools", Title: "adb", Description: "Android debugging"},
	}
	inst, avail := ExtRows(installed, cat, "")
	if len(inst) != 2 || inst[0].Name != "custom" || inst[1].Title != "Tailscale" || !inst[1].Enabled {
		t.Fatalf("installed %+v", inst)
	}
	if len(avail) != 2 || avail[0].Name != "android-tools" || avail[1].Name != "gpg" {
		t.Fatalf("available %+v", avail)
	}
	inst, avail = ExtRows(installed, cat, "ENCRYPT")
	if len(inst) != 0 || len(avail) != 1 || avail[0].Name != "gpg" {
		t.Fatalf("filtered: %+v %+v", inst, avail)
	}
}

func TestBackupView(t *testing.T) {
	now := time.Date(2026, 11, 21, 15, 0, 0, 0, time.Local)
	if v := BackupViewOf(BackupStatus{}, now); v.CanBackUp || v.Setup || !strings.Contains(v.Lines[0], "not available") {
		t.Fatalf("no restic: %+v", v)
	}
	if v := BackupViewOf(BackupStatus{Available: true}, now); !v.Setup || v.CanBackUp {
		t.Fatalf("no target: %+v", v)
	}
	v := BackupViewOf(BackupStatus{Available: true, Target: "/run/media/anna/usb/backup", Schedule: "weekly",
		Last: now.Add(-time.Hour).Unix(), Result: "partial: some files could not be read", Present: true}, now)
	want := []string{"Backs up to /run/media/anna/usb/backup", "Automatic backups: weekly",
		"Last backup: today at 14:00", "Last try: backed up, but some files could not be read"}
	if !reflect.DeepEqual(v.Lines, want) || !v.CanBackUp {
		t.Fatalf("%+v\nwant %q", v, want)
	}
	v = BackupViewOf(BackupStatus{Available: true, Target: "/mnt/usb", Schedule: "daily", Warning: "x", Result: "restic failed"}, now)
	if v.CanBackUp || v.Warning == "" || v.Lines[len(v.Lines)-1] != "The backup disk is not plugged in." {
		t.Fatalf("unplugged: %+v", v)
	}
	// A USB disk by its UUID is named by its label, and asked for.
	v = BackupViewOf(BackupStatus{Available: true, Target: "uuid:1234-ABCD/isotope-backup", Label: "USB", Kind: "disk"}, now)
	if v.CanBackUp || v.Lines[0] != "Backs up to USB (USB disk)" || v.Lines[len(v.Lines)-1] != "Plug in USB to back up" {
		t.Fatalf("USB disk unplugged: %+v", v)
	}
	v = BackupViewOf(BackupStatus{Available: true, Target: "uuid:1234-ABCD/isotope-backup", Kind: "disk", Present: true}, now)
	if !v.CanBackUp || v.Lines[0] != "Backs up to uuid:1234-ABCD/isotope-backup (USB disk)" {
		t.Fatalf("USB disk without a label: %+v", v)
	}
	v = BackupViewOf(BackupStatus{Available: true, Target: "uuid:1234-ABCD/isotope-backup", Kind: "disk"}, now)
	if v.Lines[len(v.Lines)-1] != "Plug in the backup disk to back up" {
		t.Fatalf("USB disk without a label, unplugged: %+v", v)
	}
	if got := ago(now, now.AddDate(0, 0, -1)); got != "yesterday at 15:00" {
		t.Errorf("yesterday: %q", got)
	}
}

func TestRecoveryLines(t *testing.T) {
	got := RecoveryLines(RecoveryStatus{Edition: "trusted-boot", Containers: []Container{
		{Label: "COS_PERSISTENT_LUKS", TPM: 1, Recovery: 1, TPMWorks: true},
		{Label: "COS_OEM_LUKS", TPM: 1},
	}})
	want := []string{"Trusted Boot: the disk is sealed to this computer's TPM.",
		"COS_PERSISTENT_LUKS: recovery key, TPM", "COS_OEM_LUKS: TPM (does not unlock it now)"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%q\nwant %q", got, want)
	}
}

func TestSnapshotRowsNewestFirst(t *testing.T) {
	now := time.Date(2026, 11, 21, 15, 0, 0, 0, time.Local)
	snaps := []Snapshot{
		{ID: "aaa", Host: "old", Time: now.AddDate(0, 0, -10).Unix(), Size: 4e9},
		{ID: "ccc", Host: "anna-laptop", Time: now.Add(-time.Hour).Unix(), Size: 5e9},
		{ID: "bbb", Host: "anna-laptop", Time: now.AddDate(0, 0, -1).Unix()},
	}
	got := SnapshotRows(snaps, now)
	want := []string{"today at 14:00 · anna-laptop · 5.0 GB", "yesterday at 15:00 · anna-laptop",
		"11 November 2026, 15:00 · old · 4.0 GB"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%q\nwant %q", got, want)
	}
	if snaps[0].ID != "aaa" {
		t.Fatal("SnapshotRows sorted its argument")
	}
}

func TestExportName(t *testing.T) {
	now := time.Date(2026, 11, 22, 9, 0, 0, 0, time.Local)
	if got := exportName("anna-laptop", now); got != "isotope-anna-laptop-2026-11-22.isotope-backup" {
		t.Errorf("got %q", got)
	}
	if got := exportName("", now); got != "isotope-2026-11-22.isotope-backup" {
		t.Errorf("no host: %q", got)
	}
}

func TestSetupTargets(t *testing.T) {
	usb := Block{ID: "b", Label: "USB", UUID: "1234-ABCD", FSType: "exfat", IsFilesystem: true}
	for folder, want := range map[string]string{"isotope-backup": "uuid:1234-ABCD/isotope-backup", "/a/b/": "uuid:1234-ABCD/a/b", " x ": "uuid:1234-ABCD/x"} {
		if got, err := usbTarget(usb, folder); err != nil || got != want {
			t.Errorf("folder %q: %q %v", folder, got, err)
		}
	}
	for _, bad := range []string{"", "/", "../x", "a/../b", "a\nb"} {
		if _, err := usbTarget(usb, bad); err == nil {
			t.Errorf("folder %q accepted", bad)
		}
	}

	tg, env, err := restTarget("https://backup.example.com:8000/anna/", "anna", "pw")
	if err != nil || tg != "rest:https://backup.example.com:8000/anna/" ||
		!reflect.DeepEqual(env, []string{"RESTIC_REST_USERNAME=anna", "RESTIC_REST_PASSWORD=pw"}) {
		t.Errorf("rest: %q %q %v", tg, env, err)
	}
	if _, env, _ := restTarget("http://nas:8000/", "", ""); len(env) != 0 {
		t.Errorf("rest without a user: %q", env)
	}
	for _, bad := range []string{"", "nas:8000", "https://anna:pw@nas/", "ftp://nas/"} {
		if _, _, err := restTarget(bad, "", ""); err == nil {
			t.Errorf("rest %q accepted", bad)
		}
	}

	tg, env, err = s3Target("https://s3.example.com/", "bucket", "/isotope/", "KEY", "SECRET", "eu-west-1")
	if err != nil || tg != "s3:https://s3.example.com/bucket/isotope" ||
		!reflect.DeepEqual(env, []string{"AWS_ACCESS_KEY_ID=KEY", "AWS_SECRET_ACCESS_KEY=SECRET", "AWS_DEFAULT_REGION=eu-west-1"}) {
		t.Errorf("s3: %q %q %v", tg, env, err)
	}
	if tg, env, _ := s3Target("s3.amazonaws.com", "b", "", "K", "S", ""); tg != "s3:s3.amazonaws.com/b" || len(env) != 2 {
		t.Errorf("s3 without a path or region: %q %q", tg, env)
	}
	for _, f := range [][6]string{{"", "b", "", "K", "S"}, {"e", "", "", "K", "S"}, {"e", "b", "", "", "S"}, {"e", "b", "", "K", ""}, {"e", "b/c", "", "K", "S"}} {
		if _, _, err := s3Target(f[0], f[1], f[2], f[3], f[4], f[5]); err == nil {
			t.Errorf("s3 %q accepted", f)
		}
	}

	for _, c := range []struct {
		userHost, port, path, want string
		wantPort                   uint32
	}{
		{"anna@nas.local", "22", "/srv/backup", "sftp:anna@nas.local:/srv/backup", 22},
		{"anna@nas.local", "", "backup", "sftp:anna@nas.local:backup", 22},
		{"anna@nas.local", "2222", "/srv/backup", "sftp://anna@nas.local:2222//srv/backup", 2222},
		{"anna@nas.local", "2222", "backup", "sftp://anna@nas.local:2222/backup", 2222},
	} {
		got, host, port, err := sftpTarget(c.userHost, c.port, c.path)
		if err != nil || got != c.want || host != "nas.local" || port != c.wantPort {
			t.Errorf("sftp %+v: %q %q %d %v", c, got, host, port, err)
		}
	}
	for _, bad := range [][3]string{{"", "22", "/p"}, {"anna@", "22", "/p"}, {"anna@nas", "x", "/p"}, {"anna@nas", "70000", "/p"}, {"anna@nas", "22", ""}, {"-oProxy@nas", "22", "/p"}, {"anna@nas:22", "22", "/p"}} {
		if _, _, _, err := sftpTarget(bad[0], bad[1], bad[2]); err == nil {
			t.Errorf("sftp %q accepted", bad)
		}
	}
}

func TestHistoryLines(t *testing.T) {
	now := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)
	at := func(d time.Duration) int64 { return now.Add(-d).Unix() }
	const day = 24 * time.Hour
	h := []HistoryEntry{
		{Time: at(3 * day), Kind: "install", Version: "v1.1.0", Result: "installed"},
		{Time: at(30 * time.Hour), Kind: "install", Version: "v1.2.0", Result: "failed"},
		{Time: at(2 * time.Hour), Kind: "rollback"},
		{Time: at(10 * day), Kind: "install", Version: "v1.0.0", Result: "installed"},
		{Time: at(5 * day), Kind: "install", Version: "v1.1.0", Result: "cancelled"},
		{Time: at(20 * time.Minute), Kind: "install", Result: "cancelled"},
		{Time: at(40 * day), Kind: "install", Version: "v1.0.0", Result: "installed"},
		{Time: at(15 * time.Minute), Kind: "install", Result: "failed"},
	}
	got := HistoryLines(h, "v1.1.0", now)
	want := []string{
		"An update failed · 15 minutes ago",
		"An update was cancelled · 20 minutes ago",
		"Restarted into the previous system · 2 hours ago",
		"v1.2.0 failed · yesterday",
		"v1.1.0 installed (running) · 3 days ago",
		"v1.1.0 cancelled · 5 days ago",
		"v1.0.0 installed · 10 days ago",
		"v1.0.0 installed · 13 October 2026",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got\n%q\nwant\n%q", got, want)
	}
	// Only the newest 10; none for no history.
	var many []HistoryEntry
	for i := 0; i < 15; i++ {
		many = append(many, HistoryEntry{Time: at(time.Duration(i) * time.Hour), Kind: "install", Version: "v2", Result: "installed"})
	}
	if n := len(HistoryLines(many, "", now)); n != 10 {
		t.Errorf("%d lines", n)
	}
	if HistoryLines(nil, "v1", now) != nil {
		t.Error("lines for no history")
	}
	// An empty installed version marks nothing.
	if l := HistoryLines([]HistoryEntry{{Time: at(time.Hour), Kind: "install", Result: "installed"}}, "", now); strings.Contains(l[0], "running") {
		t.Errorf("%q", l)
	}
}

func TestHistoryLinesTiesAndLocalDays(t *testing.T) {
	// Equal times: the later entry of the list first.
	now := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)
	tm := now.Add(-time.Hour).Unix()
	got := HistoryLines([]HistoryEntry{
		{Time: tm, Kind: "install", Version: "v1", Result: "installed"},
		{Time: tm, Kind: "install", Version: "v2", Result: "failed"},
	}, "", now)
	if !strings.HasPrefix(got[0], "v2 failed") || !strings.HasPrefix(got[1], "v1 installed") {
		t.Errorf("ties: %q", got)
	}
	if l := HistoryLines([]HistoryEntry{{Time: tm, Kind: "install", Result: "weird"}}, "", now); !strings.HasPrefix(l[0], "An update: weird") {
		t.Errorf("%q", l)
	}
	if l := HistoryLines([]HistoryEntry{{Time: tm, Kind: "install", Result: "installed"}}, "", now); !strings.HasPrefix(l[0], "An update was installed") {
		t.Errorf("%q", l)
	}
	// UTC+13, just after local midnight: the days count by local date.
	loc := time.FixedZone("far", 13*3600)
	n := time.Date(2026, 11, 23, 0, 30, 0, 0, loc)
	for _, c := range []struct {
		t    time.Time
		want string
	}{
		{time.Date(2026, 11, 22, 0, 10, 0, 0, loc), "yesterday"},
		{time.Date(2026, 11, 21, 23, 50, 0, 0, loc), "2 days ago"},
		{time.Date(2026, 11, 20, 12, 0, 0, 0, loc), "3 days ago"},
	} {
		if g := since(n, c.t); g != c.want {
			t.Errorf("%v: %q, want %q", c.t, g, c.want)
		}
	}
}
