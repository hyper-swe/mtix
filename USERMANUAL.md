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
13. [Configuration](#configuration)
14. [Web UI](#web-ui)
15. [REST API](#rest-api)
16. [gRPC API](#grpc-api)
17. [MCP Tools](#mcp-tools)
18. [Backup, Export, and Import](#backup-export-and-import)
19. [Content Integrity](#content-integrity)
20. [Troubleshooting](#troubleshooting)

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

### Tool Categories

**Node Management:** `mtix_create`, `mtix_show`, `mtix_list`, `mtix_update`, `mtix_delete`, `mtix_undelete`, `mtix_decompose`

**Workflow:** `mtix_claim`, `mtix_unclaim`, `mtix_done`, `mtix_defer`, `mtix_cancel`, `mtix_reopen`, `mtix_ready`, `mtix_blocked`, `mtix_search`, `mtix_rerun`

**Context:** `mtix_context`, `mtix_prompt`, `mtix_annotate`, `mtix_resolve_annotation`

**Dependencies:** `mtix_dep_add`, `mtix_dep_remove`, `mtix_dep_show`

**Sessions:** `mtix_session_start`, `mtix_session_end`, `mtix_session_summary`, `mtix_agent_heartbeat`, `mtix_agent_state`, `mtix_agent_work`

**Analytics:** `mtix_stats`, `mtix_progress`, `mtix_stale`, `mtix_orphans`

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

## Troubleshooting

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
