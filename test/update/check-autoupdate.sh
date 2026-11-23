#!/usr/bin/env bash
# Automatic updates (isotope-autoupdate.timer runs `isotope update --auto`).
#
#   test/update/check-autoupdate.sh --repo    the tree: the timer, the unit
#                                             and its conditions, the
#                                             Dockerfile wiring and the
#                                             agent removal (CI)
#   test/update/check-autoupdate.sh IMAGE     a built image: the timer is
#                                             enabled and the CLI knows
#                                             `update auto`; nothing on the
#                                             agent
set -euo pipefail
cd "$(dirname "$0")/../.."

fail=0
pass() { echo "PASS: $*"; }
bad()  { echo "FAIL: $*"; fail=1; }
check() { # check DESC CMD...
    local d="$1"; shift
    if "$@" >/dev/null 2>&1; then pass "$d"; else bad "$d"; fi
}

timer=usr/lib/systemd/system/isotope-autoupdate.timer
unit=usr/lib/systemd/system/isotope-autoupdate.service
link=etc/systemd/system/timers.target.wants/isotope-autoupdate.timer

repo_mode() {
    local C=rootfs/common
    check "the timer exists" test -f "$C/$timer"
    check "the timer runs daily" grep -qx 'OnCalendar=daily' "$C/$timer"
    check "the timer catches up a missed run" grep -qx 'Persistent=true' "$C/$timer"
    check "the timer starts at a random time" grep -qE '^RandomizedDelaySec=[1-9]' "$C/$timer"
    check "the timer is wanted by timers.target" grep -qx 'WantedBy=timers.target' "$C/$timer"
    check "the unit exists" test -f "$C/$unit"
    check "the unit runs isotope update --auto" grep -qx 'ExecStart=/usr/bin/isotope update --auto' "$C/$unit"
    check "the unit is a oneshot" grep -qx 'Type=oneshot' "$C/$unit"
    check "the unit runs as root (no User=)" sh -c "! grep -q '^User=' '$C/$unit'"
    for c in '!/run/cos/live_mode' '!/run/cos/install_mode' '!/run/cos/uki_install_mode' '!/run/isotope/live-desktop' \
        '!/run/cos/passive_mode' '!/run/cos/recovery_mode'; do
        check "the unit does not start when $c" grep -qx "ConditionPathExists=$c" "$C/$unit"
    done
    check "the daemon checks the same boot markers as the unit" sh -c \
        "grep -q '\"/run/cos/live_mode\", \"/run/cos/install_mode\", \"/run/cos/uki_install_mode\"' cmd/isotope/autoupdate.go"
    check "the daemon skips the previous and the recovery system, as the unit does" sh -c \
        "grep -q 'autoPassiveMarker  = \"/run/cos/passive_mode\"' cmd/isotope/autoupdate.go && grep -q 'autoRecoveryMarker = \"/run/cos/recovery_mode\"' cmd/isotope/autoupdate.go"
    check "the setting is /oem/96_isotope_autoupdate.yaml, a number no other /oem file uses" sh -c \
        "grep -q '\"/oem/96_isotope_autoupdate.yaml\"' cmd/isotope/autoupdate.go && ! grep -rhoE '/oem/96_[a-z_-]+' rootfs cmd pkg --include=* | grep -v '96_isotope_autoupdate' | grep -q ."
    check "both image stages enable the timer" sh -c \
        "[ \$(grep -c '^RUN systemctl enable isotope-autoupdate.timer' Dockerfile) = 2 ]"
    check "the server-config stage copies the timer and the unit" sh -c "
        sed -n '/^FROM scratch AS server-config/,/^FROM /p' Dockerfile |
            grep -q 'COPY $C/$unit $C/$timer /usr/lib/systemd/system/'"
    check "the agent removes the timer, the unit and the link" sh -c "
        grep -q '/$timer' Dockerfile.agent &&
        grep -q '/$unit' Dockerfile.agent &&
        grep -q '/$link' Dockerfile.agent"
}

image_mode() {
    local img="$1" flavor
    flavor=$(docker run --rm --entrypoint sh "$img" -c 'sed -n "s/^FLAVOR=//p" /etc/isotope/release')
    echo "image $img, flavor ${flavor:-unknown}"
    in_image() { # in_image DESC CMD
        if docker run --rm --entrypoint sh "$img" -c "$2" >/dev/null 2>&1; then pass "$1"; else bad "$1"; fi
    }
    if [ "$flavor" = agent ]; then
        in_image "the agent has no automatic update timer, unit or link" \
            "! test -e /$timer && ! test -e /$unit && ! test -e /$link"
        return
    fi
    in_image "the timer is enabled" "test \"\$(readlink /$link)\" = /$timer && test -f /$timer"
    in_image "the unit is installed" "grep -qx 'ExecStart=/usr/bin/isotope update --auto' /$unit"
    in_image "isotope knows update auto" "isotope update auto help | grep -q 'Automatic updates'"
    in_image "automatic updates are off on a new machine" "! test -e /oem/96_isotope_autoupdate.yaml"
}

case "${1:-}" in
--repo) repo_mode ;;
"") echo "usage: $0 --repo | IMAGE" >&2; exit 2 ;;
*) image_mode "$1" ;;
esac
if [ "$fail" -eq 0 ]; then echo "all automatic update checks passed"; fi
exit "$fail"
