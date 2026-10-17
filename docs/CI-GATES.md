# CI and release gates

Every pull request and push to `main` runs **17 CI checks**. Merge only the
reviewed commit with every check successful; a skipped or cancelled check is
not a passing check.

| Checks | Count | Purpose |
| --- | ---: | --- |
| `test-go-shard (OS, shard)` | 6 | Three package shards on each of `ubuntu-22.04` and `macos-latest` |
| `test-go` | 1 | Require all six Go shard jobs to succeed |
| `test-web` | 1 | Web tests |
| `lint` | 1 | Pinned Go lint |
| `pg-16 package`, `pg-17 package` | 6 | CLI, transport and end-to-end suites against each PostgreSQL version |
| `test-fault-injection` | 1 | Disk-full and crash durability conformance |
| `build` | 1 | Build and verify the binary after its prerequisite checks |

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

Because the workflow is dispatched from `main`, a change to it takes effect
only after it is merged; contract tests and `--dry-run` cover it before then.

## Release gates

Run [the release checklist](RELEASE-CHECKLIST.md) on the exact CI-green commit
before tagging. All 17 ordinary CI checks, including the six shards and
`test-go` aggregate, must pass alongside independent review and the other
documented quality gates.

The tag-triggered release workflow retains its full preflight race and
coverage invocations with `-p 1`, unchanged test flags and the 20-minute
per-package timeout. Its PostgreSQL-enabled packages share a database and
remain serialized. Sharding ordinary CI does not replace the release
preflight, security scans, coverage gate, fault-injection or cloud contracts.
