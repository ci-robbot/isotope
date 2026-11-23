#!/bin/sh
# isotope-first-run-apps is the old name of the first-run app picker, kept so
# habits, menus and old configs keep working. By hand it opens Isotope
# Welcome's Apps page (`isotope welcome apps`); as the login hook of a config
# from before Isotope Welcome (`--login`) it is `isotope-welcome --login`,
# which opens nothing once the user went through it or skipped it. The login
# gate and its stamps are Isotope Welcome's (cmd/isotope-welcome/, its Go tests).
# Runs in an Alpine container, with fake isotope and isotope-welcome that log
# their arguments.
#
#   test/first-run/check-first-run-apps.sh            run in alpine (docker)
#   test/first-run/check-first-run-apps.sh --inner    run here
set -eu

if [ "${1:-}" != --inner ]; then
    repo=$(cd "$(dirname "$0")/../.." && pwd)
    exec docker run --rm -v "$repo:/src:ro" "${ALPINE_IMAGE:-alpine:3.22}" \
        sh /src/test/first-run/check-first-run-apps.sh --inner
fi

src=${SRC:-/src}
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
mkdir -p "$work/bin"
printf '#!/bin/sh\necho "isotope $*" > "%s/log"\n' "$work" > "$work/bin/isotope"
printf '#!/bin/sh\necho "isotope-welcome $*" > "%s/log"\n' "$work" > "$work/bin/isotope-welcome"
chmod +x "$work/bin/isotope" "$work/bin/isotope-welcome"
PATH="$work/bin:$PATH"

fail=0
# ARGS|WANT
for c in "|isotope welcome apps" "--login|isotope-welcome --login"; do
    args=${c%%|*} want=${c#*|}
    : > "$work/log"
    # shellcheck disable=SC2086
    sh "$src/rootfs/common/usr/bin/isotope-first-run-apps" $args
    got=$(cat "$work/log")
    if [ "$got" = "$want" ]; then
        echo "PASS: isotope-first-run-apps $args runs '$want'"
    else
        echo "FAIL: isotope-first-run-apps $args ran [$got], want [$want]" >&2; fail=1
    fi
done
exit "$fail"
