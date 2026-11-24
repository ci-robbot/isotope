#!/bin/sh
# Runs Isotope's polkit rule files against the case table in
# rules-harness.js: who may format a disk, mount one, manage users, restore,
# install firmware, use a smart card reader (the smartcards extension's rule).
# Uses node when it is on PATH, else the node:22-alpine container.
set -eu
repo=$(cd "$(dirname "$0")/../.." && pwd)
rules=rootfs/common/usr/share/polkit-1/rules.d
set -- test/polkit/rules-harness.js "$rules/50-isotope-udisks.rules" "$rules/50-isotope-update.rules" "$rules/50-isotope-fwupd.rules" \
    extensions/smartcards/files/usr/share/polkit-1/rules.d/50-isotope-pcscd.rules
if command -v node >/dev/null 2>&1; then
    cd "$repo"
    exec node "$@"
fi
exec docker run --rm -v "$repo:/src:ro" -w /src node:22-alpine node "$@"
