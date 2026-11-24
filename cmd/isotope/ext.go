package main

// System extensions (sysexts): what Kairos merges over /usr at run time. The
// daemon installs, enables, disables and removes them through `kairos-agent
// sysext`, and never writes the Kairos extension directory itself. It reads
// that directory to list what is installed, because `kairos-agent sysext
// list` prints a debug dump through its logger, not a format to parse.
//
// Before an extension is enabled, the daemon lists its files with
// systemd-dissect and compares them with the running /usr: an extension that
// would put a different copy over a file the desktop has (gpg's libgcrypt,
// for example) is refused unless the caller forces it. See
// docs/superpowers/specs/2026-11-07-extensions-design.md.

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// isotopeCatalog lists Isotope's own extensions (extensions/ in this
// repository, published by .github/workflows/extensions.yml). It comes before
// hadronLayersCatalog, the catalog Kairos itself defaults to, so a name both
// publish installs Isotope's build.
const (
	isotopeCatalog      = "https://kairos-community.github.io/isotope/extensions.json"
	hadronLayersCatalog = "https://kairos-io.github.io/hadron-layers/releases.json"
)

// defaultCatalogs is the list used without extensions.conf, or without a
// valid line in it. It matches the extensions.conf the image ships.
func defaultCatalogs() []string { return []string{isotopeCatalog, hadronLayersCatalog} }

// The extension job's outcome, Status.ExtResult. The values are part of the
// bus interface.
const (
	extResultOK       = "ok"
	extResultFailed   = "failed"
	extResultConflict = "conflict"
)

var (
	// errExtNeedsAdvanced refuses an install by URL or path while advanced
	// mode is off: only catalog names are installed then.
	errExtNeedsAdvanced = errors.New("advanced mode is off; only extensions from the catalogs can be installed")
	// errExtRunning refuses to cancel an extension job: kairos-agent may be
	// writing the extension directory.
	errExtRunning   = fmt.Errorf("%w: a system extension change is running and cannot be cancelled", ErrBusy)
	errExtNoBackend = errors.New("this service cannot manage system extensions")
)

// extName is a catalog name or an installed file name: no path separators, no
// leading dash (it would read as a flag to kairos-agent), nothing a shell or
// a menu would split.
var extName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+-]{0,127}$`)

const (
	maxExtRefLen     = 512
	maxExtVersionLen = 64
)

func errNotExtName(name string) error { return fmt.Errorf("%q is not an extension name", name) }

func extConfFile() string {
	if p := os.Getenv("ISOTOPE_UPDATE_EXT_CONF"); p != "" {
		return p
	}
	return "/etc/isotope/extensions.conf"
}

// extDir is where kairos-agent keeps installed extensions; enabled ones are
// links in its common/ (and active/, passive/, recovery/) subdirectories.
func extDir() string {
	if p := os.Getenv("ISOTOPE_UPDATE_EXT_DIR"); p != "" {
		return p
	}
	return "/var/lib/kairos/extensions"
}

// extMergedDir holds a link for every extension systemd-sysext merges now.
func extMergedDir() string {
	if p := os.Getenv("ISOTOPE_UPDATE_EXT_RUN_DIR"); p != "" {
		return p
	}
	return "/run/extensions"
}

// extStateDir keeps where each extension came from, which Kairos does not
// record. It is ours; the Kairos directory is not.
func extStateDir() string {
	if p := os.Getenv("ISOTOPE_UPDATE_EXT_STATE"); p != "" {
		return p
	}
	return "/var/lib/isotope-update/extensions"
}

// hostRoot is the system the conflict check compares an extension with. A
// variable so tests can point it at a fake /usr.
var hostRoot = "/"

// extGOARCH is the catalog's name for this machine's architecture. A
// variable so tests do not depend on the machine.
var extGOARCH = runtime.GOARCH

// systemdArch is the extension-release ARCHITECTURE= value for a GOARCH.
var systemdArch = map[string]string{"amd64": "x86-64", "arm64": "arm64"}

// Catalogs reads extensions.conf: one `catalog=URL` per line, searched in
// order. Without the file, or without one valid line, the default catalogs.
// A URL must be http or https and hold no comma: kairos-agent splits its
// --catalog values on commas.
func Catalogs() []string {
	b, err := os.ReadFile(extConfFile())
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			fmt.Fprintf(os.Stderr, "isotope: cannot read %s: %v; using the default catalogs\n", extConfFile(), err)
		}
		return defaultCatalogs()
	}
	var out []string
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		k, v, ok := strings.Cut(line, "=")
		if !ok || strings.TrimSpace(k) != "catalog" {
			continue
		}
		v = strings.TrimSpace(v)
		u, err := url.Parse(v)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || strings.ContainsAny(v, ", \t") {
			fmt.Fprintf(os.Stderr, "isotope: %s: ignoring catalog=%q, not an http(s) URL\n", extConfFile(), v)
			continue
		}
		out = append(out, v)
	}
	if len(out) == 0 {
		return defaultCatalogs()
	}
	return out
}

// CatalogEntry is one extension a catalog publishes for this machine.
type CatalogEntry struct {
	Name, Title, Description, Latest string
	// Catalog is the URL of the catalog that publishes it.
	Catalog string
	// Versions are the tags with an image for this architecture, as listed.
	Versions []string
	// Installed and Enabled describe the installed copy, if any.
	Installed, Enabled bool
}

// catalogDoc is the Kairos catalog format (kairos-io/kairos
// sdk/extensions/catalog.go), plus title and description, which hadron-layers
// publishes and Kairos ignores.
type catalogDoc struct {
	Repo   string `json:"repo"`
	Layers []struct {
		Name        string `json:"name"`
		Title       string `json:"title"`
		Description string `json:"description"`
		Latest      string `json:"latest"`
		Tags        []struct {
			Tag    string `json:"tag"`
			Sysext map[string]struct {
				OCI string `json:"oci"`
			} `json:"sysext"`
		} `json:"tags"`
	} `json:"layers"`
}

// parseCatalog reads one catalog. An entry is kept only when its latest tag
// has an image for arch: that is what `kairos-agent sysext install NAME`
// installs, and it fails otherwise. Entries with a name kairos-agent could
// not be given safely are dropped.
func parseCatalog(r io.Reader, source, arch string) ([]CatalogEntry, error) {
	var doc catalogDoc
	if err := json.NewDecoder(r).Decode(&doc); err != nil {
		return nil, fmt.Errorf("parse extension catalog %s: %w", source, err)
	}
	var out []CatalogEntry
	for _, l := range doc.Layers {
		if !extName.MatchString(l.Name) {
			continue
		}
		e := CatalogEntry{Name: l.Name, Title: oneLine(l.Title), Description: oneLine(l.Description), Latest: l.Latest, Catalog: source}
		latestOK := false
		for _, t := range l.Tags {
			if a, ok := t.Sysext[arch]; ok && a.OCI != "" {
				e.Versions = append(e.Versions, t.Tag)
				latestOK = latestOK || t.Tag == l.Latest
			}
		}
		if latestOK {
			out = append(out, e)
		}
	}
	return out, nil
}

// oneLine keeps catalog text to one printable line, for a menu.
func oneLine(s string) string {
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, s)
	return strings.Join(strings.Fields(s), " ")
}

// mergeCatalogs keeps the first catalog's entry for a name, as kairos-agent
// does when it searches them in order.
func mergeCatalogs(lists ...[]CatalogEntry) []CatalogEntry {
	seen := map[string]bool{}
	var out []CatalogEntry
	for _, l := range lists {
		for _, e := range l {
			if seen[e.Name] {
				continue
			}
			seen[e.Name] = true
			out = append(out, e)
		}
	}
	return out
}

// maxCatalogBytes bounds a catalog download; hadron-layers' is a few KB.
const maxCatalogBytes = 4 << 20

var catalogClient = &http.Client{Timeout: 30 * time.Second}

func fetchCatalog(ctx context.Context, u string) ([]CatalogEntry, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := catalogClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: %s", u, resp.Status)
	}
	return parseCatalog(io.LimitReader(resp.Body, maxCatalogBytes), u, extGOARCH)
}

// InstalledExt is one extension in the Kairos extension directory.
type InstalledExt struct {
	// Name is File without ".raw" and a ".sysext" before it.
	Name, File string
	// Enabled: linked in common/, so merged at every boot. Merged: merged now.
	Enabled, Merged bool
	// Source is what it was installed from, when this service installed it.
	Source string
}

func extDisplayName(file string) string {
	n := strings.TrimSuffix(file, ".raw")
	return strings.TrimSuffix(n, ".sysext")
}

// extMatches is kairos-agent's own rule (agent/pkg/action/sysext.go,
// GetExtension): the whole file name, or the part before a dot.
func extMatches(file, name string) bool {
	return file == name || strings.HasPrefix(file, name+".")
}

func exists(p string) bool { _, err := os.Lstat(p); return err == nil }

// listInstalled reads the extension directory the way kairos-agent does:
// the .raw files at its top.
func listInstalled() ([]InstalledExt, error) {
	entries, err := os.ReadDir(extDir())
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []InstalledExt
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".raw" {
			continue
		}
		f := e.Name()
		x := InstalledExt{
			Name:    extDisplayName(f),
			File:    f,
			Enabled: exists(filepath.Join(extDir(), "common", f)),
			Merged:  exists(filepath.Join(extMergedDir(), f)),
		}
		if b, err := os.ReadFile(filepath.Join(extStateDir(), f)); err == nil {
			x.Source = strings.TrimSpace(string(b))
		}
		out = append(out, x)
	}
	return out, nil
}

// findInstalled resolves a name the user typed: the display name first, then
// kairos-agent's rule. More than one match is an error, not a guess.
func findInstalled(name string) (InstalledExt, error) {
	list, err := listInstalled()
	if err != nil {
		return InstalledExt{}, err
	}
	for _, x := range list {
		if x.Name == name || x.File == name {
			return x, nil
		}
	}
	var hits []InstalledExt
	for _, x := range list {
		if extMatches(x.File, name) {
			hits = append(hits, x)
		}
	}
	switch len(hits) {
	case 1:
		return hits[0], nil
	case 0:
		return InstalledExt{}, fmt.Errorf("no installed extension is called %q", name)
	}
	return InstalledExt{}, fmt.Errorf("%q names more than one installed extension; use the file name", name)
}

func recordSource(file, source string) error {
	if err := os.MkdirAll(extStateDir(), 0o755); err != nil {
		return err
	}
	return writeRunFile(filepath.Join(extStateDir(), file), source+"\n")
}

func forgetSource(file string) {
	_ = os.Remove(filepath.Join(extStateDir(), file))
	_ = os.Remove(extActivationFile(file))
}

// isExtReference says whether ref names an image rather than a catalog
// entry: kairos-agent reads an absolute path or a scheme (oci:, docker:,
// file:, https:, ...) as a reference.
func isExtReference(ref string) bool {
	return strings.HasPrefix(ref, "/") || strings.Contains(ref, ":")
}

// checkExtRef refuses what must never reach kairos-agent: a leading dash, a
// control character, an overlong value.
func checkExtRef(ref string) error {
	switch {
	case ref == "":
		return errors.New("name the extension to install")
	case len(ref) > maxExtRefLen:
		return fmt.Errorf("the extension reference is longer than %d bytes", maxExtRefLen)
	case strings.HasPrefix(ref, "-"):
		return fmt.Errorf("%q is not an extension name or URL", ref)
	case strings.IndexFunc(ref, func(r rune) bool { return r <= ' ' || r == 0x7f }) >= 0:
		return fmt.Errorf("%q holds a space or a control character", ref)
	case !isExtReference(ref) && !extName.MatchString(ref):
		return fmt.Errorf("%q is not an extension name", ref)
	}
	return nil
}

func checkExtVersion(v string) error {
	switch {
	case len(v) > maxExtVersionLen:
		return fmt.Errorf("the version is longer than %d bytes", maxExtVersionLen)
	case strings.HasPrefix(v, "-"):
		return fmt.Errorf("%q is not a version", v)
	case strings.IndexFunc(v, func(r rune) bool { return r < ' ' || r == 0x7f }) >= 0:
		return fmt.Errorf("%q holds a control character", v)
	}
	return nil
}

// mtreeEntry is one line of `systemd-dissect --mtree`.
type mtreeEntry struct {
	Path, Type, SHA256, Link string
	Size                     int64
}

// ExtImage is what the conflict check needs from an extension image.
type ExtImage struct {
	// Release is its extension-release file.
	Release map[string]string
	Entries []mtreeEntry
}

// parseMtree reads systemd-dissect's mtree listing (src/dissect/dissect.c,
// mtree_print_item): a path relative to the image root, "/" after a
// directory, then key=value words. Paths and link targets are C-escaped.
func parseMtree(r io.Reader) ([]mtreeEntry, error) {
	var out []mtreeEntry
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) == 0 {
			continue
		}
		e := mtreeEntry{Path: strings.TrimSuffix(unescapeC(f[0]), "/")}
		for _, kv := range f[1:] {
			k, v, _ := strings.Cut(kv, "=")
			switch k {
			case "type":
				e.Type = v
			case "sha256sum":
				e.SHA256 = v
			case "link":
				e.Link = unescapeC(v)
			case "size":
				e.Size, _ = strconv.ParseInt(v, 10, 64)
			}
		}
		out = append(out, e)
	}
	return out, sc.Err()
}

// unescapeC undoes systemd's xescape (\xNN) and cescape (\\, \n, ...).
func unescapeC(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] != '\\' || i+1 >= len(s) {
			b.WriteByte(s[i])
			continue
		}
		i++
		switch s[i] {
		case 'x':
			if i+2 < len(s) {
				if n, err := strconv.ParseUint(s[i+1:i+3], 16, 8); err == nil {
					b.WriteByte(byte(n))
					i += 2
					continue
				}
			}
			b.WriteString(`\x`)
		case 'n':
			b.WriteByte('\n')
		case 't':
			b.WriteByte('\t')
		case 'r':
			b.WriteByte('\r')
		default:
			b.WriteByte(s[i])
		}
	}
	return b.String()
}

// checkRelease refuses an extension built for another machine or another
// system: ARCHITECTURE must be this machine's, ID `_any` or `isotope`.
func checkRelease(rel map[string]string, goarch string) error {
	if len(rel) == 0 {
		return errors.New("it has no extension-release file, so it is not a system extension")
	}
	if want := systemdArch[goarch]; rel["ARCHITECTURE"] != want {
		return fmt.Errorf("it is built for the architecture %q, not %q", rel["ARCHITECTURE"], want)
	}
	if id := rel["ID"]; id != "_any" && id != "isotope" {
		return fmt.Errorf("it is built for the system %q, not for any system (_any) or Hadron", id)
	}
	return nil
}

// findConflicts lists the files of an extension that the system under root
// already has with other content. Only /usr and /opt are merged; the
// extension's own release file cannot clash. A file the system lacks, and
// one it has byte for byte, are fine.
func findConflicts(root string, entries []mtreeEntry) ([]string, error) {
	var out []string
	for _, e := range entries {
		p := e.Path
		if !(p == "usr" || strings.HasPrefix(p, "usr/") || p == "opt" || strings.HasPrefix(p, "opt/")) ||
			strings.HasPrefix(p, "usr/lib/extension-release.d/") {
			continue
		}
		host := filepath.Join(root, p)
		fi, err := os.Lstat(host)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			// The system has a file where the extension has a directory
			// on the way (usr/lib/x/y with x a file): that file is the
			// clash, and it is reported on its own line.
			if errors.Is(err, syscall.ENOTDIR) {
				continue
			}
			return nil, err
		}
		same := false
		switch e.Type {
		case "dir":
			// The system's directory may be a link to one (lib64 -> lib).
			if st, err := os.Stat(host); err == nil && st.IsDir() {
				same = true
			}
		case "link":
			if fi.Mode()&os.ModeSymlink != 0 {
				t, err := os.Readlink(host)
				same = err == nil && t == e.Link
			}
		case "file":
			if fi.Mode().IsRegular() && fi.Size() == e.Size {
				if e.Size == 0 {
					same = true
				} else {
					sum, err := fileSHA256(host)
					if err != nil {
						return nil, err
					}
					same = sum == e.SHA256
				}
			}
		default:
			same = false
		}
		if !same {
			out = append(out, "/"+p)
		}
	}
	return out, nil
}

func fileSHA256(p string) (string, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// ExtBackend runs the extension commands. The real one is kairos-agent and
// systemd-dissect; tests use a fake. Names are installed file names.
type ExtBackend interface {
	// InstallExt runs `kairos-agent sysext install` for a catalog name (with
	// an optional version) or a reference.
	InstallExt(catalogs []string, ref, version string, progress func(string)) error
	// EnableExt enables for every boot (--common) and merges now (--now).
	EnableExt(file string, progress func(string)) error
	DisableExt(file string, progress func(string)) error
	RemoveExt(file string, progress func(string)) error
	// ReloadExt makes the running system read what a merge or an unmerge
	// changed: systemd's units, and the D-Bus policy, udev rules and polkit
	// actions and rules when a says the extension has some. kairos-agent
	// leaves the bus, udevd and polkitd as they were.
	ReloadExt(a extActivation, progress func(string)) error
	// StartUnit starts a unit and waits for it, so a unit that fails is an
	// error. StopUnit stops one.
	StartUnit(unit string, progress func(string)) error
	StopUnit(unit string, progress func(string)) error
	// Dissect reads an image's extension-release and its file list.
	Dissect(path string) (ExtImage, error)
}

// systemdDissect is the dissect tool; ISOTOPE_UPDATE_DISSECT replaces it for
// tests.
func systemdDissect() string {
	if p := os.Getenv("ISOTOPE_UPDATE_DISSECT"); p != "" {
		return p
	}
	return "/usr/bin/systemd-dissect"
}

func (kairosBackend) InstallExt(catalogs []string, ref, version string, progress func(string)) error {
	args := []string{"sysext", "install"}
	for _, c := range catalogs {
		args = append(args, "--catalog="+c)
	}
	if version != "" {
		args = append(args, "--version="+version)
	}
	return agentErr("install", streamCommand(kairosAgent(), append(args, "--", ref), progress))
}

func (kairosBackend) EnableExt(file string, progress func(string)) error {
	return agentErr("enable", streamCommand(kairosAgent(), []string{"sysext", "enable", "--common", "--now", "--", file}, progress))
}

func (kairosBackend) DisableExt(file string, progress func(string)) error {
	return agentErr("disable", streamCommand(kairosAgent(), []string{"sysext", "disable", "--common", "--now", "--", file}, progress))
}

func (kairosBackend) RemoveExt(file string, progress func(string)) error {
	return agentErr("remove", streamCommand(kairosAgent(), []string{"sysext", "remove", "--now", "--", file}, progress))
}

// extActivation is what a merged extension asks of the running system:
// the units it owns and enables from /usr, and whether it brings D-Bus
// system configuration, udev rules or polkit actions and rules.
type extActivation struct {
	Units  []string `json:"units,omitempty"`
	DBus   bool     `json:"dbus,omitempty"`
	Udev   bool     `json:"udev,omitempty"`
	Polkit bool     `json:"polkit,omitempty"`
}

func (a extActivation) empty() bool { return len(a.Units) == 0 && !a.DBus && !a.Udev && !a.Polkit }

// reloads says whether a merge or an unmerge of the extension needs the bus,
// udevd or polkitd to read their files again.
func (a extActivation) reloads() bool { return a.DBus || a.Udev || a.Polkit }

// extStartTargets are the targets whose .wants links in an extension are
// started after the merge. They are the ones a booted system has reached;
// graphical.target is never reached on Kairos.
var extStartTargets = []string{"multi-user.target", "sockets.target", "timers.target"}

// extUnitDirs are where the system at hostRoot keeps unit files: a unit
// found there belongs to the base (or to another extension), never to the
// one being enabled.
var extUnitDirs = []string{"usr/lib/systemd/system", "etc/systemd/system", "lib/systemd/system"}

// unitTemplate is foo@.service for foo@bar.service, "" for a plain unit.
func unitTemplate(unit string) string {
	at := strings.Index(unit, "@")
	dot := strings.LastIndex(unit, ".")
	if at < 0 || dot < at {
		return ""
	}
	return unit[:at+1] + unit[dot:]
}

// extActivationOf reads an image's file list, before it is merged, against
// the system at root. A unit counts when the image links it directly in
// usr/lib/systemd/system/<target>.wants/ (the way an extension enables a
// unit, since it cannot write /etc), ships its unit file (or template)
// there too, and the system has no unit file of that name: so an
// extension never starts or stops a unit of the base, such as
// NetworkManager.service, even when it carries the same link.
func extActivationOf(entries []mtreeEntry, root string) extActivation {
	var a extActivation
	ships := map[string]bool{}
	for _, e := range entries {
		if u, ok := strings.CutPrefix(e.Path, "usr/lib/systemd/system/"); ok && !strings.Contains(u, "/") && e.Type != "dir" {
			ships[u] = true
		}
	}
	hostHas := func(unit string) bool {
		for _, d := range extUnitDirs {
			if exists(filepath.Join(root, d, unit)) {
				return true
			}
		}
		return false
	}
	seen := map[string]bool{}
	for _, e := range entries {
		switch {
		case strings.HasPrefix(e.Path, "usr/share/dbus-1/system.d/"),
			strings.HasPrefix(e.Path, "usr/share/dbus-1/system-services/"):
			a.DBus = a.DBus || e.Type != "dir"
		case strings.HasPrefix(e.Path, "usr/lib/udev/rules.d/"):
			a.Udev = a.Udev || e.Type != "dir"
		case strings.HasPrefix(e.Path, "usr/share/polkit-1/actions/"),
			strings.HasPrefix(e.Path, "usr/share/polkit-1/rules.d/"):
			a.Polkit = a.Polkit || e.Type != "dir"
		}
		if e.Type != "link" {
			continue
		}
		for _, t := range extStartTargets {
			unit, ok := strings.CutPrefix(e.Path, "usr/lib/systemd/system/"+t+".wants/")
			if !ok || unit == "" || strings.Contains(unit, "/") || seen[unit] {
				continue
			}
			tmpl := unitTemplate(unit)
			owned := ships[unit] || (tmpl != "" && ships[tmpl])
			if !owned || hostHas(unit) || (tmpl != "" && hostHas(tmpl)) {
				continue
			}
			seen[unit] = true
			a.Units = append(a.Units, unit)
		}
	}
	return a
}

// extActivationFile records, per installed file, what activate did, so a
// disable or remove stops exactly those units: once merged, the image's
// unit files are on the system too and can no longer be told from the
// base's.
func extActivationFile(file string) string {
	return filepath.Join(extStateDir(), file+".activation")
}

func recordActivation(file string, a extActivation) error {
	b, err := json.Marshal(a)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(extStateDir(), 0o755); err != nil {
		return err
	}
	return writeRunFile(extActivationFile(file), string(b)+"\n")
}

// readActivation is the record of file, empty without one: an extension
// enabled before Isotope started units, or by kairos-agent by hand, has
// none, and nothing of it is stopped.
func readActivation(file string) extActivation {
	var a extActivation
	if b, err := os.ReadFile(extActivationFile(file)); err == nil {
		_ = json.Unmarshal(b, &a)
	}
	return a
}

// extSysRun runs systemctl, busctl and udevadm for ReloadExt, StartUnit
// and StopUnit. A variable so tests can see the calls.
var extSysRun fwRunner = execCtx

// extSysTimeout bounds each of those commands; a start or a stop waits for
// the unit.
const extSysTimeout = 2 * time.Minute

func extSys(progress func(string), name string, args ...string) error {
	ctx, cancel := context.WithTimeout(context.Background(), extSysTimeout)
	defer cancel()
	out, err := extSysRun(ctx, name, args...)
	text := strings.TrimSpace(stripANSI(string(out)))
	for _, l := range strings.Split(text, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			progress(l)
		}
	}
	if err != nil {
		if text != "" {
			return fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, text)
		}
		return fmt.Errorf("%s %s: %w", name, strings.Join(args, " "), err)
	}
	return nil
}

// ReloadExt reloads systemd (systemd-sysext does too; a second reload
// costs nothing), the bus's policy (a service cannot own its name before;
// ReloadConfig keeps every connection), udev's rules (a reload triggers
// nothing) and polkit's actions and rules. polkitd watches its directories,
// but the merge mounts a new /usr over the one it watches, so it misses the
// files; its reload (SIGHUP, polkit.service is Type=notify-reload) reads
// them again and keeps the registered agents. try-reload-or-restart does
// nothing when polkitd is not running: it reads them when it starts.
func (kairosBackend) ReloadExt(a extActivation, progress func(string)) error {
	if err := extSys(progress, "systemctl", "daemon-reload"); err != nil {
		return err
	}
	if a.DBus {
		if err := extSys(progress, "busctl", "call", "org.freedesktop.DBus", "/org/freedesktop/DBus", "org.freedesktop.DBus", "ReloadConfig"); err != nil {
			return err
		}
	}
	if a.Udev {
		if err := extSys(progress, "udevadm", "control", "--reload"); err != nil {
			return err
		}
	}
	if a.Polkit {
		return extSys(progress, "systemctl", "try-reload-or-restart", "polkit.service")
	}
	return nil
}

func (kairosBackend) StartUnit(unit string, progress func(string)) error {
	return extSys(progress, "systemctl", "start", "--", unit)
}

func (kairosBackend) StopUnit(unit string, progress func(string)) error {
	return extSys(progress, "systemctl", "stop", "--", unit)
}

func agentErr(verb string, err error) error {
	if err != nil {
		return fmt.Errorf("kairos-agent sysext %s: %w", verb, err)
	}
	return nil
}

// Dissect runs systemd-dissect twice: --json for the extension-release, and
// --mtree for every file with its SHA-256. Both mount the image read-only;
// neither enables it. Colours and the pager are off, so the output parses.
func (kairosBackend) Dissect(path string) (ExtImage, error) {
	run := func(args ...string) ([]byte, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		cmd := exec.CommandContext(ctx, systemdDissect(), append([]string{"--no-pager"}, args...)...)
		cmd.Env = append(os.Environ(), "SYSTEMD_COLORS=0", "NO_COLOR=1")
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		if err != nil {
			return nil, fmt.Errorf("systemd-dissect %s: %w: %s", args[0], err, strings.TrimSpace(stderr.String()))
		}
		return out, nil
	}
	js, err := run("--json=short", path)
	if err != nil {
		return ExtImage{}, err
	}
	var meta struct {
		SysextRelease map[string]string `json:"sysextRelease"`
	}
	if err := json.Unmarshal(js, &meta); err != nil {
		return ExtImage{}, fmt.Errorf("reading systemd-dissect's description of %s: %w", path, err)
	}
	mt, err := run("--mtree", "--mtree-hash=yes", path)
	if err != nil {
		return ExtImage{}, err
	}
	entries, err := parseMtree(bytes.NewReader(mt))
	if err != nil {
		return ExtImage{}, err
	}
	return ExtImage{Release: meta.SysextRelease, Entries: entries}, nil
}

// streamCommand runs name with stdout and stderr merged into one pipe and
// hands each line to progress, so lines keep their order. The caller names
// the command in the error.
func streamCommand(name string, args []string, progress func(string)) error {
	cmd := exec.Command(name, args...)
	// The lines go to a tray window and a log, not a terminal: ask for no
	// colours, and strip any escape sequence that still comes through.
	cmd.Env = append(os.Environ(), "NO_COLOR=1")
	r, w, err := os.Pipe()
	if err != nil {
		return err
	}
	defer r.Close()
	cmd.Stdout, cmd.Stderr = w, w
	if err := cmd.Start(); err != nil {
		w.Close()
		return err
	}
	w.Close() // the child holds its own copy; EOF comes when it exits

	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	sc.Split(scanLinesOrCR)
	for sc.Scan() {
		if l := strings.TrimSpace(stripANSI(sc.Text())); l != "" {
			progress(l)
		}
	}
	// A line over the buffer stops the scanner; say so, and keep draining so
	// the child never blocks on a full pipe.
	if err := sc.Err(); err != nil {
		progress("isotope: stopped reading " + filepath.Base(name) + "'s output: " + err.Error())
	}
	io.Copy(io.Discard, r)
	return cmd.Wait()
}

// ansiSequence matches the terminal escape sequences a command writes even
// when it is not on a terminal: CSI (colours, cursor moves) and OSC (titles,
// links). A tray window cannot draw them; it shows the escape byte as a box.
var ansiSequence = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]|\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)`)

func stripANSI(s string) string { return ansiSequence.ReplaceAllString(s, "") }

// extCache keeps the merged catalog for releasesTTL, for the same reason the
// release list is cached: every browse would otherwise fetch every catalog.
type extCache struct {
	mu      sync.Mutex
	entries []CatalogEntry
	key     string
	at      time.Time
}

// catalog fetches and merges the configured catalogs. A catalog that cannot
// be fetched is skipped and logged; only when none can be is it an error.
func (d *Daemon) catalog(ctx context.Context) ([]CatalogEntry, error) {
	urls := Catalogs()
	key := strings.Join(urls, "\n")
	d.extCat.mu.Lock()
	if d.extCat.key == key && time.Since(d.extCat.at) < releasesTTL {
		c := append([]CatalogEntry(nil), d.extCat.entries...)
		d.extCat.mu.Unlock()
		return c, nil
	}
	d.extCat.mu.Unlock()

	var lists [][]CatalogEntry
	var errs []string
	for _, u := range urls {
		l, err := fetchCatalog(ctx, u)
		if err != nil {
			fmt.Fprintln(os.Stderr, "isotope: skipping an extension catalog:", err)
			errs = append(errs, err.Error())
			continue
		}
		lists = append(lists, l)
	}
	if len(lists) == 0 {
		return nil, fmt.Errorf("could not read any extension catalog: %s", strings.Join(errs, "; "))
	}
	merged := mergeCatalogs(lists...)
	if len(errs) == 0 {
		d.extCat.mu.Lock()
		d.extCat.entries, d.extCat.key, d.extCat.at = append([]CatalogEntry(nil), merged...), key, time.Now()
		d.extCat.mu.Unlock()
	}
	return merged, nil
}

// Catalog is the merged catalog with every entry marked installed or not.
func (d *Daemon) Catalog(ctx context.Context) ([]CatalogEntry, error) {
	entries, err := d.catalog(ctx)
	if err != nil {
		return nil, err
	}
	installed, err := listInstalled()
	if err != nil {
		return nil, err
	}
	for i := range entries {
		for _, x := range installed {
			if x.Name == entries[i].Name || extMatches(x.File, entries[i].Name) {
				entries[i].Installed = true
				entries[i].Enabled = entries[i].Enabled || x.Enabled
			}
		}
	}
	return entries, nil
}

// Extensions lists the installed extensions.
func (d *Daemon) Extensions() ([]InstalledExt, error) { return listInstalled() }

// ExtensionFiles lists the files and links an installed extension adds.
func (d *Daemon) ExtensionFiles(name string) ([]string, error) {
	if d.ext == nil {
		return nil, errExtNoBackend
	}
	x, err := findInstalled(name)
	if err != nil {
		return nil, err
	}
	img, err := d.ext.Dissect(filepath.Join(extDir(), x.File))
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range img.Entries {
		if e.Type != "dir" && e.Path != "." {
			out = append(out, "/"+e.Path)
		}
	}
	sort.Strings(out)
	return out, nil
}

// errExtConflict carries the conflicting files to the job's outcome.
type errExtConflict struct{ files []string }

func (e *errExtConflict) Error() string {
	const show = 20
	files := e.files
	more := ""
	if len(files) > show {
		more = fmt.Sprintf(" and %d more", len(files)-show)
		files = files[:show]
	}
	return fmt.Sprintf("it replaces files the system already has with different ones: %s%s", strings.Join(files, ", "), more)
}

// vet runs the checks before an extension is enabled. A wrong architecture
// or system is never overridable; a conflict is, with force, and then only
// reported as a warning.
func (d *Daemon) vet(file string, force bool) (ExtImage, error) {
	img, err := d.ext.Dissect(filepath.Join(extDir(), file))
	if err != nil {
		return img, fmt.Errorf("could not read the extension: %w", err)
	}
	if err := checkRelease(img.Release, extGOARCH); err != nil {
		return img, err
	}
	clash, err := findConflicts(hostRoot, img.Entries)
	if err != nil {
		return img, fmt.Errorf("could not compare the extension with the system: %w", err)
	}
	if len(clash) == 0 {
		return img, nil
	}
	if !force {
		return img, &errExtConflict{files: clash}
	}
	d.progress(fmt.Sprintf("Warning: forced over %d file(s) the system has with other content: %s", len(clash), strings.Join(clash, ", ")))
	return img, nil
}

// plan is what an extension about to be merged owns and enables. It must
// run before EnableExt: once merged, the extension's own unit files are on
// the host's /usr, and extActivationOf would take them for the base's. An
// extension that is merged already (enabled again) keeps its record.
func (d *Daemon) plan(x InstalledExt, img ExtImage) extActivation {
	if x.Merged {
		return readActivation(x.File)
	}
	return extActivationOf(img.Entries, hostRoot)
}

// activate starts what a just-merged extension owns and enables (a, from
// plan, before the merge), and returns the lines the job's result adds:
// the units started, and any that did not start (the extension stays
// enabled; they start at the next boot through their link).
func (d *Daemon) activate(file string, a extActivation) string {
	if a.empty() {
		return ""
	}
	if err := recordActivation(file, a); err != nil {
		d.progress("isotope: could not record the extension's services: " + err.Error())
	}
	var out strings.Builder
	if err := d.ext.ReloadExt(a, d.progress); err != nil {
		out.WriteString("\nCould not reload the system's services: " + err.Error())
	}
	var started []string
	for _, u := range a.Units {
		d.progress("Starting " + u + "…")
		if err := d.ext.StartUnit(u, d.progress); err != nil {
			fmt.Fprintf(&out, "\nNot started: %s (%v); it starts at the next restart.", u, err)
			continue
		}
		started = append(started, u)
	}
	if len(started) > 0 {
		out.WriteString("\nStarted: " + strings.Join(started, ", ") + ".")
	}
	return out.String()
}

// deactivate stops, before an unmerge, the units activate started for x
// (its record), and returns the result's lines and the record. It never
// stops the disable or remove: a unit that does not stop is a line.
func (d *Daemon) deactivate(x InstalledExt) (string, extActivation) {
	a := readActivation(x.File)
	if !x.Merged || len(a.Units) == 0 {
		return "", a
	}
	var out strings.Builder
	var stopped []string
	for _, u := range a.Units {
		d.progress("Stopping " + u + "…")
		if err := d.ext.StopUnit(u, d.progress); err != nil {
			fmt.Fprintf(&out, "\nCould not stop %s: %v", u, err)
			continue
		}
		stopped = append(stopped, u)
	}
	if len(stopped) > 0 {
		out.WriteString("\nStopped: " + strings.Join(stopped, ", ") + ".")
	}
	return out.String(), a
}

// unmerged reloads the bus, udev and polkit after an unmerge, so none keeps
// the policy or rules of an extension that is gone.
func (d *Daemon) unmerged(x InstalledExt, a extActivation) {
	if !x.Merged || !a.reloads() {
		return
	}
	if err := d.ext.ReloadExt(a, d.progress); err != nil {
		d.progress("isotope: could not reload the system's services: " + err.Error())
	}
}

// startExt starts an extension job and returns at once, like Apply. It takes
// the daemon's one job slot, so it refuses during a check or an upgrade and
// they refuse during it. The outcome is Status.ExtResult and ExtDetail when
// the state leaves extension.
func (d *Daemon) startExt(what string, run func() (string, error)) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	switch {
	case !d.managed:
		return errUnmanaged
	case d.ext == nil:
		return errExtNoBackend
	case d.stopping:
		return ErrStopping
	case d.rebooting:
		return ErrRebooting
	case d.busyLocked() || d.job != nil:
		return ErrBusy
	}
	j := &job{cancel: func() {}, prev: d.state, prevDetail: d.detail}
	d.job = j
	d.extResult, d.extDetail = "", ""
	d.setLocked(StateExtension, what)
	d.wg.Add(1)
	go func() {
		defer d.wg.Done()
		msg, err := run()
		d.mu.Lock()
		defer d.mu.Unlock()
		d.job = nil
		var conflict *errExtConflict
		switch {
		case errors.As(err, &conflict):
			d.extResult, d.extDetail = extResultConflict, err.Error()
		case err != nil:
			d.extResult, d.extDetail = extResultFailed, err.Error()
		default:
			d.extResult, d.extDetail = extResultOK, msg
		}
		prev, detail := j.prev, j.prevDetail
		if prev == StateError {
			// The error was the last job's; nothing is wrong now.
			prev, detail = StateIdle, ""
		}
		d.setLocked(prev, detail)
	}()
	return nil
}

// InstallExtension installs a catalog name (at version, or the latest) or,
// in advanced mode, a reference, then checks it and enables it for every
// boot. A refused extension is removed again, so nothing half-installed
// stays. Bad input is refused here, before a job starts.
func (d *Daemon) InstallExtension(ref, version string, force bool) error {
	if err := checkExtRef(ref); err != nil {
		return err
	}
	if err := checkExtVersion(version); err != nil {
		return err
	}
	isRef := isExtReference(ref)
	if isRef && version != "" {
		return errors.New("a version can only be given with a catalog name")
	}
	if isRef && !AdvancedEnabled() {
		return errExtNeedsAdvanced
	}
	if !isRef {
		if x, err := findInstalled(ref); err == nil {
			return fmt.Errorf("%s is already installed (%s); remove it first", ref, x.File)
		}
	}
	source := ref
	if version != "" {
		source += "@" + version
	}
	return d.startExt("Installing the extension "+ref, func() (string, error) {
		before, err := listInstalled()
		if err != nil {
			return "", err
		}
		had := map[string]bool{}
		for _, x := range before {
			had[x.File] = true
		}
		if err := d.ext.InstallExt(Catalogs(), ref, version, d.progress); err != nil {
			return "", err
		}
		after, err := listInstalled()
		if err != nil {
			return "", err
		}
		var added []string
		for _, x := range after {
			if !had[x.File] {
				added = append(added, x.File)
			}
		}
		if len(added) != 1 {
			return "", fmt.Errorf("kairos-agent reported success but added %d extension files, not one; nothing was enabled", len(added))
		}
		file := added[0]
		img, err := d.vet(file, force)
		if err != nil {
			if rerr := d.ext.RemoveExt(file, d.progress); rerr != nil {
				return "", fmt.Errorf("%w; removing it again failed too: %v", err, rerr)
			}
			return "", err
		}
		if err := recordSource(file, source); err != nil {
			d.progress("isotope: could not record where the extension came from: " + err.Error())
		}
		a := d.plan(InstalledExt{File: file}, img)
		if err := d.ext.EnableExt(file, d.progress); err != nil {
			return "", fmt.Errorf("%s is installed but could not be enabled: %w", extDisplayName(file), err)
		}
		return fmt.Sprintf("%s is installed and enabled.", extDisplayName(file)) + d.activate(file, a), nil
	})
}

// SetExtensionEnabled turns an installed extension on (after the same checks
// as an install) or off, now and for every boot.
func (d *Daemon) SetExtensionEnabled(name string, on, force bool) error {
	if !extName.MatchString(name) {
		return errNotExtName(name)
	}
	x, err := findInstalled(name)
	if err != nil {
		return err
	}
	if !on {
		return d.startExt("Disabling the extension "+x.Name, func() (string, error) {
			lines, a := d.deactivate(x)
			if err := d.ext.DisableExt(x.File, d.progress); err != nil {
				return "", err
			}
			d.unmerged(x, a)
			return x.Name + " is disabled." + lines, nil
		})
	}
	return d.startExt("Enabling the extension "+x.Name, func() (string, error) {
		img, err := d.vet(x.File, force)
		if err != nil {
			return "", err
		}
		a := d.plan(x, img)
		if err := d.ext.EnableExt(x.File, d.progress); err != nil {
			return "", err
		}
		return x.Name + " is enabled." + d.activate(x.File, a), nil
	})
}

// RemoveExtension disables and deletes an installed extension.
func (d *Daemon) RemoveExtension(name string) error {
	if !extName.MatchString(name) {
		return errNotExtName(name)
	}
	x, err := findInstalled(name)
	if err != nil {
		return err
	}
	return d.startExt("Removing the extension "+x.Name, func() (string, error) {
		lines, a := d.deactivate(x)
		if err := d.ext.RemoveExt(x.File, d.progress); err != nil {
			return "", err
		}
		d.unmerged(x, a)
		forgetSource(x.File)
		return x.Name + " is removed." + lines, nil
	})
}
