# Isotope look follow-ups Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development. Steps use checkbox (`- [ ]`) syntax.

**Goal:** Close the follow-ups left by the Isotope look (PR #129) on the same branch, `feat/isotope-look`.

**Architecture:** Each follow-up is a small independent change to an existing part: Welcome (Gio), the Isotope menu (fuzzel), the COSMIC theme overlay (gen-theme.py `COSMIC_LOOK`), the COSMIC panel/workspaces applet, the COSMIC portal config, and CI.

**Spec:** `docs/superpowers/specs/2026-11-20-isotope-look-design.md` (the look's spec; these tasks finish it).

## Global Constraints

- Spec non-goal: no restyle of sway, i3 or niri beyond icons. A change to a shared file (`rootfs/`) that changes the look of those variants is out; put COSMIC-only things in `rootfs-cosmic/` or behind a COSMIC check.
- Colours only from `theme/palette.toml` through `tools/gen-theme.py`; `python3 tools/gen-theme.py --check` must pass.
- Every change that a built image shows gets a line in `test/look/check-look.sh` when it can be checked statically.
- A new or changed file under `patches/cosmic/` changes the patch hash and recompiles every COSMIC component (2-3 h). Prefer config over a patch; use a patch only when no config can do it, and say why in its header (header rule in `patches/cosmic/README.md`).
- Commits: `git -c user.name="Ettore Di Giacinto" -c user.email=mudler@kairos.io commit -s`, message ends with `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`. Never bare `git stash`.

## Review Focus

1. Welcome on a small screen (1280x800, and 1366x768) must fit with its buttons reachable above the dock.
2. Welcome on sway/i3/niri must still have usable window controls (or none, where the WM has none).
3. The Isotope menu on sway/i3/niri must look as before.
4. Light mode text on the quick-settings popup must keep the contrast floor.
5. Flatpak apps on COSMIC must keep the dark/light colour scheme from the COSMIC portal after the GTK portal is added.

---

### Task 1: Welcome window fits and matches the look

The VM showed Isotope Welcome (`welcome/main.go:109`: `app.Size(1100dp, 720dp)`, `app.Decorated(true)`) on COSMIC with a blue Gio fallback title bar, buttons on the right, and a window larger than a 1280x800 screen (the dock covers its "Set up as new" card).

**Files:** `welcome/main.go`, `guikit/` (if the title bar is shared), tests next to them.

- [ ] Find why Gio draws its own decorations on COSMIC (does cosmic-comp offer xdg-decoration server-side, and does Gio ask for it?). If server-side decorations are available, use them. Otherwise draw an Isotope-styled title bar in `guikit` (palette colours, round buttons on the left on COSMIC, as the rest of the look; right elsewhere is acceptable) and set `app.Decorated(false)`.
- [ ] Make the window fit: a default size that fits 1280x800 minus the top bar and dock, a `MinSize`, and content that scrolls instead of growing the window. Check the dp-to-px scale assumptions.
- [ ] Tests: unit tests for any pure layout/size logic (e.g. button order, size clamping). Build with the tags the Dockerfile uses (`nox11,novulkan`) in a container that has the Gio cgo headers.
- [ ] Commit.

### Task 2: Isotope menu styled on COSMIC

The Isotope menu (fuzzel, `rootfs/etc/xdg/fuzzel/fuzzel.ini`, opened by `isotope-menu`) is an 850 px wide, flat dark list with plain fonts and no rounded selection chip. On COSMIC it should match the look: rounded corners, the interface font (Inter Variable), comfortable padding and line height, a rounded selection chip in the accent, a narrower width, frosted-looking translucent background.

**Files:** a COSMIC-only fuzzel config (e.g. `rootfs-cosmic/etc/xdg/fuzzel/fuzzel.ini` overriding the shared one, or `isotope-menu` passing `--config` on COSMIC — pick the one that does not touch sway/i3/niri), `tools/gen-theme.py` rules for its colours, `test/look/check-look.sh`.

- [ ] Confirm how the COSMIC image gets fuzzel.ini today and that the change reaches only COSMIC.
- [ ] Style it; colours via gen-theme.py rules (both day and night are not needed if fuzzel cannot switch; use night and note it — or pick by `is_dark` in `isotope-menu` if cheap).
- [ ] check-look line for the COSMIC fuzzel config; gen-theme `--check` passes.
- [ ] Commit.

### Task 3: Light quick-settings popup translucency

In light mode the quick-settings popup (`applets/isotope-quick`) is almost opaque light grey; in dark it is frosted. Find where the popup background alpha comes from (the COSMIC theme's light `background`/`primary` alpha in `COSMIC_LOOK`, the applet's own container style, or cosmic-panel), and make light match dark's frosted look while keeping text contrast (gen-theme CONTRAST floors).

**Files:** `tools/gen-theme.py` (`COSMIC_LOOK`), `theme/cosmic-theme.json` (regenerated), or `applets/isotope-quick/src/` — whichever is the cause.

- [ ] Find the cause and record it in the commit message.
- [ ] Fix; regenerate; `gen-theme.py --check` passes; tests in `tools/isotope-cosmic-theme` or applets updated if they pin the value.
- [ ] Commit.

### Task 4: Workspace indicator fits the bar

The stock COSMIC workspaces applet (`com.system76.CosmicAppletWorkspaces`) in the Isotope top bar draws its pill as tall as the bar, touching the top and bottom edges. Make it sit inside the bar with the same vertical margin as the other applets.

**Files:** the panel config under `rootfs-cosmic/usr/share/cosmic-layouts/isotope` (size, padding, spacing), or, only if no config can do it, a patch in `patches/cosmic/cosmic-applets/`.

- [ ] Find what sets the pill height (read cosmic-applets epoch-1.11.0 source: the workspaces applet). Prefer panel config. If a patch is needed, write it with the required header and say so in the report (it triggers a full COSMIC rebuild).
- [ ] check-look line if the setting is static.
- [ ] Commit.

### Task 5: Flatpak GTK apps follow the look on COSMIC

xdg-desktop-portal-cosmic serves only `org.freedesktop.appearance` and text scaling, so Flatpak GTK apps (Firefox) get neither the left window buttons (`org.gnome.desktop.wm.preferences button-layout`) nor `icon-theme`. Add xdg-desktop-portal-gtk (already built in the Dockerfile `xdg-desktop-portal-gtk` stage for other variants) to the COSMIC image as a second Settings backend: a COSMIC portals config with `org.freedesktop.impl.portal.Settings=cosmic;gtk` (xdg-desktop-portal merges Settings backends), keeping cosmic as the first so the colour scheme still comes from COSMIC, and cosmic for every other interface (file chooser etc.).

**Files:** `Dockerfile` (COSMIC stages), `rootfs-cosmic/usr/share/xdg-desktop-portal/cosmic-portals.conf` (or the name the existing COSMIC portal config uses), `test/look/check-look.sh`, `AGENTS.md` (remove the "Known limit" paragraph or rewrite it), the look spec's limitation note.

- [ ] Confirm the merge behaviour of Settings backends in the xdg-desktop-portal version the image ships (read its source/docs) and that the GTK backend reads the compiled gsettings defaults (dconf/gsettings backend available in the image).
- [ ] Wire it; make sure the GTK portal does not claim other interfaces on COSMIC.
- [ ] check-look: the COSMIC portals config names gtk for Settings, and the GTK portal binary links.
- [ ] Commit. (VM check with Flatpak Firefox happens in Task 7.)

### Task 6: CI guard for the libcosmic revision on an epoch bump

Renovate bumps `COSMIC_VERSION`, but `applets/Cargo.lock` and `tools/isotope-cosmic-theme` stay on libcosmic `430f3d4`, and `patches/cosmic/README.md`'s table goes stale. Add a CI step (network allowed) that, for the `COSMIC_VERSION` in the Dockerfile, fetches cosmic-applets' `Cargo.lock` at tag `epoch-<version>` and fails when its libcosmic rev differs from the one in `applets/Cargo.lock` and `tools/isotope-cosmic-theme/Cargo.lock`, or from the README table's row, with a message saying what to update.

**Files:** a script under `tools/` or `test/`, `.github/workflows/ci.yml`, `AGENTS.md` (one line).

- [ ] Script with a local test mode (fixture lock files) so it is testable without network; run it against the real tag once.
- [ ] Wire into ci.yml.
- [ ] Commit.

### Task 7: Build, check, VM

- [ ] Build the COSMIC image locally (`/usr/bin/make DESKTOP=cosmic LLVM_IMAGE=... COSMIC_SHELL_IMAGE=... IMAGE=cosmic-desktop:look image`), run `test/look/check-look.sh`.
- [ ] VM: Welcome at first login fits 1280x800 with Isotope title bar; Isotope menu look; light popup; workspace pill; install Flatpak Firefox and confirm left buttons and Conflux icons (needs network in the VM).
- [ ] Update PR #129 body (remove the follow-ups and the Flatpak limit if fixed), push.
