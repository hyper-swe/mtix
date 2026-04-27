# mtix Security Model

> **Document version:** 1.0
> **Applies to:** mtix v0.1.x (SQLite mode) and the planned v0.2.x BYO Postgres mode (MTIX-14)

This document is the security and trust contract for mtix. It tells you what mtix protects against and — equally important — what it does not. Read it before adopting mtix in any environment beyond a single developer's laptop.

If a guarantee in this document conflicts with what the code actually does, **the code is the bug**. File a security advisory.

---

## Audience and scope

This document covers two operating modes:

1. **SQLite mode (default, current):** one developer or one machine using mtix locally. Database in `.mtix/data/mtix.db`. Concurrent agents on the same machine sharing one DB. Sync between machines via git-tracked `.mtix/tasks.json`.
2. **BYO Postgres mode (planned, MTIX-14):** a small trusted team sharing one Postgres instance. CLI and agents on each team member's machine connect directly to the shared PG.

It does **not** cover:

- A multi-tenant SaaS where multiple unrelated tenants share infrastructure (separate roadmap; would require an mtix server with row-level security and per-tenant identity).
- A hosted PG offering by HyperSWE (separate roadmap).
- Use of mtix in adversarial open-source contexts where the team is not mutually trusted.

---

## Trust model

### Who is trusted

| Party | Trust level | Why |
|---|---|---|
| The local user running `mtix` | Full | They own the machine and the DB |
| Any agent / process on a trusted machine | Full | If the machine is trusted, processes on it are trusted |
| In BYO PG mode: anyone with the connection string | Full | The DSN is a credential equivalent to root in the mtix database |
| Other team members in BYO PG mode | Full | A team's mtix DB is shared like a team's git repo — by membership, not isolation |

### Who is NOT trusted

| Party | Why mtix doesn't trust them | What protects against them |
|---|---|---|
| Network adversaries (sniff/MitM) | Could read or modify in transit | TLS verify-full when connecting to remote PG |
| Compromised git server | Could tamper with `tasks.json` | PG is canonical in BYO mode; git is a snapshot, not the source of truth |
| Anyone without the PG connection string | Not authenticated | PG-level authentication is the gate |
| Other tenants (multi-tenant scenario) | Not in scope | Use the future hosted SaaS, not BYO PG |

### Plain-English consequences

- If any team member's laptop is compromised in BYO PG mode, **all task data is at risk**. There is no per-user data isolation within a single mtix instance.
- If the PG provider (Supabase, Neon, RDS, your DBA) is compromised, **mtix data is compromised**. mtix does not encrypt data at rest within PG.
- Your team's mtix database is exactly as private as your team's PG instance. Treat the DSN like a production database credential.

---

## Threat model

| # | Threat | What mtix does about it | Residual risk | What you do |
|---|---|---|---|---|
| 1 | **Credentials in git** (DSN committed by accident) | mtix refuses to load DSN from any tracked config file. DSN must come from `MTIX_PG_DSN` env var or `.mtix/secrets` (gitignore-enforced, mode 0600). | Low — fail-closed at config load | Use a secrets manager or env var; never paste DSN into a yaml that gets committed |
| 2 | **MitM on PG connection** (network adversary reads/modifies traffic) | mtix defaults to `sslmode=verify-full` and refuses `sslmode=disable` unless explicit `--insecure-tls` flag is set AND host is localhost. | Low if `verify-full` is honored end-to-end | Use a managed PG provider that enforces TLS; verify the root CA matches |
| 3 | **SQL injection** (malicious filter values) | All store layer SQL uses bound parameters. Audited in MTIX-9.1 (FR-17.1) for the existing SQLite driver; same audit ports to PG driver in MTIX-14.1. | Very low — depends on no future regression | Run the parameterization regression tests on every change |
| 4 | **Insider mutation tampering** (compromised team member edits/deletes data via mtix) | Append-only `audit_log` table records every mutation atomically. PG triggers prevent `UPDATE`/`DELETE` on audit rows. | Medium — superuser can disable triggers; insider with write access can still create or modify nodes | Use least-privilege PG roles; archive `audit_log` to immutable cold storage for true tamper evidence |
| 5 | **Audit log tampering** (DBA edits or deletes audit rows) | Triggers raise exception on `UPDATE`/`DELETE`. WAL archival recommended for safety-critical adopters. | Medium — PG superuser bypasses triggers | For tamper evidence, ship `audit_log` to an external append-only store (S3 with object lock, immudb, etc.) |
| 6 | **Hook bypass** (`git push --no-verify` or running on a machine without the hook) | Client-side hooks are advisory. Server-side enforcement (pre-receive on self-hosted git, GitHub Action on github.com) is the only real gate. | Medium — depends on team policy | If `tasks.json` freshness matters, deploy the example pre-receive hook or GitHub Action |
| 7 | **PG provider compromise** (Supabase / Neon / RDS hosting layer breached) | Out of mtix's scope. mtix data is exactly as safe as the PG instance hosting it. | High — depends on provider | Pick a provider you trust; encrypt sensitive task content client-side if needed |
| 8 | **DoS via mutation spam** (script writes millions of nodes) | mtix has no per-actor rate limit. Configurable retention on `audit_log` prevents unbounded growth there. | Medium — relies on PG-side controls | Set PG-side rate limits; configure `audit_log` retention policy |
| 9 | **Replay or forgery of mutation history** (forged actor field in `audit_log`) | mtix CLI populates `actor` from local config. A compromised CLI can write any actor name. | Medium — logical identity, not enforced at PG layer | Use PG-level user accounts as the real authentication; trust only what `pg_stat_activity` reports |
| 10 | **Denial of read access via PG exhaustion** | Connection pool with limits; document pgbouncer for >5 users | Low — operational concern | Pool sizing per the workflow doc |

---

## What BYO Postgres mode protects against

- **Network adversaries on the path between CLI and PG** — via mandatory TLS verify-full.
- **SQL injection in filter inputs** — via bound parameters at every store call site.
- **Silent data loss** — via append-only `audit_log` written atomically with every mutation. If a mutation commits, an audit row commits with it; if either fails, the whole transaction rolls back.
- **Audit gaps from concurrent access** — PG MVCC handles concurrent writes natively, replacing the SQLite WAL + busy_timeout workarounds.
- **Stale view of state** — every CLI reads from PG live; no cache divergence.

## What BYO Postgres mode does NOT protect against

- **Compromised team members** — anyone with the DSN can read and write everything. mtix does not partition data per-user within a single instance.
- **Compromised PG provider** — the provider has full access to your data. mtix does not encrypt at rest beyond what PG itself provides.
- **PG superuser disabling triggers** — `audit_log` tamper-resistance assumes triggers are not bypassed.
- **Multi-tenant isolation** — BYO PG is one shared database. For per-tenant isolation, use the planned hosted SaaS.
- **Forged actor identity in the audit log** — `actor` is a logical identity supplied by the CLI; the real authentication boundary is the PG user.
- **Bypassed git hooks** — `git push --no-verify` or absence of installed hooks defeats the snapshot-on-push pattern. Use server-side enforcement for safety-critical teams.
- **Replay of past mutations** — mtix mutations are idempotent at the data layer, but the audit log does not chain row hashes (planned for v2 of the schema; existing schema accommodates the column).

---

## Security checklist for adopters

Before going live with BYO Postgres mode, verify each of these:

- [ ] Connection uses `sslmode=verify-full` (test: connecting with `verify-ca` fails).
- [ ] Connection uses a server certificate signed by a trusted CA (test: `sslrootcert` set if managed PG requires it).
- [ ] DSN is stored in `MTIX_PG_DSN` env var or `.mtix/secrets` (gitignored, mode 0600). **Not** in any tracked config file.
- [ ] `.mtix/secrets` is in `.gitignore` (test: `git check-ignore .mtix/secrets` succeeds).
- [ ] PG role used by mtix is **least privilege**: SELECT/INSERT/UPDATE/DELETE on data tables only, not SUPERUSER, not CREATEDB, not REPLICATION.
- [ ] `audit_log` table exists with the trigger preventing UPDATE/DELETE (test: `UPDATE audit_log SET actor = 'x'` raises exception).
- [ ] Audit log retention policy is defined (default: 90 days; longer for safety-critical adopters).
- [ ] Backup procedure for the PG instance is in place AND has been tested to restore.
- [ ] DR runbook tested: restore from `.mtix/tasks.json` snapshot into a fresh PG.
- [ ] At least one of: client-side pre-push hook installed across all team machines, OR server-side enforcement (pre-receive hook or GitHub Action).
- [ ] All team members have read this document and understand the trust model.
- [ ] If running pgbouncer in front of PG, it is in **session mode** (not transaction mode — mtix uses prepared statements and advisory locks that transaction mode breaks).

---

## Reporting security issues

Open a [GitHub Security Advisory](https://github.com/hyper-swe/mtix/security/advisories/new) on the repo. Do not file public issues for vulnerabilities.

mtix is a pre-funding open-source project. Triage is best-effort. Critical issues get patched within days; lower-severity issues may take longer. There is no formal SLA.

---

## Document version history

| Version | Date | Change |
|---|---|---|
| 1.0 | TBD on first release | Initial publication alongside MTIX-14 BYO Postgres rollout |

Changes that alter the trust model (adding/removing a guarantee, adding a new threat) require a documented version bump and a corresponding `CHANGELOG.md` security note.
