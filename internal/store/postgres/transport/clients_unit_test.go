// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package transport_test

import (
	"context"
	"testing"
	"time"

	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
	"github.com/stretchr/testify/require"
)

// These tests exercise the guard / error branches of the client-gate
// API without a database — they run everywhere, including local laptops
// and CI without MTIX_PG_TEST_DSN.

func TestClientActiveWindow_Sane(t *testing.T) {
	// A generous-but-bounded window: long enough not to drop a
	// merely-quiet client mid-upgrade, short enough that a departed
	// client eventually stops holding the gate closed.
	require.Equal(t, 30*24*time.Hour, transport.ClientActiveWindow)
}

func TestUpsertProjectClient_NilPool(t *testing.T) {
	var p *transport.Pool
	err := p.UpsertProjectClient(context.Background(), "MTIX", "m", "0.2.0")
	require.Error(t, err)
}

func TestProjectAllClientsAtLeast_NilPool(t *testing.T) {
	var p *transport.Pool
	ok, err := p.ProjectAllClientsAtLeast(context.Background(), "MTIX", "0.2.0")
	require.Error(t, err)
	require.False(t, ok)
}

func TestProjectAllClientsAtLeast_EmptyPrefix(t *testing.T) {
	// A closed (but non-nil) pool still validates args before querying;
	// an empty prefix is a caller bug surfaced as an error.
	dsn := requireTestDSN(t)
	pool, err := transport.New(context.Background(), dsn, transport.Options{InsecureTLS: true})
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	ok, err := pool.ProjectAllClientsAtLeast(context.Background(), "", "0.2.0")
	require.Error(t, err)
	require.False(t, ok)
}

func TestProjectAllClientsAtLeast_BadMinVersion(t *testing.T) {
	dsn := requireTestDSN(t)
	pool, err := transport.New(context.Background(), dsn, transport.Options{InsecureTLS: true})
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	ok, err := pool.ProjectAllClientsAtLeast(context.Background(), "MTIX", "not-a-version")
	require.Error(t, err)
	require.False(t, ok)
}

func TestSetClientIdentity_NilPoolNoPanic(t *testing.T) {
	var p *transport.Pool
	require.NotPanics(t, func() { p.SetClientIdentity("m", "0.2.0") })
}
