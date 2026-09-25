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

0. Before the apply, pull refuses an event stamped more than
   `sync.max_lamport_jump` above `meta.sync.lamport` (default 2^32) and
   quarantines it (see [Quarantine](#quarantine)), so no single event
   can carry the clock toward the FR-18.7 overflow guard.
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
held    = the well-formed workflow event with the highest key in the
          local sync_events log for the same node (own events and
          mirrored foreign events, winners and losers), excluding e
          and every malformed event (see the residual list below)
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

- Columns other than status, `closed_at` and `defer_until` (assignee,
  agent state, `previous_status`, progress) are written only by the
  events that write them locally. When events arrive out of order, an
  event that won when it arrived may leave such a column set, and the
  final winner may not overwrite it. Example: a claim applies, then a
  `done` with a higher key arrives; the assignee stays set. A replica
  that received the `done` first rejects the claim and keeps the
  assignee it had. Likewise, after a claim race in which the agent
  whose claim lost marks the node done before pulling, both replicas
  show `done`, each with its own agent as assignee. Status converges
  on every replica for claims and status changes that travel as events
  (see the item on local writes that emit no event), also when a
  malformed event is present (see the item on malformed workflow
  events). `closed_at` converges among replicas that received the
  events by sync, except as the item on the `closed_at` range
  describes; the originating store can differ (see the item on
  `closed_at` on the originating store).
- `defer_until` (the wake time of `mtix defer --until`) is cleared by
  every winning claim, unclaim and `transition_status`, including a
  transition into `deferred`, and only a `defer` event sets it. No 0.5.x
  client emits a `defer` event: a local defer emits `transition_status`,
  so the hub never carries the wake time. A `defer` event whose wake time
  falls outside UTC years 1 to 9999 stores none.
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
  B `in_progress`. A dependent that a cascade cancel unblocks does
  travel, as its own `transition_status`, so other replicas show it
  unblocked while the descendant that blocked it is not cancelled
  there (MTIX-95.21). Descendants cancelled by a cascade cancel, which
  other replicas keep in their previous status, can differ
  the same way.
- Unknown to-status: a `transition_status` to a status this build
  does not know (for example one added by a newer client) writes the
  status column (and `updated_at`) alone and logs a warning naming the
  event and the status. It does not fail the event: a failed event
  fails its whole pull batch, and the pull cursor never moves past it.
- Malformed workflow events: a `transition_status` whose payload
  cannot be decoded or has no to-status (missing, null or empty), and
  a `claim` or `defer` whose payload cannot be decoded (including an
  `until` that is not an RFC 3339 timestamp), are malformed. One Go
  rule decides this (`decodeWorkflowPayload` in
  `internal/store/sqlite/sync_workflow_payload.go`), and both the
  apply path and the winner check use it. A malformed event changes no
  node column, is recorded as applied, and never fails the event, so
  it does not stop the pull. It never counts as the held event, so it
  does not block an older workflow event, and the order in which a
  replica receives it does not change the status it ends on. When it
  beats every well-formed held event, apply logs a warning naming it.
  `unclaim` is never malformed: its payload is not read. In 0.5.x a
  malformed event is recorded as applied, not quarantined for retry,
  so a later build that could read it does not apply it either.
  Upgrade consequence: the winner check re-decodes stored payloads
  with the running build's rule. If a later build widens the rule, an
  event this build recorded as malformed (and never applied) would
  count as held there although its columns were never written, and it
  could block older events. Any widening of `decodeWorkflowPayload`
  must therefore ship with a step that re-applies or quarantines the
  events the old rule rejected.

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

## Pull

`mtix sync pull` (and every pull the daemon runs) has two passes.

**Cursor pass.** The pull cursor is a keyset position, the tuple
`(lamport_clock, event_id)` of the last event pulled, kept in
`meta.sync.last_pulled_clock` and `meta.sync.last_pulled_event_id`
(MTIX-95.4). `PullEvents` returns the hub events after it, in that
order, in batches of `--limit` (default 1000):

```
WHERE (lamport_clock, event_id) > ($1, $2)
ORDER BY lamport_clock, event_id
LIMIT $3
```

Each next batch starts after the previous batch's last event. The event
id breaks Lamport ties: Lamport clocks are not unique, and before 0.5.4
the cursor was the batch's highest clock alone, so events that shared
it but did not fit in the batch were never pulled. Each batch is applied
in one local transaction, each event checked first and then applied
through `IdempotentApply` (see [Idempotent apply](#idempotent-apply)) in
its own savepoint; an event that fails is quarantined and the batch goes
on (see [Quarantine](#quarantine)). The cursor is saved in that same
transaction, at the batch's last event, quarantined events included,
except at a clock at or above 2^53 or beyond `sync.max_lamport_jump`,
decided from the clock itself. The batch's applies, its quarantine rows
and the cursor commit together or not at all, so a crash cannot leave
applied events behind an older cursor, or a cursor past events that
were neither applied nor quarantined.

**Upgrading from a Lamport-only cursor.** A store written by an earlier
version has only `meta.sync.last_pulled_clock`. When this version opens
the store it adds `meta.sync.last_pulled_event_id` and
`meta.sync.clone.checkpoint_event_id`, empty (`INSERT OR IGNORE`, no
schema version change). An empty event id sorts before every event id,
so the first pull reads the events at exactly the saved clock again:
`IdempotentApply` dedupes the ones already applied, and any that the
old paging skipped at that clock are applied. Events it skipped at
earlier clocks are recovered by the late-event sweep's one-time
comparison of the full hub history (below).

**Clone.** `mtix sync clone` pages the hub log by the same keyset. After
each batch it saves its `--resume` checkpoint, both halves, in
`meta.sync.clone.checkpoint` and `meta.sync.clone.checkpoint_event_id`
(a checkpoint without an event id resumes at its clock, idempotently).
When it completes it saves the pull cursor at the last event it cloned,
so the first pull after a clone asks the hub only for newer events.
Before 0.5.4 clone left the pull cursor at 0 and that first pull read
the whole hub log again. `mtix sync reconcile --discard-local` resets
both halves of the pull cursor.

**Hub index.** Hub migration 015 adds `idx_sync_events_lamport_event_id`
on `sync_events (lamport_clock, event_id)`, an additive
`CREATE INDEX IF NOT EXISTS`, so each page is read from the cursor on,
in index order.
`TestPullEvents_PlanUsesKeysetIndex_NoSeqScanNoSort` checks that the
planner can serve the query from it. Migrations run in `mtix sync init`
with the hub owner's DSN, so an existing hub gets the index the next
time its owner runs `mtix sync init`. Every migration runs in one
transaction (see [Migration single-flight](#migration-single-flight)),
so the index is built inside it, not concurrently, and `sync_events`
stays locked until that transaction commits: pushes and pulls both wait
for `mtix sync init` to finish. Run it when a pause in sync is
acceptable. Until the index exists, pulls return the same events, but
each page scans and sorts `sync_events`.

The Lamport clock is stamped by the client that wrote the event, not by
the hub. A teammate who worked offline pushes events stamped below the
cursors of teammates who kept working, so the cursor pass never returns
them.

**Straddling pushes.** A late push can straddle the cursor: a node's
create stamped below it and an edit of that node above it (one offline
client made more events than the cursor gap, or a teammate who received
the node edited it). The cursor pass then returns the edit without its
create, and the edit's apply fails because the node is missing
(`model.ErrNotFound`). The edit is quarantined and the cursor moves past
it. The late-event sweep below delivers the create in Lamport order, and
the quarantine retry at the end of the same pull applies the edit
(stderr says `quarantined event <id>`, then `N quarantined events
applied on retry`). Before quarantine (MTIX-95.11) the failed batch
rolled back and pull retried the cursor pass once after the sweep.
Together with the sweep's Lamport order, this is how a node's create
always applies before its edits.

**Late-event sweep** (`cmd/mtix/sync_pull_sweep.go`,
`cmd/mtix/sync_pull_sweep_apply.go`, `transport/late_events.go`). After
the cursor pass, pull runs two phases.

1. **Listing.** It lists hub event ids in `(created_at, event_id)` keyset
   pages of `--limit`. Normally that is the ids created since
   `meta.sync.last_sweep_at` minus a 15-minute overlap. When
   `meta.sync.last_sweep_at` is empty (the first pull after upgrading,
   after `mtix sync clone`, or after `mtix sync reconcile
   --discard-local`), it lists the full id history instead, once, from
   the zero position. It diffs each page against the ids this store holds
   in `sync_events` (its own events and every mirrored one) or
   `applied_events`, or has quarantined (`sync_quarantine`; the
   quarantine retries those itself), and stages the missing ids, each
   with its Lamport
   clock (the listing returns it), in the local table
   `sync_sweep_pending`. It applies nothing: the listing order is not
   causal. One client's push gives all its events the same `created_at`,
   so their order is event-id order, and an edit can carry a smaller
   event id than its node's create (after a clock step-back on that
   client); a create that had to be renumbered is pushed after edits it
   precedes.
2. **Apply**, once the listing is complete. It reads the staged ids in
   pull order (Lamport clock, then event id), `--limit` at a time; for
   each chunk it fetches the events by id and applies them through the
   same checks and `IdempotentApply` path in one transaction, removing
   each id from `sync_sweep_pending` in that transaction. An event that
   fails is quarantined with source `sweep` and removed from
   `sync_sweep_pending` in the same transaction. Every chunk's clocks are at
   or above the previous chunk's, so the order is global while memory and
   each transaction stay bounded by `--limit`, and a pull stopped between
   chunks resumes at the next one. Lamport order is causal: an event is always
   stamped above every event its client had applied when it was made, so
   a node's create applies before any edit of that node. Recovered events
   are late and low-Lamport by construction, so a recovered `claim`,
   `unclaim`, `defer` or `transition_status` goes through the workflow
   winner rule (see
   [Workflow events](#workflow-events-last-writer-wins-at-ingest)): one
   older than the node's newest held workflow event is recorded as
   received and changes nothing. A staged id this store now holds, or
   that the hub no longer has, is removed without an apply.
3. **Finish.** When nothing is left staged, it records the hub time read
   before its first page in `meta.sync.last_sweep_at` (RFC 3339, UTC)
   and clears the listing progress. If a concurrent pull has staged ids
   meanwhile, it records nothing and reports no error; a later pull
   applies those ids and records the sweep.

**Resumable.** On a large hub the one-time full diff can take longer
than one pull may run (a daemon pull has a 60-second deadline), and so
can a window after a long absence. With each page's staged ids, in the
same local transaction, the listing saves its position in
`meta.sync.sweep_after_id` and `meta.sync.sweep_after_created_at` (the
last listed id) and the hub time read before its first page in
`meta.sync.sweep_started_at` (RFC 3339 UTC). A last page with nothing
to stage writes nothing. A pull that stops in either phase keeps its
progress, its staged ids and the old `meta.sync.last_sweep_at`. The
next pull resumes listing after the saved position with the saved start
time (stderr says `late-event sweep: resuming the full hub event
comparison after <id>`, or `late-event sweep: resuming after <id>` for
a window), then applies every staged id, including those an earlier
pull left, still in Lamport order because the lower clocks were applied
first. Events created while a sweep was interrupted are covered by the
next window, which starts 15 minutes before that start time. A saved
time that is not an RFC 3339 time is reported and the listing restarts
from its own start. `mtix sync reconcile --discard-local` and `mtix sync
clone` clear the progress keys, the staged ids and
`meta.sync.last_sweep_at`.

The sweep does not move the pull cursor. After a full-history diff
pull prints `late-event sweep (first run, full hub history): N late
events recovered`, even when N is 0; after a windowed sweep it prints
`late-event sweep: N late events recovered` only when N > 0; N counts
the events this pull recovered. `mtix sync status` shows `last sweep`
(`last_sweep_at` in `--json`; empty or `never` before the first sweep
completes). While the full diff is part-way done (saved listing
progress or staged ids), the table shows `never (full hub comparison in
progress)` and `--json` sets `full_sweep_in_progress` to true;
`sweep_pending_events` in `--json` counts the staged ids. A value that is not an RFC 3339 time
is reported on stderr and replaced by a full-history diff. A
full-history diff also reports on stderr how many hub ids this pull
compared in how many pages (`late-event sweep: compared N hub event ids
in P pages`).

**Hub clock only.** `created_at` is `DEFAULT now()` on the hub, the
start time of the push transaction. Each listing statement also returns
the hub's `now()`, and the window and `meta.sync.last_sweep_at` are
computed from that value alone, never from this machine's clock, so a
skewed client clock changes nothing. Because `created_at` is the
transaction's start time, a push that started before a sweep can
commit after it with an earlier `created_at`. The 15-minute overlap
covers push transactions of normal length, which take seconds. It is
not a bound: the 10-second `statement_timeout` limits each statement,
not the push transaction, so a stalled push transaction can stay open
longer than 15 minutes, and an event it then commits is missed by later
windowed sweeps. Bounding the push transaction is a separate follow-up;
the complete fix is a hub-assigned event order in a later protocol
version.

**Cost.** In the common case, where nothing is missing and the window
holds at most `--limit` ids, the sweep adds one hub query to each pull:
the id listing, which also returns the hub clock. A larger window adds
one listing query per further `--limit` ids, and late events add one
fetch per `--limit` of them. The quarantine retry reads and writes only
the local store and adds no hub query. The first sweep on a store pages through
the full id history once, across as many pulls as it needs. The sweep
runs only inside a pull and has no timer, so an idle hub that scales to
zero stays idle; a daemon that pulls on an interval runs the sweep on
each of its pulls. Hub migration 014 adds `idx_sync_events_created_at`
(an additive `CREATE INDEX IF NOT EXISTS`) so each listing page reads
only its own rows. Migrations run in `mtix sync init` and need the hub
owner, so an existing hub gets the index the next time its owner runs
`mtix sync init`. Until then each listing page scans `sync_events`
once: one scan per pull for a window, and one per page for the full
diff, which the saved progress spreads across pulls. The fetch by id
always uses the primary key.
`TestLateEventQueries_PlanUsesIndex_ServesSweepWithoutSeqScan` checks
that the planner can serve each query from its index.

**Failure.** A recovered event that fails its checks or its apply is
quarantined like one from the cursor pass, and the sweep completes and
records its time. A hub failure (a listing or fetch that fails, a hub
that returns no clock) fails the pull; the events it has not applied
stay staged, and the next pull applies them after the events with
lower Lamport clocks.

## Quarantine

`cmd/mtix/sync_pull_quarantine.go`,
`internal/sync/validator/validator_ingest.go`,
`internal/store/sqlite/sync_quarantine.go` and
`internal/store/sqlite/schema_quarantine.go` (MTIX-95.11). The hub checks
an event when it is pushed, but a replica cannot trust that every hub
row passed that check (a row written by an older or modified client, or
directly into the database). Pull therefore checks every event it
receives, from the cursor pass or the late-event sweep, before applying
it, in this order:

1. **Lamport clock** (`validator.CheckIngestClock`). A clock at or above
   2^53 is refused (FR-18.7 overflow guard), and so is a clock more than
   `sync.max_lamport_jump` above the local clock (`meta.sync.lamport`,
   read in the batch's transaction). The clock is checked first, so an
   event with an extreme clock is always refused for its clock, whatever
   else is wrong with it. The default bound is 2^32 (4294967296). A
   Lamport gap is bounded by the number of events a replica has not yet
   seen, so no real history comes near it, while no single event can
   carry the clock near 2^53, after which every event this replica emits
   would be refused at push. Set it with `mtix config set
   sync.max_lamport_jump <positive integer>`; any other value is refused,
   and one edited into `config.yaml` by hand is ignored in favour of the
   default. A failure to read the local clock is a local fault: it fails
   the batch and never quarantines a hub event.
2. **Envelope.** The FR-18.7 validation the hub runs at push
   (`validator.ValidateIngest`, which shares its rules with `Validate`):
   payload at most 64 KB and nesting depth at most 10, vector clock at
   most 100 entries, each below 2^53, and the id grammars (`author_id`,
   `author_machine_hash`, `project_prefix`, `op_type`). A hub row the
   transport could not fully decode (a `vector_clock` that is not a map
   of counters) is returned marked malformed instead of failing the whole
   pull, and is refused here. The clock-relative FR-18.8 rule only warns:
   an event stamped more than 24 hours ahead of this machine's clock is
   applied, with `WARN: pull: event <id> is stamped more than 24h ahead
   of this machine's clock` on stderr (review F-25). The hub refuses such
   an event at push, but at ingest this machine's clock may be the wrong
   one, and refusing a hub event would make this replica diverge from its
   peers.

An admitted event is applied through `IdempotentApply` inside its own
savepoint. An event that fails a check or its apply has the savepoint
rolled back (its mirror row in `sync_events` included) and is stored in
the local table `sync_quarantine` instead:

| Column | Meaning |
|---|---|
| `event_id` | primary key |
| `source` | the pass that first quarantined it: `pull` (cursor pass) or `sweep` |
| `raw_event` | the event as JSON, as pulled |
| `reason` | why it was first quarantined (one line, control characters removed) |
| `first_seen`, `last_attempt` | RFC 3339 UTC |
| `attempts` | every failed attempt, the first included |
| `cli_version` | the mtix version that first quarantined it |

The batch goes on with the next event. A quarantined event is never
written to `sync_events` and never advances the local Lamport or vector
clock. A later failure of the same event counts one attempt and moves
`last_attempt`; nothing else in its row is rewritten. When the cursor
pass or the sweep meets an event that is already quarantined, it leaves
the event to the quarantine retry without touching its row. An event
already in `applied_events` skips the checks: `IdempotentApply` dedupes
it with no clock change. The hub copy of this replica's own event, which
is only in `sync_events` until a pull acknowledges it, is checked like
any other, since its hub row can be changed after the push; a legitimate
copy always passes.

**Cursor.** The pull cursor moves past quarantined events, except past
a Lamport clock at or above 2^53 or more than `sync.max_lamport_jump`
above the local clock after the batch. That decision comes from the
clock itself, never from the reason recorded for the event: a cursor at
an extreme clock would stop the cursor pass from returning any later
event. Only a failure of the transaction itself fails the batch:
reading the local clock, a savepoint or quarantine write, or a pull
that is cancelled or times out, which never quarantines the event it
was on.

**Retry.** Every pull retries the whole quarantine, in Lamport order
(the `lamport_clock` of `raw_event`, then `event_id`), `--limit` rows
per local transaction: at the start of the pull, before the hub is
contacted (so it also runs when the hub is unreachable), and again at
the end of a pull that applied events, since those can be what a
quarantined event lacked (a `link_dep` target, a node's create). A row
whose event is already in `applied_events` (for example after `mtix sync
clone` applied it) is removed before any check. Every other row goes
through the same checks and its own savepoint, including a row for this
replica's own event that is only in `sync_events`: an event that applies is removed; one that fails again
stays, with `attempts` and `last_attempt` updated. The retry uses the
stored raw event and contacts no hub. `mtix sync reconcile
--discard-local --yes` empties the quarantine with the rest of the local
sync state, and deletes local tasks and unpushed changes (its dry run
shows only the node count); the next pull runs the checks again and
quarantines again what still fails. The agent guidance requires `mtix
sync push`, a pending count of 0 and a human's go-ahead before it. A
quarantined copy of this replica's own event (its hub row changed after
the push) never clears by itself, because every retry re-checks the
stored copy, even after the hub row is repaired; the documented order is:
repair the hub row, push, then discard-local and pull. Discarding first
would drop the replica's own true copy, which would then return only as
a quarantined hub event.

**Clone.** `mtix sync clone` has no quarantine and stays all or
nothing, so it runs the same checks (`checkPulledEvent`: the clock, then
the envelope, a malformed row included) on every hub event before it
writes anything (`preflightClone`, `cmd/mtix/sync_clone_check.go`), with
the running clock the clone would have (the local clock, then the
highest clock checked so far). When any event fails, clone refuses the
whole clone and writes nothing; the refusal names the event and the
reason and gives the recovery (`cloneRecovery`): on the fresh store,
`mtix sync pull`, which quarantines the event and applies the rest;
`mtix sync reconcile --discard-local --yes` only on a store that already
holds sync state, after a push, a pending count of 0 and a human's
go-ahead, since it deletes local tasks and unpushed changes. The apply
checks each event again, so an event pushed to the hub after the check
stops the clone at that batch. A clone that completes resets the
quarantine along with the late-event sweep state, since every hub event
passed. The check reads the hub's event log once more before the apply
reads it.

**Cost per pull.** The retry reads every quarantined row and writes
`attempts` and `last_attempt` for each one that fails again, up to twice
per pull (start and end), in the local database only. An event refused
for its clock keeps the cursor below it, so every cursor pass downloads
it from the hub again (one row each; the cursor pass leaves it to the
retry and writes nothing for it). None of this adds a hub query or wakes
an idle hub; a daemon that pulls on an interval repeats it on each pull.

**Surfaces.** Pull prints each newly quarantined event on stderr and, at
the end, `quarantine: N pulled events held, not applied` on stdout. `mtix
sync status` shows `quarantined events` (`quarantined_events` in
`--json`). `mtix sync quarantine list` (`--json` for an array) lists
every quarantined event read-only through the service layer: event id,
node, op, attempts, first seen, last attempt and reason, and in JSON
also the Lamport clock, source and CLI version. `mtix sync doctor` has a
`quarantined events` check that fails while the table is not empty; its
detail says the events are retried on every pull, to run `mtix sync
pull` first, and to list them with `mtix sync quarantine list`.

The table is created with `CREATE TABLE IF NOT EXISTS` the next time
mtix opens the store (no schema version change) and is local only.

## Idempotent apply

A replica applies each event at most once. `event_id` is the dedupe
key, checked against two local tables. On apply:

```
if event_id already in applied_events:
    return nil (already applied, no-op)
if event_id already in local sync_events (any sync_status):
    # own-event rule: this replica already holds the event
    advance lamport, merge VC (both from the LOCAL row's clocks),
    INSERT OR IGNORE into applied_events
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
it merges the clocks of the local `sync_events` row, never those of the
pulled copy, and records the event in `applied_events` with the local
row's Lamport clock, but never applies it again. The local row is what
this replica emitted; the hub row of a pushed event can be changed on
the hub (MTIX-95.11). Pull's ingest checks also run on the hub copy of
an own event until it is acknowledged: only an event already in
`applied_events` skips them (see [Quarantine](#quarantine)). Applying it again would log a spurious LWW
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

Every migration file runs in that one transaction, so an index a
migration adds (014, 015) is built inside it, not concurrently. The
transaction also re-runs the `ALTER TABLE sync_events ADD COLUMN IF NOT
EXISTS` of migrations 010 and 013, which take an `ACCESS EXCLUSIVE` lock
on `sync_events` even when the column exists, and hold it until the
transaction commits. So while `mtix sync init` migrates, pushes and
pulls both wait for it. A push or pull whose statement waits past its
10-second statement timeout is retried, up to 5 attempts in all (about
50 seconds, `DefaultRetryConfig` in `transport/retry.go`), so it
normally completes once `mtix sync init` finishes. It fails only if the
wait outlasts its retries; a push then keeps its events queued and a
pull keeps its cursor, and the next one succeeds. Run `mtix sync init`
when a pause in sync is acceptable.
On a large hub the first build of an index makes that pause longer;
once the index exists its `CREATE INDEX IF NOT EXISTS` only checks for
it. `mtix sync init` gives the connection and the whole migration 30
seconds; a migration that takes longer is rolled back, and the hub
keeps working without the new index (pulls stay correct, only slower).

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
(default 30s). Each tick is a full `mtix sync pull`: the same tuple
cursor, saved with each batch, and the same late-event sweep. It does
NOT push by itself — pushes happen via the
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
  - `internal/store/postgres/transport/late_events.go` and
    `cmd/mtix/sync_pull_sweep.go` — late-event sweep of pull
  - `internal/store/postgres/transport/migrate.go` — single-flight
    migration
  - `internal/sync/redact/redact.go` — DSN scrubber + panic Recover
