# mtix export format and `.mtix/tasks.json`

> For users and auditors. The commands are described in
> [USERMANUAL.md](../USERMANUAL.md) (Backup, Export, and Import); the
> requirements are FR-7.8 and FR-15 in [REQUIREMENTS.md](../REQUIREMENTS.md).

`mtix export` writes one JSON document, and mtix keeps the same document in
`.mtix/tasks.json` after every change (the git-tracked board, FR-15). This
page describes that document: what it carries, how its checksum is
computed, and how `mtix import` and the automatic import read it back.

The current format is `schema_version` **2.0.0**, written by mtix 0.5.4 and
later. mtix 0.5.3 and earlier wrote 1.0.0.

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
| `node_count` | Number of entries in `nodes`; an import refuses a file where they differ. |
| `checksum` | SHA-256 of the canonical JSON of `nodes` and `dependencies` (see [Checksum](#checksum)). |

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
| `uid` | 1.0.0 | Durable internal identity; omitted when empty. |
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

## Checksum

The checksum is the SHA-256, in lowercase hex, of the JSON document
`{"nodes": [...], "deps": [...]}`, with nodes sorted by `id` and
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
  the client stopped importing. Until every teammate runs 0.5.4 or later,
  do not run writing commands on an older client, and do not commit its
  `.mtix/tasks.json`. If such a file was committed, restore
  `.mtix/tasks.json` from the last good commit in git history, or restore
  the store of a machine that imported it from
  `.mtix/data/pre-sync-backup.db` (the copy taken before the last automatic
  import; copy it over `.mtix/data/mtix.db` while no mtix process runs). A
  0.5.4 store is protected from such a file by the auto-import guard
  (MTIX-95.31.2), which refuses a replace import that would drop
  annotations or nodes the store holds.

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
followed by a replace import leaves the store as it was.

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
- When the node's `content_hash` differs, the file's values replace the
  other fields. When it is the same, the other fields keep their local
  values.
- A merge cannot remove an annotation or an activity entry. To make the
  store match a file exactly, use replace mode.
