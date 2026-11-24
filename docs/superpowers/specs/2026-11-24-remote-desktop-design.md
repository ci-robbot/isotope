# Remote desktop (wayvnc extension)

Issue kairos-community/isotope#136. Status: implemented on
`feat/remote-desktop`.

## Intent

- Someone at another computer sees and controls a running Isotope desktop
  with an ordinary VNC viewer (TigerVNC, Remmina, RealVNC Viewer,
  virt-viewer).
- Off by default. A user turns it on for their own session, in Isotope
  System's Network page or with `isotope remote-desktop on`. It listens only
  while it is on, the firewall allows its port only while it is on, and it
  ends with the session.
- Every connection is encrypted and needs a password.

## Findings

- wayvnc 0.10.2 needs neatvnc 1.0 and aml 1.0. It needs a screencopy
  protocol (`zwlr_screencopy_manager_v1` or `ext_image_copy_capture`) and,
  unless started with `--disable-input`, `zwlr_virtual_pointer_manager_v1`
  and `zwp_virtual_keyboard_manager_v1` (main.c: it exits otherwise).
- Sway 1.10.1 on wlroots 0.18.2 has all three.
- niri 26.04 (the Dockerfile's `NIRI_VERSION`) has them too:
  `src/protocols/screencopy.rs` (wlr-screencopy, with damage),
  `src/protocols/virtual_pointer.rs`, and smithay's virtual keyboard
  (`delegate_virtual_keyboard_manager!` in `src/handlers/mod.rs`). So niri
  ships.
- COSMIC epoch 1.11.0: cosmic-comp has ext-image-copy-capture and a virtual
  keyboard, but no `zwlr_virtual_pointer`, so wayvnc would be view-only.
  xdg-desktop-portal-cosmic 1.11.0 has a `RemoteDesktop` portal
  (`remote_desktop.rs`, input through libei), but nothing in the image is a
  server that speaks it (gnome-remote-desktop uses Mutter's own API; krfb
  is Qt and KDE Frameworks). So: not yet. When a portal-based server is
  small enough to ship, COSMIC can get it behind the same CLI and section.
- i3: the only real choice is x11vnc 0.9.17 (May 2025, after six years
  without a release). It brings libvncserver and its own TLS setup, and its
  default password is the DES "VNC password". That is a second server, with
  a second security model and a second set of tests, for the one X11
  variant. Decision: nothing on i3. The page says so and points to Sway or
  niri, which keep i3's key habits.
- neatvnc's security types: with `NVNC_AUTH_REQUIRE_AUTH` and
  `NVNC_AUTH_REQUIRE_ENCRYPTION` (wayvnc sets the second unless
  `relax_encryption`), it offers VeNCrypt (only when TLS credentials are
  set; subtype X509Plain only) and RSA-AES 256 and 128 (with Nettle), and
  never None, the DES VNC password or Apple DH. wayvnc compares the user
  name too: it must equal the configured one.
- neatvnc reads an RSA key only as PKCS #1 (`BEGIN RSA PRIVATE KEY`).
  GnuTLS reads the EC key as SEC 1 (`BEGIN EC PRIVATE KEY`). Go's
  `x509.MarshalPKCS1PrivateKey` and `x509.MarshalECPrivateKey` write those.
- `wayvnc --detached` sets up the server and listens with no compositor, so
  the extension's test stage can talk to the real server.
- The firewall has an allowed-ports API (`AllowPort`, `DenyPort`,
  `FirewallStatus` on `isotope-updated`, polkit admin only, the note on each
  rule). The admin polkit rule needs no session, so the user unit's
  `ExecStopPost` can call it as the user.
- Kairos merges only `/usr/bin`, `/usr/sbin`, `/usr/lib`, `/usr/share`,
  `/usr/include` and `/usr/src`; a user unit in an extension would also need
  a `daemon-reload` of every user manager. So the unit is in the image.

## Decisions

- **An extension** (`extensions/remote-desktop/`, the modemmanager
  pattern): wayvnc and wayvncctl, and their libraries in
  `/usr/lib/isotope-ext/remote-desktop` behind a RUNPATH. Few machines need
  remote desktop; GnuTLS, Nettle and GMP are not in the image. The image's
  libraries it uses: libc, zlib, pixman, libdrm, libxkbcommon,
  libwayland-client (on every Sway and niri image). The extension's version
  is wayvnc's.
- **The image part is small and pure Go**: `isotope remote-desktop` and the
  user unit `isotope-remote-desktop.service`. The CLI holds all of it (the
  checks, the configuration, the keys, the firewall rule); Isotope System
  runs the CLI, as the Compatibility page runs `isotope glibc`.
- **Session-scoped, not persistent.** No `[Install]`, never enabled. Remote
  login without a running session is a non-goal, and after a restart
  someone must log in at the machine anyway; a server that comes back at
  every login is a standing open port.
- **Always a password, always encryption.** `enable_auth=true`, the user's
  own login name, a password of its own (8 to 64 characters; never the
  login password, so no PAM), TLS (VeNCrypt X509Plain, a self-signed ECDSA
  P-256 certificate made once and kept, its SHA-256 shown on the page) and
  RSA-AES (an RSA 3072 key made once). There is no "password only" mode:
  plain VNC authentication is DES and leaves the session in the clear. Both
  encrypted kinds are always offered, so the user does not have to choose:
  TigerVNC, Remmina and virt-viewer use VeNCrypt, RealVNC Viewer RSA-AES.
- **The firewall through its own API.** `on` adds 5900/tcp with the note
  `Remote desktop (<user>)` only while the firewall is on and only when no
  rule allows the port already. `off`, the unit's `ExecStopPost` (any end:
  off, a crash, logout) and the tray at login (a rule left by a power cut)
  remove only a rule with that note. While the firewall is on, polkit makes
  turning it on an administrator's job; the page says so to others. With the
  firewall off, every user can turn it on, as every port is open then.
- **IPv4 only** (`address=0.0.0.0`). Binding `::` as well fails with
  `EADDRINUSE` on a dual-stack kernel, and `::` alone fails where IPv6 is
  off.
- **One port, 5900.** A second user's server on the same machine at the
  same time does not start, and says why.
- **The keyboard layout** the installer chose is given to wayvnc as
  `XKB_DEFAULT_LAYOUT`/`VARIANT` (the systemd user manager does not have
  them on Sway).
- **Never on the live desktop, the server or the agent**: the CLI refuses
  (live marker, `FLAVOR`), the unit has the live and install markers, the
  server takes no user unit, and Dockerfile.agent removes it.

## Non-goals

- RDP. Remote login without a running session (a headless or greeter
  session). A notification when a viewer connects (wayvnc's control socket
  has the events; a follow-up). Choosing the port. IPv6.

## Tests

- The extension's `test` stage: no file over the base, only the merged
  hierarchies, no unit or D-Bus file, every library found; `wayvnc
  --detached` with a configuration in the CLI's shape on keys in the CLI's
  formats: without a password it does not start; it offers VeNCrypt and
  RSA-AES and nothing unencrypted; VeNCrypt offers only X509Plain;
  `rd-probe` (a GnuTLS client, `test/rd-probe.c`) is refused with a wrong
  password and a wrong user name, and let in with the right ones.
- `pkg/remotedesktop`: the password rules, the generator, the note within
  the firewall's checks.
- `cmd/isotope/remote_desktop_test.go`: on and off with a fake systemctl
  and firewall (the files, their modes, the key formats, the certificate,
  the rule added and removed), other rules and a firewall that is off left
  alone, a refusal for someone who is not an administrator, a server that
  does not listen (stopped, port closed, the journal's reason), the
  `stopped` and tray clean-ups, every variant and the live system, `serve`'s
  arguments and keyboard, and its refusal of a configuration others can read.
- `cmd/isotope-system/remote_test.go`: the section's view for every state,
  the password dialog and Turn on, a refusal, Open Extensions, and the page
  against the real CLI. Goldens `remote-{off,on,password,extension,cosmic}.png`.
- `test/remote-desktop/check-remote-desktop.sh --repo` (CI) and `IMAGE`
  (desktop-images.yml, release.yml, also the server and the agent).

## Manual checks that need a booted desktop

1. Sway and niri: `isotope ext install remote-desktop`, then the Network
   page: Set password, Turn on. From another computer, TigerVNC
   (`vncviewer HOST:5900`): the certificate's fingerprint matches the page,
   the user name and password let in, the pointer and keyboard work (a
   German layout too), the screen follows. RealVNC Viewer (RSA-AES) and
   Remmina likewise.
2. `isotope firewall status` shows 5900/tcp "Remote desktop (USER)" while
   on, and not after Turn off, after `systemctl --user kill
   isotope-remote-desktop`, or after logging out.
3. A user who is not an administrator with the firewall on: Turn on says
   only an administrator can.
4. Power off while on; at the next login the rule is gone (tray).
5. The lock screen: a viewer sees swaylock or Noctalia's lock, and input
   goes to it.
6. On COSMIC and i3 the section says why it is not there; on the live
   desktop, that it needs an installed system.
