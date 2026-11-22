// Package isorelease is what an Isotope system knows about its own release
// (/etc/isotope/release) and the release tags: the image line, the image
// tag of a flavor, line and release, and the order of release tags. The
// isotope command (update/) and Isotope System (system/) share it, so
// they compute the same image for the same release.
package isorelease

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// DefaultFile is written by the Dockerfile's kairos stage. It is the one
// place the installed system records which image line it upgrades along.
const DefaultFile = "/etc/isotope/release"

// Release is the identity of the running system.
type Release struct {
	Flavor        string // sway, i3, cosmic, niri, agent or server
	Version       string // the isotope release tag, e.g. v0.1.0
	HadronVersion string // the Hadron base release, e.g. v0.6.3
	ImageRepo     string // e.g. ghcr.io/kairos-community/isotope
	GitHubRepo    string // e.g. kairos-community/isotope
	// GPU is the image line: GPUFull or GPUVM. Releases up to v0.0.7 wrote
	// no GPU key; Load reads them as GPUFull (see Line).
	GPU string
	// TrustedBoot is set on a Trusted Boot (UKI) image, whose release file
	// says TRUSTED_BOOT=true. Images built before the key was written have
	// none and are not Trusted Boot.
	TrustedBoot bool
}

// The two image lines a desktop flavor is released in. Each release pushes
// <flavor>-<tag> (full: Mesa's Intel and AMD drivers and linux-firmware, for
// real machines) and <flavor>-vm-<tag> (virtual GPUs and llvmpipe only, and
// no firmware: smaller, for virtual machines). The agent and the server
// edition have one line each, agent-<tag> and server-<tag>.
const (
	GPUFull = "full"
	GPUVM   = "vm"
	// LineUKI is the Trusted Boot line, <flavor>-uki-<tag>: a container
	// that holds the signed UKI built from the full line's image. A Trusted
	// Boot system can only upgrade to it (kairos-agent refuses a source
	// without a UKI), and a GRUB system must never install it.
	LineUKI = "uki"
)

// The flavors that are not desktops. The agent appliance has one line and
// no update service; the server edition has no desktop and one image line
// besides Trusted Boot.
const (
	FlavorAgent  = "agent"
	FlavorServer = "server"
)

// Line is the image line this system upgrades along. A release file without a
// GPU key comes from v0.0.7 or earlier: those were GPU=vm images, but their
// own isotope-update can only pull <flavor>-<tag>, which is the full line now,
// and the full image runs in a VM too. So they are on the full line. A
// Trusted Boot system is on the uki line whatever its GPU key says.
func (r Release) Line() string {
	if r.TrustedBoot {
		return LineUKI
	}
	if r.GPU == GPUVM {
		return GPUVM
	}
	return GPUFull
}

// LineTag is the image tag release.yml pushes for a flavor, line and release.
// The uki line includes the server edition; the agent has no uki line.
func LineTag(flavor, line, tag string) string {
	if line == LineUKI && flavor != FlavorAgent {
		return flavor + "-" + LineUKI + "-" + tag
	}
	if line == GPUVM && flavor != FlavorAgent && flavor != FlavorServer {
		return flavor + "-" + GPUVM + "-" + tag
	}
	return flavor + "-" + tag
}

// ImageFor is the OCI reference release.yml pushes for a tag of this
// system's flavor and line.
func (r Release) ImageFor(tag string) string {
	return fmt.Sprintf("%s:%s", r.ImageRepo, LineTag(r.Flavor, r.Line(), tag))
}

// File is the release file to read: DefaultFile, or the test's
// ISOTOPE_UPDATE_RELEASE_FILE.
func File() string {
	if p := os.Getenv("ISOTOPE_UPDATE_RELEASE_FILE"); p != "" {
		return p
	}
	return DefaultFile
}

// Load reads a KEY=value file. Values may be quoted. Every key the
// upgrade path needs must be present: a half-written file would otherwise make
// isotope-update pull an image reference like ":-v1.0.0".
func Load(path string) (Release, error) {
	kv, err := ReadKV(path)
	if err != nil {
		return Release{}, err
	}

	r := Release{
		Flavor:        kv["FLAVOR"],
		Version:       kv["VERSION"],
		HadronVersion: kv["HADRON_VERSION"],
		ImageRepo:     kv["IMAGE_REPO"],
		GitHubRepo:    kv["GITHUB_REPO"],
		GPU:           kv["GPU"],
	}
	switch r.GPU {
	case "", GPUFull, GPUVM:
	default:
		return Release{}, fmt.Errorf("%s: GPU is %q, want %s or %s", path, r.GPU, GPUFull, GPUVM)
	}
	// A wrong value must not guess a line: on a Trusted Boot system a
	// normal image would be offered, which kairos-agent cannot install.
	switch tb := kv["TRUSTED_BOOT"]; tb {
	case "", "false":
	case "true":
		r.TrustedBoot = true
	default:
		return Release{}, fmt.Errorf("%s: TRUSTED_BOOT is %q, want true or false", path, tb)
	}
	for name, v := range map[string]string{
		"FLAVOR": r.Flavor, "VERSION": r.Version,
		"IMAGE_REPO": r.ImageRepo, "GITHUB_REPO": r.GitHubRepo,
	} {
		if v == "" {
			return Release{}, fmt.Errorf("%s: %s is not set", path, name)
		}
	}
	return r, nil
}

// ReadKV reads a KEY=value file, the format of /etc/isotope/release
// and /etc/isotope/session. Blank lines and # comments are skipped;
// values may be quoted.
func ReadKV(path string) (map[string]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	kv := map[string]string{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		kv[strings.TrimSpace(k)] = strings.Trim(strings.TrimSpace(v), `"'`)
	}
	return kv, sc.Err()
}

// semver is the subset of semantic versioning release tags use: vMAJOR.MINOR.PATCH
// with an optional -prerelease. Build metadata is ignored.
type semver struct {
	major, minor, patch int
	pre                 string
}

func parseSemver(s string) (semver, bool) {
	s = strings.TrimPrefix(s, "v")
	s, _, _ = strings.Cut(s, "+")
	core, pre, _ := strings.Cut(s, "-")
	parts := strings.Split(core, ".")
	if len(parts) != 3 {
		return semver{}, false
	}
	var n [3]int
	for i, p := range parts {
		v, err := strconv.Atoi(p)
		if err != nil || v < 0 {
			return semver{}, false
		}
		n[i] = v
	}
	return semver{n[0], n[1], n[2], pre}, true
}

// IsTag says whether s is a release tag like v1.2.3 (or v1.2.3-rc1).
func IsTag(s string) bool {
	_, ok := parseSemver(s)
	return ok
}

// Newer reports whether candidate is a later release than current. A current
// version that does not parse (a dev build) is always older than a real
// release, so a dev box is offered the latest one.
func Newer(candidate, current string) bool {
	c, ok := parseSemver(candidate)
	if !ok {
		return false
	}
	cur, ok := parseSemver(current)
	if !ok {
		return true
	}
	if c.major != cur.major {
		return c.major > cur.major
	}
	if c.minor != cur.minor {
		return c.minor > cur.minor
	}
	if c.patch != cur.patch {
		return c.patch > cur.patch
	}
	// Same core: a release beats its own prereleases.
	switch {
	case c.pre == cur.pre:
		return false
	case c.pre == "":
		return true
	case cur.pre == "":
		return false
	default:
		return c.pre > cur.pre
	}
}
