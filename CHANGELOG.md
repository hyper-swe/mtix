# Changelog

All notable changes to mtix are documented in this file.

The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

> **Pre-1.0 note:** mtix is GA-quality and production-ready, but still
> pre-1.0. Minor versions may introduce breaking schema changes until
> 1.0; each ships a documented migration path in its Migration section.

---

## [Unreleased]

### Security
- **Cleared two gRPC advisories that failed the release vulnerability gate, GO-2026-6443 and GO-2026-6348 (MTIX-95.23).** The source-mode `govulncheck` scan that the release gate runs reached both in `google.golang.org/grpc` v1.82.1, but only through the `internal/api/grpc` package, which no mtix binary links. Released binaries, including v0.5.3-beta, were not affected. GO-2026-6348 is heap exhaustion from fragmented HTTP/2 DATA frames. GO-2026-6443 is a panic on a request that carries neither an `:authority` nor a `Host` header, and it occurs only on servers that use xDS routing, which mtix does not. `google.golang.org/grpc` moves to v1.83.2, and the modules it requires move with it (`golang.org/x/net`, `golang.org/x/sys`, `golang.org/x/sync`, `golang.org/x/crypto`, `golang.org/x/text` and `google.golang.org/genproto/googleapis/rpc`; `google.golang.org/protobuf` is unchanged). The scan now reports no reachable vulnerabilities, so the gate passes.

### Fixed
- **The automatic import after a `git pull` deleted local data the pulled `.mtix/tasks.json` lacked, silently (MTIX-95.31.2).** When a `git pull` or checkout changed `.mtix/tasks.json`, the next mtix command, even `mtix show`, replaced the local store with the file, so annotations, activity entries, tasks, dependencies and field values (an assignee, for example) that only the local store held were deleted without a word; a teammate's board without the local annotations took them from 2 to 0. The automatic import now compares the node and dependency data of the store with the file first (agents and sessions are runtime state and are still replaced). A task, an annotation, an annotation's resolution, an activity entry or a dependency the file lacks is always a loss (so a teammate's deliberate `mtix dep remove` is refused too), and so is a local task whose id the file gives to a different task (both carry a uid and the uids differ, as when two clones each create `PROJ-3`), listed as `a different task under this id` with both titles. A field value the file leaves empty is a loss unless the file's copy of the task is current: it holds all of the task's local activity entries and its `updated_at` is not older, so a stale copy, one from before a local `mtix update` or `mtix delete` (which record no activity), or one from a client older than 0.5.4 (which writes no activity) cannot blank a value, while a field a teammate cleared on purpose (unclaim, reopen, undefer, undelete, a later update) applies. The rule fails open in one direction: a copy with a later `updated_at` that missed a local `mtix update` or `mtix delete`, after a concurrent edit to the same task or with a teammate's clock ahead, counts as current, so a field it leaves empty is applied as cleared; the pre-import backup keeps the state before. On a loss the import is refused: nothing is imported, no backup is taken and the stored hash is kept, so the refusal repeats, printed once, on every command. It lists what would be lost, task by task, and three ways to proceed, run from the project root, each of which resolves it: `mtix import .mtix/tasks.json --mode merge` (keeps every local task, annotation, activity entry and dependency and adds the file's; for a task whose content is unchanged the local field values win, so it can undo a teammate's field change), `mtix sync --fix` (keeps the local store and rewrites the file from it) and `mtix import .mtix/tasks.json --mode replace` (the file wins). This protects a 0.5.4 store from a board re-exported by an older client: it is always refused. A board that only adds or changes data is imported as before, now with one line on stderr: `mtix: imported the changed .mtix/tasks.json: nodes N added, N updated, N removed; dependencies N added, N removed (local database backed up to ...)`. A successful automatic import now also refreshes the conflict baseline, so a second pull before any write is imported instead of being reported as a conflict. A conflict (both the store and the file changed since the last sync) whose replace would also lose data is now printed as a refusal with the loss list, which `mtix sync` keeps showing, and its replace option names what the replace deletes. The replace re-checks, inside its own transaction, that the store is unchanged since the comparison: a write that lands in between, from another process or the MCP server, is kept, nothing is imported, and the next command checks the file again. `mtix import --mode merge` no longer overwrites a local task with a different task under the same id (before, it replaced the task's title, description and uid, and no backup held the old one): it renumbers the local task and its subtree to the next number free in both the store and the file, keeping the uid, annotations and activity, and imports the file's task at the id, so the published board keeps its numbers; the renumbering is printed (uid, old id, new id; `--remap-file` saves it) and applied only with `--confirm`, and without it nothing is written. `mtix import --mode replace` itself is not guarded.
- **A write could overwrite a pulled `.mtix/tasks.json` that was never imported (MTIX-95.31.2).** Every write re-exported the store over `.mtix/tasks.json`, so a board pulled while `mtix mcp`, `mtix serve` or the daemon ran (they import only when they start), pulled with automatic import off, or refused by the automatic import was silently replaced by the local board. Now no writer exports over a file that changed on disk since mtix last wrote or imported it: the export first runs the automatic import, and when that does not import the file, keeps it, keeps the write in the local store, records the refusal as pending and says so in one line with the way out for that kind of refusal (a newer schema: upgrade, never `mtix sync --fix`; a file that fails its checks, for example merged by hand: check it, then `mtix import .mtix/tasks.json --recompute-checksum`, or `mtix sync --fix`). `mtix sync --fix` and an explicit `mtix import` of the file resolve it. The export that follows any write, including `mtix import` of another file, imports a changed board first. MCP tool results do not show these notices, so the agent instructions `mtix docs generate` writes (CLAUDE.md, AGENTS.md) now tell agents to run `mtix sync` before committing or pushing `.mtix/tasks.json`.
- **The automatic-import skip list matched command names, not paths (MTIX-95.31.2).** `mtix sync init` and `mtix sync migrate` never imported a changed board because they share a name with `mtix init` and `mtix migrate`. The list now matches the full command path, so only `mtix init`, `mtix export`, `mtix import`, `mtix migrate`, `mtix recover`, `mtix help`, `mtix version`, `mtix sync` itself and `mtix plugin install` skip it.
- **`sync.auto_sync` was documented but never read, so the automatic import could not be turned off (MTIX-95.31.2).** `mtix config set sync.auto_sync false` now turns the automatic import of a changed `.mtix/tasks.json` off. `mtix config set` accepts only true or false and rejects anything else; a value written by hand that is neither leaves automatic import on, the default, with a one-line warning. `.mtix/config.yaml` is tracked in git, so the switch applies to every clone, but a store that holds no tasks, such as a fresh clone's, is always imported: skipping it would let the first write overwrite the board and reuse its ids. The switch does not turn off the automatic export, but no write overwrites a pulled board: with it off, a changed `.mtix/tasks.json` stays until you import it (`mtix import .mtix/tasks.json --mode merge`) or keep your board (`mtix sync --fix`).
- **The backup before an automatic import kept only the last one, and could miss recent writes (MTIX-95.31.2).** It was one file, `.mtix/data/pre-sync-backup.db`, overwritten by every import and copied byte by byte from `mtix.db`, without the changes still in the write-ahead log. Each automatic import now first checks the whole file (format, schema version, node count, checksum, time values, a readable store, no loss), and only then writes a verified snapshot (`VACUUM INTO`, then `PRAGMA quick_check`) to `.mtix/data/backups/pre-sync-<UTC time>.db`; the newest 5 are kept, older `pre-sync-*` backups are deleted, and nothing else in the directory is touched. A file that fails a check or is refused takes no backup, so failing imports never rotate good backups away. Every import that applies takes a new backup of the store as it is then, even of a board imported before; only a retry of an import that failed while writing reuses its backup, and only while the store is unchanged. If the backup cannot be written, the import is skipped with a warning, as before. An existing `.mtix/data/pre-sync-backup.db` is left in place.
- **`mtix export` and `.mtix/tasks.json` left out every annotation, and a replace import, including the automatic import after a `git pull`, deleted them (MTIX-95.31.1).** Annotations hold review verdicts, close receipts and corrections, and `mtix show --json` returns them, but the export carried none, so a committed board kept each ticket's status and lost the evidence behind it. Import never wrote annotations either, so a replace import, which clears the store first, left every node without them. The export also left out the activity stream, `previous_status`, the invalidation fields (`invalidated_at`, `invalidated_by`, `invalidation_reason`), `deleted_by`, `estimate_min`, `actual_min`, `code_refs`, `commit_refs`, `metadata` and `session_id`. The export and `.mtix/tasks.json` now carry every column of every node (`schema_version` 2.0.0): the annotations, `code_refs` and `commit_refs` in the structure `mtix show --json` returns, every activity entry, and `previous_status`, the invalidation fields, `deleted_by`, `estimate_min`, `actual_min`, `metadata` and `session_id`. The checksum covers them, so an edited annotation fails verification. The major version rose from 1 to 2 so that a client older than 0.5.4 skips the file as newer than it supports, instead of dropping the fields it does not know (see Upgrading). No column is left out; [docs/EXPORT-FORMAT.md](docs/EXPORT-FORMAT.md) lists every field and explains why the derived ones are included. A replace import writes every exported field back, so an export followed by a replace import leaves the store as it was. A merge import never drops a local annotation: annotations merge as a union by annotation id, so a file without annotations keeps the local ones; for an id both sides hold, the local copy wins unless the incoming copy is resolved and the local one is not, so a resolution never regresses. The activity stream merges as a union of distinct entries: an entry is identified by its id, type, author, text and time together, so two different entries that share an id are both kept, and an entry both sides hold is kept once. A file written by 0.5.3 or earlier (`schema_version` 1.0.0) still verifies and imports: a merge keeps the local annotations, activity and other 2.0.0 fields, and a replace leaves every node without them, as a replace always did. Import now also rejects any time value that no reader could parse back (not RFC 3339, or a UTC year outside 1 to 9999, such as `9999-12-31T23:00:00-05:00`) in a node, annotation, activity entry, dependency, agent or session, and an empty value in a required time field (a node's `created_at` or `updated_at`, a dependency's `created_at`, a session's `started_at`). The error names the record and the field, and nothing is written. `mtix import` now also applies the FR-15.2g check the automatic import applies: it refuses a file whose major `schema_version` is higher than the one it writes, with `newer than supported ... upgrade mtix`, before writing anything. A node whose stored annotations, activity, `code_refs` or `commit_refs` cannot be parsed now fails `mtix export` and the automatic export with an error that names the node, the field and `mtix recover`, instead of being exported without them; `mtix recover` salvages such a node without that field and says so in its report. While the store cannot be exported, the automatic import refuses a changed `.mtix/tasks.json` and changes nothing, with the same message: it cannot rule out local changes the file lacks, which a replace import would lose. A refused `mtix import` (for example a file from a newer major schema version) no longer rewrites `.mtix/tasks.json` or its stored hash; only an import that wrote something re-exports the board. Annotations lost by an earlier replace import are not restored by upgrading.
- **A `.mtix/tasks.json` written by mtix could fail its own checksum, so the automatic import after a `git pull` or checkout, and `mtix import` of the file, refused it (MTIX-107.39).** When a stored text held bytes that are not valid UTF-8 (for example a multi-byte character cut in half), 0.5.3 hashed the first JSON encoding of the export, in which each such byte appears as the escape `\ufffd`. A reader decodes that escape to the replacement character U+FFFD and re-encodes it as the raw character, so the reader's encoding differed from the one hashed and the checksum never matched. The next command then logged `auto-import failed: checksum verification failed` and skipped the file, and `mtix import` failed the same way, although mtix had written the file and nobody had edited it. The checksum is now computed over the export as a reader decodes it; for valid text nothing changes, so other checksums stay the same. A file written by 0.5.3 or earlier that held such text now verifies too, unless it also held a genuine U+FFFD character; check such a file, then import it with `mtix import --recompute-checksum`. The scenario as first reported (create a task, comment, copy `tasks.json`, write again, restore the copy) did not reproduce on its own: without such text, a restored copy always verified.
- **`mtix sync pull` re-applied this client's own pushed events (MTIX-95.2).** A pull returns every hub event past the cursor, including the events this client pushed itself, and apply deduplicated only on `applied_events`, which a locally emitted event does not enter until its first echo. Each own event was therefore applied a second time. A field update whose field had any other event in the local log (earlier or newer) logged a spurious conflict, and the same pair was logged twice when the field had been written twice. A replayed claim, unclaim, defer or status change overwrote newer local state: claim, push, `mtix done`, pull left the node `in_progress` with `closed_at` still set. An event that is already in the local event log is now recorded as applied and its clocks are merged, but it is not applied again and logs no conflict. Events from other clients are resolved exactly as before, and hooks and inbox delivery still fire once per event. Conflict rows and statuses written by earlier pulls are not changed by upgrading.
- **Subtree operations could reach another project's nodes when a project prefix contains `_` (MTIX-95.17).** Cascade delete, cascade cancel, undelete, the tree view, subtree-scoped stats and the `under` subtree filter of list and search (CLI, REST API and MCP) select a node's descendants by matching its ID with SQL `LIKE`, which reads `_` as "any one character". The FR-2.1a prefix grammar that `mtix init` and `mtix create` enforce rejects `_`, but the sync `project_prefix` grammar accepts it, so a project received through sync can have one. An operation on `A_B-1` therefore also matched `AXB-1.1` in a different project: a cascade delete or cancel changed it, undelete restored it, and tree, stats, list and search included it. Every subtree pattern now escapes the ID, so it matches only the node's own descendants. Neither grammar changes (the sync grammar still accepts `_`, so events already on a hub keep applying); the difference between them is now documented in the code. Nodes changed by an earlier cross-project cascade are not repaired by upgrading.
- **Undelete brought back descendants that had been deleted separately (MTIX-95.18).** Undeleting a node restored every soft-deleted descendant, including one that was deleted on its own before the parent's cascade delete, so deliberately deleted work came back and the parent's progress no longer matched its children. Each delete now records which delete removed each node, in a local `cascade_deletes` table that is created automatically. The schema version does not change, so earlier 0.5.x binaries still open the database. Undelete restores the node and only the descendants that the same delete removed, even when both deletes happened in the same second by the same author. Undeleting a descendant that an ancestor's cascade removed restores it and the descendants that the same cascade removed; the ancestor stays deleted. The parent of every restored node has its progress recomputed. Each record also carries the node's deletion time and author and is used only while they still match the node. An earlier 0.5.x binary restores without clearing records and deletes without writing them, so a node it has restored and deleted again is normally treated as having no record; a re-delete in the same second by the same author can still look recorded. For a node without a usable record (deleted before upgrading, by an earlier binary, or received through sync or an import), undelete restores the descendants without a usable record that were deleted in the same second by the same author, which is how an earlier cascade looked. `mtix rerun --strategy delete` deletes each descendant separately, so after a rerun delete undelete restores one node at a time: undelete each node, starting from the top. Undelete still emits no sync event, so other replicas keep the node deleted. Nodes restored by an earlier undelete are not changed by upgrading.
- **An older claim, unclaim, defer or status change pulled from the hub could revert newer state, and replicas could disagree on who holds a task (MTIX-95.10).** These workflow events were applied unconditionally in arrival order, so whichever arrived last won. A late claim from a teammate reopened a task marked done locally, and two agents that claimed the same task between syncs could each end up seeing the other agent's claim on their own machine. A pulled workflow event now applies only if it is newer than every workflow event this client already holds for that task, not counting malformed ones (see below): the higher Lamport clock wins, and a tie goes to the higher event id. For claims and status changes, which travel as events, every client therefore ends on the same status whatever order they arrived in, and clients that received the events by sync also agree on `closed_at`. Two automatic changes are not synced as events, so status can still differ there: a task claimed on one client while another adds a dependency that blocks it can end up blocked on one and in progress on the other, and descendants of a cascade cancel can differ the same way. The client that made a change can still show a different `closed_at`: it stamps its own clock, which can differ from the event's time within the same second; an invalidation leaves its `closed_at` as it was (empty for an open task) while other clients stamp it; and a restore from invalidated keeps its `closed_at` while other clients clear it. An older event is still recorded as received, but it changes nothing and is not listed as a conflict. A terminal status (`done`, `cancelled`, `invalidated`) that arrives by sync now stamps `closed_at` from the time the change was made, in whole seconds, rather than the time it was pulled; a change time after year 9999, which the timestamp format cannot hold, falls back to the time it was pulled so the task stays readable. A synced `done` now also sets the task's progress to 1.0, a synced block or invalidation records the previous status, and an auto-unblock clears it, as the local commands do. Known limit: the assignee and other secondary fields can still differ between clients when events arrive out of order. For example, if two agents claim a task between syncs and the agent whose claim lost marks it done before pulling, both clients show the task done, each with its own agent as the assignee. A status change to a status this version does not know (for example one added by a newer version) sets the status alone and logs a warning. A malformed workflow event (a status change with no usable target status, or whose content cannot be read, or a claim or defer whose content cannot be read, including a defer date that is not a valid time) is recorded and changes nothing. It never counts as the newest event for its task, so it cannot block an older claim or status change, and clients agree on the status whatever order they received it in; when it is newer than every well-formed workflow event held for the task, a warning names it (MTIX-95.27). Neither stops the pull; before this change, a claim or defer whose content could not be read failed that pull and every later one. Statuses written by earlier pulls are not changed by upgrading.
- **A cascade cancel left the descendants' dependents blocked and their ancestors' progress stale (MTIX-95.21).** `mtix cancel --cascade` (and the cascade option of the REST API and of the MCP `mtix_cancel` tool) unblocked only the tasks that the named node itself was blocking, and recomputed progress only above the named node. A task blocked by a cancelled descendant stayed blocked, and the named node and every cancelled descendant with children kept their old progress. The cascade now does for every descendant it cancels what a single cancel does: each task it was blocking is unblocked once no other blocker is unresolved, returning to the status it had before it was blocked, and the progress of every node above a cancelled descendant is recomputed once, deepest first, up to the top of the tree. All of it happens in the cancel's own transaction, so a failure changes nothing. A cancel without `--cascade` behaves as before. The cascade still emits no sync event for the descendants in 0.5.x: other replicas receive the named node's cancel and the unblock of each task it released, but they keep the descendants in their previous status, so a task the cascade unblocked shows as unblocked there while the descendant that blocked it is not cancelled. Statuses and progress left by an earlier cascade are not repaired by upgrading. To release a task that an earlier cascade left `blocked`, run `mtix unblock <id>`: it re-derives the task's status from its current blockers and clears the block once all of them are resolved. No command recomputes stale progress; a node's progress is recomputed the next time one of its children changes status.
- **`mtix show` prints annotation, description and prompt text safely (MTIX-100.1).** Annotation author, addressee and text, the description and the prompt are normalized before they reach the terminal: control characters other than newline and tab are removed, so a stored carriage return or escape sequence can no longer overwrite a line (for example make a FAIL verdict read as PASS) or restyle the terminal; CRLF becomes LF; leading blank lines and trailing whitespace are trimmed; an empty annotation shows `(empty)` and a missing timestamp `unknown time`; a description or prompt that holds only whitespace or control characters is omitted like an unset one; the prompt is cut to 100 characters on a character boundary, so a cut can no longer leave a stray byte; and continuation lines are indented so no stored line can pass for a label or for `Annotations: none`. Title and assignee still print as stored (MTIX-100.6), and invisible Unicode formatting characters such as bidirectional overrides are not removed yet (MTIX-100.7). `--json` still prints the raw record.
- **`mtix show` prints a node's annotations (MTIX-98).** `show` printed no annotations, although they carry review verdicts and `mtix show --json` already returned them, so a node with a FAIL verdict looked as if it had none and a successful `mtix annotate` looked like a failed write. `show` now prints every annotation after the description, oldest first, each with its ISO-8601 UTC timestamp and author, the addressee and a resolved marker when present, and further lines of text indented. A node with no annotations prints `Annotations: none`. The help text now lists exactly the lines `show` prints. JSON output and the MCP show tool, which already returned annotations, are unchanged.
- **`mtix sync pull` could permanently miss changes a teammate pushed after working offline (MTIX-95.5).** Pull asks the hub for the events whose Lamport clock is above this client's cursor, and that clock is stamped by the client that made the change. A teammate who worked offline pushes events stamped below the cursors of teammates who kept working, so those teammates never pulled them and their copies silently diverged. After its cursor pass, pull now sweeps for late events: it lists the ids of the hub events created since its previous sweep, minus a 15-minute overlap, and stages the ones this client does not hold; once the listing is complete it applies all the staged events through the same path, in Lamport order across the whole sweep, reading them from the staging table `--limit` at a time so memory and each transaction stay bounded. A late push can also straddle a teammate's cursor (a task's creation stamped below it and an edit above it), which made the regular fetch fail on the edit and every later pull fail the same way; pull now runs the sweep when that happens and retries the regular fetch once, so a task's creation applies before its edits in both cases. The window and the recorded sweep time come from the hub's clock only, so a wrong clock on the client has no effect. A recovered claim, unclaim, defer or status change that is older than the task's newest one is recorded as received and changes nothing (the MTIX-95.10 rule). The first pull after upgrading compares the full hub event history once, in pages, and prints `late-event sweep (first run, full hub history): N late events recovered`, so changes missed before the upgrade arrive then; later pulls print `late-event sweep: N late events recovered` only when they recover something. A sweep saves its listing position with each page's staged ids, and staged events stay staged until applied, so on a large hub a comparison that a pull cannot finish in time (a daemon pull stops after 60 seconds) continues on the next pull instead of starting over. In the common case (nothing missing, and at most `--limit` events since the last sweep) the sweep adds one hub query to a pull; a larger window adds a listing query per further `--limit` ids, recovered events a fetch per `--limit` of them, and a straddling push one more fetch pass. It runs only during a pull and adds no timer, so an idle hub that scales to zero stays idle. Hub migration 014 adds an index on `sync_events (created_at)`, created only when absent, so the sweep reads only recent events; migrations need the hub owner, so an existing hub gets the index the next time its owner runs `mtix sync init`, and until then each page the sweep lists scans the hub's event table (one scan per pull for the usual window; the one-time full comparison takes one per page, spread across pulls). `mtix sync status` shows the last sweep time (`last sweep`; `last_sweep_at` in `--json`) and whether the first full comparison is still in progress (`full_sweep_in_progress`, with `sweep_pending_events` counting staged events, in `--json`); `mtix sync clone` and `mtix sync reconcile --discard-local` clear the sweep time, the saved progress and the staged events, so the next pull compares the full history again. The staging table (`sync_sweep_pending`) is created automatically in the local database; the schema version does not change. A recovered event that cannot be applied fails the pull, as it would in the cursor pass; it stays staged and the next pull retries it after the events with lower Lamport clocks. Known limit: the overlap covers push transactions of normal length, which take seconds; an event from a stalled push transaction that started more than 15 minutes before a sweep and committed after it is not recovered by later pulls.
- **`mtix defer --until` checked the wake time and then dropped it, so a deferred task never woke (MTIX-95.22).** The timestamp was validated and never stored, on every path: the CLI, the MCP `mtix_defer` tool (which also accepted a value that is not a timestamp), the REST API and the gRPC handler. A task deferred until a date therefore stayed deferred, `mtix ready` listed it at once, and a claim was never refused. The wake time is now stored in the same transaction as the deferral, in UTC and whole seconds (`defer_until` in `mtix show <id> --json`). A wake time whose UTC year is outside 1 to 9999 (for example `9999-12-31T23:00:00-05:00`, which is year 10000 in UTC) is rejected as invalid input on every path, because it would be stored as text that no read of the task or its project could parse; a synced defer event carrying one stores no wake time. It counts as passed once it is equal to or earlier than now, one rule for claims, `mtix ready` and the background pass: before it a claim is refused; from it on `mtix ready` lists the task, a claim succeeds, and the next background pass (`mtix gc`, or `POST /api/v1/admin/gc` on a running server) reopens it. The pass re-checks each task in the transaction that reopens it, so a claim or a new deferral made after the pass found the task is kept. A defer without `--until` stores no wake time and clears an earlier one; such a task is listed by `mtix ready` and can be claimed at once, as before. Deferring a task that is already deferred replaces its wake time and records the change in the task's activity. Leaving deferred clears the wake time in the same change: the background pass, a reopen, a claim, a cancel (including a cascade cancel) and an invalidation, and any claim, unclaim or status change received through sync. A deferral received through sync stores no wake time, so a wake time left from an earlier deferral can no longer reopen it and undo it on every machine. The MCP tool now rejects an `until` that is not RFC 3339 as invalid input. The REST API answers `400` to a request body that is not empty or a JSON object whose only key is `until` (for example a literal `null`, a misspelled key or data after the object) instead of ignoring it; an empty body still defers. `mtix defer` also recorded its author as `cli` instead of this process's identity; it now records the process identity (`MTIX_AUTHOR_ID`, then the `author_id` config key, then `cli`) on the activity entry and the sync event. The MCP defer tool served by `mtix mcp` records `MTIX_AUTHOR_ID` or the `author_id` config key when one of them is set, and `mcp`, as before, when neither is. Known limit in 0.5.x: hub sync does not carry the wake time. It still sends the deferral as the same status-change event as before, so other machines that sync through the hub see the task deferred with no wake time. A git-tracked `.mtix/tasks.json` does carry `defer_until`, so a machine that imports that file gets the wake time. Tasks deferred before upgrading have no stored wake time; defer them again with `--until` to set one.

### Changed
- **`mtix sync` reports the automatic import state (MTIX-95.31.2).** Besides the drift report, it prints the `sync.auto_sync` value as configured and whether automatic import is on, for example `Auto-import: enabled (sync.auto_sync: true)`, and the last automatic import mtix refused or skipped for you to decide (a lossy replace, a conflict, a newer schema, a local store that cannot be exported, or a failed backup) with its time, its reason and whether it is pending or resolved. `mtix sync --json` adds `auto_import` (`enabled`, `setting`, `setting_error`, `last_refusal` with its `kind`). The record lives in `.mtix/data/auto-import-refusal.json`.
- **The solo workflow now keeps `.mtix/data/` out of git (MTIX-95.31.2).** It told solo users to commit `.mtix/data/`, which would also commit the pre-import backups and other local sync state; the database is a local cache of `.mtix/tasks.json`, which a fresh clone imports. If you followed the old advice, add `.mtix/data/` to `.gitignore`.

### Upgrading
- **Upgrade every teammate who shares a `.mtix/tasks.json` board to 0.5.4 or later, and until then keep older clients from writing (MTIX-95.31.1).** 0.5.4 writes the board as `schema_version` 2.0.0, which carries annotations, the activity stream and the other node fields. A client older than 0.5.4 supports only 1.x, so its automatic import skips the file and logs `tasks.json schema version is newer than supported — upgrade mtix`; its `mtix import` of the file fails with `checksum verification failed`, because it drops the fields it does not know. Nearly every node has activity entries, so this applies to practically every board 0.5.4 writes, not only to boards with comments. Skipping the file protects the older client's store only until its next writing command: every write re-exports `.mtix/tasks.json` from that store, which lacks everything it skipped and every annotation, and records the file as in sync. Committing that file reverts every change made upstream since the client stopped importing. On such a client even an `mtix import` that fails rewrites `.mtix/tasks.json` from its store the same way, so running `mtix import .mtix/tasks.json` there is no remedy. So, until every teammate runs 0.5.4 or later, do not run writing commands or `mtix import` on an older client and do not commit its `.mtix/tasks.json`. If such a file was committed, restore `.mtix/tasks.json` from the last good commit in git history, or restore the store of a machine that imported it from the backup taken before that import: `.mtix/data/backups/pre-sync-<time>.db` on 0.5.4, `.mtix/data/pre-sync-backup.db` on 0.5.3 and earlier (copy it over `.mtix/data/mtix.db` while no mtix process runs). A 0.5.4 store is protected from such a file by the auto-import guard (MTIX-95.31.2): the file carries no annotations or activity, so its automatic import is refused and nothing changes. 0.5.4 reads boards written by older clients (1.x). `.mtix/tasks.json` grows, because it now carries every comment and each node's activity.

## [0.5.3-beta] - 2026-09-03

A security patch. Upgrading is a drop-in binary replacement — no schema, API or CLI changes.

### Security
- **Cleared 11 reachable vulnerabilities (MTIX-83).** `govulncheck` reported eleven vulnerabilities that this code actually calls: seven in the Go standard library — including `crypto/tls`, `net/http`, `net/url`, `html/template`, `encoding/xml` and `encoding/asn1` — plus `google.golang.org/grpc`, `golang.org/x/text`, `golang.org/x/net` and `github.com/quic-go/quic-go`. Reachability was not theoretical: `mtix serve` is an HTTP server, `crypto/tls` carries every connection to a Postgres hub, and the scanner traced a path from the webhook adapter into `http.Client.Do`. The toolchain moves to go1.26.6 and the four modules to their fixed releases; `govulncheck` now reports zero. These were present in v0.5.2-beta and earlier.
- **The vulnerability scan is now a gate rather than a suggestion.** It ran nowhere in CI, and the pre-flight check only ever WARNed — and only when the tool happened to be installed, so on a machine without it the step silently passed. That is why eleven advisories accumulated unnoticed. `govulncheck` now runs as a hard gate in the release workflow, and pre-flight FAILS both on findings and on the tool being absent.

---

## [0.5.2-beta] - 2026-09-02

A correctness patch for `mtix sync backfill`. Anyone who has reparented or renumbered a node should upgrade before backfilling; a stream produced by 0.5.1-beta or earlier can fail to apply on a fresh replica.

### Fixed
- **Backfill emitted a reparented child before its parent, breaking `sync clone` (MTIX-88).** `mtix sync backfill` synthesized `create_node` events by walking the `nodes` table in `created_at` order, and lamport is assigned monotonically in walk order. But `created_at` is wall-clock and says nothing about hierarchy: reparenting and renumbering leave a child row whose timestamp predates the parent it now hangs under, so that child was emitted with a *lower* lamport than its parent. Since `nodes.parent_id` is a `FOREIGN KEY` and `foreign_keys` is on for every connection, a consumer applying the stream in lamport order — `mtix sync clone` — failed the child insert with `FOREIGN KEY constraint failed`, deterministically, every time. The reporter saw 8 such violations across two subtrees. Backfill now orders nodes topologically, so a parent always precedes its children. The sort is *stable*: a stream that was already correctly ordered comes back in exactly its previous `created_at` order, so upgrading does not churn event streams that were fine. A child whose parent is absent from the set (a soft-deleted parent, which the walk excludes) is treated as a root rather than dropped.

### Upgrading
- Existing hubs are unaffected — this changes only how a *new* backfill stream is generated, and backfill is already refused by default when `sync_events` is non-empty. If a previous backfill produced an unapplyable stream, re-run `mtix sync backfill --force` on 0.5.2-beta and re-push.

---

## [0.5.1-beta] - 2026-09-01

A patch release: the web project switcher was missing from the shipped binary, and the build toolchain's npm advisories are cleared. No schema, API, or CLI changes, and no Go code changed at all — upgrading is a drop-in binary replacement.

### Security
- **Cleared the npm advisories blocking the release gate (MTIX-82).** v0.5.0-beta carried an `overrides` entry hard-pinning `brace-expansion` to exactly `5.0.8`, added to clear two advisories at the time. A later advisory covers `4.0.0 - 5.0.8`, so the pinned version became vulnerable itself — and because the pin named an exact version, `npm audit fix` could not reach the patched `5.0.9`. The pin was blocking its own remedy. It is removed rather than re-pinned, so patch releases now flow in on their own; upstream has caught up and the tree resolves to a fixed version unaided. `npm audit` reports zero vulnerabilities. Every affected package was a devDependency of the build/lint toolchain — the runtime dependencies (`react`, `react-dom`) were never affected and nothing vulnerable was ever inside the binary. No semver-major bumps: eslint stays on 9.x, and the eleven changed packages move by patch level only. The rebuilt SPA bundle is byte-identical to v0.5.1-beta's, so the shipped artifact is unchanged.

- **Patched a browserslist advisory that landed mid-release (MTIX-84).** `browserslist` at or below 4.28.6 is subject to unbounded memory growth and an uncaught crash / prototype write when reading untrusted custom stats (GHSA-c83g-rgw3-j3cx, GHSA-73wf-gq98-2v4g). It resolves to 4.28.8, alongside the caniuse/electron-to-chromium data packages it carries. Build tooling only — not in the shipped bundle — and the rebuilt CSS and JS are byte-identical to the previous build, so autoprefixer's output is unaffected.

### Fixed
- **The project scope selector was absent at runtime (MTIX-81).** In v0.5.0-beta the multi-project web UI shipped its backend but not its frontend: the top bar showed a legacy "Select Project" button that did nothing on click (it carried only hover handlers and no `onClick`), and there was no way to view or switch to a non-primary project. The SPA source was correct throughout — `internal/web/dist/`, the tree the binary serves via `//go:embed`, had not been regenerated since before the multi-project UI landed, so every binary built after it embedded the older bundle. The baseline is now rebuilt from the same source the release ships. Users on v0.5.0-beta who need a workaround before upgrading can scope through the API directly (`GET /api/v1/search?project=<prefix>`, or `?project=all`), which was never affected.
- **The embed baseline could not be committed, which is why the drift persisted (MTIX-81).** The GoReleaser ignore rule was an unanchored `dist/`, which also matched `internal/web/dist/`. Its files stayed tracked only because tracked files override `.gitignore`, but the SPA build emits content-hashed filenames, so each rebuild produced new names that git silently refused to add — a regenerated bundle could be built and never land in a commit. The rule is now anchored to `/dist/`; `web/dist/` keeps its own rule.
- **The release pipeline's UI rebuild never overwrote anything (MTIX-81).** Both the CI and release workflows ran `cp -r dist ../internal/web/dist`, but that destination is tracked and already exists, so the copy nested the fresh build at `internal/web/dist/dist/` and left the committed files untouched — the embed kept serving the old bundle. This is why a stale UI survived a pipeline that appeared to rebuild it on every run. Both workflows now remove the directory first, matching the Makefile.
- **Nothing verified that the embedded UI matched its source.** A new `make embed-check` builds the SPA and diffs it against the tracked baseline, failing on drift instead of silently refreshing it, and runs as part of `make verify` and the pre-flight gate.

---

## [0.5.0-beta] - 2026-07-28

The agent-to-agent coordination release: the FR-19 hooks/inbox foundation and FR-20 origin-independent dispatch together let agents wake and hand off work with no human message-bus — plus corruption hardening for multi-agent filesystems, multi-project support, cloud-Postgres compatibility, per-agent conflict auditability, an MCP read-only mode, and supply-chain/security fixes.

### Added
- **FR-19 event hooks & agent notifications (MTIX-47):** the foundation the coordination fabric is built on. A per-agent **inbox** derived from the event journal (no separate mailbox), addressed comments (`mtix comment --to <agent>`), and a **hooks engine** (`.mtix/hooks.yaml`) whose matcher fires delivery adapters — `inbox`, `webhook`, `append-file`, and `exec` — on journaled events. Includes MCP inbox tools, the `mtix hooks list|fire|log|trust` CLI, and loop prevention (via-hook guard + per-node rate limit). Kills the human-as-message-bus between agents.
- **Origin-independent hook dispatch (FR-20 / MTIX-56.1):** hooks now fire for a journaled event based only on the event being in the journal and the hook not yet having fired for it on this host — never on who wrote the event or how it arrived (local CLI, MCP, sync-arrival from the hub, another process). A durable per-`(hook, event)` **dispatch ledger** replaces the local/synced dual-cursor split: exactly-once per host across restarts, concurrent triggers and out-of-order arrival (the same ledger pattern as the MTIX-55 inbox ack fix). Crash recovery is at-least-once via a claim lease — a trigger that dies between claim and fire is re-fired, never lost; a fire that ran and failed is recorded and never auto-retried. Wake `exec` scripts should be idempotent (check the inbox before launching).
- Fresh clones and first pulls into an empty store initialize the dispatch floor at the journal tail, so bootstrapped history never fires a hook backlog storm.
- **`mtix daemon` (MTIX-56.2/56.3):** the host's first-class event dispatcher — pull-then-dispatch every 5s; fully functional with no hub (local-tail mode for cross-process writes); `mtix daemon install|status|start|stop|uninstall` registers it as an OS service (launchd / systemd --user / Task Scheduler) with boot-start and crash-restart, one service per project.
- **Global `-C, --project-dir <dir>` (MTIX-56.4):** every command can target a named project like `git -C`, applied before store init; `mcp --project` becomes a deprecated alias (`mtix mcp -C dir` unchanged).
- **Prompt-terminated delivery (MTIX-56.8):** `mtix inbox --format prompt|context` emits agent-ready text (events + ack/reply contract; empty inbox → empty output, the wake-script idempotency check); reference wake script at `examples/hooks/wake-agent.sh` launches any harness CLI with the inbox as the prompt.
- **Channel adapter (MTIX-56.7, experimental):** `mtix mcp --channel-agent <id>` also acts as a Claude Code channel (research preview) — pushes the agent's new inbox events into the running session, with ack/reply through the same server's tools, and holds an mtix presence session while serving. Requires launching Claude Code with `--channels` (or the development flag during the preview).
- Three-agent scenario regression test (planner→developer→tester) and the FR-20 cross-host release-gate e2e (exec wake exactly-once, restart-safe, crash-injection re-fire) (MTIX-56.5).
- **Multi-project in one database (MTIX-37, FR-MULTI-PROJECT):** `--project` / `--all-projects` scope on query commands, `mtix projects`, project-aware create defaulting to the primary prefix, a project argument on the MCP query tools, and a multi-project web UI (scope selector, project-aware create, NodeID badges).
- **Cloud Postgres out of the box (MTIX-48):** Neon and Supabase work as sync hubs without special configuration; connect-retry on transient errors; provider setup docs.
- **`mtix unblock <id>` recovery command (MTIX-44):** recompute a node's blocked state on demand, with blocked-lifecycle docs.
- **`--changed-since` filter on `list` and `search` (MTIX-6.1):** query nodes updated after an RFC3339 timestamp or a relative window (`1h`, `30m`, `24h`), so a sync agent can poll "what changed since my last check?" instead of fetching everything and diffing. Composes with the other filters and `--json`.
- **MCP read-only client mode (MTIX-2.1.3):** every MCP tool now carries an access scope — `read` / `write` / `admin`. `mtix mcp --read-only` (or the `mcp.read_only` config key) serves only the query tools and refuses every mutation — for an untrusted or observer agent. Unlisted tools default to `write`, so a newly added tool is never callable read-only until explicitly marked `read`.
- **Per-agent author identity (MTIX-24):** `MTIX_AUTHOR_ID` (per process, set once in an agent's bootstrap) or the `author_id` config key gives each same-machine agent a distinct sync-event author, validated against the FR-18.7 grammar and rejected loudly if invalid. See Security for why this matters.

### Changed
- **Safe install/upgrade path (MTIX-56.11):** new `make install` (unlink-then-copy via `install(1)`, `PREFIX` overridable) and documented upgrade commands for the binary-download path — on macOS an in-place `cp` over an existing binary invalidates its cached code signature and every run is killed (`Killed: 9`); with the daemon installed as a service this becomes a crash loop. `mtix daemon install` output and the manual now carry the upgrade steps.
- **Exec hooks are detached spawns (MTIX-56.9):** dispatch returns at spawn and never blocks a CLI command or agent tool call. "Delivered" now means *spawned*; a script's non-zero exit is the script's own to report (best-effort logged), the inbox ack is the success signal, and `timeout-seconds` is enforced best-effort by the spawning process. Spawn failures stay terminal errors, never auto-retried.
- **Host-local exec dispatch policy (MTIX-56.10):** `mtix hooks exec-dispatch any|daemon|off` — `daemon` routes every wake through the supervised `mtix daemon` (CLI/server triggers defer entirely); `off` makes a host never launch anything while other adapters still deliver. Stored beside the trust hash, never synced.
- **`include-synced` is deprecated and now a no-op** (accepted for config compat). This is a behavior change: hooks that previously fired only on local events now also fire on sync-arrived events, deduped per host by the ledger. Fleet-level control is hook **placement**: a hook configured on N hosts fires on N hosts, once each — put a wake hook only on the host that should run it. `mtix sync daemon --dispatch-hooks` now dispatches events of every origin (no more "designated synced dispatcher").
- **Unsafe filesystems now hard-refuse writes (MTIX-54/57/58).** On a positively-identified unsafe (FUSE/network) filesystem, or when cross-context FUSE access is detected (`.fuse_hidden` orphans present even if the local FS looks safe), the store opens **read-only** — reads, `mtix recover`, and `mtix export` still work, but every write is refused at a single choke point. Both recurring field corruptions came through the old write override, so `MTIX_ALLOW_UNSAFE_FS` is **retired** (ignored with a deprecation warning). See Migration.
- **Import streams the export off disk (MTIX-2.3.1):** `mtix import` decodes with a `json.Decoder` instead of reading the whole file into a byte slice and `json.Unmarshal`-ing it, dropping the redundant whole-file copy held at peak. Trailing non-whitespace after the export object is now rejected (parity with the old parser), so a truncated or concatenated file no longer silently imports its first object and ignores the rest.

### Security
- **Exec trust now pins the wake-script content, not just `hooks.yaml` (MTIX-49).** The content-hash trust folded in only `hooks.yaml`, so editing a script an exec hook runs (approve, then swap the payload) executed new code with no re-consent. `mtix hooks trust` now pins the content of every local file an exec command runs; editing `hooks.yaml` *or* any referenced wake-script voids trust until re-run. Closes the approve-then-swap escalation.
- **`mtix plugin install` no longer writes through dangling symlinks — CWE-59 (MTIX-29).** The write-if-absent install paths used `os.Stat`, which reports a dangling symlink as absent, so a committed symlink in a shared repo could redirect a plugin-install write to an attacker-chosen path outside the project. The absence check now uses `os.Lstat`, so any symlink counts as present and the guard skips it.
- **Unsafe-filesystem write refusal (MTIX-58)** removes the corruption class that a shared/FUSE `.mtix` created; the override that enabled it is gone (see Changed).
- **Same-machine agent conflicts are now auditable (MTIX-24).** Emitted events defaulted every author to `"cli"`, and vector clocks are keyed by author — so two same-machine agents sharing that default produced VC-Equal (not `Concurrent()`) events, and the hub's conflict detector never recorded a `sync_conflicts` row for their contested edits. Convergence via LWW was always correct; only audit visibility was reduced. Setting a distinct `MTIX_AUTHOR_ID` / `author_id` per agent makes their concurrent edits `Concurrent()`, so every contested edit lands in the hub conflict log. The old "direct meta UPDATE" runbook workaround is retired.

### Fixed
- **Sticky `blocked` status in team setups (MTIX-44):** sync-applied status changes skipped the derived-state recompute (`unblockDependents`), so a node stayed `blocked` after its dependency closed on another machine. Sync-apply now recomputes derived state.
- **Multi-hyphen project prefixes corrupted on clone (MTIX-39):** the sync emit path mis-parsed a multi-hyphen prefix, corrupting the project column. Derivation + grammar fixed; covered by a cloud-contract case (MTIX-41).
- **Cloud-Postgres contract gate was a false-green (MTIX-42):** provider tests skipped silently when the Supabase/Neon DSN secrets were unset. The gate now runs against real cloud Postgres.
- **Stale/NULL `content_hash` on sync-applied edits (MTIX-46):** local `UpdateNode` recomputes `content_hash` on a content change (FR-3.7), but the sync-apply handlers skipped it — a synced content edit left a stale hash and a synced-created node carried a NULL hash. Since `content_hash` feeds export and import-merge identity detection (FR-7.8), replicas could diverge byte-for-byte on export and mis-detect content identity on merge (sync convergence via LWW was never affected). Sync-apply now maintains the hash.
- **`mtix sync backup` failed on a verify-full DSN with no trust root (MTIX-59):** it passed the DSN verbatim to `pg_dump`, which then aborted looking for `~/.postgresql/root.crt`. When the DSN requests certificate verification but names no `sslrootcert` (and none is otherwise configured), backup now defaults `PGSSLROOTCERT=system` (the OS trust store), so a public-CA cloud hub backs up out of the box; a private-CA hub still needs an explicit cert.
- **`mtix sync backup` failed on a DSN with a special-character password (MTIX-61):** it handed the DSN verbatim to `pg_dump`, whose libpq URI parser mis-split a password containing an unencoded `@` (a common cloud-pooler case) — while every other connection uses Go's `pgx`, which tolerates it, so only backup failed. Backup now parses the DSN with `pgx` and hands `pg_dump` the connection via `PG*` environment variables (literal values, no parsing ambiguity for any password).
- **Cloud e2e coverage for four previously untested CLI paths (MTIX-21):** `mtix sync backup` (pg_dump through a connection pooler), `sync daemon` (sustained periodic pull), `sync conflicts resolve` (LWW round-trip), and `sync reconcile` (`--discard-local` / `--rename-to` / `--import-as`) had only unit coverage — they are now exercised end-to-end against real Neon and Supabase in the cloud-contract release gate.

### Migration
- **Shared-filesystem `.mtix` users must move to one local `.mtix` per machine + a sync hub.** With `MTIX_ALLOW_UNSAFE_FS` retired (MTIX-58), a `.mtix` on a FUSE/network mount now opens read-only and refuses writes — the setup that caused the field corruptions. Migrate each context to its own local store and replicate through the Postgres hub (this is exactly the FR-20 hub-per-host topology). Reads and `mtix recover` still work on the old store to get data out.
- **Re-run `mtix hooks trust` after editing any wake-script (MTIX-49),** not just after editing `hooks.yaml` — exec is now skipped until the new script content is trusted.
- **Wake `exec` hooks are now detached and their exit codes are no longer in `mtix hooks log` (MTIX-56.9);** scripts should self-report failures. **Hooks now fire on sync-arrived events (MTIX-56.4 `include-synced` no-op);** place a wake hook only on the host that should run it.

---

## [0.4.0-beta] - 2026-06-29

### Added
- **Distributed node identity & team sync (MTIX-30, ADR-003):** dot-path IDs now stay clean under concurrent and offline creation. Each node has a stable internal `uid` (its create-event id) so a renumber moves a display number without breaking references; the surface still shows only the dot-path.
  - Offline-created nodes get a provisional ID (a uid-shaped segment) and auto-settle into a clean number on the next sync (MTIX-30.3). mtix warns before a provisional ID is externalized into a commit or PR.
  - Concurrent creates of the same number auto-resolve: the hub registry (a derived partial-unique index) accepts the first and tells the second to renumber. Both nodes survive — this fixes MTIX-28 (concurrent create silently losing one node) (MTIX-30.4 / 30.7).
  - Subtree renumber is atomic: one transaction rewrites the node and all descendants, so no read sees a torn subtree (MTIX-30.5).
  - Restore-from-backup safety: the rare settled-vs-settled collision is never auto-picked. `mtix sync mark-restored` (operator-only) opens a restore window; `mtix sync collisions list` and `mtix sync collisions resolve <id> --winner held|incoming` let an admin choose which node keeps the number while the other renumbers. No node is ever lost, and only the affected node is blocked — the rest of the team keeps syncing (MTIX-30.8).
  - `mtix sync migrate` drives the one-time migration (uid backfill, hub dedup sweep, version-gated registry index); idempotent and a no-op once complete (MTIX-30.9, 30.10, 30.14).
  - New safety scenarios 12–18 in `docs/traceability.json` (restore Option B, same-epoch no-false-positive, atomic renumber, import uid validation, crash-resilience, ENOSPC on a sync write, online concurrent-create) are gated by `traceability_test.go`. Design and audit rationale in [ADR-003](ADR-003-DISTRIBUTED-NODE-IDENTITY.md); operator docs in the USERMANUAL "Distributed identity & team sync" section; trust model in `docs/SECURITY-MODEL.md`.
- **Codex and pi plugin targets (MTIX-27, issue #15):** `mtix plugin install --target codex` writes the project's AGENTS.md briefing and a `[mcp_servers.mtix]` entry in `.codex/config.toml` (`--global` for `~/.codex/`); existing files are never modified — the stanza to add is printed instead. `--target pi` installs AGENTS.md (which pi loads natively; `--global` for `~/.pi/agent/`) and prints pi-mcp-adapter setup guidance, since pi has no built-in MCP by design. New `docs/mcp-config/codex.toml` snippet; MCP-SETUP sections for both agents.

### Changed
- `mtix plugin install` help no longer advertises cursor/windsurf targets that were never implemented (manual MCP config for those remains documented in MCP-SETUP).

---

## [v0.3.0-beta] — 2026-06-11

**Headline: storage durability hardening (NFR-2.8) — refuse, mirror, back up, recover.**
Driven by a field incident in which a database was torn by a WAL
checkpoint on a 99%-full disk and the data was unrecoverable. mtix now
refuses work it cannot finish safely, keeps the tasks.json mirror current
on every interface, takes automatic verified backups, and ships a
first-class salvage path — with a fault-injection suite proving all of it
on every CI build. No schema migration: the database schema version is
unchanged; v0.2.0-beta projects open directly.

**Notable behavior changes:**
- Writes are refused below an 8 MiB free-disk floor (`MTIX_MIN_FREE_BYTES`
  to tune, `0` disables); reads keep working.
- Automatic rolling backups are ON by default (daily, keep 7, under
  `.mtix/data/backups/`); disable with `MTIX_BACKUP_INTERVAL=0`.
- Corrupted databases are refused at open with recovery guidance and
  exit code 4; disk-full failures exit 3.

### Added
- **Disk-full safety (NFR-2.8, MTIX-26):** free-space pre-flight before every write transaction and backup (`MTIX_MIN_FREE_BYTES`, default 8 MiB floor); fail-stop latch on fatal storage errors (disk full, I/O error, detected corruption) — mtix refuses further writes instead of continuing into undefined state; database-open failures on packed volumes now name disk pressure instead of a bare `SQLITE_CANTOPEN`.
- **Integrity check on open (NFR-2.6a, MTIX-26.4):** truncated database files (in-header page count exceeding file size with no WAL to replay) are refused *before* the first connection opens, preserving recovery evidence; `PRAGMA quick_check` runs before any write on every open. `MTIX_SKIP_INTEGRITY_CHECK=1` is the documented recovery-tooling escape hatch (bypasses both gates, with a DANGER log).
- **Mirror parity for long-running interfaces (FR-15.3, MTIX-26.1):** mutations made through the MCP server, `mtix serve`, and `mtix sync daemon` now update the `.mtix/tasks.json` mirror via a debounced store on-commit hook — previously only CLI commands exported, leaving agent-driven projects without the redundancy layer.
- **`mtix recover` + `import --recompute-checksum` (MTIX-26.5):** salvage readable rows from a damaged database read-only (per-row reads, `cell_size_check=OFF`), merge unreadable rows from the tasks.json mirror, synthesize placeholder parents, and emit an importable export plus a salvage report — without modifying the damaged files. `--recompute-checksum` (loudly) accepts hand-reconstructed exports.
- **Automated rolling backups (MTIX-26.6):** verified snapshots into `.mtix/data/backups/` on the post-mutation cadence (default daily, keep 7; `MTIX_BACKUP_INTERVAL` / `MTIX_BACKUP_RETAIN`); failures log and never fail the command or disturb existing backups.
- **Structured exit codes (MTIX-26.8):** `3` = disk full, `4` = corrupted, `1` = generic — CLI contract documented in USERMANUAL and asserted by the fault-injection suite.
- **Claims-to-test traceability gate (MTIX-26.8):** `docs/traceability.json` maps every QUALITY-STANDARDS §3.6 safety scenario to test functions; `traceability_test.go` fails the build when a declared scenario lacks a linked existing test.
- **Fault-injection conformance suite (MTIX-26.7):** `e2e/faultinject` drives the real binary through disk-full writes, genuine ENOSPC, kill -9 mid-write, the 2026-05-19 field-incident signature, and a full recover round trip, on a dedicated tiny volume; runs on every CI build (`test-fault-injection` job). Local harness: `scripts/faultfs.sh`.
- **ADR-002 (MTIX-26.9):** records the decision to not add a local event journal or content-addressed bodies now, with revisit triggers.

- **Release process (MTIX-22):** `docs/RELEASE-CHECKLIST.md` run before every tag; all four deferred post-MTIX-15 audit findings dispositioned (`docs/audit/MTIX-22-deferred-dispositions.md`); auto-generated CLI reference regenerated.

### Changed
- Write connections now set `PRAGMA synchronous = FULL` and `PRAGMA wal_autocheckpoint = 1000` explicitly instead of relying on driver defaults (MTIX-26.3); ADR-001's stale `synchronous=NORMAL` reference corrected.

### Security
- Go toolchain pinned to go1.26.4: fixes two reachable standard-library issues (net/textproto error escaping via the MCP stdio reader; crypto/x509 hostname-parsing inefficiency on the HTTPS serve path). `govulncheck`: 0 reachable vulnerabilities.
- Web dev-dependency advisories resolved (`npm audit`: 0 vulnerabilities).

---

## [v0.2.0-beta] — 2026-05-18

**Headline: BYO Postgres sync hub for team collaboration (FR-18).**
Local SQLite remains canonical on every CLI; the hub is an
event-sourced replication mechanism, not a tenancy boundary. Solo
workflow is unchanged.

### Architectural framing

The v1.0 design draft (MTIX-14) framed Postgres as the canonical
store. The shipped MTIX-15 design has the local SQLite as canonical
and Postgres as a hub for replication events. The hub never sees
your tasks until you push; teammates see your tasks only after they
pull. Every CLI keeps its own complete copy of the project.

See
[docs/SECURITY-MODEL.md](docs/SECURITY-MODEL.md) (trust contract,
v1.1) and
[docs/SYNC-PROTOCOL.md](docs/SYNC-PROTOCOL.md) (protocol details
for contributors) for the full design rationale.

### Added

- **`mtix sync` subcommand family (10 commands)** — the FR-18
  surface:
  - `mtix sync init [DSN]` — provision hub schema + register
    project. Single-flighted via `pg_advisory_xact_lock` for
    concurrent inits.
  - `mtix sync clone [DSN]` — pull the full event log and replay
    into the local SQLite. Idempotent.
  - `mtix sync push` — drain the local pending queue to the hub.
    Singleton per `.mtix/` via a pushlock.
  - `mtix sync pull` — apply new hub events to the local SQLite.
  - `mtix sync status` — pending queue + last push/pull
    timestamps + machine_hash.
  - `mtix sync doctor` — 5 health checks (PG reachable, schema
    current, queue draining, no orphan applied, secrets file mode).
    Exit code 2 on any failure so CI / monitoring can gate.
  - `mtix sync conflicts list|resolve <id>` — inspect contested
    edits and override LWW per-conflict.
  - `mtix sync reconcile --discard-local|--rename-to|--import-as
    [--dry-run]` — whole-project escape hatches for divergent
    history.
  - `mtix sync daemon [--interval SEC] [--install]` — long-running
    periodic pull; prints systemd / launchd unit when
    `--install` is set.
  - `mtix sync backup --output FILE` — wraps `pg_dump` for the 5
    mtix-owned tables for portable export.
  - `mtix sync backfill [--dry-run]` — **upgrade path for v0.1.x
    users.** Walks the canonical nodes / annotations / dependencies
    tables and synthesizes `sync_events` rows so the next push
    populates the hub with existing history. See Migration section.
- **`mtix_sync_workflow` MCP tool** — exposes sync-state
  recommendations to LLM agents. State buckets: solo,
  sync-configured-no-hub, sync-active, divergent-state-pending,
  hub-unreachable. Output bounded to 4 KB; DSN never returned.
  Untrusted-context warning in the tool description per FR-18.17.
- **Event-sourced sync data plane (12 op_types)** —
  `create_node`, `update_field`, `set_acceptance`, `set_prompt`,
  `transition_status`, `claim`, `unclaim`, `cancel`, `delete`,
  `undelete`, `link_dep`, `unlink_dep`, `comment`. UUID v7
  event IDs, Lamport scalar + vector clock per author, LWW
  resolution at apply time keyed by
  `(lamport, wall_clock_ts, author_machine_hash)`.
- **Append-only hub invariants** — PG triggers raise on
  `UPDATE`/`DELETE` of `audit_log` and `sync_conflicts`.
  Manual conflict resolution INSERTs a `resolution='manual'` row
  that supersedes the LWW row.
- **DSN redactor + panic Recover** — every error / log / MCP
  output / panic flows through `redact.DSN`; `main()` wraps with
  `defer redact.Recover` so panics with a DSN in scope are
  scrubbed before the runtime printer sees them.
- **Performance benchmarks** in `benchmarks/`. Targets met (with
  headroom): solo CLI latency < 10 ms median (~170 µs observed);
  100K-node memory < 50 MB (~20–30 MB observed); push/pull 1000
  events < 5 s (~470 / 520 ms observed); pool MaxConns ≤ 5.
- **Fuzz targets** in `internal/sync/validator/fuzz_test.go`:
  `FuzzEventDecode`, `FuzzVectorClockMerge`,
  `FuzzPushEventsValidation`.
- **3-CLI E2E suite** in `e2e/sync_e2e_test.go` (gated on
  `MTIX_PG_TEST_DSN`): happy-path convergence, LWW conflict,
  divergent history, repeated-push idempotency, 9-agent surge,
  lost-laptop recovery, queue-full refusal, backfill round-trip,
  hub dedup on duplicate force-push, wall-clock-ts preservation.
- **Documentation**
  - `docs/SECURITY-MODEL.md` v1.1 — full sync trust model
    including the "Known audit-trail limitation: same-authorID
    conflicts" tradeoff.
  - `docs/SYNC-PROTOCOL.md` (new) — protocol-level spec for
    contributors and auditors.
  - `docs/SYNC-DESIGN.md` — architectural overview (already
    existed; cross-linked).
  - `docs/audit/MTIX-15-audit-pass2.md` — security audit evidence
    table (22 items PASS) with file:line references to every
    test that proves a requirement.
  - USERMANUAL chapter "Team collaboration with sync (FR-18)".
  - README "Team sync" subsection + 10 sync subcommands in the
    CLI reference.
  - CONTRIBUTING "Testing sync changes" section (local Postgres
    in Docker; fuzz target invocation; DSN hygiene sweep).

### Fixed

- **MTIX-17 — auto-unblock dependents when a blocker is marked
  done, cancelled, or invalidated.** Pre-existing bug reported by
  an external user. `executeTransitionTx` and `executeCancelTx`
  updated the transitioning node but never walked reverse `blocks`
  dependencies; the dependent stayed in `StatusBlocked` until the
  dep was manually removed. Now: after a resolving transition,
  walk `dependencies WHERE from_id = resolvedID`, call
  `autoUnblockNode` on each `to_id` inside the same tx, and emit
  a `transition_status` sync event so teammates see the unblock.
  Multi-blocker case handled correctly (dependent stays blocked
  until ALL blockers resolve). FR-3.8a invalidated-takes-precedence
  rule preserved. Fixes a sync-invariant violation that also
  affected the pre-existing dep-remove unblock path.

### Changed

- `transport.DefaultPoolDefaults.MaxConns` lowered from 8 → 5 per
  FR-18 / MTIX-15.10. 10 active developers × 5 conns = 50, well
  within managed-PG defaults.
- `examples/hooks/pre-push` now calls `mtix sync push` (with
  `MTIX_SYNC_HOOK=1` for warn-and-skip on transient errors)
  instead of the pre-15 `mtix snapshot`. Falls back silently if
  no DSN is configured (sync is opt-in).
- `go.mod toolchain go1.26.3` — pinned for `govulncheck`-clean
  builds. Bump when stdlib CVEs land; see
  `docs/audit/MTIX-15-audit-pass2.md`.

### Security

- `govulncheck ./...` — clean against this commit. Required two
  upstream bumps: `toolchain go1.26.3` (stdlib CVEs in 1.26.1)
  and `golang.org/x/net v0.51.0 → v0.54.0`.
- DSN regression sweep covers all 11 sync subcommands plus the
  MCP tool (`cmd/mtix/sync_dsn_hygiene_test.go`).
- Panic redaction wired at `main()` (`cmd/mtix/main.go` —
  `defer redact.Recover(nil)`).
- TLS `verify-full` is the default; weaker `sslmode` allowed only
  on loopback hosts with `--insecure-tls`.
- `Source()` refuses to load a DSN from any tracked config under
  `.mtix/` — fail-closed at the earliest detectable misconfiguration.
- 22-item security audit (12 original design-audit items + 3
  penetration-style checks + 7 new HIGH requirements: 3 fuzz
  targets, VC overflow at transport, 3 panic-redaction tests).
  Evidence table in `docs/audit/MTIX-15-audit-pass2.md`.

### Migration: upgrading from v0.1.x

The local SQLite is already canonical in v0.1.x. Upgrading does
not move your tickets. The v1 → v2 schema migration runs
automatically on first command after upgrade; it adds the
`sync_events` / `sync_conflicts` / `sync_projects` /
`applied_events` tables and meta sentinels, leaving your `nodes`,
`dependencies`, `audit_log`, and `meta` rows untouched.

**To enable sync replication for an existing project:**

```bash
# 1. Upgrade the binary
go install github.com/hyper-swe/mtix/cmd/mtix@v0.2.0-beta
# or brew upgrade mtix once the formula lands

# 2. Provision a Postgres hub (Supabase / Neon / RDS / self-hosted)
#    Create a least-privilege role — see docs/SECURITY-MODEL.md
export MTIX_SYNC_DSN="postgresql://mtix_sync@hub.example.com:5432/mtix_hub?sslmode=verify-full"

# 3. Initialize the hub (single teammate, once)
mtix sync init

# 4. Backfill — synthesize sync_events from your existing nodes
mtix sync backfill --dry-run    # preview: counts what will be emitted
mtix sync backfill              # commit the synthesis

# 5. Push the backfilled events to the hub
mtix sync push

# 6. Other teammates clone the populated hub
mtix sync clone
```

**Important migration notes:**

- Backfilled events use `authorID="cli"` (the default). If two
  CLIs share that authorID and concurrently edit the same field,
  their vector clocks are `Equal()` rather than `Concurrent()` —
  the hub does NOT log a `sync_conflicts` row even though LWW
  still resolves the contention deterministically. For full
  hub-side audit-trail visibility, set distinct authorIDs per
  agent. See `docs/SECURITY-MODEL.md` →
  "Known audit-trail limitation: same-authorID conflicts".
- `mtix sync backfill` is refusal-by-default if `sync_events` is
  already non-empty. To re-backfill, run
  `mtix sync reconcile --discard-local` first.
- Un-pushed events are NOT durable across machine loss. For
  compliance-grade durability, run `mtix sync daemon` as a
  systemd / launchd service (the daemon prints the unit file via
  `--install`).
- Solo users without a hub do not need to do anything; the
  upgrade is a no-op for solo workflows.

### Known limitations

- Same-authorID concurrent edits do not produce hub-side
  `sync_conflicts` rows (LWW still converges; documented above
  and in SECURITY-MODEL.md).
- Un-pushed events on a lost machine are unrecoverable; daemon
  mitigates the window.
- Backfill emits synthetic events representing the final state of
  each node, not every intermediate state from the v0.1.x
  lifetime. Operators who need full historical event replay must
  consult the pre-upgrade `audit_log` as the authoritative
  record.

### Quality bar

- `go test -race -count=1 ./...` — 23 packages green.
- `golangci-lint run ./...` — 0 issues.
- `govulncheck ./...` — clean.
- 22-item security audit PASS;
  [evidence table](docs/audit/MTIX-15-audit-pass2.md) cites every
  test by file:line.

---

## [v0.1.5-beta] — 2026-04-26

### Changed
- Dependency bumps (goreleaser-action and related CI maintenance).
  Full diff: `git log v0.1.4-beta..v0.1.5-beta`.

## [v0.1.4-beta] — 2026-04-25

### Fixed
- MTIX-12 — canonicalize `node_type` from `depth` on export to
  match import-side semantics.

## [v0.1.3-beta] — 2026-04-13

### Added
- `mtix_briefing` MCP tool + `--format briefing` for paste-into-context
  output across agent-facing docs (FR-17).

## [v0.1.2-beta] — 2026-04-09

### Changed
- Internal task housekeeping (MTIX-8 closure).

## [v0.1.1-beta] — 2026-04-08

### Fixed
- npm dev dependency advisories (vite high-severity).

## [v0.1.0-beta] — 2026-04-01

### Added
- Initial public beta release. Solo / single-machine workflow
  with local SQLite (WAL mode) canonical store; git-tracked
  `.mtix/tasks.json` snapshot; agent-native CLI / REST / gRPC /
  MCP surfaces. Full feature inventory in README.md.

---

[Unreleased]: https://github.com/hyper-swe/mtix/compare/v0.2.0-beta...HEAD
[v0.2.0-beta]: https://github.com/hyper-swe/mtix/compare/v0.1.5-beta...v0.2.0-beta
[v0.1.5-beta]: https://github.com/hyper-swe/mtix/compare/v0.1.4-beta...v0.1.5-beta
[v0.1.4-beta]: https://github.com/hyper-swe/mtix/compare/v0.1.3-beta...v0.1.4-beta
[v0.1.3-beta]: https://github.com/hyper-swe/mtix/compare/v0.1.2-beta...v0.1.3-beta
[v0.1.2-beta]: https://github.com/hyper-swe/mtix/compare/v0.1.1-beta...v0.1.2-beta
[v0.1.1-beta]: https://github.com/hyper-swe/mtix/compare/v0.1.0-beta...v0.1.1-beta
[v0.1.0-beta]: https://github.com/hyper-swe/mtix/releases/tag/v0.1.0-beta
