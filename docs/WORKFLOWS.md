# Workflows

> **Project:** PROJ

## Decompose → Claim → Done

The core workflow for AI agents:

### 1. Decompose

Break large tasks into smaller, actionable subtasks:

```
mtix decompose <parent_id> --file plan.jsonl
```

Or via MCP: `mtix_decompose`

**Important:** After decomposing, populate every child with context:

```
mtix update <child_id> --description "..." --prompt "..." --acceptance "..."
```

The CLI will warn if children are created without these fields. A task without `description`, `prompt`, and `acceptance` is not ready for agent pickup. See [CONTEXT_CHAIN.md](CONTEXT_CHAIN.md) for guidance on writing effective task context.

### 2. Claim

Claim a task before working on it:

```
mtix claim <id> --agent <your_agent_id>
```

This transitions the node to `in_progress` and assigns you.

### 3. Execute

Read the node's prompt and acceptance criteria. Do the work.

### 4. Mark Done

```
mtix done <id>
```

Progress automatically rolls up to parent nodes.

### 5. Unclaim (if needed)

If you cannot complete a task, release it:

```
mtix unclaim <id> --reason "explanation"
```

## Batch Operations

Use `mtix decompose` for atomic child creation. All children are created in a single transaction.

## Dependency Handling

Before working, check for blocking dependencies:

```
mtix blocked <id>
```

If blocked, either resolve the blocker or defer the task.
