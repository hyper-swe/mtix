---
description: "Execute tasks in MTIX project using mtix. Use when working on tasks, picking up work, claiming nodes, completing work, or managing task lifecycle."
allowed-tools:
  - mcp__mtix__mtix_create
  - mcp__mtix__mtix_ready
  - mcp__mtix__mtix_context
  - mcp__mtix__mtix_claim
  - mcp__mtix__mtix_unclaim
  - mcp__mtix__mtix_done
  - mcp__mtix__mtix_defer
  - mcp__mtix__mtix_show
  - mcp__mtix__mtix_list
  - mcp__mtix__mtix_search
  - mcp__mtix__mtix_agent_heartbeat
  - mcp__mtix__mtix_session_start
  - mcp__mtix__mtix_session_end
  - mcp__mtix__mtix_blocked
  - mcp__mtix__mtix_comment
---

# MTIX — Task Execution

## NON-NEGOTIABLE: Context Chain Traversal

**Before doing ANY work, call `mcp__mtix__mtix_context` with the node ID.** The assembled prompt from root→node IS your complete briefing. It contains the business goal (root), technical scope (middle levels), and exact implementation instructions (leaf).

- Do NOT work from titles alone
- Do NOT skip this step under any circumstances
- If the assembled context is insufficient to execute independently, STOP and escalate — the parent task decomposition is incomplete
- This rule is as inviolable as parameterized SQL queries in safety-critical systems

## NON-NEGOTIABLE: No Code Without a Task

**NEVER write code without a corresponding mtix task.** If no task exists for the work you're about to do — whether it's a feature, bug fix, refactor, or even a one-line change — you MUST create one first using `mcp__mtix__mtix_create`, then claim it, then work.

Every task you create MUST pass the completeness test: *"Can a different agent, with zero conversation history, execute this task using ONLY the assembled context chain from root to this node?"*

When creating a task, always populate:
- **description** — why this task exists, scope, constraints
- **prompt** — exact files to modify, functions to write, inputs/outputs, error conditions, edge cases, test scenarios
- **acceptance** — testable criteria with specific verifiable outcomes (not vague goals)

If the task has children, decompose with `mcp__mtix__mtix_decompose` and ensure every leaf node is fully populated. A title-only task is not a task.

This applies even when:
- You discover a bug mid-work on another task
- The user asks for a "quick fix"
- The change seems trivially small

## Execution Protocol

Follow this sequence exactly:

### 1. Start Session
Call `mcp__mtix__mtix_session_start` with your agent ID and the MTIX project. This creates an auditable session record.

### 2. Find Work
Call `mcp__mtix__mtix_ready` to list nodes available for pickup. These are unassigned, unblocked nodes in `open` status.

### 3. Read Context (MANDATORY)
Call `mcp__mtix__mtix_context` with the node ID. Read the ENTIRE assembled prompt. This is your briefing — business goal, technical constraints, exact instructions, acceptance criteria, and test specifications.

### 4. Claim
Call `mcp__mtix__mtix_claim` with the node ID and your agent ID. This prevents other agents from picking up the same work.

### 5. Send Heartbeats
Call `mcp__mtix__mtix_agent_heartbeat` every 5 minutes during execution. Missed heartbeats trigger stale detection — your work may be reclaimed by another agent.

### 6. Execute
Follow the assembled prompt precisely. Write code, tests, and documentation as specified. Meet every acceptance criterion.

### 7. Verify Before Done
Before calling `mcp__mtix__mtix_done`:

- **All acceptance criteria met** — check each one explicitly
- **Tests written and passing** — no stub implementations
- **Independent verification** — if you implemented it, another agent should verify it. Add a traceability comment linking task→requirement→test→result using `mcp__mtix__mtix_comment`
- **No stub implementations** — functions must perform their documented purpose end-to-end. A compiling stub is not an implementation

### 8. Mark Done
Call `mcp__mtix__mtix_done` with the node ID. Progress automatically rolls up to parent nodes.

### 9. End Session
When all work is complete, call `mcp__mtix__mtix_session_end`.

## Blocked/Deferred Protocol

**Never abandon a task silently.** If you cannot complete a task:

- **Blocked by dependency:** Call `mcp__mtix__mtix_comment` to document the blocker, then check `mcp__mtix__mtix_blocked` for the dependency status
- **Deferred:** Call `mcp__mtix__mtix_defer` with a root cause explanation. Include what remains to be done and why you're deferring
- **Anomaly detected:** Always annotate with `mcp__mtix__mtix_comment` — unexplained failures, inconsistent state, or missing context must be recorded for audit

## Error Recovery

| Situation | Action |
|-----------|--------|
| Context is insufficient | STOP. Do not guess. Use `mcp__mtix__mtix_comment` to flag the gap. Defer the task. |
| Claim conflict | Another agent has the node. Find other ready work via `mcp__mtix__mtix_ready`. |
| Heartbeat lapsed | Your work may be reclaimed. Re-check claim status via `mcp__mtix__mtix_show`. |
| Tests fail | Do NOT mark done. Fix the tests or defer with an explanation. |
| State machine error | Check allowed transitions in STATUS_MACHINE.md. Do not force transitions. |

## Traceability

Every completed task must have an auditable trail:
1. Session record (who worked on it, when)
2. Traceability comment (task → requirement → test → result)
3. Verification evidence (tests passing, acceptance criteria met)

This is not optional — it is the baseline for all MTIX work, aligned with DO-178C, IEC 62304, NASA-STD-8739.8, and MIL-STD-498 standards.
