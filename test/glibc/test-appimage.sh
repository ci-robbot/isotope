#!/usr/bin/env bash
# Builds the AppImage binfmt handler (tools/isotope-appimage.c) against musl,
# as the Dockerfile does, and a fake type 2 AppImage: a static ELF stub with
# "AI\x02" at offset 8 and a payload after it, which writes its argv, the size
# and the bytes 8 to 10 of /proc/self/exe and $APPIMAGE. Then it checks:
#  - on the host (/run/systemd/system is a directory and /usr/bin/isotope an
#    executable regular file, both under the -DTESTING root), it hands the
#    AppImage to a fake isotope as `glibc run --appimage -- PATH ARGS`;
#  - anywhere else (a container), it runs the AppImage's own runtime once,
#    with its argv, from a copy that holds the whole file without the magic,
#    so the binfmt rule cannot match it again;
#  - its refusals (not an AppImage, setgid), exit 127 with a sentence.
# The kernel's binfmt rule itself needs root to register and is not used: the
# handler is started the way the kernel starts it (handler PATH ARGS).
set -eu

repo=$(cd "$(dirname "$0")/../.." && pwd)
t=$(mktemp -d)
trap 'rm -rf "$t"' EXIT
fail=0
MUSL_IMAGE=${MUSL_IMAGE:-alpine:3.22}

ok()  { printf 'ok   %s\n' "$1"; }
bad() { printf 'FAIL %s\n' "$1"; fail=1; }

cat > "$t/stub.c" <<'C'
#include <fcntl.h>
#include <stdio.h>
#include <stdlib.h>
#include <sys/stat.h>
#include <unistd.h>
int main(int argc, char **argv)
{
	FILE *o = fopen(getenv("FAKE_OUT"), "a");
	struct stat st;
	unsigned char m[3] = { 9, 9, 9 };
	int fd = open("/proc/self/exe", O_RDONLY);
	if (!o || fd < 0 || fstat(fd, &st) != 0 || pread(fd, m, 3, 8) != 3)
		return 99;
	fprintf(o, "RUN\n");
	for (int i = 0; i < argc; i++)
		fprintf(o, "ARG[%s]\n", argv[i]);
	fprintf(o, "EXE_SIZE=%lld\nMAGIC=%02x%02x%02x\nAPPIMAGE=%s\n", (long long)st.st_size,
		m[0], m[1], m[2], getenv("APPIMAGE") ? getenv("APPIMAGE") : "unset");
	return 7;
}
C

docker run --rm -v "$repo/tools:/src:ro" -v "$t:/out" "$MUSL_IMAGE" sh -ec '
    apk add --no-cache gcc musl-dev >/dev/null
    f="-static-pie -O2 -Wall -Wextra -Werror"
    cc $f -DTESTING /src/isotope-appimage.c -o /out/handler
    cc $f /src/isotope-appimage.c -o /out/prod
    cc -static -O2 /out/stub.c -o /out/stub
    printf "#include <sys/auxv.h>\nint main(void){return getauxval(AT_SECURE) ? 0 : 1;}\n" > /out/probe.c
    cc -static -O2 /out/probe.c -o /out/probe
    chown '"$(id -u):$(id -g)"' /out/handler /out/prod /out/stub /out/probe'

# The fake AppImage: the stub, the magic in its e_ident padding, a payload.
mkdir -p "$t/work" "$t/root/usr/bin" "$t/root/run/systemd/system"
cp "$t/stub" "$t/work/app.AppImage"
printf 'AI\002' | dd of="$t/work/app.AppImage" bs=1 seek=8 conv=notrunc status=none
head -c 8192 /dev/urandom >> "$t/work/app.AppImage"
chmod +x "$t/work/app.AppImage"
cp "$t/work/app.AppImage" "$t/app.saved"
size=$(wc -c < "$t/work/app.AppImage")

# A fake isotope that writes its arguments; the root has /run/systemd/system.
cat > "$t/root/usr/bin/isotope" <<'SH'
#!/bin/sh
{ echo ISOTOPE; for a in "$@"; do printf 'ARG[%s]\n' "$a"; done; } >> "$FAKE_OUT"
SH
chmod +x "$t/root/usr/bin/isotope"

export ISOTOPE_APPIMAGE_TEST_ROOT="$t/root" FAKE_OUT="$t/out"
unset APPIMAGE
h="$t/handler"

check() { # name, expected exit, command...
    name=$1; want_rc=$2; shift 2
    rc=0
    ( cd "$t/work" && "$@" ) 2>"$t/err" || rc=$?
    if [ "$rc" = "$want_rc" ] && cmp -s "$t/want" "$t/out"; then ok "$name"; else
        bad "$name (exit $rc, want $want_rc)"; diff "$t/want" "$t/out" || true; cat "$t/err"; fi
    rm -f "$t/out"
}
runtime_want() { # argv0 as given, APPIMAGE as the stub sees it (the handler sets none)
    { printf 'RUN\nARG[%s]\nARG[a]\nARG[b c]\nARG[-x]\n' "$1"
      printf 'EXE_SIZE=%s\nMAGIC=000000\nAPPIMAGE=%s\n' "$size" "$2"; } > "$t/want"
}

rm -f "$t/out"
printf 'ISOTOPE\nARG[glibc]\nARG[run]\nARG[--appimage]\nARG[--]\nARG[%s]\nARG[a]\nARG[b c]\nARG[-x]\n' \
    "$t/work/app.AppImage" > "$t/want"
check "host: hands the AppImage to isotope glibc run --appimage" 0 "$h" "$t/work/app.AppImage" a "b c" -x

# A container: no /run/systemd/system, isotope present or not.
mv "$t/root/run/systemd/system" "$t/systemd.saved"
runtime_want "$t/work/app.AppImage" unset
check "container: runs the runtime once, whole file, no magic, its argv" 7 "$h" "$t/work/app.AppImage" a "b c" -x
runtime_want ./app.AppImage unset
check "container: a relative path, the environment as it was" 7 "$h" ./app.AppImage a "b c" -x
runtime_want ./app.AppImage /somewhere/else.AppImage
check "container: an APPIMAGE that is set passes through" 7 env APPIMAGE=/somewhere/else.AppImage "$h" ./app.AppImage a "b c" -x
: > "$t/root/run/systemd/system"
runtime_want ./app.AppImage unset
check "container: /run/systemd/system that is not a directory" 7 "$h" ./app.AppImage a "b c" -x
rm "$t/root/run/systemd/system"
mv "$t/systemd.saved" "$t/root/run/systemd/system"

# Booted with systemd, but /usr/bin/isotope is missing, or not an
# executable regular file.
mv "$t/root/usr/bin/isotope" "$t/isotope.saved"
runtime_want ./app.AppImage unset
check "systemd without /usr/bin/isotope: runs the runtime" 7 "$h" ./app.AppImage a "b c" -x
mkdir "$t/root/usr/bin/isotope"
check "systemd with a directory at /usr/bin/isotope: runs the runtime" 7 "$h" ./app.AppImage a "b c" -x
rmdir "$t/root/usr/bin/isotope"
cp "$t/isotope.saved" "$t/root/usr/bin/isotope"; chmod -x "$t/root/usr/bin/isotope"
check "systemd with a non-executable /usr/bin/isotope: runs the runtime" 7 "$h" ./app.AppImage a "b c" -x
mv "$t/isotope.saved" "$t/root/usr/bin/isotope"

if cmp -s "$t/app.saved" "$t/work/app.AppImage"; then ok "the AppImage file is left as it was"
else bad "the AppImage file is left as it was"; fi

refusal() { # name, expected text, command...
    name=$1; text=$2; shift 2
    rm -f "$t/out"
    rc=0
    ( cd "$t/work" && "$@" ) 2>"$t/err" || rc=$?
    if [ "$rc" = 127 ] && [ ! -e "$t/out" ] && grep -q "is an AppImage" "$t/err" \
        && grep -q -- "$text" "$t/err" && grep -q 'isotope glibc --help' "$t/err"
    then ok "$name"; else bad "$name (exit $rc)"; cat "$t/err"; fi
}
printf 'not an appimage\n' > "$t/work/plain"
mv "$t/root/run/systemd/system" "$t/systemd.saved" # a container
refusal "container: not a type 2 AppImage" "not a type 2 AppImage" "$h" ./plain
mv "$t/systemd.saved" "$t/root/run/systemd/system"

# setgid: a group of ours that is not the primary one (or root), and a mount
# without nosuid: a setgid probe says whether the kernel sets AT_SECURE here.
setgid_group=$(id -G | tr ' ' '\n' | grep -vx "$(id -g)" | head -n 1 || true)
[ "$(id -u)" = 0 ] && setgid_group=65534
cp "$t/handler" "$t/sghandler"
if [ -n "$setgid_group" ] && chgrp "$setgid_group" "$t/sghandler" "$t/probe" \
    && chmod g+s "$t/sghandler" "$t/probe" && "$t/probe"; then
    refusal "setgid handler (AT_SECURE)" "setuid or setgid" "$t/sghandler" ./app.AppImage
else
    printf 'skip setgid handler (no second group, or %s is mounted nosuid)\n' "$t"
fi

# The production build: no test hook, no PT_INTERP.
if grep -q ISOTOPE_APPIMAGE_TEST_ROOT "$t/prod"; then bad "production build has no test hook"
else ok "production build has no test hook"; fi
if readelf -lW "$t/prod" | grep -q INTERP; then bad "handler has no PT_INTERP"
else ok "handler has no PT_INTERP"; fi

exit "$fail"
