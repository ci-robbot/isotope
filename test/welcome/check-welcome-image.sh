#!/usr/bin/env bash
# The checks are strings that the image's sh runs, on purpose.
# shellcheck disable=SC2016
# Isotope Welcome in a built image: the binary of the variant's build, the app
# list, the session line that starts it at first login and the window rule that
# floats it; on COSMIC, no cosmic-initial-setup but its layouts. For the agent
# image, the opposite: no isotope-welcome and nothing that starts it.
#
#   test/welcome/check-welcome-image.sh IMAGE FLAVOR
#
# FLAVOR is sway, i3, cosmic, niri or agent. sway, cosmic and niri get the
# Wayland-only build (no libX11); i3 the build with X11, whose libraries the
# image has.
set -euo pipefail

usage() { echo "usage: $0 IMAGE FLAVOR" >&2; exit 2; }
[ $# -eq 2 ] || usage
IMAGE="$1"
FLAVOR="$2"
case "$FLAVOR" in sway|i3|cosmic|niri|agent) ;; *) usage ;; esac

fail=0
run() { docker run --rm --entrypoint sh "$IMAGE" -c "$1"; }
check() {
    local desc="$1"; shift
    if run "$*" >/dev/null 2>&1; then
        echo "PASS: $desc"
    else
        echo "FAIL: $desc"; fail=1
    fi
}

# Session configuration lines, without comments.
active='grep -rhsv "^[[:space:]]*\(#\|//\)"'
# Every file a variant's session reads its start-up lines from.
case "$FLAVOR" in
    sway)        conf="/etc/sway" ;;
    i3|agent)    conf="/etc/i3" ;;
    niri)        conf="/etc/niri" ;;
    cosmic)      conf="/etc/xdg/autostart" ;;
esac

if [ "$FLAVOR" = agent ]; then
    check "agent: no /usr/bin/isotope-welcome" '! test -e /usr/bin/isotope-welcome'
    check "agent: no app list" '! test -e /usr/share/isotope/first-run-apps.list'
    check "agent: no isotope-first-run-apps" '! test -e /usr/bin/isotope-first-run-apps'
    check "agent: the session starts neither isotope-welcome nor isotope-first-run-apps" \
        "! $active /etc/i3 /etc/xdg/autostart | grep -q -e 'isotope-welcome' -e 'isotope-first-run-apps'"
    exit "$fail"
fi

# A program, not the notification script of the same name it replaces: ldd
# lists its libraries (libwayland-client, which both builds link).
check "/usr/bin/isotope-welcome is the Gio program" \
    'test -x /usr/bin/isotope-welcome && ldd /usr/bin/isotope-welcome 2>&1 | grep -q libwayland-client'
check "isotope-welcome: its libraries resolve" \
    'test -x /usr/bin/isotope-welcome && ! ldd /usr/bin/isotope-welcome 2>&1 | grep -q "not found"'
if [ "$FLAVOR" = i3 ]; then
    check "i3: the X11 build (links libX11)" 'ldd /usr/bin/isotope-welcome 2>&1 | grep -q libX11'
else
    check "$FLAVOR: the Wayland build (no libX11)" \
        'ldd /usr/bin/isotope-welcome 2>&1 | grep -q libwayland-client && ! ldd /usr/bin/isotope-welcome 2>&1 | grep -q libX11'
fi
check "the app list /usr/share/isotope/first-run-apps.list is installed" \
    'test -s /usr/share/isotope/first-run-apps.list'
# isotope-welcome's own usage, not isotope's "unknown command" (also 2).
check "'isotope welcome --nonsense' exits 2 with isotope-welcome's usage" \
    'out=$(isotope welcome --nonsense 2>&1); [ $? -eq 2 ] && echo "$out" | grep -q "usage: isotope-welcome"'
check "isotope-first-run-apps opens the Apps page" \
    'test -x /usr/bin/isotope-first-run-apps && grep -qx "exec isotope welcome apps" /usr/bin/isotope-first-run-apps'

case "$FLAVOR" in
    sway)
        check "sway: the session starts 'isotope-welcome --login'" \
            "$active $conf | grep -qx 'exec isotope-welcome --login'"
        check "sway: isotope-welcome floats" \
            "$active $conf | grep -qx 'for_window \[app_id=\"isotope-welcome\"\] floating enable'"
        ;;
    i3)
        check "i3: the session starts 'isotope-welcome --login'" \
            "$active $conf | grep -qx 'exec --no-startup-id isotope-welcome --login'"
        check "i3: isotope-welcome floats" \
            "$active $conf | grep -qx 'for_window \[class=\"isotope-welcome\"\] floating enable'"
        ;;
    niri)
        check "niri: the session starts 'isotope-welcome --login'" \
            "$active $conf | grep -qx 'spawn-at-startup \"isotope-welcome\" \"--login\"'"
        check "niri: isotope-welcome floats" \
            "$active $conf | tr -d '\n' | grep -q 'window-rule {[^}]*match app-id=\"[\\^]isotope-welcome[\$]\"[^}]*open-floating true[^}]*}'"
        ;;
    cosmic)
        check "COSMIC: the session starts 'isotope-welcome --login'" \
            "grep -qx 'Exec=isotope-welcome --login' /etc/xdg/autostart/isotope-welcome.desktop"
        check "COSMIC: no cosmic-initial-setup autostart" \
            '! test -e /etc/xdg/autostart/com.system76.CosmicInitialSetup.desktop'
        check "COSMIC: no /usr/bin/cosmic-initial-setup" '! test -e /usr/bin/cosmic-initial-setup'
        check "COSMIC: the layouts the Layout page reads are installed" \
            'test -e /usr/share/cosmic-layouts/bottom-panel'
        ;;
esac
check "$FLAVOR: the session no longer starts isotope-first-run-apps --login" \
    "! $active $conf | grep -q 'isotope-first-run-apps'"
# Every other line that names it must pass the exact token --login (bare,
# or quoted as niri's spawn-at-startup does); the float rules are exempt.
login_token='(^|[[:space:]"])--login("|[[:space:]]|$)'
check "$FLAVOR: the session starts isotope-welcome only with --login" \
    "! $active $conf | grep 'isotope-welcome' | grep -v -e 'for_window' -e 'match app-id' | grep -vE '$login_token' | grep -q ."

exit "$fail"
