#!/usr/bin/env bash
# MTIX-112.17.24: deterministic shards of the cmd/mtix tests.
#
# cmd/mtix alone approached the 20m per-package go test budget in the
# PostgreSQL CI jobs and the remote bar. Its tests are split into SHARDS
# groups by a stable hash of the test name, so a new test is assigned to a
# shard automatically and never runs twice or not at all. The listed names
# come from the compiled test binary (go test -list), the same source go test
# -run selects from.
#
#   bash scripts/cmd-mtix-shards.sh check            fail unless the split is exact
#   bash scripts/cmd-mtix-shards.sh pattern <1..N>   print the -run regex of one shard
#
# Typical use (timeout, -race and -count are unchanged, nothing is skipped):
#   bash scripts/cmd-mtix-shards.sh check
#   go test ./cmd/mtix/ -race -count=1 -timeout 20m -run "$(bash scripts/cmd-mtix-shards.sh pattern 2)"
set -euo pipefail
export LC_ALL=C

SHARDS=3

die() {
	printf 'cmd-mtix-shards: %s\n' "$*" >&2
	exit 1
}

usage() {
	echo "usage: bash scripts/cmd-mtix-shards.sh check | pattern <1..$SHARDS>" >&2
	exit 2
}

mode=${1:-}
case "$mode" in
check) [ "$#" -eq 1 ] || usage ;;
pattern)
	[ "$#" -eq 2 ] || usage
	[[ "$2" =~ ^[0-9]+$ ]] || usage
	[ "$2" -ge 1 ] && [ "$2" -le "$SHARDS" ] || usage
	;;
*) usage ;;
esac

cd "$(dirname "${BASH_SOURCE[0]}")/.."
scratch=$(mktemp -d "${TMPDIR:-/tmp}/mtix-cmd-shards.XXXXXX")
cleanup() {
	rm -f "$scratch/listing" "$scratch/names" "$scratch/assigned" "$scratch/odd" \
		"$scratch/selected" "$scratch/twice" "$scratch/never"
	rmdir "$scratch"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

# List every test the binary can run (tests, examples, fuzz seeds). The list
# is read from a file so that a failed go test is not hidden by a pipe.
go test -list '.*' ./cmd/mtix/ >"$scratch/listing" || die 'cannot list the cmd/mtix tests'
# A listed test whose name falls outside the safe character set would be in no
# shard, so refuse it instead of dropping it silently.
grep -E '^(Test|Example|Fuzz)' "$scratch/listing" | grep -vE '^(Test|Example|Fuzz)[A-Za-z0-9_]*$' >"$scratch/odd" || true
if [ -s "$scratch/odd" ]; then
	printf 'cmd-mtix-shards: unsupported test name (cannot be selected by an exact -run pattern):\n' >&2
	cat "$scratch/odd" >&2
	exit 1
fi
grep -E '^(Test|Example|Fuzz)[A-Za-z0-9_]*$' "$scratch/listing" | sort -u >"$scratch/names" || true
[ -s "$scratch/names" ] || die 'no tests listed for cmd/mtix'

# shard = djb2(name) mod SHARDS + 1. awk doubles stay exact: h < 2^32, h*33 < 2^38.
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

pattern_of() {
	awk -v s="$1" '$1 == s { names = names (names == "" ? "" : "|") $2 } END { if (names != "") print "^(" names ")$" }' "$scratch/assigned"
}

if [ "$mode" = pattern ]; then
	p=$(pattern_of "$2")
	[ -n "$p" ] || die "empty shard $2"
	printf '%s\n' "$p"
	exit 0
fi

# check: every shard is non-empty and each name is selected by exactly one
# shard's real run pattern. The names each shard selects are collected, then
# compared per name: a name in two shards and a name in none both fail, even
# when the counts happen to add up.
total=$(wc -l <"$scratch/names" | tr -d ' ')
: >"$scratch/selected"
for s in $(seq 1 "$SHARDS"); do
	p=$(pattern_of "$s")
	[ -n "$p" ] || die "empty shard $s"
	grep -E "$p" "$scratch/names" >>"$scratch/selected" || true
done
sort "$scratch/selected" | uniq -d >"$scratch/twice"
sort -u "$scratch/selected" | comm -23 "$scratch/names" - >"$scratch/never"
problems=0
if [ -s "$scratch/twice" ]; then
	printf 'cmd-mtix-shards: tests selected by more than one shard:\n' >&2
	cat "$scratch/twice" >&2
	problems=1
fi
if [ -s "$scratch/never" ]; then
	printf 'cmd-mtix-shards: tests selected by no shard:\n' >&2
	cat "$scratch/never" >&2
	problems=1
fi
[ "$problems" -eq 0 ] || exit 1
printf 'cmd-mtix-shards: %s tests in %s shards, split is exact\n' "$total" "$SHARDS"
