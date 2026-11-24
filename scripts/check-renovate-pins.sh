#!/bin/sh
# Every `ARG <NAME>_VERSION=` and `ARG <NAME>_IMAGE=` in the Dockerfiles must
# sit directly under a `# renovate:` comment, so Renovate (renovate.json) keeps
# it up to date, or be listed below with the reason it is updated by hand.
set -eu

cd "$(dirname "$0")/.."

# Updated by hand:
#   HADRON: follow-hadron.yml moves it (scripts/bump-hadron.sh).
#   FREETYPE, the GnuPG stack, SQLITE, LINUX_FIRMWARE, SOF_BIN, INTEL_UCODE,
#     SLANG, NEWT: the tarball is checked against a pinned SHA256, which
#     Renovate cannot update.
#   ZIG: ly needs one exact Zig release.
#   DEJAVU, LIBDBUSMENU, STARTUP_NOTIFICATION, ATK, LIBEV: no new releases,
#     or no tags Renovate can read.
#   LIBNL, WPA, HOSTAPD, WIRELESS_REGDB: tags do not spell the version
#     (libnl3_11_0, hostap_2_11, master-2024-10-07).
#   GPERF, NCURSES, GETTEXT, SBC, GMP, MPFR: tarball-only releases.
#   UNICODE_EMOJI: unicode.org files, no tags Renovate can read.
#   LIBDAEMON: a 2012 tarball with no repository tags; its SHA256 is pinned.
#   GHOSTSCRIPT: the release tags spell the version without dots (gs10080),
#     and the tarball is checked against a pinned SHA256.
unmanaged="HADRON FREETYPE LIBGPGERROR LIBASSUAN GPGME LIBGCRYPT LIBKSBA NPTH
SQLITE NTBTLS GNUPG ZIG DEJAVU LIBDBUSMENU STARTUP_NOTIFICATION ATK LIBEV LIBNL
WPA HOSTAPD WIRELESS_REGDB GPERF NCURSES GETTEXT SBC GMP MPFR LINUX_FIRMWARE SOF_BIN
INTEL_UCODE UNICODE_EMOJI SLANG NEWT LIBDAEMON GHOSTSCRIPT"
# Images updated by hand (the _IMAGE suffix dropped):
#   BASE: Hadron's image, moved with HADRON (Dockerfile), or the local i3
#     image the agent overlays (Dockerfile.agent).
#   KAIROS_INIT: a branch tag (master), not a version.
unmanaged_images="BASE KAIROS_INIT"

missing=$(awk -v unmanaged="$unmanaged" -v unmanaged_images="$unmanaged_images" '
  BEGIN {
    n = split(unmanaged, u, /[ \n]+/); for (i = 1; i <= n; i++) skip[u[i]] = 1
    n = split(unmanaged_images, u, /[ \n]+/); for (i = 1; i <= n; i++) skipimg[u[i]] = 1
  }
  /^ARG [A-Z0-9_]+_VERSION=/ {
    name = $2; sub(/_VERSION=.*/, "", name)
    if (!(name in skip) && prev !~ /^# renovate: /) print FILENAME ":" FNR ": " $2
  }
  /^ARG [A-Z0-9_]+_IMAGE=/ {
    name = $2; sub(/_IMAGE=.*/, "", name)
    if (!(name in skipimg) && prev !~ /^# renovate: datasource=docker /) print FILENAME ":" FNR ": " $2
  }
  { prev = $0 }
' Dockerfile Dockerfile.agent extensions/*/Dockerfile)

if [ -n "$missing" ]; then
  echo "Version pins without a '# renovate:' comment (see renovate.json):" >&2
  echo "$missing" >&2
  exit 1
fi
echo "All version pins are tracked by Renovate or listed as updated by hand."
