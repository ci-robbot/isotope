# Programs Built for glibc Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A glibc program or AppImage that a user starts on an Isotope desktop
runs. A glibc one runs in a per-user Ubuntu LTS Distrobox created on first use.
A musl or static AppImage runs natively. Where it cannot run, the user gets one
clear sentence.

**Architecture:** A static stand-in loader at `/lib64/ld-linux-x86-64.so.2`
hands glibc ELF programs to `isotope glibc run`. A binfmt rule hands AppImages
to the same command, which extracts them with a pure-Go squashfs reader and
picks native or box from the ELF interpreters inside. `isotope glibc` drives
Distrobox on Docker.

**Tech Stack:** C (static, musl) for the loader, Go (`cmd/isotope`, pure Go),
Distrobox 1.8 on Docker, systemd-binfmt, bash image checks.

**Spec:** `docs/superpowers/specs/2026-11-23-glibc-programs-design.md` (read
"Calls made after the builder check"; they bind).

## Global Constraints

- Box name `isotope-glibc`; image `docker.io/library/ubuntu:26.04` from
  `ARG GLIBC_BOX_IMAGE` (Renovate, even-year `.04` tags only), written as
  `image=<ref>` to `/usr/share/isotope/glibc.conf`; Go falls back to the same
  default.
- Loader path `/usr/lib/ld-linux-x86-64.so.2` (`/lib64` -> `usr/lib`), static,
  no libraries; exit 127 with a sentence when it cannot hand off; loop guard env
  `ISOTOPE_GLIBC_LOADER_ACTIVE`; per-user opt-out `$XDG_CONFIG_HOME/isotope/glibc`
  `auto=off` and `ISOTOPE_GLIBC_AUTO=0`.
- binfmt rule exactly `:isotope-appimage:M:8:\x41\x49\x02::/usr/libexec/isotope-appimage:F`
  in `/usr/lib/binfmt.d/isotope-appimage.conf`; `systemd-binfmt.service` enabled.
- AppImage cache `$XDG_CACHE_HOME/isotope/appimages/<sha256>/`, pruned after 30
  days unused and on `isotope glibc reset`; `APPDIR`, `APPIMAGE`, `ARGV0` set.
- Box create lock `$XDG_RUNTIME_DIR/isotope/glibc.lock`; wait up to 30 s for
  `/run/docker.sock`.
- Notifications through `cmd/isotope`'s D-Bus `Notify`, never `notify-send`.
- Ships on the four desktop variants only: not the server, not the agent
  (Dockerfile.agent removes it).
- `cmd/isotope` stays pure Go; a new shared package goes in `pkg/` only if two
  programs need it.
- Every new Go package is in a CI job (`scripts/check-go-coverage.sh`) and every
  Go build stage copies what it imports (`scripts/check-go-stage-copies.sh`).
- Commits: `git -c user.name="Ettore Di Giacinto" -c user.email=mudler@kairos.io commit -s`,
  conventional subjects, last line `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`.
  No bare `git stash`, no push, no image builds, no docker prune.

## Review Focus

1. A glibc program started from inside the box must never come back to the
   stand-in (loop): the env guard, and the box having its own loader. Test in
   Task 2 (`TestRunSetsLoaderGuard`) and Task 1 (guard set → message, exit 127).
2. Arguments with spaces, quotes, newlines and a leading `-` reach the program
   unchanged through loader → isotope → distrobox → `bash -c 'exec -a "$0" "$@"'`
   (bash, not sh: Ubuntu's sh is dash, whose `exec` has no `-a`).
   Test in Task 1 and Task 2 (`TestRunArgvRoundTrip`).
3. A malicious or corrupt AppImage (squashfs entries with `..`, absolute
   symlinks pointing out of the cache, a huge file) must not write outside its
   cache directory. Test in Task 3 (`TestExtractRefusesEscapingPaths`).
4. Two programs started together on a fresh account create the box once.
   Test in Task 2 (`TestCreateIsLocked`).
5. No ELF the image itself ships names `ld-linux-x86-64.so.2` as interpreter,
   so nothing of Isotope's own is rerouted. Check in Task 4 (`check-glibc.sh`).

---

### Task 1: The stand-in loader

**Files:**
- Create: `tools/isotope-glibc-loader.c`, `test/glibc/test-loader.sh`
- Modify: `Dockerfile` (a `glibc-loader` stage like `isotope-gpu-probe`'s,
  built `-static -O2 -Wall -Wextra -Werror`, copied to
  `/usr/lib/ld-linux-x86-64.so.2` in `desktop-default`), `.github/workflows/ci.yml`
  (run `test/glibc/test-loader.sh`, which compiles the loader with the runner's
  cc or in a musl container, as the gpu-probe test does if it has one).

**Interfaces:**
- Produces: the exec contract
  `/usr/bin/isotope glibc run --loader -- <abs program path> <argv[1..]>` with
  `ISOTOPE_GLIBC_ARGV0=<original argv[0]>` in the environment.

- [ ] **Step 1:** `test/glibc/test-loader.sh` builds the loader and a fake
  `isotope` (records its argv and env to a file), and links a test glibc-style
  program whose interpreter is the built loader (`gcc -Wl,--dynamic-linker=<path>`
  or `patchelf --set-interpreter`, or run the loader directly). Cases:
  kernel-started (AT_EXECFN path) → fake isotope sees `glibc run --loader -- <abs path> a "b c" $'d\ne' -x`
  and `ISOTOPE_GLIBC_ARGV0`; direct invocation `loader ./prog args` → same;
  relative path made absolute against the cwd; uid 0 (skip unless root),
  `/run/isotope/live-desktop` (use an override env for the marker path in
  tests only, named `ISOTOPE_GLIBC_LOADER_TEST_ROOT`, compiled in only with
  `-DTESTING`), guard env set, `ISOTOPE_GLIBC_AUTO=0`, `auto=off` in the config
  file, and no `/usr/bin/isotope` → each prints its sentence on stderr and
  exits 127.
- [ ] **Step 2:** Run it; it fails (no loader).
- [ ] **Step 3:** Implement `tools/isotope-glibc-loader.c` (getauxval(AT_EXECFN);
  realpath against cwd; refusals; execve of isotope with the program path,
  argv, env plus `ISOTOPE_GLIBC_ARGV0`). Sentences: say what the program is, why
  it cannot run here, what to do (`isotope glibc --help`).
- [ ] **Step 4:** The test passes; shellcheck passes; the Dockerfile stage
  builds the binary statically (`! ldd` output; `file`-free check: the ELF has
  no PT_INTERP — test with `readelf -l` in the stage).
- [ ] **Step 5:** Commit `feat(glibc): a stand-in glibc loader that hands programs to isotope glibc`.

### Task 2: `isotope glibc` — the box

**Files:**
- Create: `cmd/isotope/glibc.go`, `cmd/isotope/glibc_test.go`
- Modify: `cmd/isotope/main.go` (the `glibc` subcommand and its help).

**Interfaces:**
- Consumes: Task 1's exec contract.
- Produces: `glibcRun(opts runOpts, file string, args []string) int` used by
  Task 3 for the box path; `runOpts{Loader, AppImage, Native, Box bool}`;
  subcommands `run`, `create`, `enter`, `status`, `reset`, `auto on|off`.

- [ ] **Step 1:** Tests against a fake `distrobox`/`docker` on PATH and a temp
  `XDG_*`: `TestRunRefuses` (root, live marker, no docker socket after the wait
  — make the wait injectable, user not in docker group in this session vs in
  `/etc/group`), `TestCreateThenRun` (exact `distrobox create --yes --name isotope-glibc --image <image>`
  then `distrobox enter isotope-glibc -- bash -c 'exec -a "$0" "$@"' <argv0> <path> <args>`),
  `TestRunArgvRoundTrip`, `TestRunSetsLoaderGuard`, `TestPathMapping`
  (`$HOME/...` unchanged, the shared dirs unchanged, others under `/run/host`),
  `TestCreateIsLocked` (two concurrent runs, one create), `TestFailedCreateCleansUp`,
  `TestPrunedBoxIsRecreated`, `TestNoTerminalNotifies` (fake Notify),
  `TestImageFromConf` (conf, and default when missing), `TestAutoOff`.
- [ ] **Step 2:** Run them; they fail.
- [ ] **Step 3:** Implement. Check which paths distrobox 1.8.2.5 shares
  (read its `distrobox-create` source in the Dockerfile stage's version) and
  encode them in the path mapping with a comment citing it.
- [ ] **Step 4:** `go test -race ./cmd/isotope/...` passes; `go vet`; gofmt.
- [ ] **Step 5:** Commit `feat(glibc): isotope glibc runs glibc programs in an Ubuntu LTS Distrobox`.

### Task 3: AppImages

**Files:**
- Create: `cmd/isotope/appimage.go`, `cmd/isotope/appimage_test.go`,
  `cmd/isotope/testdata/appimage/` (small test AppImages built by a test
  helper at test time, not binaries committed, unless tiny),
  `rootfs/common/usr/lib/binfmt.d/isotope-appimage.conf`,
  `rootfs/common/usr/libexec/isotope-appimage`.
- Modify: `go.mod`/`go.sum` (a pure-Go squashfs reader; pick a maintained one,
  check its licence is compatible with Apache-2.0, and say which in the report).

**Interfaces:**
- Consumes: Task 2's `glibcRun` for the box path.
- Produces: `appimageOffset(f *os.File) (int64, error)`,
  `extractAppImage(path, cacheDir string) (appDir string, err error)`,
  `needsGlibc(appDir string) (bool, error)`.

- [ ] **Step 1:** Tests: `TestAppImageOffset` (end of section headers of a
  built ELF + appended squashfs), `TestExtractCachesBySHA`,
  `TestExtractRefusesEscapingPaths` (`..`, absolute paths, symlink escaping),
  `TestNeedsGlibc` (AppRun symlink to a glibc ELF → true; musl ELF → false;
  static → false; script AppRun over usr/bin ELFs, one glibc → true),
  `TestRunAppImageNative` (sets APPDIR, APPIMAGE, ARGV0; no docker needed),
  `TestRunAppImageBox` (goes through glibcRun with the cache path),
  `TestNativeBoxOverride`, `TestCachePrune` (30 days). Build the test squashfs
  images with the chosen library's writer or `mksquashfs` if the CI runner has
  squashfs-tools (install it in the CI step if needed).
- [ ] **Step 2:** They fail.
- [ ] **Step 3:** Implement; the handler script is
  `exec /usr/bin/isotope glibc run --appimage -- "$@"`.
- [ ] **Step 4:** Tests pass with `-race`; shellcheck on the handler.
- [ ] **Step 5:** Commit `feat(glibc): AppImages run natively or in the box, by what they contain`.

### Task 4: Image wiring, checks and docs

**Files:**
- Create: `test/glibc/check-glibc.sh` (repo mode and `IMAGE` mode), the help
  page (the builder's call: a `docs/` page and a short section in `isotope glibc --help`).
- Modify: `Dockerfile` (`ARG GLIBC_BOX_IMAGE` with its renovate comment;
  `desktop-default` writes `glibc.conf`; `systemd-binfmt.service` enabled the
  way other units are linked there; the loader copied in), `renovate.json`
  (the even-year LTS rule for `docker.io/library/ubuntu`), `Dockerfile.agent`
  (remove the loader, the binfmt rule, the handler and the conf, with `! test -e`
  checks), the `server-config` stage unchanged (the server gets none of it),
  `.github/workflows/desktop-images.yml` and `release.yml` (run
  `check-glibc.sh IMAGE` on desktops; on the agent image check absence),
  `ci.yml` (repo mode), `AGENTS.md` (a "Programs built for glibc" row; the
  server table says it has none), `README.md` (one line).

- [ ] **Step 1:** `check-glibc.sh`: the loader exists at both paths, is static
  (no PT_INTERP), `glibc.conf` names an even-year `.04` Ubuntu image, the binfmt
  file has exactly the rule, `systemd-binfmt.service` is enabled, no ELF under
  `/usr` names `ld-linux-x86-64.so.2` as interpreter (scan with a small loop
  using `readelf -l` from the runner on the extracted image, or a Go/python
  scanner), and a real glibc binary (copied from the pinned Ubuntu image's
  `/usr/bin/true`) started on the image with a fake `/usr/bin/isotope` reaches
  `glibc run --loader -- <path>`. On the agent and server images: none of the
  files exist.
- [ ] **Step 2:** It fails in repo mode.
- [ ] **Step 3:** Wire the Dockerfiles, renovate, workflows and docs.
- [ ] **Step 4:** `check-glibc.sh --repo` passes; actionlint; shellcheck;
  `docker buildx build --call check` on both Dockerfiles; the COSMIC stage keys
  are unchanged against main (`python3 tools/stage-key.py --arg GPU=full cosmic-shell`
  and the other three); `scripts/check-go-coverage.sh`;
  `scripts/check-go-stage-copies.sh`; `scripts/check-renovate-pins.sh`.
- [ ] **Step 5:** Commit `feat(glibc): ship the glibc loader and AppImage handler on the desktops`.

### Task 5: The Compatibility page in Isotope System

Added at the maintainer's request ("nice if the user can set this compatibility
(auto/on/off) in the isotope system settings"; the names are theirs: the page is "Compatibility", the setting on it "Glibc compatibility").

**Files:**
- Create: `cmd/isotope-system/page_compat.go`, `cmd/isotope-system/compat.go`,
  their tests and screenshot goldens.
- Modify: the page list and `isotope system [PAGE]` (`compatibility`), the
  live-system gating (`cmd/isotope-system/live.go`: not available on live),
  `cmd/isotope/glibc.go` only if a machine-readable status is needed
  (`isotope glibc status --json`), AGENTS.md (the Isotope System row and the
  glibc row), `test/system/check-system-menu.sh` if it lists pages.

**Interfaces:**
- Consumes: Task 2's per-user setting file `$XDG_CONFIG_HOME/isotope/glibc`
  (`auto=on|off`, default on) and its `status`, `create` and `reset`
  subcommands; Task 3's cache, which reset prunes.
- Produces: a page titled "Compatibility" (sidebar entry too), whose first
  section is "Glibc compatibility", with:
  - a switch "Run programs built for other Linux systems automatically" that
    writes `auto=` through the same code path as `isotope glibc auto` (the
    file format must stay one implementation: call `isotope glibc auto on|off`
    or share a package in `pkg/` if both programs need it — the builder's call,
    stated in the report);
  - "Glibc environment": Not set up yet / Ready, its image and its size, from
    `isotope glibc status --json`; "Set up now" runs `isotope glibc create`
    with progress; "Reset…" confirms, runs `isotope glibc reset`, and shows its
    "in use, close those programs" refusal as a message, not an error dialog.

- [ ] **Step 1:** Tests: the switch round trip (file written, read back, a
  missing file reads as on), the status states from a fake `isotope` (absent,
  ready, error), "Set up now" and "Reset" call the right commands, live shows
  the not-available text, and screenshot goldens for the page's states (other
  pages' goldens unchanged).
- [ ] **Step 2:** They fail.
- [ ] **Step 3:** Implement, following the existing pages' style.
- [ ] **Step 4:** `go test` for `cmd/isotope-system` and `cmd/isotope` with
  their tags, vet, gofmt, the coverage and stage-copy checks.
- [ ] **Step 5:** Commit `feat(system): a Compatibility page with the glibc setting`.
