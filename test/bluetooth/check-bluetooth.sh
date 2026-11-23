#!/usr/bin/env bash
# The checks are strings that the inner sh expands, on purpose.
# shellcheck disable=SC2016
# Check a built desktop image's Bluetooth audio and file transfer: every codec plugin of
# PipeWire's bluez5 module is there and its libraries resolve, the codec
# libraries carry their licence, and none of them leaks headers.
#
#   test/bluetooth/check-bluetooth.sh [IMAGE]
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

D=/usr/lib/spa-0.2/bluez5
for c in sbc aac aptx ldac lc3 opus; do
    check "codec plugin $c exists and its libraries resolve" \
        "test -f $D/libspa-codec-bluez5-$c.so && ldd $D/libspa-codec-bluez5-$c.so >/tmp/ldd 2>&1 && ! grep -q 'not found' /tmp/ldd"
done
check "the bluez5 monitor plugin resolves" \
    "ldd $D/libspa-bluez5.so >/tmp/ldd 2>&1 && ! grep -q 'not found' /tmp/ldd"
check "bluetoothctl runs" "bluetoothctl --version"

for l in libfreeaptx libldacBT_enc libldacBT_abr liblc3 libfdk-aac libopus; do
    check "$l is installed" "ls /usr/lib/$l.so.* >/dev/null"
done
for n in libfreeaptx libldac liblc3 fdk-aac opus; do
    check "licence of $n is installed" "ls /usr/share/licenses/$n/* >/dev/null"
done
# The codec libraries are runtime files only: no headers, pkg-config files.
check "no codec headers or pkg-config files" \
    '! ls /usr/include/freeaptx.h /usr/include/ldac /usr/include/lc3.h /usr/include/fdk-aac /usr/include/opus /usr/lib/pkgconfig/libfreeaptx.pc /usr/lib/pkgconfig/ldacBT-*.pc /usr/lib/pkgconfig/lc3.pc /usr/lib/pkgconfig/fdk-aac.pc /usr/lib/pkgconfig/opus.pc 2>/dev/null | grep -q .'

# File transfer (OBEX): obexd and its libraries resolve (ldd only; libical 3
# comes from the libical-runtime stage), the user unit is there with the
# dbus-org.bluez.obex.service name that the D-Bus session activation file asks
# for, and libical 3 ships its licence and no headers.
check "obexd exists and its libraries resolve" \
    'test -x /usr/libexec/bluetooth/obexd && ldd /usr/libexec/bluetooth/obexd >/tmp/ldd 2>&1 && ! grep -q "not found" /tmp/ldd'
check "obex.service is a systemd user unit" "test -f /usr/lib/systemd/user/obex.service"
check "dbus-org.bluez.obex.service links to obex.service for every user" \
    '[ "$(readlink /usr/lib/systemd/user/dbus-org.bluez.obex.service)" = obex.service ]'
check "obex.service has its D-Bus session activation file" \
    "grep -q obex.service /usr/share/dbus-1/services/org.bluez.obex.service"
check "libical 3 is installed with its licence" \
    "test -e /usr/lib/libical.so.3 && test -e /usr/lib/libicalvcal.so.3 && test -f /usr/share/licenses/libical/LICENSE.LGPL21.txt"
check "no libical headers, pkg-config or CMake files" \
    '! ls /usr/include/libical /usr/lib/pkgconfig/libical*.pc /usr/lib/cmake/LibIcal 2>/dev/null | grep -q .'
# LE Audio: BlueZ 5.79 keeps these behind Experimental, and the ISO sockets
# behind the kernel's ISO feature; main.conf turns on both.
check "main.conf turns on LE Audio (Experimental and the ISO socket feature)" \
    "grep -q '^Experimental = true' /etc/bluetooth/main.conf && grep -q '^KernelExperimental = 6fbaf188-05e0-496a-9885-d6ddfdb4e03e' /etc/bluetooth/main.conf"
for p in bap bass vcp mcp csip micp ccp; do
    check "bluetoothd has the LE Audio profile $p" \
        "grep -aq __bluetooth_builtin_$p /usr/libexec/bluetooth/bluetoothd"
done

exit "$fail"
