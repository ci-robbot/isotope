#!/usr/bin/env bash
# The checks are strings that the inner sh expands, on purpose.
# shellcheck disable=SC2016
# Isotope's firewall (isotope-firewall.service, table inet isotope).
#
#   test/firewall/check-firewall.sh --repo        the tree, and the loader
#                                                 against a fake nft (CI)
#   test/firewall/check-firewall.sh IMAGE         a built image: the files
#                                                 parse (nft -c), also the
#                                                 daemon's output for hard
#                                                 notes, the unit is enabled,
#                                                 SSH is a default; nothing
#                                                 on the agent
#   test/firewall/check-firewall.sh IMAGE --load  privileged, on a throwaway
#                                                 CI runner: load the ruleset
#                                                 into the host, check that
#                                                 Docker's tables stay, a
#                                                 published port answers and
#                                                 a host port does not, a
#                                                 reload replaces the table,
#                                                 then unload
#
# --load changes the firewall of the machine it runs on; it unloads the table
# at the end, also on failure. Do not run it on a machine you use.
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
    if [ "$fail" -eq 0 ]; then echo "all firewall checks passed"; fi
    exit "$fail"
}

unit=usr/lib/systemd/system/isotope-firewall.service
link=etc/systemd/system/sysinit.target.wants/isotope-firewall.service

# ---------------------------------------------------------------------------
repo_mode() {
    local C=rootfs/common
    local base=$C/usr/share/isotope/firewall.nft defaults=$C/usr/share/isotope/firewall-default.nft
    check "the unit exists" test -f "$C/$unit"
    check "the unit is linked into sysinit.target" \
        test "$(readlink "$C/$link")" = "/$unit"
    for c in '!/run/cos/live_mode' '!/run/cos/install_mode' '!/run/cos/uki_install_mode'; do
        check "the unit does not start when $c" grep -qx "ConditionPathExists=$c" "$C/$unit"
    done
    check "the unit runs before network-pre.target" grep -qx 'Before=network-pre.target shutdown.target' "$C/$unit"
    check "the unit wants network-pre.target" grep -qx 'Wants=network-pre.target' "$C/$unit"
    check "the unit loads with isotope-firewall load" grep -qx 'ExecStart=/usr/libexec/isotope-firewall load' "$C/$unit"
    check "the unit reloads in one transaction (ExecReload is load, not unload)" grep -qx 'ExecReload=/usr/libexec/isotope-firewall load' "$C/$unit"
    check "the unit's start and reload are bounded" grep -qx 'TimeoutSec=30' "$C/$unit"
    check "the unit unloads when it stops" grep -qx 'ExecStop=/usr/libexec/isotope-firewall unload' "$C/$unit"
    check "isotope-updated reloads the unit, never restarts it" sh -c "grep -q '\"reload-or-restart\", \"isotope-firewall.service\"' cmd/isotope/firewall.go && ! grep -q '\"restart\", \"isotope-firewall' cmd/isotope/firewall.go"
    check "the loader is executable" test -x "$C/usr/libexec/isotope-firewall"
    check "no file flushes the ruleset or another table" sh -c "! grep -hv '^[[:space:]]*#' $base $defaults $C/usr/libexec/isotope-firewall | grep -q flush"
    check "the base ruleset drops input by default" grep -q 'type filter hook input priority filter; policy drop;' "$base"
    check "the base ruleset hooks only input" sh -c "[ \$(grep -c 'hook ' $base) = 1 ]"
    check "SSH is not a fixed rule (it can be removed)" sh -c "! grep -q 'dport 22' $base"
    check "SSH is in the default allowed list" grep -q 'tcp dport 22 accept comment "SSH"' "$defaults"
    check "no image has a separate server ruleset" sh -c "! find rootfs -name 'firewall-server.nft' | grep -q ."
    check "the server-config stage copies the unit, the loader and both rulesets" sh -c "
        sed -n '/^FROM scratch AS server-config/,/^FROM /p' Dockerfile > /tmp/isotope-server-config.\$\$ &&
        grep -q 'COPY $C/$unit /$unit' /tmp/isotope-server-config.\$\$ &&
        grep -q 'COPY $C/usr/libexec/isotope-firewall /usr/libexec/isotope-firewall' /tmp/isotope-server-config.\$\$ &&
        grep -q 'COPY $base $defaults /usr/share/isotope/' /tmp/isotope-server-config.\$\$ &&
        grep -q 'COPY $C/etc/systemd/system/sysinit.target.wants/ ' /tmp/isotope-server-config.\$\$; rc=\$?; rm -f /tmp/isotope-server-config.\$\$; exit \$rc"
    check "the desktop and server stages stop on a base without nft" sh -c "[ \$(grep -c '^RUN test -x /usr/sbin/nft ||' Dockerfile) = 2 ]"
    check "the agent removes the unit, its link, the loader and both rulesets" sh -c "
        grep -q 'rm -f /etc/systemd/system/sysinit.target.wants/isotope-firewall.service' Dockerfile.agent &&
        grep -q '/usr/libexec/isotope-firewall /usr/share/isotope/firewall.nft' Dockerfile.agent &&
        grep -q '/usr/share/isotope/firewall-default.nft' Dockerfile.agent"
    check "the polkit file declares the firewall action" \
        grep -q '<action id="io.kairos.isotope-update.firewall">' "$C/usr/share/polkit-1/actions/io.kairos.isotope-update.policy"

    # The loader, against a fake nft that records what it is given.
    local w; w=$(mktemp -d)
    # shellcheck disable=SC2064 # expand now: w is local.
    trap "rm -rf '$w'" RETURN
    mkdir -p "$w/share" "$w/etc" "$w/bin"
    cp "$base" "$defaults" "$w/share/"
    cat >"$w/bin/nft" <<'EOF'
#!/bin/sh
# Records each call; `nft -f -` keeps its input. FAKE_NFT_FAIL_ON makes a
# load whose input has that text fail; FAKE_NFT_NO_TABLE makes `list table` fail.
echo "nft $*" >>"$FAKE_NFT_LOG"
case "$*" in
*"-f -"*)
    in=$(cat)
    printf '%s\n' "$in" >>"$FAKE_NFT_LOG"
    if [ -n "${FAKE_NFT_FAIL_ON:-}" ] && printf '%s' "$in" | grep -q "$FAKE_NFT_FAIL_ON"; then exit 1; fi ;;
"list table inet isotope") [ -z "${FAKE_NFT_NO_TABLE:-}" ] || exit 1 ;;
esac
exit 0
EOF
    chmod +x "$w/bin/nft"
    run() { # run ARG: the loader, with the log in $w/log
        : >"$w/log"
        ISOTOPE_FIREWALL_SHARE="$w/share" ISOTOPE_FIREWALL_ETC="$w/etc" ISOTOPE_FIREWALL_NFT="$w/bin/nft" \
            ISOTOPE_FIREWALL_OEM="$w/oem.yaml" FAKE_NFT_LOG="$w/log" sh "$C/usr/libexec/isotope-firewall" "$@"
    }
    run load >/dev/null
    check "load with no settings loads the base ruleset (on by default)" grep -q 'policy drop' "$w/log"
    check "load with no settings allows the defaults (SSH), after the base" \
        sh -c "awk '/policy drop/{a=NR} /dport 22/{b=NR} END{exit !(a && b && a<b)}' '$w/log'"
    check "load is one nft transaction from stdin" sh -c "[ \$(grep -c '^nft ' '$w/log') = 1 ] && grep -qx 'nft -f -' '$w/log'"
    # The settings file exists but yip could not read it, so it wrote no
    # allowed list: the base rules alone, not the defaults (SSH may have
    # been removed on purpose).
    echo '[' >"$w/oem.yaml"
    run load >/dev/null 2>&1
    check "an unreadable settings file loads the base rules without the defaults" \
        sh -c "grep -q 'policy drop' '$w/log' && ! grep -q 'dport 22' '$w/log'"
    rm "$w/oem.yaml"
    printf 'table inet isotope {\n\tchain input {\n\t\ttcp dport 8080 accept comment "dev"\n\t}\n}\n' >"$w/etc/firewall.nft"
    run load >/dev/null
    check "saved settings replace the defaults: SSH removed stays removed" \
        sh -c "awk '/policy drop/{a=NR} /dport 8080/{c=NR} END{exit !(a && c && a<c)}' '$w/log' && ! grep -q 'dport 22' '$w/log'"
    echo off >"$w/etc/firewall"
    run load >/dev/null
    check "off deletes the table and loads nothing" \
        sh -c "grep -qx 'nft delete table inet isotope' '$w/log' && ! grep -q 'policy drop' '$w/log'"
    : >"$w/log"
    FAKE_NFT_NO_TABLE=1 run unload
    check "unload without the table deletes nothing" sh -c "! grep -q delete '$w/log'"
    run unload
    check "unload deletes only table inet isotope" \
        sh -c "[ \"\$(grep '^nft ' '$w/log' | grep -v '^nft list ')\" = 'nft delete table inet isotope' ]"
    echo on >"$w/etc/firewall"
    : >"$w/log"
    if ISOTOPE_FIREWALL_SHARE="$w/share" ISOTOPE_FIREWALL_ETC="$w/etc" ISOTOPE_FIREWALL_NFT="$w/bin/nft" \
        ISOTOPE_FIREWALL_OEM="$w/oem.yaml" FAKE_NFT_LOG="$w/log" FAKE_NFT_FAIL_ON='dport 8080' sh "$C/usr/libexec/isotope-firewall" load >/dev/null 2>&1; then
        bad "a broken allowed-ports file fails the unit"
    else
        pass "a broken allowed-ports file fails the unit"
    fi
    # What the second `nft -f -` got: the text after the last such line.
    awk '/^nft -f -$/{buf=""; next} {buf=buf $0 "\n"} END{printf "%s", buf}' "$w/log" >"$w/last"
    check "a broken allowed-ports file still loads the fixed rules, without the defaults" \
        sh -c "[ \$(grep -c '^nft -f -' '$w/log') = 2 ] && grep -q 'policy drop' '$w/last' && ! grep -q 'dport 22' '$w/last' && ! grep -q 8080 '$w/last'"
    if run bogus >/dev/null 2>&1; then bad "an unknown command fails"; else pass "an unknown command fails"; fi
}

# ---------------------------------------------------------------------------
image_mode() {
    local img="$1" flavor
    flavor=$(docker run --rm --entrypoint sh "$img" -c 'sed -n "s/^FLAVOR=//p" /etc/isotope/release')
    echo "image $img, flavor ${flavor:-unknown}"
    in_image() { # in_image DESC CMD: CMD in the image, unprivileged
        if docker run --rm --entrypoint sh "$img" -c "$2" >/dev/null 2>&1; then pass "$1"; else bad "$1"; fi
    }
    # nft -c needs CAP_NET_ADMIN to read the kernel's cache; the container's
    # own network namespace is a throwaway one.
    in_netns() { # in_netns DESC CMD
        if docker run --rm --network none --cap-add NET_ADMIN --entrypoint sh "$img" -c "$2" >/dev/null 2>&1; then
            pass "$1"
        else
            bad "$1"
        fi
    }
    if [ "$flavor" = agent ]; then
        in_image "the agent has no firewall unit or link" \
            "! test -e /$unit && ! test -e /$link && ! find /etc/systemd /usr/lib/systemd -name 'isotope-firewall*' | grep -q ."
        in_image "the agent has no firewall loader" "! test -e /usr/libexec/isotope-firewall"
        in_image "the agent has no firewall ruleset" \
            "! test -e /usr/share/isotope/firewall.nft && ! test -e /usr/share/isotope/firewall-default.nft"
        finish
    fi
    in_image "nft is in the image" "test -x /usr/sbin/nft"
    in_image "the unit is enabled (linked into sysinit.target)" "test \"\$(readlink /$link)\" = /$unit && test -f /$unit"
    in_image "the unit is off on a live boot" "grep -qx 'ConditionPathExists=!/run/cos/live_mode' /$unit"
    in_image "the unit reloads in one transaction" "grep -qx 'ExecReload=/usr/libexec/isotope-firewall load' /$unit"
    in_image "nftables.service (flush ruleset) is not enabled" "! find /etc/systemd/system -name nftables.service | grep -q ."
    in_netns "nft -c -f /usr/share/isotope/firewall.nft" "nft -c -f /usr/share/isotope/firewall.nft"
    in_netns "nft -c -f /usr/share/isotope/firewall-default.nft" "nft -c -f /usr/share/isotope/firewall-default.nft"
    in_netns "isotope-firewall check (every file, as the unit loads them)" "/usr/libexec/isotope-firewall check"
    # The daemon's own output for notes that are hard on nft's parser
    # (cmd/isotope TestFirewallRulesGolden writes it), with the image's nft.
    if docker run --rm --network none --cap-add NET_ADMIN -v "$PWD/cmd/isotope/testdata/firewall-rules.nft:/tmp/rules.nft:ro" \
        --entrypoint sh "$img" -c 'cat /usr/share/isotope/firewall.nft /tmp/rules.nft | nft -c -f -' >/dev/null 2>&1; then
        pass "the image's nft takes the rules isotope-updated writes"
    else
        bad "the image's nft takes the rules isotope-updated writes"
    fi
    in_image "the polkit file declares the firewall action" \
        "grep -q '<action id=\"io.kairos.isotope-update.firewall\">' /usr/share/polkit-1/actions/io.kairos.isotope-update.policy"
    in_image "isotope has the firewall command" "isotope firewall help | grep -q 'allow PORT/PROTO'"
    in_image "SSH is a default the administrator can remove, not a fixed rule" \
        "grep -q 'tcp dport 22 accept' /usr/share/isotope/firewall-default.nft && ! grep -q 'dport 22' /usr/share/isotope/firewall.nft"
}

# ---------------------------------------------------------------------------
# --load: the ruleset in the runner's own network namespace, next to Docker.
load_mode() {
    local img="$1" flavor web=isotope-fw-web host=isotope-fw-host net=isotope-fw-net ns=isotope-fw
    local box=busybox:1.37 lan=""
    flavor=$(docker run --rm --entrypoint sh "$img" -c 'sed -n "s/^FLAVOR=//p" /etc/isotope/release')
    if [ "$flavor" = agent ]; then
        echo "SKIP: the agent has no firewall"
        finish
    fi
    fw() { docker run --rm --privileged --network host --entrypoint /usr/libexec/isotope-firewall "$img" "$@"; }
    nftc() { docker run --rm --privileged --network host --entrypoint nft "$img" "$@"; }
    has() { grep -q -e "$1" <<<"$2"; } # has PATTERN TEXT
    cleanup() {
        fw unload >/dev/null 2>&1 || true
        docker rm -f "$web" "$host" >/dev/null 2>&1 || true
        docker network rm "$net" >/dev/null 2>&1 || true
        if [ "$lan" = netns ]; then
            sudo -n ip link del isofw0 >/dev/null 2>&1 || true
            sudo -n ip netns del "$ns" >/dev/null 2>&1 || true
        fi
    }
    trap cleanup EXIT
    cleanup
    scripts/retry.sh 5 15 docker pull -q "$box" >/dev/null

    # A published container port (Docker's DNAT), a program listening on the
    # host, and a client "on the LAN". With sudo (a CI runner) the client is
    # a network namespace behind a veth pair, so its packets come in on an
    # interface Docker does not own, as a LAN's come in on the network card:
    # Docker DNATs them in prerouting and they go through forward. Without
    # sudo it is a container on another Docker network, which Docker's
    # isolation may keep from the published port.
    # busybox httpd answers 404 for an empty directory, and wget fails on a
    # 404, so each server gets a page to serve.
    serve='mkdir -p /www && echo ok >/www/index.html && exec httpd -f -h /www -p "$0"'
    docker run -d --name "$web" -p 18080:80 "$box" sh -c "$serve" 80 >/dev/null
    docker run -d --name "$host" --network host "$box" sh -c "$serve" 18081 >/dev/null
    local gw
    if sudo -n true 2>/dev/null; then
        lan=netns
        sudo -n ip netns add "$ns"
        sudo -n ip link add isofw0 type veth peer name isofw1
        sudo -n ip link set isofw1 netns "$ns"
        sudo -n ip addr add 10.199.0.1/24 dev isofw0
        sudo -n ip link set isofw0 up
        sudo -n ip -n "$ns" addr add 10.199.0.2/24 dev isofw1
        sudo -n ip -n "$ns" link set isofw1 up
        sudo -n ip -n "$ns" link set lo up
        gw=10.199.0.1
    else
        lan=docker
        docker network create "$net" >/dev/null
        gw=$(docker network inspect -f '{{(index .IPAM.Config 0).Gateway}}' "$net")
    fi
    echo "the LAN client: $lan, the host at $gw"
    # shellcheck disable=SC2317 # called through check
    local_get() { docker run --rm --network host "$box" wget -q -T 3 -O /dev/null "http://127.0.0.1:$1/" >/dev/null 2>&1; }
    # shellcheck disable=SC2317 # called through check
    from_lan() {
        if [ "$lan" = netns ]; then
            sudo -n ip netns exec "$ns" curl -s -m 3 -o /dev/null "http://$gw:$1/" >/dev/null 2>&1
        else
            docker run --rm --network "$net" "$box" wget -q -T 3 -O /dev/null "http://$gw:$1/" >/dev/null 2>&1
        fi
    }
    sleep 1
    # What works without the firewall: the checks below need these, or they
    # would pass for the wrong reason.
    # Wait for both servers to come up before the baseline is judged.
    for _ in 1 2 3 4 5 6 7 8 9 10; do
        local_get 18080 && local_get 18081 && break
        sleep 1
    done
    check "without the firewall 127.0.0.1:18080 answers" local_get 18080
    check "without the firewall the host port answers from the LAN" from_lan 18081
    local lan_published=0
    if from_lan 18080; then lan_published=1; fi
    if [ "$lan" = netns ] && [ "$lan_published" = 0 ]; then
        bad "without the firewall the published port answers from the LAN"
    fi
    local before after rs
    before=$(nftc list tables | sort)
    echo "tables before:"; printf '  %s\n' "$before"

    if fw load; then pass "isotope-firewall load"; else bad "isotope-firewall load"; fi
    rs=$(nftc list ruleset)
    sed -n '/^table inet isotope/,/^}/p' <<<"$rs"
    if has '^table inet isotope' "$rs"; then pass "the ruleset has table inet isotope"; else bad "the ruleset has table inet isotope"; fi
    if has 'hook input priority filter; policy drop;' "$rs"; then pass "its input chain drops by default"; else bad "its input chain drops by default"; fi
    if has 'tcp dport 22 accept comment "SSH"' "$rs"; then pass "SSH is allowed by default"; else bad "SSH is allowed by default"; fi
    after=$(nftc list tables | sort)
    local t missing=0
    while IFS= read -r t; do
        [ -n "$t" ] || continue
        grep -qxF "$t" <<<"$after" || { echo "  $t is gone"; missing=1; }
    done <<<"$before"
    if [ "$missing" = 0 ]; then pass "every table from before is still there (Docker's included)"; else bad "a table from before is gone"; fi
    check "the published port answers on 127.0.0.1:18080" local_get 18080
    if [ "$lan_published" = 1 ]; then
        check "the published port answers from the LAN (DNAT goes through forward, not input)" from_lan 18080
    else
        echo "SKIP: Docker's isolation keeps the other network from the published port, with or without the firewall"
    fi
    if from_lan 18081; then bad "a host port that is not allowed is blocked from the LAN"; else pass "a host port that is not allowed is blocked from the LAN"; fi

    # A reload (the unit's ExecReload) replaces the table in one transaction:
    # the same rules, once.
    if fw load; then pass "a second load (the reload)"; else bad "a second load (the reload)"; fi
    local rs2; rs2=$(nftc list table inet isotope)
    if [ "$(grep -c 'dport 22' <<<"$rs2")" = 1 ] && [ "$(sed -n '/^table inet isotope/,/^}/p' <<<"$rs")" = "$rs2" ]; then
        pass "the reload leaves the same rules, once"
    else
        bad "the reload leaves the same rules, once"
    fi

    if fw unload; then pass "isotope-firewall unload"; else bad "isotope-firewall unload"; fi
    after=$(nftc list tables | sort)
    if has 'inet isotope' "$after"; then bad "unload deletes table inet isotope"; else pass "unload deletes table inet isotope"; fi
    if [ "$after" = "$before" ]; then pass "unload keeps every other table"; else bad "unload keeps every other table"; fi
    check "after unload the host port answers again" from_lan 18081
}

case "${1:-}" in
--repo) repo_mode ;;
"") echo "usage: $0 --repo | IMAGE [--load]" >&2; exit 2 ;;
*)
    if [ "${2:-}" = --load ]; then load_mode "$1"; else image_mode "$1"; fi ;;
esac
finish
