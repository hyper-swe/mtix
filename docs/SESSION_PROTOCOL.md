# Session Protocol

> **Project:** PROJ

## Session Lifecycle

### Start

Run `mtix agent register <agent-id>` at session boot. Repeat registration on the same board exits 0 with an "already registered" notice and refreshes the heartbeat. It preserves the existing state, work assignment, project, and active session; it does not start or replace a session. With `--json`, the status is `registered` for a new identity or `already_registered` for a repeat. Use a unique ID per agent; registration currently identifies an agent by ID on the board, without a live-session ownership check.

```
mtix session start --agent <agent_id>
```

Or MCP: `mtix_session_start`

- Creates a new session record
- Automatically ends any previous active session for the agent
- Returns a session ID for tracking

### During Session

- Send heartbeats: `mtix agent heartbeat` or `mtix_agent_heartbeat`
- Heartbeat interval: at least every 5 minutes
- Missing heartbeats trigger stale agent detection

### End

```
mtix session end
```

Or MCP: `mtix_session_end`

- Records session summary (nodes created, completed, time spent)
- Releases any claimed nodes

## Handoff Protocol

When transferring work to another agent:

1. Unclaim any in-progress nodes with a detailed reason
2. End your session
3. The next agent starts a new session and claims available nodes

## Session Timeout

Sessions automatically time out after the configured `session.timeout` (default: 4 hours). On timeout:

- Session is marked as ended
- Claimed nodes are unclaimed with reason "session timeout"

## Compaction Survival

Session data persists through database compaction. Session summaries are stored in the activity log and can be retrieved later for audit purposes.

## Best Practices

1. Always start a session before claiming nodes
2. Send regular heartbeats to avoid stale detection
3. End sessions cleanly — do not just disconnect
4. Include meaningful reasons when unclaiming nodes
