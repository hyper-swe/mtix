---
name: task-execution
description: Execute mtix tasks using the context chain. Claim tasks, read assembled context, implement with TDD, and mark done.
---

# Task Execution with mtix

Nonempty agent IDs and assignees must fit in 64 UTF-8 bytes and must not be whitespace-only or contain control or invisible format characters (including bidi controls and zero-width characters). Accepted raw values are preserved unchanged. An empty create assignee means no explicit assignment: parent auto-claim still applies when enabled. A present empty update assignee clears the assignment; omission preserves it. Claim requires an agent ID. Unclaim acts as the given agent. The CLI uses `--agent`, else `MTIX_AUTHOR_ID`, else the `author_id` config key; MCP uses `agent_id`, with the same fallbacks; REST requires the `X-Agent-ID` header and returns 400 without it; gRPC has no fallback and needs the agent in the request. It is refused when no agent is set. Only the assignee can release a claim unless force is given. This check is a coordination guard on a self-asserted identity (any caller can name the assignee), not authorization. These checks apply to existing CLI, REST, gRPC and MCP inputs; they do not rewrite historical imported or synchronized identities.

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

## Every action item becomes a ticket

When you find or promise an action item (a follow-up, cleanup, promised check or open question), file an mtix ticket right away. Do not leave it in chat, notes or a state file. A ticket keeps the item traceable, findable later and auditable.

```bash
mtix create "Add retry to the export check" --under PROJ-12 --type chore \
  --description "Why the item exists and its scope" \
  --prompt "Exact files, functions and edge cases" \
  --acceptance "Testable outcome"
mtix create "Cleanup after the migration" --description "..." --prompt "..." --acceptance "..."
```

Use `--under <id>` to place the item under related work. Omit it for a new root ticket.

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

## Sharing a ticket link

To point a person at a ticket, run `mtix url <id>` (for example `mtix url MTIX-12.3`; add `--json` for `{"id", "url"}`). It prints the link that opens that ticket's full detail (prompt, annotations, acceptance) in the web UI, and the link survives reload and back/forward in the browser. It needs a running `mtix serve` for this project: with none running it prints nothing on stdout and exits non-zero naming `mtix serve`, so start the server (or ask the user to) and run it again; an unknown id fails with not found. The command contacts no remote service and never needs a credential. Never put a token in a URL.

Verify: `mtix url <id>` prints one `http://<host>:<port>/ticket/<id>` line (host is the address the serve is bound to, `127.0.0.1` for a wildcard bind); opening it shows the ticket detail on first load, and `?node=<id>` on any web UI address is rewritten to the same path. Troubleshoot: a non-zero exit naming `mtix serve` means no live serve for this project (`mtix serve` writes `.mtix/serve.json` while it runs and removes it on exit; a record whose process is dead, or whose port does not answer as an mtix serve, counts as none, and the file is git-ignored and is not the lock that routes commands); to target another project from any directory run `mtix -C <dir> url <id>`; an invalid id is rejected before anything is looked up.

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

## Reading Ticket Comments

With `mtix serve`, open a ticket's **Activity** tab. **All** includes comments and other events, newest first; **Comments** filters the loaded activity pages to plain and addressed comments. Each comment shows its author and, when present, recipient. Bodies use sanitized Markdown; identities stay literal text. Use **Load more** for older entries and refresh the ticket after comments arrive from another process. Viewing a comment does not acknowledge an inbox event: acknowledge explicitly after handling it.
