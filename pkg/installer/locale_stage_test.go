package installer

import (
	"os"
	"strings"
	"testing"
)

func localeChoice(locale string) Choice {
	return Choice{Hostname: "box", Username: "ada", Disk: "/dev/vda", KeyboardLayout: "de",
		ConsoleKeymap: "de", Timezone: "Europe/Berlin", Github: "ada", Locale: locale}
}

// fileContent is the content of the cloud-config file at path, unindented.
func fileContent(t *testing.T, cfg, path string) string {
	t.Helper()
	i := strings.Index(cfg, "- path: "+path+"\n")
	if i < 0 {
		t.Fatalf("no file %s in:\n%s", path, cfg)
	}
	lines := strings.Split(cfg[i:], "\n")
	var out []string
	in := false
	for _, l := range lines[1:] {
		if !in {
			in = strings.HasPrefix(l, "          content: |")
			continue
		}
		if !strings.HasPrefix(l, "            ") {
			break
		}
		out = append(out, l[12:])
	}
	return strings.Join(out, "\n") + "\n"
}

func TestLocaleInCloudConfig(t *testing.T) {
	without := RenderCloudConfig(localeChoice(""), "$6$abc$def")
	for _, no := range []string{"locale.conf", "91_isotope_locale", "LANG="} {
		if strings.Contains(without, no) {
			t.Errorf("no Locale, but the config has %q", no)
		}
	}
	with := RenderCloudConfig(localeChoice("de_DE.UTF-8"), "$6$abc$def")
	if got := fileContent(t, with, "/etc/locale.conf"); got != "LANG=de_DE.UTF-8\n" {
		t.Errorf("/etc/locale.conf = %q", got)
	}
	if !strings.Contains(with, "path: /oem/91_isotope_locale.yaml") {
		t.Error("no /oem/91_isotope_locale.yaml")
	}
	// A name that could break out of the YAML is dropped, not written.
	for _, bad := range []string{"de_DE\nfoo: bar", "x y", "$(reboot)", strings.Repeat("a", 65)} {
		if out := RenderCloudConfig(localeChoice(bad), "$6$abc$def"); out != without {
			t.Errorf("Locale %q changed the config", bad)
		}
	}
}

// The text installer passes no locale: its config must not change by a byte.
func TestNoLocaleCloudConfigIsUnchanged(t *testing.T) {
	want, err := os.ReadFile("testdata/cloudconfig-no-locale.golden")
	if err != nil {
		t.Fatal(err)
	}
	if got := RenderCloudConfig(localeChoice(""), "$6$abc$def"); got != string(want) {
		t.Errorf("config differs from the golden file:\n%s", got)
	}
}

// isotope-updated's SetLocale (cmd/isotope/settings_bus.go, oemLocale) writes
// the same /oem file. update cannot be imported here, so both sides are
// tested against one golden file: update's TestOemLocaleMatchesTheInstaller.
func TestLocaleStageMatchesSetLocale(t *testing.T) {
	golden, err := os.ReadFile("testdata/oem-locale-de_DE.golden")
	if err != nil {
		t.Fatal(err)
	}
	cfg := RenderCloudConfig(localeChoice("de_DE.UTF-8"), "x")
	if got := fileContent(t, cfg, "/oem/91_isotope_locale.yaml"); got != string(golden) {
		t.Errorf("installer's /oem file differs from SetLocale's:\n%s\nwant:\n%s", got, golden)
	}
	if OEMLocaleYAML("de_DE.UTF-8") != string(golden) {
		t.Error("OEMLocaleYAML differs from the golden file")
	}
}

// The installer's config runs at every boot. Every write of /etc/locale.conf
// or /oem/91_isotope_locale.yaml must be guarded by the existence of that
// /oem file, so a later SetLocale is not undone at the next boot.
func TestLanguageWritesAreGuardedByTheOEMFile(t *testing.T) {
	cfg := RenderCloudConfig(localeChoice("de_DE.UTF-8"), "$6$abc$def")
	guard := "if: '[ ! -e /oem/91_isotope_locale.yaml ]'"
	var entries []string
	for _, part := range strings.Split(cfg, "\n    - name: ")[1:] {
		entries = append(entries, part)
	}
	seen := map[string]bool{}
	for _, e := range entries {
		for _, path := range []string{"/etc/locale.conf", "/oem/91_isotope_locale.yaml"} {
			if !strings.Contains(e, "- path: "+path+"\n") {
				continue
			}
			seen[path] = true
			if !strings.Contains(e, "\n      "+guard+"\n") {
				t.Errorf("a stage writes %s with no guard:\n%s", path, e)
			}
		}
	}
	if len(seen) != 2 {
		t.Errorf("expected both files written, got %v", seen)
	}
	// /etc/locale.conf is for the first session (initramfs); /oem is
	// written from the boot stage, where /oem is mounted read-write.
	boot := strings.Index(cfg, "\n  boot:\n")
	if i := strings.Index(cfg, "- path: /etc/locale.conf"); i < 0 || i > boot {
		t.Error("/etc/locale.conf must be written in the initramfs stage")
	}
	if i := strings.Index(cfg, "- path: /oem/91_isotope_locale.yaml"); i < boot {
		t.Error("/oem/91_isotope_locale.yaml must be written in the boot stage")
	}
}

func TestLocaleConfMatchesTheGolden(t *testing.T) {
	golden, err := os.ReadFile("testdata/locale.conf-de_DE.golden")
	if err != nil {
		t.Fatal(err)
	}
	cfg := RenderCloudConfig(localeChoice("de_DE.UTF-8"), "x")
	if got := fileContent(t, cfg, "/etc/locale.conf"); got != string(golden) {
		t.Errorf("/etc/locale.conf = %q, want %q", got, golden)
	}
}
