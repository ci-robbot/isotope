# Smart cards and security keys

Part of kairos-community/isotope#136.

## Intent

A user plugs in a smart card reader, a PIV or OpenPGP card, or a USB token
(YubiKey, Nitrokey) and uses it: `gpg --card-status`, signing and decrypting
with keys on the card, `ssh` with the card's key through PKCS#11,
`pkcs11-tool`, a browser or another PKCS#11 program. FIDO2/U2F keys (WebAuthn
in a browser, `ssh-keygen -t ed25519-sk`) work too.

## Decisions

Maintainer decisions are marked (M); the rest were made while building it.

1. **An extension, `extensions/smartcards/` (M).** Few users need a reader,
   and the stack (a daemon, a socket, a polkit action, card drivers) is
   surface the image does not need to carry. Pattern: `extensions/modemmanager`.
   It carries:
   - **pcsc-lite 2.5.4**: `pcscd` (in `/usr/bin`, like the modemmanager
     extension's daemon), `pcscd.socket` enabled from `/usr` by its
     `sockets.target.wants` link, so `isotope ext` starts the socket after the
     merge and the boot's sysext merge enables it at every boot; the service
     starts on the first client and exits when idle (upstream's
     `--auto-exit`). Its `Requires=pcscd.socket` stops the service when
     `isotope ext` stops the socket before an unmerge.
   - **CCID 1.8.4**, the USB reader driver, with libusb 1.0.30 linked in
     statically (the image has a shared libusb only where CUPS or fwupd are,
     and a copy in `/usr/lib` is what `isotope ext` refuses).
   - **OpenSC 0.27.1**: its tools and the PKCS#11 module, linked as
     `/usr/lib/pkcs11/opensc-pkcs11.so` and registered with p11-kit
     (`/usr/share/p11-kit/modules/opensc.module`, absolute path); the desktops
     have p11-kit (gnome-keyring's), the server ignores the file (M). Its own
     libraries are in `/usr/lib/isotope-ext/smartcards` behind a RUNPATH, as
     the README asks. No `/etc/opensc.conf` (a sysext cannot ship `/etc`);
     OpenSC runs on its defaults without one, and an administrator may write
     one. No `pkcs11-register`: it writes host paths into browsers' NSS
     databases, and Isotope's browsers are Flatpaks.
   - **GnuPG's `scdaemon` and `gpg-card`** at the image's GnuPG version, 2.4.9.
2. **`libpcsclite.so.1` in `/usr/lib`, public.** OpenSC, scdaemon and other
   PC/SC programs `dlopen` it by name, and `libpcsclite.so.1` itself opens
   `libpcsclite_real.so.1` by bare name, which musl resolves against the main
   program's paths, not the library's RUNPATH. A private directory would
   break that. The image has no libpcsclite (OpenConnect is built
   `--without-libpcsclite`), and `check-smartcards.sh IMAGE` fails if it ever
   gets one, so the clash `isotope ext` would refuse is caught in CI.
3. **How gpg-agent finds scdaemon (M).** gpg-agent runs
   `GNUPG_LIBEXECDIR/scdaemon` unless `scdaemon-program` says otherwise. The
   image builds GnuPG with `--libexecdir=/usr/lib/gnupg`, which Kairos merges
   (not `/usr/libexec`), so the extension puts scdaemon at
   `/usr/lib/gnupg/scdaemon` and no configuration is needed.
4. **The image's gnupg stage changes (M: only in stages the COSMIC keys do
   not reach).** With `--disable-card-support` (which also turns scdaemon
   off) the agent is built without `BUILD_WITH_SCDAEMON`: its `SCD` command
   answers "Not supported", so `gpg-card` cannot work, and gpg has no
   `--card-status`, `--card-edit` or `keytocard`. So the stage now keeps card
   support, adds `--disable-ccid-driver`, builds scdaemon and removes it after
   the install. Without the extension a card command says there is no smart
   card daemon. `tools/stage-key.py` confirms the four COSMIC keys do not move
   (the gnupg stage is reached by neither `cosmic-shell` nor `cosmic-apps`).
5. **scdaemon only through pcscd.** `--disable-ccid-driver`: scdaemon's own
   driver takes a reader exclusively, away from pcscd and OpenSC, the
   classic "gpg works, then OpenSC does not" problem. On a C library other
   than glibc scdaemon's default driver is `libpcsclite.so`, a development
   link no system ships; the extension's build changes the default to
   `libpcsclite.so.1` (as on glibc) and fails if the source changed.
6. **GnuPG libraries static in the extension.** scdaemon and gpg-card link
   libgpg-error, libassuan, libgcrypt, libksba and npth statically, at the
   image's versions and checksums (held equal by `check-smartcards.sh
   --repo`): the server has no GnuPG libraries, and the desktops' shared ones
   may later differ from these.
7. **pcscd runs as root with no capabilities.** pcsc-lite 2.x runs as a
   `pcscd` user (sysusers) and CCID's udev rule gives readers to the `pcscd`
   group. On Kairos `/etc` is built again at every boot, so a user an
   extension adds would not exist when udev sets up a reader at boot, and
   `systemd-sysusers` runs only when `/etc` needs an update. Drop-ins reset
   `User=` and the socket's `SocketUser=`/`SocketGroup=`; upstream's sandbox
   stays (empty `CapabilityBoundingSet=`, `ProtectSystem=strict`,
   `NoNewPrivileges=`, the syscall filter). Root without capabilities opens
   the root-owned USB device nodes and nothing more. No sysusers file and no
   CCID udev rule are shipped.
8. **polkit decides who uses a reader (M).** pcscd asks polkit
   `org.debian.pcsc-lite.access_pcsc` and `access_card`. The extension's
   `50-isotope-pcscd.rules` answers YES for root and for a local, active
   session (any user, not only `admin`: a smart card is the user's own), NO
   for everyone else (SSH logins, inactive sessions, system users), instead
   of relying on the action's defaults. A Flatpak app of that session with
   the `pcsc` socket is allowed too: polkit cannot tell it from the session.
9. **`isotope ext` reloads polkit.** polkitd watches its directories, but the
   merge mounts a new `/usr` over the one it watches, so a merged action
   would be unknown and every client refused until a reboot. After merging
   or unmerging an extension with files under `usr/share/polkit-1/actions`
   or `rules.d`, `isotope ext` runs `systemctl try-reload-or-restart
   polkit.service` (polkit 126 is `Type=notify-reload`: SIGHUP reloads the
   actions and rules and keeps the registered agents; nothing happens when
   polkitd is not running). The `.activation` record keeps it for the unmerge.
10. **FIDO2/U2F keys: nothing to add (M: check the image).** The Hadron base
    (v0.6.7) has systemd's `fido_id` and `60-fido-id.rules` (a key's hidraw
    device gets `ID_SECURITY_TOKEN` and the `security-device` tag),
    `70-uaccess.rules` (`ENV{ID_SECURITY_TOKEN}=="?*"` and
    `ENV{ID_SMARTCARD_READER}=="?*"` tagged `uaccess`) and
    `73-seat-late.rules` (the `uaccess` builtin), systemd is built with ACL
    and logind, the sessions are logind sessions (ly, greetd with
    `pam_systemd`), and the x86 kernel has `CONFIG_HIDRAW=y`. So the active
    session's user already gets the hidraw device of a security key.
    `check-smartcards.sh IMAGE` checks those rules on every image, so a base
    that drops them goes red.
11. **No Isotope System page (M).** The catalog entry's title and
    description are the one line on the Extensions page.
12. **Versions.** pcsc-lite, CCID and OpenSC are pinned with a SHA256 and a
    Renovate comment (OpenConnect's style); the extension's version
    (`SMARTCARDS_VERSION`, its tag) is pcsc-lite's. GLib, PCRE2,
    libucontext, polkit and libusb follow the image's pins (build-only).
    polkit's client library is built `libs-only`, without its actions and
    translations (they need gettext's `msgfmt`); the image's copy runs.

## Non-goals

Smart-card login at the greeter (pam_pkcs11 or similar), a PIV management
UI, serial readers (`/etc/reader.conf.d`), scdaemon's own CCID driver, the
TPM daemon (`tpm2daemon`), the agent image (it takes no extensions).

## Tests

- The extension's `build` stage: every ELF's NEEDED libraries are the C
  library, GLib, polkit's library, libsystemd, libudev, libcrypto, zlib,
  libpcsclite or its own; the OpenSC programs and module have the RUNPATH;
  pcscd links polkit and libsystemd; scdaemon and gpg-card link no GnuPG
  library shared, scdaemon names `libpcsclite.so.1` and has no libusb; the
  CCID driver links no libusb.
- The extension's `test` stage (Hadron base): no file over the base, only
  merged hierarchies, every library found (`ldd`, with the CCID driver's
  pcscd symbols filtered as modemmanager's plugins), the units, drop-ins,
  socket link, polkit and p11-kit files; then pcscd on the container's
  socket (`--disable-polkit`, no polkitd there) reached by `opensc-tool`
  (no readers), `pkcs11-tool` through `/usr/lib/pkcs11/opensc-pkcs11.so`, and
  `scdaemon --server` (version, and SERIALNO through PC/SC with no PC/SC
  error).
- `test/polkit/check-polkit-rules.sh`: the rule's cases.
- `cmd/isotope/ext_test.go`: `TestExtPolkitFilesReloadPolkit` (polkit files
  reload polkitd after the merge and the unmerge, the socket is the unit
  started), and the real backend's commands.
- `test/smartcards/check-smartcards.sh --repo` (CI) and `IMAGE`
  (desktop-images.yml and release.yml, the server and the agent too).

## Manual checks (need a booted desktop and hardware)

1. `isotope ext install smartcards`; `systemctl status pcscd.socket` is active;
   the job's result says "Started: pcscd.socket".
2. With a reader or YubiKey: `opensc-tool --list-readers` lists it as the
   desktop user; over SSH as the same user it is refused (polkit), as root
   it works.
3. `gpg --card-status` shows the OpenPGP card; `gpg-card list`; sign with a
   card key (`gpg --clearsign`), decrypt; `gpgconf --kill scdaemon` then
   `opensc-tool --list-readers` still works (shared reader).
4. `ssh -I /usr/lib/pkcs11/opensc-pkcs11.so user@host` with a PIV key;
   `p11-kit list-modules` lists `opensc`.
5. A FIDO2 key without the extension: `ls -l /dev/hidraw*` shows an ACL for
   the user (`getfacl`), `ssh-keygen -t ed25519-sk` works, WebAuthn works in
   Flathub's Firefox.
6. `isotope ext disable smartcards`: pcscd.socket and pcscd.service stop,
   polkit forgets the actions (`pkaction | grep pcsc` is empty).
7. A Flatpak app with `--socket=pcsc` (e.g. a browser with its own OpenSC)
   sees the reader.
