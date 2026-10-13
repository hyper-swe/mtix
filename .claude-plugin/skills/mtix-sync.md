---
description: "Set up, use, verify and recover the MTIX sync hub, the shared Postgres database that carries tasks between machines. Use when configuring a hub from zero, creating least-privilege database roles, sourcing the hub credential, running the push-then-pull routine, reading mtix sync doctor output, or recovering from a sync failure (quarantined or held events, a pending queue that never drains, workflow state an older pull reverted, a hub restored from a backup)."
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
Fresh responses keep their original shape and omit these fields. One possibly
stale notice also goes to stderr, including protocol and daemon startup when
`--json` is supplied. CLI JSON stdout remains one valid response.

Check freshness before using task state or assembled context to take an action.
When stale is true, retry the same read after the current owner finishes. Never
delete, replace or unlink `.mtix/data/sync.lock` to bypass its owner, and do not
use `mtix sync --fix` to force a fresh read: it rewrites the pulled board from the
local cache. These fields describe sync-lock contention only; still follow any
other auto-import refusal or warning. Commands that skip automatic import,
including `mtix sync status` and `mtix sync doctor`, do not make this check.

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
The mark is stored the first time `mtix sync doctor` or a sync command sees
the config keys, the secrets DSN or an applied-event record; until one of them
runs, nothing is stored.
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
  during setup. mtix does not poll the hub on a timer by default, so an idle
  database stays idle.
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
7. Run `mtix docs generate --force` in a process that can see the DSN source (`MTIX_SYNC_DSN` set, or `.mtix/secrets` present); without one, the SYNC section stays the one-line pointer. It writes `.mtix/docs/CLAUDE.md` and `.mtix/docs/AGENTS.md`; the project-root `CLAUDE.md` and `AGENTS.md` that `mtix plugin install` writes only when absent are not refreshed by it and point at the `.mtix/docs/` copy, which is the one mtix keeps current: read the SYNC section there. In a file that already has generated marker blocks, `--force` refreshes those blocks and appends any block the file lacks (the SYNC section included) after your own text, and changes nothing else. A file with no markers is rewritten whole, so ask the user first if they edited it.

For a development hub on this machine (loopback or a local socket only), `--insecure-tls` on the sync commands allows a weaker `sslmode`; never use it for a remote hub.

## Daily routine

- At session start run `mtix sync push`, then `mtix sync pull`. Push first, so a pull never runs while local changes are unpushed.
- After you change tasks, run `mtix sync push`.
- `mtix sync status` shows the local queue (`pending`, quarantined and held events, open conflicts) and does not contact the hub.
- In a git hook or script, set `MTIX_SYNC_HOOK=1`: a transient hub error then prints a warning and the command continues. A busy local sync lock also warns that pull was not performed; retry after the current owner finishes.
- After a pull, read the output for `conflicts`, `quarantined` and `renumbered`. A task number can change once when two agents created the same number; re-resolve by title, as the operating guide says.

## Cost and databases that pause when idle

Every `mtix sync init`, `push`, `pull`, `clone`, `doctor`, `backup`, `harden`, `migrate`, `mark-restored` and `collisions` command contacts the hub, even when nothing is pending. A database that scales to zero or suspends when idle therefore wakes on each of them (a push or pull at session start is one wake-up), and the first contact after idle waits for it to resume (mtix allows each hub connection 30 seconds). `mtix sync status`, `mtix sync quarantine list` and `mtix sync repair --status` read only the local store. Task commands such as `mtix create` and `mtix done` write locally and do not contact the hub.

mtix does not poll the hub on a timer by default. Keep it that way against a database that pauses: sync after activity (session start and after changes), never in a loop. `mtix daemon` and `mtix sync daemon` pull on an interval and keep the database awake around the clock; do not start either, and do not put sync in a cron job or a short-interval hook, without telling the user what it costs.

## Verify with doctor

Run `mtix sync doctor`, or `mtix sync doctor --json` for machine-readable output. Exit code 0 means no check failed; 2 means at least one did. Each check names its fix, and the fix names who runs it: the table owner, a role administrator, or you.

| Check | What it verifies |
|-------|------------------|
| `PG reachable` | the connection opens and answers |
| `schema current` | the hub has the current migrations, and the connecting role can run `record_restore_collision` and cannot write collision rows |
| `queue draining` | no event waits in `pending` for over an hour |
| `quarantined events` | no pulled event is held in the local quarantine |
| `held push events` | push holds no event the hub would refuse |
| `unique node uids` | no two tasks share a uid |
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
| `quarantined events` fails | `mtix sync pull` held events it could not validate or apply | Run `mtix sync pull` again. If events remain, run `mtix sync quarantine list` and give the user the event ids and reasons. Never delete quarantine rows or raise `sync.max_lamport_jump`. |
| `held push events` fails, or a `WARN: ... over the 65536-byte sync limit` line | push holds an event the hub would refuse | Follow the fix doctor prints for each event: shorten or split the field and save it again; for a held task creation, stop editing that task and tell the user. |
| `unique node uids` fails | two tasks share a uid | Do not edit the database. Follow the recovery the check names, and ask the user. |
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

Stop and ask the user, with the exact command, when: setup needs a database role, a grant, a revoke or a password; a DSN or CA bundle must be set; doctor names a fix that only the table owner or a role administrator can run; a repair, a harden `--apply` or a `mtix sync reconcile` is the next step; events stay quarantined or held after the fixes above; or the same failure comes back after you fixed it. Give the user the check name, the error line without any credential, and what you already tried.

## Never

- Never read, print, paste or commit a credential, and never put one in a tracked file, a ticket, a comment, a commit or chat.
- Never use the owner's or an administrator's credential for daily sync.
- Never run `mtix sync push --force` or `mtix sync repair --status --apply` unless the user asks for it.
- Never run `mtix sync reconcile --discard-local` yourself: it deletes local tasks and unpushed changes, needs `mtix sync push` and `pending` 0 first, and needs the ticket count typed at an interactive terminal, so it cannot run unattended. Ask the user to run it.
- Never run `mtix sync harden --apply` before the user approves the dry run, and never run `mtix sync harden` from a hook, a push or the daemon.
- Never start a timer or interval sync against a database that pauses when idle without telling the user the cost.
- Never edit `.mtix` data, `.mtix/tasks.json` by hand, or the hub's tables, and never delete quarantine rows.
- Never run `mtix sync mark-restored` more than once for one restore.
