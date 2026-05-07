# MTIX-15 Security Audit Pass 2 — Evidence Table

Re-verification of the 12 design-audit items from MTIX-15.0 against
the shipped code in MTIX-15.2 through 15.10. Each row cites a
specific test that asserts the requirement.

## Status legend

- **PASS** — test exists, was located by grep, and asserts the
  requirement directly.
- **MISSING** — no test found; closing the gap is 15.11.2's scope.
- **AMBIGUOUS** — multiple candidates; the closest is cited.

## 12 original audit items

| # | Description | File:line | Test name | Asserts | Status |
|---|---|---|---|---|---|
| 1 | Malformed events rejected (validator) | `internal/sync/validator/validator_test.go:55,74,97` | `TestValidate_RejectsOversizedPayload`, `TestValidate_RejectsTooDeepPayload`, `TestValidate_RejectsFutureTimestamp` | Validator surfaces structured errors for payload size, depth, and timestamp violations. | PASS |
| 1b | Malformed events rejected (apply path) | `internal/store/sqlite/sync_apply_test.go:426` | `TestApply_MalformedPayloadPerOpType` | Per-op-type payload schema violations rejected at apply with proper error wrapping. | PASS |
| 2 | Replay attacks idempotent | `internal/store/sqlite/sync_apply_test.go:146` | `TestApply_DuplicateEventIDIsNoop` | Same event applied 3× → exactly 1 row in applied_events; no duplicate node row; no panic. | PASS |
| 3 | LWW manipulation deterministic | `internal/store/sqlite/sync_apply_lww_test.go:137` | `TestApplyLWW_SameLamportSameTSTieBreakByMachineHash` | Two competing events with identical `(lamport, wall_clock_ts)`: lower `author_machine_hash` wins, deterministically. | PASS |
| 4 | DSN never in CLI output (10 commands) | `cmd/mtix/sync_dsn_hygiene_test.go:31` | `TestDSN_NeverInAnyFR18CommandOutput` | All 10 FR-18 sync commands run with sentinel-bearing DSN; sentinel absent from stdout/stderr regardless of success/error path. | PASS |
| 4b | DSN never in MCP tool output | `internal/mcp/tools_sync_workflow_test.go:159` | `TestSyncWorkflowTool_NeverLeaksDSN` | mtix_sync_workflow MCP tool never surfaces DSN sentinel or hostname in result text. | PASS |
| 4c | redact.DSN scheme coverage | `internal/sync/redact/redact_test.go` (existing) + `cmd/mtix/sync_dsn_hygiene_test.go:93` | `TestDSN_RedactDSNCatchesAllSchemes` | Redaction strips secrets across `postgres://`, `postgresql://`, `jdbc:postgresql://`. | PASS |
| 5 | TLS verify-full enforced | `internal/store/postgres/transport/dsn_test.go:136` | `TestEnforceTLS_RefusesWeakerWithoutInsecureFlag` | A DSN with weaker sslmode is rejected pre-flight unless `--insecure-tls` is set; loopback exception covered separately at line 150. | PASS |
| 5b | TLS verify-full default | `internal/store/postgres/transport/dsn_test.go:116` | `TestEnforceTLS_DefaultsToVerifyFull` | DSN without explicit sslmode is upgraded to verify-full. | PASS |
| 6 | audit_log atomic with mutations (chaos) | `internal/store/sqlite/sync_apply_chaos_test.go:39` | `TestApply_AtomicityUnderKill` | Subprocess SIGKILL'd at 0–100ms intervals during IdempotentApply; parent verifies `nodes count == applied_events count` (both 0 or both 1, never half). | PASS |
| 7 | node_type canonicalization at apply | `internal/store/sqlite/sync_apply_test.go:167` | `TestApply_CreateNode_CanonicalizesNodeType` | Event payload claiming `node_type='story'` at depth=0 is overwritten with canonical `NodeTypeEpic` during apply. | PASS |
| 8 | Map iteration determinism (VC marshal) | `internal/model/sync_clock_test.go:83` | `TestVectorClock_MarshalDeterministic` | VectorClock JSON marshaling sorts keys lexically; 4 random insertion orderings produce byte-identical output. | PASS |
| 9 | Schema migration single-flight (advisory lock) | `internal/store/postgres/transport/integration_test.go:159` | `TestMigrate_ConcurrentSingleFlight` | 10 concurrent Migrate calls via separate pools succeed; only one runs schema work; sync_events exists exactly once. PG-gated. | PASS |
| 10 | SQL injection regression | `internal/store/postgres/transport/security_test.go:52` | `TestSQLInjection_AttackPatternsHandledSafely` | 8 attack patterns (`DROP TABLE`, `OR 1=1`, `UNION SELECT`, etc.) in event fields; accepted forms round-trip as literal strings; rejected forms surface structured errors; tables intact after each. PG-gated. | PASS |
| 11 | Singleton pusher under contention | `internal/sync/pushlock/pushlock_test.go:28` | `TestPushLock_SecondConcurrentAcquireFailsWithErrLockHeld` | First `pushlock.Acquire` succeeds; second concurrent acquire returns `ErrLockHeld`. | PASS |
| 12 | Reconciliation atomicity (failure injection) | `internal/store/sqlite/sync_reconcile_atomicity_test.go:190` | `TestRenameTo_AtomicityFailureMidLoop` | `reconcileFailAfterN` chaos hook fails after the 2nd rename; error wrapped, IDs revert to pre-rename state, prefix sentinel rolled back. | PASS |

## Penetration-style checks (from 15.11 prompt)

| Check | Status | Notes |
|---|---|---|
| Lamport manipulation rejected | DEFERRED to 15.11.2 | The prompt asks for a test that submits an event with manipulated lamport_clock and verifies hub rejection. Validator covers schema; need explicit test that lower-lamport event after a higher-lamport one is handled per LWW (not panic). |
| Vector clock overflow rejected | DEFERRED to 15.11.2 | Need boundary test at 2^53 ± 1. |
| Binary strings sweep (no DSN in built binary) | NOT IN SCOPE FOR 15.11.1 | Static-binary check; likely a CI step. |

## New requirements (HIGH) — scope of 15.11.2

| # | Requirement | Status |
|---|---|---|
| N1 | Fuzz targets: FuzzEventDecode, FuzzVectorClockMerge, FuzzPushEventsValidation | TODO (15.11.2) |
| N2 | Vector clock overflow test (2^53 boundary) | TODO (15.11.2) |
| N3 | Panic message sanitization sweep (across postgres://, postgresql://, jdbc:postgresql://) | TODO (15.11.2) |

## Verification

To re-run the named tests on a fresh checkout:

```bash
# Items 1, 1b, 2, 3, 6, 7
go test -count=1 -run 'TestValidate_Rejects|TestApply_MalformedPayloadPerOpType|TestApply_DuplicateEventIDIsNoop|TestApplyLWW_SameLamportSameTSTieBreakByMachineHash|TestApply_AtomicityUnderKill|TestApply_CreateNode_CanonicalizesNodeType' ./internal/store/sqlite/... ./internal/sync/validator/...

# Item 4, 4b, 4c
go test -count=1 -run 'TestDSN_NeverInAnyFR18CommandOutput|TestSyncWorkflowTool_NeverLeaksDSN|TestDSN_RedactDSNCatchesAllSchemes' ./cmd/mtix/... ./internal/mcp/...

# Items 5, 5b
go test -count=1 -run 'TestEnforceTLS' ./internal/store/postgres/transport/...

# Item 8
go test -count=1 -run 'TestVectorClock_MarshalDeterministic' ./internal/model/...

# Items 9, 10 (PG-gated)
MTIX_PG_TEST_DSN=... go test -count=1 -run 'TestMigrate_ConcurrentSingleFlight|TestSQLInjection_AttackPatternsHandledSafely' ./internal/store/postgres/transport/...

# Item 11
go test -count=1 -run 'TestPushLock_SecondConcurrentAcquireFailsWithErrLockHeld' ./internal/sync/pushlock/...

# Item 12
go test -count=1 -run 'TestRenameTo_AtomicityFailureMidLoop' ./internal/store/sqlite/...
```

## Summary

12/12 original audit items have a passing test cited above. 3 new
requirements (fuzz, VC overflow, panic sanitization) are TODO and
form 15.11.2's scope. 1 deferred penetration-style check (lamport
manipulation) is also picked up by 15.11.2.

**This sub-ticket (15.11.1) is the audit MAP — it does not add new
tests.** 15.11.2 closes the gaps; 15.11.3 produces the final
sign-off table with git SHA and govulncheck output.
