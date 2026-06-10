# RCA: Unrecoverable SQLite corruption on full disk (2026-05-19)

**Status:** Root cause established (mechanism repro pending — MTIX-26.7)
**Severity:** Critical — permanent loss of user data; contradicts our published robustness claims
**Affected version:** v0.1.5-beta (commit b9b402c). Current head shares the vulnerable storage code.
**Remediation epic:** MTIX-26
**Evidence:** External diagnostic bundle (held privately by maintainer; customer identifiers redacted from this document). DB SHA256 `ca461687…d9dc21`.

## 1. Impact

An external user's `.mtix/data/mtix.db` became unreadable (`enable WAL: database disk image is malformed (11)`) after their disk reached 99% capacity. Of 80 nodes:

- 47 were salvaged by per-id reads with `cell_size_check=OFF` (the PK index survived; specific leaf pages did not).
- 33 were unrecoverable from the database. The user reconstructed 24 of them from their LLM-agent session transcripts — losing timestamps, audit trails, and relationships. The rest were lost outright.
- `mtix import` then **rejected** the reconstructed export because checksum verification has no recovery override, blocking the last-mile recovery.

## 2. Forensic findings (independently verified)

| Observation | Value |
|---|---|
| Header page count (offset 28) | **136 pages** → expected file size 557,056 B |
| Actual file size | **294,912 B = 72 pages** |
| change_counter / version_valid_for | 80 / 80 — header is *internally valid*; the file tail is missing |
| Freelist trunk | page 50, 20 free pages; freelist and interior B-tree pages reference pages 73–135, which are physically absent |
| WAL at discovery | present but **0 bytes**; WAL/SHM disappeared after a later mtix invocation |
| File size quirk | 294,912 B matches the DB's size from ~3 weeks earlier |

This is a torn checkpoint signature: page 1 (carrying the new 136-page count) and other low-numbered pages were backfilled into the main file successfully (in-place overwrites), while writes that required **extending** the file failed under ENOSPC. The state was still recoverable at that moment — the WAL held the truth — and became permanent only when the WAL was subsequently reset/removed.

The size quirk supports a long-lived-process pattern: the main file had not grown in ~3 weeks because the user drives mtix through the MCP server (a long-running process), so committed data accumulated in the WAL between rare checkpoints.

## 3. Causal chain

1. **Mirror absent where it mattered (design gap).** FR-15.3 auto-export of `tasks.json` fires only in the CLI's cobra `PersistentPostRunE` (`cmd/mtix/root.go:73-81`). The MCP server (`cmd/mtix/mcp.go`) and `serve` never trigger it. For an agent-driven user — our primary audience — the redundant mirror effectively does not exist. → MTIX-26.1
2. **No pre-flight space check; no fail-stop (design gap).** Nothing refuses to start a file-resizing operation on a near-full disk, and ENOSPC/EIO are not classified anywhere (`internal/store/sqlite/tx.go` wraps only busy errors). mtix kept operating against a failing filesystem. → MTIX-26.2
3. **Checkpoint tore under ENOSPC (trigger).** WAL backfill updated existing pages incl. the header, failed on file extension. Mechanism repro + verification of modernc.org/sqlite's ENOSPC semantics vs C SQLite is owed by MTIX-26.7.
4. **WAL was lost after the failed checkpoint (fatal step).** A later invocation left the WAL at 0 bytes. Until then the data was recoverable. Whether this was driver behavior (modernc WAL reset after failed backfill), checkpoint-on-close, or recovery-on-open must be established by fault injection; a guard belongs in MTIX-26.2.
5. **Corruption ran silent (detection gap).** No integrity/quick check on open (only `mtix verify`, manual, and `quick_check` on backups). First detection was a hard open failure, after further invocations had already disturbed the WAL/SHM. → MTIX-26.4
6. **No recovery path (response gap).** No `mtix recover`; `migrate` is a no-op; import checksum verification has no documented recovery override (`internal/store/sqlite/import.go:55-62`; `--force` only covers zero-node imports). → MTIX-26.5
7. **No automated backups (response gap).** `mtix backup` is sound (VACUUM INTO + quick_check) but manual-only. → MTIX-26.6

Contributing-factor corrections to the external analysis we received:

- We do **not** run `synchronous=NORMAL`. modernc.org/sqlite v1.35.0 defaults to `synchronous=FULL` in WAL mode (verified empirically). However, ADR-001 *documents* NORMAL while code sets nothing — drift that invites a silent durability downgrade. Make it explicit. → MTIX-26.3
- `synchronous=FULL` did not and could not prevent this: the failure was a torn checkpoint plus WAL loss, not a lost fsync.

## 4. Why our audits missed it

1. **Scope blind spot.** The MTIX-15 audit (pass 2, 22 items) was a *sync-layer security* audit. Our chaos tests prove SIGKILL atomicity — faults in *our process*. No test anywhere injects *environment* faults: ENOSPC, EIO, short writes. We tested "our code dies"; we never tested "the world fails under us."
2. **Declared scenarios without tests, and nothing to catch that.** QUALITY-STANDARDS scenario #5 (startup integrity check) and #10 (shutdown WAL checkpoint) were written down and never implemented. REQUIREMENTS FR-15.3b ("auto-export failure, e.g. disk full, must warn") was never tested. No traceability mechanism links declared scenarios to test IDs, so drift was invisible. → MTIX-26.8
3. **Doc/code conformance unchecked.** ADR-001's `synchronous=NORMAL` claim contradicted the code for the project's entire life. → MTIX-26.3, MTIX-26.8
4. **Interface-coverage blind spot.** FR-15's durability story was designed and tested around CLI process lifecycle; nobody asked whether the invariant ("mirror written on every mutation") holds per *interface* (MCP, serve). Our own positioning is agent-native; the audit model wasn't. → MTIX-26.1

The honest conformance bar for the robustness claims we cite is fault-injection evidence: mtix must survive `kill -9` during every write while the filesystem intermittently returns ENOSPC, with either a consistent DB+WAL or a clean fail-stop, and the test suite proving it must be public. Until MTIX-26.7 is green in CI, our claims are aspirational, and public wording must be softened accordingly (tracked in MTIX-26.8).

## 5. Remediation plan (MTIX-26)

| Ticket | Priority | Fix |
|---|---|---|
| MTIX-26.1 | P0 | Mirror parity: auto-export on every mutation via MCP/serve |
| MTIX-26.2 | P0 | Pre-flight disk-space checks; fail-stop on ENOSPC/EIO; WAL never reset after failed checkpoint |
| MTIX-26.3 | P0 | Explicit `synchronous=FULL` + `wal_autocheckpoint`; fix ADR-001 drift; disk-full NFR |
| MTIX-26.4 | P0 | `PRAGMA quick_check` on open; refuse writes on failure |
| MTIX-26.5 | P1 | `mtix recover` + `import --recompute-checksum` recovery path |
| MTIX-26.6 | P1 | Automated rolling backups with rotation |
| MTIX-26.7 | P1 | Fault-injection suite (ENOSPC/EIO/kill-9 matrix) as release gate; must reproduce this incident's signature |
| MTIX-26.8 | P1 | Claims-to-test traceability matrix enforced in CI; wording pass until green |
| MTIX-26.9 | P2 | ADR: append-only event journal + content-addressed bodies |

**Interim guidance for users (until P0 ships):** run `mtix sync --fix` (or any CLI mutation) periodically when driving mtix via MCP, commit `tasks.json` to git, and take `mtix backup` snapshots. A post-mutation git hook on `tasks.json` provides point-in-time recovery today.

## 6. Lessons

- A redundancy feature that does not run on every interface is not redundancy; invariants must be stated and tested per interface.
- Durability pragmas protect against power loss, not against continuing to write into a failing filesystem. Fail-stop is a feature.
- A corrupt-but-WAL-intact database is a recoverable incident; losing the WAL converted an incident into permanent data loss. Detection-on-open and never-touch-WAL-after-failure are the guards.
- Declared safety scenarios require enforced traceability to tests, or they decay into marketing.
