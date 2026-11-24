# Mobile broadband (ModemManager extension)

Issue kairos-community/isotope#136, batch 4. Status: implemented on
`feat/modemmanager`.

## Goal

- A cellular modem (QMI, MBIM, or an AT modem with a network port) shows on
  the Network page of Isotope System with its state, and connects and
  disconnects there. The APN comes from the mobile-broadband-provider-info
  database, or from a small "APN…" dialog.
- ModemManager is an extension (`isotope ext install modemmanager`), not
  part of the image: few machines have a modem.

## Findings

- NetworkManager is built with `-Dmodem_manager=false` (Dockerfile
  `networkmanager` stage). Without it there is no WWAN device plugin.
- NetworkManager's binary exports only the internal symbols that the plugins
  of its own build use: `tools/create-exports-NetworkManager.sh` writes the
  linker version script from the `U` symbols of the plugins it built. So a
  WWAN plugin built in a separate stage against a NetworkManager built
  without modem support is not safe to load. The binary itself must be built
  with `-Dmodem_manager=true`.
- The `networkmanager` stage does not feed the COSMIC prebuild keys. The
  stages that `cosmic-shell` and `cosmic-apps` reach (GPU=full and GPU=vm)
  include `bluez`, `glib2` and `pipewire`, but not `networkmanager`. So the
  stage is changed in place; the pipewire-codecs/bluez-full pattern is not
  needed. The four keys are the same before and after (checked with
  `tools/stage-key.py`).
- Kairos merges only `/usr/bin:/usr/share:/usr/lib:/usr/include:/usr/src:/usr/sbin`
  (`SYSTEMD_SYSEXT_HIERARCHIES` in kairos-init's `99_sysext.yaml`). A file in
  `/usr/libexec` is never merged. libqmi and libmbim start `qmi-proxy` and
  `mbim-proxy` from their libexecdir, so the extension puts them under
  `/usr/lib`. The extension's `test` stage checks every path.
- `kairos-agent sysext enable --now` links the image into `/run/extensions`
  and restarts `systemd-sysext`, which reloads systemd. Nothing starts a unit
  of the extension, and the D-Bus daemon does not read the new policy file,
  so ModemManager could not own its name.

## Decisions

### 1. The image: NetworkManager's WWAN plugin and libmm-glib

The `networkmanager` stage builds with `-Dmodem_manager=true` and
`-Dmobile_broadband_provider_info_database=/usr/share/mobile-broadband-provider-info/serviceproviders.xml`,
against libmm-glib from a new `mm-glib` stage (ModemManager's source,
without QMI, MBIM, udev, polkit or systemd). The stage copies the runtime
`libmm-glib.so.0` into `/networkmanager`, so every image that copies
NetworkManager gets the library with it.

libmm-glib stays in the image. NetworkManager reads its device plugins once,
at start; a plugin that arrived with the extension would need a NetworkManager
restart (every connection drops) and would have to match the image's
NetworkManager build exactly. With the plugin always present and no
ModemManager running, NetworkManager watches the name
`org.freedesktop.ModemManager1`, logs once that ModemManager is not
available, and has no modem devices. (On a systemd system it never
activates ModemManager itself: it pokes the name only when `!sd_booted()`,
`nm-modem-manager.c` in 1.50.) The cost is about 1.5 MB.

`-Dppp=false` stays: no pppd in the image. Modems that only work over a PPP
serial link (old 3G sticks) do not connect; QMI and MBIM modems, which use a
network port, do. That is nearly every built-in laptop modem.

### 2. Everywhere, not only the desktops

The plugin and libmm-glib are in every image, the server and the agent
included. They are cheap, NetworkManager is rebuilt for every image anyway,
and a headless box with an LTE uplink is a plain use of the server edition:
`isotope ext install modemmanager` and `nmcli` work there. Splitting the
plugin out of the server image would add a stage and a check for no gain.

### 3. The extension `modemmanager`

`extensions/modemmanager/`: ModemManager, `mmcli`, its plugins, libqmi
(`qmicli`, `qmi-proxy`), libmbim (`mbimcli`, `mbim-proxy`), libqrtr-glib,
libgudev, the D-Bus policy and activation file, `ModemManager.service`, the
udev rules, and the mobile-broadband-provider-info database.

- Its own shared libraries (libmm-glib, libqmi-glib, libmbim-glib,
  libqrtr-glib, libgudev, ModemManager's plugins) are in
  `/usr/lib/isotope-ext/modemmanager`, found through a RUNPATH. None is in
  `/usr/lib`: the image has `libmm-glib.so.0` (decision 1) and the desktops
  have `libgudev-1.0.so.0`, and a second copy there is what `isotope ext`
  refuses. Shared libraries in a private directory avoid the link-order
  problems of four static GLib libraries that depend on each other.
- GLib, libsystemd and libudev come from the image. Every Isotope image has
  GLib (NetworkManager needs it). The build stage fails if an ELF file needs
  another library.
- The D-Bus policy is Isotope's: only root (NetworkManager) and the `admin`
  group may call ModemManager or receive its signals. It is built without
  polkit (the desktops run no polkit agent on sway, i3 and niri), and
  upstream's no-polkit policy lets every user read the SMS messages and the
  location. Limiting the calls is not enough: the system bus delivers
  signals to any connection that asks, and an incoming SMS comes with its
  text in `InterfacesAdded`. So the default context denies
  `receive_sender="org.freedesktop.ModemManager1"`, and root and `admin`
  allow it again.
- The unit is enabled from `/usr`: a `multi-user.target.wants` link, as
  hadron-layers' tailscale does, and the
  `dbus-org.freedesktop.ModemManager1.service` alias, so `mmcli` and
  Noctalia can D-Bus-activate it. A drop-in re-runs udev for tty, usbmisc
  and wwan devices before the daemon starts (`After=systemd-sysext.service
  systemd-udev-trigger.service`), because the boot's coldplug can run
  before `systemd-sysext` merges the rules (ModemManager ignores a port
  without `ID_MM_CANDIDATE`). Not net: every interface would get a change
  event at each start.
- At boot the wants link is the only thing that starts ModemManager:
  NetworkManager never activates it on a systemd system. `systemd-sysext`
  merges before `sysinit.target` and reloads systemd, so the link is known
  long before `multi-user.target` is reached; whether systemd adds it to the
  boot transaction it already built is not tested here (see the manual
  list). If it does not, the fix is an Isotope oneshot after
  `systemd-sysext.service` that starts the merged `.wants` units (Flatcar's
  `ensure-sysext.service`). There is no `After=` or `Wants=` against
  NetworkManager in either direction: NetworkManager follows the name's
  owner and adds the modems when ModemManager appears, and ordering one
  before the other would only delay the network by the udev settle.

### 4. Starting the units after the merge

`isotope-updated` does after a merge what `kairos-agent` leaves out. From the
image's file list (the `systemd-dissect --mtree` read of the conflict check,
before the merge) it takes the units the extension owns and enables: a link
in `multi-user.target.wants`, `sockets.target.wants` or `timers.target.wants`
under `usr/lib/systemd/system`, whose unit file (or template) the image
ships there too, and of which the system has no unit file. So an extension
never starts or stops a unit of the base: a link identical to the base's
`multi-user.target.wants/NetworkManager.service` passes the conflict check
but is not the extension's. It also notes whether the image brings D-Bus
system configuration or udev rules, and records all of it next to the
extension's source (`/var/lib/isotope-update/extensions/<file>.activation`):
once merged, the image's unit files can no longer be told from the base's.

After `kairos-agent sysext enable --now` it runs `systemctl daemon-reload`,
the D-Bus `ReloadConfig` and `udevadm control --reload`, then starts each
unit and waits for it (`systemctl start`, 2 minutes at most), so a unit that
fails is a failure. Before a disable or a remove of a merged extension it
stops exactly the recorded units, so no daemon keeps running from an
unmerged image, and after the unmerge it reloads the bus and udev again. An
extension without a record (enabled by an older Isotope, or by `kairos-agent`
by hand) has nothing stopped. The job's result names each unit started,
stopped or not started, on the lines after the first: `isotope ext` prints
them and the Extensions page of Isotope System shows them. A unit that does
not start leaves the extension enabled; it starts at the next boot through
its link. This is generic: hadron-layers' extensions with an enabled unit
(tailscale) now start at once under Isotope, which `kairos-agent` alone does
not do.

### 5. The Network page

NetworkManager's modem device (type 8) shows in DEVICES as "Mobile broadband"
with its state, the connection, the address and the operator code. A modem
row has Connect or Disconnect, and "APN…".

- Connect activates the modem's saved connection, or calls
  `AddAndActivateConnection` with no settings: NetworkManager completes a gsm
  (or cdma) connection with `gsm.auto-config`, which looks the APN up in the
  provider database by the network the modem registered on.
- "APN…" sets the APN of the modem's connection (an empty APN means
  automatic again) and creates the connection if there is none. It is only
  the APN: user names and passwords are rare and stay in nmcli.
- Without the extension there is no modem device and the page is unchanged.

What each variant shows for a modem:

| Variant | Network menu | Settings |
|---|---|---|
| sway, i3 | `isotope-wifi-menu` is Wi-Fi only: no modem | Isotope System's Network page |
| COSMIC | Isotope's quick settings and COSMIC's network applet: no modem | COSMIC Settings has no mobile broadband page (epoch 1.11.0); Isotope System's Network page |
| niri | Noctalia 5.4.1 has cellular support: the modem with its operator, signal and technology, through ModemManager (admin users; others see the gsm connection through NetworkManager) | Isotope System's Network page |

## Tests

- `extensions/modemmanager` `test` stage: no file over the base, every path
  in a merged hierarchy, no `/etc`, the binaries run, every ELF resolves with
  the image's GLib, the units and links, and a D-Bus smoke test (a system
  bus in the container: ModemManager owns its name, `mmcli -L` answers for
  root and an `admin` member, and is refused for another user; with a stub
  that owns the name and sends a signal, an `admin` member receives it and
  `nobody` does not).
- `test/network/check-wwan.sh IMAGE`: the plugin and libnm-wwan resolve, the
  NetworkManager binary exports every internal symbol the plugins need, the
  provider database path is compiled in, and none of the extension's files
  is in the image. In desktop-images.yml and release.yml, also on the server.
- Go: `cmd/isotope/ext_test.go` (only owned units, started after a merge,
  stopped before a disable or remove, a base link such as NetworkManager's
  never touched, a unit that fails reported), `cmd/isotope-system` (modem
  rows, connect, APN, the saved connection of a modem that is not ready,
  the Extensions page naming the units),
  golden screenshots `testdata/modem-*.png` (`modem_screenshot_test.go`).

## Not tested here (manual)

- A real modem: registration, the automatic APN, a data session, SIM PIN.
- Boot with the extension enabled: ModemManager started by the merged
  `multi-user.target.wants` link (decision 3).
- FCC unlock: some laptop modems (Lenovo's Fibocom and Quectel) need an
  unlock script, which ModemManager ships but does not enable. An admin links
  one into `/etc/ModemManager/fcc-unlock.d/`.
