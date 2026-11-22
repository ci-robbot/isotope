# Isotope System: one window to manage the system

## Intent

Today every system job on Isotope ends in a terminal. The update icon
(`isotope-update tray`) and the Isotope menu (`isotope-menu`, Super+Escape)
open a terminal that runs `isotope upgrade`, `isotope switch`, `isotope ext`,
`isotope backup` or `isotope recovery` and waits for Enter. The maintainer:
"its not only the updates app - but the whole thing there. e.g. the sys
extensions install, backup/restore etc", and "lets make it a real app for
managing the system". A later scope change adds users, disks and network.

**Isotope System** is that app. It is one Gio window with a sidebar, with
the look of the graphical installer (`install-gui/`) and Isotope Welcome
(`welcome/`): the same `guikit/` package, the Isotope palette, the same
buttons and fields. The tray stays a notifier and opens a page of this app.
The Isotope menu's entries open the same pages.

## Goals

- One window for Isotope's system jobs, on sway, i3, COSMIC and niri, the
  same on each.
- Pages: Updates, Desktop & image, Extensions, Backup, Security, Network,
  Users, Disks, About.
- No terminal. The app calls the services the CLI calls, through the same
  D-Bus interface (`io.kairos.IsotopeUpdate1`) and the same polkit actions.
- The tray and the Isotope menu open the right page of the app.
- `isotope system [PAGE]` opens it, like `isotope welcome [PAGE]`.
- Non-admin users can open every page and read it; the buttons that change
  the system say "Only an administrator can …" and are off. The daemon
  decides again on every call.

## Non-goals

- Sound, displays, keyboard, mouse, appearance, Bluetooth, power. Each
  desktop already has them (COSMIC Settings, Noctalia, the `isotope-*-menu`
  scripts), and Isotope Welcome sets the look once.
- The server edition (no desktop) and the agent image (no admin user, no
  update service): neither ships the app. The CLI stays the way to do every
  job there.
- A settings daemon of its own. Privileged work stays in `isotope-updated`.
- Replacing the CLI. Every page has a CLI equivalent; the CLI stays the
  reference and the way to script.

## Anti-goals

- Running `sudo`, `pkexec` or a terminal from the app.
- Re-implementing a privileged job in the app (computing what to install is
  fine, installing it is the daemon's job).
- Sending a secret (a passphrase, a recovery key, a password) as a D-Bus
  string. Secrets go through a pipe fd, as `recovery_bus.go` does.

## Name

The binary is `isotope-system`, the window title "Isotope System", the
Wayland app ID and X11 class `isotope-system`. The user-facing command is
`isotope system [PAGE]`, a subcommand of `isotope` (AGENTS.md "Names":
a new user-facing command is a subcommand of `isotope`). It runs
`isotope-system` with the same arguments, as `isotope welcome` runs
`isotope-welcome`.

Why a second binary at all: `isotope` is pure Go, built with
`CGO_ENABLED=0`, and runs on the server, which has no Wayland, EGL or fonts.
The GUI needs cgo and those libraries, so it cannot live in that binary.
Isotope Welcome made the same choice for the same reason.

"System" and not "Settings" (the maintainer's choice): on COSMIC the app
sits next to "COSMIC Settings", and two "Settings" apps in one launcher
would be easy to confuse. "System" also says what it manages: the
image, its updates and extensions, backups, the disk, the users. The
Isotope menu entry is "System" and the tray item "Isotope System…".

## Approaches considered

1. **A new Gio module that talks to the daemon directly (picked).** A module
   `system/`, like `welcome/`, with its own small client for
   `io.kairos.IsotopeUpdate1` and NetworkManager, its models as plain Go
   types with unit tests, and its pages on `guikit/`. The daemon stays the
   one place privileged work happens; the app is a client next to the CLI
   and the tray. The release file and the release tags (which image a
   release is for a flavor and line, which tag is newer) move from
   `update/` into the importable package `update/isorelease`, which the CLI
   and the app both use, so they never compute different images. The cost
   left: a second client of the same D-Bus interface (the CLI's is package
   `main` in `update/` and cannot be imported). Welcome already keeps its
   own client for `SetLocale`. A follow-up can move the client into an
   importable package too.

2. **The app drives the CLI.** The app runs `isotope upgrade --yes`,
   `isotope ext install`, … as child processes and parses their output.
   It reuses the CLI to the letter, but the CLI's output is prose for
   people, not an interface; a reworded line breaks the app. The CLI's
   backup path goes through `sudo`, which needs a terminal for the password.
   And progress, refusals and questions (the inspect report, "Install
   anyway?") would need a machine mode in every subcommand. Rejected.

3. **Fold the pages into Isotope Welcome.** Welcome is a flow (Back,
   Continue, a stamp); a settings app is a set of independent pages. One
   binary with two modes would make both harder to read, and Welcome's
   first-login gate and stamp logic would leak into every system job.
   Rejected. Welcome's planned "Backup" page (2026-11-20 design) moves here,
   and Welcome's `backup` argument, which only says "not available yet"
   today, can open `isotope system backup` later.

## The window

`guikit.Window` and `Theme.Frame`, as the installer and Welcome. The sidebar
is a navigation list, not a step list: no ticks, the current page lit, every
row clickable, and Up/Down keys move between pages. guikit gains
`Theme.NavSidebar` for it, next to the step-list `Sidebar` it keeps. The
note at the bottom is "Isotope v0.1.2 · sway", as in Welcome.

Each page is a scrolled body (`guikit.Scrolled`, `guikit.Pad`) and, when it
has actions, a footer with them. No Back or Continue: the sidebar moves
between pages. Esc closes a dialog; Ctrl+Plus, Ctrl+Minus and Ctrl+0 zoom
(`guikit.WindowKeys`). A job that runs (an upgrade, an extension, a backup)
keeps running when the user goes to another page, and its page shows it on
return; closing the window while the daemon runs a job leaves the job to the
daemon (it never depended on the window), and the tray reports the end.

One instance per user: a second `isotope system PAGE` asks the first one
to show PAGE, over the session bus (`io.kairos.IsotopeSystem1`,
`ShowPage(s)`), and exits. Without the session bus it opens a second window.

## Pages

Each page lists what it shows, what it does, and the backend.

### Updates

- Shows: "Isotope v0.1.2 is up to date" / "v0.1.3 is available" (the tray's
  status line, `Status` on the daemon), the pin when there is one, the
  advanced mode, the last progress line of a running job.
- **Check now**: `Check`, then `Status` and `Releases`.
- **Install vX.Y.Z** (latest) and **Other releases** (a pick list of the
  published tags, older ones too): computes the reference like the CLI
  (`isorelease.LineTag`, as `ReleaseRef`), calls `Inspect`, shows the report
  the CLI prints (image, digest, flavor, version, graphics line, source),
  asks "Install vX.Y.Z? The running system is not touched; the new one is
  used from the next boot.", then `Apply` and follows `StateChanged` and
  `Progress` signals: progress lines, a spinner, Cancel while inspecting.
- At the end: "Restart now" (`Reboot`). A failure shows the daemon's words
  and "Nothing changed on the running system; you can retry safely", or, for
  `pending=failed|started`, "restart before trying again", as the CLI.
- **Restart into the previous system** (`RebootPrevious`), behind a
  confirmation, hidden in the states the tray hides it.
- **Install an image…** in advanced mode, never on Trusted Boot: a field for
  an OCI reference, the same inspect report, and the overridable refusal
  asked as its own question ("This is not a published Isotope release.
  Install it anyway?"), never pre-answered.
- History: not in phase 1. The daemon keeps no history of installs; the
  page shows the installed release and the boot entries' names instead
  ("previous system: fallback"). A history needs the daemon to record one.

### Desktop & image

- Shows the running desktop (sway, i3, COSMIC, niri), the image line
  (hardware, virtual machine, Trusted Boot) and the edition.
- **Switch desktop**: four cards, the current one marked. Picking another
  shows the CLI's warning ("Your per-user desktop settings may not carry
  over, and the login screen may change."), then the same inspect, apply
  and restart flow as Updates, with `LineRef(flavor, line, newest)`.
- **Switch image line**: hardware ↔ virtual machine with `lineWarning`'s
  text. Not on Trusted Boot (one line).
- Hidden on the server and agent (the app is not there anyway).

### Extensions

- Two lists: **Installed** (`Extensions`: name, version, enabled, source)
  and **Available** (`Catalog`: the catalogs' entries, name, description,
  versions). A search field filters both.
- Installed row: Enable/Disable (`SetExtensionEnabled`), Remove
  (`RemoveExtension`, behind a confirmation), Files (`ExtensionFiles`).
- Available row: Install (`InstallExtension(name, "", false)`); a version
  pick when the entry has several.
- A refusal for conflicting files (`ext_result=conflict`) shows the daemon's
  sentence and an "Install anyway" that repeats the call with `force`, as
  the CLI's `--force`.
- In advanced mode, "Install from a reference…": a field for an OCI
  reference.
- Progress: the extension job's lines; the outcome is `ext_result` and
  `ext_detail` from `Status` when the state leaves `extension`.

### Backup

Phase 1 (the daemon has these, added in this change):

- **Status**: target, schedule, last backup, last result and the 14-day
  warning: `BackupStatus` on the daemon (no polkit: the config "holds no
  secret", and `isotope status` already reads it as any user).
- **Back up now**: `BackupNow`, a job in the daemon's one job slot
  (`StateBackup`, progress lines from restic), under the new polkit action
  `io.kairos.isotope-update.backup` (admin, no Flatpak callers). The same
  `backupRun` the CLI runs as root.
- **Schedule**: Daily, Weekly, Off: `SetBackupSchedule`, same action.
- Without a target: "This machine has no backup yet", and in phase 1 the
  CLI command that sets one up.

Phase 2, as designed in `2026-11-20-isotope-welcome-design.md` ("The Backup
page", "Disks", "Remote targets", "Daemon API"), which moves here: choosing
a USB disk or a remote target, `BackupInit` with the password shown once,
the list of backups, export, and restore. Welcome keeps only "Restore from
a backup" on its first page, which calls the same methods.

### Security

Only where it applies: on an encrypted disk (the installer's marker
`/etc/isotope/encryption`) or Trusted Boot. Elsewhere the page says the disk
is not encrypted and that encryption is chosen at install time.

- Shows (`RecoveryStatus`, admin only): the edition, each LUKS container
  with its keyslots (passphrase, recovery key, TPM, whether the TPM unlocks
  it).
- **New recovery key**: the CLI's flow in a dialog: the current passphrase
  or recovery key (a secret field; skipped when the TPM unlocks every
  container), the new key generated in the app with crypto/rand, shown once
  in mono two groups per line, "I saved it", type one group back, then
  `RecoveryNewKey` with both through a pipe fd. Done in phase 2.
- **Reseal** (Trusted Boot, after a firmware update): `RecoveryReseal`.
  Done in phase 2.

### Network

Wi-Fi and wired, through NetworkManager on the system bus. NetworkManager
runs with `auth-polkit=false` on the desktops, so any local user may change
connections, as from the Wi-Fi menus today.

- Status: connected or not, the primary connection, each device (Wi-Fi,
  Ethernet) with its state and IPv4 address.
- Wi-Fi: on/off (`WirelessEnabled`), the networks in range (strongest
  first, a lock on the secured ones, the connected one marked), Connect
  (password field for secured ones; WPA3-only gets `sae`, as Welcome),
  Disconnect, and **Forget** for a saved network (delete its connection
  profile). Hidden network: SSID and password.
- Wired: state and address only.
- VPN: not in phase 1. NetworkManager's VPN plugins are not in the image;
  WireGuard profiles work through NM without a plugin and can be a later
  addition (import a `.conf`).

The NetworkManager code is a copy of Welcome's `nm.go` extended with forget,
Wi-Fi on/off and devices. A follow-up moves both onto one shared package.

### Users (phase 2, done)

On Kairos `/etc/passwd` and `/etc/shadow` are rebuilt at every boot from the
cloud-configs in `/oem`: the installer writes the first user there. A user
added only to `/etc/passwd` (`useradd`, accountsservice) is gone after a
reboot. So the page changes users through the daemon, which writes both:

- New daemon methods under a new polkit action
  `io.kairos.isotope-update.users` (admin only): `Users()` (no polkit: name,
  full name, uid, admin, from `/etc/passwd` and `/etc/group`),
  `AddUser(name, fullName, admin, fd)`, `RemoveUser(name, keepHome)`,
  `SetPassword(name, fd)`, `SetAdmin(name, on)`. The password comes in through
  a pipe fd and is stored as a SHA-512 crypt hash, never in clear.
- Each change is applied now (`useradd`, `chpasswd -e`, `gpasswd`) and
  written to `/oem/95_isotope_users.yaml`, a yip `users:` stanza the boot
  applies again. The installer's user stays in its own file; a change to it
  is written as an override in ours.
- Guards: the daemon refuses to remove or demote the last member of
  `admin`, and refuses system accounts (uid < 1000) and root.
- The page: the list; Add (name, full name, password twice, admin
  switch); per user Change password, Administrator on/off, Remove (with
  "keep the home folder", default on). A user changes their own password
  with the same method (the daemon allows a caller to change their own
  password without the admin action).
- COSMIC Settings' Users page talks to accountsservice, which writes only
  `/etc`; its changes may not survive a reboot on Kairos.

  **Decision (maintainer, phase 2):** if a booted test confirms that users
  added there are lost at reboot, Isotope hides COSMIC Settings' Users page,
  and Isotope System is the one place users change.

  The booted test, on an installed COSMIC image (not the live ISO), as the
  installer's admin user:
  1. In COSMIC Settings > System > Users, add a standard user `probe` with
     a password, and turn Administrator on for it. Change the full name of
     the installer's user.
  2. Before the reboot, record `getent passwd probe`, `getent group admin`,
     `ls -ld /home/probe` and the full name, and log in as `probe` at
     cosmic-greeter once.
  3. Reboot. Check again: does `probe` exist in `/etc/passwd` and
     `/etc/shadow`, is it still in `admin`, can it log in with its
     password, is the full name change kept? Note whether `/home/probe`
     survives without its account (the home is on the persistent partition,
     so an orphaned home is the likely result).
  4. Hide the page if any of these is lost after the reboot. Keep it if
     all of them survive.

  How to hide it, without a patch: cosmic-settings epoch-1.11.0 compiles each
  page behind a cargo feature, and `page-users` is part of its default
  `linux` feature set (`cosmic-settings/Cargo.toml`). The
  Dockerfile's `cosmic-apps` stage builds it through `tools/cosmic-build.sh`,
  which passes `COSMIC_CARGO_ARGS` to the single `cargo build`. Set it for
  cosmic-settings to `-p cosmic-settings --no-default-features --features`
  followed by the default list without `page-users`: `a11y, avif,
  single-instance, wgpu, systemd, cosmic-comp-config, page-accessibility,
  page-about, page-bluetooth, page-date, page-default-apps, page-display,
  page-input, page-legacy-applications, page-networking, page-power,
  page-region, page-sound, page-window-management, page-workspaces,
  xdg-portal, wayland`. Without the feature the System section has no Users
  sub-page and `cosmic-settings users` opens nothing. `build.rs` still
  writes `com.system76.CosmicSettings.Users.desktop`, so the stage also
  deletes it (and the `.Users.policy` file the justfile installs) from
  `$COSMIC_OUT`, and asserts that the file is gone, so that launchers have no
  entry that opens nothing. When COSMIC moves to a new epoch, check the
  feature list again against the new tag. A patch under
  `patches/cosmic/cosmic-settings/` is not needed and is not added. Any
  change to that build (this one too) rebuilds COSMIC, so it is made only
  after the booted test.

### Disks (phase 2, done)

udisks2 2.12 is in the image (Dockerfile `udisks2` stage), with
`50-isotope-udisks.rules` (an active local session may mount, eject and
power off removable disks) and `90-isotope-udisks-ignore.rules`, which marks
the Kairos partitions (`COS_*`, `*_LUKS`, fixed LUKS volumes) as system
devices udisks2 hides.

- The page lists every drive (`org.freedesktop.UDisks2` ObjectManager):
  model, size, removable or not, partitions with label, filesystem, size and
  usage (`statfs` of the mount point).
- The system disk and its partitions (`COS_STATE`, `COS_PERSISTENT`,
  `COS_OEM`, `COS_RECOVERY`, `COS_GRUB`, the `*_LUKS` containers and any
  device udisks2 marks `HintSystem` or `HintIgnore`) are shown read-only,
  with "Isotope's system disk" and no buttons. The rule is in the app
  (`isSystemDevice`, unit-tested) and, independently, in udisks2's own
  policy (system devices need `filesystem-mount-system` / `modify-device-
  system`, which no Isotope rule grants).
- Removable media: Mount, Unmount, Eject, Power off, through udisks2 as the
  user (allowed by the existing rule).
- **Format** a removable disk: one partition, exFAT or ext4, a label.
  **Decision (maintainer):** an admin formats a removable disk without a
  password, after a clear confirmation dialog. The dialog names the disk
  (model, size, device), lists what is on it now (partitions, labels,
  used space), and says "Everything on this disk will be erased. This
  cannot be undone." The destructive button says "Erase and format" and
  Cancel has the focus. A disk that is mounted is unmounted first, and the
  dialog says so. udisks2's `Format` needs
  `org.freedesktop.udisks2.modify-device`, which defaults to
  `auth_admin_keep`, and the sway, i3 and niri sessions run no polkit agent
  to ask. So phase 2 adds to `50-isotope-udisks.rules` the equivalent of
  `allow_active` yes, but only for members of `admin`: `modify-device` (and
  the `filesystem-mount` that follows it) is YES, without authentication,
  for a local, active subject in group `admin`. It stays NO for every
  other subject, inactive and remote sessions included, and
  `modify-device-other-seat` is unchanged. The system disk and its
  partitions stay read-only: udisks2 asks for `modify-device-system` for
  those, which no Isotope rule grants, and the app shows them without
  buttons. No daemon change.

### About

Isotope version, Hadron version, desktop, image line, edition, kernel,
hostname, the release notes link of the installed version
(`https://github.com/kairos-community/isotope/releases/tag/<tag>`, opened
with `xdg-open`), and the licence.

## How the desktops' own settings fit

The rule (AGENTS.md): every variant has the same Isotope features, reached
the same way. So every page exists on every variant. Per page:

| Page | sway, i3 | niri (Noctalia) | COSMIC |
|---|---|---|---|
| Updates, Desktop & image, Extensions, Backup, Security, About | only here | only here | only here |
| Network | here; the Wi-Fi menu stays | here; Noctalia's panel stays | here; COSMIC Settings' Network stays |
| Users | only here | only here | here; COSMIC Settings' Users page hidden if a booted test confirms it does not persist (see Users) |
| Disks | only here | only here | here; COSMIC has no disk manager |

Network is duplicated on purpose, not linked out: every variant must reach
it the same way, and both sides drive the same NetworkManager, so they never
disagree. Each Network page has a line "Also in COSMIC Settings / the
control center" with a link that opens it (`cosmic-settings wireless`,
`noctalia msg panel-open control-center network`), so a COSMIC user who
prefers COSMIC's page can go there.

## Privilege model

| Job | Path | Who |
|---|---|---|
| Read status, releases, catalog, extensions, backup status | `io.kairos.IsotopeUpdate1`, no polkit | any local user |
| Install, switch, cancel, restart, rollback | daemon, `io.kairos.isotope-update.apply` / `.reboot` | admin |
| Extensions | daemon, `.extensions` | admin |
| Recovery key | daemon, `.recovery`, secrets as a pipe fd | admin |
| Backup now, schedule (phase 1) | daemon, new `.backup` | admin |
| Backup setup, restore (phase 2, done) | daemon, `.backup`; restore `.restore` (YES for admin, no password; see the Welcome design) | admin |
| Users (phase 2, done) | daemon, new `.users`, passwords as a pipe fd | admin; own password: the user |
| Network | NetworkManager, `auth-polkit=false` | any local user |
| Disks: mount, eject | udisks2, `50-isotope-udisks.rules` | active local session |
| Disks: format (phase 2, done) | udisks2, extended rule | admin, active local session |

`50-isotope-update.rules` already answers every `io.kairos.isotope-update.*`
action YES for `admin` and NO for others, without interaction, so the new
actions need only their `.policy` entries. The app asks `getgrnam("admin")`
whether the user is a member only to start read-only; the daemon decides.

## Tray and menu hand-off

The tray keeps its icon, its notifications and its status line. Its items
that opened a terminal open the app instead:

| Tray item | Today | Now |
|---|---|---|
| Install vX.Y.Z | terminal `isotope upgrade` | `isotope system updates` |
| Other releases ▸ vX | terminal `isotope upgrade --version vX` | `isotope system updates` (the release is picked there) |
| Switch desktop ▸ X, the other image line | terminal `isotope switch` | `isotope system desktop` |
| Install an image… | terminal | `isotope system updates` |
| Extensions… | `isotope-menu Extensions` | `isotope system extensions` |
| Recovery key… | `isotope-menu "Recovery key"` | `isotope system security` |
| Cancel, Restart now, Restart into the previous system | the daemon | unchanged |
| — | — | new: "Isotope System…" (`isotope system`) |

When `isotope-system` is not installed (an image built before it, a
custom build), the tray falls back to the terminal as today.

`isotope-menu`: "Update", "Extensions", "Backup" and "Recovery key" run
`isotope system updates|extensions|backup|security`, with the old
terminal flows kept as the fallback when `isotope-system` is missing. A
new "System" entry opens the app on its first page. **Decision
(maintainer):** the menu keeps both, the "System" entry and the separate
Update, Extensions, Backup and Recovery key entries that open their pages,
so the jobs people reach from the menu today stay one step away.

`isotope system` pages: `updates` (default), `desktop`, `extensions`,
`backup`, `security`, `network`, `users`, `disks`, `about`. An unknown page
exits 2, as `isotope-welcome`.

## Per-variant wiring

- Built in the Dockerfile like Welcome: a `system-build` stage, two
  builds (`-tags nox11,novulkan` for sway, COSMIC and niri; `-tags novulkan`
  with X11 for i3), `system-${DESKTOP}` picks one, copied in
  `desktop-default` as `/usr/bin/isotope-system`. The stage runs the Go
  tests.
- `/usr/share/applications/isotope-system.desktop`, so launchers (fuzzel,
  COSMIC's launcher, Noctalia, `isotope-apps`) list it as "Isotope
  System".
- Window rules: float it by app ID (sway `for_window [app_id=…]`, i3
  `for_window [class=…]`, niri `window-rule`), as Welcome. COSMIC needs no
  rule.
- Dockerfile.agent removes it and its desktop file; the server never builds
  it.
- AGENTS.md: a new feature-table row "Isotope System", and the rows for
  the tray, extensions, recovery key and backup say the app is how they are
  reached.

## Testing

- `system/`: Go unit tests for the models, with a fake daemon and a fake
  NetworkManager: page arguments, the update state machine (what the Updates
  page offers in each daemon state, mirroring `BuildMenu`), the inspect
  report text, the extension list merge (installed ∪ catalog), the backup
  status lines, the release reference (`lineTag` cases copied from
  `update/release_test.go`), the system-device rule for Disks. No pixel
  tests in phase 1; Welcome's screenshot harness can be reused later.
- `update/`: tests for `isotope system` (execs the binary with the page),
  the tray's new click targets and its terminal fallback, `BackupStatus`,
  `BackupNow` and `SetBackupSchedule` with the existing fakes.
- `guikit/`: `NavSidebar` row layout test (pure Go).
- CI: a `system tests` step like `welcome tests`, and
  `test/system/check-system-menu.sh` for the menu and the wiring.
- Not verified without a booted image: the D-Bus calls against a real
  daemon, NetworkManager and polkit; the window rules; the tray hand-off.
  `test/system/check-system-image.sh` (a later change) checks a built
  image for the binary, the desktop file, the window rules and the menu.

## Phases

- **Phase 1 (first PR)**: the app, its window and sidebar; Updates, Desktop
  & image, Extensions, Network, Security (status), Backup (status, back up
  now, schedule; new daemon methods), About; `isotope system`; tray and
  menu hand-off; Dockerfile, every variant, AGENTS.md. Users and Disks show
  in the sidebar as "coming" pages that say what they will do and which
  CLI or desktop tool does it today.
- **Phase 2** (done): Users (daemon methods, `/oem` persistence), Disks (udisks2,
  format rule), Backup setup and restore (the Welcome design's API), the new
  recovery key and reseal dialogs, an update history.

## Decisions on the open questions

The maintainer answered the four questions of the first draft:

1. Name: "Isotope System" (`isotope-system`, `isotope system [PAGE]`),
   not "Isotope Settings". See Name.
2. COSMIC Settings' Users page: hidden on Isotope if a booted test
   confirms that users added there are lost at reboot. Phase 2; the test
   and the way to hide it (a cargo feature, no patch) are under Users.
3. The Isotope menu keeps both the "System" entry and the separate Update,
   Extensions, Backup and Recovery key entries. See Tray and menu
   hand-off.
4. Admins may format a removable disk without a password, after a clear
   confirmation dialog; the system disk and its partitions stay
   read-only. Phase 2; see Disks.
