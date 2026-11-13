# CI and release gates

Pull requests run **26 CI checks**: the full Linux matrix plus exactly one
macOS job (`test-go-shard (macos-latest, other)`). Every push to `main` and the
nightly schedule (03:17 UTC, default branch) run **35 CI checks**, which adds
the nine macOS SQLite, `cmd/mtix` and service shard legs. Merge only the reviewed commit
with every check successful; a skipped or cancelled check is not a passing
check. `test-go` is the required status check on `main` and keeps that exact
name; it is the only required check.

| Checks | Count | Purpose |
| --- | ---: | --- |
| `test-go-shard (OS, other)` | 2 | The other-package shard on each of `ubuntu-22.04` and `macos-latest` |
| `test-go-sqlite (OS, shard N)` | 8 (PR: 4) | Four SQLite test shards (MTIX-270; the remote bar keeps three); macOS legs run on push and nightly only |
| `test-go-cmd (OS, shard N)` | 6 (PR: 3) | Three `cmd/mtix` test shards; macOS legs run on push and nightly only |
| `test-go-service (OS, shard N)` | 4 (PR: 2) | Two service test shards; macOS legs run on push and nightly only |
| `test-go` | 1 | Require every Go shard job the event generates (twenty on push and nightly, eleven on a PR) to succeed; on a PR also run the main macOS health check |
| `test-web` | 1 | Web tests |
| `lint` | 1 | Pinned Go lint |
| `pg-16 package`, `pg-17 package` | 4 | Transport and end-to-end suites against each PostgreSQL version |
| `pg-16 cmd/mtix shard N`, `pg-17 cmd/mtix shard N` | 6 | The `cmd/mtix` tests in three shards per PostgreSQL version |
| `test-fault-injection` | 1 | Disk-full and crash durability conformance |
| `build` | 1 | Check the quiet relay reference and both relay benchmarks, then build and verify the binary |

## Per-event macOS coverage and the red-main rule (MTIX-112.17.42)

Hosted macOS capacity is limited, so a pull request runs one macOS job and the
rest of the macOS matrix runs on `main`. The `os` axis of `test-go-sqlite` and
`test-go-cmd` is chosen by event; on a PR the macOS legs are never generated,
so the `test-go` aggregate stays strict: a skipped, cancelled or failed need
still fails it on every event.

To keep macOS regressions from slipping past PRs, `scripts/main-macos-health.sh`
judges the newest informative `ci.yml` run on `main` (event push or schedule).
It fails unless every job listed in `scripts/main-macos-expected-jobs.txt` (the
single list; a test keeps it equal to the macOS legs the workflow generates)
is present and `success` on that run. A skipped, cancelled, missing or unknown
macOS job is red. The script runs in two places.

1. **PR check (advisory).** The `test-go` job runs it on `pull_request`, from
   the BASE branch's copy of the script (the step checks out the current tip of the
   base branch, `github.base_ref`, never the base SHA stored at the PR's last
   sync, which can predate the script and fail with "No such file or
   directory"; it names no repository, so it is always the base repo, with no
   PR code and `persist-credentials: false`), so a PR cannot quietly edit its
   own gate. A PR can still edit `ci.yml` itself,
   so for a hostile PR this check is advisory. Reviewers treat any change to the
   script, its expected-job list or the step as a gate change. The PR that
   introduces the script has no base copy and merges through the override label.
2. **Merge-time gate (enforcing).** A PR check cannot re-judge itself after it
   is green, and the ruleset (25175194) is not strict, so a PR whose `test-go`
   passed before main turned red could still merge. Changing the ruleset is out
   of scope. Instead the merger runs the script standalone with gh credentials
   immediately before EVERY merge (merge train and admin merges alike). The
   command runs the copy of the script and its expected-job list on
   `origin/main` as fetched at merge time, never the PR branch's or a stale
   local copy, from the repository root (replace `<PR>`):

   <!-- merge-gate-command -->
   ```
   ( d=; git fetch origin main && d=$(mktemp -d) &&
     git archive FETCH_HEAD scripts/main-macos-health.sh scripts/main-macos-expected-jobs.txt | tar -x -C "$d" &&
     GITHUB_REPOSITORY=hyper-swe/mtix PR_LABELS="$(gh pr view <PR> --json labels --jq '[.labels[].name]')" \
       bash "$d/scripts/main-macos-health.sh"; rc=$?
     if [ -n "$d" ]; then rm -f "$d/scripts/main-macos-health.sh" "$d/scripts/main-macos-expected-jobs.txt"
       rmdir "$d/scripts" "$d"; fi; exit "$rc" )
   ```

   Exit 0 is the only go. A red, unknown or failing result (non-zero exit, API
   error, no output) stops the merge. The one exception is the PR that
   introduces the script: `origin/main` has no copy yet, the command fails, and
   that PR merges through the override label after its own review.

Policy, newest `push` or `schedule` run first (other events never count):

- Jobs are read per run attempt (`actions/runs/ID/attempts/N/jobs`) and
  de-duplicated by job name to the highest `run_attempt`, because the jobs API
  has been seen returning rows of two attempts together.
- Latest attempt completed and not cancelled: judge it (see above).
- Latest attempt cancelled, or a re-run in progress (`run_attempt` above 1): judge
  the previous attempt, walking down past attempts that were themselves
  cancelled. A red earlier attempt stays red, so cancelling a re-run does not
  clear a red main; if no attempt has a result, fail closed.
- First attempt cancelled: red if a macOS job failed or timed out; otherwise
  skipped, for at most 3 runs, then fail closed.
- First attempt still in progress: not judged. A run in progress is not red;
  this is an accepted residual, and the next merge after it finishes sees the
  result.
- Fail closed on any API error (the gh message is printed), unreadable data, or
  no run to judge.
- Override: a maintainer labels the PR that repairs `main` with `main-macos-fix`
  (create the label once; triage rights and above can apply it). The PR check
  skips the gate and prints a notice; the merge-time command above honours the
  label from `PR_LABELS`. A label is read from the event payload, and
  `pull_request` has no `labeled` trigger (it would re-run every check on every
  label change), so after labelling, push a new commit or close and reopen the
  PR to start a run that sees it; re-running the old run is untested and not
  relied on. Remove the label, and push again, once `main` is green.
- Permissions: only the `test-go` job gains `actions: read` (to list runs and
  jobs); no other job is widened. Fork PRs get a read-only token and no
  secrets; the gate step receives only that token and the repository name, and
  the checkout does not persist credentials.

## Go race shards

The explicit package lists are in `scripts/ci-go-shards/`:

- `sqlite.txt`: `internal/store/sqlite`, run in the separate four-test-shard matrix (macOS legs on push and nightly only).
- `cli.txt`: `cmd/mtix`, run in the separate three-test-shard `test-go-cmd` matrix (macOS legs on push and nightly only).
- `service.txt`: `internal/service`, run in the separate two-test-shard `test-go-service` matrix (macOS legs on push and nightly only).
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

All shard matrices have `fail-fast: false`, so one failed shard does not
cancel the others. The aggregate job named exactly `test-go` needs
`test-go-shards`, `test-go-sqlite`, `test-go-cmd` and `test-go-service` and uses `always()` so failed or
skipped dependencies cannot skip the gate. It accepts only `success` from all
four matrices; failure, cancellation, skipping or a missing result from any of the four
fails the aggregate. The build continues to depend on this gate.

The workflow tests execute the aggregate's actual shell for those outcomes
and check the OS/shard matrix, flags and check count. The package-guard tests
include an unlisted package that must fail until it is explicitly assigned,
as well as duplicate membership and failed inventory enumeration.

## Service test shards and child diagnostics (MTIX-112.51)

The service race suite exhausted the unchanged 20-minute package budget on
macOS while newly started tests appeared in the timeout dump. A hosted
service-only diagnostic run [42348872854](https://github.com/hyper-swe/mtix/actions/runs/42348872854)
passed all 1,580 root tests in 903.292 seconds; its longest test took 33 seconds.
That measurement shows cumulative suite cost, with no long single-test stall
reproduced. The earlier multi-package jobs had different contention; the
standalone timing is not a measurement of the new CI shards.

Service now has its own two-shard matrix on both OSes, using the same stable
hash and compiled-test partition checker already used by the remote bar:

```bash
bash scripts/ci-go-shards.sh test-check internal/service 2
bash scripts/ci-go-shards.sh test-pattern internal/service 2 1
```

The inventory is listed with `go test -race -list`, using the same race build as the job. Every compiled test, example and fuzz seed matches exactly one shard pattern;
subtests follow their parent. Each job checks package and test ownership before
running `go test ./internal/service/ -race -count=1 -timeout 20m -run "$pattern"`.
No test is skipped and no deadline is raised. `scripts/ci_service_workflow_test.go`
checks the compiled inventory and executes the workflow's guard/run commands,
including failure propagation. Package-ownership tests execute the real job
commands so service cannot silently remain in `other` or go untested. All four
service jobs are required by the strict `test-go` aggregate, bringing ordinary
CI to 33 checks (35 after MTIX-270 added a fourth SQLite shard). Record actual per-shard hosted durations in the PR evidence;
the full remote bar runs the code and guards, but does not execute changed
`ci.yml` jobs.

The native export child helper drains stdout in bounded chunks even when a line
exceeds 64 KiB. It retains a head and tail totaling up to 32 KiB per stream,
both stdout and stderr, and marks omitted bytes so a late failure stays visible. On an early exit or unsuccessful joined exit,
it reports retained stdout, stderr and process status after joining the reader
and process. Existing handshakes and 60-second guards are unchanged. This makes
the next child failure diagnosable; it does not establish or fix the historical
ManyWriters failure's cause.

## SQLite test shards (MTIX-112.17.29)

The SQLite race package approached the unchanged 20-minute timeout on macOS.
PR CI runs it in four hash shards on Linux (macOS on push and nightly; MTIX-270 raised
the count from three because one shard ran within 15 percent of the limit), using the same
`scripts/ci-go-shards.sh` modes as the remote bar, which keeps its own three SQLite shards:

```bash
bash scripts/ci-go-shards.sh test-check internal/store/sqlite 4
bash scripts/ci-go-shards.sh test-pattern internal/store/sqlite 4 2   # the -run regex of shard 2
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
registration against all four patterns. No assertion, test body, race flag
or timeout is weakened, and no skip flag is added.

The two old SQLite job names are replaced by six `test-go-sqlite` checks.
Together with the four CLI/other jobs, the aggregate and the remaining checks,
this changes the merge requirement from 21 successful checks to **25 of 25**; the `cmd/mtix` split below makes it **29 of 29**, and the service split above makes it **33 of 33** on a push; from MTIX-112.17.42 a PR needs **25 of 25**; MTIX-270 adds a fourth SQLite shard on each OS, so a PR needs **26 of 26** and a push **35 of 35** (the macOS SQLite, `cmd/mtix` and service legs run on main only).

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
timeout). Measured on the first `test-go-cmd` run (PR 261, run 40726735086),
shards 1, 2 and 3: macOS 6m26s, 7m59s and 9m14s; Ubuntu 8m19s, 5m17s and 6m01s.
All six are under budget; the slowest uses 46 percent of the timeout. If any
shard job exceeds the budget, raise the shard count. The shard count lives in `cmd-mtix-shards.sh` (`SHARDS`) and
a workflow test fails if CI, the PostgreSQL job and the remote bar disagree. The
macOS remote bar (`--os macos-latest`) is sharded the same way (MTIX-112.17.42.6): see "macOS remote bar" below.

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
covers the same set in sharded macOS jobs (see "macOS remote bar" below).

## macOS remote bar

The whole-package `make test-race` run on macOS overran the 20-minute per-package
timeout for `cmd/mtix` and `internal/store/sqlite` under `-race`. The `--os macos-latest`
run therefore uses three jobs that mirror the Linux ones (MTIX-112.17.42.6):

- `bar-macos`: `make lint-go`, `govulncheck`, and every package that is neither sharded
  nor `cmd/mtix` (the `serial` and `parallel` manifests in `scripts/remote-bar-packages/`,
  `-p 2`). It no longer runs `make test-race`. There is no PostgreSQL on macOS, so the
  PostgreSQL-gated suites skip there, as before.
- `bar-macos-shard`: the same matrix as the Linux `bar-shard` job (`internal/store/sqlite`
  in 3 shards, `internal/service` in 2), each guarded by `ci-go-shards.sh test-check`.
- `bar-macos-cmd`: `cmd/mtix` in the shards of `scripts/cmd-mtix-shards.sh`, guarded by its
  `check`.

A macOS dispatch therefore runs 9 macOS jobs (1 `bar-macos`, 5 `bar-macos-shard`, 3
`bar-macos-cmd`; it was 1), plus `validate` on Ubuntu. Hosted macOS runner concurrency is
limited and shared with the macOS legs of the main CI run, so jobs can queue. Queue time counts
toward the wrapper's overall `RB_TIMEOUT_SECS` (default 5400 s) and each job has its own
90-minute `timeout-minutes`; avoid dispatching a macOS bar while a main-branch macOS run is in
flight. A green `--os macos-latest` run of the sharded bar is confirmed by dispatching it after
review, not by the review loop.

Every `go test` keeps `-race -count=1 -timeout 20m`, with no `-skip` or `-short`. The step
names are the ones `scripts/remote-bar.sh` classifies. `scripts/remote_bar_macos_shards_test.go`
parses the workflow and fails if the macOS shard set differs from the Linux one, a package or
`cmd/mtix` shard is dropped or listed twice, a flag differs from Linux, a check step comes after
its run step, or `bar-macos` runs `make test-race` or loses `lint-go` or `govulncheck`. Raise a shard
count in the Linux matrix and the macOS matrix together, or that test fails.

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
| `--base <rev>` | Compares the snapshot's `.github` with `<rev>` instead of the merge-base of `HEAD` and `origin/main`. `<rev>` must be a commit that is an ancestor of `origin/main`; anything else exits 2 |
| `--web` | Adds the web job: `npm ci`, `npm audit` (dev dependencies included, any advisory fails), `npm run lint`, `npx tsc -b`, `npx vitest run`, `npm run build`, then `diff -r web/dist internal/web/dist`. Any difference fails the job |

`--os` accepts only `ubuntu-22.04` (default) and `macos-latest`; any other
value exits 2 before anything is pushed. The options combine. Exit codes are
unchanged: 0 green, 1 bar red (including the web checks), 2 infrastructure or
usage failure, 3 tree mismatch. Default runs send no extra dispatch inputs.

When the run fails, the script prints the failed log in two parts. A
`failing tests:` block comes first. It lists every line of the whole log that
holds a Go test failure marker (`--- FAIL:`, a `FAIL <package>` line, `panic:`,
`[build failed]` or `test timed out`), once each (the runner's timestamp prefix
and a trailing test duration are ignored when comparing lines), at most 100
lines. A `log tail (last 200 lines):` block follows with the end of the log.
The failing-test names often sit earlier in the log than the last 200 lines, so
read the first block to see which tests failed. The verdict line and the exit
code are unchanged.

### Workflow changes and older bases

The wrapper refuses a snapshot that changes `.github`, because GitHub runs
push-event workflows from the pushed commit. It judges a change against the
base of the checkout it runs from: the merge-base of that checkout's `HEAD` and
`origin/main`, or the
revision you pass with `--base` (which must be an ancestor of `origin/main`, so
an unreviewed commit cannot pose as a base). A snapshot built on an older base that only
lacks later `.github` changes from `main` is not refused. The wrapper prints a
line that says so and continues.

A snapshot whose `.github` differs from that base still exits 2 unless you
pass `--allow-workflow-diff`. A changed `remote-bar.yml` is refused with or
without that flag. If no base resolves (for example, in a shallow clone), the
wrapper compares with `origin/main`, so an unresolved base never reads as an
older base. Run `git fetch --unshallow` to avoid that;
`--base` also needs history back to the commit you name.

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
before tagging. All 35 ordinary CI checks of the main push run (the full matrix), including the twenty race shards and
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
