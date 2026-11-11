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
# Residual risks, accepted for a test tool: a pid reused by an unrelated
# process looks alive, and a taker killed between mkdir and rmdir of
# <lock-path>.t blocks further takeovers until that directory is removed.

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

acquire() {
	if ln -s "$ME" "$LOCK" 2>/dev/null; then
		return 0
	fi
	if holder_alive; then
		return 1
	fi
	# Stale: only the arm that creates the serialiser may take over.
	if ! mkdir "$LOCK.t" 2>/dev/null; then
		return 1
	fi
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

if ! acquire; then
	log guard-skip
	exit 0
fi
trap release EXIT
trap 'exit 143' INT TERM HUP
log armed
"$@"
rc=$?
log "exit-$rc"
exit "$rc"
