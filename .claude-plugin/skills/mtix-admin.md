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
- `sync.keep_roles` — comma-separated hub roles that `mtix sync harden` leaves with their access (default: none; see Hub Privileges). Setting it turns on strict mode for the doctor's `hub-privileges` check

## Hub Privileges (Sync Hub Owners)

Only for projects that sync through a Postgres hub (`mtix sync init`). Everyday use needs no role configuration. `mtix sync harden` is for an owner who wants the hub's sync tables usable only by the owner and the roles the team chooses. It is a CLI command; run it only when a human asks for it, never from a hook, push or daemon. It contacts the hub only while it runs, so it does not keep a scale-to-zero database awake.

The append-only tables (`audit_log`, `sync_conflicts`, `sync_events`) refuse TRUNCATE. `mtix sync init` adds these guards automatically; `mtix sync harden` checks them.

**Doctor:** `mtix sync doctor` runs the same verification as its `hub-privileges` check, as whichever role the DSN names. A **WARN** (exit 0) means roles other than the owner can use the sync tables, or a TRUNCATE guard is missing or disabled; this is acceptable for everyday use, may be fine when the database is reachable only from a private network, and blocks nothing. It is a **FAIL** (exit 2) only in strict mode (`sync.keep_roles` set), and then whenever `mtix sync harden` would report a finding: not only a role outside the list or a missing or disabled guard, but also a kept role that can grant its access on or holds a privilege another role granted it, an mtix object owned by another role, and a membership through which a role can reach every table. If the verification cannot run (hub unreachable, schema incomplete), the check is a WARN that says so by default, and a FAIL in strict mode. With `--json` the check carries `name`, `pass`, `warn`, `detail`, `fix` and `findings`. `mtix sync init` changes no privileges (it only restores missing guards and replaces a guard bound to another function), and `mtix sync push` issues no DDL.

**Routine:**
1. As the role that owns the sync tables (the one that ran `mtix sync init`; a superuser or a member of the owner role may run it too), run the dry run: `mtix sync harden`. It changes nothing.
2. Show the human the role list at the top of the report. Every role that is not kept loses its access with `--apply`. A role other people or services use to sync must be kept with `--keep-role <role>` (repeatable); members of a kept role keep their access through it. Superusers are out of scope: harden neither checks nor changes them.
3. Only after the human approves that list: `mtix sync harden --apply --keep-role <role>`.
4. When the report prints `mtix config set sync.keep_roles <roles>`, show it to the human; running it records the kept roles for later runs and also turns on strict mode for `mtix sync doctor` (its `hub-privileges` check then fails, instead of warning, when any other role can use the sync tables). Harden never writes the config itself.
5. Verify: run `mtix sync harden` again and expect exit 0. `verification passed` means: apart from the table owner, superusers, and the kept roles and their members, no role holds a privilege on the sync tables, their sequences or the mtix functions (EXECUTE on the trigger functions aside), or a role membership that leads to one, ADMIN OPTION included; the owner's default privileges give such roles nothing; and every TRUNCATE guard is in place. It says nothing about superusers or about who can reach the database over the network.

**Exit codes:** 0 verification passed; 2 changes pending (dry run) or access remains after `--apply`; 1 error or refusal. With `--json` the report carries `before` and `after`, each with `caller`, `caller_scope` (`owner`, `superuser`, `kept` or `checked`), `findings` (`role`, `object`, `kind`, `privileges`, `via`, `scope`, `fix`, `manual`, `note`), `statements` and `kept_roles`, plus `executed` and `keep_roles_hint`. A finding with `fix` is changed by `--apply`; one with `manual` or `note` is not.

**What `--apply` changes, in one transaction:** every privilege on the sync tables (column privileges included), their sequences and the mtix functions held by PUBLIC, by the roles a data API uses for anonymous and signed-in callers, and by every other role except the owner, superusers and the kept roles; a kept role's right to grant its privileges on (first the owner grants again anything a kept role holds by another role's grant, so the kept role keeps it); the owner's default privileges toward those roles; a membership in `pg_read_all_data`, `pg_write_all_data` or (from PostgreSQL 17) `pg_maintain`, ADMIN-only memberships included, when the owner may revoke it (this is cluster-wide); a missing TRUNCATE guard, one whose trigger executes another function (compared by OID, so a function of the guard's name in another schema counts as another function; the guard migration replaces it), or one that is disabled or fires only in replication sessions (every guard ends enabled). After `--apply` every finding that remains is listed with its statement.

**Information, never a failure:** EXECUTE on the mtix trigger functions (they cannot be called directly) and other roles' default privileges (they do not apply to tables the owner creates). A freshly migrated hub verifies clean.

**Troubleshooting:**
- `refused: the connecting role does not own every sync table`: connect as the table owner and run it again. Nothing was changed.
- `raised a server WARNING`: an mtix object named in the message is owned by another role, so the owner cannot change its privileges. Nothing was changed. A database administrator returns the object to the table owner, then run it again.
- `the hub schema is incomplete`: run `mtix sync init` first.
- `an administrator runs: ...`: access harden may not change, such as a read-all membership granted by another role. Give the statement to the database administrator.
- `owner_membership`: the role is a member of the owner role (inheriting it or able to SET ROLE to it) and has all of its privileges. Harden reports it and never changes it; removing the membership is the administrator's decision. The connecting role is checked too unless it owns the sync tables, is a superuser or is kept; the report's `Connecting role` line says which. A member of the owner role that runs harden therefore always sees its own membership: when that is the only finding, the report says to run as the owner itself or keep the role.
- `superuser_membership`, or membership in `pg_execute_server_program`, `pg_read_server_files` or `pg_write_server_files`: the role can reach every table without a privilege on it. Harden reports it; an administrator removes the membership.
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

**Back up:** `mtix sync backup --output hub-<date>.sql` runs `pg_dump` for every table the hub migrations create, with its data, and prints the table list. It needs a `pg_dump` at least as new as the hub's server, on `PATH` or named by `MTIX_PG_DUMP`. It connects with the settings sync uses: `sslmode` is `verify-full` when the DSN names none; a weaker `sslmode` needs `--insecure-tls` and works only when every host is loopback or a local socket; the CA file comes from `sslrootcert` in the DSN or from `MTIX_SYNC_SSLROOTCERT`, and with neither `pg_dump` verifies the hub against the system trust store (the backup says so). Client certificates (`sslcert`, `sslkey`) are not passed to `pg_dump`, so a hub that requires one cannot be backed up with this command yet. mtix creates the output file readable and writable only by its owner (mode 0600) and never overwrites one: give every backup a new path. A failed backup, including one interrupted with Ctrl-C or SIGTERM, leaves no file.

**Restore into an empty database (the runbook):**
1. Restore the dump with `psql -f <file>`, connected to the empty database as the role that will own the sync tables. Put the connection in `PG*` variables (`PGHOST`, `PGPORT`, `PGUSER`, `PGDATABASE`, `PGSSLMODE=verify-full`, and `PGSSLROOTCERT=<ca.pem>` for the hub's CA file, or `system` for the operating system's trust store with libpq 16 or later) and the password in `~/.pgpass`, never on the command line. psql reports an error for each trigger: the dump holds no trigger functions yet.
2. Run `mtix sync init`, with the hub DSN naming that owner role. It recreates every mtix function and trigger and keeps the restored data, the restore epoch included.
3. Run `mtix sync doctor` and check `hub-triggers` (below).
4. Run `mtix sync mark-restored` exactly once, then `mtix sync collisions list`.

**Verify:** in `mtix sync doctor --json`, the `hub-triggers` check has `pass: true` and no `warn`: every function and trigger the hub migrations define exists, every trigger executes the function its migration binds (compared by OID, so a function of the right name in another schema counts as another function), and every trigger is enabled (`tgenabled` `O`, or `A` for one set to fire always; both count, as for `mtix sync harden`). Without `--json` the fix is printed on a `fix:` line under the check. A gap is a WARN (exit 0), or a FAIL (exit 2) in strict mode (`sync.keep_roles` set). Its `detail` names each missing function, missing trigger, trigger that executes another function and trigger that is not enabled, and its `fix` names the table owner who runs it (`as the table owner (<role>): ...`) and what to run, in order: `mtix sync init` for anything missing and for a trigger that executes another function (init replaces it in one transaction, so the table is never unguarded); the printed `ALTER TABLE <schema>.<table> ENABLE TRIGGER <name>;` statement for a trigger that is not enabled. Then run the doctor again.

**Troubleshooting:**
- `already exists`: the output path exists (a file or a symlink); choose a new path.
- `weak sslmode requires --insecure-tls`, or `not loopback or a local socket`: the DSN names an `sslmode` weaker than `verify-full`. Use `verify-full`; `--insecure-tls` is for a development hub on loopback or a local socket only.
- A certificate error after `system trust store (PGSSLROOTCERT=system)`: the hub's certificate comes from a private CA; set `sslrootcert=<ca.pem>` in the DSN or `MTIX_SYNC_SSLROOTCERT`.
- `server version mismatch` from `pg_dump`: install a `pg_dump` at least as new as the hub's server and point `MTIX_PG_DUMP` at it.

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
