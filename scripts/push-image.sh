#!/usr/bin/env bash
# push-image.sh REF
#
# Pushes REF, retried through scripts/retry.sh, and prints the digest the
# registry accepted, for cosign to sign. The digest comes from docker push's
# own "<tag>: digest: sha256:... size: N" line. Reading the tag back from the
# registry right after the push can get a 404 while GHCR catches up (the
# v0.2.3 cosmic vm job failed that way), so the registry is asked only when
# that line is missing, and then with retries too. RETRY_DELAY (seconds)
# overrides the first wait, for tests.
set -euo pipefail

[ "$#" -eq 1 ] || { echo "usage: $0 REF" >&2; exit 2; }
ref=$1
here="$(cd "$(dirname "$0")" && pwd)"
log="$(mktemp)"
trap 'rm -f "$log"' EXIT

# The push's output goes to stderr for the job log, and to $log for the digest.
# shellcheck disable=SC2016 # $1 and $2 expand in the inner bash
"$here/retry.sh" 5 "${RETRY_DELAY:-15}" bash -c 'set -o pipefail; docker push "$1" 2>&1 | tee "$2" >&2' _ "$ref" "$log"

tag="${ref##*:}"
digest="$(sed -n "s/^${tag}: digest: \(sha256:[0-9a-f]\{64\}\) size: [0-9]*$/\1/p" "$log" | tail -n 1)"
if ! [[ "$digest" =~ ^sha256:[0-9a-f]{64}$ ]]; then
	echo "push-image: no digest line in the push output, asking the registry" >&2
	# shellcheck disable=SC2016 # $1 and $d expand in the inner bash
	digest="$("$here/retry.sh" 6 "${RETRY_DELAY:-10}" bash -c \
		'd="$(docker buildx imagetools inspect "$1" --format "{{json .Manifest}}" | jq -r .digest)" &&
		 [[ "$d" =~ ^sha256:[0-9a-f]{64}$ ]] && echo "$d"' _ "$ref")"
fi
[[ "$digest" =~ ^sha256:[0-9a-f]{64}$ ]] || { echo "push-image: could not resolve the pushed digest of $ref" >&2; exit 1; }
echo "$digest"
