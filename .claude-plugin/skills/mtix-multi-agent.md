---
description: "Coordinate multiple agents in MTIX project using mtix. Use when registering agents, managing parallel work, handling handoffs, checking agent status, or resolving stale claims."
allowed-tools:
  - mcp__mtix__mtix_agent_register
  - mcp__mtix__mtix_agent_state
  - mcp__mtix__mtix_agent_heartbeat
  - mcp__mtix__mtix_agent_work
  - mcp__mtix__mtix_session_start
  - mcp__mtix__mtix_session_end
  - mcp__mtix__mtix_ready
  - mcp__mtix__mtix_claim
  - mcp__mtix__mtix_unclaim
  - mcp__mtix__mtix_stale
---

# MTIX — Multi-Agent Coordination

## Context Chain Awareness

When coordinating agents, remember: every agent MUST call `mtix_context` before working on any task. The context chain is the agent's complete briefing. An agent working without context is an agent working blind.

## Agent Identity

Every agent has a unique ID (e.g., `agent-claude-1`, `agent-opus-review`). All operations are traced to this identity for audit:
- Session records track which agent worked when
- Claim records show who owns each task
- Heartbeat records prove agent liveness
- Comments are attributed to the authoring agent

**Never use anonymous or shared agent IDs.** Each agent instance must be uniquely identifiable.

## Agent Lifecycle

### 1. Registration
Call `mcp__mtix__mtix_agent_register` to create the agent record. Registration is automatic on first claim, but explicit registration is preferred for audit clarity.

### 2. Session Start
Call `mcp__mtix__mtix_session_start` with agent ID and project. This creates a timestamped session record.

### 3. Active Work
During work, the agent:
- Claims nodes via `mcp__mtix__mtix_claim`
- Sends heartbeats via `mcp__mtix__mtix_agent_heartbeat` every 5 minutes
- Executes tasks following the context chain
- Marks nodes done or defers them

### 4. Session End
Call `mcp__mtix__mtix_session_end` when work is complete.

## Heartbeat Protocol

Heartbeats prove agent liveness. Without them, the system assumes the agent has crashed.

- **Frequency:** Every 5 minutes during active work
- **Detection:** `mcp__mtix__mtix_stale` reports agents whose last heartbeat exceeds the configured threshold (default: 30 minutes)
- **Consequence:** Stale agents may have their work reclaimed by other agents via force-reclaim
- **Recovery:** If you detect your own heartbeat lapsed, immediately send a heartbeat and verify your claims are still active

## Concurrent Claim Rules

- **One agent per node.** A node can only be claimed by one agent at a time
- **No double-claiming.** If you try to claim an already-claimed node, you get `ErrAlreadyClaimed`
- **Check before claiming:** Use `mcp__mtix__mtix_show` to verify a node is unclaimed before attempting to claim it
- **Force-reclaim:** Only available when the current agent is stale (heartbeat expired). Never force-reclaim an active agent's work

## Handoff Protocol

When unclaiming a node (via `mcp__mtix__mtix_unclaim`):

1. **Always provide a reason** — the next agent needs context on why you stopped
2. **Document progress** — use `mcp__mtix__mtix_comment` to record what was completed, what remains, and any issues discovered
3. **Clean state** — ensure no partial changes are left that would confuse the next agent

## Independent Verification

**The agent that implements a task should NOT be the same agent that verifies it.**

For critical tasks:
1. The implementing agent marks the task as done
2. A separate verification agent reviews the work
3. The verifier checks: acceptance criteria met, tests passing, no stub implementations, traceability comments present
4. If verification fails, the verifier adds a comment and reopens the task

This principle aligns with NASA IV&V requirements, DO-178C verification objectives, and IEC 62304 safety class requirements.

## Stale Agent Recovery

When `mcp__mtix__mtix_stale` reports stale agents:

1. **Investigate first** — is the agent truly dead, or just temporarily unresponsive?
2. **Check partial work** — use `mcp__mtix__mtix_agent_work` to see what the stale agent was working on
3. **Document** — add a comment to affected nodes explaining the stale detection
4. **Reclaim if needed** — unclaim the node so it becomes available for other agents
5. **Never delete agent records** — they are part of the audit trail

## Parallel Work Coordination

When multiple agents work simultaneously:

- Each agent works on different nodes — never the same node
- Use `mcp__mtix__mtix_ready` to find unclaimed work
- Declare cross-task dependencies via `mtix_dep_add` to prevent conflicts
- After completing work that others depend on, verify the dependent tasks are unblocked
