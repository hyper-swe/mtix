#!/usr/bin/env sh
# wake-agent.sh — reference cold-start wake script (FR-20 §9 rung 1).
#
# Wire it as an exec hook on the host where the agent should run
# (placement is designation — only this host fires it):
#
#   # .mtix/hooks.yaml
#   hooks:
#     - name: wake-developer
#       match:
#         events: [comment.addressed]
#         to-agent: developer
#       deliver: [exec]
#       exec:
#         command: ["/path/to/wake-agent.sh", "developer"]
#         timeout-seconds: 86400
#
# Then review and trust the config on this host: mtix hooks trust
#
# A local lease coalesces concurrent wakes. The agent handles and acknowledges
# events; the script rechecks before releasing and never acknowledges them.
# Renewal failures lasting beyond the lease TTL, or SIGKILL with a surviving
# harness, can permit overlapping runs. This reference is not hard fencing.
#
# NOTE (MTIX-56.9): mtix spawns this script DETACHED and does not wait;
# timeout-seconds is best-effort. Long launches are fine, but self-bound
# anything that could hang. Exit codes are yours to report (log or
# mtix comment) — the fabric's success signal is the inbox ack.
#
# The launch line is harness-specific; keep exactly one uncommented.

set -eu
umask 077
AGENT="${1:?usage: wake-agent.sh <agent-id>}"
WAKE_AGENT_LOG="${WAKE_AGENT_LOG:-${XDG_STATE_HOME:-${HOME:?}/.local/state}/mtix/wake-agent.log}"
HOLDER= RENEWER= CHILD=
WAKE_MAX_RUNS="${WAKE_MAX_RUNS:-20}"
case "$WAKE_MAX_RUNS" in
    ''|*[!0-9]*) printf '%s\n' 'wake-agent: WAKE_MAX_RUNS must be a positive integer' >&2; exit 1 ;;
esac
[ "$WAKE_MAX_RUNS" -gt 0 ] || exit 1

report() {
    MESSAGE="$(date -u '+%Y-%m-%dT%H:%M:%SZ') wake-agent: $*"
    printf '%s\n' "$MESSAGE" >&2
    LOG_DIR=$(dirname "$WAKE_AGENT_LOG")
    if mkdir -p "$LOG_DIR" && chmod 700 "$LOG_DIR"; then
        printf '%s\n' "$MESSAGE" >> "$WAKE_AGENT_LOG" || printf '%s\n' 'wake-agent: cannot write log' >&2
    else
        printf '%s\n' 'wake-agent: cannot create log directory' >&2
    fi
}

resolve_tool() {
    TOOL=$1
    case "$TOOL" in
        */*) FOUND=$TOOL ;;
        *) FOUND=$(command -v "$TOOL" 2>/dev/null) || FOUND=
           if [ -z "$FOUND" ]; then
               for TOOL_DIR in /opt/homebrew/bin /usr/local/bin "$HOME/.local/bin" "$HOME/go/bin"; do
                   if [ -x "$TOOL_DIR/$TOOL" ]; then FOUND=$TOOL_DIR/$TOOL; break; fi
               done
           fi ;;
    esac
    [ -n "$FOUND" ] && [ -f "$FOUND" ] && [ -x "$FOUND" ] || return 1
    TOOL_DIR=$(CDPATH= cd -- "$(dirname "$FOUND")" && pwd -P) || return 1
    printf '%s/%s\n' "$TOOL_DIR" "$(basename "$FOUND")"
}

MTIX_BIN=$(resolve_tool "${MTIX_BIN:-mtix}") || { report 'mtix executable unavailable'; exit 127; }
HARNESS_BIN=$(resolve_tool "${HARNESS_BIN:-claude}") || { report 'harness executable unavailable'; exit 127; }

stop_children() {
    if [ -n "$CHILD" ]; then kill "$CHILD" 2>/dev/null || :; wait "$CHILD" 2>/dev/null || :; CHILD=; fi
    if [ -n "$RENEWER" ]; then kill "$RENEWER" 2>/dev/null || :; wait "$RENEWER" 2>/dev/null || :; RENEWER=; fi
}

cleanup() {
    stop_children
    if [ -n "$HOLDER" ]; then
        "$MTIX_BIN" inbox lease release --agent "$AGENT" --holder "$HOLDER" --abandon >/dev/null || report 'could not abandon lease'
        HOLDER=
    fi
}

interrupted() {
    trap '' INT TERM HUP USR1
    report 'wake interrupted; stopping owned processes'
    cleanup
    exit 1
}

lost_lease() {
    trap '' INT TERM HUP USR1
    report 'lease no longer owned; stopping wake'
    HOLDER=
    stop_children
    exit 1
}

trap cleanup EXIT
trap interrupted INT TERM HUP
trap lost_lease USR1

if HOLDER=$("$MTIX_BIN" inbox lease acquire --agent "$AGENT" --ttl 15m 2>&1); then STATUS=0; else STATUS=$?; fi
if [ "$STATUS" -eq 6 ]; then HOLDER=; exit 0; fi
if [ "$STATUS" -ne 0 ]; then report "lease acquisition failed ($STATUS): $HOLDER"; HOLDER=; exit 1; fi

PARENT=$$
renew_loop() {
    SLEEP_PID=
    trap 'if [ -n "$SLEEP_PID" ]; then kill "$SLEEP_PID" 2>/dev/null || :; wait "$SLEEP_PID" 2>/dev/null || :; fi; exit 0' TERM INT HUP
    while kill -0 "$PARENT" 2>/dev/null; do
        sleep 60 & SLEEP_PID=$!
        wait "$SLEEP_PID" || exit 0
        SLEEP_PID=
        kill -0 "$PARENT" 2>/dev/null || exit 0
        if "$MTIX_BIN" inbox lease acquire --agent "$AGENT" --holder "$HOLDER" --ttl 15m >/dev/null; then STATUS=0; else STATUS=$?; fi
        if [ "$STATUS" -eq 6 ]; then kill -USR1 "$PARENT" 2>/dev/null || :; exit 0; fi
        [ "$STATUS" -eq 0 ] || report "lease renewal failed ($STATUS); TTL recovery remains available"
    done
}
renew_loop & RENEWER=$!

RUNS=0
while :; do
    if PAYLOAD=$("$MTIX_BIN" inbox --agent "$AGENT" --format prompt); then STATUS=0; else STATUS=$?; fi
    if [ "$STATUS" -ne 0 ]; then report "inbox read failed ($STATUS)"; exit "$STATUS"; fi
    if [ -n "$PAYLOAD" ]; then
        if [ "$RUNS" -ge "$WAKE_MAX_RUNS" ]; then report 'drain limit reached; leaving pending events for recovery'; exit 1; fi
        RUNS=$((RUNS + 1))
        # Use a POSIX builtin constant-format printf and static harness arguments.
        printf '%s\n' "$PAYLOAD" | "$HARNESS_BIN" -p &
        # For a Codex harness, select this static stdin interface instead:
        # printf '%s\n' "$PAYLOAD" | "$HARNESS_BIN" exec - &
        CHILD=$!
        if wait "$CHILD"; then STATUS=0; else STATUS=$?; fi
        CHILD=
        [ "$STATUS" -eq 0 ] || report "harness exited ($STATUS); rechecking pending events"
    fi
    if OUTCOME=$("$MTIX_BIN" inbox lease release --agent "$AGENT" --holder "$HOLDER" --ttl 15m 2>&1); then STATUS=0; else STATUS=$?; fi
    if [ "$STATUS" -ne 0 ]; then report "lease release failed ($STATUS): $OUTCOME"; exit 1; fi
    case "$OUTCOME" in
        relaunch) : ;;
        released) HOLDER=; exit 0 ;;
        *) report 'unexpected lease release outcome'; exit 1 ;;
    esac
done
