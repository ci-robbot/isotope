#!/usr/bin/env bash
# The checks are strings that the inner sh expands, on purpose.
# shellcheck disable=SC2016
# Remote desktop: the remote-desktop extension (wayvnc) and the image's part,
# `isotope remote-desktop` and its user unit (see
# docs/superpowers/specs/2026-11-24-remote-desktop-design.md).
#
#   test/remote-desktop/check-remote-desktop.sh --repo   the tree: the unit
#                       (never enabled, the live markers, serve and stopped),
#                       the extension's pins and its test stage, the Network
#                       page's section, the Go stages, the agent's removal,
#                       the server left out (CI)
#   test/remote-desktop/check-remote-desktop.sh IMAGE    a built image: the
#                       unit, not enabled; `isotope remote-desktop`; none of
#                       the extension's files in the image; on the agent and
#                       the server, no unit
#
# A real viewer against Sway and niri is on the design note's manual list;
# the extension's own test stage checks the server a viewer meets.
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
    if [ "$fail" -eq 0 ]; then echo "all remote desktop checks passed"; fi
    exit "$fail"
}

unit=usr/lib/systemd/user/isotope-remote-desktop.service
markers='/run/cos/live_mode /run/cos/install_mode /run/cos/uki_install_mode /run/isotope/live-desktop'

S=$(mktemp -d)
trap 'rm -rf "$S"' EXIT
stage() { # stage FILE NAME
    awk -v s="$2" '$0 ~ "^FROM .* AS "s"$" {p=1; print; next} p && /^FROM / {exit} p' "$1" >"$S/$2"
}

# ---------------------------------------------------------------------------
repo_mode() {
    local C=rootfs/common m X=extensions/remote-desktop
    check "the unit runs isotope remote-desktop serve" grep -qx 'ExecStart=/usr/bin/isotope remote-desktop serve' "$C/$unit"
    check "the unit closes the port however the server ends" grep -qx 'ExecStopPost=/usr/bin/isotope remote-desktop stopped' "$C/$unit"
    check "the unit needs wayvnc from the extension" grep -qx 'ConditionPathExists=/usr/bin/wayvnc' "$C/$unit"
    check "the unit has no [Install] section: never enabled" sh -c "! grep -q '^\[Install\]' $C/$unit"
    check "nothing enables the unit" sh -c "! find rootfs -path '*.wants/*' -name 'isotope-remote-desktop*' | grep -q ."
    for m in $markers; do
        check "the unit never runs with $m" grep -qx "ConditionPathExists=!$m" "$C/$unit"
    done
    check "isotope remote-desktop is a command" grep -q 'case "remote-desktop":' cmd/isotope/main.go
    check "the tray closes a port left open at login" grep -q 'go rdCleanup(' cmd/isotope/tray.go
    check "the configuration always asks for a password, and a test holds it to that" sh -c "
        grep -qx 'enable_auth=true' cmd/isotope/remote_desktop.go &&
        grep -qF '[]string{\"relax_encryption\", \"allow_broken_crypto\", \"enable_pam\"}' cmd/isotope/remote_desktop_test.go"
    check "the firewall is changed through isotope-updated's allowed ports" sh -c "
        grep -q 'c.AllowPort(remotedesktop.Port' cmd/isotope/remote_desktop.go && grep -q 'c.DenyPort(remotedesktop.Port' cmd/isotope/remote_desktop.go"
    check "the Network page has the section" grep -q 'u.caps(i18n.T("REMOTE DESKTOP"))' cmd/isotope-system/remote.go
    check "Isotope System runs the CLI" grep -q 'hooks.RemoteDesktop = remoteCLI{bin: "isotope"}' cmd/isotope-system/main.go
    stage Dockerfile update-build
    stage Dockerfile system-build
    stage Dockerfile server-config
    check "the CLI's stage copies and tests pkg/remotedesktop" sh -c "
        grep -qx 'COPY pkg/remotedesktop/ pkg/remotedesktop/' $S/update-build && grep -q './pkg/remotedesktop/...' $S/update-build"
    check "Isotope System's stage copies pkg/remotedesktop" grep -qx 'COPY pkg/remotedesktop/ pkg/remotedesktop/' "$S/system-build"
    check "the server takes no user units" sh -c "! grep -q 'systemd/user' $S/server-config"
    check "the agent removes the unit and checks" sh -c "
        grep -q 'rm -f /usr/lib/systemd/user/isotope-remote-desktop.service' Dockerfile.agent &&
        grep -qF \"! find /usr/lib/systemd/user /etc/systemd/user -name 'isotope-remote-desktop*'\" Dockerfile.agent"
    # The extension.
    check "the extension has its menu entry" sh -c "jq -e '.title and .description' $X/extension.json"
    check "the extension's version is wayvnc's, pinned for Renovate" sh -c "
        grep -B1 '^ARG REMOTE_DESKTOP_VERSION=[0-9]' $X/Dockerfile | grep -q 'renovate: .*depName=any1/wayvnc'"
    check "neatvnc is built with TLS and Nettle" grep -q -- '-Dtls=enabled -Dnettle=enabled' "$X/Dockerfile"
    check "the extension ships only wayvnc and wayvncctl as programs" grep -qF '[ "$(ls /output/usr/bin | tr' "$X/Dockerfile"
    check "its test stage refuses unencrypted security types" grep -q "an unencrypted or broken type is offered" "$X/Dockerfile"
    check "its test stage logs in through VeNCrypt and refuses a wrong password" sh -c "
        grep -q 'a wrong password was not refused' $X/Dockerfile && grep -q 'the right user name and password were refused' $X/Dockerfile"
    finish
}

# ---------------------------------------------------------------------------
image_mode() {
    local img="$1" flavor m
    flavor=$(docker run --rm --entrypoint sh "$img" -c 'sed -n "s/^FLAVOR=//p" /etc/isotope/release' || true)
    in_image() { # in_image DESC CMD
        if docker run --rm --entrypoint sh "$img" -c "$2" >/dev/null 2>&1; then pass "$1"; else bad "$1"; fi
    }
    in_image "isotope remote-desktop is a command" "/usr/bin/isotope remote-desktop help | grep -q 'remote desktop password'"
    in_image "no file of the extension is in the image" \
        "! test -e /usr/bin/wayvnc && ! test -e /usr/bin/wayvncctl && ! test -e /usr/lib/isotope-ext/remote-desktop"
    if [ "$flavor" = agent ] || [ "$flavor" = server ]; then
        in_image "$flavor: no remote desktop unit" \
            "! find /usr/lib/systemd/user /etc/systemd/user -name 'isotope-remote-desktop*' 2>/dev/null | grep -q ."
        finish
    fi
    in_image "the unit is there" "test -f /$unit"
    in_image "the unit runs isotope remote-desktop serve" "grep -qx 'ExecStart=/usr/bin/isotope remote-desktop serve' /$unit"
    in_image "the unit is not enabled for anyone" \
        "! find /usr/lib/systemd/user /etc/systemd/user -path '*.wants/*' -name 'isotope-remote-desktop*' 2>/dev/null | grep -q ."
    for m in $markers; do
        in_image "the unit never runs with $m" "grep -qx 'ConditionPathExists=!$m' /$unit"
    done
    in_image "the firewall's port 5900 is closed on a new machine" \
        "! grep -qs 5900 /usr/share/isotope/firewall-default.nft"
    finish
}

case "${1:-}" in
--repo) repo_mode ;;
"" | -h | --help) sed -n '4,19p' "$0"; exit 2 ;;
*) image_mode "$1" ;;
esac
