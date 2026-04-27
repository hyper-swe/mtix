// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

//go:build e2e

// Package postgres contains the E2E test harness for the BYO Postgres
// store driver (MTIX-14.1). It defines a PostgresProvider abstraction
// over three runtime targets:
//
//   - "docker"  : ephemeral postgres:16-alpine container managed via the
//     local docker CLI. Hermetic, no external dependencies. Default in CI.
//   - "supabase": managed Supabase project addressed by MTIX_TEST_SUPABASE_DSN.
//     Each test isolates itself in a unique schema; cleanup runs in t.Cleanup.
//   - "neon"    : Neon serverless Postgres addressed by MTIX_TEST_NEON_DSN.
//     Uses unique schema isolation (Neon branching is a future enhancement).
//
// The same shared contract test suite runs against all three providers, so
// provider-specific quirks (pgbouncer transaction-mode constraints, serverless
// cold starts, IPv6 routing) are caught early and uniformly.
//
// Build tag: e2e — these tests do NOT run in the default unit-test build.
//
// Run examples:
//
//	go test ./e2e/postgres/ -tags=e2e -provider=docker
//	go test ./e2e/postgres/ -tags=e2e -provider=supabase
//	go test ./e2e/postgres/ -tags=e2e -provider=neon
//
// See e2e/postgres/README.md for full operational guidance.
package postgres

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"
)

// PostgresProvider abstracts the three target Postgres environments
// (Docker, Supabase, Neon) behind a single contract. Test code MUST use
// this interface rather than instantiating a specific provider directly,
// so the shared contract suite remains provider-agnostic.
//
// The interface name retains the redundant Postgres prefix intentionally:
// the package name "postgres" is itself imported by tests as
// `postgres.PostgresProvider` — the long form makes call sites explicit
// about which DB family they target.
//
//revive:disable-next-line:exported
type PostgresProvider interface {
	// Name returns the canonical provider identifier ("docker", "supabase", "neon").
	// Used in logs (DSN-redacted), test names, and CI matrix labels.
	Name() string

	// Setup provisions an isolated Postgres environment and returns a DSN
	// that the store driver can connect to. The cleanup func MUST be safe
	// to call multiple times and MUST run even on test failure (via t.Cleanup).
	//
	// The returned DSN MUST NOT be logged; callers should pass it directly
	// to the store constructor and discard. See secret_redactor.go.
	Setup(ctx context.Context, t *testing.T) (dsn string, cleanup func())

	// SupportsAdvisoryLocks reports whether the provider supports
	// session-level advisory locks. False for connections routed through
	// pgbouncer in transaction-pool mode (Supabase port 6543), since
	// pgbouncer multiplexes sessions across server connections.
	SupportsAdvisoryLocks() bool

	// SupportsPreparedStatements reports whether the provider preserves
	// prepared statements across queries. False for pgbouncer transaction
	// mode for the same multiplexing reason.
	SupportsPreparedStatements() bool
}

// Errors returned by provider construction.
var (
	// ErrProviderUnknown indicates the requested provider name is not registered.
	ErrProviderUnknown = errors.New("unknown postgres provider")

	// ErrProviderUnavailable indicates the provider cannot run in the current
	// environment (e.g. Docker not installed, required env var missing).
	// Tests should call t.Skip() rather than t.Fatal() on this error so that
	// developers without all three providers can still run a subset.
	ErrProviderUnavailable = errors.New("postgres provider unavailable")
)

// Provider name constants. Use these instead of string literals.
const (
	ProviderDocker   = "docker"
	ProviderSupabase = "supabase"
	ProviderNeon     = "neon"
)

// Environment variable names for cloud providers.
const (
	// EnvSupabaseDSN is the Supabase Postgres DSN. Read by the supabase provider.
	EnvSupabaseDSN = "MTIX_TEST_SUPABASE_DSN" //nolint:gosec // env var name, not a credential
	// EnvNeonDSN is the Neon Postgres DSN. Read by the neon provider.
	EnvNeonDSN = "MTIX_TEST_NEON_DSN" //nolint:gosec // env var name, not a credential
	// EnvProvider selects the active provider when no -provider flag is given.
	EnvProvider = "MTIX_TEST_PROVIDER"
)

// SelectProvider resolves a provider by name. Used by both the test suite
// (driven by the -provider flag) and standalone tooling. The optional opts
// allow tests to override defaults without touching package-level state.
func SelectProvider(name string, opts ...ProviderOption) (PostgresProvider, error) {
	cfg := defaultConfig()
	for _, opt := range opts {
		opt(&cfg)
	}
	switch name {
	case ProviderDocker:
		return newDockerProvider(cfg), nil
	case ProviderSupabase:
		return newSupabaseProvider(cfg)
	case ProviderNeon:
		return newNeonProvider(cfg)
	default:
		return nil, fmt.Errorf("%w: %q", ErrProviderUnknown, name)
	}
}

// ProviderOption customizes provider construction. Tests use this to inject
// fake docker / SQL hooks; production paths use defaults.
type ProviderOption func(*providerConfig)

// providerConfig collects optional inputs shared across providers.
// All fields have safe defaults; each ProviderOption sets one field.
type providerConfig struct {
	// dockerCmd is the executable used by the docker provider. Defaults to
	// "docker" on PATH; tests inject a fake binary.
	dockerCmd string
	// dockerImage is the postgres image. Defaults to postgres:16-alpine.
	dockerImage string
	// startupTimeout bounds container startup / first-connection time.
	// Defaults to 60s, large enough for cold-start providers (Neon).
	startupTimeout time.Duration
	// supabaseDSN overrides MTIX_TEST_SUPABASE_DSN (for tests).
	supabaseDSN string
	// neonDSN overrides MTIX_TEST_NEON_DSN (for tests).
	neonDSN string
	// suiteTag is appended to generated schema names so parallel CI jobs
	// don't collide. Defaults to "" (only the random suffix differentiates).
	suiteTag string
}

func defaultConfig() providerConfig {
	return providerConfig{
		dockerCmd:      "docker",
		dockerImage:    "postgres:16-alpine",
		startupTimeout: 60 * time.Second,
	}
}

// WithDockerCmd overrides the docker executable name. Used by tests to point
// at a fake binary that simulates docker behavior without root or daemons.
func WithDockerCmd(cmd string) ProviderOption {
	return func(c *providerConfig) { c.dockerCmd = cmd }
}

// WithDockerImage overrides the postgres image tag.
func WithDockerImage(image string) ProviderOption {
	return func(c *providerConfig) { c.dockerImage = image }
}

// WithStartupTimeout overrides the cold-start timeout. Tests use a short
// timeout to exercise the timeout branch quickly.
func WithStartupTimeout(d time.Duration) ProviderOption {
	return func(c *providerConfig) { c.startupTimeout = d }
}

// WithSupabaseDSN injects a DSN, bypassing MTIX_TEST_SUPABASE_DSN.
func WithSupabaseDSN(dsn string) ProviderOption {
	return func(c *providerConfig) { c.supabaseDSN = dsn }
}

// WithNeonDSN injects a DSN, bypassing MTIX_TEST_NEON_DSN.
func WithNeonDSN(dsn string) ProviderOption {
	return func(c *providerConfig) { c.neonDSN = dsn }
}

// WithSuiteTag tags generated schema/db names so parallel CI shards don't
// collide. Caller-supplied tag is sanitized to [a-z0-9_].
func WithSuiteTag(tag string) ProviderOption {
	return func(c *providerConfig) { c.suiteTag = sanitizeTag(tag) }
}

// uniqueSuffix returns a 16-char hex suffix safe to embed in schema/db names.
// Falls back to a timestamp if crypto/rand is unavailable, never panics.
func uniqueSuffix() string {
	var buf [8]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 16)
	}
	return hex.EncodeToString(buf[:])
}

// sanitizeTag lower-cases and strips any byte outside [a-z0-9_], so the tag
// is safe to embed verbatim in a Postgres identifier without quoting.
func sanitizeTag(tag string) string {
	tag = strings.ToLower(tag)
	out := make([]byte, 0, len(tag))
	for i := 0; i < len(tag); i++ {
		c := tag[i]
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '_':
			out = append(out, c)
		}
	}
	return string(out)
}

// uniqueSchemaName builds a schema identifier guaranteed to be unique across
// concurrent CI runs. Format: mtix_test_<tag>_<unix>_<random>.
// All components are lower-case; total length stays well under PG's 63-char
// identifier limit.
func uniqueSchemaName(tag string) string {
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	if tag == "" {
		return fmt.Sprintf("mtix_test_%s_%s", ts, uniqueSuffix())
	}
	return fmt.Sprintf("mtix_test_%s_%s_%s", tag, ts, uniqueSuffix())
}

// uniqueDBName builds a database name suitable for the docker provider.
// Distinct prefix from schema names so cleanup tooling can target either.
func uniqueDBName(tag string) string {
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	if tag == "" {
		return fmt.Sprintf("mtix_db_%s_%s", ts, uniqueSuffix())
	}
	return fmt.Sprintf("mtix_db_%s_%s_%s", tag, ts, uniqueSuffix())
}

// dockerAvailable reports whether the docker CLI is callable. Tests call
// t.Skip when this returns false so contributors without Docker can still
// run the cloud-provider tests.
func dockerAvailable(cmd string) bool {
	_, err := exec.LookPath(cmd)
	return err == nil
}

// resolveDSN returns the DSN for a cloud provider, preferring the explicit
// option over the environment variable. Returns ErrProviderUnavailable when
// neither is set so callers can t.Skip cleanly.
func resolveDSN(explicit, envVar string) (string, error) {
	if explicit != "" {
		return explicit, nil
	}
	dsn := os.Getenv(envVar)
	if dsn == "" {
		return "", fmt.Errorf("%w: env %s not set", ErrProviderUnavailable, envVar)
	}
	return dsn, nil
}
