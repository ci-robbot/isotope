#!/usr/bin/env bash
# The checks are strings that the inner sh expands, on purpose.
# shellcheck disable=SC2016
# Check that a built desktop image's NetworkManager can do WireGuard, which
# the Network page of Isotope System imports (Settings.AddConnection), and
# OpenConnect (company VPN): the NetworkManager-openconnect plugin, the
# openconnect CLI the tray's secret agent logs in with, and vpnc-script.
# NetworkManager runs in no container here: this checks nmcli, libnm and the
# WireGuard setting. NetworkManager 1.50 builds WireGuard into its core, and
# the WireGuard code uses no crypto library, so -Dcrypto=null is fine.
#
#   test/network/check-vpn.sh [IMAGE]
set -euo pipefail
IMAGE="${1:-sway-desktop:dev}"
fail=0
check() {
    local desc="$1"; shift
    if docker run --rm --entrypoint sh "$IMAGE" -c "$*" >/dev/null 2>&1; then
        echo "PASS: $desc"
    else
        echo "FAIL: $desc"; fail=1
    fi
}

check "nmcli runs" "nmcli --version"
# No check that runs nmcli against NetworkManager: the container has no
# system bus. --offline below needs none and proves the type.
check "libnm has the WireGuard setting type" "grep -q NMSettingWireGuard /usr/lib/libnm.so.0"
check "nmcli --offline makes a WireGuard keyfile" \
    "nmcli --offline connection add type wireguard con-name t ifname wg0 ipv4.method disabled ipv6.method disabled | grep -q '^type=wireguard'"
check "the WireGuard setting is in libnm" \
    "grep -q 'listen-port' /usr/lib/libnm.so* && grep -q 'peer-routes' /usr/lib/libnm.so*"
check "libnm resolves its libraries" \
    'ldd /usr/lib/libnm.so.0 >/tmp/ldd 2>&1 && ! grep -q "not found" /tmp/ldd'

# OpenConnect. The .name file must point at the installed service; no GTK
# auth dialog is shipped, the tray's agent does its job.
NAME=/usr/lib/NetworkManager/VPN/nm-openconnect-service.name
check "the OpenConnect plugin's .name file is installed" \
    "grep -qx 'service=org.freedesktop.NetworkManager.openconnect' $NAME && grep -qx 'program=/usr/libexec/nm-openconnect-service' $NAME"
check "nm-openconnect-service and its helper exist and resolve" \
    'for b in /usr/libexec/nm-openconnect-service /usr/libexec/nm-openconnect-service-openconnect-helper; do
        test -x "$b" || exit 1
        ldd "$b" >/tmp/ldd 2>&1 || exit 1
        ! grep -q "not found" /tmp/ldd || exit 1
     done'
check "the plugin's D-Bus policy lets the service own its name" \
    "grep -q 'org.freedesktop.NetworkManager.openconnect' /usr/share/dbus-1/system.d/nm-openconnect-service.conf"
check "the nm-openconnect user exists" "grep -q '^nm-openconnect:' /etc/passwd"
check "openconnect runs, on OpenSSL, with every protocol of the form" \
    'v=$(openconnect --version 2>&1) && echo "$v" | grep -q "Using OpenSSL" &&
     for p in anyconnect gp fortinet nc pulse; do echo "$v" | grep "Supported protocols" | grep -qw "$p" || exit 1; done'
check "openconnect resolves its libraries" \
    'ldd "$(command -v openconnect)" >/tmp/ldd 2>&1 && ! grep -q "not found" /tmp/ldd'
check "vpnc-script is installed and is openconnect's default" \
    'test -x /usr/share/vpnc-scripts/vpnc-script && openconnect --version 2>&1 | grep -q "/usr/share/vpnc-scripts/vpnc-script"'
check "no OpenConnect headers or pkg-config files" \
    '! test -e /usr/include/openconnect.h && ! test -e /usr/lib/pkgconfig/openconnect.pc'
# Isotope System reads wg-quick files itself, so wg(8) is not needed; the
# kernel module is the kernel's business and is built in.
exit "$fail"
