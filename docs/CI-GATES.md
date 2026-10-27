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
of 17). The remote bar's Linux run is `make lint-go && make test-race-core`
(every package except `cmd/mtix`) plus the three `cmd/mtix` shards; together
they cover exactly what `make test-race` covers. The `--os macos-latest` run
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
