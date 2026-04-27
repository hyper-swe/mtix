# E2E Postgres Test Harness (MTIX-14.9)

This directory contains the foundational test infrastructure for the BYO
Postgres rollout (MTIX-14). One **shared contract suite** runs against
three Postgres providers behind one common interface:

| Provider   | When it runs            | Setup cost | DSN source                        |
|------------|-------------------------|------------|-----------------------------------|
| `docker`   | Every PR, every laptop  | Free       | Auto (testcontainers/CLI)         |
| `supabase` | Release tags only       | Free tier  | `MTIX_TEST_SUPABASE_DSN` secret   |
| `neon`     | Release tags only       | Free tier  | `MTIX_TEST_NEON_DSN` secret       |

**Build tag:** every file in this directory has `//go:build e2e`. The
default `go test ./...` ignores them; you must opt in with `-tags=e2e`.

## Quick start

```bash
# Local: docker provider (requires a running docker daemon)
make test-pg-docker

# Local: cloud providers (require DSN env vars)
export MTIX_TEST_SUPABASE_DSN='postgres://...:5432/postgres?sslmode=verify-full'
export MTIX_TEST_NEON_DSN='postgres://...neon.tech/db?sslmode=require'
make test-pg-supabase
make test-pg-neon

# Run whichever providers have credentials configured
make test-pg-all
```

## Provider selection

Tests pick a provider in this order:

1. The `-provider=<name>` flag on `go test`
2. The `MTIX_TEST_PROVIDER` environment variable
3. The default: `docker`

```bash
go test ./e2e/postgres/ -tags=e2e -provider=docker
go test ./e2e/postgres/ -tags=e2e -provider=supabase
go test ./e2e/postgres/ -tags=e2e -provider=neon
```

## Provider quirks (important)

### Supabase

Supabase exposes Postgres on **two** ports:

- **5432 (direct):** session pooling. Prepared statements and advisory
  locks work normally. Concurrency capped lower on the free tier.
- **6543 (Supavisor / pgbouncer transaction mode):** a connection is
  borrowed for one statement at a time, so **prepared statements break**
  and **session-level advisory locks become unreliable**.

The harness detects the port and reports capability flags via
`SupportsPreparedStatements()` and `SupportsAdvisoryLocks()`. Tests that
need either feature `t.Skip` automatically when the active provider
lacks it. **For the contract suite, prefer the 5432 DSN.**

### Neon

Neon is serverless: an idle compute instance spins down after ~5
minutes, and the next connection cold-starts in 2–5 seconds. The
default startup timeout (`60s`) is sized to absorb this. Do **not**
shorten it without understanding the cold-start window.

Neon also routes connections through an HTTP-style proxy that injects
SNI headers; standard `pgx` / `lib/pq` drivers handle this transparently
as long as `?sslmode=require` is part of the DSN. mtix's connection-string
validator enforces `verify-full`/`require` for non-loopback hosts.

### Docker

The Docker provider uses the local `docker` CLI to launch
`postgres:16-alpine` per test. Connections are over loopback with
`sslmode=disable` (no TLS — no useful threat model on `127.0.0.1`). The
provider auto-skips when:

- `docker` is not on `PATH`
- the daemon is unreachable
- the container fails to become ready within `startupTimeout`

This means contributors without Docker installed can still run the
cloud-provider tests; the suite stays green.

## Cost guidance

The free tiers of Supabase and Neon are sufficient for our CI use:

- **Supabase:** 500 MB DB; 2 paused projects on free plan. The contract
  suite creates and drops a fresh schema per test, so storage growth is
  bounded by leaks — see "Cleanup" below.
- **Neon:** 0.5 GiB storage; 191 compute hours / month. Each contract
  test connects briefly, so total compute usage per release is well
  under the cap.

If you exhaust free-tier quotas, `make test-pg-cloud` will start failing
with provider-specific errors. **Do not** upgrade to a paid tier without
team consensus — the Docker provider gives full coverage on every PR
and is the primary safety net.

## Cleanup

Tests register `t.Cleanup` hooks that drop their per-test schema. If a
test crashes hard (SIGKILL, OOM) the cleanup may not run. Use the
nuclear cleanup tool to reclaim orphaned schemas older than 24 hours:

```bash
# Dry run (default): list what would be dropped
go run -tags=cleanup ./tools/cleanup-test-schemas.go \
    -dsn "$MTIX_TEST_SUPABASE_DSN" \
    -older-than 24h

# Actually drop
go run -tags=cleanup ./tools/cleanup-test-schemas.go \
    -dsn "$MTIX_TEST_SUPABASE_DSN" \
    -older-than 24h \
    -dry-run=false
```

Schemas not matching the `mtix_test_*` prefix are NEVER touched.

## Secret hygiene

DSNs contain database credentials and MUST NOT appear in logs, error
messages, PR comments, or screenshots. The harness enforces this via:

1. **`RedactingWriter` on `os.Stderr`** (see `secret_redactor.go`). Any
   write whose content matches a Postgres DSN pattern is replaced with
   `<REDACTED:postgres-dsn>` before being delivered to the original
   stderr.
2. **`RedactDSN()`** — used by every `t.Skipf` / `t.Logf` call that
   includes provider state.
3. **Regression test** — `TestProvider_DSNNeverInTestOutput` asserts a
   secret-laden synthetic stderr is fully scrubbed.
4. **Workflow secrets** — GitHub Actions handles the cloud DSNs. Never
   echo them in `run:` steps. The release workflow consumes them as
   `env:` only.

If you find a leak, file a security advisory; do **not** open a public
PR.

## Troubleshooting

| Symptom | Likely cause | Fix |
|---------|--------------|-----|
| `provider "docker" unavailable: docker binary "docker" not on PATH` | Docker isn't installed | Install Docker Desktop / Colima, or run a different provider |
| `docker run: exit 125` | Daemon not running | `open -a Docker` (mac) or `systemctl start docker` |
| `readiness timeout` (Docker) | Very slow first pull | Pre-pull: `docker pull postgres:16-alpine`, then re-run |
| `provider "supabase" unavailable: env MTIX_TEST_SUPABASE_DSN not set` | Cloud DSN missing | Export the env var or run `-provider=docker` |
| `pq: prepared statement "stmtcache_..." does not exist` | Hit pgbouncer transaction mode | Use the `:5432` direct DSN, not `:6543` |
| Cold start timeout (Neon) | Compute spun down, network jitter | Re-run; if persistent, raise `WithStartupTimeout` |
| `pq: must be owner of schema mtix_test_...` | Cleanup script hit a non-orphan | The cleanup tool refuses non-`mtix_test_*` schemas; check the input |

## Where the pieces live

```
e2e/postgres/
├── README.md                  ← you are here
├── provider.go                ← PostgresProvider interface + selector
├── provider_docker.go         ← Docker (CLI-driven) implementation
├── provider_supabase.go       ← Supabase (DSN-driven) implementation
├── provider_neon.go           ← Neon (DSN-driven) implementation
├── secret_redactor.go         ← DSN scrubbing for logs and stderr
├── main_test.go               ← TestMain + flag parsing + stderr wrap
├── contract_test.go           ← shared contract suite (10 tests)
├── quirks_test.go             ← provider-specific quirk assertions
└── provider_test.go           ← harness self-tests (the 6 in the ticket)
tools/
└── cleanup-test-schemas.go    ← nuclear orphan-schema cleanup
```

## Status

- **Docker provider:** implemented, runs end-to-end against a real
  container when Docker is available, skips cleanly otherwise.
- **Supabase / Neon providers:** Setup path implemented (unique-schema
  DSN composition, capability detection). Active SQL execution is
  stubbed pending **MTIX-14.1** (the BYO PG store driver). Until 14.1
  lands, contract tests `t.Skip` with `pg store driver not yet
  implemented (MTIX-14.1)` so the harness stays CI-green.
- **CI integration:** `ci.yml` runs the Docker job on every PR;
  `release.yml` runs Supabase + Neon on tag, gated on the secrets being
  present.

## Adding a new provider

1. Implement `PostgresProvider` in `e2e/postgres/provider_<name>.go`.
2. Register a constructor in `SelectProvider`.
3. Add a constant for any DSN env var.
4. Document quirks here and add tests to `quirks_test.go`.
5. Add a `make test-pg-<name>` target.
6. Add a CI job (PR or release, depending on cost).

Keep the contract suite untouched — that's the point of the abstraction.
