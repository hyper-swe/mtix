---
name: task-execution
description: Execute mtix tasks using the context chain. Claim tasks, read assembled context, implement with TDD, and mark done.
---

# Task Execution with mtix

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
