# Desktop batch 1 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Ship these, each controlled from Isotope System on all four desktops:
- Intel VA-API;
- Bluetooth codecs, LE Audio and file transfer, plus a Bluetooth page;
- WireGuard import and OpenConnect;
- the QEMU guest agent on the vm line and smartmontools on every image, both from hadron-layers;
- a firewall.

**Architecture:**
- **Build:** the build-only pieces are Dockerfile stages, with image checks.
- **Isotope System (`system/`):** it talks to NetworkManager, BlueZ and obexd over D-Bus as the user, as `system/nm.go` does today. It talks to PipeWire through `wpctl`.
- **The tray (`update/`, pure Go):** it registers NetworkManager's secret agent and the OBEX agent. When either needs an answer, it runs `isotope-system ask …` and reads the dialog's JSON answer from stdout.
- **The firewall:** the only root path. It goes through `isotope-updated` and polkit, with an `/oem` stage.

**Tech Stack:** Dockerfile (BuildKit), Go 1.25 (`system/` with cgo and Gio over `guikit/`; `update/` in pure Go with godbus), POSIX sh, systemd, PipeWire/WirePlumber, BlueZ, NetworkManager, nftables.

**Spec:** `docs/superpowers/specs/2026-11-23-desktop-batch1-design.md`. Its "Decisions" section is binding. Tracking issue: #136.

## Global Constraints

- All four desktops (sway, i3, niri, COSMIC) get every item, as AGENTS.md "Every desktop variant has the same feature set" says. The server gets only the firewall. The agent image (Dockerfile.agent) removes the Bluetooth page's menu wiring only if it removes Isotope System; check what it does today and follow it.
- Settings live in Isotope System. Root changes go through `isotope-updated` and polkit (admin only, no Flatpak callers). Never a terminal, sudo or pkexec.
- No CLI parsing, except the codec choice (Decision 3): `pw-dump` JSON is read for the card's profiles (wpctl 0.5.7 does not print them) and `wpctl set-profile` writes the choice.
- OpenConnect v1 handles a password and a typed second factor. No SAML (Decision 1).
- The tray registers the agents. There is no new session service (Decision 2).
- The firewall is on by default on installed desktops and the server, with SSH allowed on the server. It is off on the live ISO (Decision 4).
- Names: "Isotope", `isotope-*`, `/etc/isotope`, `/usr/share/isotope`. Never "Hadron" for an Isotope feature.
- Copy is plain Simplified Technical English, in the voice of the existing pages.
- **Disk:** `df -h /` must show at least 10 GB free before any build. Never prune docker caches. Never build a whole image locally; build stages with `docker buildx build --target <stage> -o type=cacheonly .`. Whole images are verified in CI.
- **Commits:** `git -c user.name="Ettore Di Giacinto" -c user.email=mudler@kairos.io commit -s`, conventional subjects, ending with `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`. Never bare `git stash`.
- **Tests:**
  - `update/`: `go vet ./... && go test ./...`.
  - `system/`: in a container, with `docker run --rm -v "$PWD:/src" -w /src/system golang:1.25-alpine sh -c 'apk add -q gcc musl-dev pkgconf wayland-dev libxkbcommon-dev mesa-dev vulkan-headers && CGO_ENABLED=1 go vet -tags nox11,novulkan ./... && CGO_ENABLED=1 go test -tags nox11,novulkan ./...'` (called SYSTEST below).
  - gofmt and shellcheck clean.

## Review Focus

1. **Bluetooth adapter missing or off** (a desktop PC with no adapter, rfkill on). The page shows "No Bluetooth adapter" or an "Off" state, never an error loop. Pinned in Task 4: `TestBluetoothNoAdapter`, `TestBluetoothPoweredOff`.
2. **A WireGuard file that is not valid** (missing PrivateKey, two `[Interface]` sections, a key that is not base64, Windows line endings, comments). The import refuses with a message naming the problem, and accepts CRLF and comments. Pinned in Task 7: `TestParseWireGuard*` cases.
3. **A tray agent's dialog closed without an answer, or never answering.** NetworkManager gets `UserCanceled`, and obexd gets a refusal after 60 s. Neither blocks the tray. Pinned in Task 6: `TestSecretAgentCancel`, `TestObexAgentTimeout`.
4. **The firewall breaks Docker or SSH.** A published container port and SSH on the server still work. Pinned in Task 11: image check `docker run -p` reachable, `nft list ruleset` keeps Docker's tables.
5. **Codec switch on a device with only SBC.** The quality choice is hidden, or lists one item, and never calls `wpctl` with a profile that does not exist. Pinned in Task 5: `TestCodecChoiceOnlySBC`.

---

## File map

| File | Responsibility |
|---|---|
| `Dockerfile` | stages `gmmlib`, `intel-media-driver`, `libfreeaptx`, `libldac`, `liblc3`, `fdk-aac`, `opus`, `openconnect`, `nm-openconnect`; flags in `pipewire`, `bluez`, `networkmanager`; the qemu-ga layer on the vm line; the firewall files |
| `system/bluez.go`, `system/bluez_test.go` | `Bluetooth` interface over BlueZ and obexd, plus its fake |
| `system/page_bluetooth.go` | the Bluetooth page |
| `system/codec.go` | `wpctl` profile listing and switching |
| `system/nm.go`, `system/nm_vpn.go`, `system/wireguard.go` | VPN list and actions, the WireGuard parser, OpenConnect add |
| `system/page_network.go` | the VPN section and the firewall row |
| `system/ask.go` | `isotope-system ask vpn-secret` and `ask bluetooth-file` dialogs (JSON on stdout) |
| `update/agents.go`, `update/agents_test.go` | the tray's NetworkManager SecretAgent and OBEX Agent1 |
| `update/firewall.go`, daemon methods, polkit | the firewall (Task 11) |
| `rootfs/usr/bin/isotope-bluetooth-menu`, `rootfs/usr/bin/isotope-menu`, `applets/isotope-quick/src/state.rs` | open `isotope system bluetooth` for details |
| `rootfs/etc/bluetooth/main.conf`, `rootfs/usr/lib/systemd/user/obex.service` (if upstream's is not installed) | LE Audio, OBEX |
| `test/graphics/check-desktop-gl.sh`, `test/bluetooth/check-bluetooth.sh` (new), `test/network/check-vpn.sh` (new), `test/firewall/check-firewall.sh` (new) | image checks |
| `AGENTS.md` | the Bluetooth, VPN, firewall and guest agent rows |

---

### Task 1: Intel VA-API

**Files:** `Dockerfile` (new stages `gmmlib`, `intel-media-driver` after `libva-full` ~line 375; copy into the image where the `libva` drivers are copied), `test/graphics/check-desktop-gl.sh`.

- [ ] **Step 1:** Add the failing check to `check-desktop-gl.sh`, on the `full` line only: `/usr/lib/dri/iHD_drv_video.so` exists (or wherever `LIBVA_DRIVERS_PATH` points; read how the radeonsi VA driver is checked and mirror it), and `ldd` finds every library.
- [ ] **Step 2:** Add the stages:
  - `ARG GMMLIB_VERSION` and `ARG INTEL_MEDIA_DRIVER_VERSION`, each with a renovate comment (`datasource=github-releases`, `depName=intel/gmmlib` and `intel/media-driver`);
  - build with CMake on `toolchain`, against the `libva` stage's output;
  - `full` only, using the `libva-${MESA_GPU}` pattern if `MESA_GPU` is on main by then, otherwise `GPU`.
  - Ship `iHD_drv_video.so` and `libigdgmm.so.*` only, with no headers or pkg-config files (#134 rule).
- [ ] **Step 3:** `docker buildx build --build-arg GPU=full --build-arg DESKTOP=sway --target intel-media-driver -o type=cacheonly .` passes. `scripts/check-renovate-pins.sh` passes.
- [ ] **Step 4:** Commit `feat(graphics): Intel VA-API through intel-media-driver`.

### Task 2: Bluetooth audio codecs

**Files:** `Dockerfile` (new stages `libfreeaptx`, `libldac`, `liblc3`, `fdk-aac`, `opus` near `sbc` ~line 1585; `pipewire` meson flags ~line 1640; COPY lines everywhere `sbc` is copied: grep `COPY --from=sbc`), `test/bluetooth/check-bluetooth.sh` (new, PASS/FAIL style of `test/server/check-server.sh`, `IMAGE` arg), a CI wiring line next to the other desktop image checks in `.github/workflows/desktop-images.yml` and `release.yml`.

- [ ] **Step 1:** Write `check-bluetooth.sh` with these checks:
  - each of `libspa-codec-bluez5-{sbc,aac,aptx,ldac,lc3,opus}.so` exists under `/usr/lib/spa-0.2/bluez5/`, and `ldd` resolves it;
  - `bluetoothctl --version` runs.
  It fails today, because only sbc exists.
- [ ] **Step 2:** Add the stages, each pinned with a renovate comment:
  - `libfreeaptx`: github-tags `iamthehorker/libfreeaptx`;
  - `libldac`: `EHfive/ldacBT`, built with CMake;
  - `liblc3`: `google/liblc3`, built with meson;
  - `fdk-aac`: use the patent-free `fdk-aac-free` source, the one tracked by `https://gitlab.freedesktop.org/wtaymans/fdk-aac-stripped`, or the `mstorsjo/fdk-aac` tag with the patented parts removed. Pick the one whose licence the image's `/usr/share/licenses` can carry, and write why in the stage comment;
  - `opus`: `xiph/opus`.
  Turn on PipeWire's `-Dbluez5-codec-aptx=enabled -Dbluez5-codec-ldac=enabled -Dbluez5-codec-lc3=enabled -Dbluez5-codec-aac=enabled -Dbluez5-codec-opus=enabled -Dbluez5-codec-lc3plus=disabled`. Copy each library wherever `sbc` is copied today. The libraries only; no headers.
- [ ] **Step 3:** `docker buildx build --target pipewire -o type=cacheonly .` passes. In the build stage's own RUN, list `/pipewire/usr/lib/spa-0.2/bluez5/` and fail if a codec `.so` is missing.
- [ ] **Step 4:** Commit `feat(audio): AAC, aptX, LDAC, LC3 and Opus for Bluetooth headphones`.

### Task 3: BlueZ LE Audio and OBEX

**Files:** `Dockerfile` (`bluez` stage ~line 1570: `--enable-obex`, and drop `--disable-obex`; check that the LE Audio profiles (`bap`, `bass`, `vcp`, `mcp`, `csip`) are built by default in the pinned BlueZ version; libical is needed for obexd, so add a stage if the toolchain lacks it), `rootfs/etc/bluetooth/main.conf`, `test/bluetooth/check-bluetooth.sh`.

- [ ] **Step 1:** Extend `check-bluetooth.sh`:
  - `/usr/libexec/bluetooth/obexd` exists and resolves;
  - `obex.service` exists as a systemd user unit, plus the D-Bus session activation file `org.bluez.obex.service`;
  - `main.conf` has the LE Audio settings Step 2 decides.
- [ ] **Step 2:** Build BlueZ with obex. Read the pinned BlueZ's `src/main.conf` and NEWS: if LE Audio still needs `Experimental = true` (and `KernelExperimental` for ISO sockets), set both in `rootfs/etc/bluetooth/main.conf` with a comment. If it does not, leave them unset and say so in the commit. The server does not get these files: check the `server-config` stage list.
- [ ] **Step 3:** `--target bluez` builds. shellcheck is clean.
- [ ] **Step 4:** Commit `feat(bluetooth): LE Audio and file transfer (OBEX) in BlueZ`.

### Task 4: BlueZ model in Isotope System

**Files:** Create `system/bluez.go`, `system/bluez_test.go`. Modify `system/ui.go` (`Hooks.Bluetooth Bluetooth`) and `system/main.go` (wire the real client).

**Interfaces (Produces):**
```go
type BTDevice struct {
    Path      string // D-Bus object path, the identity
    Name      string
    Icon      string // BlueZ Icon: audio-headset, input-keyboard, phone, ...
    Paired    bool
    Connected bool
    Battery   int // 0-100, -1 unknown
}
type BTState struct {
    Adapter bool // an adapter exists
    Powered bool
    Discovering bool
    Devices []BTDevice // paired first, then by name
}
type Bluetooth interface {
    State() (BTState, error)
    SetPowered(on bool) error
    Discover(d time.Duration) error // StartDiscovery, StopDiscovery after d
    Pair(path string) error         // runs the page's Agent1 for prompts
    Connect(path string) error
    Disconnect(path string) error
    Forget(path string) error       // Adapter1.RemoveDevice
    SendFile(path, file string, progress func(sent, total int64)) error // obexd Client1 + ObjectPush1
}
// PairPrompt is how the page answers BlueZ's Agent1 while pairing.
type PairPrompt interface {
    PinCode(device string) (string, bool)
    Passkey(device string) (uint32, bool)
    Confirm(device string, passkey uint32) bool
}
```
`bluezBluetooth` is the real one: system bus `org.bluez` via `ObjectManager.GetManagedObjects`, plus the session bus `org.bluez.obex` for SendFile.

- [ ] **Step 1:** Tests against a fake object tree, built by a helper that returns `map[dbus.ObjectPath]map[string]map[string]dbus.Variant`:
  - `TestBluetoothState`: two devices, one paired and connected with `Battery1.Percentage`=80, and one nearby. The sort order and the fields match.
  - `TestBluetoothNoAdapter`: no `Adapter1` gives `Adapter=false` and no error.
  - `TestBluetoothPoweredOff`.
  - `TestForgetCallsRemoveDevice`: it records the method and the arg.
  Split the parsing (`stateFromObjects(objs) BTState`) from the bus, so these run without D-Bus.
- [ ] **Step 2:** SYSTEST fails, because the code is not there yet.
- [ ] **Step 3:** Implement `stateFromObjects` and `bluezBluetooth`. For `Pair`: export an `org.bluez.Agent1` at `/io/kairos/IsotopeSystem/agent`, `RegisterAgent(…, "KeyboardDisplay")`, `RequestDefaultAgent`, call `Device1.Pair`, then `UnregisterAgent`. Its methods call the `PairPrompt` the page gives.
- [ ] **Step 4:** SYSTEST passes.
- [ ] **Step 5:** Commit `feat(system): BlueZ model for the Bluetooth page`.

### Task 5: The Bluetooth page, codec choice, and the menus

**Files:** Create `system/page_bluetooth.go`, `system/codec.go`, and tests. Modify `system/args.go` (`PageBluetooth Page = "bluetooth"`, Title "Bluetooth", in `Pages` after `PageNetwork`), `rootfs/usr/bin/isotope-bluetooth-menu`, `rootfs/usr/bin/isotope-menu`, `applets/isotope-quick/src/state.rs:73`, the shortcut sheet if a binding changes, and AGENTS.md's Isotope System and Bluetooth menu rows.

**Interfaces:**
```go
// codec.go
type Profile struct{ Name, Label string; Active bool } // Name: a2dp-sink-aac; Label: "AAC"
func Profiles(deviceName string) ([]Profile, error) // wpctl status/inspect: the device's bluez card profiles, A2DP only
func SetProfile(deviceName, profile string) error   // wpctl set-profile <card id> <index>
```
The labels are SBC, SBC-XQ, AAC, aptX, aptX HD, LDAC and LC3, mapped from WirePlumber's profile names. An unknown name keeps its raw name.

- [ ] **Step 1:** Tests:
  - `TestBluetoothPageStates`: no adapter, off, on with devices. Each draws without panicking, and the expected strings are present: "No Bluetooth adapter", "Bluetooth is off", the device names.
  - `TestCodecChoiceOnlySBC`: with one profile, there is no choice control and `SetProfile` is never called.
  - `TestProfilesParse`: canned `wpctl` output gives the profiles and the active one. Put the `wpctl` output in `testdata/`.
  - `TestPairPromptConfirm`: a fake Agent1 confirm request opens the dialog, and Yes returns true.
- [ ] **Step 2:** SYSTEST fails.
- [ ] **Step 3:** Implement the page in the style of `page_network.go`:
  - the header has the on/off toggle and "Search for devices" (30 s);
  - each device row shows the icon, name and state, the battery when it is known, and Connect, Disconnect, Pair, Forget (with a confirmation) and "Send file…";
  - a "Sound quality" choice on an audio device when `Profiles` gives more than one;
  - pairing prompts use the app's dialog helpers (`dialog.go`).
  Change the menus:
  - `isotope-bluetooth-menu` keeps its fast actions and gains "Settings…", which runs `isotope system bluetooth`;
  - `isotope-menu`'s Bluetooth entry opens `isotope system bluetooth` on every desktop, as the Network entry does for its page; check how that one chooses;
  - the COSMIC quick settings' Bluetooth detail becomes `&["isotope", "system", "bluetooth"]`.
- [ ] **Step 4:** SYSTEST passes. `cargo check` for `applets/` is not possible locally without the COSMIC build environment, so leave the Rust one-liner to CI and say so. Update `test/system/check-system-menu.sh` if it lists pages.
- [ ] **Step 5:** Commit `feat(system): Bluetooth page with pairing, battery and sound quality`.

### Task 6: Ask dialogs and the tray's agents

**Files:** Create `system/ask.go` and `update/agents.go`, with tests. Modify `system/main.go` (`ask` subcommand) and `update/trayapp.go` (start the agents).

**Interfaces:**
- `isotope-system ask vpn-secret --name NAME --fields password[,otp] [--message TEXT]` prints `{"password":"…","otp":"…"}` and exits 0. It exits 1 on Cancel or when the window is closed.
- `isotope-system ask bluetooth-file --device NAME --file NAME --size BYTES` prints `{"accept":true}` or `{"accept":false}`.
- `update/agents.go`:
  ```go
  type Asker func(args ...string) (map[string]any, error) // runs isotope-system ask ...; error on exit 1 or a timeout
  func StartSecretAgent(conn *dbus.Conn, ask Asker) error // org.freedesktop.NetworkManager.SecretAgent at /io/kairos/Isotope/SecretAgent, AgentManager.Register("io.kairos.isotope")
  func StartObexAgent(conn *dbus.Conn, ask Asker, downloads string) error // org.bluez.obex.Agent1, AgentManager1.RegisterAgent
  ```
  - `GetSecrets` answers only for VPN connections (`vpn` and `wireguard` settings), with `flags & ALLOW_INTERACTION`. For anything else it returns `NoSecrets`, so other agents and nmcli keep working.
  - `AuthorizePush` asks, then returns the target path `downloads/<name>`, with a ` (1)` suffix on a clash. It refuses after 60 s.

- [ ] **Step 1:** Tests in `update/`, with an `Asker` fake and the D-Bus method functions called directly:
  - `TestSecretAgentVPN`;
  - `TestSecretAgentIgnoresWifi`;
  - `TestSecretAgentCancel` (the asker errors, which gives `org.freedesktop.NetworkManager.SecretAgent.UserCanceled`);
  - `TestObexAgentAccept` (the path is in downloads, and the clash suffix works);
  - `TestObexAgentTimeout` (the asker blocks; the reply is a refusal at 60 s, injectable as 50 ms in the test).
  In `system/`: `TestAskVPNSecretJSON`, which drives the dialog model with typed fields and checks stdout.
- [ ] **Step 2:** The tests fail.
- [ ] **Step 3:** Implement both. The tray starts both agents at start-up. A failure to register is logged, not fatal: obexd or NetworkManager may be missing on the agent image.
- [ ] **Step 4:** `update` tests, then SYSTEST, pass.
- [ ] **Step 5:** Commit `feat(tray): answer VPN passwords and incoming Bluetooth files`.

### Task 7: VPN list and WireGuard import

**Files:** Create `system/wireguard.go`, `system/nm_vpn.go`, and tests. Modify `system/nm.go` (`Network` interface) and `system/page_network.go` (the VPN section).

**Interfaces:**
```go
type VPN struct{ ID, UUID, Kind string; Active bool } // Kind: "WireGuard" or "OpenConnect"
// added to Network:
VPNs() ([]VPN, error)
ActivateVPN(uuid string) error
DeactivateVPN(uuid string) error
RemoveVPN(uuid string) error
ImportWireGuard(name string, conf []byte) error
AddOpenConnect(c OpenConnect) error // Task 8
func ParseWireGuard(conf []byte) (settings, error) // NetworkManager's settings map for AddConnection
```

- [ ] **Step 1:** Tests:
  - `TestParseWireGuard` with a full config: Address v4 and v6, DNS, MTU, ListenPort, two peers, AllowedIPs, Endpoint, PersistentKeepalive and a PresharedKey. Check the produced `connection`, `wireguard` (`private-key`, `peers` as `aa{sv}`), `ipv4` and `ipv6` maps against NetworkManager's documented keys.
  - `TestParseWireGuardCRLFAndComments`.
  - `TestParseWireGuardErrors`: no PrivateKey; a key that is not 32-byte base64; a peer without PublicKey; a second `[Interface]`; an unknown section. Each gives an error that names the line.
  - `TestVPNsFromConnections`: it filters `vpn` and `wireguard` types and marks the active ones.
- [ ] **Step 2:** The tests fail.
- [ ] **Step 3:**
  - Implement `ParseWireGuard` and the NetworkManager calls: `Settings.AddConnection`, `ActivateConnection` with `/` as device and specific object, `DeactivateConnection`, and `Connection.Delete`.
  - The page's "VPN" section lists the connections, with "Import WireGuard…" (a file picker through the app's existing file dialog helper, or a path field if the app has none) and "Add company VPN…" (Task 8).
  - The image's NetworkManager is built with `-Dcrypto=null`. Check that a WireGuard connection activates with it: NetworkManager's WireGuard code may need a crypto backend to derive the public key. Run a containerised `NetworkManager --version` and read the meson options of the pinned version. If it does need one, switch `-Dcrypto` to the backend the toolchain has (`openssl` or `gnutls`), say why in the stage comment, and add that to this task's commit.
- [ ] **Step 4:** SYSTEST passes. Add an image check, `test/network/check-vpn.sh`, wired like Task 2's: `nmcli connection import type wireguard file <a fixture>` succeeds inside the image (`NetworkManager` running in a privileged container is too heavy, so check the libnm plugin and `nmcli`'s support with `nmcli --version` and the presence of the WireGuard setting in `nmcli connection add type wireguard --help` output).
- [ ] **Step 5:** Commit `feat(system): VPN list and WireGuard import on the Network page`.

### Task 8: OpenConnect

**Files:** `Dockerfile` (stages `openconnect` and `nm-openconnect`, copied with NetworkManager), `system/nm_vpn.go`, `system/page_network.go`, `test/network/check-vpn.sh`.

**Interfaces:** `type OpenConnect struct{ Name, Gateway, Protocol, User string }`. `Protocol` is one of `anyconnect`, `gp`, `fortinet` or `nc`, labelled "Cisco AnyConnect", "Palo Alto GlobalProtect", "Fortinet" and "Juniper / Pulse".

- [ ] **Step 1:**
  - Tests: `TestOpenConnectSettings`. The connection map has `vpn.service-type` = `org.freedesktop.NetworkManager.openconnect`, `vpn.data` with `gateway`, `protocol` and `cookie-flags=2`, and the user name. Password flags make NetworkManager ask the agent (`password-flags=2`).
  - Image check: `/usr/lib/NetworkManager/VPN/nm-openconnect-service.name` and `/usr/libexec/nm-openconnect-service` exist and resolve.
- [ ] **Step 2:** The tests fail.
- [ ] **Step 3:**
  - Build `openconnect`: library and CLI, pinned with a renovate comment, `--without-gnutls` with OpenSSL from the toolchain, and libxml2 from the toolchain. Use upstream's `vpnc-script` or a minimal one. The CLI fetches it, so pin and ship it as `/usr/share/vpnc-scripts/vpnc-script`.
  - Build `NetworkManager-openconnect` with `--without-gnome --without-authdlg`, pinned.
  - The page's "Add company VPN…" form has Name, Server, Type and User name.
  - NetworkManager asks the tray agent (Task 6) for the password and second factor when connecting. Check how NetworkManager-openconnect's service asks for secrets without the GTK auth dialog (`nm-openconnect-auth-dialog` is absent). If the service needs an auth dialog binary to collect the cookie, write a minimal `/usr/libexec/nm-openconnect-auth-dialog` in Go (in `update/`, as `isotope nm-openconnect-auth`, a link). It speaks NetworkManager's auth-dialog stdin/stdout protocol, runs `openconnect --authenticate` with the password and OTP from `isotope-system ask vpn-secret`, and returns cookie, gateway and gwcert. Document which path you took. This is the risky step: if neither works within the task, stop and report.
- [ ] **Step 4:** SYSTEST, the update tests, `--target nm-openconnect` and shellcheck all pass.
- [ ] **Step 5:** Commit `feat(network): company VPN through OpenConnect`.

### Task 9: OBEX send and receive in the UI

**Files:** `system/page_bluetooth.go` ("Send file…", with progress on the row), `system/bluez.go` (`SendFile` real implementation), `test/bluetooth/check-bluetooth.sh` (the tray autostart is unchanged; obex.service is activatable).

- [ ] **Step 1:** Tests:
  - `TestSendFileProgress`: a fake session object for `Transfer1`, where Status `active` with Transferred moving to `complete` reports progress and then done, and `error` reports a failure message.
  - `TestSendFileNoObex`: obexd is missing on the session bus, which gives "File transfer is not available" and no panic.
- [ ] **Step 2:** The tests fail.
- [ ] **Step 3:** Implement `SendFile`: `Client1.CreateSession(addr, {"Target": "opp"})`, `ObjectPush1.SendFile`, a watch on `Transfer1.PropertiesChanged`, then `RemoveSession`. The row's "Send file…" uses the file picker. Receiving is the tray agent's job (Task 6), so nothing goes on the page beyond a line: "Files sent to this computer are saved in Downloads."
- [ ] **Step 4:** SYSTEST passes.
- [ ] **Step 5:** Commit `feat(system): send files to Bluetooth devices`.

### Task 10: Layers from hadron-layers: the QEMU guest agent (vm line) and smartmontools (every image)

**Blocked on** the hadron-layers `qemu-guest-agent` and `smartmontools` layer PRs merging. smartmontools goes into every desktop image (both lines) and the server, with `smartd` not enabled. The Disks page can read health from it later (#136). Its report gives the image reference scheme in `/tmp/claude-1000/-home-mudler--git/5780f878-ac93-42cd-b7ae-8b80c885a83a/scratchpad/hadron-layers-qga-report.md`.

**Files:** `Dockerfile` (`ARG QEMU_GUEST_AGENT_LAYER_VERSION` with a renovate comment for the layer's image; a `guest-tools-${GPU}` stage pair, where `vm` copies the layer's files and `full` is empty; COPY in `desktop-default`), the server untouched, `test/boot-ux/check-boot-units.sh` or a new check.

- [ ] **Step 1:** Write the failing checks:
  - on a `vm` image, `/usr/bin/qemu-ga` runs `--version` and `qemu-guest-agent.service` is present and enabled the way the layer intends;
  - on a `full` image, both are absent.
- [ ] **Step 2:** Add the stages: `FROM <layer image>:<version> AS qga-layer`, then copy its `usr/` tree.
- [ ] **Step 3:** `--target guest-tools-vm` builds.
- [ ] **Step 4:** Do the same for smartmontools: an `ARG SMARTMONTOOLS_LAYER_VERSION` with a renovate comment, a `smart-layer` stage, and a COPY of its `usr/` in `desktop-default` and `server-default`. The checks: `smartctl --version` runs on every image, and `smartd.service` is not enabled.
- [ ] **Step 5:** Commit `feat(image): QEMU guest agent and smartmontools from hadron-layers`.

### Task 11: Firewall

**Blocked on** kairos-io/kairos#6193 (the `nft` binary in Hadron) and Isotope's `HADRON_VERSION` reaching that release.

**Files:**
- Create `rootfs/usr/share/isotope/firewall.nft`, `rootfs/usr/lib/systemd/system/isotope-firewall.service`, `rootfs/usr/libexec/isotope-firewall`, `update/firewall.go` and tests, and `test/firewall/check-firewall.sh`.
- Modify the daemon's D-Bus methods and the polkit actions and rules: `io.kairos.isotope-update.firewall` in the polkit policy file, plus the admin rule, following `io.kairos.isotope-update.settings`. Also modify `update/main.go` (`isotope firewall`), `system/page_network.go` (the Firewall row), the `server-config` stage list, and AGENTS.md.

**Interfaces:**
- **Daemon:** `FirewallStatus() (on bool, rules [](port uint16, proto string, note string))`, `SetFirewall(on bool)`, `AllowPort(port uint16, proto string, note string)`, `DenyPort(port uint16, proto string)`. `proto` is `tcp` or `udp`.
- **State:**
  - `/oem/94_isotope_firewall.yaml` is an initramfs-stage cloud-config that writes `/etc/isotope/firewall` (`on|off`) and `/etc/isotope/firewall.nft` (the user's accept rules) at each boot, like `91_isotope_locale.yaml`.
  - `isotope-firewall load` loads `/usr/share/isotope/firewall.nft` plus `/etc/isotope/firewall.nft` into table `inet isotope` (input hook, priority filter, policy drop; established, related, lo, icmp, icmpv6, dhcp, dhcpv6 and mdns accepted; on the server, also tcp 22 from `/usr/share/isotope/firewall-server.nft`).
  - `isotope-firewall unload` deletes only `table inet isotope`.

- [ ] **Step 1:** Tests:
  - `TestFirewallYAML` (round trip of the rule set);
  - `TestAllowPortValidates` (port 0, 65536 and proto `icmp` are refused);
  - `TestFirewallCLI` (`isotope firewall allow 8080/tcp "dev server"` calls the daemon);
  - `check-firewall.sh` on the image: `nft -c -f` on both files, the unit is enabled, and on the server the SSH rule is present. CI runs, privileged: `isotope-firewall load`, then `nft list ruleset` has `table inet isotope` and keeps Docker's tables. A container started with `-p 18080:80` answers on `127.0.0.1:18080`.
- [ ] **Step 2:** The tests fail.
- [ ] **Step 3:**
  - Implement it.
  - The unit is `Before=network-pre.target`, `Wants=network-pre.target`, `ConditionPathExists=!/run/cos/live_mode` (live ISO off; check the real live marker the installer uses), and `ExecStart=/usr/libexec/isotope-firewall load`.
  - The Network page's Firewall row shows On or Off and the allowed list, with "Allow incoming…" and Remove.
- [ ] **Step 4:** Update tests, SYSTEST, shellcheck and polkit check (`test/polkit/check-polkit-rules.sh`) all pass.
- [ ] **Step 5:** Commit `feat(security): a firewall, on by default, managed in Isotope System`.

### Task 12: Docs

**Files:** `AGENTS.md`, `README.md`.

- [ ] **Step 1:** AGENTS.md rows:
  - **"Bluetooth menu"** becomes "Bluetooth": it covers the page, the codecs, LE Audio, OBEX and the tray agent.
  - **"Isotope System"** gains the Bluetooth page, the VPN section and the Firewall row.
  - **New rows:** "VPN" and "Firewall".
  - **Graphics:** the Intel VA note now says iHD is built.
  - **The vm line:** the guest agent.
  - **The server table:** the firewall.
  - README's feature list gets one line each.
- [ ] **Step 2:** Every command, path and unit named exists (grep).
- [ ] **Step 3:** Commit `docs: Bluetooth, VPN, firewall and guest agent`.

## Order

Tasks 1–3 are build-only and come first. Task 4 → Task 5 → Task 6 → Task 7 → Task 8 → Task 9 → Task 12 run on this branch. Tasks 10 and 11 wait for their blockers. Each lands on this branch when its blocker is met, or in a follow-up PR if the branch merges first.

## Self-review

- **Spec coverage:**
  - VPN (Tasks 7, 8, 6);
  - Bluetooth codecs (2), LE Audio and OBEX (3, 9), the page (4, 5), the tray agents (6);
  - Intel VA (1), the guest agent and smartmontools layers (10), the firewall (11), docs (12);
  - Decisions 1–4 appear in the Global Constraints and Tasks 8, 6, 5 and 11.
- **Type consistency:** `Bluetooth`, `BTState`, `Network`, `VPN`, `OpenConnect`, `Asker` and `Profile` are each defined once and used by name.
- **Review Focus:** each of the five lines has a named test in its task.
