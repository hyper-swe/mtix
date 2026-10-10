---
name: multi-agent
description: Coordinate multiple agents working on mtix tasks. Manage claiming, sessions, heartbeats, and parallel execution.
---

# Multi-Agent Coordination with mtix

Nonempty agent IDs and assignees must fit in 64 UTF-8 bytes and must not be whitespace-only or contain control or invisible format characters (including bidi controls and zero-width characters). Accepted raw values are preserved unchanged. An empty create assignee means no explicit assignment: parent auto-claim still applies when enabled. A present empty update assignee clears the assignment; omission preserves it. Claim and unclaim keep their existing required/default actor rules. These checks apply to existing CLI, REST, gRPC and MCP inputs; they do not rewrite historical imported or synchronized identities.

## Agent Registration

```bash
mtix claim <id> --agent <agent-name>
mtix session start <agent-name>
```

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
