#!/usr/bin/env bash
# The checks are strings that the inner sh expands, on purpose.
# shellcheck disable=SC2016
# Fingerprint sign-in: the fingerprint system extension (fprintd, libfprint)
# and the image's PAM lines that wait for it (see
# docs/superpowers/specs/2026-11-24-fingerprint-design.md).
#
#   test/fingerprint/check-fingerprint.sh --repo   the tree: the extension's
#                                                  files, every fingerprint
#                                                  PAM line starts with "-",
#                                                  which services get one,
#                                                  the login screens keep the
#                                                  password, the persisted
#                                                  prints, the agent's
#                                                  removals, the Users page
#                                                  (CI)
#   test/fingerprint/check-fingerprint.sh IMAGE    a built image: no file of
#                                                  the extension, and PAM run
#                                                  for real (test/fingerprint/
#                                                  pam-matrix.sh): every
#                                                  service works without the
#                                                  extension, with it and no
#                                                  reader, and a finger opens
#                                                  only sudo, polkit and the
#                                                  lock screens; nothing on
#                                                  the server and the agent
#
# A real reader is on the design note's manual list.
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
    if [ "$fail" -eq 0 ]; then echo "all fingerprint checks passed"; fi
    exit "$fail"
}

ext=extensions/fingerprint
fpline='-auth      sufficient   pam_fprintd.so  timeout=10'
aliasline='-auth      [success=done default=ignore]  pam_isotope_unix.so  nullok'
state=rootfs/common/system/oem/94_isotope_state.yaml

# One stage's lines, for the greps.
stage() { awk -v s="$1" '$0 ~ "^FROM .* AS "s"$" {p=1; print; next} p && /^FROM / {exit} p' "$2"; }

# ---------------------------------------------------------------------------
repo_mode() {
    # The extension.
    check "the extension has its Dockerfile and catalog entry" sh -c "test -f $ext/Dockerfile && jq -e '.title and .description' $ext/extension.json"
    check "the extension's version is fprintd's, tracked by Renovate" sh -c "
        grep -B1 '^ARG FINGERPRINT_VERSION=[0-9]' $ext/Dockerfile | grep -q 'renovate: .*depName=libfprint/fprintd'"
    check "libfprint is pinned and tracked by Renovate" sh -c "
        grep -B1 '^ARG LIBFPRINT_VERSION=[0-9]' $ext/Dockerfile | grep -q 'renovate: .*depName=libfprint/libfprint'"
    check "the extension builds on the image's Hadron and has default and test stages" sh -c "
        grep -qx 'ARG HADRON_VERSION' $ext/Dockerfile && grep -q '^FROM scratch AS default\$' $ext/Dockerfile &&
        grep -q '^FROM ghcr.io/kairos-io/hadron:\${HADRON_VERSION} AS test\$' $ext/Dockerfile"
    check "its libraries and daemon live in the private directory (RUNPATH), not /usr/libexec" sh -c "
        grep -q 'rpath,/usr/lib/isotope-ext/fingerprint' $ext/Dockerfile &&
        grep -q -- '--libexecdir=lib/isotope-ext/fingerprint' $ext/Dockerfile"
    check "pam_fprintd.so goes to /usr/lib/security, with the pam_unix alias next to it" sh -c "
        grep -q -- '-Dpam_modules_dir=/usr/lib/security' $ext/Dockerfile &&
        grep -q 'ln -s pam_unix.so /output/usr/lib/security/pam_isotope_unix.so' $ext/Dockerfile"
    check "the extension ships no /etc (a system extension merges /usr only)" grep -q 'rm -rf /output/etc ' "$ext/Dockerfile"
    check "the extension installs its licences" grep -q 'usr/share/licenses/fingerprint-extension' "$ext/Dockerfile"
    check "the extension's test stage starts fprintd on a system bus" grep -q 'fprintd answers on the system bus' "$ext/Dockerfile"

    # The PAM services in the tree.
    local f
    check "every fingerprint line in rootfs/ starts with \"-\"" sh -c "
        ! grep -rhE 'pam_fprintd|pam_isotope_unix' rootfs/*/etc/pam.d | grep -v '^-' | grep -qv '^#'"
    for f in rootfs/sway/etc/pam.d/swaylock rootfs/i3/etc/pam.d/i3lock; do
        check "$f: the typed password, then a finger, then the login stack" sh -c "
            [ \"\$(grep -v '^#' $f | grep -v '^\$' | sed -n 1,3p)\" = \"\$(printf '%s\n%s\n%s' '$aliasline' '$fpline' 'auth       include      login')\" ]"
    done
    f=rootfs/cosmic/etc/pam.d/cosmic-greeter
    check "$f (the lock screen): a finger first, then the login stack" sh -c "
        [ \"\$(grep -E '^-?auth' $f | sed -n 1,2p)\" = \"\$(printf '%s\n%s' '$fpline' 'auth       include      login')\" ] && grep -q '^account    include      login' $f"
    check "the COSMIC login screen is its own service, with no finger" sh -c "
        grep -qx 'service = \"cosmic-greeter-login\"' rootfs/cosmic/etc/greetd/cosmic-greeter.toml &&
        test -f rootfs/cosmic/etc/pam.d/cosmic-greeter-login && ! grep -q pam_fprintd rootfs/cosmic/etc/pam.d/cosmic-greeter-login &&
        grep -q 'pam_gnome_keyring.so auto_start' rootfs/cosmic/etc/pam.d/cosmic-greeter-login"
    check "no other PAM file in the tree names the fingerprint modules" sh -c "
        [ \"\$(grep -rlE 'pam_fprintd|pam_isotope_unix' rootfs/*/etc/pam.d | sort | tr '\n' ' ')\" = 'rootfs/cosmic/etc/pam.d/cosmic-greeter rootfs/i3/etc/pam.d/i3lock rootfs/sway/etc/pam.d/swaylock ' ]"
    stage desktop-default Dockerfile >/tmp/fp-desktop-default.$$
    stage server-default Dockerfile >/tmp/fp-server-default.$$
    check "desktop-default puts the line first in sudo and polkit-1" sh -c "
        grep -qF \"line='$fpline'\" /tmp/fp-desktop-default.$$ && grep -q 'for svc in sudo polkit-1; do' /tmp/fp-desktop-default.$$"
    check "the server's PAM services stay as Hadron's (no fingerprint line)" sh -c "! grep -q pam_fprintd /tmp/fp-server-default.$$"
    rm -f /tmp/fp-desktop-default.$$ /tmp/fp-server-default.$$
    check "the live stage's lock bypass still finds the lock services" sh -c "
        grep -q 'for svc in cosmic-greeter swaylock i3lock; do' rootfs/common/usr/libexec/isotope-live-boot"

    # The prints, the agent, the page, the wiring.
    check "/var/lib/fprint is kept across reboots" sh -c "awk '/for p in/,/; do/' $state | grep -q /var/lib/fprint"
    check "test-persist-state.sh expects /var/lib/fprint" grep -qE '/var/lib/fprint[ "]' test/state/test-persist-state.sh
    check "the agent removes every fingerprint line and checks none is left" sh -c "
        grep -qF '/pam_fprintd\\.so/d' Dockerfile.agent && grep -q 'grep -rqs -e pam_fprintd -e pam_isotope_unix /etc/pam.d /usr/lib/pam.d' Dockerfile.agent"
    check "Isotope System's Users page has the fingerprint section" sh -c "
        grep -q 'FINGERPRINTS' cmd/isotope-system/page_users.go && grep -q 'Install the fingerprint extension' cmd/isotope-system/fprint_section.go"
    check "the image checks run this script" sh -c "
        grep -q 'test/fingerprint/check-fingerprint.sh' .github/workflows/desktop-images.yml &&
        grep -q 'test/fingerprint/check-fingerprint.sh' .github/workflows/release.yml"
    check "pam-matrix.sh parses" sh -n test/fingerprint/pam-matrix.sh
    finish
}

# ---------------------------------------------------------------------------
image_mode() {
    local img="$1" flavor tmp hadron
    flavor=$(docker run --rm --entrypoint sh "$img" -c 'sed -n "s/^FLAVOR=//p" /etc/isotope/release' || true)
    in_image() { # in_image DESC SCRIPT
        if docker run --rm --entrypoint sh "$img" -c "$2" >/dev/null 2>&1; then pass "$1"; else bad "$1"; fi
    }
    in_image "the image has no file of the fingerprint extension" \
        '! test -e /usr/lib/security/pam_fprintd.so && ! test -e /usr/lib/security/pam_isotope_unix.so && ! test -e /usr/lib/isotope-ext/fingerprint && ! test -e /usr/share/dbus-1/system-services/net.reactivated.Fprint.service'
    in_image "pam_unix.so, which the extension's alias points to, is in /usr/lib/security" 'test -f /usr/lib/security/pam_unix.so'
    case "$flavor" in
    server | agent)
        in_image "$flavor: no PAM service names the fingerprint modules" '! grep -rqs -e pam_fprintd -e pam_isotope_unix /etc/pam.d /usr/lib/pam.d' ;;
    *)
        in_image "sudo's first auth line is the fingerprint line" "test \"\$(grep -m1 -E '^-?auth' /etc/pam.d/sudo)\" = '$fpline'"
        in_image "polkit-1's first auth line is the fingerprint line" "f=/etc/pam.d/polkit-1; [ -f \$f ] || f=/usr/lib/pam.d/polkit-1; test \"\$(grep -m1 -E '^-?auth' \$f)\" = '$fpline'"
        in_image "/var/lib/fprint is kept across reboots" "awk '/for p in/,/; do/' /system/oem/94_isotope_state.yaml | grep -q /var/lib/fprint" ;;
    esac

    # PAM for real: pam-check and the stand-in, built with the image's
    # toolchain (Dockerfile.pam), then pam-matrix.sh as root in the image.
    tmp="$(mktemp -d)"
    trap 'rm -rf "$tmp"' RETURN
    chmod 755 "$tmp"
    hadron="$(awk -F= '/^ARG HADRON_VERSION=/{print $2; exit}' Dockerfile)"
    if ! docker build -q --build-arg HADRON_VERSION="$hadron" -f test/fingerprint/Dockerfile.pam \
            --output "type=local,dest=$tmp" test/fingerprint >/dev/null; then
        bad "could not build pam-check"; finish
    fi
    cp test/fingerprint/pam-matrix.sh "$tmp/"
    chmod 755 "$tmp"/*
    if docker run --rm --user 0 --entrypoint sh -v "$tmp:/probe:ro" "$img" /probe/pam-matrix.sh "${flavor:-unknown}"; then
        pass "PAM: every service works with and without the extension ($flavor)"
    else
        bad "PAM: see the lines above ($flavor)"
    fi
    finish
}

case "${1:-}" in
--repo) repo_mode ;;
"" | -h | --help) sed -n '2,/^set -euo/p' "$0" | sed '$d'; exit 2 ;;
*) image_mode "$1" ;;
esac
