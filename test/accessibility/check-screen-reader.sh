#!/usr/bin/env bash
# The checks are strings that the inner sh expands, on purpose; the fakes' helpers are called through check().
# shellcheck disable=SC2016,SC2317
# The screen reader: the `screen-reader` extension (Orca with speech), the
# accessibility bus in the COSMIC and niri images, and isotope-screen-reader,
# the switch behind Super+Alt+S and the Isotope menu (see
# docs/superpowers/specs/2026-11-24-screen-reader-design.md).
#
#   test/accessibility/check-screen-reader.sh --repo   the tree: the switch
#       with fake busctl and systemctl on each desktop, the key bindings
#       and the sheets, the menu entry, the bus in the COSMIC and niri
#       stages, the extension's pins against the image's, the agent's
#       removal (CI)
#   test/accessibility/check-screen-reader.sh IMAGE    a built image: on
#       COSMIC and niri the bus, the switch and every library the extension
#       takes from the image (extensions/screen-reader/image-libs.txt); on
#       sway and i3 the switch refuses; no file of the extension in any
#       image; none of it on the agent and the server
#
# The extension's own build and offline tests are its Dockerfile's test
# stage (.github/workflows/extensions.yml). Speech on a real desktop is on
# the design note's manual list.
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
    if [ "$fail" -eq 0 ]; then echo "all screen reader checks passed"; fi
    exit "$fail"
}

ext=extensions/screen-reader
script=rootfs/common/usr/bin/isotope-screen-reader

# Each stage's lines, one file per stage, for the checks' greps.
S=$(mktemp -d)
trap 'rm -rf "$S"' EXIT
stage() { # stage FILE NAME
    awk -v s="$2" '$0 ~ "^FROM .* AS "s"$" {p=1; print; next} p && /^FROM / {exit} p' "$1" >"$S/$2"
}
arg() { # arg FILE NAME: the value of ARG NAME= (the first one)
    sed -n "s/^ARG $2=//p" "$1" | head -n 1
}

# ---------------------------------------------------------------------------
# The switch, run with fakes: busctl keeps the bus property in a file,
# systemctl the unit's state, and both log what they were asked.
toggle_tests() {
    local t log
    t=$(mktemp -d)
    log=$t/log
    mkdir -p "$t/bin"
    cat >"$t/bin/busctl" <<'EOF'
#!/bin/sh
echo "busctl $*" >>"$FAKE_LOG"
case "$*" in
    *get-property*ScreenReaderEnabled*) echo "b $(cat "$FAKE_SR" 2>/dev/null || echo false)" ;;
    *set-property*ScreenReaderEnabled*) echo "$8" >"$FAKE_SR" ;;
esac
EOF
    cat >"$t/bin/systemctl" <<'EOF'
#!/bin/sh
echo "systemctl $*" >>"$FAKE_LOG"
case "$*" in
    *is-active*) [ "$(cat "$FAKE_UNIT" 2>/dev/null)" = active ] ;;
    *start*) [ -e "$FAKE_UNIT_MISSING" ] && exit 5; echo active >"$FAKE_UNIT" ;;
    *stop*) echo inactive >"$FAKE_UNIT" ;;
    *daemon-reload*) rm -f "$FAKE_UNIT_MISSING" ;;
esac
EOF
    printf '#!/bin/sh\nexit 0\n' >"$t/orca"
    chmod +x "$t/bin/"* "$t/orca"
    run() { # run DESKTOP ARG: the switch's exit status
        printf 'DESKTOP_NAME=%s\n' "$1" >"$t/session"
        PATH="$t/bin:$PATH" FAKE_LOG="$log" FAKE_SR="$t/sr" FAKE_UNIT="$t/unit" \
            FAKE_UNIT_MISSING="$t/missing" ISOTOPE_SESSION_FILE="$t/session" \
            ISOTOPE_ORCA="${ORCA:-$t/orca}" sh "$script" "$2" >"$t/out" 2>&1
    }
    reset() { : >"$log"; rm -f "$t/sr" "$t/unit" "$t/missing"; }
    has() { grep -q -- "$1" "$log"; }
    local sr

    reset; run niri on; check "niri: on sets IsEnabled" has 'set-property org.a11y.Bus /org/a11y/bus org.a11y.Status IsEnabled b true'
    check "niri: on sets ScreenReaderEnabled" has 'ScreenReaderEnabled b true'
    check "niri: on starts orca.service" has 'systemctl --user start orca.service'
    run niri status; check "niri: status says on" grep -qx on "$t/out"
    run niri toggle; check "niri: toggle turns it off and stops orca.service" has 'systemctl --user stop orca.service'
    sr=$(cat "$t/sr"); check "niri: off clears ScreenReaderEnabled" [ "$sr" = false ]
    reset; echo active >"$t/unit"; run niri toggle
    check "niri: toggle stops an Orca started some other way" has 'stop orca.service'
    reset; touch "$t/missing"; run niri on
    check "niri: a unit the manager has not seen yet starts after a reload" has 'daemon-reload'
    check "niri: ...and is then running" grep -qx active "$t/unit"

    reset; echo true >"$t/sr"; run niri restore
    check "niri: restore starts Orca when the setting is on" has 'start orca.service'
    reset; run niri restore
    check "niri: restore does nothing when it is off" sh -c "! grep -q 'start orca.service' '$log'"

    reset; run COSMIC on; check "COSMIC: on sets ScreenReaderEnabled (cosmic-session starts Orca)" has 'ScreenReaderEnabled b true'
    check "COSMIC: on does not start a unit" sh -c "! grep -q 'systemctl' '$log'"
    run COSMIC toggle; sr=$(cat "$t/sr"); check "COSMIC: toggle turns it off" [ "$sr" = false ]
    reset; run COSMIC restore; check "COSMIC: restore is cosmic-session's job" sh -c "! grep -q 'set-property\|systemctl' '$log'"

    for d in Sway i3; do
        reset
        if run "$d" on; then bad "$d: on refuses"; else pass "$d: on refuses"; fi
        check "$d: and says it works on COSMIC and niri" grep -q 'works on the COSMIC and niri desktops' "$t/out"
        check "$d: and changes nothing" sh -c "! grep -q 'set-property\|systemctl' '$log'"
        run "$d" restore; check "$d: restore is quiet" [ ! -s "$t/out" ]
    done

    reset
    if ORCA=$t/none run niri on; then bad "without the extension, on refuses"; else pass "without the extension, on refuses"; fi
    check "...and says how to install it" grep -q 'isotope ext install screen-reader' "$t/out"
    check "...and changes nothing" sh -c "! grep -q 'set-property\|systemctl' '$log'"
    ORCA=$t/none run niri restore; check "without the extension, restore is quiet" [ ! -s "$t/out" ]
    if run niri bogus; then bad "an unknown word is refused"; else pass "an unknown word is refused"; fi
    rm -rf "$t"
}

# ---------------------------------------------------------------------------
repo_mode() {
    local v
    check "the switch is POSIX sh" sh -n "$script"
    check "the switch is executable" test -x "$script"
    toggle_tests

    # Keys, sheets, menu.
    check "niri: Super+Alt+S runs the switch, also when locked" grep -qF 'Super+Alt+S allow-when-locked=true hotkey-overlay-title="Screen Reader On/Off" { spawn "isotope-screen-reader" "toggle"; }' rootfs/niri/etc/niri/config.kdl
    check "niri: no upstream pkill orca binding is left" sh -c "! grep -q 'pkill orca' rootfs/niri/etc/niri/config.kdl"
    check "niri: the session restores it at login" grep -qx 'spawn-at-startup "isotope-screen-reader" "restore"' rootfs/niri/etc/niri/config.kdl
    check "niri: the sheet lists Super+Alt+S" grep -q 'Super + Alt + S	Screen reader on and off' rootfs/niri/usr/share/isotope/shortcuts.txt
    check "COSMIC: the sheet lists COSMIC's own Super+Alt+S" grep -q 'Super + Alt + S	Screen reader on and off' rootfs/cosmic/usr/share/isotope/shortcuts.txt
    check "sway and i3 bind no screen reader key" sh -c "! grep -rq 'isotope-screen-reader' rootfs/sway rootfs/i3"
    check "the Isotope menu has the entry on COSMIC and niri" sh -c "
        grep -qF 'Screen reader\${tab}' rootfs/common/usr/bin/isotope-menu &&
        grep -qF '\"Screen reader\") exec isotope-screen-reader toggle ;;' rootfs/common/usr/bin/isotope-menu"

    # The image: the accessibility bus on COSMIC and niri, the switch, the agent.
    stage Dockerfile cosmic-rootfs
    stage Dockerfile niri-rootfs
    stage Dockerfile i3-rootfs
    stage Dockerfile server-config
    for s in cosmic-rootfs niri-rootfs i3-rootfs; do
        check "$s has the accessibility bus (at-spi2-core)" grep -qx 'COPY --from=at-spi2-core /at-spi2-core /' "$S/$s"
    done
    check "the server takes no switch" sh -c "! grep -q 'isotope-screen-reader' $S/server-config"
    check "the agent removes the switch, and checks" sh -c "
        grep -q 'rm -f /usr/bin/isotope-screen-reader' Dockerfile.agent &&
        grep -q '! test -e /usr/bin/isotope-screen-reader' Dockerfile.agent"

    # The extension.
    check "the extension has a title and a description" sh -c "
        grep -q '\"title\": \"Screen reader\"' $ext/extension.json && grep -q '\"description\"' $ext/extension.json"
    v=$(arg $ext/Dockerfile SCREEN_READER_VERSION)
    check "the extension's version is Orca's ($v)" sh -c "[ -n '$v' ] && grep -q 'orca-\${SCREEN_READER_VERSION}.tar.xz' $ext/Dockerfile"
    for n in GLIB_VERSION PCRE2_VERSION PIPEWIRE_VERSION LIBSNDFILE_VERSION; do
        check "$n is the image's ($(arg Dockerfile $n))" [ "$(arg $ext/Dockerfile $n)" = "$(arg Dockerfile $n)" ]
    done
    check "gobject-introspection is the series of the image's GLib" \
        [ "$(arg $ext/Dockerfile GOBJECT_INTROSPECTION_VERSION | cut -d. -f2)" = "$(arg Dockerfile GLIB_VERSION | cut -d. -f2)" ]
    check "libatspi has the a11y-manager device (2.56 or later)" sh -c "
        v=\$(sed -n 's/^ARG ATSPI_VERSION=//p' $ext/Dockerfile); [ \"\$(printf '2.56\n%s\n' \"\$v\" | sort -V | head -n 1)\" = 2.56 ]"
    check "the extension's GTK typelibs describe GTK 3, the image's" sh -c "
        grep -q 'Gtk-3.0' $ext/Dockerfile && [ \"\$(sed -n 's/^ARG GTK3_VERSION=//p' Dockerfile | head -n 1 | cut -d. -f1)\" = 3 ]"
    check "the build stage reads image-libs.txt, and so does the test stage" sh -c "
        grep -qx 'COPY image-libs.txt /build/image-libs.txt' $ext/Dockerfile && grep -q '/image-runtime' $ext/Dockerfile"
    check "image-libs.txt has only linked and typelib lines" sh -c "
        ! grep -v '^#' $ext/image-libs.txt | grep -v '^\$' | grep -qvE '^(linked|typelib) lib[A-Za-z0-9_+.-]+\.so\.[0-9]+\$'"
    check "the extension ships /usr/bin/orca and orca.service, nothing in /etc" sh -c "
        grep -q 'ln -s ../lib/isotope-ext/screen-reader/bin/orca /output/usr/bin/orca' $ext/Dockerfile &&
        grep -q '/output/usr/lib/systemd/user/orca.service' $ext/Dockerfile"
    finish
}

# ---------------------------------------------------------------------------
image_mode() {
    local img="$1" flavor desktop lib
    flavor=$(docker run --rm --entrypoint sh "$img" -c 'sed -n "s/^FLAVOR=//p" /etc/isotope/release' || true)
    desktop=$(docker run --rm --entrypoint sh "$img" -c '. /etc/isotope/session 2>/dev/null; echo "${DESKTOP_NAME:-}"' || true)
    in_image() { # in_image DESC CMD
        if docker run --rm --entrypoint sh "$img" -c "$2" >/dev/null 2>&1; then pass "$1"; else bad "$1"; fi
    }
    in_image "no file of the extension is in the image" \
        '! test -e /usr/bin/orca && ! test -e /usr/lib/isotope-ext/screen-reader &&
         ! test -e /usr/lib/systemd/user/orca.service && ! test -e /usr/share/licenses/screen-reader-extension'
    if [ "$flavor" = agent ] || [ "$flavor" = server ]; then
        in_image "$flavor: no screen reader switch" "! test -e /usr/bin/isotope-screen-reader"
        finish
    fi
    in_image "the switch is there" "test -x /usr/bin/isotope-screen-reader"
    case "$desktop" in
    COSMIC|niri)
        in_image "$desktop: the accessibility bus launcher and registry" \
            "test -x /usr/lib/at-spi-bus-launcher && test -x /usr/lib/at-spi2-registryd &&
             grep -q '^Exec=/usr/lib/at-spi-bus-launcher' /usr/share/dbus-1/services/org.a11y.Bus.service &&
             test -f /usr/share/dbus-1/accessibility-services/org.a11y.atspi.Registry.service &&
             test -f /usr/lib/systemd/user/at-spi-dbus-bus.service"
        in_image "$desktop: the bus launcher finds its libraries" "! ldd /usr/lib/at-spi-bus-launcher 2>&1 | grep -qE 'not found|Error loading'"
        in_image "$desktop: the setting is kept in GSettings" \
            "grep -q 'screen-reader-enabled' /usr/share/glib-2.0/schemas/org.gnome.desktop.a11y.applications.gschema.xml"
        while read -r kind lib; do
            case "$kind" in linked|typelib) ;; *) continue ;; esac
            in_image "$desktop: the extension's $kind library $lib" "test -e /usr/lib/$lib || test -e /lib/$lib"
        done <extensions/screen-reader/image-libs.txt
        in_image "$desktop: the Isotope menu offers it" "grep -qF '\"Screen reader\") exec isotope-screen-reader toggle' /usr/bin/isotope-menu"
        if [ "$desktop" = niri ]; then
            in_image "niri: Super+Alt+S and the login restore" \
                "grep -q 'spawn \"isotope-screen-reader\" \"toggle\"' /etc/niri/config.kdl &&
                 grep -qx 'spawn-at-startup \"isotope-screen-reader\" \"restore\"' /etc/niri/config.kdl"
        fi
        ;;
    *)
        in_image "$desktop: the switch refuses, and says where it works" \
            "! isotope-screen-reader on 2>/tmp/err && grep -q 'COSMIC and niri' /tmp/err"
        ;;
    esac
    finish
}

case "${1:-}" in
--repo) repo_mode ;;
"" | -h | --help) sed -n '4,23p' "$0"; exit 2 ;;
*) image_mode "$1" ;;
esac
