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

## Configuration

```bash
mtix config get <key>   # Read config value
mtix config set <k> <v> # Set config value
```

`sync.keep_roles` lists, comma-separated, the hub roles `mtix sync harden`
leaves with their access (default: none).

## Hub Privileges (sync hub owners)

Only for projects that sync through a Postgres hub. Everyday use needs no
role configuration. Run these only when a human asks, as the role that owns
the sync tables (the one that ran `mtix sync init`); a superuser or a member
of the owner role may run them too:

```bash
mtix sync harden                          # Dry run: lists every role, default privilege and membership it would change; changes nothing
mtix sync harden --apply --keep-role <r>  # After a human approves the role list: restrict the sync tables to the owner and the kept roles
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

The node-number registry is the unique index `sync_events_node_registry_uidx` on `sync_events`. An index of that name that is not valid or not ready (`pg_index.indisvalid` or `indisready` false, as a failed or interrupted build leaves it) is skipped by `mtix sync init`'s migration. An index that is not ready checks no new create; one that is ready but not valid still refuses a duplicate create, but queries do not use it and it must be built again. `mtix sync doctor`'s `schema current` check then fails in every mode, and `mtix sync init` prints a WARN; both print the fix: as the table owner, run `mtix sync migrate --yes` while the version gate is open. That run records the duplicate creates of every project on the hub, drops the index and builds it again, leaving the recorded duplicates out by event id: they stay in the event log unchanged. On a hub without the index whose projects hold duplicate creates, `mtix sync init` refuses and names the same fix; run `mtix sync init` again after it. A hub with more than 100 duplicate creates, or more than 3600 bytes of their event ids, or a node that lost two numbers (the remap ledger records one number per node) is refused before the build, with the count or the creates named, and a refusal changes nothing on the hub; pushes keep working, and a push whose create takes a number already in use is still renumbered. `mtix sync migrate` never reports an index that is not valid or not ready as present. Verify: in `mtix sync doctor --json` the `schema current` check has `pass: true`, and `mtix sync migrate --json` shows `registry_index` with `valid` and `ready` true.

Never pass `--apply` without a human approving the dry run's role list. Never
run `mtix sync harden` from a hook, a push or the daemon.

## Sync Hub Backup, Restore and Conflicts

Only for projects that sync through a Postgres hub; run these only when a
human asks. Never put the DSN or a password on a command line.

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
