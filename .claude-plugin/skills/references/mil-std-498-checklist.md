# MIL-STD-498 Compliance Mapping — MTIX

This checklist maps mtix workflows to MIL-STD-498 (Software Development and Documentation).

## Document Type Mapping

MIL-STD-498 defines document types that map naturally to the mtix task hierarchy:

| MIL-STD-498 Document | mtix Equivalent |
|----------------------|-----------------|
| SRS (Software Requirements Specification) | Story-level tasks with requirements in description/prompt |
| SDD (Software Design Description) | Epic/issue-level tasks with architectural decisions |
| STP (Software Test Plan) | `tests` field in leaf tasks |
| STR (Software Test Report) | Completion comments with test results |
| SPS (Software Product Specification) | Export snapshot with checksums |

## CSCI/CSC Decomposition

MIL-STD-498 organizes software into Computer Software Configuration Items (CSCIs) and Computer Software Components (CSCs).

mtix supports this via dot-notation hierarchy:
```
PROJ-1         → CSCI: "Navigation Subsystem"
PROJ-1.1       → CSC: "Position Calculator"
PROJ-1.1.1     → Unit: "Coordinate Transform Function"
```

Use `mtix_tree` to visualize the CSCI/CSC decomposition. The context chain from root to leaf provides the full decomposition path.

## Classification Awareness

**Task descriptions must NOT contain classified information.**

mtix is an unclassified task management tool. Tasks should reference classified documents by document number and classification level, never by content:
- "Implement requirements from SRS-NAV-001 (SECRET)" — acceptable
- Copying classified requirement text into the task description — NOT acceptable

## Configuration Status Accounting

MIL-STD-498 §5.14 requires configuration status accounting. mtix provides:

| Requirement | mtix Feature |
|-------------|--------------|
| Unique identification | Dot-notation IDs |
| Current status | `mtix_show`, `mtix_stats` |
| Change history | State machine transitions, activity log |
| Baseline snapshots | `mtix_export` with checksums |
| Integrity verification | `mtix_verify` (SHA-256 content hashes) |

## Test Readiness Review

Before marking a CSCI-level task as done, conduct a Test Readiness Review:

1. All child tasks (CSCs and units) are in `done` status
2. All test tasks have passing results documented in comments
3. No unresolved blocked or deferred tasks
4. `mtix_progress` shows 100% completion
5. `mtix_verify` confirms data integrity

Use `mtix_tree` to verify the complete decomposition is done, not just the top-level node.

## Agent Session Logging

All agent sessions are logged for audit:
- Agent ID, project, start time, end time
- Nodes claimed and completed during the session
- Heartbeat records proving continuous operation

This satisfies MIL-STD-498 §5.15 requirements for engineering environment documentation and process evidence.

## Subcontractor Oversight

When multiple agent teams work on different CSCIs:
- Each team's agents use distinct agent IDs
- Session records attribute work to specific teams
- `mtix_stale` detects unresponsive agents across all teams
- Cross-team dependencies tracked via `mtix_dep_add`
