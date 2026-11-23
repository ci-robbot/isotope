#!/usr/bin/env bash
# The live ISO's GRUB menu, rendered the way auroraboot/build.sh renders it
# (both call auroraboot/render-grub.sh). The desktop menu leads with a live
# desktop entry; the install entries keep today's cmdlines; the server and
# agent menus are byte-equal to what they were before the live desktop.
# testdata/legacy-*.cfg are the renders of the old single template.
set -euo pipefail
cd "$(dirname "$0")/../.."

RENDER=auroraboot/render-grub.sh
GOLD=test/live/testdata
fail=0
ok()  { echo "ok   $*"; }
bad() { echo "FAIL $*" >&2; fail=1; }

# entry FILE ID: the whole menuentry block with that --id.
entry() {
  awk -v id="--id $2 " '
    /^menuentry / { inb = index($0, id) > 0 }
    inb { print }
    inb && /^}/ { inb = 0 }' "$1"
}
# linux_line FILE ID: the entry's linux line.
linux_line() { entry "$1" "$2" | grep -E '^[[:space:]]+linux '; }
title_line()  { entry "$1" "$2" | head -n1; }

eq() { # eq DESCRIPTION EXPECTED ACTUAL
  if [ "$2" = "$3" ]; then ok "$1"; else bad "$1"; diff <(echo "$2") <(echo "$3") >&2 || true; fi
}
has()  { case " $2 " in *" $3 "*) ok "$1";; *) bad "$1: no '$3'";; esac; }
hasnt(){ case "$2" in *"$3"*) bad "$1: has '$3'";; *) ok "$1";; esac; }

t="$(mktemp -d)"; trap 'rm -rf "$t"' EXIT

# --- one console palette in both templates (tools/gen-theme.py owns it) ----
pal() { grep '^set isotope_vt_palette=' "$1"; }
if [ -n "$(pal auroraboot/live-grub.cfg.tmpl)" ] && \
   [ "$(pal auroraboot/live-grub.cfg.tmpl)" = "$(pal auroraboot/live-grub-desktop.cfg.tmpl)" ]; then
  ok "isotope_vt_palette is identical in both templates"
else
  bad "isotope_vt_palette differs between the templates"
fi

# --- server and agent: byte-equal to the old render -----------------------
for flavor in server agent; do
  ARCH=amd64 "$RENDER" "$flavor" > "$t/a"
  if cmp -s "$t/a" "$GOLD/legacy-amd64.cfg"; then ok "$flavor amd64 equals today's"; else bad "$flavor amd64 differs from today's"; fi
  ARCH=arm64 "$RENDER" "$flavor" > "$t/a"
  if cmp -s "$t/a" "$GOLD/legacy-arm64.cfg"; then ok "$flavor arm64 equals today's"; else bad "$flavor arm64 differs from today's"; fi
  ARCH=amd64 ISOTOPE_EXTRA_LIVE_CMDLINE="foo=bar" "$RENDER" "$flavor" > "$t/a"
  if cmp -s "$t/a" "$GOLD/legacy-amd64-extra.cfg"; then ok "$flavor extra cmdline equals today's"; else bad "$flavor extra cmdline differs from today's"; fi
done

# --- desktop variants -------------------------------------------------------
for flavor in sway i3 niri cosmic; do
  echo "== $flavor"
  old="$GOLD/legacy-amd64.cfg"
  ARCH=amd64 "$RENDER" "$flavor" > "$t/d"
  d="$t/d"

  if grep -q '^set default="isotope-live"$' "$d"; then ok "default is isotope-live"; else bad "default is not isotope-live"; fi
  first="$(grep -m1 '^menuentry ' "$d")"
  case "$first" in
    'menuentry "Isotope" --id isotope-live '*) ok "first entry is Isotope, id isotope-live" ;;
    *) bad "first entry: $first" ;;
  esac

  live="$(linux_line "$d" isotope-live)"
  hasnt "live entry has no install-mode" "$live" "install-mode"
  has   "live entry has isotope.live" "$live" "isotope.live"
  eq    "live entry is today's isotope entry, live instead of install" \
        "$(linux_line "$old" isotope-install | sed 's/ install-mode-interactive / isotope.live /')" "$live"

  safe="$(linux_line "$d" isotope-live-safe-graphics)"
  eq "Isotope (safe graphics) title" 'menuentry "Isotope (safe graphics)" --id isotope-live-safe-graphics --class os --unrestricted {' "$(title_line "$d" isotope-live-safe-graphics)"
  has "safe graphics has isotope.live" "$safe" "isotope.live"
  has "safe graphics has nomodeset rendered" "$safe" "nomodeset"
  hasnt "safe graphics has no placeholder" "$safe" "{{"
  hasnt "safe graphics has no install-mode" "$safe" "install-mode"

  for pair in "Install Isotope|isotope-install" \
              "Install Isotope (text)|isotope-install-text" \
              "Install Isotope (safe graphics)|isotope-install-safe-graphics"; do
    name="${pair%%|*}"; id="${pair#*|}"
    eq "$name title" "menuentry \"$name\" --id $id --class os --unrestricted {" "$(title_line "$d" "$id")"
    eq "$name keeps its old cmdline" "$(linux_line "$old" "$id")" "$(linux_line "$d" "$id")"
    has "$name has install-mode-interactive" "$(linux_line "$d" "$id")" "install-mode-interactive"
  done
  has "text installer keeps isotope.installer=text" "$(linux_line "$d" isotope-install-text)" "isotope.installer=text"

  for id in isotope-boot-local isotope-boot-details isotope-debug; do
    eq "$id is byte-equal to today's" "$(entry "$old" "$id")" "$(entry "$d" "$id")"
  done

  ARCH=arm64 "$RENDER" "$flavor" > "$t/d64"
  hasnt "arm64 render has no placeholder" "$(cat "$t/d64")" "{{"
  hasnt "arm64 linux lines have no nomodeset" "$(grep -E '^[[:space:]]+linux ' "$t/d64")" "nomodeset"
done

exit "$fail"
