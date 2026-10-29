#!/usr/bin/env bash
# MTIX-112.17.22: fixed package lists may never silently omit or repeat a package.
# MTIX-112.17.27: the same guard for the remote bar's package sets, and a
# test-level hash shard mode for the slow packages (sqlite, service).
#
#   bash scripts/ci-go-shards.sh check | list {sqlite|cli|other}             ci.yml shards
#   bash scripts/ci-go-shards.sh bar-check | bar-list {serial|parallel|sharded}
#       remote bar package sets (scripts/remote-bar-packages/*.txt); cmd/mtix
#       has its own shards (scripts/cmd-mtix-shards.sh) and is not listed
#   bash scripts/ci-go-shards.sh test-check <dir> <N>
#   bash scripts/ci-go-shards.sh test-pattern <dir> <N> <k>
#       split one package's tests into N shards by a stable hash of the test
#       name; test-pattern prints the -run regex of shard k
set -euo pipefail
export LC_ALL=C

die() {
	printf 'go-shards: %s\n' "$*" >&2
	exit 1
}

usage() {
	cat >&2 <<'USAGE'
usage: bash scripts/ci-go-shards.sh check | list {sqlite|cli|other}
       bash scripts/ci-go-shards.sh bar-check | bar-list {serial|parallel|sharded}
       bash scripts/ci-go-shards.sh test-check <dir> <N> | test-pattern <dir> <N> <k>
USAGE
	exit 2
}

mode=${1:-}
shard=${2:-}
pkgdir=${2:-}
SHARDS=${3:-}
case "$mode" in
check | bar-check) [ "$#" -eq 1 ] || usage ;;
list)
	[ "$#" -eq 2 ] || usage
	case "$shard" in sqlite | cli | other) ;; *) usage ;; esac
	;;
bar-list)
	[ "$#" -eq 2 ] || usage
	case "$shard" in serial | parallel | sharded) ;; *) usage ;; esac
	;;
test-check) [ "$#" -eq 3 ] || usage ;;
test-pattern)
	[ "$#" -eq 4 ] || usage
	[[ "$4" =~ ^[0-9]+$ ]] || usage
	;;
*) usage ;;
esac
case "$mode" in
test-*)
	[[ "$pkgdir" =~ ^[A-Za-z0-9_][A-Za-z0-9_./-]*$ ]] || usage
	case "/$pkgdir/" in */../*) usage ;; esac
	{ [[ "$SHARDS" =~ ^[0-9]+$ ]] && [ "$SHARDS" -ge 1 ]; } || usage
	if [ "$mode" = test-pattern ]; then
		{ [ "$4" -ge 1 ] && [ "$4" -le "$SHARDS" ]; } || usage
	fi
	;;
esac

cd "$(dirname "${BASH_SOURCE[0]}")/.."
scratch=$(mktemp -d "${TMPDIR:-/tmp}/mtix-go-shards.XXXXXX")
cleanup() {
	rm -f "$scratch/actual" "$scratch/all" "$scratch/listed" \
		"$scratch/duplicate" "$scratch/unlisted" "$scratch/stale" \
		"$scratch/listing" "$scratch/odd" "$scratch/names" "$scratch/expected" \
		"$scratch/assigned" "$scratch/selected" "$scratch/twice" "$scratch/never"
	rmdir "$scratch"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

# check_packages <manifest-dir> <names...>: every package of go list ./...
# (minus cmd/mtix for the bar sets) is in exactly one manifest.
check_packages() {
	local dir=$1 name manifest kind
	shift
	# pipefail preserves go list errors, including an error after partial output.
	if ! go list ./... | grep -v node_modules | grep -vxF "$EXCLUDE" | sort >"$scratch/actual"; then
		die 'cannot enumerate the selected Go packages'
	fi
	[ -s "$scratch/actual" ] || die 'empty selected Go package inventory'
	for name in "$@"; do
		manifest="$dir/$name.txt"
		[ -s "$manifest" ] || die "missing or empty manifest: $manifest"
		cat "$manifest" >>"$scratch/all"
	done
	sort "$scratch/all" >"$scratch/listed"
	uniq -d "$scratch/listed" >"$scratch/duplicate"
	comm -23 "$scratch/actual" "$scratch/listed" >"$scratch/unlisted"
	comm -13 "$scratch/actual" "$scratch/listed" >"$scratch/stale"
	local problems=0
	for kind in duplicate unlisted stale; do
		if [ -s "$scratch/$kind" ]; then
			printf 'go-shards: %s packages:\n' "$kind" >&2
			cat "$scratch/$kind" >&2
			problems=1
		fi
	done
	[ "$problems" -eq 0 ] || exit 1
}

# check_pg_consumers: a package whose *_test.go reads MTIX_PG_TEST_DSN shares
# the one database, so it must be in the serial set (remote bar only).
check_pg_consumers() {
	local file dir pkg bad=0
	while IFS= read -r file; do
		dir=$(dirname "$file")
		[ "$dir" != . ] || continue
		pkg=$(grep -E "/${dir#./}\$" "$scratch/actual" || true)
		[ -n "$pkg" ] || continue
		if ! grep -qxF "$pkg" scripts/remote-bar-packages/serial.txt; then
			printf 'go-shards: PostgreSQL consumer outside the serial set: %s\n' "$pkg" >&2
			bad=1
		fi
	done < <(grep -rlE --include='*_test.go' --exclude-dir=node_modules --exclude-dir=.git --exclude-dir=web \
		'(=[[:space:]]*|(Getenv|LookupEnv)\([[:space:]]*)"MTIX_PG_TEST_DSN"' . | sort -u)
	[ "$bad" -eq 0 ] || exit 1
}

# test_names <dir>: lists the package's tests twice by two independent
# extractions (names: the safe-name set that is hashed; expected: every
# Test/Example/Fuzz line the binary printed), so that dropping a name from the
# hashed set shows up as a test selected by no shard.
test_names() {
	# The list is read from a file so that a failed go test is not hidden by a pipe.
	go test -list '.*' "./$1/" >"$scratch/listing" || die "cannot list the $1 tests"
	grep -E '^(Test|Example|Fuzz)' "$scratch/listing" | grep -vE '^(Test|Example|Fuzz)[A-Za-z0-9_]*$' >"$scratch/odd" || true
	if [ -s "$scratch/odd" ]; then
		printf 'go-shards: unsupported test name (cannot be selected by an exact -run pattern):\n' >&2
		cat "$scratch/odd" >&2
		exit 1
	fi
	grep -E '^(Test|Example|Fuzz)[A-Za-z0-9_]*$' "$scratch/listing" | sort -u >"$scratch/names" || true
	awk '/^(Test|Example|Fuzz)/ { print $1 }' "$scratch/listing" | sort -u >"$scratch/expected"
	[ -s "$scratch/expected" ] || die "no tests listed for $1"
	[ -s "$scratch/names" ] || die "no tests listed for $1"
}

# assign_shards: shard = djb2(name) mod SHARDS + 1. awk doubles stay exact:
# h < 2^32, h*33 < 2^38.
assign_shards() {
	awk -v n="$SHARDS" '
BEGIN {
	chars = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZ_abcdefghijklmnopqrstuvwxyz"
}
{
	h = 5381
	for (i = 1; i <= length($0); i++) {
		h = (h * 33 + index(chars, substr($0, i, 1))) % 4294967296
	}
	print (h % n) + 1, $0
}' "$scratch/names" >"$scratch/assigned"
}

pattern_of() {
	awk -v s="$1" '$1 == s { names = names (names == "" ? "" : "|") $2 } END { if (names != "") print "^(" names ")$" }' "$scratch/assigned"
}

case "$mode" in
check | list)
	EXCLUDE='^$'
	: >"$scratch/all"
	check_packages scripts/ci-go-shards sqlite cli other
	[ "$mode" != list ] || cat "scripts/ci-go-shards/$shard.txt"
	;;
bar-check | bar-list)
	EXCLUDE=github.com/hyper-swe/mtix/cmd/mtix
	: >"$scratch/all"
	check_packages scripts/remote-bar-packages serial parallel sharded
	check_pg_consumers
	if [ "$mode" = bar-list ]; then
		cat "scripts/remote-bar-packages/$shard.txt"
	else
		printf 'go-shards: %s packages in 3 sets, split is exact\n' "$(wc -l <"$scratch/listed" | tr -d ' ')"
	fi
	;;
test-check | test-pattern)
	[ -d "$pkgdir" ] || die "no such package directory: $pkgdir"
	test_names "$pkgdir"
	assign_shards
	if [ "$mode" = test-pattern ]; then
		p=$(pattern_of "$4")
		[ -n "$p" ] || die "empty shard $4"
		printf '%s\n' "$p"
		exit 0
	fi
	# Each name is selected by exactly one shard's real run pattern: a name in
	# two shards and a name in none both fail, even when the counts add up.
	: >"$scratch/selected"
	for s in $(seq 1 "$SHARDS"); do
		p=$(pattern_of "$s")
		[ -n "$p" ] || die "empty shard $s"
		grep -E "$p" "$scratch/expected" >>"$scratch/selected" || true
	done
	sort "$scratch/selected" | uniq -d >"$scratch/twice"
	sort -u "$scratch/selected" | comm -23 "$scratch/expected" - >"$scratch/never"
	problems=0
	if [ -s "$scratch/twice" ]; then
		printf 'go-shards: tests selected by more than one shard:\n' >&2
		cat "$scratch/twice" >&2
		problems=1
	fi
	if [ -s "$scratch/never" ]; then
		printf 'go-shards: tests selected by no shard:\n' >&2
		cat "$scratch/never" >&2
		problems=1
	fi
	[ "$problems" -eq 0 ] || exit 1
	printf 'go-shards: %s tests in %s shards, split is exact\n' "$(wc -l <"$scratch/expected" | tr -d ' ')" "$SHARDS"
	;;
esac
