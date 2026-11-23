package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
)

// Tests for the Trusted Boot (uki) image line: release file, Classify,
// refs, the tray menu and the CLI. The daemon's are in daemon_test.go.

func tbRel() Release {
	r := policyRel()
	r.TrustedBoot = true
	return r
}

var ukiLabels = map[string]string{labelFlavor: "sway", labelVersion: "v0.0.4", labelGPU: gpuFull, labelTrustedBoot: "true"}

func TestLoadReleaseTrustedBoot(t *testing.T) {
	for _, c := range []struct {
		line string
		want bool
	}{
		{"", false}, // images from before the key
		{"TRUSTED_BOOT=false\n", false},
		{"TRUSTED_BOOT=true\n", true},
		{`TRUSTED_BOOT="true"` + "\n", true},
	} {
		r, err := LoadRelease(writeRelease(t, goodRelease+c.line))
		if err != nil || r.TrustedBoot != c.want {
			t.Errorf("%q: got %v, %v; want %v", c.line, r.TrustedBoot, err, c.want)
		}
	}
	for _, bad := range []string{"TRUSTED_BOOT=yes\n", "TRUSTED_BOOT=1\n", "TRUSTED_BOOT=True\n"} {
		if _, err := LoadRelease(writeRelease(t, goodRelease+bad)); err == nil || !strings.Contains(err.Error(), "TRUSTED_BOOT") {
			t.Errorf("%q: got %v, want an error naming TRUSTED_BOOT", bad, err)
		}
	}
}

func TestClassifyTrustedBootTrustsOnlyTheUKILineByName(t *testing.T) {
	rel := tbRel()
	pub := []Latest{{Tag: "v0.0.4"}}
	for _, flavor := range []string{"sway", "niri"} {
		v, err := Classify(policyInfo(t, rel.ImageRepo+":"+flavor+"-uki-v0.0.4", nil), rel, pub)
		want := Verdict{Flavor: flavor, Version: "v0.0.4", GPU: lineUKI, Trusted: true, Isotope: true}
		if err != nil || v != want {
			t.Errorf("%s: got %+v, %v; want %+v", flavor, v, err, want)
		}
	}
	srv := tbRel()
	srv.Flavor = flavorServer
	v, err := Classify(policyInfo(t, rel.ImageRepo+":server-uki-v0.0.4", nil), srv, pub)
	if err != nil || !v.Trusted || v.Flavor != flavorServer {
		t.Errorf("server: got %+v, %v", v, err)
	}
}

func TestClassifyTrustedBootRefusesTheOtherLines(t *testing.T) {
	rel := tbRel()
	pub := []Latest{{Tag: "v0.0.4"}}
	for _, ref := range []string{
		rel.ImageRepo + ":sway-v0.0.4",    // the full line
		rel.ImageRepo + ":sway-vm-v0.0.4", // the vm line
		rel.ImageRepo + ":niri-v0.0.4",
		"example.com/other/img:latest",
	} {
		if _, err := Classify(policyInfo(t, ref, nil), rel, pub); !errors.Is(err, ErrNeedsTrustedBoot) {
			t.Errorf("%s: got %v, want ErrNeedsTrustedBoot", ref, err)
		}
	}
	// Labelled as Isotope, but not as Trusted Boot.
	for _, tb := range []string{"", "false"} {
		labels := map[string]string{labelFlavor: "sway", labelVersion: "v0.0.4"}
		if tb != "" {
			labels[labelTrustedBoot] = tb
		}
		if _, err := Classify(policyInfo(t, "example.com/other/img:latest", labels), rel, pub); !errors.Is(err, ErrNeedsTrustedBoot) {
			t.Errorf("trusted-boot=%q: got %v, want ErrNeedsTrustedBoot", tb, err)
		}
	}
	// A label that is neither true nor false is not trusted as a UKI.
	labels := map[string]string{labelFlavor: "sway", labelVersion: "v0.0.4", labelTrustedBoot: "yes"}
	if _, err := Classify(policyInfo(t, "example.com/other/img:latest", labels), rel, pub); err == nil {
		t.Error("trusted-boot=yes: accepted")
	}
}

func TestClassifyTrustedBootRejectsUKILookalikes(t *testing.T) {
	rel := tbRel()
	pub := []Latest{{Tag: "v0.0.4"}, {Tag: "v0.0.4-rc1"}}
	for _, ref := range []string{
		rel.ImageRepo + ":sway-uki-v0.0.5",                 // not published
		rel.ImageRepo + ":sway-uki-v0.0.4-rc1",             // prerelease
		rel.ImageRepo + ":foo-uki-v0.0.4",                  // not a flavor
		rel.ImageRepo + ":sway-uki-uki-v0.0.4",             // line twice
		rel.ImageRepo + ":sway-vm-uki-v0.0.4",              // two lines
		rel.ImageRepo + ":uki-sway-v0.0.4",                 // line first
		"ghcr.io/evil/isotope:sway-uki-v0.0.4",             // other repo
		rel.ImageRepo + ":sway-uki-v0.0.4@sha256:" + hex64, // resolved by digest
	} {
		if v, err := Classify(policyInfo(t, ref, nil), rel, pub); err == nil {
			t.Errorf("%s: accepted as %+v", ref, v)
		}
	}
}

func TestClassifyTrustedBootByLabel(t *testing.T) {
	want := Verdict{Flavor: "sway", Version: "v0.0.4", GPU: lineUKI, Isotope: true}
	if v, err := Classify(policyInfo(t, "example.com/other/img:latest", ukiLabels), tbRel(), nil); err != nil || v != want {
		t.Fatalf("got %+v, %v; want %+v", v, err, want)
	}
	// The labels from before the rename count too.
	legacy := map[string]string{
		legacyLabelPrefix + "flavor": "sway", legacyLabelPrefix + "version": "v0.0.4",
		legacyLabelPrefix + "trusted-boot": "true",
	}
	if v, err := Classify(policyInfo(t, "example.com/other/img:latest", legacy), tbRel(), nil); err != nil || v != want {
		t.Fatalf("legacy labels: got %+v, %v", v, err)
	}
}

// The guards that ran before still win on Trusted Boot.
func TestClassifyTrustedBootKeepsTheAgentAndEditionGuards(t *testing.T) {
	rel := tbRel()
	agent := map[string]string{labelFlavor: "agent", labelVersion: "v0.0.4", labelTrustedBoot: "true"}
	if _, err := Classify(policyInfo(t, "example.com/x:y", agent), rel, nil); !errors.Is(err, ErrAgentImage) {
		t.Errorf("agent: got %v", err)
	}
	pub := []Latest{{Tag: "v0.0.4"}}
	if _, err := Classify(policyInfo(t, rel.ImageRepo+":server-uki-v0.0.4", nil), rel, pub); !errors.Is(err, ErrServerImage) {
		t.Errorf("server on a desktop: got %v", err)
	}
}

func TestClassifyGRUBRefusesUKIImages(t *testing.T) {
	rel := policyRel()
	pub := []Latest{{Tag: "v0.0.4"}}
	for name, info := range map[string]ImageInfo{
		"uki release tag":      policyInfo(t, rel.ImageRepo+":sway-uki-v0.0.4", nil),
		"uki tag elsewhere":    policyInfo(t, "example.com/other/img:sway-uki-v0.0.4", nil),
		"trusted-boot label":   policyInfo(t, "example.com/other/img:latest", ukiLabels),
		"label on release tag": policyInfo(t, rel.ImageRepo+":sway-v0.0.4", ukiLabels),
		"legacy label":         policyInfo(t, "example.com/other/img:latest", map[string]string{legacyLabelPrefix + "trusted-boot": "true"}),
	} {
		if _, err := Classify(info, rel, pub); !errors.Is(err, ErrTrustedBootImage) {
			t.Errorf("%s: got %v, want ErrTrustedBootImage", name, err)
		}
	}
	// trusted-boot=false is a normal image.
	labels := map[string]string{labelFlavor: "sway", labelVersion: "v0.0.4", labelTrustedBoot: "false"}
	if v, err := Classify(policyInfo(t, "example.com/other/img:latest", labels), rel, nil); err != nil || !v.Isotope {
		t.Errorf("trusted-boot=false: got %+v, %v", v, err)
	}
}

// A GRUB system's verdicts on its own lines do not change.
func TestClassifyGRUBLinesUnchanged(t *testing.T) {
	rel := policyRel()
	pub := []Latest{{Tag: "v0.0.4"}}
	for ref, want := range map[string]Verdict{
		rel.ImageRepo + ":niri-v0.0.4":    {Flavor: "niri", Version: "v0.0.4", GPU: gpuFull, Trusted: true, Isotope: true},
		rel.ImageRepo + ":niri-vm-v0.0.4": {Flavor: "niri", Version: "v0.0.4", GPU: gpuVM, Trusted: true, Isotope: true},
	} {
		if v, err := Classify(policyInfo(t, ref, nil), rel, pub); err != nil || v != want {
			t.Errorf("%s: got %+v, %v; want %+v", ref, v, err, want)
		}
	}
	for line, want := range map[string]string{"": "sway-v0.0.4", gpuFull: "sway-v0.0.4", gpuVM: "sway-vm-v0.0.4"} {
		r := policyRel()
		r.GPU = line
		if ref, err := ReleaseRef(r, "sway", "v0.0.4"); err != nil || ref.Tag != want {
			t.Errorf("GPU=%q: got %+v, %v; want tag %s", line, ref, err, want)
		}
	}
}

func TestUKIRefs(t *testing.T) {
	rel := tbRel()
	for flavor, want := range map[string]string{"sway": "sway-uki-v0.0.4", "niri": "niri-uki-v0.0.4", flavorServer: "server-uki-v0.0.4"} {
		ref, err := ReleaseRef(rel, flavor, "v0.0.4")
		if err != nil || ref.Tag != want {
			t.Errorf("%s: got %+v, %v; want tag %s", flavor, ref, err, want)
		}
	}
	if got := rel.ImageFor("v0.0.4"); got != rel.ImageRepo+":sway-uki-v0.0.4" {
		t.Errorf("ImageFor = %q", got)
	}
	// GPU=vm in the release file does not move a Trusted Boot system off uki.
	rel.GPU = gpuVM
	if rel.Line() != lineUKI {
		t.Errorf("Line() = %q, want uki", rel.Line())
	}
}

func TestAdvancedNeverAllowedOnTrustedBoot(t *testing.T) {
	t.Setenv("ISOTOPE_UPDATE_CONF", t.TempDir()+"/update.conf")
	if !advancedAllowed(policyRel()) {
		t.Fatal("a GRUB system with the default update.conf must allow advanced mode")
	}
	if advancedAllowed(tbRel()) {
		t.Fatal("advanced mode allowed on Trusted Boot")
	}
}

// On Trusted Boot the tray offers no other line and no custom image, but
// keeps "Switch desktop" (to the other desktops' uki images).
func TestBuildMenuTrustedBoot(t *testing.T) {
	st := menuStatus(StateUpToDate)
	st.Advanced = true
	m := BuildMenu(st, menuReleases, "sway", lineUKI)
	for _, id := range ids(m) {
		if strings.HasPrefix(id, "gpu/") || id == "advanced" {
			t.Errorf("Trusted Boot menu has %q", id)
		}
	}
	if sw, ok := find(m, "switch"); !ok || len(sw.Children) != len(switchable)-1 {
		t.Errorf("switch = %+v (%v)", sw, ok)
	}
	srv := BuildMenu(st, menuReleases, flavorServer, lineUKI)
	if _, ok := find(srv, "advanced"); ok {
		t.Error("Trusted Boot server menu has advanced")
	}
	// The same status on the full line still has both.
	full := BuildMenu(st, menuReleases, "sway", gpuFull)
	for _, id := range []string{"gpu/vm", "advanced", "switch"} {
		if _, ok := find(full, id); !ok {
			t.Errorf("full-line menu lacks %q", id)
		}
	}
}

// appendRelease adds lines to the release file setup or daemonEnv wrote.
func appendRelease(t *testing.T, lines string) {
	t.Helper()
	p := os.Getenv("ISOTOPE_UPDATE_RELEASE_FILE")
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, append(b, lines...), 0o644); err != nil {
		t.Fatal(err)
	}
}

// setupTrustedBoot is setup on a Trusted Boot system: v0.2.0 is the latest
// release, with a uki manifest.
func setupTrustedBoot(t *testing.T, flavor string) {
	t.Helper()
	setupFlavor(t, flavor, "v0.1.0", "v0.2.0")
	appendRelease(t, "TRUSTED_BOOT=true\n")
	fakeGitHubReleases(t, "", withUKIManifests(t, `[{"tag_name":"v0.2.0","html_url":"https://example.invalid/v0.2.0"}]`))
}

// withUKIManifests is withManifests for a release that build-uki also
// finished: every entry without "assets" gets an uploaded sway manifest and
// an uploaded sway-uki one.
func withUKIManifests(t *testing.T, releases string) string {
	t.Helper()
	var list []map[string]any
	if err := json.Unmarshal([]byte(releases), &list); err != nil {
		t.Fatalf("releases fixture: %v", err)
	}
	for _, r := range list {
		if _, ok := r["assets"]; !ok {
			r["assets"] = []map[string]string{
				{"name": fmt.Sprintf("isotope-sway-%v-amd64.iso.build.json", r["tag_name"]), "state": "uploaded"},
				{"name": fmt.Sprintf("isotope-sway-uki-%v-amd64.iso.build.json", r["tag_name"]), "state": "uploaded"},
			}
		}
	}
	b, err := json.Marshal(list)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// mixedReleases: v0.3.0 was built with TRUSTED_BOOT_RELEASE off (normal
// manifests only); v0.2.9 has a uki manifest; v0.2.8's uki manifest is
// still uploading; v0.2.7 has a uki ISO but no uki manifest.
const mixedReleases = `[
	{"tag_name":"v0.3.0","assets":[
		{"name":"isotope-sway-v0.3.0-amd64.iso.build.json","state":"uploaded"},
		{"name":"isotope-niri-v0.3.0-amd64.iso.build.json","state":"uploaded"}]},
	{"tag_name":"v0.2.9","assets":[
		{"name":"isotope-sway-v0.2.9-amd64.iso.build.json","state":"uploaded"},
		{"name":"isotope-niri-uki-v0.2.9-amd64.iso.build.json","state":"uploaded"}]},
	{"tag_name":"v0.2.8","assets":[
		{"name":"isotope-sway-v0.2.8-amd64.iso.build.json","state":"uploaded"},
		{"name":"isotope-sway-uki-v0.2.8-amd64.iso.build.json","state":"open"}]},
	{"tag_name":"v0.2.7","assets":[
		{"name":"isotope-sway-v0.2.7-amd64.iso.build.json","state":"uploaded"},
		{"name":"isotope-sway-uki-v0.2.7-amd64.iso","state":"uploaded"}]}
]`

func TestFetchReleasesTrustedBootNeedsAUKIManifest(t *testing.T) {
	fakeGitHubReleases(t, "", mixedReleases)
	for _, c := range []struct {
		trustedBoot bool
		want        []string
	}{
		{false, []string{"v0.3.0", "v0.2.9", "v0.2.8", "v0.2.7"}}, // any manifest, as before
		{true, []string{"v0.2.9"}},
	} {
		got, err := FetchReleases(context.Background(), "kairos-community/isotope", c.trustedBoot)
		if err != nil {
			t.Fatal(err)
		}
		var tags []string
		for _, l := range got {
			tags = append(tags, l.Tag)
		}
		if !reflect.DeepEqual(tags, c.want) {
			t.Errorf("trustedBoot=%v: tags %v, want %v", c.trustedBoot, tags, c.want)
		}
		latest, err := FetchLatest(context.Background(), "kairos-community/isotope", c.trustedBoot)
		if err != nil || latest.Tag != c.want[0] {
			t.Errorf("trustedBoot=%v: latest %+v, %v; want %s", c.trustedBoot, latest, err, c.want[0])
		}
	}
	// Nothing with a uki manifest: a Trusted Boot system has no update.
	fakeGitHubReleases(t, "", `[{"tag_name":"v0.3.0","assets":[{"name":"isotope-sway-v0.3.0-amd64.iso.build.json","state":"uploaded"}]}]`)
	if _, err := FetchLatest(context.Background(), "kairos-community/isotope", true); err == nil {
		t.Error("Trusted Boot: want an error when no release has a uki manifest")
	}
}

// `isotope status` reads the same rule: a Trusted Boot system is not told
// about a release it cannot pull; a GRUB system is.
func TestStatusLatestFollowsTheLine(t *testing.T) {
	setupTrustedBoot(t, "sway")
	f := okDaemon()
	useDaemon(t, f)
	fakeGitHubReleases(t, "", mixedReleases)
	if _, out, e := runCLI("", "status"); !strings.Contains(out, "Latest:     v0.2.9 is available.") {
		t.Errorf("Trusted Boot: out %q, stderr %q", out, e)
	}
	setup(t, "v0.1.0", "v0.2.0")
	useDaemon(t, f)
	fakeGitHubReleases(t, "", mixedReleases)
	if _, out, e := runCLI("", "status"); !strings.Contains(out, "Latest:     v0.3.0 is available.") {
		t.Errorf("GRUB: out %q, stderr %q", out, e)
	}
}

func TestTrustedBootUpgradesAlongTheUKILine(t *testing.T) {
	setupTrustedBoot(t, "sway")
	f := okDaemon()
	f.info.GPU = lineUKI
	useDaemon(t, f)
	code, out, e := runCLI("", "upgrade", "--yes")
	if code != exitOK || len(f.applied) == 0 || !strings.HasSuffix(f.applied[0], ":sway-uki-v0.2.0") {
		t.Fatalf("code %d, applied %v, stderr %q", code, f.applied, e)
	}
	// Its own line: no warning about another one.
	if strings.Contains(out, "This is the hardware image") || strings.Contains(out, "virtual machine") {
		t.Fatalf("warned about a line: %q", out)
	}
}

func TestTrustedBootSwitchesDesktopOnTheUKILine(t *testing.T) {
	setupTrustedBoot(t, "sway")
	f := okDaemon()
	useDaemon(t, f)
	if code, _, e := runCLI("y\nn\n", "switch", "niri"); code != exitOK || len(f.applied) == 0 || !strings.HasSuffix(f.applied[0], ":niri-uki-v0.2.0") {
		t.Fatalf("code %d, applied %v, stderr %q", code, f.applied, e)
	}
}

func TestTrustedBootRefusesSwitchGPU(t *testing.T) {
	setupTrustedBoot(t, "sway")
	for _, args := range [][]string{{"--gpu", "vm"}, {"--gpu", "full"}, {"niri", "--gpu", "vm"}} {
		f := okDaemon()
		useDaemon(t, f)
		code, _, e := runCLI("y\ny\n", append([]string{"switch"}, append(args, "--yes")...)...)
		if code != exitUsage || !strings.Contains(e, "Trusted Boot") || f.called("Apply") {
			t.Errorf("switch %v: code %d, stderr %q, calls %v", args, code, e, f.calls)
		}
	}
}

func TestTrustedBootRefusesACustomImage(t *testing.T) {
	setupTrustedBoot(t, "sway")
	f := okDaemon()
	useDaemon(t, f) // okDaemon reports advanced mode on
	for _, img := range []string{"ghcr.io/x/y:t", "-"} {
		code, _, e := runCLI("ghcr.io/x/y:t\ny\ny\n", "upgrade", "--yes", "--image", img)
		if code != exitUsage || !strings.Contains(e, "Trusted Boot") || f.called("Inspect") || f.called("Apply") {
			t.Errorf("--image %s: code %d, stderr %q, calls %v", img, code, e, f.calls)
		}
	}
}

func TestStatusShowsTrustedBoot(t *testing.T) {
	setupTrustedBoot(t, "sway")
	f := okDaemon()
	useDaemon(t, f)
	if _, out, _ := runCLI("", "status"); !strings.Contains(out, "Image:      Trusted Boot (signed UKI, hardware image)") {
		t.Fatalf("out %q", out)
	}
	setupTrustedBoot(t, flavorServer)
	useDaemon(t, f)
	if _, out, _ := runCLI("", "status"); !strings.Contains(out, "server edition (no desktop), Trusted Boot") {
		t.Fatalf("server: out %q", out)
	}
	// A GRUB system says what it said before.
	setup(t, "v0.1.0", "v0.2.0")
	useDaemon(t, f)
	if _, out, _ := runCLI("", "status"); !strings.Contains(out, "Image:      hardware (Intel and AMD GPU drivers, firmware)\n") {
		t.Fatalf("GRUB: out %q", out)
	}
}
