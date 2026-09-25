---
name: admin
description: Administrative operations for mtix projects. Backup, export, import, verification, and statistics.
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

## Documentation

```bash
mtix docs generate      # Regenerate agent documentation
mtix plugin install     # Install IDE skill files
```

## Sync Recovery

```bash
mtix sync pull                             # First, and again just before --apply: a repair on a stale log can revert a teammate's newer change everywhere
mtix sync repair --status                  # List nodes an older pull left reverted (dry run)
mtix sync repair --status --apply          # Back up, then repair all but flagged nodes (a human's go-ahead first)
mtix sync repair --status --apply --force  # Also repair flagged nodes, after a human reviews each one
mtix sync push                             # Send the repair events
```

Quarantined pulled events (MTIX-95.11): `mtix sync pull` checks every pulled event (its Lamport clock, below 2^53 and at most `sync.max_lamport_jump` above the local clock, default 4294967296; then the FR-18.7 size, shape and id rules) and holds one that fails, or whose apply fails, in a local quarantine instead of applying it; every pull retries the quarantine.

```bash
mtix sync doctor                           # "quarantined events" fails while any are held
mtix sync quarantine list                  # Read-only: event id, node, op, attempts, first seen, last attempt, reason (--json)
mtix sync pull                             # Retries them; one waiting for a task or dependency target applies once it arrives
```

`mtix sync clone` refuses, naming the event, and writes nothing while the hub holds an event that fails these checks. On the fresh store, recover with `mtix sync pull` alone, which quarantines that event and applies the rest. `mtix sync reconcile --discard-local --yes` is only for a store that already holds sync state, and it deletes local tasks and unpushed changes (its dry run shows only the node count): before `--yes`, run `mtix sync push`, confirm `mtix sync status` shows pending 0, and get a human's go-ahead. A quarantined copy of this replica's own event does not clear by itself: the hub row is repaired first, then push, then (with a human's go-ahead) discard-local `--yes` and pull. Never delete quarantine rows or edit the local database, never raise `sync.max_lamport_jump` (`mtix config set sync.max_lamport_jump <positive integer>`) without a human's confirmation, and never get past a refused clone any other way; show the human the listed reasons for an event that stays quarantined.
