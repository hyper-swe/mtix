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
# The renewer also stops when this marker exists, so stopping it never depends
# on a signal reaching it in time (a TERM sent while it starts can be lost).
STOP_FILE="$WAKE_AGENT_LOG.stop.$$"
SLEEP_FILE="$STOP_FILE.sleep"
WAKE_MAX_RUNS="${WAKE_MAX_RUNS:-20}"

prepare_log() {
    LOG_DIR=$(dirname "$WAKE_AGENT_LOG")
    [ -d "$LOG_DIR" ] || mkdir -p -m 700 "$LOG_DIR"
}

report() {
    MESSAGE="$(date -u '+%Y-%m-%dT%H:%M:%SZ') wake-agent: $*"
    printf '%s\n' "$MESSAGE" >&2
    if prepare_log; then
        printf '%s\n' "$MESSAGE" >> "$WAKE_AGENT_LOG" || printf '%s\n' 'wake-agent: cannot write log' >&2
    else
        printf '%s\n' 'wake-agent: cannot create log directory' >&2
    fi
}

case "$WAKE_MAX_RUNS" in
    ''|*[!0-9]*) report 'WAKE_MAX_RUNS must be a positive integer'; exit 1 ;;
esac
if ! [ "$WAKE_MAX_RUNS" -gt 0 ] 2>/dev/null; then
    report 'WAKE_MAX_RUNS must be a positive integer'; exit 1
fi

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

kill_renewer_sleep() {
    SLEEP_RECORDED=
    read -r SLEEP_RECORDED < "$SLEEP_FILE" 2>/dev/null || :
    if [ "$SLEEP_RECORDED" -gt 0 ] 2>/dev/null; then kill -KILL "$SLEEP_RECORDED" 2>/dev/null || :; fi
}

# Create the marker first, then read the sleep pid: the renewer stores the pid
# first, then checks the marker, so one side always sees the other and neither
# a lost nor an ignored TERM can leave the renewer waiting on its sleep.
stop_renewal() {
    if [ -n "$RENEWER" ]; then
        (set -C; : > "$STOP_FILE") 2>/dev/null || :
        kill "$RENEWER" 2>/dev/null || :
        kill_renewer_sleep
        wait "$RENEWER" 2>/dev/null || :; RENEWER=
        rm -f -- "$STOP_FILE" "$SLEEP_FILE" 2>/dev/null || :
    fi
}

stop_children() {
    if [ -n "$CHILD" ]; then kill "$CHILD" 2>/dev/null || :; wait "$CHILD" 2>/dev/null || :; CHILD=; fi
    stop_renewal
}

cleanup() {
    stop_children
    if [ -n "$HOLDER" ]; then
        if OUTCOME=$("$MTIX_BIN" inbox lease release --agent "$AGENT" --holder "$HOLDER" --abandon 2>>"$WAKE_AGENT_LOG"); then
            [ "$OUTCOME" = abandoned ] || report 'unexpected lease abandon outcome'
        else
            report 'could not abandon lease'
        fi
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

if ! prepare_log; then report 'cannot create log directory'; exit 1; fi
if HOLDER=$("$MTIX_BIN" inbox lease acquire --agent "$AGENT" --ttl 15m 2>>"$WAKE_AGENT_LOG"); then STATUS=0; else STATUS=$?; fi
if [ "$STATUS" -eq 6 ]; then HOLDER=; exit 0; fi
if [ "$STATUS" -ne 0 ]; then report "lease acquisition failed ($STATUS)"; HOLDER=; exit 1; fi
case "$HOLDER" in
    ''|*[!0-9a-f]*) HOLDER=; report 'invalid lease holder'; exit 1 ;;
esac
if [ "${#HOLDER}" -ne 32 ]; then HOLDER=; report 'invalid lease holder'; exit 1; fi

PARENT=$$
stop_renewal_commands() {
    if [ -n "$SLEEP_PID" ]; then kill -KILL "$SLEEP_PID" 2>/dev/null || :; wait "$SLEEP_PID" 2>/dev/null || :; SLEEP_PID=; fi
    if [ -n "$RENEW_PID" ]; then kill "$RENEW_PID" 2>/dev/null || :; wait "$RENEW_PID" 2>/dev/null || :; RENEW_PID=; fi
}

renew_loop() {
    SLEEP_PID= RENEW_PID= STOPPING=
    trap 'STOPPING=1; stop_renewal_commands' TERM INT HUP
    while kill -0 "$PARENT" 2>/dev/null && [ ! -e "$STOP_FILE" ]; do
        sleep 60 >/dev/null 2>&1 </dev/null & SLEEP_PID=$!
        rm -f -- "$SLEEP_FILE" 2>/dev/null || :
        (set -C; printf '%s\n' "$SLEEP_PID" > "$SLEEP_FILE") 2>/dev/null || :
        if [ -n "$STOPPING" ] || [ -e "$STOP_FILE" ]; then stop_renewal_commands; return 0; fi
        wait "$SLEEP_PID" || return 0
        SLEEP_PID=
        rm -f -- "$SLEEP_FILE" 2>/dev/null || :
        kill -0 "$PARENT" 2>/dev/null || exit 0
        [ ! -e "$STOP_FILE" ] || return 0
        "$MTIX_BIN" inbox lease acquire --agent "$AGENT" --holder "$HOLDER" --ttl 15m >/dev/null 2>>"$WAKE_AGENT_LOG" & RENEW_PID=$!
        if [ -n "$STOPPING" ]; then stop_renewal_commands; return 0; fi
        if wait "$RENEW_PID"; then STATUS=0; else STATUS=$?; fi
        RENEW_PID=
        [ -z "$STOPPING" ] || return 0
        if [ "$STATUS" -eq 6 ]; then kill -USR1 "$PARENT" 2>/dev/null || :; exit 0; fi
        [ "$STATUS" -eq 0 ] || report "lease renewal failed ($STATUS); TTL recovery remains available"
    done
}
rm -f -- "$STOP_FILE" "$SLEEP_FILE" 2>/dev/null || :
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
    stop_renewal
    if OUTCOME=$("$MTIX_BIN" inbox lease release --agent "$AGENT" --holder "$HOLDER" --ttl 15m 2>>"$WAKE_AGENT_LOG"); then STATUS=0; else STATUS=$?; fi
    if [ "$STATUS" -ne 0 ]; then report "lease release failed ($STATUS)"; exit 1; fi
    case "$OUTCOME" in
        relaunch)
            rm -f -- "$STOP_FILE" "$SLEEP_FILE" 2>/dev/null || :
            renew_loop & RENEWER=$! ;;
        released) HOLDER=; exit 0 ;;
        *) report 'unexpected lease release outcome'; exit 1 ;;
    esac
done
