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
mtix sync repair --status          # List nodes an older pull left reverted (dry run)
mtix sync repair --status --apply  # Back up, then repair them (a human's go-ahead first)
mtix sync push                     # Send the repair events
```
