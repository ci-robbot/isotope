#!/usr/bin/env bash
# Test rootfs/usr/libexec/isotope-live-boot in an alpine container, against a
# temporary root. No image build.
#
#   test/live/test-live-stage.sh
set -euo pipefail
cd "$(dirname "$0")/../.."

if [ "${1:-}" != "--inner" ]; then
    exec docker run --rm -v "$PWD:/repo:ro" alpine:3 sh /repo/test/live/test-live-stage.sh --inner
fi

# ---- inside the container (busybox sh) ----
cd /repo
script=/repo/rootfs/usr/libexec/isotope-live-boot
fail=0
pass() { echo "PASS: $*"; }
bad()  { echo "FAIL: $*"; fail=1; }
check() { # check DESC CMD...
    d="$1"; shift
    if "$@" >/dev/null 2>&1; then pass "$d"; else bad "$d"; fi
}

# new_root VARIANT -> prints a fresh temp root with the base files a booted
# image has: passwd with kairos at 1000, groups, ly config, session file.
new_root() {
    r=$(mktemp -d)
    mkdir -p "$r/etc/ly" "$r/etc/greetd" "$r/etc/isotope" "$r/oem" "$r/iso" "$r/run/cos" "$r/home" "$r/usr/share/wayland-sessions"
    printf 'root:x:0:0:root:/root:/bin/sh\nkairos:x:1000:1000::/home/kairos:/bin/bash\n' >"$r/etc/passwd"
    printf 'root:x:0:\nadmin:x:998:kairos\nvideo:x:27:\naudio:x:29:\nkairos:x:1000:\n' >"$r/etc/group"
    printf 'kairos:100000:65536\n' >"$r/etc/subuid"; cp "$r/etc/subuid" "$r/etc/subgid"
    printf 'DESKTOP_NAME=Sway\nSESSION=sway\n' >"$r/etc/isotope/session"
    printf 'root:!:1::::::\nkairos:!:1::::::\n' >"$r/etc/shadow"
    printf '# ly\nauto_login_user = \nbox_title = Isotope\n' >"$r/etc/ly/config.ini"
    mkdir -p "$r/usr/share/applications"
    cp /repo/rootfs/usr/share/applications/isotope-install.desktop "$r/usr/share/applications/"
    echo "$r"
}
run() { # run ROOT CMDLINE
    ISOTOPE_LIVE_ROOT="$1" ISOTOPE_LIVE_CMDLINE="$2" ISOTOPE_LIVE_OEM="$1/oem" ISOTOPE_LIVE_ISO="$1/iso" sh "$script"
}
live_cmd=$(mktemp); echo "BOOT_IMAGE=/boot/vmlinuz isotope.live root=live:CDLABEL=X" >"$live_cmd"
plain_cmd=$(mktemp); echo "BOOT_IMAGE=/boot/vmlinuz isotope.liveness=1 root=live:CDLABEL=X" >"$plain_cmd"
eq() { if [ "$2" = "$3" ]; then pass "$1"; else bad "$1"; fi; }
snapshot() { (cd "$1" && find . -type f -exec cksum {} + | sort; find . | sort); }

# not-live: no isotope.live word (a longer word does not count either)
r=$(new_root); : >"$r/run/cos/live_mode"; before=$(snapshot "$r")
run "$r" "$plain_cmd"
eq "not-live: cmdline without isotope.live writes nothing" "$before" "$(snapshot "$r")"
r=$(new_root); before=$(snapshot "$r")
run "$r" "$live_cmd"
eq "not-live: no live_mode writes nothing" "$before" "$(snapshot "$r")"

# live
r=$(new_root); : >"$r/run/cos/live_mode"
printf '[default_session]\ncommand = "x"\n' >"$r/etc/greetd/cosmic-greeter.toml"
run "$r" "$live_cmd"
check "live: marker written" test -f "$r/run/isotope/live-desktop"
check "live: no install_mode" test ! -e "$r/run/cos/install_mode"
check "live: user in passwd with display name" grep -q '^live:x:[0-9]*:[0-9]*:Isotope Live:/home/live:' "$r/etc/passwd"
uid=$(awk -F: '$1=="live"{print $3}' "$r/etc/passwd")
check "live: UID '$uid' is set and is not 1000" test -n "$uid" -a "$uid" != 1000
check "live: kairos keeps UID 1000" grep -q '^kairos:x:1000:' "$r/etc/passwd"
check "live: in admin" grep -q '^admin:.*:.*live' "$r/etc/group"
check "live: in video" grep -q '^video:x:27:live$' "$r/etc/group"
check "live: in audio" grep -q '^audio:x:29:live$' "$r/etc/group"
check "live: groups the image lacks are not created" test -z "$(grep -E '^(render|input|bluetooth|seat|docker):' "$r/etc/group")"
check "live: one group of its own, once" test "$(grep -c '^live:' "$r/etc/group")" = 1
check "live: subuid range after kairos'" grep -qx 'live:165536:65536' "$r/etc/subuid"
check "live: subgid range after kairos'" grep -qx 'live:165536:65536' "$r/etc/subgid"
check "live: kairos stays in admin" grep -q '^admin:x:998:kairos,live$' "$r/etc/group"
check "live: shadow entry has no password" grep -q '^live:!:' "$r/etc/shadow"
check "live: home exists" test -d "$r/home/live"
check "ly: auto_login_user" grep -qx 'auto_login_user = live' "$r/etc/ly/config.ini"
check "ly: auto_login_session from the session file" grep -qx 'auto_login_session = sway' "$r/etc/ly/config.ini"
check "ly: auto_login_service" grep -qx 'auto_login_service = ly-autologin' "$r/etc/ly/config.ini"
check "ly: other keys kept" grep -qx 'box_title = Isotope' "$r/etc/ly/config.ini"
check "greetd: initial_session" grep -qx '\[initial_session\]' "$r/etc/greetd/cosmic-greeter.toml"
check "greetd: command" grep -qx 'command = "start-cosmic"' "$r/etc/greetd/cosmic-greeter.toml"
check "greetd: user" grep -qx 'user = "live"' "$r/etc/greetd/cosmic-greeter.toml"
check "greetd: default_session kept" grep -q '^\[default_session\]' "$r/etc/greetd/cosmic-greeter.toml"
check "cosmic: idle lock is off for live (LockScreen runs nothing)" \
    grep -q 'LockScreen: "true"' "$r/home/live/.config/cosmic/com.system76.CosmicSettings.Shortcuts/v1/system_actions"
inst="$r/home/live/.local/share/applications/isotope-install.desktop"
check "live: Install Isotope is unhidden for the live user" test -f "$inst"
check "live: the unhidden copy has no NoDisplay and keeps Exec" sh -c "! grep -q '^NoDisplay' '$inst' && grep -qx 'Exec=isotope-live-install' '$inst'"
check "live: the copy does not say it is hidden" sh -c "! grep -qi 'hidden' '$inst'"
check "live: the image's own desktop file stays hidden" grep -qx 'NoDisplay=true' "$r/usr/share/applications/isotope-install.desktop"
check "live: kairos gets no launcher copy" test ! -e "$r/home/kairos/.local"
r2=$(new_root); : >"$r2/run/cos/live_mode"
run "$r2" "$live_cmd"
check "non-cosmic: no COSMIC config in the live home" test ! -e "$r2/home/live/.config/cosmic"
# The COSMIC override is for `live` only and only on the live path.
check "cosmic: override is not in kairos' home" test ! -e "$r/home/kairos/.config"
check "cosmic: override is not in /root or the image's /etc" test ! -e "$r/root" -a -z "$(find "$r/etc" -name system_actions)"
if [ "$(id -u)" = 0 ]; then
    live_uid=$(awk -F: '$1=="live"{print $3}' "$r/etc/passwd")
    eq "cosmic: the override is owned by live" "$live_uid" "$(stat -c %u "$r/home/live/.config/cosmic/com.system76.CosmicSettings.Shortcuts/v1/system_actions")"
else
    echo "SKIP: ownership of the override (not root)"
fi
check "cosmic: only the override is under .config" test "$(find "$r/home/live/.config" -type f | wc -l)" = 1
r3=$(new_root); : >"$r3/run/cos/live_mode"; mkdir -p "$r3/home/kairos"
printf '[default_session]\n' >"$r3/etc/greetd/cosmic-greeter.toml"
run "$r3" "$plain_cmd"
check "cosmic: no override without isotope.live on the cmdline" test -z "$(find "$r3/home" -name system_actions)"
r3=$(new_root); printf '[default_session]\n' >"$r3/etc/greetd/cosmic-greeter.toml"
run "$r3" "$live_cmd"
check "cosmic: no override without live_mode" test -z "$(find "$r3/home" -name system_actions)"
r3=$(new_root); : >"$r3/run/cos/live_mode"; mkdir -p "$r3/home/kairos"
printf '[default_session]\n' >"$r3/etc/greetd/cosmic-greeter.toml"
printf 'install:\n  auto: true\n' >"$r3/oem/99_install.yaml"
run "$r3" "$live_cmd"
check "cosmic: no override when an install config wins" test -z "$(find "$r3/home" -name system_actions)"
# niri: Noctalia's lock screen is switched off for live, the same way.
r4=$(new_root); : >"$r4/run/cos/live_mode"; mkdir -p "$r4/usr/share/isotope" "$r4/home/kairos"
: >"$r4/usr/share/isotope/noctalia-defaults.toml"
run "$r4" "$live_cmd"
check "niri: Noctalia lock screen is off for live" \
    grep -qx 'enabled = false' "$r4/home/live/.config/noctalia/99-isotope-live.toml"
check "niri: the lockscreen table is named" grep -qx '\[lockscreen\]' "$r4/home/live/.config/noctalia/99-isotope-live.toml"
check "niri: sorts after the Isotope defaults file" test "$(printf '%s\n' 00-isotope-defaults.toml 99-isotope-live.toml | sort | tail -n 1)" = 99-isotope-live.toml
check "niri: nothing in kairos' home" test ! -e "$r4/home/kairos/.config"
check "niri: no COSMIC override" test ! -e "$r4/home/live/.config/cosmic"
check "sway: no Noctalia override" test ! -e "$r2/home/live/.config/noctalia"
r4=$(new_root); : >"$r4/run/cos/live_mode"; mkdir -p "$r4/usr/share/isotope"; : >"$r4/usr/share/isotope/noctalia-defaults.toml"
run "$r4" "$plain_cmd"
check "niri: no Noctalia override off the live path" test -z "$(find "$r4/home" -name '99-isotope-live.toml')"

# PAM: the lock screens accept `live` (COSMIC locks before suspend through
# logind, with no setting to turn that off). The real service files are used.
pam_root() { # pam_root CMDLINE -> a root with the repo's three lock services
    pr=$(new_root); : >"$pr/run/cos/live_mode"; mkdir -p "$pr/etc/pam.d"
    cp rootfs-cosmic/etc/pam.d/cosmic-greeter rootfs-sway/etc/pam.d/swaylock rootfs-i3/etc/pam.d/i3lock "$pr/etc/pam.d/"
    printf '#%%PAM-1.0\nauth       required     pam_unix.so\n' >"$pr/etc/pam.d/login"
    cp "$pr/etc/pam.d/login" "$pr/login.orig"
    run "$pr" "$1"
    echo "$pr"
}
pr=$(pam_root "$live_cmd")
rule='auth       sufficient   pam_succeed_if.so user = live'
for svc in cosmic-greeter swaylock i3lock; do
    f="$pr/etc/pam.d/$svc"
    check "pam: $svc has the rule" grep -qxF "$rule" "$f"
    check "pam: $svc keeps the banner first" test "$(sed -n 1p "$f")" = '#%PAM-1.0'
    first_auth=$(grep -m1 '^auth' "$f")
    eq "pam: $svc has the rule as its first auth line" "$rule" "$first_auth"
    check "pam: $svc keeps its own auth include" grep -q 'auth .*include .*login' "$f"
    check "pam: $svc has the rule once" test "$(grep -c pam_succeed_if "$f")" = 1
    check "pam: the image's $svc has no such rule" sh -c "! grep -rqs pam_succeed_if rootfs*/etc/pam.d/$svc"
done
check "pam: the rule is for the user live only" sh -c "! grep -h pam_succeed_if $pr/etc/pam.d/* | grep -v 'user = live\$'"
check "pam: other services are untouched" cmp -s "$pr/etc/pam.d/login" "$pr/login.orig"
run "$pr" "$live_cmd"
check "pam: a second run adds nothing" test "$(grep -c pam_succeed_if "$pr/etc/pam.d/cosmic-greeter")" = 1
pr=$(pam_root "$plain_cmd")
check "pam: no rule without isotope.live" test -z "$(grep -rl pam_succeed_if "$pr/etc/pam.d")"
pr=$(new_root); mkdir -p "$pr/etc/pam.d"; cp rootfs-cosmic/etc/pam.d/cosmic-greeter "$pr/etc/pam.d/"
run "$pr" "$live_cmd"
check "pam: no rule without live_mode" test -z "$(grep -rl pam_succeed_if "$pr/etc/pam.d")"
pr=$(new_root); : >"$pr/run/cos/live_mode"; mkdir -p "$pr/etc/pam.d"; cp rootfs-cosmic/etc/pam.d/cosmic-greeter "$pr/etc/pam.d/"
printf 'install:\n  auto: true\n' >"$pr/oem/99_install.yaml"
run "$pr" "$live_cmd"
check "pam: no rule when an install config wins" test -z "$(grep -rl pam_succeed_if "$pr/etc/pam.d")"
check "pam: no service file in the repo carries the rule" sh -c "! grep -rqs pam_succeed_if rootfs*/etc/pam.d Dockerfile Dockerfile.agent"

# live without a greetd file or a ly config: nothing is invented for greetd
r=$(new_root); : >"$r/run/cos/live_mode"; rm -rf "$r/etc/greetd"
run "$r" "$live_cmd"
check "live: no greetd file, none created" test ! -e "$r/etc/greetd"

# session names: the SESSION key of the variant, never a guess from the files
r=$(new_root); : >"$r/run/cos/live_mode"; printf 'SESSION=i3\n' >"$r/etc/isotope/session"
mkdir -p "$r/usr/share/xsessions"; : >"$r/usr/share/xsessions/other.desktop"
run "$r" "$live_cmd"
check "ly: session is the SESSION key" grep -qx 'auto_login_session = i3' "$r/etc/ly/config.ini"
# COSMIC: ships a wayland session, has no ly. ly's config is never created.
r=$(new_root); : >"$r/run/cos/live_mode"; rm -rf "$r/etc/ly"; printf 'SESSION=cosmic\n' >"$r/etc/isotope/session"
mkdir -p "$r/usr/share/wayland-sessions"; : >"$r/usr/share/wayland-sessions/cosmic.desktop"
run "$r" "$live_cmd"
check "cosmic: no ly config is created" test ! -e "$r/etc/ly"
# no SESSION key: ly is left alone
r=$(new_root); : >"$r/run/cos/live_mode"; : >"$r/etc/isotope/session"; before=$(cat "$r/etc/ly/config.ini")
run "$r" "$live_cmd"
eq "ly: no SESSION key, config untouched" "$before" "$(cat "$r/etc/ly/config.ini")"

# install-config: each fixture, mirroring install-ui/core ConfigPresent
cfgcase() { # cfgcase NAME EXPECT(yes|no) CONTENT [FILE]
    r=$(new_root); : >"$r/run/cos/live_mode"
    mkdir -p "$(dirname "$r/oem/${4:-99_install.yaml}")"
    printf '%b' "$3" >"$r/oem/${4:-99_install.yaml}"
    run "$r" "$live_cmd"
    if [ "$2" = yes ]; then
        if [ -f "$r/run/cos/install_mode" ] && [ ! -e "$r/run/isotope/live-desktop" ] \
            && ! grep -q '^live:' "$r/etc/passwd" && ! grep -q "auto_login_user.*live" "$r/etc/ly/config.ini"; then
            pass "install-config: $1 installs"; else bad "install-config: $1 installs"; fi
    else
        if [ ! -e "$r/run/cos/install_mode" ] && [ -f "$r/run/isotope/live-desktop" ] && grep -q '^live:' "$r/etc/passwd"; then
            pass "install-config: $1 stays live"; else bad "install-config: $1 stays live"; fi
    fi
}
cfgcase "install: block"          yes '#cloud-config\ninstall:\n  device: auto\n'
cfgcase "users: block"            yes '#cloud-config\nusers:\n  - name: a\n'
cfgcase "both blocks"             yes 'users:\n  - name: a\ninstall:\n  auto: true\n'
cfgcase "indented key"            yes '  install:\n    auto: true\n'
cfgcase "neither block"           no  '#cloud-config\nstages:\n  boot: []\n'
cfgcase "commented-out users"     no  '#cloud-config\n# users:\n#   - name: a\n# install:\n'
cfgcase "key in a nested value"   no  'stages:\n  boot:\n    - name: "x users: y"\n'
cfgcase "a .yml file counts"      yes 'install:\n  auto: true\n' 98_install.yml
cfgcase "ISO root config (AuroraBoot --cloud-config)" yes 'install:\n  auto: true\n' ../iso/config.yaml
cfgcase "ISO root config without a block" no 'stages:\n  boot: []\n' ../iso/config.yaml
cfgcase "cdrom datasource, 95_userdata dir" yes 'users:\n  - name: a\n' 95_userdata/user-data
cfgcase "cdrom datasource, 95_userdata file" yes 'users:\n  - name: a\n' 95_userdata
cfgcase "a non-yaml file does not" no  'install:\n  auto: true\n' 99_install.txt

# idempotent
r=$(new_root); : >"$r/run/cos/live_mode"; printf '[default_session]\n' >"$r/etc/greetd/cosmic-greeter.toml"
run "$r" "$live_cmd"; first=$(snapshot "$r")
run "$r" "$live_cmd"
eq "idempotent: a second run changes nothing" "$first" "$(snapshot "$r")"

# image-clean: nothing the stage writes comes from the image
dirty=$(grep -rlE 'auto_login_user[[:space:]]*=[[:space:]]*live|initial_session' \
    rootfs*/etc/ly rootfs*/etc/greetd 2>/dev/null || true)
eq "image-clean: no autologin or initial_session in the image overlays" "" "$dirty"
dirty=$(grep -nE 'auto_login_user[[:space:]]*=[[:space:]]*live|initial_session|^live:' Dockerfile Dockerfile.agent 2>/dev/null || true)
eq "image-clean: no Dockerfile writes the live user or autologin" "" "$dirty"
dirty=$(grep -rlE '(^|[^a-z])live:x:' rootfs* --include=passwd 2>/dev/null || true)
eq "image-clean: no baked passwd entry" "" "$dirty"

# stage file: runs the script in initramfs, ordered as yip orders it.
# Upstream names, fixed by kairos-init (pkg/bundled/cloudconfigs):
# https://github.com/kairos-io/kairos-init/tree/main/pkg/bundled/cloudconfigs
check "stage: initramfs runs the script" grep -q '/usr/libexec/isotope-live-boot' rootfs/system/oem/51_isotope_live.yaml
check "stage: the script is in the initramfs stage" grep -q '^  initramfs:' rootfs/system/oem/51_isotope_live.yaml
check "stage: 90 installer is in the initramfs stage" grep -q '^  initramfs:' rootfs/system/oem/90_desktop_installer.yaml
order=$( { ls rootfs/system/oem; printf '10_accounting.yaml\n51_reset.yaml\n52_installer.yaml\n'; } | sort | grep -E '^(10_accounting|51_isotope_live|52_installer|90_desktop_installer)\.yaml$' | tr '\n' ' ')
eq "stage: yip order is accounting, live, installer, desktop installer" \
    "10_accounting.yaml 51_isotope_live.yaml 52_installer.yaml 90_desktop_installer.yaml " "$order"
check "stage: no 89_isotope_live.yaml left" test ! -e rootfs/system/oem/89_isotope_live.yaml

# each variant's SESSION names a session file the variant ships
for v in sway i3 niri cosmic; do
    name=$(sed -n 's/^SESSION=//p' "rootfs-$v/etc/isotope/session")
    eq "session: $v sets SESSION" "$v" "$name"
    case $v in
        sway|niri) f="rootfs-$v/usr/share/wayland-sessions/$name.desktop" ;;
        i3) f="rootfs-$v/usr/share/xsessions/$name.desktop" ;;
        # cosmic-session installs cosmic.desktop; the image build runs cosmic-build cosmic-session
        cosmic) f=""; check "session: cosmic builds cosmic-session" grep -q 'cosmic-build cosmic-session' Dockerfile ;;
    esac
    [ -z "$f" ] || check "session: $v ships $f" test -f "$f"
done

if [ "$fail" = 0 ]; then echo "all live-stage checks passed"; else echo "live-stage checks FAILED"; exit 1; fi
