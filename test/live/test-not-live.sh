#!/usr/bin/env bash
# Test rootfs/usr/bin/isotope-not-live in an alpine container (busybox sh).
#
#   test/live/test-not-live.sh
set -euo pipefail
cd "$(dirname "$0")/../.."

if [ "${1:-}" != "--inner" ]; then
    exec docker run --rm -v "$PWD:/repo:ro" alpine:3 sh /repo/test/live/test-not-live.sh --inner
fi

# ---- inside the container (busybox sh) ----
script=/repo/rootfs/usr/bin/isotope-not-live
fail=0
pass() { echo "PASS: $*"; }
bad()  { echo "FAIL: $*"; fail=1; }
eq() { if [ "$2" = "$3" ]; then pass "$1"; else bad "$1 (got '$3', want '$2')"; fi; }

d=$(mktemp -d)
marker="$d/live-desktop"

out=$(ISOTOPE_LIVE_MARKER="$marker" sh "$script" echo ran 2>&1) && rc=0 || rc=$?
eq "no marker: runs the command" "ran" "$out"
eq "no marker: exit status 0" 0 "$rc"

ISOTOPE_LIVE_MARKER="$marker" sh "$script" sh -c 'exit 7' && rc=0 || rc=$?
eq "no marker: the command's exit status is kept" 7 "$rc"

out=$(ISOTOPE_LIVE_MARKER="$marker" sh "$script" printf '%s|%s' 'a b' c)
eq "no marker: arguments keep their spaces" "a b|c" "$out"

: >"$marker"
out=$(ISOTOPE_LIVE_MARKER="$marker" sh "$script" echo ran 2>&1) && rc=0 || rc=$?
eq "marker: runs nothing and prints nothing" "" "$out"
eq "marker: exit status 0" 0 "$rc"

ISOTOPE_LIVE_MARKER="$marker" sh "$script" sh -c "touch $d/side-effect" && rc=0 || rc=$?
eq "marker: the command did not run" "no" "$([ -e "$d/side-effect" ] && echo yes || echo no)"

ISOTOPE_LIVE_MARKER="$marker" sh "$script" false && rc=0 || rc=$?
eq "marker: exit 0 even for a command that would fail" 0 "$rc"

# The default marker path, when the override is not set.
if grep -q '/run/isotope/live-desktop' "$script"; then pass "default marker is /run/isotope/live-desktop"; else bad "default marker is /run/isotope/live-desktop"; fi

if [ "$fail" = 0 ]; then echo "all isotope-not-live checks passed"; else echo "isotope-not-live checks FAILED"; exit 1; fi
