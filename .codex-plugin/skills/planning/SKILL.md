---
name: planning
description: Plan and decompose work using mtix hierarchical task structure. Create stories, epics, and leaf tasks with complete context chains.
---

# Planning with mtix

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
