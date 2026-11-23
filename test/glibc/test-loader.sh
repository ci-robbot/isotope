#!/usr/bin/env bash
# Builds the stand-in glibc loader (tools/isotope-glibc-loader.c) against
# musl, a fake isotope and, with the runner's cc, a program whose interpreter
# is the loader, then checks the hand-off contract and each refusal (message,
# exit 127).
set -eu

repo=$(cd "$(dirname "$0")/../.." && pwd)
t=$(mktemp -d)
trap 'rm -rf "$t"' EXIT
fail=0
CC=${CC:-cc}
# The loader is built against musl, as in the Dockerfile: a static glibc
# binary cannot start as an interpreter (it finds its own headers through the
# program's AT_PHDR). Static-pie, so that it loads at a free address.
MUSL_IMAGE=${MUSL_IMAGE:-alpine:3.22}

ok()  { printf 'ok   %s\n' "$1"; }
bad() { printf 'FAIL %s\n' "$1"; fail=1; }

mkdir -p "$t/root/usr/bin" "$t/work" "$t/home"
build_loader() { # extra flags..., output name in $t
    out=$1; shift
    docker run --rm -v "$repo/tools:/src:ro" -v "$t:/out" "$MUSL_IMAGE" sh -ec '
        out=$1; shift
        apk add --no-cache gcc musl-dev >/dev/null
        cc -static-pie -O2 -Wall -Wextra -Werror "$@" /src/isotope-glibc-loader.c -o "/out/$out"
        chown '"$(id -u):$(id -g)"' "/out/$out"' sh "$out" "$@"
}
build_loader loader -DTESTING

# A fake isotope: one argument per line, then the env entry we care about.
cat > "$t/root/usr/bin/isotope" <<'SH'
#!/bin/sh
{
    for a in "$@"; do printf 'ARG[%s]\n' "$a"; done
    printf 'ARGV0=%s\n' "${ISOTOPE_GLIBC_ARGV0-unset}"
} > "$FAKE_OUT"
SH
chmod +x "$t/root/usr/bin/isotope"

# A program whose interpreter is the loader. It never runs for real.
printf 'int main(void){return 0;}\n' > "$t/prog.c"
"$CC" -O2 -no-pie -Wl,--dynamic-linker="$t/loader" "$t/prog.c" -o "$t/work/prog"

export ISOTOPE_GLIBC_LOADER_TEST_ROOT="$t/root"
export FAKE_OUT="$t/out"
export HOME="$t/home"
unset XDG_CONFIG_HOME ISOTOPE_GLIBC_LOADER_ACTIVE ISOTOPE_GLIBC_AUTO

expect_handoff() { # name, expected ARGV0, program path
    name=$1; want0=$2; progpath=$3
    {
        printf 'ARG[glibc]\nARG[run]\nARG[--loader]\nARG[--]\n'
        printf 'ARG[%s]\nARG[a]\nARG[b c]\nARG[d\ne]\nARG[-x]\n' "$progpath"
        printf 'ARGV0=%s\n' "$want0"
    } > "$t/want"
    if cmp -s "$t/want" "$t/out"; then ok "$name"; else
        bad "$name"; diff "$t/want" "$t/out" || true; fi
}

rm -f "$t/out"
( cd "$t/work" && exec -a myname "$t/work/prog" a "b c" "d
e" -x )
expect_handoff "kernel-started: absolute path, argv and ARGV0" myname "$t/work/prog"

rm -f "$t/out"
( cd "$t/work" && ./prog a "b c" "d
e" -x )
expect_handoff "kernel-started: relative path made absolute" ./prog "$t/work/prog"

rm -f "$t/out"
( cd "$t/work" && "$t/loader" ./prog a "b c" "d
e" -x )
expect_handoff "direct invocation" ./prog "$t/work/prog"

# Each refusal: exit 127, a sentence on stderr naming the program and saying
# what to do; isotope is not reached.
refusal() { # name, expected text; the command is stdin-less, run via "$@"
    name=$1; text=$2; shift 2
    rm -f "$t/out"
    rc=0
    ( cd "$t/work" && "$@" ) 2>"$t/err" || rc=$?
    if [ "$rc" = 127 ] && [ ! -e "$t/out" ] \
        && grep -q "prog is a program built for glibc" "$t/err" \
        && grep -q -- "$text" "$t/err" && grep -q 'isotope glibc --help' "$t/err"
    then ok "$name"; else bad "$name (rc=$rc)"; cat "$t/err"; fi
}

refusal "guard env set" "loop" env ISOTOPE_GLIBC_LOADER_ACTIVE=1 ./prog
refusal "ISOTOPE_GLIBC_AUTO=0" "turned off" env ISOTOPE_GLIBC_AUTO=0 ./prog

mkdir -p "$t/home/.config/isotope"
printf '# c\nauto = off\n' > "$t/home/.config/isotope/glibc"
refusal "auto=off in \$HOME/.config" "turned off" ./prog
rm "$t/home/.config/isotope/glibc"
mkdir -p "$t/xdg/isotope"
printf 'auto=off\n' > "$t/xdg/isotope/glibc"
refusal "auto=off in \$XDG_CONFIG_HOME" "turned off" env XDG_CONFIG_HOME="$t/xdg" ./prog
printf 'auto=on\nother=off\n' > "$t/xdg/isotope/glibc"
rm -f "$t/out"
( cd "$t/work" && XDG_CONFIG_HOME="$t/xdg" ./prog a ) || true
if [ -e "$t/out" ]; then ok "auto=on and other keys do not opt out"; else bad "auto=on and other keys do not opt out"; fi
chmod 000 "$t/xdg/isotope/glibc" 2>/dev/null || true
rm -f "$t/out"
( cd "$t/work" && XDG_CONFIG_HOME="$t/xdg" ./prog a ) || true
if [ -e "$t/out" ]; then ok "unreadable config means auto on"; else bad "unreadable config means auto on"; fi
rm -f "$t/xdg/isotope/glibc"

mkdir -p "$t/root/run/isotope"; : > "$t/root/run/isotope/live-desktop"
refusal "live system marker" "live system" ./prog
rm "$t/root/run/isotope/live-desktop"

mv "$t/root/usr/bin/isotope" "$t/isotope.saved"
refusal "no /usr/bin/isotope" "/usr/bin/isotope is missing" ./prog
mv "$t/isotope.saved" "$t/root/usr/bin/isotope"

# A setgid program: the kernel starts the loader with AT_SECURE. It needs a
# group of ours that is not the primary one (or root) and a mount without
# nosuid; a setgid probe says whether the kernel sets AT_SECURE here.
setgid_group=$(id -G | tr ' ' '\n' | grep -vx "$(id -g)" | head -n 1 || true)
[ "$(id -u)" = 0 ] && setgid_group=65534
printf '#include <sys/auxv.h>\nint main(void){return getauxval(AT_SECURE) ? 0 : 1;}\n' > "$t/probe.c"
"$CC" -O2 "$t/probe.c" -o "$t/work/probe"
cp "$t/work/prog" "$t/work/sprog"
if [ -n "$setgid_group" ] && chgrp "$setgid_group" "$t/work/probe" "$t/work/sprog" \
    && chmod g+s "$t/work/probe" "$t/work/sprog" && "$t/work/probe"; then
    refusal "setgid program (AT_SECURE)" "setuid and setgid glibc programs are not handed to the container" ./sprog
else
    printf 'skip setgid program (no second group, or %s is mounted nosuid)\n' "$t"
fi

if [ "$(id -u)" = 0 ]; then
    refusal "uid 0" "root" ./prog
else
    printf 'skip uid 0 (not running as root)\n'
fi

# The test hook must not exist in a production build.
build_loader prod
if grep -q ISOTOPE_GLIBC_LOADER_TEST_ROOT "$t/prod"; then bad "production build has no test hook"
else ok "production build has no test hook"; fi
if readelf -lW "$t/prod" | grep -q INTERP; then bad "loader has no PT_INTERP"
else ok "loader has no PT_INTERP"; fi

exit "$fail"
