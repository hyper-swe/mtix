# mtix export format and `.mtix/tasks.json`

> For users and auditors. The commands are described in
> [USERMANUAL.md](../USERMANUAL.md) (Backup, Export, and Import); the
> requirements are FR-7.8 and FR-15 in [REQUIREMENTS.md](../REQUIREMENTS.md).

`mtix export` writes one JSON document, and mtix keeps the same document in
`.mtix/tasks.json` after every change (the git-tracked board, FR-15). This
page describes that document: what it carries, how its checksum is
computed, and how `mtix import` and the automatic import read it back.

The current format is `schema_version` **2.0.0**, written by mtix 0.5.4 and
later. mtix 0.5.3 and earlier wrote 1.0.0. With the experimental
`history.export` switch on, the file is **2.1.0** and also carries task
history (see [Task history in the export](#task-history-in-the-export-experimental)).

## Envelope

| Key | Meaning |
|---|---|
| `version` | Format generation (integer, currently `1`). |
| `schema_version` | Format version (semver). The automatic import and `mtix import` both refuse a file whose major version is higher than the one they write, with the message `newer than supported ... upgrade mtix`, and change nothing. mtix 0.5.4 reads 1.x and 2.x files. |
| `exported_at` | When the file was written (RFC 3339, UTC). Not covered by the checksum. |
| `mtix_version` | Version of the mtix that wrote the file (may be empty). |
| `project` | Project the export was taken for (may be empty: the export holds every project). |
| `nodes` | Every node, soft-deleted ones included, sorted by `id`. |
| `dependencies` | Every dependency, sorted by `from_id`, then `to_id`. |
| `agents` | Agent records. |
| `sessions` | Session records. |
| `history` | Only in a 2.1.0 file, and only when it has entries: the change history of tasks that no longer exist (see below). Covered by the checksum. |
| `node_count` | Number of entries in `nodes`; an import refuses a file where they differ. |
| `checksum` | SHA-256 of the canonical JSON of `nodes` and `dependencies` (see [Checksum](#checksum)). |
| `ids_checksum` | Optional. Present only when the file holds an `uid_hex`, `task_uid_hex` or `event_id_hex` field (see [Ids that are not valid UTF-8](#ids-that-are-not-valid-utf-8)). It covers those fields. A file without such a field has neither the field nor this checksum. |

## Node fields

A node carries every column of the nodes table, under the column's own
name. A field marked "omitted when empty" is left out of the JSON when it
has no value, so a node without it is written, and hashed, exactly as a
1.0.0 file wrote it. The major version rose to 2.0.0 because a client that
does not know the new fields would drop them (see
[Compatibility](#compatibility)).

| Field | Since | Notes |
|---|---|---|
| `id`, `parent_id`, `depth`, `seq`, `project` | 1.0.0 | Identity and position. |
| `title`, `description`, `prompt`, `acceptance` | 1.0.0 | Content. |
| `node_type` | 1.0.0 | Derived from `depth`; an import re-derives it and ignores the file's value. |
| `issue_type`, `priority`, `labels` | 1.0.0 | `labels` is the stored JSON text, as a string. |
| `status`, `progress`, `assignee`, `creator`, `agent_state`, `weight` | 1.0.0 | State. `progress` is written back as exported. |
| `content_hash` | 1.0.0 | Hash of the content fields; a merge import compares it. |
| `created_at`, `updated_at` | 1.0.0 | RFC 3339. |
| `closed_at`, `defer_until`, `deleted_at` | 1.0.0 | RFC 3339; omitted when empty. |
| `uid` | 1.0.0 | Durable internal identity; omitted when empty. Every node of a board mtix 0.5.4 writes carries one: a node that reached the store without a uid (from a board written before uids were shared) is given a backfill uid, a UUIDv8 whose 12 bits after the version hold the marker `0xBF1`, when it is imported or when the store opens. |
| `previous_status` | 2.0.0 | Status to return to when a block or invalidation clears; omitted when empty. |
| `estimate_min`, `actual_min` | 2.0.0 | Minutes; omitted when empty. |
| `code_refs`, `commit_refs` | 2.0.0 | Same structure as `mtix show --json`; omitted when empty. |
| `annotations` | 2.0.0 | Every annotation (comment, review verdict), in the structure `mtix show --json` returns: `id`, `author`, `text`, `created_at`, `resolved`, and `addressee` when set. Omitted when the node has none. |
| `invalidated_at`, `invalidated_by`, `invalidation_reason` | 2.0.0 | Omitted when empty. |
| `activity` | 2.0.0 | The node's activity stream: `id`, `type`, `author`, `text`, `created_at`, `metadata`. Omitted when empty. |
| `deleted_by` | 2.0.0 | Who soft-deleted the node; omitted when empty. |
| `metadata` | 2.0.0 | The stored JSON text, as a string; omitted when empty. |
| `session_id` | 2.0.0 | Agent session the node was created in; omitted when empty. |

**Columns deliberately left out: none.** The derived columns are exported
too: `node_type` is re-derived from `depth` on export and on import, and
`progress` and `content_hash` are written back as exported. SQLite's
implicit `rowid` is not a declared column: it is a local storage key, and
an import rebuilds the full-text search index from the rows it writes.
Local-only tables (the sync event log, hook and inbox state, the
cascade-delete records) are not part of the export.

## Task history in the export (experimental)

> **Off by default.** `mtix config set history.export true` is refused while a
> session registration or daemon heartbeat (seen in the last 30 days) lacks the
> task-history marker that every build from 0.6.0 writes, and `mtix doctor`
> shows the `history.export.guard` row (`--json`: blockers and `min_version`).
> mtix 0.5.4 refuses a 2.1.0 file (`checksum verification failed`, nothing
> stored). The guard cannot see other
> machines, CI jobs or clones that read the file through git: confirm
> `mtix --version` on each before enabling.

The config key `history.export` (`true` or `false`, default `false`)
decides whether the file carries the per-task change history that mtix
records locally.

- **Off (the default).** The file is byte-for-byte what mtix wrote before
  this switch existed: `schema_version` stays `2.0.0`, no node has a
  `history` key and there is no top-level `history` list. Released readers
  keep working with it.
- **On.** `schema_version` is `2.1.0` and the envelope's writer-version key (see the table above) names the
  mtix that last wrote the file (this build fills it in even when the caller
  names none), so a reader can tell that an older writer may
  have dropped history. Each node carries its entries in a `history` list
  (omitted when it has none), oldest first: `id`, `task_uid`, `ts`,
  `actor`, `origin`, `kind`, `field`, `old` and `new` (structured fields
  only; free text is never recorded) and `event_id`. History of a task that
  no longer exists goes in the optional top-level `history` list, sorted by
  `task_uid`, `ts`, `id`; the list is omitted when empty, and no
  placeholder task ever stands in for such a task. `task_uid` is the task's
  `uid`, or its `id` when it has none.
- **Checksum.** The node lists are part of each node, and the top-level
  list is part of the hashed document (`{"nodes", "deps", "history"}`; the
  last key is absent when the list is empty), so an edit to any entry fails
  verification.
- **Reading.** mtix accepts 2.1.0 and 2.0.0 files alike. A replace or merge
  import (and the automatic import) unions the file's history into the store
  by entry id, node lists and the top-level list alike. The union is
  idempotent and does not depend on entry order; a local entry is never
  dropped or changed. An entry the store holds identically counts as
  `history_skipped`; one stored under a different entry id but the same
  event key (`task_uid`, `event_id`, `field`) is the same event and counts
  the same way. An entry id the store holds with other content (any field,
  `task_uid` included) is a conflict: the local row is kept, the entry counts
  in `history_conflicts` and a warning names the count and the first few
  entry ids, never the content; the import still succeeds. An entry keyed by
  a legacy dot id instead of a task uid is skipped and counted in
  `history_skipped_legacy_key`. New entries count in `history_added`. An
  entry id the file itself repeats with different content is stored in neither
  copy and counts as a conflict. While `history.export` is on and the
  envelope's writer-version key is older than 0.6.0 (the first release that
  keeps task history) or empty, the import warns that history may be
  incomplete; it is silent otherwise. A file is refused
  when an entry has a kind, origin or time outside what mtix writes, or
  when a top-level entry names a task the file holds.
- **Retention.** With `history.max_entries_per_task` set, a task's oldest
  change entries are trimmed and one `retention_drop` entry is written (field
  `retention`, `new` the count dropped as a decimal string, `old` the time of
  the newest dropped entry, in the `ts` form). Such entries are exempt from
  trimming and travel like any other. When reading, a change entry at or
  before the cutoff this copy of the board recorded when it trimmed the task is
  skipped (counted in `history_skipped`), so an older export does not bring
  trimmed entries back. An entry dated more than 24 hours ahead of the reading machine's clock is
  skipped and counted, and a cutoff is never later than that clock. A change entry dated ahead of that clock (inside the allowance) is neither counted toward the limit nor trimmed until the clock passes it. A stored cutoff later than the clock counts as the clock when reading. That cutoff is a local record of each copy, never part
  of the export, so trimming on one machine does not trim the others. A marker
  from another replica is stored as history but is never a cutoff, so a board
  that never trimmed keeps the grow-only union; a marker whose `old` is not a stored-form time or whose
  `new` is not a positive whole number is skipped with a warning.
- **Conflict detection.** For a 2.1.0 file the writer-version key is left
  out of the hash used to detect conflicts, so an upgrade does not look like
  a change to the board.
- **A running process** follows `mtix config set history.export` without a
  restart. `mtix recover` labels its output 2.1.0 and keeps the history when
  the mirror carries it.
- **Older readers.** A released mtix (0.5.4 and earlier) drops the fields it
  does not know, so a file with history fails its checksum there: `mtix
  import` fails with `checksum verification failed` and the automatic import
  loads no nodes. That is why the switch is off by default.

## Checksum

The checksum is the SHA-256, in lowercase hex, of the JSON document
`{"nodes": [...], "deps": [...]}` (plus `"history": [...]` in a 2.1.0 file
whose top-level list is not empty), with nodes sorted by `id` and
dependencies by `from_id`, then `to_id`. It covers every node field above,
annotations included, so a file edited after mtix wrote it fails
verification. The envelope fields, agents and sessions are not covered.

The document is hashed as a reader decodes it. JSON cannot hold text that
is not valid UTF-8: mtix writes each such byte as the escape `\ufffd`, and
a reader decodes it to the replacement character U+FFFD. mtix therefore
encodes the document, decodes it and encodes it again before hashing, so
the writer and every reader hash the same bytes. For valid UTF-8 the result
is the first encoding, so checksums of other files did not change.

mtix 0.5.3 and earlier hashed the first encoding, so a file they wrote for
text holding invalid UTF-8 never verified. Verification also accepts that
checksum: the same decoded content with every U+FFFD spelled as `\ufffd`.
A file that held both invalid UTF-8 and a genuine U+FFFD character still
fails; check it, then import it with `mtix import --recompute-checksum`.

## Ids that are not valid UTF-8

A node `uid` or a history `task_uid` or `event_id` can hold bytes that are not
valid UTF-8 (a sync event id is accepted as received). JSON cannot carry them,
so the file adds an optional hex field, written only for such a value:

| Where | Field | Plain field beside it |
|---|---|---|
| node | `uid_hex` | `uid` is left out |
| history entry | `task_uid_hex`, `event_id_hex` | `task_uid` and `event_id` keep the U+FFFD spelling |

mtix reads the hex field in preference to the plain one, so the bytes round
trip exactly through export and import. A value that is not hex is refused
(`invalid input`). A file with no such id has none of these fields, and is
byte-for-byte what earlier versions wrote.

The two checksums are split so that older readers keep working. `checksum`
covers exactly what mtix 0.5.4 decodes (no hex field, an invalid-UTF-8 `uid`
left out). mtix 0.5.4 imports a 2.0.0 snapshot (`history.export` off) holding
a hex field and gives that one node a fresh uid; every other column and node is
unchanged. A history file (2.1.0) needs a current reader: 0.5.4 refuses every
2.1.0 file, with or without hex fields. `ids_checksum` covers the hex fields
(each by kind, owner and bytes). mtix verifies both, so an edit to a hex field
is detected, and so is removing one hex field or `ids_checksum` while others
remain. The checksums guard against accidents, not deliberate edits: removing
every hex field together with `ids_checksum` leaves the old-reader form, and
that node then imports with a fresh uid.

The 1.0.0 form (written for a conflict baseline) keeps JSON's U+FFFD spelling
of such a `uid`, as 0.5.3 wrote it, and has no hex field.

## Compatibility

- **Reading older files.** A 1.0.0 file (mtix 0.5.3 and earlier) verifies
  against its original checksum and imports. It carries none of the 2.0.0
  fields. A merge import keeps the local values of those fields,
  annotations and activity included. A replace import leaves every node
  without them.
- **Older clients reading 2.0.0 files.** A client older than 0.5.4 supports
  only 1.x, so its automatic import skips a 2.0.0 file and logs
  `tasks.json schema version is newer than supported — upgrade mtix`. Its
  `mtix import` of the file fails with `checksum verification failed`,
  because it drops the fields it does not know. Nearly every node has
  activity entries, so this applies to practically every file 0.5.4
  writes.
- **An older client must not write to a shared board.** Skipping the file
  protects the older client's store only until its next writing command.
  Every write re-exports `.mtix/tasks.json` from that store, which lacks
  everything the client skipped and every annotation, and records the file
  as in sync. Committing that file reverts every change made upstream since
  the client stopped importing. On such a client even an `mtix import`
  that fails rewrites `.mtix/tasks.json` the same way. Until every
  teammate runs 0.5.4 or later, do not run writing commands or
  `mtix import` on an older client, and do not commit its
  `.mtix/tasks.json`. If such a file was committed, restore
  `.mtix/tasks.json` from the last good commit in git history, or restore
  the store of a machine that imported it from the backup taken before
  that import: `.mtix/data/backups/pre-sync-<time>.db` (0.5.4 and later,
  newest 5 kept) or `.mtix/data/pre-sync-backup.db` (0.5.3 and earlier).
  To restore it, stop every mtix process (the MCP server, `mtix serve` and
  the sync daemon), copy it over `.mtix/data/mtix.db`, delete
  `.mtix/data/mtix.db-wal` and `.mtix/data/mtix.db-shm`, then continue. A 0.5.4
  store is protected from such a file by the auto-import guard
  (MTIX-95.31.2): the file carries no annotations or activity, so its
  automatic import is refused and nothing changes (see "Automatic import"
  below).

## Import

Every import checks the file before it writes anything, and writes nothing
when a check fails:

- the major `schema_version` is not higher than the one this mtix writes;
- `node_count` equals the number of nodes;
- the checksum verifies;
- every time value (node timestamps, annotation and activity times,
  dependency, agent and session times) is RFC 3339 and its UTC year is 1
  to 9999. A value such as `9999-12-31T23:00:00-05:00` is year 10000 in UTC
  and is rejected: no reader could parse it back. The required times (a
  node's `created_at` and `updated_at`, a dependency's `created_at`, a
  session's `started_at`) must not be empty; the others may be. The error
  names the record and the field.

A refused `mtix import` also leaves `.mtix/tasks.json` and its stored hash
as they were: only an import that wrote something re-exports the board.

The automatic import of a changed `.mtix/tasks.json` first exports the
local store to check it for changes the file lacks. If the store cannot be
exported (for example a JSON cell that does not parse), the import is
refused and nothing changes; the message names the node, the field and
`mtix recover`, which salvages everything readable.

**Replace mode** (`mtix import --mode replace`, and the automatic import of
a changed `.mtix/tasks.json`) deletes every node, dependency, agent and
session, then writes the file's content, every field included. An export
followed by a replace import leaves the store as it was. The automatic
import refuses a replace that would delete local data (below); the
explicit `mtix import --mode replace` runs no loss check, but since 0.5.4 it needs the ticket count typed at an interactive terminal (no flag supplies it), so an agent never runs it: ask the user.

**Automatic import** (FR-15.2, MTIX-95.31.2). Before it replaces the
store with a changed `.mtix/tasks.json`, the automatic import runs every
check above, then compares the node and dependency data of the store with
the file, node by node (agents and sessions are runtime state and are
replaced without comparison). These are always a loss when the file lacks
them:

- a node (by id);
- an annotation (by id), or the resolution of one: resolved locally, open
  in the file;
- an activity entry (by the merge key: id, type, author, text and time);
- a dependency (from, to, type);
- the whole local node, when the file gives its id to a different task:
  both carry a `uid` and the uids differ (they are one task only when
  `created_at` is equal and one uid was assigned after the task was
  created: a marked backfill uid, whatever its time, or a UUIDv7 minted
  more than an hour after `created_at`, a clone's backfill at upgrade), or
  the file gives the id to another local task, moved there; listed as "a
  different task under this id", with both titles. A local node whose
  `uid` the file holds under another id is the same task, renumbered by
  another clone: it is compared with that copy, not lost;
- the whole local node, when the file holds its id with another title and
  either copy has no `uid` to compare (a board written before uids were
  shared); listed as "a task under this id with a different title and no
  uid to compare", with both titles. With the same title the two are one
  task.

A non-empty field value that the file leaves empty or leaves out (an empty
string, list or object, `null`, or, for `labels` and `metadata`, the text
`[]`, `{}` or `null`) is a loss unless the file's copy of the node is
current: the file carries activity (`schema_version` 2.0.0 or later), its
copy holds every local activity entry of the node, and its `updated_at` is
not older than the local one. Activity is append-only, and the writes that
record none (`mtix update`, `mtix delete`) still move `updated_at`, so a
current copy has seen the local changes and a field it cleared (unclaim,
reopen, undefer, undelete, a later update) was cleared on purpose. A 1.x
file is never current, nor is a copy whose `updated_at` cannot be read;
clock skew that makes a current copy look older refuses. Numbers are never
empty, and `node_type` is exempt because import derives it from `depth`.
A `uid` the file leaves out is no loss when the local `uid` is a marked
backfill uid: the replace keeps it, so a board without uids imports as
before; a create-time `uid` (a UUIDv7) the file leaves out is a loss. A
stale copy that changes a non-empty value, rather than clearing it, is
applied (a checkout of an older board does this on purpose); the backup
keeps the state before. The rule fails open in one direction: a copy with
a later `updated_at` that missed a local update or delete (neither records
activity), after a concurrent edit or with a clock that runs ahead, counts
as current, so a field it leaves empty is applied as cleared.

On a loss the import is refused and nothing changes: no import, no
backup, the stored hash kept, so it refuses again on every command. The
refusal names the loss node by node and three ways to proceed, each of
which resolves it: `mtix import .mtix/tasks.json --mode merge` (backs up
the database; keeps every local node and dependency, every local
annotation and activity entry, with the file's added, and every field
value the refusal lists, with the node's
local status when the value is part of it; refuses, writing nothing, where a node's status, assignee,
agent state or wake time differs from the file's (it lists each conflict;
rerun it with `--prefer theirs|ours`, or `--theirs ID,ID` and `--ours ID,ID`);
decides the other field values
per node by its content, the title, description, prompt, acceptance and
labels that `content_hash` covers: where the local content matches the
file's, every other local field value stays, and where it
differs, the file's copy wins, so a local edit to that node (an unclaim or
a prompt edit, for example) is undone, except a value the file leaves
empty in a copy that is not current; check `git log -p .mtix/tasks.json`
afterwards and reapply what it undid; a local node whose id the file gives
to a different task, one with another `uid`, is renumbered with its
subtree to the next number free in both, keeping its `uid`, and applied
only with `--confirm`),
`mtix sync --fix` (rewrite the file from the store) and
`mtix import .mtix/tasks.json --mode replace` (the last resort: it needs the ticket count typed at an interactive terminal). The replace re-checks, in
its own transaction, that the store is unchanged since the comparison; if
it changed, nothing is written and the next command compares again. A file that only adds or
changes data is imported, after a verified backup to
`.mtix/data/backups/pre-sync-<UTC time>.db` (the newest 5 are kept; a new
one for every applied import, reused only by a retry of a failed import
while the store is unchanged), and one line on stderr says how many nodes
and dependencies it added, updated and removed. A file that fails its
checks is recorded as a pending refusal too. `sync.auto_sync: false` turns
the automatic import off, except for a store that holds no nodes.
`mtix sync` reports the switch and the last refusal.

**Auto-export never overwrites a pulled board** (FR-15.3e). Every writer,
the CLI and `mtix mcp`, `mtix serve` and the daemon alike, exports to
`.mtix/tasks.json` only when the file on disk is the one mtix last wrote or
imported. Otherwise it runs the automatic import first; when that does not
import the file, the file is kept, the refusal is recorded as pending and
one line names the way out for its kind (a newer schema needs an upgrade,
never `mtix sync --fix`).

**Merge mode** (`mtix import`, the default):

- A node the store does not hold is created as exported.
- For a node the store holds, annotations merge as a union keyed by
  annotation id alone. No local annotation is dropped, so a file without
  annotations keeps the local ones. For an id both sides hold, the local
  copy wins, unless the incoming copy is resolved and the local one is
  not: a resolution never regresses. When anything is added, the list is
  ordered by time, then id.
- The activity stream merges as a union of distinct entries. An entry is
  identified by its id, type, author, text and time together (activity ids
  are derived from a timestamp and can repeat across machines), so two
  different entries that share an id are both kept, and an entry both
  sides hold is kept once. When anything is added, the list is ordered by
  time, then id.
- When the node's `content_hash` (its title, description, prompt,
  acceptance and labels) differs, the file's values replace the other
  fields, so a local edit to them is undone, except a value the file
  leaves empty in a copy that is not current, which stays, with the node's
  local status when the value is part of it. When it is the
  same, the other fields keep their local values, so a change the file
  carries to them (a teammate's claim, for example) is not applied.
- A merge cannot remove an annotation or an activity entry. To make the
  store match a file exactly, use replace mode.

## Local exchange-record format 1

The receipt is local `.mtix/data/exchange-record.json`, separate from the portable export envelope. It requires no SQLite table/schema bump; older binaries ignore it. Header fields are `format` (1), injected UTC `written_at`, `source` (`git-import` or `hub-pull`), `board_form` (`current` or `schema-1.0.0`), `board_hash`, optional `board_file_hash`, optional `parent_board_hash`, `node_count`, `tasks`, and a hub-only `boundary`.

`board_file_hash` is SHA-256 of exact received tasks-file bytes and is absent for hub receipts. `board_hash` uses the existing ordered decoded export-hash form with `exported_at` excluded. Current form binds received node/dependency/agent/session array order and checksum. Historical 1.0.0 uses the existing explicit historical projection/checksum behavior, without new sorting or normalization of the received file. Historical raw and projected hashes can differ. A previous record's board hash is only a local association, not verified ancestry; the writer omits it when it equals the new board hash. Readers may accept an equal association without inventing a lineage cycle rule.

Each task entry contains `kind` (`uid` or `legacy-id`), `identity`, observed `id`, full projected-node SHA-256 `h`, and `fields`, a hash for every declared exported-node field. Task entries sort by explicit identity; their hashes are invariant to node-array order while the whole-board digest remains ordered-form sensitive. UID is used only when actually received; an absent UID is explicitly a legacy ID and is never silently matched to a later backfill. Field hashes bind `{"present":false}` or `{"present":true,"value":VALUE}` using JSON encoding, preserving historical absence versus an explicit value. Full-task hashes include carried evidence and optional fields in the selected export form. Records store hashes, not copies of task prose.

Hub boundaries bind `local_sequence` (local journal rowid, not a hub sequence), both pull cursor halves (`pull_clock`, `pull_event_id`), sweep completion/progress fields, pending staging count and unresolved encountered count to the SAME read-only WAL snapshot as board rows. Completion requires explicit drain/sweep success and no encountered quarantine or missing event; a known local journal or applied event qualifies, with quarantine checked first. Per-pull tracking charges UTF-8 ID bytes plus 64 bookkeeping bytes per unique ID against the existing positive service import cap; duplicates cost zero. This is bounded logical accounting, not an exact RSS guarantee. Overflow conservatively omits a receipt without reducing the pull workload.

Older strict provenance readers using `DisallowUnknownFields` report receipts carrying `pending_outbound` as unavailable.

The additive nullable integer `boundary.pending_outbound` counts all local `sync_events` rows whose status is `pending`, including held push events. It is captured in the same read-only WAL transaction as the board and `local_sequence`; pending staging and outbound counts are separate. New hub receipts require an explicit numeric zero. A positive count withholds publication, preserves the exact prior bytes or continued absence and saves `pending-outbound`; failures saving that diagnostic remain errors. Push acknowledgment alone neither publishes nor clears it: only a new eligible complete pull can publish known zero. Readers accept an absent or null value as legacy unknown and infer read-only `outbound-boundary-unknown`, overridden by a later saved nonempty omission. Nonzero values (including negative values), non-integers and invalid encodings are refused. A known-zero historical receipt remains valid when later local work is pending. Automatic tasks-file imports retain their existing receipt rules and have no hub boundary.

The provenance reader is bounded, validates format/hash/identity/count/time/ownership, returns independent values and never creates paths or repairs temporary files. Valid older provenance remains readable despite local changes or saved omission state. It does not match incoming lineage. Diagnostic state uses only the existing-meta key `meta.sync.exchange_record.reason`; no attempt-timestamp key is added. Status may warn that a saved omission is stale after a diagnostic-clear failure. Unix private mode is not a Windows ACL guarantee; receipt publication on Windows is explicitly unsupported pending native runtime verification.

Diagnostic categories are fixed and bounded at both storage and reporting boundaries. Unknown stored or supplied values become `unknown`, without repeating their original text. A failed completed publication retains its specific category instead of becoming an incomplete-pull report. Fetched responses must match their exact requested batch: unexpected, missing, nil or repeated IDs withhold the receipt while existing valid event admission remains unchanged.

Compatible imports whose schema has no defined receipt hash form still import successfully. They preserve any prior receipt and report `unsupported-schema`; no form is guessed. A failure saving that omission remains an error.
