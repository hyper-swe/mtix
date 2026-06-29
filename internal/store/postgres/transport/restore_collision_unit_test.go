// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package transport_test

import (
	"context"
	"testing"

	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
	"github.com/stretchr/testify/require"
)

// PG-free guards for the MTIX-30.8 restore-collision surface (ADR-003 §6.1,
// §15): a nil/unopened pool must fail loud, never panic, on every entry point.

func TestRestoreEpoch_NilPoolGuards(t *testing.T) {
	var p *transport.Pool
	ctx := context.Background()

	_, err := p.CurrentRestoreEpoch(ctx)
	require.ErrorContains(t, err, "pool not open")

	_, err = p.MarkRestored(ctx)
	require.ErrorContains(t, err, "pool not open")
}

func TestCollisions_NilPoolGuards(t *testing.T) {
	var p *transport.Pool
	ctx := context.Background()

	_, err := p.ListOpenCollisions(ctx, "MTIX")
	require.ErrorContains(t, err, "pool not open")

	_, err = p.GetOpenCollision(ctx, 1)
	require.ErrorContains(t, err, "pool not open")

	_, err = p.ResolveCollision(ctx, 1, "winner", "MTIX-1.5", "admin")
	require.ErrorContains(t, err, "pool not open")

	_, _, _, _, err = p.PushEventsWithCollisions(ctx, nil)
	require.NoError(t, err, "an empty batch is a no-op, even on a nil pool")
}
