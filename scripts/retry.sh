#!/usr/bin/env bash
# retry.sh ATTEMPTS DELAY CMD [ARG...]
#
# Runs CMD until it exits 0, at most ATTEMPTS times. It waits DELAY seconds
# after the first failure and doubles the wait after each one, capped at 120
# seconds. It exits with CMD's last status. The release uses it for calls to
# registries, sigstore and the GitHub API, where a 5xx, an EOF or a tag the
# registry does not list yet is common and goes away on its own.
set -uo pipefail

if [ "$#" -lt 3 ] || ! [[ "$1" =~ ^[1-9][0-9]*$ ]] || ! [[ "$2" =~ ^[0-9]+$ ]]; then
	echo "usage: $0 ATTEMPTS DELAY CMD [ARG...]" >&2
	exit 2
fi
attempts=$1 delay=$2
shift 2

n=1
while :; do
	"$@"
	status=$?
	[ "$status" -eq 0 ] && exit 0
	if [ "$n" -ge "$attempts" ]; then
		echo "retry: '$1' failed $n times (last status $status), giving up" >&2
		exit "$status"
	fi
	echo "retry: '$1' failed (status $status, attempt $n of $attempts), trying again in ${delay}s" >&2
	sleep "$delay"
	n=$((n + 1))
	delay=$((delay * 2))
	[ "$delay" -gt 120 ] && delay=120
done
