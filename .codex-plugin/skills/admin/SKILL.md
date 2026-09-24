---
name: admin
description: Administrative operations for mtix projects. Backup, export, import, verification, statistics, and sync hub privileges.
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
mtix config set sync.keep_roles <r>[,<r>] # Record the kept roles (harden prints this command; it never writes config)
mtix sync harden --json                   # Report for agents: before/after findings, statements, keep_roles_hint
```

Exit code 0 means verification passed (only the owner and the kept roles can
use the sync tables, and every TRUNCATE guard is in place); 2 means changes
are pending or access remains; 1 means an error or a refusal. A role that is
not the table owner, a member of it or a superuser is refused and nothing
changes. Members of a kept role keep their access through it; superusers are
out of scope. A server WARNING fails the run and nothing changes. Access
harden may not change is printed with the statement a database administrator
runs. EXECUTE on the mtix trigger functions is information only, so a freshly
migrated hub verifies clean.

Never pass `--apply` without a human approving the dry run's role list. Never
run `mtix sync harden` from a hook, a push or the daemon.

## Documentation

```bash
mtix docs generate      # Regenerate agent documentation
mtix plugin install     # Install IDE skill files
```
