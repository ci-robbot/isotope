package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kairos-community/isotope/pkg/installer"
)

// stubGUI points cage and the GUI at scripts in a temporary directory and
// the markers at another; it returns the cage script's path.
func stubGUI(t *testing.T, cage, cmdline string) string {
	t.Helper()
	dir := t.TempDir()
	run := filepath.Join(dir, "run")
	t.Setenv("ISOTOPE_INSTALL_RUN_DIR", run)
	oldCage, oldGUI, oldCmd, oldProbe := cagePath, guiPath, cmdlinePath, probePath
	t.Cleanup(func() { cagePath, guiPath, cmdlinePath, probePath = oldCage, oldGUI, oldCmd, oldProbe })
	cagePath = filepath.Join(dir, "cage")
	guiPath = filepath.Join(dir, "isotope-install-gui")
	cmdlinePath = filepath.Join(dir, "cmdline")
	// No probe unless a test writes one: the machine's own GPU must not
	// decide a test.
	probePath = filepath.Join(dir, "isotope-gpu-probe")
	for p, body := range map[string]string{cagePath: cage, guiPath: "exit 0"} {
		if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(cmdlinePath, []byte(cmdline), 0o644); err != nil {
		t.Fatal(err)
	}
	return cagePath
}

func TestGUIWanted(t *testing.T) {
	stubGUI(t, "exit 0", "quiet splash install-mode-interactive\n")
	if !guiWanted() {
		t.Fatal("not wanted with cage and the GUI present")
	}
	if err := os.WriteFile(cmdlinePath, []byte("quiet isotope.installer=text splash\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if guiWanted() {
		t.Fatal("wanted with isotope.installer=text")
	}
	stubGUI(t, "exit 0", "")
	os.Remove(cagePath)
	if guiWanted() {
		t.Fatal("wanted without cage (the server)")
	}
}

func TestRunGUIFallsBackBeforeTheFirstFrame(t *testing.T) {
	stubGUI(t, "exit 1", "")
	fallback, note, err := runGUI()
	if err != nil || !fallback || !strings.Contains(note, "could not start") {
		t.Fatalf("fallback %v, note %q, err %v", fallback, note, err)
	}
}

func TestRunGUIDone(t *testing.T) {
	stubGUI(t, `touch "$ISOTOPE_INSTALL_RUN_DIR/gui-started"; exit 0`, "")
	fallback, _, err := runGUI()
	if err != nil || fallback {
		t.Fatalf("fallback %v, err %v", fallback, err)
	}
}

// The GUI asks for a relaunch with a new layout: cage starts again with it
// as its keymap.
func TestRunGUIRelaunchesWithTheLayout(t *testing.T) {
	stubGUI(t, `
r="$ISOTOPE_INSTALL_RUN_DIR"
touch "$r/gui-started"
if [ ! -f "$r/second" ]; then
	touch "$r/second"
	echo '{"next":"account","layout":"it"}' > "$r/state.json"
	exit 75
fi
echo "$XKB_DEFAULT_LAYOUT $ISOTOPE_INSTALL_GUI_RELAUNCH $LIBSEAT_BACKEND" > "$r/env"
exit 0`, "")
	fallback, _, err := runGUI()
	if err != nil || fallback {
		t.Fatalf("fallback %v, err %v", fallback, err)
	}
	b, err := os.ReadFile(filepath.Join(installer.RunDir(), "env"))
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(b)); got != "it 1 builtin" {
		t.Fatalf("second cage's environment: %q", got)
	}
}

func TestRunGUICrashAfterStartShowsText(t *testing.T) {
	stubGUI(t, `touch "$ISOTOPE_INSTALL_RUN_DIR/gui-started"; exit 2`, "")
	fallback, note, err := runGUI()
	if err != nil || !fallback || !strings.Contains(note, "stopped") {
		t.Fatalf("fallback %v, note %q, err %v", fallback, note, err)
	}
}

func TestRunGUICrashDuringTheInstallStartsNoSecond(t *testing.T) {
	stubGUI(t, `r="$ISOTOPE_INSTALL_RUN_DIR"; touch "$r/gui-started" "$r/install-running"; exit 2`, "")
	fallback, _, err := runGUI()
	if err != errInstallRunning || fallback {
		t.Fatalf("fallback %v, err %v", fallback, err)
	}
}

func TestGUIStateHasNoSecrets(t *testing.T) {
	p := filepath.Join(t.TempDir(), "state.json")
	if err := installer.WriteGUIState(p, installer.GUIState{Next: "account", Layout: "de", Variant: "nodeadkeys", Username: "alex"}); err != nil {
		t.Fatal(err)
	}
	s, err := installer.ReadGUIState(p)
	if err != nil || s.Layout != "de" || s.Variant != "nodeadkeys" || s.Username != "alex" {
		t.Fatalf("%+v, %v", s, err)
	}
	b, _ := os.ReadFile(p)
	for _, w := range []string{"pass", "key_value", "recovery_key_value"} {
		if strings.Contains(string(b), w) {
			t.Fatalf("state has %q: %s", w, b)
		}
	}
	if err := os.WriteFile(p, []byte(`{"layout":"de;rm -rf /"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if s, _ := installer.ReadGUIState(p); s.Layout != "" {
		t.Fatalf("an invalid layout survived: %q", s.Layout)
	}
}

func TestRunGUIRescueShell(t *testing.T) {
	stubGUI(t, `touch "$ISOTOPE_INSTALL_RUN_DIR/gui-started"; exit 76`, "")
	fallback, _, err := runGUI()
	if err != errRescueShell || fallback {
		t.Fatalf("fallback %v, err %v", fallback, err)
	}
}

// The live ISO has no GPU driver on a VM with no 3D (and none with
// nomodeset): wlroots must be allowed llvmpipe, and Gio needs the X11 cursor
// names the installer's theme provides. Either missing stops cage or the GUI
// before the first frame. A value already set wins.
func TestGUIEnvRendersWithoutAGPU(t *testing.T) {
	t.Setenv("WLR_RENDERER_ALLOW_SOFTWARE", "")
	t.Setenv("XCURSOR_THEME", "")
	env := guiEnv(t.TempDir(), false, "")
	for _, want := range []string{"WLR_RENDERER_ALLOW_SOFTWARE=1", "XCURSOR_THEME=isotope-installer"} {
		found := false
		for _, e := range env {
			found = found || e == want
		}
		if !found {
			t.Errorf("guiEnv lacks %s", want)
		}
	}
	t.Setenv("XCURSOR_THEME", "Bibata")
	for _, e := range guiEnv(t.TempDir(), false, "") {
		if strings.HasPrefix(e, "XCURSOR_THEME=") && e != "XCURSOR_THEME=Bibata" {
			t.Errorf("guiEnv overrode a set XCURSOR_THEME: %s", e)
		}
	}
}

// softwareCage is a cage that records, for each run, the rendering variables
// it got, and fails before the first frame (no gui-started) unless it runs
// in software and okInSoftware is set: a GPU whose Mesa driver is missing.
func softwareCage(okInSoftware bool) string {
	ok := "exit 1"
	if okInSoftware {
		ok = `touch "$r/gui-started"; exit 0`
	}
	return `
r="$ISOTOPE_INSTALL_RUN_DIR"
echo "${WLR_RENDERER:-} ${LIBGL_ALWAYS_SOFTWARE:-} ${ISOTOPE_SOFTWARE_RENDERING:-}" >> "$r/runs"
if [ "${LIBGL_ALWAYS_SOFTWARE:-}" = 1 ]; then
	` + ok + `
fi
exit 1`
}

func cageRuns(t *testing.T) []string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(installer.RunDir(), "runs"))
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimRight(string(b), "\n"), "\n")
}

// cage stops before the first frame (no GL driver for the GPU): it starts
// once more in software, and the GUI comes up there.
func TestRunGUIRetriesInSoftware(t *testing.T) {
	stubGUI(t, softwareCage(true), "")
	fallback, _, err := runGUI()
	if err != nil || fallback {
		t.Fatalf("fallback %v, err %v", fallback, err)
	}
	runs := cageRuns(t)
	if len(runs) != 2 {
		t.Fatalf("cage ran %d times, want 2: %q", len(runs), runs)
	}
	if strings.TrimSpace(runs[0]) != "" {
		t.Errorf("first cage already in software: %q", runs[0])
	}
	if runs[1] != "pixman 1 1" {
		t.Errorf("second cage's environment: %q, want WLR_RENDERER=pixman LIBGL_ALWAYS_SOFTWARE=1 ISOTOPE_SOFTWARE_RENDERING=1", runs[1])
	}
}

// Software rendering fails too: the text installer, after exactly one
// retry.
func TestRunGUISoftwareFailsTooShowsText(t *testing.T) {
	stubGUI(t, softwareCage(false), "")
	fallback, note, err := runGUI()
	if err != nil || !fallback || !strings.Contains(note, "could not start") {
		t.Fatalf("fallback %v, note %q, err %v", fallback, note, err)
	}
	if runs := cageRuns(t); len(runs) != 2 {
		t.Fatalf("cage ran %d times, want 2: %q", len(runs), runs)
	}
}

// A keyboard-layout relaunch after the software retry stays in software.
func TestRunGUISoftwareKeepsRelaunches(t *testing.T) {
	stubGUI(t, `
r="$ISOTOPE_INSTALL_RUN_DIR"
echo "${LIBGL_ALWAYS_SOFTWARE:-}" >> "$r/runs"
[ "${LIBGL_ALWAYS_SOFTWARE:-}" = 1 ] || exit 1
touch "$r/gui-started"
if [ ! -f "$r/relaunched" ]; then
	touch "$r/relaunched"
	echo '{"next":"account","layout":"it"}' > "$r/state.json"
	exit 75
fi
exit 0`, "")
	fallback, _, err := runGUI()
	if err != nil || fallback {
		t.Fatalf("fallback %v, err %v", fallback, err)
	}
	if got := strings.Join(cageRuns(t), ","); got != ",1,1" {
		t.Fatalf("LIBGL_ALWAYS_SOFTWARE per cage run: %q, want \",1,1\"", got)
	}
}

// isotope-gpu-probe says the GPU has no working driver (exit 1): cage
// starts in software at once, with no failed first try.
func TestRunGUIStartsInSoftwareWhenTheProbeSaysSo(t *testing.T) {
	stubGUI(t, softwareCage(true), "")
	if err := os.WriteFile(probePath, []byte("#!/bin/sh\necho 'hardware radeon: no Mesa driver for this GPU (EGL failed)'\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	fallback, _, err := runGUI()
	if err != nil || fallback {
		t.Fatalf("fallback %v, err %v", fallback, err)
	}
	if runs := cageRuns(t); len(runs) != 1 || runs[0] != "pixman 1 1" {
		t.Fatalf("cage runs: %q, want one in software", runs)
	}
}

// A probe that finds a working driver (exit 0), or no DRM device (exit 2),
// leaves the first cage on the GPU; exit 1 gives the probe's class.
func TestGPUNeedsSoftware(t *testing.T) {
	stubGUI(t, "exit 0", "")
	if soft, _ := gpuNeedsSoftware(); soft {
		t.Fatal("software without a probe")
	}
	for _, c := range []struct {
		code  int
		line  string
		soft  bool
		class string
	}{
		{0, "AMD CEDAR", false, ""},
		{2, "no DRM device", false, ""},
		{1, "hardware radeon: no Mesa driver for this GPU (EGL failed)", true, gpuHardware},
		{1, "virtual virtio_gpu: software (llvmpipe)", true, gpuVirtual},
		{1, "firmware simple-framebuffer: software (llvmpipe)", true, gpuFirmware},
		{1, "", true, gpuHardware},
	} {
		body := fmt.Sprintf("#!/bin/sh\necho '%s'\nexit %d\n", c.line, c.code)
		if err := os.WriteFile(probePath, []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
		if soft, class := gpuNeedsSoftware(); soft != c.soft || class != c.class {
			t.Errorf("probe %d %q: %v %q, want %v %q", c.code, c.line, soft, class, c.soft, c.class)
		}
	}
}

// A VM without 3D: cage starts in software at once, and the GUI gets no
// warning.
func TestRunGUIVirtualGPUHasNoWarning(t *testing.T) {
	stubGUI(t, softwareCage(true), "")
	if err := os.WriteFile(probePath, []byte("#!/bin/sh\necho 'virtual bochs-drm: software (llvmpipe)'\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	fallback, _, err := runGUI()
	if err != nil || fallback {
		t.Fatalf("fallback %v, err %v", fallback, err)
	}
	if runs := cageRuns(t); len(runs) != 1 || runs[0] != "pixman 1 " {
		t.Fatalf("cage runs: %q, want one in software with no warning", runs)
	}
}

func TestGUIEnvSoftware(t *testing.T) {
	t.Setenv("LIBGL_ALWAYS_SOFTWARE", "")
	has := func(env []string, want string) bool {
		for _, e := range env {
			if e == want {
				return true
			}
		}
		return false
	}
	soft := guiEnv(t.TempDir(), true, gpuHardware)
	for _, want := range []string{"WLR_RENDERER=pixman", "LIBGL_ALWAYS_SOFTWARE=1", "GBM_ALWAYS_SOFTWARE=1", "ISOTOPE_SOFTWARE_RENDERING=1", "ISOTOPE_GPU_CLASS=hardware"} {
		if !has(soft, want) {
			t.Errorf("software guiEnv lacks %s", want)
		}
	}
	hard := guiEnv(t.TempDir(), false, "")
	for _, unwanted := range []string{"WLR_RENDERER=pixman", "LIBGL_ALWAYS_SOFTWARE=1", "GBM_ALWAYS_SOFTWARE=1", "ISOTOPE_SOFTWARE_RENDERING=1"} {
		if has(hard, unwanted) {
			t.Errorf("hardware guiEnv has %s", unwanted)
		}
	}
	// A virtual GPU without 3D: software, but no warning in the GUI.
	virt := guiEnv(t.TempDir(), true, gpuVirtual)
	if !has(virt, "WLR_RENDERER=pixman") || !has(virt, "GBM_ALWAYS_SOFTWARE=1") {
		t.Error("virtual guiEnv is not in software")
	}
	for _, e := range virt {
		if strings.HasPrefix(e, "ISOTOPE_SOFTWARE_RENDERING=") || strings.HasPrefix(e, "ISOTOPE_GPU_CLASS=") {
			t.Errorf("virtual guiEnv warns: %s", e)
		}
	}
}
