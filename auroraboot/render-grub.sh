#!/usr/bin/env bash
# Render the live ISO's GRUB menu for one flavor to stdout.
#   render-grub.sh <FLAVOR>     FLAVOR: sway, i3, niri, cosmic, server or agent
# Environment: ARCH (default amd64), ISOTOPE_EXTRA_LIVE_CMDLINE (optional).
# build.sh and test/live/check-grub.sh both call this script.
#
# The desktop variants get live-grub-desktop.cfg.tmpl, which starts with a live
# desktop entry. The server and the agent keep live-grub.cfg.tmpl.
set -euo pipefail
cd "$(dirname "$0")"

FLAVOR="${1:?usage: render-grub.sh <FLAVOR>}"
ARCH="${ARCH:-amd64}"

case "$FLAVOR" in
  sway|i3|niri|cosmic) TEMPLATE=live-grub-desktop.cfg.tmpl ;;
  server|agent)        TEMPLATE=live-grub.cfg.tmpl ;;
  *) echo "render-grub.sh: unknown flavor '$FLAVOR'" >&2; exit 2 ;;
esac

# {{NOMODESET}} is " nomodeset" on x86/amd64 and empty elsewhere.
case "$ARCH" in
  amd64|x86_64) NOMODESET=" nomodeset" ;;
  *)            NOMODESET="" ;;
esac
EXTEND_CMDLINE="${ISOTOPE_EXTRA_LIVE_CMDLINE:+ ${ISOTOPE_EXTRA_LIVE_CMDLINE}}"

sed -e "s|{{NOMODESET}}|${NOMODESET}|g" \
    -e "s|{{EXTEND_CMDLINE}}|${EXTEND_CMDLINE}|g" \
    "$TEMPLATE"
