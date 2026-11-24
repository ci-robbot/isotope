#!/usr/bin/env bash
# The checks are strings that the inner sh expands, on purpose.
# shellcheck disable=SC2016
# Printing: CUPS, cups-filters, cups-browsed, Ghostscript, Avahi and ipp-usb,
# GTK's CUPS print backend, and where CUPS keeps its printers.
#
#   test/printing/check-printing.sh --repo    the tree: the configs, the
#                                             stages' wiring in the
#                                             Dockerfiles, the persistence,
#                                             the polkit action (CI)
#   test/printing/check-printing.sh IMAGE     a built desktop image: the
#                                             binaries and their libraries,
#                                             the GTK module, the units and
#                                             links, cupsd -t, the persistent
#                                             ServerRoot; nothing on the agent
set -euo pipefail
cd "$(dirname "$0")/../.."

fail=0
pass() { echo "PASS: $*"; }
bad()  { echo "FAIL: $*"; fail=1; }
check() { # check DESC CMD...
    local d="$1"; shift
    if "$@" >/dev/null 2>&1; then pass "$d"; else bad "$d"; fi
}
finish() {
    if [ "$fail" -eq 0 ]; then echo "all printing checks passed"; fi
    exit "$fail"
}

# stage NAME: the text of one Dockerfile stage.
stage() {
    awk -v want="$1" '/^FROM / { on = ($NF == want) } on' Dockerfile
}

# ---------------------------------------------------------------------------
repo_mode() {
    local E=rootfs/common/etc/cups state=rootfs/common/system/oem/94_isotope_state.yaml
    # Persistence: the printers in /var/lib/cups, kept on the persistent
    # partition, while the settings in /etc/cups come from the image.
    check "cups-files.conf moves CUPS's ServerRoot to /var/lib/cups" grep -qx 'ServerRoot /var/lib/cups' "$E/cups-files.conf"
    check "94_isotope_state.yaml keeps /var/lib/cups on the persistent partition" \
        sh -c "awk '/for p in/,/; do/' '$state' | grep -q /var/lib/cups"
    check "test-persist-state.sh expects /var/lib/cups" grep -q '/var/lib/cups"' test/state/test-persist-state.sh
    check "the image's admin group is GID 900, as kairos-init makes it" \
        sh -c "[ \"\$(grep -c 'groupadd -f -g 900 admin' Dockerfile)\" = 2 ] && ! grep -qE 'groupadd -f admin|for g in admin ' Dockerfile"
    check "the server's config does not take /etc/cups" sh -c "! awk '/^FROM .* AS server-config\$/,/^FROM .* AS desktop-rootfs/' Dockerfile | grep -q etc/cups"
    # The scheduler's settings.
    check "cupsd listens on the socket and localhost only" \
        sh -c "grep -qx 'Listen localhost:631' '$E/cupsd.conf' && grep -qx 'Listen /run/cups/cups.sock' '$E/cupsd.conf' && [ \"\$(grep -c '^Listen' '$E/cupsd.conf')\" = 2 ] && ! grep -q '^Port' '$E/cupsd.conf'"
    check "cupsd shares nothing" sh -c "grep -qx 'Browsing No' '$E/cupsd.conf' && grep -qx 'DefaultShared No' '$E/cupsd.conf'"
    check "cupsd does not exit when idle (cups-browsed keeps a connection)" grep -qx 'IdleExitTimeout 0' "$E/cupsd.conf"
    check "@SYSTEM is root and admin" grep -qx 'SystemGroup root admin' "$E/cups-files.conf"
    check "filters run as lp" sh -c "grep -qx 'User lp' '$E/cups-files.conf' && grep -qx 'Group lp' '$E/cups-files.conf'"
    check "CUPS logs to the journal" sh -c "grep -qx 'ErrorLog syslog' '$E/cups-files.conf'"
    check "CUPS-Get-Devices stays admin only" grep -q 'CUPS-Set-Default CUPS-Get-Devices>' "$E/cupsd.conf"
    check "cups-browsed makes queues for local (IPP-over-USB) printers only, so a Remove lasts" grep -qx 'CreateIPPPrinterQueues LocalOnly' "$E/cups-browsed.conf"
    check "cups-browsed makes no queues for other CUPS servers" grep -qx 'CreateRemoteCUPSPrinterQueues No' "$E/cups-browsed.conf"
    check "cups-browsed publishes nothing" grep -qx 'BrowseLocalProtocols none' "$E/cups-browsed.conf"
    check "cups-browsed puts addresses in its URIs (musl cannot resolve .local)" grep -qx 'IPBasedDeviceURIs IPv4' "$E/cups-browsed.conf"
    check "cups-browsed browses DNS-SD only" grep -qx 'BrowseRemoteProtocols dnssd' "$E/cups-browsed.conf"
    # The stages.
    local s l
    for s in libusb libdaemon avahi cups libjpeg-turbo qpdf ghostscript libcupsfilters libppd cups-filters cups-browsed ipp-usb printing-files printing; do
        check "the $s stage exists" grep -qE "^FROM .* AS $s\$" Dockerfile
    done
    local t o
    t=$(stage cups)
    for o in --with-dnssd=avahi --with-tls=openssl --enable-libusb --with-ondemand=systemd --with-domainsocket=/run/cups/cups.sock; do
        check "CUPS is built with $o" grep -q -- "$o" <<<"$t"
    done
    check "the ghostscript stage checks its cups, pwgraster and appleraster devices" \
        grep -q 'for d in cups pwgraster appleraster' <<<"$(stage ghostscript)"
    check "libcupsfilters stays on 2.1 (renovate.json)" sh -c "grep -q '\"OpenPrinting/libcupsfilters\"' renovate.json && grep -q '\"allowedVersions\": \"<2.2\"' renovate.json && grep -q '^ARG LIBCUPSFILTERS_VERSION=2\\.1\\.' Dockerfile"
    local g
    for g in gtk3 gtk3-x11; do
        t=$(stage "$g")
        check "$g builds GTK's CUPS print backend" grep -q -- '-Dprint_backends=file,cups' <<<"$t"
        check "$g copies CUPS" grep -qx 'COPY --from=cups /cups /' <<<"$t"
    done
    check "every GTK 3 pin is the same version" sh -c "[ \"\$(grep -h '^ARG GTK3_VERSION=' Dockerfile Dockerfile.agent | sort -u | wc -l)\" = 1 ]"
    check "desktop-default copies printing before the session config" \
        sh -c "awk '/^FROM .* AS desktop-default\$/,/^FROM .* AS server-default\$/' Dockerfile | grep -n -e '^COPY --from=printing / /' -e '^COPY --from=desktop-config / /' | cut -d: -f2 | tr '\n' ' ' | grep -q '^COPY --from=printing / / COPY --from=desktop-config / /'"
    check "server-default copies no printing" sh -c "! awk '/^FROM .* AS server-default\$/,/^FROM .* AS sway-default\$/' Dockerfile | grep -q -e 'from=printing' -e 'from=cups'"
    for l in multi-user.target.wants/cups.service multi-user.target.wants/cups-browsed.service \
             multi-user.target.wants/avahi-daemon.service sockets.target.wants/cups.socket \
             sockets.target.wants/avahi-daemon.socket dbus-org.freedesktop.Avahi.service; do
        check "desktop-default links $l" sh -c "awk '/^FROM .* AS desktop-default\$/,/^FROM .* AS server-default\$/' Dockerfile | grep -q '$l'"
    done
    check "the agent removes every printing file and GTK's CUPS backend" \
        sh -c "grep -q 'done < /usr/share/isotope/printing.files' Dockerfile.agent && grep -q 'printbackends/libprintbackend-cups.so' Dockerfile.agent"
    # The daemon's action.
    check "the polkit file declares the printers action" \
        grep -q '<action id="io.kairos.isotope-update.printers">' rootfs/common/usr/share/polkit-1/actions/io.kairos.isotope-update.policy
    check "the printers action is admin only by default (no allow_* yes)" \
        sh -c "awk '/io.kairos.isotope-update.printers/,/<\\/action>/' rootfs/common/usr/share/polkit-1/actions/io.kairos.isotope-update.policy | grep -q '<allow_active>no</allow_active>'"
    check "isotope-updated uses the printers action" grep -q 'actionPrinters = "io.kairos.isotope-update.printers"' cmd/isotope/dbus.go
    check "the rules harness covers the printers action" grep -q 'up + "printers"' test/polkit/rules-harness.js
    check "Isotope System has a Printers page" grep -q 'PagePrinters   Page = "printers"' cmd/isotope-system/args.go
}

# ---------------------------------------------------------------------------
image_mode() {
    local img="$1" flavor
    in_image() { # in_image DESC CMD
        if docker run --rm --entrypoint sh "$img" -c "$2" >/dev/null 2>&1; then pass "$1"; else bad "$1"; fi
    }
    flavor=$(docker run --rm --entrypoint sh "$img" -c 'sed -n "s/^FLAVOR=//p" /etc/isotope/release')
    if [ "$flavor" = agent ]; then
        in_image "the agent has no CUPS, cups-browsed, Avahi, ipp-usb or Ghostscript" \
            "! test -e /usr/sbin/cupsd && ! test -e /usr/sbin/cups-browsed && ! test -e /usr/sbin/avahi-daemon && ! test -e /usr/sbin/ipp-usb && ! test -e /usr/bin/gs"
        in_image "the agent has no printing units or links" \
            "! find /etc/systemd /usr/lib/systemd /usr/lib/udev -name 'cups*' -o -name 'avahi*' -o -name '*ipp-usb*' | grep -q ."
        in_image "the agent has no GTK CUPS backend" "! test -e /usr/lib/gtk-3.0/3.0.0/printbackends/libprintbackend-cups.so"
        finish
    fi
    if [ "$flavor" = server ]; then
        in_image "the server has no printing" "! test -e /usr/sbin/cupsd && ! test -e /usr/sbin/avahi-daemon && ! test -e /etc/cups"
        finish
    fi
    for b in /usr/sbin/cupsd /usr/sbin/lpadmin /usr/bin/lpstat /usr/bin/lp /usr/sbin/cups-browsed \
             /usr/sbin/avahi-daemon /usr/bin/avahi-browse /usr/sbin/ipp-usb /usr/bin/gs \
             /usr/lib/cups/filter/universal /usr/lib/cups/backend/dnssd /usr/lib/cups/backend/ipp \
             /usr/lib/cups/backend/implicitclass /usr/lib/gtk-3.0/3.0.0/printbackends/libprintbackend-cups.so; do
        in_image "$b exists and its libraries resolve" "test -e $b && ldd $b >/tmp/ldd 2>&1 && ! grep -q 'not found' /tmp/ldd"
    done
    in_image "every library and program of the printing stage resolves" \
        'grep -E "^/usr/(s?bin|lib)/" /usr/share/isotope/printing.files | while read -r f; do case "$f" in *.so*|*/bin/*|*/sbin/*|/usr/lib/cups/*) ;; *) continue ;; esac; [ -f "$f" ] || continue; head -c 4 "$f" | grep -q ELF || continue; ldd "$f" 2>&1 | grep -q "not found" && { echo "$f"; exit 1; }; done; true'
    in_image "Ghostscript has the cups, pwgraster and appleraster devices" \
        'd="$(gs -h)"; for x in cups pwgraster appleraster; do echo "$d" | grep -qw "$x" || exit 1; done'
    in_image "cupsd's configuration parses" "cupsd -t -c /etc/cups/cupsd.conf -s /etc/cups/cups-files.conf"
    in_image "CUPS's ServerRoot is /var/lib/cups" "grep -qx 'ServerRoot /var/lib/cups' /etc/cups/cups-files.conf && test -d /var/lib/cups"
    in_image "/var/lib/cups is kept on the persistent partition" "grep -q /var/lib/cups /system/oem/94_isotope_state.yaml"
    in_image "CUPS's test page is there" "test -f /usr/share/cups/data/testprint"
    in_image "the driverless MIME rules are there" "ls /usr/share/cups/mime/*.convs >/dev/null && grep -rq 'application/vnd.cups-pdf' /usr/share/cups/mime"
    for l in multi-user.target.wants/cups.service multi-user.target.wants/cups-browsed.service \
             multi-user.target.wants/avahi-daemon.service sockets.target.wants/cups.socket \
             sockets.target.wants/avahi-daemon.socket dbus-org.freedesktop.Avahi.service; do
        in_image "$l is linked" "test -L /etc/systemd/system/$l && test -f /etc/systemd/system/$l"
    done
    in_image "ipp-usb is started by its udev rule, from /usr/sbin" \
        "grep -q 'SYSTEMD_WANTS}+=\"ipp-usb.service\"' /usr/lib/udev/rules.d/71-ipp-usb.rules && grep -qx 'ExecStart=/usr/sbin/ipp-usb udev' /usr/lib/systemd/system/ipp-usb.service"
    in_image "the lp and avahi users exist" "getent passwd lp && getent passwd avahi"
    in_image "Avahi announces no SSH service" "! ls /etc/avahi/services/ssh.service /etc/avahi/services/sftp-ssh.service"
    in_image "Avahi's D-Bus policy is installed" "test -f /usr/share/dbus-1/system.d/avahi-dbus.conf"
    in_image "no printing headers or pkg-config files" "! ls /usr/include/cups /usr/include/avahi-client /usr/lib/pkgconfig/cups.pc"
    in_image "every printing component ships its licence" \
        'for n in cups avahi libusb libdaemon ghostscript qpdf libjpeg-turbo libcupsfilters libppd cups-filters cups-browsed ipp-usb; do ls /usr/share/licenses/$n/* >/dev/null 2>&1 || { echo $n; exit 1; }; done'
    in_image "the polkit file declares the printers action" \
        "grep -q '<action id=\"io.kairos.isotope-update.printers\">' /usr/share/polkit-1/actions/io.kairos.isotope-update.policy"
}

case "${1:-}" in
--repo) repo_mode ;;
"") echo "usage: $0 --repo | IMAGE" >&2; exit 2 ;;
*) image_mode "$1" ;;
esac
finish
