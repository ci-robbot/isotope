#!/usr/bin/env bash
# The live desktop's static contract, checked on a built desktop image or on
# the repo tree.
#
#   test/live/check-live.sh IMAGE        a built desktop image (CI)
#   test/live/check-live.sh --repo       rootfs/ plus each rootfs-VARIANT/
#
# - ly and cosmic-greeter start unless an install runs (not on live_mode), and
#   both boot stage files have the twin stage that starts the login screen on
#   a live desktop boot.
# - isotope-not-live runs its arguments unless the marker exists.
# - Every lock, idle-lock and tray start line goes through it.
# - The backup timer does not run on a live desktop.
# - Autologin has what it needs: ly's config.ini (sway, i3, niri), the COSMIC
#   session file, the session file SESSION= names.
# - The installer's window (app ID and X11 class isotope-install) floats on
#   sway, i3 and niri. In an image, isotope-install-gui is the Wayland build
#   (cage), and i3 also has isotope-install-gui-x11, which NEEDs libX11.
# - The image holds no live user, no autologin and no unhidden installer
#   launcher: they exist only on a live boot. The launcher's desktop file is
#   shipped with NoDisplay=true; the live stage writes the unhidden copy into
#   the live user's home. The agent image removes the launcher and helper.
#
# In repo mode the files the image build produces (ly's config.ini, COSMIC's
# session file) are not in the tree, and those checks are skipped.
set -euo pipefail
cd "$(dirname "$0")/../.."

mode=image
IMAGE=""
case "${1:-}" in
    --repo) mode=repo ;;
    "") echo "usage: $0 IMAGE | --repo" >&2; exit 2 ;;
    *) IMAGE="$1" ;;
esac

fail=0
pass() { echo "PASS: $*"; }
bad()  { echo "FAIL: $*"; fail=1; }
skip() { echo "SKIP: $*"; }
check() { # check DESC CMD...
    local d="$1"; shift
    if "$@" >/dev/null 2>&1; then pass "$d"; else bad "$d"; fi
}

work=$(mktemp -d)
cid=""
cleanup() { [ -z "$cid" ] || docker rm -f "$cid" >/dev/null 2>&1 || true; rm -rf "$work"; }
trap cleanup EXIT

# The roots to check: "VARIANT DIR" pairs. Repo mode merges the common overlay
# and the variant's, in the order the Dockerfile copies them. Image mode copies
# the files the checks read out of the image.
roots=()
if [ "$mode" = repo ]; then
    for v in sway i3 niri cosmic; do
        mkdir -p "$work/$v"
        cp -a rootfs/. "$work/$v/"
        cp -a "rootfs-$v/." "$work/$v/"
        roots+=("$v:$work/$v")
    done
else
    cid=$(docker create --entrypoint sh "$IMAGE")
    mkdir -p "$work/image"
    for p in etc system usr/bin usr/libexec usr/lib/systemd/system usr/share/wayland-sessions \
        usr/share/xsessions usr/share/applications; do
        mkdir -p "$work/image/$(dirname "$p")"
        docker cp "$cid:/$p" "$work/image/$p" >/dev/null 2>&1 || true
    done
    v=$(sed -n 's/^SESSION=//p' "$work/image/etc/isotope/session" 2>/dev/null | head -n 1)
    [ -n "$v" ] || { echo "FAIL: $IMAGE has no SESSION= in /etc/isotope/session"; exit 1; }
    roots+=("$v:$work/image")
fi

# Run isotope-not-live with the marker present (STATE on) or absent (off). In
# image mode the marker is made inside the container, a host path would not
# exist there; in repo mode the script in the tree runs here.
not_live() { # not_live on|off CMD...
    local state="$1"; shift
    if [ "$mode" = repo ]; then
        local m="$work/marker-$variant"
        rm -f "$m"
        [ "$state" != on ] || : >"$m"
        ISOTOPE_LIVE_MARKER="$m" sh "$work/${variant}/usr/bin/isotope-not-live" "$@"
    else
        docker run --rm --entrypoint sh "$IMAGE" -c \
            'if [ "$1" = on ]; then : >/tmp/marker; fi; shift; exec env ISOTOPE_LIVE_MARKER=/tmp/marker isotope-not-live "$@"' \
            sh "$state" "$@"
    fi
}

# Non-comment lines of FILES that start something that locks, idles or shows the
# tray (swayidle, xss-lock, isotope-update tray) without isotope-not-live.
bare_lines() {
    grep -HnE '(swayidle|xss-lock|isotope-update"?[[:space:]]+"?tray)' "$@" 2>/dev/null |
        grep -vE '^[^:]+:[0-9]+:[[:space:]]*(#|//)' |
        grep -v 'isotope-not-live' |
        awk '!(/swayidle/ && !/swaylock/)' || true # a swayidle line without swaylock only blanks
}
# Non-comment lines of FILES that lock the screen by hand or by a key
# (swaylock, i3lock, $lock, Noctalia's session lock, loginctl lock-session)
# without isotope-not-live. The definition `set $lock` is not a use.
# shellcheck disable=SC2016
bare_lock_lines() {
    grep -HnE 'swaylock|i3lock|\$lock($| )|session lock|lock-session' "$@" 2>/dev/null |
        grep -vE '^[^:]+:[0-9]+:[[:space:]]*(#|//)' |
        grep -vE ':[0-9]+:[[:space:]]*set \$lock ' |
        grep -v 'isotope-not-live' || true
}
# The same lines, for the check that the probe finds something at all.
all_lines() {
    grep -HnE '(swayidle|xss-lock|isotope-update"?[[:space:]]+"?tray)' "$@" 2>/dev/null |
        grep -vE '^[^:]+:[0-9]+:[[:space:]]*(#|//)' || true
}

# shellcheck disable=SC2016
all_lock_lines() {
    grep -HnE 'swaylock|i3lock|\$lock($| )|session lock|lock-session' "$@" 2>/dev/null |
        grep -vE '^[^:]+:[0-9]+:[[:space:]]*(#|//)' || true
}

for entry in "${roots[@]}"; do
    variant="${entry%%:*}"
    r="${entry#*:}"
    echo "== $variant"

    # --- the helper
    check "$variant: isotope-not-live is in the image" test -f "$r/usr/bin/isotope-not-live"
    out=$(not_live off echo ran 2>&1) || out="(failed)"
    if [ "$out" = ran ]; then pass "$variant: isotope-not-live runs its arguments without the marker"
    else bad "$variant: isotope-not-live runs its arguments without the marker (got '$out')"; fi
    out=$(not_live on echo ran 2>&1) && rc=0 || rc=$?
    if [ -z "$out" ] && [ "$rc" = 0 ]; then pass "$variant: isotope-not-live is silent and exits 0 with the marker"
    else bad "$variant: isotope-not-live is silent and exits 0 with the marker (got '$out', status $rc)"; fi

    # --- login screen gates: install_mode, not live_mode
    if [ "$variant" = cosmic ]; then
        gate="$r/etc/systemd/system/cosmic-greeter.service.d/10-conditions.conf"
        stage="$r/system/oem/91_desktop_session.yaml"
        unit_start='systemctl start --no-block cosmic-greeter.service'
    else
        gate="$r/etc/systemd/system/ly@.service.d/10-conditions.conf"
        stage="$r/system/oem/91_desktop_session.yaml"
        unit_start='systemctl start --no-block ly@tty1.service'
    fi
    check "$variant: login screen waits for no install (!install_mode)" grep -qx 'ConditionPathExists=!/run/cos/install_mode' "$gate"
    check "$variant: login screen waits for no interactive install" grep -qx 'ConditionPathExists=!/run/cos/install_mode_interactive' "$gate"
    check "$variant: login screen starts on an installed system (|!live_mode)" grep -qx 'ConditionPathExists=|!/run/cos/live_mode' "$gate"
    check "$variant: login screen starts on the live desktop (|marker)" grep -qx 'ConditionPathExists=|/run/isotope/live-desktop' "$gate"
    check "$variant: live_mode is not a plain guard (no entry-wide block)" bash -c "! grep -qE '^ConditionPathExists=!/run/cos/live_mode' '$gate'"
    check "$variant: boot stage has the live twin, guarded by the marker" \
        grep -qF "if: '[ -f /run/isotope/live-desktop ]'" "$stage"
    # The twin's commands (up to the next stage or the end) start the login screen without waiting.
    twin=$(awk '/if: .\[ -f \/run\/isotope\/live-desktop \]./{f=1; next} /^    - /{f=0} f' "$stage")
    check "$variant: the live twin starts the login screen with --no-block" bash -c "grep -qF '$unit_start' <<<\"\$1\"" _ "$twin"

    # --- lock, idle and tray lines
    case $variant in
        sway) files=("$r/etc/sway/config") ;;
        i3) files=("$r/etc/i3/config" "$r"/etc/i3/config.d/*.conf) ;;
        niri) files=("$r/etc/niri/config.kdl") ;;
        cosmic) files=("$r"/etc/xdg/autostart/*.desktop) ;;
    esac
    found=$(all_lines "${files[@]}")
    check "$variant: the config has a tray start line" bash -c "grep -qE 'isotope-update' <<<\"\$1\"" _ "$found"
    case $variant in
        sway) check "$variant: the config has the swayidle line" bash -c "grep -q swayidle <<<\"\$1\"" _ "$found" ;;
        i3) check "$variant: the config has the xss-lock line" bash -c "grep -q xss-lock <<<\"\$1\"" _ "$found" ;;
    esac
    bare=$(bare_lines "${files[@]}")
    if [ -z "$bare" ]; then
        pass "$variant: every lock and tray start line goes through isotope-not-live"
    else
        bad "$variant: lock or tray lines without isotope-not-live:"; printf '  %s\n' "$bare"
    fi
    # --- manual lock paths: keys, power menus, the Isotope menu
    case $variant in
        sway) lockfiles=("$r/etc/sway/config" "$r/usr/bin/isotope-power-menu" "$r/usr/bin/isotope-menu") ;;
        i3) lockfiles=("$r/etc/i3/config" "$r"/etc/i3/config.d/*.conf "$r/usr/bin/isotope-power-menu" "$r/usr/bin/isotope-menu") ;;
        niri) lockfiles=("$r/etc/niri/config.kdl" "$r/usr/bin/isotope-menu") ;;
        cosmic) lockfiles=("$r/usr/bin/isotope-menu") ;;
    esac
    barelock=$(bare_lock_lines "${lockfiles[@]}")
    if [ -z "$barelock" ]; then
        pass "$variant: every manual lock (key, power menu, Isotope menu) goes through isotope-not-live"
    else
        bad "$variant: lock commands without isotope-not-live:"; printf '  %s\n' "$barelock"
    fi
    if [ -n "$(all_lock_lines "${lockfiles[@]}")" ]; then pass "$variant: the lock probe finds lock commands"; else bad "$variant: the lock probe finds nothing"; fi
    case $variant in
        sway|i3)
            # The power menu hides Lock on live, and shows it elsewhere (stub picker prints its rows).
            stub="$work/stub-$variant"; mkdir -p "$stub"
            printf '#!/bin/sh\ncat >&2\nexit 1\n' >"$stub/isotope-pick"; chmod +x "$stub/isotope-pick"
            m="$work/pm-marker-$variant"; rm -f "$m"
            rows=$(PATH="$stub:$PATH" ISOTOPE_LIVE_MARKER="$m" sh "$r/usr/bin/isotope-power-menu" 2>&1 || true)
            if grep -q '^Lock' <<<"$rows"; then pass "$variant: the power menu offers Lock off the live desktop"; else bad "$variant: the power menu offers Lock off the live desktop"; fi
            : >"$m"
            rows=$(PATH="$stub:$PATH" ISOTOPE_LIVE_MARKER="$m" sh "$r/usr/bin/isotope-power-menu" 2>&1 || true)
            if grep -q '^Lock' <<<"$rows" || ! grep -q '^Log out' <<<"$rows"; then bad "$variant: the power menu hides Lock on the live desktop"; else pass "$variant: the power menu hides Lock on the live desktop"; fi
            ;;
        cosmic)
            check "cosmic: the Isotope menu's power list hides Lock on live" grep -qF 'ISOTOPE_LIVE_MARKER:-/run/isotope/live-desktop' "$r/usr/bin/isotope-menu"
            ;;
    esac
    if [ "$variant" = sway ]; then
        check "sway: idle blanking (swayidle dpms) runs everywhere" \
            grep -qE '^exec swayidle .*dpms off' "$r/etc/sway/config"
        check "sway: the swayidle lock line is gated" \
            grep -qE "^exec isotope-not-live swayidle .*swaylock" "$r/etc/sway/config"
    fi
    # --- PAM: the lock screens accept `live` (cosmic-greeter locks before suspend with no setting to stop it)
    case $variant in sway) lockpam=swaylock ;; i3) lockpam=i3lock ;; cosmic) lockpam=cosmic-greeter ;; *) lockpam="" ;; esac
    if [ -n "$lockpam" ]; then
        check "$variant: /etc/pam.d/$lockpam exists, for the live stage to open" test -f "$r/etc/pam.d/$lockpam"
        check "$variant: the live stage writes the pam_succeed_if rule" grep -q 'pam_succeed_if.so user = live' "$r/usr/libexec/isotope-live-boot"
        check "$variant: the image's $lockpam has no live rule" bash -c "! grep -qs pam_succeed_if '$r/etc/pam.d/$lockpam'"
    fi
    if [ "$mode" = image ]; then
        if docker run --rm --entrypoint sh "$IMAGE" -c 'ls /usr/lib/security/pam_succeed_if.so /lib/security/pam_succeed_if.so /usr/lib64/security/pam_succeed_if.so 2>/dev/null | grep -q .' >/dev/null 2>&1; then
            pass "$variant: the image has pam_succeed_if.so"
        else
            bad "$variant: the image has no pam_succeed_if.so (the live lock bypass needs it)"
        fi
    fi
    # --- the live welcome's keyboard pick on i3 runs setxkbmap
    if [ "$mode" = image ] && [ "$variant" = i3 ]; then
        if docker run --rm --entrypoint sh "$IMAGE" -c 'command -v setxkbmap' >/dev/null 2>&1; then
            pass "i3: the image has setxkbmap (the live welcome's keyboard)"
        else
            bad "i3: the image has no setxkbmap (the live welcome cannot switch the layout)"
        fi
    fi
    if [ "$variant" = niri ]; then
        check "niri: the live boot stage switches off Noctalia's lock screen" \
            grep -q 'lockscreen' "$r/usr/libexec/isotope-live-boot"
    fi
    if [ "$variant" = i3 ]; then
        check "i3: the xss-lock line keeps its form (exec --no-startup-id isotope-not-live xss-lock)" \
            grep -qE '^exec --no-startup-id isotope-not-live xss-lock ' "$r/etc/i3/config.d/10-screen-lock.conf"
    fi

    # --- the installer as a window (isotope-install-gui --window)
    case $variant in
        sway) check "sway: the installer window floats (app_id isotope-install)" \
            grep -qxF 'for_window [app_id="isotope-install"] floating enable' "$r/etc/sway/config" ;;
        i3) check "i3: the installer window floats (class isotope-install)" \
            grep -qxF 'for_window [class="isotope-install"] floating enable' "$r/etc/i3/config" ;;
        niri) check "niri: the installer window floats (app-id isotope-install)" \
            grep -qF 'match app-id="^isotope-install$"' "$r/etc/niri/config.kdl" ;;
    esac
    if [ "$mode" = image ]; then
        # NEEDED, not ldd: on i3 Mesa's libEGL pulls libX11 in for both builds.
        needed() { readelf -d "$1" 2>/dev/null | sed -n 's/.*(NEEDED).*\[\(.*\)\]/\1/p'; }
        gui="$r/usr/bin/isotope-install-gui"
        check "$variant: isotope-install-gui (cage, Wayland) NEEDs libwayland-client" bash -c "readelf -d '$gui' | grep -q 'NEEDED.*libwayland-client'"
        if needed "$gui" | grep -q '^libX11'; then bad "$variant: isotope-install-gui (cage) NEEDs libX11; it must be the Wayland build"
        else pass "$variant: isotope-install-gui (cage) is the Wayland build (no libX11)"; fi
        x11="$r/usr/bin/isotope-install-gui-x11"
        if [ "$variant" = i3 ]; then
            if needed "$x11" | grep -q '^libX11\.so'; then pass "i3: isotope-install-gui-x11 (the live X11 session's) NEEDs libX11"
            else bad "i3: isotope-install-gui-x11 is missing or does not NEED libX11"; fi
        else
            check "$variant: no isotope-install-gui-x11 (only i3 runs X11)" test ! -e "$x11"
        fi
    fi

    # --- backup timer
    check "$variant: the backup timer does not run on a live desktop" \
        grep -qx 'ConditionPathExists=!/run/isotope/live-desktop' "$r/usr/lib/systemd/system/isotope-backup.timer.d/10-not-live.conf"

    # --- autologin needs
    name=$(sed -n 's/^SESSION=//p' "$r/etc/isotope/session" 2>/dev/null | head -n 1)
    check "$variant: SESSION= is set" test -n "$name"
    if [ "$variant" = i3 ]; then sdir=xsessions; else sdir=wayland-sessions; fi
    if [ "$variant" = cosmic ] && [ "$mode" = repo ]; then
        skip "cosmic: cosmic-session installs the session file at image build"
    else
        check "$variant: the session file SESSION=$name names exists ($sdir/$name.desktop)" test -f "$r/usr/share/$sdir/$name.desktop"
    fi
    if [ "$variant" = cosmic ]; then
        if [ "$mode" = repo ]; then
            skip "cosmic: cosmic.desktop is built into the image"
        else
            check "cosmic: /usr/share/wayland-sessions/cosmic.desktop exists" test -f "$r/usr/share/wayland-sessions/cosmic.desktop"
        fi
        check "cosmic: greetd config is in the image" test -f "$r/etc/greetd/cosmic-greeter.toml"
    elif [ "$mode" = repo ]; then
        skip "$variant: ly's /etc/ly/config.ini is installed by the ly build, not in the tree"
    else
        check "$variant: /etc/ly/config.ini exists (autologin writes into it)" test -f "$r/etc/ly/config.ini"
    fi

    # --- the image is clean
    check "$variant: no live user in /etc/passwd" bash -c "! grep -qs '^live:' '$r/etc/passwd'"
    leak=$(grep -rsE '^[[:space:]]*auto_login_user[[:space:]]*=[[:space:]]*[^[:space:]]|^\[initial_session\]' "$r/etc/ly" "$r/etc/greetd" || true)
    if [ -z "$leak" ]; then pass "$variant: no autologin value in ly or greetd config"
    else bad "$variant: autologin set in the image:"; printf '  %s\n' "$leak"; fi
    # --- the Install Isotope launcher: hidden in the image, shown by the live stage
    inst="$r/usr/share/applications/isotope-install.desktop"
    check "$variant: isotope-install.desktop ships" test -f "$inst"
    check "$variant: isotope-install.desktop is NoDisplay=true (hidden in the image)" grep -qx 'NoDisplay=true' "$inst"
    check "$variant: isotope-install.desktop runs isotope-live-install, with TryExec" \
        bash -c "grep -qx 'Exec=isotope-live-install' '$inst' && grep -qx 'TryExec=isotope-live-install' '$inst'"
    check "$variant: the launcher's icon is a Conflux link (theme/conflux-links.txt)" \
        bash -c "grep -qx 'Icon=isotope-install' '$inst' && grep -qx 'isotope-install software-store' theme/conflux-links.txt"
    check "$variant: isotope-install.desktop has no Hidden=false/NoDisplay=false override" bash -c "! grep -qE '^(Hidden|NoDisplay)=false' '$inst'"
    check "$variant: isotope-live-install and its helper are executable" \
        test -x "$r/usr/bin/isotope-live-install" -a -x "$r/usr/libexec/isotope-live-install-run"
    check "$variant: isotope-menu lists Install Isotope first on live" \
        bash -c "grep -q 'entries=\"Install Isotope' '$r/usr/bin/isotope-menu' && grep -q '\"Install Isotope\") exec isotope-live-install' '$r/usr/bin/isotope-menu'"
    check "$variant: the live stage unhides it for the live user only" grep -q 'setup_installer_launcher$' "$r/usr/libexec/isotope-live-boot"
done

if [ "$mode" = repo ]; then
    echo "== agent"
    ag=rootfs-agent/etc/systemd/system/ly@.service.d/20-agent-profile.conf
    check "agent: its drop-in sorts after ly's 10-conditions.conf" test "$(printf '%s\n' 10-conditions.conf 20-agent-profile.conf | sort | tail -n 1)" = 20-agent-profile.conf
    check "agent: its drop-in resets ConditionPathExists= (clears the | conditions too)" grep -qx 'ConditionPathExists=' "$ag"
    check "agent: the reset comes before its own conditions" test "$(grep -n '^ConditionPathExists=$' "$ag" | head -n1 | cut -d: -f1)" -lt "$(grep -n '^ConditionPathExists=!' "$ag" | head -n1 | cut -d: -f1)"
    check "agent: the installer launcher, its helper and its desktop file are removed" \
        bash -c "grep -q 'rm -f /usr/bin/isotope-live-install /usr/libexec/isotope-live-install-run' Dockerfile.agent && grep -q '/usr/share/applications/isotope-install.desktop' Dockerfile.agent && grep -q '! test -e /usr/bin/isotope-live-install' Dockerfile.agent"
    check "agent: no triggering | condition after the reset (the agent starts on live too)" bash -c "! grep -q '^ConditionPathExists=|' '$ag'"
fi

if [ "$fail" = 0 ]; then echo "all live desktop checks passed"; else echo "live desktop checks FAILED"; exit 1; fi
