# Printing: CUPS, driverless, and a Printers page

Issue: kairos-community/isotope#136, batch 3.

## Goal

A network or USB printer that speaks IPP (IPP Everywhere, AirPrint, Mopria,
IPP-over-USB) works on every desktop variant with no driver: GTK apps,
Flatpak apps and `lp` can print to it. Isotope System gets a Printers page:
list the printers, add one that was found, make one the default, remove one,
print a test page. The server and the agent image get no printing.

No proprietary or model-specific drivers. A printer that needs one (old
GDI or host-based printers) is out of scope.

## Components and stages

Every component is built from source in its own Dockerfile stage, on the
Hadron musl toolchain:

| Stage | What | Why |
|---|---|---|
| `libusb` | libusb 1.0 | CUPS's `usb` backend, ipp-usb |
| `libdaemon` | libdaemon 0.14 | avahi-daemon needs it |
| `avahi` | Avahi 0.9-rc5 (daemon, libavahi-client, libavahi-glib) | DNS-SD for CUPS, cups-browsed, ipp-usb and the page. 0.9-rc5 is what Alpine ships; 0.8 (2020) has a dozen unfixed CVEs. From the git tag, so `autoreconf` with gettext-tools |
| `cups` | CUPS 2.4.20 | the scheduler, `lpadmin`, libcups. OpenSSL, Avahi, systemd socket activation, PAM, libusb |
| `libjpeg-turbo`, `qpdf` | JPEG and QPDF 12 | libcupsfilters 2.1 needs QPDF (C++) |
| `ghostscript` | Ghostscript 10.08 with its `cups` devices | renders PDF to PWG and Apple raster for printers that take no PDF; the bundled third-party libraries, linked in |
| `libcupsfilters` | 2.1.2 | the filter functions. Not 2.2.x: its release notes say 2.2.0 does not build and 2.2.1 has regressions from the PDFio move, and point to 2.1.2. No Poppler, no mutool: Ghostscript is the one renderer |
| `libppd` | 2.1.1 | PPD support that cups-filters and cups-browsed need |
| `cups-filters` | 2.0.1 | the CUPS filters (`universal`, `driverless`) |
| `cups-browsed` | 2.1.1 | makes queues for IPP-over-USB printers only (`CreateIPPPrinterQueues LocalOnly`) |
| `ipp-usb` | 0.9.34 (Go, cgo with libusb and libavahi-client) | IPP-over-USB printers |
| `printing` | `FROM scratch`: the runtime files of all the above, and a list of them | copied in `desktop-default`; the agent removes every file on the list |

`desktop-default` copies `printing`; `server-default` does not.

**nss-mdns: not possible.** musl has no NSS, so `getaddrinfo("x.local")`
cannot go through Avahi. A `dnssd://` URI does not avoid that: CUPS resolves
the service through its Avahi client, but then looks up the `.local` host
name Avahi returned with getaddrinfo. Against the router's DNS that fails
("Name does not resolve"); through systemd-resolved's stub it fails as soon
as the printer has no IPv6 address (resolved fails the AAAA query, and musl
fails the whole lookup). Seen on an HP LaserJet MFP M140w on Isotope 0.2.5.
So the page adds a printer by the IPv4 address Avahi resolved,
`ipp(s)://ADDRESS:PORT/<rp>` (`printing.AddressURI`), and cups-browsed is
set to `IPBasedDeviceURIs IPv4` for the same reason. Only an IPv6 link-local
printer keeps a `dnssd://` URI. The cost: a printer that gets a new address
(a new DHCP lease) must be added again; a reserved address avoids that.

## GTK's CUPS print backend and the COSMIC keys

GTK is built with `-Dprint_backends=file`. It also compiles that list into
libgtk as the default `gtk-print-backends` setting, so shipping only the
module would not be enough: GTK would not load it.

`tools/stage-key.py` hashes every stage a COSMIC half reaches. On main, with
`--arg GPU=full` and `--arg GPU=vm`, neither `cosmic-shell` nor `cosmic-apps`
reaches `gtk3` or `gtk3-x11` (they reach glib2, pipewire, bluez, flatpak,
xdg-desktop-portal, mesa and so on, not GTK). So the GTK stages do not feed
the keys, and the `pipewire-codecs` / `bluez-full` pattern (a second stage
copied over the first) is not needed. The change is in place:
`-Dprint_backends=file,cups` in `gtk3` and `gtk3-x11`, which then copy the
`cups` stage. It costs one rebuild of the GTK stages and what links them,
once.

Proof that the keys do not move, on main and on this branch:

```
python3 tools/stage-key.py --arg GPU=full cosmic-shell
python3 tools/stage-key.py --arg GPU=full cosmic-apps
python3 tools/stage-key.py --arg GPU=vm cosmic-shell
python3 tools/stage-key.py --arg GPU=vm cosmic-apps
make -s GPU=full STAGE=cosmic-shell print-stage-key   # and the other three
```

main: `cosmic-shell-d950af973e2b`, `cosmic-apps-41b1e8d62366` (full),
`cosmic-shell-bb9c229f03c9`, `cosmic-apps-2a98130b314a` (vm). The branch must
print the same four; the report records both runs. No new stage is in the
reach set either: none of the printing stages is copied by a stage that the
COSMIC halves reach.

## Services

All linked in `desktop-default`, as bluetooth and UPower are:

- `cups.socket` (sockets.target) and `cups.service` (multi-user.target):
  cups-browsed keeps a connection to cupsd, so cupsd runs from boot.
- `cups-browsed.service` (multi-user.target).
- `avahi-daemon.service` and `avahi-daemon.socket`, with the
  `dbus-org.freedesktop.Avahi.service` alias for D-Bus activation.
- `ipp-usb.service` is started by its udev rule when an IPP-over-USB device
  is plugged in (upstream's design; `ipp-usb udev` exits when the last device
  goes), so it is not linked into a target.

They also run on the live desktop: printing from the live session is useful
and touches nothing on the disk.

**cups-browsed makes local queues only.** With `CreateIPPPrinterQueues
Driverless` it made a queue for every network printer: the page's Add then
made a second one (cups-browsed's queues have an address URI, not the
`dnssd://` one), and a Remove came back within seconds. `LocalOnly` (and
`CreateRemoteCUPSPrinterQueues No`, `BrowseLocalProtocols none`) is the
simpler and reliable of the two fixes: a `BrowseFilter` deny written on
Remove would need the daemon to edit cups-browsed.conf, which lives in the
rebuilt `/etc`. So network printers are added in the page (GTK apps also list
them directly through libcups and Avahi), and only an IPP-over-USB printer
gets an automatic queue while it is plugged in. The page matches a found
printer to a queue by URI, UUID, address and port, or name, and shows such an
automatic queue (a loopback address, or `implicitclass://`) without Remove.

**Firewall: no new rule.** cups-browsed and the page only browse (DNS-SD).
Avahi queries from port 5353 and the answers come back as multicast to
224.0.0.251 / ff02::fb port 5353, which the base ruleset already accepts.
IPP itself is an outgoing TCP connection to the printer (631) and its answers
are `established`. ipp-usb listens on loopback only (`iif lo`). cupsd listens
on the socket and localhost only, with `Browsing No`, so nothing is shared
and no inbound port is needed. Not covered: CUPS's SNMP backend broadcasts
to find old non-IPP printers, and the unicast answers to a broadcast are not
`established`; it is only for legacy printers, which need drivers anyway.
firewalld's own `mdns` service makes the same choice. Unicast mDNS
answers to 5353 (some printers send them) are dropped too: conntrack does not
tie them to a multicast query. Compliant printers answer by multicast.

## Persistence

Kairos rebuilds `/etc` and `/var` at every boot. CUPS keeps its queues in
`printers.conf` and `ppd/` under its ServerRoot, `/etc/cups`.

Options:

1. Bind-mount `/etc/cups` (CUSTOM_BIND_MOUNTS): the first boot copies the
   image's `/etc/cups` to the persistent partition, and from then on the
   image's `cupsd.conf` and `cups-browsed.conf` never reach the machine
   again: an upgrade could not fix a setting.
2. An `/oem` cloud-config written by the daemon (as the firewall does):
   the queues would have to be re-made at boot (`lpadmin -m everywhere`
   asks the printer, which may be off), or `printers.conf` and the PPDs
   copied into the YAML after every change, which misses changes made with
   `lpadmin` or by cups-browsed.
3. **Chosen:** move CUPS's state out of `/etc`. `cups-files.conf` sets
   `ServerRoot /var/lib/cups`; cupsd still reads `cupsd.conf` and
   `cups-files.conf` from `/etc/cups` (their paths are compiled in), and
   keeps `printers.conf`, `classes.conf`, `subscriptions.conf`, `ppd/` and
   `ssl/` in `/var/lib/cups`. `/var/lib/cups` is added to the
   CUSTOM_BIND_MOUNTS list of `94_isotope_state.yaml`, next to
   `/var/lib/bluetooth`. The settings follow the image, the queues survive,
   and any tool that changes a queue (the page, `lpadmin`, cups-browsed) is
   kept.

## The Printers page

`isotope system printers`. Works on the live system (not in `liveOnly`).

- **Listing, as the user.** `CUPS-Get-Printers` and `CUPS-Get-Default` over
  IPP on `/run/cups/cups.sock`, with `github.com/OpenPrinting/goipp`
  (BSD-2-Clause, OpenPrinting, the IPP codec ipp-usb uses). Every user may
  read the queues.
- **Found printers, as the user.** Avahi's D-Bus API (godbus, already a
  dependency): browse `_ipp._tcp` and `_ipps._tcp` for a few seconds, resolve
  each, read the TXT record (`ty` model, `note` location, `UUID`, `URF` and
  `pdl` for driverless support). IPP-over-USB printers show up too: ipp-usb
  publishes them on loopback. Not `CUPS-Get-Devices`: CUPS allows it to
  `@SYSTEM` only, and it runs every backend for many seconds.
- **Test page, as the user.** `Print-Job` with CUPS's own test page
  (`/usr/share/cups/data/testprint`, `application/vnd.cups-banner`), as the
  CUPS web interface does. Printing is open to every user.
- **Add, make default, remove: through `isotope-updated`.** `AddPrinter(name,
  uri, info, location)`, `SetDefaultPrinter(name)`, `RemovePrinter(name)`,
  behind the new polkit action `io.kairos.isotope-update.printers`
  (admin only, no Flatpak callers, as the firewall). The daemon runs
  `lpadmin` (`-p NAME -E -v URI -m everywhere -o printer-is-shared=false`,
  `-d NAME`, `-x NAME`) with a timeout, no shell. `-m everywhere` asks the
  printer for its capabilities and makes the PPD, which a pure-Go client
  cannot do. Names, URIs and texts are checked on both sides by
  `pkg/printing`: a queue name is letters, digits, `.`, `_` and `-`; a URI is
  `ipp`, `ipps` or `dnssd`.
- cups-files.conf's `SystemGroup root admin` lets an administrator use
  `lpadmin` in a terminal too; others get CUPS's own refusal.

## Agent image

`Dockerfile.agent` removes every file of the `printing` stage (its list,
`/usr/share/isotope/printing.files`, which leaves out lcms2 and libjpeg), the unit links, the users' groups are
left, and GTK's `libprintbackend-cups.so` (the agent builds its own GTK with
`print_backends=file`, so it would not be loaded anyway).

## Tests

- Go, in CI (`unit` job): `pkg/printing` (names, URIs, queue names);
  `cmd/isotope` (`AddPrinter` and friends against a fake `lpadmin`: argv,
  refusals, timeout, errors; introspection lists the methods);
  `cmd/isotope-system` (the IPP client against a fake CUPS on a Unix socket
  speaking goipp; the Avahi TXT parsing; the page's view model; the daemon
  client; the page against the fake daemon).
- Screenshot goldens: `cmd/isotope-system/testdata/printers-*.png`, compared
  within a tolerance as Isotope Welcome's are, `-update` to rewrite. The other
  pages have no goldens, and nothing else in `pkg/guikit` changes.
- `test/printing/check-printing.sh --repo` (CI): the configs, the units'
  wiring in the Dockerfile, ServerRoot and the bind mount, the GTK options,
  the agent removal. `IMAGE` (desktop-images.yml, release.yml): binaries and
  `ldd`, the GTK module, the units and links, `cupsd -t`, the persistence
  path, the polkit action.
- `test/polkit/check-polkit-rules.sh`: printers is yes for admin, no for
  others.
- `test/state/test-persist-state.sh`: `/var/lib/cups` is in the list.
- Stage builds are left to CI (8 GB of disk here).
