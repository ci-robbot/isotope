package main

import (
	"os"
	"path/filepath"
	"testing"
)

// fakeGraphics points the About page's probe and marker at a temporary
// directory; it returns the marker's path.
func fakeGraphics(t *testing.T, probe string) string {
	t.Helper()
	dir := t.TempDir()
	oldProbe, oldMarker := gpuProbePath, softwareMarker
	t.Cleanup(func() { gpuProbePath, softwareMarker = oldProbe, oldMarker })
	gpuProbePath = filepath.Join(dir, "isotope-gpu-probe")
	marker := filepath.Join(dir, "software-rendering")
	softwareMarker = func() string { return marker }
	t.Setenv("XDG_RUNTIME_DIR", dir)
	t.Setenv("ISOTOPE_SOFTWARE_RENDERING", "")
	if probe != "" {
		if err := os.WriteFile(gpuProbePath, []byte("#!/bin/sh\n"+probe+"\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return marker
}

func TestGraphicsLine(t *testing.T) {
	marker := fakeGraphics(t, "echo 'AMD CEDAR (DRM 2.50.0)'; exit 0")
	if got := graphicsLine(false); got != "AMD CEDAR (DRM 2.50.0)" {
		t.Errorf("a working GPU: %q", got)
	}
	if err := os.WriteFile(marker, []byte("hardware radeon: no Mesa driver for this GPU (EGL failed)\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := graphicsLine(false); got != softwareGraphics {
		t.Errorf("with the software marker: %q", got)
	}

	fakeGraphics(t, "echo 'radeon: no Mesa driver'; exit 1")
	t.Setenv("ISOTOPE_SOFTWARE_RENDERING", "1")
	if got := graphicsLine(false); got != softwareGraphics {
		t.Errorf("with ISOTOPE_SOFTWARE_RENDERING=1: %q", got)
	}
	t.Setenv("ISOTOPE_SOFTWARE_RENDERING", "")
	// The probe's own "no" without the session's fallback (an older
	// session, a GPU that broke since): the row is left out, not guessed.
	if got := graphicsLine(false); got != "" {
		t.Errorf("probe exit 1 without the marker: %q", got)
	}

	fakeGraphics(t, "")
	if got := graphicsLine(false); got != "" {
		t.Errorf("no probe: %q", got)
	}
}

// The software row says why, from the marker's class and the image line.
func TestGraphicsLineSaysWhy(t *testing.T) {
	marker := fakeGraphics(t, "exit 1")
	for _, c := range []struct {
		line    string
		vmImage bool
		want    string
	}{
		{"hardware radeon: no Mesa driver for this GPU (EGL failed)", false, softwareGraphics},
		{"hardware i915: no Mesa driver for this GPU (EGL failed)", true, softwareGraphicsVMImage},
		{"virtual virtio_gpu: software (llvmpipe)", true, softwareGraphicsVirtual},
		{"virtual bochs-drm: software (llvmpipe)", false, softwareGraphicsVirtual},
		{"firmware simple-framebuffer: software (llvmpipe)", false, softwareGraphicsFirmware},
	} {
		if err := os.WriteFile(marker, []byte(c.line+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if got := graphicsLine(c.vmImage); got != c.want {
			t.Errorf("%q (vm image %v): %q, want %q", c.line, c.vmImage, got, c.want)
		}
	}
}

// The About page shows the row once it is read, and draws without it.
func TestAboutShowsGraphics(t *testing.T) {
	fakeGraphics(t, "echo 'Mesa Intel(R) HD Graphics 4000 (IVB GT2)'; exit 0")
	u, _, _ := testUI(t, true)
	u.show(PageAbout)
	p := u.view(PageAbout).(*aboutPage)
	eventually(t, "the Graphics row", func() bool {
		u.mu.Lock()
		defer u.mu.Unlock()
		return p.graphics == "Mesa Intel(R) HD Graphics 4000 (IVB GT2)"
	})
	u.Layout(gtxOf(1100, 720))
}
