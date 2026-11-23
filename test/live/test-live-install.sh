#!/usr/bin/env bash
# Test rootfs/usr/bin/isotope-live-install and
# rootfs/usr/libexec/isotope-live-install-run in an alpine container
# (busybox sh), with stubs for sudo, flock, swaymsg, i3-msg, niri and
# notify-send that log their argv.
#
#   test/live/test-live-install.sh
# The stubs are single-quoted on purpose.
# shellcheck disable=SC2016
set -euo pipefail
cd "$(dirname "$0")/../.."

if [ "${1:-}" != "--inner" ]; then
    exec docker run --rm -v "$PWD:/repo:ro" alpine:3 sh /repo/test/live/test-live-install.sh --inner
fi

# ---- inside the container (busybox sh) ----
launcher=/repo/rootfs/usr/bin/isotope-live-install
helper=/repo/rootfs/usr/libexec/isotope-live-install-run
fail=0
pass() { echo "PASS: $*"; }
bad()  { echo "FAIL: $*"; fail=1; }
eq() { if [ "$2" = "$3" ]; then pass "$1"; else bad "$1 (got '$3', want '$2')"; fi; }

d=$(mktemp -d)
bin="$d/bin"; log="$d/log"; mkdir -p "$bin"
rt="/run/user/$(id -u)"; mkdir -p "$rt"
marker="$d/live-desktop"; installed="$d/installed"
stub() { printf '#!/bin/sh\n%s\n' "$2" >"$bin/$1"; chmod +x "$bin/$1"; }
stub sudo 'echo "sudo $*" >>"$LOG"
# Log the preserved variables the way sudo would pass them on.
for a in "$@"; do case $a in --preserve-env=*) list=${a#--preserve-env=}; for v in $(echo "$list" | tr , " "); do eval "echo \"env $v=\${$v-<unset>}\" >>\"\$LOG\""; done ;; esac; done
exit "${SUDO_RC:-0}"'
# The lock: held when FLOCK_HELD=1 (flock -n fails), else taken.
stub flock 'echo "flock $*" >>"$LOG"; [ "${FLOCK_HELD:-0}" = 1 ] && exit 1; exit "${FLOCK_RC:-0}"'
stub swaymsg 'echo "swaymsg $*" >>"$LOG"'
stub i3-msg 'echo "i3-msg $*" >>"$LOG"'
stub notify-send 'echo "notify-send $*" >>"$LOG"'
stub niri 'echo "niri $*" >>"$LOG"
case "$*" in *--json*) echo "[{\"id\":3,\"title\":\"x\",\"app_id\":\"foot\",\"workspace_id\":1},{\"id\":12,\"title\":\"Install Isotope\",\"app_id\":\"isotope-install\",\"workspace_id\":2}]" ;; esac'

run() { # run ENV... : run the launcher with a clean environment plus ENV
    : >"$log"
    env -i PATH="$bin:/usr/bin:/bin" LOG="$log" ISOTOPE_LIVE_MARKER="$marker" ISOTOPE_LIVE_INSTALLED="$installed" \
        XDG_RUNTIME_DIR="$rt" "$@" sh "$launcher" >"$d/out" 2>&1 && rc=0 || rc=$?
}
has() { if grep -qxF -- "$2" "$log"; then pass "$1"; else bad "$1 (log: $(tr '\n' '|' <"$log"))"; fi; }
hasnt() { if grep -q -- "$2" "$log"; then bad "$1 (log: $(tr '\n' '|' <"$log"))"; else pass "$1"; fi; }

# not-live
rm -f "$marker" "$installed"
run WAYLAND_DISPLAY=wayland-1 XDG_CURRENT_DESKTOP=sway
eq "not-live: exit 1" 1 "$rc"
has "not-live: notifies" "notify-send -a Install Isotope The installer runs only on the live system."
hasnt "not-live: no sudo" "^sudo"

: >"$marker"
# first launch, Wayland
run WAYLAND_DISPLAY=wayland-1 XDG_CURRENT_DESKTOP=sway LANG=de_DE.UTF-8
eq "first-launch: exit 0" 0 "$rc"
has "first-launch: sudo argv" "sudo -n --preserve-env=WAYLAND_DISPLAY,XDG_RUNTIME_DIR,XDG_CURRENT_DESKTOP,LANG /usr/libexec/isotope-live-install-run --choices $rt/isotope/live-choices.json"
has "first-launch: WAYLAND_DISPLAY kept" "env WAYLAND_DISPLAY=wayland-1"
has "first-launch: XDG_RUNTIME_DIR kept" "env XDG_RUNTIME_DIR=$rt"
has "first-launch: XDG_CURRENT_DESKTOP kept" "env XDG_CURRENT_DESKTOP=sway"
hasnt "first-launch: no DISPLAY passed" "DISPLAY=:"
has "first-launch: the lock is taken without waiting" "flock -n 9"

# first launch, X11 (i3): DISPLAY and XAUTHORITY
run DISPLAY=:0 XAUTHORITY="$rt/xauth" XDG_CURRENT_DESKTOP=i3
has "x11: sudo argv" "sudo -n --preserve-env=DISPLAY,XAUTHORITY,XDG_RUNTIME_DIR,XDG_CURRENT_DESKTOP /usr/libexec/isotope-live-install-run --choices $rt/isotope/live-choices.json"
has "x11: DISPLAY kept" "env DISPLAY=:0"

# values that are refused are not passed
run WAYLAND_DISPLAY=wayland-1 'XDG_CURRENT_DESKTOP=sw
ay'
hasnt "newline in XDG_CURRENT_DESKTOP is dropped" "XDG_CURRENT_DESKTOP"
run DISPLAY=':0;id' XDG_CURRENT_DESKTOP=i3
hasnt "bad DISPLAY is dropped" "DISPLAY"
run WAYLAND_DISPLAY=wayland-1 XDG_RUNTIME_DIR=/tmp/evil
eq "a runtime dir outside /run/user/UID: exit 1" 1 "$rc"
hasnt "a runtime dir outside /run/user/UID: no sudo" "^sudo"
run DISPLAY=:0 XAUTHORITY=/etc/shadow XDG_CURRENT_DESKTOP=i3
hasnt "XAUTHORITY outside the user's dirs is dropped" "XAUTHORITY"

# second launch: raise, no sudo
run WAYLAND_DISPLAY=wayland-1 XDG_CURRENT_DESKTOP=sway FLOCK_HELD=1
eq "second-launch: exit 0" 0 "$rc"
has "second-launch: sway focus" 'swaymsg [app_id="isotope-install"] focus'
hasnt "second-launch: no sudo" "^sudo"
run DISPLAY=:0 XDG_CURRENT_DESKTOP=i3 FLOCK_HELD=1
has "second-launch: i3 focus" 'i3-msg [class="isotope-install"] focus'
run WAYLAND_DISPLAY=wayland-1 XDG_CURRENT_DESKTOP=niri FLOCK_HELD=1
has "second-launch: niri lists windows" "niri msg --json windows"
has "second-launch: niri focuses by id" "niri msg action focus-window --id 12"
hasnt "second-launch: niri: no sudo" "^sudo"

# COSMIC second launch: a notification
run WAYLAND_DISPLAY=wayland-1 XDG_CURRENT_DESKTOP=COSMIC FLOCK_HELD=1
eq "cosmic-second-launch: exit 0" 0 "$rc"
has "cosmic-second-launch: notifies" "notify-send -a Install Isotope The installer is already open."
hasnt "cosmic-second-launch: no sudo" "^sudo"

# after the install
: >"$installed"
run WAYLAND_DISPLAY=wayland-1 XDG_CURRENT_DESKTOP=sway
eq "installed: exit 0" 0 "$rc"
has "installed: says so" "notify-send -a Install Isotope Isotope is installed. Restart to use it."
hasnt "installed: no sudo" "^sudo"
rm -f "$installed"

# the installer's exit status is the launcher's, and a failure is shown
run WAYLAND_DISPLAY=wayland-1 XDG_CURRENT_DESKTOP=sway SUDO_RC=3
eq "the installer's exit status is passed on" 3 "$rc"
has "a failed sudo or installer is shown" "notify-send -a Install Isotope The installer stopped (status 3). Open it again to retry."
run WAYLAND_DISPLAY=wayland-1 XDG_CURRENT_DESKTOP=sway
hasnt "a clean exit shows nothing" "notify-send"

# flock: only exit 1 means "already open"
run WAYLAND_DISPLAY=wayland-1 XDG_CURRENT_DESKTOP=COSMIC FLOCK_RC=127
eq "flock missing: exit 1" 1 "$rc"
hasnt "flock missing: not reported as already open" "already open"
has "flock missing: reported as an error" "notify-send -a Install Isotope The installer could not start (flock failed with status 127)."
hasnt "flock missing: no sudo" "^sudo"

# XAUTHORITY with ..
run DISPLAY=:0 XAUTHORITY="$rt/../../etc/shadow" XDG_CURRENT_DESKTOP=i3
hasnt "XAUTHORITY with .. is dropped" "XAUTHORITY"

# ---- the helper (as root: marker, binary choice) ----
envrun() { # envrun ENV... : run the helper with a clean environment plus ENV
    : >"$log"
    env -i PATH="$bin:/usr/bin:/bin" LOG="$log" ISOTOPE_LIVE_MARKER="$marker" ISOTOPE_LIVE_BIN="$bin" "$@" \
        sh "$helper" >"$d/out" 2>&1 && rc=0 || rc=$?
}
stub isotope-install-gui 'echo "wayland $*" >>"$LOG"'
rm -f "$marker"
envrun WAYLAND_DISPLAY=wayland-1
eq "helper: no marker refuses" 1 "$rc"
hasnt "helper: no marker starts nothing" "wayland"
: >"$marker"
# HENV="VAR=value ..." hrun ARGS...: the helper with a clean environment
# plus HENV, then ARGS.
hrun() {
    : >"$log"
    # shellcheck disable=SC2086 # HENV is split on purpose.
    env -i PATH="$bin:/usr/bin:/bin" LOG="$log" ISOTOPE_LIVE_MARKER="$marker" ISOTOPE_LIVE_BIN="$bin" $HENV \
        sh "$helper" "$@" >"$d/out" 2>&1 && rc=0 || rc=$?
}
HENV="WAYLAND_DISPLAY=wayland-1" hrun --choices "$rt/isotope/live-choices.json"
has "helper: wayland runs the window build" "wayland --window --choices $rt/isotope/live-choices.json"
stub isotope-install-gui-x11 'echo "x11 $*" >>"$LOG"'
HENV="DISPLAY=:0" hrun --choices "$rt/isotope/live-choices.json"
has "helper: X11 only runs the X11 build" "x11 --window --choices $rt/isotope/live-choices.json"
HENV="DISPLAY=:0 WAYLAND_DISPLAY=wayland-1" hrun --choices "$rt/isotope/live-choices.json"
has "helper: XWayland session (both set) runs the Wayland build" "wayland --window --choices $rt/isotope/live-choices.json"
HENV="WAYLAND_DISPLAY=wayland-1" hrun --choices /etc/passwd
eq "helper: a choices file outside a runtime dir is refused" 2 "$rc"
HENV="WAYLAND_DISPLAY=wayland-1" hrun --nonsense
eq "helper: an unknown argument is refused" 2 "$rc"

if [ "$fail" = 0 ]; then echo "all isotope-live-install checks passed"; else echo "isotope-live-install checks FAILED"; exit 1; fi
