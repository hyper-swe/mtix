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

`.mtix/tasks.json` is the same document (`schema_version` 2.0.0 from mtix 0.5.4). An mtix client older than 0.5.4 skips such a file as newer than it supports, but its next writing command re-exports `.mtix/tasks.json` from its own store, without the annotations and changes it skipped, and committing that file reverts them upstream. On such a client even an `mtix import` that fails rewrites `.mtix/tasks.json` the same way. Until every agent and teammate sharing a board runs 0.5.4 or later, never run writing commands or `mtix import` on an older client and never commit its `.mtix/tasks.json`. If one was committed, restore `.mtix/tasks.json` from the last good commit in git history, or restore the store from the backup taken before the import (`.mtix/data/backups/pre-sync-<time>.db`; `.mtix/data/pre-sync-backup.db` on 0.5.3 and earlier), copied over `.mtix/data/mtix.db` while no mtix process runs. A 0.5.4 store is protected by the auto-import guard (see "Automatic import of `.mtix/tasks.json`" below): such a file carries no annotations or activity, so its automatic import is refused and nothing changes.

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

## Automatic import of `.mtix/tasks.json`

When a `git pull` or checkout changes `.mtix/tasks.json`, the next mtix CLI command replace-imports it before it runs. The MCP server imports only when it starts, and its stderr is not shown to you. After a pull, run a CLI command that auto-imports, for example `mtix list`, then check with `mtix sync`. The MCP server protects itself too: its writes never overwrite a board that changed on disk; they import it first, or keep it and record why.

- **Applied.** An import that loses nothing prints one line: `mtix: imported the changed .mtix/tasks.json: nodes N added, N updated, N removed; dependencies N added, N removed (local database backed up to .mtix/data/backups/pre-sync-<time>.db)`. The newest 5 pre-import backups are kept; a file that fails a check or is refused takes none. A field a teammate cleared on purpose (unclaim, reopen, undefer, undelete, a later update) applies: their copy of that task holds all of its activity and is not older. The rule fails open in one direction: a teammate's copy with a later `updated_at` counts as current even when it missed your `mtix update` or `mtix delete` (neither records activity), after a concurrent edit to the same task or with a teammate's clock ahead, so a field it leaves empty is applied as cleared; the pre-import backup keeps the state before. The replace re-checks, in its own transaction, that the store is still the one it compared: a write that lands in between (another process, or the MCP server) is kept, nothing is imported, and the next command checks the file again.
- **Refused.** If the file lacks a node, an annotation, an annotation's resolution, an activity entry or a dependency the store holds, or leaves a field empty in a copy of a task that is not current (it lacks some of the task's local activity or its `updated_at` is older: a stale board, or one from an mtix client older than 0.5.4), the import is refused and nothing changes. A teammate's deliberate `mtix dep remove` is refused too. The refusal starts `mtix: auto-import of .mtix/tasks.json refused`, lists the loss node by node, and repeats on every command until someone chooses. Until then, writes save to the store but do not rewrite `.mtix/tasks.json` (one line says so). Choose, from the project root:
  - `mtix import .mtix/tasks.json --mode merge` backs up the database, keeps every value the refusal lists (every local node, annotation, activity entry and dependency, and every listed field value, with the task's local status when the value is part of it) and adds the file's changes. For a task whose content is unchanged the local field values win, so a teammate's status or assignee change to it is not applied, and the board you commit next reverts it upstream. A task the file holds under another id with the same uid (another clone renumbered it) moves there without `--confirm`; two copies with the same creation time are one task even under different uids (clones upgraded from before uids were shared), and the file's uid is adopted. A task listed as `a different task under this id` (you and a teammate each created that id, or you created it while the refusal was pending) is never overwritten: merge renumbers your task and its subtree to the next number free in both the store and the file, keeping its uid, and the file's task keeps the id. It prints the renumbering (uid, old id, new id; `--remap-file <path>` saves it) and applies it only when you rerun it with `--confirm`.
  - `mtix sync --fix` keeps the local store and rewrites `.mtix/tasks.json` from it; every change in the file is dropped.
  - `mtix import .mtix/tasks.json --mode replace` makes the file win and deletes what was listed. Run `mtix backup <file>` first.
- **Not rewritten.** When a write finds `.mtix/tasks.json` changed on disk and not imported, it keeps the file and prints one line with the way out for that kind of refusal (`mtix sync --json` shows `kind`). A write's export, including the one after `mtix import` of another file, imports a changed board first. A conflict (both the file and the store changed) whose replace would also delete local data is printed as a refusal with the loss list, and its replace option names what it deletes. A file written by a newer mtix (`newer_schema`) needs an upgrade: never rewrite it with `mtix sync --fix`. A file that fails its checks (`invalid_file`, for example merged by hand): check it, then `mtix import .mtix/tasks.json --recompute-checksum`, or keep the local board with `mtix sync --fix`.
- **Never** run the replace import to silence a refusal that lists annotations, activity entries or nodes: they hold review verdicts and close receipts. If you cannot tell whose changes are newer, stop and ask the human.
- **Verify.** `mtix sync` never prints `In sync` while an import of the board is pending or an id carries another uid in the file (`OUT OF SYNC: tasks.json changed and has not been imported`, `Another uid in tasks.json`). It prints `Auto-import: enabled (sync.auto_sync: <value>)` or `disabled`, and `Last auto-import refusal: <time> (pending|resolved <time>|no longer pending): <reason>` or `none`; `mtix sync --json` reports the same under `auto_import`. After you resolve a refusal, it must no longer read `pending`.
- **Switch.** `sync.auto_sync` (default `true`) switches only the automatic import; `mtix config set sync.auto_sync false` turns it off, and `mtix config set` accepts only true or false. `.mtix/config.yaml` is tracked in git, so the switch applies to every clone, but a store with no tasks (a fresh clone) is always imported. With it off, a pulled board is not overwritten by writes; import it with `mtix import .mtix/tasks.json --mode merge`, or keep your board with `mtix sync --fix`.

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
