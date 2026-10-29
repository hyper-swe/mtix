---
description: "Administer MTIX project using mtix. Use when backing up data, exporting/importing tasks, running garbage collection, managing configuration, verifying data integrity, restricting who can use a sync hub's tables (mtix sync harden), backing up or restoring a sync hub (mtix sync backup), resolving sync conflicts, repairing workflow state that an older sync pull reverted, handling events that sync pull quarantined, tasks that share a uid (doctor check unique node uids), events that sync push holds because a field is over the sync limit, or a sync push pending queue that never drains."
allowed-tools:
  - Bash(mtix *)
  - mcp__mtix__mtix_stats
---

# MTIX — Administration


## Host wake settings

This build stores and reports `wake.live` and `wake.max_hop_depth`.
Live wake control, the agent hop limit and `MTIX_WAKE_HOP` delivery take effect
when the wake scanner ships. They do not control live delivery in this build.

The wake scan core ships in this build, but nothing constructs or runs it yet:
no delivery route exists before a later release. So in this build no scan runs,
no registration is marked stale by a scan and no stale registration is expired by
one, and no agent is woken live.

**When the scanner is available, stop live wakes on this host with `mtix config set wake.live off`.**
For one agent use `mtix config set wake.live off --agent worker`.
The stored live-wake default is on, regardless of session approval mode.
You should not need to tune these settings: use the normal handle, reply and ack routine.

These keys belong to this host's operator-local `wake.json`, never to
`.mtix/config.yaml`. The host-bound JSON envelope stores scopes in
`data.host` and `data.agents`. Without `--agent` the config commands select
the host scope; `--agent worker` selects that agent's override. An omitted
agent key inherits the host value, then the default. Effective host live-off
dominates an agent live-on override; the scanner will enforce that value. Settings in a committed
workspace file are ignored and reported; they never grant wake authority.
Existing flat envelope and strict-identity settings remain readable.

| Key | Default | Meaning |
|---|---|---|
| `wake.live` | `true` | Live wake on/off setting for the scanner |
| `wake.wakes_per_hour` | unset, unlimited | Enforced by the callable dispatcher: recipient wakes across boards per rolling hour |
| `wake.per_sender_per_hour` | unset, unlimited | stored, not yet enforced: sender events per hour |
| `wake.remote_awake_daily_cap` | unset, unlimited | stored, not yet enforced: remote-induced awake seconds per day |
| `wake.reply_extensions_per_hour` | unset, unlimited | stored, not yet enforced: reply-window extensions per hour |
| `wake.envelope_max_events` | unset, unlimited | Events in one pulled envelope |
| `wake.envelope_max_body_bytes` | unset, unlimited | Bytes per event body |
| `wake.envelope_max_total_bytes` | unset, unlimited | Total envelope bytes |
| `wake.max_hop_depth` | `32` | Scanner hop guard; depth 32 will be allowed, 33 held |
| `wake.backoff_after_unacked` | `5` | Enforced by the callable dispatcher: durable unacknowledged-wake backoff |
| `inbox.strict_board_identity` | `false` | Also require matching hub identity |

Read a value with `mtix config get wake.max_hop_depth`, or append `--agent worker`.
Set an optional limit with `mtix config set wake.wakes_per_hour 12`.
Optional limits accept a nonnegative whole number; `0`, `off` or `unset`
means unlimited. Guards are always on: zero, negative, empty and off values
are refused. Tightening a limit, enabling strict identity or turning live
wake off needs no terminal. Loosening a limit, disabling strict identity or
turning live wake on requires terminal stdin. Deleting an override follows
the same rule when the inherited value would loosen it.

`mtix doctor` and `mtix doctor --json` report effective values and their
source under `wake.settings`, including configured agent overrides.
`settings.ignored-workspace-key` lists ignored workspace keys and exact
`mtix config set` remedies. Remove those keys from `.mtix/config.yaml` and
configure desired values operator-locally. Doctor is read-only, including
with `--fix`; it never changes these settings or grants permission.

Addressed-comment payloads carry optional nonnegative integer `hop` context;
missing means zero; malformed sync metadata warns and is read as zero without
withholding the comment. When the scanner ships, a wake exec child receives
`MTIX_WAKE_HOP`, the maximum
hop of its covered events, and `MTIX_WAKE_ATTEMPT`, an audit-only attempt token.
Its addressed sends carry one more hop. Other sends start at zero; malformed
process context logs a warning and starts at zero without failing the send.
Context injected by a hook into an already-running session cannot change that
session's environment, so its sends start at zero. This is an accident net,
not an adversarial control. The scanner's held diagnostic will name the depth and
exact configuration command; changing a guard never acknowledges the event.

These settings do not install a wake route. The callable dispatcher enforces
recipient budgets and durable backoff; held wakes re-arm in the same dispatcher; scanner discovery remains
a separate feature. A configured limit of 12 holds the 13th attempt in a rolling
hour, before claim or send, across boards for the same agent, host and user.
Duplicate claims and held events spend no allowance; transport failures do.

After five consecutive wakes without a new acknowledgement, the next wake
waits 1 minute; each further unacknowledged attempt doubles the delay up to
30 minutes. Ack after handling keeps backoff quiet. A new acknowledgement on
any board resets this host recipient's backoff, including a lower selective
sequence or newly covered cumulative prefix. Repeating an already acknowledged
event or prefix does not reset it. Acknowledgements do not return rate allowance.

A local board identity keeps moved boards and ordinary database copies on the
same protected counter floor, including moves back to an earlier path. Each
fresh fenced attempt still spends one credit. The identity stays local; exports
and sync do not carry it. Identity-less restores at an unseen path, and legacy
mirrors without an identity, may under-count history, never over-count. They
establish an uncharged history baseline while retaining existing recipient
credits and unacked debt. An identity-less database at a previously used path
is treated as that path's board and can charge its own history. Doctor reads
this state without creating an identity.

The protected mirror retains spent allowance across restarts and store restores.
`budget.rewind` in `mtix doctor` text and JSON reports a lower local counter or
retained rewind diagnostic. It is read-only, even with `--fix`; doctor never
repairs counters or resets allowance. Interrupted accepted attempts remain
conservatively charged. Budget and backoff holds retain inbox events and name
their cause and exact configuration command.

A held wake (loop guard or recipient budget) never advances the registration's own
high-water mark, and later events of that registration wait behind it so none is skipped.
Every pass of the callable dispatcher re-checks the hold. Once a dispatcher is wired in, a
grant change, a `wake.*` setting change and an inbox call also re-check the affected agent's
held wakes; there is no timer or background loop, and a failed re-check never fails your command.
This build wires no dispatcher yet, so those activities start no pass and nothing is delivered
automatically; the first pass after you loosen the setting
(for example the printed `mtix config set wake.max_hop_depth <n>`) wakes the held event,
and the registration continues from its own mark. Events at or below the mark never wake.
`wake.guard-trips` in `mtix doctor` text and JSON lists recent loop-guard trips (hop depth
and back-off), newest first, with agent, chain, time and the exact fix command; JSON rows
are under `data.trips`. It is read-only, even with `--fix`. You should not need to tune
these guards; an empty list is the normal state.

## Health checks

Run `mtix doctor` for registered checkout and host diagnostics, or
`mtix doctor --json` for an array containing exactly `id`, `status`, `detail`
and `fix` on every row. Status is `pass`, `warn`, `fail` or `info`. Exit 0
means no FAIL; WARN and INFO are advisory. Exit 2 means at least one FAIL.
An empty applicable registry is `[]` in JSON. Windows reports
`platform.windows` as INFO because built-in wakes are unavailable.

On an opened local board, `wake.ledger` reports retained wake rows, claimed
rows and rejected completions as INFO, including when all counts are zero.
A board requiring a dispatch protocol newer than this binary supports reports
FAIL. Install a compatible binary, restart the dispatcher and rerun doctor;
never lower the board's protocol floor to bypass refusal. This check is
read-only: `mtix doctor --fix` does not prune wake rows, reset claims, lower
the protocol floor or grant permissions.

The sync result rows use a `sync.` prefix and run only for configured
transports. A relay-only checkout does not run unused Postgres checks.
The separate `mtix sync doctor` command keeps its existing report and behavior.

Plain `mtix doctor` preserves operator-local files, including stale attach shims
and records. Use `mtix doctor --fix` for registered shim and glue repairs;
other commands retain automatic attach refresh.

Use `mtix doctor --fix` only when you want explicitly registered non-grant
repairs to run; each successful repair is checked again. Grant commands and
other advisory remedies are printed for you to run yourself, never executed
by this flag. Existing sync checks currently register no automatic repairs.
Follow each printed remedy and rerun doctor. All report fields and diagnostics
are redacted before output; never put credentials into a command argument.

The read-only `docs.stale` check compares the board-root `MTIX.md` with
this binary's generated instructions and checks live native pointer lines.
Missing or stale instructions and missing or duplicate pointers produce WARN.
Fenced and indented examples do not count as live pointers. `HYPERSWE.md`
suppresses the missing-pointer requirement; existing pointers are allowed and
`MTIX.md` is still checked. Follow the exact printed command,
`mtix -C <board-root> docs generate`, then rerun doctor. Generation keeps the
first live owned pointer and removes later duplicates, preserving other bytes.
This check never writes documents or Git exclusions, including with `--fix`.

Every write (for example `mtix create`) re-exports `.mtix/tasks.json` in this
binary's export schema: `2.0.0`, or `2.1.0` with `history.export`. The first
write after an mtix upgrade can change a committed file's `schema_version` and
add fields, which breaks a reader that rejects unknown fields.
`sync.auto_sync` does not stop this re-export: it turns off automatic import
only. The read-only `export.schema` check reports PASS when the file declares
the schema this binary writes and WARN with the fix when it declares another.
To upgrade deliberately, run `mtix sync` and, when it reports no pending import,
`mtix sync --fix`, then commit `.mtix/tasks.json` and update the strict reader.
To keep the older schema, write to the board only with the mtix version that
wrote it. When the file declares a NEWER schema than this binary writes, the
fix says to upgrade mtix instead. The check never writes the file, including with `--fix`.

Setup completion (`setup.done`, MTIX-112.19.2) remains a FUTURE registration.
Do not infer that setup is current from the absence of that row.

### Attach checks

The operator-local directory can hold release-defined attach artifacts. The
attach shim (`mtix-shim`) is a POSIX shell script that records the unresolved
mtix path and the operator's uid. On every run, with a fixed `PATH`, it walks
that path one component at a time from `/`, following at most 32 symbolic
links (to files or directories) and resolving `..` physically. It refuses
unless every directory it enters (so every ancestor and the directory holding
each link), every link and the final file are owned by the operator or root
and not writable by group or others (a root-owned sticky directory such as
`/tmp` is allowed). It then runs the fully resolved path it checked, never a
path through a link. The Go check behind `setup.shim` applies the same rule.
This version installs no shim, harness glue or host
dispatcher service; it checks and repairs those artifacts when they exist.
Doctor shows these rows only when they apply:

- `setup.shim`: PASS when the shim matches this release. WARN with the fix
  `mtix doctor --fix` when its bytes are out of date; the repair rewrites
  only the shim and its record. FAIL when its target is refused or its record is
  missing; the fix names the refused component (for example `chmod go-w`
  on the writable directory or file, or removing a link owned by another
  user). Windows has no shim,
  and `platform.windows` says so.
- `glue.<harness>`: harness glue called mtix with an attach protocol other
  than this release's. The previous protocol is still served and the glue is
  marked for refresh; any other protocol makes that session inbox-only, so
  read messages with `mtix inbox`. The fix names `mtix doctor --fix` only
  when this build can rewrite that glue; glue newer than this mtix needs an
  mtix upgrade (compare `mtix version`), and older glue this build cannot
  rewrite must be corrected or removed in the harness configuration. The
  attach verb's stderr line and alert name the same fix.
- `attach.failures`: recent `attach.protocol-skew` and `attach.verb-failure`
  operator alerts from the hidden attach verbs, with counts. These verbs are
  entry points for harness glue, not for agents. They always exit 0, even on
  arguments they cannot parse or a verb this build does not know, print one
  line on stderr and nothing on stdout, and record every failure as an
  operator alert that carries its own fix (for example the glue fix above for
  protocol skew, or upgrading mtix for an unknown verb).
- `attach.state`: INFO when operator state cannot be read at all (for example
  `HOME` is unset); the attach rows are skipped and the row names the remedy.
  Doctor itself still runs.
- `skew.dispatchers`: a host dispatcher heartbeat on this board that is older
  than this mtix, or below the board's `min_dispatch_protocol`. This version
  installs no dispatcher service, so mtix cannot restart it; the fix names
  the PID to stop and start. `mtix agent register` prints the same notice.
  It never restarts a dispatcher from inside a harness session, and restarts
  one at most once per hour. Until a restart has really happened, every check
  reports the older dispatcher again and raises an operator alert; mtix never
  claims a restart it did not make.

Unsandboxed commands other than plain `mtix doctor`, and registered
`mtix doctor --fix` repairs, rewrite an out-of-date shim without a terminal. These repairs never change a grant,
approval or confirmation.

## Trust and grants

On a board never connected to a hub or relay, registered ids are implicitly wakeable unless explicitly denied. A hub or relay board requires one confirmation per agent, board and host. To confirm an id, run `mtix agent allow dev`; terminal stdin is required. For automation use `mtix agent allow dev --yes`. The beta accepts this under harness session markers, records their names without values and flags them in doctor; it does not refuse the confirmation. `mtix agent deny dev` needs no terminal and explicitly holds that id, including on local-only boards. Both commands assign a missing board identity when upgrading a legacy board. You can allow a valid id before registration: its preconfirmation stays pending for this host, board and exact id. It becomes usable when that id registers here, without a second confirmation; denial revokes it immediately. On a hub board, a preconfirmation also needs the same known hub identity. Doctor shows the current pending registration count and each exact `mtix agent deny ID` revoke command.

Grants and the protected `confirmations.jsonl` log live in the operator-local mtix configuration directory, outside the project. The log records action, source, time, mode and harness marker names with private file access. `mtix doctor` and `mtix doctor --json` show `agents.unconfirmed` with the real allow command and `agents.confirmations` with the retained audit history. `mtix doctor --fix` never confirms an id. Uncommitted allows stay ineffective while publication is pending; accepted denies remain effective. Use `mtix agent permissions recover` to list stuck batches and `mtix agent permissions recover --cancel <batch-id>` to cancel one with durable audit. Cancellation preserves prior committed grants and accepted denies, and permanently refuses the cancelled caller id. A new operator deny cancels older uncommitted allows for that id and quarantines other pending allows so unavailable publication cannot block denial. Removed notice targets are retained as `undeliverable`. Recovery never grants permission. A legacy pending allow without a committed preimage becomes a quarantine hold, separate from an operator deny. It stays held after cancellation and registration, and file requests leave it unchanged with `quarantined_requires_operator`. Only a fresh `mtix agent allow <agent-id>` confirmation in that board checkout clears the hold (use `--yes` for automation). Doctor reports each hold and its exact clearing command; `mtix agent permissions recover` lists pending batches and holds. A completed receipt stays committed if only cleanup fails and cannot be cancelled. The reusable file batch API remembers supplied ids and accepted time/registration metadata, so identical retries never regrant an older allow after a later deny. Its protected `permissions-replays.json` retains at most 1024 batches within 1 MiB. New batches evict the oldest completed receipts by completion time; pending batches are never evicted. Retained completed ids replay their accepted result, while an evicted completed id is treated as a new request. Cancelled or quarantined ids remain terminal in separate protected audit records even after replay-receipt eviction. File-mode allow never clears an explicit deny: that agent is unchanged with outcome `denied_requires_operator`, and the rest of the batch applies. Only the actual operator command `mtix agent allow <agent-id>` at a terminal, or `--yes` with logged harness markers, clears the deny. Doctor shows receipt count and bytes and retained denied file-allow outcomes. Explicit denials are informational with their time and source; doctor gives no allow remedy for an operator denial. The reusable `PermissionReplayResult` accessor returns a complete retained completed batch or an explicit unavailable result after receipt eviction; it never reconstructs partial counts from rotated history. There is no apply/setup command for this API.

Each committed change batch publishes one unaddressed neutral board line with a separately generated UUIDv7 event identity, independent of caller batch ids: wake permissions changed on a host (see that host's mtix doctor). It contains no host or agent id, path or user name; ordinary sync provenance remains unchanged. An empty board receives no new task or line: the local log and doctor show `skipped_no_node`. When a board first becomes hub-connected, implicitly eligible ids become held and operator-local Alerts report the required confirmation.

Grants match board identity by default; optional strict hub-identity matching remains off by default. No sender allowlist is added in this beta: share a hub only with trusted principals. The reusable file-batch API supports orchestration; the full setup and file-application commands belong to a future setup release and are not shipped here. Confirmation selection facts remain separate and start unchecked.

## Local coordination schema

Coordination tables are added locally without increasing the database schema version. The released v0.5.4-beta client was verified for `list`, `create`, and `sync pull`, with these additive local coordination tables preserved. Newly created stores have a stable random `store_instance_id`; reopening a store never replaces it, and upgrading a legacy store does not assign one automatically. The `min_dispatch_protocol` metadata key is reserved for planned dispatch compatibility; these schema foundations do not enable an attached dispatcher.

## Pending mirror export diagnostics

`mtix sync status` and `mtix sync doctor --json` report local `pending_exports.count`, `oldest_at` and `oldest_age_seconds` without importing, draining or contacting the hub for that check. Doctor warns after one hour and prints `mtix list`: it retries protected import-first mirror recovery, and a pulled-board refusal still needs its printed resolution. Then verify with `mtix sync status`. Do not delete markers or recommend `mtix sync --fix` automatically; it intentionally discards the pulled board. New clients coalesce requests into one durable metadata record, while legacy files can temporarily add to the count and conservative age; old clients can keep creating them. This bounds live intent metadata, not the WAL. See the sync skill's Pending mirror exports section for cutoff, durability and upgrade limits.

## Context: Data Integrity is Non-Negotiable

mtix manages task hierarchies that may track safety-critical work (aviation maintenance, medical device development, mission control operations, defense systems). Every administrative operation must preserve data integrity and maintain the audit trail.

## Backup-Before-Mutate Protocol

**ALWAYS run `mtix backup <path>` before any destructive operation:**

- Before `mtix import <file> --mode merge` (may overwrite existing data)
- Before `mtix gc` (permanently removes soft-deleted data)
- Before any database maintenance
- Before major configuration changes

Backup creates a timestamped copy of the SQLite database. Store backups in a safe location outside the project directory.

## Export

Run `mtix export > <file>` to save a JSON snapshot of all project data.

The export includes:
- All nodes with every field, including each node's annotations (comments, review verdicts, close receipts) and activity stream
- All dependencies
- All agent records
- All session records
- SHA-256 checksum for integrity verification; it covers the nodes, annotations included, and the dependencies

`.mtix/tasks.json` is the same document (`schema_version` 2.0.0 from mtix 0.5.4). An mtix client older than 0.5.4 skips such a file as newer than it supports, but its next writing command re-exports `.mtix/tasks.json` from its own store, without the annotations and changes it skipped, and committing that file reverts them upstream. On such a client even an `mtix import` that fails rewrites `.mtix/tasks.json` the same way. Until every agent and teammate sharing a board runs 0.5.4 or later, never run writing commands or `mtix import` on an older client and never commit its `.mtix/tasks.json`. If one was committed, restore `.mtix/tasks.json` from the last good commit in git history, or restore the store from the backup taken before the import (`.mtix/data/backups/pre-sync-<time>.db`; `.mtix/data/pre-sync-backup.db` on 0.5.3 and earlier): stop every mtix process (the MCP server, `mtix serve` and the sync daemon), copy the backup over `.mtix/data/mtix.db`, delete `.mtix/data/mtix.db-wal` and `.mtix/data/mtix.db-shm`, then continue. A 0.5.4 store is protected by the auto-import guard (see "Automatic import of `.mtix/tasks.json`" below): such a file carries no annotations or activity, so its automatic import is refused and nothing changes.

**After export:** Verify the checksum field is present. Store exports alongside backups for disaster recovery.

## Import

Run `mtix import <file> --mode merge` from the CLI. If it reports renumbering without applying, review the report before rerunning with `--confirm`; this flag confirms renumbering only. The MCP server exposes no import tool. Import modes:

- **merge** — creates nodes the store lacks; for a node it has, annotations and activity merge as a union (no local annotation is ever dropped, and a resolved annotation stays resolved), and the other fields take the file's values only when the node's content hash differs
- **replace** — replaces all project data with the import file, annotations and activity included; a file written before mtix 0.5.4 carries no annotations, so a replace import of it leaves every node without them. It needs the ticket count typed at an interactive terminal, so an agent never runs it: ask the user; prefer merge.

Every import refuses a file that names a task id outside the node id grammar (as a task, a parent or a dependency end) that the store does not already hold, and writes nothing; the error starts `nothing was imported: invalid node id` and names the ids. A store that already holds such a task imports a file that holds it, such as its own export. See Quarantined Pulled Events: invalid node id.

**Uid adoptions (merge).** When the file holds a task's id under another uid and one of the two uids was assigned when a clone upgraded (more than an hour after the task was created), `mtix import --mode merge` treats them as one task and gives the local task the file's uid. It lists every adoption under `uids adopted from the file` in its report on stderr, applied or not: the id, both uids and both titles (`uid_adoptions` in `--json` once the import is applied; `--remap-file` maps the local uid to the id). The merge, and the automatic import after a git pull, carry the adopted uid onto the task's unpushed sync events and any held copy of them in the same step, so a later push sends the task's creation and its changes under one uid; changes already pushed keep theirs. If the import is refused because a push is running, run it again when the push has ended. Do not edit event uids by hand. Known limit (experimental relay): a change already published to the relay keeps its old uid on relay peers. Check every adoption whose titles differ: two different tasks created in the same second, when at least one clone assigned its uid after the task was created (at upgrade, or as a backfill uid on import or open, for example for a task created before 0.2), are treated as one, the merge keeps the file's task, and the local one survives only in the backup the merge took first. The automatic import refuses such a pair and lists it as `treated as the same task (uid assigned at upgrade)` with both titles; for it, the merge takes the file's title and content. If the titles name two different tasks, do not merge yet: show both to the user, copy the local task to a new one (`mtix show <id>`, then `mtix create` with its title and description; writes stay local while the refusal is pending), then merge. If a merge already took it, restoring the backup drops every change made since the merge: recover right away, or first note the later changes and redo them afterwards. The user stops every mtix process that uses this store (the MCP server, `mtix serve`, a running `mtix daemon`), copies the backup over `.mtix/data/mtix.db` and deletes `.mtix/data/mtix.db-wal` and `.mtix/data/mtix.db-shm` (they belong to the replaced database and must not be replayed onto the backup), then, before any mtix command runs, restores the pulled board (`git checkout -- .mtix/tasks.json`, or the file from the pulled commit), because the merge's own export rewrote it. The next command refuses again; copy the task as above, then run `mtix import .mtix/tasks.json --mode merge --confirm`. mtix gives every task it imports without a uid a backfill uid (a UUIDv8 with a fixed marker), and a store gives its tasks without a uid one when it opens, so boards written by 0.5.4 carry a uid for every task, and two clones that each backfilled a task keep one task. A board without uids does not lose a backfill uid: the missing uid is no loss, and the replace keeps the local uid, so a teammate's boards from a client at 0.3 or older keep importing (a uid given at creation that such a board leaves out is still a loss). When the file's copy has no uid to compare (a board from a client at 0.3 or older, or one never re-exported after an upgrade), the titles decide: another title is a different task, which the automatic import lists as `a task under this id with a different title and no uid to compare` and the merge renumbers with `--confirm` (listed under `of these, a task under this id with a different title and no uid to compare`); if a teammate only retitled it, the merge keeps both copies, and the renumbered one holds the comments, activity and field values the board lacked: the user moves them to the task at the id, or keeps the renumbered copy, before anything is deleted. The opposite case: two copies with identical titles listed as `a different task under this id` (every uid the task carries was assigned within an hour of its creation) are one task; the merge renumbers the local copy only with `--confirm`, and the user decides whether to delete the duplicate.

Import writes nothing when the schema version (a higher major version than this mtix writes), the node count, the checksum or any time value (RFC 3339, UTC year 1 to 9999; the required ones not empty) fails its check; the error names what failed. A refused import leaves `.mtix/tasks.json` untouched.

If an export, or the automatic import of a changed `.mtix/tasks.json`, fails because a stored node field cannot be read, the error names the node, the field and `mtix recover`: nothing was imported, and local changes are kept. Run `mtix recover` to salvage everything readable (see its report) before importing again. For such a field (comments, activity, `code_refs`, `commit_refs`), `mtix recover` takes the value from the task's copy in `.mtix/tasks.json` when that copy is usable (the only task under that id, the same uid when both carry one, `schema_version` 2.0.0 or later, times that pass the import checks), and its report says `restored from the mirror <path>` with the number of entries. The copy is whatever `.mtix/tasks.json` holds: the last export, or a file a `git pull` or checkout put there (for example one whose automatic import was refused), which is another clone's copy and can hold its comments and lack local ones. A mirror whose checksum does not verify is still used, and the note then ends `(the mirror checksum did not verify)`. Otherwise the report says the field is dropped and why, and the task is salvaged without it. A note `node A: the index entry for A points at the row of B; ...` means the primary-key index is damaged: nothing from that row is salvaged as A, and the row is salvaged once, as B. A itself is then salvaged from the database when another index entry reaches A's own row, taken from `.mtix/tasks.json` when the mirror holds it, or listed on the report's LOST line; show such tasks to the user. A note `node B: the table holds two rows with id B: ...` means the table holds duplicate rows with that id. A mirror copy is eligible only when it is the sole copy under that ID and its times pass import validation. Eligible copies rank rows by nonempty `uid` match, then nonempty `content_hash` match. A hash breaks UID ties; equal matches or no eligible match retain the existing first-kept row. The report names `uid-match`, `hash-match` and `first-kept` as applicable, lists B once, names the kept and losing rowids, and holds every losing row's content as exported JSON. Show that note to the user and have them decide which content to keep before the salvage file is imported. Before the salvage file is imported, show the user every dropped or restored field and every note ending `(the mirror checksum did not verify)`, and have them compare each restored field with the local history (for example `git log -p .mtix/tasks.json`).

**Import protocol:**
1. Run `mtix backup <path>` first
2. Import the data
3. Run `mtix verify` to confirm integrity post-import
4. Check `mcp__mtix__mtix_stats` to verify expected node counts

**Never skip the post-import verification.** A corrupt import in a safety-critical environment could hide incomplete or missing tasks.

## Automatic import of `.mtix/tasks.json`

When a `git pull` or checkout changes `.mtix/tasks.json`, the next mtix CLI command replace-imports it before it runs. The MCP server imports only when it starts, and its stderr is not shown to you. After a pull, run a CLI command that auto-imports, for example `mtix list`, then check with `mtix sync`. The MCP server protects itself too: its writes never overwrite a board that changed on disk; they import it first, or keep it and record why.

- **Applied.** An import that loses nothing prints one line: `mtix: imported the changed .mtix/tasks.json: nodes N added, N updated, N removed; dependencies N added, N removed (local database backed up to .mtix/data/backups/pre-sync-<time>.db)`. The newest 5 pre-import backups are kept; a file that fails a check or is refused takes none. A field a teammate cleared on purpose (unclaim, reopen, undefer, undelete, a later update) applies: their copy of that task holds all of its activity and is not older. The rule fails open in one direction: a teammate's copy with a later `updated_at` counts as current even when it missed your `mtix update` or `mtix delete` (neither records activity), after a concurrent edit to the same task or with a teammate's clock ahead, so a field it leaves empty is applied as cleared; the pre-import backup keeps the state before. The replace re-checks, in its own transaction, that the store is still the one it compared: a write that lands in between (another process, or the MCP server) is kept, nothing is imported, and the next command checks the file again.
- **First pull after upgrading from 0.5.3 or earlier.** The import finds the board you and your teammate last exchanged (the last-exchanged record, then the git merge-base board) and decides task by task: unchanged here takes the teammate's value, unchanged in the file keeps yours, history the older board could not carry is kept. It refuses only when both sides changed the same field, the file lacks a task you hold, a dependency between tasks you both already had differs in either direction (a dependency of a task you made since, or of a task new in the file, is resolved; if the other task was renumbered on the other side the import is still refused, and `mtix import .mtix/tasks.json --mode merge` is the way out the refusal names), or no such board exists; the refusal then names what was missing. The notice says which source was used and what was kept.
- **Refused.** If the file lacks a node, an annotation, an annotation's resolution, an activity entry or a dependency the store holds, or leaves a field empty in a copy of a task that is not current (the local task has no activity, it lacks some of the task's local activity or its `updated_at` is older: a stale board, or one from an mtix client older than 0.5.4), the import is refused and nothing changes. On a local task with no activity, an equal or later file timestamp cannot prove the teammate saw a local-only edit: a non-empty prompt (or another field) the file leaves empty is reported as a loss. This can also refuse an intentional clear on such a task; merge keeps the listed local value, while explicit replace takes the file. A teammate's `mtix dep remove` applies when the source task is current and carries a new system activity entry recording removal of that exact target and edge kind; an already-seen removal entry cannot justify omitting a re-added edge (MTIX-107.44). The refusal starts `mtix: auto-import of .mtix/tasks.json refused`, lists the loss node by node, and repeats on every command until someone chooses. Until then, writes save to the store but do not rewrite `.mtix/tasks.json` (one line says so). Choose, from the project root:
  - `mtix import .mtix/tasks.json --mode merge` backs up the database, keeps every value the refusal lists (every local node, annotation, activity entry and dependency, and every listed field value, with the task's local status when the value is part of it) and adds the file's changes. Where a task's status, assignee, agent state, wake time or deletion state differs from the file's, it writes nothing and lists each conflict (yours, the file's, an activity hint) until you rerun it with `--prefer theirs|ours`, or `--theirs ID,ID` and `--ours ID,ID`; the choice is recorded in the task's activity. Where they agree, the other field values are decided per task by its content (title, description, prompt, acceptance, labels): where it matches the file's, the local values win; where it differs, the file's copy wins, so your own edits to that task (a prompt edit, for example) are undone. Check `git log -p .mtix/tasks.json` afterwards and reapply what it undid. A task the file holds under another id with the same uid (another clone renumbered it) moves there without `--confirm`; two copies under different uids are one task only when they share a creation time and one uid was given to the task more than an hour after it was created, by a clone that upgraded from before uids were shared (the file's uid is then adopted); every other pair, such as tasks two clones created in the same second, stays two tasks; without a uid to compare, another title is another task. A local task whose id the board gives to another local task (a clone moved it there) is listed as a different task too, and the merge renumbers it with `--confirm`. A merge that would change nothing takes no backup. A task listed as `a different task under this id` (you and a teammate each created that id, or you created it while the refusal was pending) is never overwritten: merge renumbers your task and its subtree to the next number free in both the store and the file, keeping its uid, and the file's task keeps the id. It prints the renumbering (uid, old id, new id; `--remap-file <path>` saves it) and applies it only when you rerun it with `--confirm`.
  - `mtix sync --fix` keeps the local store and rewrites `.mtix/tasks.json` from it; every change in the file is dropped.
  - Not an agent's choice: `mtix import .mtix/tasks.json --mode replace` makes the file win and deletes what was listed, and it needs the ticket count typed at an interactive terminal (no flag supplies it), so an agent never runs it (it writes a verified snapshot first; `mtix backup <file>` is still a good extra copy). Name `mtix import .mtix/tasks.json --mode merge` or `mtix sync --fix` first: both keep local data.
- **Not rewritten.** When a write finds `.mtix/tasks.json` changed on disk and not imported, it keeps the file and prints one line with the way out for that kind of refusal (`mtix sync --json` shows `kind`). A write's export, including the one after `mtix import` of another file, imports a changed board first. A conflict (both the file and the store changed) whose replace would also delete local data is printed as a refusal with the loss list, and its replace option names what it deletes. The merge a conflict names decides per task: a task whose status, assignee, agent state, wake time or deletion state differs from the file's is listed and nothing is written until you choose `--prefer theirs|ours` or `--theirs ID,ID` / `--ours ID,ID` (a wrong choice undoes a teammate's claim, for example); where they agree and the task's content (title, description, prompt, acceptance, labels) matches the file's, your field values win, and where it differs, the file's copy wins (it undoes your prompt edit, for example). A conflict although nothing changed locally (after an upgrade from 0.5.3 or earlier, when that version's last automatic import was not followed by a writing command): run `mtix sync --fix`, then `git checkout HEAD -- .mtix/tasks.json`, then `mtix list`, which imports the board with the loss check; never stop after `mtix sync --fix`, which alone drops every change in the file. A file written by a newer mtix (`newer_schema`) needs an upgrade: never rewrite it with `mtix sync --fix`. A file that fails its checks (`invalid_file`, for example merged by hand): check it, then `mtix import .mtix/tasks.json --recompute-checksum`, or keep the local board with `mtix sync --fix`.
- **Never** run the replace import (it needs the ticket count typed at an interactive terminal, it cannot run unattended) to silence a refusal that lists annotations, activity entries or nodes: they hold review verdicts and close receipts. If you cannot tell whose changes are newer, stop and ask the user.
- **Verify.** `mtix sync` never prints `In sync` while an import of the board is pending or an id carries another uid in the file (`OUT OF SYNC: tasks.json changed and has not been imported`, `Another uid in tasks.json`). It prints `Auto-import: enabled (sync.auto_sync: <value>)` or `disabled`, and `Last auto-import refusal: <time> (pending|resolved <time>|no longer pending): <reason>` or `none`; `mtix sync --json` reports the same under `auto_import`. After you resolve a refusal, it must no longer read `pending`.
- **Switch.** `sync.auto_sync` (default `true`) switches only the automatic import; `mtix config set sync.auto_sync false` turns it off, and `mtix config set` accepts only true or false. `.mtix/config.yaml` is tracked in git, so the switch applies to every clone, but a store with no tasks (a fresh clone) is always imported. With it off, a pulled board is not overwritten by writes; import it with `mtix import .mtix/tasks.json --mode merge` (the merge writes nothing while a task's status, assignee, agent state, wake time or deletion state differs from the file's: it lists each conflict, and you rerun it with `--prefer theirs|ours`, or `--theirs ID,ID` and `--ours ID,ID`, which it records in the task's activity; where they agree, your other field values win when the task's content (title, description, prompt, acceptance, labels) matches the file's, and otherwise the file's copy wins, undoing your edits to that task; afterwards check `git log -p .mtix/tasks.json` and reapply what it undid), or keep your board with `mtix sync --fix`.
- **Export schema.** Every write re-exports `.mtix/tasks.json` in this binary's export schema, with `sync.auto_sync` on or off, so the first write after an mtix upgrade can change a committed file's `schema_version` and break a reader that rejects unknown fields. `mtix doctor`'s `export.schema` row warns about a mismatch and prints the fix (see Health checks).

## Garbage Collection

Run `mtix gc` to permanently remove soft-deleted nodes past the retention period (default: 30 days). Purging a deleted task whose creation `mtix sync push` holds does not release its held changes: they stay held, and `mtix sync doctor` keeps listing them. Run `mtix sync push` before purging such a task, so its latest changes are checked and held first.

**Before GC:**
- Verify retention period is appropriate for your compliance requirements (some standards require longer retention)
- Check what will be collected — soft-deleted nodes that are past retention
- Backup first

**GC does NOT delete:**
- Active nodes (any status except soft-deleted)
- Nodes within the retention window
- Agent records or session records (these are permanent audit trail)

## Configuration

Run `mtix config get <key>` to view a setting and `mtix config set <key> <value>` to change it.

Key configuration options:
- `prefix` — project prefix for dot-notation IDs
- `max_depth` — maximum hierarchy depth (default: 50)
- `auto_claim` — whether to auto-claim on creation
- `sync.max_lamport_jump` — how far above the local Lamport clock a pulled event may be stamped before `mtix sync pull` quarantines it (default: 4294967296, that is 2^32; a positive integer; see Sync Recovery: Quarantined Pulled Events)
- `agent_stale_threshold` — heartbeat timeout for stale detection (default: 30m)
- `session_timeout` — maximum session duration (default: 8h)
- `data.soft_delete_retention` — how long to keep deleted data (default: 720h/30 days)
- `sync.keep_roles` — comma-separated hub roles that `mtix sync harden` leaves with their access (default: none; see Hub Privileges). Setting it turns on strict mode for the doctor's `hub-privileges` check, which then fails whenever `mtix sync harden` would report a finding, or when the check cannot run
- `history.export` — EXPERIMENTAL, default `false`. When `true`, `.mtix/tasks.json` is written in export format 2.1.0 with each task's change history; mtix 0.5.4 and earlier cannot read such a file (their import fails its checksum, and their automatic import loads nothing). Turn it on only when the user asks and every machine and daemon that touches the board runs this version or later; no doctor check verifies that yet, so ask the user. A running process follows `mtix config set history.export` without a restart

## Hub Privileges (Sync Hub Owners)

Only for projects that sync through a Postgres hub (`mtix sync init`). Everyday use needs no role configuration. `mtix sync harden` is for an owner who wants the hub's sync tables usable only by the owner and the roles the team chooses. It is a CLI command; run it only when the user asks for it, never from a hook, push or daemon. It contacts the hub only while it runs, so it does not keep a scale-to-zero database awake.

The append-only tables (`audit_log`, `sync_conflicts`, `sync_events`) refuse TRUNCATE. `mtix sync init` adds these guards automatically; `mtix sync harden` checks them.

**Restore collisions:** the hub stamps every event with its own restore epoch (a trigger on `sync_events` sets it from `sync_hub_state` on every insert). A syncing role records restore collisions only through the hub function `record_restore_collision`, which runs as the table owner and records a collision only when the hub's own data shows an earlier-epoch create holding the number, so the role needs EXECUTE on that function and no INSERT on `sync_node_collisions`. When `mtix sync init` adds this function to an existing hub (migration 017), the table owner then grants EXECUTE on it to each syncing role (`GRANT EXECUTE ON FUNCTION record_restore_collision TO <role>;`) and revokes the privileges the least-privilege list no longer names (`REVOKE INSERT ON sync_node_collisions FROM <role>;` and `REVOKE USAGE ON SEQUENCE sync_node_collisions_collision_id_seq FROM <role>;`). Upgrade every syncing client first, then run the REVOKE statements; if the REVOKE comes first, an older client's push that meets a restore collision fails until that client upgrades. `mtix sync doctor`'s `schema current` check, run with a syncing role's DSN, reports each of these as a WARN by default and a FAIL in strict mode, with the fix the table owner runs: a hub without migration 017 (pushes keep working; the fix is `mtix sync init`), a role that cannot execute the function (a push that meets a restore collision fails until the table owner runs the printed GRANT), create events stamped with a restore epoch below 0 or above the hub's current epoch (restore-collision checks treat each as not earlier than the current epoch; the fix is the printed UPDATE, which sets each to the current epoch), and a role that can write collision rows: it holds INSERT on `sync_node_collisions` or USAGE on its sequence (a grant, or a predefined role such as `pg_write_all_data`), owns the schema that holds the sync tables, can reach, through a chain of SET ROLE and ADMIN OPTION, a role that holds either or owns that schema, or a superuser it can then SET ROLE to, has CREATEROLE before PostgreSQL 16, or is a member of `pg_execute_server_program` or `pg_write_server_files`. The check skips the table owner, roles that inherit it, and superusers. The fix is the exact REVOKE the table owner runs for a plain grant the owner made, and, for every other path, the path by name, with the chain that reaches it, which a role administrator removes (see "Hub health checks" in the user manual). `mtix sync harden --apply` revokes EXECUTE on the function from every role that is not kept, so keep each syncing role with `--keep-role`.

**Init locking and deadlines.** `mtix sync init` allows 30 seconds to connect,
then a separate `--migrate-timeout` (default `5m`, positive Go duration)
for the entire migration, including waiting for the migration advisory lock.
Other init callers and admin commands (`mtix sync migrate`,
`mtix sync repair-uids` and `mtix sync harden --apply`) take this lock too;
their own deadlines may end the wait first. Ordinary pushes and pulls do
not acquire it. Migrations still replay to restore missing functions and triggers.
Migrations 010 and 013 check the column catalog before ALTER: an up-to-date
`sync_events` takes no `ACCESS EXCLUSIVE` column lock, so event SELECTs can
continue. Adding a missing column blocks both event reads and writes until
commit or rollback. The migration's index statements (including first builds
of 014/015) take `SHARE` locks that block event writes until the transaction
ends, even when every index already exists. Index statements alone allow
event SELECTs. Migration 006 skips unchanged, enabled
audit/conflict triggers, so an up-to-date hub takes no `ACCESS EXCLUSIVE`
relation lock. Trigger repair drops and recreates a trigger, blocking reads
and writes on its table until commit or rollback. Thus init can still delay
a push, even on a current hub. A queued exclusive lock can also make later
SELECTs wait. Schedule upgrades and restore initialization when a sync pause
is acceptable.

On migration deadline expiry, init names `--migrate-timeout`. Migration is
atomic: an interrupted index build rolls back. Retry as the hub owner using
the larger budget printed in the error. For a default-budget timeout,
`mtix sync init --migrate-timeout=15m` is one example; increase it further for
the hub's size, then run `mtix sync doctor`. This also applies to a joiner's first init and
the post-restore runbook: repeated short retries cannot finish an index that
always exceeds the budget. Locks last until commit or rollback, within the
migration budget plus server cancellation/rollback cleanup; an earlier caller
cancellation or server timeout can end the attempt sooner. Waiting pushes and
pulls have their own statement timeout and retry limits (normally five
10-second attempts plus backoff), and may fail before init ends. Their queued
events and pull cursor survive; retry them after init succeeds. After migration,
registry checks and hub registration get a fresh 30-second budget.

**Registry index:** the node-number registry is the unique index `sync_events_node_registry_uidx` on `sync_events`. An index of that name that is not valid or not ready (`pg_index.indisvalid` or `indisready` false, as a failed or interrupted build leaves it) is skipped by `mtix sync init`'s migration. An index that is not ready checks no new create; one that is ready but not valid still refuses a duplicate create, but queries do not use it and it must be built again. A hub that holds `sync_events` but no index of that name is a WARN in `schema current` by default (a FAIL in strict mode), with the same fix. `mtix sync doctor`'s `schema current` check fails in every mode for an index that is not valid or not ready, and `mtix sync init` prints a WARN; both print the fix: as the table owner, run `mtix sync migrate --yes` while the version gate is open. That run records the duplicate creates of every project on the hub, drops the index and builds it again, leaving the recorded duplicates out by event id: they stay in the event log unchanged. On a hub without the index whose projects hold duplicate creates, `mtix sync init` refuses and names the same fix; run `mtix sync init` again after it. A hub with more than 100 duplicate creates, or more than 3600 bytes of their event ids, or a node that lost two numbers (the remap ledger records one number per node) is refused before the build, with the count or the creates named; a refused build drops and builds nothing, but Phase 1 of the same run has already recorded the duplicates (remap and conflict rows), and the report, in text and in `--json`, still shows that sweep phase; pushes keep working, and a push whose create takes a number already in use is still renumbered. `mtix sync migrate` never reports an index that is not valid or not ready as present. Verify: in `mtix sync doctor --json` the `schema current` check has `pass: true`, and `mtix sync migrate --json` shows `registry_index` with `valid` and `ready` true.

**Doctor:** `mtix sync doctor` runs the same verification as its `hub-privileges` check, as whichever role the DSN names. A **WARN** (exit 0) means roles other than the owner can use the sync tables, or a TRUNCATE guard is missing or disabled; this is acceptable for everyday use, may be fine when the database is reachable only from a private network, and blocks nothing. It is a **FAIL** (exit 2) only in strict mode (`sync.keep_roles` set), and then whenever `mtix sync harden` would report a finding: not only a role outside the list or a missing or disabled guard, but also a kept role that can grant its access on or holds a privilege another role granted it, an mtix object owned by another role, and a membership through which a role can reach every table. If the verification cannot run (hub unreachable, schema incomplete), the check is a WARN that says so by default, and a FAIL in strict mode. With `--json` the check carries `name`, `pass`, `warn`, `detail`, `fix` and `findings`. `mtix sync init` changes no existing privilege (it restores missing guards, replaces a guard bound to another function, and creates the restore-collision functions of migration 017 without EXECUTE for PUBLIC, whatever the owner's default privileges), and `mtix sync push` issues no DDL.

**Routine:**
1. As the role that owns the sync tables (the one that ran `mtix sync init`; a superuser or a member of the owner role may run it too), run the dry run: `mtix sync harden`. It changes nothing.
2. Show the user the role list at the top of the report. Every role that is not kept loses its access with `--apply`. A role other people or services use to sync must be kept with `--keep-role <role>` (repeatable); members of a kept role keep their access through it. Superusers are out of scope: harden neither checks nor changes them.
3. Only after the user approves that list: `mtix sync harden --apply --keep-role <role>`.
4. When the report prints `mtix config set sync.keep_roles <roles>`, show it to the user; running it records the kept roles for later runs and also turns on strict mode for `mtix sync doctor` (its `hub-privileges` check then fails, instead of warning, whenever `mtix sync harden` would report a finding, or when the check cannot run). Harden never writes the config itself.
5. Verify: run `mtix sync harden` again and expect exit 0. `verification passed` means: apart from the table owner, superusers, and the kept roles and their members, no role holds a privilege on the sync tables, their sequences or the mtix functions (EXECUTE on the trigger functions aside), or a role membership that leads to one, ADMIN OPTION included; the owner's default privileges give such roles nothing; and every TRUNCATE guard is in place. It says nothing about superusers or about who can reach the database over the network. The REPLICATION role attribute is outside the check too: a role that has it is checked for its privileges and memberships like any other role, but not for the attribute. Review the roles that have it (`SELECT rolname FROM pg_catalog.pg_roles WHERE rolreplication;`) with the database administrator.

**Exit codes:** 0 verification passed; 2 changes pending (dry run) or access remains after `--apply`; 1 error or refusal. With `--json` the report carries `before` and `after`, each with `caller`, `caller_scope` (`owner`, `superuser`, `kept` or `checked`), `findings` (`role`, `object`, `kind`, `privileges`, `via`, `scope`, `fix`, `manual`, `note`), `statements` and `kept_roles`, plus `executed` and `keep_roles_hint`. A finding with `fix` is changed by `--apply`; one with `manual` or `note` is not.

**What `--apply` changes, in one transaction:** every privilege on the sync tables (column privileges included), their sequences and the mtix functions held by PUBLIC, by the roles a data API uses for anonymous and signed-in callers, and by every other role except the owner, superusers and the kept roles; a kept role's right to grant its privileges on (first the owner grants again anything a kept role holds by another role's grant, so the kept role keeps it); the owner's default privileges toward those roles; a membership in `pg_read_all_data`, `pg_write_all_data` or (from PostgreSQL 17) `pg_maintain`, ADMIN-only memberships included, when the owner may revoke it (this is cluster-wide); a missing TRUNCATE guard, one whose trigger executes another function (compared by OID, so a function of the guard's name in another schema counts as another function; the guard migration replaces it), or one that is disabled or fires only in replication sessions (every guard ends enabled). After `--apply` every finding that remains is listed with its statement.

**Information, never a failure:** EXECUTE on the mtix trigger functions (they cannot be called directly) and other roles' default privileges (they do not apply to tables the owner creates). A freshly migrated hub verifies clean.

**Troubleshooting:**
- `refused: the connecting role does not own every sync table`: connect as the table owner and run it again. Nothing was changed.
- `raised a server WARNING`: an mtix object named in the message is owned by another role, so the owner cannot change its privileges. Nothing was changed. A database administrator returns the object to the table owner, then run it again.
- `the hub schema is incomplete`: run `mtix sync init` first.
- `an administrator runs: ...`: access harden may not change, such as a read-all membership granted by another role. Give the statement to the database administrator.
- `owner_membership`: the role is a member of the owner role (inheriting it or able to SET ROLE to it) and has all of its privileges. Harden reports it and never changes it; removing the membership is the administrator's decision. The connecting role is checked too unless it owns the sync tables, is a superuser or is kept; the report's `Connecting role` line says which. A member of the owner role that runs harden therefore always sees its own membership: when that is the only finding, the report says to run as the owner itself or keep the role.
- `superuser_membership` (a role that can SET ROLE to a superuser, or that holds ADMIN OPTION on a role that can), or membership in `pg_execute_server_program`, `pg_read_server_files` or `pg_write_server_files`: the role can reach every table without a privilege on it. Harden reports it; an administrator removes the membership.
- `object_owner`: an mtix function or sequence is owned by another role, which could replace or alter it. Give the printed `ALTER ... OWNER TO` statement to the database administrator.
- `grantor`: a kept role holds a privilege that another role granted. `--apply` grants it again from the owner before revoking that role's grants, so the kept role keeps it.
- `createrole`: before PostgreSQL 16 a CREATEROLE role can grant itself any role that is not a superuser. Harden reports it; an administrator removes CREATEROLE.
- A member of the owner role or of a read-all role is reported even when it holds only ADMIN OPTION, because it can grant that role to itself.
- `kept role ... does not exist` or `cannot be kept`: a kept role must exist, and PUBLIC, the data-API roles and `pg_` roles cannot be kept.

**Never:**
- Never pass `--apply` without the user approving the dry run's role list.
- Never run `mtix sync harden` from a hook, a push or the daemon.
- Never read, print or paste the hub DSN to run it; it comes from `MTIX_SYNC_DSN` or `.mtix/secrets`.

## Sync Hub Backup and Restore (Sync Hub Owners)

Only for projects that sync through a Postgres hub. Run these only when the user asks. `mtix sync backup` contacts the hub only while it runs, so it does not keep a scale-to-zero database awake beyond the dump.

**Back up:** `mtix sync backup --output hub-<date>.sql` runs `pg_dump` for every table the hub migrations create, with its data, and prints the table list. Every one of them must exist: a hub that lacks one, such as a hub not initialized since an upgrade added a table, fails the backup and leaves no file, and the error says to run `mtix sync init` with the DSN naming the table owner, then back up again. It needs a `pg_dump` at least as new as the hub's server, on `PATH` or named by `MTIX_PG_DUMP`. It uses the TLS settings sync uses: `sslmode` is `verify-full` when the DSN names none; a weaker `sslmode` needs `--insecure-tls` and works only when every host is loopback or a local socket; the CA file comes from `sslrootcert` in the DSN or from `MTIX_SYNC_SSLROOTCERT`, and with neither `pg_dump` verifies the hub against the system trust store (the backup says so). `pg_dump` does not receive the DSN's `options`, so it finds the sync tables through the default search_path of the role the DSN names, which may not be the table owner: for a hub whose schema is named only in the DSN, first run `ALTER ROLE <the DSN's role> IN DATABASE <the DSN's database> SET search_path = <schema>, public`. It applies in that database only and takes precedence over a role-wide `ALTER ROLE <the DSN's role> SET search_path = <schema>, public`, which a setting for that role in that database overrides; a backup that cannot find a hub table prints both statements with the names filled in. Client certificates (`sslcert`, `sslkey`) are not passed to `pg_dump`, so a hub that requires one cannot be backed up with this command yet. mtix creates the output file readable and writable only by its owner (mode 0600) and never overwrites one: give every backup a new path. A failed backup, including one interrupted with Ctrl-C or SIGTERM, leaves no file.

**Restore into an empty database (the runbook):** if the hub's sync tables were in a schema other than `public`, the dump names that schema without creating it: first create it in the empty database (`CREATE SCHEMA <schema>;`, as the owner role), and in steps 1 to 3 put it first on the search_path (`PGOPTIONS='-c search_path=<schema>'` for `psql`; `options=-c search_path=<schema>` in the DSN, or `ALTER ROLE <owner> SET search_path = <schema>, public`, for mtix).
1. Restore the dump with `psql -f <file>`, connected to the empty database as the role that will own the sync tables. Put the connection in `PG*` variables (`PGHOST`, `PGPORT`, `PGUSER`, `PGDATABASE`, `PGSSLMODE=verify-full`, and `PGSSLROOTCERT=<ca.pem>` for the hub's CA file, or `system` for the operating system's trust store with libpq 16 or later) and the password in `~/.pgpass`, never on the command line. psql reports an error for each trigger: the dump holds no trigger functions yet.
2. Check the owner's search_path first, above all for a hub in `public`: connected as that role, `SHOW search_path;` and `SELECT current_schema();` must put the sync tables' schema first. The default `"$user", public` puts a schema named after the role first when one exists; `mtix sync init` then refuses, changing nothing, until `ALTER ROLE <owner> SET search_path = public` (or `<schema>, public`) is run. Then run `mtix sync init`, with the hub DSN naming that owner role. It recreates every mtix function and trigger and keeps the restored data, the restore epoch included.
3. Run `mtix sync doctor` and check `hub-triggers` (below). Then confirm the DSN reaches the restored database: as the table owner, in the restored database, run `SELECT count(*) FROM <schema>.sync_events;` with the schema named (`public` unless the hub used another), then `SELECT count(*) FROM sync_events;` as the DSN's role with the DSN's search_path. If the two counts differ, the DSN does not reach the restored database. If they are equal and not zero, that rules out only a new or empty hub: a source hub that is still reachable and holds the same events gives the same count, so also confirm that the DSN's host and database name the restored server. For a hub that has events the count must not be zero. The source hub's `sync_events` count, taken just before the backup, is a weaker reference still: the DSN's count must be at least that number (the hub only gains events), which rules out only a new or emptier hub. Never use the `COPY <n>` lines `psql` prints during the restore: they do not name their table.
4. A dump holds no privileges: after `mtix sync init`, grant each syncing role the least-privilege list again, EXECUTE on `record_restore_collision` included, then run `mtix sync doctor` with a syncing role's DSN. Its `schema current` check names a syncing role that cannot execute the function. The least-privilege list is in step 2 of the small-team workflow.
5. Run `mtix sync mark-restored` exactly once, with the DSN naming the table owner, then `mtix sync collisions list`. A syncing role set up with the least-privilege list holds no UPDATE on `sync_hub_state`, so it cannot run `mtix sync mark-restored`, which runs as the table owner. The least-privilege list is in step 2 of the small-team workflow (`.mtix/docs/workflows/small-team.md`) and in `docs/SECURITY-MODEL.md`.

**Verify:** in `mtix sync doctor --json`, the `hub-triggers` check has `pass: true` and no `warn`: every function and trigger the hub migrations define exists, every trigger executes the function its migration binds (compared by OID, so a function of the right name in another schema counts as another function), and every trigger is enabled (`tgenabled` `O`, or `A` for one set to fire always; both count, as for `mtix sync harden`). Without `--json` the fix is printed on a `fix:` line under the check. A gap is a WARN (exit 0), or a FAIL (exit 2) in strict mode (`sync.keep_roles` set). Its `detail` names each missing function, missing trigger, trigger that executes another function and trigger that is not enabled, and its `fix` names the table owner who runs it (`as the table owner (<role>): ...`) and what to run, in order: `mtix sync init` for anything missing and for a trigger that executes another function (init replaces it in one transaction, so the table is never unguarded); the printed `ALTER TABLE <schema>.<table> ENABLE TRIGGER <name>;` statement for a trigger that is not enabled. Then run the doctor again.

**Troubleshooting:**
- `already exists`: the output path exists (a file or a symlink); choose a new path.
- `weak sslmode requires --insecure-tls`, or `not loopback or a local socket`: the DSN names an `sslmode` weaker than `verify-full`. Use `verify-full`; `--insecure-tls` is for a development hub on loopback or a local socket only.
- A certificate error after `system trust store (PGSSLROOTCERT=system)`: the hub's certificate comes from a private CA; set `sslrootcert=<ca.pem>` in the DSN or `MTIX_SYNC_SSLROOTCERT`.
- `server version mismatch` from `pg_dump`: install a `pg_dump` at least as new as the hub's server and point `MTIX_PG_DUMP` at it.
- `a hub table was not found` from `mtix sync backup` (`pg_dump` names the table above it): no file was left. If the hub has not been initialized since an upgrade added the table, run `mtix sync init` with the DSN naming the table owner, then back up again. If the hub's schema is named only in the DSN, first run the printed `ALTER ROLE <the DSN's role> IN DATABASE <the DSN's database> SET search_path = <schema>, public`, which takes precedence over a role-wide `ALTER ROLE <the DSN's role> SET search_path = <schema>, public`. If the DSN's role lacks USAGE on the hub's schema, which leaves that schema off its search_path, run the GRANT statements the backup prints: the schema's owner grants USAGE on the schema (find it with `SELECT nspowner::regrole FROM pg_catalog.pg_namespace WHERE nspname = '<schema>';`; for `public` in a database created on PostgreSQL 15 or later it is `pg_database_owner`, that is, the database's owner), and the table owner grants SELECT on each sync table and sync-table sequence by name (`GRANT SELECT ON TABLE <schema>.applied_events, <schema>.audit_log, ... TO <the DSN's role>;`), so the role gets nothing else in the schema; then keep that role with `--keep-role`, so `mtix sync harden --apply` leaves its access.
- `the sync tables are in schema …, but the first schema on the search_path is …` from `mtix sync init`, or from a `mtix sync harden --apply` that would restore or replace a TRUNCATE guard (a privilege-only `--apply` is not affected): nothing was changed. The usual cause is a schema named after the connecting role, which the default search_path (`"$user", public`) puts first. Put the sync tables' schema first on the owner's search_path (`ALTER ROLE <owner> SET search_path = public` when the tables are in `public`, else `ALTER ROLE <owner> SET search_path = <schema>, public`, or `options=-c search_path=...` in the DSN) and run it again. To keep a separate hub in the first schema instead (one hub per search_path), put that schema alone on the search_path. `mtix sync doctor`'s `hub-triggers` check states the mismatch and puts this step first in its fix.

## Sync Conflicts

`mtix sync status` counts unresolved conflicts only (`open_conflicts` in `--json`): an lww conflict with no later manual resolution for the same node and field. It has no `conflicted` count; no sync path sets that status. `mtix sync conflicts list` shows the unresolved conflicts, and `--all` shows every row with `unresolved` true or false.

`mtix sync conflicts resolve <id> --action keep-local|keep-remote|both-renumbered|acknowledge` records the decision only. Its output says `decision recorded; node state not changed` (`--json`: `decision_recorded: true`, `node_state_changed: false`). To apply the chosen value, edit the node with `mtix update` and push. The decision resolves the conflict and every earlier one on the same node and field; a conflict recorded later on that node and field is unresolved again. Resolve the newest conflict of a node and field: a conflict that already has a later decision or a later conflict on its node and field is refused as invalid input, and the error names the newest conflict id of that node and field (resolve that one; if the error says it is resolved too, the decision already stands). An id that is itself a `manual` row is refused as invalid input: resolve the conflict it answers.

**Never:**
- Never read, print or paste the hub DSN or its password; it comes from `MTIX_SYNC_DSN` or `.mtix/secrets`.
- Never put a password on the `psql` or `pg_dump` command line.
- Never run `mtix sync mark-restored` more than once for one restore.

## Integrity Verification

Run `mtix verify` to check content hash integrity across all nodes.

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

**Clock skew misleads the check in both directions.** When this machine's clock is behind a teammate's, a genuine replay can be flagged. When it is ahead, a teammate's newer state can be listed as a `replay`, and `--apply` would revert it. Before `--apply`, check each replay's winner time and whether this machine or another machine made it, and show any doubtful node to the user (`mtix show <id>`).

Left alone: nodes without claim, unclaim, defer or status-change events; a blocked node with an unresolved blocker, and a node cancelled by `mtix cancel --cascade` on an ancestor without a cancel event of its own, which are not synced as events; an assignee set later with `mtix update --assignee` (flagged when it arrives by import); the wake time of this machine's own deferral; `closed_at` after this machine invalidated or restored the node.

**Apply (only with the user's explicit approval):**

```bash
mtix sync pull                     # again, just before applying
mtix sync repair --status          # list again; apply only what is still listed
mtix sync repair --status --apply
mtix sync push
```

`--apply` first writes a verified backup to `.mtix/data/backups/pre-repair-status-<UTC time>.db`; if it cannot, it stops without changing anything (free disk space and run it again). It then repairs each node that is not flagged, in its own transaction: it writes the derived state, adds an activity entry with the text `sync repair` naming the winning event, recomputes the parent's progress and re-exports `.mtix/tasks.json`. Only when the status changes does it emit a status-change event (reason `sync repair`, stamped with the winning event's time so other machines keep their `closed_at`, or with the current time if that time is in the future, since `mtix sync push` refuses an event more than a day ahead) and unblock dependents; that event fires `status.changed` hooks here and on every machine that pulls it.

**Flagged nodes:** never pass `--force` on your own. Show each flagged node to the user (`mtix show <id>`, the reason, the stored and derived values); if they confirm that the stored state is the damage, run `mtix sync pull`, then `mtix sync repair --status --apply --force`, which repairs the flagged nodes too.

**Verify:** `mtix sync repair --status` lists nothing (or only flagged nodes the user chose to keep), and `mtix sync status` counts the repair events as pending until `mtix sync push` sends them.

**Recover:** to undo a repair before `mtix sync push`, stop every mtix process (the MCP server, `mtix serve` and the sync daemon), copy the backup over `.mtix/data/mtix.db`, delete `.mtix/data/mtix.db-wal` and `.mtix/data/mtix.db-shm` (a stale write-ahead log would replay old writes onto the restored file), then continue. After the push a restore does not undo it, because the next pull brings the repair events back; change the node's status with the normal commands instead.

## Sync Recovery: Quarantined Pulled Events

`mtix sync pull` checks every event it receives before applying it: first its Lamport clock (below 2^53, and at most `sync.max_lamport_jump` above the local clock; default 4294967296, that is 2^32), then the same size, shape and id rules the hub applies at push (payload at most 64 KB and 10 levels deep, at most 100 vector-clock entries, well-formed author, machine and project ids, a hub row that decodes), and then every task id the event names (its task, a creation's parent, a link's or unlink's target) against the node id grammar. An event stamped more than 24 hours ahead of this machine's clock is applied with a `WARN`. Each event applies on its own: an event that fails a check or its apply (for example a dependency whose target has not arrived) is rolled back and kept in the local table `sync_quarantine` (in `.mtix/data/mtix.db`), and the pull goes on. A quarantined event never changes the local clock and is never lost, and one refused for its clock never moves the pull cursor. Every pull retries the quarantine at its start, before it contacts the hub, and again at its end when it applied events; an event that applies, or that the store has already applied, leaves the quarantine. Retries are local and add no hub query, so they do not keep a scale-to-zero database awake; an event refused for its clock is downloaded again by each pull.

**Recognize:** `mtix sync doctor` fails its `quarantined events` check while any pulled event remains unacknowledged (exit 2; with `--json` the check has `"pass": false` and the detail); `mtix sync status` shows `quarantined events` above 0 (`quarantined_events` in `--json`); a pull prints `quarantined event <id> (<pass> pass): <reason>` on stderr and `quarantine: N pulled events held, not applied` at the end.

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
   - `envelope validation` (payload too large or too deep, a vector clock over its caps, an id that does not match its grammar, a hub row that does not decode): the hub holds an event that a correct client never pushes. It will not apply by itself. Show the user the event ids and reasons, for whoever runs the hub.
   - `lamport_clock at or above 2^53`: the event's clock is past the overflow guard. It will never apply; show it to the user, for whoever runs the hub.
   - `sync.max_lamport_jump`: the event's Lamport clock is far above this replica's. If the user confirms the hub's clocks are legitimately that high, raise the bound: `mtix config set sync.max_lamport_jump <positive integer>` (only a positive integer is accepted; `mtix config delete sync.max_lamport_jump` restores the default). The next pull retries the event with the new bound.

**Verify:** `mtix sync doctor` passes the `quarantined events` check when no unacknowledged pulled event remains. Events that applied leave the quarantine; reviewed invalid-node-id events may stay retained with their local acknowledgement (see the invalid-node-id routine below). Check `unacknowledged quarantined events 0` in `mtix sync status`; the retained `quarantined events` total need not be zero. Events with `source` `push` are held push events: see Sync Recovery: Held Push Events.

**Recover:**
- **A refused clone on a fresh store:** run `mtix sync pull` alone. It quarantines the event and applies the rest. Clone has no quarantine: it runs the same checks on every hub event before it writes anything and refuses the whole clone, naming the event and the reason, while the hub holds an event that fails them; a clone that completes empties the quarantine because every hub event passed.
- **Rebuilding a store that already holds sync state:** `mtix sync reconcile --discard-local` deletes local tasks and unpushed changes, and empties the quarantine with the rest of the local sync state. It needs the ticket count typed at an interactive terminal (no flag supplies it), so an agent never runs it: ask the user to run it. Its dry run shows only the node count, not what would be lost. Before asking, run `mtix sync push` and confirm `mtix sync status` shows `pending` 0. Once the user has run it, you run `mtix sync pull`, which runs the checks and quarantines again any event that still fails.
- **A quarantined copy of this replica's own event** (its hub row was changed after the push) does not clear by itself, even after the hub row is repaired: every retry re-checks the stored copy. Recover in this order: whoever runs the hub repairs the hub row first; run `mtix sync push` and confirm `mtix sync status` shows `pending` 0; then ask the user to run `mtix sync reconcile --discard-local` (it asks for the ticket count, typed at an interactive terminal; no flag supplies it, so it cannot run unattended) and, once they have run it, run `mtix sync pull`. Discarding before the hub row is repaired drops this replica's own true copy of the event, which then comes back only as a quarantined hub event.
- **A quarantined event from a teammate whose hub row was repaired** stays quarantined too: every retry re-checks the stored copy, so repairing the hub row does not clear it. The recovery is the guarded rebuild: run `mtix sync push` and confirm `mtix sync status` shows `pending` 0; ask the user to run `mtix sync reconcile --discard-local` (it deletes local tasks and unpushed changes and asks for the ticket count, typed at an interactive terminal, so it cannot run unattended); then run `mtix sync pull`, which fetches the repaired row. Never raise `sync.max_lamport_jump` for a row the hub has repaired: the bound is not what holds it, and it protects this replica's clock.

**Never:**
- Never delete rows from `sync_quarantine` or edit the local database to force an event in; a quarantined event is the only local copy of what the hub sent.
- Never raise `sync.max_lamport_jump` without the user's confirmation: the bound is what keeps one extreme event from pushing this replica's clock to where the hub refuses all its later changes.
- Never try to get past a refused `mtix sync clone` by any means other than `mtix sync pull` on the fresh store; the refusal is what keeps an event the pull would quarantine out of the store.
- Never run `mtix sync reconcile --discard-local` yourself: it needs the ticket count typed at an interactive terminal (no flag supplies it), so it cannot run unattended; hand it over only after `mtix sync push`, with `mtix sync status` showing `pending` 0 and the user's approval, because it deletes local tasks and unpushed changes.
- Never copy quarantined events into tickets or shared documents beyond the event ids and reasons; the raw events hold task content.

## Quarantined Pulled Events: uid held by a local task

A pulled `create_node` event whose uid a different local task already holds is not applied and not dropped: it is quarantined with the reason `uid "<u>" is held by local task <id>` and retried on every pull. `mtix sync quarantine list` shows it and the doctor's `quarantined events` check names this step.

**Decide which case it is:** run `mtix show <id>` and compare it with the quarantined event's title and content. A uid names one task for good (ADR-003), so the same uid means the same task: the same title and content means the task exists twice under two ids, and different content means the uid was duplicated (an old import, a restored copy). No mtix command resolves either case in this version (tracked as MTIX-95.31.8.1; `mtix sync reconcile` keeps uids, so it does not apply).

**Never:** delete the local task, edit `sync_quarantine` rows, or clear the uid yourself. Ask the user with the uid, the local task id and the event id, and stop.

**Verify:** `mtix sync pull` retries it; `mtix sync doctor` passes `quarantined events` and `mtix sync quarantine list --json` no longer shows the event.

## Quarantined Pulled Events: invalid node id

A task id has the form `PREFIX-<number>` followed by `.<number>` segments: an uppercase ASCII prefix of at most 20 characters (letters, digits, `-` and `_`, starting with a letter), a numeric root and numeric or uid child segments, with no empty segment. mtix never creates any other id. A pulled event that names another id (its task, a creation's parent, or a link's or unlink's target), such as `AA-1é.4`, came from a modified client or a hub row changed by hand. It is quarantined with a reason that contains `invalid node id`, the field and the id, and it is never applied; no retry applies it. The relay refuses such a record the same way, and every import refuses a file that brings in such an id.

**Recognize:** `mtix sync status` shows `quarantined invalid ids` above 0 (`quarantined_invalid_ids` in `--json`), and the doctor's `quarantined events` check says how many of the quarantined events are for an invalid node id.

**Routine:** run `mtix sync quarantine list --json` and report the event ids, nodes and reasons to whoever runs the hub. After reviewing an invalid-node-id pulled event, record that this replica accepts it as unapplied:

```bash
mtix sync quarantine acknowledge --handle <handle> --reason '<why this event is accepted as unapplied>'
```

Doctor's Fix names the event by its handle: the first 12 hex characters of the SHA-256 of the exact event id bytes, which `mtix sync quarantine list` also prints in a HANDLE column (`handle` and `event_id_sha256` in `--json`). The handle is always printable and typeable, even when the id holds NUL, invalid UTF-8 or shell metacharacters, so the printed command can be copied as it is: `mtix sync quarantine acknowledge --handle <handle> --reason '<your explanation>'`. Replace `<your explanation>` with why this event is safe to accept as unapplied; without `--reason` the command is refused. A handle may be shortened to 8 hex characters; one that matches more than one event is refused with the length that separates them. Use the positional `<event-id>` form only for an ordinary id. Then run doctor again after each acknowledgement. Use `mtix sync quarantine list --json` to report the original id to the operator; JSON keeps NUL and escapable bytes in the id exactly but shows invalid UTF-8 bytes as U+FFFD; `event_id_sha256` and the handle always name the exact stored bytes. A printed handle may be longer than 12 characters when two retained events share a shorter prefix. For example, with one eligible event, doctor prints this full Fix line:

```text
fix: Inspect and report retained events (1 eligible unacknowledged; first shown): mtix sync quarantine list; add --reason '<your explanation>' after the handle, then: mtix sync quarantine acknowledge --handle 914809b83eee
```

The command starts after `then:`; add `--reason` and your own explanation after the handle as described above.

Only retained events with `source` `pull` or `sweep` whose raw event fails the node-id validator are eligible. Stored reason text alone cannot grant eligibility. A missing or blank reason, a held push event, an unrelated failure, malformed raw JSON or a raw event-id mismatch is refused. No terminal or hub connection is required. The resolved process author, required reason, UTC time and exact raw-event SHA256 form an immutable local receipt; repeating a valid acknowledgement returns the first receipt. Reasons are limited to 500 UTF-8 bytes. On a repeat, text explains that the first receipt stands and the supplied reason was not recorded; JSON sets `already_acknowledged` to `true` (`false` for a new receipt). The receipt is local to this replica and is never exported, imported or synced.

The command retains the raw quarantine and original reason. It never applies, removes or repairs the event, changes sync clocks or cursors, changes the hub, or acknowledges another replica. Every pull still retries the event. A different raw event reusing the same id needs a fresh acknowledgement. Do not try to apply the invalid event.

**Tasks this store already holds.** A sync from before this check could bring such a task in. It stays in the store: you can read and edit it here, and `mtix list` shows it; `mtix show` takes its uid in place of the id. Other replicas quarantine every change to it that they pull, and the doctor's `valid node ids` check warns, naming each task and its uid (`mtix sync status` counts them as `invalid local ids`). Recreate each task you still need with `mtix create` and stop using the old one; no mtix command renames a task in this version.

**Never:** edit `sync_quarantine` rows or the `nodes` table to change an id, and never create a task id by hand.

**Verify:** `mtix sync doctor` passes `quarantined events` only when zero unacknowledged pulled events remain, and reports the retained acknowledged count. Any unrelated or unacknowledged quarantine still fails that check; all other doctor failures need their own fixes. `mtix sync status` retains the unapplied total and adds `acknowledged quarantined events` and `unacknowledged quarantined events` (`acknowledged_quarantined_events` and `unacknowledged_quarantined_events` in JSON). `quarantined invalid ids` can remain above zero. `mtix sync quarantine list` marks acknowledged rows; `--json` includes their `acknowledgement` with `actor`, `reason`, `acknowledged_at` and `raw_event_sha256`. Retained acknowledged events need not disappear. The separate `valid node ids` check passes without a warning only once no local task has such an id.

## Recovery: Duplicate Task UIDs

Every task has a durable uid, and no two tasks (soft-deleted ones included) may share one: the local database enforces it with a unique index. A database written by an older mtix can already hold two tasks with one uid. mtix still opens it, loses no row and changes no uid, logs `duplicate_node_uids`, and keeps the old non-unique index until the duplicates are gone.

**Recognize:** `mtix verify` prints `INTEGRITY FAILURE` (with `--json`, `uid_unique_ok` is false and `uid_unique_recovery` holds the text), or `mtix sync doctor` fails its `unique node uids` check (exit 2; with `--json` the check has `"pass": false` and the detail). The detail names each shared uid and the task ids that hold it.

**Fix:** (1) Back up first: `mtix backup .mtix/data/backups/pre-uid-fix.db`. (2) Decide which task keeps the uid, normally the original (the one the hub and teammates know). (3) For every other holder, clear its uid in the local database, `UPDATE nodes SET uid = '' WHERE id = '<id>';` (for example with the `sqlite3` shell on `.mtix/data/mtix.db`), while no mtix process is running. (4) Run any `mtix` command: the open gives that task a fresh uid and creates the unique index.

**Synced project: ask the user; do not run the fix yourself.** The project syncs if `mtix sync status` shows a `project_prefix` other than `-` or a `pushed` or `applied` count above 0, `MTIX_SYNC_DSN` is set, or `.mtix/secrets` exists (when in doubt, treat the project as synced). A task given a new uid has no create event on the hub or at any peer, and events already queued locally under the shared uid are applied to the other holder, not to it. No mtix command repairs this on a synced project in this version (tracked as MTIX-95.31.8.1). Report the uids and task ids from the detail, ask the user to run `mtix sync push` until `mtix sync status` shows `pending` 0 before anything is changed, and stop.

**Verify:** `mtix verify` prints `All node uids unique` and `mtix sync doctor` passes the `unique node uids` check.

**Never:** delete a task row to remove a duplicate, or edit uids on tasks that are not listed.

## Sync Recovery: Held Push Events

The sync hub accepts an event payload of at most 64 KB (65536 bytes); local fields can be larger (a new task's prompt may be 100 KB and its description 50 KB; acceptance criteria, comments and later edits of a description or prompt have no local size limit). A change whose sync event is over the limit is saved locally, but the hub would refuse it. `mtix sync push` checks each pending event with the hub's rules before it sends a batch and **holds** an event the hub would refuse: the event stays pending in the local queue, is recorded in `sync_quarantine` with source `push` and the reason, and is never sent. The rest of the batch is pushed, and later changes of other tasks keep pushing, except dependency links while a task creation is held (see below). Push contacts the hub only when it runs, as before; holding adds no hub query.

The reason starts with the kind of hold:
- `too large: ...` or `refused: ...` — **permanent**: a payload over the limit, a broken nesting-depth, id or Lamport/vector-clock cap rule, or an event id the hub already holds with other content (`refused: the hub already holds this event id with a different ...`; see Sync Recovery: A Pending Queue That Never Drained). It stays held.
- `temporary: clock: ...` — the event is stamped more than 24 hours ahead of this machine's clock. Every push checks it again and releases it once it passes.
- `depends on held create of <event id> (<task>)` — while a task's creation is held, none of the changes of its subtree are sent. In 0.5.x an event names its task by display number only, so push holds every change of that task, the creation of its child tasks at any depth and every change of those, including changes made after the last push. Push finds the task a change is about by the task's internal id, not its number, so a change made before the task was renumbered (by `mtix import --mode merge --confirm` on this machine, or by a renumber the hub asks for during a push) is still held, and a change of another task that later takes one of its old numbers is not. The same holds when the task's internal id changed after its creation was queued: a merge import (`mtix import --mode merge`, or the automatic import after a git pull) can give the task the id the board holds for it, and a creation queued by an mtix from before changes carried an internal id has none. Push then finds the task by the creation's own id or else by the number the creation names, and holds its later changes and its child tasks too (a task that has taken that number since is held as well). The merge carries the adopted id onto the task's pending changes and the held copy of its creation in the same step, so the task is still found by its internal id after a later renumber on this machine. A merge that would do this is refused while `mtix sync push` runs: run it again once the push has ended. The automatic import after a git pull does the same under the push lock; while a push runs it is skipped with a one-line notice, never fails the command, and imports the pulled board on the next command. If `mtix gc` purges a deleted task of the subtree, a change already held stays held, a change of a task whose own creation is held is still found by its internal id, and any other change is checked by the number it names. Known limit: a change made under a number the task got by a renumber on this machine, which no push checked before `mtix gc` purged the task, is not recognized. The reason names the nearest held creation.
- `depends on held create of <event id> (<task>): links made while a task creation is held wait for it; this version names a link's target by number` — a **held link**: a dependency link or unlink names the task it points to by number only, and this version keeps no record of the numbers a task had, so while any task creation is held, every link or unlink made after it is held, whatever task it names (a teammate's task that took one of the held task's old numbers included). The reason names the earliest held creation; the link pushes once no creation made before it is held.

Once a clock hold on a task's creation clears, the creation and the changes of its subtree go through the ordinary push, in the same run. Push first checks each of those changes with the hub's rules: one the hub would refuse stays held under its own reason (its first-held time and attempt count are kept), and if it is a child task's creation, that child's subtree stays held with it. In the ordinary push, if a teammate took the task's number while the creation was held, the hub renumbers the creation, and push re-addresses every change of the task and its subtree to the new number before it sends any of them (see Sync Recovery: A Teammate Numbered the Same Task First). A creation held permanently keeps its whole subtree held.

**Recognize:**
- With a hub configured, a CLI command that made the change prints one line on stderr (for an MCP tool call, an extra text block in its result): `WARN: <id>: the <field> field makes this <op> sync event <n> bytes, over the 65536-byte sync limit; saved locally, but sync push will hold it and not send it (shorten or split <field>; see mtix sync doctor)`; for a task's creation (op `create_node`) the line ends `saved locally, but this task's creation will be held with every later change of it; do not edit it to fix this; see mtix sync doctor`. The change itself succeeded. Changes made through the web UI, the REST API or gRPC (`mtix serve`) print no warning; push still holds their events and doctor reports them. Imports (`mtix import`, the automatic import of `.mtix/tasks.json`) write no sync events, so nothing they write is pushed, held or warned about.
- `mtix sync push` prints `push: held event <event id> (<node> <op>), not pushed: <reason>` on stderr for each newly held event, `push: released held event ...` for each hold it releases, and `held: N events not pushed ...` at the end.
- `mtix sync doctor` fails its `held push events` check (exit 2; with `--json` the check has `"pass": false`) and lists up to five held events as `<node> <op>: <fix> (reason: <reason>)`.
- `mtix sync status` shows `held push events` above 0 (`held_push_events` in `--json`). Held events also stay in `pending`, so `pending` does not reach 0 while any is held.

**Routine:** run `mtix sync doctor` and follow the fix it names for each held event (`mtix sync quarantine list --json` lists them all, read-only; keep the entries whose `source` is `push`):
- **Field edit over the limit** (fix: `shorten or split the field; the next edit pushes`): shorten that field on the node, or split its content (for example move part of a long prompt or description into child tasks), and save it again (`mtix update <id> --prompt ...`, `mtix prompt <id> ...`, `mtix update <id> --description ...`, `mtix update <id> --acceptance ...`). Then run `mtix sync push`; the new event pushes.
- **Held task creation** (op `create_node`; fix: `stop editing this task and escalate`): stop editing that task and show the user the node id and the reason. Shortening a field does not help: the creation itself must be sent, and there is no automatic re-send in this release. The task, its later changes and its subtree stay local.
- **Dependent** (fix: `resolves with the held creation it depends on`): nothing to do for the event itself; handle the creation it names.
- **Held link** (fix: `a link made while a task creation is held; it pushes once no creation made before it is held ...`): nothing to do for the link itself; handle the creation it names. Do not re-create the link to get it through: the new link waits the same way.
- **Clock hold** (fix: `check this machine's clock; the event pushes once its stamp is within 24 h of the clock`): tell the user this machine's clock was ahead when the change was made, or is behind now. The event pushes on the first push after its stamp is within 24 hours of the clock; if it is a task's creation, the changes of its subtree push with it.

**Verify:** `mtix sync push` prints no new `push: held event` line for your edit, and the node's new value reaches teammates on their next `mtix sync pull`; a released clock hold prints `push: released held event`. Permanent holds and their dependents stay held and listed: `mtix sync doctor` keeps failing its `held push events` check and `mtix sync status` keeps counting them, because this release has no command to release or discard one held event. Tell the user which held events your edit supersedes.

**Push stops with `another mtix operation is in progress, retry in a moment`:** while it decides which events to hold, push may wait for the local database's write lock, up to 5 seconds. If another mtix process (a pull applying a backlog, another agent) keeps it longer, push fails with `hold invalid events of batch N: another mtix operation is in progress, retry in a moment`. The batches before the failing one were already sent and marked pushed; the events of the failing batch and later were not sent and stay pending. Nothing is lost and nothing is sent twice. Let the other mtix command finish, then run `mtix sync push` again to send the rest.

**Never:**
- Never delete rows from `sync_quarantine` or edit the local database to release or remove a held event.
- Never keep editing a task whose creation is held: its changes stay local with it.
- Never run `mtix sync reconcile --discard-local` to clear held events: it deletes every unpushed change and local task along with them, and `mtix sync push` leaves held events pending, so `pending` will not reach 0 while events are held. It needs the ticket count typed at an interactive terminal (no flag supplies it), so it cannot run unattended; ask the user, naming the held events that would be discarded.
- Never raise or bypass the 64 KB limit; it is the hub's limit, not a setting.

## Sync Recovery: A Teammate Numbered the Same Task First (Renumbered Creations)

Two replicas that create a task with the same number offline (both make `PROJ-1.1`) meet at the hub: it keeps the first and tells the second to renumber, and `mtix sync push` reports `<n> renumbered`. Nothing is lost and no command is needed. What this version guarantees, and how to check it:

- **A creation goes first.** Push sends no change about a task, no creation or change of a task below it and no dependency link to or from it while that task's creation is not yet on the hub. Such changes wait in `pending` and go in a later batch of the same push, so a push that has both shows two or more batches. A creation that never reaches the hub (held, or blocked by a restore collision) keeps everything that depends on it back.
- **A renumber moves the whole task.** When the hub renumbers a creation, mtix moves the task and its subtree to the next free number in one step and re-addresses every unsent change of the subtree, the creation of each child task and every unsent link into it to the new numbers. Nothing of the task is sent under the old number, which now belongs to the teammate's task. A task you deleted locally is renumbered too, and its delete follows it. A change the hub already holds is history and keeps the number it was sent under.
- **Pull never crosses the two tasks.** A change carries its task's internal id and applies by it, so it never lands on a task of another id. An event that names a number instead (a creation's parent, a link's or unlink's target, the task itself for a change from a client at 0.5.3 or older that carries no id) is refused when that number, or one above it, belongs to a task of yours whose creation is not yet on the hub. The event is quarantined (`mtix sync quarantine list`, reason `number held by a task whose creation is not on the hub yet`) and applied by the quarantine retry after your push has moved your task; nothing of it is applied to your task, and it is not dropped. Every later event of the teammate's about that creation (an edit, claim, comment, delete, child or link) is quarantined behind it (reason `depends on a quarantined create <event id>`) and applied after it, in order.

**Verify:**
1. `mtix sync push`, then `mtix sync pull`, then `mtix sync status`: `pending` 0. A pull that had quarantined the teammate's creation prints `pull: <n> quarantined events applied on retry; 0 still quarantined`.
2. `mtix show <old number>` shows the teammate's task, unchanged, and `mtix show <new number>` shows yours with its claim, edits, child tasks and links. The new number is the one `mtix list` shows for your task's title.
3. `mtix sync doctor` passes.

**Recover:** if `mtix sync quarantine list` still lists the teammate's creation after a push and a pull, run `mtix sync push` and `mtix sync pull` again; an event that stays quarantined for another reason follows Sync Recovery: Quarantined Pulled Events. Update any commit or PR text that names the old number of your task. Events that a client from before this fix pushed to the hub under an old number stay there (hub history is never rewritten), and a fresh `mtix sync clone` of such a hub can fail with `node <id> (uid=""): not found`; no recovery exists in this version (a clone applies a batch in one transaction and does not quarantine); it is tracked as MTIX-95.37.1, so report the error to whoever runs the hub and do not retry the clone.

Version note: sync protocol 2 (ADR-006) replaces how this is done, not what it guarantees. A renumber then stops rewriting events and emits a `rebind` identity event, links carry both tasks' internal ids, and every event keys on its uid, so the re-addressing and the by-number fallbacks above are no longer needed once a hub is cut over. Until then this section applies.

- Never edit the local database, or delete `sync_events` or `sync_quarantine` rows, to move a task or release an event.
- Never assume a task kept its number after a push that reports `renumbered`: look it up by title.

## Sync Recovery: A Pending Queue That Never Drained (Events Already on the Hub)

An event can reach the hub without this replica recording it: push marks an event `pushed` locally only after the hub confirms the batch. The confirmation is lost when the connection drops during the commit, when mtix crashes or the disk fails or fills up after the hub committed and before the local mark, or when push retries after a network drop (after a commit error the transport runs the whole batch again, and the hub already holds what the first attempt wrote). Before 0.5.4 the hub confirmed an event only when it inserted it, so such an event stayed pending forever; push reads the queue oldest first, 100 events per batch, and stopped at a batch nothing confirmed, so 100 of them at the head of the queue stopped every later change from being pushed while `mtix sync push` still reported success. **Push now acknowledges events already on the hub:** the hub confirms an event whose id it already holds when its copy has the same task, operation and payload, and push marks it pushed and goes on. Push contacts the hub only when it runs, as before; the check is one more query within a push, only for a batch that holds events the hub already had.

**Recognize:**
- A pending queue that never drained after a lost acknowledgement (a client older than 0.5.4): `mtix sync status` shows a `pending` count that does not go down across pushes, and `mtix sync doctor` fails its `queue draining` check (`<n> events pending for >1h — pusher may be stuck`), while `mtix sync push` exits 0.
- On this version, `mtix sync push` prints each batch on stderr as `push progress: batch <n> (<sent> sent, <accepted> accepted: <i> inserted, <p> already on the hub; <r> renumbered, <c> conflicts)` and prints up to three per-pass summary lines on stdout as `push complete: <n> events pushed across <b> batches (<i> inserted, <p> already on the hub); <r> renumbered, <c> conflicts surfaced`. Each line reports totals for that pass, and the last line describes the final pass. The **inserted** count is the events this push wrote to the hub; the **already on the hub** count is the events an earlier push had delivered (and a re-sent creation of a task the hub already holds). Both are marked pushed. A non-zero already-on-the-hub count after an interrupted or failed push is expected, not an error. An event already on the hub is not checked for conflicts again, so a re-push adds no conflict row on the hub and nothing to the `conflicts` counts. After a lost commit acknowledgement a push's `conflicts` count can read 0 although the first attempt recorded conflicts: run `mtix sync conflicts list` to see them.

**Routine:** after upgrading, run `mtix sync push` once. It marks the events already on the hub pushed and pushes the rest of the queue in the same run. Nothing else is needed: no hub change, migration or local repair. If a push fails part-way (an error after some `push progress` lines), run `mtix sync push` again: events already on the hub are confirmed, never written twice.

**Verify:** `mtix sync status` shows `pending` 0 (held push events stay counted in `pending`; see Sync Recovery: Held Push Events), `mtix sync doctor` passes `queue draining`, and a second `mtix sync push` prints `push complete: 0 events pushed across 0 batches (0 inserted, 0 already on the hub); 0 renumbered, 0 conflicts surfaced`.

**An event under the same id with other content:** if the hub holds the event's id with a different task, operation or payload, push never confirms it, since that would mark a change pushed that teammates never received. It holds the event permanently (source `push`, reason `refused: the hub already holds this event id with a different <field> (hub copy: <task> <op>)`, naming each field that differs), prints `push: held event <event id> (<node> <op>), not pushed: <reason>. See mtix sync doctor`, and pushes the rest. `mtix sync doctor` fails `held push events` with the fix `the hub refuses this event; show the reason to the operator`, or, for a task's creation, `stop editing this task and escalate`; the later changes of that task and its subtree are then held with it. A correct client never produces this: the local copy of the event was changed after it reached the hub, or another client wrote an event under the same id. Show the user the event id, the task and the reason, for whoever runs the hub.

**Never:**
- Never mark events pushed by hand (`UPDATE sync_events ...`) or delete pending events to make the queue drain: an event the hub does not hold then never reaches teammates. Upgrade and run `mtix sync push`.
- Never run `mtix sync reconcile --discard-local` to clear a pending queue that does not drain: it deletes every unpushed change and local task, and it needs the ticket count typed at an interactive terminal (no flag supplies it), so it cannot run unattended.
- Never delete the hold of an event under the same id with other content, or edit the event to match the hub's copy.

## Operator-local state

`mtix hooks status` and `mtix hooks status --json` validate the operator-local state directory and show corrective guidance. This check creates no state and grants no hook approval. The directory is `$XDG_CONFIG_HOME/mtix` (default `~/.config/mtix`) on macOS and Linux, or `%APPDATA%\mtix` on Windows. Use an operator-owned directory with mode 0700 and files with mode 0600; Windows requires a protected user-only ACL. Keep it outside projects, git work trees, temporary directories, `~/.codex`, `CODEX_HOME` and harness writable roots, with no symlink components. Never commit or dotfile-sync it.

Filesystem checks cannot detect sandbox write access granted to an operator-owned directory. Keep operator state outside every sandbox-writable root; the operator must choose that placement. `mtix hooks status` and `mtix sync doctor` report this limitation in text and JSON. On Unix, every ancestor must be owned by root or the operator and must not be writable by a group or other users; a sticky directory is not an exception. On Windows, ancestor owners are limited to the operator, SYSTEM, Administrators and TrustedInstaller. Rights to delete entries, change access rules or ownership, or generic write/full access are limited to those principals; creating files or subdirectories in an ancestor is allowed. The state directory and files still require protected user-only access. Unix refuses group-writable homes and network or directory-service-owned parents that do not meet these ownership rules. Set `XDG_CONFIG_HOME` to an operator-owned directory with root- or operator-owned ancestors and no group or other write access; `mtix hooks status --json` reports the concrete cause and validates the new placement, and `mtix sync doctor` names these placement constraints and the remedy. Atomic replacement does not establish power-loss durability. Filesystems without the required hard-link support are refused with the cause reported.

This directory holds a persistent host identifier and host-specific state records. Other-host records are ignored and reported. Missing records are empty; invalid or inaccessible state is refused. This storage API validates records; hook approval and legacy conversion follow the workflow below. To recover, correct the reported path, ownership or access rules manually, then repeat `mtix hooks status --json`; preserve existing state records while correcting permissions.

### Recent operator alerts

`mtix sync doctor` and `mtix sync doctor --json` show the `alerts.recent` check, sampling the latest 20 logical diagnostics on this host. A recent diagnostic is a WARN and does not fail an otherwise passing doctor; absent alert state passes without creating files. This check reads only operator-local state; other checks in the same doctor still contact a configured hub, and a missing hub configuration can independently fail the overall report. Alerts never wake anyone, never address the concerned agent and never enter the board journal. Surface the diagnostic to the operator and follow the exact fix it prints; do not repost it to the agent named in the row.

The log carries one logical row per agent and class per rolling hour, with a count and first/last timestamps. Another occurrence increments the count; exactly one hour after the first occurrence opens another window, and while the rate record exists a backward clock step does not reset the count and never refuses a raise. `alerts.jsonl` and two numbered archives retain three segments of at most 1 MiB each. Rate records retain the current count across restarts and log eviction: one bounded host-specific record per distinct agent/class pair, and a record whose hour window has closed is removed automatically on the next alert write. Their disk usage is therefore bounded by the number of distinct agent/class pairs raised in the last hour, not by a fixed cap. A pruned or lost rate record restarts the count from zero: the next occurrence is counted as a fresh window and a warning (`operator alert rate record missing; count restarts`) is logged when a row for that pair is still inside its hour; the raise is never refused. This is separate from wake-frequency policy. Interrupted writes may leave duplicate physical log rows, which doctor reconciles by stable row identifier and greatest compatible count.

A diagnostic can be accepted durably before its log projection finishes. An I/O error may therefore leave an accepted occurrence, and retrying a raise can count another occurrence. The next writer replays pending projection without incrementing its count. Doctor displays accepted pending diagnostics without repairing state or creating a host identifier or lock. Diagnostic text is bounded plain UTF-8; JSON rows carry `id`, `host_id`, `agent`, `class`, `first_at`, `last_at`, `count`, `detail` and `fix`. A failed `alerts.recent` check means state could not be validated: preserve all existing logs, rate and pending records, run `mtix hooks status --json`, and correct the reported path or access rules. Do not delete a pending record, unlink the alert lock or copy records from another host to clear a warning. Existing permissions and placement constraints still apply; atomic replacement does not promise power-loss durability.

## Exec hook approvals

Exec approvals belong to the local operator. Use `mtix hooks status` to inspect
per-hook decisions and the exact command to request review. An agent asks the
operator to review the named hook, then run `mtix hooks trust <hook>` from an
interactive operator terminal outside agent sessions. Agents never run this
approval command themselves. Changing one hook or its project script requires
review of that hook; other hook approvals remain independent.

The approval records are stored outside the project, bound to the local host
and canonical project identity. Each record pins the selected hook stanza and
its project script contents. For example, after reviewing the `wake` hook:

```sh
mtix hooks status --json
mtix hooks trust -- 'wake'
```

Standard input must be a real terminal. Pipes, redirected input, and agent
session markers refuse new approvals. Existing validated approvals remain
usable in agent sessions. Status commands report decisions without writing
approval state; `mtix hooks trust <hook> --status` inspects one hook.

On the first eligible run outside an agent session, an untracked legacy
`.mtix/hooks.trust` with a matching content hash can be converted once. A
tracked, changed, or unverifiable legacy record requires individual hook
review. An eligible refusal is recorded and later runs do not retry conversion;
an agent session defers it without recording an attempt. New `mtix init`
projects exclude `.mtix/hooks.trust` in `.gitignore`.

An approved exec hook launches from the project root, whatever the working
directory of the process that dispatches it. Launch rules:

- The script is always pinned. The program itself, or the first non-option
  argument of `sh`, `bash`, `zsh`, `dash`, `ksh`, `ash`, `python`, `node`,
  `ruby` or `perl` (also when reached through `env`), must be a project file
  that was approved. If it is not approved, the launch is refused, even when
  the file does not exist yet: create it and approve the hook again. Paths are
  judged both as entered and after following links, so a link into the project
  counts as a project path.
- Options before the script must be in this list; anything else is refused
  when the hook is approved and again at launch, because the script position
  is never guessed.
  - `sh`, `bash`, `zsh`, `dash`, `ksh`, `ash`, `tcsh`, `csh` and `fish` share
    one table: `-o NAME`, `+o NAME`, `-O NAME`, `+O NAME`, `--posix`,
    `--noprofile`, `--norc`, and clusters of `-` or `+` with the letters
    `a e f h m n u v x b C`. The option `-b` ends option processing for every
    shell name (the argument after it is the script), because `sh` can be
    `zsh`. Options outside the table are refused.
  - `python`: `-W VALUE`, `-X VALUE`, and clusters of `-` with
    `B E I O q s S u v x`. `-X pycache_prefix=...` is refused.
  - `node`: `--no-warnings`, `--enable-source-maps`, `--trace-warnings`,
    `--no-deprecation`. `perl`: clusters of `-` with `w W T t`. `ruby`: `-w`,
    `-W0`, `-W1`, `-W2`.
  - `env`: `-u NAME`, `--` and `NAME=VALUE`. `env -i`, and `env -u` of any
    `MTIX_*` name (in any letter case), are refused, because they remove
    variables that mtix sets for loop prevention. After `env --` the next
    argument is the program and gets the same checks.
  - Interpreters are matched by name without regard to case or version suffix
    (`SH`, `bash5`, `python3.12`, `python3.11-intel64`, `zsh-5.9`, `nodejs`,
    `perl5.34`, also with a `.exe` suffix): a family name followed by a
    version or architecture suffix that starts with a digit, dot, underscore
    or hyphen. Also covered: `pythonw`, `pypy`.
  - Inline code and code loading are refused, per interpreter: `sh`, `bash`
    and the other shells `-c`, `--rcfile` and `--init-file`; `python` `-c` and
    `-m`; `node` `-e`, `-r` and `--require`; `perl` `-e` and `-I`; `ruby`
    `-e`, `-r` and `-I`. Move the logic into a script file in the project and
    approve it.
  - A hook may use at most one top-level `env`; a second `env` or any other
    command wrapper (`nohup`, `nice`, `ionice`, `xargs`, `arch`, `timeout`,
    `stdbuf`, `unbuffer`, `setsid`, `sudo`, `doas`, `command`, `exec`,
    `builtin`, `time`, `chrt`, `taskset`, `script`, `flock`, `watch`,
    `caffeinate`, `busybox`) is refused, naming the wrapper. A relative path
    that leaves the project (`../../usr/bin/nohup`) is a PATH-style program,
    not a project file. An approved hook runs as written: pinning covers
    project script files, and a hook that makes some other program run
    project files is the operator's responsibility.
  - Environment assignments through `env` follow an allowlist. Every argument
    containing `=` before the program is an assignment, and its name must be
    a valid identifier. Allowed: `LANG`, `LANGUAGE`, `TZ`, `TERM`, `NO_COLOR`,
    `LC_*`, and project-specific names matching `^[A-Z][A-Z0-9_]*$` that are
    not in a loader family. Refused by name or family: `BASH_ENV`, `ENV`,
    `ZDOTDIR`, `FPATH`, `MODULE_PATH`, `SHELLOPTS`, `BASHOPTS`, `PS4`,
    `PROMPT_COMMAND`, `IFS`, `CDPATH`, `GCONV_PATH`, `GOFLAGS`, `GOENV`,
    `GOROOT`, `GOPATH`, `APPDATA`, `LOCALAPPDATA`, `USERPROFILE`, `PHPRC`,
    `PHP_INI_SCAN_DIR`, `TCLLIBPATH`, `CLASSPATH`, `MAVEN_OPTS`,
    `GRADLE_OPTS`, `BASH_*`, `ZSH*`, `PYTHON*`, `NODE_*`, `PERL*`, `RUBY*`,
    `GEM_*`, `LD_*`, `DYLD_*`, `OPENSSL_*`, `XDG_*`, `GIT_*`, `JAVA_*`,
    `JDK_*`, `LUA_*`, `DOTNET_*`, `CORECLR_*`, `COR_*`, `ERL_*`, `JULIA_*`,
    `R_PROFILE*`, `R_ENVIRON*`, `R_LIBS*`, `GO*`, `CGO_*`, `RUSTC_*`,
    `CARGO_*` and `NPM_CONFIG_*`, and the variables that name a command to
    run: `LESSOPEN`, `PAGER`, `EDITOR`, `VISUAL`, `SHELL`, `COMSPEC` and
    `PATHEXT`. All `MTIX_*` names are refused too: mtix sets its own (loop
    prevention, identity, sync credentials). Lowercase names such as
    `http_proxy` are refused. `PATH` cannot be set by a hook: mtix provides a
    fixed one. `HOME` is accepted only when its value is outside the project
    (also after following links). The refusal message names the variable and
    the rule.
  - The environment a hook runs in is explicit. The dispatcher's environment
    is not inherited: the hook receives only `HOME`, `USER`, `LOGNAME`,
    `LANG`, `TZ`, `TMPDIR`, `TERM`, `NO_COLOR` and the exact locale names
    `LC_ALL`, `LC_CTYPE`, `LC_COLLATE`, `LC_MESSAGES`, `LC_MONETARY`,
    `LC_NUMERIC`, `LC_TIME`, `LC_ADDRESS`, `LC_IDENTIFICATION`,
    `LC_MEASUREMENT`, `LC_NAME`, `LC_PAPER` and `LC_TELEPHONE` (other `LC_`
    names are not passed), and the deny rules below apply to every name. On
    Windows the hook also receives `SystemRoot`, `ComSpec`, `PATHEXT`,
    `USERPROFILE`, `TEMP` and `TMP`; `ComSpec`, `PATHEXT` and `USERPROFILE`
    are the one exception to the deny rules, because the system needs them.
    It also receives the
    variables named in the hook's `exec.env-pass` list, and the mtix markers
    `MTIX_EVENT`, `MTIX_HOOK_ORIGIN` and `MTIX_HOOK_SCRIPT_PATH`. Credentials
    such as `MTIX_SYNC_DSN` and relay keys never reach a hook.
    `MTIX_AUTHOR_ID` is the hook's `to-agent`, or unset; the dispatcher's
    author id is never passed on.
  - `exec.env-pass` is a list of variable names in the hook stanza, so the
    operator approves it with the hook. Names must be valid variable names
    and must not be credential-shaped (`*_KEY`, `*_TOKEN`, `*_SECRET`,
    `*_PASSWORD`, anything with `DSN`), an `MTIX_*` name, `PATH`, or in a
    loader or startup family; such a name disables the hook with a warning
    that names it. To give a hook a secret, read it from an operator-owned
    file inside the script. The list holds at most 32 names of at most 128
    bytes each; a longer list or name is refused the same way, at load and
    when you run `mtix hooks approve`, which names the variable and the
    reason.
  - The `PATH` of a hook is fixed: the directory of the running `mtix`
    binary (unless it is inside the project), then system directories
    (in this order: `/usr/local/bin`, `/usr/bin`, `/bin`, `/usr/sbin`,
    `/sbin`; on macOS `/opt/homebrew/bin` and `/opt/homebrew/sbin` come
    first, then `/usr/local/bin`, `/usr/bin`, `/bin`, `/usr/sbin`, `/sbin`;
    on Windows `System32` and the Windows directory under `C:\Windows`; the dispatcher's
    `SystemRoot` is not used for the hook `PATH`). It has no relative entry and no project
    directory. The program and the interpreter are resolved against it, never
    against the dispatcher's `PATH`, so use an absolute program path for tools
    installed elsewhere.
- Any other argument that names a project file at approval time, in any
  position except the program and the script, and including the value of
  `--opt=VALUE` or `NAME=VALUE`, is pinned
  and replaced by a private verified copy made from the bytes that were checked
  against the approval. Pass output or data files that the hook writes through
  the environment or stdin, not as project-file arguments. The copy's path is
  `$0` for the script, and the original path is in `MTIX_HOOK_SCRIPT_PATH`.
- An argument that names an existing project file that was not approved is
  refused, and so is an argument outside the project that is a link to such a
  file, even a link whose target does not exist yet.
- URLs, plain words, directories and paths that do not exist pass unchanged
  unless they are in the script position.

Copies live in the `exec-copies` directory inside the operator state
directory, one directory per content and executable bit; they are not pruned
automatically. File mode is not part of the approval: after `chmod` on the
original, the next launch uses a matching copy with no new review. Scripts must
not locate sibling files relative to `$0`; use paths from the project root. A
script that differs from its approval at launch time starts nothing and is
logged as an error. A bare program name, such as `python3`, is resolved to an
absolute path at launch and recorded in the hook log; a name that resolves to a
file inside the project is refused, so name that script by its project path.
Programs outside the project are not part of the approval, so upgrading an
interpreter needs no new review.

Residual: a file that appears at a non-script position between the launch check
and the moment the interpreter opens it is not covered, because only files that
existed at approval are pinned there.

Program review notice: when you approve an exec hook whose program is neither
a pinned project script nor a known interpreter running one, `mtix hooks trust`
prints a "Program review" notice, and its `--json` result carries a
`program_review` object with `class`, `review_needed`, `program`, `detail` and
`fix`. A known interpreter means an exact interpreter name (for example `sh`,
`bash`, `python3`, `node`; case-insensitive, with `.exe`, `.cmd` or `.bat`
accepted) as a bare name or as an absolute path in a PATH directory, running a
pinned project script; any other path or name is reported with its
symlink-resolved path. A hook that cannot be classified is reported as
`unknown` and needs review. `mtix hooks status` reports the same object for
each hook, and `mtix doctor` shows the `hooks.programs` check: WARN with the
exact fix lists the approved hooks that need review and any `hooks.yaml` load
problem, PASS means none do (no row appears when there is no `hooks.yaml`).
The notice is advisory: the hook still runs as written and launch behaviour
does not change. To clear it, run the program through a pinned project script
and approve the hook again.

Strict program mode (opt-in, off by default): on a host where you want more
than the advisory notice, run `mtix hooks strict on` at a terminal. With it on,
an exec hook whose program is not a pinned project script or a known
interpreter running one launches only after you approve that program with
`mtix hooks trust --program <hook>`. The approval is bound to the
symlink-resolved program path, the hook name and the project, and a changed
program path or hook configuration voids it; a program that resolves inside
the project is refused because it is a script and must be pinned instead. A
refused launch reports the rule and the exact approval command. The setting
and the approvals are operator-local state outside the project: `strict on`,
`strict off` and `trust --program` need terminal stdin and are refused under an
agent session. `mtix hooks strict status` works without a terminal and has
`--json` (`enabled`, `detail`, `fix`, `programs`); `mtix hooks status --json`
carries the same object under `strict`, and `mtix doctor` shows the
`hooks.strict` check (PASS when the mode is off or nothing needs approval, WARN
with the exact approval commands otherwise). An unreadable strict record
refuses launches rather than allowing them. An approval binds the program's resolved path, not its file content: the
contents are not pinned, so approve only programs in locations that
agents and other untrusted processes cannot write. A launch wrapper other than
the known env, found by name in a PATH directory, is itself the program that
needs approval.
Whether a wrapper or an interpreter is the known one is decided by the
physical directory that holds it, after links are followed. A slot is the known
one only when that directory has no link left in it (a path that still passes
through a link, such as a chain of links too long to follow, is not known and
needs approval); under strict mode a known slot launches from that directory.
`mtix hooks trust --program`
prints the program it will approve before it writes the approval.

Hook program paths must be clean: a program written with a parent-directory
segment ("..") is refused at launch, naming the hook, because the checked path
and the file the operating system runs can differ once a link comes before the
segment. Write the program as an absolute path without ".." or as a
project-relative path (scripts/run.sh and ./scripts/run.sh are fine). Paths are
resolved link by link, so a script operand is judged where it actually lands.

Append-file hooks: an `append-file` hook writes only to a regular file inside
the project. Its `append-file.path` must be relative. The file is opened
through a handle on the project directory, so a path that leads outside the
project, including through a symlinked directory, is refused at open time. A
final component that is a symlink, a file that is not a regular file, and a
file with more than one hard link are refused, and nothing is written through
them; a pipe never blocks delivery. A new file is created with mode 0600. An
existing file keeps its mode: `mtix hooks status` warns when it is readable or
writable by group or others and prints `chmod 600 <file>`, and delivery still
writes to it. An event whose name or node id holds a control or line-separator
character, or whose payload holds a line break, is refused, so one event is one
line. `mtix hooks status` lists each configured `append-file` path that
delivery would refuse, with the reason and the exact fix: text output prints
`Fix:` lines and `--json` carries an `append_file` list of `hook`, `path`,
`reason`, `fix` and `refused`. On Windows the same confinement and
same-file checks apply and a multiply linked file is refused at delivery (as is a file whose
link count cannot be read), but
`mtix hooks status` does not list it and permission bits are not reported.
