# Firmware updates (kairos-community/isotope#136, batch 3)

## Intent

Isotope updates the firmware of the machine and its devices with fwupd and
the LVFS (Linux Vendor Firmware Service), as the big distributions do:

- fwupd and its CLI `fwupdmgr` on the four desktops and the server;
- a FIRMWARE section on Isotope System's Updates page: the devices fwupd can
  update, the update each one has, and an Update button with progress;
- the tray says when a device has a firmware update;
- the LVFS metadata is refreshed once a day on an installed system.

The CLI is fwupd's own `fwupdmgr`. Isotope does not wrap it.

## Research

### Where the ESP is, and whether fwupd can write it

fwupd's `uefi-capsule` plugin updates the system firmware (and anything the
firmware lists in its ESRT table) with a UEFI capsule. It copies the capsule
and its EFI helper `fwupdx64.efi` to the EFI system partition (ESP), points
`BootNext` at the helper and asks for a restart. At the restart the helper
hands the capsule to the firmware. If the firmware supports Capsule-on-Disk
(rare), fwupd writes the capsule to `\EFI\UpdateCapsule` and sets
`OsIndications` instead, and no EFI helper runs.

fwupd 2.1 finds the ESP only through udisks2
(`fu_context_get_esp_volumes` -> `fu_volume_new_by_kind`, which lists
udisks2 block objects with the ESP partition type). It mounts the ESP
through udisks2 for the write when nothing has it mounted
(`fu_volume_locker_new`), and unmounts it afterwards. `EspLocation` in
`fwupd.conf` only picks one of the volumes udisks2 lists; it does not replace
udisks2. Without udisks2, fwupd has no ESP and the plugin cannot update.

What Kairos does with the ESP (kairos-io/kairos master 9624a979, the
immucore, agent and sdk directories):

| | Standard edition (GRUB) | Trusted Boot edition (systemd-boot, UKI) |
|---|---|---|
| ESP | `COS_GRUB`, vfat, 64 MiB (`sdk/constants: EfiSize`) | `COS_GRUB`, vfat, 4 GiB (Isotope's installer) or 15 GiB (agent default) |
| Mounted at run time | no. Only `/run/cos/efi`, during an install or an upgrade (`EfiDirTransient`) | yes, at `/efi`, **read-only** (`immucore/pkg/state/steps_uki.go` `UKIMountESPPartition`, options `ro`). Kairos' own writers remount it read-write around one call (`kairos-init` `08_efi_assessment.yaml`, `09_systemd_services.yaml`) |
| Secure Boot | off. Hadron builds shim from source and nobody signs it, so the firmware refuses it while Secure Boot is on | on, always: user mode with Isotope's PK, KEK and db (Microsoft's certificates kept in KEK and db) |
| TPM unlock | not used for the boot chain | the disk key is sealed to a signed policy on PCR 11 only (`sdk/kcrypt/lock.go`: `publicKeyPcrs` default 11, no plain PCRs), so a firmware update does not stop the disk from unlocking |

What fwupd does under Secure Boot (`plugins/uefi-capsule/fu-uefi-common.c`
`fu_uefi_get_built_app_path`): with Secure Boot on it uses only
`fwupdx64.efi.signed`, and stops with "`.../fwupdx64.efi.signed` cannot be
found" when there is none. With shim on the ESP it chain-loads the helper
through shim, which trusts the distribution's key; `DisableShimForSecureBoot`
loads it directly, which works only when the helper is signed with a key in
db.

### The finding, per edition

- **Standard edition: capsules work.** Secure Boot is necessarily off, so the
  unsigned `fwupdx64.efi` starts. The ESP is not mounted at run time, so
  fwupd mounts it read-write through udisks2 (as root, which udisks2 always
  allows), writes `\EFI\isotope\fwupdx64.efi` and the capsule, sets
  `BootNext`, and unmounts. After the restart the firmware applies the
  capsule and boots `BootOrder`, whose first entry is Isotope's own
  (`11_isotope_efi_boot_entry.yaml`). fwupd then reads the result from the
  ESRT and its history (`/var/lib/fwupd/pending.db`, kept across reboots,
  see Persistent state) and removes the files from the ESP
  (`RebootCleanup=true`, the default). A capsule is usually 8 to 32 MiB;
  the 64 MiB ESP holds shim, GRUB and its config in a few MiB. fwupd checks
  the free space before it writes, so a capsule that does not fit fails
  before the restart, not at it. udisks2 marks the Kairos partitions
  `UDISKS_IGNORE` and `UDISKS_SYSTEM` (`90-isotope-udisks-ignore.rules`); fwupd
  reads neither hint as "skip" (it reads `HintSystem` only to call the ESP
  internal), so the rule stays as it is.
- **Trusted Boot edition: capsules are off; every other device updates.**
  `fwupdx64.efi` is not signed with Isotope's db key, and Secure Boot cannot
  be off there, so the firmware would refuse it: fwupd stops before it
  writes anything ("cannot be found"), and the page would offer an update
  that always fails. Capsule-on-Disk needs no helper, but the ESP is mounted
  read-only at `/efi` and holds the signed UKIs; fwupd would get the mount
  that is already there and fail with a read-only file system. So the
  Trusted Boot images set `DisabledPlugins=uefi_capsule` (the Dockerfile
  `kairos` stage, where `TRUSTED_BOOT` is known, also on a `UKI_FROM`
  build). The other plugins (NVMe and SATA disks, Thunderbolt, USB docks,
  hubs, keyboards, mice, cameras, Logitech receivers, the TPM, and so on)
  write to their device and never touch the ESP, so they work.
- **The server: the same as Trusted Boot, for another reason.** It has no
  udisks2 (no removable disks to mount), so fwupd finds no ESP. The server
  image also sets `DisabledPlugins=uefi_capsule`, so `fwupdmgr get-devices`
  shows no update that cannot install. NVMe and SATA disks, USB and
  Thunderbolt devices, and a BMC through fwupd's `redfish` plugin update
  over SSH with `fwupdmgr`.

### Why "capsules off there" and not "the whole feature waits"

Of the three options (capsules off, only non-UEFI devices, the whole
feature waits), Trusted Boot and the server take "only non-UEFI devices":
the plugin that needs the ESP is off, and everything else works. Waiting
would also hold back the SSD, dock and peripheral firmware, which is most of
what the LVFS ships for a desktop, for a problem that only the system
firmware has. "Capsules off" with nothing else would be the same as this,
said less exactly.

The way to turn capsules on later, written here so it is not lost:

- Trusted Boot: sign `fwupdx64.efi` with the db key where the UKIs are
  signed (`auroraboot/build-uki.sh`, which has the keys), install it as
  `fwupdx64.efi.signed`, set `DisableShimForSecureBoot=true`, and remount
  `/efi` read-write around fwupd's write (Kairos' `ExecStartPre=mount -o
  remount,rw /efi` pattern). PCR 11 does not change with the firmware, so the
  disk still unlocks. A firmware update that resets the Secure Boot keys to
  the factory set would stop Isotope's UKIs from booting until the keys are
  enrolled again; that is the risk to test on real hardware first.
- Server: udisks2 on the server, D-Bus activated only.

## Decisions

1. **What is built.** Runtime files only, in new stages; none of them is
   reached by `cosmic-build`, so the COSMIC prebuilt keys do not move (see
   COSMIC keys):
   - `libusb` (1.0.30): fwupd needs `libusb_init_context` (>= 1.0.27).
   - `gnu-efi` (4.0.4), build time only, for `fwupd-efi`.
   - `fwupd-efi` (1.9): `fwupdx64.efi` in `/usr/lib/fwupd/efi`, with
     Isotope's SBAT entry. Both fwupd stages use `--libexecdir=lib`, as
     flatpak and polkit do: `/usr/libexec` is a persistent path on Kairos.
   - `fwupd` (2.1.10): the daemon, `fwupdmgr`, `fwupdtool`, the units, the
     D-Bus policy, the polkit actions and rule, the LVFS remote. It builds
     against the stages that exist: `glib2`, `pcre2`, `libxmlb`, `polkit`,
     `sqlite-gnupg` (libsqlite3), `gettext-tools` (msgfmt for the policy),
     `hwdata` (`usb.ids`), and the toolchain's libcurl, OpenSSL, liblzma,
     zlib, libsystemd and libblkid (also in the Hadron base). fwupd 2.1 has
     libjcat built in and checks the LVFS signatures with OpenSSL (it takes
     GnuTLS first when it has it; here it has not), so there is no GnuTLS,
     libjcat, json-glib, libarchive, protobuf-c, efivar or libgusb.
     Off: docs, introspection, man pages, tests, the capsule splash (it needs
     Python's PIL at build time), ModemManager, passim (peer-to-peer
     metadata), libdrm, libmnl, bash and fish completions, the firmware
     packager.
   - `fwupd-runtime`: `/fwupd` without headers, pkg-config, static
     libraries and installed tests, plus `libusb-1.0.so.*`,
     `libsqlite3.so.*` and `libxmlb.so.*` (the same files as the `libxmlb`
     stage that the desktops copy already; the server has none). The stage
     fails when a library fwupd needs is missing (`ldd`).
2. **Where.** `desktop-default` and `server-default` copy `fwupd-runtime`
   and enable `fwupd-refresh.timer`. Not the agent: Dockerfile.agent removes
   every fwupd file and the timer link, and checks that none is left.
3. **Persistent state.** fwupd keeps the LVFS metadata, the history and the
   pending updates in `/var/lib/fwupd`, and a cache it can rebuild in
   `/var/cache/fwupd`. Kairos rebuilds `/var` at every boot.
   `/var/lib/fwupd` joins the paths of `94_isotope_state.yaml`
   (`CUSTOM_BIND_MOUNTS`, so `/usr/local/.state/var-lib-fwupd.bind`, as the
   saved networks and Bluetooth pairings), on the desktops and the server.
   Without it a capsule's result would be lost at the restart that applies
   it, and fwupd would offer the same update again. The cache is not kept.
4. **Metadata refresh.** fwupd's own `fwupd-refresh.timer` and
   `fwupd-refresh.service` (`fwupdmgr refresh` as the `fwupd-refresh` user,
   added to the image's `/etc/passwd` in the `kairos` stage as `tss` is),
   enabled in the image. Isotope's drop-ins make the timer daily
   (`OnCalendar=daily`, `RandomizedDelaySec=1h`, `Persistent=true`; fwupd's
   own is hourly) and give the timer and the service the firewall's
   `ConditionPathExists=!` lines for `/run/cos/live_mode`,
   `/run/cos/install_mode`, `/run/cos/uki_install_mode` and
   `/run/isotope/live-desktop`: never on the live ISO, the installer or the
   Trusted Boot install medium.
   The service also has `ExecCondition=/usr/bin/isotope unmetered`: the
   refresh skips a metered connection with the same NetworkManager check as
   automatic updates (`Metered` yes or guess-yes skips, no NetworkManager
   runs, an unreadable answer skips; exit 1 is a skipped condition to
   systemd, not a failure).

5. **Polkit.** fwupd's own actions let any active local user install a
   signed LVFS update to an internal or a plugged-in device without a
   password (`update-internal-trusted`, `update-hotplug-trusted`:
   `allow_active` yes). Isotope's rule is that only `admin` changes the
   machine. `50-isotope-fwupd.rules` (sorted before fwupd's own
   `org.freedesktop.fwupd.rules`) answers every `org.freedesktop.fwupd.*`
   action:
   - YES for the `fwupd-refresh` user on `get-remotes` and
     `refresh-remote` (the timer), and nothing else for it;
   - NO for anyone not in `admin`;
   - YES for a member of `admin`, in any session (so `fwupdmgr update`
     works over SSH), on a plain update: `update-internal-trusted`,
     `update-hotplug-trusted`, `device-activate`, `get-remotes`,
     `refresh-remote`, `verify`, `verify-update`, `clear-results`,
     `get-bios-settings`, `quit`;
   - AUTH_ADMIN_KEEP (the administrator's password) for everything else:
     downgrades, unsigned firmware (`update-internal`, `update-hotplug`),
     `set-bios-settings`, `modify-config`, `reset-config`, the remotes
     (`enable-remote`, `modify-remote`, `clean-remote`), emulation,
     `self-sign`, `set-approved-firmware`, the host security fixes,
     `device-unlock`, `modify-device`, and any action a newer fwupd adds.

   **Where the password prompt shows.** `fwupdmgr` starts `pkttyagent` on
   its terminal, so `fwupdmgr downgrade` asks for the password in a
   terminal on every desktop and over SSH. COSMIC has a graphical agent
   (cosmic-osd). sway, i3 and niri run none, so a graphical program there
   that asks for one of those actions gets "not authorized". That is the
   chosen behaviour: Isotope System's FIRMWARE section only installs signed
   upgrades, which need no password, and when fwupd refuses anyway it says
   why ("needs the administrator password … run fwupdmgr in a terminal (for
   example: fwupdmgr downgrade)"). This is the opposite of the restore rule
   (YES with no password, because Isotope System must do the restore
   itself); here the rare actions have a terminal path.

   **Flatpak callers.** isotope-updated refuses a caller whose
   `/proc/PID/root` has `.flatpak-info`, before polkit is asked. fwupd is
   not Isotope's daemon, and a polkit rule cannot do that check: polkit
   126 gives a rule the subject's user, groups, session, seat, `local`,
   `active` and its *system* unit (`system_unit`, `no_new_privileges`),
   and a Flatpak app and the rest of the session share one system unit,
   `user@UID.service`. So the rule cannot refuse a Flatpak caller. What it
   limits instead: an admin's Flatpak app that may talk to
   `org.freedesktop.fwupd` can only install the signed upgrades the LVFS
   offers (fwupd's `OnlyTrusted` is on and cannot be changed over D-Bus);
   a downgrade, a BIOS setting, a config or remote change asks for the
   password, which the user sees.

   Reading the devices and their updates (`GetDevices`, `GetUpgrades`)
   needs no polkit, so the tray works for every user.
   `test/polkit/check-polkit-rules.sh` runs 43 fwupd cases.
6. **Isotope System.** The Updates page gets a FIRMWARE section, after
   automatic updates, through `pkg/fwupd` (pure Go, also used by the tray):
   fwupd's D-Bus API on the system bus, as the user, no daemon of Isotope's
   in between.
   - Each updatable device: its name, vendor and version. A device with an
     update says "Version X is available" with the release's summary and
     an "Update" button (admin only; a notice says so to anyone else).
   - Update: the page downloads the release from its LVFS location (HTTPS
     only, at most 512 MiB, the SHA-256 or SHA-1 of the release's checksum
     list checked) into an unlinked temporary file, hands its descriptor to
     fwupd's `Install`, and shows fwupd's `Status` and `Percentage`. fwupd
     checks the cabinet's signature against the metadata itself.
   - A device that needs a restart (`NeedsReboot`: the capsule is staged)
     says "Restart to finish the firmware update" with "Restart…", which asks
     first and then goes through the page's existing restart
     (`isotope-updated`'s `Reboot`), so the A/B logic and its checks stay in
     one place.
   - fwupd not on the bus (the agent, a broken install): "Firmware updates are
     not available on this system." Not on the live system: the Updates
     page already says to install first.
7. **The tray.** `watchFirmware` in `cmd/isotope`: five minutes after the
   session starts and every six hours, the devices with an upgrade. One
   notification per device and version, with an "Open Isotope System" button
   (`isotope system updates`); the record is
   `$XDG_STATE_HOME/isotope-update/firmware-notified` (one `device version`
   line each). Not with `--no-icon`, not on the live desktop (the tray does
   not run there) and only when Isotope System is installed. A failure is
   logged, never a notification.
8. **CLI.** `fwupdmgr`, unchanged.
9. **No restart during a firmware write.** fwupd's logind plugin holds a
   `block` inhibitor on `shutdown` while it writes (who `fwupd`, why
   "Firmware Update in Progress"). isotope-updated restarts as root with
   `systemctl reboot`, which skips inhibitors (root has
   `reboot-ignore-inhibit`). So `Reboot` and `RebootPrevious` first call
   logind's `ListInhibitors` and refuse while a `block` inhibitor covers
   `shutdown` (`Refused`, the sentence: "A firmware update is being
   written. Restarting now could damage the device. Try again when it is
   done."; another program's block names it). isotope-updated's own
   inhibitor is skipped: its jobs are refused by state. An unreadable list
   does not block. logind, not fwupd's `Status` property: reading `Status`
   would D-Bus-activate fwupd (which enumerates the hardware first) on every
   restart, a stopped fwupd has nothing to say, and logind also covers a
   write by `fwupdtool`, which is not the daemon. Isotope System turns off
   "Restart now", "Restart into the previous system" and the firmware
   "Restart…" while it has fwupd write, and a restart asked anyway (from a
   dialog) says the same sentence without calling the daemon; the tray's
   Restart and the automatic-update notification reach the daemon, which
   refuses, and the tray shows the sentence.
10. **Secure Boot after a capsule.** Many vendor capsules reset the
   firmware settings to their defaults, which turn Secure Boot on; the
   standard edition's shim is not signed, so the computer then does not
   start Isotope until Secure Boot is off again. The confirmation of a
   `uefi_capsule` device's update says: "Some firmware updates turn Secure
   Boot back on. If the computer then does not start Isotope, turn Secure
   Boot off in the firmware setup."
11. **Licences.** The `libusb`, `fwupd-efi` and `fwupd` stages install
   their `COPYING` (LGPL-2.1+) in `/usr/share/licenses/<name>/`, and
   `fwupd-runtime` keeps the three.

## COSMIC keys

`tools/stage-key.py` for `cosmic-shell`, `cosmic-apps` and `cosmic-build`,
both GPU lines, before and after this change, are the same:

| | full | vm |
|---|---|---|
| cosmic-shell | `d950af973e2b` | `bb9c229f03c9` |
| cosmic-apps | `41b1e8d62366` | `2a98130b314a` |
| cosmic-build | `5737e0379948` | `d64f3c61e941` |
 The new stages copy from `glib2`, `pcre2`,
`libxmlb`, `polkit`, `gettext-tools`, `hwdata` and `sqlite-gnupg` but change
none of them.

## Build check

The `fwupd-efi` and `fwupd-runtime` stages were built on this branch (not a
whole image): fwupd 2.1.10 configures and links on the Hadron musl toolchain,
`fwupdtool --version` and `get-plugins` run (with `uefi_capsule`, `nvme`,
`thunderbolt`, `redfish`), the LVFS remote asks for `firmware.xml.zst`
(libxmlb has zstd), `fwupdx64.efi` is a PE32+ with the `fwupd-efi.isotope`
SBAT line, and `fwupd-runtime` is 11 MiB.

## Tests

- `pkg/fwupd`: a fake fwupd on a private `dbus-daemon` (devices, upgrades,
  `Install` with the descriptor, the progress properties, fwupd's
  `NothingToDo` error), and the download against an `httptest` TLS server
  (checksum, size cap, HTTPS only).
- `cmd/isotope-system`: the section's states with a fake firmware hook, the
  update flow, the restart question, a non-admin's click; the golden
  screenshots `testdata/firmware-*.png` (`firmware_screenshot_test.go`, its
  helpers prefixed `firmware`).
- `cmd/isotope`: the tray's notifications, once per device and version.
- `test/polkit/check-polkit-rules.sh`: the fwupd rule's cases.
- `test/firmware/check-firmware.sh --repo` (CI) and `IMAGE`: the files,
  the timer, the persistence, the polkit rule, `DisabledPlugins` on the
  server and Trusted Boot, the agent's removals, `ldd` of the daemon.

## Manual (needs real hardware)

- A BIOS capsule that turns Secure Boot back on: the machine does not
  start Isotope until Secure Boot is turned off in the firmware setup;
  check the confirmation's warning matches what happens.
- A restart asked while a dock or SSD update is being written: refused
  with the firmware sentence.

- A capsule update on a standard install with an LVFS-listed system
  firmware: the restart, the firmware's own update screen, the result in
  the Updates page and `fwupdmgr get-history` after the reboot.
- A USB or NVMe device update on a Trusted Boot install.
- `fwupdmgr get-devices` and `fwupdmgr update` over SSH on the server.
