# Automatic updates (kairos-community/isotope#136, batch 2)

## Intent

An administrator can turn on "Automatic updates". From then on the machine
installs a newer release of its own flavor and image line into the other A/B
slot by itself, in the background, and says "Restart to finish". Nothing
restarts by itself. The setting is off by default.

#136's wording: an "Automatic updates" setting on the Updates page: off, or
download and install at the next restart. `isotope-updated` stages the
upgrade, and the tray says "Restart to finish".

## Decisions

1. **Where the setting lives.**
   - `isotope-updated` method `SetAutoUpdate(b on)`, behind the existing
     polkit action `io.kairos.isotope-update.settings` (admin only, no
     Flatpak callers). The action is "machine settings an administrator
     changes" (today the system language); an on/off switch is that. A new
     action would add a policy entry and nothing the admin rule does not
     already decide, since `50-isotope-update.rules` answers every
     `io.kairos.isotope-update.*` action the same way.
   - It persists in `/oem/96_isotope_autoupdate.yaml` (96 is free: the /oem
     files are 90-95 and 99). The file is a cloud-config with only a `name`
     and the `isotope_autoupdate: {enabled: BOOL}` key, which yip ignores.
     Unlike the firewall nothing is written under /etc at boot: only the
     daemon and the CLI read the setting, and they read /oem.
   - No file, or a file that does not parse, is off.
   - `AutoUpdateStatus` (open to all, like `FirewallStatus`) returns the
     setting, the last run and the staged version.
   - CLI: `isotope update auto [status|on|off]`. Isotope System: an
     "Automatic updates" section on the Updates page with a Turn on/Turn off
     button (the Firewall section's pattern; guikit has no switch widget).
2. **Who runs it.** The root daemon, from `isotope-autoupdate.timer`
   (daily, `RandomizedDelaySec=1h`, `Persistent=true`), which starts
   `isotope-autoupdate.service`: `isotope update --auto` as root.
   - The CLI reads the setting first and exits when it is off, so an off
     machine never even activates the daemon.
   - Otherwise it calls `RunAutoUpdate` on the daemon, which accepts only
     uid 0 (the bus vouches for the uid). Root is not in `admin`, so polkit
     would refuse it; and an admin who wants an install now has "Install".
   - The decision and the install run in the daemon, so they share the one
     job slot, the reboot marker and the signals the tray follows.
   - The unit has the firewall's `ConditionPathExists=!` lines for
     `/run/cos/live_mode`, `/run/cos/install_mode`,
     `/run/cos/uki_install_mode`, and also `/run/isotope/live-desktop`.
3. **When it skips.** In this order; each skip is recorded as the last run:
   - the setting is off (not recorded: "it does nothing");
   - the system is unmanaged (no release file);
   - a live, install or Trusted Boot medium boot (the three markers);
   - the daemon is stopping or a reboot was asked;
   - an update is staged, or an install failed and waits for a restart;
   - another job holds the slot;
   - the connection is metered: NetworkManager's `Metered` is `yes` or
     `guess-yes`. No NetworkManager on the bus counts as not metered; any
     other error skips (it could be metered);
   - on battery below 30%: UPower's `OnBattery` and the display device's
     `Percentage`. No UPower (the server) counts as not on battery; a
     battery whose level cannot be read skips;
   - a pin: the user installed an older release on purpose, and "Install
     the latest" would undo that, so the automatic run does not.
   - (review) a boot of the recovery system, or of the previous system
     (`/run/cos/passive_mode`: a rollback, or the boot loader's fallback
     after the new system did not boot): "booted into the previous version
     (rollback)". The unit has both as `ConditionPathExists=!` too.
   - (review) a held release: `isotope rollback` holds the release it
     leaves, and a passive boot holds the newest installed release in the
     history. The run never installs the held release (or an older one)
     again; a newer one installs, and installing the held one by hand lifts
     the hold. The hold is `/usr/local/isotope-update/autoupdate-hold`.
4. **What it installs.** The newest published release, through the same
   steps as "Install the latest": `Check`, `ReleaseRef` for the running
   flavor and line, then `evaluate` (Resolve, `Classify` against the
   published list). It installs only when the verdict is trusted, of the
   same flavor and line, its version is that newest tag, and that is newer
   than the installed one. Never advanced mode, never another flavor or
   line, never a downgrade. The digest it resolved is passed to the same
   `Apply` job, which resolves and classifies again before kairos-agent.
5. **What the user sees.**
   - Every install that ends well writes `/run/isotope-update/staged`
     (`<version> <auto|manual>`). `Status` gets `staged` and `staged_auto`
     while the state is reboot-pending after a finished install.
   - Tray: the status line is "Isotope vX is installed: restart to finish";
     after an automatic install a notification "Restart to finish" with a
     Restart action, once per staged version (`$XDG_STATE_HOME/
     isotope-update/staged-notified`). The action is the menu's Restart.
   - Updates page: the headline "Version vX is installed and starts at the
     next restart" when a version is staged; the section shows the last
     run.
   - A failure is logged by the daemon and kept as the last run. It is not
     a notification, so a retry each day does not notify each day.
6. **The server.** The same timer, unit and setting; no tray. `isotope
   update auto status` prints the setting, the last run and a staged
   version.
7. **The agent image** removes the timer, the unit and the timer link, as it
   removes the update service.

## State

- Setting: `/oem/96_isotope_autoupdate.yaml`.
- Last run: `/usr/local/isotope-update/autoupdate` (next to the install
  history, because Kairos rebuilds /var): one line, tab-separated: unix
  time, result (`installed`, `up-to-date`, `skipped`, `failed`), version,
  detail. An automatic install writes `installed` (or `failed`) when the job
  ends; the start of the job is not a result.
- Staged version: `/run/isotope-update/staged`, a tmpfs file, so the reboot
  clears it with the reboot marker.

## Tests

- Go, `cmd/isotope`: the setting file round trip and off by default; every
  skip with fakes for NetworkManager, UPower and the clock, and the fake
  backend (the upgrade runner); the install path through the fake registry
  and GitHub (same flavor and line, newest, digest); never a downgrade,
  never another flavor, never with a pin; the result of a failed job; the
  uid-0 gate and the settings action on the bus; the CLI `auto` and
  `--auto`; the tray's status line and the one notification per version.
- Go, `cmd/isotope-system`: the section's view per state, the Turn on/off
  click, the staged headline; screenshot goldens for the new Updates states
  (new files only).
- `test/polkit/check-polkit-rules.sh` (unchanged action set, run anyway).
- `test/update/check-autoupdate.sh --repo` (CI) and `IMAGE`: the timer, the
  unit, its conditions, the enable link in both image stages, the
  server-config copy, the agent removal.

## Not checked here

- A real timer run on a VM: metered Wi-Fi, a laptop on battery, a staged
  upgrade that boots. The manual test list is in the report.
- That every notification server shows the Restart action (mako needs
  `makoctl menu` or its default action, dunst its context menu).
