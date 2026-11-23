#!/usr/bin/env bash
# The checks are strings that the inner sh expands, on purpose.
# shellcheck disable=SC2016
# Programs built for glibc: the stand-in loader, the AppImage binfmt rule and
# its handler, the glibc container's image setting
# (docs/superpowers/specs/2026-11-23-glibc-programs-design.md).
#
#   test/glibc/check-glibc.sh --repo   the tree: the Dockerfile wiring, the
#                                      server and the agent left out, the
#                                      Renovate pin, the workflows, the docs
#   test/glibc/check-glibc.sh IMAGE    a built image. A desktop: the loader at
#                                      both paths and static, the handler
#                                      static, the help page, glibc.conf names
#                                      an Ubuntu LTS, the binfmt rule and
#                                      systemd-binfmt, no ELF of the image
#                                      names ld-linux-x86-64.so.2, and a real
#                                      glibc program (Ubuntu's /usr/bin/true)
#                                      reaches `isotope glibc run --loader`.
#                                      The server and the agent: none of it.
#
# IMAGE mode pulls the image glibc.conf names to take its /usr/bin/true;
# GLIBC_TEST_IMAGE names another image to take it from.
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
    if [ "$fail" -eq 0 ]; then echo "all glibc checks passed"; fi
    exit "$fail"
}

loader=usr/lib/ld-linux-x86-64.so.2
rule_file=usr/lib/binfmt.d/isotope-appimage.conf
handler=usr/libexec/isotope-appimage
conf=usr/share/isotope/glibc.conf
rule=':isotope-appimage:M:8:\x41\x49\x02::/usr/libexec/isotope-appimage:F'
help_page=docs/glibc-programs.md
doc=usr/share/doc/isotope/glibc-programs.md

# An Ubuntu LTS image reference: docker.io/library/ubuntu:YY.04, YY even.
# shellcheck disable=SC2317 # called through check
is_lts_ref() {
    [[ "$1" =~ ^docker\.io/library/ubuntu:[0-9]*[02468]\.04$ ]]
}

# The lines of a Dockerfile stage: from its FROM line to the next FROM.
stage() { # stage FILE NAME
    awk -v name="$2" '
        /^FROM / { on = ($NF == name) ; if (on) { print; next } }
        on' "$1"
}

# ---------------------------------------------------------------------------
repo_mode() {
    local C=rootfs/common tmp desk server srvcfg ref
    tmp=$(mktemp -d)
    stage Dockerfile glibc-loader >"$tmp/loader"
    stage Dockerfile appimage-handler >"$tmp/handler"
    stage Dockerfile desktop-default >"$tmp/desk"
    stage Dockerfile server-default >"$tmp/server"
    stage Dockerfile server-config >"$tmp/srvcfg"
    desk=$tmp/desk server=$tmp/server srvcfg=$tmp/srvcfg

    check "the loader's source exists" test -f tools/isotope-glibc-loader.c
    check "the loader is built static-pie, with no PT_INTERP and no test hook" sh -c "
        grep -q 'cc -static-pie ' '$tmp/loader' &&
        grep -q '! readelf -lW /glibc-loader/$loader | grep -q INTERP' '$tmp/loader' &&
        grep -q '! grep -q ISOTOPE_GLIBC_LOADER_TEST_ROOT' '$tmp/loader'"
    check "desktop-default copies the loader" grep -qx 'COPY --from=glibc-loader /glibc-loader /' "$desk"
    check "the handler's source exists" test -f tools/isotope-appimage.c
    check "the handler is built static-pie, with no PT_INTERP and no test hook" sh -c "
        grep -q 'cc -static-pie .*/build/isotope-appimage.c -o /appimage-handler/$handler' '$tmp/handler' &&
        grep -q '! readelf -lW /appimage-handler/$handler | grep -q INTERP' '$tmp/handler' &&
        grep -q '! grep -q ISOTOPE_APPIMAGE_TEST_ROOT' '$tmp/handler'"
    check "desktop-default copies the handler" grep -qx 'COPY --from=appimage-handler /appimage-handler /' "$desk"
    check "desktop-default copies the help page to /$doc" grep -qx "COPY $help_page /$doc" "$desk"
    check "desktop-default stops without the handler or the help page" sh -c "
        grep -q 'test -x /$handler' '$desk' && grep -q 'test -f /$doc' '$desk'"
    check "desktop-default takes ARG GLIBC_BOX_IMAGE" grep -qx 'ARG GLIBC_BOX_IMAGE' "$desk"
    check "desktop-default writes glibc.conf as image=<GLIBC_BOX_IMAGE>" \
        grep -qF "RUN printf 'image=%s\\n' \"\${GLIBC_BOX_IMAGE}\" > /$conf" "$desk"
    check "desktop-default stops without systemd-binfmt, the automount, binfmt_misc or /lib64 -> usr/lib" sh -c "
        grep -q 'test -L /usr/lib/systemd/system/sysinit.target.wants/systemd-binfmt.service' '$desk' &&
        grep -q 'test -L /usr/lib/systemd/system/sysinit.target.wants/proc-sys-fs-binfmt_misc.automount' '$desk' &&
        grep -q 'binfmt_misc.ko' '$desk' &&
        grep -q 'test \"\$(readlink /lib64)\" = usr/lib' '$desk'"
    check "server-default has no loader, no handler, no glibc.conf and no help page" sh -c "! grep -q -e glibc-loader -e appimage-handler -e glibc.conf -e GLIBC_BOX_IMAGE -e glibc-programs.md '$server'"
    check "server-config copies no binfmt rule, no handler and no whole rootfs/common directory that holds them" sh -c "
        ! grep -q -e binfmt.d -e isotope-appimage '$srvcfg' &&
        ! grep -qE '^COPY $C/(usr/(lib|libexec)/?)? ' '$srvcfg'"

    # The pin.
    ref=$(sed -n 's/^ARG GLIBC_BOX_IMAGE=//p' Dockerfile)
    check "GLIBC_BOX_IMAGE is an Ubuntu LTS ($ref)" is_lts_ref "$ref"
    check "GLIBC_BOX_IMAGE sits under its renovate comment" sh -c "
        grep -B1 '^ARG GLIBC_BOX_IMAGE=' Dockerfile | head -n 1 |
            grep -qx '# renovate: datasource=docker depName=docker.io/library/ubuntu'"
    check "cmd/isotope falls back to the same image" \
        grep -qF "glibcDefaultImage = \"$ref\"" cmd/isotope/glibc.go
    check "cmd/isotope reads image= from /$conf" sh -c "
        grep -qF 'confFile:   \"/$conf\"' cmd/isotope/glibc.go &&
        grep -qF '[\"image\"]' cmd/isotope/glibc.go"
    check "renovate.json updates GLIBC_BOX_IMAGE to even-year .04 tags only" python3 -I - "$ref" <<'PY'
import json, re, sys
ref = sys.argv[1]
d = json.load(open("renovate.json"))
src = open("Dockerfile").read()
js = lambda r: r.replace("(?<", "(?P<")
hits = [m.groupdict() for cm in d["customManagers"] for s in cm["matchStrings"]
        for m in re.finditer(js(s), src) if m.groupdict().get("depName") == "docker.io/library/ubuntu"]
assert len(hits) == 1 and hits[0]["datasource"] == "docker", hits
assert hits[0]["currentValue"] == ref.rsplit(":", 1)[1], hits
rules = [r for r in d["packageRules"] if "docker.io/library/ubuntu" in r.get("matchDepNames", [])]
assert len(rules) == 1, rules
allowed = re.compile(rules[0]["allowedVersions"].strip("/"))
for tag, ok in [(hits[0]["currentValue"], True), ("28.04", True), ("27.04", False), ("26.10", False),
                ("resolute", False), ("26.04.1", False), ("resolute-20261101", False)]:
    assert bool(allowed.search(tag)) == ok, tag
PY

    # The rule and the handler.
    check "the binfmt file holds exactly the rule" sh -c "[ \"\$(cat $C/$rule_file)\" = '$rule' ] && [ \$(wc -l < $C/$rule_file) = 1 ]"
    check "rootfs/common has no handler of its own (the appimage-handler stage builds it)" test ! -e "$C/$handler"

    # The agent.
    check "Dockerfile.agent removes the loader, the rule, the handler and glibc.conf" sh -c "
        grep -q 'rm -f /$loader /$rule_file' Dockerfile.agent &&
        grep -q '/$handler /$conf' Dockerfile.agent && grep -qE '^ +/$doc && ' Dockerfile.agent"
    for f in "$loader" "lib64/ld-linux-x86-64.so.2" "$rule_file" "$handler" "$conf" "$doc"; do
        check "Dockerfile.agent checks that /$f is gone" grep -q "! test -e /$f" Dockerfile.agent
    done

    # The workflows.
    check "ci.yml runs the loader test, the handler test and this check" sh -c "
        grep -q 'run: test/glibc/test-loader.sh' .github/workflows/ci.yml &&
        grep -q 'run: test/glibc/test-appimage.sh' .github/workflows/ci.yml &&
        grep -q 'run: test/glibc/check-glibc.sh --repo' .github/workflows/ci.yml"
    check "desktop-images.yml checks each built image" grep -q 'test/glibc/check-glibc.sh "${DESKTOP}-desktop:dev"' .github/workflows/desktop-images.yml
    check "release.yml checks each built image and the agent" sh -c "
        grep -q 'test/glibc/check-glibc.sh \"\${DESKTOP}-desktop:dev\"' .github/workflows/release.yml &&
        grep -q 'test/glibc/check-glibc.sh \"\$AGENT_IMAGE\"' .github/workflows/release.yml"
    check "agent-gates.yml checks the agent image" grep -q 'test/glibc/check-glibc.sh agent-desktop:dev' .github/workflows/agent-gates.yml

    # The docs.
    check "the help page exists" test -f "$help_page"
    check "isotope glibc --help names the help page in the image, then on GitHub" sh -c "
        grep -n '/$doc on this system' cmd/isotope/glibc.go | cut -d: -f1 > '$tmp/l1' &&
        grep -n 'blob/main/$help_page' cmd/isotope/glibc.go | cut -d: -f1 > '$tmp/l2' &&
        [ \$(wc -l < '$tmp/l1') = 1 ] && [ \$(wc -l < '$tmp/l2') = 1 ] && [ \$(cat '$tmp/l1') -lt \$(cat '$tmp/l2') ]"
    check "AGENTS.md has the Programs built for glibc row" grep -q '^| Programs built for glibc |' AGENTS.md
    check "AGENTS.md's server table says the server has none of it" grep -q 'isotope glibc' <(sed -n '/^## The server edition/,/^## /p' AGENTS.md)
    check "README.md mentions isotope glibc" grep -q 'isotope glibc' README.md
    rm -rf "$tmp"
}

# ---------------------------------------------------------------------------
image_mode() {
    local img="$1" flavor ref src elfs bad_elfs
    flavor=$(docker run --rm --entrypoint sh "$img" -c 'sed -n "s/^FLAVOR=//p" /etc/isotope/release' 2>/dev/null || true)
    echo "image $img, flavor ${flavor:-unknown}"
    in_image() { # in_image DESC CMD: CMD in the image, unprivileged
        if docker run --rm --network none --entrypoint sh "$img" -c "$2" >/dev/null 2>&1; then pass "$1"; else bad "$1"; fi
    }
    case "$flavor" in
    agent | server)
        for f in "$loader" lib64/ld-linux-x86-64.so.2 "$rule_file" "$handler" "$conf" "$doc"; do
            in_image "the $flavor image has no /$f" "! test -e /$f && ! test -L /$f"
        done
        in_image "the $flavor image has no ld-linux-x86-64.so.2 anywhere" \
            "! find / -xdev -name ld-linux-x86-64.so.2 2>/dev/null | grep -q ."
        return
        ;;
    sway | i3 | cosmic | niri) ;;
    *)
        bad "unknown flavor '${flavor}' in $img's /etc/isotope/release"
        return
        ;;
    esac

    in_image "the loader is at /$loader and at /lib64/ld-linux-x86-64.so.2" \
        "test -f /$loader && test -x /$loader && test /lib64/ld-linux-x86-64.so.2 -ef /$loader"
    in_image "glibc.conf holds one line, image=<ref>" \
        "[ \$(wc -l < /$conf) = 1 ] && grep -qx 'image=[^ ]*' /$conf"
    ref=$(docker run --rm --network none --entrypoint sh "$img" -c "sed -n 's/^image=//p' /$conf" 2>/dev/null || true)
    check "glibc.conf names an Ubuntu LTS (${ref:-none})" is_lts_ref "$ref"
    in_image "the binfmt file holds exactly the rule" \
        "[ \"\$(cat /$rule_file)\" = '$rule' ] && [ \$(wc -l < /$rule_file) = 1 ]"
    in_image "the handler is executable" "test -f /$handler && test -x /$handler"
    in_image "the help page is at /$doc" "test -s /$doc"
    in_image "systemd-binfmt.service is enabled (linked into sysinit.target)" \
        "test -f /usr/lib/systemd/system/systemd-binfmt.service &&
         { test -L /usr/lib/systemd/system/sysinit.target.wants/systemd-binfmt.service ||
           test -L /etc/systemd/system/sysinit.target.wants/systemd-binfmt.service; } &&
         ! test /etc/systemd/system/systemd-binfmt.service -ef /dev/null"
    in_image "binfmt_misc is mounted on demand (the automount) and is a module or built in" \
        "test -L /usr/lib/systemd/system/sysinit.target.wants/proc-sys-fs-binfmt_misc.automount &&
         { ls /usr/lib/modules/*/kernel/fs/binfmt_misc.ko* || grep -qs '/binfmt_misc.ko\$' /usr/lib/modules/*/modules.builtin; }"
    in_image "isotope has the glibc command" "isotope glibc help | grep -q 'isotope glibc run'"

    work=$(mktemp -d)
    cid=""
    trap 'if [ -n "$cid" ]; then docker rm -f "$cid" >/dev/null 2>&1; fi; rm -rf "$work"' EXIT

    # Every ELF in the image, with its interpreter. docker export, not docker
    # cp: docker cp stops at the first symlink that points out of the copied
    # directory, and leaves half a tree.
    cid=$(docker create --entrypoint sh "$img")
    if docker export "$cid" | python3 -I test/glibc/scan-elf-interp.py >"$work/elfs"; then
        elfs=$(wc -l <"$work/elfs")
        check "the image has ELF files to scan ($elfs)" test "$elfs" -gt 100
        check "the loader is static (an ELF without PT_INTERP)" grep -qxF "/$loader	" "$work/elfs"
        check "the handler is static (an ELF without PT_INTERP)" grep -qxF "/$handler	" "$work/elfs"
        bad_elfs=$(awk -F '\t' '$2 ~ /(^|\/)ld-linux-x86-64\.so\.2$/' "$work/elfs")
        if [ -z "$bad_elfs" ]; then
            pass "no ELF of the image names ld-linux-x86-64.so.2 as its interpreter"
        else
            bad "these ELFs of the image would start through the stand-in loader:"
            printf '%s\n' "$bad_elfs" | sed 's/^/    /'
        fi
    else
        bad "could not scan the ELF files of $img"
    fi
    docker rm -f "$cid" >/dev/null 2>&1 || true
    cid=""

    # A real glibc program, as the kernel starts it on the image: Ubuntu's
    # /usr/bin/true, run as a user, with a fake isotope that writes its
    # arguments, mounted read-only over the real one.
    src=${GLIBC_TEST_IMAGE:-$ref}
    if [ -z "$src" ]; then
        bad "no image to take a glibc program from"
        return
    fi
    docker image inspect "$src" >/dev/null 2>&1 || scripts/retry.sh 3 10 docker pull "$src" >/dev/null ||
        { bad "could not pull $src"; return; }
    # Ubuntu 26.04's /usr/bin/true is a link (to gnutrue): take its target.
    local real
    real=$(docker run --rm --network none --entrypoint readlink "$src" -f /usr/bin/true 2>/dev/null || true)
    mkdir -p "$work/src" "$work/prog" "$work/out"
    cid=$(docker create --entrypoint sh "$src")
    if [ -z "$real" ] || ! docker export "$cid" | tar -x -C "$work/src" "${real#/}"; then
        bad "could not take /usr/bin/true (${real:-unresolved}) out of $src"
        return
    fi
    docker rm -f "$cid" >/dev/null 2>&1 || true
    cid=""
    cp "$work/src/${real#/}" "$work/prog/true"
    check "$src's /usr/bin/true ($real) is a glibc program" sh -c "
        tar -c -C '$work/prog' true | python3 -I test/glibc/scan-elf-interp.py |
            grep -qx '/true	/lib64/ld-linux-x86-64.so.2'"
    cat >"$work/isotope" <<'SH'
#!/bin/sh
{
    for a in "$@"; do printf '%s\n' "$a"; done
    printf 'ARGV0=%s\n' "${ISOTOPE_GLIBC_ARGV0-unset}"
} >/out/argv
SH
    chmod 755 "$work/isotope" "$work/prog/true"
    chmod 777 "$work/out"
    docker run --rm --network none --user 1000:1000 \
        -v "$work/prog:/opt/glibc-test:ro" \
        -v "$work/isotope:/usr/bin/isotope:ro" \
        -v "$work/out:/out" \
        --entrypoint /opt/glibc-test/true "$img" a 'b c' >"$work/run.log" 2>&1 || true
    printf '%s\n' glibc run --loader -- /opt/glibc-test/true a 'b c' ARGV0=/opt/glibc-test/true >"$work/want"
    if cmp -s "$work/want" "$work/out/argv"; then
        pass "a glibc program started on the image reaches isotope glibc run --loader -- PATH ARGS"
    else
        bad "a glibc program started on the image reaches isotope glibc run --loader -- PATH ARGS"
        echo "    want:"; sed 's/^/      /' "$work/want"
        echo "    got:"; sed 's/^/      /' "$work/out/argv" 2>/dev/null || echo "      (nothing)"
        echo "    output:"; sed 's/^/      /' "$work/run.log"
    fi
}

case "${1:-}" in
--repo) repo_mode ;;
"" | -h | --help)
    sed -n '4,22p' "$0"
    exit 2
    ;;
*) image_mode "$1" ;;
esac
finish
