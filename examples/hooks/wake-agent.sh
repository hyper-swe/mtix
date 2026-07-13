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
#         timeout-seconds: 30
#
# Then review and trust the config on this host: mtix hooks trust
#
# IDEMPOTENT BY CONSTRUCTION (safe under at-least-once dispatch — a double
# wake launches nothing twice):
#   1. If the inbox has nothing unacked, a previous wake (or a live session)
#      already handled it -> exit without launching.
#   2. The launched agent receives the inbox AS ITS PROMPT and acks each
#      event after handling, which is what makes step 1 true next time.
#
# NOTE (MTIX-56.9): mtix spawns this script DETACHED and does not wait;
# timeout-seconds is best-effort. Long launches are fine, but self-bound
# anything that could hang. Exit codes are yours to report (log or
# mtix comment) — the fabric's success signal is the inbox ack.
#
# The launch line is harness-specific; keep exactly one uncommented.

set -eu

AGENT="${1:?usage: wake-agent.sh <agent-id>}"

# Nothing unhandled -> nothing to do (empty output means empty inbox).
PAYLOAD="$(mtix inbox --agent "$AGENT" --format prompt)"
[ -z "$PAYLOAD" ] && exit 0

# --- pick ONE launch line for your harness -----------------------------
# Claude Code (headless; add --channels ... to keep the session push-reachable):
claude -p "$PAYLOAD"
# OpenAI Codex CLI:
# codex exec "$PAYLOAD"
# Cursor CLI:
# agent -p "$PAYLOAD"
# Any other runtime that takes a prompt argument works the same way.
