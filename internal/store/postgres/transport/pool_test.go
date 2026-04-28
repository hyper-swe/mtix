// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package transport_test

import (
	"context"
	"testing"

	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
	"github.com/stretchr/testify/require"
)

// These tests exercise the PG-free branches of the Pool API: nil
// receivers, error-paths for invalid DSN, and the defaults helper.
// Tests requiring a live PG live in integration_test.go and skip when
// MTIX_PG_TEST_DSN is unset.

func TestPool_HealthCheckOnNilPool(t *testing.T) {
	var p *transport.Pool
	err := p.HealthCheck(context.Background())
	require.Error(t, err, "nil pool MUST surface an error, never panic")
}

func TestPool_HealthCheckOnZeroPool(t *testing.T) {
	p := &transport.Pool{}
	err := p.HealthCheck(context.Background())
	require.Error(t, err, "zero-value pool MUST surface an error, never panic")
}

func TestPool_CloseOnNilPool(t *testing.T) {
	var p *transport.Pool
	require.NotPanics(t, p.Close, "nil receiver Close MUST be safe")
}

func TestPool_CloseOnZeroPool(t *testing.T) {
	p := &transport.Pool{}
	require.NotPanics(t, p.Close, "zero-value Close MUST be safe")
}

func TestPool_InnerOnNilPool(t *testing.T) {
	var p *transport.Pool
	require.Nil(t, p.Inner())
}

func TestPool_MigrateOnNilPool(t *testing.T) {
	var p *transport.Pool
	err := p.Migrate(context.Background())
	require.Error(t, err)
	require.Contains(t, err.Error(), "pool not open")
}

func TestPool_NewWithBadDSN(t *testing.T) {
	_, err := transport.New(context.Background(), "not-a-url", transport.Options{})
	require.Error(t, err, "non-URL DSN must fail at TLS-posture step")
}

func TestPool_NewWithWeakerSSLModeOnRemoteRefused(t *testing.T) {
	_, err := transport.New(context.Background(),
		"postgres://user:pass@db.example.com/db?sslmode=disable",
		transport.Options{InsecureTLS: true},
	)
	require.Error(t, err, "weak sslmode on remote host must be refused even with --insecure-tls")
}

func TestDefaultPoolDefaults(t *testing.T) {
	d := transport.DefaultPoolDefaults()
	require.Equal(t, int32(8), d.MaxConns)
	require.NotZero(t, d.ConnLifetime)
	require.NotZero(t, d.StatementTimeout)
	require.NotZero(t, d.HealthCheckPeriod)
}

func TestAdvisoryLockKeyStable(t *testing.T) {
	require.Equal(t, "mtix_sync_migration", transport.AdvisoryLockKey,
		"advisory-lock key MUST stay verbatim — every CLI hashes the same string (FR-18.14)")
}
