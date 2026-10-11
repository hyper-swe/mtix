---
description: "Set up, use, verify and recover the MTIX sync hub, the shared Postgres database that carries tasks between machines. Use when configuring a hub from zero, creating least-privilege database roles, sourcing the hub credential, running the push-then-pull routine, reading mtix sync doctor output, or recovering from a sync failure (quarantined or held events, a pending queue that never drains, workflow state an older pull reverted, a hub restored from a backup)."
---

# MTIX — Sync Hub

The hub is a Postgres database that you and your teammates' agents push task changes to and pull each other's changes from. Each machine keeps its own local store; the hub carries the event log between them. This skill is the full runbook: requirements, setup from zero, daily use, verification, troubleshooting and recovery. The short routine is in the managed SYNC section of `CLAUDE.md` and `AGENTS.md`.

## Pending mirror exports

Mirror exports read nodes, dependencies, agents and sessions from one read-only WAL snapshot, allowing writers to commit while the export reads. The mirror file and its conflict baseline describe that same snapshot; publication writes the file before its hashes. A write committed after the snapshot is handled by a later export.

A writing command that encounters the mirror sync lock saves its database change and returns without waiting. A durable request in `.mtix/data/export-pending/` keeps the mirror update pending across process exit or restart. A service import lock holder drains after releasing its lock; the next automatic export or either daemon's next tick also retries, without another task mutation. Each trigger performs at most three successful publication passes, releasing the lock between passes; contention, refusal or errors stop recovery. Requests arriving after a pass's snapshot remain for a later pass or trigger. Publication errors retain requests for recovery. Request creation or enumeration errors are reported while a protected mirror publication is still attempted; an unreadable request snapshot cannot authorize deleting requests. Automatic retries never overwrite a changed, unreadable or refused pulled board: resolve the import refusal using `mtix sync` guidance first. Do not edit or delete pending requests by hand. Long-running interfaces mark the request synchronously in their post-commit hook before starting debounce, and still schedule the export if marking fails. This narrows the debounce crash window; a small commit-to-hook window and the CLI commit-to-post-run window remain outside this guarantee. Request and publication files are synced. Unix directory Sync EINVAL/ENOTSUP is logged and treated as best-effort; other directory errors remain failures. Windows does not sync directory entries. Power-loss durability of directory entries is not promised where directory Sync is unsupported.

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
- In a git hook or script, set `MTIX_SYNC_HOOK=1`: a transient hub error then prints a warning and the command continues, so it never blocks a code push.
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
