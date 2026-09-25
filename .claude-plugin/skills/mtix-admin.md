---
description: "Administer MTIX project using mtix. Use when backing up data, exporting/importing tasks, running garbage collection, managing configuration, verifying data integrity, repairing workflow state that an older sync pull reverted, or handling events that sync pull quarantined."
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

**Uid adoptions (merge).** When the file holds a task's id under another uid and one of the two uids was assigned when a clone upgraded (more than an hour after the task was created), `mtix import --mode merge` treats them as one task and gives the local task the file's uid. It lists every adoption under `uids adopted from the file` in its report on stderr, applied or not: the id, both uids and both titles (`uid_adoptions` in `--json` once the import is applied; `--remap-file` maps the local uid to the id). Check every adoption whose titles differ: two different tasks created in the same second, when at least one clone assigned its uid after the task was created (at upgrade, or as a backfill uid on import or open, for example for a task created before 0.2), are treated as one, the merge keeps the file's task, and the local one survives only in the backup the merge took first. The automatic import refuses such a pair and lists it as `treated as the same task (uid assigned at upgrade)` with both titles; for it, the merge takes the file's title and content. If the titles name two different tasks, do not merge yet: show both to the human, copy the local task to a new one (`mtix show <id>`, then `mtix create` with its title and description; writes stay local while the refusal is pending), then merge. If a merge already took it, restoring the backup drops every change made since the merge: recover right away, or first note the later changes and redo them afterwards. The human stops every mtix process that uses this store (the MCP server, `mtix serve`, a running `mtix daemon`), copies the backup over `.mtix/data/mtix.db` and deletes `.mtix/data/mtix.db-wal` and `.mtix/data/mtix.db-shm` (they belong to the replaced database and must not be replayed onto the backup), then, before any mtix command runs, restores the pulled board (`git checkout -- .mtix/tasks.json`, or the file from the pulled commit), because the merge's own export rewrote it. The next command refuses again; copy the task as above, then run `mtix import .mtix/tasks.json --mode merge --confirm`. mtix gives every task it imports without a uid a backfill uid (a UUIDv8 with a fixed marker), and a store gives its tasks without a uid one when it opens, so boards written by 0.5.4 carry a uid for every task, and two clones that each backfilled a task keep one task. A board without uids does not lose a backfill uid: the missing uid is no loss, and the replace keeps the local uid, so a teammate's boards from a client at 0.3 or older keep importing (a uid given at creation that such a board leaves out is still a loss). When the file's copy has no uid to compare (a board from a client at 0.3 or older, or one never re-exported after an upgrade), the titles decide: another title is a different task, which the automatic import lists as `a task under this id with a different title and no uid to compare` and the merge renumbers with `--confirm` (listed under `of these, a task under this id with a different title and no uid to compare`); if a teammate only retitled it, the merge keeps both copies, and the renumbered one holds the comments, activity and field values the board lacked: the human moves them to the task at the id, or keeps the renumbered copy, before anything is deleted. The opposite case: two copies with identical titles listed as `a different task under this id` (every uid the task carries was assigned within an hour of its creation) are one task; the merge renumbers the local copy only with `--confirm`, and the human decides whether to delete the duplicate.

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
  - `mtix import .mtix/tasks.json --mode merge` backs up the database, keeps every value the refusal lists (every local node, annotation, activity entry and dependency, and every listed field value, with the task's local status when the value is part of it) and adds the file's changes. For a task whose content is unchanged the local field values win, so a teammate's status or assignee change to it is not applied, and the board you commit next reverts it upstream. A task the file holds under another id with the same uid (another clone renumbered it) moves there without `--confirm`; two copies under different uids are one task only when they share a creation time and one uid was given to the task more than an hour after it was created, by a clone that upgraded from before uids were shared (the file's uid is then adopted); every other pair, such as tasks two clones created in the same second, stays two tasks; without a uid to compare, another title is another task. A local task whose id the board gives to another local task (a clone moved it there) is listed as a different task too, and the merge renumbers it with `--confirm`. A merge that would change nothing takes no backup. A task listed as `a different task under this id` (you and a teammate each created that id, or you created it while the refusal was pending) is never overwritten: merge renumbers your task and its subtree to the next number free in both the store and the file, keeping its uid, and the file's task keeps the id. It prints the renumbering (uid, old id, new id; `--remap-file <path>` saves it) and applies it only when you rerun it with `--confirm`.
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
- `sync.max_lamport_jump` — how far above the local Lamport clock a pulled event may be stamped before `mtix sync pull` quarantines it (default: 4294967296, that is 2^32; a positive integer; see Sync Recovery: Quarantined Pulled Events)
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

**Pull first, always.** Run `mtix sync pull` before listing and again just before `--apply`. A repair that changes the status emits an event stamped with this machine's newest Lamport clock; on a log that lacks a teammate's newer, unpulled change, that event can win over the change (whenever this machine's Lamport clock is ahead of it) and revert it on every machine that pulls it. A 0.5.4 pull no longer replays this machine's own events, so pulling is safe.

**List (a dry run, safe at any time):**

```bash
mtix sync pull                     # bring in every teammate's change first
mtix sync repair --status          # list the nodes that differ; writes nothing
mtix sync repair --status --json   # the same list as JSON
```

When it lists differences, the dry run ends with a reminder to pull first (the `reminder` field in `--json`, omitted when there is no reminder).

Each listed node names its winning event (its newest well-formed claim, unclaim, defer or status-change event, chosen by the rule a pull applies), when it was made, whether this machine or another machine made it, and a reason, then, per differing field, the stored and the derived value. The fields compared are status, assignee, agent_state, whether `closed_at` is set, and the progress of a node without children. The reasons:
- `replay`: the stored state is what an older event of the node writes (shown as the replayed event), which is what an older pull left. `--apply` repairs it.
- `derived fields`: the status is right; only `closed_at` or progress is out of date. `--apply` repairs it.
- `FLAGGED not a replay; review`: anything else, for example newer state that arrived by importing `.mtix/tasks.json`, a status change recorded after the winning event, a difference only in the assignee or agent_state (a later `mtix update --assignee` leaves no trace), or a cancelled node whose ancestor was cancelled after its winning event (possibly a cascade cancel). `--apply` skips it.

**Clock skew misleads the check in both directions.** When this machine's clock is behind a teammate's, a genuine replay can be flagged. When it is ahead, a teammate's newer state can be listed as a `replay`, and `--apply` would revert it. Before `--apply`, check each replay's winner time and whether this machine or another machine made it, and show any doubtful node to the human (`mtix show <id>`).

Left alone: nodes without claim, unclaim, defer or status-change events; a blocked node with an unresolved blocker, and a node cancelled by `mtix cancel --cascade` on an ancestor without a cancel event of its own, which are not synced as events; an assignee set later with `mtix update --assignee` (flagged when it arrives by import); the wake time of this machine's own deferral; `closed_at` after this machine invalidated or restored the node.

**Apply (only with a human's explicit go-ahead):**

```bash
mtix sync pull                     # again, just before applying
mtix sync repair --status          # list again; apply only what is still listed
mtix sync repair --status --apply
mtix sync push
```

`--apply` first writes a verified backup to `.mtix/data/backups/pre-repair-status-<UTC time>.db`; if it cannot, it stops without changing anything (free disk space and run it again). It then repairs each node that is not flagged, in its own transaction: it writes the derived state, adds an activity entry with the text `sync repair` naming the winning event, recomputes the parent's progress and re-exports `.mtix/tasks.json`. Only when the status changes does it emit a status-change event (reason `sync repair`, stamped with the winning event's time so other machines keep their `closed_at`, or with the current time if that time is in the future, since `mtix sync push` refuses an event more than a day ahead) and unblock dependents; that event fires `status.changed` hooks here and on every machine that pulls it.

**Flagged nodes:** never pass `--force` on your own. Show each flagged node to the human (`mtix show <id>`, the reason, the stored and derived values); if they confirm that the stored state is the damage, run `mtix sync pull`, then `mtix sync repair --status --apply --force`, which repairs the flagged nodes too.

**Verify:** `mtix sync repair --status` lists nothing (or only flagged nodes the human chose to keep), and `mtix sync status` counts the repair events as pending until `mtix sync push` sends them.

**Recover:** to undo a repair before `mtix sync push`, stop every mtix process and copy the backup over `.mtix/data/mtix.db`. After the push a restore does not undo it, because the next pull brings the repair events back; change the node's status with the normal commands instead.

## Sync Recovery: Quarantined Pulled Events

`mtix sync pull` checks every event it receives before applying it: first its Lamport clock (below 2^53, and at most `sync.max_lamport_jump` above the local clock; default 4294967296, that is 2^32), then the same size, shape and id rules the hub applies at push (payload at most 64 KB and 10 levels deep, at most 100 vector-clock entries, well-formed author, machine and project ids, a hub row that decodes). An event stamped more than 24 hours ahead of this machine's clock is applied with a `WARN`. Each event applies on its own: an event that fails a check or its apply (for example a dependency whose target has not arrived) is rolled back and kept in the local table `sync_quarantine` (in `.mtix/data/mtix.db`), and the pull goes on. A quarantined event never changes the local clock and is never lost, and one refused for its clock never moves the pull cursor. Every pull retries the quarantine at its start, before it contacts the hub, and again at its end when it applied events; an event that applies, or that the store has already applied, leaves the quarantine. Retries are local and add no hub query, so they do not keep a scale-to-zero database awake; an event refused for its clock is downloaded again by each pull.

**Recognize:** `mtix sync doctor` fails its `quarantined events` check (exit 2; with `--json` the check has `"pass": false` and the detail); `mtix sync status` shows `quarantined events` above 0 (`quarantined_events` in `--json`); a pull prints `quarantined event <id> (<pass> pass): <reason>` on stderr and `quarantine: N pulled events held, not applied` at the end.

**Routine:**
1. Run `mtix sync pull` again. Events that were waiting for a task or a dependency target apply once it has arrived.
2. If `mtix sync doctor` still fails the check, list the quarantine (read-only; it changes nothing and does not contact the hub):
   ```bash
   mtix sync quarantine list          # event id, node, op, attempts, first seen, last attempt, reason
   mtix sync quarantine list --json   # the same as an array, plus lamport_clock, source and cli_version
   ```
   `reason` is why the event was first quarantined; `attempts` counts every failed try; `source` is `pull` (the cursor pass) or `sweep` (the late-event sweep).
3. Read each reason:
   - `not found` or `FOREIGN KEY`: the event needs a task or dependency target this replica does not hold yet. Ask the teammate who made it to run `mtix sync push`, then pull again.
   - `envelope validation` (payload too large or too deep, a vector clock over its caps, an id that does not match its grammar, a hub row that does not decode): the hub holds an event that a correct client never pushes. It will not apply by itself. Show the human the event ids and reasons, for whoever runs the hub.
   - `lamport_clock at or above 2^53`: the event's clock is past the overflow guard. It will never apply; show it to the human, for whoever runs the hub.
   - `sync.max_lamport_jump`: the event's Lamport clock is far above this replica's. If the human confirms the hub's clocks are legitimately that high, raise the bound: `mtix config set sync.max_lamport_jump <positive integer>` (only a positive integer is accepted; `mtix config delete sync.max_lamport_jump` restores the default). The next pull retries the event with the new bound.

**Verify:** `mtix sync quarantine list` prints `no quarantined events`, `mtix sync doctor` passes the `quarantined events` check, and `mtix sync status` shows `quarantined events 0`.

**Recover:**
- **A refused clone on a fresh store:** run `mtix sync pull` alone. It quarantines the event and applies the rest. Clone has no quarantine: it runs the same checks on every hub event before it writes anything and refuses the whole clone, naming the event and the reason, while the hub holds an event that fails them; a clone that completes empties the quarantine because every hub event passed.
- **Rebuilding a store that already holds sync state:** `mtix sync reconcile --discard-local --yes` deletes local tasks and unpushed changes, and empties the quarantine with the rest of the local sync state. Its dry run (without `--yes`) shows only the node count, not what would be lost. Before `--yes`: run `mtix sync push`, confirm `mtix sync status` shows `pending` 0, and get a human's go-ahead. Then run it, and `mtix sync pull`, which runs the checks and quarantines again any event that still fails.
- **A quarantined copy of this replica's own event** (its hub row was changed after the push) does not clear by itself, even after the hub row is repaired: every retry re-checks the stored copy. Recover in this order: whoever runs the hub repairs the hub row first; run `mtix sync push` and confirm `mtix sync status` shows `pending` 0; then, with a human's go-ahead, `mtix sync reconcile --discard-local --yes` and `mtix sync pull`. Discarding before the hub row is repaired drops this replica's own true copy of the event, which then comes back only as a quarantined hub event.

**Never:**
- Never delete rows from `sync_quarantine` or edit the local database to force an event in; a quarantined event is the only local copy of what the hub sent.
- Never raise `sync.max_lamport_jump` without a human's confirmation: the bound is what keeps one extreme event from pushing this replica's clock to where the hub refuses all its later changes.
- Never try to get past a refused `mtix sync clone` by any means other than `mtix sync pull` on the fresh store; the refusal is what keeps an event the pull would quarantine out of the store.
- Never run `mtix sync reconcile --discard-local --yes` without first running `mtix sync push`, confirming `mtix sync status` shows `pending` 0 and getting a human's go-ahead: it deletes local tasks and unpushed changes.
- Never copy quarantined events into tickets or shared documents beyond the event ids and reasons; the raw events hold task content.
