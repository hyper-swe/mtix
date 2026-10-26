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

## FR-21 relay publish guard (MTIX-165)

`TestRelayPublish_MeetsTheLatencyTarget` runs by default, including under
`-race` and `-short`. Each of seven pairs prepares two independent fresh
200-event fixtures before timing. It alternates reference/candidate order,
validates every count and positive duration, and checks the median of the
seven candidate/reference ratios against 1.5. It does not divide unpaired
medians. Each side's frames, cursor and pending journal are checked after
both timers. Fixture creation and validation are excluded from timing.

The frozen fresh-success reference performs journal reads, JSON encoding,
segment appends, cursor persistence and writer close without calling the
publisher. It shares store/JSON/segment primitives with production, so common
cost changes can cancel in the relative ratio. It covers this fresh batch
path, not rotation, retries or recovery. Existing correctness tests cover
those paths. The separate quiet check retains the unchanged FR-21 §9
1 ms/reference/event ceiling, comparing full batch nanoseconds before division.
The ceiling applies to the reference; candidate regressions use the ratio.

CI's existing Linux build job runs this blocking command after Go setup:

```bash
go test ./benchmarks -run '^TestRelayPublish_MeetsTheLatencyTarget$' -bench '^BenchmarkRelay(Publish|Ingest)$' -benchtime=200x -count=1 -timeout=5m -v -args -relay-quiet
```

Explicit `-relay-quiet` rejects `-race` and `-short`; neither flag disables the
ordinary relative guard. Both real relay benchmarks execute in the quiet step.
The publish benchmark asserts prepared = published = requested, even above
its publisher's usual batch limit. The ingest I/O structural guard is unchanged.

The 1.5 ratio was frozen after ten ordinary and ten race runs of seven pairs
on Apple M5/Go 1.26.9. The largest unmutated run median was 1.135569; a real
publisher-only extra-work mutant had median ratio 2.348368 while staying below
1 ms/event. It added approximately one measured publication's CPU work to
each actual publication. These observations provide calibration headroom,
not a universal noise bound or a guarantee to detect every 50% regression.
Do not automatically raise the limit when a test fails. Inspect all seven
pairs, rerun the focused guard with recorded host load, and measure both the
unchanged reference and the same real-work mutant before proposing calibration
changes. Keep outliers and document toolchain/host drift in the review evidence.
