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
remains; 1 means an error or a refusal. A role that is
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
mode, when `sync.keep_roles` is set, and then for every finding harden would
report (kept roles' grant options and grants from other roles, mtix objects
owned by another role, and memberships that reach every table included).
`mtix sync init` changes no privileges and `mtix sync push` issues no DDL.

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
`ALTER ROLE <the DSN's role> SET search_path = <schema>, public` (the role
the DSN names, which may not be the table owner). Every hub table must exist: a hub that lacks one, such as a hub
not initialized since an upgrade added a table, fails the backup with `a
hub table was not found`; run `mtix sync init` with the DSN naming the
table owner, then back up again.
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
the table owner in the restored database, or be at least the source hub's
count taken just before the backup, never psql's unlabeled `COPY <n>`
lines, and must not be zero for a hub that has events) until `hub-triggers` passes (its `fix` names the table owner who runs it
and, in order, `mtix sync init` for a missing trigger or one bound to
another function, and the `ALTER TABLE ... ENABLE TRIGGER` statement for
one not enabled, printed on a `fix:` line without `--json`; functions are
compared by OID; tgenabled `O` and `A` both count as enabled; a gap is a
WARN by default, a FAIL in strict mode), then
`mtix sync mark-restored` once and `mtix sync collisions list`.

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
