---
name: PROJ
description: mtix-managed project PROJ
version: 94ee679-dirty
allowed-tools:
  - mtix_create
  - mtix_show
  - mtix_list
  - mtix_done
  - mtix_claim
  - mtix_unclaim
  - mtix_context
  - mtix_decompose
  - mtix_search
  - mtix_ready
  - mtix_prompt
  - mtix_annotate
  - mtix_session_start
  - mtix_session_end
  - mtix_agent_heartbeat
---

# PROJ — mtix Skill

This skill enables AI agents to work on the **PROJ** project using mtix task management.

## Core Principle: Context Chain

The dot-notation task hierarchy (e.g., `PROJ-1.3.2`) is a context chain. Traversing from root to leaf gives you the complete briefing:

- **Root** → business goal and constraints
- **Middle levels** → technical approach and scope
- **Leaf** → exact implementation instructions (files, functions, tests)

**Always call `mtix_context` before executing a task.** The assembled prompt is your primary instruction set.

## Quick Start

1. Start a session: `mtix_session_start`
2. Find work: `mtix_ready`
3. **Read context: `mtix_context` with the node ID** — this is mandatory
4. Claim: `mtix_claim`
5. Execute following the assembled prompt
6. Mark done: `mtix_done`

## When Creating or Decomposing Tasks

Write descriptions that **complete the context chain.** Each child should add:
- **File paths** and function names to modify
- **Input/output contracts** (what the API accepts and returns)
- **Edge cases** to handle (errors, empty input, limits)
- **Test cases** to write (function names and scenarios)

The test: *"Can an agent execute this task using only the assembled context from root to this node?"*

## Documentation

See [AGENTS.md](AGENTS.md) for the full operating guide.
See [CONTEXT_CHAIN.md](CONTEXT_CHAIN.md) for writing effective task descriptions.
