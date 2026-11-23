#!/usr/bin/env bash
# check-go-stage-copies.sh
#
# Every Go build stage in the Dockerfiles copies go.mod, go.sum and only the
# directories its program needs, so an unrelated change does not rebuild it.
# A new package in pkg/ that a program starts to import must be added to that
# stage's COPY lines too, or the image build fails with "no required module
# provides package" (main, 50e792f: pkg/firewall). This script compares, for
# each stage, the module packages `go list -deps -test` names for its program
# with what the stage copies, and fails on any package left out.
#
# Run from the repository root. Needs go, but no C compiler: go list does not
# compile, it only needs CGO_ENABLED=1 to resolve the Gio packages.
set -euo pipefail

module="$(go list -m)"

# stage file, stage name, program package. A stage built FROM another stage
# inherits its COPY lines (system-build is FROM welcome-build).
stages=(
	"Dockerfile install-ui-build ./cmd/isotope-install-ui"
	"Dockerfile install-gui-build ./cmd/isotope-install-gui"
	"Dockerfile welcome-build ./cmd/isotope-welcome"
	"Dockerfile system-build ./cmd/isotope-system"
	"Dockerfile update-build ./cmd/isotope"
	"Dockerfile.agent agent-gobuild ./cmd/isotope-agent"
)

# copies FILE STAGE: the repository paths the stage's COPY lines name
# (sources only, without --from copies), following FROM <stage> chains.
copies() {
	local file=$1 stage=$2
	awk -v want="$stage" '
		/^FROM / {
			name = ""
			for (i = 1; i <= NF; i++) if (toupper($i) == "AS") name = $(i + 1)
			cur = name
			parent[cur] = $2
			next
		}
		cur != "" && /^COPY / && $0 !~ /--from=/ {
			for (i = 2; i < NF; i++) if ($i !~ /^--/) src[cur] = src[cur] " " $i
		}
		END {
			s = want
			while (s != "") {
				print src[s]
				s = (parent[s] in src || parent[s] in parent) ? parent[s] : ""
			}
		}
	' "$file" | tr ' ' '\n' | sed -e 's|/$||' -e '/^$/d' -e '/^\.$/d' | sort -u
}

fail=0
for entry in "${stages[@]}"; do
	read -r file stage prog <<<"$entry"
	if ! grep -qE "^FROM .* [Aa][Ss] ${stage}\$" "$file"; then
		echo "FAIL: $file has no stage $stage (update this script's list)"
		fail=1
		continue
	fi
	have="$(copies "$file" "$stage")"
	# Every module package the program and its tests need, as repository dirs.
	need="$(CGO_ENABLED=1 go list -tags novulkan -deps -test -f '{{if .Module}}{{if eq .Module.Path "'"$module"'"}}{{.Dir}}{{end}}{{end}}' "$prog" |
		sed -n "s|^$PWD/||p" | sort -u)"
	missing=""
	for dir in $need; do
		covered=false
		for c in $have; do
			case "$dir/" in "$c"/*) covered=true; break ;; esac
		done
		$covered || missing="$missing $dir"
	done
	if [ -n "$missing" ]; then
		echo "FAIL: $file stage $stage ($prog) does not copy:$missing"
		fail=1
	else
		echo "PASS: $file stage $stage copies every package $prog needs"
	fi
done
exit "$fail"
