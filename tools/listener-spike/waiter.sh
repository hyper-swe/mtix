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
# file, and the stale holder is re-read under that serialiser.
# Test-only environment variable: WAITER_TEST_HOLD names a file; while it
# exists a taker keeps the <lock-path>.t serialiser, so a test can plant a
# live holder deterministically. It is inert when unset or empty and must
# never be set outside tests.
# A TERM, INT or HUP to the script stops the wrapped command, releases the
# lock and exits 143.
# Residual risks, accepted for a test tool: a pid reused by an unrelated
# process looks alive, and an arm killed while owning <lock-path>.t
# blocks further acquisitions until that file is removed;
# an arm that meets such a stuck serialiser (still held after about one
# second with no live holder) logs guard-skip-takeover-busy, so the
# evidence tells a harness fault from a live holder. Under extreme load a
# fresh-lock waiter can also exceed that second while the winner has not yet
# linked, and log takeover-busy; this is a documented residual.
# A lock path that is not a symlink (a real directory or a regular file) is
# never taken over: the arm logs guard-skip-lock-not-symlink and exits 0,
# and leaves the path as found.
#
# Why every acquisition is serialised (MTIX-285). The base script guarded
# only the takeover of a stale link. Two arms could both see a stale or
# absent lock; one removes the stale link and relinks, and the other's
# unserialised fresh ln -s then succeeds after that remove. Both arm (2x
# stale-takeover and 2x armed, seen in 27 of 500 loaded runs). Fresh and
# stale paths now both go through the noclobber .t file. The unserialised
# fresh-link path is eliminated; the exact mechanism is inferred from the
# 2x stale-takeover + 2x armed signature.

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
	pid_alive "$text"
}

pid_alive() {
	case $1 in
	pid:[0-9]*)
		pid=${1#pid:}
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
# exists, 2 when a stale lock cannot be taken over (stuck serialiser) and 3
# when the lock path is not a symlink.
# The subshell is load-bearing: its EXIT trap removes $LOCK.t when acquire
# returns; braces would let the trap be replaced and leak the serialiser.
acquire() (
	if holder_alive; then
		return 1
	fi
	# Noclobber creates a regular file exclusively. Protect fresh installs
	# too, so no arm can enter between removing a stale link and relinking.
	if ! (set -C; : >"$LOCK.t") 2>/dev/null; then
		takeover_busy
		return $?
	fi
	trap 'rm -f "$LOCK.t"' EXIT
	while [ -n "$WAITER_TEST_HOLD" ] && [ -e "$WAITER_TEST_HOLD" ]; do
		sleep 0.02
	done
	stale=false
	if [ -L "$LOCK" ]; then
		text=$(readlink "$LOCK") || return 1
		pid_alive "$text" && return 1
		rm -f "$LOCK" || return 2
		stale=true
	elif [ -e "$LOCK" ]; then
		return 3
	fi
	ln -s "$ME" "$LOCK" || return 2
	if "$stale"; then
		log stale-takeover
	fi
	return 0
)

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
3)
	log guard-skip-lock-not-symlink
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
