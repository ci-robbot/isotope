#!/bin/sh
# licence.sh NAME DIR: copy DIR's licence files (COPYING*, LICENSE*,
# LICENCE*, the REUSE LICENSES/ directory) to the extension's own licence
# directory, /usr/share/licenses/fcitx5-extension/NAME. Fails when DIR has
# none, so a component never ships without its licence.
set -eu
name=$1 dir=$2
dest=/output/usr/share/licenses/fcitx5-extension/$name
mkdir -p "$dest"
found=
for f in "$dir"/COPYING* "$dir"/LICENSE* "$dir"/LICENCE* "$dir"/LICENSES/*; do
    [ -f "$f" ] || continue
    cp "$f" "$dest/"
    found=1
done
[ -n "$found" ] || { echo "no licence in $dir for $name" >&2; exit 1; }
