# Live desktop on the ISO: try or install

## Why

Today, a desktop ISO boots straight into the installer. Someone who downloads Isotope cannot see it before they wipe a disk, and cannot check that their Wi-Fi, screen, sound and touchpad work.

The default boot becomes a live desktop of that variant. A welcome window asks whether to **Install Isotope** or **Try it out**, with a few lines of help. The installer then opens as a window on that desktop. The live session must look and feel like the rest of Isotope: the same guikit look, palette, fonts, title bar and copy voice as Isotope Welcome, Isotope System and the graphical installer. It must not feel like a debug mode.

## Goals

- **Default entry:** the ISO's first menu entry, "Isotope", boots the live desktop of its variant (sway, i3, niri, COSMIC). It logs in automatically, with no password prompt.
- **Welcome window:** at login, the "Welcome to Isotope" window offers Install and Try, and gives short help.
- **Install from the desktop:** "Install Isotope" stays available for the whole session: in the welcome window, as a launcher in the app list, in the dock or panel where the variant has one, and in the Isotope menu.
- **Installer as a window:** the graphical installer runs as a normal window in the live session. It asks the same questions and runs the same install as today, on every variant.
- **Automated installs keep working** (maintainer's decision). When the ISO carries an install config (a `users:` or `install:` block, which the installer finds today in `/oem`: `install-ui/core/wizard.go:538-542`, `config.go:237-263`), the default entry does not start the live desktop. It installs unattended, exactly as today. See section 1.
- **Other entries stay:**
  - "Install Isotope": today's graphical installer, fullscreen in cage, with no desktop;
  - "Install Isotope (text)": today's text installer;
  - the safe-graphics variants;
  - "boot details";
  - "debug".
- **Same image:** the installed system is unchanged. The live session never touches the disk until the user installs.

## Non-goals

- **Persistence.** Changes in the live session are lost at shutdown. The welcome window says so.
- **The server and agent images.** Their ISOs keep today's menu: the server has no desktop, and the agent installs unattended.
- **The Trusted Boot (UKI) medium.** It keeps today's behaviour. Its signed cmdline is the same on every entry, so it cannot offer a live entry next to install entries (`90_desktop_installer.yaml`).
- **A separate live image.** The live desktop is the same squashfs as today.

## Anti-goals (a failure even if it works)

- **A debug look:** a shell prompt, a raw ly or greeter screen, or the `kairos` login on the way to the desktop.
- **Installed-system dialogs:** Isotope Welcome's first-login flow, the update tray or backup reminders on the live session. They belong to an installed system.
- **Live-media leaks:** any live-only access (the automatic login, passwordless root for the installer) that ends up on an installed system.
- **Unequal variants:** a variant that boots to a live desktop whose welcome or installer looks or works differently from the others, beyond the shell's own chrome.

## Approaches

- **A. Desktop first, with a welcome window on top. Chosen by the maintainer.** The live desktop comes up logged in. A welcome window offers Install or Try. Install opens the installer as a window. The user sees the real desktop at once and can try it, then install whenever they want. The cost: the installer must run as a window and must get root from inside a user session.
- **B. Chooser first, before the desktop.** A fullscreen cage chooser offers Install, which runs today's installer, or Try, which starts the desktop. It reuses today's root and cage path. But "Try, then Install" restarts the session, and the desktop is not the first thing the user sees.

## Design

### 1. Boot menu (`auroraboot/live-grub.cfg.tmpl`)

| Entry | Cmdline difference from today's `isotope` entry | Result |
|---|---|---|
| **Isotope** (default) | no `install-mode-interactive`; adds `isotope.live` | live desktop |
| Isotope (safe graphics) | as above, with `{{NOMODESET}}` | live desktop in software rendering |
| Install Isotope | today's `isotope` entry, renamed | the graphical installer in cage, as today |
| Install Isotope (text) | today's text entry, renamed | the text installer |
| Install Isotope (safe graphics) | today's safe-graphics entry, renamed | as today |
| boot local node, boot details, debug | unchanged | unchanged |

- **Marker.** `isotope.live` alone marks the live desktop boot. The live boot still gets immucore's `/run/cos/live_mode`. It gets none of the install sentinels (`/run/cos/install_mode*`), so kairos-installer and kairos-interactive do not run.
- **Hook.** An initramfs-stage cloud-config writes `/run/isotope/live-desktop` when `/proc/cmdline` has `isotope.live` and `/run/cos/live_mode` exists. Every later rule keys on that file, never on the cmdline again.
- **An ISO with an install config.** When the live boot finds an install config, the same stage does not write the marker. It hands the boot to the unattended installer instead, as today's install entry does. The builder chooses how: write `/run/cos/install_mode` before the installer services are evaluated, or start `kairos-installer` directly. Then nothing of the live desktop starts, and the config's install runs exactly as it does today. A check covers both cases.
- **One template.** One GRUB template is rendered for every ISO today, including the server and the agent (`auroraboot/build.sh:46-48`). The live entries go into a desktop-only template, or a render switch, so the server and agent menus stay as they are.

### 2. The live user and automatic login

- **The user.** A live-only account `live`, display name "Isotope Live", is created by a stage under the same guard as the marker. It is in `admin` and `video`. Its home is on the tmpfs.
  - The `kairos` user that kairos-init creates on live boots stays as it is. It is the serial-console login.
  - This is my call. `live` reads better than `kairos` in a panel or a terminal prompt.
- **UID.** kairos-init's `kairos` user takes UID 1000 on live boots, so `live` gets the next free UID. Nothing in the live path may assume UID 1000.
- **Login screen gate.** ly and cosmic-greeter today refuse to start on any live boot (`ConditionPathExists=!/run/cos/live_mode`). The gate becomes "not on an install boot": `ConditionPathExists=!/run/cos/install_mode` and `ConditionPathExists=!/run/cos/install_mode_interactive`. So they start on the live desktop boot and on the installed system, as today, and never on an install boot.
  - Both `rootfs/system/oem/91_desktop_session.yaml` (ly) and `rootfs-cosmic/system/oem/91_desktop_session.yaml` (cosmic-greeter) get a twin stage for the live desktop. The agent's `ly@.service.d/20-agent-profile.conf` is the precedent for these gates.
- **Automatic login.**
  - **ly (sway, i3, niri):** ly 1.4.1 has `auto_login_user`, `auto_login_session` and `auto_login_service = ly-autologin`. The agent image already uses them (`rootfs-agent/system/oem/90_agent_profile.yaml`). A live-only stage sets `auto_login_user = live` and the variant's session in `/etc/ly/config.ini`.
  - **cosmic-greeter (greetd):** a live-only stage adds `[initial_session] command = "start-cosmic" user = "live"` to `/etc/greetd/cosmic-greeter.toml`. greetd logs in once, and the greeter appears after logout.
  - Both edits are made in `/etc`, which is a tmpfs overlay on the live boot, so they never reach an installed system.
- **No splash gap.** The boot splash stays until the session's first frame, as on the installed system.

### 3. What the live session runs, and what it does not

- **Live marker in the session.** The session sees `/run/isotope/live-desktop`. Every Isotope autostart reads it:
  - **Isotope Welcome's first-login flow (`isotope-welcome --login`):** it does not start on live. The live welcome starts instead (section 4).
  - **The update tray:** it does not start on live. A sway or i3 `exec`, niri `spawn-at-startup` or COSMIC autostart line checks the marker. The autostart files get `X-…-Condition` or a wrapper, whichever the variant supports.
  - **The backup timer and reminders:** off on live.
  - **The software rendering notice:** it still runs. It matters most on a try-out.
  - **Screen locking and suspend locking:** off on live. **My call.** `live` has no password, so a lock after five idle minutes, possibly during an install, would lock the user out. The marker turns this off on each variant: sway's `swayidle` lock lines, i3's `xss-lock`, niri's lock, and COSMIC's idle lock. Idle *blanking* of the screen stays.
- **Isotope System on live.** It opens, but its Updates, Desktop & image, Backup, Security and Users pages say "Not available on the live system. Install Isotope first." Network, Bluetooth, Disks and About work, because they help someone check their hardware.

### 4. The live welcome window

- **The app.** `isotope-welcome live`, a new flow in the existing Isotope Welcome binary. It reuses its window, guikit, `guikit.TitleBar` on COSMIC, and the Welcome page's look. Started at login on live in place of `--login`.
- **Its one page, sketched:**
```
 ┌──────────────────────────────────────────────────────────┐
 │                       ◆ Isotope                           │
 │               Welcome to Isotope 0.3 (COSMIC)             │
 │   You are running Isotope from the USB stick. Nothing    │
 │   on this computer changes until you install it.         │
 │                                                           │
 │   ┌──────────────────────┐   ┌──────────────────────┐     │
 │   │  Install Isotope     │   │  Try it out          │     │
 │   │  Put Isotope on this │   │  Look around first.  │     │
 │   │  computer's disk.    │   │  Install at any time.│     │
 │   └──────────────────────┘   └──────────────────────┘     │
 │                                                           │
 │   Wi-Fi: top bar   ·   Apps: Super+D   ·   Help: Super+F1 │
 │   Changes in this session are lost when you shut down.   │
 │   Language: [English ▾]   Keyboard: [us ▾]                │
 └──────────────────────────────────────────────────────────┘
```
  - The help line is per variant. It is generated from the same shortcut data as `isotope-help`, so it never lies.
- **Install** closes the window and starts the installer (section 5).
- **Try it out** closes the window. A desktop or dock launcher stays.
- **Language and keyboard.** They apply to the live session at once (`XKB` and `LANG` for new windows, the compositor's own keymap call). The installer starts with the same values, which replaces its own keyboard step on the live path.
  - **The installed system keeps the language too** (maintainer's decision). `install-ui/core` gains the locale in the cloud-config it writes. It writes `/etc/locale.conf` and the same `/oem/91_isotope_locale.yaml` stage that Isotope Welcome's `SetLocale` writes, so both paths give one result.
  - The first-login Welcome then skips its Language page when the locale is already set.
  - This changes `install-ui/core`, with tests, and the text installer gets the same field without a new question.

### 5. The installer as a window

- **The flag.** `isotope-install-gui --window`. Without the flag it behaves exactly as today in cage.
  - The window is decorated by the compositor, gets `guikit.TitleBar` on COSMIC, is sized to the output like Welcome (`WindowSize`), and uses the app ID `isotope-install` for the window rules (floating on sway, i3 and niri).
  - The language and keyboard pages are skipped when the live welcome set them: they are read from the session.
  - On i3 (X11), the installer is built a second time with X11, as Welcome and System are (`-tags novulkan`), and `install-gui-${DESKTOP}` picks the build.
- **Root.** The installer partitions the disk, so it must run as root. `isotope-live-install` (a small script) runs `sudo -n /usr/libexec/isotope-live-install-run`. That helper runs the installer as root on the user's display, passing `WAYLAND_DISPLAY` or `DISPLAY` and `XDG_RUNTIME_DIR` (root can open the user's socket).
  - `live` is in `admin`, and kairos-init's accounting stage already gives `%admin` passwordless sudo on every boot. That rule is checked by `test/accounting/check-default-user.sh:80`. So the live session has full root, like any live medium. **My call:** no narrow sudoers rule. The helper is plumbing, not a security boundary.
  - What must never leak to an installed system is the `live` user and the automatic login. Both are created only by the live stage, in tmpfs `/etc`, and the image check proves neither is in the image.
- **One at a time.** The installer runs once. A second launch raises the running window or refuses with a notification.
- **The end.**
  - The installer's last page offers "Restart now" (reboot, eject prompt as today) and "Keep trying". The live session continues, and the launcher says "Isotope is installed. Restart to use it."
  - A failed install shows today's error page with the log, and the session continues.

### 6. "Install Isotope" launchers (live only)

- **The desktop entry.** `/usr/share/applications/isotope-install.desktop` (Name "Install Isotope", Isotope's install icon) has `Exec=isotope-live-install` and `TryExec`. It is shown only on live: written by the live stage, or present with `Hidden=true` and unhidden by the stage. The builder picks whichever the image checks can prove.
- **Where it appears:**
  - **sway and i3:** the Isotope menu's first entry on live, and `isotope-apps`.
  - **niri:** the Noctalia launcher's app list, and the Isotope menu.
  - **COSMIC:** pinned first in the dock on live, using the dockpin code Welcome already uses, and in the launcher.

### 7. Look and copy

- **One look.** Everything new is drawn with guikit, in Isotope's palette and fonts. Buttons, cards and the title bar look as they do in Isotope Welcome and the installer.
- **Copy.** Plain Simplified Technical English, in the voice of the existing pages.
- **Screenshot tests.** The live welcome gets screenshot tests in `welcome/testdata/` for COSMIC and a tiling variant. The installer in window mode gets screenshot tests in `install-gui/testdata/`.
- **Menu wording.** The GRUB entries are renamed in the same style as today ("Isotope", "Install Isotope", …).

## Testing

- **Go.**
  - `welcome/`: the live flow chooses Install and Try, applies language and keyboard, and is gated by the marker.
  - `install-gui/`: `--window` mode skips the language and keyboard steps when they are set.
  - `install-ui/core`: the locale lands in the cloud-config.
- **Scripts.** `test/live/check-live.sh` on every desktop image:
  - the live-only cloud-config stages exist and are guarded by the marker;
  - no sudoers file and no autologin values are in the image;
  - the GRUB template has the new entries, with the right keywords;
  - the gates of ly and cosmic-greeter;
  - the launcher is hidden by default.
- **Boot in a VM, in CI if the existing VM test harness (the agent gates' QEMU scripts) can boot a desktop ISO in time; otherwise by QA:**
  - the default entry reaches the live desktop with the welcome window;
  - the install entry reaches cage.
- **Manual QA list for the PR:** real hardware, all four variants, both installer paths, and the network and Bluetooth pages on live.

## What is left to the builder

- The exact stage file names and where the live stages sit in the order. They must run after kairos-init's accounting stage.
- How each variant's autostart reads the live marker.
- Whether the launcher file is written by the live stage or unhidden by it.
- How the installer window raises itself on a second launch.

## Decisions

1. **Maintainer:** the desktop comes first, with a welcome window (approach A).
2. **Maintainer:** an install config on the ISO wins. The default entry then installs unattended, as today.
3. **Maintainer:** the language picked in the live welcome carries over to the installed system.
4. **My call:** `live` has full passwordless root through `admin`, as live media do. There is no narrow sudo rule.
5. **My call:** screen locking is off on live.
6. **My call:** the live account is named `live`, with the display name "Isotope Live".
