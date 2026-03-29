# mtix MCP Server Setup

mtix runs as a Model Context Protocol (MCP) server, allowing LLM agents to manage tasks programmatically. The MCP server uses stdio transport (JSON-RPC 2.0 over stdin/stdout).

## Prerequisites

1. Install mtix and ensure it's in your PATH
2. Initialize a project: `cd /path/to/project && mtix init --prefix PROJ`

## Quick Start

```bash
# Test that MCP server starts correctly
echo '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","clientInfo":{"name":"test","version":"1.0"}}}' | mtix mcp --project /path/to/project
```

## Multi-Project Support

Use `--project` (or `-C`) to point mtix at any project directory. Each project has its own `.mtix/` database, so you can manage multiple projects independently:

```json
{
  "mcpServers": {
    "mtix-webapp": {
      "command": "mtix",
      "args": ["mcp", "--project", "/home/user/webapp"]
    },
    "mtix-api": {
      "command": "mtix",
      "args": ["mcp", "--project", "/home/user/api-service"]
    },
    "mtix-infra": {
      "command": "mtix",
      "args": ["mcp", "--project", "/home/user/infrastructure"]
    }
  }
}
```

Each entry spawns a separate mtix process with its own database. Agents connected to `mtix-webapp` only see webapp tasks; agents on `mtix-api` only see API tasks. No cross-contamination.

## Client Configuration

### Claude Desktop

Edit `~/Library/Application Support/Claude/claude_desktop_config.json` (macOS) or `%APPDATA%\Claude\claude_desktop_config.json` (Windows):

```json
{
  "mcpServers": {
    "mtix": {
      "command": "mtix",
      "args": ["mcp", "--project", "/path/to/your/project"]
    }
  }
}
```

### Claude Code

Use the CLI:

```bash
claude mcp add mtix -- mtix mcp --project /path/to/your/project
```

Or manually edit `.claude/settings.json`:

```json
{
  "mcpServers": {
    "mtix": {
      "command": "mtix",
      "args": ["mcp", "--project", "/path/to/your/project"]
    }
  }
}
```

### Cursor

Edit `.cursor/mcp.json` in your project root:

```json
{
  "mcpServers": {
    "mtix": {
      "command": "mtix",
      "args": ["mcp", "--project", "/path/to/your/project"]
    }
  }
}
```

### Windsurf

Edit `~/.codeium/windsurf/mcp_config.json`:

```json
{
  "mcpServers": {
    "mtix": {
      "command": "mtix",
      "args": ["mcp", "--project", "/path/to/your/project"]
    }
  }
}
```

## The Context Chain — How Agents Get Their Briefing

The dot-notation hierarchy (e.g., `PROJ-1.3.2`) is more than an ID scheme — it's a **context chain**. Each level adds a layer of context:

```
PROJ-1       → "Build user authentication" (business goal)
PROJ-1.3     → "Implement rate limiting for auth endpoints" (technical scope)
PROJ-1.3.2   → "Add sliding window counter in internal/ratelimit/window.go" (exact instruction)
```

When an agent calls `mtix_context` for `PROJ-1.3.2`, it receives the **assembled prompt** — a single document containing all ancestor context plus the node's own prompt. The agent gets the full picture without reading anything else.

### Using mtix_context

**Every agent should call `mtix_context` before executing a task.** This is the most important tool in the set:

```json
{"name": "mtix_context", "arguments": {"id": "PROJ-1.3.2"}}
```

The response includes:
- **Ancestor chain** — root to target, each with title, description, and prompt
- **Siblings** — peer tasks at the same level (for coordination awareness)
- **Blockers** — dependency nodes that must complete first
- **Assembled prompt** — the complete briefing, ready to execute from

### Writing Tasks for the Context Chain

When creating or decomposing tasks via `mtix_create` or `mtix_decompose`, write descriptions that **complete the context chain:**

- **Parent provides the "why"** — business goal, constraints, architecture decisions
- **Child provides the "what"** — file paths, function names, API contracts, test cases, edge cases

The test: *"If an agent reads only the assembled context from root to this node, can it execute without asking questions?"*

**Good child prompt:** *"Add exponential backoff retry to auth API calls. Modify internal/http/client.go:SendRequest(). Max 3 retries, base delay 100ms, jitter ±20ms. Test: TestSendRequest_TransientFailure_RetriesWithBackoff in client_test.go."*

**Poor child prompt:** *"Add retry logic."*

See the auto-generated `CONTEXT_CHAIN.md` in your project's `.mtix/docs/` for detailed examples.

## Available Tools (36)

Once connected, the following MCP tools are available:

### Node Management
| Tool | Description |
|------|-------------|
| `mtix_create` | Create a new node |
| `mtix_show` | Get full node details |
| `mtix_list` | List nodes with filters |
| `mtix_update` | Update node fields |
| `mtix_delete` | Soft-delete a node |
| `mtix_undelete` | Recover a soft-deleted node |
| `mtix_decompose` | Batch-create children |

### Workflow
| Tool | Description |
|------|-------------|
| `mtix_claim` | Claim a node for an agent |
| `mtix_unclaim` | Release assignment |
| `mtix_done` | Mark complete |
| `mtix_defer` | Postpone until later |
| `mtix_cancel` | Descope work |
| `mtix_reopen` | Reverse terminal state |
| `mtix_rerun` | Invalidate and reprocess |
| `mtix_ready` | List nodes ready for pickup |
| `mtix_blocked` | List blocked nodes |
| `mtix_search` | Full-text search |

### Context & Prompts
| Tool | Description |
|------|-------------|
| `mtix_context` | Assemble context chain (ancestors, siblings, blockers) |
| `mtix_prompt` | Update a node's prompt |
| `mtix_annotate` | Add annotations/comments |
| `mtix_resolve_annotation` | Mark annotations resolved |

### Dependencies
| Tool | Description |
|------|-------------|
| `mtix_dep_add` | Add dependency between nodes |
| `mtix_dep_remove` | Remove dependency |
| `mtix_dep_show` | Get blockers for a node |

### Sessions & Agents
| Tool | Description |
|------|-------------|
| `mtix_session_start` | Start an agent session |
| `mtix_session_end` | End session |
| `mtix_session_summary` | Get session statistics |
| `mtix_agent_heartbeat` | Send heartbeat |
| `mtix_agent_state` | Get/set agent state |
| `mtix_agent_work` | Get current work assignment |

### Analytics
| Tool | Description |
|------|-------------|
| `mtix_stats` | Project statistics |
| `mtix_progress` | Node progress rollup |
| `mtix_stale` | Stale agents and tasks |
| `mtix_orphans` | Root-level nodes |

### Discovery
| Tool | Description |
|------|-------------|
| `mtix_discover` | List all available tools |
| `mtix_docs_generate` | Regenerate agent documentation |

## Logging

MCP server logs are written to `<project>/.mtix/logs/mtix.log` (never to stdout, which is reserved for the JSON-RPC protocol stream).

## Troubleshooting

**"not in an mtix project"** — Run `mtix init --prefix PROJ` in the project directory first, or check that `--project` points to the correct path.

**No tools showing up** — Ensure `--project` points to a directory containing a `.mtix/` folder (or a parent directory that does).

**Connection drops** — Check `.mtix/logs/mtix.log` for errors. Common causes: database locked, permissions issues.

**Multiple projects** — Each project needs its own MCP server entry with a unique name (e.g., `mtix-webapp`, `mtix-api`). Each runs as a separate process with its own database.
