---
name: planning
description: Plan and decompose work using mtix hierarchical task structure. Create stories, epics, and leaf tasks with complete context chains.
---

# Planning with mtix

## Creating classified and assigned work

`mtix create --type` selects the issue type: `bug`, `feature`, `task`, `chore`, `refactor`, `test`, or `doc`. Omission leaves `issue_type` unset; it does not default to `task`. Hierarchy `node_type` is derived from depth independently (epic, story, issue, micro). `mtix show` and `mtix list` display both classifications; JSON carries separate fields.

`mtix update TEST-1 --type refactor` changes work classification; `mtix update TEST-1 --type=` clears it. On updates, omission preserves the current classification; an empty string clears it to SQL NULL. CLI, MCP `mtix_update`, REST PATCH and gRPC updates validate the same seven values. Their JSON `issue_type` field may be omitted or null to preserve, or `""` to clear; the proto field uses presence and explicit `ISSUE_TYPE_UNSPECIFIED` to clear. Existing unclassified nodes remain unset; labels do not imply a type. Default node JSON omits an unset `issue_type`; explicit field projections and exports include it as an empty string.

`mtix list --type story --issue-type bug` combines hierarchy and work classification. List `--type` filters `node_type` (epic, story, issue, micro); create/update `--type` sets `issue_type`. List `--issue-type bug,feature` matches either classification and rejects invalid values. Default list text, JSON and `--fields id,node_type,issue_type` expose both classifications. Export/import preserves classified and unset values.

Sync carries classification on `create_node` and on `update_field` with `field_name: "issue_type"` and a JSON string `new_value`; `""` clears it. Apply validates values before writes, deduplication, held-event acknowledgement and LWW comparison, rejecting unknown types and non-string values even when an event loses or is already held. The hub stores and forwards these field events under its envelope checks. Upgrade every replica before emitting classification updates: older clients reject the unfamiliar update field and cannot apply it until upgraded.


`mtix create --assign <agent>` creates an open node and claims it atomically, yielding `in_progress` with normal claim activity and agent state. The creator remains the resolved author (`MTIX_AUTHOR_ID`, configured author, then `cli`). An explicit assignee overrides parent auto-claim; without it, the existing parent auto-claim setting applies. A failed explicit claim rolls back the node and its creation/claim activity and sync events.

MCP `mtix_create`, REST create and gRPC create accept `issue_type` and `assignee` with the same behavior. MCP accepts an optional `creator` (default `mcp`), REST retains the `X-Agent-ID` author, and gRPC retains `creator`; none uses the assignee as creator. Sync carries issue type as an optional create payload field; older payloads leave the issue type unset. Upgrade every replica before relying on synchronized issue type. A node created while a 0.5.4 replica is attached stays unclassified on that replica after upgrade; already-applied create events are not replayed. Assignment uses the ordinary claim event.



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

## Declaring Dependencies

For the CLI, use `mtix dep add <from-id> <to-id> --type related` for an informational link; `mtix dep add --help` lists every accepted type. Use the same type with `mtix dep remove`.

Supported types: `blocks`, `related`, `discovered_from`, and `duplicates`. Parent-child relationships are inherent in dot-notation IDs and are not dependencies.
