# mtix Sync Protocol Design

> **Status:** v1 — design lock-in (MTIX-15.1)
> **Audience:** mtix contributors, security reviewers, and adopting teams.
> **Companion documents:**
> - [SECURITY-MODEL.md](./SECURITY-MODEL.md) — trust contract for both SQLite and sync deployments.
> - [REQUIREMENTS.md FR-18](../REQUIREMENTS.md) — normative requirements that bind every implementation ticket.

This document is the canonical specification for mtix's local-first team sync. Every implementation ticket (MTIX-15.2 through MTIX-15.13) refers to a numbered section here. If a future change to the code conflicts with this document, the document MUST be updated in the same change — drift between code and design is a defect.

## 1. Goals and non-goals

### 1.1 Goals
- **Local-first.** Every CLI keeps its own SQLite database. The hub is a sync substrate, not the system of record at command time. A developer with no network can do everything except push/pull.
- **Convergence.** Any two CLIs that have observed the same set of events converge to byte-identical local state, regardless of the order events arrived.
- **Auditability.** Every mutation that lands locally is also recorded as a sync event in the same SQLite transaction. No ghost mutations; no orphan events.
- **Set up once and forget.** No daemons required for correctness. No periodic maintenance. Sync triggers in-process on mutations; an opt-in daemon exists only for real-time UX.
- **Safety.** Existing FR guarantees (NodeTypeForDepth canonicalization, atomic audit log, deterministic ordering, parameterized SQL) apply equally to events that arrive over sync.

### 1.2 Non-goals
- **Hosted SaaS.** A HyperSWE-operated multi-tenant cloud is out of scope for v0.2 (separate roadmap). BYO-hub-Postgres only.
- **CRDTs.** Vector clocks + Last-Write-Wins resolution is sufficient for tasks (small object count, low write rate, deterministic tie-break suffices). CRDT machinery would add complexity without a measurable benefit at this scale.
- **Per-user authentication beyond the DSN.** Section 7.6 documents this as a residual risk; v0.2 adopters get team-level trust only. Per-user PG roles + RLS are deferred to a future major.
- **Confidentiality boundary inside a team.** mtix is not a side-channel-resistant system; see Section 7.7.

## 2. Architecture overview

```
+----------------+     +----------------+     +----------------+
|    Laptop A    |     |    Laptop B    |     |    Laptop C    |
|  ----------    |     |  ----------    |     |  ----------    |
|  mtix CLI      |     |  mtix CLI      |     |  mtix CLI      |
|  Local SQLite  |     |  Local SQLite  |     |  Local SQLite  |
|  sync_events   |     |  sync_events   |     |  sync_events   |
|     (queue)    |     |     (queue)    |     |     (queue)    |
+--------+-------+     +--------+-------+     +--------+-------+
         |                      |                      |
   Push/Pull              Push/Pull              Push/Pull
         |                      |                      |
         v                      v                      v
                  +-----------------------------+
                  |        Sync Hub (PG)        |
                  |  -----------------------    |
                  |  sync_events  (canonical)   |
                  |  sync_conflicts             |
                  |  sync_projects              |
                  |  applied_events  (dedupe)   |
                  |  audit_log    (append-only) |
                  +-----------------------------+
```

**Invariants:**

1. The local SQLite is authoritative for *executing CLI commands*. Reads never touch the hub.
2. The hub is authoritative for *cross-CLI ordering*. The hub assigns no IDs and runs no business logic; it is a durable, ordered event log with conflict bookkeeping.
3. Every mutation produces exactly one sync event, written in the same SQLite transaction as the mutation (chaos test enforced — see MTIX-15.2).
4. Every event in `applied_events` corresponds to either (a) a row mutation in `nodes`, or (b) an explicit tombstone. The chaos tests in MTIX-15.4 prove this.

## 3. Event schema

Events are the unit of replication. Schema lives in `sync_events` (local mirror) and on the hub.

### 3.1 Fields (normative)
| Field | Type | Notes |
|---|---|---|
| `event_id` | TEXT (UUID v7) | Globally unique. UUID v7 puts the timestamp prefix in the high bits, so event_id sorts naturally by emission time within one author. |
| `project_prefix` | TEXT | The project the event belongs to (e.g. `MTIX`). Routes events on the hub. |
| `node_id` | TEXT | The node the event mutates. Tombstone events still reference the original ID. |
| `op_type` | TEXT (enum) | One of the 12 operation types in §3.3. |
| `payload` | JSONB | Op-specific payload; <=64KB serialized; nesting depth <=10 (§4.2). |
| `wall_clock_ts` | INT64 | Milliseconds since UNIX epoch on the emitting CLI. **Never** the source of truth for ordering. |
| `lamport_clock` | INT64 | Lamport scalar; primary ordering input (§8.1). |
| `vector_clock` | JSONB map[author_id]int64 | Per-author counter; primary causality detector (§8.1). |
| `author_id` | TEXT | Logical identity from local config; matches `^[a-z0-9_-]{1,64}$` (validator §4.2). |
| `author_machine_hash` | TEXT (hex) | SHA-256 prefix of the emitter's machine ID; used as the LWW final tie-break. Stable per machine, opaque to the user. |
| `sync_status` | TEXT (local only) | `pending` \| `pushed` \| `conflicted` \| `applied`. Hub mirror always equals `applied`. |
| `created_at` | TIMESTAMPTZ | Hub insert time; informational only. |
| `retained_until` | TIMESTAMPTZ NULL | Reserved for v2 compaction (§5.2). NULL in v1. |

### 3.2 Hub-only tables
- **`sync_conflicts`** — every non-trivial LWW resolution that drops a value. Columns: `conflict_id`, `event_id_a`, `event_id_b`, `node_id`, `field_name`, `resolution` (`lww`|`tombstone`|`manual`), `resolved_at`, `resolved_by` (NULL unless manual override).
- **`sync_projects`** — one row per project. Columns: `project_prefix` (PK), `first_event_hash` (used for divergent-history detection §10), `created_at`, `schema_version`, `last_seen_cli_version` (per MTIX-15.7 advisory check).
- **`applied_events`** (also local) — `event_id` PK; existence ⇒ event has been applied; idempotent dedupe key.

### 3.3 Operation types (12)
1. `create_node` — payload: full Node row (title, parent_id, node_type, etc.).
2. `update_field` — payload: `{field_name, new_value}` for a single field.
3. `transition_status` — payload: `{from_status, to_status}`.
4. `claim` — payload: `{agent_id, ttl_seconds}`.
5. `unclaim` — payload: `{}`.
6. `defer` — payload: `{reason}`.
7. `comment` — payload: `{author_id, body}`.
8. `link_dep` — payload: `{depends_on_node_id, dep_type}`.
9. `unlink_dep` — payload: `{depends_on_node_id}`.
10. `delete` — payload: `{}` (tombstone). Monotonic — once deleted, stays deleted; see §8.3.
11. `set_acceptance` — payload: `{acceptance_text}`.
12. `set_prompt` — payload: `{prompt_text}`.

Adding a 13th op_type is a **major** protocol version bump (§4).

## 4. Protocol versioning

### 4.1 Constant
`MTIX_SYNC_PROTOCOL_VERSION = 1` at v0.2 release. Bumped per the policy below.

### 4.2 Version policy
- **Minor bump (1.0 → 1.1):** purely additive. New optional payload fields, new error codes, new op-type **only** if old clients can safely ignore unknown events (they cannot — so new op_types are major). Old clients MUST tolerate unknown optional fields.
- **Major bump (1.x → 2.0):** any breaking change. New required payload field, removed field, new op_type, semantics change of an existing field, change to vector-clock format, change to LWW tie-break order.
- The hub MUST refuse cross-major requests with `SYNC_VERSION_MISMATCH` and a one-line upgrade hint pointing at the matching mtix release.

### 4.3 Wire metadata
Every `PushEvents` and `PullEvents` request carries the client's protocol version in a metadata envelope: `{protocol_version: 1, client_version: "mtix v0.2.0-beta", client_os: "linux"}`. The hub records `protocol_version` and `client_version` (the latter feeds `last_seen_cli_version` per §6.3).

### 4.4 Migration paths
| Scenario | Hub behavior | CLI behavior |
|---|---|---|
| Old client (1.0) + new hub (1.1) | Accept push and pull (additive) | Works. May surface new event fields it ignores. |
| New client (1.1) + old hub (1.0) | Accept push and pull; new optional fields stored as-is in JSONB | Works. New CLI features may degrade gracefully. |
| Cross-major (1.x ↔ 2.x) | Refuse with `SYNC_VERSION_MISMATCH` | `mtix sync doctor` surfaces the version mismatch + upgrade command. |
| Mixed-version team (some 1.0, some 1.1, all on a 1.1 hub) | Hub serves the lowest common denominator. Events from a 1.1 client containing a 1.1-only optional field are still pulled by 1.0 clients; the 1.0 client tolerates the unknown field. | Each CLI works at its own version. The team is encouraged to upgrade together but is not forced. |

### 4.5 Schema validation envelope (binds with §5)
Every event entering the hub passes the §5 validator before any business logic runs. Validator is the first line of defense against malformed input from a buggy or hostile client.

## 5. Hub-side validation (HIGH severity)

The hub MUST reject any event that fails validation, with structured error `MTIX_SYNC_EVENT_INVALID` plus the failing field name. Partial-batch acceptance is forbidden; the entire `PushEvents` batch fails atomically if any event in it is invalid.

### 5.1 Validator rules
| Rule | Constraint | Failure code |
|---|---|---|
| `event_id` format | Valid UUID v7 | `EVENT_ID_INVALID` |
| `op_type` | One of the 12 enum values | `OP_TYPE_UNKNOWN` |
| `payload` size | `len(payload) <= 64 * 1024` bytes serialized | `PAYLOAD_TOO_LARGE` |
| `payload` depth | JSON nesting depth `<= 10` | `PAYLOAD_TOO_NESTED` |
| `wall_clock_ts` | Non-negative int64; `<= now + 24h` (§9) | `TIMESTAMP_FUTURE` |
| `lamport_clock` | Non-negative int64; `< 2^53` (vector-clock overflow protection) | `LAMPORT_OVERFLOW` |
| `vector_clock` | Flat map; entries `<= 100`; each value `< 2^53` | `VECTOR_CLOCK_INVALID` / `VECTOR_CLOCK_OVERFLOW` |
| `author_id` | Matches `^[a-z0-9_-]{1,64}$` | `AUTHOR_ID_INVALID` |
| `node_id` | Matches the dot-notation grammar from FR-2 | `NODE_ID_INVALID` |
| `project_prefix` | Matches `^[A-Z][A-Z0-9_]{0,15}$` | `PROJECT_PREFIX_INVALID` |

### 5.2 Hub event log retention (deferred, with rationale)
- Default retention: **forever** (`hub.events_retention_days = 0`).
- Setting key (`hub.events_retention_days`) and schema column (`sync_events.retained_until`) MUST exist in the v1 schema with explicit TODO comments. Both are no-ops in v1.
- **Why compaction is deferred to v2:**
  1. v1 ships with a known unbounded-growth bound: 1M events per project (§6.2 supported scale). At that ceiling, the event log is well under the practical Postgres single-table size limit.
  2. Compaction is a multi-CLI consensus problem: every CLI must agree on which events were folded into a snapshot, and which `applied_events` records correspond to the snapshot. Getting this wrong silently corrupts state.
  3. Implementing compaction now would block beta on a non-blocking concern (no real adopter is at the 1M-event ceiling on day one).
  4. The schema column reservation makes the v2 transition non-breaking. Future compaction sets `retained_until` on folded events, then deletes them after a grace window.

## 6. Operating envelope and limits

### 6.1 Privacy / PII
- mtix does **not** classify or scrub PII. The hub stores whatever the team writes into nodes, verbatim — titles, descriptions, prompts, comments, every text field.
- mtix is **not** GDPR/HIPAA/PCI-compliant. `mtix delete` writes a tombstone event (op_type `delete`); the original content remains in the event log and in any downstream backups.
- Teams that require true right-to-erasure MUST either:
  1. Wait for the planned hosted SaaS (separate roadmap), which will offer per-tenant erasure semantics, **or**
  2. Rotate the entire hub DB: dump, scrub at the SQL level, restore to a fresh Postgres instance, re-clone every CLI.
- This statement is duplicated verbatim in `SECURITY-MODEL.md` so adopters cannot miss it.

### 6.2 Tested-and-supported scale
| Dimension | Supported | Soft warning at | Hard ceiling |
|---|---|---|---|
| Active developers per hub | <= 10 | 15 | 25 (PG connection pool exhaustion likely) |
| Nodes per project | <= 10,000 | 25,000 | 100,000 (UI pagination becomes mandatory) |
| Events per project lifetime | <= 1,000,000 | 500,000 | 5,000,000 (full clone exceeds 30s; must paginate) |
| Concurrent agents per laptop | <= 10 | 25 | 50 (singleton pusher serializes them) |

What fails outside the envelope:
- Pull latency grows linearly with event count past 100K events. `mtix sync pull --limit` paginates.
- `mtix sync clone` for projects above 10K events MUST stream in batches of 1000 with a progress bar (MTIX-15.7).
- `mtix sync status` aggregates over `sync_events` and degrades past 100K events.

### 6.3 Version compatibility (advisory)
- Each `PushEvents` / `PullEvents` request carries `client_version`. The hub stores the highest seen version per project in `sync_projects.last_seen_cli_version`.
- `mtix sync doctor` warns when local version `< last_seen_cli_version - 1 minor`.
- The check is **advisory**, never blocking. Mixed-minor teams stay productive; the warning nudges everyone forward.

## 7. Trust and threat model

### 7.1 Who is trusted (extends SECURITY-MODEL.md §3)
| Party | Trust | Why |
|---|---|---|
| Local CLI user | Full | Owns the laptop; owns the local SQLite |
| Any process on a trusted laptop | Full | Same machine; same trust scope |
| Anyone with the hub DSN | Full | DSN is the credential; team-shared like a git remote URL |
| Other team members | Full | A team's mtix hub is shared like a team's git repo — by membership, not isolation |

### 7.2 Who is NOT trusted
- Network adversaries — mitigated by `sslmode=verify-full` (§7.4).
- Anyone without the DSN — gated by Postgres authentication.
- Other tenants — out of scope; use the future hosted SaaS.
- A malicious or buggy peer CLI — mitigated by validators (§5), idempotent apply (§8), and node_type canonicalization on apply.

### 7.3 Threat catalogue
| # | Threat | Mitigation in v1 | Residual risk |
|---|---|---|---|
| T1 | Credentials in git | DSN refused from any tracked file; only `MTIX_SYNC_DSN` env var or `.mtix/secrets` (mode 0600, gitignored). Refusal is fail-closed at config load. | Low — fail-closed |
| T2 | MitM on the PG connection | `sslmode=verify-full` default; weaker modes refused unless `--insecure-tls` AND host is loopback | Low if the operator picks a managed PG with proper certs |
| T3 | SQL injection from malicious filter values or event payloads | Bound parameters at every PG call; MTIX-9.1 attack-pattern test ported to the PG transport | Very low — depends on no future regression |
| T4 | Insider mutation tampering (compromised team member edits/deletes via mtix) | Append-only `audit_log` written atomically with every mutation; PG triggers prevent UPDATE/DELETE on audit rows | Medium — PG superuser bypasses triggers; insider with write access can still create or modify nodes |
| T5 | Audit log tampering (DBA edits or deletes audit rows) | Triggers raise on UPDATE/DELETE | Medium — PG superuser bypasses; safety-critical adopters ship audit_log to immutable cold storage |
| T6 | Hook bypass (`git push --no-verify`, missing hook on a machine) | Client-side hooks are advisory; server-side enforcement (pre-receive, GitHub Action) is the real gate | Medium — depends on team policy |
| T7 | Replay attack from a stolen sync event | Idempotent apply via `applied_events` PK; replay is a no-op | Very low |
| T8 | Future-timestamp abuse to win LWW | Hub rejects `wall_clock_ts > now + 24h` (§9); LWW primarily uses `lamport_clock`, `wall_clock_ts` is tie-break only | Very low |
| T9 | Vector clock overflow to bypass causality detection | Validator caps each VC entry at `< 2^53`; fuzz target in MTIX-15.11 | Very low |
| T10 | Malformed events crashing the hub or apply engine | Schema validator (§5); fuzz targets in MTIX-15.11; `IdempotentApply` chaos test in MTIX-15.4 | Very low |
| T11 | Conflict storm (200 concurrent edits to the same node) | Conflict storm UX (§11); LWW remains deterministic regardless of count | Low — UX degrades gracefully, semantics intact |
| T12 | Divergent history (Charlie joins from a solo project with conflicting prefix) | First-sync hash check refuses with structured error and four resolution paths (§10) | Low |
| T13 | Conflict log poisoning (insider writes thousands of bogus conflicts) | sync_conflicts inherits audit_log triggers; conflicts have no semantic effect on node state | Low |
| T14 | Stolen DSN gives full read+write to the hub | Documented as residual; rotate via PG password rotation; existing CLIs fail closed | Medium — only mitigation is DSN rotation; per-user PG roles deferred to v2 |
| T15 | Side-channel attacks (timing, cache, memory) | Out of scope: mtix is not a confidentiality boundary inside a team | N/A — see §7.7 |
| T16 | DSN leakage via panic message or log | Top-level `defer-recover` runs every panic value through `RedactDSN` (extends MTIX-14.9 redactor); regression sweep in MTIX-15.11 | Very low |
| T17 | Partial schema migration leaves the hub broken | PG advisory lock single-flights migrations; partial-migration kill-9 test in MTIX-15.3 | Very low |
| T18 | CLI version drift (one peer is 6 months stale) | Advisory `last_seen_cli_version` warning in `mtix sync doctor` (§6.3) | Low — operational, not data-integrity |
| T19 | Hub data loss (PG instance is destroyed) | `mtix sync backup` performs `pg_dump` of mtix-owned tables (MTIX-15.7); restore documented in workflow doc | Medium — adopter is responsible for backup retention |
| T20 | Reconciliation aborts mid-flight, leaving local DB in a half-state | Reconciliation atomicity tests with synthetic failure injection per resolution path (MTIX-15.6) | Very low |

### 7.4 TLS posture
- Default: `sslmode=verify-full`. Refuse `verify-ca`, `prefer`, `disable` unless `--insecure-tls` AND host resolves to loopback.
- `MTIX_SYNC_SSLROOTCERT` env var and DSN `sslrootcert=` parameter honored. Managed PG providers (Supabase, Neon, RDS) commonly require this; the docs in workflows/* explain how to fetch the provider's CA bundle.
- pgbouncer in **session mode** is supported. Transaction mode breaks prepared statements and advisory locks; `mtix sync doctor` detects transaction mode via `SHOW pool_mode` and warns.

### 7.5 DSN sourcing (fail-closed)
| Source | Allowed | Why |
|---|---|---|
| `MTIX_SYNC_DSN` env var | Yes | Standard 12-factor pattern; trivial to rotate |
| `.mtix/secrets` file (mode 0600, gitignored) | Yes | Persists across shells; gitignore rule auto-installed by `mtix sync init` |
| Any tracked YAML/JSON config (`.mtix/config.yaml`, etc.) | **NO** | Refused with `MTIX_SYNC_DSN_IN_TRACKED_FILE` at config load. Test: place DSN in `.mtix/config.yaml`, expect refusal. |
| CLI flag `--dsn` | **NO** | Process listings expose flags; refused at flag parse |

### 7.6 Stolen DSN (HIGH)
- A stolen DSN gives the holder full read+write on the hub. This is documented as a known residual risk.
- **Mitigation:** rotate the DSN by rotating the PG role password (`ALTER ROLE mtix_user WITH PASSWORD '...'`). Existing CLIs fail closed at the next push/pull; users re-prompt and update `.mtix/secrets`. `mtix sync doctor` surfaces the auth failure with the exact rotation guidance.
- **Not in v1:** per-user PG roles + row-level security. Designed but not implemented; would require a server-mediated identity flow, which is outside the BYO-PG scope. Tracked for a future major.

### 7.7 Side-channel non-applicability (LOW)
- mtix is not a confidentiality boundary inside a team. All members with the DSN see all data.
- Therefore: timing-attack mitigations, cache-side-channel mitigations, and constant-time comparisons in the sync code path are **out of scope**.
- This is documented explicitly so a future contributor does not waste effort hardening against a non-threat. If mtix ever ships a multi-tenant mode, this section MUST be revisited.

## 8. Conflict resolution

### 8.1 Vector clocks + Lamport scalar
- Each event carries a `vector_clock: map[author_id]int64` and a `lamport_clock: int64`.
- On emit: `lamport_clock = max(local_lamport, max(observed_lamport_in_pulled_events)) + 1`. Vector clock entry for `local.author_id` is incremented.
- On apply: local lamport advances to `max(local, event.lamport_clock)`. Vector-clock entry for `event.author_id` updated.
- Causality: event A causally precedes event B iff `A.vc <= B.vc` componentwise. A and B are concurrent iff neither dominates the other; concurrent events of the same field trigger conflict resolution.

### 8.2 LWW resolution (deterministic)
For two concurrent events touching the same field of the same node, the winner is selected by this strict ordering:
1. **Primary:** higher `lamport_clock` wins. Lamport reflects observed causality; the writer who saw more events first wins.
2. **First tie-break:** higher `wall_clock_ts` wins. Bounded by §9 to limit clock-skew abuse.
3. **Final tie-break:** lower `author_machine_hash` wins (lexicographic compare). Stable per machine, opaque to the user, deterministic across replays.
- **No randomness.** Property-based test in MTIX-15.4 enforces byte-identical state across 1000 random shuffles.

### 8.3 Resolution classes
- **Disjoint fields.** Two concurrent events touching different fields of the same node both apply; no conflict logged.
- **Same field.** LWW per §8.2. Loser is logged to `sync_conflicts` (hub) and `.mtix/conflicts.log` (local).
- **Delete vs update.** Tombstone always wins. Once `op_type=delete` is applied, every subsequent non-delete event for that node is a no-op (apply engine returns nil; logs at debug). Tombstones are monotonic: the deletion record itself stays, but updates that arrive after a delete are dropped.
- **Delete on non-existent node.** No-op. Does not create a phantom tombstone.

### 8.4 Manual override
- `mtix sync conflicts list` surfaces unresolved conflicts.
- `mtix sync conflicts resolve <id> --action keep-local|keep-remote|both-renumbered` writes a new event that supersedes the LWW outcome. Recorded as `resolution=manual` in `sync_conflicts`.

## 9. Timestamp validation (HIGH)

- Every event has `wall_clock_ts: int64` (milliseconds since epoch).
- Hub MUST reject events with `wall_clock_ts > now + 24h` with `SYNC_TIMESTAMP_FUTURE`. This bounds clock-skew abuse for the LWW tie-break (§8.2).
- Hub MUST accept (but log a warning for) events with `wall_clock_ts < now - 30d`. Legitimate replay from a long-offline laptop is allowed.
- Conflict resolution uses `lamport_clock` first; `wall_clock_ts` is the first tie-break only. A clock-skewed event cannot win against a higher-Lamport event regardless of timestamp.
- Test boundaries: `+25h`, `+30d`, `+1y` all rejected; `-31d` accepted with WARN log.

## 10. Divergent-history detection and reconciliation

### 10.1 First-sync detection
- `mtix sync init` creates the project: writes `sync_projects` row with `first_event_hash = SHA256(canonical_serialization_of_first_event)`.
- `mtix sync clone` and the first `mtix sync push` after `init` query `sync_projects WHERE prefix=local.project_prefix`:
  - **No row:** create it (we are the first writer).
  - **Row exists, hash matches:** proceed.
  - **Row exists, hash differs:** refuse with structured error `MTIX_SYNC_DIVERGENT_HISTORY` and four-option summary.

### 10.2 Four resolution paths
1. **`--discard-local`** — drop local nodes, clear sync_events, run a fresh clone. Unrecoverable; requires `--yes` to skip the y/N prompt.
2. **`--rename-to NEWPREFIX`** — atomically rewrite all local node IDs from `<old>-N` to `<new>-N`; register `NEWPREFIX` as a new project on the hub; push. Both prefixes coexist.
3. **`--import-as PARENT-ID`** — re-parent the entire local tree under `PARENT-ID`; renumber local IDs into the new namespace; push as additions.
4. **`--dry-run`** — preview only. Output identical to the actual run's audit events, minus side effects. Diff-tested against the actual run in MTIX-15.6.

### 10.3 Atomicity (HIGH)
Every reconciliation is a single SQLite transaction at the local layer plus a single PG transaction at the hub layer. Synthetic-failure injection tests in MTIX-15.6 prove:
- `--rename-to` failing on rename N rolls back ALL N renames; `RECONCILE_START` audit event present, `RECONCILE_DONE` absent, `.mtix/id-rename-map.json` absent or `partial=true`.
- `--import-as` failing on hub push N leaves local nodes re-parented but `sync_status='pending'`; the next push completes without duplicate events.
- `--discard-local` failing mid-DROP leaves an explicit `.mtix/.reconcile.aborted` marker; subsequent mtix calls refuse until `--resume` or `--abort-cleanup`.

### 10.4 Prefix collision check
Before `--rename-to NEWPREFIX`, query the hub for any project with `prefix=NEWPREFIX`. If found, refuse with `MTIX_RECONCILE_PREFIX_COLLISION` listing the existing project's node count. The check happens BEFORE any local mutation; test enforces.

### 10.5 Audit trail
Every reconciliation emits structured audit events:
- `RECONCILE_START {chosen_path, node_count, estimated_duration_ms}`
- `RENAME_NODE {old_id, new_id}` per node
- `RECONCILE_DONE {final_node_count, duration_ms, errors}`
Plus `.mtix/id-rename-map.json` for post-reconciliation agent lookup ("where did MTIX-3.1 go?").

## 11. Conflict storm UX

When unresolved conflicts exceed 50 for one project:
- `mtix sync status` surfaces a banner: `X conflicts grouped across Y nodes. Use mtix sync conflicts resolve --batch <node_id> to accept LWW for all fields of one node.`
- `mtix sync conflicts list` groups by `node_id` and shows counts per node.
- `--batch <node_id>` accepts LWW for all fields of one node.
- `--batch-all` accepts LWW for everything across all conflicts. Requires a confirmation prompt with the count of values that will be dropped; cannot be bypassed without `--yes`.

### 11.1 Agent surface
When `mtix context <node_id>` is called on a node with unresolved conflicts, the rendered context appends a `CONFLICT` block:

```
=== CONFLICT (unresolved) ===
field: assignee
  candidate A: alice  (event 0193fa-..., lamport=42, ts=2026-04-27T10:00Z)
  candidate B: bob    (event 0193fb-..., lamport=42, ts=2026-04-27T10:00:01Z)
LWW winner currently exposed in this context: bob (tie-break by wall_clock_ts).
Per AGENTS.md, agents MUST NOT silently choose; escalate via mtix_comment.
=== END CONFLICT ===
```

`AGENTS.md` is updated to instruct: agents that see a CONFLICT block MUST `mtix_comment` requesting human resolution rather than acting on the LWW value.

## 12. Pluggability boundaries (mgit integration note)

mtix and mgit are sibling projects in the HyperSWE stack. The integration contract is **event-bus-shaped**, not RPC-shaped: mtix emits events, mgit subscribes; mgit emits events, mtix subscribes. Neither imports the other.

### 12.1 Outbound events from mtix to mgit (proposed)
- `node_done {node_id, project_prefix, completed_at}` — mgit can use this to auto-tag a commit, generate release notes, or trigger CI for downstream tasks.
- `node_blocked {node_id, dependency_id, reason}` — mgit can comment on related PRs.
- `reconciliation_completed {project_prefix, path}` — mgit invalidates any cached PR-to-task mappings.

### 12.2 Inbound events from mgit to mtix (proposed)
- `branch_merged {project_prefix, node_id, sha, merged_at}` — mtix transitions matching `in_progress` nodes to `done` (opt-in, configurable per project).
- `pr_review_requested {project_prefix, node_id, reviewer}` — mtix surfaces a comment.

### 12.3 Transport (deferred)
The actual event transport (NATS? Postgres LISTEN/NOTIFY on the existing hub? File-based outbox?) is **not** decided in v0.2. v0.2 ships the events as a stable contract; the transport ticket lands in v0.3. This keeps mtix shippable without blocking on cross-repo coordination.

## 13. Decision log

### D1. Sync hub vs BYO Postgres canonical store
**Decision:** Sync hub (events flowing through PG; local SQLite remains authoritative for reads).
**Considered alternative:** BYO Postgres canonical store (every CLI reads/writes PG directly).
**Why:** A BYO PG canonical store would require duplicating the entire `internal/store/sqlite/` (17.5K LOC) for Postgres. The local-first sync model adds ~3-5K LOC of new code (events table, push/pull, apply engine, conflict resolution) and reuses the existing SQLite store unchanged. Net result: same product, ~70% less code, every existing test still passes.
**Trade-off accepted:** Eventual consistency between team members. Mitigated by `mtix sync pull` being fast (<1s for typical batches) and the opt-in daemon for real-time UX.

### D2. Vector clocks vs CRDTs
**Decision:** Vector clocks + LWW.
**Considered alternative:** CRDTs (LWW-Map, RGA, etc.) for field-level merging.
**Why:** mtix tasks have low write rates, small object counts, and human-readable text fields where partial merges produce incoherent garbage. The trade between "always merge" (CRDT) and "deterministic LWW with surfaced conflicts" (current design) favors LWW: the user sees what was dropped and can override. CRDTs would silently merge concurrent edits, producing unreadable text fields.

### D3. Events vs diffs
**Decision:** Events (full op-type with payload).
**Considered alternative:** Row-level diffs (column before/after).
**Why:** Events carry intent (`transition_status`, `claim`) which lets the apply engine validate state-machine transitions. Diffs would lose the intent and require the engine to infer which transition was meant. Events also compose naturally for the audit log (every event IS the audit row).

### D4. Lamport vs wall-clock primary ordering
**Decision:** Lamport primary; wall-clock secondary tie-break.
**Considered alternative:** Wall-clock primary.
**Why:** Wall-clock skew across laptops is unbounded in practice. Using wall-clock primarily would let a clock-fast laptop win every conflict. Lamport reflects observed causality and is monotonically increasing per CLI; it is the right primary for "who saw more before they wrote."

### D5. UUID v7 for event_id
**Decision:** UUID v7.
**Considered alternative:** UUID v4, ULID, snowflake.
**Why:** UUID v7 is timestamp-prefixed, so event_id sorts naturally by emission time within one author. This makes per-author event log scans trivially fast. UUID v4 has no temporal ordering. ULID is functionally equivalent but less standardized. Snowflake requires a coordinator.

### D6. Append-only audit_log via PG triggers
**Decision:** Triggers raise on UPDATE/DELETE.
**Considered alternative:** Application-only enforcement.
**Why:** Application-only enforcement is bypassed by any direct SQL access (operator, DBA, debugging session). Triggers shift the boundary to PG itself. The residual risk (PG superuser can disable triggers) is documented in T5 and mitigated by archiving `audit_log` to immutable cold storage for safety-critical adopters.

### D7. PG advisory lock for schema migrations
**Decision:** `pg_advisory_xact_lock(hash('mtix_sync_migration'))` inside the migration transaction.
**Considered alternative:** Application-level mutex; client-side coordination.
**Why:** N concurrent CLIs first-connecting to a fresh hub MUST not race the schema migration. Application-level coordination requires every CLI to know about every other CLI. PG advisory locks delegate the coordination to PG itself. The chaos test in MTIX-15.3 proves correctness under SIGKILL mid-migration.

### D8. Hub-side schema validation as the first line of defense
**Decision:** Every event is validated at the hub (size limits, enum check, regex check) before any business logic.
**Considered alternative:** Client-side validation only.
**Why:** Client-side validation is bypassed by any CLI with a bug or malicious modification. Hub-side validation is mandatory. Client-side validation is duplicated for fast failure UX but is not the trust boundary.

### D9. UUID `applied_events` PK for replay idempotency
**Decision:** Local table `applied_events(event_id PK, applied_at, applied_by_lamport)`.
**Considered alternative:** Track high-water-mark per author; reject events with lamport <= HWM.
**Why:** Per-author HWM breaks down with batched replay (events arrive non-monotonically per author due to causal reordering). UUID PK is simple, correct, and fits in memory for any practical event count.

### D10. Property-based tests for replay determinism
**Decision:** Property-based test (1000 random seeds) generates causal-respecting shuffles and asserts byte-identical final state.
**Considered alternative:** A handful of hand-written replay tests.
**Why:** Hand-written tests cover the cases the author imagined. Property-based testing catches the corner cases the author did not. With 12 op_types and unbounded interleavings, exhaustive enumeration is impossible; random sampling with a stable property (final state byte-identical) is the practical alternative.

### D11. `mtix sync backup` ships pg_dump as the DR primitive
**Decision:** Ship `mtix sync backup --output FILE` that calls `pg_dump` for mtix-owned tables.
**Considered alternative:** Recommend external backup tooling without an mtix wrapper.
**Why:** Adopters outside the safety-critical workflow rarely have a robust DB backup discipline. Shipping the wrapper makes "did you back up" a one-liner that's easy to put in a cron. Restore is documented in workflows/safety-critical.md; this is the same pg_dump/psql pair every PG operator already knows.

### D12. Compaction deferred to v2 of the schema
**Decision:** Reserve schema column + setting key in v1; do not implement.
**Considered alternative:** Implement compaction in v1 to bound event log growth.
**Why:** §5.2 — see full rationale. v1 ships a 1M-event ceiling per project that no real adopter is at on day one; compaction is a multi-CLI consensus problem that materially expands the v1 scope without delivering measurable adopter value.

### D13. Singleton pusher via flock (not a daemon)
**Decision:** Background pusher acquires `.mtix/sync.push.lock` via `flock(LOCK_EX|LOCK_NB)`. First mutation in a process becomes the pusher; concurrent mutations write events and exit immediately.
**Considered alternative:** Long-running daemon process.
**Why:** Daemons are a deployment liability — they have to be installed, started, monitored, restarted on crash. flock is kernel-level, requires zero install, and is auto-released on process exit. The opt-in `mtix sync daemon` exists for real-time UX (every-30s pull) but is never required for correctness.

### D14. Hooks are advisory; server-side enforcement is the real gate
**Decision:** Pre-push hooks ship as documented examples. Safety-critical adopters configure server-side enforcement (pre-receive on self-hosted git, GitHub Action on github.com).
**Considered alternative:** Mandatory client-side hook installation as part of `mtix sync init`.
**Why:** Client-side hooks are trivially bypassed (`git push --no-verify`, missing on a new clone). Pretending they are enforcement gives a false sense of security. The workflows/safety-critical.md doc explicitly walks adopters through server-side enforcement as the only real gate.

## 14. Cross-references

| Section | Implementation ticket | Verification ticket |
|---|---|---|
| §3 Event schema | MTIX-15.2 | MTIX-15.11 |
| §4 Protocol versioning | MTIX-15.3 (envelope), 15.7 (CLI surfacing) | MTIX-15.11 |
| §5 Hub-side validation | MTIX-15.3 | MTIX-15.11 (fuzz) |
| §5.2 Retention reservation | MTIX-15.2 | MTIX-15.11 |
| §6 Operating envelope | MTIX-15.10 (perf), 15.12 (docs) | MTIX-15.11 |
| §7 Threat model | every 15.x | MTIX-15.11 |
| §8 Conflict resolution | MTIX-15.5 | MTIX-15.4 (property), 15.11 |
| §9 Timestamp validation | MTIX-15.3 | MTIX-15.11 |
| §10 Reconciliation | MTIX-15.6 | MTIX-15.6 (atomicity), 15.11 |
| §11 Conflict storm UX | MTIX-15.5 (logic), 15.7 (CLI), 15.8 (MCP) | MTIX-15.9 (E2E) |
| §12 mgit integration | event contract only in v0.2 | n/a |

## 15. Document version

| Version | Date | Change |
|---|---|---|
| 1.0 | 2026-04-27 | Initial design lock-in (MTIX-15.1). Covers protocol versioning, validation, retention deferral, scale envelope, threat catalogue, conflict resolution, reconciliation, decision log. |

Future changes to this document MUST bump this version, update the changelog row, and reference the corresponding implementation ticket. If a code change conflicts with this document, the document MUST be updated in the same change.
