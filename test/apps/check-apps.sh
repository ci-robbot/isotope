#!/usr/bin/env bash
# The checks are strings that the inner sh expands, on purpose.
# shellcheck disable=SC2016
# Apps: Isotope System's Apps page and the automatic app updates (see
# docs/superpowers/specs/2026-11-24-apps-page-design.md).
#
#   test/apps/check-apps.sh --repo   the tree: the user timer and service,
#                                    the link that enables them, the menu
#                                    entry, the Go stages, the agent's
#                                    removals, the server left out (CI)
#   test/apps/check-apps.sh IMAGE    a built image: the units and the link,
#                                    `isotope apps`, the menu entry; on the
#                                    agent and the server, none of them
#
# Installing, updating and removing a real Flathub app is on the design
# note's manual list.
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
    if [ "$fail" -eq 0 ]; then echo "all apps checks passed"; fi
    exit "$fail"
}

units=usr/lib/systemd/user
timer=$units/isotope-app-updates.timer
service=$units/isotope-app-updates.service
link=$units/timers.target.wants/isotope-app-updates.timer
markers='/run/cos/live_mode /run/cos/install_mode /run/cos/uki_install_mode /run/isotope/live-desktop'

# Each stage's lines, one file per stage, for the checks' greps.
S=$(mktemp -d)
trap 'rm -rf "$S"' EXIT
stage() { # stage FILE NAME
    awk -v s="$2" '$0 ~ "^FROM .* AS "s"$" {p=1; print; next} p && /^FROM / {exit} p' "$1" >"$S/$2"
}

# ---------------------------------------------------------------------------
repo_mode() {
    local C=rootfs/common m
    check "the timer is daily, spread over an hour, and catches up" sh -c "
        grep -qx 'OnCalendar=daily' $C/$timer && grep -qx 'RandomizedDelaySec=1h' $C/$timer &&
        grep -qx 'Persistent=true' $C/$timer && grep -qx 'WantedBy=timers.target' $C/$timer"
    check "the service runs the automatic run once" sh -c "
        grep -qx 'Type=oneshot' $C/$service && grep -qx 'ExecStart=/usr/bin/isotope apps update --auto' $C/$service"
    check "the service needs flatpak" grep -qx 'ConditionPathExists=/usr/bin/flatpak' "$C/$service"
    for m in $markers; do
        check "the timer never runs with $m" grep -qx "ConditionPathExists=!$m" "$C/$timer"
        check "the service never runs with $m" grep -qx "ConditionPathExists=!$m" "$C/$service"
    done
    check "every user's timers.target wants the timer" sh -c "
        [ -L $C/$link ] && [ \"\$(readlink $C/$link)\" = ../isotope-app-updates.timer ] && [ -f $C/$link ]"
    check "the Isotope menu's Install apps opens the Apps page" grep -qF '"Install apps") system_page apps || exec isotope welcome apps ;;' "$C/usr/bin/isotope-menu"
    check "the menu test covers it" grep -q "Install apps opens the Apps page" test/system/check-system-menu.sh
    check "Apps is the first page of Isotope System" grep -q '^var Pages = \[\]Page{PageApps, ' cmd/isotope-system/args.go
    check "the Apps page is one of the live system's install-first pages" grep -q 'PageApps: *true,' cmd/isotope-system/live.go
    check "the tray watches for app updates" grep -q 'go watchApps(' cmd/isotope/tray.go
    check "isotope apps is a command" grep -q 'case "apps":' cmd/isotope/main.go
    check "automatic app and system updates share the metered and battery checks" sh -c "
        grep -q 'if why := envSkip(env); why != \"\"' cmd/isotope/apps.go && grep -q 'return envSkip(d.autoEnv)' cmd/isotope/autoupdate.go"
    stage Dockerfile update-build
    stage Dockerfile system-build
    stage Dockerfile server-config
    check "the CLI's stage copies and tests pkg/flatpak" sh -c "
        grep -qx 'COPY pkg/flatpak/ pkg/flatpak/' $S/update-build && grep -q './pkg/flatpak/...' $S/update-build &&
        grep -q 'first-run-apps.list' $S/update-build"
    check "Isotope System's stage copies pkg/flatpak" grep -qx 'COPY pkg/flatpak/ pkg/flatpak/' "$S/system-build"
    check "the server takes no user units" sh -c "! grep -q 'systemd/user' $S/server-config"
    check "the agent removes the timer, its service and the link, and checks" sh -c "
        grep -q '/usr/lib/systemd/user/isotope-app-updates.timer' Dockerfile.agent &&
        grep -q '/usr/lib/systemd/user/isotope-app-updates.service' Dockerfile.agent &&
        grep -q '/usr/lib/systemd/user/timers.target.wants/isotope-app-updates.timer' Dockerfile.agent &&
        grep -qF \"! find /usr/lib/systemd/user /etc/systemd/user -name 'isotope-app-updates*'\" Dockerfile.agent"
    check "the agent removes Isotope System" grep -q 'rm -f /usr/bin/isotope-system' Dockerfile.agent
    finish
}

# ---------------------------------------------------------------------------
image_mode() {
    local img="$1" flavor m
    flavor=$(docker run --rm --entrypoint sh "$img" -c 'sed -n "s/^FLAVOR=//p" /etc/isotope/release' || true)
    in_image() { # in_image DESC CMD
        if docker run --rm --entrypoint sh "$img" -c "$2" >/dev/null 2>&1; then pass "$1"; else bad "$1"; fi
    }
    in_image "isotope apps is a command" "/usr/bin/isotope apps help | grep -q 'update --auto'"
    if [ "$flavor" = agent ] || [ "$flavor" = server ]; then
        in_image "$flavor: no app update timer, service or link" \
            "! find /usr/lib/systemd/user /etc/systemd/user -name 'isotope-app-updates*' 2>/dev/null | grep -q ."
        in_image "$flavor: no Isotope System" "! test -e /usr/bin/isotope-system"
        if [ "$flavor" = server ]; then
            in_image "server: no flatpak, so isotope apps says so" \
                "! test -e /usr/bin/flatpak && /usr/bin/isotope apps update --auto 2>&1 | grep -q 'Flatpak is not on this system'"
        fi
        finish
    fi
    in_image "the timer and the service are there" "test -f /$timer && test -f /$service"
    in_image "every user's timers.target wants the timer" "test -L /$link && test -f /$link"
    in_image "the service runs isotope apps update --auto" "grep -qx 'ExecStart=/usr/bin/isotope apps update --auto' /$service"
    for m in $markers; do
        in_image "the service never runs with $m" "grep -qx 'ConditionPathExists=!$m' /$service"
    done
    in_image "flatpak is there" "test -x /usr/bin/flatpak"
    in_image "Isotope System is there" "test -x /usr/bin/isotope-system"
    in_image "the Isotope menu opens the Apps page" "grep -qF '\"Install apps\") system_page apps' /usr/bin/isotope-menu"
    in_image "the picks list is there" "grep -q '^browser|org.mozilla.firefox|' /usr/share/isotope/first-run-apps.list"
    finish
}

case "${1:-}" in
--repo) repo_mode ;;
"" | -h | --help) sed -n '4,16p' "$0"; exit 2 ;;
*) image_mode "$1" ;;
esac
