# mtix User Manual

A complete guide to using mtix for hierarchical task management.

---

## Table of Contents

1. [Getting Started](#getting-started)
2. [Project Initialization](#project-initialization)
3. [Creating and Managing Nodes](#creating-and-managing-nodes)
4. [Hierarchy and Decomposition](#hierarchy-and-decomposition)
5. [Status Lifecycle](#status-lifecycle)
6. [Workflow Commands](#workflow-commands)
7. [Dependencies](#dependencies)
8. [Progress Tracking](#progress-tracking)
9. [Prompt Steering and Context Assembly](#prompt-steering-and-context-assembly)
10. [Annotations and Comments](#annotations-and-comments)
11. [Agent and Session Management](#agent-and-session-management)
12. [Search and Queries](#search-and-queries)
13. [Working with multiple projects](#working-with-multiple-projects)
14. [Configuration](#configuration)
15. [Web UI](#web-ui)
16. [REST API](#rest-api)
17. [gRPC API](#grpc-api)
18. [MCP Tools](#mcp-tools)
19. [Backup, Export, and Import](#backup-export-and-import)
20. [Content Integrity](#content-integrity)
21. [Team collaboration with sync (FR-18)](#team-collaboration-with-sync-fr-18)
22. [Distributed identity & team sync](#distributed-identity--team-sync)
23. [Troubleshooting](#troubleshooting)

---

## Getting Started

### Prerequisites

- Go 1.25+ (for building from source)
- Node.js 18+ (only if building the web UI)

### Installation

```bash
# Build the complete suite (web UI + Go binary)
make build

# Or build components separately
make build-web    # Build React SPA and embed into Go binary
make build-go     # Build Go binary only
```

The result is a single binary with no external dependencies. SQLite is embedded (pure Go, no CGO). The web UI is compiled from `web/` (React + TypeScript + Vite) and embedded in the binary.

To run tests before building:

```bash
make build-checked   # Run all tests (Go + web), then build
make test-all        # Run Go + web tests without building
```

### Global Flags

Every command supports these flags:

| Flag | Description |
|------|-------------|
| `--json` | Output in machine-readable JSON format |
| `--log-level` | Override log level: `debug`, `info`, `warn`, `error` |

---

## Project Initialization

Initialize a new mtix project in the current directory:

```bash
mtix init --prefix MYPROJ
```

This creates a `.mtix/` directory containing:
- `config.yaml` — project configuration
- `mtix.db` — SQLite database (WAL mode)
- Generated agent documentation in `.mtix/docs/`

### Prefix Rules

The project prefix identifies all nodes (e.g., `MYPROJ-1`, `MYPROJ-1.2.3`). It must:
- Start with an uppercase letter
- Contain only uppercase letters, digits, and hyphens
- Be 1–20 characters long
- Match the pattern `^[A-Z][A-Z0-9-]{0,19}$`

Examples: `PROJ`, `MY-APP`, `TASK1`, `NASA-MCR`

---

## Creating and Managing Nodes

### Create a Node

```bash
# Minimal — just a title
mtix create "Build authentication module"

# Full options
mtix create "Build authentication module" \
  --under PROJ-1 \
  --type feature \
  --priority 1 \
  --description "Implement OAuth2 flow with refresh tokens" \
  --prompt "Create login, token refresh, and logout endpoints" \
  --acceptance "All endpoints return proper HTTP status codes" \
  --labels "security,backend" \
  --assign agent-claude
```

#### Create Flags

| Flag | Description | Default |
|------|-------------|---------|
| `--under` | Parent node ID | (root-level) |
| `--type` | Issue type: `bug`, `feature`, `task`, `chore`, `refactor`, `test`, `doc` | `task` |
| `--priority` | 1 (critical) to 5 (backlog) | 3 |
| `--description` | Detailed description (max 50KB) | |
| `--prompt` | LLM agent instructions (max 100KB) | |
| `--acceptance` | Acceptance criteria | |
| `--labels` | Comma-separated labels | |
| `--assign` | Assign to agent on creation | |

### Create a Micro Issue (Shorthand)

```bash
mtix micro "Validate email format" --under PROJ-1.1
mtix micro "Write unit tests" --under PROJ-1.1 --prompt "Cover happy path and edge cases"
```

The `micro` command is shorthand for creating a child node. The `--under` flag is required.

### View a Node

```bash
mtix show PROJ-1

# JSON output for scripting
mtix show PROJ-1 --json
```

`mtix show` prints the node's details as labeled lines, then every annotation (oldest first, with its UTC timestamp and author), then the prompt cut to 100 characters. Annotation text, author and addressee, the description and the prompt are normalized for the terminal: control characters other than newline and tab are removed, leading blank lines and trailing whitespace are trimmed, and continuation lines are indented, so stored text cannot overwrite a line, restyle the terminal or pass for a label. Title and assignee print as stored, and invisible Unicode formatting characters (such as bidirectional overrides) are not removed yet. When you need the exact stored text, for example to quote a review verdict, use `--json`: it prints the raw record, unchanged.

### Update a Node

```bash
mtix update PROJ-1 \
  --title "Updated title" \
  --description "New description" \
  --priority 2 \
  --labels "urgent,backend"
```

Only specified fields are updated. Omitted fields remain unchanged.

### Delete and Restore

```bash
# Soft-delete (recoverable for 30 days by default)
mtix delete PROJ-1.2

# Cascade delete — removes all descendants too
mtix delete PROJ-1 --cascade

# Restore a soft-deleted node
mtix undelete PROJ-1.2
```

Each delete records which delete removed each node it soft-deletes, and `mtix undelete` uses that record: it restores the node and only the descendants that the same delete removed. A descendant that was deleted separately before a cascade delete, on its own or by its own cascade, stays deleted when you undelete the cascade's node, even if both deletes happened in the same second. Restore it with its own `mtix undelete`. If you undelete a node that an ancestor's cascade removed, mtix restores that node and the descendants the same cascade removed, and the ancestor stays deleted. The progress of the parent of each restored node is recomputed.

That record also carries the node's deletion time and author, and mtix uses it only while they still match the node. Earlier mtix versions did not record which delete removed each descendant. An earlier 0.5.x binary that opens the same database restores without clearing records and deletes without writing them. A delete that arrives through sync or an import carries no record either. When you undelete a node without a usable record, mtix restores the descendants without a usable record that were deleted in the same second by the same author. That can include a descendant that was deleted separately in that second.

After `mtix rerun --strategy delete`, undelete restores one node at a time, because rerun deletes each descendant separately. To bring the subtree back, undelete each node, starting from the top.

Undelete is local: it emits no sync event. Other replicas keep the node deleted.

Soft-deleted nodes are automatically purged after the retention period (default 30 days, configurable via `data.soft_delete_retention`).

---

## Hierarchy and Decomposition

### Dot-Notation IDs

Every node has an ID that encodes its position in the hierarchy:

```
PROJ-1              Epic (depth 0)
PROJ-1.1            Story (depth 1)
PROJ-1.1.1          Issue (depth 2)
PROJ-1.1.1.1        Micro issue (depth 3)
PROJ-1.1.1.1.1      Sub-micro issue (depth 4)
...                  No depth limit
```

The node type is derived automatically from depth:

| Depth | Type |
|-------|------|
| 0 | Story |
| 1 | Epic |
| 2 | Issue |
| 3+ | Micro |

### Batch Decomposition

Create multiple children at once:

```bash
mtix decompose PROJ-1.1 \
  "Implement login endpoint" \
  "Add token refresh" \
  "Create logout handler" \
  "Write integration tests"
```

This atomically creates `PROJ-1.1.1`, `PROJ-1.1.2`, `PROJ-1.1.3`, and `PROJ-1.1.4`.

### View the Tree

```bash
# Default depth of 10
mtix tree PROJ-1

# Limit depth
mtix tree PROJ-1 --depth 3
```

Output:

```
PROJ-1: Build authentication module [open] (33%)
  PROJ-1.1: Implement login endpoint [done] (100%)
    PROJ-1.1.1: Validate email format [done]
    PROJ-1.1.2: Write unit tests [done]
  PROJ-1.2: Add token refresh [in_progress] (0%)
  PROJ-1.3: Create logout handler [open] (0%)
```

---

## Status Lifecycle

Every node moves through a 7-state machine with enforced transitions.

### States

| Status | Icon | Description |
|--------|------|-------------|
| `open` | | Newly created, ready for work |
| `in_progress` | | Claimed by an agent, work underway |
| `blocked` | | Has unresolved blocking dependencies (auto-managed) |
| `done` | | Work complete |
| `deferred` | | Postponed until a future date or indefinitely |
| `cancelled` | | Work descoped with reason |
| `invalidated` | | Parent prompt changed, needs re-evaluation (auto-managed) |

### Transition Map

```
open ──────────► in_progress ──────► done
 │                    │                │
 │                    ▼                │
 │               blocked ◄────────────┘ (via reopen)
 │                    │
 ├──► deferred        │
 │       │            │
 │       ▼            ▼
 └──► cancelled   (auto-resolves
                   when blockers clear)
```

**Terminal states:** `done`, `cancelled`, `invalidated` — these block child creation.

### Auto-Managed Transitions

The system automatically manages these transitions:
- **Blocking:** When a `blocks` dependency is added, the target node moves to `blocked`
- **Unblocking:** When all blockers are resolved, the node returns to its previous state (`open` or `in_progress`)
- **Invalidation:** When a parent's prompt changes, descendants move to `invalidated`

`blocked` is system-managed: you cannot set or clear it directly, and
`blocked → done` (or claiming a blocked node) is disallowed by design — resolve
the blockers instead. If a node is stuck `blocked` even though its blockers are
resolved, run `mtix unblock <id>` to re-derive its status from the current
blockers (a no-op if a genuine blocker remains).

---

## Workflow Commands

### Claim (Start Work)

```bash
mtix claim PROJ-1.1.1 --agent agent-claude
```

Transitions the node from `open` → `in_progress` and assigns the agent.

### Unclaim (Release Work)

```bash
mtix unclaim PROJ-1.1.1 --reason "Need more context from parent task"
```

Returns the node to `open` and clears the assignee. A reason is required.

### Done (Complete Work)

```bash
mtix done PROJ-1.1.1
```

Marks the node as `done`. Progress automatically rolls up to parent nodes.

### Defer (Postpone)

```bash
# Defer with no wake time
mtix defer PROJ-1.3

# Defer until a specific time (RFC 3339, with a zone)
mtix defer PROJ-1.3 --until "2026-04-01T00:00:00Z"
```

`--until` sets the node's wake time. It must be an RFC 3339 timestamp with
a zone (`2026-04-01T00:00:00Z`, `2026-04-01T09:00:00+05:30`) whose UTC year
is 1 to 9999; anything else is rejected and the node is not deferred. The wake time is stored with the
deferral, in UTC and whole seconds, as the `defer_until` field of
`mtix show <id> --json`.

- **Before the wake time, claims are refused** (`mtix claim` reports that
  the node is still deferred).
- **From the wake time on** (the wake time counts as passed once it is
  equal to or earlier than now), `mtix ready` lists the node again and a
  claim succeeds, and the next background pass (`mtix gc`, or
  `POST /api/v1/admin/gc` on a running server) sets it back to `open`. The
  pass re-checks each node as it reopens it, so a claim or a new deferral
  made in the meantime is kept.
- **Without `--until`** the node has no wake time, and any earlier wake
  time is cleared.
- **A deferred node with no wake time** (a defer without `--until`, or any
  deferral received through sync) is listed by `mtix ready` and can be
  claimed at once.
- **Deferring a node that is already deferred** replaces its wake time
  (or clears it, without `--until`); the change is recorded in the node's
  activity.
- **Leaving deferred clears the wake time:** the background pass, a reopen,
  a claim and a cancel (including a cascade cancel) remove it in the same
  change, so an old wake time can never wake a later deferral.

The MCP tool `mtix_defer` takes the same wake time as `until`, and the REST
API as `{"until": "..."}` in the body of `POST /api/v1/nodes/:id/defer`. The
API answers `400` to a body that is not empty or a JSON object whose only
key is `until`, and to an `until` that is not RFC 3339 or whose UTC year is
outside 1 to 9999 (see also `api/openapi.yaml`).

**Hub sync (0.5.x) does not carry the wake time.** Other machines that sync
through the hub receive the deferral as a status change, but not the wake
time. There the node is deferred with no wake time, and any wake time it
held before is cleared: it is not reopened when the time passes, and a
claim is not refused. Any other claim or status change received through
sync also clears the wake time. Set the wake time on the machine that will
pick the task up. A git-tracked `.mtix/tasks.json` is different: it
exports `defer_until`, so a machine that imports the file gets the wake
time, and claims there are refused until it passes.

### Cancel (Descope)

```bash
# Cancel a single node
mtix cancel PROJ-1.3 --reason "Out of scope for v1"

# Cancel a node and all its descendants
mtix cancel PROJ-1 --reason "Entire feature dropped" --cascade
```

A reason is required.

Cancelling a node unblocks each task it was blocking once that task has
no other unresolved blocker; the task returns to the status it had
before it was blocked. Progress is recomputed for the node's parent and
every ancestor above it.

With `--cascade`, every descendant that is not already `done`,
`cancelled` or `invalidated` is cancelled too, in the same step, and
each one is handled like a single cancel: the tasks it was blocking are
unblocked, and the progress of every node above a cancelled descendant,
up to the top of the tree, is recomputed. Descendants that are already
`done` keep their status.

With sync, a cascade cancel in this version sends other machines no
event for the descendants it cancels: they receive the cancel of the
node you named and the unblock of each task it released, nothing else.
On other machines the descendants are therefore not cancelled and keep
their previous status, and a task that the cascade unblocked shows as
unblocked there even though the descendant that blocked it is not
cancelled. If other machines must see every descendant cancelled,
cancel the descendants one at a time, deepest first, without
`--cascade`.

A cascade cancel made by an earlier version unblocked only the tasks
the named node was blocking, and upgrading does not repair what it
left. To release a task that is still `blocked` (see `mtix blocked`)
although every blocker shown by `mtix dep show <id>` is resolved, run
`mtix unblock <id>`: it
re-derives the task's status from its current blockers and never
overrides a blocker that is still unresolved. No command recomputes
stale progress; a node's progress is recomputed the next time one of
its children changes status.

### Reopen (Reverse Terminal State)

```bash
mtix reopen PROJ-1.3
```

Moves a `done` or `cancelled` node back to `open`.

### Rerun (Invalidate and Reprocess)

```bash
# Invalidate all descendants and reopen
mtix rerun PROJ-1.1 --reason "Requirements changed"

# Only reopen nodes that were still open
mtix rerun PROJ-1.1 --strategy open_only --reason "Prompt updated"
```

Available strategies:
- `all` (default) — invalidate and reopen all descendants
- `open_only` — only reopen open/in-progress descendants
- `delete` — soft-delete descendants for fresh start
- `review` — mark for manual review

`--strategy delete` invalidates each descendant and then deletes it separately, so `mtix undelete` restores one node at a time: undeleting a child leaves its own children deleted. To bring the subtree back, undelete each node, starting from the top, then use `mtix restore` to return each one from `invalidated` to its previous status.

### Restore (After Invalidation)

```bash
mtix restore PROJ-1.1.1
```

Restores an invalidated node to its previous status.

---

## Dependencies

### Dependency Types

| Type | Description | Behavior |
|------|-------------|----------|
| `blocks` | Hard blocker — target cannot proceed until source is done | Auto-sets target to `blocked` |
| `related` | Informational link — no workflow effect | Display only |
| `discovered_from` | Found during work on source | Display only |
| `duplicates` | Marks as duplicate of another node | Display only |

### Manage Dependencies

```bash
# Add a blocking dependency (PROJ-2 blocks PROJ-1.1)
mtix dep add PROJ-2 PROJ-1.1

# Add with explicit type
mtix dep add PROJ-2 PROJ-1.1 --type blocks
mtix dep add PROJ-3 PROJ-1.1 --type related

# View blockers for a node
mtix dep show PROJ-1.1

# Remove a dependency
mtix dep remove PROJ-2 PROJ-1.1 --type blocks
```

### Cycle Detection

When adding a `blocks` dependency, mtix checks for cycles. If `A blocks B` and `B blocks C`, attempting to add `C blocks A` will fail with a cycle detection error.

---

## Progress Tracking

### Automatic Rollup

Progress propagates from leaf nodes to the root in a single transaction. When a child is marked `done`, the parent's progress updates automatically.

```bash
mtix progress PROJ-1
```

Output:

```
PROJ-1: Build authentication module
  Progress: 67% (2/3 children complete)
  Children: 3 total, 2 done, 0 blocked, 1 open
```

### How Progress Is Calculated

- **Leaf nodes:** 0% when open, 100% when done
- **Parent nodes:** Average of children's progress (weighted if configured)
- **Excluded from calculation:** Cancelled and invalidated nodes

### Weighted Progress

Enable weighted progress in configuration to use each node's `weight` field:

```bash
mtix config set progress.weighted true
```

---

## Prompt Steering and Context Assembly

### Setting Prompts

Prompts are instructions for LLM agents working on a node:

```bash
# Set or update a prompt
mtix prompt PROJ-1.1 "Implement the OAuth2 login endpoint using the existing auth middleware. Use the /auth/login path. Return a JWT access token and a refresh token cookie."
```

When a parent's prompt changes, all descendant nodes are automatically invalidated (moved to `invalidated` status) since their context may have changed.

### Context Assembly

View the full assembled context for a node. This includes the ancestor chain from root to target, sibling context, and blocking dependencies:

```bash
mtix context PROJ-1.1.1

# Limit token budget
mtix context PROJ-1.1.1 --max-tokens 4000
```

The assembled context includes:
1. **Ancestor chain** — from root story down to the target node, with prompts at each level
2. **Source attribution** — each prompt is marked `[HUMAN-AUTHORED]` or `[LLM-GENERATED]` based on the creator
3. **Sibling context** — peer nodes under the same parent (titles and status)
4. **Blocking dependencies** — any unresolved blockers with their details
5. **Unresolved annotations** — comments and notes needing attention

---

## Annotations and Comments

### Add Comments

```bash
mtix comment PROJ-1.1 "The login endpoint should also handle MFA flow"
```

### Add Annotations

```bash
mtix annotate PROJ-1.1 "Consider rate limiting on this endpoint"
```

Annotations appear in the context assembly and are visible to agents working on the node.

### Resolve Annotations

When an annotation has been addressed:

```bash
mtix resolve-annotation PROJ-1.1 <annotation-id>
```

Resolved annotations are excluded from context assembly.

---

## Agent and Session Management

### Agent Lifecycle

Agents (typically LLM coding agents) have a lifecycle managed through heartbeats and state tracking.

#### Agent States

| State | Description |
|-------|-------------|
| `idle` | No current work assignment |
| `working` | Actively processing a task |
| `stuck` | Unable to proceed, needs help |
| `done` | Finished current work |

#### Commands

```bash
# Get agent state
mtix agent state agent-claude

# Set agent state
mtix agent state agent-claude --set working

# Send heartbeat (proves agent is alive)
mtix agent heartbeat agent-claude

# View current work assignment
mtix agent work agent-claude
```

### Sessions

Sessions track an agent's work period from start to finish:

```bash
# Start a session
mtix session start agent-claude

# End a session
mtix session end agent-claude

# View session summary (nodes created, completed, time spent)
mtix session summary agent-claude
```

### Stale Detection

Agents that haven't sent a heartbeat within the stale threshold are flagged:

```bash
mtix stale
```

The stale threshold is configurable (default: 24 hours):

```bash
mtix config set agent.stale_threshold 30m
```

---

## Search and Queries

### Full-Text Search

```bash
mtix search --query "authentication"
```

Uses SQLite FTS5 for full-text search across titles, descriptions, and prompts.

`mtix search` accepts the same multi-value filter flags as `mtix list`
(see below) — they combine with the FTS query.

### Filtered Listing

```bash
# List all open nodes
mtix list --status open

# List nodes under a parent
mtix list --under PROJ-1

# Filter by assignee
mtix list --assignee agent-claude

# Filter by priority
mtix list --priority 1

# Filter by node type
mtix list --type issue

# Limit results
mtix list --limit 20
```

#### Multi-value filters

Every filter on `mtix list` and `mtix search` accepts a comma-separated
list of values. Multiple values within one flag combine with **OR**;
multiple flags combine with **AND**. Empty/missing values are simply
ignored.

```bash
# Done OR cancelled nodes (status OR within one flag)
mtix list --status done,cancelled

# Three subtrees at once (Under OR within one flag)
mtix list --under PROJ-1,PROJ-2,PROJ-5

# Two priorities AND two node types (AND across flags, OR within)
mtix list --priority 1,2 --type issue,story

# Real-world: get the focused slice an agent is working on
mtix list --under PROJ-3,PROJ-4 --status done --type issue --json
```

| Flag | Type | Example |
|---|---|---|
| `--status` | enum list | `open,in_progress,done,cancelled` |
| `--under` | ID list | `PROJ-1,PROJ-2,PROJ-5.3` |
| `--type` | enum list | `epic,story,issue,micro` |
| `--assignee` | string list | `agent-a,agent-b,human-vimal` |
| `--priority` | int list (1–5) | `1,2,3` |

All values are passed to SQLite as bound parameters — there is no SQL
injection vector. Whitespace inside the list is trimmed; empty entries
are skipped (`a,,b` is the same as `a,b`).

The HTTP API at `GET /api/v1/search` accepts the same syntax as either
comma-separated (`?under=PROJ-1,PROJ-2`) or repeated (`?under=PROJ-1&under=PROJ-2`)
query parameters.

#### Field projection (`--fields`)

With `--json`, use `--fields` to restrict the output to specific node fields.
This reduces payload size and lets agents focus on exactly the data they need
without post-processing.

```bash
# Only IDs, titles, and prompt text — ideal for context analysis
mtix list --under PROJ-1 --status done --fields id,title,prompt --json

# Just statuses for a progress overview
mtix list --under PROJ-1 --fields id,status,progress --json
```

Field names must match the JSON tags exactly (lowercase, underscore-separated).
Unknown field names return an error listing all valid fields. When `--fields`
is omitted, the full node object is returned (current default behavior).
`--fields` has no effect on table output (non-JSON mode).

#### Briefing format (`--format briefing`)

The briefing format renders each node as a labeled text block, ready to paste
into an LLM context window or read at a glance. No JSON parsing needed.

```bash
# Briefing of all done issues under two epics
mtix list --under PROJ-1,PROJ-2 --status done --type issue --format briefing

# Briefing with only specific fields
mtix list --under PROJ-1 --format briefing --fields id,title,prompt,acceptance

# Briefing with field truncation (useful for large prompts)
mtix list --format briefing --max-field-chars 500

# Include empty fields (omitted by default)
mtix list --format briefing --show-empty
```

Output format:
```
================================================================================
ID: PROJ-1.3
TITLE: Implement rate limiting
NODE_TYPE: issue
STATUS: done
PRIORITY: 1
ASSIGNEE: agent-claude
DESCRIPTION:
  Add sliding window rate limiter to auth endpoints.
PROMPT:
  Implement token bucket algorithm in middleware/ratelimit.go...
ACCEPTANCE:
  1. Rate limit enforced at 100 req/s per agent.
  2. 429 response with Retry-After header.
```

Each node block is separated by `=` characters. Single-line fields use
`LABEL: value`. Multi-line fields use `LABEL:\n  indented body`. Control
characters are sanitized (replaced with U+FFFD). Fields containing
newlines in normally single-line positions are auto-promoted to multi-line
to prevent label injection.

**Via MCP:** use the `mtix_briefing` tool with the same filter parameters
to get briefing output directly from an MCP-connected agent.

### Quick Query Commands

```bash
# Nodes ready for pickup (unblocked, unassigned, open)
mtix ready

# Nodes blocked by dependencies
mtix blocked

# Stale agents (heartbeat timeout exceeded)
mtix stale

# Root-level nodes (no parent)
mtix orphans
```

### Project Statistics

```bash
mtix stats
```

Output:

```
Project Statistics:
  Total nodes: 42
  By status:
    open:        12
    in_progress:  5
    blocked:      3
    done:        18
    deferred:     2
    cancelled:    2
  Overall progress: 43%
```

---

## Working with multiple projects

A single mtix database can track more than one **project prefix**. On a large
effort you might keep your primary `PROJ` tickets next to a `PROJ-DEV-OPS`
project for house-keeping and ops work — all in the same `.mtix` directory,
behind the same hub, with no second install. This is opt-in: if you only ever
use one project, nothing below changes how mtix behaves (see
[Backward compatibility](#backward-compatibility) at the end of this section).

### The primary project

The `prefix` you set at `mtix init` is your **primary project**. It is the
default scope for every list-style command and the default project for a new
root node. Other projects are always addressed explicitly. You never have to
register a project up front — a project comes into existence the moment you
create its first root (projects are derived from your data, not a config list).

### Creating a node in another project

`mtix create --project <PREFIX>` files a **root** node into a different project,
overriding the primary:

```bash
# A root in the primary project (no flag needed)
mtix create "Ship the release"

# A root in a second project
mtix create "Rotate the signing keys" --project PROJ-DEV-OPS
```

If `--project` names a prefix that does **not** yet exist in the database, the
CLI confirms before creating it, so a typo cannot silently spawn a junk
project:

```text
$ mtix create "Rotate the signing keys" --project PROJ-DEV-OPS
Create new project PROJ-DEV-OPS? [y/N] y
○ Created PROJ-DEV-OPS-1: Rotate the signing keys
```

Pass `--yes` to skip the confirmation (useful in scripts):

```bash
mtix create "Rotate the signing keys" --project PROJ-DEV-OPS --yes
```

**Children inherit their parent's project.** When you create a node `--under` a
parent, it is always filed into the parent's project — you never pass
`--project` for a child. If you do pass one, it must match the parent's
project, otherwise the create errors (a node can never live in a different
project than its parent):

```bash
# Inherits PROJ-DEV-OPS from the parent — no --project needed
mtix create "Generate the new keypair" --under PROJ-DEV-OPS-1

# Errors: a child cannot be filed into a different project than its parent
mtix create "Wrong" --under PROJ-DEV-OPS-1 --project PROJ
```

### Scoping list-style commands

The list-style commands — `list`, `search`, `query`, `orphans`, `blocked`,
`ready`, `stale` — default to the **primary** project. Two flags change the
scope:

- `--project <PREFIX>` — show only that project.
- `--all-projects` — span every project in the database.

```bash
# Default: only the primary project
mtix list

# Only the ops project
mtix list --project PROJ-DEV-OPS

# Every project at once
mtix list --all-projects

# Same flags work on the other list-style commands
mtix ready --project PROJ-DEV-OPS
mtix orphans --all-projects
```

`--project` and `--all-projects` are mutually exclusive. ID-addressed commands
(`show <id>`, `tree <id>`) need no scope flag — the ID already carries its
project, including multi-hyphen prefixes like `PROJ-DEV-OPS-1.2`.

### Listing the projects in a database

`mtix projects` lists every project present, with its node count, and marks the
primary with a `*`:

```bash
mtix projects
```

```text
PROJECT          NODES   PRIMARY
PROJ                42   *
PROJ-DEV-OPS         7
```

Add `--json` for a machine-readable list (`prefix`, `count`, `is_primary`).

### Sync carries every project in the database

Sync is **project-agnostic by design**: `mtix sync push`, `pull`, and `clone`
carry **all** projects in your local database to and from the hub — there is no
per-project sync flag or cursor. One database maps to one hub, and that hub
holds every project in the database. A teammate who clones the hub reconstructs
*all* of those projects, not just the primary.

This is intentional (FR-MULTI-PROJECT MP-20): the hub is already namespaced per
project internally (each project has its own number registry, version gate, and
settlement), so carrying several projects over one connection is free, while
per-project routing would add coordination with no benefit for the single-team,
single-hub model mtix targets. If you genuinely need two projects to live on
different hubs, give them two separate `.mtix` databases. See
[docs/SYNC-DESIGN.md §6.4](docs/SYNC-DESIGN.md) for the design rationale.

### Backward compatibility

If your database holds a single project, mtix behaves exactly as it always has:
the primary is the default everywhere, no command requires `--project`, no
confirmation prompt ever appears, and `mtix projects` simply lists the one
project. Every multi-project affordance is opt-in and only matters once a second
project exists or you pass `--project` / `--all-projects`.

---

## Configuration

Configuration lives in `.mtix/config.yaml`. Manage it via the CLI:

```bash
# View a setting
mtix config get auto_claim

# Change a setting
mtix config set auto_claim true

# Reset to default
mtix config delete auto_claim
```

### Configuration Reference

| Key | Default | Description |
|-----|---------|-------------|
| `prefix` | `PROJ` | Project ID prefix |
| `auto_claim` | `false` | Auto-claim children when parent is claimed |
| `max_depth` | `50` | Advisory depth warning (does not reject) |
| `agent.stale_threshold` | `24h` | Duration before agent is considered stale |
| `agent.stuck_timeout` | `0` (disabled) | Auto-unclaim stuck agents after this duration |
| `session.timeout` | `4h` | Max session duration before auto-end |
| `data.soft_delete_retention` | `720h` (30 days) | Time before soft-deleted nodes are purged |
| `progress.weighted` | `false` | Use weight field in progress calculation |
| `sync.auto_sync` | `true` | Automatic import of a changed `.mtix/tasks.json` (after `git pull`); `false` turns it off, except for a store that holds no tasks. `mtix config set` accepts only true or false. Tracked in git with `.mtix/config.yaml`, so it applies to every clone. Writes keep exporting, but never over a pulled board that was not imported. See "Automatic import of `.mtix/tasks.json`" |

---

## Web UI

Start the server to access the web UI:

```bash
mtix serve --port 8377
```

Open `http://127.0.0.1:8377` in your browser.

### Views

- **All Issues** — Filterable table of all nodes with status, priority, assignee, and progress. Filter by status using the tab bar.
- **Dashboard** — Project overview with overall progress bar, status breakdown (stacked bar chart), per-status counts, and quick stat cards.
- **Tree View** — Expandable sidebar tree that lazy-loads children on expand. Shows status icons, short IDs, and progress percentages for parent nodes.
- **Node Detail** — Full node view with inline title editing, status transitions via badge popover, prompt editor, children list with quick-add bar, context chain, and tabbed Description/Activity/Dependencies sections.
- **Stale Board** — Lists stale agents and tasks needing attention.
- **Agent Activity** — Real-time agent status and current work.

### Creating Issues

Press **`c`** or click the **New** button in the top bar to open the create modal. Fill in:
- **Title** (required)
- **Description** (optional)
- **LLM Prompt** (optional)
- **Priority** (P0–P4, default P2)
- **Parent ID** (optional, e.g. `1.2` to create as a child)

Press **Cmd+Enter** to submit or **Escape** to cancel.

### Keyboard Shortcuts

| Key | Action |
|-----|--------|
| `Cmd+K` / `Ctrl+K` | Open command palette |
| `c` | Create new issue |
| `j` / `k` | Navigate down/up in issue list |
| `x` | Mark focused issue as done |
| `e` | Edit title (in detail view) |
| `p` | Edit prompt (in detail view) |
| `[` | Toggle sidebar |
| `Enter` | Open focused issue |
| `Escape` | Close modal/palette |

### Status Transitions

Click a status badge to see valid transitions. Forward transitions apply immediately; destructive transitions (e.g. Cancel) show a confirmation popover. The 7-state machine:
`open` → `in_progress` → `done` / `blocked` / `deferred` / `cancelled`

### Command Palette

**Cmd+K** opens the palette. Type to search nodes, or choose from actions like "Create node". Recent nodes appear when the palette opens with no query.

### Dark/Light Theme

Toggle via the sun/moon icon in the top bar. The theme persists in localStorage. Uses an indigo accent (#6366F1) with a refined design system.

### Real-Time Updates

The UI connects to `/ws/events` via WebSocket for live updates. Connection status shows in the top bar and breadcrumb (green dot = connected, pulsing yellow = reconnecting, red = disconnected).

### Server Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--addr` | `127.0.0.1` | Bind address |
| `--port` | `8377` | HTTP port |

The server binds to localhost by default. To expose on the network, use `--addr 0.0.0.0` (requires authentication configuration).

---

## REST API

The REST API is available at `/api/v1/` when the server is running. All mutation endpoints require the `X-Requested-With: mtix` header for CSRF protection.

### Node Endpoints

| Method | Path | Description |
|--------|------|-------------|
| `POST` | `/api/v1/nodes` | Create a node |
| `GET` | `/api/v1/nodes/:id` | Get a node |
| `PATCH` | `/api/v1/nodes/:id` | Update a node |
| `DELETE` | `/api/v1/nodes/:id` | Soft-delete a node |
| `GET` | `/api/v1/nodes/:id/children` | List children |
| `POST` | `/api/v1/nodes/:id/decompose` | Batch create children |
| `GET` | `/api/v1/nodes/:id/ancestors` | Get ancestor chain |
| `GET` | `/api/v1/nodes/:id/tree` | Get subtree |

### Workflow Endpoints

| Method | Path | Description |
|--------|------|-------------|
| `POST` | `/api/v1/nodes/:id/claim` | Claim node (requires `X-Agent-ID` header) |
| `POST` | `/api/v1/nodes/:id/unclaim` | Release assignment |
| `POST` | `/api/v1/nodes/:id/done` | Mark complete |
| `POST` | `/api/v1/nodes/:id/defer` | Defer work |
| `POST` | `/api/v1/nodes/:id/cancel` | Cancel with reason |
| `POST` | `/api/v1/nodes/:id/reopen` | Reopen |
| `POST` | `/api/v1/nodes/:id/rerun` | Invalidate and reprocess |
| `POST` | `/api/v1/nodes/:id/comment` | Add comment |

### Query Endpoints

| Method | Path | Description |
|--------|------|-------------|
| `GET` | `/api/v1/search` | Full-text search (`?q=`, `?status=`, `?assignee=`) |
| `GET` | `/api/v1/ready` | Nodes ready for pickup |
| `GET` | `/api/v1/blocked` | Blocked nodes |
| `GET` | `/api/v1/stale` | Stale agents |
| `GET` | `/api/v1/orphans` | Root-level nodes |
| `GET` | `/api/v1/stats` | Project statistics |
| `GET` | `/api/v1/progress/:id` | Node progress |
| `GET` | `/api/v1/context/:id` | Assembled context |

### Dependency Endpoints

| Method | Path | Description |
|--------|------|-------------|
| `POST` | `/api/v1/deps` | Add dependency |
| `DELETE` | `/api/v1/deps/:from/:to` | Remove dependency |
| `GET` | `/api/v1/deps/:id` | Get blockers |

### Agent Endpoints

| Method | Path | Description |
|--------|------|-------------|
| `POST` | `/api/v1/agents/:id/sessions/start` | Start session |
| `POST` | `/api/v1/agents/:id/sessions/end` | End session |
| `GET` | `/api/v1/agents/:id/sessions/summary` | Session summary |
| `POST` | `/api/v1/agents/:id/heartbeat` | Send heartbeat |
| `GET` | `/api/v1/agents/:id/state` | Get agent state |
| `PATCH` | `/api/v1/agents/:id/state` | Set agent state |
| `GET` | `/api/v1/agents/:id/work` | Current work |

### Admin Endpoints

| Method | Path | Description |
|--------|------|-------------|
| `GET` | `/api/v1/admin/config` | Read configuration |
| `PATCH` | `/api/v1/admin/config` | Update configuration |
| `POST` | `/api/v1/admin/gc` | Run garbage collection |
| `POST` | `/api/v1/admin/verify` | Run integrity check |
| `POST` | `/api/v1/admin/backup` | Create database backup |

### Bulk Endpoints

| Method | Path | Description |
|--------|------|-------------|
| `PATCH` | `/api/v1/bulk/nodes` | Batch update nodes (max 100) |

### WebSocket

Connect to `/ws/events` for real-time updates:

```javascript
const ws = new WebSocket("ws://127.0.0.1:8377/ws/events");

// Subscribe to specific events or subtrees
ws.send(JSON.stringify({
  subscribe: {
    under: "PROJ-1",
    events: ["node.created", "node.updated", "status.changed"]
  }
}));

ws.onmessage = (event) => {
  const data = JSON.parse(event.data);
  console.log(data.type, data.node_id, data.data);
};
```

#### Event Types

| Event | Description |
|-------|-------------|
| `node.created` | Node created |
| `node.updated` | Node fields changed |
| `node.deleted` | Node soft-deleted |
| `node.undeleted` | Node restored |
| `node.claimed` | Agent claimed a node |
| `node.unclaimed` | Agent released a node |
| `node.cancelled` | Node cancelled |
| `status.changed` | Status transitioned |
| `progress.changed` | Progress recalculated |
| `nodes.invalidated` | Batch invalidation |
| `dependency.added` | Dependency created |
| `dependency.removed` | Dependency removed |

---

## gRPC API

mtix exposes a gRPC API for high-performance integrations. The Protocol Buffer definitions are in the `proto/` directory.

### Available RPCs

The gRPC service mirrors the REST API with strongly-typed request/response messages:

- `CreateNode`, `GetNode`, `UpdateNode`, `DeleteNode`, `UndeleteNode`
- `ListChildren`, `Decompose`
- `Claim`, `Unclaim`, `Done`, `Defer`, `Cancel`, `Reopen`, `Rerun`
- `Search`, `GetReady`, `GetBlocked`, `GetStale`
- `GetStats`, `GetProgress`, `GetTree`, `GetContext`
- `AddDependency`, `RemoveDependency`

### Error Codes

| mtix Error | gRPC Code |
|------------|-----------|
| Not found | `NOT_FOUND` |
| Invalid input | `INVALID_ARGUMENT` |
| Already exists | `ALREADY_EXISTS` |
| Invalid transition | `FAILED_PRECONDITION` |
| Cycle detected | `FAILED_PRECONDITION` |
| Conflict | `ABORTED` |

---

## MCP Tools

mtix registers as an MCP (Model Context Protocol) tool provider for LLM agent frameworks. When running as a server, agents can discover and invoke tools programmatically.

Client setup for Claude Desktop, Claude Code, Cursor, Windsurf, OpenAI Codex, and pi is covered in [docs/MCP-SETUP.md](docs/MCP-SETUP.md). For Claude Code, Codex, and pi, `mtix plugin install --target <agent>` performs the setup (skills for Claude Code; AGENTS.md briefing plus MCP config for Codex; AGENTS.md plus pi-mcp-adapter guidance for pi). Installs are non-destructive: existing files are never modified.

### Tool Categories

**Node Management:** `mtix_create`, `mtix_show`, `mtix_list`, `mtix_update`, `mtix_delete`, `mtix_undelete`, `mtix_decompose`

**Workflow:** `mtix_claim`, `mtix_unclaim`, `mtix_done`, `mtix_defer`, `mtix_cancel`, `mtix_reopen`, `mtix_ready`, `mtix_blocked`, `mtix_search`, `mtix_rerun`

**Context:** `mtix_context`, `mtix_prompt`, `mtix_annotate`, `mtix_resolve_annotation`

**Dependencies:** `mtix_dep_add`, `mtix_dep_remove`, `mtix_dep_show`

**Sessions:** `mtix_session_start`, `mtix_session_end`, `mtix_session_summary`, `mtix_agent_heartbeat`, `mtix_agent_state`, `mtix_agent_work`

**Analytics:** `mtix_stats`, `mtix_progress`, `mtix_stale`, `mtix_orphans`

**Sync (FR-18):** `mtix_sync_workflow` — detects local sync state (solo / sync-configured-no-hub / sync-active / divergent-state-pending / hub-unreachable) and returns rule-based recommendations with severity and doc links. Output is bounded to 4 KB; the DSN is never returned. Includes an upgrader-detection branch that recommends `mtix sync backfill` when local nodes exist but no sync events have been emitted. See [Team collaboration with sync (FR-18)](#team-collaboration-with-sync-fr-18) for the corresponding CLI workflow.

---

## Backup, Export, and Import

### Backup

Create an atomic backup of the database:

```bash
mtix backup /path/to/backup.db
```

The backup uses SQLite's `VACUUM INTO` for atomicity and runs `PRAGMA quick_check` to verify the result.

### Export

Export all project data to JSON:

```bash
mtix export > project-data.json
```

The export includes every node with every stored field, dependencies, agents, sessions, and a SHA-256 checksum for integrity verification. `.mtix/tasks.json`, the git-tracked board, is the same document. Each node carries its annotations (comments, review verdicts, close receipts) in the structure `mtix show --json` returns, its activity stream, its invalidation fields and every other column; soft-deleted nodes are included. The checksum covers the nodes, annotations included, and the dependencies. No node field is left out. The format (`schema_version` 2.0.0 since 0.5.4; 0.5.3 and earlier wrote 1.0.0), every field and the checksum rule are described in [docs/EXPORT-FORMAT.md](docs/EXPORT-FORMAT.md).

**Mixed versions on one board.** A client older than 0.5.4 supports only `schema_version` 1.x, so its automatic import skips a file written by 0.5.4 and logs `tasks.json schema version is newer than supported — upgrade mtix`; its `mtix import` of the file fails with `checksum verification failed`. That protects its store only until its next writing command: every write re-exports `.mtix/tasks.json` from that store, which lacks everything the client skipped and every annotation, and committing that file reverts every change made upstream. On a client older than 0.5.4, even an `mtix import` that fails rewrites `.mtix/tasks.json` from that client's store, so `mtix import .mtix/tasks.json` is no remedy there either. Until every teammate who shares a board runs 0.5.4 or later, do not run writing commands or `mtix import` on an older client and do not commit its `.mtix/tasks.json`. If such a file was committed, restore `.mtix/tasks.json` from the last good commit in git history, or restore the store of a machine that imported it from the backup taken before that import: `.mtix/data/backups/pre-sync-<time>.db` (0.5.4 and later) or `.mtix/data/pre-sync-backup.db` (0.5.3 and earlier); copy it over `.mtix/data/mtix.db` while no mtix process runs. A 0.5.4 store is protected from such a file by the auto-import guard (MTIX-95.31.2): the file carries no annotations or activity, so the automatic import is refused and nothing changes (see "Automatic import of `.mtix/tasks.json`" below). 0.5.4 reads files written by older clients.

### Import

Import data from a JSON export:

```bash
# Merge mode (default) — add what is new, never drop local annotations
mtix import project-data.json

# Replace mode — drop existing data and reimport
mtix import project-data.json --mode replace
```

**Replace mode** deletes every node, dependency, agent and session, then writes the file's content with every field, annotations and activity included. An export followed by a replace import leaves the store as it was. The automatic import of a changed `.mtix/tasks.json` (after a `git pull` or checkout) is a replace import, but it is refused when it would delete local data (see "Automatic import of `.mtix/tasks.json`" below); `mtix import --mode replace` is not guarded. A file written before 0.5.4 (`schema_version` 1.0.0) carries no annotations or activity, so a replace import of it leaves every node without them.

**Merge mode**:
- A node the store does not have is created as exported.
- For a node the store has, annotations merge as a union by annotation id: no local annotation is dropped, so a file without annotations keeps the local ones. Annotations are keyed by id alone: for an id both sides hold, the local copy wins unless the incoming copy is resolved and the local one is not, so a resolution never regresses. The activity stream merges as a union of distinct entries: an entry is identified by its id, type, author, text and time together, so two different entries that share an id are both kept, and an entry both sides hold is kept once.
- When the node's content hash differs, the file's values replace its other fields; when it is the same, they keep their local values. A file written before 0.5.4 never changes the fields it does not carry (annotations, activity, `previous_status`, the invalidation fields and the others listed in docs/EXPORT-FORMAT.md).
- When the file's copy of a task is stale (it lacks one of the task's local activity entries, its `updated_at` is older, or the file comes from a client older than 0.5.4 and carries no activity), a field value the copy leaves empty keeps its local value: exactly the field values the automatic import's refusal lists. If the value is part of the task's status (a closed or wake time, an assignee, an invalidation), the task keeps its whole local status. A current copy's clear (a teammate's unclaim, say) applies.
- A task you hold whose uid the file holds under another id is the same task, renumbered by another clone: the merge moves it, and its subtree, to that id without asking for `--confirm`, and prints the move.
- A node the store holds as a different task (another uid, for example when two clones each created `PROJ-3`, even in the same second) is never overwritten. The local task and its subtree are renumbered to the next number free in both the store and the file under the same parent (the number after the highest either holds, so no earlier number is reused), keeping their uids, annotations, activity and dependencies, and the file's task takes the id: the published board keeps its numbers, and later creates continue after the new number. The import prints the renumbering (uid, old id, new id; `--remap-file <path>` writes it as JSON) and applies it only with `--confirm`; without it, nothing is written. Every task mtix imports without a uid (from a board written before uids were shared) is given one in the import, a backfill uid (a UUIDv8 that carries a fixed marker), and a store that still holds tasks without a uid gives them one when it opens (it logs how many; below the free-space floor, or with `MTIX_SKIP_INTEGRITY_CHECK=1`, it writes nothing and the next open retries), so every board 0.5.4 writes carries a uid for every task. When the file's copy has no uid to compare (a board written by a client at 0.3 or older, or one not re-exported since), the titles decide (a task of yours never lacks a uid here: the merge, and the automatic import's comparison, give it a backfill uid first, and the import shows it as `uid=(new)`): with the same title the file's node is merged into the local one as before, and with another title it is a different task, renumbered like one (the local task and its subtree move, only with `--confirm`; a local task without a uid is given one, shown as `uid=(new)` and left out of a `--remap-file` written without `--confirm`), and the import lists it under `of these, a task under this id with a different title and no uid to compare` with both titles. A task a teammate only retitled on such a board is renumbered too, and the merge keeps both copies: the renumbered one holds your comments, activity and field values the board lacked, so move them to the task at the id, or keep the renumbered copy, before you delete anything. A node under another uid is merged too when it is the same task: it has the same creation time, and at least one of the two uids was given to the task after it was created: a backfill uid, or a UUIDv7 (which carries the time it was minted) minted more than an hour after, as a clone did when it upgraded from before uids were shared. So two clones that each gave a task from such a board their own backfill uid keep one task: after a teammate's retitle, the first exchange may refuse once with `treated as the same task (uid assigned at upgrade)`, and the merge then takes the teammate's uid. Any other pair is two different tasks, even when they were created in the same second or a create waited seconds for another command: mtix treats two tasks as one only on that clear sign, because merging two tasks would lose one, while renumbering asks you first. The merge adopts the file's uid, and never replaces a uid with an empty one.
- The merge lists every uid it adopts in its report on stderr, applied or not: `uids adopted from the file (the same task under another uid): N`, then one line per task, `PROJ-3 local uid=<uid> -> file uid=<uid> (local "<your title>", file "<their title>")`. Once the import is applied, `--json` carries them as `uid_adoptions` (`id`, `local_uid`, `file_uid`, `local_title`, `file_title`); an import that waits for `--confirm` prints no JSON. `--remap-file` maps each local uid to its id, so a reference to the old uid still resolves. Check every adoption whose titles differ. One case gets past the rule: two different tasks created in the same second, when at least one clone assigned its uid after the task was created (at upgrade, or as a backfill uid when it imported the task without one or opened a store holding it without one, as happens for a task whose create event its event log lacks, for example one created before 0.2): that uid counts as assigned later, so mtix treats the two as one task and the merge keeps the file's task under the id. The report then adds `a task above whose titles differ may be two different tasks ...`, and the backup the merge took first (its path is printed before the report) holds your task. Restoring that backup drops every change made after the merge, from the store and, once the pulled board is restored, from the board: recover right away, or first note the changes made since the merge and redo them afterwards. To recover it, stop every mtix process that uses this store (the MCP server, `mtix serve`, a running `mtix daemon`) and copy that backup over `.mtix/data/mtix.db`. Then delete `.mtix/data/mtix.db-wal` and `.mtix/data/mtix.db-shm`: they belong to the replaced database and must not be replayed onto the backup. Then, before you run any mtix command, restore the pulled board: `git checkout -- .mtix/tasks.json`, or take the file from the pulled commit. The merge's own export rewrote it, so the next command would otherwise export the restored store over it and drop the teammate's task. The next command refuses the import again. Copy your task to a new one (`mtix show PROJ-3`, then `mtix create` with its title and description; writes stay local while the refusal is pending), then run `mtix import .mtix/tasks.json --mode merge --confirm`: the teammate's task keeps PROJ-3 and your copy keeps its new id. The automatic import lists such a pair, when the titles differ, as `PROJ-3: treated as the same task (uid assigned at upgrade) (local "<your title>", file "<their title>")` and refuses, even when nothing else would be lost. Its merge option then says that the merge takes the file's title and content for that task, and names each such task. If the titles name two different tasks, do not merge yet: copy your task to a new one first, as above, then merge. The opposite case is safe but can look wrong: when every uid a task carries was assigned less than an hour after the task was created (both clones upgraded within that hour), the uids count as minted at creation, so the two copies of that one task are listed as `a different task under this id` with identical titles, and the merge renumbers your copy only with `--confirm`. If both copies are the same task, merge with `--confirm`, check that the renumbered copy holds nothing the other lacks (annotations, activity), and delete it with `mtix delete <new id>`.
- `mtix import --mode merge` backs up the database first, as the automatic import does (`.mtix/data/backups/pre-sync-<UTC time>.db`, newest 5 kept), once the file has passed its checks and just before it writes. A merge that would change nothing (the same file again, say), one that awaits `--confirm`, and one refused by its checks take no backup, so they never rotate the automatic import's backups away.
- A merge never removes an annotation or activity entry. To make the store match a file exactly, use replace mode.

Import validates, before it writes anything (a failed check writes nothing):
- The major `schema_version` is not higher than the one this mtix writes; a newer file is refused with `newer than supported ... upgrade mtix` (the automatic import skips it with the same message)
- Node count matches the `node_count` field
- SHA-256 checksum over canonical JSON. A file written by mtix always verifies, including one holding text that is not valid UTF-8. One exception: a file written by 0.5.3 or earlier that held both invalid UTF-8 and a genuine U+FFFD replacement character still fails; check it, then import it with `mtix import --recompute-checksum`
- Every time value (node timestamps, annotation and activity times, dependency, agent and session times) is RFC 3339 with a UTC year from 1 to 9999, and the required ones (a node's `created_at` and `updated_at`, a dependency's `created_at`, a session's `started_at`) are not empty; the error names the record and the field

A refused import leaves `.mtix/tasks.json` and its stored hash as they were. The automatic import of a changed `.mtix/tasks.json` also refuses, and changes nothing, when the local store cannot be exported (for example a stored annotations or activity value that does not parse): it cannot rule out local changes the file lacks. `mtix export` fails the same way. Both messages name the node, the field and `mtix recover`, which salvages everything readable (see "Disk full and corruption recovery").

After an import:
- FTS5 index is rebuilt
- Sequence counters are reconstructed

### Automatic import of `.mtix/tasks.json`

`.mtix/tasks.json` is the git-tracked board. When a `git pull`, checkout or branch switch changes it, the next mtix command imports it before running. The import is a replace import: the store then matches the file. It checks the file against the store first (below), and the replace re-checks, inside its own transaction, that the store is still the one it compared: a write that lands in between, from another process or the MCP server, is kept, nothing is imported, and the next command checks the file again. Writing commands export the store back to the file afterwards, but never over a board that changed on disk and was not imported (see "Writes never overwrite a pulled board" below).

These commands never import automatically, matched by their full command path: `mtix init`, `mtix export`, `mtix import`, `mtix migrate`, `mtix recover`, `mtix help`, `mtix version`, `mtix sync` without a subcommand, `mtix plugin install`, and every command routed to a running `mtix serve` (it runs in the server). Every other command imports a changed board first, the `mtix sync` subcommands included (`mtix sync init`, `mtix sync migrate`, `mtix sync push` and the rest). `mtix mcp`, `mtix serve` and the daemon import when they start, and after that whenever one of their exports finds the board changed on disk (below). A command in the list above that writes still exports afterwards, and that export imports a changed board first: `mtix import` of another file, for example, is followed by the automatic import of a changed `.mtix/tasks.json`, with its notice or refusal.

**Notice.** Every automatic import that applies a changed file prints one line on stderr:

```
mtix: imported the changed .mtix/tasks.json: nodes 1 added, 2 updated, 0 removed; dependencies 1 added, 0 removed (local database backed up to .mtix/data/backups/pre-sync-20260924-101500.db)
```

It also refreshes the conflict baseline, so a second pull before any write is imported too, not reported as a conflict.

**Conflict baseline.** Every automatic export (after each write) and every applied import records a hash of the local store, the conflict baseline (`.mtix/data/sync-db.sha256`); `mtix export` does not. When the file changed and the store no longer matches that hash, both changed since the last sync: nothing is imported, and the refusal is recorded as a `conflict` (see the table below). **After upgrading from 0.3.0 through 0.5.3**, the baseline on disk is the one the older version recorded, over its 1.0.0 export. That export lacked the fields 0.5.4 exports (comments, activity and the other fields added in 2.0.0), and before 0.4 it had no uids either: the upgrade gives the tasks their uids. A baseline can also predate the uids a store gives its tasks without one when it opens. mtix recognizes such a baseline: when the store, unchanged, matches it in that older form, mtix records the baseline again in the current form and logs `sync_baseline_upgraded` once, and the first pull after the upgrade is not reported as a conflict, whether or not other commands ran before it. It can still be refused as a loss: when the local store holds data that boards written by an older version never carried (comments, activity entries, a previous status), a teammate whose store was filled from such boards lacks it, and so does the board they commit. The way out that keeps that data today is `mtix import .mtix/tasks.json --mode merge`, which can undo a teammate's change or your own, task by task (see the merge rule below): afterwards check `git log -p .mtix/tasks.json` and reapply what it undid. A fix is tracked. One case is not recognized: when the older version's last automatic import was not followed by a writing command. That version recorded no new baseline after an automatic import, and read-only commands record none either, so the baseline describes the store before that import, and the first pull after the upgrade is still refused as a conflict (see below for the way out). A change made while a baseline in an older form is on disk is still a conflict: a change you made before upgrading and never exported, or one made on 0.5.4 while a pulled board is kept, not imported. The exception is a change only to a field the older form lacks (a comment, an activity entry or another field added in 2.0.0, or a uid), which the older version did not detect either. The loss check below still compares those fields: a board that would delete a local comment or activity entry is refused. **A conflict although you changed nothing** (such as the case above): apply the pulled board through the automatic import, which still runs the loss check. From the project root, run `mtix sync --fix`, which records a new baseline and rewrites `.mtix/tasks.json` from the local store; then `git checkout HEAD -- .mtix/tasks.json`, which puts the pulled board back; then any command that imports, such as `mtix list`. That command imports the board and prints its one-line notice, or refuses it and lists what it would delete, as described below. Do not run `mtix sync --fix` without the checkout that follows it: alone, it drops every change in the file. `mtix import .mtix/tasks.json --mode replace` runs no loss check, so it deletes local data the file lacks, a comment for example. **The merge rule.** The merge decides per task, by its content (title, description, prompt, acceptance and labels). When your copy's content matches the file's, every local field value stays, so a teammate's claim, status, assignee or wake-time change to that task is not applied, and your next export writes your values back to `.mtix/tasks.json`, which reverts the teammate's change upstream once committed. When it differs, the file's copy wins, so your own edits to that task (an unclaim or a prompt edit, for example) are undone, except values the file leaves empty in a copy that is not current. Comments and activity entries from both sides are kept. After a merge, check `git log -p .mtix/tasks.json` and reapply what it undid.

**Backup.** mtix first checks the whole file: its format and schema version, its node count, checksum and time values, whether the local store can be read, and whether anything would be lost (below). Only a file that passes every check is backed up and imported. The backup is a verified copy of the local database (`VACUUM INTO`, then `PRAGMA quick_check`) at `.mtix/data/backups/pre-sync-<UTC time>.db`, with a `-2`, `-3`, ... suffix for further imports in the same second. Every import that applies takes a new backup of the store as it is then, even of a file imported before (a board checked out again after local writes). The one exception is a retry: when an import fails while writing, its retries reuse that backup as long as the store has not changed since. mtix keeps the newest 5 and deletes older `pre-sync-*` files only; the rolling `mtix-*.db` backups and any other file in the directory are left alone, so failing or refused imports never rotate good backups away. If the backup cannot be written, the import is skipped with a warning. To return to the state before an import, stop every mtix process (the MCP server included) and copy the backup over `.mtix/data/mtix.db`. mtix 0.5.3 and earlier kept one overwritten copy, `.mtix/data/pre-sync-backup.db`; 0.5.4 no longer writes it and leaves an existing one in place. `.mtix/data/` is local state: keep it out of git.

**What counts as a loss.** The automatic import compares the node and dependency data of the store with the file, node by node. Agents and sessions are runtime state and are always replaced. These are always a loss when the file lacks them: a task, a comment (annotation), the resolution of a comment, an activity entry, and a dependency. A field value the file leaves empty (an assignee, a description, a closed time, a wake time, a deletion) is a loss unless the file's copy of the task is current: it holds every activity entry the local copy holds, and its `updated_at` is not older than the local one. Activity only grows, and the writes that record no activity (`mtix update`, `mtix delete`) still move `updated_at`, so a current copy has seen your latest change, and a field it cleared was cleared on purpose: a teammate's `mtix unclaim`, `mtix reopen`, undeferral, `mtix undelete` or later `mtix update` applies. A copy that lacks one of your entries or is older is stale, and a file written by a client older than 0.5.4 carries no activity at all, so the fields such files leave empty are losses. A task whose id the file gives to a different task is a loss of the whole task: both copies carry a uid and the uids differ, as when you and a teammate each created `PROJ-3`, or you created a task while a refusal was pending and it took an id the pulled board already uses. It is listed as `PROJ-3: a different task under this id (local "<your title>", file "<their title>")`. A task whose id the file holds with another title while the file's copy has no uid to compare (a board from a client at 0.3 or older, or one never re-exported after an upgrade) may be a different task too: it is listed as `PROJ-3: a task under this id with a different title and no uid to compare (local "<your title>", file "<their title>")`, and the merge renumbers your task with `--confirm`. Two copies under different uids are the same task only when they have the same creation time and one of the uids was given to the task after it was created: a backfill uid (mtix gives one to every task it imports without a uid, and to every such task a store holds when it opens), whatever its time, or a uid given more than an hour after creation, when a clone upgraded from before uids were shared; the import then takes the file's uid. A file without uids does not lose a backfill uid: when the task has the same title, the missing uid is no loss, and the replace keeps your uid (a task with another title is a different task: it gets a new uid), so each pull of a teammate's board from a client at 0.3 or older imports as before; a uid your client gave the task at creation that such a file leaves out is still a loss. Every other pair, such as two tasks two clones created in the same second, is two different tasks. A task the file holds under another id with the same uid (another clone renumbered it) is not a loss either: the import moves it there. Clocks that disagree between machines can make a current copy look older; that refuses, the safe side. Three things this comparison does not treat as a loss. A stale copy can still change a non-empty value back (an older title, say), as checking out an older board does on purpose; the backup keeps the state before. A teammate's copy with a later `updated_at` counts as current even when it missed your `mtix update` or `mtix delete`, which record no activity (after you both edited the same task, or when the teammate's clock runs ahead), so a field that copy leaves empty is applied as cleared; the backup keeps the state before. And a dependency a teammate removed on purpose still counts as a loss, so their `mtix dep remove` is refused until you choose.

**Refusal.** When the file would lose anything, mtix refuses the automatic import. Nothing is imported, no backup is taken, and the stored hash is not updated, so the refusal repeats, printed once, on every command until you choose. The refusal lists what would be lost, task by task, for example:

```
mtix: auto-import of .mtix/tasks.json refused: a replace import of the changed file would delete local data the file lacks:
  PROJ-1: 2 annotations (01J9..., 01J9...), 1 activity entry
  PROJ-2: 1 activity entry, fields agent_state, assignee
Nothing was imported. Until you choose, mtix refuses again on every command, and writing commands save to the local store but leave .mtix/tasks.json as it is.
Choose one, run from the project root (/path/to/project):
  mtix import .mtix/tasks.json --mode merge    backs up the database, keeps every value the refusal lists and adds the file's changes: ...
  mtix sync --fix                              keep the local store and rewrite .mtix/tasks.json from it; every change in the file is dropped
  mtix import .mtix/tasks.json --mode replace  make the file win and delete the local data listed above (take a copy first: mtix backup <file>)
```

Run the command you choose from the project root. `mtix import` and `mtix sync` do not auto-import, so they run despite the refusal, and each of the three resolves it: `.mtix/tasks.json` is rewritten from the store afterwards.

| Command | Keeps | Loses |
|---------|-------|-------|
| `mtix import .mtix/tasks.json --mode merge` | Everything the refusal lists: every local task, comment, activity entry and dependency, and every field value listed (with the task's local status when the value is part of it), plus the file's new data and its other changes. The database is backed up first. A task listed as a different task under its id is renumbered to the next number free in both the store and the file, keeping its uid, and the file's task keeps the id; the import lists the renumbering and applies it only when you rerun it with `--confirm` | Field changes on one side, decided per task by its content (title, description, prompt, acceptance, labels): where your copy's content matches the file's, your field values win, so a teammate's claim, status, assignee or wake-time change to it is not applied, and the board you then commit reverts it upstream; where it differs, the file's values win, so your own edits to that task (an unclaim or a prompt edit, for example) are undone, except the values listed. Check `git log -p .mtix/tasks.json` afterwards and reapply what it undid (see "Merge mode" and the merge rule under "Conflict baseline") |
| `mtix sync --fix` | The local store exactly | Every change in the file: `.mtix/tasks.json` is rewritten from the store |
| `mtix import .mtix/tasks.json --mode replace` | The file exactly | Everything the refusal listed. Take a copy first with `mtix backup <file>` |

To choose: when the file came from a client older than 0.5.4, use `mtix sync --fix` or merge, and ask the teammate to upgrade. When a teammate's board is stale, find out which changes are newer (for example with `git log -p .mtix/tasks.json`); merge keeps every comment, activity entry and value the refusal lists but decides the other field values per task, so it can undo the teammate's field changes or your own edits (see the merge rule under "Conflict baseline" above), and replace keeps theirs but deletes what the refusal listed. If you cannot tell, ask before choosing.

**Writes never overwrite a pulled board.** A write exports the store to `.mtix/tasks.json` only when the file on disk is the one mtix last wrote or imported. When it changed on disk and was not imported (a pull while `mtix mcp`, `mtix serve` or the daemon runs, a pull with automatic import off, or a board mtix refused), the export first runs the automatic import. If that imports the board, the export goes ahead. Otherwise the board is left as it is, the write stays in the local store, the refusal is recorded as pending, and one line says so, with the way out for that kind of refusal:

| Kind (`mtix sync --json`) | Cause | Way out |
|------|-------|---------|
| `lossy` | The file would delete local data | The three commands above |
| `conflict` | Both the file and the local store changed since the last sync | Merge them with `mtix import .mtix/tasks.json --mode merge` (the merge decides per task: when the task's content (title, description, prompt, acceptance, labels) matches the file's, your field values win, and otherwise the file's copy wins, undoing your edits to that task; afterwards check `git log -p .mtix/tasks.json` and reapply what it undid), keep the local store with `mtix sync --fix`, or keep the file with `--mode replace`. If you changed nothing locally, use the way out under "Conflict baseline" above instead. When the replace would also delete local data, the conflict is printed as a refusal that lists it, and the line names what the replace deletes |
| `newer_schema` | A newer mtix wrote the file | Upgrade mtix, then run any command. Do not rewrite it from the local store, which would downgrade it to the older format |
| `invalid_file` | The file fails its checks (it is not an mtix export, does not parse, or its node count, checksum or times are wrong, for example after a merge by hand) | Repair and check the file, then `mtix import .mtix/tasks.json --recompute-checksum`; to keep the local board, `mtix sync --fix`, which works even when the file does not parse (`mtix sync` without `--fix` then reports the parse error) |
| `unreadable_store` | The local store cannot be exported | `mtix recover`, then import the file |
| `backup_failed` | The backup before the import could not be written | Free disk space; the next command retries |
| `not_imported` | The file changed on disk and was not imported, for example with automatic import off | `mtix import .mtix/tasks.json --mode merge` (the merge decides per task: when the task's content (title, description, prompt, acceptance, labels) matches the file's, your field values win, and otherwise the file's copy wins, undoing your edits to that task; afterwards check `git log -p .mtix/tasks.json` and reapply what it undid), or keep the local board with `mtix sync --fix` |

**Turning it off.** `sync.auto_sync: false` (`mtix config set sync.auto_sync false`) turns automatic import off. `mtix config set` accepts only true or false (in any form Go's `strconv.ParseBool` reads, such as `true`, `1`, `false`, `0`) and rejects anything else. A value written by hand that is neither true nor false leaves automatic import on, the default, with a one-line warning. `.mtix/config.yaml` is tracked in git, so the switch applies to every clone of the project. A store that holds no tasks, such as a fresh clone's, is always imported whatever the switch says, since nothing in it can be lost. With the switch off, writes keep exporting the store as long as `.mtix/tasks.json` is the file mtix last wrote; after a pull, the file is not overwritten (kind `not_imported`) until you import it with `mtix import .mtix/tasks.json --mode merge` or keep your board with `mtix sync --fix`. The merge decides per task: when the task's content (title, description, prompt, acceptance, labels) matches the file's, your field values win, and otherwise the file's copy wins, undoing your edits to that task; afterwards check `git log -p .mtix/tasks.json` and reapply what it undid.

**State.** `mtix sync` shows the `sync.auto_sync` value as configured, whether automatic import is on, and the last automatic import mtix refused or skipped for you to decide, with its time, its reason and whether it is pending, resolved or no longer pending:

```
OUT OF SYNC: tasks.json changed and has not been imported (the last auto-import refusal, below, is pending)
  SQLite:     12 nodes
  tasks.json: 12 nodes
Auto-import: enabled (sync.auto_sync: true)
Last auto-import refusal: 2026-09-24T10:15:00Z (pending): a replace import would delete local data the file lacks: PROJ-1: 2 annotations (...)
```

`mtix sync` never reports `In sync` while an automatic import of the file is pending, or while the file holds an id under another uid than the store (listed under `Another uid in tasks.json`).

`mtix sync --json` reports `in_sync` and those ids as `different_uid`, and the rest under `auto_import`: `enabled`, `setting`, `setting_error`, and `last_refusal` with `refused_at`, `file_hash`, `kind`, `reason`, `loss` (what a replace of the file would delete, when known), `resolved_at` and `pending`. The record is kept in `.mtix/data/auto-import-refusal.json`, next to the other local sync state.

---

## Content Integrity

Every node has a `content_hash` — a SHA-256 hash of its title, description, prompt, and acceptance criteria. This enables tamper detection.

### Verify Integrity

```bash
# Verify all nodes
mtix verify

# Verify a single node
mtix verify PROJ-1.1
```

Output for a full verification:

```
Verified 42 nodes
All content hashes verified OK
```

If tampering is detected:

```
Verified 42 nodes
INTEGRITY FAILURE: 2 nodes with hash mismatches:
  - PROJ-1.1
  - PROJ-3.2.1
```

### JSON Output

```bash
mtix verify --json
```

```json
{
  "total_nodes": 42,
  "verified": true,
  "mismatches": []
}
```

---

## Team collaboration with sync (FR-18)

mtix is solo-first by default — one developer, one machine, one local
SQLite store. When your team grows past one collaborator, the BYO
Postgres **sync hub** replicates events across CLIs without changing
the canonical-store model: every CLI keeps its own local SQLite as
the source of truth; the hub is a mailroom for events.

### When to use sync vs solo

| You are... | Use |
|---|---|
| One developer, one or two machines | Solo. Sync between machines via git-tracked `.mtix/tasks.json`. |
| 2–10 trusted developers | Sync mode with BYO Postgres hub (Supabase, Neon, RDS, or self-hosted). |
| Regulated team (medical, finance, aerospace) | Sync mode + the [safety-critical workflow](docs/audit/MTIX-15-audit-pass2.md) (immutable backups, daemon-as-service, server-side enforcement). |
| Multiple unrelated tenants | Wait for the planned HyperSWE hosted SaaS. The sync hub is not a tenancy boundary. |

### Setup (one teammate, once)

```bash
# Provision a Postgres hub (Supabase / Neon / RDS / self-hosted).
# Configure TLS with a trusted CA. Create a least-privilege role.

# Store the DSN — env var preferred, .mtix/secrets fallback (mode 0600).
export MTIX_SYNC_DSN="postgresql://mtix_sync@hub.example.com:5432/mtix_hub?sslmode=verify-full"

# Initialize the hub (runs migration + registers your project)
mtix sync init
```

**Never commit the DSN to a tracked yaml.** `mtix sync init` scans
`.mtix/config.{yaml,yml,json}` for DSN-shaped keys and refuses to
proceed if any are present (fail-closed).

### Cloud Postgres providers (Neon, Supabase, RDS)

mtix connects over TLS with `sslmode=verify-full` and needs a **session-mode**
connection for the migration path. The two most popular serverless providers
each need one setting; both are verified end-to-end.

**Neon** — use the **direct** endpoint (drop `-pooler` from the host). Neon's
pooled endpoint is transaction-mode, which breaks the session semantics the
migration single-flight relies on. Neon's cert is publicly trusted — no CA
setup needed.

```bash
# direct endpoint (no "-pooler" in the host)
export MTIX_SYNC_DSN="postgresql://<user>:<pw>@ep-xxxx.<region>.aws.neon.tech/<db>?sslmode=verify-full"
```

Neon scales computes to zero after inactivity, and the direct endpoint may
refuse the first connection while suspended. Wake it once (run any query in the
Neon SQL editor, or connect via the pooled endpoint) before `mtix sync init`,
or keep the compute active.

**Supabase** — use the **session pooler** (port **5432**, not the transaction
pooler on 6543). Supabase's certificate chains to its own **private CA**, so
`verify-full` needs that CA: download it from the Supabase dashboard
(Database → SSL) and point `sslrootcert` at it. Skip it and mtix's error tells
you exactly what to set.

```bash
export MTIX_SYNC_SSLROOTCERT="/path/to/supabase-ca.crt"   # or add &sslrootcert=... to the DSN
export MTIX_SYNC_DSN="postgresql://postgres.<ref>:<pw>@aws-0-<region>.pooler.supabase.com:5432/postgres?sslmode=verify-full"
```

**Any provider** — `statement_timeout` is applied per connection via SQL (not a
startup parameter), so it is honored even behind proxies/poolers that drop
startup parameters. `sslmode=require` is rejected for non-loopback hosts; use
`verify-full`, with `sslrootcert` if the provider uses a private CA.

### Setup (every other teammate)

```bash
git clone <repo>
cd <repo>
export MTIX_SYNC_DSN="..."   # same DSN
mtix sync clone              # idempotent
```

`mtix sync clone` pulls the full event log from the hub and replays
it into the local SQLite. Re-running clone is a no-op (per
`applied_events` dedupe). When it completes it sets your pull cursor,
so the next `mtix sync pull` fetches only the changes pushed after the
clone.

### Daily flow

```bash
mtix sync pull               # pull teammate events
# ... normal work via mtix create / update / done ...
mtix sync push               # ship your queue to the hub
mtix sync status             # check pending queue + last push time
```

Install the pre-push hook (`examples/hooks/pre-push`) to automate
`mtix sync push` before `git push`:

```bash
cp examples/hooks/pre-push .git/hooks/pre-push
chmod +x .git/hooks/pre-push
```

### Where pull resumes

`mtix sync pull` remembers the last event it fetched: its sync clock
and its event id (`meta.sync.last_pulled_clock` and
`meta.sync.last_pulled_event_id` in the local database). The next pull
asks the hub for the events after that one, in order of clock and then
event id, so events that carry the same clock are all fetched however
`--limit` splits them into batches. Each batch and the saved position
are written in one transaction: a pull that stops part-way resumes
after the last batch it applied, never after one it did not.

- **First pull after upgrading from 0.5.3 or earlier.** The saved
  position has no event id yet, so the pull fetches the events at the
  saved clock once more, skips the ones you already have and applies
  any that an earlier version missed at that clock.
- **Hub owner, after upgrading.** Run `mtix sync init` once with the
  hub owner's DSN to add the hub index pull reads by; the same item in
  the next section says when to run it and what it costs.

### Late changes from offline teammates

Each `mtix sync pull` first fetches the events past its cursor, then
sweeps the hub for **late events**: changes a teammate pushed after
working offline. Their events carry the lower clock values of their
machine, so the cursor alone skips them. The sweep lists the hub events
created since the previous sweep (with a 15-minute overlap) and notes
the ones your machine does not have; when the listing is done it
fetches them and applies them the usual way, oldest first by the sync
clock, so a task's creation applies before its edits. If the regular
fetch meets an edit of a task whose creation it has not received (the
creation was pushed late with an older clock), the edit is quarantined
(see [Quarantined events](#quarantined-events)), the sweep brings the
creation, and the same pull then applies the edit. A recovered claim or
status change that is older than the task's current one changes
nothing; it is only recorded as received.

- **First pull after upgrading.** It compares the full hub event history
  once and prints `late-event sweep (first run, full hub history): N
  late events recovered`. Changes missed before the upgrade arrive
  then. Later pulls print `late-event sweep: N late events recovered`
  only when they recover something.
- **Interrupted sweeps resume.** On a large hub the full comparison can
  take longer than one pull may run (a daemon pull stops after 60
  seconds). The sweep saves its place after each page it lists, and
  keeps the changes it has noted but not yet applied, so the next pull
  continues where the last one stopped instead of starting over. Until
  it finishes, `mtix sync status` shows `last sweep` as `never (full
  hub comparison in progress)`.
- **Clock.** The sweep uses the hub's clock only, so a wrong clock on
  your machine has no effect.
- **Status.** `mtix sync status` shows `last sweep`, the hub time of the
  last completed sweep (`last_sweep_at` in `--json`), or `never`
  (`full_sweep_in_progress` in `--json` says whether the first full
  comparison is part-way done, and `sweep_pending_events` counts the
  changes noted but not yet applied).
- **Cost.** In the common case (nothing missing, and at most `--limit`
  changes since the last sweep) one extra hub query per pull, and only
  during a pull. A larger window adds a query per further `--limit`
  changes, and recovered changes add a fetch per `--limit` of them. The
  sweep adds no timer, so an idle hub
  that scales to zero stays idle (a daemon that pulls on an interval
  sweeps on each of its pulls).
- **Hub owner, after upgrading.** Run `mtix sync init` once with the
  hub owner's DSN. It adds two indexes on the hub:
  `idx_sync_events_created_at`, so each sweep reads only recent events,
  and `idx_sync_events_lamport_event_id`, so each pull batch reads from
  its saved position. `mtix sync init` runs in one transaction, and
  pushes and pulls both wait until it finishes, longer on a large hub
  while it builds the indexes. A waiting push or pull retries for about
  50 seconds and normally completes once `mtix sync init` finishes; only
  if the wait outlasts that does it fail, and then nothing is lost: run
  it again. Run `mtix sync init` when a pause in sync is acceptable. `mtix sync init` stops after 30 seconds; if building
  the indexes takes longer it changes nothing, and pulls keep working
  without them.
  Until then pulls fetch the same events as with the indexes, only
  slower: each page the sweep lists scans the hub's whole event table
  (once per pull for the usual window, and once per page of the
  one-time full comparison, spread across pulls), and so does each pull
  batch.

### Quarantined events

`mtix sync pull` checks every event it receives before applying it, the
same way the hub checks an event when it is pushed:

- **Size and shape.** A payload of at most 64 KB and at most 10 levels
  of nesting, clocks below 2^53, at most 100 entries in the vector
  clock, and well-formed author, machine and project ids.
- **Clock jump.** The event's Lamport clock may be at most
  `sync.max_lamport_jump` above your local clock (default 4294967296,
  that is 2^32; far above any real team's history). One extreme event
  would otherwise push your clock so high that the hub refuses every
  change you make afterwards.
- **Future timestamps only warn.** An event stamped more than 24 hours
  ahead of your machine's clock is applied, with a `WARN` line on
  stderr; check your machine's clock if you see it.

Each event is applied on its own. An event that fails a check, or whose
apply fails (for example a dependency whose target task has not arrived
yet), is rolled back and kept in the local **quarantine** (the table
`sync_quarantine` in `.mtix/data/mtix.db`) instead of failing the pull;
the pull applies the other events and moves on. The same goes for a hub
row that does not decode. A quarantined event is never lost and never
changes your clock, and an event refused for its clock never moves the
pull's position either, so later changes keep arriving.

- **Retried on every pull.** Each pull retries the quarantine first,
  before it contacts the hub (so this works offline too), and again at
  its end when it applied events. An event whose missing task or
  dependency target has arrived applies then and leaves the quarantine,
  and so does one your store has already applied (for example after
  `mtix sync clone`). Retries are local: they add no hub query, but a pull
  downloads an event refused for its clock again each time.
- **See it.** Pull prints each newly quarantined event on stderr and, at
  the end, `quarantine: N pulled events held, not applied`. `mtix sync
  status` shows `quarantined events` (`quarantined_events` in `--json`).
  `mtix sync doctor` fails its `quarantined events` check while any
  remain.
- **Inspect it** (read-only; it changes nothing and does not contact the
  hub):

  ```bash
  mtix sync quarantine list          # event id, node, op, attempts, first seen, last attempt, reason
  mtix sync quarantine list --json   # the same, plus the Lamport clock, source and mtix version
  ```

  `reason` is why the event was first quarantined; `attempts` counts
  the failed tries; `source` (in `--json`) is `pull` (the regular fetch)
  or `sweep` (the late-event sweep).
- **What to do.** Run `mtix sync pull` again first. An event waiting for
  a task or dependency target that has not been pushed yet applies once
  its teammate pushes it. An event that fails the size, shape or clock
  checks will not apply by itself: report it to whoever runs the hub.
  Raise the bound, `mtix config set sync.max_lamport_jump <positive
  integer>`, only if you know the hub's clocks are legitimately that far
  ahead. Do not delete quarantine rows or edit the database by hand.
- **Rebuilding from the hub deletes local work.** `mtix sync reconcile
  --discard-local --yes` deletes your local tasks and unpushed changes,
  and empties the quarantine with the rest of the local sync state. Its
  dry run (without `--yes`) shows only the node count, not what would be
  lost. Before `--yes`, run `mtix sync push`, check that `mtix sync
  status` shows `pending` 0, and make sure a human has agreed. Then run
  it, and `mtix sync pull`, which quarantines again any event that still
  fails.
- **A quarantined copy of your own event.** If the hub row of an event
  you pushed was changed after the push, pull quarantines that copy and
  it does not clear by itself, even after the hub row is repaired: every
  retry checks the stored copy again. Recover in this order: whoever
  runs the hub repairs the hub row first; run `mtix sync push` and check
  that `pending` is 0; then, with a human's go-ahead, `mtix sync
  reconcile --discard-local --yes` and `mtix sync pull`. Discarding
  before the hub row is repaired drops your own true copy of the event,
  which then comes back only as a quarantined hub event.
- **Clone refuses such events.** `mtix sync clone` has no quarantine: it
  runs the same checks on every hub event before it writes anything, and
  refuses the whole clone, naming the event and the reason, when any
  event fails. On the fresh store, run `mtix sync pull` alone: it
  quarantines the event and applies the rest. Because of the check, a
  clone reads the hub's event log twice.

### Daemon mode (for durability)

Un-pushed events on a lost machine are **not recoverable**. If your
safety profile cannot tolerate that, run the daemon as an OS service so
it pulls continuously (and dispatches this host's hooks — see "Event
dispatch & the daemon"):

```bash
mtix daemon install     # launchd (macOS) / systemd --user (Linux) / Task Scheduler (Windows)
```

The daemon pulls-then-dispatches every 5 seconds. Pair with the
pre-push hook (which handles push) for both inbound and outbound
auto-sync. (`mtix sync daemon` remains as a deprecated alias for one
release.)

### Event dispatch & the daemon (FR-20)

Hook dispatch is **origin-independent**: a hook fires for a journaled
event based only on (a) the event being in the journal and (b) the hook
not yet having fired for it on this host — never on who wrote the event
or how it arrived (local CLI, MCP server, sync-arrival from the hub, or
another process writing the same `.mtix`). A durable per-`(hook, event)`
**dispatch ledger** makes firing exactly-once per host across restarts,
concurrent triggers, and out-of-order arrival.

**Placement is designation.** A hook fires on every host whose
`hooks.yaml` configures it — once per host. Put a wake `exec` only in
the `hooks.yaml` of the host where the worker should run; hosts without
the hook never fire it, no matter who posted the event. (The old
`include-synced` flag is a deprecated no-op; existing configs parse
unchanged, but hooks now fire for sync-arrived events by default — a
hook configured on N hosts fires on N hosts.)

Three triggers share the ledger, so nothing double-fires:

| Trigger | Latency | Covers |
|---|---|---|
| CLI post-command | immediate | your own command's events |
| Server on-commit (`mtix mcp`, `mtix serve`) | immediate | any agent mutation through that server |
| **`mtix daemon`** | ~one tick (5s default) | **everything** — sync-arrived, cross-process, and anything the others missed |

```bash
mtix daemon                 # pull from the hub (if configured) + dispatch, every 5s
mtix daemon --interval 10   # slower cadence
mtix daemon /path/or/DSN    # explicit hub DSN
```

With no hub configured the daemon still runs, tailing the local journal
(cross-process writes into the same `.mtix` keep dispatching). A second
instance exits cleanly (PID lock, shared with the deprecated
`mtix sync daemon` spelling).

**Crash semantics: at-least-once, never a lost wake.** A trigger that
dies between claiming an event and firing it is re-fired after a short
lease. A double wake is possible and harmless — wake scripts must be
idempotent (the shipped template checks the inbox before launching).

**Exec is a detached spawn.** Dispatch returns the moment the command
has *started* — it never blocks a CLI command or an agent's tool call
(the FR-19 async contract). "Delivered" therefore means *spawned*: a
spawn that cannot start (missing script) is a terminal `error`, never
auto-retried; the command's own exit code is the script's to report,
and the fabric's true success signal is the inbox getting acked.
`timeout-seconds` is enforced best-effort by the spawning process —
long-lived daemons enforce it, an ephemeral CLI that exits first
cannot — so scripts should self-bound.

**Exec dispatch policy (host-local, never synced).** Control which
trigger on a host may run exec hooks:

```bash
mtix hooks exec-dispatch            # show: any (default)
mtix hooks exec-dispatch daemon     # only 'mtix daemon' dispatches here —
                                    # every wake goes through the one supervised process
mtix hooks exec-dispatch off        # this host never launches anything
                                    # (inbox/webhook still deliver) — for posting-only
                                    # hosts like an agent sandbox
```

Under `daemon`, CLI and server triggers defer *entirely* (they claim
nothing), so they can never consume a wake the daemon should fire. Like
trust, the mode is stored beside `hooks.yaml` in a local file that never
syncs — placement decisions bind to a machine.

### Running the daemon as a service

The daemon is infrastructure — install it as an OS service so it starts
on boot and restarts on crash:

```bash
mtix daemon install     # launchd (macOS) / systemd --user (Linux) / Task Scheduler (Windows)
mtix daemon status
mtix daemon stop / start
mtix daemon uninstall
```

One service per project (re-running `install` refreshes it). Logs land
under `.mtix/logs/`.

**Upgrading the binary.** Replace it with unlink-then-copy — `install -m
0755 new-mtix <path>`, `rm` + `cp`, or `mv` — **never `cp` over the
existing file**: on macOS an in-place overwrite invalidates the cached
code signature and every exec is killed (`Killed: 9`), which the
service's crash-restart then respawns in a loop. The running daemon
keeps the old binary until restarted; finish the upgrade with
`mtix daemon start`.

Homebrew upgrades are inode-safe (`brew upgrade` installs a new Cellar
directory and re-points the `bin/mtix` symlink), and `mtix daemon
install` registers the upgrade-stable symlink path rather than a
version-scoped Cellar path — so after `brew upgrade mtix`, just run
`mtix daemon start` to restart the service onto the new version.

### Topology: one local `.mtix` per machine + the hub

Each machine keeps its **own local `.mtix`**; events replicate through
the Postgres hub. **Never share one `.mtix` across machines or sandboxes
via a network/FUSE mount** — SQLite's WAL shared memory cannot be
coordinated across hosts and the store *will* corrupt (mtix refuses
such filesystems at open). Origin-independent dispatch is exactly what
makes the shared mount unnecessary: post anywhere, sync, and the worker
host's daemon fires the wake locally.

### Waking agents: delivery that terminates in the prompt

LLM agents act only on what is in their context — a queue an agent must
remember to poll will not get checked. Every delivery rung therefore
ends with event content in the agent's prompt:

| Rung | Mechanism | Covers |
|---|---|---|
| 1. **Cold-start wake** | `exec` hook runs a wake script that launches the harness CLI **with the inbox as the prompt** (`mtix inbox --agent X --format prompt`) | agent not running — works for any runtime |
| 2. **Context injection** | a harness hook (session-start / prompt-submit) shells `mtix inbox --agent X --format context` | interactive sessions, next human prompt |
| 3. **Background watcher** | the agent arms `mtix inbox --wait --timeout 3600` as a harness background task; its exit re-invokes the agent, which handles, acks, re-arms | idle-but-alive session, no push mechanism |
| 4. **Channel push** | `mtix mcp --channel-agent X` (Claude Code channels, research preview) pushes events into the running session | idle or busy live session |

Rung 1 is the guaranteed backstop; rungs 2–4 are latency optimizations
and are allowed to fail — the durable inbox plus the daemon's exec wake
covers every gap. The reference wake script is
`examples/hooks/wake-agent.sh`:

```yaml
hooks:
  - name: wake-developer
    match: { events: [comment.addressed], to-agent: developer }
    deliver: [exec]
    exec:
      command: ["/path/to/wake-agent.sh", "developer"]
      timeout-seconds: 30
```

Review and trust the config on that host (`mtix hooks trust`). The
script exits without launching when the inbox is empty (the idempotency
check under at-least-once dispatch) and otherwise launches the harness
CLI with the payload — `claude -p "$PAYLOAD"`, `codex exec "$PAYLOAD"`,
`agent -p "$PAYLOAD"` (Cursor), or any runtime that takes a prompt.

Per-harness support today:

| Harness | Cold-start (rung 1) | MCP tools | Context injection (rung 2) | Push (rung 4) |
|---|---|---|---|---|
| Claude Code | `claude -p` | ✅ | ✅ hooks | ✅ channels (preview) |
| OpenAI Codex CLI | `codex exec` | ✅ | AGENTS.md convention | ✗ (no push mechanism yet) |
| Cursor CLI | `agent -p` | ✅ | ✅ hooks | ✗ |
| any prompt-taking CLI | ✅ | if MCP-capable | varies | ✗ |

**Channel mode (Claude Code, research preview).** `mtix mcp
--channel-agent developer` makes the same MCP server that serves the
mtix tools also push the developer's new inbox events into the running
session as `<channel source="mtix">` events; the agent handles, acks
(`mtix_inbox_ack`), and replies (`mtix_annotate` with `to`) through the
very same server. Launch the session with `--channels` (during the
preview, a custom channel needs `--dangerously-load-development-channels`
or an org allowlist; requires Anthropic auth and Claude Code ≥ 2.1.80).
The channel server also holds an mtix session for the agent while it
runs, so wake scripts can prefer pushing to a live session over
cold-starting a duplicate.

**Security notes.** `exec` runs only for a `hooks.yaml` the local
operator trusted by content hash (`mtix hooks trust`) — a synced or
edited config never runs code unbidden. Addressed comments are **prompt
input**: with push and cold-start wakes, any agent that can write to
your hub puts text directly into another agent's context — only
federate the hub with agents and humans you trust, exactly as you would
a shared terminal.

### Conflict handling

When two teammates edit the same field concurrently, Last-Write-Wins
at apply time deterministically picks a winner (keyed by
`lamport_clock` → `wall_clock_ts` → `author_machine_hash`). Replicas
always converge.

Claims and status changes (`mtix claim`, `unclaim`, `done`, `cancel`,
`defer`, `reopen` and the other transitions) are resolved per task
the same way: after a sync, every machine shows the status set by the
most recent claim or status change (highest `lamport_clock`, ties
broken by event id), and an older one that arrives later changes
nothing. This holds for claims and status changes, which travel as
events. Two automatic changes do not travel as events, so status can
still differ there: if one machine claims a task while another adds a
dependency that blocks it, the task can end up blocked on one machine
and in progress on the other, and tasks cancelled as descendants of a
cascade cancel can differ the same way. Beyond status nothing is
guaranteed to agree. The assignee and the other fields that go with it
can still differ between machines when changes arrive out of order: if
two agents claim the same task between
syncs and the agent whose claim lost marks it done before pulling,
both machines show the task done, each with its own agent as the
assignee. After `mtix sync pull`, check the task with `mtix show <id>`
before carrying on with claimed work, and stop if it is no longer in
progress under your name. These contests are resolved silently: they
are not listed by `mtix sync conflicts list`.

The hub also records contested edits in `sync_conflicts` for audit
visibility:

```bash
mtix sync conflicts list              # inspect what is contested
mtix sync conflicts resolve <id> --keep-local
mtix sync conflicts resolve <id> --keep-remote
```

For whole-project escapes (many conflicts, divergent history):

```bash
mtix sync reconcile --dry-run
mtix sync reconcile --discard-local            # accept hub state
mtix sync reconcile --rename-to NEWPREFIX      # republish under fresh prefix
mtix sync reconcile --import-as PARENT-ID      # graft local nodes under a hub node
```

**Audit-trail note:** when two CLIs share `authorID="cli"` (the
default), their concurrent edits produce vector clocks that are
`Equal()` rather than `Concurrent()`. LWW still resolves the
contention deterministically, but the hub does NOT record a
`sync_conflicts` row in this case. If audit-trail completeness
matters for your compliance profile, give each agent a distinct
identity: set `MTIX_AUTHOR_ID` in the agent's environment (set once
per process — `export MTIX_AUTHOR_ID=agent-2`), or the `author_id`
key in `.mtix/config.yaml` for a per-project default (the env var
wins). Both must match `^[a-z0-9_-]{1,64}$`. With distinct
identities, concurrent edits are `Concurrent()` and are recorded in
`sync_conflicts`. See [docs/SECURITY-MODEL.md](docs/SECURITY-MODEL.md)
for the full tradeoff.

### Hub health checks

```bash
mtix sync doctor             # 6 health checks: PG reachable, schema current,
                             #   queue draining, no orphan applied,
                             #   quarantined events, secrets file mode
```

Exit code 0 on all-pass; exit code 2 if any check fails (operators
can gate CI / monitoring on this).

### Backup and restore

```bash
mtix sync backup --output /tmp/hub.sql      # wraps pg_dump on 5 mtix tables
psql "$DSN" < /tmp/hub.sql                  # restore
```

For compliance-grade durability, schedule the backup to immutable
cold storage (S3 Object Lock, GCS retention, Azure Immutable). See
the [safety-critical
workflow](docs/audit/MTIX-15-audit-pass2.md) for the full procedure.

### Lost-laptop recovery

Procedure when a CLI machine is lost:

1. On every surviving CLI, run `mtix sync status`. Pending count of 0
   means all in-flight events are on the hub.
2. The lost machine's pending events (if any) are unrecoverable.
3. Provision the replacement machine. Run `mtix sync clone DSN` to
   rebuild local state from the hub event log. Replay is idempotent.

The `mtix_sync_workflow` MCP tool surfaces hub-unreachable conditions
(`meta.sync.consecutive_errors ≥ 3`) so agents notice before the
operator does.

### Repairing workflow state after an older pull

Before 0.5.4, `mtix sync pull` could re-apply events this machine had
already pushed, and it applied claims and status changes in the order
they arrived. A node could therefore be left in an older workflow
state: claim, push, `mtix done` and pull left it `in_progress` with
`closed_at` still set. 0.5.4 no longer does this, but upgrading does
not change nodes that were already reverted. `mtix sync repair
--status` re-derives each node's workflow state from the local sync
event log, with the rule a pull applies, and lists or repairs the
nodes that differ. It reads and writes only the local database and
never contacts the hub.

```bash
mtix sync pull                             # first: bring in every teammate's change
mtix sync repair --status                  # dry run: list the differences, write nothing
mtix sync repair --status --json           # the same list as JSON
mtix sync pull                             # again, just before applying
mtix sync repair --status --apply          # back up, then repair every node not flagged
mtix sync repair --status --apply --force  # also repair flagged nodes, after review
mtix sync push                             # send the repair events
```

**Pull first, and again before `--apply`.** Run `mtix sync pull` before
listing and again just before `--apply`, and apply only what the list
still shows after that pull. A repair that changes the status emits an
event stamped with this machine's newest Lamport clock. On a local log
that lacks a teammate's newer, unpulled change, that event can win over
the change (whenever this machine's Lamport clock is ahead of it), and
every machine that pulls it then reverts the teammate's change. (A 0.5.4
pull no longer replays this machine's own events, so pulling is safe.)
When the dry run lists differences, it ends with a reminder to pull
first; with `--json` the reminder is the `reminder` field, which is
omitted when there is no reminder.

**What is compared.** For each node the winner is its newest
well-formed claim, unclaim, defer or status-change event: the highest
Lamport clock wins, a tie goes to the higher event id, and a malformed
event never counts. The derived state is what that event writes when a
pull applies it. The command compares status, assignee, agent_state,
whether `closed_at` is set, and, for a node without children, progress
(a done node without children has progress 1.0), each only where the
winning event writes it. For an event this machine made, `closed_at`
follows this machine's own history: its last `done` or `cancel` sets
it and a reopen clears it.

**Replay, derived fix or flagged.** Each listed node shows its winning
event, when it was made, whether this machine or another machine made
it, and one of three reasons:

- *replay*: the stored state is what an older event of the node
  writes, which is what a replayed pull left, and nothing recorded
  after the winning event explains it. The older event is shown as the
  replayed event. `--apply` repairs it.
- *derived fields*: the status matches the winning event, and only
  `closed_at` or the progress of a node without children is out of
  date. `--apply` repairs it.
- *FLAGGED, not a replay; review*: any other difference. The stored
  state is what no older event writes; or the node's activity records
  a status change after the winning event that explains it; or only
  the assignee or agent_state differs, which `mtix update --assignee`
  can change without leaving any trace; or the node is cancelled and
  an ancestor was cancelled after its winning event, so a cascade
  cancel may have cancelled it. Such a state can be newer than the
  event log, for example after importing a teammate's
  `.mtix/tasks.json`, and repairing it would revert the teammate's
  change on every machine. `--apply` skips it; `--apply --force`
  repairs it. Check the node with `mtix show <id>` first.

The check compares times recorded on different machines, so clocks
that differ can mislead it in both directions. When this machine's
clock is behind a teammate's, a genuine replay can be flagged; review
it, then use `--force`. When this machine's clock is ahead, a
teammate's newer state (for example imported from `.mtix/tasks.json`)
can look older than this machine's events and be listed as a replay,
and `--apply` would revert it. Before `--apply`, check each replay's
winner time and whether this machine or another machine made it, and
look at the node with `mtix show <id>` when in doubt.

**What is left alone:**

- a node without claim, unclaim, defer or status-change events;
- a blocked node that still has an unresolved blocker, since a block
  added by a new dependency is not synced as an event;
- a node cancelled by `mtix cancel --cascade` on an ancestor, which has
  no cancel event of its own (one that has is flagged, see above);
- an assignee set after the winning event with `mtix update
  --assignee` (when it arrives by import, the difference is flagged,
  so `--apply` without `--force` leaves it alone);
- the wake time of this machine's own deferral (`mtix defer --until`),
  which the event does not carry;
- `closed_at` after this machine invalidated or restored the node,
  which leaves `closed_at` as it was;
- the exact `closed_at` time: only whether it is set is compared.

**What `--apply` does.** When there is something to repair, it first
writes a verified copy of the database to
`.mtix/data/backups/pre-repair-status-<UTC time>.db` (for example
`pre-repair-status-20260925T101500Z.db`, with a `-2`, `-3` suffix when
that name is taken) and stops without changing anything if it cannot,
for example on a full disk. Then, for each node in its own
transaction, it re-checks the node, writes the derived state, adds an
activity entry with the text `sync repair` that names the winning
event, and recomputes the parent's progress. When the status changes
it also emits one status-change event with the reason `sync repair`
and unblocks dependents; a repair that leaves the status alone
(`closed_at` or progress, or with `--force` the assignee or
agent_state) emits nothing.
`.mtix/tasks.json` is re-exported when a node was repaired. A second
run lists nothing.

The repair event carries the time of the winning event, so a machine
that applies it at its next pull stamps the same `closed_at` it already
had. When that time is in the future (a clock that was ahead), the
event carries the current time instead, because `mtix sync push`
refuses an event stamped more than a day ahead, and a refused event
would stop every later push. Like any status change, it fires `status.changed`
hooks on this machine and on every machine that pulls it.

**Undoing a repair.** Before `mtix sync push`, stop every mtix process
and copy the backup over `.mtix/data/mtix.db`. After the push a restore
does not undo it, because the next pull brings the repair events back;
change the node's status with the normal commands instead.

### Troubleshooting sync

| Symptom | Diagnosis | Action |
|---|---|---|
| `mtix sync push` errors with "connection refused" | Hub unreachable | Retry; check `mtix sync doctor` |
| `mtix sync push` errors with "tls handshake timeout" | TLS misconfiguration | Verify `MTIX_SYNC_SSLROOTCERT` if managed-PG requires it |
| `ErrSyncDivergentHistory` on `mtix sync init` | Hub already has a different lineage for this prefix | Run `mtix sync clone` to join, OR `mtix sync reconcile --import-as PARENT-ID` |
| `ErrSyncQueueFull` from `mtix create` / `update` | Local pending queue at the cap | `mtix sync push --force`, or raise `sync.max_queue_size` |
| `mtix sync status` shows pending count climbing | Daemon not running or hub unreachable | `systemctl status mtix-sync`; `mtix sync doctor` |
| A teammate's change is missing after `mtix sync pull` | They have not pushed yet, the late-event sweep failed (the pull reports the error), or the change is quarantined | Ask them to run `mtix sync push`, then pull again; `mtix sync status` shows `last sweep` and `quarantined events` |
| `mtix sync doctor` fails `quarantined events` | Pulled events failed their checks or their apply and are held, not applied | Run `mtix sync pull` (it retries them); if they remain, list them with `mtix sync quarantine list` (see [Quarantined events](#quarantined-events)) and report the reasons to whoever runs the hub |
| A node shows an older state than its history after a pull on a client older than 0.5.4 (for example `in_progress` after `mtix done`) | That pull replayed an older event of this machine | Upgrade, run `mtix sync pull`, then `mtix sync repair --status` and review the list; run `mtix sync pull` again, then `mtix sync repair --status --apply` and `mtix sync push`; a flagged node needs review and `--force` (see above). Pulling first matters: a repair made on a stale log can revert a teammate's newer change on every machine |

### MCP integration

The `mtix_sync_workflow` MCP tool exposes structured sync-state
recommendations to LLM agents. State buckets: `solo`,
`sync-configured-no-hub`, `sync-active`, `divergent-state-pending`,
`hub-unreachable`. Output is bounded to 4 KB. The DSN is never
returned. See [docs/MCP-SETUP.md](docs/MCP-SETUP.md).

---

## Distributed identity & team sync

mtix IDs are dot-paths (`PRJX-1.4`, `PRJX-1.4.3`). They stay clean and
human-readable even when a team creates nodes concurrently or offline.
This works because each node also has a stable internal identity (its
`uid`) that the surface never shows. You only ever type and read the
dot-path. For the full design see
[ADR-003](../ADR-003-DISTRIBUTED-NODE-IDENTITY.md) and
[docs/SYNC-DESIGN.md](docs/SYNC-DESIGN.md).

### Provisional vs settled IDs

A **settled** ID is fully numeric, like `PRJX-1.4`. It is safe to put in
a commit, a PR, or any external reference.

A **provisional** ID carries a long uid-shaped segment, like
`PRJX-1.1.0193fa8c...`. It means the node was created offline or before
the hub confirmed its number. It is valid and resolvable, but its final
number will differ.

Rules of thumb:

- Read and reference nodes by dot-path. You never need the uid.
- Do not paste a provisional ID into a commit or PR. mtix warns you when
  you are about to externalize one.
- A fully numeric ID is safe to externalize.

### Offline creation auto-settles

Create nodes with no network. They get a provisional ID. On your next
`mtix sync push`/`pull` they claim a clean number from the hub and settle
automatically. No command to run. If the claim still cannot reach the
hub, the node stays provisional and retries on the next sync.

### Concurrent creates auto-renumber

If two teammates both create `PRJX-1.4` at the same time, the hub accepts
the first to arrive and tells the second to renumber. The second node
becomes `PRJX-1.5` on its own. Both nodes survive; nothing is lost; you
do not have to do anything. This is normal operation and needs no admin.

### New sync commands

```bash
mtix sync mark-restored          # operator: open a restore window (see runbook)
mtix sync collisions list        # list restore collisions awaiting a decision
mtix sync collisions resolve <id> --winner held|incoming
mtix sync migrate                # drive the node-identity migration phases
```

`mtix sync migrate` runs the one-time migration to the distributed-identity
model (uid backfill, hub dedup sweep, version-gated registry index). It is
idempotent and a no-op once complete. Phase 1 only moves numbers when the
hub already has duplicates, and only with `--yes`; without `--yes` it
previews and changes nothing.

### Restore-from-backup runbook

Restoring the hub from a backup can, in rare cases, hand the same number
to two already-settled nodes (one number granted after the backup, then
lost by the restore). mtix never auto-picks a winner, because the node
that should keep the number may have external references. That is a human
call, so mtix blocks just the affected node and asks an admin.

Run this after every hub restore:

```bash
# 1. Restore the hub from your backup.
psql "$DSN" < /path/to/hub-backup.sql

# 2. Open a restore window. Run this EXACTLY ONCE, right after the restore.
#    This is the only way to arm restore-collision detection. Clients
#    cannot do it; only the operator can.
mtix sync mark-restored

# 3. Let teammates reconnect and push as normal.

# 4. Check for settled-vs-settled collisions.
mtix sync collisions list
```

If `collisions list` is empty, you are done — the team's normal sync
self-healed everything. If it shows a collision, each row surfaces both
contesting nodes and their signals (uids, claim timestamps). The older
claim is shown as a hint only; it is client-asserted and partly lost on a
restore, so mtix never acts on it for you.

Resolve each one:

```bash
mtix sync collisions resolve <id> --winner held       # keep the node already on the hub
# or
mtix sync collisions resolve <id> --winner incoming   # keep the blocked (queued) node
```

The loser renumbers to the next free number. No node is ever deleted. The
moved node may have external references (a commit, a PR) that need
updating — `collisions resolve` reports the old and new number so you can
fix them.

While a collision is open, only that one node is blocked. Every other
node keeps syncing normally; one unresolved collision never wedges the
team's sync stream.

---

## Troubleshooting

### Exit codes

mtix exits with structured codes so scripts and agents can react without parsing error text:

| Code | Meaning |
|---|---|
| 0 | Success |
| 1 | Generic error |
| 3 | Disk full — a write or backup was refused or failed because the volume is out of space; free space and retry |
| 4 | Database corrupted — an integrity gate failed at open; see "Disk full and corruption recovery" |
| 5 | Inbox empty — `mtix inbox --wait` timed out with no addressed events; a worker's poll loop treats this as "nothing yet, loop again" (distinct from 0 = woke with work). Only `--wait` returns this; a plain `mtix inbox` list exits 0 even when empty. Note: a harness-hosted (Claude) agent cannot stay parked *between* turns — a dormant session runs no poller, so `mtix_inbox_wait` only watches the inbox *within* a live turn. To wake a dormant agent when work lands, cold-start it with an exec wake hook on the worker host (see "Waking agents: delivery that terminates in the prompt"), or use channel push / a background watcher for a live session |

### Common Issues

**"not in an mtix project"**
Run `mtix init --prefix PREFIX` to initialize a project in the current directory, or navigate to a directory containing a `.mtix/` folder.

**Node stuck in blocked status**
Check what's blocking it with `mtix dep show <id>`. Resolve the blocking node or remove the dependency with `mtix dep remove`.

**Agent appears stale**
The agent hasn't sent a heartbeat within the threshold. Check if the agent process is running. Adjust the threshold with `mtix config set agent.stale_threshold <duration>`.

**Progress not updating**
Progress rolls up automatically when child states change. Check that child nodes have the expected status with `mtix tree <parent-id>`. Cancelled and invalidated children are excluded from calculations.

**Cannot create child under done/cancelled node**
Terminal states (`done`, `cancelled`, `invalidated`) block child creation. Reopen the parent first with `mtix reopen <id>`.

### Filesystem requirements (local disk only)

**`.mtix` must live on a real local disk to be WRITABLE.** mtix's SQLite store
uses WAL mode, which requires true shared-memory (the `-shm` wal-index) and
faithful POSIX file locking across every process that writes the database.
**FUSE-passthrough and network filesystems do not provide these** — even when
they report success — so concurrent *writes* from multiple processes/agents can
silently corrupt the database. This was the failure mode behind the 2026-07-11
and 2026-07-12 incidents (both were writes on a FUSE mount).

At startup mtix classifies the filesystem holding `.mtix`. On a
positively-identified FUSE or network mount (NFS, SMB/CIFS, 9p, sshfs, rclone,
gocryptfs, s3fs, …) it **opens READ-ONLY and refuses every write — with no
override.** Reads, `mtix recover`, and `mtix export` still work; a write returns:

```
error: writes refused: mtix database is on a filesystem unsafe for SQLite
("macfuse", at …/.mtix/data/mtix.db) — SQLite cannot write there safely …
to WRITE, move .mtix to a local disk or use the sync hub. No environment
override enables writes here
```

mtix also catches the **host-side blind spot**: a directory can report *local* to
the host while a sandbox mounts it over FUSE. libfuse leaves `.fuse_hidden*`
orphans in the directory when it unlinks an open file, so mtix treats their
presence as cross-context FUSE access and **refuses writes too** — even though
the filesystem type looks local. If you have genuinely moved off a shared mount,
remove the stale `.mtix/data/.fuse_hidden*` files.

A warning that could be overridden turned out to be a scheduled incident (both
corruptions were `MTIX_ALLOW_UNSAFE_FS` override writes), so **the write override
is retired**: `MTIX_ALLOW_UNSAFE_FS` is now ignored (with a deprecation warning)
and never enables a write.

What to do:

- **Move `.mtix` to a local disk.** The most common trap is a container/VM shared
  folder (Docker Desktop, virtiofs, gVisor) that surfaces a host directory into a
  sandbox over FUSE. Put `.mtix` on the sandbox's own local filesystem instead.
- **Sandboxed multi-agent setup?** Give **each machine/sandbox its own local
  `.mtix`** and share state through the sync hub (`mtix sync`, FR-18). That is the
  supported multi-writer topology — not one SQLite file on a shared folder.
- **Need the data off an unsafe mount?** It's still readable: `mtix recover` /
  `mtix export` open read-only, so you can salvage or migrate it to a local disk.

### Disk full and corruption recovery

mtix refuses work it cannot finish safely (NFR-2.8). On a nearly full volume you will see errors like `refusing write: only N bytes free…` — **this is deliberate**: a write that fails halfway through a checkpoint can tear the database. Reads keep working. Free disk space and retry; the floor is 8 MiB by default and can be tuned with the `MTIX_MIN_FREE_BYTES` environment variable (`0` disables the check — not recommended).

If a write fails at the filesystem level anyway (`disk is full`, `disk I/O error`), mtix latches into **fail-stop** for that process: all further writes are refused so a bad situation cannot get worse. Free space (or fix the disk) and start fresh.

If mtix reports `database … is truncated` or `integrity check … failed` at startup:

1. **Stop. Do not delete anything** — especially not `mtix.db-wal` or `mtix.db-shm`; the WAL may be the only intact copy of your latest commits.
2. Copy the entire `.mtix/data/` directory to another volume as evidence.
3. Run `mtix recover`. It reads the damaged database read-only — salvaging every readable row individually — fills gaps from the `.mtix/tasks.json` mirror, synthesizes placeholders for lost parents, and writes an importable export to `.mtix/recovered-<timestamp>.json` together with a salvage report (recovered / from-mirror / lost IDs). It never modifies the damaged files.
4. Review the report, then restore: `mtix import --mode replace .mtix/recovered-<timestamp>.json` into a fresh `mtix init` project (or this one, after moving the damaged `data/` aside). Alternatively restore a `mtix backup` snapshot if you have a newer one.
5. For hand-reconstructed export files (e.g., rebuilt from session transcripts), `mtix import --recompute-checksum` replaces the stale integrity checksum — loudly — so the standard import accepts them.
6. `MTIX_SKIP_INTEGRITY_CHECK=1` bypasses the open-time integrity gates (both the truncation check and quick_check) so `mtix verify`, `mtix backup`, and `mtix export` can reach a damaged file during recovery. mtix logs a loud DANGER line while it is set. Never use it to keep writing.

To make machine-loss recovery trivial, commit `.mtix/tasks.json` to git — it is deterministic, human-readable, and importable.

**Automatic rolling backups.** mtix also keeps verified snapshots under `.mtix/data/backups/` without any setup: after mutations, if the newest backup is older than 24 hours, a new one is taken and the newest 7 are kept. Tune with `MTIX_BACKUP_INTERVAL` (Go duration, `0` disables) and `MTIX_BACKUP_RETAIN`. Restore one with `mtix import` after exporting it, or by copying it over `data/mtix.db` while no mtix process runs.

### Garbage Collection

Clean up expired soft-deleted nodes:

```bash
mtix gc
```

This removes nodes that were soft-deleted longer than the retention period ago.

### Database Migration

The database schema is auto-migrated on startup. If needed:

```bash
mtix migrate
```

### Regenerate Documentation

```bash
mtix docs generate --force
```

Regenerates the agent-facing documentation in the `.mtix/docs/` directory.
