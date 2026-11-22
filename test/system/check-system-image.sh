#!/usr/bin/env bash
# The checks are strings that the image's sh runs, on purpose.
# shellcheck disable=SC2016
# Isotope System in a built image: the binary of the variant's build, its
# desktop file, the window rule that floats it, the Isotope menu entry, the
# daemon methods and polkit actions it uses, and the tools behind its pages.
# For the agent image, the opposite: no isotope-system and no exFAT tools.
#
#   test/system/check-system-image.sh IMAGE FLAVOR
#
# FLAVOR is sway, i3, cosmic, niri or agent. sway, cosmic and niri get the
# Wayland-only build (no libX11); i3 the build with X11.
set -euo pipefail

usage() { echo "usage: $0 IMAGE FLAVOR" >&2; exit 2; }
[ $# -eq 2 ] || usage
IMAGE="$1"
FLAVOR="$2"
case "$FLAVOR" in sway|i3|cosmic|niri|agent) ;; *) usage ;; esac

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

# Session configuration lines, without comments.
active='grep -rhsv "^[[:space:]]*\(#\|//\)"'

if [ "$FLAVOR" = agent ]; then
    check "agent: no /usr/bin/isotope-system" '! test -e /usr/bin/isotope-system'
    check "agent: no isotope-system.desktop" '! test -e /usr/share/applications/isotope-system.desktop'
    check "agent: no mkfs.exfat" '! command -v mkfs.exfat'
    exit "$fail"
fi

check "/usr/bin/isotope-system is the Gio program" \
    'test -x /usr/bin/isotope-system && ldd /usr/bin/isotope-system 2>&1 | grep -q libwayland-client'
check "isotope-system: its libraries resolve" \
    'test -x /usr/bin/isotope-system && ! ldd /usr/bin/isotope-system 2>&1 | grep -q "not found"'
if [ "$FLAVOR" = i3 ]; then
    check "i3: the X11 build (links libX11)" 'ldd /usr/bin/isotope-system 2>&1 | grep -q libX11'
else
    check "$FLAVOR: the Wayland build (no libX11)" \
        'ldd /usr/bin/isotope-system 2>&1 | grep -q libwayland-client && ! ldd /usr/bin/isotope-system 2>&1 | grep -q libX11'
fi
check "'isotope-system --nonsense' exits 2" \
    'isotope-system --nonsense >/dev/null 2>&1; [ $? -eq 2 ]'

# The desktop file starts the program itself; the Isotope menu goes through
# the isotope command.
check "the desktop file starts isotope-system" \
    'grep -qx "Exec=isotope-system" /usr/share/applications/isotope-system.desktop'

# The window rule, exactly one: COSMIC floats a window by its own rules,
# none needed.
case "$FLAVOR" in
    sway)
        check "sway: isotope-system floats, one rule" \
            "[ \"\$($active /etc/sway | grep -cx 'for_window \[app_id=\"isotope-system\"\] floating enable')\" -eq 1 ] && [ \"\$($active /etc/sway | grep -c 'isotope-system\"\]')\" -eq 1 ]"
        ;;
    i3)
        check "i3: isotope-system floats, one rule" \
            "[ \"\$($active /etc/i3 | grep -cx 'for_window \[class=\"isotope-system\"\] floating enable')\" -eq 1 ] && [ \"\$($active /etc/i3 | grep -c 'isotope-system\"\]')\" -eq 1 ]"
        ;;
    niri)
        check "niri: isotope-system floats, one rule" \
            "$active /etc/niri | tr -d '\n' | grep -q 'window-rule {[^}]*match app-id=\"^isotope-system\$\"[^}]*open-floating true[^}]*}' && [ \"\$($active /etc/niri | grep -c 'app-id=\"^isotope-system\$\"')\" -eq 1 ]"
        ;;
esac

check "the Isotope menu lists System" \
    'grep -q "^System\${tab}" /usr/bin/isotope-menu'
check "the Isotope menu calls 'isotope system'" \
    'grep -q "exec isotope system" /usr/bin/isotope-menu'

# strings is not in the image; grep -a reads the binary the same way. The
# introspection XML is a string in the binary: anchor on its method element,
# not on the bare name, which other strings hold too.
for m in AddUser BackupInit Restore UpdateHistory; do
    check "isotope-updated's interface has the method $m" "grep -aq '<method name=\"$m\"' /usr/bin/isotope"
done

policy=/usr/share/polkit-1/actions/io.kairos.isotope-update.policy
check "policy declares io.kairos.isotope-update.users" \
    "grep -q 'action id=\"io.kairos.isotope-update.users\"' $policy"
check "policy declares io.kairos.isotope-update.restore" \
    "grep -q 'action id=\"io.kairos.isotope-update.restore\"' $policy"
check "the udisks rule grants modify-device to admin" \
    'grep -q "org.freedesktop.udisks2.modify-device\"" /usr/share/polkit-1/rules.d/50-isotope-udisks.rules && grep -q "isInGroup(\"admin\")" /usr/share/polkit-1/rules.d/50-isotope-udisks.rules'

for t in useradd usermod userdel gpasswd chpasswd openssl ssh-keyscan ssh-keygen mkfs.exfat exfatlabel mkfs.ext4; do
    check "$t is on PATH" "command -v $t"
done

exit "$fail"
