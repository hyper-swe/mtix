---
name: multi-agent
description: Coordinate multiple agents working on mtix tasks. Manage claiming, sessions, heartbeats, and parallel execution.
---

# Multi-Agent Coordination with mtix

## Agent Registration

```bash
mtix claim <id> --agent <agent-name>
mtix session start <agent-name>
```

## Finding Work

```bash
mtix ready          # Tasks available for pickup (no assignee, no blockers)
mtix blocked        # Tasks waiting on dependencies
mtix stale          # Tasks with inactive agents
```

## Parallel Execution Rules

- Only one agent can claim a task at a time
- Read the context chain before starting: `mtix context <id>`
- Send heartbeats during long work: `mtix agent heartbeat <agent-name>`
- End sessions when done: `mtix session end <agent-name>`

## Conflict Prevention

- Check task status before claiming: `mtix show <id>`
- Never modify tasks claimed by other agents
- Use comments to coordinate: `mtix comment <id> "message"`
