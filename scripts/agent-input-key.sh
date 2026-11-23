#!/usr/bin/env bash
# Print a key for everything the Agent Gates workflow builds and tests:
#
#   scripts/agent-input-key.sh [TREE]
#
# TREE is a checkout of this repository (default: the current directory). Two
# trees with the same key build the same agent image and ISO with the same
# commands and run the same gate harness on it, so agent-gates.yml compares
# the key of a pull request with the key of its base and skips the build and
# the gates when they match. The key covers:
#
#   - the commands `make agent-iso` runs (a dry run), so a Makefile change
#     to the agent recipes, the Hadron release, or the AuroraBoot image counts;
#   - the stage key (tools/stage-key.py) of the i3 image the agent is built
#     on: every Dockerfile stage it reaches, the context files they copy or
#     bind-mount, and the build args they use;
#   - the stage key of the agent stage in Dockerfile.agent;
#   - the files of the gate harness and the ISO build (AGENT_HARNESS below),
#     and agent-gates.yml itself.
#
# The tool that computes the stage keys is always the one next to this
# script, so a change to the tool alone does not make two trees differ.
# Any failure exits non-zero; the caller must then run the gates.
set -euo pipefail

tree="${1:-.}"
here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
stage_key="$here/../tools/stage-key.py"

# Files the gates or the ISO build read straight from the checkout, outside
# any Dockerfile COPY. cmd/isotope-agent/, internal/agent/, rootfs/agent/,
# rootfs/i3/ and the rest of the image inputs are covered by the stage keys.
AGENT_HARNESS=(
  .github/workflows/agent-gates.yml
  auroraboot
  scripts/ci-containerd-store.sh
  test/agent
  test/boot-ux
  test/welcome/check-welcome-image.sh
  tools/vm.sh
)

mk() {
  # Unset every knob CI may set in the environment, so only the tree decides.
  env -u CACHE_REF -u CACHE_PUSH -u CACHE_SCOPE -u CACHE_ALSO -u LLVM_IMAGE -u VERSION \
    make -C "$tree" -s --no-print-directory "$@"
}

var() {
  # shellcheck disable=SC2016 # $($*) is for make, not for the shell.
  mk --eval '_agent_key_var-%: ; @echo $($*)' "_agent_key_var-$1"
}

hadron="$(var HADRON_VERSION)"
base="$(var BASE_IMAGE)"
agent_base="$(var AGENT_BASE_IMAGE)"
[[ -n "$hadron" && -n "$base" && -n "$agent_base" ]] || {
  echo "agent-input-key: could not read the image variables from $tree/Makefile" >&2
  exit 1
}

# The same build args `make agent-image` passes to the i3 build; kairos is
# the last stage of the Dockerfile, the one `make image` builds. Each key goes
# through a variable first: a failed command substitution inside echo would
# not stop the script.
i3_key="$(python3 "$stage_key" --dockerfile "$tree/Dockerfile" \
  --arg HADRON_VERSION="$hadron" --arg BASE_IMAGE="$base" \
  --arg DESKTOP=i3 --arg GPU=vm --arg FIRMWARE=false kairos)"
agent_key="$(python3 "$stage_key" --dockerfile "$tree/Dockerfile.agent" \
  --arg HADRON_VERSION="$hadron" --arg BASE_IMAGE="$agent_base" agent)"
dry_run="$(mk -n agent-iso)"

parts="$(mktemp)"
trap 'rm -f "$parts"' EXIT

{
  printf '== make -n agent-iso\n%s\n' "$dry_run"
  printf '== i3 %s\n== agent %s\n' "$i3_key" "$agent_key"
  echo "== harness"
  cd "$tree"
  for p in "${AGENT_HARNESS[@]}"; do
    [[ -e "$p" || -L "$p" ]] || { echo "missing $p"; continue; }
    find "$p" \( -type f -o -type l \) -print
  done | LC_ALL=C sort | while IFS= read -r f; do
    if [[ -L "$f" ]]; then
      printf '%s -> %s\n' "$f" "$(readlink "$f")"
    else
      printf '%s %s\n' "$f" "$(sha256sum <"$f" | cut -d' ' -f1)"
    fi
  done
} >"$parts"

if [[ -n "${AGENT_KEY_DEBUG:-}" ]]; then
  cat "$parts" >&2
fi
printf 'agent-%s\n' "$(sha256sum <"$parts" | cut -c1-16)"
