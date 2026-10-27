# CI and release gates

Every pull request and push to `main` runs **21 CI checks**. Merge only the
reviewed commit with every check successful; a skipped or cancelled check is
not a passing check.

| Checks | Count | Purpose |
| --- | ---: | --- |
| `test-go-shard (OS, shard)` | 6 | Three package shards on each of `ubuntu-22.04` and `macos-latest` |
| `test-go` | 1 | Require all six Go shard jobs to succeed |
| `test-web` | 1 | Web tests |
| `lint` | 1 | Pinned Go lint |
| `pg-16 package`, `pg-17 package` | 4 | Transport and end-to-end suites against each PostgreSQL version |
| `pg-16 cmd/mtix shard N`, `pg-17 cmd/mtix shard N` | 6 | The `cmd/mtix` tests in three shards per PostgreSQL version |
| `test-fault-injection` | 1 | Disk-full and crash durability conformance |
| `build` | 1 | Check the quiet relay reference and both relay benchmarks, then build and verify the binary |

## Go race shards

The explicit package lists are in `scripts/ci-go-shards/`:

- `sqlite.txt`: `internal/store/sqlite` on its own runner.
- `cli.txt`: `cmd/mtix` on a different runner.
- `other.txt`: every remaining selected package.

Each OS verifies the complete partition before running a shard. The guard
compares the lists with `go list ./... | grep -v node_modules` and fails for
unlisted, duplicate or stale packages, missing or empty lists, or an inventory
command failure. When adding a package, explicitly assign it to one list:

```bash
bash scripts/ci-go-shards.sh check
bash scripts/ci-go-shards.sh list other
```

Every shard retains `go test` with `-race -count=1 -timeout 20m`. The timeout is
per package. No test is skipped by the shard command; the existing
`node_modules` package exclusion is unchanged. Full Git history remains
available, including to the bookkeeping test.

The shard matrix has `fail-fast: false`, so one failed shard does not cancel
the others. The aggregate job named exactly `test-go` needs the entire matrix
and uses `always()` so failed or skipped dependencies cannot skip the gate.
It accepts only a dependency result of `success`; failure, cancellation and
skipping all fail the aggregate. The build continues to depend on this gate.

The workflow tests execute the aggregate's actual shell for those outcomes
and check the OS/shard matrix, flags and check count. The package-guard tests
include an unlisted package that must fail until it is explicitly assigned,
as well as duplicate membership and failed inventory enumeration.

## cmd/mtix shards

`cmd/mtix` alone approached the 20-minute per-package budget in one
PostgreSQL job. Its tests now run in three shards per PostgreSQL version in the
`test-go-postgres-cmd` job, and in three parallel `bar-cmd` jobs in the remote
bar. A test belongs to the shard picked by a stable hash of its name, so a new
test is assigned automatically and no manifest needs editing. The guard fails
unless every listed test is selected by exactly one shard and no shard is empty:

```bash
bash scripts/cmd-mtix-shards.sh check
bash scripts/cmd-mtix-shards.sh pattern 2   # the -run regex of shard 2
```

Each shard keeps `-race -count=1 -p 1 -timeout 20m` with nothing skipped, and
has its own database service. The branch-protection required checks change
from the single `pg-16 ./cmd/mtix/` and `pg-17 ./cmd/mtix/` names to the six
`cmd/mtix shard N` names, so the merge rule is now 21 of 21 checks (it was 17
of 17). The remote bar's Linux run covers exactly what `make test-race` covers,
split into parallel jobs (see "Remote bar package sets" below). The `--os macos-latest` run
still uses `make test-race`.

## Remote bar

`scripts/remote-bar.sh` runs a tree's committed bar (`make lint-go && make
test-race`, then `govulncheck`) on GitHub-hosted runners, by dispatching
`.github/workflows/remote-bar.yml` from `main` for a throwaway `ci-scratch/*`
snapshot branch. It is the default way to run a full bar for a change; it does
not replace the pull request checks above.

| Option | Effect |
| --- | --- |
| (none) | Go bar on `ubuntu-22.04` with PostgreSQL 16 |
| `--os macos-latest` | Go bar on a macOS runner, for native Darwin checks. Runners offer no PostgreSQL service on macOS, so the PG-gated suites skip |
| `--web` | Adds the web job: `npm ci`, `npm audit` (dev dependencies included, any advisory fails), `npm run lint`, `npx tsc -b`, `npx vitest run`, `npm run build`, then `diff -r web/dist internal/web/dist`. Any difference fails the job |

`--os` accepts only `ubuntu-22.04` (default) and `macos-latest`; any other
value exits 2 before anything is pushed. The options combine. Exit codes are
unchanged: 0 green, 1 bar red (including the web checks), 2 infrastructure or
usage failure, 3 tree mismatch. Default runs send no extra dispatch inputs.

### Remote bar package sets (MTIX-112.17.27)

The Linux remote bar used to run every package in one job under `-p 1`
(run 40368214334: core job 38m20s). The time was in packages that need no
PostgreSQL, not in the serialization, so the packages are now split by whether
they touch the single shared PostgreSQL database. Every job keeps `-race
-count=1 -timeout 20m` and skips nothing.

| Job | Packages | Flags | PostgreSQL |
| --- | --- | --- | --- |
| `bar` | the PostgreSQL consumers (`scripts/remote-bar-packages/serial.txt`: `internal/store/postgres/transport`, `e2e`, `benchmarks`), plus `make lint-go` and `govulncheck` | `-p 1` | one service, one DSN |
| `bar-parallel` | every other package (`parallel.txt`) | `-p 4` | none |
| `bar-shard` | `internal/store/sqlite` (3 shards), `internal/service` (2 shards) (`sharded.txt`) | per shard | none |
| `bar-cmd` | `cmd/mtix`, 3 shards (unchanged) | `-p 1` | one service per shard |

A PostgreSQL consumer is a package whose `*_test.go` reads `MTIX_PG_TEST_DSN`
(`e2e/postgres` only checks the workflow text and sits behind the `e2e` build
tag, so it is not in `go list ./...`). Those packages drop and re-migrate the
shared database, so they never run at the same time. `scripts/ci-go-shards.sh bar-check`
fails unless the three manifests plus the `cmd/mtix` shards equal `go list ./...`
exactly (no package missing, repeated or stale), and `scripts/ci-go-shards.sh
test-check <dir> <N>` fails unless every test of a sharded package is selected by
exactly one shard. The guard `scripts/ci_go_shards_test.go` scans the
repository for PostgreSQL consumers and fails if one sits in the parallel or
sharded set. A new package must be added to one manifest; if its tests read
`MTIX_PG_TEST_DSN`, it goes in `serial.txt`.

```bash
bash scripts/ci-go-shards.sh bar-check
bash scripts/ci-go-shards.sh bar-list serial
bash scripts/ci-go-shards.sh test-check internal/service 2
bash scripts/ci-go-shards.sh test-pattern internal/service 2 1   # -run regex of shard 1
```

Wall time of the Linux bar (whole run, slowest job decides):

| Layout | Slowest job | Whole run |
| --- | --- | --- |
| Before (run 40368214334): one `bar` job, all packages `-p 1` | `bar` 38m20s | 38m20s plus `bar-cmd` in parallel (6.0, 6.1, 7.9 min) |
| After (run 40452661218) | `bar-cmd` shard 2, 8m33s | 8m46s |

Per job in run 40452661218: `bar` (lint, PG serial, govulncheck) 3m59s;
`bar-parallel` 5m30s; `bar-shard` sqlite 1/3 5m38s, 2/3 7m33s, 3/3 5m33s;
`bar-shard` service 1/2 4m35s, 2/2 5m06s; `bar-cmd` 1 7m51s, 2 8m33s, 3 5m56s.
The critical path is now the existing `cmd/mtix` shard 2. The measurement
dispatched the branch's own workflow, because `remote-bar.sh` dispatches the
workflow on `main` (MTIX-112.17.30).

### Reuse of an identical green run (MTIX-112.17.26)

Before dispatching, the script looks for a completed, successful remote-bar
run of the same snapshot tree, the same main commit the snapshot was cut from,
the same `--os`, `--web` setting and wrapper version. `remote-bar.yml` writes
them into the run name as `tree=... base=... os=... web=... wrapper=...`, and
its `validate` job fails any run whose values are not strictly well formed
(40 hex digits, a fixed os set, true or false, digits), so a title cannot be
forged. The script matches the whole title against one anchored pattern and
compares every field exactly; branch names are never used. The run must also
have used a workflow file whose blob equals the one on `main` now.

A run is reused only if it is at most 12 hours old (`RB_REUSE_MAX_AGE_SECS`,
a positive integer, default 43200). The limit is short on purpose:
`govulncheck`, `npm audit` and floating runner images can turn red within
days with no change to the tree. Many baselines cut from the same main commit
within the day still share one run. A reuse prints `REUSED run: <url> tree=...
base=... os=... web=...` and `verdict=REUSED`, exits 0 and pushes and
dispatches nothing. Failed, cancelled, queued and in-progress runs are never
reused, and any mismatch or lookup failure dispatches a fresh run. `--no-reuse`
forces a fresh run; a snapshot run with `--allow-workflow-diff` never reuses.
If main's workflow predates the `base_commit` input the dispatch is retried
without it, and such runs carry no match data. Runs started before the
run-name change are not reused.

Because the workflow is dispatched from `main`, a change to it takes effect
only after it is merged; contract tests and `--dry-run` cover it before then.

## Release gates

Run [the release checklist](RELEASE-CHECKLIST.md) on the exact CI-green commit
before tagging. All 21 ordinary CI checks, including the six shards and
`test-go` aggregate, must pass alongside independent review and the other
documented quality gates.

The tag-triggered release workflow retains its full preflight race and
coverage invocations with `-p 1`, unchanged test flags and the 20-minute
per-package timeout. Its PostgreSQL-enabled packages share a database and
remain serialized. Sharding ordinary CI does not replace the release
preflight, security scans, coverage gate, fault-injection or cloud contracts.

The existing Linux `build` job also runs the blocking non-race, non-short
relay quiet check and both relay benchmarks. The ordinary Go race shards
retain the default paired relative guard. See [relay calibration and exact
command](../benchmarks/README.md#fr-21-relay-publish-guard-mtix-165).
