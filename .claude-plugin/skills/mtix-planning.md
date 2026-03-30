---
description: "Plan and decompose tasks in MTIX project using mtix. Use when breaking down work, creating subtasks, designing task hierarchies, or writing context-rich task descriptions."
allowed-tools:
  - mcp__mtix__mtix_create
  - mcp__mtix__mtix_decompose
  - mcp__mtix__mtix_update
  - mcp__mtix__mtix_context
  - mcp__mtix__mtix_show
  - mcp__mtix__mtix_tree
  - mcp__mtix__mtix_dep_add
  - mcp__mtix__mtix_dep_remove
  - mcp__mtix__mtix_comment
---

# MTIX — Planning & Decomposition

## NON-NEGOTIABLE: Context Chain Completeness

Every leaf task MUST have these fields populated before it is considered ready for agent pickup:
- **description** — scope, constraints, and why this task exists
- **prompt** — exact instructions: file paths, function names, API contracts, edge cases, test scenarios
- **acceptance** — testable criteria that define "done"
- **tests** — specific test function names and scenarios to write

**A task with only a title is not a task — it is an unfinished thought.** The dot-notation hierarchy exists so agents traverse parent→child and accumulate full context. Empty nodes break this contract.

**The completeness test:** *"Can an agent execute this task using ONLY the assembled context from root to this node?"* If no, add the missing details.

## Before Decomposing

1. Call `mcp__mtix__mtix_context` on the parent node to understand the full context chain
2. Read the parent's description, prompt, and acceptance criteria
3. Understand what the parent expects its children to collectively accomplish

## Decomposition Rules

### Parent Provides "Why", Child Provides "What"

- **Parent node:** Business goal, technical approach, constraints, overall acceptance criteria
- **Child node:** Specific implementation detail — file paths, function signatures, edge cases, test cases

### Each Child Adds Specific Detail

Every child must add information that the parent does not have:
- File paths and function names to modify
- Input/output contracts (what the API accepts and returns)
- Edge cases to handle (errors, empty input, boundary values)
- Test cases to write (function names and scenarios)
- Dependencies on other nodes (declare via `mcp__mtix__mtix_dep_add`)

### Safety-Critical Decomposition

- **Every child inherits safety constraints from parent.** If the parent specifies "all changes must be backward-compatible," every child must respect that
- **Never decompose away a safety requirement.** If a parent has a safety constraint, it must appear in at least one child's acceptance criteria
- **Track requirement coverage across children.** After decomposition, verify that the parent's acceptance criteria are fully covered by the union of children's acceptance criteria
- **Independent verification tasks.** For critical work, create a separate child for verification — the implementing agent should not verify its own work

## Decomposition Workflow

### Step 1: Create Structure
```
mcp__mtix__mtix_decompose with parent_id and titles
```
This creates child nodes with titles only.

### Step 2: Populate Every Child
For each child, call `mcp__mtix__mtix_update` with:
- `description`: scope and constraints
- `prompt`: exact implementation instructions
- `acceptance`: testable done criteria
- `tests`: test function names

### Step 3: Declare Dependencies
If children have ordering constraints, use `mcp__mtix__mtix_dep_add` to declare dependencies. Undeclared dependencies cause silent failures in parallel agent execution.

### Step 4: Verify Completeness
Use `mcp__mtix__mtix_tree` to view the hierarchy. For each leaf node, mentally run the completeness test. If an agent reading only the assembled context would need to ask questions, add more detail.

## Writing Effective Prompts

### Good Prompt Example
```
Implement the `CreateNode` function in `internal/store/sqlite/node.go`.

The function must:
1. Generate a dot-notation ID using atomic sequence counters
2. Validate parent status (reject if parent is cancelled/invalidated)
3. Compute content_hash via SHA-256 of title+description
4. Recalculate parent progress in the same transaction

Input: CreateNodeRequest with Title, ParentID, Project, Creator
Output: *model.Node, error
Errors: ErrInvalidInput (empty title), ErrNotFound (bad parent)

Tests to write:
- TestCreateNode_ValidInput_ReturnsNode
- TestCreateNode_EmptyTitle_ReturnsInvalidInput
- TestCreateNode_CancelledParent_ReturnsInvalidInput
- TestCreateNode_GeneratesContentHash
```

### Bad Prompt Example
```
Implement node creation.
```
This is not actionable. An agent reading this has no idea what file to modify, what function to write, what the inputs/outputs are, or what tests to create.

## Dependency Declaration

Cross-branch dependencies MUST be declared explicitly:
- Call `mcp__mtix__mtix_dep_add` with `from_id` (blocked node) and `to_id` (blocking node)
- Supported types: `blocks`, `related`, `duplicates`, `discovered_from`
- mtix performs automatic cycle detection — circular dependencies are rejected
- An agent cannot claim a node that has unresolved `blocks` dependencies
