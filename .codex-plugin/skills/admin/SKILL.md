---
name: admin
description: Administrative operations for mtix projects. Backup, export, import, verification, statistics, sync hub privileges, sync hub backup and restore, and sync conflicts.
---

# mtix Administration

## Project Health

```bash
mtix stats              # Project statistics and progress
mtix verify             # Verify content hash integrity
mtix progress <id>      # Progress rollup for a subtree
```

## Data Management

```bash
mtix export             # Export task state to JSON
mtix import <file>      # Import from JSON
mtix backup <path>      # Create database backup
mtix gc                 # Run garbage collection
```

## Pulled `.mtix/tasks.json` not imported

After a `git pull`, the next CLI command (for example `mtix list`) imports a changed `.mtix/tasks.json`, or refuses and says why; `mtix sync` shows a pending refusal.

- Never run `mtix import .mtix/tasks.json --mode replace` to silence a refusal: it runs no loss check, deletes the comments, activity and tasks the board lacks, and needs the ticket count typed at an interactive terminal (no flag supplies it), so it cannot run unattended; name the merge or `mtix sync --fix` first.
- `mtix import .mtix/tasks.json --mode merge` keeps comments and activity from both sides and writes nothing while a task's status, assignee, agent state, wake time or deletion state differs from the file's (it lists each conflict; rerun it with `--prefer theirs|ours`, or `--theirs ID,ID` and `--ours ID,ID`, which it records in the task's activity), and decides the other field values per task: when the task's content (title, description, prompt, acceptance, labels) matches the file's, yours win, and otherwise the file's copy wins, undoing your edits to that task; afterwards check `git log -p .mtix/tasks.json` and reapply what it undid. Check the list before you choose: a wrong choice undoes a teammate's claim or your own unclaim.
- A conflict although you changed nothing locally (after an upgrade from 0.5.3 or earlier): run `mtix sync --fix`, then `git checkout HEAD -- .mtix/tasks.json`, then `mtix list`, which imports the board with the loss check. Never stop after `mtix sync --fix`: alone, it drops every change in the file.

## Corruption recovery

When `mtix export` or the automatic import of `.mtix/tasks.json` fails because a stored node field cannot be read, or mtix reports an integrity error at startup: copy `.mtix/data/` aside, then run `mtix recover`. It reads the database read-only and writes `.mtix/recovered-<time>.json` with a report.

- A task whose comments, activity, `code_refs` or `commit_refs` cannot be read gets that field from its copy in `.mtix/tasks.json` when the copy is usable (the only task under that id, the same uid when both carry one, `schema_version` 2.0.0 or later, times that pass the import checks); the report says `restored from the mirror <path>`. The copy is whatever `.mtix/tasks.json` holds: the last export, or a file a `git pull` or checkout put there (for example one whose automatic import was refused), which is another clone's copy and can hold its comments and lack local ones.
- A mirror whose checksum does not verify is still used; the note then ends `(the mirror checksum did not verify)`.
- Otherwise the report says the field is dropped and why, and the task is salvaged without it.
- A note `node A: the index entry for A points at the row of B; ...` means the primary-key index is damaged: nothing from that row is salvaged as A, and the row is salvaged once, as B. A itself is then salvaged from the database when another index entry reaches A's own row, taken from `.mtix/tasks.json` when the mirror holds it, or listed on the report's LOST line. Show such tasks to the user.
- `mtix import --mode replace` of the salvage file needs the ticket count typed at an interactive terminal, it cannot run unattended; before they run it, show the user every dropped or restored field and every note ending `(the mirror checksum did not verify)`, and have them compare each restored field with the local history (for example `git log -p .mtix/tasks.json`).

## Configuration

```bash
mtix config get <key>   # Read config value
mtix config set <k> <v> # Set config value
```

`sync.keep_roles` lists, comma-separated, the hub roles `mtix sync harden`
leaves with their access (default: none).

## Hub Privileges (sync hub owners)

Only for projects that sync through a Postgres hub. Everyday use needs no
role configuration. Run these only when the user asks, as the role that owns
the sync tables (the one that ran `mtix sync init`); a superuser or a member
of the owner role may run them too:

```bash
mtix sync harden                          # Dry run: lists every role, default privilege and membership it would change; changes nothing
mtix sync harden --apply --keep-role <r>  # After the user approves the role list: restrict the sync tables to the owner and the kept roles
mtix config set sync.keep_roles <r>[,<r>] # Record the kept roles; also turns on doctor strict mode (harden prints this; it never writes config)
mtix sync harden --json                   # Report for agents: before/after caller, caller_scope, findings, statements; keep_roles_hint
```

Exit code 0 means verification passed: apart from the table owner,
superusers, and the kept roles and their members, no role holds a privilege
on the sync tables, their sequences or the mtix functions (EXECUTE on the
trigger functions aside), or a role membership that leads to one, ADMIN
OPTION included; the owner's default privileges give such roles nothing; and
every TRUNCATE guard is in place. 2 means changes are pending or access
remains; 1 means an error or a refusal. Superusers and the REPLICATION role
attribute are outside the check: a role with REPLICATION is checked for its
privileges and memberships like any other role, but not for the attribute,
so review the roles that have it (`SELECT rolname FROM pg_catalog.pg_roles
WHERE rolreplication;`) with the database administrator. A role that is
not the table owner, a member of it or a superuser is refused and nothing
changes. Members of a kept role keep their access through it, and the owner
grants again anything a kept role holds by another role's grant; superusers
are out of scope. A server WARNING fails the run and nothing changes. Access
harden may not change is printed with the statement a database administrator
runs. EXECUTE on the mtix trigger functions is information only, so a freshly
migrated hub verifies clean.

`mtix sync doctor` runs the same verification as its `hub-privileges` check: a
WARN (exit 0) by default when other roles can use the sync tables, which may be
fine on a private network and blocks nothing; a FAIL (exit 2) only in strict
mode, when `sync.keep_roles` is set, and then whenever `mtix sync harden`
would report a finding (kept roles' grant options and grants from other
roles, mtix objects owned by another role, and memberships that reach every
table included, such as `superuser_membership`: a role that can SET ROLE to
a superuser, or that holds ADMIN OPTION on a role that can), or when the
check cannot run.
`mtix sync init` changes no existing privilege (it creates the restore-collision functions of migration 017 without EXECUTE for PUBLIC, whatever the owner's default privileges) and `mtix sync push` issues no DDL.

The hub stamps every event with its own restore epoch (a trigger on `sync_events`). A syncing role records restore collisions only through the hub function `record_restore_collision`, which runs as the table owner and records a collision only when the hub's own data shows an earlier-epoch create holding the number, so the role needs EXECUTE on that function and no INSERT on `sync_node_collisions`. When `mtix sync init` adds this function to an existing hub, the table owner then grants EXECUTE on it to each syncing role and revokes the privileges the least-privilege list no longer names:

```sql
GRANT EXECUTE ON FUNCTION record_restore_collision TO <role>;
-- once every syncing client is upgraded:
REVOKE INSERT ON sync_node_collisions FROM <role>;
REVOKE USAGE ON SEQUENCE sync_node_collisions_collision_id_seq FROM <role>;
```

Upgrade every syncing client first, then run the REVOKE statements; if the REVOKE comes first, an older client's push that meets a restore collision fails until that client upgrades. `mtix sync doctor`'s `schema current` check, run with a syncing role's DSN, reports each of these as a WARN by default and a FAIL in strict mode, with the fix the table owner runs: a hub without migration 017 (pushes keep working; the fix is `mtix sync init`), a role that cannot execute the function (a push that meets a restore collision fails until the table owner runs the printed GRANT), create events stamped with a restore epoch below 0 or above the hub's current epoch (restore-collision checks treat each as not earlier than the current epoch; the fix is the printed UPDATE, which sets each to the current epoch), and a role that can write collision rows: it holds INSERT on `sync_node_collisions` or USAGE on its sequence (a grant, or a predefined role such as `pg_write_all_data`), owns the schema that holds the sync tables, can reach, through a chain of SET ROLE and ADMIN OPTION, a role that holds either or owns that schema, or a superuser it can then SET ROLE to, has CREATEROLE before PostgreSQL 16, or is a member of `pg_execute_server_program` or `pg_write_server_files`. The check skips the table owner, roles that inherit it, and superusers. The fix is the exact REVOKE the table owner runs for a plain grant the owner made, and, for every other path, the path by name, with the chain that reaches it, which a role administrator removes (see "Hub health checks" in the user manual). `mtix sync harden --apply` revokes EXECUTE on it from every role that is not kept.

The node-number registry is the unique index `sync_events_node_registry_uidx` on `sync_events`. An index of that name that is not valid or not ready (`pg_index.indisvalid` or `indisready` false, as a failed or interrupted build leaves it) is skipped by `mtix sync init`'s migration. An index that is not ready checks no new create; one that is ready but not valid still refuses a duplicate create, but queries do not use it and it must be built again. A hub that holds `sync_events` but no index of that name is a WARN in `schema current` by default (a FAIL in strict mode), with the same fix. `mtix sync doctor`'s `schema current` check fails in every mode for an index that is not valid or not ready, and `mtix sync init` prints a WARN; both print the fix: as the table owner, run `mtix sync migrate --yes` while the version gate is open. That run records the duplicate creates of every project on the hub, drops the index and builds it again, leaving the recorded duplicates out by event id: they stay in the event log unchanged. On a hub without the index whose projects hold duplicate creates, `mtix sync init` refuses and names the same fix; run `mtix sync init` again after it. A hub with more than 100 duplicate creates, or more than 3600 bytes of their event ids, or a node that lost two numbers (the remap ledger records one number per node) is refused before the build, with the count or the creates named; a refused build drops and builds nothing, but Phase 1 of the same run has already recorded the duplicates (remap and conflict rows), and the report, in text and in `--json`, still shows that sweep phase; pushes keep working, and a push whose create takes a number already in use is still renumbered. `mtix sync migrate` never reports an index that is not valid or not ready as present. Verify: in `mtix sync doctor --json` the `schema current` check has `pass: true`, and `mtix sync migrate --json` shows `registry_index` with `valid` and `ready` true.

Never pass `--apply` without the user approving the dry run's role list. Never
run `mtix sync harden` from a hook, a push or the daemon.

## Sync Hub Backup, Restore and Conflicts

Only for projects that sync through a Postgres hub; run these only when the
user asks. Never put the DSN or a password on a command line.

```bash
mtix sync backup --output hub-<date>.sql  # pg_dump of every hub table; creates the file 0600, never overwrites
mtix sync doctor --json                   # hub-triggers: every mtix function and trigger present, bound to its function, enabled (O or A)
mtix sync conflicts list [--all]          # unresolved conflicts (every row, marked, with --all)
mtix sync conflicts resolve <id> --action keep-local|keep-remote|both-renumbered|acknowledge
```

The backup uses the TLS settings sync uses (`sslmode` `verify-full` when
the DSN names none; a weaker one needs `--insecure-tls`, loopback or a
local socket only; the CA from `sslrootcert` or `MTIX_SYNC_SSLROOTCERT`).
`pg_dump` does not receive the DSN's `options`: for a hub whose schema is
named only there, first
`ALTER ROLE <the DSN's role> IN DATABASE <the DSN's database> SET search_path = <schema>, public`
(the role the DSN names, which may not be the table owner; this database
only, and it takes precedence over a role-wide
`ALTER ROLE <the DSN's role> SET search_path = <schema>, public`). Every hub table must exist: a hub that lacks one, such as a hub
not initialized since an upgrade added a table, fails the backup with `a
hub table was not found`; run `mtix sync init` with the DSN naming the
table owner, then back up again. The same error comes when the DSN's role
lacks USAGE on the hub's schema: run the GRANT statements the backup
prints (the schema's owner grants USAGE on the schema: find it with
`SELECT nspowner::regrole FROM pg_catalog.pg_namespace WHERE nspname = '<schema>';`;
for `public` in a database created on PostgreSQL 15 or later it is
`pg_database_owner`, that is, the database's owner; the table owner grants
SELECT on each sync table and sync-table sequence by name, nothing else in
the schema), then keep that role with `--keep-role` so
`mtix sync harden --apply` leaves its access.
Client certificates (`sslcert`, `sslkey`) are not passed to `pg_dump`, so a
hub that requires one cannot be backed up with this command yet. A failed
or interrupted (Ctrl-C, SIGTERM) backup leaves no file; an existing path is
refused.

Restore into an empty database (if the tables were in a schema other than
`public`, create that schema first and put it first on the search_path for
psql and for mtix; `mtix sync init`, and a `mtix sync harden --apply` that
would restore or replace a guard, refuse, changing nothing, when another
schema comes first, usually a schema named after the role under the default
`"$user", public`): `psql -f <file>` as
the role that will own the sync tables, with `PGSSLMODE=verify-full` and `PGSSLROOTCERT=<ca.pem>`
(or `system` with libpq 16 or later) and the password in `~/.pgpass` (psql
reports errors for the triggers; their functions do not exist yet), then,
after checking as that role that `SHOW search_path;` puts the tables'
schema first (not a schema named after the role), `mtix sync init` with
the DSN naming that role, then `mtix sync doctor`
(and `SELECT count(*) FROM sync_events;` through the DSN's role and
search_path must equal `SELECT count(*) FROM <schema>.sync_events;` run as
the table owner in the restored database: a mismatch shows the DSN does
not reach the restored database, while equality with a non-zero count
rules out only a new or empty hub, so also confirm that the DSN's host and
database name the restored server; the count must not be zero for a hub
that has events; being at least the source hub's count taken just before
the backup rules out only a new or emptier hub; never compare with psql's
unlabeled `COPY <n>` lines) until `hub-triggers` passes (its `fix` names the table owner who runs it
and, in order, `mtix sync init` for a missing trigger or one bound to
another function, and the `ALTER TABLE ... ENABLE TRIGGER` statement for
one not enabled, printed on a `fix:` line without `--json`; functions are
compared by OID; tgenabled `O` and `A` both count as enabled; a gap is a
WARN by default, a FAIL in strict mode), then
`mtix sync mark-restored` once, with the DSN naming the table owner, and
`mtix sync collisions list`.

A dump holds no privileges: after `mtix sync init`, grant each syncing role
the least-privilege list again, EXECUTE on `record_restore_collision`
included, then run `mtix sync doctor` with a syncing role's DSN. Its
`schema current` check names a syncing role that cannot execute the
function.

A syncing role set up with the least-privilege list holds no UPDATE on
`sync_hub_state`, so it cannot run `mtix sync mark-restored`, which runs as
the table owner. The least-privilege list is in step 2 of the small-team
workflow (`.mtix/docs/workflows/small-team.md`) and in
`docs/SECURITY-MODEL.md`.

`mtix sync status` counts unresolved conflicts only (`open_conflicts`); it
has no `conflicted` count. `resolve` records the decision only and says
`decision recorded; node state not changed`: apply the chosen value with
`mtix update` and push. A conflict recorded later on the same node and
field is unresolved again. Resolve the newest conflict of a node and field:
one with a later decision or a later conflict on its node and field is
refused as invalid input, and the error names the newest conflict id.
Resolving a `manual` row is refused as invalid input too.

## Documentation

```bash
mtix docs generate      # Regenerate agent documentation
mtix plugin install     # Install IDE skill files
```

## Sync Recovery

```bash
mtix sync pull                             # First, and again just before --apply: a repair on a stale log can revert a teammate's newer change everywhere
mtix sync repair --status                  # List nodes an older pull left reverted (dry run)
mtix sync repair --status --apply          # Back up, then repair all but flagged nodes (after the user approves)
mtix sync repair --status --apply --force  # Also repair flagged nodes, after the user reviews each one
mtix sync push                             # Send the repair events
```

Quarantined pulled events (MTIX-95.11): `mtix sync pull` checks every pulled event (its Lamport clock, below 2^53 and at most `sync.max_lamport_jump` above the local clock, default 4294967296; then the FR-18.7 size, shape and id rules) and holds one that fails, or whose apply fails, in a local quarantine instead of applying it; every pull retries the quarantine.

```bash
mtix sync doctor                           # "quarantined events" fails while any are held
mtix sync quarantine list                  # Read-only: event id, node, op, attempts, first seen, last attempt, reason (--json)
mtix sync pull                             # Retries them; one waiting for a task or dependency target applies once it arrives
```

`mtix sync clone` refuses, naming the event, and writes nothing while the hub holds an event that fails these checks. On the fresh store, recover with `mtix sync pull` alone, which quarantines that event and applies the rest. `mtix sync reconcile --discard-local` is only for a store that already holds sync state, and it deletes local tasks and unpushed changes (its dry run shows only the node count); it needs the ticket count typed at an interactive terminal (no flag supplies it), so an agent never runs it and hands it to the user: first run `mtix sync push`, confirm `mtix sync status` shows pending 0, and get the user's approval. A quarantined copy of this replica's own event does not clear by itself: the hub row is repaired first, then push, then (the user running discard-local) pull. A quarantined event from a teammate whose hub row was repaired stays quarantined too, so repairing the hub row does not clear it: the recovery is the same guarded rebuild (push, `pending` 0, the user runs discard-local, then pull); never raise `sync.max_lamport_jump` for a row the hub has repaired. Never delete quarantine rows or edit the local database, never raise `sync.max_lamport_jump` (`mtix config set sync.max_lamport_jump <positive integer>`) without the user's confirmation, and never get past a refused clone any other way; show the user the listed reasons for an event that stays quarantined.

Held push events (MTIX-95.12): the sync hub accepts an event payload of at most 64 KB, while local fields can be larger. With a hub configured, a CLI change over the limit still succeeds and prints `WARN: <id>: the <field> field makes this <op> sync event <n> bytes, over the 65536-byte sync limit ...` (for MCP, an extra text block of the result); web UI, REST and gRPC changes print no warning, and imports write no sync events. `mtix sync push` holds such an event (it stays pending, recorded in the local quarantine with source `push`, never sent) and pushes the rest. The reason starts with its kind: `too large:`/`refused:` (permanent), `temporary: clock:` (stamped more than 24h ahead; every push re-checks it and releases it once it passes), or `depends on held create of <event id> (<task>)` (while a task's creation is held, none of the changes of its subtree are sent: push holds every change of that task and of the tasks below it, including changes made after the last push, finding the task by its internal id, so a change made before a renumber is still held and a change of a task that later takes an old number is not; after `mtix gc` purges a deleted task, its held changes stay held and others are checked by the number they name; the reason names the nearest held creation; known limits: if a task whose internal id a merge import changed is then renumbered on this machine, or a task renumbered on this machine is purged by `mtix gc`, its changes that no push checked before that are not recognized). A dependency link or unlink names its target by number only, so while any creation is held, every link or unlink made after it is held (reason ending `links made while a task creation is held wait for it; ...`, naming the earliest held creation) until no creation made before it is held. Once a clock hold on a creation clears, the creation and the changes of its subtree go through the ordinary push in the same run, where a renumber by the hub moves the task and re-addresses every unsent change of its subtree to the new number before any is sent (MTIX-95.37): push sends a task's creation before any change, child creation or link that depends on it, and pull never applies a teammate's change to a task of yours whose creation is not on the hub yet, whether the change names it by id or by number (as a parent, a link target, or an old client's task number): such an event is quarantined and retried after your push moves your task. After a push that reports `renumbered`, find your task by title, not by its old number, and run `mtix sync pull` and `mtix sync status`. A teammate's events the hub already holds under an old number from before this fix stay there, and a fresh `mtix sync clone` of such a hub can fail with `node <id> (uid=""): not found`; there is no recovery in this version (tracked as MTIX-95.37.1), so report it to whoever runs the hub. Push checks each of those changes first; one the hub would refuse stays held under its own reason, and a child creation among them keeps its own subtree held. A creation held permanently keeps its whole subtree held.

```bash
mtix sync doctor                           # "held push events" fails while any are held; lists "<node> <op>: <fix> (reason: ...)"
mtix sync status                           # "held push events" (held_push_events in --json); held events stay in pending
mtix sync quarantine list --json           # Read-only; held push events have "source": "push"
```

Follow doctor's fix per event: a field edit over the limit, shorten or split the field and save it again (the new event pushes); a held task creation, stop editing the task and show the user (no automatic re-send in this release); a dependent resolves with the held creation it depends on, and a held link pushes once no creation made before it is held (do not re-create it); a clock hold, check this machine's clock (the event pushes once its stamp is within 24 h of the clock, and a creation's subtree pushes with it). Permanent holds and their dependents stay listed (no command in this release releases or discards one). Never delete quarantine rows, and never use `mtix sync reconcile --discard-local` (it deletes local tasks and unpushed changes, so it needs `mtix sync push` and `pending` 0 first, then asks for the ticket count, typed at an interactive terminal, so it cannot run unattended) to clear held events.

A pending queue that never drained (MTIX-95.3): push marks an event pushed only after the hub confirms the batch, so an event the hub committed stays pending when the confirmation is lost (a connection dropped during the commit), when mtix crashed or the disk failed or filled up before the local mark, or when push retried after a network drop. Before 0.5.4 the hub confirmed only events it inserted, so these stayed pending forever, and 100 of them at the head of the queue stopped every later push while `mtix sync push` reported success (`mtix sync status` pending never going down; `mtix sync doctor` failing `queue draining`). Push now acknowledges events already on the hub: an event whose id the hub holds with the same task, operation and payload is confirmed and marked pushed. After upgrading, run `mtix sync push` once; nothing else is needed (no hub change; the check is one more query within a push, only for a batch holding events the hub already had).

```bash
mtix sync push                             # stderr: "push progress: batch <n> (<sent> sent, <accepted> accepted: <i> inserted, <p> already on the hub; ...)"
                                           # stdout: "push complete: <n> events pushed across <b> batches (<i> inserted, <p> already on the hub); ..."
mtix sync status                           # verify: pending 0 (held push events stay counted in pending)
mtix sync doctor                           # verify: "queue draining" passes
```

The inserted count is events this push wrote to the hub; the already-on-the-hub count is events an earlier push delivered (and a re-sent creation of a task the hub holds); both are marked pushed, and a non-zero already-on-the-hub count after an interrupted push is expected; an event already on the hub is not checked for conflicts again, so a re-push adds no conflict; after a lost commit acknowledgement a push's conflict count can read 0, so check `mtix sync conflicts list`. An event whose id the hub holds with a different task, operation or payload is never confirmed: push holds it permanently (reason `refused: the hub already holds this event id with a different <field> (hub copy: <task> <op>)`; for a task's creation its later changes and subtree are held too) and `mtix sync doctor` fails `held push events`; show the user the event id, task and reason for whoever runs the hub. Never mark events pushed by hand, delete pending events or holds, or use `mtix sync reconcile --discard-local` (it deletes local tasks and unpushed changes, so it needs `mtix sync push` and `pending` 0 first, then asks for the ticket count, typed at an interactive terminal, so it cannot run unattended) to make the queue drain.

## Operator-local state

Run `mtix hooks status` or `mtix hooks status --json` to validate the operator-local state directory and read corrective guidance. This check creates no state and grants no hook approval. The directory is `$XDG_CONFIG_HOME/mtix` (default `~/.config/mtix`) on macOS and Linux, or `%APPDATA%\mtix` on Windows. Use an operator-owned directory with mode 0700 and files with mode 0600; Windows requires a protected user-only ACL. Keep it outside projects, git work trees, temporary directories, `~/.codex`, `CODEX_HOME` and harness writable roots, with no symlink components. Never commit or dotfile-sync it.

This directory holds a persistent host identifier and host-specific state records. Other-host records are ignored and reported. Missing records are empty; invalid or inaccessible state is refused. This storage foundation does not move existing hook approvals or enable new grants. To recover, correct the reported path, ownership or access rules manually, then repeat `mtix hooks status --json`; preserve existing state records while correcting permissions.
