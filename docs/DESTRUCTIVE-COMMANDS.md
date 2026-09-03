# Destructive commands: the inventory and the rule

> Status: normative. Owner: MTIX-90. Re-audit whenever a command gains a
> `DELETE FROM` on `nodes` or `sync_events`, locally or on a hub.

Losing a team's ticket history once is unrecoverable trust damage, so mtix
treats any command that bulk-deletes tickets as a different class of
operation from everything else it does. This document states the rule,
lists every path that deletes tickets, and records the position on each
path that is not gated, so the audit is never silently partial.

## The rule

A command that bulk-deletes tickets, or is otherwise irreversible, must:

1. **Say exactly what it will destroy** before doing anything: the
   operation, the scope (store path and its projects, or the hub) and the
   exact row counts (tickets, journal events).
2. **Require a confirmation no flag can satisfy.** The operator types the
   ticket count shown in the warning. `--yes` and `--force` select
   execution over preview; they never answer the prompt. A count, unlike a
   fixed word, cannot be baked into a script or an alias, and unlike a
   project prefix it is unambiguous for a store holding several projects.
3. **Refuse when stdin is not a terminal.** mtix is agent-native and runs
   in CI, and this is the deliberate consequence: a command that deletes
   every ticket has no non-interactive form. Automation that reaches one
   fails closed with instructions instead of wiping a store. There is no
   environment variable or flag that lifts this.
4. **Snapshot first.** A verified copy of the database (`VACUUM INTO`, the
   same copy `mtix backup` makes) is written to
   `.mtix/data/backups/pre-<operation>-<time>.db` after the confirmation
   and before the first delete. If the snapshot cannot be written the
   command aborts: the usual cause is a full disk, which is exactly when a
   delete must not run. Snapshots use a prefix the rolling-backup rotation
   does not match, so they are never removed automatically.
5. **Never be recommended as a routine remedy** in an error message, help
   text, document or release note.

When there is nothing to destroy (every count is zero) the command says so
and proceeds without a prompt or a snapshot. That is the `mtix recover`
path: a replace-import into a fresh, empty project.

The implementation is `guardDestructive` in `cmd/mtix/destructive_confirm.go`.
The gate lives in the CLI layer on purpose. The store functions underneath
(`sqlite.DiscardLocal`, `Store.Import` in replace mode) stay ungated
because the e2e harness and the automatic import call them; any new CLI
wrapper around them must call the guard before its first delete.

## Inventory

Audited on 2026-09-03 by searching every non-test `DELETE FROM` in the Go
sources and every command surface that reaches one. Re-run the search
when auditing again:

```sh
grep -rn "DELETE FROM" --include='*.go' . | grep -v _test.go
```

### Gated

| Command | What it deletes | Confirmation |
|---|---|---|
| `mtix sync reconcile --discard-local --yes` | Every row of `nodes` and `dependencies` (all projects, soft-deleted included), the whole sync journal (`sync_events`, `sync_conflicts`, `applied_events`), the inbox and hook bookkeeping, and `sync_projects`; then adopts hub state. | Typed ticket count at a terminal; snapshot `pre-discard-local-<time>.db`. |
| `mtix import --mode replace <file>` | Every row of `nodes`, `dependencies`, `sessions`, `agents` and `sequences`, then inserts the file's content. Any ticket not in the file is gone. | Typed ticket count at a terminal; snapshot `pre-import-replace-<time>.db`. Skipped when the store is empty. The file's checksum is verified before the prompt so a rejected file never asks for a confirmation. |

### Not gated, with the recorded position

| Path | What it does | Position |
|---|---|---|
| Automatic import on every command (FR-15.2, `SyncService.AutoImport`) | When `.mtix/tasks.json` differs from the last export this process wrote, the store is rebuilt from the file in replace mode: the same `DELETE FROM nodes` as above, inside whatever command was running. | **Documented exposure, not gated.** It runs inside every command, including read-only and non-interactive ones, so it cannot prompt. It is reversible: the state it replaced is the previous tracked export in git history, and the rolling backups in `.mtix/data/backups/` hold recent copies. The known failure is checking out a branch whose export is older than the database and then running any mtix write, which re-imports the older board; the procedural rule is to run mtix writes only on the branch whose export is current. Gating this path would mean gating every command. |
| `mtix gc` and the daemon retention sweep (FR-3.3a, `BackgroundService`) | Permanently deletes rows that are already soft-deleted and older than `data.soft_delete_retention` (default 30 days). | **Not gated by design.** Only rows an operator already deleted with `mtix delete`, and only after the retention window, which is the undo period (`mtix undelete`). The sweep runs unattended in the daemon and cannot prompt. |
| `mtix delete [--cascade]` | Soft-deletes a node, or a subtree with `--cascade`. | Reversible with `mtix undelete` until retention expires. Not bulk in the irreversible sense. |
| `mtix sync migrate --yes` | Records renumber remaps and collisions on the hub and adds the registry index. | No row is deleted. Renumbers are recorded in `node_renumber_remaps` and listed by `mtix sync conflicts`. `--yes` selects apply over preview. |
| `mtix sync reconcile --rename-to` / `--import-as` | Rewrites node IDs; writes `.mtix/id-rename-map.json`. | No row is deleted; reversible by renaming back; the map records every change. |
| `mtix sync backfill --force` | Appends a second history alongside the first. | Deletes nothing. MTIX-89 tracks a supported regenerate path; when built it must not delete nodes. |
| `mtix sync conflicts resolve` | Resolves one conflict by LWW. | One row at a time. `--batch-all` is not implemented; when built it must follow this rule (SYNC-DESIGN §11). |
| `mtix sync relay retire-peer`, `reset-peer`, relay pruning (FR-21) | Removes relay segments after quorum acknowledgement, or bumps a publisher epoch. | Relay segments are transport; every store keeps its canonical data. Retiring a peer that later returns is covered in FR-21. |
| `mtix sync mark-restored` | Bumps the hub restore epoch. | Deletes nothing. |
| `mtix sync clone` | Applies hub history into an empty local journal. | Refuses when the local journal is non-empty. |
| Hub-side reset (MTIX-89 criterion 4) | Would run `DELETE FROM sync_events WHERE project_prefix = ...` on a shared hub. | **Not built.** MTIX-92's uid repair removes the need for it. Any future hub-side delete must use this gate with the hub named in the scope line, and is less forgiving than a local one because other clients depend on the rows. |
| HTTP API and MCP server | | Neither exposes a route or tool that reaches `DiscardLocal` or a replace-mode import (verified by search on the audit date). Keep it that way: a server cannot prompt. |

## Recovering from a confirmed mistake

Every gated command prints the snapshot path before it deletes. To roll
back, stop any daemon, back up the current database if it has changed
since, then copy the snapshot over `.mtix/data/mtix.db` and restart. The
snapshot is a complete, verified SQLite database.
