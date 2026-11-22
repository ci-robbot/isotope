# Plan: Isotope System, phase 1

Design: `docs/superpowers/specs/2026-11-21-isotope-system-app-design.md`.

Gio packages need cgo headers this host lacks; build and test them in a
container (`golang:1.25-alpine` + `wayland-dev libxkbcommon-dev mesa-dev`
and the X11 -dev packages for the i3 build). `update/` tests run on the host.

## Tasks

1. **guikit: `NavSidebar`.** A sidebar of clickable rows (no ticks), the
   current one lit, with a `Nav` struct holding one `widget.Clickable` per
   row. Test: `Nav.Clicked` returns the index of a clicked row; rows count.
2. **update: backup over D-Bus.** TDD in `update/`:
   - `BackupStatus()` → `a{sv}` (target, schedule, last, result, warning,
     available) from `loadBackupConfig`; no polkit.
   - `SetBackupSchedule(s)` under `io.kairos.isotope-update.backup`.
   - `BackupNow()`: a job in the daemon's slot, `StateBackup`, progress
     lines, outcome in `backup_result` / `backup_detail`; runs `backupRun`.
   - Policy action in `io.kairos.isotope-update.policy`; introspection XML;
     tray `statusLine`, `busyState`, `installsAllowed` know `StateBackup`.
3. **update: `isotope system [PAGE]`.** Execs `/usr/bin/isotope-system`
   like `cmdWelcome`. Tray clicks open its pages when the binary is
   there, the terminal otherwise; new "Isotope System…" item. Tests.
4. **system/ module, models (pure Go, TDD).**
   - `args.go`: page arguments.
   - `release.go`: `/etc/isotope/release` subset, `lineTag`, `refFor`.
   - `bus.go`: client of `io.kairos.IsotopeUpdate1` (Status, Check,
     Releases, Inspect, Apply, Cancel, Reboot, RebootPrevious, Extensions,
     Catalog, InstallExtension, SetExtensionEnabled, RemoveExtension,
     RecoveryStatus, BackupStatus, BackupNow, SetBackupSchedule, Watch).
   - `updates.go`: what the Updates page offers per state; inspect report.
   - `extensions.go`: merge installed and catalog rows; filter.
   - `nm.go`: NetworkManager (from welcome) + devices, forget, Wi-Fi on/off.
   - `system.go`: is the user admin, desktop, edition, encryption marker.
5. **system/ UI.** `main.go` (window, single instance over the session
   bus), `ui.go` (frame, sidebar, page switch, dialogs), one `page_*.go`
   per page. Build both tag sets in the container.
6. **Wiring.** Dockerfile `system-build` + `system-${DESKTOP}` and the
   copy; Dockerfile.agent removal; desktop file; window rules for sway, i3,
   niri; `isotope-menu`; CI step; desktop-images path filter.
7. **Docs.** AGENTS.md feature row and the rows that mention the terminal.
8. **PR** (draft) with what is verified and what is not.
