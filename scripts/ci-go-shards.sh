#!/usr/bin/env bash
# MTIX-112.17.22: fixed package lists may never silently omit or repeat a package.
# MTIX-112.17.27: the same guard for the remote bar's package sets, and a
# test-level hash shard mode for the slow packages (sqlite, service).
#
#   bash scripts/ci-go-shards.sh check | list {sqlite|cli|service|other}             ci.yml shards
#   bash scripts/ci-go-shards.sh bar-check | bar-list {serial|parallel|sharded}
#       remote bar package sets (scripts/remote-bar-packages/*.txt); cmd/mtix
#       has its own shards (scripts/cmd-mtix-shards.sh) and is not listed
#   bash scripts/ci-go-shards.sh test-check <dir> <N>
#   bash scripts/ci-go-shards.sh test-pattern <dir> <N> <k>
#       split one package's tests into N shards by a stable hash of the test
#       name; test-pattern prints the -run regex of shard k. The shard count is
#       the caller's: ci.yml and remote-bar.yml run internal/store/sqlite in
#       6 (MTIX-131.2) and internal/service in 2
#   A package may pin known-heavy tests to a shard in
#   scripts/ci-go-shard-pins/<dir with / as ->.txt (MTIX-131.2): first line
#   "shards <N>" or "shards <N> hash <H>", then "<TestName> <shard>" lines
#   (# comments allowed). The file holds for that N only; any other count, a
#   malformed, duplicate or stale pin (a name the binary no longer lists)
#   fails. Unpinned tests keep their hash shard, over shards 1..H (default N),
#   so shards above H hold pinned tests only.
set -euo pipefail
export LC_ALL=C

die() {
	printf 'go-shards: %s\n' "$*" >&2
	exit 1
}

usage() {
	cat >&2 <<'USAGE'
usage: bash scripts/ci-go-shards.sh check | list {sqlite|cli|service|other}
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
	case "$shard" in sqlite | cli | service | other) ;; *) usage ;; esac
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
		"$scratch/assigned" "$scratch/selected" "$scratch/twice" "$scratch/never" \
		"$scratch/pins" "$scratch/pins.names"
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
	go test -race -list '.*' "./$1/" >"$scratch/listing" || die "cannot list the $1 tests"
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

# load_pins <dir>: reads scripts/ci-go-shard-pins/<dir with / as ->.txt, if
# present, into $scratch/pins ("<name> <shard>"). The first non-comment line
# must be "shards <N>" or "shards <N> hash <H>" with N equal to the caller's
# count and 1 <= H <= N (HASH, default N); every later line is one pin. A
# malformed, duplicate or stale pin fails, so a pin can never make a test run
# twice or not at all (the exact-split check below still runs).
load_pins() {
	local file line first=1
	local -a f
	file="scripts/ci-go-shard-pins/${1//\//-}.txt"
	: >"$scratch/pins"
	: >"$scratch/pins.names"
	HASH=$SHARDS
	[ -f "$file" ] || return 0
	while IFS= read -r line || [ -n "$line" ]; do
		line=${line%%#*}
		read -r -a f <<<"$line" || true
		[ "${#f[@]}" -eq 0 ] && continue
		if [ "$first" = 1 ]; then
			first=0
			{ [ "${f[0]}" = shards ] && [[ "${f[1]:-}" =~ ^[1-9][0-9]*$ ]] && { [ "${#f[@]}" -eq 2 ] ||
				{ [ "${#f[@]}" -eq 4 ] && [ "${f[2]}" = hash ] && [[ "${f[3]}" =~ ^[1-9][0-9]*$ ]]; }; }; } ||
				die "$file: first line must be 'shards <N>' or 'shards <N> hash <H>'"
			[ "${f[1]}" = "$SHARDS" ] || die "$file: pins are for ${f[1]} shards, not $SHARDS"
			if [ "${#f[@]}" -eq 4 ]; then
				[ "${f[3]}" -le "$SHARDS" ] || die "$file: hash ${f[3]} exceeds ${f[1]} shards"
				HASH=${f[3]}
			fi
			continue
		fi
		if [ "${#f[@]}" -ne 2 ] || ! [[ "${f[0]}" =~ ^(Test|Example|Fuzz)[A-Za-z0-9_]*$ ]] ||
			! [[ "${f[1]}" =~ ^[0-9]+$ ]] || [ "${f[1]}" -lt 1 ] || [ "${f[1]}" -gt "$SHARDS" ]; then
			die "invalid pin in $file: $line"
		fi
		! grep -qxF -- "${f[0]}" "$scratch/pins.names" || die "duplicate pin ${f[0]} in $file"
		grep -qxF -- "${f[0]}" "$scratch/names" || die "stale pin ${f[0]} in $file: the test binary does not list it"
		printf '%s\n' "${f[0]}" >>"$scratch/pins.names"
		printf '%s %s\n' "${f[0]}" "${f[1]}" >>"$scratch/pins"
	done <"$file"
	[ "$first" = 0 ] || die "$file: first line must be 'shards <N>' or 'shards <N> hash <H>'"
}

# assign_shards: a pinned name goes to its pin; any other name to
# djb2(name) mod HASH + 1 (HASH is SHARDS unless the pin file lowers it). awk
# doubles stay exact: h < 2^32, h*33 < 2^38.
assign_shards() {
	awk -v n="$HASH" -v pins="$scratch/pins" '
BEGIN {
	while ((getline line < pins) > 0) {
		split(line, pf, " ")
		pin[pf[1]] = pf[2]
	}
	close(pins)
	chars = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZ_abcdefghijklmnopqrstuvwxyz"
}
{
	h = 5381
	for (i = 1; i <= length($0); i++) {
		h = (h * 33 + index(chars, substr($0, i, 1))) % 4294967296
	}
	if ($0 in pin) print pin[$0], $0
	else print (h % n) + 1, $0
}' "$scratch/names" >"$scratch/assigned"
}

pattern_of() {
	awk -v s="$1" '$1 == s { names = names (names == "" ? "" : "|") $2 } END { if (names != "") print "^(" names ")$" }' "$scratch/assigned"
}

case "$mode" in
check | list)
	EXCLUDE='^$'
	: >"$scratch/all"
	check_packages scripts/ci-go-shards sqlite cli service other
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
	load_pins "$pkgdir"
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
