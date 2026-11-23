package main

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/kairos-community/isotope/pkg/guikit"
)

// Scales are the display scales the Accessibility page offers.
var Scales = []float64{1, 1.25, 1.5, 1.75, 2}

// NearestScale is the offered scale closest to s, for showing what is set.
func NearestScale(s float64) float64 {
	best := Scales[0]
	for _, c := range Scales {
		if math.Abs(c-s) < math.Abs(best-s) {
			best = c
		}
	}
	return best
}

func run(r Runner, name string, args ...string) ([]byte, error) {
	return runFor(5*time.Second, r, name, args...)
}

// slow is the time a command that changes the screens gets: `i3-msg
// restart` answers only when i3 is back, and `cosmic-randr mode` waits for
// the compositor to apply the mode.
const slow = 15 * time.Second

func runFor(d time.Duration, r Runner, name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()
	out, err := r.Run(ctx, name, args...)
	if err != nil {
		return out, fmt.Errorf("%s: %w", name, err)
	}
	return out, nil
}

func fmtScale(s float64) string { return strconv.FormatFloat(s, 'f', -1, 64) }

// ApplyScale keeps the display scale s for the next login, then sets it now
// on every output (written first, so a failed live change still persists):
//
//   - sway: `swaymsg output * scale` now; `output * scale` in
//     ~/.config/sway/isotope-scale.conf, which /etc/sway/config includes (so
//     outputs plugged in later get it too).
//   - niri: `niri msg output <name> scale` for each output now; an `output`
//     block per output in ~/.config/niri/isotope-scale.kdl, which
//     /etc/niri/config.kdl includes (niri also reloads it live). niri has no
//     wildcard output, so a monitor plugged in later gets niri's own scale.
//   - i3: Xft.dpi (96 × s) in ~/.Xresources, merged now with `xrdb -merge`
//     and at login by ly's setup.sh; then `i3-msg restart`, so i3's bar and
//     titles, which read Xft.dpi only when i3 starts, follow at once.
//   - COSMIC: `cosmic-randr mode --scale` for each enabled output, as COSMIC
//     Settings' display page does; cosmic-comp keeps it in
//     ~/.local/state/cosmic-comp/outputs.ron itself.
func ApplyScale(r Runner, home string, d Desktop, s float64) error {
	switch d {
	case Sway:
		if err := writeFile(filepath.Join(home, ".config", "sway", "isotope-scale.conf"),
			"# The display scale, set by Isotope Welcome (Accessibility).\n"+
				"# /etc/sway/config includes this file.\n"+
				"output * scale "+fmtScale(s)+"\n"); err != nil {
			return err
		}
		_, err := run(r, "swaymsg", "output", "*", "scale", fmtScale(s))
		return err
	case Niri:
		names, err := niriOutputNames(r)
		if err != nil {
			return err
		}
		if err := writeNiriScale(filepath.Join(home, ".config", "niri", "isotope-scale.kdl"), names, s); err != nil {
			return err
		}
		for _, n := range names {
			if _, err := run(r, "niri", "msg", "output", n, "scale", fmtScale(s)); err != nil {
				return err
			}
		}
		return nil
	case I3:
		xres := filepath.Join(home, ".Xresources")
		if err := setXresource(xres, "Xft.dpi", strconv.Itoa(int(math.Round(96*s)))); err != nil {
			return err
		}
		if _, err := run(r, "xrdb", "-merge", xres); err != nil {
			return err
		}
		_, err := runFor(slow, r, "i3-msg", "restart")
		return err
	case Cosmic:
		outs, err := cosmicOutputs(r)
		if err != nil {
			return err
		}
		for _, o := range outs {
			if !o.Enabled || o.Mirroring || o.Width == 0 {
				continue
			}
			if _, err := runFor(slow, r, "cosmic-randr", "mode",
				"--scale", fmt.Sprintf("%.2f", s),
				"--refresh", fmt.Sprintf("%d.%03d", o.Refresh/1000, o.Refresh%1000),
				o.Name, strconv.Itoa(o.Width), strconv.Itoa(o.Height)); err != nil {
				return err
			}
		}
		return nil
	}
	return fmt.Errorf("no display scale on %q", d)
}

// CurrentScale is the scale of the first active output, 1 when it cannot be
// read.
func CurrentScale(r Runner, d Desktop) float64 {
	if r == nil {
		return 1
	}
	switch d {
	case Sway:
		out, err := run(r, "swaymsg", "-t", "get_outputs", "-r")
		if err != nil {
			return 1
		}
		var outs []struct {
			Active bool    `json:"active"`
			Scale  float64 `json:"scale"`
		}
		if json.Unmarshal(out, &outs) == nil {
			for _, o := range outs {
				if o.Active && o.Scale > 0 {
					return o.Scale
				}
			}
		}
	case Niri:
		outs, err := niriOutputs(r)
		if err != nil {
			return 1
		}
		for _, n := range sortedKeys(outs) {
			if l := outs[n].Logical; l != nil && l.Scale > 0 {
				return l.Scale
			}
		}
	case I3:
		out, err := run(r, "xrdb", "-query")
		if err != nil {
			return 1
		}
		for _, l := range strings.Split(string(out), "\n") {
			k, v, ok := strings.Cut(l, ":")
			if ok && strings.TrimSpace(k) == "Xft.dpi" {
				if dpi, err := strconv.ParseFloat(strings.TrimSpace(v), 64); err == nil && dpi > 0 {
					return dpi / 96
				}
			}
		}
	case Cosmic:
		outs, err := cosmicOutputs(r)
		if err != nil {
			return 1
		}
		for _, o := range outs {
			if o.Enabled && o.Scale > 0 {
				return o.Scale
			}
		}
	}
	return 1
}

const textFactor = "text-scaling-factor"

// SetLargerText makes text 1.25 times larger, or normal again, through
// org.gnome.desktop.interface text-scaling-factor, which GTK apps and the
// GTK portal (Flatpak apps) follow live.
//
// On COSMIC it does nothing, and the page offers no "Larger text" there:
// xdg-desktop-portal-cosmic (epoch-1.11.0, src/main.rs read_all/read_one)
// answers text-scaling-factor with a hardcoded 1.0, and it is the portal
// Flatpak apps use (cosmic-portals.conf: default=cosmic;gtk); libcosmic apps
// do not read the key; and COSMIC has no font size setting (its Appearance
// page has only the interface density, which changes spacing, not text).
// Only native GTK apps would follow, and COSMIC ships almost none, so the
// page points to the display scale instead.
func SetLargerText(r Runner, home string, d Desktop, on bool) error {
	if d == Cosmic {
		return nil
	}
	v := "1.0"
	if on {
		v = "1.25"
	}
	if _, err := gsettings(r, "set", gsettingsSchema, textFactor, v); err != nil {
		return fmt.Errorf("gsettings: %w", err)
	}
	return nil
}

// ReadLargerText says whether text is larger than normal now.
func ReadLargerText(r Runner) bool {
	if r == nil {
		return false
	}
	out, err := gsettings(r, "get", gsettingsSchema, textFactor)
	if err != nil {
		return false
	}
	f, err := strconv.ParseFloat(strings.TrimSpace(string(out)), 64)
	return err == nil && f > 1.01
}

func writeFile(p, content string) error {
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	return os.WriteFile(p, []byte(content), 0o644)
}

// setXresource sets "name: value" in an X resource file, keeping every
// other line, and adds it when it is missing.
func setXresource(path, name, value string) error {
	b, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	var lines []string
	if s := strings.TrimRight(string(b), "\n"); s != "" {
		lines = strings.Split(s, "\n")
	}
	line, done := name+": "+value, false
	for i, l := range lines {
		k, _, ok := strings.Cut(l, ":")
		if ok && strings.TrimSpace(k) == name {
			lines[i], done = line, true
		}
	}
	if !done {
		lines = append(lines, line)
	}
	return writeFile(path, strings.Join(lines, "\n")+"\n")
}

type niriOutput struct {
	Logical *struct {
		Scale float64 `json:"scale"`
	} `json:"logical"`
}

// niriOutputs is `niri msg --json outputs`: the outputs by connector name.
func niriOutputs(r Runner) (map[string]niriOutput, error) {
	out, err := run(r, "niri", "msg", "--json", "outputs")
	if err != nil {
		return nil, err
	}
	var outs map[string]niriOutput
	if err := json.Unmarshal(out, &outs); err != nil {
		return nil, fmt.Errorf("niri outputs: %w", err)
	}
	return outs, nil
}

func niriOutputNames(r Runner) ([]string, error) {
	outs, err := niriOutputs(r)
	if err != nil {
		return nil, err
	}
	return sortedKeys(outs), nil
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

const niriScaleHeader = "// The display scale, set by Isotope Welcome (Accessibility).\n" +
	"// /etc/niri/config.kdl includes this file; niri reloads it when it changes.\n\n"

// niriBlock is one output block of isotope-scale.kdl, in the form
// writeNiriScale writes it.
var niriBlock = regexp.MustCompile(`(?m)^output "((?:[^"\\]|\\.)*)" \{\n    scale ([0-9.]+)\n\}\n?`)

// writeNiriScale writes scale s for the outputs names, and keeps the
// blocks of outputs that are not plugged in now.
func writeNiriScale(path string, names []string, s float64) error {
	scales := map[string]string{}
	if b, err := os.ReadFile(path); err == nil {
		for _, m := range niriBlock.FindAllStringSubmatch(string(b), -1) {
			scales[unquoteKDL(m[1])] = m[2]
		}
	}
	for _, n := range names {
		scales[n] = fmtScale(s)
	}
	var sb strings.Builder
	sb.WriteString(niriScaleHeader)
	for i, n := range sortedKeys(scales) {
		if i > 0 {
			sb.WriteString("\n")
		}
		fmt.Fprintf(&sb, "output %s {\n    scale %s\n}\n", quoteKDL(n), scales[n])
	}
	return writeFile(path, sb.String())
}

func quoteKDL(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}

func unquoteKDL(s string) string {
	return strings.NewReplacer(`\\`, `\`, `\"`, `"`).Replace(s)
}

// cosmicOutputs reads `cosmic-randr list --kdl` (guikit.ParseCosmicOutputs).
func cosmicOutputs(r Runner) ([]guikit.CosmicOutput, error) {
	out, err := run(r, "cosmic-randr", "list", "--kdl")
	if err != nil {
		return nil, err
	}
	return guikit.ParseCosmicOutputs(out)
}
