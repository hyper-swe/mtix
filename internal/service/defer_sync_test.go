// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service_test

import (
	"context"
	"database/sql"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
	"github.com/hyper-swe/mtix/internal/sync/clock"
)

// peerDeferEvent builds the 0.5.x event a peer emits when it defers a node
// with no wake time: a transition_status open→deferred, keyed above every
// event this replica holds so it wins.
func peerDeferEvent(t *testing.T, s *sqlite.Store, nodeID string) *model.SyncEvent {
	t.Helper()
	var lamport int64
	require.NoError(t, s.QueryRow(context.Background(),
		`SELECT COALESCE(MAX(lamport_clock), 0) FROM sync_events`).Scan(&lamport))
	payload, err := model.EncodePayload(&model.TransitionStatusPayload{
		From: model.StatusOpen, To: model.StatusDeferred, Reason: "deferred via CLI",
	})
	require.NoError(t, err)
	return &model.SyncEvent{
		EventID:           clock.MustNewEventID(),
		ProjectPrefix:     "PROJ",
		NodeID:            nodeID,
		OpType:            model.OpTransitionStatus,
		Payload:           payload,
		WallClockTS:       time.Date(2030, 6, 1, 12, 30, 0, 0, time.UTC).UnixMilli(),
		LamportClock:      lamport + 10,
		VectorClock:       model.VectorClock{"peer-b": 1},
		AuthorID:          "peer-b",
		AuthorMachineHash: "bbbbbbbbbbbbbbbb",
	}
}

// systemTransitions counts the transition_status events this replica emitted
// as "system", which is what the wake pass emits.
func systemTransitions(t *testing.T, s *sqlite.Store, nodeID string) int {
	t.Helper()
	var n int
	require.NoError(t, s.QueryRow(context.Background(),
		`SELECT COUNT(*) FROM sync_events WHERE node_id = ? AND op_type = ? AND author_id = ?`,
		nodeID, string(model.OpTransitionStatus), "system").Scan(&n))
	return n
}

// TestWakeDeferredNodes_PeerDefersAfterWake_StaysDeferred is the review probe
// (MTIX-95.22 round 2): a node deferred here with a wake time that passed is
// reopened by the wake pass; a peer then defers it with no wake time, and the
// deferral arrives through sync. Before the fix the wake pass left the old
// wake time on the node and the sync apply kept it, so the next wake pass
// reopened the peer's deferral and emitted a transition_status that undid it
// on every replica.
func TestWakeDeferredNodes_PeerDefersAfterWake_StaysDeferred(t *testing.T) {
	now := time.Date(2030, 6, 1, 12, 0, 0, 0, time.UTC)
	clk := func() time.Time { return now }
	s, err := sqlite.New(t.TempDir(), slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	s.SetClock(clk)
	svc := service.NewNodeService(s, nil, nil, nil, clk)
	bg := service.NewBackgroundService(s, nil, nil, clk)
	ctx := context.Background()
	node, err := svc.CreateNode(ctx, &service.CreateNodeRequest{Project: "PROJ", Title: "Probe", Creator: "a"})
	require.NoError(t, err)
	past := now.Add(-time.Hour)
	require.NoError(t, svc.DeferNode(ctx, node.ID, &past, "deferred via CLI", "agent-a"))

	require.NoError(t, bg.RunScan(ctx))
	woken, err := s.GetNode(ctx, node.ID)
	require.NoError(t, err)
	require.Equal(t, model.StatusOpen, woken.Status)
	assert.Nil(t, woken.DeferUntil, "the wake pass clears the wake time it acted on")
	require.Equal(t, 1, systemTransitions(t, s, node.ID))

	require.NoError(t, s.WithTx(ctx, func(tx *sql.Tx) error {
		return sqlite.IdempotentApply(ctx, tx, peerDeferEvent(t, s, node.ID))
	}))
	deferred, err := s.GetNode(ctx, node.ID)
	require.NoError(t, err)
	require.Equal(t, model.StatusDeferred, deferred.Status)
	assert.Nil(t, deferred.DeferUntil, "a deferral received through sync carries no wake time")

	require.NoError(t, bg.RunScan(ctx))
	after, err := s.GetNode(ctx, node.ID)
	require.NoError(t, err)
	assert.Equal(t, model.StatusDeferred, after.Status, "the peer's deferral is not undone")
	assert.Equal(t, 1, systemTransitions(t, s, node.ID), "no second wake event")
}

// TestApplyPeerDeferral_StaleWakeTime_IsCleared isolates the sync half of the
// probe: a node that still carries an old wake time (seeded directly, as a
// store written before the fix could hold) receives a peer's deferral through
// sync. The apply stores no wake time, so the wake pass leaves the deferral
// alone (MTIX-95.22 round 2).
func TestApplyPeerDeferral_StaleWakeTime_IsCleared(t *testing.T) {
	now := time.Date(2030, 6, 1, 12, 0, 0, 0, time.UTC)
	clk := func() time.Time { return now }
	s, err := sqlite.New(t.TempDir(), slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	s.SetClock(clk)
	svc := service.NewNodeService(s, nil, nil, nil, clk)
	bg := service.NewBackgroundService(s, nil, nil, clk)
	ctx := context.Background()
	node, err := svc.CreateNode(ctx, &service.CreateNodeRequest{Project: "PROJ", Title: "Stale", Creator: "a"})
	require.NoError(t, err)
	_, err = s.WriteDB().ExecContext(ctx,
		`UPDATE nodes SET defer_until = ? WHERE id = ?`, "2030-06-01T11:00:00Z", node.ID)
	require.NoError(t, err)

	require.NoError(t, s.WithTx(ctx, func(tx *sql.Tx) error {
		return sqlite.IdempotentApply(ctx, tx, peerDeferEvent(t, s, node.ID))
	}))
	require.NoError(t, bg.RunScan(ctx))

	got, err := s.GetNode(ctx, node.ID)
	require.NoError(t, err)
	assert.Equal(t, model.StatusDeferred, got.Status)
	assert.Nil(t, got.DeferUntil)
	assert.Zero(t, systemTransitions(t, s, node.ID))
}
