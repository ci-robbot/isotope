#!/usr/bin/env bash
# Boot ordering of a built image: what decides how soon the login screen
# appears, and that tty1 always has a way to log in.
#
#   test/boot-ux/check-boot-units.sh [IMAGE]
#
# - No boot stage waits for the network. The Kairos boot stage runs inside
#   cos-setup-boot.service, and ly and cosmic-greeter start after it, so a
#   `systemctl start` there of a unit ordered after network-online.target
#   held the login screen until the network came up or wait-online gave up
#   (120 s with no cable, or on Wi-Fi).
# - NetworkManager is the only network manager: systemd-networkd and its
#   wait-online (which cannot see Wi-Fi) are not enabled.
# - getty@tty1 is enabled, not masked: Kairos' own boot stage enables it, and
#   it is the login when no login screen runs. A login screen on tty1 takes
#   the VT with Conflicts=getty@tty1 and gives it back with OnFailure=.
#
# - UPower and power-profiles-daemon are installed and run from boot on the
#   desktop images and the agent.
# - udisks2 is installed and run from boot on the desktop images, with the
#   polkit rule and the udev rule that hides the Kairos partitions; the agent
#   has none.
#
# Works on every desktop image, the agent and the server (which has no login
# screen and no UPower, so only the first three groups apply).
set -euo pipefail
IMAGE="${1:-sway-desktop:dev}"
fail=0
run() { docker run --rm --entrypoint sh "$IMAGE" -c "$1"; }
check() {
    local desc="$1"; shift
    if run "$*" >/dev/null 2>&1; then
        echo "PASS: $desc"
    else
        echo "FAIL: $desc"; fail=1
    fi
}

# Every unit a boot stage starts and waits for, that is ordered after
# network-online.target. Prints "FILE: UNIT"; empty is a pass.
# shellcheck disable=SC2016
waits='
for f in /system/oem/*.yaml; do
    grep -E "^[[:space:]]*-?[[:space:]]*systemctl start " "$f" | grep -v -- "--no-block" |
        sed -E "s/.*systemctl start //; s/(\|\||;|&&).*//" | tr " " "\n" | grep -v "^-" | grep . |
        while read -r u; do
            case "$u" in *.*) ;; *) u="$u.service" ;; esac
            t="${u%%@*}"; [ "$t" = "$u" ] || t="$t@.${u##*.}"
            for d in /usr/lib/systemd/system /etc/systemd/system; do
                cat "$d/$u" "$d/$t" "$d/$u.d/"*.conf "$d/$t.d/"*.conf 2>/dev/null
            done | grep -qE "^(After|Wants|Requires)=.*network-online\.target" && echo "$f: $u"
        done
done
true'
blocking="$(run "$waits" 2>/dev/null)" || { echo "FAIL: could not read the boot stages"; fail=1; }
if [ -z "$blocking" ]; then
    echo "PASS: no boot stage waits for a unit ordered after network-online.target"
else
    echo "FAIL: a boot stage waits for a unit ordered after network-online.target (use --no-block):"
    printf '  %s\n' "$blocking"
    fail=1
fi
# Same probe, on a stage that does wait, so a probe that finds nothing at all
# cannot pass the check above.
check "the probe finds a blocking start" \
    "mkdir -p /system/oem && printf 'stages:\n  boot:\n    - commands:\n        - systemctl start docker.service || true\n' > /system/oem/99_probe.yaml && test -f /usr/lib/systemd/system/docker.service && ($waits) | grep -q 99_probe.yaml"

ena() { echo "[ \"\$(systemctl --root=/ is-enabled $1 2>/dev/null)\" = enabled ]"; }
for u in systemd-networkd.service systemd-networkd.socket systemd-networkd-wait-online.service; do
    check "$u is not enabled (NetworkManager manages the network)" "! $(ena "$u") && ! ls /etc/systemd/system/*.wants/$u >/dev/null 2>&1"
done
check "NetworkManager and its wait-online are enabled" \
    "$(ena NetworkManager.service) && $(ena NetworkManager-wait-online.service)"

check "the boot stage persists saved networks and Bluetooth pairings" \
    "grep -q /etc/NetworkManager/system-connections /system/oem/94_isotope_state.yaml && grep -q /var/lib/bluetooth /system/oem/94_isotope_state.yaml"
check "getty@tty1 is enabled, not masked" "$(ena getty@tty1.service)"
# Kairos' 09_systemd_services.yaml runs this at every boot; on a masked unit it
# fails and the boot logs "failed to run systemctl enable getty@tty1".
check "Kairos' 'systemctl enable getty@tty1' succeeds" "systemctl enable getty@tty1"

# The login screens on tty1: the template ly@ (sway, i3, niri, agent) and
# cosmic-greeter. UNIT FILE DROPIN GETTY
dm() {
    local unit="$1" file="$2" dropin="$3" getty="$4"
    if ! run "test -f $file" >/dev/null 2>&1; then return; fi
    check "$unit gives tty1 to its login screen (Conflicts=$getty)" "grep -qx 'Conflicts=$getty' $file"
    check "$unit gives tty1 back when it fails (OnFailure=$getty)" "grep -qx 'OnFailure=$getty' $dropin"
    check "getty@tty1 can start again after the login screen gave up" \
        "grep -qx 'StartLimitIntervalSec=0' /usr/lib/systemd/system/getty@tty1.service.d/20-isotope.conf"
}
dm ly@tty1 /usr/lib/systemd/system/ly@.service \
    /usr/lib/systemd/system/ly@.service.d/20-getty-fallback.conf 'getty@%i.service'
dm cosmic-greeter /usr/lib/systemd/system/cosmic-greeter.service \
    /usr/lib/systemd/system/cosmic-greeter.service.d/20-getty-fallback.conf getty@tty1.service

# Battery and power profiles, on every desktop variant and the agent (not the
# server). A laptop running COSMIC showed no battery: /sys/class/power_supply
# had BAT0, but there was no UPower on the system bus, which COSMIC's battery
# applet and Noctalia read it from. Both units are wanted by multi-user.target
# because Kairos never reaches graphical.target, their WantedBy=.
if run "grep -qx FLAVOR=server /etc/isotope/release" >/dev/null 2>&1; then
    echo "SKIP: UPower and power-profiles-daemon (server edition)"
else
    for s in upower:/usr/lib/upowerd:org.freedesktop.UPower \
             power-profiles-daemon:/usr/lib/power-profiles-daemon:org.freedesktop.UPower.PowerProfiles; do
        u="${s%%:*}"; rest="${s#*:}"; bin="${rest%%:*}"; bus="${rest#*:}"
        check "$u: daemon, unit, D-Bus policy and activation file are installed" \
            "test -x $bin && grep -q '^ExecStart=$bin' /usr/lib/systemd/system/$u.service && test -f /usr/share/dbus-1/system.d/$bus.conf && grep -qx 'SystemdService=$u.service' /usr/share/dbus-1/system-services/$bus.service"
        check "$u.service is wanted by multi-user.target" \
            "test -L /etc/systemd/system/multi-user.target.wants/$u.service && test -e /etc/systemd/system/multi-user.target.wants/$u.service"
        check "$u: its libraries resolve" "test -x $bin && ! ldd $bin 2>&1 | grep -q 'not found'"
    done
    check "UPower and power-profiles-daemon polkit actions are installed" \
        "test -f /usr/share/polkit-1/actions/org.freedesktop.upower.policy && test -f /usr/share/polkit-1/actions/power-profiles-daemon.policy"
fi

# Removable disks: udisks2 mounts a USB or NVMe disk for the active session
# (a file manager, the Backup page in Welcome). Its unit is wanted by
# multi-user.target for the same reason as UPower's. The agent image removes
# it; the server never had it.
if run "grep -qx FLAVOR=server /etc/isotope/release" >/dev/null 2>&1; then
    echo "SKIP: udisks2 (server edition)"
elif run "grep -qx FLAVOR=agent /etc/isotope/release" >/dev/null 2>&1; then
    check "udisks2 is removed from the agent image" \
        "! test -e /usr/lib/udisks2/udisksd && ! test -e /usr/lib/systemd/system/udisks2.service && ! test -L /etc/systemd/system/multi-user.target.wants/udisks2.service"
else
    check "udisks2: daemon and unit are installed" \
        "test -x /usr/lib/udisks2/udisksd && grep -q '^ExecStart=/usr/lib/udisks2/udisksd' /usr/lib/systemd/system/udisks2.service"
    check "udisks2: its libraries resolve" \
        "test -x /usr/lib/udisks2/udisksd && ls /usr/lib/libbd_*.so.3 >/dev/null && ! for f in /usr/lib/udisks2/udisksd /usr/lib/libbd_*.so.3; do ldd \$f 2>&1; done | grep -qE 'not found|No such file|Error'"
    check "udisks2.service is wanted by multi-user.target" \
        "test -L /etc/systemd/system/multi-user.target.wants/udisks2.service && test -e /etc/systemd/system/multi-user.target.wants/udisks2.service"
    check "udisks2: D-Bus policy and activation file are installed" \
        "test -f /usr/share/dbus-1/system.d/org.freedesktop.UDisks2.conf && grep -qx 'SystemdService=udisks2.service' /usr/share/dbus-1/system-services/org.freedesktop.UDisks2.service"
    check "udisks2: polkit actions and the session rule are installed" \
        "test -f /usr/share/polkit-1/actions/org.freedesktop.UDisks2.policy && r=/usr/share/polkit-1/rules.d/50-isotope-udisks.rules && grep -qF 'udisks2.filesystem-mount\"' \$r && grep -qF 'udisks2.eject-media\"' \$r && grep -qF 'udisks2.power-off-drive\"' \$r && grep -q 'subject.local && subject.active' \$r && ! (grep -v '^//' \$r | grep -q 'mount-system\\|unmount-others')"
    check "udisks2: the Kairos partitions are ignored by udev" \
        "r=/usr/lib/udev/rules.d/90-isotope-udisks-ignore.rules && grep -q 'COS_\\*.*UDISKS_IGNORE' \$r && grep -q 'crypto_LUKS.*UDISKS_IGNORE' \$r && grep -q 'COS_.*UDISKS_SYSTEM' \$r"
    check "udisks2: the udev rule has no syntax errors" \
        "udevadm verify /usr/lib/udev/rules.d/90-isotope-udisks-ignore.rules"
fi

exit "$fail"
