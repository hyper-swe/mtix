# Changelog

All notable changes to mtix are documented in this file.

The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

> **Pre-1.0 note:** mtix is GA-quality and production-ready, but still
> pre-1.0. Minor versions may introduce breaking schema changes until
> 1.0; each ships a documented migration path in its Migration section.

---

## [0.5.0-beta] - 2026-07-20

The agent-to-agent coordination release: the FR-19 hooks/inbox foundation and FR-20 origin-independent dispatch together let agents wake and hand off work with no human message-bus — plus corruption hardening for multi-agent filesystems, multi-project support, cloud-Postgres compatibility, and two security fixes.

### Added
- **FR-19 event hooks & agent notifications (MTIX-47):** the foundation the coordination fabric is built on. A per-agent **inbox** derived from the event journal (no separate mailbox), addressed comments (`mtix comment --to <agent>`), and a **hooks engine** (`.mtix/hooks.yaml`) whose matcher fires delivery adapters — `inbox`, `webhook`, `append-file`, and `exec` — on journaled events. Includes MCP inbox tools, the `mtix hooks list|fire|log|trust` CLI, and loop prevention (via-hook guard + per-node rate limit). Kills the human-as-message-bus between agents.
- **Origin-independent hook dispatch (FR-20 / MTIX-56.1):** hooks now fire for a journaled event based only on the event being in the journal and the hook not yet having fired for it on this host — never on who wrote the event or how it arrived (local CLI, MCP, sync-arrival from the hub, another process). A durable per-`(hook, event)` **dispatch ledger** replaces the local/synced dual-cursor split: exactly-once per host across restarts, concurrent triggers and out-of-order arrival (the same ledger pattern as the MTIX-55 inbox ack fix). Crash recovery is at-least-once via a claim lease — a trigger that dies between claim and fire is re-fired, never lost; a fire that ran and failed is recorded and never auto-retried. Wake `exec` scripts should be idempotent (check the inbox before launching).
- Fresh clones and first pulls into an empty store initialize the dispatch floor at the journal tail, so bootstrapped history never fires a hook backlog storm.
- **`mtix daemon` (MTIX-56.2/56.3):** the host's first-class event dispatcher — pull-then-dispatch every 5s; fully functional with no hub (local-tail mode for cross-process writes); `mtix daemon install|status|start|stop|uninstall` registers it as an OS service (launchd / systemd --user / Task Scheduler) with boot-start and crash-restart, one service per project.
- **Global `-C, --project-dir <dir>` (MTIX-56.4):** every command can target a named project like `git -C`, applied before store init; `mcp --project` becomes a deprecated alias (`mtix mcp -C dir` unchanged).
- **Prompt-terminated delivery (MTIX-56.8):** `mtix inbox --format prompt|context` emits agent-ready text (events + ack/reply contract; empty inbox → empty output, the wake-script idempotency check); reference wake script at `examples/hooks/wake-agent.sh` launches any harness CLI with the inbox as the prompt.
- **Channel adapter (MTIX-56.7, experimental):** `mtix mcp --channel-agent <id>` also acts as a Claude Code channel (research preview) — pushes the agent's new inbox events into the running session, with ack/reply through the same server's tools, and holds an mtix presence session while serving. Requires launching Claude Code with `--channels` (or the development flag during the preview).
- Three-agent scenario regression test (planner→developer→tester) and the FR-20 cross-host release-gate e2e (exec wake exactly-once, restart-safe, crash-injection re-fire) (MTIX-56.5).
- **Multi-project in one database (MTIX-37, FR-MULTI-PROJECT):** `--project` / `--all-projects` scope on query commands, `mtix projects`, project-aware create defaulting to the primary prefix, a project argument on the MCP query tools, and a multi-project web UI (scope selector, project-aware create, NodeID badges).
- **Cloud Postgres out of the box (MTIX-48):** Neon and Supabase work as sync hubs without special configuration; connect-retry on transient errors; provider setup docs.
- **`mtix unblock <id>` recovery command (MTIX-44):** recompute a node's blocked state on demand, with blocked-lifecycle docs.

### Changed
- **Safe install/upgrade path (MTIX-56.11):** new `make install` (unlink-then-copy via `install(1)`, `PREFIX` overridable) and documented upgrade commands for the binary-download path — on macOS an in-place `cp` over an existing binary invalidates its cached code signature and every run is killed (`Killed: 9`); with the daemon installed as a service this becomes a crash loop. `mtix daemon install` output and the manual now carry the upgrade steps.
- **Exec hooks are detached spawns (MTIX-56.9):** dispatch returns at spawn and never blocks a CLI command or agent tool call. "Delivered" now means *spawned*; a script's non-zero exit is the script's own to report (best-effort logged), the inbox ack is the success signal, and `timeout-seconds` is enforced best-effort by the spawning process. Spawn failures stay terminal errors, never auto-retried.
- **Host-local exec dispatch policy (MTIX-56.10):** `mtix hooks exec-dispatch any|daemon|off` — `daemon` routes every wake through the supervised `mtix daemon` (CLI/server triggers defer entirely); `off` makes a host never launch anything while other adapters still deliver. Stored beside the trust hash, never synced.
- **`include-synced` is deprecated and now a no-op** (accepted for config compat). This is a behavior change: hooks that previously fired only on local events now also fire on sync-arrived events, deduped per host by the ledger. Fleet-level control is hook **placement**: a hook configured on N hosts fires on N hosts, once each — put a wake hook only on the host that should run it. `mtix sync daemon --dispatch-hooks` now dispatches events of every origin (no more "designated synced dispatcher").
- **Unsafe filesystems now hard-refuse writes (MTIX-54/57/58).** On a positively-identified unsafe (FUSE/network) filesystem, or when cross-context FUSE access is detected (`.fuse_hidden` orphans present even if the local FS looks safe), the store opens **read-only** — reads, `mtix recover`, and `mtix export` still work, but every write is refused at a single choke point. Both recurring field corruptions came through the old write override, so `MTIX_ALLOW_UNSAFE_FS` is **retired** (ignored with a deprecation warning). See Migration.

### Security
- **Exec trust now pins the wake-script content, not just `hooks.yaml` (MTIX-49).** The content-hash trust folded in only `hooks.yaml`, so editing a script an exec hook runs (approve, then swap the payload) executed new code with no re-consent. `mtix hooks trust` now pins the content of every local file an exec command runs; editing `hooks.yaml` *or* any referenced wake-script voids trust until re-run. Closes the approve-then-swap escalation.
- **`mtix plugin install` no longer writes through dangling symlinks — CWE-59 (MTIX-29).** The write-if-absent install paths used `os.Stat`, which reports a dangling symlink as absent, so a committed symlink in a shared repo could redirect a plugin-install write to an attacker-chosen path outside the project. The absence check now uses `os.Lstat`, so any symlink counts as present and the guard skips it.
- **Unsafe-filesystem write refusal (MTIX-58)** removes the corruption class that a shared/FUSE `.mtix` created; the override that enabled it is gone (see Changed).

### Fixed
- **Sticky `blocked` status in team setups (MTIX-44):** sync-applied status changes skipped the derived-state recompute (`unblockDependents`), so a node stayed `blocked` after its dependency closed on another machine. Sync-apply now recomputes derived state.
- **Multi-hyphen project prefixes corrupted on clone (MTIX-39):** the sync emit path mis-parsed a multi-hyphen prefix, corrupting the project column. Derivation + grammar fixed; covered by a cloud-contract case (MTIX-41).
- **Cloud-Postgres contract gate was a false-green (MTIX-42):** provider tests skipped silently when the Supabase/Neon DSN secrets were unset. The gate now runs against real cloud Postgres.
- **Stale/NULL `content_hash` on sync-applied edits (MTIX-46):** local `UpdateNode` recomputes `content_hash` on a content change (FR-3.7), but the sync-apply handlers skipped it — a synced content edit left a stale hash and a synced-created node carried a NULL hash. Since `content_hash` feeds export and import-merge identity detection (FR-7.8), replicas could diverge byte-for-byte on export and mis-detect content identity on merge (sync convergence via LWW was never affected). Sync-apply now maintains the hash.

### Migration
- **Shared-filesystem `.mtix` users must move to one local `.mtix` per machine + a sync hub.** With `MTIX_ALLOW_UNSAFE_FS` retired (MTIX-58), a `.mtix` on a FUSE/network mount now opens read-only and refuses writes — the setup that caused the field corruptions. Migrate each context to its own local store and replicate through the Postgres hub (this is exactly the FR-20 hub-per-host topology). Reads and `mtix recover` still work on the old store to get data out.
- **Re-run `mtix hooks trust` after editing any wake-script (MTIX-49),** not just after editing `hooks.yaml` — exec is now skipped until the new script content is trusted.
- **Wake `exec` hooks are now detached and their exit codes are no longer in `mtix hooks log` (MTIX-56.9);** scripts should self-report failures. **Hooks now fire on sync-arrived events (MTIX-56.4 `include-synced` no-op);** place a wake hook only on the host that should run it.

---

## [0.4.0-beta] - 2026-06-29

### Added
- **Distributed node identity & team sync (MTIX-30, ADR-003):** dot-path IDs now stay clean under concurrent and offline creation. Each node has a stable internal `uid` (its create-event id) so a renumber moves a display number without breaking references; the surface still shows only the dot-path.
  - Offline-created nodes get a provisional ID (a uid-shaped segment) and auto-settle into a clean number on the next sync (MTIX-30.3). mtix warns before a provisional ID is externalized into a commit or PR.
  - Concurrent creates of the same number auto-resolve: the hub registry (a derived partial-unique index) accepts the first and tells the second to renumber. Both nodes survive — this fixes MTIX-28 (concurrent create silently losing one node) (MTIX-30.4 / 30.7).
  - Subtree renumber is atomic: one transaction rewrites the node and all descendants, so no read sees a torn subtree (MTIX-30.5).
  - Restore-from-backup safety: the rare settled-vs-settled collision is never auto-picked. `mtix sync mark-restored` (operator-only) opens a restore window; `mtix sync collisions list` and `mtix sync collisions resolve <id> --winner held|incoming` let an admin choose which node keeps the number while the other renumbers. No node is ever lost, and only the affected node is blocked — the rest of the team keeps syncing (MTIX-30.8).
  - `mtix sync migrate` drives the one-time migration (uid backfill, hub dedup sweep, version-gated registry index); idempotent and a no-op once complete (MTIX-30.9, 30.10, 30.14).
  - New safety scenarios 12–18 in `docs/traceability.json` (restore Option B, same-epoch no-false-positive, atomic renumber, import uid validation, crash-resilience, ENOSPC on a sync write, online concurrent-create) are gated by `traceability_test.go`. Design and audit rationale in [ADR-003](ADR-003-DISTRIBUTED-NODE-IDENTITY.md); operator docs in the USERMANUAL "Distributed identity & team sync" section; trust model in `docs/SECURITY-MODEL.md`.
- **Codex and pi plugin targets (MTIX-27, issue #15):** `mtix plugin install --target codex` writes the project's AGENTS.md briefing and a `[mcp_servers.mtix]` entry in `.codex/config.toml` (`--global` for `~/.codex/`); existing files are never modified — the stanza to add is printed instead. `--target pi` installs AGENTS.md (which pi loads natively; `--global` for `~/.pi/agent/`) and prints pi-mcp-adapter setup guidance, since pi has no built-in MCP by design. New `docs/mcp-config/codex.toml` snippet; MCP-SETUP sections for both agents.

### Changed
- `mtix plugin install` help no longer advertises cursor/windsurf targets that were never implemented (manual MCP config for those remains documented in MCP-SETUP).

---

## [v0.3.0-beta] — 2026-06-11

**Headline: storage durability hardening (NFR-2.8) — refuse, mirror, back up, recover.**
Driven by a field incident in which a database was torn by a WAL
checkpoint on a 99%-full disk and the data was unrecoverable. mtix now
refuses work it cannot finish safely, keeps the tasks.json mirror current
on every interface, takes automatic verified backups, and ships a
first-class salvage path — with a fault-injection suite proving all of it
on every CI build. No schema migration: the database schema version is
unchanged; v0.2.0-beta projects open directly.

**Notable behavior changes:**
- Writes are refused below an 8 MiB free-disk floor (`MTIX_MIN_FREE_BYTES`
  to tune, `0` disables); reads keep working.
- Automatic rolling backups are ON by default (daily, keep 7, under
  `.mtix/data/backups/`); disable with `MTIX_BACKUP_INTERVAL=0`.
- Corrupted databases are refused at open with recovery guidance and
  exit code 4; disk-full failures exit 3.

### Added
- **Disk-full safety (NFR-2.8, MTIX-26):** free-space pre-flight before every write transaction and backup (`MTIX_MIN_FREE_BYTES`, default 8 MiB floor); fail-stop latch on fatal storage errors (disk full, I/O error, detected corruption) — mtix refuses further writes instead of continuing into undefined state; database-open failures on packed volumes now name disk pressure instead of a bare `SQLITE_CANTOPEN`.
- **Integrity check on open (NFR-2.6a, MTIX-26.4):** truncated database files (in-header page count exceeding file size with no WAL to replay) are refused *before* the first connection opens, preserving recovery evidence; `PRAGMA quick_check` runs before any write on every open. `MTIX_SKIP_INTEGRITY_CHECK=1` is the documented recovery-tooling escape hatch (bypasses both gates, with a DANGER log).
- **Mirror parity for long-running interfaces (FR-15.3, MTIX-26.1):** mutations made through the MCP server, `mtix serve`, and `mtix sync daemon` now update the `.mtix/tasks.json` mirror via a debounced store on-commit hook — previously only CLI commands exported, leaving agent-driven projects without the redundancy layer.
- **`mtix recover` + `import --recompute-checksum` (MTIX-26.5):** salvage readable rows from a damaged database read-only (per-row reads, `cell_size_check=OFF`), merge unreadable rows from the tasks.json mirror, synthesize placeholder parents, and emit an importable export plus a salvage report — without modifying the damaged files. `--recompute-checksum` (loudly) accepts hand-reconstructed exports.
- **Automated rolling backups (MTIX-26.6):** verified snapshots into `.mtix/data/backups/` on the post-mutation cadence (default daily, keep 7; `MTIX_BACKUP_INTERVAL` / `MTIX_BACKUP_RETAIN`); failures log and never fail the command or disturb existing backups.
- **Structured exit codes (MTIX-26.8):** `3` = disk full, `4` = corrupted, `1` = generic — CLI contract documented in USERMANUAL and asserted by the fault-injection suite.
- **Claims-to-test traceability gate (MTIX-26.8):** `docs/traceability.json` maps every QUALITY-STANDARDS §3.6 safety scenario to test functions; `traceability_test.go` fails the build when a declared scenario lacks a linked existing test.
- **Fault-injection conformance suite (MTIX-26.7):** `e2e/faultinject` drives the real binary through disk-full writes, genuine ENOSPC, kill -9 mid-write, the 2026-05-19 field-incident signature, and a full recover round trip, on a dedicated tiny volume; runs on every CI build (`test-fault-injection` job). Local harness: `scripts/faultfs.sh`.
- **ADR-002 (MTIX-26.9):** records the decision to not add a local event journal or content-addressed bodies now, with revisit triggers.

- **Release process (MTIX-22):** `docs/RELEASE-CHECKLIST.md` run before every tag; all four deferred post-MTIX-15 audit findings dispositioned (`docs/audit/MTIX-22-deferred-dispositions.md`); auto-generated CLI reference regenerated.

### Changed
- Write connections now set `PRAGMA synchronous = FULL` and `PRAGMA wal_autocheckpoint = 1000` explicitly instead of relying on driver defaults (MTIX-26.3); ADR-001's stale `synchronous=NORMAL` reference corrected.

### Security
- Go toolchain pinned to go1.26.4: fixes two reachable standard-library issues (net/textproto error escaping via the MCP stdio reader; crypto/x509 hostname-parsing inefficiency on the HTTPS serve path). `govulncheck`: 0 reachable vulnerabilities.
- Web dev-dependency advisories resolved (`npm audit`: 0 vulnerabilities).

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
