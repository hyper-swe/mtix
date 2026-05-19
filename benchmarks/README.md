# Performance Benchmarks (MTIX-15.10)

Performance targets and load tests for the FR-18 BYO Postgres sync
layer plus the underlying solo CLI path.

## Targets

| Target | Source | File | Status |
|---|---|---|---|
| Solo CLI command latency < 10ms (median) | FR-18 / 15.10 | `solo_latency_test.go` | met |
| 100K-node project memory < 50MB heap | FR-18 / 15.10 | `memory_test.go` | met |
| Pool MaxConns per CLI ≤ 5 | FR-18 / 15.10 | `pool_config_test.go` | met (lowered from 8 → 5) |
| Sync push of 1000 events < 5s | FR-18 / 15.10 | `sync_throughput_test.go` | met (~470 ms observed) |
| Sync pull of 1000 events < 5s | FR-18 / 15.10 | `sync_throughput_test.go` | met (~520 ms observed) |

## Running

```bash
# Default suite (skips slow 100K-node test by default)
go test ./benchmarks/...

# Include the 100K-node memory test — set MTIX_PERF_LONG=1
MTIX_PERF_LONG=1 go test ./benchmarks/...

# PG-bound throughput tests — need BOTH a DSN AND MTIX_PERF_LONG=1
MTIX_PG_TEST_DSN='postgres://...?sslmode=disable' \
  MTIX_PERF_LONG=1 \
  go test ./benchmarks/...

# Skip slow tests during dev iteration
go test -short ./benchmarks/...

# Benchmarks (ns/op output, no pass/fail)
go test -bench . -run=^$ ./benchmarks/...
```

All three `TestPerf_*` threshold tests are gated behind
`MTIX_PERF_LONG=1`:

- `TestPerf_Memory_100KNodes` — 100K-node insertion + race detector
  blows the 10-min timeout on GitHub-hosted runners.
- `TestPerf_SoloCommandTargets` — race overhead makes the list
  median ~10× slower; the 10ms target false-fails.
- `TestPerf_PushPullTargets` — race overhead makes the 1000-event
  pull take ~6s on CI vs ~0.5s un-instrumented; the 5s target
  false-fails.

The underlying perf is fine on both dev boxes and CI; the gate just
keeps the threshold assertions out of the default race sweep. A
dedicated perf CI job that sets the env var (and runs without
`-race`) is the right home for the threshold checks. The
`BenchmarkSync*` and `BenchmarkSolo*` functions still emit ns/op
without the gate so trend tracking continues.

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

Sync throughput (postgres:16 in Docker, local network):

```
push 1000 events ~ 470 ms (target 5s)
pull 1000 events ~ 520 ms (target 5s)
BenchmarkSyncPushPullRoundTrip_100Events ~ 35 ms/op
```

10× headroom on the throughput targets. Real-world hub latency
(managed PG, cross-region) will dominate in production; the
laptop-Docker numbers establish the lower bound.

## Failure mode

When a target is missed:

1. The failing test reports the OBSERVED value alongside the threshold,
   so regressions are diagnosable from CI logs.
2. If the target is structurally unmeetable (e.g. a managed PG
   provider's cold start is longer than 5s), the maintainer documents
   the limitation in this README with a planned mitigation, marks
   the test as `t.Skip` with a TODO comment naming the mitigation
   ticket, and surfaces it in the release notes.
