package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/kairos-community/isotope/pkg/installer"
)

// The graphical installer. isotope-install-ui stays the program the Kairos
// installer services run (90_desktop_installer.yaml); on a desktop ISO it
// starts install-gui inside cage and keeps the text installer as the
// fallback. See docs/superpowers/specs/2026-11-17-graphical-installer-design.md,
// "Starting".

// The paths are variables so the tests can point them at stubs.
var (
	cagePath    = "/usr/bin/cage"
	guiPath     = "/usr/bin/isotope-install-gui"
	cmdlinePath = "/proc/cmdline"
	probePath   = "/usr/bin/isotope-gpu-probe"
)

// textOnlyArg on the kernel command line keeps the text installer; the live
// ISO's "isotope (text installer)" entry adds it.
const textOnlyArg = "isotope.installer=text"

// maxRelaunches bounds the keyboard-layout relaunches, so a GUI that asks for
// one at every start cannot keep cage flashing for ever.
const maxRelaunches = 50

// guiWanted says whether to start the graphical installer: cage and the GUI
// are in the image (the desktop editions; the server has neither) and the
// boot did not ask for text.
func guiWanted() bool {
	for _, p := range []string{cagePath, guiPath} {
		st, err := os.Stat(p)
		if err != nil || st.IsDir() || st.Mode()&0o111 == 0 {
			return false
		}
	}
	b, err := os.ReadFile(cmdlinePath)
	if err != nil {
		return true
	}
	for _, f := range strings.Fields(string(b)) {
		if f == textOnlyArg {
			return false
		}
	}
	return true
}

// unattended says whether the install needs no questions: the paths that
// keep the text progress screen.
func unattended() (bool, error) {
	if os.Getenv("ISOTOPE_INSTALL_NONINTERACTIVE") == "1" {
		return true, nil
	}
	return installer.ConfigPresent(installer.OEMDir(), installer.ConfigOut())
}

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// errInstallRunning stops the text installer from starting a second install
// over one the graphical installer left running.
var errInstallRunning = errors.New("the graphical installer stopped while the install was running; restart the computer and install again")

// errRescueShell is the GUI's "Open a terminal" on an image without a
// terminal for cage: the text installer's rescue shell takes tty1.
var errRescueShell = errors.New("rescue shell")

// The GPU classes isotope-gpu-probe names on exit 1, the first word of its
// line: a real GPU whose Mesa driver is missing or fails, a virtual
// machine's GPU without 3D (software is normal there), or only the
// firmware's framebuffer on real hardware (nomodeset, or the kernel driver
// not loaded yet).
const (
	gpuHardware = "hardware"
	gpuVirtual  = "virtual"
	gpuFirmware = "firmware"
)

// gpuNeedsSoftware runs isotope-gpu-probe, which makes a GBM device and an
// EGL context on the primary GPU as a compositor does. Exit status 1 means
// Mesa has no working hardware driver for it (an old Radeon without r600,
// any real GPU on the vm image line, a driver that crashes, a VM without
// 3D): Mesa does not fall back to software by itself then, and cage exits
// before the first frame. It returns the probe's class with it. No probe
// (an older image) or another answer means try the GPU.
func gpuNeedsSoftware() (bool, string) {
	if _, err := os.Stat(probePath); err != nil {
		return false, ""
	}
	out, err := exec.Command(probePath).Output()
	var ee *exec.ExitError
	if !errors.As(err, &ee) || ee.ExitCode() != 1 {
		return false, ""
	}
	class, _, _ := strings.Cut(strings.TrimSpace(string(out)), " ")
	switch class {
	case gpuVirtual, gpuFirmware:
		return true, class
	}
	return true, gpuHardware
}

// runGUI runs `cage -s -- isotope-install-gui` until the GUI is done. It
// starts cage again, with the saved layout as its keymap, each time the GUI
// asks for that (installer.GUIRelaunchExit). When cage stops before the GUI's
// first frame, it starts it once more in software rendering (see guiEnv),
// and only then gives up; it starts in software at once when
// isotope-gpu-probe says the GPU has no working driver. That retry is not a
// relaunch and does not count against maxRelaunches. It reports fallback
// when the text installer should take over, with a line to show above it.
func runGUI() (fallback bool, note string, err error) {
	run := installer.RunDir()
	if err := os.MkdirAll(run, 0o700); err != nil {
		return true, "", nil
	}
	_ = os.Remove(installer.GUIStatePath())
	software, class := gpuNeedsSoftware()
	for i := 0; ; i++ {
		_ = os.Remove(installer.GUIStartedPath())
		cmd := exec.Command(cagePath, "-s", "--", guiPath)
		cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
		cmd.Env = guiEnv(run, software, class)
		werr := cmd.Run()
		code := 0
		var ee *exec.ExitError
		switch {
		case werr == nil:
			// The GUI ran the install; the machine restarts from it.
			return false, "", nil
		case errors.As(werr, &ee):
			code = ee.ExitCode()
		default:
			// cage could not be run at all.
			return true, "The graphical installer could not start. This is the text installer.", nil
		}
		if code == installer.GUIRelaunchExit && exists(installer.GUIStatePath()) && i < maxRelaunches {
			continue
		}
		if code == installer.GUIShellExit && exists(installer.GUIStartedPath()) {
			return false, "", errRescueShell
		}
		if !exists(installer.GUIStartedPath()) && !software {
			// No GL driver that works for this GPU: try once more
			// with no hardware driver at all. Nothing was asked yet,
			// so the GUI starts at its first page again.
			software, class = true, gpuHardware
			_ = os.Remove(installer.GUIStatePath())
			i--
			continue
		}
		if !exists(installer.GUIStartedPath()) {
			// No DRM device, no seat, no GL even in software:
			// nothing was asked yet.
			return true, "The graphical installer could not start. This is the text installer.", nil
		}
		if exists(installer.InstallRunningPath()) {
			return false, "", errInstallRunning
		}
		_ = os.Remove(installer.GUIStatePath())
		return true, fmt.Sprintf("The graphical installer stopped (exit status %d). This is the text installer: start again from the first question.", code), nil
	}
}

// guiEnv is cage's environment: the keymap the GUI asked for, a runtime
// directory for the Wayland socket (a system service has none), libseat's
// builtin backend (the service runs as root on tty1 with no logind
// session), software rendering allowed, the installer's cursor theme, and
// the flag that lets the GUI ask for a relaunch.
//
// With software, nothing loads a hardware GL driver: cage draws with
// wlroots' pixman renderer (WLR_RENDERER=pixman), which needs no EGL or GBM
// at all, so even a GPU whose Mesa driver crashes or is missing cannot stop
// it; GLES2 on llvmpipe would still go through Mesa's GBM and EGL loader.
// The GUI, a Gio client that needs EGL, gets llvmpipe through
// LIBGL_ALWAYS_SOFTWARE=1 (and GBM_ALWAYS_SOFTWARE=1, so nothing in it loads
// the hardware driver through GBM either) and hands cage shared-memory
// buffers, which pixman composites. ISOTOPE_SOFTWARE_RENDERING=1 makes the
// GUI say so on its first page, and ISOTOPE_GPU_CLASS says why; not on a
// virtual GPU (class virtual), where software is normal and nothing is
// missing.
func guiEnv(run string, software bool, class string) []string {
	env := os.Environ()
	set := func(k, v string) {
		for i, e := range env {
			if strings.HasPrefix(e, k+"=") {
				env[i] = k + "=" + v
				return
			}
		}
		env = append(env, k+"="+v)
	}
	if os.Getenv("XDG_RUNTIME_DIR") == "" {
		dir := filepath.Join(run, "xdg")
		_ = os.MkdirAll(dir, 0o700)
		set("XDG_RUNTIME_DIR", dir)
	}
	if os.Getenv("LIBSEAT_BACKEND") == "" {
		set("LIBSEAT_BACKEND", "builtin")
	}
	// A machine with no GPU driver Mesa knows (a VM with no 3D, nomodeset)
	// has only llvmpipe, and wlroots refuses a software renderer unless told
	// it may: cage then exits before the GUI starts.
	if os.Getenv("WLR_RENDERER_ALLOW_SOFTWARE") == "" {
		set("WLR_RENDERER_ALLOW_SOFTWARE", "1")
	}
	// Adwaita under the X11 names Gio asks for; see
	// rootfs/common/usr/share/icons/isotope-installer/index.theme.
	if os.Getenv("XCURSOR_THEME") == "" {
		set("XCURSOR_THEME", "isotope-installer")
	}
	if software {
		set("WLR_RENDERER", "pixman")
		set("LIBGL_ALWAYS_SOFTWARE", "1")
		set("GBM_ALWAYS_SOFTWARE", "1")
		if class != gpuVirtual {
			set("ISOTOPE_SOFTWARE_RENDERING", "1")
			set("ISOTOPE_GPU_CLASS", class)
		}
	}
	set("ISOTOPE_INSTALL_GUI_RELAUNCH", "1")
	if s, err := installer.ReadGUIState(installer.GUIStatePath()); err == nil && s.Layout != "" {
		set("XKB_DEFAULT_LAYOUT", s.Layout)
		set("XKB_DEFAULT_VARIANT", s.Variant)
	}
	return env
}
