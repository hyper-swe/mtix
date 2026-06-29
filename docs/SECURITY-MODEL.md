# mtix Security Model

> **Document version:** 1.2
> **Applies to:** mtix v0.2.x (SQLite local store + optional BYO Postgres sync hub, FR-18)

This document is the security and trust contract for mtix. It tells you what mtix protects against and — equally important — what it does not. Read it before adopting mtix in any environment beyond a single developer's laptop.

If a guarantee in this document conflicts with what the code actually does, **the code is the bug**. File a security advisory.

---

## Audience and scope

This document covers two operating modes:

1. **Solo mode (default):** one developer or one machine using mtix locally. Canonical store is `.mtix/data/mtix.db` (SQLite). Concurrent agents on the same machine share that one DB. Cross-machine sharing via git-tracked `.mtix/tasks.json`.
2. **Sync mode (FR-18, optional):** a small trusted team sharing one BYO Postgres hub for event replication. **The local SQLite remains the canonical store on every CLI.** Postgres is a hub for events, not a canonical store. Each CLI emits events locally, pushes them to the hub, and pulls others' events.

It does **not** cover:

- A multi-tenant SaaS where multiple unrelated tenants share infrastructure (separate roadmap; would require an mtix server with row-level security and per-tenant identity).
- A hosted PG offering by HyperSWE (separate roadmap).
- Use of mtix in adversarial open-source contexts where the team is not mutually trusted.

### Storage layering (important)

| Layer | Role | Persistence | Authority |
|---|---|---|---|
| `.mtix/data/mtix.db` (SQLite) | Canonical local store | Survives across runs; backed up to `.mtix/tasks.json` | Source of truth |
| `.mtix/tasks.json` | Git-tracked snapshot | Survives across machines via git | Human-readable view; sentinel hashes detect drift |
| BYO Postgres hub (sync mode) | Replication mechanism | Receives `sync_events`; replays to other CLIs | NOT a canonical store; treat as a mailroom |

If the hub is wiped, every CLI keeps its local SQLite intact. If a CLI's SQLite is destroyed without push, those events are lost (see "Lost-laptop recovery" below).

---

## Trust model

### Who is trusted

| Party | Trust level | Why |
|---|---|---|
| The local user running `mtix` | Full | They own the machine and the DB |
| Any agent / process on a trusted machine | Full | If the machine is trusted, processes on it are trusted |
| In sync mode: anyone with the hub DSN | Full | The DSN is a credential equivalent to root in the sync hub |
| Other team members in sync mode | Full | A team's sync hub is shared like a team's git repo — by membership, not isolation |

### Who is NOT trusted

| Party | Why mtix doesn't trust them | What protects against them |
|---|---|---|
| Network adversaries (sniff/MitM) | Could read or modify in transit | TLS verify-full when connecting to the hub |
| Compromised git server | Could tamper with `tasks.json` | SQLite is canonical; `tasks.json` is a derived snapshot with sentinel-hash drift detection |
| Anyone without the hub DSN | Not authenticated | PG-level authentication is the gate |
| Other tenants (multi-tenant scenario) | Not in scope | Use the future hosted SaaS, not a shared sync hub |

### Plain-English consequences

- If any team member's laptop is compromised in sync mode, **all task data is at risk**. There is no per-user data isolation within a single mtix instance.
- If the PG provider (Supabase, Neon, RDS, your DBA) is compromised, **the hub data is compromised**. Each CLI's local SQLite remains intact, but the attacker can read every event ever pushed.
- Your team's hub is exactly as private as the PG instance hosting it. Treat the DSN like a production database credential.

---

## Threat model

| # | Threat | What mtix does about it | Residual risk | What you do |
|---|---|---|---|---|
| 1 | **Credentials in git** (DSN committed by accident) | mtix refuses to load DSN from any tracked config file. DSN must come from `MTIX_SYNC_DSN` env var or `.mtix/secrets` (gitignore-enforced, mode 0600). | Low — fail-closed at config load | Use a secrets manager or env var; never paste DSN into a yaml that gets committed |
| 2 | **MitM on PG connection** (network adversary reads/modifies traffic) | mtix defaults to `sslmode=verify-full` and refuses `sslmode=disable` unless explicit `--insecure-tls` flag is set AND host is localhost. | Low if `verify-full` is honored end-to-end | Use a managed PG provider that enforces TLS; verify the root CA matches |
| 3 | **SQL injection** (malicious filter values) | All store and transport SQL uses bound parameters. Audited in MTIX-9.1 (FR-17.1) for the SQLite driver and MTIX-15.3 / MTIX-15.11 audit pass 2 for the PG transport (`TestSQLInjection_AttackPatternsHandledSafely`). | Very low — depends on no future regression | Run the parameterization regression tests on every change |
| 4 | **Insider mutation tampering** (compromised team member edits/deletes data via mtix) | Append-only `audit_log` table records every mutation atomically. PG triggers prevent `UPDATE`/`DELETE` on audit rows. | Medium — superuser can disable triggers; insider with write access can still create or modify nodes | Use least-privilege PG roles; archive `audit_log` to immutable cold storage for true tamper evidence |
| 5 | **Audit log tampering** (DBA edits or deletes audit rows) | Triggers raise exception on `UPDATE`/`DELETE`. WAL archival recommended for safety-critical adopters. | Medium — PG superuser bypasses triggers | For tamper evidence, ship `audit_log` to an external append-only store (S3 with object lock, immudb, etc.) |
| 6 | **Hook bypass** (`git push --no-verify` or running on a machine without the hook) | Client-side hooks are advisory. Server-side enforcement (pre-receive on self-hosted git, GitHub Action on github.com) is the only real gate. | Medium — depends on team policy | If `tasks.json` freshness matters, deploy the example pre-receive hook or GitHub Action |
| 7 | **PG provider compromise** (Supabase / Neon / RDS hosting layer breached) | Out of mtix's scope. mtix data is exactly as safe as the PG instance hosting it. | High — depends on provider | Pick a provider you trust; encrypt sensitive task content client-side if needed |
| 8 | **DoS via mutation spam** (script writes millions of nodes) | mtix has no per-actor rate limit. Configurable retention on `audit_log` prevents unbounded growth there. | Medium — relies on PG-side controls | Set PG-side rate limits; configure `audit_log` retention policy |
| 9 | **Replay or forgery of mutation history** (forged actor field in `audit_log`) | mtix CLI populates `actor` from local config. A compromised CLI can write any actor name. | Medium — logical identity, not enforced at PG layer | Use PG-level user accounts as the real authentication; trust only what `pg_stat_activity` reports |
| 10 | **Denial of read access via PG exhaustion** | Connection pool with limits; document pgbouncer for >5 users | Low — operational concern | Pool sizing per the workflow doc |

---

## Sync hub trust model (FR-18)

The sync hub is a replication mechanism, not a canonical store. Events flow CLI → hub → other CLIs. Convergence is by deterministic LWW at apply time, not by hub authority.

### DSN handling

1. **`MTIX_SYNC_DSN` env var** — production-preferred path. Lives in the environment, never on disk.
2. **`.mtix/secrets`** — file-mode 0600 is enforced; `Source()` refuses looser modes. Auto-gitignored by `mtix sync init`.
3. **Tracked config files** (`.mtix/config.{yaml,yml,json}`) — `Source()` scans for DSN-shaped keys and **refuses to proceed** if any are present. Fail-closed at the earliest detectable misconfiguration.

Every error string that may contain a DSN passes through `redact.DSN` before reaching stderr, MCP output, or panic traces. `cmd/mtix/main.go` wraps `main()` with `defer redact.Recover(nil)` so panics with a DSN in scope are redacted before the runtime printer sees them.

### TLS posture

- `verify-full` is the default. `EnforceTLSPosture` defaults the DSN's sslmode to verify-full when omitted.
- Weaker `sslmode` is allowed **only** on loopback hosts (`localhost`, `127.0.0.1`, `::1`) and **only** when `--insecure-tls` is set explicitly.
- `MTIX_SYNC_SSLROOTCERT` populates `sslrootcert` for managed-PG providers that require a CA bundle.

### Hub trust boundary

Writes to the hub authenticate via PG-level user accounts. The pre-flight validator (`internal/sync/validator`) enforces schema, payload size, JSON depth, lamport overflow, vector-clock cap, and timestamp grace **before any PG round-trip**. Malformed or oversized events never reach the hub.

### Convergence (LWW)

Replicas converge deterministically by `(lamport_clock, wall_clock_ts, author_machine_hash)` — lowest machine_hash wins on a tie. Apply-time LWW (`internal/store/sqlite/sync_apply.go`) keeps every CLI on the same state regardless of push/pull order.

### Audit trail invariants

- `audit_log` table has a PG trigger that raises on `UPDATE` or `DELETE`. Append-only by construction.
- `sync_conflicts` table is similarly append-only; manual conflict resolutions INSERT a new row with `resolution='manual'` that supersedes the LWW row.
- Schema migration is single-flight via `pg_advisory_xact_lock(AdvisoryLockKey)`. Concurrent Migrate calls from 10 CLIs all return cleanly; only one runs the schema work.

### Known audit-trail limitation: same-authorID conflicts

The default `authorID` for emitted events is `"cli"` (the `authorIDFallback` constant in `internal/store/sqlite/sync_emit.go`). Vector clocks are keyed by authorID, so two CLIs sharing the same authorID that edit the same field concurrently produce vector clocks that are `Equal()` rather than `Concurrent()`. The hub-side conflict detector (`detectConflicts` in `push_pull.go`) only INSERTs `sync_conflicts` rows for events whose VCs are `Concurrent()` — same-authorID concurrent edits are therefore **not recorded in the hub conflict log**.

This is an intentional design tradeoff:

- **Correctness is preserved.** Apply-time LWW (keyed by `lamport`, `wall_clock_ts`, `author_machine_hash`) converges every replica deterministically. There is no divergence in the resulting node state.
- **Audit-trail visibility is reduced.** Operators relying on `sync_conflicts` for traceability of contested edits will not see same-authorID conflicts.
- **The unit of causal isolation is the agent, not the machine.** Two agents on the same machine that share `authorID="cli"` are causally indistinguishable by design. To recover hub-side conflict logging, set distinct authorIDs per agent.

If your safety profile requires complete hub-side audit of every contested edit, set unique `authorID` per agent process; the CLI does not yet expose a flag to override (planned). For now, document the tradeoff in the team's runbook.

### Restore-epoch trust model (MTIX-30 / ADR-003)

The distributed-identity feature adds one safety-critical trigger: the
settled-vs-settled restore collision (ADR-003 §6.1, "Option B"). Reaching it
blocks a node and pulls an admin into a resolution queue, so what is allowed to
arm it matters.

**The trigger is the operator's epoch bump, and only that.** The hub keeps a
monotonic `restore_epoch`, advanced *only* by an explicit operator action
(`mtix sync mark-restored`). No client or push path can advance it. Each
accepted `create_node` is hub-stamped with the current epoch at acceptance —
hub-side, never client-asserted. A restore collision is detected only when a
held create in the current epoch contests an incoming claim from an earlier era
(ADR-003 Addendum A).

**A client "previously-settled" flag was considered and rejected** on security
review. It would put a forgeable, client-asserted signal on the trigger of a
safety-critical path. A compromised client (poor hygiene even under the
trusted-team contract) could set it to fabricate restore collisions —
escalating ordinary creates into the admin queue (an availability nuisance of
blocked nodes) and, at worst, social-engineering an admin into renumbering a
legitimate ticket. That is recoverable (the uid is stable, no node is lost) but
it breaks external references and wastes trust. The operator epoch bump avoids
it: it is a deliberate, supervised action a client cannot manufacture.

**Calibration:** under the trusted-team contract, a compromised client
**cannot trigger Option B during normal operation**. With no restore there is
no epoch advance, so the Option-B path is closed and every collision takes the
ordinary auto-renumber path (a liveness event, no admin). The attack window
shrinks to the operator-supervised interval right after a restore. Within that
window resolution stays human-gated: no auto-pick, the older-claim default is
advisory only (audit F-5), and the loser renumbers via `Store.RenumberSubtree`
without deleting any create event — so no node is ever lost.

The registry referee itself is **liveness, not a security boundary**: a broken
or hostile hub can at worst force a renumber; it cannot lose or corrupt a node,
because each CLI keeps its canonical local SQLite (ADR-003 §9). A `uid` is an
identifier, not a secret or capability; nothing treats knowing it as
authorization.

## Lost-laptop recovery

Un-pushed events are **local-only and not durable across machine loss**. The local sync_events queue carries `sync_status='pending'` rows until `mtix sync push` drains them.

Procedure when a CLI machine is lost:

1. On every surviving CLI, run `mtix sync status` — pending count of 0 means all your in-flight events are already on the hub.
2. The lost machine's pending events (if any) are unrecoverable.
3. Provision the replacement machine. Run `mtix sync clone DSN` to rebuild local state from the hub event log. Replay is idempotent (per `applied_events` dedupe).

The hub-unreachable detector (`internal/sync/workflow`) surfaces this risk: when `meta.sync.consecutive_errors ≥ 3`, the `mtix_sync_workflow` MCP tool reports state `hub-unreachable` and recommends `mtix sync doctor`. Operators who care about durability across machine loss must push frequently OR run `mtix sync daemon` for periodic auto-push.

## Queue-full handling

`sync.max_queue_size` in `meta` caps the pending queue size. When the cap is reached, `enforceQueueLimit` returns `model.ErrSyncQueueFull` from the emit path. **The new event is refused — not silently dropped.** Surfaced to the caller of `mtix create` / `mtix update` / etc. as a structured error mentioning the cap and the remediation (`mtix sync push --force` or raise the cap).

Default cap is `0` (unlimited). Set explicitly via the `sync.max_queue_size` meta key for environments where unbounded local growth is unacceptable.

---

## What sync mode protects against

- **Network adversaries between CLI and hub** — mandatory TLS verify-full; weaker sslmode only on loopback.
- **DSN leakage in logs / errors / MCP output / panic traces** — every error string passes through `redact.DSN`; `defer redact.Recover` wraps `main()` for panic paths. Sentinel-based regression sweep covers all 10 FR-18 sync commands plus the MCP tool.
- **Malformed events reaching the hub** — pre-flight validator runs before any PG round-trip; rejects schema violations, oversized payloads, deep JSON, lamport overflow, VC overflow, future timestamps beyond grace.
- **Audit log tampering** — PG triggers raise on UPDATE/DELETE of `audit_log` and `sync_conflicts`.
- **Replay of pushed events** — `applied_events.event_id` is the dedupe key. Replay is a no-op.
- **Migration race** — `pg_advisory_xact_lock` single-flights schema work across concurrent CLIs.
- **Silent data loss in the queue** — queue-full returns `ErrSyncQueueFull`; events never silently dropped.

## What sync mode does NOT protect against

- **Compromised team members** — anyone with the DSN can read and write everything. mtix does not partition data per-user within a single instance.
- **Compromised PG provider** — the provider has full access to hub data.
- **PG superuser disabling triggers** — `audit_log` tamper-resistance assumes triggers are not bypassed.
- **Lost un-pushed events** — see "Lost-laptop recovery" above. The hub never sees an event until `push` succeeds.
- **Forged author identity** — `author_id` is a logical identifier from the CLI. PG-level user accounts are the real authentication boundary.
- **Bypassed git hooks** — `git push --no-verify` or absence of installed hooks defeats the pre-push sync. Use server-side enforcement for safety-critical teams.
- **Same-authorID audit-trail completeness** — see "Known audit-trail limitation: same-authorID conflicts" above.

---

## Security checklist for adopters

Before going live with sync mode, verify each of these:

- [ ] Connection uses `sslmode=verify-full` (test: `mtix sync doctor` reports PG reachable AND schema current).
- [ ] Connection uses a server certificate signed by a trusted CA (test: `MTIX_SYNC_SSLROOTCERT` set if managed PG requires it).
- [ ] DSN is stored in `MTIX_SYNC_DSN` env var or `.mtix/secrets` (gitignored, mode 0600). **Not** in any tracked config file (`Source()` will refuse to load if it detects one).
- [ ] `.mtix/secrets` is in `.gitignore` (test: `git check-ignore .mtix/secrets` succeeds; `mtix sync init` installs the rule automatically).
- [ ] PG role used by the hub is **least privilege**: SELECT/INSERT on `sync_events`, `sync_conflicts`, `sync_projects`, `applied_events`, `audit_log` only. Not SUPERUSER, not CREATEDB, not REPLICATION.
- [ ] `audit_log` and `sync_conflicts` triggers are in place (test: `UPDATE audit_log SET ...` raises exception).
- [ ] Backup procedure for the hub is in place AND has been tested to restore (use `mtix sync backup --output FILE` for the mtix-owned tables).
- [ ] DR runbook tested: rebuild a CLI from a fresh `mtix sync clone DSN`.
- [ ] At least one of: client-side pre-push hook installed across all team machines (`examples/hooks/pre-push` calls `mtix sync push`), OR server-side enforcement.
- [ ] If durability across machine loss matters: `mtix sync daemon` is running as a systemd/launchd service on each developer's machine (push interval ≤ 30s recommended).
- [ ] All team members have read this document and understand the trust model and the same-authorID limitation.
- [ ] If running pgbouncer in front of PG, it is in **session mode** (not transaction mode — mtix uses advisory locks that transaction mode breaks).

---

## Reporting security issues

Open a [GitHub Security Advisory](https://github.com/hyper-swe/mtix/security/advisories/new) on the repo. Do not file public issues for vulnerabilities.

mtix is a pre-funding open-source project. Triage is best-effort. Critical issues get patched within days; lower-severity issues may take longer. There is no formal SLA.

---

## Document version history

| Version | Date | Change |
|---|---|---|
| 1.0 | unreleased | Drafted alongside MTIX-14 BYO Postgres rollout (canonical-store framing; never shipped) |
| 1.1 | 2026-05 | MTIX-15 sync hub trust model: hub is replication, not canonical; DSN handling via redact + Recover; LWW convergence; same-authorID audit-trail tradeoff documented; lost-laptop and queue-full procedures |
| 1.2 | 2026-06 | MTIX-30 / ADR-003 restore-epoch trust model: operator-gated epoch is the un-forgeable Option-B discriminator; client "previously-settled" flag rejected as a forgeable signal on a safety-critical trigger; calibration that a compromised client cannot reach Option B in normal operation |

Changes that alter the trust model (adding/removing a guarantee, adding a new threat) require a documented version bump and a corresponding `CHANGELOG.md` security note.
