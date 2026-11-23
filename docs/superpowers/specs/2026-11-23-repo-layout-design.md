# Repository layout: one Go module, cmd/ and pkg/, rootfs/<variant>

## Why

The top of the repository mixes seven Go modules (wired with `replace ../x`)
and seven `rootfs*` folders. Programs and libraries sit together (`update/`
is the `isotope` program and also holds `isorelease` and `recoverykey`,
which `system` imports). The maintainer wants a conventional layout: programs
under `cmd/`, shared libraries under `pkg/`, image trees under `rootfs/`.

## Goals

- One Go module at the root, `github.com/kairos-community/isotope`, with no
  `replace` lines.
- Programs in `cmd/<binary>/`, shared libraries in `pkg/<name>/`, the agent's
  private packages in `internal/agent/`.
- Image trees in `rootfs/common/` and `rootfs/<variant>/`.
- No behaviour change: the same binaries, built with the same tags, ship at
  the same paths in the images.

## Non-goals

- No change to `applets/` (Rust), `splash/` (C), `theme/`, `extensions/`,
  `auroraboot/`, `test/`, `docs/`, `scripts/`, `tools/`, `patches/`.
- No code changes beyond import paths, package names that must change, and
  relative paths in tests.

## Anti-goals

- Moving `tools/cosmic-build.sh` or `patches/cosmic/`: they feed the COSMIC
  prebuild stage keys (`tools/stage-key.py`), and a new key means a
  multi-hour COSMIC rebuild. The keys must be the same before and after.

## Approaches

1. **One root module** (picked by the maintainer): no replace wiring, one
   `go.sum`, plain `cmd/` + `pkg/`. A dependency bump re-runs every Go build
   stage, which costs minutes.
2. One module per program under `cmd/` and `pkg/`, with a `go.work`: each
   program keeps its own dependency set, but the seven `go.mod` files and
   their replace wiring stay.

## The layout

| Today | After |
|---|---|
| `update/` (package main) | `cmd/isotope/` |
| `update/isorelease/` | `pkg/isorelease/` |
| `update/recoverykey/` | `pkg/recoverykey/` |
| `system/` | `cmd/isotope-system/` |
| `welcome/` | `cmd/isotope-welcome/` |
| `install-ui/` (package main) | `cmd/isotope-install-ui/` |
| `install-ui/core/` | `pkg/installer/` (package `installer`) |
| `install-gui/` | `cmd/isotope-install-gui/` |
| `guikit/` | `pkg/guikit/` |
| `agent/cmd/isotope-agent/` | `cmd/isotope-agent/` |
| `agent/internal/*` | `internal/agent/*` |
| `agent/` other files (README, testdata, scripts) | next to what they describe, under `internal/agent/` or `cmd/isotope-agent/` |
| `rootfs/` | `rootfs/common/` |
| `rootfs-<v>/` for sway, i3, niri, cosmic, server, agent | `rootfs/<v>/` |

Test data moves with its package (`testdata/` stays beside the tests). The
golden files that `update` reads from `../install-ui/core/testdata` are read
from `pkg/installer/testdata` through the new relative path.

## What has to follow the move

- Every Go import path; `go.mod`/`go.sum` merged at the root (`go mod tidy`
  on the merged module; the dependency versions each module pinned must not
  go down: take the highest).
- Dockerfile, Dockerfile.agent: `COPY` sources, build stage `WORKDIR`s and
  `go build` package paths, the build tags per binary unchanged.
- Makefile, workflows (paths filters, `go test` and `go vet` jobs, the
  agent-gates paths), renovate (`gomod` manager finds the root module).
- Test scripts and Go tests that name repository paths.
- AGENTS.md (CLAUDE.md), README, docs/DEVELOPMENT.md, and other docs that
  name the moved paths. Old specs in `docs/superpowers/` keep their history
  as written.

## Checks that prove it

- `go build ./...`, `go vet` and `go test` with each binary's tags in the
  CGO container, as CI does.
- The test scripts' repo modes (`check-live.sh --repo` and the others).
- `python3 tools/stage-key.py` gives the same COSMIC keys on both lines as
  on main.
- `docker buildx build --call check` on both Dockerfiles, actionlint,
  shellcheck.
- No reference to an old path is left (`git grep` for `rootfs-`, `install-ui/`,
  `guikit/`, and so on, outside old specs and changelogs).
- The image builds in the PR's CI are the final proof.

## Left to the builder

- The order of the moves and the commit split (one commit per area is
  easier to review than one big commit).
- Where the agent's non-Go files go, as long as the agent image and its
  tests still find them.
