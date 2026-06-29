// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package transport_test

import (
	"context"
	"testing"
	"time"

	syncpkg "github.com/hyper-swe/mtix/internal/sync"

	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
	"github.com/stretchr/testify/require"
)

// TestProjectUIDCutoverReady_NilPool: the gate helper surfaces an error on
// a nil pool rather than reporting a (false) ready, so a caller bug can
// never be read as "safe to cut over".
func TestProjectUIDCutoverReady_NilPool(t *testing.T) {
	var p *transport.Pool
	ok, err := p.ProjectUIDCutoverReady(context.Background(), "MTIX")
	require.Error(t, err)
	require.False(t, ok)
}

// TestProjectUIDCutoverReady_DeferredThenEnabled is the REQUIRED cutover
// gating case (ADR-003 §7 Phase 3): a project stays on node_id keying
// (gate CLOSED) while any active client is below UIDKeyedMinVersion, and
// switches to uid-authoritative keying (gate OPEN) only once every active
// client is at/above it. The gate defers cutover; it never forces it.
func TestProjectUIDCutoverReady_DeferredThenEnabled(t *testing.T) {
	pool := openTestPool(t)
	require.NoError(t, pool.Migrate(context.Background()))
	ctx := context.Background()

	const project = "MTIX"
	now := time.Now().UTC()

	// One up-to-date client and one stale (pre-UID) client, both active.
	require.NoError(t, pool.UpsertProjectClient(ctx, project, "aaaaaaaaaaaaaaaa", syncpkg.UIDKeyedMinVersion))
	require.NoError(t, pool.UpsertProjectClient(ctx, project, "bbbbbbbbbbbbbbbb", "0.1.9"))

	ready, err := pool.ProjectUIDCutoverReady(ctx, project)
	require.NoError(t, err)
	require.False(t, ready,
		"cutover must be DEFERRED while a mixed-version project has any client below UIDKeyedMinVersion")

	// The stale client upgrades.
	require.NoError(t, pool.UpsertProjectClient(ctx, project, "bbbbbbbbbbbbbbbb", "0.2.0"))

	ready, err = pool.ProjectUIDCutoverReady(ctx, project)
	require.NoError(t, err)
	require.True(t, ready,
		"cutover must be ENABLED once every active client is at/above UIDKeyedMinVersion")

	_ = now
}
