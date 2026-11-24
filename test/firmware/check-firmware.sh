#!/usr/bin/env bash
# The checks are strings that the inner sh expands, on purpose.
# shellcheck disable=SC2016
# Firmware updates: fwupd with the LVFS (see
# docs/superpowers/specs/2026-11-24-firmware-updates-design.md).
#
#   test/firmware/check-firmware.sh --repo   the tree: the stages, the
#                                            timer drop-ins, the persistence,
#                                            the polkit rule, where the
#                                            capsule plugin is off, the
#                                            agent's removals (CI)
#   test/firmware/check-firmware.sh IMAGE    a built image: fwupd, fwupdmgr
#                                            and their libraries, the EFI
#                                            helper, the LVFS remote, the
#                                            timer and its drop-ins, the
#                                            rules, the persisted path; on
#                                            the server and a Trusted Boot
#                                            image the capsule plugin off;
#                                            on the agent, no fwupd
#
# A real update needs hardware with LVFS firmware: that is on the design
# note's manual list.
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
    if [ "$fail" -eq 0 ]; then echo "all firmware checks passed"; fi
    exit "$fail"
}

timer_dropin=usr/lib/systemd/system/fwupd-refresh.timer.d/10-isotope.conf
service_dropin=usr/lib/systemd/system/fwupd-refresh.service.d/10-isotope.conf
rule=usr/share/polkit-1/rules.d/50-isotope-fwupd.rules
state=system/oem/94_isotope_state.yaml
markers='/run/cos/live_mode /run/cos/install_mode /run/cos/uki_install_mode /run/isotope/live-desktop'

# Each stage's lines, one file per stage, for the checks' greps.
S=$(mktemp -d)
trap 'rm -rf "$S"' EXIT
stages() {
    local s
    for s in fwupd fwupd-efi desktop-default server-default server-config kairos; do
        awk -v s="$s" '$0 ~ "^FROM .* AS "s"$" {p=1; print; next} p && /^FROM / {exit} p' Dockerfile >"$S/$s"
    done
}

# ---------------------------------------------------------------------------
repo_mode() {
    local C=rootfs/common m
    for m in $markers; do
        check "the refresh timer never runs with $m" grep -qx "ConditionPathExists=!$m" "$C/$timer_dropin"
        check "the refresh service never runs with $m" grep -qx "ConditionPathExists=!$m" "$C/$service_dropin"
    done
    check "the refresh timer is daily (fwupd's own is hourly)" sh -c "
        grep -qx 'OnCalendar=' $C/$timer_dropin && grep -qx 'OnCalendar=daily' $C/$timer_dropin &&
        [ \$(grep -c '^OnCalendar=' $C/$timer_dropin) = 2 ] && grep -qx 'Persistent=true' $C/$timer_dropin"
    check "the refresh skips a metered connection" grep -qx 'ExecCondition=/usr/bin/isotope unmetered' "$C/$service_dropin"
    check "/var/lib/fwupd is kept across reboots" grep -q '/var/lib/fwupd; do' "$C/$state"
    check "every fwupd stage installs its licence" sh -c "
        grep -q 'L=/libusb/usr/share/licenses/libusb && install -d' Dockerfile &&
        grep -q 'install -Dm644 COPYING /fwupd-efi/usr/share/licenses/fwupd-efi/COPYING' Dockerfile &&
        grep -q 'install -Dm644 COPYING /fwupd/usr/share/licenses/fwupd/COPYING' Dockerfile"
    check "a restart waits for a firmware write (logind's block inhibitors)" sh -c "
        grep -q 'd.restartBlocked()' cmd/isotope/daemon.go && grep -q 'd.blockers = logindBlockers(conn)' cmd/isotope/dbus.go"
    check "the polkit rule: admin only, the password for all but a plain update, the refresh user for the remotes" sh -c "
        grep -q 'if (!subject.isInGroup(\"admin\")) {' $C/$rule && grep -q 'return polkit.Result.AUTH_ADMIN_KEEP;' $C/$rule &&
        grep -q 'subject.user == \"fwupd-refresh\"' $C/$rule"
    check "the polkit rule sorts before fwupd's own" sh -c "[ \"\$(printf '%s\n' 50-isotope-fwupd.rules org.freedesktop.fwupd.rules | LC_ALL=C sort | head -1)\" = 50-isotope-fwupd.rules ]"
    check "the polkit harness runs the fwupd rule" grep -q '50-isotope-fwupd.rules' test/polkit/check-polkit-rules.sh
    stages
    check "the stages build fwupd-efi, fwupd and the runtime files" sh -c "
        grep -q '^FROM toolchain AS fwupd-efi\$' Dockerfile && grep -q '^FROM toolchain AS fwupd\$' Dockerfile &&
        grep -q '^FROM fwupd AS fwupd-runtime\$' Dockerfile"
    check "fwupd checks signatures with OpenSSL and keeps its helpers out of /usr/libexec" sh -c "
        grep -q -- '-Dopenssl=enabled' $S/fwupd && grep -q -- '--libexecdir=lib' $S/fwupd &&
        grep -q -- '--libexecdir=lib' $S/fwupd-efi"
    check "the desktops copy fwupd and enable the refresh timer" sh -c "
        grep -qx 'COPY --from=fwupd-runtime /fwupd-runtime /' $S/desktop-default &&
        grep -q 'systemctl enable fwupd-refresh.timer' $S/desktop-default"
    check "the desktops keep the capsule plugin (udisks2 finds the ESP)" sh -c "
        grep -q '^COPY --from=udisks2 /udisks2 /' $S/desktop-default && ! grep -q DisabledPlugins $S/desktop-default"
    check "the server copies fwupd, enables the timer and turns the capsule plugin off" sh -c "
        grep -qx 'COPY --from=fwupd-runtime /fwupd-runtime /' $S/server-default &&
        grep -q 'systemctl enable fwupd-refresh.timer' $S/server-default &&
        grep -q 'DisabledPlugins=uefi_capsule' $S/server-default"
    check "the server takes the timer drop-ins and the polkit rules" sh -c "
        grep -q 'fwupd-refresh.timer.d/' $S/server-config && grep -q 'fwupd-refresh.service.d/' $S/server-config &&
        grep -q '^COPY rootfs/common/usr/share/polkit-1/ /usr/share/polkit-1/' $S/server-config"
    check "a Trusted Boot image turns the capsule plugin off" sh -c "
        grep -qF '[ \"\${TRUSTED_BOOT}\" = false ] || [ ! -f /etc/fwupd/fwupd.conf ]' $S/kairos &&
        grep -q 'DisabledPlugins=uefi_capsule' $S/kairos"
    check "the kairos stage adds the fwupd-refresh user" grep -q 'systemd-sysusers fwupd.conf' "$S/kairos"
    check "the agent removes fwupd, its timer, rules and user" sh -c "
        grep -q '/usr/bin/fwupdmgr' Dockerfile.agent && grep -q 'timers.target.wants/fwupd-refresh.timer' Dockerfile.agent &&
        grep -q '50-isotope-fwupd.rules' Dockerfile.agent && grep -q 'userdel fwupd-refresh' Dockerfile.agent"
    check "the tray watches for firmware updates" grep -q 'go watchFirmware(' cmd/isotope/tray.go
    finish
}

# ---------------------------------------------------------------------------
image_mode() {
    local img="$1" flavor trusted m
    flavor=$(docker run --rm --entrypoint sh "$img" -c 'sed -n "s/^FLAVOR=//p" /etc/isotope/release' || true)
    trusted=$(docker run --rm --entrypoint sh "$img" -c 'sed -n "s/^TRUSTED_BOOT=//p" /etc/isotope/release' || true)
    in_image() { # in_image DESC CMD
        if docker run --rm --entrypoint sh "$img" -c "$2" >/dev/null 2>&1; then pass "$1"; else bad "$1"; fi
    }
    if [ "$flavor" = agent ]; then
        in_image "the agent has no fwupd" "! test -e /usr/bin/fwupdmgr && ! test -e /usr/lib/fwupd && ! test -e /etc/fwupd"
        in_image "the agent has no fwupd units or timer link" "! ls /usr/lib/systemd/system/fwupd* && ! test -e /etc/systemd/system/timers.target.wants/fwupd-refresh.timer"
        in_image "the agent has no fwupd polkit or D-Bus files" "! grep -rqs org.freedesktop.fwupd /usr/share/polkit-1 /usr/share/dbus-1"
        in_image "the agent has no fwupd-refresh user" "! grep -q '^fwupd-refresh:' /etc/passwd"
        finish
    fi
    in_image "fwupd and fwupdmgr are in the image" "test -x /usr/lib/fwupd/fwupd && test -x /usr/bin/fwupdmgr"
    in_image "fwupd finds every library" "! ldd /usr/lib/fwupd/fwupd /usr/bin/fwupdmgr 2>&1 | grep -E 'not found|Error'"
    # --help, not --version: --version also asks the daemon for its version
    # over the system bus, which a container has not ("Failed to connect to
    # daemon"). --help loads the same binary and libraries and exits 0.
    in_image "fwupdmgr runs" "fwupdmgr --help"
    in_image "the EFI helper is where fwupd looks" "test -f /usr/lib/fwupd/efi/fwupdx64.efi"
    in_image "the LVFS remote is on" "grep -qx 'Enabled=true' /etc/fwupd/remotes.d/lvfs.conf"
    in_image "the LVFS keys are there" "test -f /etc/pki/fwupd-metadata/LVFS-CA.pem"
    in_image "fwupd is D-Bus activated" "grep -q 'SystemdService=fwupd.service' /usr/share/dbus-1/system-services/org.freedesktop.fwupd.service"
    in_image "the refresh timer is enabled" "test -L /etc/systemd/system/timers.target.wants/fwupd-refresh.timer"
    for m in $markers; do
        in_image "the refresh timer never runs with $m" "grep -qx 'ConditionPathExists=!$m' /$timer_dropin"
        in_image "the refresh service never runs with $m" "grep -qx 'ConditionPathExists=!$m' /$service_dropin"
    done
    in_image "the refresh user exists" "grep -q '^fwupd-refresh:' /etc/passwd"
    in_image "Isotope's fwupd rule is there" "test -f /$rule"
    in_image "the refresh skips a metered connection" "grep -qx 'ExecCondition=/usr/bin/isotope unmetered' /$service_dropin"
    in_image "the licences of fwupd, fwupd-efi and libusb are there" "test -s /usr/share/licenses/fwupd/COPYING && test -s /usr/share/licenses/fwupd-efi/COPYING && test -s /usr/share/licenses/libusb/COPYING"
    in_image "/var/lib/fwupd is kept across reboots" "grep -q '/var/lib/fwupd; do' /$state"
    if [ "$flavor" = server ] || [ "$trusted" = true ]; then
        in_image "the capsule plugin is off (no ESP for fwupd here)" "grep -qx 'DisabledPlugins=uefi_capsule' /etc/fwupd/fwupd.conf"
    else
        in_image "the capsule plugin is on" "! grep -q '^DisabledPlugins=.*uefi_capsule' /etc/fwupd/fwupd.conf"
        in_image "udisks2 is there to find the ESP" "test -x /usr/lib/udisks2/udisksd"
    fi
    finish
}

case "${1:-}" in
--repo) repo_mode ;;
"" | -h | --help) sed -n '4,22p' "$0"; exit 2 ;;
*) image_mode "$1" ;;
esac
