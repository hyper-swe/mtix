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
# Defer indefinitely
mtix defer PROJ-1.3

# Defer until a specific time
mtix defer PROJ-1.3 --until "2026-04-01T00:00:00Z"
```

When the `--until` time passes, the node becomes eligible for pickup again.

### Cancel (Descope)

```bash
# Cancel a single node
mtix cancel PROJ-1.3 --reason "Out of scope for v1"

# Cancel a node and all its descendants
mtix cancel PROJ-1 --reason "Entire feature dropped" --cascade
```

A reason is required.

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

The export includes nodes, dependencies, agents, sessions, and a SHA-256 checksum for integrity verification.

### Import

Import data from a JSON export:

```bash
# Merge mode (default) — skip unchanged nodes, update modified ones
mtix import project-data.json

# Replace mode — drop existing data and reimport
mtix import project-data.json --mode replace
```

Import validates:
- Node count matches the `node_count` field
- SHA-256 checksum over canonical JSON
- FTS5 index is rebuilt after import
- Sequence counters are reconstructed

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
`applied_events` dedupe).

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

### Daemon mode (for durability)

Un-pushed events on a lost machine are **not recoverable**. If your
safety profile cannot tolerate that, run the daemon as a systemd or
launchd service:

```bash
mtix sync daemon --install | sudo tee /etc/systemd/system/mtix-sync.service
sudo systemctl enable --now mtix-sync
```

The daemon runs `mtix sync pull` every 30 seconds by default. Pair
with the pre-push hook (which handles push) for both inbound and
outbound auto-sync.

### Designated hook dispatch (multi-machine wake)

Event hooks (`.mtix/hooks.yaml`) fire on the machine where the
triggering command ran. For a **local** event that is exactly right —
the wake action runs next to the worker. But an event that arrived
from **another machine** (a synced event) is skipped by default, so a
hook whose action must run on a specific host — e.g. an `exec` that
launches a worker — would never fire for work posted elsewhere.

Designate **one** host to act on synced events by running its daemon
with `--dispatch-hooks`:

```bash
mtix sync daemon --dispatch-hooks
```

After each pull, that host fires hooks marked `include-synced: true`
on the events it just received:

```yaml
hooks:
  - name: wake-worker-on-done
    match: { events: [status.changed], status-to: [done], to-agent: opus }
    include-synced: true      # act on events from other machines too
    deliver: [exec]
```

Run `--dispatch-hooks` on exactly **one** host so a synced event fires
once team-wide (a separate cursor makes it exactly-once even across
daemon restarts). Every other host — and all normal CLI commands —
still fire hooks only for their own local events, so there is no
duplicate firing. `exec` trust is local, so trust `hooks.yaml` on the
designated host (`mtix hooks trust`).

### Conflict handling

When two teammates edit the same field concurrently, Last-Write-Wins
at apply time deterministically picks a winner (keyed by
`lamport_clock` → `wall_clock_ts` → `author_machine_hash`). Replicas
always converge.

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
matters for your compliance profile, set distinct authorIDs per
agent. See [docs/SECURITY-MODEL.md](docs/SECURITY-MODEL.md) for the
full tradeoff.

### Hub health checks

```bash
mtix sync doctor             # 5 health checks: PG reachable, schema current,
                             #   queue draining, no orphan applied,
                             #   secrets file mode
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

### Troubleshooting sync

| Symptom | Diagnosis | Action |
|---|---|---|
| `mtix sync push` errors with "connection refused" | Hub unreachable | Retry; check `mtix sync doctor` |
| `mtix sync push` errors with "tls handshake timeout" | TLS misconfiguration | Verify `MTIX_SYNC_SSLROOTCERT` if managed-PG requires it |
| `ErrSyncDivergentHistory` on `mtix sync init` | Hub already has a different lineage for this prefix | Run `mtix sync clone` to join, OR `mtix sync reconcile --import-as PARENT-ID` |
| `ErrSyncQueueFull` from `mtix create` / `update` | Local pending queue at the cap | `mtix sync push --force`, or raise `sync.max_queue_size` |
| `mtix sync status` shows pending count climbing | Daemon not running or hub unreachable | `systemctl status mtix-sync`; `mtix sync doctor` |

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
| 5 | Inbox empty — `mtix inbox --wait` timed out with no addressed events; a worker's poll loop treats this as "nothing yet, loop again" (distinct from 0 = woke with work). Only `--wait` returns this; a plain `mtix inbox` list exits 0 even when empty. Harness-hosted (Claude) agents should park via the `mtix_inbox_wait` MCP tool instead of a backgrounded CLI `--wait` |

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
