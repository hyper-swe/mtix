#!/bin/sh
# Copyright 2025-2026 HyperSWE
# SPDX-License-Identifier: Apache-2.0
#
# waiter.sh: single-instance guard around a wait command (MTIX-112.40.1).
#
# usage: waiter.sh <lock-path> <log-file> <label> <command> [args...]
#
# Acquires <lock-path>, a symlink whose text is "pid:<pid of this script>"
# (created atomically with ln -s, so the holder is never unknown), runs the
# wrapped command as a child, releases the lock on exit and exits with the
# command's exit code (exit code 2 is the wake signal and passes through).
# A second arm while the holder is alive logs guard-skip and exits 0. A
# stale lock (holder dead, or lock text not "pid:<number>") is taken over
# by exactly one arm: takeover is serialised through the <lock-path>.t
# directory, and the stale holder is re-read under that serialiser.
# Test-only environment variable: WAITER_TEST_HOLD names a file; while it
# exists a taker keeps the <lock-path>.t serialiser, so a test can plant a
# live holder deterministically. It is inert when unset or empty and must
# never be set outside tests.
# A TERM, INT or HUP to the script stops the wrapped command, releases the
# lock and exits 143.
# Residual risks, accepted for a test tool: a pid reused by an unrelated
# process looks alive, and a taker killed between mkdir and rmdir of
# <lock-path>.t blocks further takeovers until that directory is removed;
# an arm that meets such a stuck serialiser (still held after about one
# second with no live holder) logs guard-skip-takeover-busy, so the
# evidence tells a harness fault from a live holder.

if [ "$#" -lt 4 ]; then
	echo "usage: waiter.sh <lock-path> <log-file> <label> <command> [args...]" >&2
	exit 64
fi
LOCK=$1
LOGF=$2
LABEL=$3
shift 3
ME="pid:$$"

log() {
	printf '%s %s %s\n' "$(date +%s)" "$LABEL" "$1" >>"$LOGF"
}

# holder_alive succeeds when the lock text names a live process.
holder_alive() {
	text=$(readlink "$LOCK" 2>/dev/null) || return 1
	case $text in
	pid:[0-9]*)
		pid=${text#pid:}
		case $pid in *[!0-9]*) return 1 ;; esac
		kill -0 "$pid" 2>/dev/null
		;;
	*) return 1 ;;
	esac
}

# takeover_busy waits briefly for a contended serialiser. It returns 1
# when a live holder appeared (a plain guard-skip) and 2 when the
# serialiser stays held with no live holder (a stuck taker).
takeover_busy() {
	n=0
	while [ "$n" -lt 10 ]; do
		holder_alive && return 1
		sleep 0.1
		n=$((n + 1))
	done
	holder_alive && return 1
	return 2
}

# acquire returns 0 when this arm holds the lock, 1 when a live holder
# exists and 2 when a stale lock cannot be taken over (stuck serialiser).
acquire() {
	if ln -s "$ME" "$LOCK" 2>/dev/null; then
		return 0
	fi
	if holder_alive; then
		return 1
	fi
	# Stale: only the arm that creates the serialiser may take over.
	if ! mkdir "$LOCK.t" 2>/dev/null; then
		takeover_busy
		return $?
	fi
	# Test hook: hold the serialiser while the named file exists.
	while [ -n "$WAITER_TEST_HOLD" ] && [ -e "$WAITER_TEST_HOLD" ]; do
		sleep 0.02
	done
	took=1
	if holder_alive; then
		took=1
	else
		rm -f "$LOCK"
		if ln -s "$ME" "$LOCK" 2>/dev/null; then
			log stale-takeover
			took=0
		fi
	fi
	rmdir "$LOCK.t" 2>/dev/null
	return "$took"
}

release() {
	if [ "$(readlink "$LOCK" 2>/dev/null)" = "$ME" ]; then
		rm -f "$LOCK"
	fi
}

acquire
case $? in
0) ;;
2)
	log guard-skip-takeover-busy
	exit 0
	;;
*)
	log guard-skip
	exit 0
	;;
esac
trap release EXIT
# The wrapped command runs in the background and the script waits for it, so
# a signal reaches the trap at once and stops the command too (a foreground
# child would delay the trap until it returned). Its stdin is /dev/null.
child=
trap 'if [ -n "$child" ]; then kill "$child" 2>/dev/null; fi; exit 143' INT TERM HUP
log armed
"$@" &
child=$!
wait "$child"
rc=$?
log "exit-$rc"
exit "$rc"
