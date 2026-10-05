---
name: planning
description: Plan and decompose work using mtix hierarchical task structure. Create stories, epics, and leaf tasks with complete context chains.
---

# Planning with mtix

## Creating classified and assigned work

`mtix create --type` selects the issue type: `bug`, `feature`, `task`, `chore`, `refactor`, `test`, or `doc`. Omission leaves `issue_type` unset; it does not default to `task`. Hierarchy `node_type` is derived from depth independently (epic, story, issue, micro). `mtix show` and `mtix list` display both classifications; JSON carries separate fields.

`mtix create --assign <agent>` creates an open node and claims it atomically, yielding `in_progress` with normal claim activity and agent state. The creator remains the resolved author (`MTIX_AUTHOR_ID`, configured author, then `cli`). An explicit assignee overrides parent auto-claim; without it, the existing parent auto-claim setting applies. A failed explicit claim rolls back the node and its creation/claim activity and sync events.

MCP `mtix_create`, REST create and gRPC create accept `issue_type` and `assignee` with the same behavior. MCP accepts an optional `creator` (default `mcp`), REST retains the `X-Agent-ID` author, and gRPC retains `creator`; none uses the assignee as creator. Sync carries issue type as an optional create payload field; older clients ignore it and older payloads leave the issue type unset. Assignment uses the ordinary claim event.



## Creating Tasks

```bash
mtix create "Task title" --description "Why this exists" --prompt "Exact instructions" --acceptance "Testable done criteria"
```

## Decomposing Tasks

Break large tasks into subtasks:
```bash
mtix create "Subtask title" --under PROJ-1 --description "..." --prompt "..." --acceptance "..."
```

## The Completeness Test

Every leaf task must pass: "Can a different agent, with zero context, execute this task using ONLY the assembled context chain from root to this node?"

If not, add: file paths, function names, inputs/outputs, edge cases, test scenarios.

## Context Chain Design

Write each level to complete the chain:
- Story: business goal and success criteria
- Epic: technical scope and approach
- Issue: exact files, functions, and test cases
