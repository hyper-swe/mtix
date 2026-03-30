# AGENTS.md — mtix Integration

mtix is an AI-native micro issue manager. It provides hierarchical task
decomposition with dot-notation context chains for LLM agents.

## Setup

```bash
# Install mtix (choose one)
brew install hyper-swe/tap/mtix
# or download from https://github.com/hyper-swe/mtix/releases

# Initialize in your project
cd /path/to/your/project
mtix init --prefix PROJ
```

## Task Workflow

### 1. Find Work

```bash
mtix ready              # List tasks available for pickup
mtix list --status open  # All open tasks
```

### 2. Read Context

```bash
mtix context PROJ-1.2.3  # Assembled briefing from root to this task
```

The context chain traverses from root to leaf, combining every ancestor's
description into a single prompt. This is your complete briefing — read it
fully before starting.

### 3. Claim and Execute

```bash
mtix claim PROJ-1.2.3 --agent codex
# ... do the work ...
mtix done PROJ-1.2.3
```

### 4. Create Tasks for New Work

Every change needs a task — even one-line fixes:

```bash
mtix create "Fix null validation in CreateNode" \
  --description "Why this task exists" \
  --prompt "Exact files and functions to modify" \
  --acceptance "Testable criteria that define done"
```

### 5. Decompose Large Tasks

```bash
mtix create "Subtask title" --under PROJ-1 \
  --description "..." --prompt "..." --acceptance "..."
```

### 6. Export State

```bash
mtix export  # Export task state to .mtix/tasks.json
```

## Rules

1. **Always read the context chain** before starting work (`mtix context <id>`)
2. **Never skip task creation** — every change must be tracked
3. **Report blockers** with `mtix comment <id> "blocked: reason"`
4. **Run `mtix export`** before committing — include tasks.json in your commit

## MCP Integration

mtix includes an MCP server for direct tool access:

```bash
mtix mcp --project /path/to/project
```

This provides 36+ tools for task management, agent coordination,
and project administration via the Model Context Protocol.
