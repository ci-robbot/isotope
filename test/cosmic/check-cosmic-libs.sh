#!/usr/bin/env bash
# Check that the COSMIC binaries in a cosmic image find every library and every
# symbol they need.
#
#   test/cosmic/check-cosmic-libs.sh IMAGE
#
# The COSMIC halves (the cosmic-shell and cosmic-apps stages) are built once,
# on the full image line, and the vm line uses the same binaries (see the
# Makefile, "built once, on the full line"). From Mesa they link only
# libgbm.so.1; EGL and Vulkan are opened at run time. This check proves that the
# binaries built against the full line's libraries load against the libraries
# of the image they ship in, on either line.
#
# musl's ldd is its dynamic loader in list mode. It loads every needed library
# and resolves every symbol now, as glibc's `ldd -r` does: a missing library
# prints "Error loading shared library", a missing symbol "Error relocating
# ...: symbol not found", and both exit non-zero. musl's ldd has no -r option.
set -euo pipefail
IMAGE="${1:?usage: $0 IMAGE}"

echo "==> COSMIC libraries: $IMAGE"

# Must be there: the compositor and panel (cosmic-shell) and a few apps
# (cosmic-apps), including the ones that link libgbm.so.1 (cosmic-comp,
# cosmic-workspaces, xdg-desktop-portal-cosmic) and libpipewire.
required="cosmic-comp cosmic-panel cosmic-workspaces cosmic-settings-daemon cosmic-settings cosmic-files cosmic-store cosmic-greeter xdg-desktop-portal-cosmic"

# The script runs in the image with BusyBox or coreutils sh. It prints one
# PASS/FAIL line per binary and exits 1 on any failure.
# shellcheck disable=SC2016
script='
fail=0
checked=""
for name in $REQUIRED; do
    f=$(command -v "$name" 2>/dev/null || true)
    [ -n "$f" ] || for d in /usr/libexec /usr/lib; do [ -x "$d/$name" ] && f="$d/$name" && break; done
    if [ -z "$f" ]; then
        echo "FAIL: $name is not in the image"; fail=1
    else
        checked="$checked $f"
    fi
done
# Every other COSMIC program in /usr/bin too (some are scripts).
for f in /usr/bin/cosmic-*; do
    [ -f "$f" ] || continue
    case " $checked " in *" $f "*) continue ;; esac
    [ "$(head -c 4 "$f" | od -An -c | tr -d " ")" = "177ELF" ] && checked="$checked $f"
done
for f in $checked; do
    if out=$(ldd "$f" 2>&1) && ! printf "%s\n" "$out" | grep -qE "not found|Error"; then
        echo "PASS: $f resolves"
    else
        echo "FAIL: $f does not resolve:"
        printf "%s\n" "$out" | grep -E "not found|Error" | sed "s/^/    /"
        fail=1
    fi
done
exit $fail
'

docker run --rm --entrypoint sh -e REQUIRED="$required" "$IMAGE" -c "$script"
