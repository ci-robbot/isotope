#!/usr/bin/env bash
# The checks are strings that the inner sh expands, on purpose.
# shellcheck disable=SC2016
# Smart cards and security keys: the `smartcards` extension
# (extensions/smartcards) and the image's part of it (see
# docs/superpowers/specs/2026-11-24-smartcards-design.md).
#
#   test/smartcards/check-smartcards.sh --repo   the tree: the extension's
#                                                GnuPG and build pins equal
#                                                the image's, the image's
#                                                gnupg stage keeps card
#                                                support and leaves scdaemon
#                                                out, the units, drop-ins,
#                                                polkit rule and p11-kit
#                                                module (CI)
#   test/smartcards/check-smartcards.sh IMAGE    a built image: none of the
#                                                extension's files, every
#                                                library it needs, the udev
#                                                rules that give FIDO keys to
#                                                the session's user; where
#                                                GnuPG is, card support in
#                                                gpg and gpg-agent (the
#                                                agent, which takes no
#                                                extensions, skips the
#                                                library checks)
#
# A real reader, token and FIDO key are on the design note's manual list.
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
    if [ "$fail" -eq 0 ]; then echo "all smart card checks passed"; fi
    exit "$fail"
}

ext=extensions/smartcards
files=$ext/files
units=usr/lib/systemd/system

# The value of `ARG NAME=` in a Dockerfile (the first one).
arg() { sed -n "s/^ARG $2=//p" "$1" | head -n 1; }

# ---------------------------------------------------------------------------
repo_mode() {
    local S n v
    S=$(mktemp -d)
    trap 'rm -rf "$S"' RETURN
    awk '$0 ~ "^FROM .* AS gnupg$" {p=1; print; next} p && /^FROM / {exit} p' Dockerfile >"$S/gnupg"

    # scdaemon must speak to the image's gpg-agent and link the same GnuPG
    # libraries; the build-only libraries follow the image's so one Renovate
    # PR moves both.
    for n in LIBGPGERROR_VERSION LIBGPGERROR_SHA256 LIBASSUAN_VERSION LIBGCRYPT_VERSION \
             LIBGCRYPT_SHA256 LIBKSBA_VERSION LIBKSBA_SHA256 NPTH_VERSION NPTH_SHA256 \
             GNUPG_VERSION GNUPG_SHA256 GLIB_VERSION PCRE2_VERSION LIBUCONTEXT_VERSION \
             POLKIT_VERSION LIBUSB_VERSION; do
        v=$(arg Dockerfile "$n")
        check "the extension's $n is the image's ($v)" test -n "$v" -a "$(arg "$ext/Dockerfile" "$n")" = "$v"
    done
    check "the extension's version is pcsc-lite's, under a renovate comment" sh -c "
        grep -B1 '^ARG SMARTCARDS_VERSION=' $ext/Dockerfile | grep -q '^# renovate: datasource=github-tags depName=LudovicRousseau/PCSC' &&
        grep -q 'pcsc-lite-\${SMARTCARDS_VERSION}.tar.xz' $ext/Dockerfile"
    check "every new tarball is checked against a SHA256" sh -c "
        grep -q 'PCSC_LITE_SHA256}  p.tar.xz' $ext/Dockerfile && grep -q 'CCID_SHA256}  c.tar.xz' $ext/Dockerfile &&
        grep -q 'OPENSC_SHA256}  o.tar.gz' $ext/Dockerfile && grep -q 'sha256sum -c -; tar' $ext/Dockerfile"
    check "the version pins are tracked" scripts/check-renovate-pins.sh

    check "the image's gpg and gpg-agent keep card support" sh -c "
        ! grep -q -- '--disable-card-support' $S/gnupg && ! grep -q -- '--disable-scdaemon' $S/gnupg"
    check "the image's GnuPG has no CCID driver of its own (readers go through pcscd)" grep -q -- '--disable-ccid-driver' "$S/gnupg"
    check "the image leaves scdaemon to the extension, and checks it" sh -c "
        grep -q ' /gnupg/usr/lib/gnupg/scdaemon$' $S/gnupg && grep -q 'test ! -e /gnupg/usr/lib/gnupg/scdaemon' $S/gnupg &&
        grep -q \"gpg --dump-options | grep -qx -- '--card-status'\" $S/gnupg"
    check "gpg-agent starts scdaemon where the extension puts it (/usr/lib, merged)" sh -c "
        grep -q -- '--libexecdir=/usr/lib/gnupg' $S/gnupg && grep -q -- '--libexecdir=/usr/lib/gnupg' $ext/Dockerfile &&
        grep -q 'install -Dm755 scd/scdaemon /output/usr/lib/gnupg/scdaemon' $ext/Dockerfile"
    check "scdaemon opens libpcsclite.so.1 and has no CCID driver" sh -c "
        grep -q 'DEFAULT_PCSC_DRIVER \"libpcsclite.so.1\"' $ext/Dockerfile && grep -q -- '--disable-ccid-driver' $ext/Dockerfile"
    check "the flatpak runtime check still wants no scdaemon in the image" grep -q 'test ! -e /usr/lib/gnupg/scdaemon' test/flatpak/check-runtime.sh

    check "pcscd is built with polkit, libsystemd and libudev" sh -c "
        grep -q -- '-Dpolkit=true' $ext/Dockerfile && grep -q -- '-Dlibsystemd=true' $ext/Dockerfile &&
        grep -q -- '-Dlibudev=true' $ext/Dockerfile && grep -q -- '-Dsystemdunit=system' $ext/Dockerfile"
    check "the socket is enabled from /usr" grep -q 'ln -s ../pcscd.socket $u/sockets.target.wants/pcscd.socket' "$ext/Dockerfile"
    check "pcscd runs as root (no pcscd user on Kairos)" grep -qx 'User=' "$files/$units/pcscd.service.d/10-isotope.conf"
    check "the socket belongs to root" sh -c "
        grep -qx 'SocketUser=' $files/$units/pcscd.socket.d/10-isotope.conf && grep -qx 'SocketGroup=' $files/$units/pcscd.socket.d/10-isotope.conf"
    check "the polkit rule answers both pcsc-lite actions" sh -c "
        grep -q 'org.debian.pcsc-lite.access_pcsc' $files/usr/share/polkit-1/rules.d/50-isotope-pcscd.rules &&
        grep -q 'org.debian.pcsc-lite.access_card' $files/usr/share/polkit-1/rules.d/50-isotope-pcscd.rules"
    check "the polkit harness runs the pcscd rule" grep -q '50-isotope-pcscd.rules' test/polkit/check-polkit-rules.sh
    check "the p11-kit module names OpenSC's module where the extension links it" sh -c "
        grep -qx 'module: /usr/lib/pkcs11/opensc-pkcs11.so' $files/usr/share/p11-kit/modules/opensc.module &&
        grep -q 'ln -s ../isotope-ext/smartcards/opensc-pkcs11.so /output/usr/lib/pkcs11/opensc-pkcs11.so' $ext/Dockerfile"
    check "isotope ext reloads polkit after a merge with polkit files" sh -c "
        grep -q '\"usr/share/polkit-1/rules.d/\"' cmd/isotope/ext.go && grep -q 'try-reload-or-restart\", \"polkit.service\"' cmd/isotope/ext.go"
    check "the catalog entry has a title and a description" sh -c "
        jq -e '.title != \"\" and .description != \"\"' $ext/extension.json"
    check "AGENTS.md has the feature row" grep -q 'isotope ext install smartcards' AGENTS.md
    check "CLAUDE.md is AGENTS.md" cmp -s AGENTS.md CLAUDE.md
    finish
}

# ---------------------------------------------------------------------------
image_mode() {
    local img="$1" flavor
    flavor=$(docker run --rm --entrypoint sh "$img" -c 'sed -n "s/^FLAVOR=//p" /etc/isotope/release' || true)
    in_image() { # in_image DESC CMD
        if docker run --rm --entrypoint sh "$img" -c "$2" >/dev/null 2>&1; then pass "$1"; else bad "$1"; fi
    }
    # `isotope ext` refuses an extension whose files the image already has.
    in_image "no file of the extension is in the image" '
        for f in /usr/bin/pcscd /usr/lib/libpcsclite.so.1 /usr/lib/libpcsclite_real.so.1 /usr/lib/pcsc \
                 /usr/lib/gnupg/scdaemon /usr/bin/gpg-card /usr/bin/opensc-tool /usr/bin/pkcs11-tool \
                 /usr/lib/pkcs11/opensc-pkcs11.so /usr/share/p11-kit/modules/opensc.module \
                 /usr/lib/isotope-ext/smartcards /usr/lib/systemd/system/pcscd.service \
                 /usr/lib/systemd/system/pcscd.socket /usr/share/polkit-1/actions/org.debian.pcsc-lite.policy \
                 /usr/share/polkit-1/rules.d/50-isotope-pcscd.rules /usr/share/licenses/smartcards-extension; do
            ! test -e "$f" || { echo "$f"; exit 1; }
        done'
    # The agent has no system extensions (Dockerfile.agent removes isotope-ext).
    if [ "$flavor" != agent ]; then
    in_image "the libraries the extension links are in the image" '
        for l in libglib-2.0.so.0 libgobject-2.0.so.0 libgio-2.0.so.0 libpolkit-gobject-1.so.0 \
                 libsystemd.so.0 libudev.so.1 libcrypto.so.3 libz.so.1; do
            test -e "/usr/lib/$l" || { echo "$l"; exit 1; }
        done'
    in_image "polkit runs here, so pcscd can ask it" "test -f /usr/lib/systemd/system/polkit.service"
    fi
    # FIDO2/U2F keys need no extension: systemd's fido_id tags the hidraw
    # device of a security key, and uaccess gives it to the active session.
    in_image "fido_id tags security keys" "test -x /usr/lib/udev/fido_id &&
        grep -q 'SUBSYSTEM==\"hidraw\", IMPORT{program}=\"fido_id\"' /usr/lib/udev/rules.d/60-fido-id.rules"
    in_image "a security key and a reader go to the active session's user" "
        grep -q 'ENV{ID_SECURITY_TOKEN}==\"?\\*\", TAG+=\"uaccess\"' /usr/lib/udev/rules.d/70-uaccess.rules &&
        grep -q 'ENV{ID_SMARTCARD_READER}==\"?\\*\", TAG+=\"uaccess\"' /usr/lib/udev/rules.d/70-uaccess.rules &&
        grep -q 'RUN{builtin}+=\"uaccess\"' /usr/lib/udev/rules.d/73-seat-late.rules"
    if [ "$flavor" = server ]; then
        in_image "the server has no GnuPG (scdaemon would wait for a gpg-agent)" "! command -v gpg"
        finish
    fi
    in_image "gpg has card support" "gpg --dump-options | grep -qx -- '--card-status'"
    # Without the extension the agent's SCD command answers "No SmartCard
    # daemon"; a gpg-agent built without card support answers "Not supported".
    in_image "gpg-agent passes card commands to scdaemon" '
        export GNUPGHOME=$(mktemp -d); chmod 700 "$GNUPGHOME"
        out=$(gpg-connect-agent "SCD GETINFO version" /bye 2>&1); echo "$out"
        gpgconf --kill gpg-agent >/dev/null 2>&1 || true
        ! echo "$out" | grep -qi "not supported" && echo "$out" | grep -q "No SmartCard daemon"'
    finish
}

case "${1:-}" in
--repo) repo_mode ;;
"" | -h | --help) sed -n '4,22p' "$0"; exit 2 ;;
*) image_mode "$1" ;;
esac
