#!/bin/sh
# Check the software-rendering fallback without a GPU: the session hook
# (rootfs/usr/libexec/isotope-gpu-env and /etc/profile.d/isotope-gpu.sh), the
# notice (rootfs/usr/bin/isotope-gpu-notice), and that every variant wires
# them before its compositor and at session start. isotope-gpu-probe,
# busctl, systemctl and id are fakes.
#
#   test/graphics/check-gpu-fallback.sh
#
# The compiled probe itself is checked on the built images by
# check-desktop-gl.sh.
# The sh -c bodies are single-quoted on purpose: their own shell expands them.
# shellcheck disable=SC2016,SC2317
set -eu
root=$(cd "$(dirname "$0")/../.." && pwd)
env_hook=$root/rootfs/usr/libexec/isotope-gpu-env
profile=$root/rootfs/etc/profile.d/isotope-gpu.sh
notice=$root/rootfs/usr/bin/isotope-gpu-notice
fail=0
ok()  { echo "ok   $1"; }
bad() { echo "FAIL $1"; fail=1; }
check() { # name, command...
    name=$1; shift
    if "$@" >/dev/null 2>&1; then ok "$name"; else bad "$name"; fi
}

t=$(mktemp -d)
trap 'rm -rf "$t"' EXIT
mkdir -p "$t/bin" "$t/run"
log=$t/log
# probe: answers with $PROBE_LINE and $PROBE_RC, logs each run.
printf '%s\n' '#!/bin/sh' 'echo probe >> "$LOG"' 'echo "mesa says hello" >&2' \
    'printf "%s\n" "$PROBE_LINE"' 'exit "$PROBE_RC"' > "$t/bin/isotope-gpu-probe"
printf '%s\n' '#!/bin/sh' 'echo "busctl $*" >> "$LOG"' > "$t/bin/busctl"
# systemctl --user: show-environment prints $SYSTEMD_ENV, the rest is logged.
printf '%s\n' '#!/bin/sh' \
    'if [ "$*" = "--user show-environment" ]; then printf "%s\n" "${SYSTEMD_ENV:-}"; else echo "systemctl $*" >> "$LOG"; fi' \
    > "$t/bin/systemctl"
# id -u answers $FAKE_UID, so the root check does not depend on who runs this.
printf '%s\n' '#!/bin/sh' 'echo "${FAKE_UID:-1000}"' > "$t/bin/id"
chmod +x "$t/bin/"*

VARS="LIBGL_ALWAYS_SOFTWARE GBM_ALWAYS_SOFTWARE WLR_RENDERER_ALLOW_SOFTWARE ISOTOPE_SOFTWARE_RENDERING"
# hook RC LINE [VAR=VALUE...]: source the hook twice, under set -e, in a fresh
# shell with the fake probe, and print the variables it sets.
hook() {
    rc=$1 line=$2; shift 2
    env -i PATH="$t/bin:/usr/bin:/bin" LOG="$log" XDG_RUNTIME_DIR="$t/run" \
        ISOTOPE_GPU_PROBE="$t/bin/isotope-gpu-probe" \
        PROBE_RC="$rc" PROBE_LINE="$line" "$@" sh -ec '
            . "$1"
            for v in $2 ISOTOPE_GPU_CHECKED; do
                eval "echo \"$v=\${$v:-}\""
            done
            . "$1"
            echo "survived set -e"' sh "${HOOK:-$env_hook}" "$VARS"
}
has() { # LINE OUTPUT
    printf '%s\n' "$2" | grep -qx "$1"
}
marker=$t/run/isotope/software-rendering
stamp=$t/run/isotope/software-rendering-noticed

: > "$log"
mkdir -p "$t/run/isotope" && : > "$stamp"
out=$(hook 1 "hardware radeon: no Mesa driver for this GPU (EGL failed)" 2>&1)
for v in $VARS; do
    check "probe exit 1: $v=1" has "$v=1" "$out"
done
check "probe exit 1: the hook survives set -e" has "survived set -e" "$out"
check "probe exit 1: the marker holds the probe's line" \
    grep -qx 'hardware radeon: no Mesa driver for this GPU (EGL failed)' "$marker"
check "probe exit 1: a new marker clears the notice's stamp" test ! -e "$stamp"
check "the probe runs once per environment, even when sourced twice" \
    test "$(grep -c probe "$log")" = 1
case "$out" in *"mesa says hello"*) bad "the probe's stderr reaches the console" ;; *) ok "the probe's stderr is silenced" ;; esac

# A crash (any status but 0 and 2) is a broken driver: software too.
rm -f "$marker"
out=$(hook 139 "")
check "probe crash: GBM_ALWAYS_SOFTWARE=1" has 'GBM_ALWAYS_SOFTWARE=1' "$out"
check "probe crash: a marker with a generic line" grep -qx 'hardware unknown: no Mesa driver for this GPU' "$marker"

# Exit 0 after an earlier fallback takes it back: in this environment, and in
# the systemd user manager (niri-session's import-environment).
: > "$stamp"
: > "$log"
out=$(hook 0 "AMD CEDAR" LIBGL_ALWAYS_SOFTWARE=1 GBM_ALWAYS_SOFTWARE=1 WLR_RENDERER_ALLOW_SOFTWARE=1 ISOTOPE_SOFTWARE_RENDERING=1 \
    SYSTEMD_ENV="$(printf 'HOME=/home/a\nLIBGL_ALWAYS_SOFTWARE=1\nISOTOPE_SOFTWARE_RENDERING=1')")
for v in $VARS; do
    check "probe exit 0: an earlier $v is taken back" has "$v=" "$out"
done
check "probe exit 0: the hook survives set -e" has "survived set -e" "$out"
check "probe exit 0: no marker and no stamp" sh -c 'test ! -e "$1" && test ! -e "$2"' sh "$marker" "$stamp"
check "probe exit 0: the user manager's stale variables are unset" \
    grep -qx "systemctl --user unset-environment $VARS" "$log"
: > "$log"
hook 0 "AMD CEDAR" SYSTEMD_ENV="HOME=/home/a" >/dev/null
check "probe exit 0: a user manager with no fallback is left alone" sh -c '! grep -q unset-environment "$1"' sh "$log"
# ... but a LIBGL_ALWAYS_SOFTWARE the user set stays.
out=$(hook 0 "AMD CEDAR" LIBGL_ALWAYS_SOFTWARE=1)
check "probe exit 0: the user's own LIBGL_ALWAYS_SOFTWARE stays" has 'LIBGL_ALWAYS_SOFTWARE=1' "$out"

mkdir -p "$t/run/isotope" && echo "hardware radeon: old" > "$marker"
: > "$log"
out=$(hook 2 "no DRM device" SYSTEMD_ENV="$(printf 'GBM_ALWAYS_SOFTWARE=1\nISOTOPE_SOFTWARE_RENDERING=1')")
check "probe exit 2: no fallback" has 'LIBGL_ALWAYS_SOFTWARE=' "$out"
check "probe exit 2: an old marker goes" test ! -e "$marker"
check "probe exit 2: the user manager's stale variables are unset" \
    grep -qx "systemctl --user unset-environment $VARS" "$log"

# The marker is never written through a link.
mkdir -p "$t/elsewhere"
ln -sf "$t/elsewhere/target" "$marker"
hook 1 "hardware radeon: x" >/dev/null
check "the marker replaces a link instead of writing through it" \
    sh -c 'test ! -L "$1" && test -s "$1" && test ! -e "$2"' sh "$marker" "$t/elsewhere/target"

# The profile.d file, pointed at this tree's hook: the hook in a local
# login, nothing over SSH or for root.
sed "s|/usr/libexec/isotope-gpu-env|$env_hook|g" "$profile" > "$t/profile.sh"
check "profile.d names /usr/libexec/isotope-gpu-env" grep -q '\. /usr/libexec/isotope-gpu-env' "$profile"
: > "$log"
HOOK=$t/profile.sh hook 1 "hardware radeon: x" >/dev/null
check "profile.d runs the hook in a local login" test "$(grep -c probe "$log")" = 1
: > "$log"
HOOK=$t/profile.sh hook 1 "hardware radeon: x" SSH_CONNECTION="10.0.0.1 1 10.0.0.2 22" >/dev/null
check "profile.d does nothing over SSH" test ! -s "$log"
: > "$log"
HOOK=$t/profile.sh hook 1 "hardware radeon: x" FAKE_UID=0 >/dev/null
check "profile.d does nothing for root (sudo -i, su -)" test ! -s "$log"

# The notice.
note() {
    env -i PATH="$t/bin:/usr/bin:/bin" LOG="$log" XDG_RUNTIME_DIR="$t/run" \
        ISOTOPE_RELEASE="$t/release" sh "$notice" "$@"
}
printf 'FLAVOR=sway\nGPU=full\n' > "$t/release"
rm -rf "$t/run/isotope"
: > "$log"
check "notice: nothing without the marker" test -z "$(note --print)"
note
check "notice: no notification without the marker" test ! -s "$log"
mkdir -p "$t/run/isotope"
echo "hardware radeon: no Mesa driver for this GPU (EGL failed)" > "$marker"
text=$(note --print)
check "notice: titled Software rendering" test "$(printf '%s\n' "$text" | head -n1)" = "Software rendering"
check "notice: names the kernel driver" has '.*GPU (radeon), so the processor draws the screen.*' "$text"
printf 'FLAVOR=sway\nGPU=vm\n' > "$t/release"
check "notice: the vm image line on a real GPU points to the standard image" \
    has '.*image is for virtual machines.*standard image in Isotope System, Desktop & image.*' "$(note --print)"
printf 'FLAVOR=sway\nGPU=full\n' > "$t/release"
echo "firmware simple-framebuffer: software (llvmpipe)" > "$marker"
check "notice: the firmware framebuffer gets the nomodeset text" has '.*nomodeset.*' "$(note --print)"
echo 'hardware x$(reboot): no Mesa driver' > "$marker"
check "notice: a strange driver name is left out" has ".*GPU, so the processor.*" "$(note --print)"
for vm in "virtual virtio_gpu: software (llvmpipe)" "virtual bochs-drm: software (llvmpipe)" "virtual simple-framebuffer: software (llvmpipe)"; do
    echo "$vm" > "$marker"
    rm -f "$stamp"
    : > "$log"
    note
    check "notice: nothing on a virtual GPU ($vm)" sh -c 'test ! -s "$1" && test -z "$2"' sh "$log" "$(note --print)"
done
echo "hardware radeon: no Mesa driver" > "$marker"
rm -f "$stamp"
: > "$log"
note
note
check "notice: one notification per session" test "$(grep -c 'busctl --user call org.freedesktop.Notifications' "$log")" = 1
check "notice: through Notify, with the server's own timeout (-1)" \
    grep -q 'Notify susssasa{sv}i Isotope 0 video-display Software rendering .* 0 0 -1$' "$log"

# Wiring: every variant sources the hook before its compositor and starts
# the notice at session start.
check "sway: start-desktop sources the hook" grep -q '^\[ -r /usr/libexec/isotope-gpu-env \] && \. /usr/libexec/isotope-gpu-env' "$root/rootfs-sway/usr/bin/start-desktop"
check "i3: start-desktop sources the hook" grep -q '^\[ -r /usr/libexec/isotope-gpu-env \] && \. /usr/libexec/isotope-gpu-env' "$root/rootfs-i3/usr/bin/start-desktop"
check "COSMIC login screen: greetd runs isotope-greeter-start" grep -qx 'command = "isotope-greeter-start"' "$root/rootfs-cosmic/etc/greetd/cosmic-greeter.toml"
check "COSMIC login screen: isotope-greeter-start sources the hook, then cosmic-greeter-start" \
    sh -c 'grep -q "\. /usr/libexec/isotope-gpu-env" "$1" && grep -q "^exec cosmic-greeter-start" "$1" && test -x "$1"' sh "$root/rootfs-cosmic/usr/bin/isotope-greeter-start"
check "COSMIC session: /etc/profile.d/isotope-gpu.sh (greetd sources /etc/profile first)" test -r "$profile"
check "niri: the session entry runs isotope-niri-session" grep -qx 'Exec=isotope-niri-session' "$root/rootfs-niri/usr/share/wayland-sessions/niri.desktop"
check "niri: isotope-niri-session sources the hook, then niri-session" \
    sh -c 'grep -q "\. /usr/libexec/isotope-gpu-env" "$1" && grep -q "^exec niri-session" "$1" && test -x "$1"' sh "$root/rootfs-niri/usr/bin/isotope-niri-session"
check "sway: the config starts the notice" sh -c 'grep -v "^[[:space:]]*#" "$1" | grep -qx "exec isotope-gpu-notice"' sh "$root/rootfs-sway/etc/sway/config"
check "i3: the config starts the notice" sh -c 'grep -v "^[[:space:]]*#" "$1" | grep -qx "exec --no-startup-id isotope-gpu-notice"' sh "$root/rootfs-i3/etc/i3/config"
check "niri: spawn-at-startup starts the notice" sh -c 'grep -v "^[[:space:]]*//" "$1" | grep -qx "spawn-at-startup \"isotope-gpu-notice\""' sh "$root/rootfs-niri/etc/niri/config.kdl"
check "COSMIC: an autostart entry starts the notice" grep -qx 'Exec=isotope-gpu-notice' "$root/rootfs-cosmic/etc/xdg/autostart/isotope-gpu-notice.desktop"
check "the notice is executable" test -x "$notice"
exit $fail
