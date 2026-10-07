---
name: task-execution
description: Execute mtix tasks using the context chain. Claim tasks, read assembled context, implement with TDD, and mark done.
---

# Task Execution with mtix

Nonempty agent IDs and assignees must fit in 64 UTF-8 bytes and must not be whitespace-only or contain control or invisible format characters (including bidi controls and zero-width characters). Accepted raw values are preserved unchanged. An empty create assignee means no explicit assignment: parent auto-claim still applies when enabled. A present empty update assignee clears the assignment; omission preserves it. Claim and unclaim keep their existing required/default actor rules. These checks apply to existing CLI, REST, gRPC and MCP inputs; they do not rewrite historical imported or synchronized identities.

## Before Starting Any Work

1. Run `mtix ready` to find tasks available for pickup
2. Run `mtix context <id>` to read the assembled context chain from root to leaf
3. Run `mtix claim <id> --agent codex` to claim the task

## The Context Chain

The dot-notation hierarchy (e.g., PROJ-42.1.3) IS your briefing. Each level adds context:
- Root: business goal
- Middle: technical scope
- Leaf: exact implementation instructions

Always run `mtix context <id>` before starting — it assembles the full prompt from root to your task.

## Workflow

1. Read the context chain completely
2. Write failing tests first (TDD)
3. Implement the minimum code to pass
4. Verify all acceptance criteria from the task
5. Run `mtix done <id>` when complete

## Rules

- Never skip the context chain — it contains your complete briefing
- Every change must have an mtix task — use `mtix create` if none exists
- Report blockers: `mtix comment <id> "blocked: <reason>"`

## Inspecting deferred tasks

`mtix show <id>` prints a timed deferred task as
`Status:   ⏸ deferred (until 2026-10-01T09:00:00Z)`, with the wake time
in ISO-8601 UTC. It adds nothing when no wake time is set or the task is
not deferred, even if a stale `defer_until` remains. `--json` returns the
stored record unchanged.

## Watching Task Events

WebSocket subscriptions at `/ws/events` match `under` to the named node and its
dotted descendants on a `.` boundary: `PROJ-1` includes `PROJ-1` and `PROJ-1.2`,
but rejects `PROJ-10` and `PROJ-10.2`. Omitted or empty `under` accepts every node.
A non-empty `events` whitelist must also match; an omitted or empty `events`
list accepts every event type.

Connect a WebSocket client to the local server's `/ws/events` endpoint and send
`{"subscribe":{"under":"PROJ-1","events":["node.updated"]}}` to watch updates
for that task and its descendants. Verify the watch with an event for the named
task or a child; a lookalike sibling must not appear. If expected events are
missing, check both `under` and the non-empty event whitelist before retrying.
