# Live desktop on the ISO Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** The desktop ISOs boot a live desktop by default. The welcome window offers "Install Isotope" or "Try it out". The installer runs as a window. Three entries stay in the menu: "Install Isotope" (cage), the text installer, and safe graphics.

**Architecture:**
- **Boot.** A new GRUB entry for the live desktop passes `isotope.live`. An initramfs cloud-config stage then either hands over to the unattended installer, when the ISO carries an install config, or does three things: writes the marker `/run/isotope/live-desktop`, creates the `live` user, and sets automatic login for ly or greetd.
- **Session.** Everything Isotope starts reads the marker. Isotope Welcome shows the live page, and the installed-system pieces (the tray, the lock, backup) do not start.
- **Install.** `isotope-live-install` runs `isotope-install-gui --window` as root on the user's display.

**Tech Stack:** GRUB template, yip cloud-config stages, POSIX sh, systemd, ly 1.4.1, greetd, Go (`welcome/`, `install-gui/`, `install-ui/core`, `update/`, `system/`) over `guikit/`, Dockerfile.

**Spec:** `docs/superpowers/specs/2026-11-23-live-desktop-design.md`. Its "Decisions" section is binding.

## Global Constraints

- **Variants.** All four desktop variants (sway, i3, niri, COSMIC) get every item. The server and agent ISOs keep today's menu and behaviour. The Trusted Boot (UKI) medium is unchanged.
- **Live only.** The `live` user, the automatic login, and the live-only launcher are created only on a live desktop boot, in tmpfs `/etc` and `/run`. The image never contains them, and a check proves it.
- **Install config wins.** An ISO that carries an install config installs unattended from the default entry, exactly as today.
- **Language carries over.** The language from the live welcome reaches the installed system through `install-ui/core`. The first-login Welcome then skips its Language page.
- **No lock on live.** Screen and suspend locking are off on live. Idle blanking stays.
- **One look.** guikit everywhere: Isotope's palette and fonts, and `guikit.TitleBar` on COSMIC. Copy is in Simplified Technical English. Screenshot tests cover the live welcome and the installer in window mode.
- **The marker.** It is `/run/isotope/live-desktop`. Nothing reads `/proc/cmdline` after the initramfs stage.
- **Names.** "Isotope", `isotope-*`, the user `live` with the display name "Isotope Live". Never "Hadron" for an Isotope feature.
- **Disk.** `df -h /` must show at least 10 GB before any build. Never prune docker. Never build a whole image locally.
- **Commits.** `git -c user.name="Ettore Di Giacinto" -c user.email=mudler@kairos.io commit -s`, with a conventional subject, ending with `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`. Never bare `git stash`.
- **Tests.**
  - `update/` and `install-ui/`: `go vet ./... && go test -race ./...`.
  - `welcome/`, `system/` and `install-gui/`: in a container: `docker run --rm -v "$PWD:/src" -v isotope-gocache:/root/.cache/go-build -w /src/<module> golang:1.25-alpine sh -c 'apk add -q gcc musl-dev pkgconf wayland-dev libxkbcommon-dev mesa-dev vulkan-headers libx11-dev libxcursor-dev libxfixes-dev && CGO_ENABLED=1 go vet -tags nox11,novulkan ./... && CGO_ENABLED=1 go test -tags nox11,novulkan ./...'` (SYSTEST below).
  - shellcheck and gofmt clean.

## Review Focus

1. **An ISO with a `users:` or `install:` config, booted on the default entry.** It must install unattended, with no live desktop and no `live` user. Pinned in Task 2: `test/live/test-live-stage.sh` case `install-config`.
2. **The installed system after an install from the live session.** It has no `live` user, no automatic login, no live launcher, and screen locking is on. Pinned in Task 2 and Task 3: image checks that none of these files are in the image, plus a case that runs the stage without the marker.
3. **i3 (X11).** "Install Isotope" from the i3 live session must open the installer. The installer's i3 build has X11, and the helper passes `DISPLAY`. Pinned in Task 7: `install-gui-i3` built with X11, and `test/live/check-live.sh` checks the binary's NEEDED libX11 on the i3 image.
4. **A second click on "Install Isotope" while the installer runs.** It must not start a second install. Pinned in Task 8: `test/live/test-live-install.sh` case `second-launch`.
5. **The live welcome after the user picks a keyboard layout.** The installer must start with that layout and skip its keyboard step. Pinned in Task 5 and Task 7: `TestLiveSessionChoicesReachInstaller` and `TestWindowModeSkipsKeyboardWhenSet`.

---

## File map

| File | Responsibility |
|---|---|
| `auroraboot/live-grub.cfg.tmpl`, `auroraboot/live-grub-desktop.cfg.tmpl` (new), `auroraboot/build.sh` | menu entries; the desktop ISOs use the new template |
| `rootfs/system/oem/89_isotope_live.yaml` (new), `rootfs/usr/libexec/isotope-live-boot` (new) | the initramfs decision: hand over to the unattended installer, or set up the live desktop (marker, user, automatic login) |
| `rootfs/etc/systemd/system/ly@.service.d/10-conditions.conf`, `rootfs-cosmic/etc/systemd/system/cosmic-greeter.service.d/10-conditions.conf`, both `91_desktop_session.yaml` | the login-screen gate and the twin live stage |
| `rootfs/usr/bin/isotope-not-live` (new) | `exec`s its arguments unless the marker exists; used for the lock and the tray lines |
| sway, i3, niri configs; COSMIC autostart files | wrap the lock and tray lines with `isotope-not-live`; Welcome's `--login` picks the live page by itself |
| `welcome/page_live.go` (new), `welcome/main.go`, `welcome/flow.go` | the live flow |
| `install-ui/core/config.go`, `wizard.go` | the locale in the cloud-config |
| `install-gui/main.go`, `app.go` | `--window` mode |
| Dockerfile `install-gui-build` | the X11 build for i3 |
| `rootfs/usr/bin/isotope-live-install` (new), `rootfs/usr/libexec/isotope-live-install-run` (new), `/usr/share/applications/isotope-install.desktop` | the launcher |
| `system/` | live notices on the install-only pages |
| `rootfs/usr/bin/isotope-menu` | "Install Isotope" first on live |
| `test/live/*.sh` (new), CI workflows | the checks |
| `AGENTS.md`, `README.md` | docs |

---

### Task 1: Boot menu

**Files:** `auroraboot/live-grub-desktop.cfg.tmpl` (new, a copy of the current template with the changes below), `auroraboot/build.sh` (pick the template by flavor: desktop variants get the new one; server and agent keep `live-grub.cfg.tmpl`), `test/live/check-grub.sh` (new, run in CI with the other static checks in `ci.yml`).

- [ ] **Step 1:** Write `check-grub.sh`. It renders both templates the way `build.sh` does (factor the render into a function `build.sh` and the test share), then asserts:
  - **Desktop menu, first entry:** "Isotope", `--id isotope-live`, has `isotope.live`, and has no `install-mode`. It is the `default`.
  - **"Isotope (safe graphics)":** has `isotope.live` and `{{NOMODESET}}` rendered.
  - **"Install Isotope", "Install Isotope (text)" and "Install Isotope (safe graphics)":** each carries today's cmdline of its old entry (`install-mode-interactive`, and `isotope.installer=text` on the text one).
  - **"boot local node", "boot details" and "debug":** byte-equal to today's.
  - **Server and agent:** the render output equals today's.
- [ ] **Step 2:** Run it. It FAILS.
- [ ] **Step 3:** Write the template and the `build.sh` switch. Find where `build.sh` knows the flavor (`FLAVOR` or the image label). Set `set default="isotope-live"`.
- [ ] **Step 4:** Run it. It PASSES. shellcheck is clean.
- [ ] **Step 5:** Commit `feat(iso): a live desktop entry first in the desktop ISOs' menu`.

### Task 2: The live boot stage

**Files:** `rootfs/usr/libexec/isotope-live-boot` (new, POSIX sh), `rootfs/system/oem/89_isotope_live.yaml` (new), `test/live/test-live-stage.sh` (new, runs the script in `alpine:3` against a temp root, like `test/nvidia/test-isotope-nvidia.sh`), CI wiring.

**Interfaces:**
- `isotope-live-boot` uses these overridable paths:
  - `ISOTOPE_LIVE_ROOT` (default `/`);
  - `ISOTOPE_LIVE_CMDLINE` (default `/proc/cmdline`);
  - `ISOTOPE_LIVE_OEM` (default `/oem`).
- It acts only when the cmdline has `isotope.live` and `run/cos/live_mode` exists. Then:
  - **Install config present.** If `oem/` has an install config, found the same way `install-ui/core` finds one (port its predicate: a `users:` or `install:` block in any `*.yaml`), it writes `run/cos/install_mode` and exits 0. No marker and no user.
  - **Otherwise** it does four things:
    - writes `run/isotope/live-desktop`;
    - creates the user `live` (display name "Isotope Live", groups `admin video`, home `/home/live`, no password);
    - writes the ly autologin keys into `etc/ly/config.ini` (`auto_login_user = live`, `auto_login_session` = the session from `/etc/isotope/session`'s session file name, `auto_login_service = ly-autologin`);
    - writes the greetd `[initial_session]` block (`command = "start-cosmic"`, `user = "live"`) into `etc/greetd/cosmic-greeter.toml`, only when that file exists.
- `89_isotope_live.yaml` runs `isotope-live-boot` in the `initramfs` stage. It runs after kairos-init's accounting stage, which creates `kairos` with UID 1000, and before the installer overrides in `90_desktop_installer.yaml`, so the `install_mode` it writes is seen there.

- [ ] **Step 1:** Write `test-live-stage.sh` with these cases:
  - `not-live`: no `isotope.live`. Nothing is written.
  - `live`: the marker is written. `live` is in `etc/passwd` with "Isotope Live", in `admin`, with a UID that is not 1000. ly's config has the three keys. greetd's file has `[initial_session]` when present.
  - `install-config`: an `oem/99_install.yaml` with `install:`. Then `run/cos/install_mode` exists, there is no marker and no `live` user.
  - `idempotent`: a second run changes nothing.
  - `image-clean`: the repo's `rootfs*/etc/ly/config.ini` and `cosmic-greeter.toml` have no `auto_login_user = live` and no `initial_session`.
- [ ] **Step 2:** Run it. It FAILS.
- [ ] **Step 3:** Implement the script and the stage. Check that `90_desktop_installer.yaml`'s first stage (`[ -f /run/cos/install_mode ]`) is in the same initramfs stage, after `89`, so the handover works. If yip's ordering does not allow that, write the kairos-installer override from `89` as well, and explain why in a comment.
- [ ] **Step 4:** Run it. It PASSES. shellcheck is clean.
- [ ] **Step 5:** Commit `feat(live): the live desktop boot: marker, live user, automatic login; an install config still installs`.

### Task 3: Login screens and session gating

**Files:**
- the ly and cosmic-greeter `10-conditions.conf`;
- `rootfs/system/oem/91_desktop_session.yaml` and `rootfs-cosmic/system/oem/91_desktop_session.yaml`;
- `rootfs/usr/bin/isotope-not-live` (new);
- `rootfs-sway/etc/sway/config` (`swayidle` line 73, tray line 80);
- `rootfs-i3/etc/i3/config.d/10-screen-lock.conf` and `30-update-tray.conf`;
- `rootfs-niri/etc/niri/config.kdl` (its lock or idle lines, and the tray if started);
- `rootfs-cosmic/etc/xdg/autostart/isotope-update-tray.desktop`, and COSMIC's idle lock (find where cosmic-idle or the screen lock is configured; add a live-time config write if needed);
- the backup timer (a `ConditionPathExists=!/run/isotope/live-desktop` drop-in on `isotope-backup.timer`);
- `test/live/check-live.sh` (new image check, wired into `desktop-images.yml` and `release.yml` next to `check-boot-units.sh`).

- [ ] **Step 1:** Write `check-live.sh IMAGE`, with these checks:
  - **ly and cosmic-greeter units:** their conditions are `!/run/cos/install_mode` and `!/run/cos/install_mode_interactive`, and no longer `!live_mode`.
  - **Twin stages:** both `91_desktop_session.yaml` files have one, guarded by `-f /run/isotope/live-desktop`, that starts the login screen.
  - **`isotope-not-live`:** runs its arguments without the marker, and exits 0 without running them with the marker. Test this in the image with a temp marker path through `ISOTOPE_LIVE_MARKER`.
  - **Lock and tray lines:** every lock or tray start line in the four variants' configs goes through `isotope-not-live`. Grep the files in the image.
  - **Backup timer:** has the live condition.
  - **Image clean:** no `live` user, no autologin value, no `isotope-install.desktop` unhidden.
- [ ] **Step 2:** Run it against a test image built from a temporary stage (Hadron base plus `rootfs*/` files). If that is too heavy, run it in CI only, and grep the repo's files locally. It FAILS.
- [ ] **Step 3:** Implement.
- [ ] **Step 4:** It PASSES. shellcheck is clean. `test/boot-ux/check-boot-units.sh` still passes its static parts (it checks ly's gate today: update that check to the new conditions).
- [ ] **Step 5:** Commit `feat(live): login screens start on the live desktop; no lock, tray or backup there`.

### Task 4: Isotope System on live

**Files:** `system/` (a `live bool` from the marker in `Hooks`), and the pages Updates, Desktop & image, Backup, Security and Users.

- [ ] **Step 1:** Write `TestLivePagesSayInstallFirst`. With `live=true`, each of the five pages draws "Not available on the live system. Install Isotope first." plus an "Install Isotope" button that runs `isotope-live-install`. Network, Bluetooth, Disks and About are unchanged.
- [ ] **Step 2:** SYSTEST FAILS.
- [ ] **Step 3:** Implement.
- [ ] **Step 4:** It PASSES.
- [ ] **Step 5:** Commit `feat(system): on the live system, the install-only pages point to the installer`.

### Task 5: The live welcome

**Files:** `welcome/page_live.go` (new), `welcome/main.go`, `welcome/flow.go`, plus tests and screenshots in `welcome/testdata/`.

**Interfaces:**
- `PageLive PageID = "live"`; `func NewLiveFlow() *Flow` (pages: `[PageLive]`).
- `isotope-welcome --login` and a bare `isotope-welcome`: when the marker exists, open the live flow and write no stamp.
- `isotope welcome live` opens it directly.
- The live page writes the session choices to `$XDG_RUNTIME_DIR/isotope/live-choices.json`: `{"lang":"de_DE.UTF-8","layout":"de","variant":""}`. Task 7 reads it.

The page follows the spec's sketch in section 4:
- the title "Welcome to Isotope" with `VERSION` and the variant from `/etc/isotope/release`;
- two cards, "Install Isotope" and "Try it out";
- the help line;
- the "changes are lost" line;
- the Language and Keyboard choosers, reusing the Language page's widgets and their data sources.

The help line comes from the first three rows of `/usr/share/isotope/shortcuts.txt` for the Wi-Fi, apps and help keys. If a key is missing, its item is dropped, never invented.

- [ ] **Step 1:** Write these tests:
  - `TestLoginOnLiveOpensLiveFlow`: the marker is set, and the stamp is untouched.
  - `TestLivePageInstallRunsInstaller`: the Install card runs `isotope-live-install` through the existing `run` hook, and the window closes.
  - `TestLivePageTryCloses`.
  - `TestLiveSessionChoicesReachInstaller`: picking a layout writes the JSON and calls the session keymap hook.
  - `TestLiveHelpLineFromShortcuts`: a fixture `shortcuts.txt` with one missing key.
  - Screenshot tests `live-cosmic.png` and `live-sway.png`, in the style of the existing ones.
- [ ] **Step 2:** SYSTEST FAILS.
- [ ] **Step 3:** Implement.
  - Applying the keymap live:
    - sway: `swaymsg input type:keyboard xkb_layout`;
    - niri: `niri msg action`, or a config write, whichever niri supports;
    - i3: `setxkbmap`;
    - COSMIC: its keyboard config through cosmic-config, the same way the Welcome Language page sets the layout today. Find that code and reuse it.
  - Applying the language: export `LANG` for apps started from now on, through `dbus-update-activation-environment --systemd LANG`.
- [ ] **Step 4:** It PASSES. Check the screenshots by eye.
- [ ] **Step 5:** Commit `feat(welcome): the live welcome: install or try, with language and keyboard`.

### Task 6: The language reaches the installed system

**Files:** `install-ui/core/config.go` (`writeLocaleStage`, `Choice`), `install-ui/core/wizard.go`, `welcome/page_language.go` (skip when set).

**Interfaces:** `Choice.Locale string` (for example `de_DE.UTF-8`). When it is set, the cloud-config's locale stage also writes `/etc/locale.conf` (`LANG=…`) and the content of `/oem/91_isotope_locale.yaml`, the same file Isotope Welcome's `SetLocale` writes. Read that code in `update/`, or wherever the daemon implements it, and produce identical output.

- [ ] **Step 1:** Write these tests:
  - `TestLocaleInCloudConfig`: the locale stage and `locale.conf` appear only when `Locale` is set.
  - `TestLocaleStageMatchesSetLocale`: byte-equal with the daemon's writer. Share the function if both are reachable; otherwise use a golden file.
  - In `welcome/`, `TestLanguagePageSkippedWhenLocaleSet`: `/etc/locale.conf` exists with `LANG`, so the first-login flow has no Language page.
- [ ] **Step 2:** The tests FAIL.
- [ ] **Step 3:** Implement. The text installer passes no locale, so its behaviour is unchanged.
- [ ] **Step 4:** The tests PASS.
- [ ] **Step 5:** Commit `feat(installer): carry the live session's language to the installed system`.

### Task 7: The installer as a window

**Files:** `install-gui/main.go` (the `--window` flag), `install-gui/app.go` (skip steps), `install-gui/screenshot_test.go`, and the Dockerfile `install-gui-build` stage. Build twice, like `welcome-build`: `-tags nox11,novulkan` and an X11 build. `install-gui-${DESKTOP}` picks one, and i3 gets X11. Also `test/live/check-live.sh`.

**Interfaces:**
- `isotope-install-gui --window [--choices FILE]`:
  - The window is decorated, not fullscreen, and has the app ID `isotope-install`.
  - It is sized with Welcome's `WindowSize` logic. Move that function to `guikit` if both need it.
  - It has `guikit.TitleBar` on COSMIC (`XDG_CURRENT_DESKTOP=COSMIC`).
- With `--choices`, the Keyboard step is skipped and its values come from the file, and `Choice.Locale` is set from `lang`.
- Without `--window`, the installer behaves byte-for-byte as today, so the existing screenshots must not change.
- Window rules: `for_window [app_id="isotope-install"] floating enable` in sway; the i3 class rule; the niri rule.

- [ ] **Step 1:** Write these tests:
  - `TestWindowModeSkipsKeyboardWhenSet`;
  - `TestWindowModeKeepsStepsWithoutChoices`;
  - a screenshot `window-cosmic.png`;
  - the existing screenshots stay byte-equal;
  - `check-live.sh` on the i3 image: `install-gui` NEEDED includes `libX11`.
- [ ] **Step 2:** SYSTEST FAILS.
- [ ] **Step 3:** Implement. On the X11 build, check how Welcome's i3 build is set up and copy it.
- [ ] **Step 4:** It PASSES.
- [ ] **Step 5:** Commit `feat(installer): run as a window in the live session`.

### Task 8: The launcher

**Files:**
- `rootfs/usr/bin/isotope-live-install` (new);
- `rootfs/usr/libexec/isotope-live-install-run` (new);
- `rootfs/usr/share/applications/isotope-install.desktop` (shipped with `NoDisplay=true`; the live boot stage in Task 2 writes an unhidden copy to `/run/isotope/applications/` and adds that directory to `XDG_DATA_DIRS` for the session through `/etc/profile.d/isotope-live.sh`, which acts only with the marker; or another live-only mechanism the builder proves);
- `rootfs/usr/bin/isotope-menu` ("Install Isotope" first when the marker exists);
- the COSMIC dock pin on live (Welcome's `dockpin` code, called from the live flow at start);
- `test/live/test-live-install.sh` (new, alpine with stubs).

**Interfaces:**
- `isotope-live-install`:
  - exits 1 with the notification "The installer runs only on the live system." when there is no marker;
  - raises the running installer and exits 0 when the lock file `$XDG_RUNTIME_DIR/isotope/installer.lock` is held (use `flock -n`);
  - otherwise runs `sudo -n /usr/libexec/isotope-live-install-run --choices "$XDG_RUNTIME_DIR/isotope/live-choices.json"`, passing `WAYLAND_DISPLAY`, `DISPLAY`, `XDG_RUNTIME_DIR` and `XDG_CURRENT_DESKTOP` through `--preserve-env`.
- Raising the window:
  - sway: `swaymsg '[app_id="isotope-install"] focus'`;
  - i3: `i3-msg '[class="isotope-install"] focus'`;
  - niri: `niri msg action focus-window` by ID from `niri msg windows`;
  - COSMIC: no IPC, so a notification "The installer is already open."
- `isotope-live-install-run` checks the marker, then `exec`s `isotope-install-gui --window --choices FILE`.

- [ ] **Step 1:** Write `test-live-install.sh`, with stubs for `sudo`, `flock`, `swaymsg` and `notify-send` that log their argv. Cases:
  - `not-live` refuses;
  - `first-launch` runs sudo with the right argv and env;
  - `second-launch` raises and does not run sudo;
  - `cosmic-second-launch` notifies.
- [ ] **Step 2:** It FAILS.
- [ ] **Step 3:** Implement. Make "Install Isotope" the first entry in `isotope-menu` on live.
- [ ] **Step 4:** It PASSES. shellcheck is clean. `check-live.sh` asserts that the shipped desktop file is `NoDisplay=true`.
- [ ] **Step 5:** Commit `feat(live): Install Isotope launcher on every live desktop`.

### Task 9: Docs and the full check run

**Files:** `AGENTS.md`, `README.md`, `docs/DEVELOPMENT.md`.

- [ ] **Step 1:** Add an AGENTS.md row "Live desktop on the ISO". It covers:
  - the menu and the marker;
  - the `live` user and the automatic login per variant;
  - what does not run on live;
  - the welcome, the installer window and the launcher;
  - that an install config wins;
  - the tests.
  Also update the rows it touches: Graphical installer, First-login welcome, Update tray, Screenshots and screen lock, and Isotope System. The README download section gets a "Try it first" line.
- [ ] **Step 2:** Grep that every path and command named exists.
- [ ] **Step 3:** Commit `docs: the live desktop on the ISO`.

## Order

Tasks 1 → 2 → 3 come first: the boot path, then the gates. Then 5 → 6 → 7 → 8, which are the UI and depend on 2's marker and 5's choices file. Task 4 can go anywhere after 2. Task 9 comes last.

## Self-review

- **Spec coverage:**
  - menu (1), marker, user and automatic login, install-config handover (2);
  - gates and the no-lock, no-tray, no-backup rule (3);
  - System on live (4);
  - welcome (5), language carried over (6), installer window and X11 (7), launcher and single instance (8), docs (9);
  - UKI unchanged (1: only the desktop GRUB template changes; the UKI build is untouched);
  - server and agent menus unchanged (1).
- **Types:** `PageLive`, `NewLiveFlow`, `live-choices.json` (`lang`, `layout`, `variant`), `Choice.Locale`, `--window` and `--choices`, and the marker path are each defined once and used by name.
- **Review Focus:** each of the five items has a named test.
