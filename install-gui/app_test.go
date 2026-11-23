package main

import (
	"context"
	"image"
	"testing"

	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"

	"github.com/kairos-community/isotope/install-ui/core"
)

// These need no GPU: they drive the UI's page logic over core.

func TestBackWalksThePagesInOrder(t *testing.T) {
	u := fixture(t, false)
	toSummary(t, u)
	for _, want := range []gpage{pEncrypt, pDisk, pAccount, pKeyboard, pWelcome, pWelcome} {
		u.prev()
		if u.page != want {
			t.Fatalf("back went to %d, want %d", u.page, want)
		}
	}
}

func TestSummaryChangeAndForwardAgain(t *testing.T) {
	u := fixture(t, false)
	toSummary(t, u)
	u.w.Page = u.firstCorePage(pAccount)
	u.goTo(pAccount)
	set(u.user, "sam")
	mustNext(t, u, pDisk)
	mustNext(t, u, pEncrypt)
	mustNext(t, u, pRecovery)
	set(u.rk, "vutrnlkj")
	mustNext(t, u, pSummary)
	if u.w.Choice.Username != "sam" || u.w.Choice.RecoveryKeyValue != testKey {
		t.Fatalf("choice after the change: %+v", u.w.Choice)
	}
}

func TestPasswordsMustMatch(t *testing.T) {
	u := fixture(t, false)
	toAccount(t, u)
	set(u.pw2, "something else")
	if u.passwordsMatch() {
		t.Fatal("different passwords match")
	}
	u.next()
	if u.page != pAccount || u.pw2.Err == "" {
		t.Fatalf("page %d, error %q", u.page, u.pw2.Err)
	}
}

func TestCoreErrorsShowUnderTheirFields(t *testing.T) {
	u := fixture(t, false)
	toAccount(t, u)
	set(u.user, "Alex")
	set(u.pw, "")
	set(u.pw2, "")
	u.next()
	if u.page != pAccount {
		t.Fatal("left the page with errors")
	}
	if u.user.Err == "" || u.pw.Err != "Password is required." {
		t.Fatalf("errors: user %q, password %q", u.user.Err, u.pw.Err)
	}
}

func TestEncryptOffSkipsTheKey(t *testing.T) {
	u := fixture(t, false)
	toEncrypt(t, u)
	u.encOn = false
	mustNext(t, u, pSummary)
	if u.w.Choice.Encrypt || u.w.Choice.RecoveryKey {
		t.Fatalf("choice: %+v", u.w.Choice)
	}
}

func TestShortPassphraseStays(t *testing.T) {
	u := fixture(t, false)
	toEncrypt(t, u)
	set(u.pp, "short")
	set(u.pp2, "short")
	u.next()
	if u.page != pEncrypt || u.pp.Err == "" {
		t.Fatalf("page %d, error %q", u.page, u.pp.Err)
	}
}

func TestRecoveryKeyContinueNeedsTheGroup(t *testing.T) {
	u := fixture(t, false)
	toRecovery(t, u)
	if u.recoveryConfirmed() {
		t.Fatal("a part of the group confirms")
	}
	set(u.rk, "vutrnlkj")
	if !u.recoveryConfirmed() {
		t.Fatal("the group does not confirm")
	}
	if u.w.Page != core.PageRecoveryConfirm {
		t.Fatal("checking the group moved the wizard")
	}
}

func TestTrustedBootAsksOnlyForTheKey(t *testing.T) {
	u := fixture(t, true)
	toAccount(t, u)
	mustNext(t, u, pDisk)
	mustNext(t, u, pEncrypt)
	mustNext(t, u, pSummary)
	if u.w.Choice.Encrypt || u.w.Choice.RecoveryKey {
		t.Fatalf("choice: %+v", u.w.Choice)
	}
}

func TestNewLayoutRelaunches(t *testing.T) {
	u := fixture(t, false)
	var got []core.GUIState
	u.hooks.relaunch = func(s core.GUIState) { got = append(got, s) }
	u.canRelaunch = true
	toKeyboard(t, u)
	u.pickKeyboard(u.kbSel)
	set(u.host, "box")
	u.next()
	if len(got) != 2 {
		t.Fatalf("%d relaunches, want 2", len(got))
	}
	if got[0].Layout != "it" || got[0].Next != "keyboard" || got[1].Next != "account" {
		t.Fatalf("states: %+v", got)
	}

	// Same layout as cage: no relaunch.
	u2 := fixture(t, false)
	u2.canRelaunch = true
	u2.runLayout = "it"
	toKeyboard(t, u2)
	mustNext(t, u2, pAccount)
}

func TestRestoreReopensWhereItWas(t *testing.T) {
	u := fixture(t, false)
	u.restore(core.GUIState{Next: "account", Layout: "it", Timezone: "Europe/Paris", Hostname: "box", Username: "alex", Github: "alex", Disk: "/dev/sda", Encrypt: true})
	if u.page != pAccount || u.w.Choice.KeyboardLayout != "it" || u.w.Choice.Timezone != "Europe/Paris" {
		t.Fatalf("page %d, choice %+v", u.page, u.w.Choice)
	}
	if u.user.Editor.Text() != "alex" || u.pw.Editor.Text() != "" || u.diskSel != 1 || !u.encOn {
		t.Fatal("fields not restored")
	}
}

func TestMapClickPicksTheNearestCity(t *testing.T) {
	u := fixture(t, false)
	toKeyboard(t, u)
	u.mapSize = image.Pt(360, 180)
	// Boston, in a 360x180 map: one pixel a degree.
	u.clickMap(image.Pt(109, 48))
	if z := u.w.Zones[u.tzSel]; z != "America/New_York" {
		t.Fatalf("picked %s", z)
	}
	if u.tzField.Editor.Text() != "America/New_York" || u.zoneFiltering() {
		t.Fatal("the field does not show the picked zone")
	}
}

func TestGithubLookupIsDebouncedAndOnce(t *testing.T) {
	u := fixture(t, false)
	calls := make(chan string, 4)
	u.hooks.githubKeys = func(_ context.Context, name string) (int, error) { calls <- name; return 2, nil }
	toAccount(t, u)
	u.ghState = githubState{results: u.ghState.results}
	set(u.gh, "sam")
	u.ghState.typed, u.ghState.typedAt = "sam", u.now()
	u.lookupGithub(false)
	if len(calls) != 0 {
		t.Fatal("asked GitHub while the name was being typed")
	}
	u.lookupGithub(true)
	if got := <-calls; got != "sam" {
		t.Fatalf("asked for %q", got)
	}
	u.lookupGithub(true)
	if len(calls) != 0 {
		t.Fatal("asked twice for one name")
	}
	r := <-u.ghState.results
	u.ghState.results <- r
	u.drain()
	if txt, good, ok := u.githubStatus(); !ok || !good || txt != "2 keys found" {
		t.Fatalf("status %q %v %v", txt, good, ok)
	}
}

// The welcome page's software line takes room only when cage runs in
// software, so the default page (and its screenshots) stay as they were.
func TestSoftwareNoteOnlyInSoftware(t *testing.T) {
	u := fixture(t, false)
	var ops op.Ops
	gtx := layout.Context{
		Ops:         &ops,
		Metric:      unit.Metric{PxPerDp: 1, PxPerSp: 1},
		Constraints: layout.Exact(image.Pt(560, 400)),
	}
	if d := u.softwareNote(gtx); d.Size != (image.Point{}) {
		t.Fatalf("software note drawn on a working GPU: %v", d.Size)
	}
	u.software = true
	if d := u.softwareNote(gtx); d.Size.Y == 0 {
		t.Fatal("no software note in software rendering")
	}
}

func TestSoftwareLineNamesTheCause(t *testing.T) {
	u := fixture(t, false)
	u.software = true
	if got := u.softwareLine(); got != softwareText {
		t.Errorf("full image, real GPU: %q", got)
	}
	u.vmImage = true
	if got := u.softwareLine(); got != softwareTextVM {
		t.Errorf("vm image, real GPU: %q", got)
	}
	u.gpuClass = "firmware"
	if got := u.softwareLine(); got != softwareTextFirmware {
		t.Errorf("firmware framebuffer: %q", got)
	}
}
