# Isotope Welcome: a graphical first login

## Intent

The first login on Isotope is not finished. Today:

- `isotope-first-run-apps` opens a terminal with a numbered checklist
  ("Type numbers to change the choice, Enter to install").
- `isotope-welcome` sends one notification that names some keys.
- On COSMIC, `cosmic-initial-setup` runs first. Its look is not Isotope's,
  it duplicates our app step with COSMIC Store, and its layout page is broken
  on Isotope: it runs `killall cosmic-panel`, Isotope has no `killall`, so the
  layout the user picks shows only at the next login (seen on a laptop,
  2026-11-20).
- Backup and restore exist only as `isotope backup` and `isotope restore` in
  a terminal. The tray has no backup item.

The installer is now a graphical app (`install-gui`, Gio) with a strong
Isotope look. The user asked for the first login to look and feel like the
installer: one window, the same sidebar, the same buttons, every variant.

**Isotope Welcome** is that window. It is a new Gio app, `isotope-welcome`,
that shares the installer's look through a shared package. It replaces the
terminal app checklist, the welcome notification and, on COSMIC,
`cosmic-initial-setup`, and it takes over what that wizard did that the
installer does not do already. Its Backup page is also the graphical backup
tool, opened from the tray and the Isotope menu.

## Goals

- One graphical first-login flow on sway, i3, COSMIC and niri, with the
  installer's look.
- Fold in what `cosmic-initial-setup` does that the installer does not:
  language, Wi-Fi, appearance, accessibility (scale and text size), apps,
  shortcuts, and on COSMIC its layout, workflow and launcher pages.
- Restore a backup from the first page, on a newly installed machine.
- A Backup page: set up a target, back up now, schedule, see the last
  backup and the list. Opened by a new "Backup…" item in the tray and by the
  Isotope menu's "Backup" entry, in place of the terminal.
- Every page can be skipped. The flow never blocks the session.
- Can be opened again: Isotope menu → "Welcome", and its single pages
  ("Install apps", "Backup") open on their own.

## Non-goals

- The agent image. It has its own first run (`isotope-agent-first-run`);
  `Dockerfile.agent` removes `isotope-welcome` as it removes
  `isotope-first-run-apps` today.
- The server edition. It has no desktop.
- Keyboard layout, time zone and the user account. The installer asks for
  them. A later change can add them as pages for machines installed
  unattended.
- A screen reader. `cosmic-initial-setup` offers one; Isotope ships no Orca.
  Adding it is a separate decision.
- A settings app. Welcome sets things once; changing them later is each
  desktop's own job (COSMIC Settings, config files).
- Light themes for the bars, terminals and notifications of sway, i3 and
  niri. See Appearance.

## Anti-goals

These make the change a failure even if it works:

- A page that looks applied and is not until the next login, as COSMIC's
  layout page does today. Each setting either applies now, or the page says
  "applies when you log in again" and Ready offers "Log out".
- A half-themed desktop: a light mode that turns apps light and leaves dark
  bars and terminals with unreadable text.
- A second visual language. Every widget comes from the shared package; the
  installer and Welcome do not drift.
- A root shell, a sudo prompt or a terminal anywhere in the flow.
- Asking an existing user to go through first-time setup again after an
  upgrade.

## Constraints

- **musl, everything built from source** in the Dockerfile. No new UI
  toolkit: Gio, as the installer. No GTK4.
- **i3 runs on X11.** The installer is built `-tags nox11,novulkan` because it
  always runs in cage. Welcome needs a native window on i3 too, so i3 gets
  an X11 build of it (see Build).
- **Every variant has the same features** (AGENTS.md). Where a variant has
  no equivalent (COSMIC's panel layout), the page shows only there, and this
  document says so.
- **AGENTS.md: user commands are subcommands of `isotope`.** That binary is
  static (`CGO_ENABLED=0`) and Gio needs cgo, so the window is its own binary,
  as `isotope-install-gui` is. `isotope welcome [PAGE]` is the command people
  type; it runs `/usr/bin/isotope-welcome`.
- **Root work goes through `isotope-updated`.** The tray already talks to this
  root daemon over D-Bus (`io.kairos.IsotopeUpdate1`), with polkit and the
  `admin` group deciding. Backup and restore join it. Secrets cross the bus
  as a file descriptor, as the recovery methods do (`update/recovery_bus.go`).
- **Existing machines.** A user who already has the stamps
  `~/.local/state/isotope/first-run-apps` or `.../welcomed` (or the
  `hadron/` ones from before the rename) did their first login already.
  They never see the flow at login. They get the "Welcome" menu entry.

## Approaches

Considered:

- **A. A new app on a shared package (chosen).** Move the installer's look
  (palette, fonts, widgets, the sidebar/content/footer frame, focus and
  scale handling) into a shared Go module. `install-gui` and a new
  `isotope-welcome` both import it. Costs a refactor first; the installer's
  screenshot tests guard it. The two apps stay alike by construction, and
  Welcome ships no installer code.
- **B. A `--welcome` mode in `isotope-install-gui`.** The fastest start, but
  one binary would mix root install code and session code, the installed
  system would ship installer code, and each change risks the other.
- **C. GTK4/libadwaita.** Native Wi-Fi and accessibility widgets, but it does
  not look like the installer, adds GTK4 to the image and a second UI stack.
  Against why the user asked for this.

The user picked A, and asked for the refactor to share the installer's
components. That refactor is in progress as its own PR
(`refactor/shared-gui-kit`): a shared module (`guikit/`) with no visual
change, checked by the installer's screenshot tests.

## The flow

```
┌──────────────┬──────────────────────────────────────────┐
│  ◆ Isotope   │                                          │
│              │   Welcome to Isotope                     │
│ ● Welcome    │   Isotope COSMIC, v0.0.25                │
│ ○ Language   │                                          │
│ ○ Network    │   ( Set up as new )                      │
│ ○ Appearance │   ( Restore from a backup )              │
│ ○ Layout  ¹  │                                          │
│ ○ Workflow ¹ │                                          │
│ ○ Launcher ¹ │                                          │
│ ○ Access…    │                                          │
│ ○ Apps       │                                          │
│ ○ Shortcuts  │                                          │
│ ○ Ready      │                       [Skip]  [Continue] │
└──────────────┴──────────────────────────────────────────┘
  ¹ COSMIC only
```

The window is a normal decorated window, about 1100×720 dp, centered, not
fullscreen. sway, i3 and niri get a window rule that floats it (app ID and
X11 class `isotope-welcome`). It uses the installer's look, with the
sidebar's step list and Go back / Skip / Continue in the footer, Esc for Go
back, and Ctrl+Plus / Ctrl+Minus for scale.

### 1. Welcome

The Isotope mark, the edition and version (from `/etc/isotope/release`),
one paragraph, and two choices:

- **Set up as new** (default): continue to Language.
- **Restore from a backup**: the restore steps (below). After a whole-machine
  restore the machine restarts, so the flow ends there.

The Restore choice is offered only to an `admin` user, and only while the
machine is **new**. `isotope restore` has no such check today (it checks only
that the disk is set up the same way), so this design defines it, and the
**daemon enforces it**, not only the page. A machine is new when all of these
hold:

- exactly one human user (UID ≥ 1000), the one the installer created;
- no backup target is configured and no backup was ever taken on it;
- no restore is pending or finished on it;
- Welcome's stamp is not yet written for that user (the first login is not
  over).

On a machine that is not new, the choice is hidden and the daemon's
`Restore` refuses with "This machine is already in use; restore parts of a
backup with isotope restore --only". Restoring over a used system stays a
CLI job.

*Builder's call (marked as such):* `Restore` is destructive, so its polkit
action asks for the admin's password (`auth_admin_keep`), unlike the other
backup methods. A separate action `io.kairos.isotope-update.restore` carries
that rule.

*Implementation note (phase 2):* the action is YES for `admin`, with no
password, like the other actions. The sway, i3 and niri sessions run no
polkit agent, so `auth_admin_keep` could never be answered there. A restore
is protected by the machine-is-new check, the backup password and the
confirmation instead. Also, `BackupInit` returns no password: the app
generates it and sends it through a pipe fd.

#### Restore steps

1. **Where is the backup**: a USB disk (see "Disks"), a remote target
   (see "Remote targets": SFTP, REST server, S3), or a file written by
   `isotope backup --export`.
2. **Password**: the backup's password.
3. **Which backup**: the snapshots in the target, newest first, with host
   and date. When several hosts back up to the target, they are grouped by
   host. Default: the latest of the host with the most recent backup.
4. **Confirm**: what comes back (users and passwords, hostname, homes, SSH
   keys, extensions, settings, the Flatpak apps), and that the machine
   restarts at the end.
5. **Progress**, then the restart.

The work is `isotope restore` itself, run by the daemon (see "Daemon API").
The daemon refuses a restore that the CLI would refuse (an encryption
mismatch, for example), and the page shows the CLI's error text.

### 2. Language

A searchable list of the system's UTF-8 locales (`locale -a`, from
musl-locales; 305 on the COSMIC image), shown by their names ("Deutsch
(Deutschland)"), the current one selected. It is the system locale for every
user, as on COSMIC.

**It must survive a reboot.** On Kairos `/etc` is rebuilt at every boot, so a
locale written only to `/etc/locale.conf` (which is what localed's
`SetLocale` does) is lost. The keyboard layout has the same problem and
solves it with the installer's cloud-config in `/oem`, applied by an
initramfs stage at every boot (CLAUDE.md, "Keyboard layout and timezone
from the installer"). The language does the same:

- A new daemon method `SetLocale(lang)` on `io.kairos.IsotopeUpdate1`, under
  a new polkit action `io.kairos.isotope-update.settings` (admin, no
  password, no Flatpak callers). It checks `lang` against `locale -a`,
  writes `LANG=<lang>` to `/etc/locale.conf` now, and writes an `/oem`
  cloud-config (`/oem/91_isotope_locale.yaml` or next to the installer's
  keyboard file) that writes it again at every boot.
- The builder checks that `LANG` from `/etc/locale.conf` reaches the session
  on every login manager (ly, greetd with cosmic-greeter) and the user's
  systemd instance. If one does not read it, the same change makes it, for
  example through `/etc/profile.d/`, as the keyboard does with
  `isotope-keyboard.sh`.
- localed: the COSMIC image on the laptop (2026-11-20) has
  `systemd-localed` and `localectl status` works, though CLAUDE.md says
  Hadron has none and the Dockerfile patches cosmic-initial-setup for its
  absence. Welcome does not depend on it either way. The builder updates
  that CLAUDE.md line to match what the image has.

The new language applies at the next login. If it changed, Ready shows
"Log out to use <language>" with a Log out button. Welcome's own text stays
English this time (see "What is left to the builder" for translations).

A non-admin user sees the page read-only with "Ask an administrator to
change the language".

### 3. Network

- If a connection is up (NetworkManager says `CONNECTED_GLOBAL`), the page
  shows "Connected to <name>" and Continue.
- Otherwise: the Wi-Fi networks NetworkManager sees, by strength, with a lock
  for secured ones; pick one, type the password, Connect. A "Hidden
  network…" link takes an SSID. Errors show inline.
- No Wi-Fi device and no connection: "Plug in a network cable, or continue
  without a network".

Talks to NetworkManager over D-Bus (it is on every desktop variant). The
user's own polkit rights decide, as they do for the tray's network applets.
Continue without a network is allowed; Apps then says so (below).

### 4. Appearance

- **Dark or light**, two large cards. Dark is the default and Isotope's
  look.

No accent colours and no "frosted" toggle. `cosmic-initial-setup`'s
appearance page (epoch-1.11.0) has no accent picker, and its frosted toggle
works only because it rebuilds COSMIC's theme with libcosmic's palette code
(`builder.build()`); nothing in COSMIC rebuilds the theme from a changed
builder key alone (cosmic-settings-daemon watches the built themes and the
mode, not the builders). Writing those keys from Welcome would look applied
and change nothing, which is an anti-goal. COSMIC Settings → Appearance keeps
both.

How it applies:

- Every variant: the app preference, so GTK, Qt and Flatpak apps follow it:
  `org.gnome.desktop.interface color-scheme` (`prefer-dark` / `prefer-light`)
  and `gtk-theme` (`Adwaita-dark` / `Adwaita`: the image's schema override
  sets `Adwaita-dark`, and GTK3 on Wayland follows that key, not
  `color-scheme`) through gsettings, read by the portal;
  `gtk-application-prefer-dark-theme` in the user's
  `~/.config/gtk-3.0/settings.ini` (i3 sets it system-wide today).
- COSMIC: also `com.system76.CosmicTheme.Mode` `is_dark`. COSMIC applies it
  live (cosmic-settings-daemon watches it and updates gsettings itself).
- sway, i3, niri: the desktop chrome (bar, terminal, notifications, menus)
  stays Isotope's dark theme in both modes. Only apps change. This avoids the
  half-themed desktop (an anti-goal). The light card says "Apps use a light
  theme; the bar and menus stay dark".

### 5–7. Layout, Workflow, Launcher (COSMIC only)

The three pages of `cosmic-initial-setup` with the same choices, drawn with
Isotope's widgets:

- **Layout**: the layouts in `/usr/share/cosmic-layouts/` (today
  `top-panel-and-bottom-dock` and `bottom-panel`), each with its `icon.png`.
  Applied as upstream does: replace the matching directories under
  `~/.config/cosmic/` with the layout's. Then restart the panel **with
  `pkill -x cosmic-panel`**. `cosmic-session` starts it again, so the
  change shows at once. A test asserts the panel process is new after the
  change.
- **Workflow** and **Launcher**: information pages, as in
  `cosmic-initial-setup` (its `workflow.rs` and `launcher.rs`, COSMIC
  epoch-1.11.0, write no settings). They explain workspaces and the launcher
  and point to COSMIC Settings; nothing is set.

### 8. Accessibility

- **Display scale**: 100%, 125%, 150%, 175%, 200%, the current one selected.
  It applies at once and is kept for later logins:
  - COSMIC: its output scale config, as COSMIC Settings writes it.
  - sway: `swaymsg output * scale N` now, and an `output * scale N` line in
    a user include file that the Isotope sway config includes.
  - niri: `niri msg output <name> scale N` now, and the user's niri config.
  - i3: `Xft.dpi` (96 × N) through `xrdb -merge` now, and in
    `~/.Xresources`, which the i3 session loads. X11 apps that read Xft.dpi
    follow at once; others need a restart, and the page says so on i3.
- **Larger text**: on/off, `org.gnome.desktop.interface text-scaling-factor`
  1.25 (and COSMIC's interface density/font size key on COSMIC).

The builder checks each persistence point against the variant's config
(the include file or key may not exist yet) and adds it where it is missing.

### 9. Apps

Today's curated list, as a page:

- **Browser**, one choice: Firefox (default), Chromium, Brave, LibreWolf, or
  none.
- **Essentials**, any number: image viewer (Loupe), media player (VLC), text
  editor, PDF viewer (Papers), all on; Flatpak permissions (Flatseal), off.
- Apps already installed show as installed and cannot be unticked.

The list moves into one data file, `/usr/share/isotope/first-run-apps.list`,
in today's format (`group|Flatpak ID|label|preselected`), so it is defined
once.

Continue starts `flatpak --user install --noninteractive flathub <ids>`
from the per-user `flathub` remote that `isotope-user-setup` keeps signed,
and moves on. The install runs in the background while the user continues.
The sidebar shows the progress under "Apps", and Ready waits for it with a
progress bar. On COSMIC, the chosen browser is also pinned to the dock (its
ID appended to the user's `com.system76.CosmicAppList/v1/favorites`).

- No network, or no usable Flathub remote: the page says so, with "Try
  again" and "Skip". Skipped for these reasons, the next login sends today's
  notification ("Isotope will offer a browser…"), and the notification opens
  `isotope welcome apps`.
- A failed app is listed on Ready, with "Try again".

### 10. Shortcuts

The variant's main keys, drawn as key caps: Super+Esc (the Isotope menu),
Super+D (apps), Super+F1 (all shortcuts), Super+Shift+E (power), and what
the variant adds, from `/usr/share/isotope/shortcuts.txt`, which each variant
ships. A "Show all shortcuts" link opens the full sheet as Super+F1 does.
This replaces the `isotope-welcome` notification.

### 11. Ready

What was set and installed, what failed (with Try again), and, if the
language changed, "Log out to use <language>" with Log out. Finish closes
the window and writes the stamp. Amendment (final review I1): while the Apps
batch still installs, Log out is disabled and reads "Installing apps…",
because ending the session kills the installs; it becomes Log out when the
batch ends. "You can open this again from the Isotope
menu (Super+Esc → Welcome)".

## The Backup page

Opened alone (no flow sidebar steps; the sidebar shows only "Backup") by
`isotope welcome backup`, the tray's new "Backup…" item and the Isotope
menu's "Backup" entry. Only for `admin` users: others see "Ask an
administrator", as the CLI refuses them.

- **No target yet**: "Back up this machine". Choose where: a USB disk
  (see "Disks"), or a remote target (see "Remote targets"). "Include Docker
  and Podman storage" (off). Continue creates the backup there with a new password and shows it
  once, in the recovery key's style (mono, grouped), with "I saved it".
  Or "Use an existing backup": type its password.
- **With a target**: the target, the last backup (date, size, ok or the
  error), the schedule (Daily / Weekly / Off), "Back up now" with progress,
  "Export to a file…" (`--export`), and the list of backups (date, host,
  size).
- A USB target that is not plugged in shows "Plug in <disk> to back up" and
  disables Back up now, as the CLI's scheduled run skips it.

### Disks

Nothing on Isotope can mount a removable disk today: no udisks2, gvfs or
udiskie in the image, and the CLI accepts only a folder that is already
mounted (`checkOnDisk` in `update/backup.go`). The user chose to ship
**udisks2** on the four desktop variants (built from source, with its polkit
rules: an active local session may mount removable media). It is its own PR,
before the Backup step, and also lets the desktops' file managers mount USB
disks (COSMIC Files, and others through udisks).

- The pages list removable filesystems through udisks2's D-Bus API, mount
  the chosen one as the user (`/run/media/<user>/<label>`), and let the user
  pick or create a folder on it.
- The daemon stores the target by the filesystem's UUID plus the path inside
  it, not by the mount point, so the timer finds the disk again wherever it
  is mounted. The scheduled run mounts it through udisks2 if needed (as root,
  allowed), backs up, and unmounts what it mounted. "Not plugged in" means
  no filesystem with that UUID.
- The CLI keeps accepting a plain directory; a target set by UUID is a new
  form the CLI also understands (`isotope backup status` shows the disk's
  label).

### Remote targets

The user chose to offer all three of restic's remote kinds in the GUI. Each
gets a form; the values travel to the daemon as one secret fd (never as
D-Bus strings) and the daemon writes them to `/usr/local/isotope-backup/env`
(0600), as the CLI's `init` asks people to do by hand today.

- **REST server**: URL, user, password.
- **S3** (and S3-compatible): endpoint URL, bucket, path, access key ID,
  secret access key, region (optional).
- **SFTP**: `user@host`, port, path. The daemon generates root's backup SSH
  key (ed25519, `/usr/local/isotope-backup/ssh/id_ed25519`, not `/root`,
  which is not persistent) the first time, and the page shows the public key
  with Copy, to add to the server's `authorized_keys`. Connect then fetches
  the host key, and the page shows its SHA256 fingerprint for the user to
  confirm; on yes the daemon writes it to its own `known_hosts` next to the
  key. restic's sftp runs ssh with that key, that `known_hosts` and
  `StrictHostKeyChecking=yes`, so no prompt can ever appear.

On a new machine (Restore), the same forms come first, so the snapshots can
be listed. For SFTP restore, the old machine's key is gone with it: the page
shows the new public key and says "Add this key on the server, then Connect".

The daemon methods carry the target kind and the credential fd:
`BackupInit`, `BackupList` and `Restore` take a second fd for credentials, or
one fd with both, in a small documented format. The builder picks, following
`recovery_bus.go`.

"Restore…" on this page is not offered: a whole-machine restore belongs to
a new machine (the Welcome page), a partial one stays a CLI job. This page
links to `isotope restore --help` text for that.

## Daemon API

`isotope-updated` gains backup methods on `io.kairos.IsotopeUpdate1`, under a
new polkit action `io.kairos.isotope-update.backup` (allowed to `admin`
without a password, like the others; added to the `.policy` and
`50-isotope-update.rules`). They run the same code as `isotope backup` and
`isotope restore` (`update/backup.go`, `update/restore.go`), not a shell-out
to the CLI through sudo.

Roughly (the builder fixes the exact signatures, following
`recovery_bus.go` and `ext_bus.go`):

- `BackupStatus() → (target, schedule, last result, warning)`
- `BackupInit(target, withContainers, existing bool, secret fd) → password`
  (a new backup returns its generated password once; an existing one reads
  it from the fd)
- `BackupNow(withContainers)`, `BackupExport(path)` with progress signals,
  `BackupCancel()`
- `BackupSchedule(daily|weekly|off)`
- `BackupList(source, secret fd) → snapshots` (source empty = the target)
- `Restore(source, snapshot, host, secret fd)` with progress signals; ends
  in the restart, as the CLI does. Refused unless the machine is new (see
  Welcome), under `io.kairos.isotope-update.restore` (password asked)
- Remote-target helpers for SFTP: `BackupSSHKey() → public key` (creates it
  once) and `BackupHostKey(host, port) → fingerprint`, then the confirmed
  fingerprint is passed to `BackupInit`/`BackupList`/`Restore`

Passwords always come in as a file descriptor, never as a string argument.
Callers in a Flatpak sandbox are refused, as today.

`SetLocale` (see Language) is the one non-backup method this design adds,
under its own action `io.kairos.isotope-update.settings`.

This supersedes the "A graphical backup app" non-goal of
`2026-11-16-isotope-cli-backup-design.md`.

When the feature lands, the CLAUDE.md feature table is updated in the same
PR: the "First-login welcome" and "First-run app picker" rows become one
"First-login welcome" row describing `isotope-welcome`, and the "Backup and
restore" row gains the Backup page and the tray item.

## Starting, and what it replaces

At session start, where each variant starts `isotope-welcome` and
`isotope-first-run-apps --login` today (sway and i3 `exec`, niri
`spawn-at-startup`, COSMIC an autostart file), the session starts
`isotope-welcome --login`. It:

1. exits if `~/.local/state/isotope/welcome` exists, or if any of the old
   stamps exists (`first-run-apps`, `welcomed`, or their `hadron/` copies):
   a user from before this change is never asked again;
2. otherwise opens the flow. The stamp is written on Finish, or when the
   user closes the window ("Skip setup?" asks first; closing is a skip).
   Amendment (final review I1, I2): a skip or close while the apps are not
   done writes `apps-pending`, not `skipped`, so the next login reminds:
   Apps was skipped offline, the Apps page was still checking the network
   (that counts as offline), or a batch was still installing. Only one
   whole flow runs at a time (a lock in `$XDG_RUNTIME_DIR`), and a window
   that fails before it can draw writes no stamp.

Removed or replaced:

- `rootfs/usr/bin/isotope-first-run-apps` (the terminal checklist). The
  name stays as a link that runs `isotope welcome apps`, so the menu, habits
  and old configs keep working.
- `rootfs/usr/bin/isotope-welcome` (the notification script). The binary of
  the same name replaces it.
- COSMIC: `cosmic-initial-setup` is no longer started. Its autostart file
  `/etc/xdg/autostart/com.system76.CosmicInitialSetup.desktop` is removed
  from the image, and the binary and its data are not installed, except
  `/usr/share/cosmic-layouts`, which the Layout page reads. The wait for it
  in `isotope-first-run-apps` goes away.
- Isotope menu: "Install apps" runs `isotope welcome apps`; "Backup" runs
  `isotope welcome backup`; a new "Welcome" entry runs `isotope welcome`.
- Tray: a "Backup…" item, after "Extensions…", that runs
  `isotope welcome backup`. Only shown where the image has restic, as the
  menu's entry is.

## Build

- `guikit/`: the shared module (from the refactor PR).
- `welcome/`: the app, its own Go module like `install-gui/`, built in a new
  Dockerfile stage after `install-gui-build`, with cgo and `novulkan`. The
  stage runs its tests and the `--nonsense` exit-code check as the
  installer's does.
- *Builder's call (marked as such):* **two builds of the same source.** Gio's
  X11 backend links libX11, libX11-xcb, libXcursor, libXfixes and
  libxkbcommon-x11, which sway, COSMIC and niri do not ship (the sway image
  avoids libX11 on purpose, Dockerfile ~line 2654). So the Wayland variants
  get a `-tags nox11` build and i3 gets a build with X11 (i3 already has
  the X11 libraries, and the Wayland ones for the installer). Same code, same
  tests; the image check runs `ldd` on the installed binary per variant.
- Installed on the four desktop variants. Removed by `Dockerfile.agent`.

## Tests

- **Screenshots**: every page and state (offline Network, Apps installing,
  Apps failed, Backup with and without a target, the password shown, the
  restore steps, Restore hidden on a used machine), at 1440×900 and
  1024×768, with `gioui.org/gpu/headless` and testdata PNGs, as
  `install-gui/screenshot_test.go` does.
- **Appliers**: each setting writer (locale, color scheme, COSMIC keys,
  scale per variant, layout copy, dock pin) is a small function tested
  against a temporary `$HOME` and a fake command runner: the files and
  commands it produces, per variant.
- **Login gate**: a script test for `--login` with each old stamp present
  and absent, as `test/first-run/check-first-run-apps.sh` does today.
- **Daemon**: the backup methods with the fake authorizer of the existing
  bus tests: refused without the action, secrets and credentials read from
  the fd and never logged, a Flatpak caller refused, `Restore` refused on a
  machine that is not new (each rule of the "new" list on its own), the env
  file written 0600, SFTP refused with an unconfirmed host key.
- **USB**: a target by UUID found at another mount point; an unplugged disk
  skipped by the scheduled run; what the run mounted is unmounted.
- **Image**: COSMIC has no `cosmic-initial-setup` autostart; every desktop
  variant has `/usr/bin/isotope-welcome`; the agent image does not.
- **On hardware**: a fresh COSMIC install on the laptop (the one that
  showed the layout bug) and a VM for sway, i3 and niri: run the flow, then
  log out and in, and check that nothing changed and the flow does not
  return.

## Order of work

Each step is its own PR, and each leaves the image working:

1. **Shared look** (`guikit/`), no visual change. In progress.
2. **Welcome shell and the core pages**: the app, Welcome (without
   Restore), Language, Network, Apps, Shortcuts, Ready; session wiring;
   old scripts replaced; `cosmic-initial-setup` removed.
3. **Appearance, Accessibility, and the COSMIC pages** (Layout, Workflow,
   Launcher).
4. **udisks2** on the four desktop variants, with its polkit rules.
5. **Backup**: the daemon methods and polkit actions, USB targets by UUID,
   the remote forms (REST, S3, SFTP with key and host-key confirmation), the
   Backup page, the tray item and menu entry.
6. **Restore** on the Welcome page, with the "new machine" rule in the
   daemon.

Every variant must get the same first login on the same day (the user's
requirement: "the same experience" everywhere). So steps 2 and 3 land
together, in one release: no release ships Welcome on some variants and
`cosmic-initial-setup` on COSMIC, or Welcome without its Appearance and
Accessibility pages on some variants. They may be separate PRs merged back
to back, or one PR; the image between them is never released.

## What is left to the builder

- The exact `guikit` API (from the refactor) and the page code.
- NetworkManager D-Bus calls vs `nmcli`: D-Bus preferred; either is fine if
  tested.
- The D-Bus method signatures and progress signals, following the existing
  bus files.
- The COSMIC config keys for accent, frosted, workflow, launcher and scale:
  read from `cosmic-initial-setup` and `cosmic-settings` at the COSMIC
  version the Dockerfile pins.
- Locale display names: from the locale's own `LC_IDENTIFICATION` data, or
  a small embedded table.
- Translating Welcome itself: out of scope now; keep the strings in one
  place so it can be added.
