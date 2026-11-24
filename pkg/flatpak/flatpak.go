// Package flatpak is the user's own Flatpak installation (`flatpak --user`)
// as Isotope's tools need it: the installed apps and runtimes, the pending
// updates, Flathub's appstream catalog that flatpak keeps for the user, the
// permissions in an app's metadata, and installing, updating and removing
// with flatpak's own progress. It runs the flatpak command through a
// Runner, which the tests replace with outputs recorded from flatpak 1.16.
// Pure Go: cmd/isotope (also on the server) and Isotope System share it.
// See docs/superpowers/specs/2026-11-24-apps-page-design.md.
package flatpak

import (
	"bufio"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"
)

// Remote is the one remote Isotope's tools use.
const Remote = "flathub"

// ErrNoFlatpak is the answer on a system without the flatpak command.
var ErrNoFlatpak = errors.New("Flatpak is not on this system")

// queryTimeout bounds the commands that only read.
const queryTimeout = 2 * time.Minute

// Client is the user's installation.
type Client struct {
	R Runner
	// Bin is the flatpak command.
	Bin string
	// DataDir is the user installation, ~/.local/share/flatpak.
	DataDir string
	// Arch is flatpak's name for this machine's architecture.
	Arch string
}

// New is the client of the real flatpak, for the user running it.
func New() *Client {
	return &Client{R: ExecRunner{}, Bin: "flatpak", DataDir: UserDir(), Arch: Arch()}
}

// UserDir is the user installation: $FLATPAK_USER_DIR, or
// $XDG_DATA_HOME/flatpak (~/.local/share/flatpak).
func UserDir() string {
	if d := os.Getenv("FLATPAK_USER_DIR"); d != "" {
		return d
	}
	if d := os.Getenv("XDG_DATA_HOME"); d != "" {
		return filepath.Join(d, "flatpak")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "share", "flatpak")
}

// Arch is flatpak's name for GOARCH.
func Arch() string {
	switch runtime.GOARCH {
	case "amd64":
		return "x86_64"
	case "arm64":
		return "aarch64"
	case "386":
		return "i386"
	}
	return runtime.GOARCH
}

// Available says whether the flatpak command is there.
func (c *Client) Available() bool {
	_, err := exec.LookPath(c.Bin)
	return err == nil
}

func (c *Client) out(ctx context.Context, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()
	return c.R.Output(ctx, c.Bin, append([]string{"--user"}, args...)...)
}

// FlathubReady says whether the user's flathub remote exists and is
// enabled: isotope-user-setup disables it when it cannot verify it, and
// that is left alone (Isotope Welcome's rule).
func (c *Client) FlathubReady(ctx context.Context) bool {
	out, err := c.out(ctx, "remotes", "--columns=name,options")
	if err != nil {
		return false
	}
	for _, r := range rows(out, 2) {
		if r[0] != Remote {
			continue
		}
		disabled := false
		for _, o := range strings.Split(r[1], ",") {
			if strings.TrimSpace(o) == "disabled" {
				disabled = true
			}
		}
		if !disabled {
			return true
		}
	}
	return false
}

// Installed is one installed app or runtime.
type Installed struct {
	Ref                 Ref
	Name, Version, Size string
	SizeBytes           int64
	// Runtime is the runtime an app uses, ID/ARCH/BRANCH.
	Runtime string
	Origin  string
	// EOL is why the ref is end-of-life ("" when it is not); Rebase is
	// the ref that replaces it.
	EOL, Rebase string
}

// listColumns are the columns Installed asks `flatpak list` for.
const listColumns = "application,arch,branch,name,version,size,runtime,origin,options"

// Installed lists the user's apps and runtimes, apps first, by name.
func (c *Client) Installed(ctx context.Context) ([]Installed, error) {
	var all []Installed
	for _, kind := range []string{KindApp, KindRuntime} {
		out, err := c.out(ctx, "list", "--"+kind, "--columns="+listColumns)
		if err != nil {
			return nil, err
		}
		all = append(all, parseList(out, kind)...)
	}
	return all, nil
}

func parseList(out []byte, kind string) []Installed {
	var list []Installed
	for _, r := range rows(out, 9) {
		in := Installed{Ref: Ref{Kind: kind, ID: r[0], Arch: r[1], Branch: r[2]}, Name: r[3], Version: r[4],
			Size: r[5], Runtime: r[6], Origin: r[7]}
		in.SizeBytes, _ = ParseSize(r[5])
		in.EOL, in.Rebase = options(r[8])
		list = append(list, in)
	}
	sort.SliceStable(list, func(i, j int) bool { return strings.ToLower(list[i].Name) < strings.ToLower(list[j].Name) })
	return list
}

// Update is one pending update of an installed ref from Flathub.
type Update struct {
	Ref  Ref
	Name string
	// Version is the new version, from the catalog ("" when it has none).
	Version       string
	Download      string
	DownloadBytes int64
	// Commit is the start of the new commit's checksum.
	Commit string
}

// Updates lists the pending updates: `flatpak remote-ls --updates`, which
// compares the installed commits with Flathub's summary (a network read).
func (c *Client) Updates(ctx context.Context) ([]Update, error) {
	out, err := c.out(ctx, "remote-ls", Remote, "--updates", "--columns=ref,name,version,download-size,commit")
	if err != nil {
		return nil, err
	}
	return parseUpdates(out), nil
}

func parseUpdates(out []byte) []Update {
	var ups []Update
	for _, r := range rows(out, 5) {
		ref, ok := ParseRef(r[0])
		if !ok {
			continue
		}
		u := Update{Ref: ref, Name: r[1], Version: r[2], Download: r[3], Commit: r[4]}
		u.DownloadBytes, _ = ParseSize(r[3])
		ups = append(ups, u)
	}
	// Apps first.
	sort.SliceStable(ups, func(i, j int) bool { return ups[i].Ref.IsApp() && !ups[j].Ref.IsApp() })
	return ups
}

// RemoteInfo is what `flatpak remote-info` says about a ref on Flathub.
type RemoteInfo struct {
	Ref                 Ref
	Version, License    string
	Download, Installed string
	DownloadBytes       int64
	Runtime             string
	EOL, Rebase         string
}

// RemoteInfo reads Flathub's ref of app id (or a full ref).
func (c *Client) RemoteInfo(ctx context.Context, ref string) (RemoteInfo, error) {
	out, err := c.out(ctx, "remote-info", Remote, ref)
	if err != nil {
		return RemoteInfo{}, err
	}
	return parseRemoteInfo(out), nil
}

// parseRemoteInfo reads remote-info's aligned "Label: value" lines.
func parseRemoteInfo(out []byte) RemoteInfo {
	var ri RemoteInfo
	sc := bufio.NewScanner(strings.NewReader(string(out)))
	for sc.Scan() {
		k, v, ok := strings.Cut(strings.TrimSpace(sc.Text()), ": ")
		if !ok {
			continue
		}
		v = strings.TrimSpace(v)
		switch k {
		case "Ref":
			ri.Ref, _ = ParseRef(v)
		case "Version":
			ri.Version = v
		case "License":
			ri.License = v
		case "Download":
			ri.Download = v
			ri.DownloadBytes, _ = ParseSize(v)
		case "Installed":
			ri.Installed = v
		case "Runtime":
			ri.Runtime = v
		case "End-of-life":
			ri.EOL = v
		case "End-of-life-rebase":
			ri.Rebase = v
		}
	}
	return ri
}

// RemoteMetadata is the metadata of Flathub's newest commit of ref (an app
// ID or a full ref): what the app gets once installed or updated.
func (c *Client) RemoteMetadata(ctx context.Context, ref string) (Metadata, error) {
	out, err := c.out(ctx, "remote-info", "--show-metadata", Remote, ref)
	if err != nil {
		return Metadata{}, err
	}
	return ParseMetadata(out), nil
}

// InstalledMetadata is the metadata of the installed ref.
func (c *Client) InstalledMetadata(ctx context.Context, ref string) (Metadata, error) {
	out, err := c.out(ctx, "info", "--show-metadata", ref)
	if err != nil {
		return Metadata{}, err
	}
	return ParseMetadata(out), nil
}

// Running is the IDs of the apps running now (`flatpak ps`).
func (c *Client) Running(ctx context.Context) (map[string]bool, error) {
	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()
	out, err := c.R.Output(ctx, c.Bin, "ps", "--columns=application")
	if err != nil {
		return nil, err
	}
	got := map[string]bool{}
	for _, r := range rows(out, 1) {
		got[r[0]] = true
	}
	return got, nil
}

// AppstreamDir is where flatpak keeps Flathub's catalog for this user:
// appstream.xml.gz and the icons.
func (c *Client) AppstreamDir() string {
	return filepath.Join(c.DataDir, "appstream", Remote, c.Arch, "active")
}

// AppstreamAge is how long ago flatpak refreshed the catalog (the
// .timestamp file it touches); ok is false when it never did.
func (c *Client) AppstreamAge(now time.Time) (time.Duration, bool) {
	st, err := os.Stat(filepath.Join(c.DataDir, "appstream", Remote, c.Arch, ".timestamp"))
	if err != nil {
		st, err = os.Stat(filepath.Join(c.AppstreamDir(), "appstream.xml.gz"))
		if err != nil {
			return 0, false
		}
	}
	return now.Sub(st.ModTime()), true
}

// RefreshAppstream downloads Flathub's catalog again.
func (c *Client) RefreshAppstream(ctx context.Context) error {
	_, err := c.out(ctx, "update", "--appstream", Remote)
	return err
}

// LoadCatalog reads the catalog flatpak keeps for the user.
func (c *Client) LoadCatalog() (*Catalog, error) {
	return LoadCatalog(c.AppstreamDir())
}

// The jobs. Each runs flatpak with -y and not --noninteractive: the
// interactive transaction is the one that prints its progress, plain
// when the output is not a terminal ("Installing 1/2… ███  36%").

// Install starts the install of app id from Flathub.
func (c *Client) Install(id string) (Process, error) {
	return c.R.Start(c.Bin, "--user", "install", "-y", Remote, id)
}

// Update starts the update of refs (full refs or IDs); none updates all.
func (c *Client) Update(refs ...string) (Process, error) {
	return c.R.Start(c.Bin, append([]string{"--user", "update", "-y"}, refs...)...)
}

// Uninstall starts the removal of app id, and of its data
// (~/.var/app/ID) when deleteData is set.
func (c *Client) Uninstall(id string, deleteData bool) (Process, error) {
	args := []string{"--user", "uninstall", "-y"}
	if deleteData {
		args = append(args, "--delete-data")
	}
	return c.R.Start(c.Bin, append(args, id)...)
}

// RemoveUnused removes the runtimes and extensions nothing uses any more.
func (c *Client) RemoveUnused(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()
	_, err := c.R.Output(ctx, c.Bin, "--user", "uninstall", "--unused", "--noninteractive", "-y")
	return err
}

// Run starts `flatpak run ID` in its own session; it does not wait.
func (c *Client) Run(id string) error { return c.R.Launch(c.Bin, "run", id) }

// DataPath is the app's data, ~/.var/app/ID.
func DataPath(id string) string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".var", "app", id)
}

// DataSize is the size of the files under dir, 0 when there is none.
func DataSize(dir string) int64 {
	var n int64
	_ = filepath.WalkDir(dir, func(_ string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.Type().IsRegular() {
			if info, err := d.Info(); err == nil {
				n += info.Size()
			}
		}
		return nil
	})
	return n
}
