---
description: "Administer MTIX project using mtix. Use when backing up data, exporting/importing tasks, running garbage collection, managing configuration, verifying data integrity, restricting who can use a sync hub's tables (mtix sync harden), backing up or restoring a sync hub (mtix sync backup), or resolving sync conflicts."
allowed-tools:
  - mcp__mtix__mtix_export
  - mcp__mtix__mtix_import
  - mcp__mtix__mtix_gc
  - mcp__mtix__mtix_backup
  - mcp__mtix__mtix_config
  - mcp__mtix__mtix_verify
---

# MTIX — Administration

## Context: Data Integrity is Non-Negotiable

mtix manages task hierarchies that may track safety-critical work (aviation maintenance, medical device development, mission control operations, defense systems). Every administrative operation must preserve data integrity and maintain the audit trail.

## Backup-Before-Mutate Protocol

**ALWAYS run `mcp__mtix__mtix_backup` before any destructive operation:**

- Before `mcp__mtix__mtix_import` (may overwrite existing data)
- Before `mcp__mtix__mtix_gc` (permanently removes soft-deleted data)
- Before any database maintenance
- Before major configuration changes

Backup creates a timestamped copy of the SQLite database. Store backups in a safe location outside the project directory.

## Export

Call `mcp__mtix__mtix_export` to create a JSON snapshot of all project data.

The export includes:
- All nodes with full field data
- All dependencies
- All agent records
- All session records
- SHA-256 checksum for integrity verification

**After export:** Verify the checksum field is present. Store exports alongside backups for disaster recovery.

## Import

Call `mcp__mtix__mtix_import` with the export file path and mode:

- **merge** — adds new data without overwriting existing nodes
- **replace** — replaces all project data with the import file

**Import protocol:**
1. Run `mcp__mtix__mtix_backup` first
2. Import the data
3. Run `mcp__mtix__mtix_verify` to confirm integrity post-import
4. Check `mcp__mtix__mtix_stats` to verify expected node counts

**Never skip the post-import verification.** A corrupt import in a safety-critical environment could hide incomplete or missing tasks.

## Garbage Collection

Call `mcp__mtix__mtix_gc` to permanently remove soft-deleted nodes past the retention period (default: 30 days).

**Before GC:**
- Verify retention period is appropriate for your compliance requirements (some standards require longer retention)
- Check what will be collected — soft-deleted nodes that are past retention
- Backup first

**GC does NOT delete:**
- Active nodes (any status except soft-deleted)
- Nodes within the retention window
- Agent records or session records (these are permanent audit trail)

## Configuration

Use `mcp__mtix__mtix_config` to view or modify project settings.

Key configuration options:
- `prefix` — project prefix for dot-notation IDs
- `max_depth` — maximum hierarchy depth (default: 50)
- `auto_claim` — whether to auto-claim on creation
- `agent_stale_threshold` — heartbeat timeout for stale detection (default: 30m)
- `session_timeout` — maximum session duration (default: 8h)
- `data.soft_delete_retention` — how long to keep deleted data (default: 720h/30 days)
- `sync.keep_roles` — comma-separated hub roles that `mtix sync harden` leaves with their access (default: none; see Hub Privileges). Setting it turns on strict mode for the doctor's `hub-privileges` check, which then fails whenever `mtix sync harden` would report a finding, or when the check cannot run

## Hub Privileges (Sync Hub Owners)

Only for projects that sync through a Postgres hub (`mtix sync init`). Everyday use needs no role configuration. `mtix sync harden` is for an owner who wants the hub's sync tables usable only by the owner and the roles the team chooses. It is a CLI command; run it only when a human asks for it, never from a hook, push or daemon. It contacts the hub only while it runs, so it does not keep a scale-to-zero database awake.

The append-only tables (`audit_log`, `sync_conflicts`, `sync_events`) refuse TRUNCATE. `mtix sync init` adds these guards automatically; `mtix sync harden` checks them.

**Restore collisions:** the hub stamps every event with its own restore epoch (a trigger on `sync_events` sets it from `sync_hub_state` on every insert). A syncing role records restore collisions only through the hub function `record_restore_collision`, which runs as the table owner and records a collision only when the hub's own data shows an earlier-epoch create holding the number, so the role needs EXECUTE on that function and no INSERT on `sync_node_collisions`. When `mtix sync init` adds this function to an existing hub (migration 017), the table owner then grants EXECUTE on it to each syncing role (`GRANT EXECUTE ON FUNCTION record_restore_collision TO <role>;`) and revokes the privileges the least-privilege list no longer names (`REVOKE INSERT ON sync_node_collisions FROM <role>;` and `REVOKE USAGE ON SEQUENCE sync_node_collisions_collision_id_seq FROM <role>;`). Upgrade every syncing client first, then run the REVOKE statements; if the REVOKE comes first, an older client's push that meets a restore collision fails until that client upgrades. `mtix sync doctor`'s `schema current` check, run with a syncing role's DSN, reports each of these as a WARN by default and a FAIL in strict mode, with the fix the table owner runs: a hub without migration 017 (pushes keep working; the fix is `mtix sync init`), a role that cannot execute the function (a push that meets a restore collision fails until the table owner runs the printed GRANT), create events stamped with a restore epoch below 0 or above the hub's current epoch (restore-collision checks treat each as not earlier than the current epoch; the fix is the printed UPDATE, which sets each to the current epoch), and a role that can write collision rows: it holds INSERT on `sync_node_collisions` or USAGE on its sequence (a grant, or a predefined role such as `pg_write_all_data`), owns the schema that holds the sync tables, can reach, through a chain of SET ROLE and ADMIN OPTION, a role that holds either or owns that schema, or a superuser it can then SET ROLE to, has CREATEROLE before PostgreSQL 16, or is a member of `pg_execute_server_program` or `pg_write_server_files`. The check skips the table owner, roles that inherit it, and superusers. The fix is the exact REVOKE the table owner runs for a plain grant the owner made, and, for every other path, the path by name, with the chain that reaches it, which a role administrator removes (see "Hub health checks" in the user manual). `mtix sync harden --apply` revokes EXECUTE on the function from every role that is not kept, so keep each syncing role with `--keep-role`.

**Registry index:** the node-number registry is the unique index `sync_events_node_registry_uidx` on `sync_events`. An index of that name that is not valid or not ready (`pg_index.indisvalid` or `indisready` false, as a failed or interrupted build leaves it) checks no new create, and `mtix sync init`'s migration skips it. `mtix sync doctor`'s `schema current` check then fails in every mode, and `mtix sync init` prints a WARN; both print the fix: as the table owner, run `mtix sync migrate --yes` while the version gate is open. That run records the duplicate creates of every project on the hub, drops the index and builds it again, leaving the recorded duplicates out by event id: they stay in the event log unchanged. On a hub without the index whose projects hold duplicate creates, `mtix sync init` refuses and names the same fix; run `mtix sync init` again after it. A hub with more than 100 duplicate creates is refused before the build, with the count; pushes keep working, and a push whose create takes a number already in use is still renumbered. `mtix sync migrate` never reports an index that is not valid or not ready as present. Verify: in `mtix sync doctor --json` the `schema current` check has `pass: true`, and `mtix sync migrate --json` shows `registry_index` with `valid` and `ready` true.

**Doctor:** `mtix sync doctor` runs the same verification as its `hub-privileges` check, as whichever role the DSN names. A **WARN** (exit 0) means roles other than the owner can use the sync tables, or a TRUNCATE guard is missing or disabled; this is acceptable for everyday use, may be fine when the database is reachable only from a private network, and blocks nothing. It is a **FAIL** (exit 2) only in strict mode (`sync.keep_roles` set), and then whenever `mtix sync harden` would report a finding: not only a role outside the list or a missing or disabled guard, but also a kept role that can grant its access on or holds a privilege another role granted it, an mtix object owned by another role, and a membership through which a role can reach every table. If the verification cannot run (hub unreachable, schema incomplete), the check is a WARN that says so by default, and a FAIL in strict mode. With `--json` the check carries `name`, `pass`, `warn`, `detail`, `fix` and `findings`. `mtix sync init` changes no existing privilege (it restores missing guards, replaces a guard bound to another function, and creates the restore-collision functions of migration 017 without EXECUTE for PUBLIC, whatever the owner's default privileges), and `mtix sync push` issues no DDL.

**Routine:**
1. As the role that owns the sync tables (the one that ran `mtix sync init`; a superuser or a member of the owner role may run it too), run the dry run: `mtix sync harden`. It changes nothing.
2. Show the human the role list at the top of the report. Every role that is not kept loses its access with `--apply`. A role other people or services use to sync must be kept with `--keep-role <role>` (repeatable); members of a kept role keep their access through it. Superusers are out of scope: harden neither checks nor changes them.
3. Only after the human approves that list: `mtix sync harden --apply --keep-role <role>`.
4. When the report prints `mtix config set sync.keep_roles <roles>`, show it to the human; running it records the kept roles for later runs and also turns on strict mode for `mtix sync doctor` (its `hub-privileges` check then fails, instead of warning, whenever `mtix sync harden` would report a finding, or when the check cannot run). Harden never writes the config itself.
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
- Never pass `--apply` without a human approving the dry run's role list.
- Never run `mtix sync harden` from a hook, a push or the daemon.
- Never read, print or paste the hub DSN to run it; it comes from `MTIX_SYNC_DSN` or `.mtix/secrets`.

## Sync Hub Backup and Restore (Sync Hub Owners)

Only for projects that sync through a Postgres hub. Run these only when a human asks. `mtix sync backup` contacts the hub only while it runs, so it does not keep a scale-to-zero database awake beyond the dump.

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

Run `mcp__mtix__mtix_verify` to check content hash integrity across all nodes.

**When to verify:**
- After every import operation
- After system crashes or unexpected shutdowns
- As part of regular audit cycles
- Before critical milestones or releases
- Any time data integrity is in question

**On hash mismatch:** Do NOT modify the affected nodes. Document the mismatch, escalate, and restore from the most recent verified backup.
