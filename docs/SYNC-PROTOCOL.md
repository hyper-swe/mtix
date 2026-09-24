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
- `""` for every other op

Workflow events have their own last-writer-wins rule, described next.
The remaining ops have their own semantics: delete is monotonic,
comments are append-only and dependency edges are idempotent.

### Workflow events: last-writer-wins at ingest

`claim`, `unclaim`, `transition_status` and `defer` all write one piece
of per-node state, the node's workflow state (status, and the assignee,
agent state, `closed_at` and related columns that go with it). They are
resolved per node, not per field:

```
key(e)  = (lamport_clock, event_id)
held    = the workflow event with the highest key in the local
          sync_events log for the same node (own events and mirrored
          foreign events, winners and losers), excluding e
e wins  iff there is no held event, or key(e) > key(held):
          the higher lamport_clock wins;
          on a tie, the higher event_id wins (byte-string compare)
```

The node is matched by `uid` when the event carries one, else by
`node_id`, exactly as field LWW scopes its history. Event ids are
unique, so the order is total: every replica that holds the same
workflow events for a node ends on the same winner, whatever order
they arrived in. This covers every way an old workflow event can reach
a replica after a newer one: a replayed own event (even one the
own-event rule below cannot recognize, such as after restoring the
local store from a backup), a late or out-of-order delivery, and a
teammate's concurrent claim or status change.

- **A winning event** writes exactly the columns its local mutation
  writes, with values taken from the event. The single table of those
  columns is `workflowWinnerTable` in
  `internal/store/sqlite/sync_workflow_winner.go`. `closed_at` is
  written by every row: for a terminal status (`done`, `cancelled`,
  `invalidated`) it is the winning event's `wall_clock_ts` as RFC 3339
  in whole seconds (milliseconds truncated), never the apply time;
  for any other status it is cleared.
- **A losing event** is still mirrored into `sync_events`, its clocks
  are merged and it is recorded in `applied_events`, so a re-pull is a
  no-op. It changes no node column and writes **no** `sync_conflicts`
  row: workflow state is not a user-authored field, and a conflict row
  for every lost claim race or late replay would be noise.
- **Local mutations** need no check. Their Lamport clock is above
  every event the replica holds, so they are the winner when they are
  written.

Known residual in 0.5.x:

- Columns other than status and `closed_at` (assignee, agent state,
  `previous_status`, progress, `defer_until`) are written only by the
  events that write them locally. When events arrive out of order, an
  event that won when it arrived may leave such a column set, and the
  final winner may not overwrite it. Example: a claim applies, then a
  `done` with a higher key arrives; the assignee stays set. A replica
  that received the `done` first rejects the claim and keeps the
  assignee it had. Likewise, after a claim race in which the agent
  whose claim lost marks the node done before pulling, both replicas
  show `done`, each with its own agent as assignee. Status converges
  on every replica for claims and status changes that travel as events
  (local writes that emit none are covered below); `closed_at`
  converges among replicas that received the events by sync (the
  originator exceptions follow).
- `defer_until` is not cleared by a winning claim, unclaim or
  transition at ingest, although a local claim clears it.
- `update_field` on `status`, `assignee` or `agent_state` keeps its
  per-field register above and is not compared with workflow events.
- `closed_at` on the originating store can differ from the replicas':
  - it stamps its own clock when the mutation runs, while replicas
    stamp the event's `wall_clock_ts`; the two are read separately, so
    they can differ within that second;
  - an invalidation leaves its `closed_at` as it was (NULL for an open
    node), while replicas stamp it (`invalidated` is terminal here);
  - a restore from `invalidated` keeps its `closed_at`, while replicas
    clear it.
- `closed_at` range: RFC 3339 has four-digit years and envelope
  validation rejects only a negative `wall_clock_ts`. When the
  winner's `wall_clock_ts` is outside years 1 to 9999, `closed_at`
  falls back to the apply time on that replica, so the node stays
  readable but its `closed_at` differs from the other replicas'.
- Local writes that emit no event (an auto-block when a dependency is
  added, the descendants of a cascade cancel) are invisible to the
  winner check, so status can still differ there. Example: replica A
  claims a node while replica B adds a dependency that blocks it; B's
  auto-block emits no event, so after both pull A shows `blocked` and
  B `in_progress`. Descendants cancelled by a cascade cancel can differ
  the same way: other replicas keep them in their previous status. A
  dependent that the cascade unblocked does travel, as its own
  `transition_status`, so other replicas show it unblocked while the
  descendant that blocked it is not cancelled there (MTIX-95.21).
- A `transition_status` to a status this build does not know (for
  example one added by a newer client) writes the status column (and
  `updated_at`) alone and logs a warning naming the event and the
  status. A `transition_status` whose payload cannot be decoded or has
  no to-status (missing, null or empty) changes no node column, is
  recorded as applied, and logs a warning naming the event. Neither
  fails the event: a failed event fails its whole pull batch, and the
  pull cursor never moves past it.

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

A replica applies each event at most once. `event_id` is the dedupe
key, checked against two local tables. On apply:

```
if event_id already in applied_events:
    return nil (already applied, no-op)
if event_id already in local sync_events (any sync_status):
    # own-event rule: this replica already holds the event
    advance lamport, merge VC, INSERT OR IGNORE into applied_events
    return nil (no dispatch, no conflict row, no new sync_events row)
mirror into sync_events (sync_status = 'applied')
if op_type is a workflow op and the event loses its node's workflow
    state (see "Workflow events" above):
    skip the dispatch (no node column, no conflict row)
else dispatch to applyCreateNode / applyUpdateField / ... per op_type
    (with LWW resolution and conflict logging for field ops)
advance lamport, merge VC, INSERT into applied_events
```

**Own-event rule.** A pull returns every hub event past the cursor,
including the events this CLI pushed itself. A locally emitted event
never passes through apply when it is emitted, so it is not in
`applied_events` when it first comes back; it is already in the local
`sync_events` log (`pending`, `pushed` or `conflicted`). Apply therefore
treats any event whose `event_id` is already in `sync_events` as held:
it merges the event's clocks and records it in `applied_events`, but
never applies it again. Applying it again would log a spurious LWW
conflict for every field update whose field has any other event in the
local log, earlier or newer (the same pair twice when the field was
written twice), and a replayed
`claim`, `unclaim`, `defer` or `transition_status`, which applied
unconditionally before workflow events were resolved by
last-writer-wins, would overwrite newer local state: claim, push, done,
pull would leave the node `in_progress` with `closed_at` still set.
The workflow rule above now rejects such a replay on its own as well,
so a replayed own workflow event that the own-event rule misses (for
example after the local store was restored from a backup) still
cannot revert newer local state.

Events from other CLIs are not in the local log until apply mirrors
them, so they keep the LWW resolution and conflict logging described in
[LWW resolution](#lww-resolution-apply-time), unchanged.

Replay of any event is a no-op. The same event may flow through
multiple CLIs (push → hub → pull on another CLI → re-pull on the
originator). A re-pull of a foreign event stops at its `applied_events`
row. The originator's own event stops at its `sync_events` row the
first time it returns, and at its `applied_events` row after that.
Earlier revisions of this document said the originator's
`applied_events` row alone blocked the re-apply; it did not, because
an emitted event has no such row until a pull records one.

Hooks are unaffected. The own-event rule adds no `sync_events` row, so
hook dispatch and inbox delivery still see each event exactly once. The
hook journal's `Synced` flag keeps its semantics: it is still derived
from `applied_events`, and an own event returned by a pull is recorded
there, as it was before this rule.

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
