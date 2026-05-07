# Performance Benchmarks (MTIX-15.10)

Performance targets and load tests for the FR-18 BYO Postgres sync
layer plus the underlying solo CLI path.

## Targets

| Target | Source | File | Status |
|---|---|---|---|
| Solo CLI command latency < 10ms (median) | FR-18 / 15.10 | `solo_latency_test.go` | met |
| 100K-node project memory < 50MB heap | FR-18 / 15.10 | `memory_test.go` | met |
| Pool MaxConns per CLI ≤ 5 | FR-18 / 15.10 | `pool_config_test.go` | met (lowered from 8 → 5) |
| Sync push of 1000 events < 5s | FR-18 / 15.10 | `sync_throughput_test.go` (15.10.1) | see below |
| Sync pull of 1000 events < 5s | FR-18 / 15.10 | `sync_throughput_test.go` (15.10.1) | see below |

## Running

```bash
# All non-PG perf tests (default suite, runs on every PR)
go test ./benchmarks/...

# PG-bound throughput tests — set MTIX_PG_TEST_DSN
MTIX_PG_TEST_DSN='postgres://...?sslmode=disable' go test ./benchmarks/...

# Skip slow tests during dev iteration
go test -short ./benchmarks/...

# Benchmarks (ns/op output, no pass/fail)
go test -bench . -run=^$ ./benchmarks/...
```

Tests gate slow paths on `testing.Short()`. The 100K-node memory test
takes ~20s; CI runs it on the perf job, not on every PR.

## Observed numbers (Apple M5, postgres:16 in Docker)

Solo (no PG):

```
BenchmarkSolo_Create_NoSyncOverhead-10     ~170 µs/op
BenchmarkSolo_Update_NoSyncOverhead-10     ~115 µs/op
BenchmarkSolo_List_NoSyncOverhead-10       ~3.0 ms/op (1000-node list)
```

All well under the 10ms median target. The list path is the closest
to the ceiling; future regressions there should be flagged.

100K-node steady-state memory (after `runtime.GC` × 2): typically
20–30 MB heap allocated. The 50MB ceiling has comfortable headroom.

## Failure mode

When a target is missed:

1. The failing test reports the OBSERVED value alongside the threshold,
   so regressions are diagnosable from CI logs.
2. If the target is structurally unmeetable (e.g. a managed PG
   provider's cold start is longer than 5s), the maintainer documents
   the limitation in this README with a planned mitigation, marks
   the test as `t.Skip` with a TODO comment naming the mitigation
   ticket, and surfaces it in the release notes.
