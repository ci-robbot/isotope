#!/usr/bin/env bash
# The checks are strings that the inner sh expands, on purpose.
# shellcheck disable=SC2016
# Translations: the gettext catalogs of pkg/i18n and the chain that brings
# the user's language to Isotope System, Isotope Welcome, the installer and
# the tray (see docs/superpowers/specs/2026-11-24-translations-design.md).
#
#   test/i18n/check-i18n.sh --repo   the tree: the catalogs are current and
#                                    complete (tools/i18n check), every
#                                    program picks the language, the
#                                    session gets LANG, the Go stages copy
#                                    pkg/i18n (CI)
#   test/i18n/check-i18n.sh IMAGE    a built image: each program carries the
#                                    Italian and Spanish catalogs, and the
#                                    profile script that exports LANG
#
# Seeing the translated windows on a booted desktop is on the design note's
# manual list.
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
    if [ "$fail" -eq 0 ]; then echo "all translation checks passed"; fi
    exit "$fail"
}

S=$(mktemp -d)
trap 'rm -rf "$S"' EXIT
stage() { # stage FILE NAME
    awk -v s="$2" '$0 ~ "^FROM .* AS "s"$" {p=1; print; next} p && /^FROM / {exit} p' "$1" >"$S/$2"
}

# ---------------------------------------------------------------------------
repo_mode() {
    local C=rootfs/common f out
    if out=$(go run ./tools/i18n check 2>&1); then
        pass "the catalogs are current and translate every message (go run ./tools/i18n check)"
    else
        bad "go run ./tools/i18n check"
        echo "$out" | head -40
    fi
    check "LINGUAS requires Italian and Spanish" sh -c "grep -qx it pkg/i18n/po/LINGUAS && grep -qx es pkg/i18n/po/LINGUAS"
    for f in cmd/isotope-system/main.go cmd/isotope-welcome/main.go cmd/isotope-install-gui/main.go; do
        check "$f picks the language from the session (i18n.Init)" grep -q 'i18n.Init()' "$f"
    done
    check "the text installer picks the language (i18n.Init)" sh -c "grep -l 'i18n.Init()' cmd/isotope-install-ui/*.go | grep -q ."
    check "only the tray picks a language in the isotope CLI" sh -c "
        [ \"\$(grep -l 'i18n.Init()' cmd/isotope/*.go | grep -v _test)\" = cmd/isotope/tray.go ]"
    check "the live installer window takes the live session's language" grep -q 'i18n.SetLanguage(' cmd/isotope-install-gui/window.go
    # The chain: Welcome's SetLocale and the installer write LANG to
    # /etc/locale.conf; the profile script exports it; sway and i3 push it
    # to the user manager and D-Bus activation; niri and COSMIC import the
    # login environment whole.
    printf 'LANG=it_IT.UTF-8\n' >"$S/locale.conf"
    sed "s|/etc/locale.conf|$S/locale.conf|g" "$C/etc/profile.d/isotope-locale.sh" >"$S/locale.sh"
    check "isotope-locale.sh exports LANG from /etc/locale.conf" sh -c "
        [ \"\$(env -i sh -c '. $S/locale.sh; echo \$LANG')\" = it_IT.UTF-8 ]"
    check "the installer seeds LANG for the first boot" grep -q 'LANG=de_DE.UTF-8' pkg/installer/testdata/locale.conf-de_DE.golden
    for f in rootfs/sway/usr/bin/start-desktop rootfs/i3/usr/bin/start-desktop; do
        check "$f sources isotope-locale.sh" grep -q 'isotope-locale.sh' "$f"
    done
    check "sway hands LANG to the user manager and D-Bus" grep -q 'dbus-update-activation-environment --systemd .*LANG' rootfs/sway/etc/sway/config.d/50-portals.conf
    check "i3 hands LANG to the user manager and D-Bus" grep -q 'dbus-update-activation-environment --systemd .*LANG' rootfs/i3/etc/i3/config.d/20-portals.conf
    for s in install-ui-build install-gui-build welcome-build update-build; do
        stage Dockerfile "$s"
        check "the $s stage copies pkg/i18n" grep -qx 'COPY pkg/i18n/ pkg/i18n/' "$S/$s"
    done
    check "the user guide says how to add a language" grep -q "go run ./tools/i18n update" site/guide/first-login.md
    finish
}

# ---------------------------------------------------------------------------
image_mode() {
    local img="$1" flavor b
    flavor=$(docker run --rm --entrypoint sh "$img" -c 'sed -n "s/^FLAVOR=//p" /etc/isotope/release' || true)
    in_image() { # in_image DESC CMD
        if docker run --rm --entrypoint sh "$img" -c "$2" >/dev/null 2>&1; then pass "$1"; else bad "$1"; fi
    }
    bins=/usr/bin/isotope
    if [ "$flavor" != agent ] && [ "$flavor" != server ]; then
        bins="$bins /usr/bin/isotope-system /usr/bin/isotope-welcome /usr/bin/isotope-install-gui"
    fi
    for b in $bins; do
        in_image "$b carries the Italian and Spanish catalogs" \
            "grep -q 'Language: it' $b && grep -q 'Language: es' $b && grep -q 'Plural-Forms: nplurals=2' $b"
    done
    in_image "isotope-locale.sh exports LANG from /etc/locale.conf" "grep -q 'export LANG=' /etc/profile.d/isotope-locale.sh"
    finish
}

case "${1:-}" in
--repo) repo_mode ;;
"" | -h | --help) sed -n '5,17p' "$0"; exit 2 ;;
*) image_mode "$1" ;;
esac
