# Repository Layout Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Move the repository to one Go module with `cmd/`, `pkg/` and
`internal/agent/`, and the image trees to `rootfs/common` and
`rootfs/<variant>`, with no change to what the images ship.

**Architecture:** Pure moves (`git mv`) plus the path and import rewrites
they force. Each task leaves the tree building and its checks green.

**Tech Stack:** Go (Gio, cgo for the GUIs), Docker/BuildKit, GitHub
Actions, bash and Python tooling.

**Spec:** `docs/superpowers/specs/2026-11-23-repo-layout-design.md`

## Global Constraints

- Module path: `github.com/kairos-community/isotope`, one `go.mod` at the
  root, no `replace` lines.
- Layout, exactly as the spec's table: `cmd/isotope`, `cmd/isotope-system`,
  `cmd/isotope-welcome`, `cmd/isotope-install-ui`, `cmd/isotope-install-gui`,
  `cmd/isotope-agent`, `pkg/guikit`, `pkg/installer` (package `installer`),
  `pkg/isorelease`, `pkg/recoverykey`, `internal/agent/<pkg>`,
  `rootfs/common`, `rootfs/{sway,i3,niri,cosmic,server,agent}`.
- Do not move `tools/cosmic-build.sh` or `patches/cosmic/`. The COSMIC stage
  keys from `python3 tools/stage-key.py` must equal main's on both lines
  (full: `cosmic-shell-3f4b2d6609ec`/`cosmic-apps-e08a57d2e323` at the time of
  writing; compare against main, not these literals).
- No behaviour change: same binaries, same build tags per binary, same
  install paths in the images.
- `cmd/isotope` stays pure Go (`CGO_ENABLED=0`): it must not import Gio or
  anything cgo.
- Use `git mv` so history follows the files.
- Commits: `git -c user.name="Ettore Di Giacinto" -c user.email=mudler@kairos.io commit -s`,
  conventional subjects, message ends with
  `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`.
- Never a bare `git stash`. Do not push. Disk is tight: no image builds, no
  docker prune.

## Review Focus

1. A Go build stage in the Dockerfile that now copies the whole repository
   would rebuild on any unrelated change: each stage copies `go.mod`,
   `go.sum` and only the directories its binary imports. Check by reading
   each Go stage.
2. Build tags per binary: `isotope-install-gui` is still built twice
   (`-tags nox11,novulkan` → Wayland, `-tags novulkan` → X11), welcome and
   system twice as before. Check by diffing the `go build` lines against main.
3. `cmd/isotope` must stay cgo-free: `go list -deps ./cmd/isotope` lists no
   `gioui.org` package. Added as a check in Task 2.
4. `-ldflags -X` paths in Dockerfile.agent name the agent's buildinfo
   package; after the move they must name
   `github.com/kairos-community/isotope/internal/agent/buildinfo`, or the
   version silently reads empty. Check in Task 2.
5. Generators that write into the image trees (`tools/gen-theme.py`,
   `tools/gen-shortcuts.py`, `tools/gen-grub-bg.py`) must write to the new
   paths, and their `--check` modes must pass. Check in Task 1.

---

### Task 1: The image trees under rootfs/

**Files:**
- Move: `rootfs/` → `rootfs/common/`; `rootfs-{sway,i3,niri,cosmic,server,agent}/` → `rootfs/{sway,i3,niri,cosmic,server,agent}/`
- Modify: every file that names these paths. Find them with
  `git grep -nE 'rootfs(-(sway|i3|niri|cosmic|server|agent))?/'`. Expect:
  Dockerfile, Dockerfile.agent, Makefile, `.github/workflows/*.yml` (paths
  filters and steps), `test/**`, `tools/*.py` and their tests,
  `scripts/*.sh`, Go tests that read the repository's rootfs (for example
  `system/model_test.go`).
- Do not edit: old specs and plans in `docs/superpowers/` (history).
  AGENTS.md and current docs are Task 3.

**Interfaces:**
- Produces: the paths `rootfs/common/...` and `rootfs/<variant>/...` that
  Tasks 2 and 3 use.

- [ ] **Step 1:** Do the moves. Moving `rootfs/` into `rootfs/common/` needs
  a two-step move: `git mv rootfs rootfs-common-tmp && mkdir rootfs && git mv rootfs-common-tmp rootfs/common`,
  then `git mv rootfs-<v> rootfs/<v>` for each.
- [ ] **Step 2:** Rewrite the references. In the Dockerfile, `COPY rootfs/ /`
  becomes `COPY rootfs/common/ /`, `COPY rootfs-sway/ /` becomes
  `COPY rootfs/sway/ /`, and the one-by-one `COPY rootfs/<file>` lines in
  `server-config` become `rootfs/common/<file>`. A blind `sed` of `rootfs/`
  would double-rewrite `rootfs/sway`, so rewrite the variant names first and
  read each hit.
- [ ] **Step 3:** Verify:
  - `git grep -nE 'rootfs-(sway|i3|niri|cosmic|server|agent)\b'` outside `docs/superpowers/` returns nothing.
  - `git grep -nE '(^|[^/a-z])rootfs/(etc|usr|system|var)\b'` outside `docs/superpowers/` returns nothing (those are now `rootfs/common/...`).
  - `python3 tools/gen-theme.py --check`, `python3 tools/gen-shortcuts.py --check` (or the script's own check mode) and `python3 -m unittest discover -s tools -p 'test_*.py'` pass.
  - The repo-mode test scripts pass: `test/live/check-live.sh --repo`, `test/live/test-live-stage.sh`, `test/live/test-not-live.sh`, `test/live/test-live-install.sh`, `test/system/check-system-menu.sh`, `test/graphics/check-gpu-fallback.sh`, and every other `test/**` script that runs without an image. Run what CI's repo-level job runs (read `ci.yml`).
  - The Go tests that read rootfs paths pass (run them in place; their module moves in Task 2).
  - `python3 tools/stage-key.py` (both GPU lines) gives the same keys as on main.
  - `docker buildx build --call check .` and the same for `-f Dockerfile.agent --build-arg BASE_IMAGE=alpine:3.22`, plus actionlint, are clean.
- [ ] **Step 4:** Commit: `refactor: move the image trees under rootfs/ (common and one per variant)`.

### Task 2: One Go module with cmd/, pkg/ and internal/agent

**Files:**
- Moves, as in the spec's table. `update/` holds package main at its root
  plus `isorelease/` and `recoverykey/`: move those two first to `pkg/`,
  then the rest of `update/` to `cmd/isotope/`. `install-ui/core/` →
  `pkg/installer/` (rename `package core` to `package installer` and every
  `core.` qualifier at the call sites), then the rest of `install-ui/` →
  `cmd/isotope-install-ui/`. `agent/cmd/isotope-agent/` → `cmd/isotope-agent/`,
  `agent/internal/*` → `internal/agent/*`. The agent's other files (README,
  scripts, testdata) go beside what uses them; a README describing the agent
  goes to `internal/agent/README.md`.
- Create: root `go.mod`, `go.sum`. Delete: the seven module `go.mod`/`go.sum`.
- Modify: imports in every Go file; relative paths in Go tests
  (`../install-ui/core/testdata` and so on); Dockerfile Go stages;
  Dockerfile.agent's `agent-gobuild` stage and its `-X` ldflags;
  `.github/workflows/ci.yml` Go jobs; `agent-gates.yml` and
  `desktop-images.yml` path filters; Makefile Go targets; `renovate.json`
  if it names module directories; `.gitignore` entries for moved paths.

**Interfaces:**
- Consumes: Task 1's `rootfs/...` paths.
- Produces: import paths `github.com/kairos-community/isotope/pkg/guikit`,
  `.../pkg/installer`, `.../pkg/isorelease`, `.../pkg/recoverykey`,
  `.../internal/agent/<pkg>`.

- [ ] **Step 1:** Write the root `go.mod`. Take the highest `go` and
  `toolchain` lines of the seven, and for every dependency the highest
  version any of them required. Then run `go mod tidy` in the CGO
  container (below). A version that goes down is a bug.
- [ ] **Step 2:** Do the moves and rewrite the imports. Run `gofmt -l .`
  (no output) afterwards.
- [ ] **Step 3:** Rewrite the Dockerfile Go stages. Each stage copies
  `go.mod go.sum` and then only the directories its binary needs: for
  example `install-gui-build` copies `cmd/isotope-install-gui`,
  `pkg/guikit` and `pkg/installer`. `update-build` copies
  `cmd/isotope`, `pkg/isorelease`, `pkg/recoverykey` and the
  `pkg/installer/testdata` golden files its tests read. Run `go build`
  and `go test` from `/src` on `./cmd/<binary>` with the same tags and
  output paths as before. Dockerfile.agent copies `go.mod go.sum
  cmd/isotope-agent internal/agent`, builds `./cmd/isotope-agent`, and its
  `-X` paths become `github.com/kairos-community/isotope/internal/agent/buildinfo.<Var>`.
- [ ] **Step 4:** Rewrite CI. Root-level `./...` pulls the Gio packages into
  the pure-Go jobs, so each job names its package patterns. The CLI and the
  daemon run `./cmd/isotope/... ./pkg/isorelease/... ./pkg/recoverykey/...`.
  install-ui runs `./cmd/isotope-install-ui/... ./pkg/installer/...`
  (`-race` as before). The agent runs `./cmd/isotope-agent/... ./internal/agent/...`.
  The GUI jobs keep their container and tags on `./pkg/guikit/...`,
  `./cmd/isotope-install-gui/...`, `./cmd/isotope-welcome/...` and
  `./cmd/isotope-system/...`. Keep each job's flags (`-race`, tags,
  env such as `ISOTOPE_GUI_SKIP_SCREENSHOTS`) as on main.
- [ ] **Step 5:** Verify:
  - In `golang:1.25-alpine` with the GUI packages (`apk add gcc musl-dev pkgconf wayland-dev libxkbcommon-dev mesa-dev vulkan-headers`, plus the X11 headers the i3 builds use, as ci.yml does), every CI Go job's `go vet` and `go test` command passes as rewritten in Step 4.
  - The screenshot goldens are unchanged.
  - `CGO_ENABLED=0 go build ./cmd/isotope ./cmd/isotope-install-ui ./cmd/isotope-agent` passes.
  - `go list -deps ./cmd/isotope | grep -c gioui.org` prints `0`.
  - `go build -tags nox11,novulkan ./cmd/isotope-install-gui ./cmd/isotope-welcome ./cmd/isotope-system` and the `-tags novulkan` builds pass.
  - `grep -rn 'replace ' go.mod` prints nothing.
  - `git grep -nE 'isotope/(update|system|welcome|install-ui|install-gui|guikit|agent)(/|")'` in Go files prints nothing.
  - The Task 1 checks still pass (stage keys, repo-mode scripts, buildx check, actionlint).
  - A Go build stage reproduced in a Docker-stage-shaped copy builds and tests: copy only what the stage copies into a temp dir and run its commands there, for `update-build` and `install-gui-build` at least.
- [ ] **Step 6:** Commit, as one commit or a few (module merge, moves,
  build wiring). Subjects such as `refactor: one Go module with cmd/ and pkg/`.

### Task 3: Docs and the final sweep

**Files:**
- Modify: `AGENTS.md` (CLAUDE.md is a link to it; check with `ls -l`),
  `README.md`, `docs/DEVELOPMENT.md`, `extensions/README.md`,
  `splash/README.md`, `internal/agent/README.md`, and any other current doc
  naming an old path. Do not edit dated specs and plans in
  `docs/superpowers/`.
- Modify: AGENTS.md "Adding a variant" (`rootfs/<variant>/`) and the agent
  section, so they name the new layout. Add a short "Repository layout"
  section listing `cmd/`, `pkg/`, `internal/agent/` and `rootfs/`, with one
  line on what goes where: a new program goes in `cmd/<binary>`, shared code
  in `pkg/`, and variant files in `rootfs/<variant>`.

- [ ] **Step 1:** Rewrite the docs.
- [ ] **Step 2:** Final sweep: `git grep -nE '(^|[^a-z/])(update|system|welcome|install-ui|install-gui|guikit|agent)/[A-Za-z_.-]+\.(go|md)|rootfs-(sway|i3|niri|cosmic|server|agent)'`
  outside `docs/superpowers/` and `.git`. Every hit is either fixed or is
  an image path such as `/usr/...`, not a repository path.
- [ ] **Step 3:** Run the complete local check set from Tasks 1 and 2 once
  more on the final tree.
- [ ] **Step 4:** Commit: `docs: the new repository layout`.
