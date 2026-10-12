---
name: multi-agent
description: Coordinate multiple agents working on mtix tasks. Manage claiming, sessions, heartbeats, and parallel execution.
---

# Multi-Agent Coordination with mtix

Nonempty agent IDs and assignees must fit in 64 UTF-8 bytes and must not be whitespace-only or contain control or invisible format characters (including bidi controls and zero-width characters). Accepted raw values are preserved unchanged. An empty create assignee means no explicit assignment: parent auto-claim still applies when enabled. A present empty update assignee clears the assignment; omission preserves it. Claim and unclaim keep their existing required/default actor rules. These checks apply to existing CLI, REST, gRPC and MCP inputs; they do not rewrite historical imported or synchronized identities.

## Agent Registration

```bash
mtix agent register <agent-name>
mtix claim <id> --agent <agent-name>
mtix session start <agent-name>
```

## Registration and lanes

`mtix agent register <agent-id>` also records the calling session so the id can be reached later. It stores the harness and session handle detected by identity: the harness is the one that owns the session's process (`claude`, `codex` or `pi`, found by walking up past shells), and the handle comes only from that harness's own variable (`CLAUDE_CODE_MESSAGING_SOCKET`, `CODEX_THREAD_ID` or `PI_SESSION_ID`), never from an outer harness's inherited variable. If the process or its variable cannot be identified, the session is registered without a handle and with unknown liveness: it is never replaced silently, a repeat from the same process refreshes it, and another session needs `--take-over`; `--harness` and `--session` override them, and a typed `--session` is recorded as origin `manual`. Registering never marks existing inbox events as new: it starts from the current end of the journal, so backlog reaches you through `mtix inbox`, not a wake.

- **Restart a lane:** run `mtix agent register <agent-id>` again. A row whose session has exited is replaced silently, with no flag.
- **Take over a live lane:** `mtix agent register <agent-id> --take-over`. It bumps the epoch, leaves one fixed notice in the id's inbox when it has a current work item (at most one per displaced session per 10 minutes), and is limited to 3 per id per hour; after that it needs a terminal. Take over only when you are replacing that session on purpose.
- **Move a lane:** run `mtix agent unregister <agent-id>` from the owning session. `--force` removes another live session's row (operator use).
- **Inspect:** `mtix agent list --registrations [--json]` shows harness, origin, mode, liveness (`alive`, `dead` or `unknown`; unknown is treated as live), live route and hold.
- **MCP-only rows:** origin `mcp` (no session handle, no live route) is reserved in this build: no MCP tool records a registration yet, so register from the session shell with `mtix agent register`. `agents.mcp-only` reports any such row that exists.
- **last_seen** moves only when the registered session runs `mtix agent register` or `mtix inbox` (list, `--wait`, `ack`) with its own session handle in the environment.
- **Verify:** `mtix sync doctor` reports `agents.stale` (dead sessions; a row unseen for 24 hours with a dead session is deleted) and `agents.mcp-only`, each with the exact fix; add `--json` for tools.
- **Never** register under another agent's id to "see" its inbox, and never pass `--take-over` to get around a refusal you do not understand: ask the operator.

## Finding Work

```bash
mtix ready          # Tasks available for pickup (no assignee, no blockers)
mtix blocked        # Tasks waiting on dependencies
mtix stale          # Tasks with inactive agents
```

## Parallel Execution Rules

- Only one agent can claim a task at a time
- Read the context chain before starting: `mtix context <id>`
- Send heartbeats during long work: `mtix agent heartbeat <agent-name>`
- End sessions when done: `mtix session end <agent-name>`

## Conflict Prevention

- Check task status before claiming: `mtix show <id>`
- Never modify tasks claimed by other agents
- Use comments to coordinate: `mtix comment <id> "message"`

## Reference Wake Routine

The mtix reference `examples/hooks/wake-agent.sh` reads the unhandled inbox
with `mtix inbox --agent <agent-name> --format prompt`. An empty inbox exits
without launching a harness. Keep exactly one verified launch line enabled;
supply the input through standard input:

- Claude Code: `printf '%s\n' "$PAYLOAD" | claude -p`
- OpenAI Codex CLI: `printf '%s\n' "$PAYLOAD" | codex exec -`

Other runtimes require a verified standard-input interface. The reference
preserves interior newlines, removes trailing newlines during collection and
writes one final newline. Handle each event, reply when needed, then ack that
event; an unacked event remains available on the next inbox read. See the user
manual's Waking agents section for placement and the complete routine.

Wake runs use a board-local lease keyed by agent, host identity and user. Acquire
prints the owner token; release reports `released`, `relaunch` or `abandoned`.
Exit 6 means held or stale ownership (storage errors retain exit 3/4). The reference
renews the same token every 60s with a 15m TTL and drains before releasing; only
the agent acknowledges handled events. TTL accepts 1m through 24h. Use a hook
`timeout-seconds: 86400` longer than the actual run. Configure executable
`MTIX_BIN` / `HARNESS_BIN` paths if needed under a minimal PATH; missing tools
exit 127 and log to the operator-only state log (`WAKE_AGENT_LOG` overrides it).
Catchable termination abandons the lease and cleans owned processes. The local
`WAKE_MAX_RUNS` bound defaults to 20 and leaves unhandled events durable. There
is no default hourly budget. Renewal failures beyond TTL, or SIGKILL leaving a
harness alive, can permit overlap; this POSIX reference is not hard process
fencing and is untested on Windows. The lease CLI uses Windows SID identity.
