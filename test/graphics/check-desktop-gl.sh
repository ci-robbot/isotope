#!/usr/bin/env bash
# The checks are strings that the inner sh expands, on purpose.
# shellcheck disable=SC2016
# Check that a desktop image can run desktop OpenGL through EGL, the way
# Xwayland's glamor and GTK do it with libepoxy.
#
#   test/graphics/check-desktop-gl.sh IMAGE
#
# libepoxy takes the GL entry points of a desktop GL context made through EGL
# from libOpenGL.so.0 (glvnd) or libGL.so.1 (Mesa's GLX library), and aborts
# the process when neither is there. On the Wayland variants that killed
# Xwayland on every real GPU, so the COSMIC greeter showed only a cursor. A VM
# without 3D never shows it: glamor does not start on llvmpipe there.
#
# The Wayland variants (sway, cosmic, niri) get glvnd: libOpenGL.so.0 and
# libEGL.so.1 from glvnd, Mesa as the EGL vendor. i3 and the agent keep
# Mesa's own libGL.so.1 (GLX for XLibre). On every image, gl-probe.c makes a
# desktop GL context on llvmpipe (EGL surfaceless platform, no GPU and no
# display needed) and calls glGetString through libepoxy, and does the same
# with an OpenGL ES 2 context, which the compositors render with. The probes
# run as root and as an unprivileged user.
#
# It also checks Mesa's drivers per image line (the Gallium drivers in the
# megadriver, the Vulkan drivers and the device-select layer, the VA-API
# drivers), that every library they need resolves (ldd), and that a Vulkan
# instance made through the loader lists lavapipe (vk-probe.c).
#
# The probes are compiled with the Hadron toolchain the image is built with
# (Dockerfile.probe); they need only libc.
set -euo pipefail
IMAGE="${1:?usage: $0 IMAGE}"
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO="$(dirname "$(dirname "$HERE")")"
fail=0
check() {
    local desc="$1"; shift
    if docker run --rm --entrypoint sh "$IMAGE" -c "$*" >/dev/null 2>&1; then
        echo "PASS: $desc"
    else
        echo "FAIL: $desc"; fail=1
    fi
}
libs() { # DESC PATH: PATH exists and every library it needs resolves
    check "$1" "test -e $2 && ldd $2 >/tmp/ldd 2>&1 && ! grep -q 'not found' /tmp/ldd"
}

flavor="$(docker run --rm --entrypoint sh "$IMAGE" -c \
    'sed -n "s/^FLAVOR=//p" /etc/isotope/release' 2>/dev/null || true)"
gpu="$(docker run --rm --entrypoint sh "$IMAGE" -c \
    'sed -n "s/^GPU=//p" /etc/isotope/release' 2>/dev/null || true)"
echo "==> desktop GL: $IMAGE (flavor ${flavor:-unknown}, GPU ${gpu:-unknown})"

# Mesa 25.1 and later put every Gallium driver in one megadriver,
# /usr/lib/libgallium-<version>.so, with no per-driver *_dri.so files. A
# driver left out of the build is still named there, by a stub that prints
# "<driver>: driver missing" (DRM_DRIVER_DESCRIPTOR_STUB in Mesa's
# drm_helper.h, still there in 26.3): the message an old Radeon got before
# r600 was built. So a driver is in the build exactly when its stub's message
# is not in the file. The stubs are named after the kernel driver, so svga is
# "vmwgfx" and virgl is "virtio_gpu"; llvmpipe and softpipe have no stub, the
# GL probes below run llvmpipe.
gallium='$(ls /usr/lib/libgallium-*.so | head -n1)'
libs "Mesa's megadriver finds its libraries" "$gallium"
hw_drivers="iris crocus i915 radeonsi r600 r300 nouveau zink"
for d in vmwgfx virtio_gpu; do
    check "Mesa's $d driver is built (svga, virgl)" "! grep -aqF '$d: driver missing' $gallium"
done
# Vulkan: the loader, and per line the ICD files Mesa installs. Each names its
# driver library by an absolute path, which must resolve.
vk_sw="lvp virtio"
vk_hw="radeon intel intel_hasvk nouveau"
libs "the Vulkan loader finds its libraries" /usr/lib/libvulkan.so.1
# The Vulkan headers (over 20 MB of C++ headers) are for building the loader
# only; the images must not carry them.
check "the image has no Vulkan headers" '! test -e /usr/include/vulkan'
icd() { # NAME: the ICD file is there and its library resolves
    check "the Vulkan driver $1 is there and finds its libraries" \
        "f=/usr/share/vulkan/icd.d/$1_icd.x86_64.json && test -r \$f &&
         l=\$(grep -o '\"library_path\": *\"[^\"]*\"' \$f | cut -d'\"' -f4) && test -e \"\$l\" &&
         ldd \"\$l\" >/tmp/ldd 2>&1 && ! grep -q 'not found' /tmp/ldd"
}
for v in $vk_sw; do icd "$v"; done
# VA-API: libva, and the video drivers Mesa installs in /usr/lib/dri.
va_drivers="radeonsi r600 nouveau virtio_gpu"
case "$gpu" in
    full)
        for d in $hw_drivers; do
            check "the full line has Mesa's $d driver" \
                "! grep -aqF '$d: driver missing' $gallium"
        done
        # i915 links libdrm_intel, which links libpciaccess; r600 and r300 go
        # through the radeon winsys (libdrm only), libdrm_radeon is built for
        # anything else that asks for it. radeonsi and radv link
        # libdrm_amdgpu.
        libs "libdrm_intel finds its libraries (i915)" /usr/lib/libdrm_intel.so.1
        libs "libdrm_radeon finds its libraries" /usr/lib/libdrm_radeon.so.1
        libs "libdrm_amdgpu finds its libraries (radeonsi, radv)" /usr/lib/libdrm_amdgpu.so.1
        check "the megadriver links libdrm_intel (i915)" "ldd $gallium | grep -q libdrm_intel.so.1"
        for v in $vk_hw; do icd "$v"; done
        check "the device-select Vulkan layer is there" \
            'test -r /usr/share/vulkan/implicit_layer.d/VkLayer_MESA_device_select.json'
        libs "the device-select layer finds its libraries" /usr/lib/libVkLayer_MESA_device_select.so
        libs "libva finds its libraries" /usr/lib/libva.so.2
        libs "libva-drm finds its libraries" /usr/lib/libva-drm.so.2
        libs "libva-wayland finds its libraries" /usr/lib/libva-wayland.so.2
        for d in $va_drivers; do
            libs "the VA-API driver $d finds its libraries" "/usr/lib/dri/${d}_drv_video.so"
        done
        ;;
    vm)
        # The same test the other way: it proves the stub message is still
        # what Mesa prints, so the full line's checks above are not vacuous.
        for d in $hw_drivers; do
            check "the vm line has no $d driver (stub message present)" \
                "grep -aqF '$d: driver missing' $gallium"
        done
        for v in $vk_hw; do
            check "the vm line has no $v Vulkan driver" "! test -e /usr/share/vulkan/icd.d/${v}_icd.x86_64.json"
        done
        check "the vm line has no VA-API driver" '! ls /usr/lib/dri/*_drv_video.so >/dev/null 2>&1'
        check "the vm line has no device-select layer" \
            '! test -e /usr/share/vulkan/implicit_layer.d/VkLayer_MESA_device_select.json'
        ;;
    *)
        # /etc/isotope/release names the line on every desktop image; without
        # it both per-line blocks above would be skipped in silence.
        echo "FAIL: /etc/isotope/release has no GPU=full or GPU=vm line (got '$gpu')"; fail=1
        ;;
esac

libs "libepoxy finds its libraries" /usr/lib/libepoxy.so.0
libs "libEGL finds its libraries" /usr/lib/libEGL.so.1
case "$flavor" in
    i3|agent)
        libs "Mesa's libGL.so.1 (GLX) finds its libraries" /usr/lib/libGL.so.1
        ;;
    *)
        libs "glvnd's libOpenGL.so.0 finds its libraries" /usr/lib/libOpenGL.so.0
        libs "glvnd's libGLESv2.so.2 finds its libraries" /usr/lib/libGLESv2.so.2
        libs "Mesa's EGL vendor library finds its libraries" /usr/lib/libEGL_mesa.so.0
        check "Mesa is a glvnd EGL vendor" \
            'grep -q "\"library_path\" *: *\"libEGL_mesa.so.0\"" /usr/share/glvnd/egl_vendor.d/50_mesa.json'
        check "libEGL.so.1 is glvnd's, not a second Mesa libEGL" \
            'ldd /usr/lib/libEGL.so.1 | grep -q libGLdispatch.so.0'
        # glvnd reads the vendor files as the session user (cosmic-greeter,
        # the desktop user): with a directory that only root can enter it
        # finds no vendor, and the compositor gets no EGL platform.
        check "the glvnd vendor directories and file are world-readable" \
            'for d in /usr/share/glvnd /usr/share/glvnd/egl_vendor.d; do
                 case "$(stat -c %A "$d")" in d??????r?x) ;; *) exit 1 ;; esac
             done
             case "$(stat -c %A /usr/share/glvnd/egl_vendor.d/50_mesa.json)" in
                 -??????r??) ;; *) exit 1 ;;
             esac'
        ;;
esac

# The software-rendering fallback (test/graphics/check-gpu-fallback.sh checks
# the scripts; this checks what the image ships). In a container there is no
# GPU, so the probe must answer exit 2 ("no DRM device" or a card it cannot
# open), with one line and nothing on stderr.
libs "isotope-gpu-probe finds its libraries" /usr/bin/isotope-gpu-probe
check "isotope-gpu-probe answers exit 2 without a GPU, silently" \
    '/usr/bin/isotope-gpu-probe >/tmp/out 2>/tmp/err; [ $? = 2 ] && [ "$(wc -l </tmp/out)" = 1 ] && [ ! -s /tmp/err ]'
check "the session hook is there" 'test -r /usr/libexec/isotope-gpu-env && test -r /etc/profile.d/isotope-gpu.sh'
check "the libraries the probe dlopens are there" \
    'for l in libgbm.so.1 libEGL.so.1 libGLESv2.so.2; do test -e /usr/lib/$l || exit 1; done'
case "$flavor" in
    sway)   check "sway starts the notice" 'grep -qx "exec isotope-gpu-notice" /etc/sway/config' ;;
    i3)     check "i3 starts the notice" 'grep -qx "exec --no-startup-id isotope-gpu-notice" /etc/i3/config' ;;
    niri)
        check "niri starts the notice" 'grep -qx "spawn-at-startup \"isotope-gpu-notice\"" /etc/niri/config.kdl'
        check "niri's session entry goes through the GPU check" \
            'grep -qx "Exec=isotope-niri-session" /usr/share/wayland-sessions/niri.desktop && test -x /usr/bin/isotope-niri-session && test -x /usr/bin/niri-session'
        ;;
    cosmic)
        check "COSMIC starts the notice" 'grep -qx "Exec=isotope-gpu-notice" /etc/xdg/autostart/isotope-gpu-notice.desktop'
        check "COSMIC's login screen gets the fallback" \
            'grep -qx "command = \"isotope-greeter-start\"" /etc/greetd/cosmic-greeter.toml && test -x /usr/bin/isotope-greeter-start && test -x /usr/bin/cosmic-greeter-start'
        ;;
    agent)  check "the agent has no notice" '! test -e /usr/bin/isotope-gpu-notice && ! grep -rqs isotope-gpu-notice /etc/i3' ;;
esac
case "$flavor" in
    agent) ;;
    *) check "isotope-gpu-notice is there" 'test -x /usr/bin/isotope-gpu-notice' ;;
esac

tmp="$(mktemp -d)"
chmod 755 "$tmp" # the unprivileged probe run must reach it
trap 'rm -rf "$tmp"' EXIT
hadron="$(awk -F= '/^ARG HADRON_VERSION=/{print $2; exit}' "$REPO/Dockerfile")"
if ! docker build -q --build-arg HADRON_VERSION="$hadron" -f "$HERE/Dockerfile.probe" \
        --output "type=local,dest=$tmp" "$HERE" >/dev/null; then
    echo "FAIL: could not build gl-probe and vk-probe"; exit 1
fi
probe() { # DESC USER PROBE [ARGS]: run gl-probe or vk-probe on llvmpipe in the image as USER
    local desc="$1" user="$2" bin="$3" out rc
    shift 3
    set +e
    out="$(docker run --rm --user "$user" --entrypoint "/probe/$bin" -v "$tmp:/probe:ro" \
        -e EGL_PLATFORM=surfaceless -e LIBGL_ALWAYS_SOFTWARE=1 \
        -e MESA_SHADER_CACHE_DISABLE=true "$IMAGE" "$@" 2>&1)"
    rc=$?
    set -e
    printf '%s\n' "$out" | while IFS= read -r l; do echo "    $l"; done
    if [ "$rc" -eq 0 ]; then
        echo "PASS: $desc"
    else
        echo "FAIL: $desc (exit $rc)"; fail=1
    fi
}
# As root, and as an unprivileged user (65534, nobody), like the greeter and
# the desktop session: a GL file that only root can read fails only there.
# vk-probe (vk-probe.c) creates a Vulkan instance through the loader and must
# find lavapipe among the devices: the loader, the ICD files and a Mesa
# Vulkan driver work together, on both lines.
for user in 0:0 65534:65534; do
    probe "an OpenGL ES 2 context through EGL and libepoxy answers glGetString (uid ${user%%:*})" "$user" gl-probe gles
    probe "a desktop GL context through EGL and libepoxy answers glGetString (uid ${user%%:*})" "$user" gl-probe
    probe "a Vulkan instance lists lavapipe (uid ${user%%:*})" "$user" vk-probe
done
exit $fail
