#!/usr/bin/env bash
# MTIX-112.17.22: fixed package lists may never silently omit or repeat a package.
set -euo pipefail
export LC_ALL=C

die() {
	printf 'go-shards: %s\n' "$*" >&2
	exit 1
}

usage() {
	echo 'usage: bash scripts/ci-go-shards.sh check | list {sqlite|cli|other}' >&2
	exit 2
}

mode=${1:-}
shard=${2:-}
case "$mode" in
check) [ "$#" -eq 1 ] || usage ;;
list)
	[ "$#" -eq 2 ] || usage
	case "$shard" in sqlite | cli | other) ;; *) usage ;; esac
	;;
*) usage ;;
esac

cd "$(dirname "${BASH_SOURCE[0]}")/.."
scratch=$(mktemp -d "${TMPDIR:-/tmp}/mtix-go-shards.XXXXXX")
cleanup() {
	rm -f "$scratch/actual" "$scratch/all" "$scratch/listed" \
		"$scratch/duplicate" "$scratch/unlisted" "$scratch/stale"
	rmdir "$scratch"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

# pipefail preserves go list errors, including an error after partial output.
if ! go list ./... | grep -v node_modules | sort >"$scratch/actual"; then
	die 'cannot enumerate the selected Go packages'
fi
[ -s "$scratch/actual" ] || die 'empty selected Go package inventory'

for name in sqlite cli other; do
	manifest="scripts/ci-go-shards/$name.txt"
	[ -s "$manifest" ] || die "missing or empty manifest: $manifest"
	cat "$manifest" >>"$scratch/all"
done
sort "$scratch/all" >"$scratch/listed"
uniq -d "$scratch/listed" >"$scratch/duplicate"
comm -23 "$scratch/actual" "$scratch/listed" >"$scratch/unlisted"
comm -13 "$scratch/actual" "$scratch/listed" >"$scratch/stale"

problems=0
for kind in duplicate unlisted stale; do
	if [ -s "$scratch/$kind" ]; then
		printf 'go-shards: %s packages:\n' "$kind" >&2
		cat "$scratch/$kind" >&2
		problems=1
	fi
done
[ "$problems" -eq 0 ] || exit 1

if [ "$mode" = list ]; then
	cat "scripts/ci-go-shards/$shard.txt"
fi
