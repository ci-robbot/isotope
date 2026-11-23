#!/usr/bin/env bash
# The QEMU guest agent on a built desktop image (and the agent image).
#
#   test/vm/check-guest-agent.sh [IMAGE] [vm|full]
#
# - vm image: qemu-ga runs, qemu-guest-agent.service and the udev rule that
#   starts it when the virtio port appears are installed, and the unit is not
#   enabled (no wants link: the port exists only in a QEMU VM).
# - full image (real hardware): none of the three is installed.
#
# The second argument defaults to $GPU, then to vm.
set -euo pipefail
IMAGE="${1:-sway-desktop:dev}"
GPU="${2:-${GPU:-vm}}"
fail=0
run() { docker run --rm --entrypoint sh "$IMAGE" -c "$1"; }
check() {
    local desc="$1"; shift
    if run "$*" >/dev/null 2>&1; then echo "PASS: $desc"; else echo "FAIL: $desc"; fail=1; fi
}

unit=/usr/lib/systemd/system/qemu-guest-agent.service
rule=/usr/lib/udev/rules.d/99-qemu-guest-agent.rules
case "$GPU" in
vm)
    check "qemu-ga runs" "/usr/bin/qemu-ga --version"
    check "qemu-guest-agent.service is installed" "test -f $unit"
    check "the udev rule that starts the agent is installed" "grep -q org.qemu.guest_agent.0 $rule"
    check "qemu-guest-agent.service is not enabled" \
        "! find /etc/systemd /usr/lib/systemd -name qemu-guest-agent.service -path '*.wants/*' | grep -q ."
    ;;
full)
    check "qemu-ga is absent" "! test -e /usr/bin/qemu-ga"
    check "qemu-guest-agent.service is absent" "! test -e $unit"
    check "the guest agent udev rule is absent" "! test -e $rule"
    ;;
*) echo "usage: $0 [IMAGE] [vm|full]" >&2; exit 2 ;;
esac

if [ "$fail" -eq 0 ]; then echo "guest-agent: ALL PASS"; else echo "guest-agent: FAILURES"; fi
exit "$fail"
