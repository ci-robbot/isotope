#!/bin/sh
# Runs the rootfs stage of rootfs/system/oem/94_isotope_state.yaml against a
# scratch cos-layout.env: every path is added once to CUSTOM_BIND_MOUNTS,
# what 92_docker.yaml set there is kept, and a second run changes nothing.
#
#   test/state/test-persist-state.sh
set -eu
cd "$(dirname "$0")/../.."
yaml=rootfs/system/oem/94_isotope_state.yaml
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

# The command block: the lines after "- |", without their indentation, with
# the layout file pointed at the scratch copy.
awk '/^        - \|$/ { on = 1; next } on && NF && !/^          / { exit } on { sub(/^          /, ""); print }' "$yaml" |
    sed "s#/run/cos/cos-layout.env#$tmp/layout.env#" > "$tmp/stage.sh"
grep -q CUSTOM_BIND_MOUNTS "$tmp/stage.sh" || { echo "FAIL: no command block in $yaml"; exit 1; }

printf 'OVERLAY="tmpfs:25%%"\nCUSTOM_BIND_MOUNTS="/var/lib/docker"\n' > "$tmp/layout.env"
sh "$tmp/stage.sh"
sh "$tmp/stage.sh"

want="/var/lib/docker /etc/NetworkManager/system-connections /var/lib/NetworkManager /var/lib/bluetooth /var/lib/AccountsService /var/lib/power-profiles-daemon /var/lib/systemd/backlight /var/lib/systemd/rfkill"
. "$tmp/layout.env"
fail=0
[ "$CUSTOM_BIND_MOUNTS" = "$want" ] || { echo "FAIL: CUSTOM_BIND_MOUNTS=\"$CUSTOM_BIND_MOUNTS\""; echo "      want \"$want\""; fail=1; }
[ "$(grep -c '^CUSTOM_BIND_MOUNTS=' "$tmp/layout.env")" = 1 ] || { echo "FAIL: CUSTOM_BIND_MOUNTS written more than once"; fail=1; }
grep -q '^OVERLAY=' "$tmp/layout.env" || { echo "FAIL: other layout settings lost"; fail=1; }

# Without 92_docker.yaml's value (no earlier line), the list has no leading space.
printf '' > "$tmp/layout.env"
sh "$tmp/stage.sh"
. "$tmp/layout.env"
[ "$CUSTOM_BIND_MOUNTS" = "${want#/var/lib/docker }" ] || { echo "FAIL: from empty: \"$CUSTOM_BIND_MOUNTS\""; fail=1; }

[ "$fail" = 0 ] && echo "PASS: 94_isotope_state.yaml adds every path once"
exit "$fail"
