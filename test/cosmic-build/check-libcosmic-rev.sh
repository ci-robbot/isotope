#!/bin/sh
# Guards the libcosmic revision on a COSMIC epoch bump. Renovate bumps
# COSMIC_VERSION in the Dockerfile, but applets/Cargo.lock,
# tools/isotope-cosmic-theme/Cargo.lock and the table in patches/cosmic/README.md
# stay behind. For the COSMIC_VERSION in the Dockerfile this fetches the
# Cargo.lock of every repo the README table lists, at tag epoch-<version>, and
# fails when:
#
# - the README heading names another epoch than COSMIC_VERSION;
# - a repo's libcosmic revs differ from its README row;
# - applets/ or tools/isotope-cosmic-theme/ is on another libcosmic rev than
#   cosmic-applets at that tag.
#
# Needs network, unless COSMIC_LOCKS_DIR names a directory of fixtures
# (<repo>.Cargo.lock per repo) to use instead of the download.
#
#   test/cosmic-build/check-libcosmic-rev.sh
#
# Environment: COSMIC_VERSION (default: the Dockerfile's first), COSMIC_LOCKS_DIR,
# ISOTOPE_ROOT (the tree to check, default: this repository).
set -eu

root=${ISOTOPE_ROOT:-$(cd "$(dirname "$0")/../.." && pwd)}
readme=$root/patches/cosmic/README.md
fail=0
bad() { echo "FAIL: $*" >&2; fail=1; }

args=$(grep -n '^ARG COSMIC_VERSION=' "$root/Dockerfile" | sed 's/:ARG COSMIC_VERSION=/ /' || true)
[ -n "$args" ] || [ -n "${COSMIC_VERSION:-}" ] || { echo "FAIL: no ARG COSMIC_VERSION in $root/Dockerfile" >&2; exit 1; }
if [ "$(printf '%s\n' "$args" | cut -d' ' -f2 | sort -u | wc -l)" -gt 1 ]; then
    echo "FAIL: the Dockerfile's ARG COSMIC_VERSION values disagree (line value): $(printf '%s' "$args" | tr '\n' ',' | sed 's/,$//'): set them all to one epoch" >&2
    exit 1
fi
version=${COSMIC_VERSION:-$(printf '%s\n' "$args" | head -n 1 | cut -d' ' -f2)}

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

# lock_revs FILE: the libcosmic revs a Cargo.lock names, 12 hex each, one line.
lock_revs() {
    sed -n 's|^source = "git+https://github.com/pop-os/libcosmic[^#]*#\([0-9a-f]\{12\}\).*|\1|p' "$1" | sort -u | tr '\n' ' ' | sed 's/ $//'
}

# fetch_lock REPO: the upstream Cargo.lock into $work/REPO.Cargo.lock.
fetch_lock() {
    if [ -n "${COSMIC_LOCKS_DIR:-}" ]; then
        cp "$COSMIC_LOCKS_DIR/$1.Cargo.lock" "$work/$1.Cargo.lock"
    else
        curl --retry 3 --retry-all-errors -fsSL \
            "https://raw.githubusercontent.com/pop-os/$1/epoch-$version/Cargo.lock" -o "$work/$1.Cargo.lock"
    fi
}

heading=$(sed -n 's/^## libcosmic revisions at epoch-\(.*\)$/\1/p' "$readme")
if [ "$heading" != "$version" ]; then
    bad "patches/cosmic/README.md heading says epoch-${heading:-?}, the Dockerfile builds epoch-$version: rename the heading to \"## libcosmic revisions at epoch-$version\" and refresh the table"
fi

# Table rows: | `rev` | repo, repo | and | none | repo, repo |
# shellcheck disable=SC2016 # the backticks of the Markdown table, not a command
rows=$(sed -n 's/^| `\([0-9a-f]\{12\}\)` | \(.*\) |$/\1 \2/p; s/^| none | \(.*\) |$/none \1/p' "$readme")
[ -n "$rows" ] || { echo "FAIL: no table rows in $readme" >&2; exit 1; }

applets_rev=
echo "$rows" | while IFS= read -r row; do
    printf '%s\n' "${row#* }" | tr ',' '\n' | sed 's/^ *//; s/ *$//' | sed "s|^|${row%% *} |"
done > "$work/pairs"

while read -r listed repo; do
    if ! fetch_lock "$repo" 2>"$work/err"; then
        bad "cannot get the Cargo.lock of $repo at epoch-$version ($(tr '\n' ' ' < "$work/err")): is there a tag, and is $repo still in the README table?"
        continue
    fi
    actual=$(lock_revs "$work/$repo.Cargo.lock")
    [ -n "$actual" ] || actual=none
    [ "$repo" != cosmic-applets ] || applets_rev=$actual
    if [ "$actual" != "$listed" ]; then
        bad "$repo at epoch-$version pins libcosmic $actual, patches/cosmic/README.md lists $listed: move $repo to the \`$actual\` row (and check patches/cosmic/libcosmic still applies there)"
    fi
done < "$work/pairs"

[ -n "$applets_rev" ] || { echo "FAIL: cosmic-applets is not in the README table" >&2; exit 1; }
for lock in applets/Cargo.lock tools/isotope-cosmic-theme/Cargo.lock; do
    have=$(lock_revs "$root/$lock")
    if [ "$have" != "$applets_rev" ]; then
        bad "$lock pins libcosmic ${have:-nothing}, cosmic-applets at epoch-$version pins $applets_rev: update libcosmic in $lock (cargo update -p libcosmic --precise <full sha of $applets_rev>)"
    fi
done

if [ "$fail" = 0 ]; then
    echo "ok: libcosmic $applets_rev at epoch-$version matches applets/, tools/isotope-cosmic-theme/ and the README table"
fi
exit $fail
