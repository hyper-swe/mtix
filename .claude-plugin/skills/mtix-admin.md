---
description: "Administer MTIX project using mtix. Use when backing up data, exporting/importing tasks, running garbage collection, managing configuration, or verifying data integrity."
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

`.mtix/tasks.json` is the same document (`schema_version` 1.1.0 from mtix 0.5.4). An mtix client older than 0.5.4 refuses a file written by 0.5.4 (its import reports `checksum verification failed` and leaves its store unchanged), so every agent and teammate sharing a board must run 0.5.4 or later.

**After export:** Verify the checksum field is present. Store exports alongside backups for disaster recovery.

## Import

Call `mcp__mtix__mtix_import` with the export file path and mode:

- **merge** — creates nodes the store lacks; for a node it has, annotations and activity merge as a union (no local annotation is ever dropped, and a resolved annotation stays resolved), and the other fields take the file's values only when the node's content hash differs
- **replace** — replaces all project data with the import file, annotations and activity included; a file written before mtix 0.5.4 carries no annotations, so a replace import of it leaves every node without them

Import writes nothing when the node count, the checksum or any time value (RFC 3339, UTC year 1 to 9999) fails its check; the error names what failed.

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
