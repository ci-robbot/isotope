#!/usr/bin/env bash
# The checks are strings that the inner sh expands, on purpose.
# shellcheck disable=SC2016
# Input methods: the fcitx5 system extension and the image's hooks (see
# docs/superpowers/specs/2026-11-24-input-methods-design.md).
#
#   test/input-method/check-input-methods.sh --repo   the tree: the
#       profile.d file (in an Alpine container: nothing without fcitx5,
#       the Wayland and the i3 variables with it), each session's start
#       line, the CLI, the extension's files, the agent's removals, the
#       server left out (CI)
#   test/input-method/check-input-methods.sh IMAGE    a built image: the
#       hooks and that they do nothing without the extension, every library
#       the extension takes from the image (extensions/fcitx5/image-libs.txt),
#       none of the extension's paths taken; on the agent and the server,
#       no hooks
#
# The extension itself is built and tested by its Dockerfile's test stage
# (extensions.yml). Typing in a real session is on the design note's
# manual list.
set -euo pipefail
cd "$(dirname "$0")/../.."

fail=0
pass() { echo "PASS: $*"; }
bad()  { echo "FAIL: $*"; fail=1; }
check() { # check DESC CMD...
    local d="$1"; shift
    if "$@" >/dev/null 2>&1; then pass "$d"; else bad "$d"; fi
}
finish() {
    if [ "$fail" -eq 0 ]; then echo "all input method checks passed"; fi
    exit "$fail"
}

profile=etc/profile.d/isotope-input-method.sh
libs=$(grep -v -e '^#' -e '^$' extensions/fcitx5/image-libs.txt | tr '\n' ' ')
# Paths the extension ships; an image that had one would make `isotope ext`
# refuse it.
ext_paths='/usr/bin/fcitx5 /usr/bin/fcitx5-remote /usr/share/fcitx5 /usr/lib/isotope-ext/fcitx5
/usr/share/libime /usr/share/libhangul /usr/share/isotope-ext/fcitx5
/usr/share/dbus-1/services/org.fcitx.Fcitx5.service /usr/share/applications/org.fcitx.Fcitx5.desktop
/usr/share/licenses/fcitx5-extension'

# Each stage's lines, one file per stage, for the checks' greps.
S=$(mktemp -d)
trap 'rm -rf "$S"' EXIT
stage() { # stage FILE NAME
    awk -v s="$2" '$0 ~ "^FROM .* AS "s"$" {p=1; print; next} p && /^FROM / {exit} p' "$1" >"$S/$2"
}

# The profile.d file in a throwaway Alpine container: the variables it
# leaves for sway (the Wayland desktops) and for i3, with and without
# /usr/bin/fcitx5, and that a value already set wins.
profile_case() { # profile_case DESC SESSION(none: no fcitx5) PRESET EXPECTED
    local d="$1" session="$2" preset="$3" want="$4" got
    got=$(docker run --rm -v "$PWD/rootfs/common/$profile:/p.sh:ro" alpine:3.22 sh -c '
        session=$1 preset=$2
        mkdir -p /etc/isotope
        if [ "$session" != none ]; then
            printf "#!/bin/sh\n" >/usr/bin/fcitx5 && chmod +x /usr/bin/fcitx5
            printf "SESSION=%s\nDESKTOP_NAME=x\n" "$session" >/etc/isotope/session
        fi
        [ -n "$preset" ] && export XMODIFIERS="$preset"
        . /p.sh
        for v in XMODIFIERS GTK_IM_MODULE QT_IM_MODULE QT_IM_MODULES SDL_IM_MODULE _him_session _him_key; do
            eval "x=\${$v-unset}"; printf "%s=%s;" "$v" "$x"
        done' sh "$session" "$preset" 2>&1) || true
    if [ "$got" = "$want" ]; then pass "$d"; else bad "$d: got $got"; fi
}

# ---------------------------------------------------------------------------
repo_mode() {
    local C=rootfs/common
    check "the profile.d file parses" sh -n "$C/$profile"
    check "it sets nothing unless /usr/bin/fcitx5 is executable" grep -qx 'if \[ -x /usr/bin/fcitx5 \]; then' "$C/$profile"
    if command -v docker >/dev/null 2>&1; then
        profile_case "without fcitx5 the profile.d file sets nothing" none "" \
            "XMODIFIERS=unset;GTK_IM_MODULE=unset;QT_IM_MODULE=unset;QT_IM_MODULES=unset;SDL_IM_MODULE=unset;_him_session=unset;_him_key=unset;"
        profile_case "on a Wayland desktop: XIM for Xwayland, Qt's wayland module first, no GTK module" sway "" \
            "XMODIFIERS=@im=fcitx;GTK_IM_MODULE=unset;QT_IM_MODULE=unset;QT_IM_MODULES=wayland;fcitx;SDL_IM_MODULE=fcitx;_him_session=unset;_him_key=unset;"
        profile_case "on i3: the fcitx module where an app has one, XIM otherwise" i3 "" \
            "XMODIFIERS=@im=fcitx;GTK_IM_MODULE=fcitx:xim;QT_IM_MODULE=fcitx;QT_IM_MODULES=unset;SDL_IM_MODULE=fcitx;_him_session=unset;_him_key=unset;"
        profile_case "a value the user set wins" niri "@im=ibus" \
            "XMODIFIERS=@im=ibus;GTK_IM_MODULE=unset;QT_IM_MODULE=unset;QT_IM_MODULES=wayland;fcitx;SDL_IM_MODULE=fcitx;_him_session=unset;_him_key=unset;"
    else
        echo "SKIP: no docker, the profile.d file is not run"
    fi
    check "every variable the file exports is one isotope input-method start hands to D-Bus" sh -c "
        for v in \$(sed -n 's/.*export \([A-Z_]*\)=.*/\1/p' $C/$profile | sort -u); do
            grep -q \"\\\"\$v\\\"\" cmd/isotope/inputmethod.go || exit 1; done"
    check "sway starts it" grep -qx 'exec isotope input-method start' rootfs/sway/etc/sway/config.d/60-input-method.conf
    check "sway reads its config.d" grep -qx 'include /etc/sway/config.d/\*' rootfs/sway/etc/sway/config
    check "i3 starts it" grep -qx 'exec --no-startup-id isotope input-method start' rootfs/i3/etc/i3/config.d/50-input-method.conf
    check "i3 reads its config.d" grep -qx 'include /etc/i3/config.d/\*' rootfs/i3/etc/i3/config
    check "niri starts it" grep -qx 'spawn-at-startup "isotope" "input-method" "start"' rootfs/niri/etc/niri/config.kdl
    check "COSMIC starts it" sh -c "
        f=rootfs/cosmic/etc/xdg/autostart/isotope-input-method.desktop
        grep -qx 'Exec=isotope input-method start' \$f && grep -qx 'OnlyShowIn=COSMIC;' \$f && grep -qx 'NoDisplay=true' \$f"
    for f in rootfs/sway/usr/bin/start-desktop rootfs/i3/usr/bin/start-desktop rootfs/niri/usr/bin/isotope-niri-session; do
        check "$f reads the variables" grep -q '\. /etc/profile.d/isotope-input-method.sh' "$f"
    done
    check "isotope input-method is a command" grep -q 'case "input-method":' cmd/isotope/main.go
    check "the usage text lists it" grep -q '  input-method \[command\]' cmd/isotope/main.go
    check "the extension's version is its fcitx5 release" grep -qE '^ARG FCITX5_VERSION=[0-9]' extensions/fcitx5/Dockerfile
    check "the extension has a title and a description" sh -c "
        python3 -c 'import json,sys; d=json.load(open(sys.argv[1])); assert d[\"title\"] and d[\"description\"]' extensions/fcitx5/extension.json"
    check "the extension's build checks NEEDED against image-libs.txt" sh -c "
        grep -q 'COPY image-libs.txt /image-libs.txt' extensions/fcitx5/Dockerfile &&
        grep -q \"allowed=\\\" \\\$(grep -v -e '^#' -e '^\\\$' /image-libs.txt\" extensions/fcitx5/Dockerfile"
    check "the extension's libraries are its own, behind a RUNPATH" grep -q 'DCMAKE_INSTALL_RPATH=/usr/lib/isotope-ext/fcitx5' extensions/fcitx5/Dockerfile
    check "the extension carries no /etc and no autostart file" sh -c "
        grep -q 'rm -rf /output/etc' extensions/fcitx5/Dockerfile && grep -q 'DENABLE_XDGAUTOSTART=OFF' extensions/fcitx5/Dockerfile"
    check "the extension's test stage starts fcitx5 and asks it for pinyin, anthy and hangul" sh -c "
        grep -q 'AvailableInputMethods' extensions/fcitx5/Dockerfile &&
        grep -q 'for im in keyboard-us pinyin shuangpin anthy hangul' extensions/fcitx5/Dockerfile"
    check "the extensions workflow gives the build time" sh -c "
        awk '/^  build:/{b=1} b && /timeout-minutes:/{print \$2; exit}' .github/workflows/extensions.yml | awk '{exit !(\$1 >= 120)}'"
    check "Renovate moves fcitx5 and its engines together" sh -c "
        python3 -c 'import json; r=json.load(open(\"renovate.json\")); assert any(p.get(\"groupName\")==\"fcitx5\" and \"fcitx/fcitx5-chinese-addons\" in p[\"matchDepNames\"] for p in r[\"packageRules\"])'"
    stage Dockerfile server-config
    check "the server takes neither the variables nor a session" sh -c "! grep -q 'input-method' $S/server-config"
    check "the agent removes the i3 line and the variables, and checks" sh -c "
        grep -q 'rm -f /etc/i3/config.d/50-input-method.conf /etc/profile.d/isotope-input-method.sh' Dockerfile.agent &&
        grep -q \"/etc/i3 /etc/xdg/autostart /etc/profile.d | grep -q 'input-method'\" Dockerfile.agent"
    check "the user guide has the section, with the command and the key" sh -c "
        grep -qx '## Type Chinese, Japanese and Korean' site/guide/extensions.md &&
        grep -q 'isotope ext install fcitx5' site/guide/extensions.md && grep -q '\[\[Ctrl+Space\]\]' site/guide/extensions.md"
    check "AGENTS.md has the feature row" grep -q '^| Input methods |' AGENTS.md
    finish
}

# ---------------------------------------------------------------------------
image_mode() {
    local img="$1" flavor p lib
    flavor=$(docker run --rm --entrypoint sh "$img" -c 'sed -n "s/^FLAVOR=//p" /etc/isotope/release' || true)
    in_image() { # in_image DESC CMD
        if docker run --rm --entrypoint sh "$img" -c "$2" >/dev/null 2>&1; then pass "$1"; else bad "$1"; fi
    }
    in_image "isotope input-method is a command" "/usr/bin/isotope input-method help | grep -q 'set NAME'"
    in_image "without the extension, start does nothing and says nothing" \
        "out=\$(/usr/bin/isotope input-method start 2>&1) && [ -z \"\$out\" ]"
    in_image "without the extension, status says how to install it" \
        "/usr/bin/isotope input-method status | grep -q 'isotope ext install fcitx5'"
    for p in $ext_paths; do
        in_image "the image has none of the extension's paths: $p" "! test -e $p"
    done
    if [ "$flavor" = agent ] || [ "$flavor" = server ]; then
        in_image "$flavor: no input method variables" "! test -e /$profile"
        in_image "$flavor: no session line starts it" \
            "! grep -rhsv '^[[:space:]]*#' /etc/i3 /etc/sway /etc/niri /etc/xdg/autostart 2>/dev/null | grep -q 'input-method'"
        finish
    fi
    in_image "the variables file is there" "test -f /$profile"
    in_image "without the extension it sets nothing" \
        ". /$profile; [ -z \"\${XMODIFIERS:-}\${GTK_IM_MODULE:-}\${QT_IM_MODULE:-}\${QT_IM_MODULES:-}\${SDL_IM_MODULE:-}\" ]"
    case "$flavor" in
    sway) in_image "sway starts it" "grep -qx 'exec isotope input-method start' /etc/sway/config.d/60-input-method.conf" ;;
    i3) in_image "i3 starts it" "grep -qx 'exec --no-startup-id isotope input-method start' /etc/i3/config.d/50-input-method.conf" ;;
    niri) in_image "niri starts it" "grep -qx 'spawn-at-startup \"isotope\" \"input-method\" \"start\"' /etc/niri/config.kdl" ;;
    cosmic) in_image "COSMIC starts it" "grep -qx 'Exec=isotope input-method start' /etc/xdg/autostart/isotope-input-method.desktop" ;;
    *) bad "unknown flavor '$flavor'" ;;
    esac
    for lib in $libs; do
        [ "$lib" = libc.so ] && continue
        in_image "the image has $lib, which the extension takes from it" "test -e /usr/lib/$lib"
    done
    in_image "the image has the keyboard layouts the extension reads" "test -f /usr/share/X11/xkb/rules/evdev.xml"
    finish
}

case "${1:-}" in
--repo) repo_mode ;;
"" | -h | --help) sed -n '4,22p' "$0"; exit 2 ;;
*) image_mode "$1" ;;
esac
