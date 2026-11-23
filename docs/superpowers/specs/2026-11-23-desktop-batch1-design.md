# Desktop batch 1: VPN, Bluetooth, Intel video, firewall

Tracking issue: kairos-community/isotope#136 (batch 1). Hadron dependency:
kairos-io/kairos#6193.

## Why

Common desktop tasks have no graphical path today:
- importing a WireGuard config;
- joining a company VPN;
- getting good sound from Bluetooth headphones;
- sending a file to a phone;
- turning a firewall on.

Each of these needs a terminal now, or does not work at all. Batch 1 is the
set that is cheap because the base is already there. The kernel has every
driver, NetworkManager and BlueZ are built, and Isotope System has the
pattern (a page, D-Bus to the service, polkit where root is needed).

## Goals

- Every item works on all four desktops (sway, i3, niri, COSMIC) and is
  controlled from Isotope System, plus the existing menus where they already
  cover the job.
- Root-owned changes go through `isotope-updated` and polkit, admin only.
  Changes the session may already make (NetworkManager has
  `auth-polkit=false` on the desktops; BlueZ allows the active session) go
  straight from the app, as the Network page does today (`system/nm.go`).
- Each item has image checks and an AGENTS.md row.

## Non-goals

- OpenVPN, vpnc, IPsec. These come later, as extensions.
- Web-based (SAML) sign-in for OpenConnect. v1 is user name and password,
  with or without a second factor typed in. See "Open questions".
- A Bluetooth equaliser or per-app audio routing.
- firewalld or zones. The firewall is one ruleset: on or off, plus allowed
  ports.

## Approaches considered

**Talking to the services.** The candidates were:
- (a) The app calls each service's D-Bus API directly, as `system/nm.go`
  does for NetworkManager.
- (b) Everything goes through `isotope-updated`.
- (c) Shelling out to `nmcli`, `bluetoothctl` and `wpctl`.

Pick: (a) for NetworkManager, BlueZ, OBEX and PipeWire, which already allow
the session user. (b) only for the firewall, which needs root and a file in
`/oem`. (c) is rejected: parsing CLI output breaks across versions, and the
menus that already do it are the reason we want a page.

**The Bluetooth page versus the shells' own panels.** COSMIC and Noctalia
have Bluetooth panels.
- (a) Isotope System's Bluetooth page on every desktop. The existing menus
  and the quick settings open it for details.
- (b) Link out to COSMIC Settings and Noctalia, as the Network page does for
  some details.

Pick (a). The codec choice and file transfer exist nowhere else, and one
page on every desktop is the variant rule.

## Design

### 1. VPN: WireGuard and OpenConnect (Network page)

- **The VPN list:** the Network page gains a "VPN" section that lists
  NetworkManager connections of type `wireguard` and `vpn`. Each row has
  connect and disconnect, and remove asks for confirmation. All of it goes
  through `org.freedesktop.NetworkManager` as the user, like Wi-Fi today.
  The `Network` interface in `system/nm.go` grows `VPNs()`,
  `ActivateVPN(id)`, `DeactivateVPN(id)`, `RemoveVPN(id)`,
  `ImportWireGuard(path)` and `AddOpenConnect(...)`.
- **"Import WireGuard…"** opens a file picker, through the portal or Gio's
  own, and reads a `.conf` file. It uses NetworkManager's
  `Settings.AddConnection` with a settings map the app builds from the file:
  `[Interface]` and `[Peer]` keys, DNS, AllowedIPs. The parser is a small,
  tested Go function. We do not use `nmcli connection import`, so there is
  no CLI dependency.
  - `wireguard-tools` (`wg`, `wg-quick`) is not needed for this path. It
    comes from Hadron (#6193) for terminal users.
- **"Add company VPN…" (OpenConnect).**
  - The form asks for the server, the protocol (Cisco AnyConnect, Palo Alto
    GlobalProtect, Fortinet, Juniper or Pulse) and a user name.
  - Connecting asks for the password, and a second factor if the server
    asks, in an Isotope dialog. The app is NetworkManager's secret agent
    while the page is open.
  - The build adds the `openconnect` library (with GnuTLS or OpenSSL,
    whichever the image has, and libxml2) and the `NetworkManager-openconnect`
    service plugin, built without its GTK auth dialog.
  - The tray must also answer secrets when the app is closed. Isotope's
    `isotope-update tray` is always running in the session, so it registers
    a NetworkManager secret agent that opens the same dialog through
    `isotope system network --vpn-secret`. This is open question 2.

### 2. Bluetooth (new Bluetooth page, PipeWire codecs, LE Audio, OBEX)

- **Codecs.** New Dockerfile stages build `libfreeaptx`, `libldac`, `liblc3`
  and `fdk-aac-free`, and `opus` if not already built. PipeWire turns on
  `-Dbluez5-codec-aptx`, `-ldac`, `-lc3`, `-aac`, `-opus` and
  `-Dbluez5-codec-lc3plus=disabled`. The image check proves that each
  `libspa-codec-bluez5-*.so` exists and its libraries resolve.
- **LE Audio.** BlueZ is built with the LE Audio services: BAP, BASS, VCP,
  MCP, CSIP and the `iso` socket support. The kernel's `CONFIG_BT_LE` is
  already set. `/etc/bluetooth/main.conf` sets `Experimental = true` and
  `KernelExperimental = true` only if BlueZ's version still needs them for
  LE Audio. Check this at build time.
- **The Bluetooth page** (`system/page_bluetooth.go`, `system/bluez.go`,
  `PageBluetooth = "bluetooth"`):
  - **Controls:** on and off, which BlueZ calls `Powered`; and "Search for
    devices", which runs `StartDiscovery` for 30 s.
  - **Device list:** the paired and nearby devices, with an icon from
    BlueZ's `Icon` property, a connected state, and the battery level from
    `org.bluez.Battery1`.
  - **Actions:** pair, connect, disconnect and forget.
  - **Pairing:** the app registers an `org.bluez.Agent1` while the page is
    open. A PIN or passkey request, or a confirmation of a displayed code,
    opens an Isotope dialog.
  - **Headphones:** a "Sound quality" choice lists the A2DP profiles that
    WirePlumber offers for the device, such as `a2dp-sink-sbc` and
    `a2dp-sink-aac`. It shows the codec in use and sets the profile through
    PipeWire's D-Bus-free API (`pw-cli`'s protocol through the Go `pipewire`
    client, or `wpctl set-profile`). That choice of mechanism is open
    question 3.
  - **Menus:** `isotope-bluetooth-menu` (sway, i3), the Isotope menu's
    Bluetooth entry, and the COSMIC quick settings' Bluetooth row (its detail
    link) open `isotope system bluetooth`. The menus keep their fast actions.
- **File transfer (OBEX):**
  - BlueZ is built with `--enable-obex`. `obex.service` (obexd) runs as a
    user service, D-Bus activated on the session bus.
  - **Sending:** "Send file…" on a device row picks files and calls
    `org.bluez.obex.Client1.CreateSession` and then `ObjectPush1.SendFile`.
    Progress shows on the row.
  - **Receiving:** obexd refuses incoming files unless an
    `org.bluez.obex.Agent1` is registered. The always-running tray registers
    it. An incoming file opens a small Isotope dialog,
    `isotope-system ask bluetooth-file`: the device, the file name and the
    size, with Accept and Decline. Files land in `~/Downloads`. With no
    answer in 60 s, the transfer is refused.

The tray answers VPN secrets the same way, with
`isotope-system ask vpn-secret`. The dialog prints its answer as JSON on
stdout, and the tray hands it to NetworkManager. One dialog mechanism serves
every desktop, whatever the notification server supports.

### 2b. Layers from hadron-layers: QEMU guest agent (vm line), smartmontools (every image)

The guest agent comes from a hadron-layers layer (`qemu-guest-agent`, not
Hadron's default image). The `vm` image line copies that layer's files in
at build time, pinned by version. The `full` line can install it with
`isotope ext`. Its unit binds to the guest agent's virtio port, so it never
starts on bare metal.

smartmontools (`smartctl`, `smartd`) comes from a hadron-layers layer
too, copied into every desktop image and the server, with `smartd` not
enabled.

### 3. Intel video decode

New stages build `gmmlib` and `intel-media-driver` (iHD), on the `full`
line only, after `libva`. `check-desktop-gl.sh` proves `iHD_drv_video.so`
exists in the VA driver directory and its libraries resolve with `ldd`.
`LIBVA_DRIVER_NAME` is not set: libva picks iHD on Intel by itself.

### 4. Firewall (needs nftables from Hadron #6193)

- **The ruleset:** `/usr/share/isotope/firewall.nft`.
  - inet filter;
  - input policy drop;
  - allow established and related traffic, loopback, ICMP and ICMPv6, DHCP
    and DHCPv6 replies, and mDNS;
  - on the server, allow SSH (22/tcp).
- **The allowed ports:** the user's ports are in `/etc/isotope/firewall.nft`,
  generated.
- **The unit:** `isotope-firewall.service` loads both with `nft -f` at boot,
  before `network-pre.target`.
- **Defaults:** on for desktops and the server. The live ISO has it off, so
  installation over SSH still works.
- **Isotope System, Network page, a "Firewall" row:** on and off, and
  "Allow incoming…" to add a port, a protocol and a description, or to
  remove one. These call `isotope-updated`'s new `FirewallStatus`,
  `SetFirewall(on)`, `AllowPort(port, proto, note)` and `DenyPort(...)`,
  behind polkit `io.kairos.isotope-update.firewall` (admin only, no Flatpak
  callers).
- **Persistence:** the daemon writes `/oem/94_isotope_firewall.yaml`, an
  initramfs stage that rewrites `/etc/isotope/firewall.nft` and the on or
  off flag at every boot, as `91_isotope_locale.yaml` does. So the settings
  survive a reboot.
- **The CLI:** `isotope firewall [status|on|off|allow PORT/PROTO|deny PORT/PROTO]`
  for the server and SSH users, through the same daemon calls.
- **Docker:** Docker adds its own iptables-nft chains. The Isotope ruleset
  uses its own table (`inet isotope`) and never flushes others, so Docker
  keeps working. Check this with a published container port in the image
  test.

## Order and dependencies

1. Intel VA-API, the Bluetooth codecs and LE Audio. Build-only, no UI, no
   dependency.
2. The Bluetooth page, plus the menus opening it.
3. The VPN section of the Network page: the WireGuard import, then
   OpenConnect.
4. OBEX send and receive.
5. The firewall, once Hadron #6193 ships `nft` and Isotope's `HADRON_VERSION`
   includes it.

Each step is its own PR, so a long one (OpenConnect) does not hold the
others.

## Testing

- **Go:** unit tests in `system/` with fakes for the NetworkManager, BlueZ
  and OBEX interfaces, as `nm_test.go` and `udisks` do. Screenshot tests for
  the new page and sections.
- **Image checks:**
  - the codec plugins and `iHD_drv_video.so` resolve;
  - `obexd` and its unit exist;
  - the NetworkManager OpenConnect plugin's `.name` file and service binary
    exist;
  - `isotope-firewall.service` is enabled and its ruleset parses
    (`nft -c -f`).
- **Not testable in CI:** real headphones, a real VPN server, phone
  transfers. These go in the PR's manual test list.

## Decisions (maintainer, 2026-11-23)

1. OpenConnect v1: password and a typed second factor. SAML (web sign-in) is a follow-up on #136.
2. The tray (`isotope-update tray`) registers the NetworkManager secret agent and the OBEX agent, and opens Isotope dialogs. No new session service.
3. Codec switching uses `wpctl set-profile`, the only CLI call in the page.
4. The firewall is on by default on installed desktops and the server (inbound blocked, SSH allowed on the server); off on the live ISO.
