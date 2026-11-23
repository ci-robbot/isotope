#!/usr/bin/env bash
# The checks are strings that the inner sh expands, on purpose.
# shellcheck disable=SC2016
# Network shares (SMB and NFSv4 through isotope-updated) and NTFS disks
# (udisks2 with the kernel's ntfs3 driver).
#
#   test/shares/check-shares.sh --repo   the tree: the polkit action and
#                                        rule, the udisks2 mount options,
#                                        the agent's removals (CI)
#   test/shares/check-shares.sh IMAGE    a built image: mount.cifs,
#                                        mount.nfs4, the cifs, nfsv4 and
#                                        ntfs3 modules, the udisks2 mount
#                                        options, the polkit actions, the
#                                        logout unit; on the
#                                        agent, that none of Isotope's
#                                        shares pieces is left; on the
#                                        server, no udisks2 options
#
# A real mount needs a server, a loop device and mkfs.ntfs, which neither
# the image nor the runner has: those are on the manual list of
# docs/superpowers/specs/2026-11-24-network-shares-design.md.
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
    if [ "$fail" -eq 0 ]; then echo "all shares checks passed"; fi
    exit "$fail"
}

policy=usr/share/polkit-1/actions/io.kairos.isotope-update.policy
rules=usr/share/polkit-1/rules.d/50-isotope-update.rules
opts=etc/udisks2/mount_options.conf
logout=usr/lib/systemd/system/isotope-shares-logout@.service
dropin=usr/lib/systemd/system/user@.service.d/50-isotope-shares-logout.conf

# ---------------------------------------------------------------------------
repo_mode() {
    local C=rootfs/common
    check "the polkit file declares the shares action" \
        grep -q '<action id="io.kairos.isotope-update.shares">' "$C/$policy"
    check "the polkit file declares the NFS action" \
        grep -q '<action id="io.kairos.isotope-update.shares-nfs">' "$C/$policy"
    check "the shares rule answers for a local, active session only" \
        grep -q 'return (subject.local && subject.active) ? polkit.Result.YES : polkit.Result.NO;' "$C/$rules"
    check "the NFS rule answers for a local, active administrator only" \
        grep -q 'return (subject.local && subject.active && subject.isInGroup("admin")) ? polkit.Result.YES : polkit.Result.NO;' "$C/$rules"
    check "the logout unit is bound to user@UID.service and unmounts at its stop" sh -c "
        grep -qx 'BindsTo=user@%i.service' '$C/$logout' &&
        grep -qx 'ExecStop=/usr/bin/isotope-update daemon --unmount-shares %i' '$C/$logout'"
    check "every user@.service wants the logout unit" grep -qx 'Wants=isotope-shares-logout@%i.service' "$C/$dropin"
    check "the server takes no logout unit" sh -c "! sed -n '/^FROM scratch AS server-config/,/^FROM /p' Dockerfile | grep -q 'shares-logout'"
    check "the shares rule comes before the admin rule" sh -c "
        s=\$(grep -n 'io.kairos.isotope-update.shares' $C/$rules | head -1 | cut -d: -f1)
        a=\$(grep -n 'action.id.indexOf(\"io.kairos.isotope-update.\")' $C/$rules | cut -d: -f1)
        [ -n \"\$s\" ] && [ -n \"\$a\" ] && [ \"\$s\" -lt \"\$a\" ]"
    check "the daemon asks for the shares actions" sh -c "grep -Eq 'actionShares += \"io.kairos.isotope-update.shares\"' cmd/isotope/shares.go && grep -Eq 'actionSharesNFS += \"io.kairos.isotope-update.shares-nfs\"' cmd/isotope/shares.go"
    check "the daemon mounts nosuid,nodev,noexec" grep -q 'common := \[\]string{"nosuid", "nodev", "noexec"}' cmd/isotope/shares.go
    check "the daemon uses NFSv4.2, then 4.1, and nothing older" sh -c \
        "grep -q 'range \[\]string{\"4.2\", \"4.1\"}' cmd/isotope/shares.go && ! grep -Eq 'vers=(3|4\.0|4\")' cmd/isotope/shares.go"
    check "udisks2 mounts NTFS with ntfs3 only" grep -qx 'ntfs_drivers=ntfs3' "$C/$opts"
    check "udisks2's ntfs3 defaults keep the owner and windows_names" \
        grep -qx 'ntfs:ntfs3_defaults=uid=$UID,gid=$GID,windows_names' "$C/$opts"
    check "the mount options are in the [defaults] group" grep -qx '\[defaults\]' "$C/$opts"
    check "the desktop config is copied after udisks2's own mount_options.conf" sh -c "
        u=\$(grep -n '^COPY --from=udisks2 /udisks2 /' Dockerfile | cut -d: -f1)
        c=\$(grep -n '^COPY --from=desktop-config / /' Dockerfile | cut -d: -f1)
        [ -n \"\$u\" ] && [ -n \"\$c\" ] && [ \"\$u\" -lt \"\$c\" ]"
    check "the server takes no udisks2 options" sh -c "! sed -n '/^FROM scratch AS server-config/,/^FROM /p' Dockerfile | grep -q udisks2"
    check "the firewall hooks only input, so SMB and NFS go out" sh -c \
        "[ \$(grep -c 'hook ' $C/usr/share/isotope/firewall.nft) = 1 ] && grep -q 'hook input' $C/usr/share/isotope/firewall.nft && grep -q 'ct state established,related accept' $C/usr/share/isotope/firewall.nft"
    check "the agent removes the policy and the rule, and checks no shares action is left" sh -c "
        grep -q '/usr/share/polkit-1/actions/io.kairos.isotope-update.policy' Dockerfile.agent &&
        grep -q '/usr/share/polkit-1/rules.d/50-isotope-update.rules' Dockerfile.agent &&
        grep -q \"! grep -rqs 'io.kairos.isotope-update.shares' /usr/share/polkit-1\" Dockerfile.agent"
    check "the agent removes /etc/udisks2" grep -q '/etc/udisks2 /var/lib/udisks2' Dockerfile.agent
    check "the agent removes the logout unit and its drop-in" sh -c "
        grep -q '/$logout' Dockerfile.agent && grep -q '/$dropin' Dockerfile.agent"
    check "the tray mounts the saved shares" grep -q 'go mountSavedShares(app.log)' cmd/isotope/tray.go
    finish
}

# ---------------------------------------------------------------------------
image_mode() {
    local img="$1" flavor
    flavor=$(docker run --rm --entrypoint sh "$img" -c 'sed -n "s/^FLAVOR=//p" /etc/isotope/release' || true)
    in_image() { # in_image DESC CMD
        if docker run --rm --entrypoint sh "$img" -c "$2" >/dev/null 2>&1; then pass "$1"; else bad "$1"; fi
    }
    in_image "mount.cifs is in the image" "test -x /usr/sbin/mount.cifs || test -x /sbin/mount.cifs"
    in_image "mount.nfs4 is in the image" "test -x /usr/sbin/mount.nfs4 || test -x /sbin/mount.nfs4"
    in_image "the kernel has the cifs module" "ls /usr/lib/modules/*/kernel/fs/smb/client/cifs.ko* || grep -q '/cifs.ko' /usr/lib/modules/*/modules.builtin"
    in_image "the kernel has the nfsv4 module" "ls /usr/lib/modules/*/kernel/fs/nfs/nfsv4.ko* || grep -q '/nfsv4.ko' /usr/lib/modules/*/modules.builtin"
    in_image "the kernel has the ntfs3 module, loaded through its fs-ntfs3 alias" \
        "ls /usr/lib/modules/*/kernel/fs/ntfs3/ntfs3.ko* && grep -qx 'alias fs-ntfs3 ntfs3' /usr/lib/modules/*/modules.alias"
    case "$flavor" in
    agent)
        in_image "the agent has no shares polkit action" "! grep -rqs 'io.kairos.isotope-update.shares' /usr/share/polkit-1"
        in_image "the agent has no udisks2 mount options" "! test -e /$opts"
        in_image "the agent has no Isotope System" "! test -e /usr/bin/isotope-system"
        in_image "the agent has no shares logout unit" "! test -e /$logout && ! test -e /$dropin"
        finish
        ;;
    server)
        in_image "the server has no udisks2 mount options" "! test -e /$opts"
        in_image "the server has no shares logout unit" "! test -e /$logout"
        in_image "the polkit file declares the shares action" "grep -q 'io.kairos.isotope-update.shares' /$policy"
        finish
        ;;
    esac
    in_image "udisks2 mounts NTFS with ntfs3 only" "grep -qx 'ntfs_drivers=ntfs3' /$opts"
    in_image "udisks2's ntfs3 defaults keep the owner and windows_names" "grep -qx 'ntfs:ntfs3_defaults=uid=\$UID,gid=\$GID,windows_names' /$opts"
    in_image "the polkit file declares the shares action" "grep -q '<action id=\"io.kairos.isotope-update.shares\">' /$policy"
    in_image "the shares rule is in the update rules" "grep -q 'io.kairos.isotope-update.shares' /$rules"
    in_image "the NFS action is for administrators" "grep -q '<action id=\"io.kairos.isotope-update.shares-nfs\">' /$policy && grep -q 'io.kairos.isotope-update.shares-nfs' /$rules"
    in_image "the logout unit and its drop-in are there" "test -f /$logout && test -f /$dropin"
    in_image "Isotope System is in the image" "test -x /usr/bin/isotope-system"
    finish
}

case "${1:-}" in
--repo) repo_mode ;;
"" | -*) echo "usage: $0 --repo | IMAGE" >&2; exit 2 ;;
*) image_mode "$1" ;;
esac
