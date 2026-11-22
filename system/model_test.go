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
