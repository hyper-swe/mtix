# CI and release gates

Every pull request and push to `main` runs **29 CI checks**. Merge only the
reviewed commit with every check successful; a skipped or cancelled check is
not a passing check.

| Checks | Count | Purpose |
| --- | ---: | --- |
| `test-go-shard (OS, other)` | 2 | The other-package shard on each of `ubuntu-22.04` and `macos-latest` |
| `test-go-sqlite (OS, shard N)` | 6 | Three SQLite test shards on each OS |
| `test-go-cmd (OS, shard N)` | 6 | Three `cmd/mtix` test shards on each OS |
| `test-go` | 1 | Require all fourteen Go shard jobs from the three matrices to succeed |
| `test-web` | 1 | Web tests |
| `lint` | 1 | Pinned Go lint |
| `pg-16 package`, `pg-17 package` | 4 | Transport and end-to-end suites against each PostgreSQL version |
| `pg-16 cmd/mtix shard N`, `pg-17 cmd/mtix shard N` | 6 | The `cmd/mtix` tests in three shards per PostgreSQL version |
| `test-fault-injection` | 1 | Disk-full and crash durability conformance |
| `build` | 1 | Check the quiet relay reference and both relay benchmarks, then build and verify the binary |

## Go race shards

The explicit package lists are in `scripts/ci-go-shards/`:

- `sqlite.txt`: `internal/store/sqlite`, run in the separate three-test-shard matrix on each OS.
- `cli.txt`: `cmd/mtix`, run in the separate three-test-shard `test-go-cmd` matrix on each OS.
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

Both shard matrices have `fail-fast: false`, so one failed shard does not
cancel the others. The aggregate job named exactly `test-go` needs both
`test-go-shards`, `test-go-sqlite` and `test-go-cmd` and uses `always()` so failed or
skipped dependencies cannot skip the gate. It accepts only `success` from all
three matrices; failure, cancellation, skipping or a missing result from any of the three
fails the aggregate. The build continues to depend on this gate.

The workflow tests execute the aggregate's actual shell for those outcomes
and check the OS/shard matrix, flags and check count. The package-guard tests
include an unlisted package that must fail until it is explicitly assigned,
as well as duplicate membership and failed inventory enumeration.

## SQLite test shards (MTIX-112.17.29)

The SQLite race package approached the unchanged 20-minute timeout on macOS.
PR CI runs it in three hash shards on each OS, using the same
`scripts/ci-go-shards.sh` modes as the remote bar:

```bash
bash scripts/ci-go-shards.sh test-check internal/store/sqlite 3
bash scripts/ci-go-shards.sh test-pattern internal/store/sqlite 3 2   # the -run regex of shard 2
```

The workflow passes the printed anchored pattern directly to `go test -run`,
retaining `-race -count=1 -timeout 20m`. The script lists the compiled tests,
examples and fuzz seeds, assigns names
by a stable hash, and checks that each name matches exactly one emitted
pattern. New tests are assigned automatically; subtests remain with their
parent. Internal and external tests with the same name share a selector and
run in the same shard. Each OS verifies the full package partition and the
exact test partition before running its shard. The real-package test in
`scripts/ci_sqlite_workflow_test.go` independently checks every compiled
registration against all three patterns. No assertion, test body, race flag
or timeout is weakened, and no skip flag is added.

The two old SQLite job names are replaced by six `test-go-sqlite` checks.
Together with the four CLI/other jobs, the aggregate and the remaining checks,
this changes the merge requirement from 21 successful checks to **25 of 25**; the `cmd/mtix` split below makes it **29 of 29**.

Before the split, the last five completed main CI runs measured these SQLite
package times (seconds from the original Go output; setup is excluded):

| Main CI run | macOS | Ubuntu |
| --- | ---: | ---: |
| [40441320798](https://github.com/hyper-swe/mtix/actions/runs/40441320798) | 802.467 | 1024.239 |
| [40429608472](https://github.com/hyper-swe/mtix/actions/runs/40429608472) | 822.772 | 853.764 |
| [40419953566](https://github.com/hyper-swe/mtix/actions/runs/40419953566) | 914.026 | 922.439 |
| [40418804538](https://github.com/hyper-swe/mtix/actions/runs/40418804538) | 651.540 | 954.145 |
| [40418645578](https://github.com/hyper-swe/mtix/actions/runs/40418645578) | 827.778 | 986.447 |

Validate the resulting shard times on two real PR CI runs and record the run
links and per-shard durations in the PR evidence before merging. The remote
bar dispatches `remote-bar.yml` from main; it does not exercise `ci.yml`.

## cmd/mtix shards

### Ordinary CI (MTIX-112.17.22.1)

The whole-package `cmd/mtix` race run was the `test-go-shard (OS, cli)` job. It
took 19m29s on macOS and 20m29s on Ubuntu in main run 40695298246 (PRs 251 and
254 timed out at about 20m51s). It now runs as `test-go-cmd`: three shards per
OS from the same `scripts/cmd-mtix-shards.sh` hash split (same script, shard
count and `-race -count=1 -timeout 20m`, nothing skipped). `scripts/ci-go-shards/cli.txt`
still lists `cmd/mtix`, so the package partition guard proves it has exactly one
owner, and `cmd-mtix-shards.sh check` proves every test is in exactly one shard.
The budget is **12 minutes wall time per shard job** (60 percent of the 20m
timeout), a target not yet measured on `test-go-cmd`. As a proxy only, the same
split took 6.8 to 11.0 minutes per shard in the PostgreSQL job of that run (that
job adds PostgreSQL setup, `-p 1` and `-v`). The six real `test-go-cmd` job
times are recorded here after the PR's CI runs; if any exceeds the budget, raise
the shard count. The shard count lives in `cmd-mtix-shards.sh` (`SHARDS`) and
a workflow test fails if CI, the PostgreSQL job and the remote bar disagree. The
macOS remote bar still runs `make test-race` unsharded (tracked separately).

### PostgreSQL and remote bar

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
`cmd/mtix shard N` names. That raised the count from 17 to 21; the SQLite
PR split above raises it to 25. The remote bar's Linux run covers exactly what `make test-race` covers,
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

### Workflow source and changes (MTIX-112.17.30)

Every final verdict records `workflow_ref` and `workflow_sha`, identifying the
workflow source rather than the snapshot checkout. Fresh runs use the actual
`headBranch` and `headSha` reported for the dispatch. Reused runs identify their
original workflow source; reuse requires a `main` source and a valid commit in
addition to the existing tree, base, options, age and workflow-blob checks.
Missing or contradictory source metadata cannot yield green or reuse.

A refusal before dispatch reports `workflow_ref=not-run workflow_sha=none`.
If dispatch was attempted but the source cannot be established, the verdict
reports `workflow_ref=unknown workflow_sha=unknown` and exits 2; the separate
`requested_workflow_ref=main` is only the requested source, not proof it ran.
Dry-run reports that no workflow ran.

Any difference in `.github/workflows/remote-bar.yml`, including adding or
deleting it, is refused with exit 2 before push, dispatch or reuse. This also
applies to `--dry-run` and **cannot be overridden by
`--allow-workflow-diff`**. Other `.github` changes retain that explicit opt-in;
the warning states that main's remote-bar workflow runs the tree and that the
changed workflow itself is not exercised. For example, changed `ci.yml` still
needs actual PR CI evidence.

To measure a change to `remote-bar.yml` before merge, first obtain independent
review of the change and approval for a one-off hosted measurement of that
exact reviewed commit. This is an operator step, not a script feature or a
way to bypass the refusal. The approved operator creates a unique
`ci-scratch/*` snapshot with the reviewed tree and current `origin/main` as
its sole parent, then selects that snapshot's own workflow for dispatch.
Replace the uppercase placeholders below with the exact approved values.
The first commands print the current main commit, reviewed tree and new
snapshot commit; use those printed hashes for the corresponding placeholders.
`ci-scratch/APPROVED_NAME` must be the approved, unused scratch ref:

```bash
git fetch origin main
git rev-parse origin/main
git rev-parse "REVIEWED_COMMIT_SHA^{tree}"
git commit-tree REVIEWED_TREE_SHA -p MAIN_COMMIT_SHA -m "approved workflow measurement"
git push origin SNAPSHOT_COMMIT_SHA:refs/heads/ci-scratch/APPROVED_NAME
gh workflow run remote-bar.yml --repo hyper-swe/mtix --ref ci-scratch/APPROVED_NAME \
  -f scratch_ref=ci-scratch/APPROVED_NAME -f expected_tree=REVIEWED_TREE_SHA \
  -f base_commit=MAIN_COMMIT_SHA -f wrapper_version=2
```

The operator verifies the actual workflow source ref and commit and the
expected checkout tree, waits for all jobs, and records the run URL, ref,
commit, tree and measurements in the review evidence. Delete only that owned
scratch ref after the measurement is complete. This is the route used for
the MTIX-112.17.27 measurement; a normal main-workflow bar is never evidence
that a changed remote-bar workflow ran.

## Release gates

Run [the release checklist](RELEASE-CHECKLIST.md) on the exact CI-green commit
before tagging. All 29 ordinary CI checks, including the fourteen race shards and
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
