# Changelog

All notable changes to mtix are documented in this file.

The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

> **Pre-1.0 note:** mtix is in beta. Minor versions may introduce
> breaking schema changes; the migration path is documented in the
> Migration section of each release.

---

## [Unreleased]

### Added
- **Disk-full safety (NFR-2.8, MTIX-26):** free-space pre-flight before every write transaction and backup (`MTIX_MIN_FREE_BYTES`, default 8 MiB floor); fail-stop latch on fatal storage errors (disk full, I/O error, detected corruption) — mtix refuses further writes instead of continuing into undefined state; database-open failures on packed volumes now name disk pressure instead of a bare `SQLITE_CANTOPEN`.
- **Integrity check on open (NFR-2.6a, MTIX-26.4):** truncated database files (in-header page count exceeding file size with no WAL to replay) are refused *before* the first connection opens, preserving recovery evidence; `PRAGMA quick_check` runs before any write on every open. `MTIX_SKIP_INTEGRITY_CHECK=1` is the documented recovery-tooling escape hatch.
- **Mirror parity for long-running interfaces (FR-15.3, MTIX-26.1):** mutations made through the MCP server and `mtix serve` now update the `.mtix/tasks.json` mirror via a debounced store on-commit hook — previously only CLI commands exported, leaving agent-driven projects without the redundancy layer.
- **Fault-injection conformance suite (MTIX-26.7):** `e2e/faultinject` drives the real binary through disk-full writes, genuine ENOSPC, kill -9 mid-write, and the 2026-05-19 field-incident signature on a dedicated tiny volume; runs on every CI build (`test-fault-injection` job). Local harness: `scripts/faultfs.sh`.

### Changed
- Write connections now set `PRAGMA synchronous = FULL` and `PRAGMA wal_autocheckpoint = 1000` explicitly instead of relying on driver defaults (MTIX-26.3); ADR-001's stale `synchronous=NORMAL` reference corrected.

### Pending
- v0.2.0-beta release — see entry below.

---

## [v0.2.0-beta] — 2026-05-18

**Headline: BYO Postgres sync hub for team collaboration (FR-18).**
Local SQLite remains canonical on every CLI; the hub is an
event-sourced replication mechanism, not a tenancy boundary. Solo
workflow is unchanged.

### Architectural framing

The v1.0 design draft (MTIX-14) framed Postgres as the canonical
store. The shipped MTIX-15 design has the local SQLite as canonical
and Postgres as a hub for replication events. The hub never sees
your tasks until you push; teammates see your tasks only after they
pull. Every CLI keeps its own complete copy of the project.

See
[docs/SECURITY-MODEL.md](docs/SECURITY-MODEL.md) (trust contract,
v1.1) and
[docs/SYNC-PROTOCOL.md](docs/SYNC-PROTOCOL.md) (protocol details
for contributors) for the full design rationale.

### Added

- **`mtix sync` subcommand family (10 commands)** — the FR-18
  surface:
  - `mtix sync init [DSN]` — provision hub schema + register
    project. Single-flighted via `pg_advisory_xact_lock` for
    concurrent inits.
  - `mtix sync clone [DSN]` — pull the full event log and replay
    into the local SQLite. Idempotent.
  - `mtix sync push` — drain the local pending queue to the hub.
    Singleton per `.mtix/` via a pushlock.
  - `mtix sync pull` — apply new hub events to the local SQLite.
  - `mtix sync status` — pending queue + last push/pull
    timestamps + machine_hash.
  - `mtix sync doctor` — 5 health checks (PG reachable, schema
    current, queue draining, no orphan applied, secrets file mode).
    Exit code 2 on any failure so CI / monitoring can gate.
  - `mtix sync conflicts list|resolve <id>` — inspect contested
    edits and override LWW per-conflict.
  - `mtix sync reconcile --discard-local|--rename-to|--import-as
    [--dry-run]` — whole-project escape hatches for divergent
    history.
  - `mtix sync daemon [--interval SEC] [--install]` — long-running
    periodic pull; prints systemd / launchd unit when
    `--install` is set.
  - `mtix sync backup --output FILE` — wraps `pg_dump` for the 5
    mtix-owned tables for portable export.
  - `mtix sync backfill [--dry-run]` — **upgrade path for v0.1.x
    users.** Walks the canonical nodes / annotations / dependencies
    tables and synthesizes `sync_events` rows so the next push
    populates the hub with existing history. See Migration section.
- **`mtix_sync_workflow` MCP tool** — exposes sync-state
  recommendations to LLM agents. State buckets: solo,
  sync-configured-no-hub, sync-active, divergent-state-pending,
  hub-unreachable. Output bounded to 4 KB; DSN never returned.
  Untrusted-context warning in the tool description per FR-18.17.
- **Event-sourced sync data plane (12 op_types)** —
  `create_node`, `update_field`, `set_acceptance`, `set_prompt`,
  `transition_status`, `claim`, `unclaim`, `cancel`, `delete`,
  `undelete`, `link_dep`, `unlink_dep`, `comment`. UUID v7
  event IDs, Lamport scalar + vector clock per author, LWW
  resolution at apply time keyed by
  `(lamport, wall_clock_ts, author_machine_hash)`.
- **Append-only hub invariants** — PG triggers raise on
  `UPDATE`/`DELETE` of `audit_log` and `sync_conflicts`.
  Manual conflict resolution INSERTs a `resolution='manual'` row
  that supersedes the LWW row.
- **DSN redactor + panic Recover** — every error / log / MCP
  output / panic flows through `redact.DSN`; `main()` wraps with
  `defer redact.Recover` so panics with a DSN in scope are
  scrubbed before the runtime printer sees them.
- **Performance benchmarks** in `benchmarks/`. Targets met (with
  headroom): solo CLI latency < 10 ms median (~170 µs observed);
  100K-node memory < 50 MB (~20–30 MB observed); push/pull 1000
  events < 5 s (~470 / 520 ms observed); pool MaxConns ≤ 5.
- **Fuzz targets** in `internal/sync/validator/fuzz_test.go`:
  `FuzzEventDecode`, `FuzzVectorClockMerge`,
  `FuzzPushEventsValidation`.
- **3-CLI E2E suite** in `e2e/sync_e2e_test.go` (gated on
  `MTIX_PG_TEST_DSN`): happy-path convergence, LWW conflict,
  divergent history, repeated-push idempotency, 9-agent surge,
  lost-laptop recovery, queue-full refusal, backfill round-trip,
  hub dedup on duplicate force-push, wall-clock-ts preservation.
- **Documentation**
  - `docs/SECURITY-MODEL.md` v1.1 — full sync trust model
    including the "Known audit-trail limitation: same-authorID
    conflicts" tradeoff.
  - `docs/SYNC-PROTOCOL.md` (new) — protocol-level spec for
    contributors and auditors.
  - `docs/SYNC-DESIGN.md` — architectural overview (already
    existed; cross-linked).
  - `docs/audit/MTIX-15-audit-pass2.md` — security audit evidence
    table (22 items PASS) with file:line references to every
    test that proves a requirement.
  - USERMANUAL chapter "Team collaboration with sync (FR-18)".
  - README "Team sync" subsection + 10 sync subcommands in the
    CLI reference.
  - CONTRIBUTING "Testing sync changes" section (local Postgres
    in Docker; fuzz target invocation; DSN hygiene sweep).

### Fixed

- **MTIX-17 — auto-unblock dependents when a blocker is marked
  done, cancelled, or invalidated.** Pre-existing bug reported by
  an external user. `executeTransitionTx` and `executeCancelTx`
  updated the transitioning node but never walked reverse `blocks`
  dependencies; the dependent stayed in `StatusBlocked` until the
  dep was manually removed. Now: after a resolving transition,
  walk `dependencies WHERE from_id = resolvedID`, call
  `autoUnblockNode` on each `to_id` inside the same tx, and emit
  a `transition_status` sync event so teammates see the unblock.
  Multi-blocker case handled correctly (dependent stays blocked
  until ALL blockers resolve). FR-3.8a invalidated-takes-precedence
  rule preserved. Fixes a sync-invariant violation that also
  affected the pre-existing dep-remove unblock path.

### Changed

- `transport.DefaultPoolDefaults.MaxConns` lowered from 8 → 5 per
  FR-18 / MTIX-15.10. 10 active developers × 5 conns = 50, well
  within managed-PG defaults.
- `examples/hooks/pre-push` now calls `mtix sync push` (with
  `MTIX_SYNC_HOOK=1` for warn-and-skip on transient errors)
  instead of the pre-15 `mtix snapshot`. Falls back silently if
  no DSN is configured (sync is opt-in).
- `go.mod toolchain go1.26.3` — pinned for `govulncheck`-clean
  builds. Bump when stdlib CVEs land; see
  `docs/audit/MTIX-15-audit-pass2.md`.

### Security

- `govulncheck ./...` — clean against this commit. Required two
  upstream bumps: `toolchain go1.26.3` (stdlib CVEs in 1.26.1)
  and `golang.org/x/net v0.51.0 → v0.54.0`.
- DSN regression sweep covers all 11 sync subcommands plus the
  MCP tool (`cmd/mtix/sync_dsn_hygiene_test.go`).
- Panic redaction wired at `main()` (`cmd/mtix/main.go` —
  `defer redact.Recover(nil)`).
- TLS `verify-full` is the default; weaker `sslmode` allowed only
  on loopback hosts with `--insecure-tls`.
- `Source()` refuses to load a DSN from any tracked config under
  `.mtix/` — fail-closed at the earliest detectable misconfiguration.
- 22-item security audit (12 original design-audit items + 3
  penetration-style checks + 7 new HIGH requirements: 3 fuzz
  targets, VC overflow at transport, 3 panic-redaction tests).
  Evidence table in `docs/audit/MTIX-15-audit-pass2.md`.

### Migration: upgrading from v0.1.x

The local SQLite is already canonical in v0.1.x. Upgrading does
not move your tickets. The v1 → v2 schema migration runs
automatically on first command after upgrade; it adds the
`sync_events` / `sync_conflicts` / `sync_projects` /
`applied_events` tables and meta sentinels, leaving your `nodes`,
`dependencies`, `audit_log`, and `meta` rows untouched.

**To enable sync replication for an existing project:**

```bash
# 1. Upgrade the binary
go install github.com/hyper-swe/mtix/cmd/mtix@v0.2.0-beta
# or brew upgrade mtix once the formula lands

# 2. Provision a Postgres hub (Supabase / Neon / RDS / self-hosted)
#    Create a least-privilege role — see docs/SECURITY-MODEL.md
export MTIX_SYNC_DSN="postgresql://mtix_sync@hub.example.com:5432/mtix_hub?sslmode=verify-full"

# 3. Initialize the hub (single teammate, once)
mtix sync init

# 4. Backfill — synthesize sync_events from your existing nodes
mtix sync backfill --dry-run    # preview: counts what will be emitted
mtix sync backfill              # commit the synthesis

# 5. Push the backfilled events to the hub
mtix sync push

# 6. Other teammates clone the populated hub
mtix sync clone
```

**Important migration notes:**

- Backfilled events use `authorID="cli"` (the default). If two
  CLIs share that authorID and concurrently edit the same field,
  their vector clocks are `Equal()` rather than `Concurrent()` —
  the hub does NOT log a `sync_conflicts` row even though LWW
  still resolves the contention deterministically. For full
  hub-side audit-trail visibility, set distinct authorIDs per
  agent. See `docs/SECURITY-MODEL.md` →
  "Known audit-trail limitation: same-authorID conflicts".
- `mtix sync backfill` is refusal-by-default if `sync_events` is
  already non-empty. To re-backfill, run
  `mtix sync reconcile --discard-local` first.
- Un-pushed events are NOT durable across machine loss. For
  compliance-grade durability, run `mtix sync daemon` as a
  systemd / launchd service (the daemon prints the unit file via
  `--install`).
- Solo users without a hub do not need to do anything; the
  upgrade is a no-op for solo workflows.

### Known limitations

- Same-authorID concurrent edits do not produce hub-side
  `sync_conflicts` rows (LWW still converges; documented above
  and in SECURITY-MODEL.md).
- Un-pushed events on a lost machine are unrecoverable; daemon
  mitigates the window.
- Backfill emits synthetic events representing the final state of
  each node, not every intermediate state from the v0.1.x
  lifetime. Operators who need full historical event replay must
  consult the pre-upgrade `audit_log` as the authoritative
  record.

### Quality bar

- `go test -race -count=1 ./...` — 23 packages green.
- `golangci-lint run ./...` — 0 issues.
- `govulncheck ./...` — clean.
- 22-item security audit PASS;
  [evidence table](docs/audit/MTIX-15-audit-pass2.md) cites every
  test by file:line.

---

## [v0.1.5-beta] — 2026-04-26

### Changed
- Dependency bumps (goreleaser-action and related CI maintenance).
  Full diff: `git log v0.1.4-beta..v0.1.5-beta`.

## [v0.1.4-beta] — 2026-04-25

### Fixed
- MTIX-12 — canonicalize `node_type` from `depth` on export to
  match import-side semantics.

## [v0.1.3-beta] — 2026-04-13

### Added
- `mtix_briefing` MCP tool + `--format briefing` for paste-into-context
  output across agent-facing docs (FR-17).

## [v0.1.2-beta] — 2026-04-09

### Changed
- Internal task housekeeping (MTIX-8 closure).

## [v0.1.1-beta] — 2026-04-08

### Fixed
- npm dev dependency advisories (vite high-severity).

## [v0.1.0-beta] — 2026-04-01

### Added
- Initial public beta release. Solo / single-machine workflow
  with local SQLite (WAL mode) canonical store; git-tracked
  `.mtix/tasks.json` snapshot; agent-native CLI / REST / gRPC /
  MCP surfaces. Full feature inventory in README.md.

---

[Unreleased]: https://github.com/hyper-swe/mtix/compare/v0.2.0-beta...HEAD
[v0.2.0-beta]: https://github.com/hyper-swe/mtix/compare/v0.1.5-beta...v0.2.0-beta
[v0.1.5-beta]: https://github.com/hyper-swe/mtix/compare/v0.1.4-beta...v0.1.5-beta
[v0.1.4-beta]: https://github.com/hyper-swe/mtix/compare/v0.1.3-beta...v0.1.4-beta
[v0.1.3-beta]: https://github.com/hyper-swe/mtix/compare/v0.1.2-beta...v0.1.3-beta
[v0.1.2-beta]: https://github.com/hyper-swe/mtix/compare/v0.1.1-beta...v0.1.2-beta
[v0.1.1-beta]: https://github.com/hyper-swe/mtix/compare/v0.1.0-beta...v0.1.1-beta
[v0.1.0-beta]: https://github.com/hyper-swe/mtix/releases/tag/v0.1.0-beta
