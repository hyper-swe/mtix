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

Held push events (MTIX-95.12): the sync hub accepts an event payload of at most 64 KB, while local fields can be larger. With a hub configured, a CLI change over the limit still succeeds and prints `WARN: <id>: the <field> field makes this <op> sync event <n> bytes, over the 65536-byte sync limit ...` (for MCP, an extra text block of the result); web UI, REST and gRPC changes print no warning, and imports write no sync events. `mtix sync push` holds such an event (it stays pending, recorded in the local quarantine with source `push`, never sent) and pushes the rest. The reason starts with its kind: `too large:`/`refused:` (permanent), `temporary: clock:` (stamped more than 24h ahead; every push re-checks it and releases it once it passes), or `depends on held create of <event id> (<task>)` (while a task's creation is held, none of the changes of its subtree are sent: push holds every change of that task and of the tasks below it, including changes made after the last push, finding the task by its internal id, so a change made before a renumber is still held and a change of a task that later takes an old number is not; after `mtix gc` purges a deleted task, its held changes stay held and others are checked by the number they name; the reason names the nearest held creation). A dependency link or unlink names its target by number only, so while any creation is held, every link or unlink made after it is held (reason ending `links made while a task creation is held wait for it; ...`, naming the earliest held creation) until no creation made before it is held. Once a clock hold on a creation clears, the creation and the changes of its subtree go through the ordinary push in the same run, where a renumber by the hub is a known limit of this version: the changes sent with a renumbered creation keep the old number. Push checks each of those changes first; one the hub would refuse stays held under its own reason, and a child creation among them keeps its own subtree held. A creation held permanently keeps its whole subtree held.

```bash
mtix sync doctor                           # "held push events" fails while any are held; lists "<node> <op>: <fix> (reason: ...)"
mtix sync status                           # "held push events" (held_push_events in --json); held events stay in pending
mtix sync quarantine list --json           # Read-only; held push events have "source": "push"
```

Follow doctor's fix per event: a field edit over the limit, shorten or split the field and save it again (the new event pushes); a held task creation, stop editing the task and show the human (no automatic re-send in this release); a dependent resolves with the held creation it depends on, and a held link pushes once no creation made before it is held (do not re-create it); a clock hold, check this machine's clock (the event pushes once its stamp is within 24 h of the clock, and a creation's subtree pushes with it). Permanent holds and their dependents stay listed (no command in this release releases or discards one). Never delete quarantine rows, and never use `mtix sync reconcile --discard-local --yes` to clear held events.
