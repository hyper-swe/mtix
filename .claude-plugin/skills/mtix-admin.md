---
description: "Administer MTIX project using mtix. Use when backing up data, exporting/importing tasks, running garbage collection, managing configuration, verifying data integrity, or repairing workflow state that an older sync pull reverted."
allowed-tools:
  - mcp__mtix__mtix_export
  - mcp__mtix__mtix_import
  - mcp__mtix__mtix_gc
  - mcp__mtix__mtix_backup
  - mcp__mtix__mtix_config
  - mcp__mtix__mtix_verify
---

# MTIX — Administration

## Context: Data Integrity is Non-Negotiable

mtix manages task hierarchies that may track safety-critical work (aviation maintenance, medical device development, mission control operations, defense systems). Every administrative operation must preserve data integrity and maintain the audit trail.

## Backup-Before-Mutate Protocol

**ALWAYS run `mcp__mtix__mtix_backup` before any destructive operation:**

- Before `mcp__mtix__mtix_import` (may overwrite existing data)
- Before `mcp__mtix__mtix_gc` (permanently removes soft-deleted data)
- Before any database maintenance
- Before major configuration changes

Backup creates a timestamped copy of the SQLite database. Store backups in a safe location outside the project directory.

## Export

Call `mcp__mtix__mtix_export` to create a JSON snapshot of all project data.

The export includes:
- All nodes with every field, including each node's annotations (comments, review verdicts, close receipts) and activity stream
- All dependencies
- All agent records
- All session records
- SHA-256 checksum for integrity verification; it covers the nodes, annotations included, and the dependencies

`.mtix/tasks.json` is the same document (`schema_version` 2.0.0 from mtix 0.5.4). An mtix client older than 0.5.4 skips such a file as newer than it supports, but its next writing command re-exports `.mtix/tasks.json` from its own store, without the annotations and changes it skipped, and committing that file reverts them upstream. Until every agent and teammate sharing a board runs 0.5.4 or later, never run writing commands on an older client and never commit its `.mtix/tasks.json`. If one was committed, restore `.mtix/tasks.json` from the last good commit in git history, or restore the store from `.mtix/data/pre-sync-backup.db` (copied over `.mtix/data/mtix.db` while no mtix process runs). A 0.5.4 store is protected by the auto-import guard (MTIX-95.31.2), which refuses a replace import that would drop annotations or nodes the store holds.

**After export:** Verify the checksum field is present. Store exports alongside backups for disaster recovery.

## Import

Call `mcp__mtix__mtix_import` with the export file path and mode:

- **merge** — creates nodes the store lacks; for a node it has, annotations and activity merge as a union (no local annotation is ever dropped, and a resolved annotation stays resolved), and the other fields take the file's values only when the node's content hash differs
- **replace** — replaces all project data with the import file, annotations and activity included; a file written before mtix 0.5.4 carries no annotations, so a replace import of it leaves every node without them

Import writes nothing when the schema version (a higher major version than this mtix writes), the node count, the checksum or any time value (RFC 3339, UTC year 1 to 9999; the required ones not empty) fails its check; the error names what failed. A refused import leaves `.mtix/tasks.json` untouched.

If an export, or the automatic import of a changed `.mtix/tasks.json`, fails because a stored node field cannot be read, the error names the node, the field and `mtix recover`: nothing was imported, and local changes are kept. Run `mtix recover` to salvage everything readable (see its report) before importing again.

**Import protocol:**
1. Run `mcp__mtix__mtix_backup` first
2. Import the data
3. Run `mcp__mtix__mtix_verify` to confirm integrity post-import
4. Check `mcp__mtix__mtix_stats` to verify expected node counts

**Never skip the post-import verification.** A corrupt import in a safety-critical environment could hide incomplete or missing tasks.

## Garbage Collection

Call `mcp__mtix__mtix_gc` to permanently remove soft-deleted nodes past the retention period (default: 30 days).

**Before GC:**
- Verify retention period is appropriate for your compliance requirements (some standards require longer retention)
- Check what will be collected — soft-deleted nodes that are past retention
- Backup first

**GC does NOT delete:**
- Active nodes (any status except soft-deleted)
- Nodes within the retention window
- Agent records or session records (these are permanent audit trail)

## Configuration

Use `mcp__mtix__mtix_config` to view or modify project settings.

Key configuration options:
- `prefix` — project prefix for dot-notation IDs
- `max_depth` — maximum hierarchy depth (default: 50)
- `auto_claim` — whether to auto-claim on creation
- `agent_stale_threshold` — heartbeat timeout for stale detection (default: 30m)
- `session_timeout` — maximum session duration (default: 8h)
- `data.soft_delete_retention` — how long to keep deleted data (default: 720h/30 days)

## Integrity Verification

Run `mcp__mtix__mtix_verify` to check content hash integrity across all nodes.

**When to verify:**
- After every import operation
- After system crashes or unexpected shutdowns
- As part of regular audit cycles
- Before critical milestones or releases
- Any time data integrity is in question

**On hash mismatch:** Do NOT modify the affected nodes. Document the mismatch, escalate, and restore from the most recent verified backup.

## Sync Recovery: Workflow State Reverted by an Older Pull

Before mtix 0.5.4, `mtix sync pull` could re-apply events this machine had already pushed, and applied claims and status changes in the order they arrived. A node could be left in an older workflow state: claim, push, `mtix done`, pull left it `in_progress` with `closed_at` still set. Upgrading stops new damage but does not change nodes already reverted. `mtix sync repair --status` finds and heals them from the local sync event log. It reads and writes only the local database and never contacts the hub.

**Recognize:** a node's status, assignee or agent_state is older than its own activity says (a later status change is recorded), typically after a pull on a client older than 0.5.4.

**List (a dry run, safe at any time):**

```bash
mtix sync repair --status          # list the nodes that differ; writes nothing
mtix sync repair --status --json   # the same list as JSON
```

Each listed node names its winning event (its newest well-formed claim, unclaim, defer or status-change event, chosen by the rule a pull applies), when and on which machine it was made, and a reason, then, per differing field, the stored and the derived value. The fields compared are status, assignee, agent_state, whether `closed_at` is set, and the progress of a node without children. The reasons:
- `replay`: the stored state is what an older event of the node writes (shown as the replayed event), which is what an older pull left. `--apply` repairs it.
- `derived fields`: the status is right; only `closed_at` or progress is out of date. `--apply` repairs it.
- `FLAGGED not a replay; review`: anything else, for example newer state that arrived by importing `.mtix/tasks.json`, a status change recorded after the winning event, or a node cancelled under a cancelled ancestor. `--apply` skips it. A clock difference between machines can also flag a genuine replay.

Left alone: nodes without claim, unclaim, defer or status-change events; a blocked node with an unresolved blocker, and a node cancelled by `mtix cancel --cascade` on an ancestor, which are not synced as events; an assignee set later with `mtix update --assignee`; the wake time of this machine's own deferral; `closed_at` after this machine invalidated or restored the node.

**Apply (only with a human's explicit go-ahead):**

```bash
mtix sync repair --status --apply
mtix sync push
```

`--apply` first writes a verified backup to `.mtix/data/backups/pre-repair-status-<UTC time>.db`; if it cannot, it stops without changing anything (free disk space and run it again). It then repairs each node that is not flagged, in its own transaction: it writes the derived state, adds an activity entry with the text `sync repair` naming the winning event, recomputes the parent's progress and re-exports `.mtix/tasks.json`. Only when the status changes does it emit a status-change event (reason `sync repair`, stamped with the winning event's time, so other machines keep their `closed_at`) and unblock dependents; that event fires `status.changed` hooks here and on every machine that pulls it.

**Flagged nodes:** never pass `--force` on your own. Show each flagged node to the human (`mtix show <id>`, the reason, the stored and derived values); if they confirm that the stored state is the damage, run `mtix sync repair --status --apply --force`, which repairs the flagged nodes too.

**Verify:** `mtix sync repair --status` lists nothing (or only flagged nodes the human chose to keep), and `mtix sync status` counts the repair events as pending until `mtix sync push` sends them.

**Recover:** to undo a repair before `mtix sync push`, stop every mtix process and copy the backup over `.mtix/data/mtix.db`. After the push a restore does not undo it, because the next pull brings the repair events back; change the node's status with the normal commands instead.
