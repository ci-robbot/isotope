#!/usr/bin/env bash
# The checks are strings the inner sh expands, on purpose.
# shellcheck disable=SC2016
# Check that a built image's NetworkManager can manage a modem once the
# `modemmanager` extension (extensions/modemmanager) runs ModemManager:
# the WWAN device plugin and libnm-wwan are installed and load, libmm-glib is
# there, the NetworkManager binary exports every internal symbol the plugins
# use, the provider database path the extension fills is compiled in, and no
# file of the extension is already in the image (`isotope ext` would refuse
# it). Every image has this, the server and the agent included.
#
#   test/network/check-wwan.sh IMAGE
set -euo pipefail
IMAGE="${1:?usage: $0 IMAGE}"
fail=0
pass() { echo "PASS: $1"; }
bad() { echo "FAIL: $1"; fail=1; }
check() {
    local desc="$1"; shift
    if docker run --rm --entrypoint sh "$IMAGE" -c "$*" >/dev/null 2>&1; then pass "$desc"; else bad "$desc"; fi
}

PLUGDIR='$(ls -d /usr/lib/NetworkManager/[0-9]*/ | head -n 1)'
check "the WWAN device plugin and libnm-wwan are installed" \
    "d=$PLUGDIR; test -f \"\$d/libnm-device-plugin-wwan.so\" && test -f \"\$d/libnm-wwan.so\""
check "libmm-glib is installed" "test -e /usr/lib/libmm-glib.so.0"
check "the plugins resolve their libraries" \
    "d=$PLUGDIR; for f in \"\$d\"/libnm-wwan.so \"\$d\"/libnm-device-plugin-wwan.so; do ldd \"\$f\" >/tmp/ldd 2>&1 || exit 1; ! grep -q 'not found' /tmp/ldd || exit 1; done"
check "the provider database path is compiled in" \
    "d=$PLUGDIR; grep -q /usr/share/mobile-broadband-provider-info/serviceproviders.xml \"\$d/libnm-wwan.so\""
check "the ModemManager licence ships with libmm-glib" "test -s /usr/share/licenses/ModemManager/COPYING.LIB"

# The extension's files must not be in the image.
check "no ModemManager, CLI or provider database in the image" \
    '! test -e /usr/bin/ModemManager && ! test -e /usr/sbin/ModemManager && ! test -e /usr/bin/mmcli &&
     ! test -e /usr/bin/qmicli && ! test -e /usr/bin/mbimcli &&
     ! test -e /usr/share/mobile-broadband-provider-info &&
     ! test -e /usr/share/dbus-1/system.d/org.freedesktop.ModemManager1.conf &&
     ! test -e /usr/lib/systemd/system/ModemManager.service &&
     ! test -e /usr/lib/isotope-ext'

# NetworkManager exports only the internal symbols its own plugins use (its
# linker version script is made from them). Every nm_/NM symbol a plugin
# needs must be defined by the binary or by libnm-wwan, or the plugin does
# not load. readelf runs here, on the runner: the image has no binutils.
tmp=$(mktemp -d)
cid=$(docker create "$IMAGE" /bin/true)
trap 'docker rm -f "$cid" >/dev/null 2>&1 || true; rm -rf "$tmp"' EXIT
dir=$(docker run --rm --entrypoint sh "$IMAGE" -c 'ls -d /usr/lib/NetworkManager/[0-9]*/ | head -n 1')
nmbin=$(docker run --rm --entrypoint sh "$IMAGE" -c 'command -v NetworkManager || echo /usr/bin/NetworkManager')
if docker cp -L "$cid:$nmbin" "$tmp/NetworkManager" >/dev/null &&
   docker cp -L "$cid:${dir}libnm-wwan.so" "$tmp/libnm-wwan.so" >/dev/null &&
   docker cp -L "$cid:${dir}libnm-device-plugin-wwan.so" "$tmp/plugin.so" >/dev/null; then
    defined() { readelf -W --dyn-syms "$1" | awk '$7 != "UND" && NF >= 8 {sub(/@.*/, "", $8); print $8}'; }
    needed() { readelf -W --dyn-syms "$1" | awk '$7 == "UND" && NF >= 8 {sub(/@.*/, "", $8); print $8}' |
        grep -E '^(nm_|nmp_|_nm|NM|_NM|nmtst_|c_siphash_|c_list_)' || true; }
    { defined "$tmp/NetworkManager"; defined "$tmp/libnm-wwan.so"; } | sort -u >"$tmp/have"
    missing=$({ needed "$tmp/libnm-wwan.so"; needed "$tmp/plugin.so"; } | sort -u | comm -23 - "$tmp/have")
    if [ -z "$missing" ]; then
        pass "NetworkManager exports every internal symbol the WWAN plugins use"
    else
        bad "NetworkManager does not export what the WWAN plugins use: $(echo "$missing" | tr '\n' ' ')"
    fi
else
    bad "could not copy NetworkManager and its WWAN plugins out of the image"
fi

exit "$fail"
