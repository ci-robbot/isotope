# The Apps page in Isotope System (kairos-community/isotope#136, batch 2)

## Intent

One place to find, install, update and remove Flathub apps, on every
variant: sway, i3, niri and COSMIC. COSMIC keeps COSMIC Store; the Apps page
is there too, so a user who switches desktops finds the same tool, reached the
same way. Apps update by themselves by default, but an update that asks for
more permissions waits for the user.

#136's wording: search, install, update and remove Flathub apps, built on
Isotope Welcome's Apps page; it also lists Isotope's system extensions.

The mocks the maintainer approved (2026-11-24) are the canvas
https://claude.ai/artifact/NirFA9JCqgdFCgAZv2MeoW: Browse, Search, App
details, Installed, Installing and removing, COSMIC, Updates. The page
follows them; where this note and a mock differ, this note wins.

## Decisions

1. **Where it lives.** A new Isotope System page, `apps`, the first entry
   of the sidebar (`isotope system apps`, `PageApps`). Three tabs: Browse,
   Installed, Updates (with the count, "Updates · 4", when there are any).
   The Isotope menu's "Install apps" entry opens it (today it opens Isotope
   Welcome's Apps page). Isotope Welcome's first-login Apps page stays as it
   is: a short pick-a-browser step is the right shape for a first login.
2. **Whose apps.** The user's own installation only: `flatpak --user`, as
   Isotope Welcome does. No password, no polkit, no daemon. Apps in the
   system installation (`--system`), if any, are not listed or touched.
   The Flathub remote is the user's `flathub`; when `FlathubReady` (Isotope
   Welcome's rule) is false the page says Flathub is not set up for this
   user and offers nothing that needs it.
3. **Shared code.** A new package `pkg/flatpak` (pure Go, no cgo), used by
   Isotope System and by the `isotope` CLI (the automatic updates), and
   later by Isotope Welcome. It runs the `flatpak` CLI through a runner
   interface that tests replace, and parses the appstream catalog. Isotope
   Welcome's own `flatpak.go` may move onto it in the same change if that is
   small; it must not change Welcome's behaviour or goldens.
4. **The catalog.** Browse and Search read Flathub's appstream data that
   flatpak keeps for the user (`flatpak --user update --appstream flathub`,
   then the `appstream.xml.gz` and the 64x64 icons under the user's
   `~/.local/share/flatpak/appstream/flathub/<arch>/active/`). No web API,
   no extra network service: after the first refresh, search works offline.
   The page refreshes the appstream data when it is older than a day, in
   the background. From each component: ID, name, summary, description,
   developer, categories, icon, licence, the newest release (version and
   its description, for "What's new"), and Flathub's verified flag (the
   `flathub::verification::verified` custom key). Search matches name, ID,
   summary and keywords, case-insensitively, name matches first. Categories
   are the chips: Office, Graphics, Audio & video, Internet, Development,
   Games, Education, Utilities (the freedesktop main categories).
   "Isotope picks" are the apps of `/usr/share/isotope/first-run-apps.list`.
5. **App details.** Icon, name, developer, Verified, summary, version,
   download size, licence, description, and "What it can reach": the
   permissions from the app's metadata (`flatpak remote-info
   --show-metadata` before install, the installed metadata after): home or
   host files (shown as a warning), network, display, devices, camera and
   microphone (`--device=all` or the portals do not tell them apart: say
   "All devices"), and anything else counted as "N other permissions".
   Install, Open (`flatpak run` detached, in its own session) and Remove….
6. **Install and remove.** One job at a time per window, with progress
   (flatpak's own output, parsed for the percentage), and Cancel. Remove
   asks "Remove NAME?"; the app's data (`~/.var/app/ID`) stays unless "Also
   delete its data" is checked (`--delete-data`); the dialog shows the
   data's size. After a remove the page runs `flatpak --user uninstall
   --unused --noninteractive` so runtimes nothing uses go too.
7. **Updates.**
   - The Updates tab lists the user's pending updates (apps and runtimes),
     with the version change, the size and the newest release's text, and
     Update all / Update.
   - **New permissions need an OK.** An update whose new metadata asks for
     a permission the installed version does not have (a new filesystem,
     device, socket or a D-Bus talk name) is listed under "Needs your OK"
     with what it adds, and is never installed by the automatic run. The
     user's "Review…" shows the added permissions and Update.
   - **Runtimes past end of life.** An installed app whose runtime is
     marked end-of-life gets one line saying it no longer gets security
     fixes and that only its developer can move it.
   - A running app updates in place; it starts on the new version the next
     time it is opened (flatpak's own behaviour). The row says "running,
     restarts later" when the app is running (`flatpak ps`).
8. **Automatic updates: on by default.**
   - A user unit pair, `isotope-app-updates.timer` (daily,
     `RandomizedDelaySec=1h`, `Persistent=true`) and
     `isotope-app-updates.service` (`isotope apps update --auto`), in
     `/usr/lib/systemd/user`, enabled for every user with a link in
     `/usr/lib/systemd/user/timers.target.wants` (or a user preset, if the
     image's other user units are enabled that way; follow what exists).
   - The setting is `auto=on|off` in `$XDG_CONFIG_HOME/isotope/apps`; no
     file is on. `isotope apps auto [status|on|off]` and the Updates tab's
     "Update apps automatically" Turn on/Turn off button write it.
   - The run skips, and records why: the live desktop
     (`/run/isotope/live-desktop`), the setting off, a metered connection
     and a battery below 30% while on battery (the checks automatic system
     updates use; share the code, do not copy it). Otherwise it refreshes
     the appstream data, updates every pending app and runtime except those
     that need an OK, then removes unused runtimes. The last run (time,
     result, what it updated, what it held back and why) goes to
     `$XDG_STATE_HOME/isotope/app-updates` and the tab shows it.
   - When automatic updates are off, the tray sends one notification per
     set of pending updates ("N app updates", button "Open Isotope System",
     which opens `isotope system apps`), as it does for Isotope releases.
     When they are on, it notifies only about updates that need an OK.
   - Isotope System's Updates page gets one line under its other sections,
     "N app updates", linking to the Apps page's Updates tab. Isotope's own
     releases stay on the Updates page.
9. **System extensions.** Search also matches the extension catalogs
   (the Extensions page's own data, `/etc/isotope/extensions.conf`); a match
   shows in its own "System extensions" section with "Open in Extensions".
   The Installed tab ends with one line naming the installed extensions and
   linking to the Extensions page. Extensions need an administrator and can
   restart system services, so they are managed only on their own page.
10. **The live desktop.** The Apps page is one of the pages that say "Not
   available on the live system. Install Isotope first." (`live.go`): an app
   installed there is gone at the restart.
11. **Agent and server.** Not on the server (no Flatpak, no Isotope
   System). The agent image removes Isotope System already; it must also
   remove the user timer, its service and the timer link, and check none is
   left. The `isotope apps` CLI exists on every image; with no `flatpak`
   it says Flatpak is not on this system and exits 1 (the timer is not
   there to call it).

## Non-goals

- Installing for all users (`--system`), other remotes than Flathub, or
  Flatpak bundles from a file.
- Changing permissions after install (Flatseal does that; the details view
  says so).
- Ratings, screenshots and reviews.
- Replacing COSMIC Store or Isotope Welcome's Apps step.

## Tests

- `pkg/flatpak`: the catalog parser against a small `appstream.xml.gz`
  fixture (names, categories, verified, releases, icons), search ranking,
  the metadata parser and the permission diff, the progress parser, the
  update list parser, against recorded `flatpak` outputs.
- `cmd/isotope`: `isotope apps update --auto` and `auto on|off|status`
  against a fake `flatpak` on a temporary `PATH` and the existing fakes for
  NetworkManager and UPower: each skip reason, the held-back update with new
  permissions, the state file.
- `cmd/isotope-system`: the page's model with a fake flatpak, and golden
  screenshots `testdata/apps-*.png` (browse, search, details, installed,
  remove dialog, updates with a held-back app, Flathub not set up, live),
  helpers prefixed `apps`, on CI's Ubuntu 24.04 Mesa.
- `test/apps/check-apps.sh --repo` (CI) and `IMAGE`: the units, the
  timer link, the menu entry, the agent's removal.
- AGENTS.md: a new "Apps" row in the feature table, and the Isotope System
  row and the Isotope menu row updated.

## Manual checks (need a booted desktop)

Install, open, update and remove a real Flathub app on each variant; the
automatic run on a timer; a held-back update with new permissions; COSMIC
Store and the Apps page side by side.
