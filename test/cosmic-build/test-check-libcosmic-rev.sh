#!/bin/sh
# Tests test/cosmic-build/check-libcosmic-rev.sh against fixture lock files
# (COSMIC_LOCKS_DIR), no network: a match, an applets/ mismatch, a stale README
# row, a stale README heading, a missing fixture.
#
#   test/cosmic-build/test-check-libcosmic-rev.sh
set -eu

here=$(cd "$(dirname "$0")" && pwd)
script=$here/check-libcosmic-rev.sh
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
fail=0
ok() { echo "ok: $*"; }
bad() { echo "FAIL: $*" >&2; fail=1; }

A=aaaaaaaaaaaa
B=bbbbbbbbbbbb
lock() { # lock REV
    printf '[[package]]\nname = "libcosmic"\nsource = "git+https://github.com/pop-os/libcosmic#%s%s"\n' "$1" "0000000000000000000000000000"
}

root=$work/root
mkdir -p "$root/patches/cosmic" "$root/applets" "$root/tools/isotope-cosmic-theme" "$work/locks"
printf 'ARG COSMIC_VERSION=9.9.9\n' > "$root/Dockerfile"
lock $A > "$root/applets/Cargo.lock"
lock $A > "$root/tools/isotope-cosmic-theme/Cargo.lock"
readme() { # readme EPOCH
    cat > "$root/patches/cosmic/README.md" <<R
## libcosmic revisions at epoch-$1

| libcosmic rev | Repos |
|---|---|
| \`$A\` | cosmic-applets, cosmic-panel |
| \`$B\` | cosmic-comp |
| none | cosmic-session |
R
}
readme 9.9.9
lock $A > "$work/locks/cosmic-applets.Cargo.lock"
lock $A > "$work/locks/cosmic-panel.Cargo.lock"
lock $B > "$work/locks/cosmic-comp.Cargo.lock"
printf '[[package]]\nname = "x"\n' > "$work/locks/cosmic-session.Cargo.lock"

check() { ISOTOPE_ROOT=$root COSMIC_LOCKS_DIR=$work/locks sh "$script" 2>&1; }

if out=$(check); then ok "everything matches"; else bad "match failed: $out"; fi

lock $B > "$root/applets/Cargo.lock"
if out=$(check); then bad "applets/ on another rev passed"; else
    case $out in *"applets/Cargo.lock pins libcosmic $B"*"update libcosmic"*) ok "applets mismatch names the file and the rev";; *) bad "applets mismatch message: $out";; esac
fi
lock $A > "$root/applets/Cargo.lock"

lock $B > "$root/tools/isotope-cosmic-theme/Cargo.lock"
if out=$(check); then bad "theme tool on another rev passed"; else
    case $out in *"tools/isotope-cosmic-theme/Cargo.lock pins libcosmic $B"*"update libcosmic"*) ok "theme tool mismatch names the file and the rev";; *) bad "theme mismatch message: $out";; esac
fi
lock $A > "$root/tools/isotope-cosmic-theme/Cargo.lock"

lock $B > "$work/locks/cosmic-panel.Cargo.lock"
if out=$(check); then bad "stale README row passed"; else
    case $out in *"cosmic-panel at epoch-9.9.9 pins libcosmic $B"*"lists $A"*) ok "stale README row names the repo";; *) bad "README row message: $out";; esac
fi
lock $A > "$work/locks/cosmic-panel.Cargo.lock"

readme 9.9.8
if out=$(check); then bad "stale README heading passed"; else
    case $out in *"says epoch-9.9.8"*"epoch-9.9.9"*) ok "stale README heading fails";; *) bad "heading message: $out";; esac
fi
readme 9.9.9

printf 'ARG COSMIC_VERSION=9.9.9\nFROM x\nARG COSMIC_VERSION=9.9.8\n' > "$root/Dockerfile"
if out=$(check); then bad "disagreeing ARGs passed"; else
    case $out in *"values disagree"*"1 9.9.9"*"3 9.9.8"*) ok "disagreeing COSMIC_VERSION ARGs fail, with lines";; *) bad "ARG message: $out";; esac
fi
printf 'ARG COSMIC_VERSION=9.9.9\nFROM x\nARG COSMIC_VERSION=9.9.9\n' > "$root/Dockerfile"
if out=$(check); then ok "agreeing ARGs pass"; else bad "agreeing ARGs failed: $out"; fi

rm "$work/locks/cosmic-comp.Cargo.lock"
if out=$(check); then bad "missing upstream lock passed"; else ok "missing upstream lock fails"; fi

exit $fail
