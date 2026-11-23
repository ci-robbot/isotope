#!/usr/bin/env bash
# Fail when a Go package is not vetted by any Go job in .github/workflows/ci.yml.
# The covered set is every `./path/...` pattern on a `go vet` line in ci.yml, so
# a new package needs no edit here, only a place in a job.
set -euo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")/.."
module="$(go list -m)"

mapfile -t patterns < <(
  grep -E '^[[:space:]]*(test -z .*)?go vet ' .github/workflows/ci.yml |
    grep -oE '\./[^ ]*\.\.\.' | sed -E 's#^\./##; s#/?\.\.\.$##' | sort -u
)
if ((${#patterns[@]} == 0)); then
  echo "no go vet patterns found in .github/workflows/ci.yml" >&2
  exit 1
fi

status=0
# -e keeps packages that need cgo or a tag; -tags matches the GUI jobs.
while read -r pkg; do
  rel="${pkg#"$module"}"
  rel="${rel#/}"
  covered=0
  for p in "${patterns[@]}"; do
    if [[ "$rel" == "$p" || "$rel" == "$p"/* ]]; then covered=1; break; fi
  done
  if ((covered == 0)); then
    echo "not covered by any ci.yml Go job: ./$rel" >&2
    status=1
  fi
done < <(go list -e -tags nox11,novulkan ./...)

((status == 0)) && echo "all Go packages are covered by ci.yml"
exit "$status"
