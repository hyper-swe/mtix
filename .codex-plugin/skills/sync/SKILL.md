---
name: sync
description: Set up, use, verify and recover the mtix sync hub, the shared Postgres database that carries tasks between machines. Configuring a hub from zero, least-privilege database roles, hub credentials, the push-then-pull routine, mtix sync doctor, and recovery from sync failures.
---

# MTIX — Sync Hub

The hub is a Postgres database that you and your teammates' agents push task changes to and pull each other's changes from. Each machine keeps its own local store; the hub carries the event log between them. This skill is the full runbook: requirements, setup from zero, daily use, verification, troubleshooting and recovery. The short routine is in the managed SYNC section of `CLAUDE.md` and `AGENTS.md`.

## CLI read freshness

CLI board reads retry a busy local sync lock briefly, with a total wait budget of
500 ms. If ownership becomes available during the retry, auto-import checks the
board normally. Persistent contention lets the command read the local cache but
marks JSON responses with `"stale": true` and
`"stale_reason": "sync_lock_busy"`. Object responses retain their existing fields;
array, scalar and null responses are wrapped as
`{"data": <original response>, "stale": true, "stale_reason": "sync_lock_busy"}`.
Fresh reads keep their original shape and omit these fields. One possibly stale
notice also goes to stderr, including protocol and daemon startup when `--json`
is supplied. CLI JSON stdout remains one valid response.

These fields describe read responses only. Successful mutation acknowledgements
omit `stale` and `stale_reason`; their absence on an acknowledgement does not
verify cached read freshness. When a write succeeds despite busy auto-import
ownership, one completion notice says the write succeeded and must not be
repeated. Follow that notice: never repeat a successful write. When the write
itself fails, no completion notice claims it succeeded. A later export or cleanup
error does not undo a successful write.

Check freshness before using task state or assembled context to take an action.
When stale is true, retry the same read after the current owner finishes. Never
delete, replace or unlink `.mtix/data/sync.lock` to bypass its owner, and do not
use `mtix sync --fix` to force a fresh read: it rewrites the pulled board from the
local cache. These fields describe sync-lock contention only; still follow any
other auto-import refusal or warning. Commands that skip automatic import,
including `mtix sync status` and `mtix sync doctor`, do not make this check.

## Automatic export activity

An unchanged board keeps `.mtix/tasks.json` bytes, mtime and `exported_at`,
including across idle daemon ticks. Treat these as publication metadata, not a
daemon heartbeat. A real content change publishes the new snapshot once;
pending export recovery still completes when the file already matches.
`sync_export_completed` is Debug-only: use `--log-level debug` to inspect actual
publications. Quiet idle ticks produce no completed-publication record.

## Board identity

New boards created by `mtix init` have one UUID followed by a newline in
`.mtix/board-id`. Commit this file with the board and preserve it when cloning;
it is board data outside `config.yaml`. The tested v0.5.4-beta configuration
writer preserves its bytes. Existing boards are not assigned an identity by
pull, doctor or another background command. Do not manufacture or merge IDs
by editing the file; setup and explicit rekey recovery are planned separately.

A successful `mtix sync pull` records the hub's project prefix and first-event
hash through its existing authenticated connection. Successful relay attach
records the verified relay ID. These observations live in operator-local state
for the checkout, outside committed configuration. A successfully observed
missing hub row or empty hash clears the current observation to unknown; a
failed lookup preserves the previous observation. Recording is best-effort: if it
fails after the pull or attach succeeded, the command prints a WARN and still
succeeds.

Grant matching defaults to board ID alone. The operator-local
`inbox.strict_board_identity` setting defaults to false; when true, matching
also requires the same known hub identity. An unknown identity never matches
in strict mode. This release reads the setting and existing confirmations;
setup, reconfirmation, rekey and the host-setting command are planned and do
not create or update grants here.

`mtix sync doctor` checks the local identity without an additional identity
connection. For a hub board, multiple identities fail the `board.id` check;
a missing `.mtix/board-id` or a hub identity no pull has recorded yet is a
warning (run `mtix sync pull` to record it); a hub identity a pull observed as
missing or empty fails with `mtix sync init` as its fix.
A changed recorded hub identity warns through `board.hub-identity-changed`
and names the planned reconfirmation and strict-setting options. The warning
does not perform either action. Resolve a reported identity disagreement with
the board owner; preserve existing confirmations until explicit recovery is
available. Text and JSON reports carry the same status and guidance.

A board also carries a sticky hub-connected mark that records the first
evidence of a hub or relay: sync or relay keys in `.mtix/config.yaml`, a DSN in
`.mtix/secrets`, a hub or relay event applied to the board, or a DSN taken from
the environment by a sync command that actually connected. A DSN that is only
exported in the shell never marks a board. The mark stays set when the keys or
credentials are later removed, because the board has already exchanged events.
The mark is stored the first time `mtix doctor` or `mtix sync doctor` finds evidence (sync or
relay keys, a secrets DSN or an applied-event record), or the first time
`mtix sync init`, `clone`, `pull` or `push` connects to the hub. A hub event
applied to the board also stores it. `mtix sync relay attach`, `mtix sync status`
and a failed connection do not store it; until one of the others happens,
nothing is stored.
Older mtix builds do not set it, so any applied-event record is also treated as
evidence and sets the mark. `mtix sync doctor` shows it as `board.hub-mark` with
its source in text and `--json`. A command to clear it, at an interactive
terminal only, is planned and does not exist in this release.

## Daemon ownership

`mtix daemon` and `mtix sync daemon` share a lifetime OS lock at `.mtix/data/sync.daemon.lock` (flock on Unix, LockFileEx on Windows). A concurrent start exits cleanly while the owner keeps running. `.mtix/sync.daemon.pid` is diagnostic: stale or missing PID content cannot grant ownership. The PID is published only after acquiring the lock and removed only by its matching owner while the lock is held. Normal exit and process crash release the OS lock; the lock file stays in place so existing descriptors keep using the same inode. Other filesystem or OS errors are reported. Do not delete daemon lock files to force a second instance.

## Named locks and dispatch contention

Push and dispatch coordination use `.mtix/data/sync.push.lock` and `.mtix/data/dispatch.lock`. Both require a real data directory and regular lock file owned by the effective OS user; symlinked directories or lock files, Windows reparse points and wrong object types are refused. Do not remove, replace or unlink a lock file to bypass its owner. If acquisition is refused, stop project writers and ask the operator to restore a real, correctly owned local data directory without discarding its data.

Each daemon pass takes `dispatch.lock` before pending-mirror recovery, hub pull, relay and hook dispatch, then releases it before waiting. A contended pass remains alive and retries at its existing cadence; it performs none of that work while another dispatcher holds the pass lock. This differs from a duplicate daemon start, which remains a clean no-op under the separate lifetime `sync.daemon.lock`. The legacy `mtix sync daemon --dispatch-hooks` spelling uses the same pass lock; the daemon without that flag still locks its pull/relay pass.

`mtix sync doctor` reports the local `data filesystem` check in text and JSON. Known network or FUSE mounts produce a passing WARN with local-disk recovery guidance; a failed or unavailable classifier also warns without claiming reliable locking. Only recognized local disk filesystem names (apfs on macOS; ext, xfs, btrfs and similar on Linux) pass; any other name also warns, because an unrecognized filesystem is not a locking guarantee. On Windows the filesystem cannot be classified, so this row always shows a WARN (classification unavailable); it cannot be cleared, it does not fail the doctor, and it does not mean the store is unsafe. The check performs no network contact of its own; the doctor's existing hub checks still contact the hub and can fail independently. Existing unsafe-filesystem write refusal remains in force. Give each machine its own local `.mtix` and use the hub to share changes.

## Push lock contention

A normal `mtix sync push` that drains its eligible queue releases the push lock, checks for committed arrivals, and reacquires before reading and pushing again. It performs at most three passes. An event arriving after the final pass stays durably pending and is not marked pushed; `mtix sync status` shows its pending count. Run the next explicit `mtix sync push` to deliver it. Held, unresolved deferred or no-progress work does not trigger fruitless repeat passes. `--force` keeps its single lock-free pass, and hook mode keeps its transient-error warning behavior. A contender exits successfully with its event still durable; holder delivery is not promised at the cap. Other non-pusher owners include automatic import, `mtix sync backfill`, and merge-import UID adoption; each can hold the push lock without pushing, so work skipped under any of them remains pending for the next push. The daemon remains a pull/dispatch loop and does not autonomously push these events unless a configured hook invokes push.

## Pending mirror exports

Mirror exports read nodes, dependencies, agents and sessions from one read-only WAL snapshot, allowing writers to commit while the export reads. The mirror file and its conflict baseline describe that same snapshot; publication writes the file before its hashes. A write committed after the snapshot is handled by a later export.

A writing command that encounters the mirror sync lock saves its database change and returns without waiting. New clients keep one coalesced pending-export record in the local database metadata, advancing a monotonic generation on every mark, including when already pending. The cutoff is committed before the board snapshot; only a fully published snapshot is retired, and later requests keep their own oldest age. The record survives process exit and restart. Its fixed size does not bound SQLite WAL size, and marking still requires one guarded FULL-WAL transaction: it adds no request file or directory fsync per commit. Long-running interfaces mark before debounce and still trigger if marking fails; the small commit-to-hook and CLI commit-to-post-run windows remain.

`mtix sync status` and `mtix sync doctor`, including `--json`, read pending mirror exports without draining or importing. They show count, oldest timestamp and age in seconds (`pending_exports.count`, `oldest_at`, `oldest_age_seconds`). With new clients the logical count is zero or one, not the number of mutations; unmigrated legacy `.mtix/data/export-pending/request-*` files temporarily add to that count. Future timestamps show age zero. Doctor warns when the oldest is more than one hour old and prints the exact safe recovery command `mtix list`. This invokes protected import-first recovery; follow any pulled-board refusal it prints, then check `mtix sync status` again. `mtix sync push` drains hub events, not this mirror work. Never delete pending files or edit database metadata, and never use `mtix sync --fix` as an unconditional retry: it discards the pulled board.

A service import lock holder retries after release; the next automatic export or either daemon tick also retries. Each trigger performs at most three successful publication passes, releasing the lock between passes; errors, contention and pulled-board refusals stop it and retain intent. Metadata errors are reported while protected publication is attempted, and unknown requests cannot be retired. Legacy requests are first migrated to durable metadata, then only captured, unchanged files are removed; crashes can temporarily leave duplicate counts and conservative oldest ages. Concurrent old clients may keep adding legacy files, so the storage bound applies to new clients after migration. A final identity check detects a legacy file replacement observed before unlink; an uncoordinated old writer can still replace it after that check. Automatic recovery never overwrites a changed, unreadable or refused pulled board. Publication files are synced; Unix directory Sync EINVAL/ENOTSUP is logged as best-effort and other directory errors remain failures. Windows does not sync directory entries, so unsupported directory Sync carries no power-loss guarantee.

## Optional committed-board reader

The upgrade service provides an optional read-only Git ancestor-board reader for an import in your own checkout. It reads the common ancestor of `ORIG_HEAD` and the configured upstream, then reads that commit's `.mtix/tasks.json`; it never changes Git refs, the index, the checkout or the local database, and never fetches or prompts. A trusted supported Git installation, self-contained ordinary repository metadata, available local history and one unambiguous base are required. Shallow repositories (`.git/shallow`) and borrowed-object repositories (`.git/objects/info/alternates`) are conservatively unavailable even when Git can read a base; this supported-layout boundary does not imply that every borrowed-object graph is incomplete. Missing history, an absent committed board, other unsupported metadata or platform policy, invalid board data and the fixed time/output limits also return an explained unavailable result. A Git-less or mgit-only repository has the same clean fallback; do not install a repository-supplied executable or change metadata to force a result.

This reader reports graph and board provenance; it does not establish lineage of an arbitrary incoming file. The upgrade import's last-exchanged-first ancestor selection and three-way loss check are separate consumer work. Existing import refusal and recovery behavior still applies: retain the refused board, follow the printed guidance, and do not overwrite it merely because Git is available.

## Received provenance

Successful automatic import and fully drained eligible hub pull record local received provenance in `.mtix/data/exchange-record.json` on supported Unix systems. Local changes, exports, pushes and partial/refused exchanges do not replace it. `mtix sync status` and the local `sync.exchange-record` doctor check report presence/source/time and omission warnings; the record is not proof of incoming lineage or that local changes reached the hub. Windows receipts remain unsupported pending native verification. A saved omission may be stale after diagnostics failed to clear. An abandoned `.exchange-record-*` temporary artifact requires an operator to stop all project writers and remove only that confirmed individual artifact; readers never promote or delete it. See USERMANUAL received-provenance recovery and EXPORT-FORMAT for the precise receipt contract.

A hub receipt is published only when the completed pull observes no pending outbound journal events, including held push events, in the same board snapshot. Pending work preserves the prior receipt (or its absence) and reports `pending-outbound`. Run `mtix sync push`, resolve any held work it reports, then run a new, complete `mtix sync pull`; push alone does not publish a receipt or clear that warning. Older hub receipts whose outbound boundary is absent or null remain readable, but status and doctor infer `outbound-boundary-unknown` without rewriting them. This is historical uncertainty, not a recorded failed exchange; a later saved omission takes precedence. A fresh receipt with known zero replaces that uncertainty only after a new eligible pull.

Receipt diagnostics use fixed categories; unknown stored or supplied values report `unknown`. A completed publication failure keeps its specific category. Unexpected, missing or repeated fetched IDs prevent a new receipt without changing valid event application. Pull requires exclusive local sync ownership; retry after a contending owner finishes.

A contended local sync lock makes ordinary `mtix sync pull` fail. With `MTIX_SYNC_HOOK=1`, native lock contention instead warns that the pull was not performed and returns success. Retry after the current owner finishes; no pull was completed or automatically retried. Filesystem acquisition failures and cancellation remain errors. Never delete the sync lock file to bypass its owner.

## Task history in tasks.json (experimental)

Each task carries a change log (field, old and new value for structured fields, actor, origin, time). By default `.mtix/tasks.json` does not include it, so every released mtix can still read the file. The switch is `history.export`:

1. **Configure.** `mtix config set history.export true` turns it on; `mtix config set history.export false` turns it off. Turn it on only when the user asks and every machine and daemon that reads or writes this board runs this version or later: mtix 0.5.4 and earlier fail the checksum of a file that carries history. No doctor check verifies that yet, so ask the user.
2. **Verify.** `mtix config get history.export` shows the setting. After the next change, `.mtix/tasks.json` has `schema_version` `2.1.0` and a `history` list on the tasks that have entries; with the switch off it stays `2.0.0` with no history keys.
3. **Use.** History of tasks that no longer exist travels in an optional top-level `history` list, covered by the checksum. `mtix import` (replace or merge) and the automatic import union the file's history into the store by entry id: a local entry is never dropped or changed, an entry the store already holds is counted as skipped, and the union is idempotent, so importing the same file twice adds nothing. The import result (`--json`) carries `history_added`, `history_skipped`, `history_conflicts` and `history_skipped_legacy_key`. A conflict is an entry id the store holds with other content: the local entry is kept, the import still succeeds and a warning names the count and the first few entry ids, never the content. Entries keyed by an old dot id rather than a task uid are skipped and counted, because the dot id could name a different task on another machine. The automatic import reports conflicts and skipped legacy-key entries once per imported file on stderr (with `--json`: one JSON line with `"warning":"history_import"` and the counts). An entry id the file repeats with different content is stored in neither copy and counts as a conflict. The older-writer warning applies only while `history.export` is on: when the file's last writer is older than the first mtix release that keeps task history (0.6.0 today; the threshold rises whenever the history format changes in a way older writers would lose) or recorded no version, the import prints the warning "history may be incomplete: board last written by mtix X; upgrade mtix on that machine" (for a file with no recorded version: "...written by mtix of an unrecorded version; upgrade mtix on that machine") on stderr, prefixed `warning:` for `mtix import` and `mtix: warning:` for the automatic import (with `--json`: a `warnings` list in the result, and a one-line JSON `{"warning":"history_incomplete",...}` on stderr for the automatic import). A writer at or above that release, a development build and a board with `history.export` off never warn. Upgrade that machine and let it write the board again; do not hand-edit entries. Experimental until the doctor guard (MTIX-95.31.19.6) ships.
4. **Troubleshoot.** An older mtix reporting `checksum verification failed` on a file with `schema_version` `2.1.0` is not corruption: that binary cannot read history. Upgrade it, or turn the switch off and let the next change rewrite the file as 2.0.0.
5. **Recover.** `mtix config set history.export false`, then run any writing command (for example `mtix sync push`) so the file is rewritten without history. The local change log is kept in the store either way.
6. **Bound it (optional).** `mtix config set history.max_entries_per_task <N>` keeps at most N change entries per task; unset (the default) keeps everything. Only a positive whole number is accepted, and `mtix config set history.max_entries_per_task ""` unsets it. When a task's change entries exceed N, the next change to it trims the oldest down to 90% of N (at least one is kept) and records one `retention_drop` marker (field `retention`, the count dropped in `new`, the cutoff time of the newest dropped entry in `old`); markers and redactions are never trimmed. Lowering the limit trims only at the next change, never in a sweep. A long-running process (daemon, MCP server) reads the limit when it starts, so restart it after changing the limit. An import or sync skips an incoming change entry at or before the cutoff this copy of the board recorded when it trimmed the task (a local record that is never exported, imported or synced), so trimmed entries do not come back (counted as `history_skipped`). The cutoff is local to each copy: trimming on one machine does not trim the others, and a copy that never trimmed keeps every entry. An incoming entry dated more than 24 hours ahead of this machine's clock is skipped and counted (a wrong clock must not erase local history), and a cutoff is never later than this machine's clock: a change entry dated ahead of the clock (inside the 24 hour allowance) is neither counted toward the limit nor trimmed until the clock passes it (`mtix doctor` reports how many in `ahead_of_clock`). A cutoff recorded while the clock ran ahead and now later than the clock counts as the clock on import (entries trimmed meanwhile may return from a peer); doctor warns and shows the fix, counting these in `cutoffs_ahead_of_clock`. A marker received from another machine is stored as history but is never a cutoff, and a marker with an unreadable cutoff or count is skipped with a warning. `mtix doctor --json` shows the `history.size` row with rows, tasks, `largest_tasks`, `max_entries_per_task`, `oversize_tasks` and `ahead_of_clock` in its `data` object (the row appears once the board has history or a limit is set; it warns, naming the key and the exact `mtix config set` fix, when a hand-edited value is not a positive whole number, because such a value is read as unset and nothing is trimmed). `mtix show <id> --history` lists a task's history newest first (`--history-limit`, default 50); `mtix show <id> --json` carries the same list as `history`, an empty list when the task has none (each entry has `id`, `task_uid`, `ts`, `actor`, `origin`, `kind`, `field`, `old`, `new`). Free-text fields show no text.

Never edit history entries in `.mtix/tasks.json` by hand (the checksum covers them), and never turn the switch on to work around a sync problem.

## Requirements checklist

Before setup, confirm each item with the user or the database administrator:

- **PostgreSQL 15 or later.** mtix's own tests run against 16 and 17. Roles and grants below are written for 15 and later.
- **TLS with `sslmode=verify-full`.** mtix refuses weaker modes for any host that is not loopback. If the server certificate does not chain to a publicly trusted CA, you need that CA bundle (see the connection notes below).
- **Reachability.** Either a public TLS endpoint or a private network path from every machine that syncs (a VPN or private network counts).
- **A direct or session-mode connection**, not a transaction-mode pooler.
- **Two database roles**: a table owner for setup and administration, and a least-privilege writer for daily sync (next section).
- **`pg_dump` on `PATH`** on the machine that runs `mtix sync backup`.

## Connection notes

mtix connects over TLS with `sslmode=verify-full` and needs a **session-mode**
connection for the migration path. Match your platform to the capabilities
below; each one maps to one mtix setting.

- **Connection pooling.** If your provider offers connection pooling, use its
  **direct** endpoint or its **session-mode** pooler (usually port 5432), never
  the transaction-mode pooler. Transaction mode breaks the session semantics
  the migration single-flight relies on.
- **Certificate authority.** If the server certificate does not chain to a
  publicly trusted CA (a private CA), `verify-full` needs that CA bundle: obtain
  it from your provider's console or your DBA, then set
  `MTIX_SYNC_SSLROOTCERT=/path/to/ca-bundle.crt` (or add `sslrootcert=` to the
  DSN). If you skip it, mtix's error names the setting. A publicly trusted
  certificate needs no extra setting.
- **Database that pauses when idle.** If the database scales to zero or
  suspends after inactivity, the first command waits for it to resume, and a
  direct endpoint may refuse the first connection while it is suspended. Wake
  it once (run any trivial query) before `mtix sync init`, or keep it active
  during setup. mtix does not poll the hub on a timer from the CLI, and the daemon
  contacts it only after activity by default (`sync.hub_contact`), so an idle
  database stays idle unless you set an interval (a waiting session's
  listener, `sync.listen`, keeps the hub contacted while it waits; `sync.listen off` stops that).
- **Tables exposed over an HTTP API.** If your platform can expose tables
  through an HTTP API, keep the mtix tables unexposed. `mtix sync doctor`
  confirms reachability and schema; it does not check exposure, so verify that
  in the database platform's own settings.
- **Any provider.** `statement_timeout` is applied per connection via SQL (not a
  startup parameter), so it is honored even behind proxies and poolers that
  drop startup parameters. `sslmode=require` is rejected for non-loopback
  hosts; use `verify-full`, with `MTIX_SYNC_SSLROOTCERT` if the server uses a
  private CA.

```bash
export MTIX_SYNC_SSLROOTCERT="/path/to/ca-bundle.crt"   # only for a private CA
export MTIX_SYNC_DSN="postgresql://<user>:<pw>@<host>:5432/<db>?sslmode=verify-full"
```

- **Special characters in the password.** The URL form needs a password with
  `/`, `?`, `#`, `%` or a space percent-encoded. To avoid encoding, give the
  DSN in the libpq key=value form and single-quote the password, escaping `'`
  and `\` with a backslash. Or leave the password out of the DSN and set
  `MTIX_SYNC_PASSWORD`; a password in the DSN takes precedence. The TLS rules
  are the same for both forms.

```bash
# Shell single quotes pass the DSN through unchanged. This form suits a password
# with no space, quote or backslash in it; for any other password use the second form:
export MTIX_SYNC_DSN='host=<host> port=5432 dbname=<db> user=<user> password=<pw> sslmode=verify-full'
# Or keep the password out of the DSN and read it without echo, so the shell
# never expands $, backticks or backslashes in it:
export MTIX_SYNC_DSN='postgresql://<user>@<host>:5432/<db>?sslmode=verify-full'
IFS= read -r -s MTIX_SYNC_PASSWORD && export MTIX_SYNC_PASSWORD
```
Do not put a generated password inside shell double quotes: the shell rewrites
`\`, `$` and backticks in it before mtix sees it.

## Least-privilege roles

Two roles keep the daily credential small. **The owner** creates and owns the sync tables, so it runs `mtix sync init`, `mtix sync repair-uids`, `mtix sync mark-restored`, `mtix sync harden` and `mtix sync migrate --yes`. **The writer** is the role every agent uses for `mtix sync push`, `pull`, `clone`, `status` and `doctor`; it cannot create, alter, drop or truncate anything.

The database administrator runs these statements, never an agent. Passwords are set at the administrator's own `psql` prompt (`\password mtix_owner`, then `\password mtix_writer`), so no password appears in a statement, a log or a conversation. Run as a superuser or the database's owner:

```sql
CREATE ROLE mtix_owner LOGIN;
CREATE ROLE mtix_writer LOGIN;
CREATE DATABASE mtix_hub OWNER mtix_owner;
\c mtix_hub
REVOKE CREATE ON SCHEMA public FROM PUBLIC;
GRANT USAGE, CREATE ON SCHEMA public TO mtix_owner;
GRANT CONNECT ON DATABASE mtix_hub TO mtix_writer;
```

After the owner has run `mtix sync init` once (setup step 3), the owner grants the writer its privileges. A role that syncs without owning the sync tables needs exactly this list:

- USAGE on the schema;
- SELECT on `sync_events`, `sync_hub_state`, `sync_node_collisions` and `sync_project_clients`;
- INSERT on `sync_events`, `sync_conflicts` and `sync_project_clients`;
- UPDATE on `sync_project_clients`;
- USAGE on the sequence `sync_conflicts_conflict_id_seq`;
- EXECUTE on the function `record_restore_collision`;
- UPDATE on `sync_node_collisions`, only for a role that runs `mtix sync collisions resolve`;
- SELECT on `node_renumber_remaps`, only for a role that runs `mtix sync migrate` on a hub without a valid node-number registry index;
- INSERT on `node_renumber_remaps`, only for a role that runs `mtix sync migrate --yes` on a hub without a valid node-number registry index.

As statements, run by the owner (replace `public` if the hub lives in another schema):

```sql
GRANT USAGE ON SCHEMA public TO mtix_writer;
GRANT SELECT ON TABLE sync_events, sync_hub_state, sync_node_collisions, sync_project_clients TO mtix_writer;
GRANT INSERT ON TABLE sync_events, sync_conflicts, sync_project_clients TO mtix_writer;
GRANT UPDATE ON TABLE sync_project_clients TO mtix_writer;
GRANT USAGE ON SEQUENCE sync_conflicts_conflict_id_seq TO mtix_writer;
GRANT EXECUTE ON FUNCTION record_restore_collision TO mtix_writer;
```

A pull also records this client on the hub. Right after `mtix sync pull` (and each daemon pull) connects, and before it reads any events, it writes one row to `sync_project_clients`: the board's sync project prefix (set by `mtix sync init`), this machine's hash and the CLI version. That one statement uses the connection the pull already opened, so it adds no connection and no timer and wakes a hub only when a pull was going to contact it anyway. It is what lets the version gate and any stale-client report see a client that only pulls. A role that only pulls therefore needs the same INSERT and UPDATE on `sync_project_clients` as a role that pushes. If the write fails, for instance for a role without those grants, the pull still completes with the same exit status, result and cursor, and prints one `WARN: pull client registration skipped` line that names the grant; the client then stays invisible to the version gate. To verify, after a pull the owner finds the client in `SELECT project_prefix, cli_version, last_seen_at FROM sync_project_clients ORDER BY last_seen_at DESC;`, or checks the role with `SELECT has_table_privilege('ROLE', 'sync_project_clients', 'INSERT') AND has_table_privilege('ROLE', 'sync_project_clients', 'UPDATE');`, which must return true (replace `ROLE` with the pulling role's name).

Three more grants are for a writer that also runs the command named in the comment; leave them out otherwise:

```sql
GRANT UPDATE ON TABLE sync_node_collisions TO mtix_writer; -- only for a role that runs `mtix sync collisions resolve`
GRANT SELECT ON TABLE node_renumber_remaps TO mtix_writer; -- only for a role that runs `mtix sync migrate` on a hub without a valid node-number registry index
GRANT INSERT ON TABLE node_renumber_remaps TO mtix_writer; -- only for a role that runs `mtix sync migrate --yes` on a hub without a valid node-number registry index
```

The writer holds no UPDATE on `sync_hub_state`, so it cannot run `mtix sync mark-restored`, and no INSERT on `sync_node_collisions`: the hub function `record_restore_collision` records restore collisions as the table owner. A writer that runs `mtix sync backup` also needs SELECT on every sync table and sequence; run the backup as the owner instead.

`mtix sync doctor` checks the writer's privileges and prints the exact GRANT or REVOKE for each gap. To restrict who else can use the sync tables, the owner runs `mtix sync harden`, a dry run that lists every role it would change; the admin skill has the details. Never pass `--apply` until the user has approved the dry run's role list.

## Credentials

The hub connection string (DSN) comes from one of two places, in this order:

1. the `MTIX_SYNC_DSN` environment variable of the process that runs mtix;
2. the `.mtix/secrets` file, mode 0600, holding the DSN and nothing else. `mtix sync init` adds it to `.gitignore`; `mtix sync doctor` fails its `secrets file mode` check if the mode is looser.

mtix refuses a DSN found in any tracked config file, and it accepts no DSN on the command line. A DSN looks like `postgresql://<user>:<password>@<host>:5432/<database>?sslmode=verify-full`.

**You never read, print, paste, echo, log or commit the DSN or a password.** Ask the user to set the environment variable in the shell or service that launches the agent, or to create `.mtix/secrets` themselves. Do not run `cat .mtix/secrets` or `env`, and never put a credential in a ticket, a comment, a commit or chat. If a credential has leaked, tell the user to rotate it at the database.

Use the writer's DSN for daily sync. The owner's DSN is for the commands in the owner list above, which the user runs or which you run only while the user has pointed `MTIX_SYNC_DSN` at the owner for that one command.

## Set up a hub from zero

1. Confirm the requirements checklist, then ask the database administrator to create the database and both roles with the statements above.
2. Ask the user to put the **owner's** DSN in `MTIX_SYNC_DSN` for this step.
3. Run `mtix sync init`. It runs the schema migration under a lock and creates the sync tables, functions and triggers. If it reports divergent history because this project already holds tasks the hub does not know, stop and ask the user: the way out is `mtix sync reconcile`. Its `--discard-local` path deletes local tasks and unpushed changes, needs `mtix sync push` and `pending` 0 first, and needs the ticket count typed at an interactive terminal, so the user runs it.
4. Ask the administrator to run the writer's GRANT statements from the previous section, then ask the user to switch `MTIX_SYNC_DSN` (or `.mtix/secrets`) to the **writer's** DSN.
5. Run `mtix sync doctor` and fix every FAIL it reports (see "Verify with doctor").
6. Run `mtix sync push`, then `mtix sync pull`. A second machine runs `mtix init` for the same project prefix, receives the writer's DSN the same way, and runs `mtix sync clone` to rebuild the local store from the hub.
7. Run `mtix docs generate` in a process that can see the DSN source (`MTIX_SYNC_DSN` set, or `.mtix/secrets` present); without one, the SYNC section stays the one-line pointer. This fully refreshes the root `MTIX.md` briefing, replacing edits to that mtix-owned file. Native root `CLAUDE.md` and `AGENTS.md` get only one pointer; valid old mtix marker blocks are migrated without changing other user bytes, and malformed pairs are refused. `HYPERSWE.md` suppresses new pointers and native-file creation; existing pointers remain. Git exclusion is reconciled from all existing root pointer files on every run: all ignored adds one marked mtix block in `info/exclude`; tracked/default or no pointers removes only that owned block, preserving user rules. A user rule still ignoring `MTIX.md` with a tracked pointer produces a notice. Reference files in `.mtix/docs/` retain their existing regeneration policy: `--force` refreshes marked sections of template-based files and appends missing blocks, or rewrites a marker-free template file whole. Ask the user first if they edited a reference file that will be rewritten.

For a development hub on this machine (loopback or a local socket only), `--insecure-tls` on the sync commands allows a weaker `sslmode`; never use it for a remote hub.

## Daily routine

- At session start run `mtix sync push`, then `mtix sync pull`. Push first, so a pull never runs while local changes are unpushed.
- After you change tasks, run `mtix sync push`.
- `mtix sync status` shows the local queue (`pending`, quarantined and held events, open conflicts), the quarantined events refused for an invalid node id and the local tasks with such an id, and does not contact the hub.
- In a git hook or script, set `MTIX_SYNC_HOOK=1`: a transient hub error then prints a warning and the command continues. A busy local sync lock also warns that pull was not performed; retry after the current owner finishes.
- After a pull, read the output for `conflicts`, `quarantined` and `renumbered`. A task number can change once when two agents created the same number; re-resolve by title, as the operating guide says.

## Cost and databases that pause when idle

Every `mtix sync init`, `push`, `pull`, `clone`, `doctor`, `backup`, `harden`, `migrate`, `mark-restored` and `collisions` command contacts the hub, even when nothing is pending. A database that scales to zero or suspends when idle therefore wakes on each of them (a push or pull at session start is one wake-up), and the first contact after idle waits for it to resume (mtix allows each hub connection 30 seconds). `mtix sync status`, `mtix sync quarantine list` and `mtix sync repair --status` read only the local store. Task commands such as `mtix create` and `mtix done` write locally and do not contact the hub.

mtix does not poll the hub on a timer from the CLI. After activity, sync (session start and after changes), never in a loop. When `mtix daemon` contacts the hub is set by `sync.hub_contact` (read fresh every pass, so a change applies without a restart): `activity` (the default for new boards) contacts the hub every 5 seconds for 10 minutes after real activity, every 60 seconds until 30 minutes, then not at all, so a database that pauses when idle can pause; `interval:<duration>` (at least 1s) contacts it on that period all day and keeps such a database awake around the clock; `manual` never contacts it from the daemon. `mtix config get sync.hub_contact` shows the effective mode. Real activity is recorded operator-locally (outside the board, never synced) by: a local mutation command, an addressed `mtix comment --to`, a commit made through the MCP server, `mtix agent heartbeat` (run by a model turn or a person), the attach glue's session-start and prompt-submit. It is not recorded by reads, `mtix inbox`, any wait, or any waiter arming, re-arming or refreshing a lease (a second waiter process that re-arms after the first exits never moves the activity window or the clamp). The first message to a host that has been idle past the window waits for that host's next activity (or for the listen hold below). A board created before this key existed keeps contacting the hub at the daemon's own `--interval` until you set the key, and `mtix doctor` row `sync.hub-contact` says so and prints `mtix config set sync.hub_contact activity`. With `interval:<duration>` the daemon prints a one-time cost warning at start. Local work (pending-export recovery, relay, hook dispatch) runs on every pass and never depends on the hub.

Listen hold: `sync.listen` is `live` or `off` (shipped default: `live`). A waiter (`mtix inbox --wait --agent X`) on a board that syncs through a hub or relay arms a lease and, when no daemon runs for the board, starts one detached `mtix daemon --listen` for it (the same binary, no extra argument, nothing secret on its command line) and prints one line saying so; if it starts nothing it prints why, for example `sync.listen` off or `sync.hub_contact` manual. Two waiters share one listener. While the hold is armed (real activity within `sync.listen_max_idle`) and `sync.listen` is `live`, a live waiter lease on this host makes the listener contact the hub every `sync.listen_interval` (default `10s`) even after the activity window closed, so an idle session can be woken by a message from another machine; `off` is strict activity-only contact. There is exactly one clamp: the hold ends `sync.listen_max_idle` (default `8h`, at most `72h`) after the last real activity, or sooner when the last lease's process is gone or `sync.listen` is set to `off`; re-arming or refreshing a lease never extends it. A live listening session therefore keeps a scale-to-zero database awake, at about 360 contacts an hour per board at the default interval, until that clamp; if the user does not want that, run `mtix config set sync.listen off`. `manual` overrides the hold. The listener exits within one local pass (about 5 seconds) after the last waiter lease ends, a lease whose waiter stopped refreshing it expires after 15 minutes so a crashed session cannot keep the hub awake, and after it exits nothing contacts the hub. A relay board has no database to wake, so its listener ingests the relay folder while a waiter lives. A waiter restarts a listener that exited early, but not while the board is idle-clamped (no respawn, no restart line, a quiet park). A listener started past the clamp, or on a board with no recorded activity, makes no hub contact and exits at once: a waiter that re-parks every few minutes costs the hub nothing until real activity (an addressed message or a local write) re-arms the hold. `mtix doctor` row `wake.listener.no-hub-listener` fails when waiters are parked with no listener on an armed hold and prints the fix `mtix daemon --listen`; past the clamp it passes as idle-clamped; it reads local state only. `sync.push_on_addressed_send` is reserved: it has no effect in this build. Verify with `mtix doctor`: row `sync.hub-contact` shows the mode, the last activity, the last contact and whether any contact will happen; row `sync.listen` shows whether the hold is on, its route (`poll`), the interval, the idle clamp and when the current hold ends; each failing row prints the exact `mtix config set` command. `mtix doctor` reads only local state and never contacts the hub for these rows. Never start `mtix daemon` or a cron job or a short-interval hook that pulls without telling the user what it costs. `mtix sync daemon` is the deprecated alias and keeps a fixed 5 second cadence.

## Verify with doctor

Run `mtix sync doctor`, or `mtix sync doctor --json` for machine-readable output. Exit code 0 means no check failed; 2 means at least one did. Each check names its fix, and the fix names who runs it: the table owner, a role administrator, or you.

| Check | What it verifies |
|-------|------------------|
| `PG reachable` | the connection opens and answers |
| `schema current` | the hub has the current migrations, and the connecting role can run `record_restore_collision` and cannot write collision rows |
| `queue draining` | no event waits in `pending` for over an hour |
| `quarantined events` | no pulled event is held in the local quarantine |
| `held push events` | push holds no event the hub would refuse |
| `valid node ids` | every local task id is inside the node id grammar (WARN, naming each task and its uid, when one is not) |
| `unique node uids` | no two tasks share a uid |
| `board matches sync log` | on a project that syncs, the board holds every task the local sync log creates |
| `secrets file mode` | `.mtix/secrets` is mode 0600 (passes when the file is absent) |
| `hub-triggers` | every hub function and trigger exists, is bound correctly and is enabled |
| `hub-privileges` | which roles besides the owner can use the sync tables |
| `no orphan applied` | every applied event has a matching task or tombstone |
| `relay configured` | the experimental file relay, only when `sync.relay.dir` is set (it also adds `relay reachable` and `relay record`) |

A WARN does not fail the run and blocks nothing. `hub-privileges`, `hub-triggers` and some `schema current` findings are WARN by default and FAIL only in strict mode (the `sync.keep_roles` config key is set). The goal of setup is a doctor run with no FAIL. Run doctor again after each fix.

## Troubleshooting map

| Symptom | Cause | Fix |
|---------|-------|-----|
| `sync lock` is busy and pull was not performed | another local sync owner is active | Retry after the current owner finishes. Hook mode warns and skips this pull; ordinary mode fails. Never remove the lock file to bypass ownership. |
| `PG reachable` fails, or a TLS error names `MTIX_SYNC_SSLROOTCERT` | the database is unreachable or paused; the certificate chains to a private CA; a transaction-mode pooler is in use | Retry once while a paused database resumes. For a private CA ask the user to set `MTIX_SYNC_SSLROOTCERT` to the CA bundle. Use a direct or session-mode endpoint. |
| `secrets file mode` fails | `.mtix/secrets` is looser than 0600 | Ask the user to run `chmod 600 .mtix/secrets`. |
| `schema current` names a missing migration | the owner has not run `mtix sync init` since an mtix upgrade | Ask the table owner to run `mtix sync init`. |
| `schema current` names a role that cannot execute `record_restore_collision`, or can write collision rows | the writer's grants differ from the list above | Ask the table owner to run the GRANT or REVOKE the check prints. |
| `schema current` names the node-number registry index | the index is not valid or not ready | Ask the table owner to run `mtix sync migrate --yes`, then `mtix sync init` again if init asked for it. |
| `pending` never goes down; `queue draining` fails | an earlier push delivered events whose confirmation was lost | Run `mtix sync push`: it acknowledges events already on the hub (the `already on the hub` count) and pushes the rest. |
| `quarantined events` fails | `mtix sync pull` held events it could not validate or apply | Run `mtix sync pull` again. If events remain, run `mtix sync quarantine list` and give the user the event ids and reasons. Never delete quarantine rows or raise `sync.max_lamport_jump`. Text output shows each event id double-quoted with control and non-ASCII characters escaped (for example `"0193fb00-0000-7000-8000-000000000001"`). That form is display only: never paste it into a command, and take the exact id from the `--json` output. Refusal reasons written by the event validator (tracked in MTIX-187.4.4), error text from the local store layer, and rows recorded by older versions can still show an id as received, so always take the exact id from the `--json` output. |
| `held push events` fails, or a `WARN: ... over the 65536-byte sync limit` line | push holds an event the hub would refuse | Follow the fix doctor prints for each event: shorten or split the field and save it again; for a held task creation, stop editing that task and tell the user. |
| `quarantined events` names an `invalid node id` | a pulled event names a task id outside the node id grammar, such as `AA-1é.4`; it is never applied | Inspect with `mtix sync quarantine list --json` and report the event ids, nodes and reasons to whoever runs the hub. No retry applies these events. After review, record a local receipt with `mtix sync quarantine acknowledge --handle <handle> --reason <text>` (the handle is in the HANDLE column of `mtix sync quarantine list` and in doctor's Fix; it works for every id, including ones with NUL or invalid UTF-8) (invalid-node-id pull/sweep only; reason up to 500 UTF-8 bytes; no terminal required). It stays retained and unapplied; doctor passes this check only when no unacknowledged pulled event remains. Doctor's Fix names the event by its handle: the first 12 hex characters of the SHA-256 of the exact event id bytes, which `mtix sync quarantine list` also prints in a HANDLE column (`handle` and `event_id_sha256` in `--json`). The handle is always printable and typeable, even when the id holds NUL, invalid UTF-8 or shell metacharacters, so the printed command can be copied as it is: `mtix sync quarantine acknowledge --handle <handle> --reason '<your explanation>'`. Replace `<your explanation>` with why this event is safe to accept as unapplied; without `--reason` the command is refused. A handle may be shortened to 8 hex characters; one that matches more than one event is refused with the length that separates them. Use the positional `<event-id>` form only for an ordinary id. Then run doctor again after each acknowledgement. Use `mtix sync quarantine list --json` to report the original id to the operator; JSON keeps NUL and escapable bytes in the id exactly but shows invalid UTF-8 bytes as U+FFFD; `event_id_sha256` and the handle always name the exact stored bytes. A printed handle may be longer than 12 characters when two retained events share a shorter prefix. See the admin invalid-node-id routine. |
| an id in output is quoted with backslash escapes, such as `"a\nb"` | ids that can come from sync (sync event ids, including the winner and loser of `mtix sync conflicts list`, task uids, annotation ids) are shown in plain text output as double-quoted printable ASCII, so control or non-ASCII characters in a stored id cannot affect a terminal or log; the stored id is unchanged; `mtix sync collisions list` shows both held and incoming task uids in full with the same quoting and escaping, without truncation; its `--json` output preserves the exact uids. REST, MCP and gRPC do the same in text and error messages; their JSON and protobuf fields carry the exact id. Validator refusals (and the quarantine reasons recorded from them from now on) and hub push and collision errors name the event id the same way; reasons from the apply path follow with MTIX-187.4.3, and a reason recorded by an earlier version can still hold the raw id. An id or text a caller sends that a server repeats in an error or result message is escaped in the message (control, invisible and invalid bytes appear as `\n`, `\r`, `\t`, `\xNN`, `\uNNNN` or `\UNNNNNNNN`; printable text stays readable; a literal backslash is not escaped, so read the escapes as a hint); the JSON fields still carry what was sent. The exact id is in the quarantine event_id column and in --json output, not in a log field or error text | Read the escapes as the id and report the quoted form; use the JSON output for the exact value. Never edit or rename a stored id. |
| `valid node ids` WARN | an earlier sync brought in a task whose id is outside the node id grammar | The task stays readable and editable here (`mtix show <uid>`), but other replicas quarantine its changes. Recreate each task you still need with `mtix create` and stop using the old one. |
| `unique node uids` fails | two tasks share a uid | Do not edit the database. Follow the recovery the check names, and ask the user. |
| `board matches sync log` fails | a replace import of an earlier version deleted tasks whose events stay in the local sync log; a pull acknowledges those events instead of applying them, so it cannot restore the tasks | Tell the user. The steps the check prints rebuild the board from the hub, run one at a time: `mtix sync push`, check that `mtix sync status` shows `pending` 0, then `mtix sync reconcile --discard-local --yes`, then `mtix sync pull`. The discard deletes every local task and unpushed change, which is why `pending` 0 comes first; it asks for the ticket count typed at an interactive terminal, so the user runs it. |
| `mtix import --mode replace` fails with `this store syncs with a hub` | a replace would delete tasks and changes whose events stay in the local sync log, and a pull could not restore them; the replace needs the ticket count typed at an interactive terminal, so an agent never runs it | Run `mtix sync pull` to receive teammates' changes. To rebuild the board from the hub, ask the user to run the commands the refusal names. A replace into an empty store, such as a restore into a fresh data directory, still runs. On a project that never synced a replace is allowed and discards the pending events of the tasks it drops, so a later first push cannot create them; a project that published to or ingested from a file relay counts as synced, and the automatic import records a configured relay or hub as the hub-connected mark before it imports, so a project with a relay configured is refused too. While an exported `MTIX_SYNC_DSN` is set, or a DSN key sits in a tracked config file, or the secrets file has the wrong mode and is empty, `mtix import --mode replace` is refused, and so is an automatic replace that would delete local data or drop a task the sync log holds (one that deletes nothing still runs). None of these is recorded, so removing it lifts the refusal. A secrets file that holds a DSN is recorded as the hub-connected mark by its content, whatever its mode, so fixing the mode does not lift the refusal. If the hub evidence cannot be read (for example `.mtix/secrets` is not a regular file), the automatic import stops and the replace refuses, naming the cause: fix the file, then retry. |
| `hub-triggers` WARN or FAIL | a hub function or trigger is missing, rebound or disabled | Ask the table owner to run `mtix sync init` or the `ALTER TABLE ... ENABLE TRIGGER` statement the check prints. |
| `hub-privileges` WARN | roles besides the owner can use the sync tables | Acceptable on a private network. To restrict, the owner runs `mtix sync harden` (dry run) and, with the user's approval of its role list, `--apply`. |
| `mtix sync conflicts list` shows conflicts | two machines changed the same field | `mtix sync conflicts resolve <id>` records a decision and changes no task: apply the chosen value with `mtix update`, then push. |
| a task is blocked on a restore collision | the hub was restored and two settled tasks claim one number | `mtix sync collisions list` shows them; ask the user, who resolves with `mtix sync collisions resolve`. |
| a task is `in_progress` again after `mtix done`, with `closed_at` still set | own-event replay, below | `mtix sync repair --status`, below. |

**Own-event replay.** Before 0.5.4 a pull could re-apply events this machine had already pushed, in the order they arrived, which left a task in an older workflow state: claim, push, `mtix done`, pull, and the task was `in_progress` again. Upgrading stops new damage; it does not change tasks already reverted. Now pull no longer re-applies this client's own events: an event is this client's own when its row is in the local `sync_events` log, and pull acknowledges it instead of applying it. A task already reverted is healed by `mtix sync repair --status`.

## Recover

- **Reverted workflow state.** Run `mtix sync pull`, then `mtix sync repair --status` (a dry run that reads and writes only the local store). It lists each task whose state differs from this machine's event log, with the winning event, its time and which machine made it. Pull again just before `--apply`: a repair on a stale log can revert a teammate's newer change on every machine. Ask the user to review the list before you run `mtix sync repair --status --apply`; it writes a verified backup first. Never add `--force`; it also repairs tasks flagged "not a replay; review". Then run `mtix sync push`.
- **A local store that is lost or damaged.** Back up what remains, make a fresh project directory with `mtix init`, and run `mtix sync clone` there. Clone refuses while the hub holds an event that fails the pull checks, and names it; on the fresh store the recovery is `mtix sync pull`, which quarantines that event.
- **Local changes the hub never received.** Run `mtix sync push` first and check that `mtix sync status` shows `pending` 0. Never discard local state while anything is pending.
- **The hub itself.** `mtix sync backup --output <new file>` writes a portable SQL dump of every hub table with `pg_dump`. It never overwrites a file, so choose a new path each time. A restore needs the owner: the admin skill has the runbook (restore, `mtix sync init`, a passing `hub-triggers` check, then `mtix sync mark-restored` exactly once). A dump holds no privileges, so grant the writer the list above again afterwards and run `mtix sync doctor` with the writer's DSN.

## When to ask the user

Stop and ask the user, with the exact command, when: setup needs a database role, a grant, a revoke or a password; a DSN or CA bundle must be set; doctor names a fix that only the table owner or a role administrator can run; a repair, a harden `--apply` or a `mtix sync reconcile` is the next step; events stay unacknowledged or held after the fixes above; or the same failure comes back after you fixed it. Give the user the check name, the error line without any credential, and what you already tried.

## Never

- Never read, print, paste or commit a credential, and never put one in a tracked file, a ticket, a comment, a commit or chat.
- Never use the owner's or an administrator's credential for daily sync.
- Never run `mtix sync push --force` or `mtix sync repair --status --apply` unless the user asks for it.
- Never run `mtix sync reconcile --discard-local` yourself: it deletes local tasks and unpushed changes, needs `mtix sync push` and `pending` 0 first, and needs the ticket count typed at an interactive terminal, so it cannot run unattended. Ask the user to run it.
- Never run `mtix sync harden --apply` before the user approves the dry run, and never run `mtix sync harden` from a hook, a push or the daemon.
- Never start a timer or interval sync against a database that pauses when idle without telling the user the cost.
- Never edit `.mtix` data, `.mtix/tasks.json` by hand, or the hub's tables, and never delete quarantine rows.
- Never run `mtix sync mark-restored` more than once for one restore.
