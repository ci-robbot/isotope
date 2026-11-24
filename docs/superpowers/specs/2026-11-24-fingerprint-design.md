# Fingerprint sign-in (fingerprint extension)

Issue kairos-community/isotope#136. Status: implemented on
`feat/fingerprint`.

## Intent

- A person with a fingerprint reader unlocks the screen with a finger and
  approves sudo and polkit (administrator) prompts with it.
- Fingers are added, listed and removed in Isotope System, as the user,
  over fprintd's D-Bus API.
- fprintd and libfprint are a system extension (`isotope ext install
  fingerprint`, `extensions/fingerprint/`), not part of the image: most
  machines have no reader.
- Nothing changes for anyone who does not install the extension. A broken
  PAM stack locks people out of their machine, so this is the first
  requirement, and it is checked on every image.

## Findings

- PAM files in the image. Hadron v0.6.7 ships `/etc/pam.d/{login,
  system-login, system-auth, sudo, su, su-l, sshd, passwd, chfn, chsh,
  chpasswd, newusers, runuser, runuser-l, systemd-run0, systemd-user,
  other}`; `login` includes `system-login`, which includes `system-auth`
  (faillock, pam_unix). Isotope adds `swaylock` (sway), `i3lock` (i3),
  `cosmic-greeter` and `greetd` (COSMIC); ly installs `ly`; polkit 126 puts
  `polkit-1` in `/usr/lib/pam.d` (its `pam_prefix` default). libpam 1.7.3
  reads `/etc/pam.d`, then `/usr/lib/pam.d` (checked in the base's
  `libpam.so.0`).
- Who reads which service. swaylock and i3lock run `swaylock` and
  `i3lock`. Noctalia (niri) authenticates against `login`, and drives
  fprintd itself over D-Bus when its `lockscreen.fingerprint` setting is on,
  which is its default (5.4.1, `config_types.h`); Isotope's
  `noctalia-defaults.toml` does not change it. cosmic-greeter's lock screen
  uses the service name `cosmic-greeter`, fixed in `src/locker.rs` (epoch
  1.11.0); its login screen authenticates through greetd, whose user
  session service was also `cosmic-greeter`. polkit's setuid helper uses
  `polkit-1`.
- How Kairos merges an extension: only `/usr/bin`, `/usr/sbin`, `/usr/lib`,
  `/usr/share`, `/usr/include` and `/usr/src` (`SYSTEMD_SYSEXT_HIERARCHIES`).
  `/usr/libexec` and `/etc` are never merged. `/etc` and `/var` are rebuilt
  from the image at every boot, except the persistent paths.
- Linux-PAM skips a line whose type starts with `-` when the module cannot
  be loaded (`PAM_HT_SILENT_MODULE`); the line still counts for `[...=N]`
  jumps and answers `PAM_IGNORE`. A line without `-` whose module is
  missing fails the whole stack.
- pam_fprintd answers `PAM_AUTHINFO_UNAVAIL` at once when there is no
  reader, no enrolled finger, a remote session, or a reader that another
  process holds. It needs only libpam, libsystemd and libc.
- fprintd 1.94.5 reads `/etc/fprintd.conf` and falls back to its file
  storage when the file is missing; prints are in `/var/lib/fprint`
  (`StateDirectory=fprint`). Its polkit actions: `verify` is yes for an
  active local session, `enroll` (add and remove) is `auth_self_keep`.
  fprintd always asks polkit with user interaction allowed.
- swaylock and i3lock read the whole password, then call PAM. Both are run
  with `ignore-empty-password`. cosmic-greeter's lock screen shows each PAM
  message and prompt as it comes.

## Decisions

### 1. The extension: fprintd and libfprint, private libraries

`extensions/fingerprint/` builds libfprint 1.94.100 (default drivers: every
USB and SPI reader, no virtual test drivers) and fprintd 1.94.5 on the
image's Hadron pin, in the modemmanager layout:

- The libraries libfprint needs (libusb, json-glib, libgusb, pixman,
  libgudev, libpolkit-gobject-1) and the fprintd daemon live in
  `/usr/lib/isotope-ext/fingerprint`, found through a RUNPATH: some are in
  the desktops' `/usr/lib`, and a second copy there is what `isotope ext`
  refuses. GLib, libsystemd, libudev, libpam and libcrypto are the image's;
  the build fails on any other dependency.
- fprintd is D-Bus activated (`net.reactivated.Fprint.service` names
  `fprintd.service`), so it runs only while something asks; no `.wants`
  link. The tools `fprintd-enroll`, `-list`, `-verify`, `-delete` go to
  `/usr/bin`.
- No `/etc`: fprintd takes its built-in default (file storage), which is
  what `fprintd.conf` says anyway.
- `pam_fprintd.so` goes to `/usr/lib/security`, and next to it
  `pam_isotope_unix.so`, a relative link to the image's `pam_unix.so` (see 3).
- The extension's version is fprintd's (`FINGERPRINT_VERSION`).
- Build details found by running the build stage once: gettext must be
  configured from the top directory (`msgfmt` for fprintd's polkit policy),
  and libfprint 1.94.100's `tests/meson.build` iterates a dict with one
  variable when introspection is off, which meson refuses; the Dockerfile
  names the second variable with `sed` (a no-op once upstream fixes it).
- The test stage checks the files against the base and the merged
  hierarchies, every library with `ldd`, the alias, the tools, the unit and
  D-Bus files, the polkit actions, and starts fprintd on a system bus in the
  container (it owns its name, reports no reader, `fprintd-list` answers).

### 2. The prints survive a reboot

`/var/lib/fprint` is added to `94_isotope_state.yaml`'s bind mounts (Kairos
rebuilds `/var` at every boot). This is in the image, so prints enrolled
before an extension is removed and installed again come back.

### 3. PAM: guarded lines in the image, modules in the extension

The image carries every fingerprint line; the extension carries only the
modules. Each line starts with `-`, so while the extension is not merged
(the module is missing) PAM skips it and the stack is exactly the one
before. The services and the order:

| Service | Fingerprint | Order |
|---|---|---|
| `swaylock`, `i3lock` | yes | the typed password first (`-auth [success=done default=ignore] pam_isotope_unix.so nullok`), then a finger (`-auth sufficient pam_fprintd.so timeout=10`), then `auth include login` |
| `cosmic-greeter` (COSMIC's lock screen) | yes | a finger first, then `auth include login` |
| `sudo`, `polkit-1` | yes | a finger first (the line is put first by the Dockerfile `desktop-default` stage, into Hadron's and polkit's own files, so they follow upstream) |
| Noctalia's lock (niri) | yes | Noctalia's own fprintd client, `login` untouched |
| `login`, `system-login`, `system-auth`, `ly`, `cosmic-greeter-login`, `sshd`, `su`, every other | no | unchanged |

Why each choice:

- The login screens keep the password: pam_gnome_keyring unlocks the login
  keyring with it (decided before this work). A finger never logs in.
- COSMIC's lock screen and login screen shared the PAM service
  `cosmic-greeter`. The lock screen's name is fixed in the binary, and
  patching cosmic-greeter would change the COSMIC prebuilt keys, so greetd's
  user session moves to a new service, `cosmic-greeter-login` (the old
  file's content; `/etc/greetd/cosmic-greeter.toml` names it), and
  `cosmic-greeter` becomes the lock screen's alone.
- swaylock and i3lock read the password before PAM runs. With the finger
  first, every typed password would wait for the reader's timeout. So their
  services check the typed password first, through `pam_isotope_unix.so`;
  it is a link that only the extension ships, so the line is skipped like
  the others without it, and the stack is `auth include login` alone. With
  the extension, a wrong password (or a single key and Enter: both run with
  `ignore-empty-password`) then asks for the finger. The lock screens run as
  the user, where pam_faillock cannot open its tally and does nothing, so
  checking pam_unix before `login` weakens no lockout.
- cosmic-greeter's lock screen, sudo and polkit show PAM's messages, so the
  finger comes first ("Place your finger on the reader") and the password
  prompt follows after 10 s or three tries; at once with no reader or no
  finger enrolled. pam_fprintd refuses remote sessions itself, so sudo over
  SSH asks the password.
- The server is not changed (`server-default` has no fingerprint line), and
  the agent removes every line (Dockerfile.agent): an appliance driven by an
  agent has no person at a reader.
- `-auth sufficient` before faillock means a finger bypasses a password
  lockout, as on Fedora and Ubuntu.

### 4. Isotope System: the Users page

The FINGERPRINTS section is on the Users page, under the list, for the
user's own fingers (`cmd/isotope-system/fprint_section.go`, over
`fprint.go`). The Users page is where a user changes their own password,
and a fingerprint is another way to prove it is them; the Security page is
the disk's encryption keys, which are machine-wide and admin-only. Any user
manages their own fingers there (fprintd lets a user enroll only for
themselves; other users need `setusername`, which is admin's).

- Without fprintd (the extension is not merged, or no system bus): "Fingerprint
  sign-in is not installed" and **Install the fingerprint extension**, which
  opens the Extensions page (only an administrator can install; the line
  says so to others).
- With fprintd and no reader: "No fingerprint reader".
- With a reader: its name, the enrolled fingers with **Remove…**, and **Add
  a fingerprint…**: a dialog to pick the finger (left and right hand, the
  enrolled ones off), then a dialog that follows the enrollment (fprintd's
  `EnrollStatus` signals, sender checked: "Touch the reader with your right
  index finger", "2 of 5 scans", the retry messages) with Cancel.
- polkitd watches the `/usr` that a merge mounts over, so it does not see
  fprintd's actions by itself; `isotope ext` reloads it after a merge that
  brings polkit files (added by the smartcards extension, #170). If polkit
  still has no `net.reactivated.fprint.device.enroll` (its
  `EnumerateActions`), the section says "Restart the computer to finish
  setting up fingerprint sign-in"; until then pam_fprintd's calls fail and
  the password works.

### 5. The password for enroll and remove: the page's own polkit agent

fprintd's `enroll` action is `auth_self_keep`: polkit asks the user's own
password, through an authentication agent. sway, i3 and niri run none, so
adding a finger would fail there. Isotope keeps fprintd's rule (a polkit
YES would let anyone at an unlocked session add their own finger, and then
use it for sudo) and gives the page an agent: while it adds or removes a
finger, Isotope System registers a polkit agent for its own process
(`unix-process` scope, `polkit_agent.go`). polkit prefers a process agent to
the session's, so COSMIC behaves the same. The agent answers only for the
user's own identity, shows "Confirm it is you" with a password field (and
any message PAM sends, such as pam_fprintd's), and hands the password to
polkit's setuid helper (`polkit-agent-helper-1`, its stdin protocol); polkit
and PAM check it, the page never does. Three tries, then polkit refuses.

## Non-goals

Face unlock; enrolling at first boot (Isotope Welcome); fingerprint at the
login screens; managing other users' fingers.

## Tests

- `extensions/fingerprint/Dockerfile` `test` stage (CI, extensions.yml).
- `test/fingerprint/check-fingerprint.sh --repo` (CI): the extension's
  files and pins, every fingerprint PAM line in `rootfs/` starts with `-`,
  the order of each locker's lines, the COSMIC login service, the sudo and
  polkit-1 step in `desktop-default` and none in `server-default`, the
  persisted path, the agent's removals, the page, the workflow wiring.
- `test/fingerprint/check-fingerprint.sh IMAGE` (desktop-images.yml and
  release.yml, also the server and the agent): no file of the extension in
  the image, then PAM for real (`pam-matrix.sh`, run as root in the image
  with `pam-check` and a stand-in `pam_fprintd.so` built by
  `Dockerfile.pam`): every PAM service names only modules and services that
  exist (not counting `-` lines); the right password opens, and a wrong one
  does not, every service a person types into (login, system-login,
  system-auth, sshd, sudo, polkit-1, ly, cosmic-greeter-login and the lock
  screens, the lock screens as the user) without the extension, and again
  with the extension and no reader; with a finger that matches, sudo,
  polkit-1 and the lock screens open and the others neither open nor ask
  the module; swaylock and i3lock check a typed password before the finger.
  On the server and the agent no service takes a finger.
  The same harness was run on Hadron v0.6.7 with the tree's PAM files
  before the first push, and a line without `-` made it fail.
- Go (CI, `cmd/isotope-system/`): `fprint_test.go` (fprintd's client
  against a fake fprintd on a private `dbus-daemon`: state, no reader, the
  enrollment's statuses with a forged signal from another connection
  ignored, cancel, delete; the polkit helper protocol against a stand-in
  helper; the agent refuses other identities and retries three times; the
  section's flows), and the goldens `testdata/fingerprint-*.png`
  (`fprint_screenshot_test.go`; `go test -tags nox11,novulkan -run
  TestFingerprintScreenshots ./cmd/isotope-system/ -update` rewrites them).
- `test/state/test-persist-state.sh` expects `/var/lib/fprint`.

## Manual checks (need a booted desktop and a reader)

1. Install the extension on each variant; the Users page shows the reader
   (or asks for a restart first), add a finger (the password dialog comes
   first), remove it.
2. Lock the screen: sway and i3 (type a key and Enter, touch the reader;
   the right password still unlocks at once), COSMIC (touch first, password
   after 10 s), niri (Noctalia's own prompt).
3. `sudo true` in a terminal: the finger, or the password after 10 s; over
   SSH, the password only.
4. A COSMIC Settings change that asks for the password (cosmic-osd): the
   finger works.
5. The login screens (ly, cosmic-greeter) ask only the password, and the
   keyring is unlocked after login.
6. Remove the extension: lock, sudo and polkit ask the password at once.
7. Reboot: the enrolled fingers are still there.
