package main

import (
	"image"
	"image/png"
	"os"
	"path/filepath"
	"testing"
	"time"

	"gioui.org/gpu/headless"
	"gioui.org/io/input"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"
)

// TestPreview renders every page to ISOTOPE_SYSTEM_PREVIEW/<page>.png,
// to look at a change (it compares nothing). It needs an EGL with a
// surfaceless platform (Mesa) and is skipped without the variable.
func TestPreview(t *testing.T) {
	dir := os.Getenv("ISOTOPE_SYSTEM_PREVIEW")
	if dir == "" {
		t.Skip("ISOTOPE_SYSTEM_PREVIEW is not set")
	}
	if os.Getenv("EGL_PLATFORM") == "" {
		os.Setenv("EGL_PLATFORM", "surfaceless")
	}
	u, d, _ := testUI(t, true)
	d.mu.Lock()
	d.ext = []Extension{{Name: "tailscale", Enabled: true}}
	d.cat = []CatalogEntry{
		{Name: "tailscale", Title: "Tailscale", Description: "A mesh VPN that just works", Latest: "1.78.1", Installed: true, Enabled: true},
		{Name: "gpg", Title: "GnuPG", Description: "Encryption and signing tools", Latest: "2.4.7"},
		{Name: "android-tools", Title: "Android tools", Description: "adb and fastboot", Latest: "35.0.2"},
	}
	d.backup = BackupStatus{Available: true, Target: "/run/media/anna/USB/isotope-backup", Schedule: "weekly",
		Last: time.Now().Add(-26 * time.Hour).Unix(), Result: "ok", Present: true}
	d.mu.Unlock()
	sz := image.Pt(1100, 720)
	for _, p := range Pages {
		u.show(p)
		time.Sleep(50 * time.Millisecond)
		u.pageAt = time.Time{}
		shot(t, u, sz, filepath.Join(dir, string(p)+".png"))
	}
	u.show(PageUpdates)
	u.startInstall("Isotope v1.1.0", "quay.io/x/isotope:sway-v1.1.0", false, "")
	waitQuestion(t, u, "Install")
	shot(t, u, sz, filepath.Join(dir, "updates-question.png"))
}

func shot(t *testing.T, u *UI, sz image.Point, path string) {
	t.Helper()
	win, err := headless.NewWindow(sz.X, sz.Y)
	if err != nil {
		t.Skipf("no headless GPU: %v", err)
	}
	defer win.Release()
	var r input.Router
	var ops op.Ops
	for i := 0; i < 2; i++ {
		ops.Reset()
		gtx := layout.Context{Ops: &ops, Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1},
			Constraints: layout.Exact(sz), Now: time.Now(), Source: r.Source()}
		u.Layout(gtx)
		r.Frame(&ops)
	}
	if err := win.Frame(&ops); err != nil {
		t.Fatal(err)
	}
	img := image.NewRGBA(image.Rectangle{Max: sz})
	if err := win.Screenshot(img); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		t.Fatal(err)
	}
}
