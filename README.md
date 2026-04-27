# mtix

**Multi-agent micro issue manager for code-generating LLMs.**

mtix (micro-tix) is a hierarchical task management system where multiple LLM coding agents decompose, claim, and execute work concurrently. Work breaks down into infinitely nested micro issues using dot-notation IDs (`PROJ-42.1.3.2.1`), where the hierarchy itself becomes the agent's briefing — each level adds context that flows down to the executing agent. Every operation is available through CLI, REST, gRPC, MCP, and a real-time web UI — agents and humans use whichever interface fits.

## Why mtix

**The hierarchy is the briefing.** When an agent picks up `PROJ-42.1.3`, it traverses the parent chain to assemble a complete prompt — business goal from the epic, user story from the feature scope, exact instructions from the issue. No separate documentation, no context windows stuffed with irrelevant files. The tree structure carries the intent.

**Agents decompose, not templates.** A decomposing agent reads the parent's full context and produces children with problem-specific prompts, acceptance criteria, and test specifications. The structure emerges from the problem's complexity — a one-liner gets one subtask, a cross-cutting refactor gets twelve. No predefined shapes imposed.

**Multi-agent orchestration, not just assignment.** Agents register, claim nodes, send heartbeats, and run sessions. mtix tracks who is working on what, detects stalled agents, prevents double-claiming, and auto-recovers from crashes. When ten agents work in parallel, node state and agent state stay consistent.

**Every state change is auditable.** Each transition records who, when, and why — in the same transaction as the data write. An auditor can trace any node's full lifecycle from creation to completion. Built for environments where `DO-178C`, `IEC 62304`, and `NASA-STD-8739.8` apply.

**Single binary, zero infrastructure.** Pure Go, embedded SQLite in WAL mode, embedded web UI. No database server, no container orchestration, no runtime dependencies. Copy one file, run it.

## Features

- **Infinite hierarchy** — Epics, stories, issues, micro issues, and beyond. No depth limit.
- **Dot-notation IDs** — `PROJ-42.1.3.2` encodes the full parent-child path. No UUIDs needed.
- **7-state machine** — `open` / `in_progress` / `blocked` / `done` / `deferred` / `cancelled` / `invalidated` with enforced transitions.
- **Automatic progress rollup** — Child completions propagate to root in a single transaction.
- **Dependency tracking** — Cross-branch `blocks`, `related`, `duplicates`, `discovered_from` with cycle detection.
- **Prompt chain propagation** — Parent prompts cascade to children for LLM context assembly.
- **Multi-agent orchestration** — Agent state tracking, sessions, heartbeats, stale detection.
- **Agent-native query** — Multi-value filters (`--under A,B --status done,cancelled --type issue`), `--format briefing` for paste-into-context output, `--fields` for JSON projection. No post-processing stub code needed.
- **CLI-first** — Every operation available via `mtix` CLI with `--json` for machine consumption.
- **REST API** — Full CRUD, query, and admin endpoints with CSRF protection.
- **gRPC API** — Protocol Buffers interface for high-performance integrations.
- **MCP (Model Context Protocol)** — Native tool registration for LLM agent frameworks.
- **Web UI** — Linear-inspired SPA with keyboard shortcuts (Cmd+K, j/k, c, x), create modal, expandable tree, real-time WebSocket updates.
- **Single binary** — Pure Go, no CGO, embedded SQLite (WAL mode), embedded web UI.
- **Export/Import** — JSON export with checksums, merge and replace import modes.
- **Content integrity** — SHA256 content hashes on every node, full-project verification.

## Why Dot-Notation? The Context Chain

The dot-notation hierarchy isn't just an ID scheme — it's a **context chain**. Each level encodes a layer of context that flows down to executing agents:

```
PROJ-1           "Build user authentication"              ← business goal
PROJ-1.3         "Implement rate limiting for auth"        ← technical scope
PROJ-1.3.2       "Add sliding window counter in window.go" ← exact instruction
```

When an agent calls `mtix context PROJ-1.3.2`, it receives an **assembled prompt** — a single document built by traversing root to leaf, combining every ancestor's description and prompt. The agent gets the full picture: why the work exists, what constraints apply, and exactly what to implement.

This is mtix's core design: **the hierarchy IS the briefing.** Parents provide the "why" and scope. Children provide the "what" — file paths, function names, test cases, edge cases. An LLM agent that reads the assembled context has everything it needs to execute without asking questions.

When decomposing tasks, write each child's description to complete this chain. The test: *"Can an agent execute this task using only the assembled context from root to this node?"*

## Install

### Homebrew (macOS/Linux)

```bash
brew install hyper-swe/tap/mtix
```

### Binary Download

Download pre-built binaries from [GitHub Releases](https://github.com/hyper-swe/mtix/releases).

### Go Install

```bash
go install github.com/hyper-swe/mtix/cmd/mtix@latest
```

### Claude Code Plugin

```
/plugin marketplace add hyper-swe/mtix
/plugin install mtix
```

### OpenAI Codex Plugin

See [docs/codex/AGENTS.md](docs/codex/AGENTS.md) for Codex agent setup.

## Quick Start

### Build from Source

```bash
# Prerequisites: Go 1.25+, Node.js 18+ (web UI only)

# Build the complete suite (web UI + Go binary)
make build

# Or build components separately:
make build-web    # Build React SPA and embed into Go binary
make build-go     # Build Go binary only (assumes web assets exist)
```

### Initialize a Project

```bash
mkdir my-project && cd my-project
mtix init --prefix PROJ
```

This creates a `.mtix/` directory with config, SQLite database, and generates agent documentation in `.mtix/docs/`.

### Create and Manage Nodes

```bash
# Create a story
mtix create "Build authentication module" --priority 1 --description "OAuth2 flow"

# Create child issues
mtix create "Implement login endpoint" --under PROJ-1
mtix create "Add token refresh" --under PROJ-1

# Decompose into micro issues
mtix micro "Validate email format" --under PROJ-1.1
mtix micro "Write unit tests" --under PROJ-1.1

# View the tree
mtix tree PROJ-1

# Transition status
mtix claim PROJ-1.1.1 --agent agent-claude
mtix done PROJ-1.1.1

# Check progress
mtix progress PROJ-1

# List all open nodes
mtix list --status open
```

### Filtering nodes (multi-value)

Every filter on `mtix list` and `mtix search` accepts comma-separated values.
Multiple values within one flag combine with **OR**; multiple flags combine
with **AND**. This lets agents narrow large projects down to exactly the
slice of work they care about in one call.

```bash
# All done OR cancelled epics under PROJ-1 OR PROJ-2
mtix list --under PROJ-1,PROJ-2 --status done,cancelled --type epic

# Critical or high priority issues across two assignees
mtix list --priority 1,2 --type issue --assignee agent-a,agent-b

# Search "auth" within two subtrees, restricted to two node types
mtix search --query auth --under PROJ-1,PROJ-3 --type story,issue

# JSON output for machine consumption
mtix list --under PROJ-1 --status done --json
```

Available multi-value filters: `--status`, `--under`, `--type`, `--assignee`,
`--priority`. All values are sent to SQLite as bound parameters; no
SQL injection vector.

### Start the Server

```bash
mtix serve --port 8377
```

The web UI is accessible at `http://127.0.0.1:8377`. The REST API is at `/api/v1/`.

### JSON Mode (for LLM Agents)

Every command supports `--json` for machine-readable output:

```bash
mtix list --json
mtix show PROJ-1 --json
mtix create "Fix bug" --under PROJ-1 --json
```

## Companion Projects

### mgit — Surgical rollback for LLM coding agents

[mgit](https://github.com/hyper-swe/mgit) provides task-tagged micro-commits, surgical rollback, and auto-squash for LLM agents. When paired with mtix, agents micro-commit during each task, rollback wrong decisions without losing correct work, and auto-squash on `mtix done`. mtix handles *what to do*; mgit handles *how to safely do it*.

```bash
brew install hyper-swe/tap/mgit
```

## Architecture

```
cmd/mtix/           CLI entry point (Cobra commands)
internal/
  model/            Domain types, state machine, content hashing
  store/sqlite/     SQLite storage (WAL mode, parameterized queries)
  service/          Business logic layer
  api/http/         REST API (Gin) + WebSocket events
  api/grpc/         gRPC API (Protocol Buffers)
  mcp/              Model Context Protocol tool registry
  docs/             Embedded template-based doc generator
  web/              Embedded SPA (Vite + React + Tailwind)
  cloud/            Cloud sync (auth, team management)
proto/              Protocol Buffer definitions
sdk/python/         Python SDK and gRPC client
web/                Web UI source (Vite + React + TypeScript)
e2e/                End-to-end tests
```

### Layered Design

```
CLI / REST / gRPC / MCP
         |
    Service Layer      (business logic, validation, events)
         |
    Store Interface    (data access contract)
         |
    SQLite (WAL mode)  (single binary, no external DB)
```

All handlers go through the service layer. The store contains only data access. Model depends on nothing.

## API Endpoints

### REST API (`/api/v1/`)

| Method | Path | Description |
|--------|------|-------------|
| `POST` | `/nodes` | Create a node |
| `GET` | `/nodes/:id` | Get a node |
| `PATCH` | `/nodes/:id` | Update a node |
| `DELETE` | `/nodes/:id` | Soft-delete a node |
| `GET` | `/nodes/:id/children` | List children |
| `POST` | `/nodes/:id/decompose` | Batch create children |
| `GET` | `/search?q=` | Full-text search |
| `GET` | `/ready` | Nodes ready for pickup |
| `GET` | `/blocked` | Blocked nodes |
| `GET` | `/stale` | Stale nodes |
| `GET` | `/stats` | Project statistics |
| `POST` | `/admin/gc` | Run garbage collection |
| `POST` | `/admin/backup` | Create database backup |
| `GET` | `/health` | Health check |
| `GET` | `/ws/events` | WebSocket event stream |

All mutations require `X-Requested-With: mtix` header (CSRF protection).

### MCP Tools

mtix runs as an MCP server via `mtix mcp`, exposing 36 tools for LLM agents: `mtix_create`, `mtix_context`, `mtix_claim`, `mtix_done`, `mtix_decompose`, `mtix_search`, and more. The most important tool is `mtix_context` — it assembles the full context chain from root to the target node, giving the agent its complete briefing.

See [MCP Setup Guide](docs/MCP-SETUP.md) for client configuration (Claude Desktop, Claude Code, Cursor, Windsurf).

## CLI Reference

```
mtix init [--prefix PREFIX]         Initialize a project
mtix create <title> [--under ID]    Create a node
mtix micro <title> --under ID       Create a micro issue
mtix decompose <id> [titles...]     Batch create children
mtix show <id>                      Show node details
mtix list [--status S]              List nodes (filters accept comma-separated values)
mtix tree <id>                      Show hierarchy tree
mtix update <id> [--title T]        Update node fields
mtix claim <id> --agent A           Assign to agent
mtix unclaim <id> --reason R        Unassign from agent
mtix done <id>                      Mark as done
mtix defer <id> [--until TIME]      Defer a node
mtix cancel <id> --reason R         Cancel a node
mtix reopen <id>                    Reopen done/cancelled node
mtix comment <id> <text>            Add a comment
mtix dep add <from> <to>            Add dependency
mtix search [--query Q]             Full-text search (filters accept comma-separated values)
mtix ready                          Show nodes ready for work
mtix blocked                        Show blocked nodes
mtix stale                          Show stale nodes
mtix stats                          Project statistics
mtix progress <id>                  Show progress rollup
mtix verify [id]                    Verify content hash integrity
mtix backup <path>                  Create database backup
mtix export                         Export to JSON
mtix import <file> [--mode M]       Import from JSON
mtix gc                             Run garbage collection
mtix context <id>                   Assemble context chain (root → node)
mtix serve [--port P]               Start HTTP/WebSocket server
mtix mcp [--project DIR]            Run as MCP server (stdio transport)
mtix docs generate [--force]        Regenerate agent documentation
mtix config get|set|delete <key>    Manage configuration
```

## Development

### Run Tests

```bash
make test           # Go tests
make test-web       # Web tests (Vitest)
make test-all       # Both Go and web tests
make test-race      # Go tests with race detector
make test-cover     # Go tests with coverage report
make e2e            # End-to-end tests
```

### Lint

```bash
make lint           # All linters (golangci-lint + ESLint)
make lint-go        # Go linters only
make lint-web       # ESLint only
```

### Full Verification (Pre-commit / Pre-release)

```bash
make verify         # Race tests + web tests + lint + coverage + build
make build-checked  # Run all tests, then build
```

### Release

```bash
make release-patch  # Tag patch version bump (v0.1.0 → v0.1.1)
make release-minor  # Tag minor version bump (v0.1.0 → v0.2.0)
```

Version is injected from git tags via `-ldflags` at build time.

### Generate Protobuf

```bash
make proto-gen
```

## Configuration

Configuration lives in `.mtix/config.yaml`:

```yaml
prefix: PROJ
max_depth: 50
auto_claim: false
agent_stale_threshold: 30m
session_timeout: 8h
data:
  soft_delete_retention: 720h  # 30 days
progress:
  weighted: false
```

Manage via CLI: `mtix config set auto_claim true`

## Documentation

- **[User Manual](USERMANUAL.md)** — Comprehensive guide covering every feature: hierarchy, state machine, dependencies, prompt steering, agent management, REST/gRPC/MCP APIs, backup/export/import, and more.
- **[MCP Setup Guide](docs/MCP-SETUP.md)** — Configure mtix as an MCP server for Claude Desktop, Claude Code, Cursor, and Windsurf. Includes multi-project setup and context chain usage.
- **[Security Model](docs/SECURITY-MODEL.md)** — Trust model and threat model for mtix. **Required reading before adopting BYO Postgres mode** for team collaboration. Documents what mtix protects against, what it does not, and the security checklist for adopters.

## License

Licensed under the [Apache License, Version 2.0](LICENSE).

Copyright 2025-2026 HyperSWE.
