#!/bin/sh
# The fakes are scripts written out in single quotes, on purpose.
# shellcheck disable=SC2016
# Tests for how the Isotope menu reaches Isotope System, and its wiring.
#
#   test/system/check-system-menu.sh
#
# Runs itself in an alpine container (it writes /etc/isotope/session), with
# fakes for isotope, isotope-system, isotope-pick and the terminal. It
# checks:
#   1. with isotope-system installed, Update, Extensions, Backup, Recovery
#      key and System open `isotope system PAGE`, and the menu lists
#      "System";
#   2. without it (the agent image), Update still opens the terminal flow
#      and "System" is not listed;
#   3. the wiring: the Dockerfile builds and copies it for every variant,
#      the agent removes it, sway, i3 and niri float it, the desktop file.
set -u

repo="$(cd "$(dirname "$0")/../.." && pwd)"

if [ "${1:-}" != --inside ]; then
    exec docker run --rm -v "$repo:/src:ro" alpine:3.22 sh /src/test/system/check-system-menu.sh --inside
fi

fail=0
ok()  { echo "ok: $*"; }
err() { echo "FAIL: $*"; fail=1; }
expect() {
    if [ "$2" = "$3" ]; then ok "$1"; else err "$1: got [$2], want [$3]"; fi
}

T=$(mktemp -d)
B="$T/bin"
mkdir -p "$B" /etc/isotope
export T
PATH="$B:$PATH"
printf 'DESKTOP_NAME=Sway\n' > /etc/isotope/session

fake() { printf '#!/bin/sh\n%s\n' "$2" > "$B/$1"; chmod +x "$B/$1"; }
fake isotope 'echo "isotope $*" > "$T/ran"'
fake isotope-system 'exit 0'
fake isotope-recovery 'exit 0'
fake restic 'exit 0'
# isotope-pick: records its input, picks line $T/choice or cancels.
fake isotope-pick 'cat > "$T/pick.in"
[ -f "$T/choice" ] || exit 1
sed -n "$(cat "$T/choice")p" "$T/pick.in"'
# foot --app-id=floatterm sh -c CMD: records CMD.
fake foot 'shift 3; printf "%s\n" "$1" > "$T/term"'
touch /etc/isotope/encryption

entry() {
    rm -f "$T/ran" "$T/term"
    "$repo/rootfs/usr/bin/isotope-menu" "$1"
    i=0; while [ ! -s "$T/ran" ] && [ ! -s "$T/term" ] && [ $i -lt 20 ]; do sleep 0.05; i=$((i + 1)); done
    cat "$T/ran" 2>/dev/null
}

expect "Update opens the Updates page" "$(entry Update)" "isotope system updates"
expect "Extensions opens its page" "$(entry Extensions)" "isotope system extensions"
expect "Backup opens its page" "$(entry Backup)" "isotope system backup"
expect "Recovery key opens the Security page" "$(entry 'Recovery key')" "isotope system security"
expect "System opens the app" "$(entry System)" "isotope system updates"
# Bluetooth: Isotope System's page on sway, i3 and COSMIC; niri keeps the
# Noctalia panel as its quick path.
fake isotope-bluetooth-menu 'echo "bluetooth-menu" > "$T/ran"'
fake noctalia 'echo "noctalia $*" > "$T/ran"'
expect "Bluetooth opens its page on Sway" "$(entry Bluetooth)" "isotope system bluetooth"
printf 'DESKTOP_NAME=COSMIC\n' > /etc/isotope/session
expect "Bluetooth opens its page on COSMIC" "$(entry Bluetooth)" "isotope system bluetooth"
printf 'DESKTOP_NAME=niri\n' > /etc/isotope/session
expect "Bluetooth keeps the Noctalia panel on niri" "$(entry Bluetooth)" "noctalia msg panel-open control-center bluetooth"
printf 'DESKTOP_NAME=Sway\n' > /etc/isotope/session
# The quick menu's Settings item opens the page (the agent image has none).
fake bluetoothctl 'case "$1" in show) printf "Controller 00:11:22:33:44:55 (public)\n\tPowered: yes\n" ;; esac'
fake isotope-pick 'cat > "$T/pick.in"
grep -x "  Settings…" "$T/pick.in" | head -n 1'
rm -f "$T/ran"
"$repo/rootfs/usr/bin/isotope-bluetooth-menu" >/dev/null 2>&1
expect "the Bluetooth menu has Settings…" "$(cat "$T/ran" 2>/dev/null)" "isotope system bluetooth"
fake isotope-pick 'cat > "$T/pick.in"
[ -f "$T/choice" ] || exit 1
sed -n "$(cat "$T/choice")p" "$T/pick.in"'
rm -f "$T/choice"
"$repo/rootfs/usr/bin/isotope-menu" >/dev/null 2>&1
expect "the menu lists System" "$(cut -f1 "$T/pick.in" | grep -c '^System$')" 1

# Without the app: the terminal flows, and no System entry.
rm "$B/isotope-system"
echo 1 > "$T/choice"
rm -f "$T/ran" "$T/term"
"$repo/rootfs/usr/bin/isotope-menu" Update
i=0; while [ ! -s "$T/term" ] && [ $i -lt 20 ]; do sleep 0.05; i=$((i + 1)); done
expect "without the app, Update checks in a terminal" "$(sed 's/;.*//' "$T/term" 2>/dev/null)" "isotope status"
rm -f "$T/choice"
"$repo/rootfs/usr/bin/isotope-menu" >/dev/null 2>&1
expect "without the app, no System entry" "$(cut -f1 "$T/pick.in" | grep -c '^System$')" 0

# --- wiring -----------------------------------------------------------------
D=/src/Dockerfile
for v in sway cosmic niri; do
    expect "$v gets the Wayland build" "$(grep -A1 "^FROM scratch AS system-$v\$" $D | grep -c '/out/wayland/isotope-system /usr/bin/isotope-system')" 1
done
expect "i3 gets the X11 build" "$(grep -A1 '^FROM scratch AS system-i3$' $D | grep -c '/out/x11/isotope-system /usr/bin/isotope-system')" 1
expect "the desktop stage copies the variant's build" "$(grep -c '^COPY --from=system / /$' $D)" 1
expect "the agent removes the app" "$(grep -c 'rm -f /usr/bin/isotope-system' /src/Dockerfile.agent)" 1
expect "sway floats it" "$(grep -c '^for_window \[app_id="isotope-system"\] floating enable$' /src/rootfs-sway/etc/sway/config)" 1
expect "i3 floats it" "$(grep -c '^for_window \[class="isotope-system"\] floating enable$' /src/rootfs-i3/etc/i3/config)" 1
expect "niri floats it" "$(grep -c 'match app-id="^isotope-system\$"' /src/rootfs-niri/etc/niri/config.kdl)" 1
expect "the launchers list it" "$(grep -c '^Exec=isotope-system$' /src/rootfs/usr/share/applications/isotope-system.desktop)" 1
expect "the polkit file declares the backup action" "$(grep -c '<action id="io.kairos.isotope-update.backup">' /src/rootfs/usr/share/polkit-1/actions/io.kairos.isotope-update.policy)" 1
for a in users restore; do
    expect "the polkit file declares the $a action" "$(grep -c "<action id=\"io.kairos.isotope-update.$a\">" /src/rootfs/usr/share/polkit-1/actions/io.kairos.isotope-update.policy)" 1
done

rm -rf "$T"
[ "$fail" -eq 0 ] && echo "all system menu tests passed"
exit "$fail"
