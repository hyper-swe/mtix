// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

import (
	"context"
	"database/sql"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// wakeTimeOf reads a node's defer_until column verbatim.
func wakeTimeOf(t *testing.T, s *sqlite.Store, id string) sql.NullString {
	t.Helper()
	var v sql.NullString
	require.NoError(t, s.QueryRow(context.Background(),
		`SELECT defer_until FROM nodes WHERE id = ?`, id).Scan(&v))
	return v
}

// TestCascadeCancel_DeferredDescendantWithWakeTime_ClearsItAndKeepsCascadeEffects
// ports MTIX-95.22 onto the MTIX-95.21 cascade (MTIX-95.32): a cascade cancel
// that reaches a deferred descendant with a wake time clears that wake time in
// the same transaction, as a cancel of the node itself does, so the cancelled
// node keeps no wake time that could later wake anything. The MTIX-95.21
// cascade effects stay: the descendant's dependents are unblocked and the
// root's progress is recomputed.
func TestCascadeCancel_DeferredDescendantWithWakeTime_ClearsItAndKeepsCascadeEffects(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	seedCascadeNodes(t, s,
		ccNode(ccRoot, model.StatusOpen), ccNode(ccChild, model.StatusOpen),
		ccNode(ccChild2, model.StatusDone), ccNode(ccOutside, model.StatusOpen))
	addBlocker(ctx, t, s, ccChild, ccOutside, cascadeTestTime)
	require.Equal(t, model.StatusBlocked, statusOf(t, s, ccOutside))
	require.NoError(t, s.TransitionStatus(ctx, ccChild, model.StatusDeferred, "later", "test"))
	// Test seam: give the deferred child a wake time directly, so this test
	// runs against any build of the store.
	_, err := s.WriteDB().ExecContext(ctx,
		`UPDATE nodes SET defer_until = ? WHERE id = ?`, "2031-01-01T00:00:00Z", ccChild)
	require.NoError(t, err)
	require.True(t, wakeTimeOf(t, s, ccChild).Valid)

	require.NoError(t, s.CancelNode(ctx, ccRoot, "feature dropped", "pm-1", true))

	assert.Equal(t, model.StatusCancelled, statusOf(t, s, ccChild), "the cascade cancels the deferred child")
	assert.False(t, wakeTimeOf(t, s, ccChild).Valid, "the cascade clears the child's wake time")
	assert.Equal(t, model.StatusOpen, statusOf(t, s, ccOutside), "MTIX-95.21: the child's dependent is unblocked")
	assert.InDelta(t, 1.0, progressOf(t, s, ccRoot), 1e-9,
		"MTIX-95.21: the root's progress is recomputed without the cancelled child")
}
