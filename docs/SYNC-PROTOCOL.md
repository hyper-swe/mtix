# mtix Sync Protocol — for contributors and auditors

> Companion to [SYNC-DESIGN.md](SYNC-DESIGN.md) (architectural overview)
> and [SECURITY-MODEL.md](SECURITY-MODEL.md) (trust contract). Read those
> first if you are new to the sync layer.

This document is the protocol-level specification of the FR-18 BYO
Postgres sync hub. It is aimed at people reading
`internal/store/postgres/transport/*.go`,
`internal/store/sqlite/sync_*.go`, or `internal/sync/validator/`. If you
are a USER trying to sync your team's projects, read
[USERMANUAL.md](../USERMANUAL.md) → Team collaboration with sync.

## Event model

Every mutation to the local SQLite store emits a `sync_event` row.
Events are append-only on both the local store and the hub.

### Schema (high level)

```
sync_events
  event_id              TEXT PRIMARY KEY      -- UUID v7
  project_prefix        TEXT                  -- e.g. "MTIX"
  node_id               TEXT                  -- e.g. "MTIX-15.11"
  op_type               TEXT                  -- one of 12 values, see below
  payload               JSONB                 -- op-specific shape
  wall_clock_ts         BIGINT                -- Unix millis (UTC)
  lamport_clock         BIGINT                -- monotonic per CLI
  vector_clock          JSONB                 -- {authorID: int64, ...}
  author_id             TEXT                  -- logical actor; defaults to "cli"
  author_machine_hash   TEXT                  -- 16-char hex per machine
  sync_status           TEXT                  -- pending | pushed (local only)
  created_at            TIMESTAMPTZ           -- when emitted
```

### `op_type` (12 values)

| op_type           | Triggering CLI command                       |
|-------------------|----------------------------------------------|
| `create_node`     | `mtix create`                                |
| `update_field`    | `mtix update --title/--description/...`      |
| `set_acceptance`  | `mtix update --acceptance`                   |
| `set_prompt`      | `mtix prompt`                                |
| `transition`      | `mtix done` / `defer` / `reopen` / etc.      |
| `claim`           | `mtix claim`                                 |
| `unclaim`         | `mtix unclaim`                               |
| `cancel`          | `mtix cancel`                                |
| `delete`          | `mtix delete`                                |
| `undelete`        | `mtix undelete`                              |
| `link_dep`        | `mtix dep add`                               |
| `unlink_dep`      | `mtix dep remove`                            |
| `annotate`        | `mtix annotate` / `mtix comment`             |

Each payload is validated against an op-specific schema by the
pre-flight validator (`internal/sync/validator`) before any PG
round-trip. Validator caps (FR-18.7):

- `MaxPayloadBytes` = 64 KiB
- `MaxPayloadNestingDepth` = 16
- `MaxLamportClock` = 2^53 (strict <)
- `MaxVectorClockEntries` = 100
- `MaxVectorClockValue` = 2^53 (strict <)
- `FutureTimestampGrace` = 5 minutes (events stamped further in the
  future are rejected as clock skew)
- `PastTimestampWarn` = 90 days (events older than this are still
  accepted but produce a stale-timestamp warning)

## ID generation

- `event_id` is UUID v7 (`internal/sync/clock.NewEventID`). Time-ordered
  and PK-friendly. Conflict-free across replicas by construction (random
  tail).
- `author_machine_hash` is a 16-char hex digest derived from
  `os.Hostname()` plus a stable per-machine salt
  (`internal/sync/clock.MachineHash`). Stable across CLI restarts on the
  same machine.

## Clock advancement (per CLI)

On emit:

1. `bumpLamport` reads `meta.sync.lamport`, increments, persists, returns
   the new value as the event's `lamport_clock`.
2. `bumpAndPersistVectorClock` reads `meta.sync.vector_clock`, calls
   `VectorClock.Bump(authorID)`, validates against the FR-18.7 caps,
   persists, returns the new VC.

On apply (incoming events from pull):

1. `advanceLamport` writes `meta.sync.lamport = max(current, incoming)`.
2. `mergeVectorClock` takes the per-author max of the local VC and the
   incoming VC.

The combination guarantees that locally-emitted events always have a
lamport higher than any previously-applied event from the same author —
the bedrock of causal ordering.

## LWW resolution (apply time)

When `IdempotentApply` encounters an event for `(node_id, field_key)`
that already has a prior event:

```
prior = highest-lamport event for (node_id, field_key) in sync_events
incoming wins iff (lamport, wall_clock_ts, author_machine_hash) >LEX prior
                  with author_machine_hash ascending (lower wins)
```

The comparator is total — there is always exactly one winner. Apply-time
LWW is the load-bearing convergence mechanism; the hub-side
`sync_conflicts` table is a best-effort audit log, NOT the resolution
authority. See [SECURITY-MODEL.md → Known audit-trail
limitation](SECURITY-MODEL.md#known-audit-trail-limitation-same-authorid-conflicts)
for the same-authorID tradeoff.

`field_key` is the LWW grouping key (`fieldKeyForLWW` in `sync_apply.go`):

- `update_field:<field_name>` for `update_field` events
- `set_acceptance:acceptance` for `set_acceptance`
- `set_prompt:prompt` for `set_prompt`
- `""` for ops not eligible for LWW (claim, transition, delete, etc.)

Non-LWW ops have single-row outcomes and don't need a per-field tiebreaker.

## Hub-side conflict detection

`detectConflicts` in `internal/store/postgres/transport/push_pull.go`
runs INSIDE the push transaction:

```
for each incoming event e of op_type update_field / set_acceptance / set_prompt:
  prior_events = SELECT event_id, vector_clock FROM sync_events
                 WHERE node_id = e.node_id
                   AND op_type IN (update_field, set_acceptance, set_prompt)
                   AND event_id != e.event_id
  for each prior:
    if e.vector_clock.Concurrent(prior.vector_clock):
       INSERT INTO sync_conflicts (event_id_a, event_id_b, node_id, field_name, resolution='lww')
```

`VectorClock.Concurrent` is `!a.Dominates(b) && !b.Dominates(a) && !a.Equal(b)`.

The hub also INSERTs into `sync_events` itself in the same transaction
via `ON CONFLICT (event_id) DO NOTHING`, so duplicate pushes are no-ops.

## Divergence detection

First-event-hash:

- `meta.sync.project_prefix` is the prefix of the local project.
- `meta.sync.first_event_hash` is the SHA-256 of the canonical
  marshaled representation of the lowest-lamport event ever emitted
  on this CLI.
- `sync_projects (project_prefix, first_event_hash)` on the hub
  records the canonical first-event-hash for each project.

`DetectDivergentHistory(localPrefix, localHash, hubPrefix, hubHash)`:

```
if localPrefix == "" or hubPrefix == "" or hubHash == "":
    return nil (no divergence — fresh hub or no local events)
if localPrefix != hubPrefix:
    return nil (different projects, not a divergence)
if localHash == hubHash:
    return nil (same first event — same lineage)
return ErrSyncDivergentHistory
```

`mtix sync init` runs this check before pushing. If a local project has
emitted events under prefix `PROJ` and the hub already has a different
first_event_hash for `PROJ`, init refuses and points the operator at
`mtix sync reconcile --import-as PARENT-ID` or `--rename-to`.

## Idempotent apply

`applied_events.event_id` is the dedupe key. On apply:

```
if event_id already in applied_events:
    return nil (already applied, no-op)
dispatch to applyCreateNode / applyUpdateField / ... per op_type
advance lamport, merge VC, INSERT into applied_events
```

Replay of any pushed event is a no-op. The same event may flow through
multiple CLIs (push → hub → pull on another CLI → re-pull on the
originator) and the originator's `applied_events` row blocks re-apply.

## Migration single-flight

Schema migration on the hub is gated by
`pg_advisory_xact_lock(AdvisoryLockKey)` where `AdvisoryLockKey` is the
hash of the constant string `"mtix_sync_migration"` (FR-18.14). All CLIs
hash the same string to the same lock key.

10 concurrent CLIs all calling `Migrate()` will serialize on this lock;
only one runs the DDL, the others see the schema is current and return
cleanly. See `internal/store/postgres/transport/migrate.go` and
`TestMigrate_ConcurrentSingleFlight`.

## Transport security

- TLS posture is enforced in `EnforceTLSPosture` (`transport/dsn.go`).
  Default sslmode is `verify-full`; weaker modes refused unless
  `--insecure-tls` is set AND the host is loopback.
- `MTIX_SYNC_SSLROOTCERT` populates `sslrootcert` for managed-PG
  providers that require a CA bundle.
- DSN sourcing (`Source()`) order: `MTIX_SYNC_DSN` env → `.mtix/secrets`
  (mode 0600 enforced). `Source()` refuses to load if any tracked config
  file under `.mtix/` mentions a DSN-shaped key — fail-closed at the
  earliest detectable misconfiguration.
- Every error path passes through `redact.DSN`. `cmd/mtix/main.go`
  wraps with `defer redact.Recover` so panics with a DSN in scope are
  scrubbed before the runtime printer.
- Connection pool: `MaxConns = 5` per FR-18 / MTIX-15.10 (10 active
  developers × 5 conns ≤ 50, well within managed-PG defaults).

## Queue management

- `sync.max_queue_size` in `meta` caps the pending queue per CLI.
  Default `0` (unlimited). On overflow, `enforceQueueLimit` returns
  `model.ErrSyncQueueFull` from the emit path; the new event is
  rejected, NOT silently dropped.
- `pushBatchSize` defaults to 100 events per batch in
  `cmd/mtix/sync_push.go`. `pullBatchSize` defaults to 1000 in
  `cmd/mtix/sync_pull.go` (pull is read-only on the hub so larger
  batches are safe).

## Push lock (single-flight per CLI)

`internal/sync/pushlock` is a filesystem advisory lock at
`.mtix/.pushlock`. Two concurrent `mtix sync push` invocations on the
same project: the second returns `pushlock.ErrLockHeld` and exits 0.
This prevents two processes on the same machine from racing on the
sync_events queue.

The daemon and an interactive `mtix sync push` cannot conflict because
the daemon holds the lock for the duration of its push pass.

## Daemon

`mtix sync daemon` runs `runOneDaemonPull` on a fixed interval
(default 30s). It does NOT push by itself — pushes happen via the
normal `mtix sync push` invocation (either manual or via the pre-push
hook).

The daemon writes a PID file at `.mtix/.daemonpid` (mode 0600). Liveness
is checked via `syscall0()` (`syscall.Kill(pid, 0)` on Unix, SIGINT on
Windows as best-effort). Stale PIDs are auto-cleaned.

## State machine (sync_status, local only)

```
pending  -- on emit
   |
   v  push succeeds (event_id accepted by hub)
pushed   -- terminal
```

Pull operations do NOT touch `sync_status` on the local row; they INSERT
into `applied_events` to dedupe future re-pulls. Events that the local
CLI itself emitted go pending → pushed; events received from other
CLIs are recorded in `applied_events` and the corresponding mutation is
applied to the canonical tables.

## Backup

`mtix sync backup --output FILE` wraps `pg_dump` for the 5 mtix-owned
tables:

```
--table=sync_events
--table=sync_conflicts
--table=sync_projects
--table=applied_events
--table=audit_log
--no-owner --no-privileges
```

Restore is `psql "$DSN" < FILE`. The append-only triggers permit INSERT,
so the restore replays cleanly.

## See also

- [SYNC-DESIGN.md](SYNC-DESIGN.md) — architectural overview, FR-18
  requirements, decision log.
- [SECURITY-MODEL.md](SECURITY-MODEL.md) — trust boundary, threat model,
  known limitations.
- [docs/audit/MTIX-15-audit-pass2.md](audit/MTIX-15-audit-pass2.md) —
  security audit evidence table with file:line references to every
  test that proves a requirement.
- Source landmarks:
  - `internal/sync/validator/validator.go` — pre-flight validation
  - `internal/store/sqlite/sync_emit.go` — event emission
  - `internal/store/sqlite/sync_apply.go` — idempotent apply + LWW
  - `internal/store/postgres/transport/push_pull.go` — wire push/pull
  - `internal/store/postgres/transport/migrate.go` — single-flight
    migration
  - `internal/sync/redact/redact.go` — DSN scrubber + panic Recover
