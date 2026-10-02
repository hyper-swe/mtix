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
| `.mtix/tasks.json` | Git-tracked snapshot | Survives across machines via git | Plain-text view; sentinel hashes detect drift |
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
| 2 | **MitM on PG connection** (network adversary reads/modifies traffic) | mtix defaults to `sslmode=verify-full` and refuses `sslmode=disable` unless explicit `--insecure-tls` flag is set AND every host the connection may use is loopback or a local Unix-domain socket. | Low if `verify-full` is honored end-to-end | Use a managed PG provider that enforces TLS; verify the root CA matches |
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
2. **`.mtix/secrets`** — file-mode 0600 is enforced; `Source()` refuses looser modes. It must be a regular file (a symlink to one is followed) of at most 64 KiB. Auto-gitignored by `mtix sync init`.
3. **Tracked config files** (`.mtix/config.{yaml,yml,json}`) — `Source()` scans for DSN-shaped keys and **refuses to proceed** if any are present. Fail-closed at the earliest detectable misconfiguration.
4. **Command line** — positional DSN arguments are no longer accepted; set `MTIX_SYNC_DSN` or `.mtix/secrets`.

Every error string that may contain a DSN passes through `redact.DSN` before reaching stderr, MCP output, or panic traces. A DSN that is not a valid `postgres://` or `postgresql://` URL, or whose connection settings the driver cannot parse, is reported with a fixed message that quotes none of it. Sync command errors and warnings, `mtix sync doctor` details (text and `--json`) and pg_dump's messages from `mtix sync backup` are shown with the configured DSN (from `MTIX_SYNC_DSN` or `.mtix/secrets`, read as `Source()` reads it) and its password removed; a password shorter than 6 characters is removed where it appears as a password (`:password@` or `password=`). The CLI's final error line is scrubbed the same way, also for flag and argument errors inside a project, before the project is opened. `cmd/mtix/main.go` wraps `main()` with `defer redact.Recover(nil)` so panics with a DSN in scope are redacted before the runtime printer sees them.

### TLS posture

- `verify-full` is the default. `ApproveDSN` (which `EnforceTLSPosture` wraps) defaults the DSN's sslmode to verify-full when omitted.
- The DSN is parsed once, by `ApproveDSN`, with PG* environment variables and any service file merged. The posture rule is checked on that parsed configuration, every host and fallback included, and the connection pool opens from the same configuration. Under `verify-full` every network host verifies the server certificate against its own name.
- For each network host, that check also loads the certificate files the driver uses for a TLS connection: the CA file whenever `sslrootcert` names one (from the DSN, `MTIX_SYNC_SSLROOTCERT`, `PGSSLROOTCERT`, a service file, or the driver's default `root.crt` when that file exists and nothing else names a CA file), and the client certificate and key (`sslcert`, `sslkey`; by default `postgresql.crt` and `postgresql.key`, used only when both exist) whenever the sslmode is not `disable`. The driver's default directory is `~/.postgresql/` (`%APPDATA%\postgresql\` on Windows). A DSN whose hosts are all local Unix-domain sockets loads none of them. A file the check loads that cannot be read, or that holds no usable certificate or key, stops the command before any network contact, with a fixed message that quotes neither the path nor the DSN.
- Weaker `sslmode` is allowed **only** when `--insecure-tls` is set explicitly **and** every host the connection may use, fallback hosts included, is loopback (`localhost`, `127.0.0.0/8`, `::1`) or a local Unix-domain socket.
- `MTIX_SYNC_SSLROOTCERT` populates `sslrootcert` for managed-PG providers that require a CA bundle.

### Hub trust boundary

Writes to the hub authenticate via PG-level user accounts. The pre-flight validator (`internal/sync/validator`) enforces schema, payload size, JSON depth, lamport overflow, vector-clock cap, and timestamp grace **before any PG round-trip**. Malformed or oversized events never reach the hub.

## Relay transport trust model (FR-21)

The relay is a second sync transport: the same events, carried through a
shared directory instead of a database connection. Everything below is a
*delta* to the hub model above — the event model, convergence and audit
invariants are unchanged.

### The trust boundary moves

On the hub, a server the team controls authenticates writers and
validates events before they land. A shared directory authenticates
nobody. Anything that can write the folder can write bytes that look
like events, so the reader must assume the medium is hostile and check
everything itself:

- Every record carries its own length, CRC32C and MAC, and is verified
  at read time. A reader never needs the medium's cooperation — or the
  file's completeness — to know what is true.
- The MAC binds the record's position and epochs, so a genuine record
  replayed into a different position, file, peer or key epoch fails
  authentication. That closes reorder, replay and rollback in one check.
- Every rule the hub's server-side validator applies runs locally
  before a record is applied. On a relay there is no server, so the
  reader is the validator.
- Paths are resolved without following symlinks, and any symlink under
  the relay directory is refused rather than followed.

### The shared key authenticates the fleet, not the peer

The relay's authentication uses one key shared by every peer. Stated
plainly, because it is easy to assume otherwise: **that key
authenticates the fleet, not the individual peer.** Any key-holder can
publish under any peer's identity. What it stops is everything outside
the fleet — and on a folder that other software can write, that is the
threat worth stopping. Per-peer asymmetric identity is the designated
next step and is reserved in the record format; it is not in this
version.

The practical consequence: treat the relay key exactly as you treat a
database password. It lives outside the shared directory, readable only
by its owner, and it travels between machines out of band. A leaked key
is remediated the same way a leaked connection string is — rotate it.
Rotation re-keys forward from a chosen point and records the boundary,
so history published under the old key stays verifiable while new work
uses the new one; keep the old key installed until retention has passed
the boundary, or a reader will read valid history as forged.

### Integrity is defensible here; availability is not

Anyone who can write the directory can also delete or truncate it. That
is not defended against, and pretending otherwise would be worse than
saying so: the design makes such damage **loud and recoverable** rather
than silent. A reader that meets a gap or damaged bytes stops and says
so instead of skipping past — a stalled peer is strictly better than a
peer that quietly dropped an event nobody can name. And every file in
the relay is a projection: the events themselves live in each peer's own
store, so a wiped directory costs delivery time, never data.

The same reasoning applies to the relay's own bookkeeping. A peer's
published read position is advisory: if it is damaged, that peer
re-derives its true position locally and rewrites it, and the only cost
is that other peers keep history slightly longer than they needed to.
Nothing on the medium is trusted to be authoritative about anything.

### Content is authenticated, not encrypted

Relay records carry the team's full event content — titles, comments,
prompts — signed but in the clear. A relay placed inside a
third-party-synced folder therefore ships that content to that provider.
`mtix sync doctor` warns when the relay path looks like one. If you need
confidentiality at rest, put the relay on an encrypted volume; that is
the boundary where it belongs, and mtix does not attempt it in the
record format.

A bootstrap snapshot — the file a joining peer imports to catch up — is
a full plaintext copy of a store. It is removed once every peer has read
past it, and doctor flags one left behind.

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

If your safety profile requires complete hub-side audit of every contested edit, give each agent process a distinct identity (MTIX-24): set `MTIX_AUTHOR_ID` in the agent's bootstrap environment — recommended, set once per process — or the `author_id` key in `.mtix/config.yaml` for a per-project default. Both are validated against the FR-18.7 grammar (`^[a-z0-9_-]{1,64}$`; an explicitly-set invalid value is rejected loudly) and take precedence over the `"cli"` fallback, so distinct-identity agents' concurrent edits are `Concurrent()` and are recorded in `sync_conflicts`. The env var wins per process, so it distinguishes agents even if they were pointed at one `.mtix`.

### Restore-epoch trust model (MTIX-30 / ADR-003)

The distributed-identity feature adds one safety-critical trigger: the
settled-vs-settled restore collision (ADR-003 §6.1, "Option B"). Reaching it
blocks a node and pulls an admin into a resolution queue, so what is allowed to
arm it matters.

**The trigger is the operator's epoch bump, and only that.** The hub keeps a
monotonic `restore_epoch`, advanced *only* by an explicit operator action
(`mtix sync mark-restored`), which runs as the table owner. No client or push
path can advance it. Each accepted `create_node` is hub-stamped with the
current epoch at acceptance — hub-side, never client-asserted: a trigger on
`sync_events` sets the epoch of every inserted event from `sync_hub_state`,
whatever value the inserting session supplies. A restore collision is recorded
only when the create that holds the number was stamped in an epoch earlier than
the current one, and the hub checks that itself: collisions are recorded by the
hub function `record_restore_collision`, which runs as the table owner and reads
the held create and both epochs from the hub (ADR-003 Addendum A).

**A client "previously-settled" flag was considered and rejected** on security
review. It would put a forgeable, client-asserted signal on the trigger of a
safety-critical path. A compromised client (poor hygiene even under the
trusted-team contract) could set it to fabricate restore collisions —
escalating ordinary creates into the admin queue (an availability nuisance of
blocked nodes) and, at worst, social-engineering an admin into renumbering a
legitimate ticket. That is recoverable (the uid is stable, no node is lost) but
it breaks external references and wastes trust. The operator epoch bump avoids
it: it is a deliberate, supervised action a client cannot manufacture.

**Calibration:** under the trusted-team contract, a compromised client whose
DSN names a syncing role with the least-privilege list (see the checklist
below) **cannot trigger Option B during normal operation**: it cannot advance
the epoch, set an event's epoch, or insert a collision row. A DSN that names
the table owner can change any hub table (see "What sync mode does NOT protect
against"). With no restore there is no epoch advance, so the Option-B path is
closed and every collision takes the ordinary auto-renumber path (a liveness
event, no admin). When a restore collision is recorded, resolution stays
gated on a user's decision: no auto-pick, the older-claim default is advisory only (audit
F-5), and the loser renumbers via `Store.RenumberSubtree` without deleting any
create event — so no node is ever lost.

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
3. Provision the replacement machine. Set `MTIX_SYNC_DSN` (or `.mtix/secrets`) and run `mtix sync clone` to rebuild local state from the hub event log. Replay is idempotent (per `applied_events` dedupe).

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
- **Forged, altered or relocated relay records** — every record is MAC'd over its own position and epochs; a tampered, replayed or moved record fails authentication and the reader stops rather than applying it.
- **Silent gaps in a relay stream** — records are strictly sequential per peer; a missing or repeated position stalls the reader loudly instead of being skipped.

## What sync mode does NOT protect against

- **Compromised team members** — anyone with the DSN can read and write everything. mtix does not partition data per-user within a single instance.
- **Compromised PG provider** — the provider has full access to hub data.
- **PG superuser disabling triggers** — `audit_log` tamper-resistance assumes triggers are not bypassed.
- **Lost un-pushed events** — see "Lost-laptop recovery" above. The hub never sees an event until `push` succeeds.
- **Forged author identity** — `author_id` is a logical identifier from the CLI. PG-level user accounts are the real authentication boundary.
- **Bypassed git hooks** — `git push --no-verify` or absence of installed hooks defeats the pre-push sync. Use server-side enforcement for safety-critical teams.
- **Same-authorID audit-trail completeness** — see "Known audit-trail limitation: same-authorID conflicts" above.
- **Peer impersonation within a relay fleet** — the shared relay key authenticates the fleet, not the peer; any key-holder can publish under any peer's identity. See "Relay transport trust model" above.
- **Deletion or truncation of relay files** — anyone who can write the shared directory can destroy its contents. The damage is loud and recoverable (every relay file is re-derivable from a peer's own store), but it is not prevented.
- **Confidentiality of relay content** — records are authenticated, not encrypted. A relay inside a third-party-synced folder ships the team's event content to that provider.
- **Prompt injection over the inbox (FR-20)** — with origin-independent dispatch, an addressed comment written by ANY hub participant can cold-start a worker (exec wake) or be pushed into a live agent session (channel push), landing verbatim in that agent's context. Addressed comments are prompt input: only federate the hub with agents and users you trust with that authority — the DSN is effectively the right to speak into every agent's prompt. Mitigations already in place: `exec` runs only for a locally trusted `hooks.yaml` (content hash, per host — placement is designation), commands are argv-only with event data confined to env/stdin, per-node rate limits and via-hook loop guards bound runaway firing, and a fire that errors is never auto-retried.

---

## Security checklist for adopters

Before going live with sync mode, verify each of these:

- [ ] Connection uses `sslmode=verify-full` (test: `mtix sync doctor` reports PG reachable AND schema current).
- [ ] Connection uses a server certificate signed by a trusted CA (test: `MTIX_SYNC_SSLROOTCERT` set if managed PG requires it).
- [ ] DSN is stored in `MTIX_SYNC_DSN` env var or `.mtix/secrets` (gitignored, mode 0600). **Not** in any tracked config file (`Source()` will refuse to load if it detects one).
- [ ] `.mtix/secrets` is in `.gitignore` (test: `git check-ignore .mtix/secrets` succeeds; `mtix sync init` installs the rule automatically).
- [ ] PG role used by the hub is **least privilege**: not SUPERUSER, not CREATEDB, not REPLICATION. A role that syncs without owning the sync tables holds only this least-privilege list (the same list as step 2 of the small-team workflow):
  - USAGE on the schema;
  - SELECT on `sync_events`, `sync_hub_state`, `sync_node_collisions` and `sync_project_clients`;
  - INSERT on `sync_events`, `sync_conflicts` and `sync_project_clients`;
  - UPDATE on `sync_project_clients`;
  - USAGE on the sequence `sync_conflicts_conflict_id_seq`;
  - EXECUTE on the function `record_restore_collision`;
  - UPDATE on `sync_node_collisions`, only for a role that runs `mtix sync collisions resolve`;
  - SELECT on `node_renumber_remaps`, only for a role that runs `mtix sync migrate` on a hub without a valid node-number registry index;
  - INSERT on `node_renumber_remaps`, only for a role that runs `mtix sync migrate --yes` on a hub without a valid node-number registry index.

  On a hub without a valid node-number registry index, `mtix sync migrate --yes` also builds that index when the version gate is open, that is, when the project has at least one active client and every active client runs a remap-aware mtix version. It first records the duplicate creates of every project on the hub, and the index leaves those creates out, so they stay in the event log unchanged and the build succeeds. It drops an index that is not valid or not ready and builds it again. While the gate is closed, it leaves the index for a later run. A hub with more duplicate creates than the index can leave out is refused before the build, with the count and the limit. Only the table owner can build the index. `mtix sync doctor` fails its `schema current` check while the registry index is not valid or not ready, and `mtix sync init` warns about such an index; both print the fix: as the table owner, run `mtix sync migrate --yes` while the version gate is open. A syncing role set up with the least-privilege list holds no UPDATE on `sync_hub_state`, so it cannot run `mtix sync mark-restored`, which runs as the table owner. A syncing role records restore collisions only through the hub function `record_restore_collision`, which runs as the table owner and records a collision only when the hub's own data shows an earlier-epoch create holding the number, so the role needs EXECUTE on that function and no INSERT on `sync_node_collisions`. When `mtix sync init` adds this function to an existing hub, the table owner then grants EXECUTE on it to each syncing role (`GRANT EXECUTE ON FUNCTION record_restore_collision TO <role>;`) and revokes the privileges the list no longer names (`REVOKE INSERT ON sync_node_collisions FROM <role>;` and `REVOKE USAGE ON SEQUENCE sync_node_collisions_collision_id_seq FROM <role>;`). Upgrade every syncing client first, then run the REVOKE statements; if the REVOKE comes first, an older client's push that meets a restore collision fails until that client upgrades. `mtix sync doctor`, run with a syncing role's DSN, reports in its `schema current` check a hub without the function, a role that cannot execute it, a role that holds or can reach INSERT on `sync_node_collisions` or USAGE on its sequence, and create events stamped with a restore epoch below 0 or above the hub's current epoch, with the exact fix. A restored hub has no privileges from the dump: grant each syncing role the list again, EXECUTE included, then run `mtix sync doctor` with a syncing role's DSN.
- [ ] `audit_log` and `sync_conflicts` triggers are in place (test: `UPDATE audit_log SET ...` raises exception).
- [ ] Backup procedure for the hub is in place AND has been tested to restore (use `mtix sync backup --output FILE` for the mtix-owned tables). A role that runs `mtix sync backup` also needs SELECT on every sync table and on the sequences `audit_log_audit_id_seq`, `sync_conflicts_conflict_id_seq` and `sync_node_collisions_collision_id_seq`; otherwise run the backup as the table owner.
- [ ] DR runbook tested: rebuild a CLI from a fresh `mtix sync clone` (DSN from `MTIX_SYNC_DSN` or `.mtix/secrets`).
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
| 1.3 | 2026-07 | FR-20 origin-independent dispatch: hooks fire for events of any origin on hosts that configure them (placement is designation); inbox content is prompt input for exec-wake and channel-push delivery; hub write access now implies prompt-injection reach into federated agents |

Changes that alter the trust model (adding/removing a guarantee, adding a new threat) require a documented version bump and a corresponding `CHANGELOG.md` security note.
