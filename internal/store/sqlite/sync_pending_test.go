// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
)

// MTIX-91: the push path read the pending queue without selecting uid, so
// every event reached the hub with uid NULL. The hub resolves a registered
// create's effective uid as "stored uid, or event_id when NULL", so the
// effective uid was always an event_id — and a re-emitted create carrying
// the node's real uid could never match it. The hub then treated the same
// logical node as a different one and demanded a renumber, making the
// ADR-003 §6/§9 same-node no-op unreachable for every pushed create.
//
// The uid must therefore survive the read that feeds PushEvents.

// TestReadPendingEvents_CarriesUID is the regression: the uid on a pending
// event must reach the caller, and it must be the node's stable uid rather
// than the event's own id.
func TestReadPendingEvents_CarriesUID(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	require.NoError(t, s.CreateNode(ctx, &model.Node{
		ID:        "TEST-1",
		Project:   "TEST",
		Title:     "node",
		Status:    model.StatusOpen,
		NodeType:  model.NodeTypeAuto,
		Priority:  model.PriorityMedium,
		Weight:    1.0,
		Creator:   "test",
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
		Seq:       1,
	}))

	// Re-emit the create the way backfill does, so the event id and the
	// node uid are DISTINCT — which is exactly the case the hub has to
	// recognise as the same logical node.
	_, err := s.WriteDB().ExecContext(ctx, `DELETE FROM sync_events`)
	require.NoError(t, err)
	_, err = s.Backfill(ctx, false)
	require.NoError(t, err)

	var nodeUID string
	require.NoError(t, s.WriteDB().QueryRowContext(ctx,
		`SELECT COALESCE(uid, '') FROM nodes WHERE id = 'TEST-1'`).Scan(&nodeUID))
	require.NotEmpty(t, nodeUID, "the node must have a stable uid to carry")

	events, err := s.ReadPendingEvents(ctx, 100)
	require.NoError(t, err)
	require.NotEmpty(t, events)

	var create *model.SyncEvent
	for _, e := range events {
		if e.OpType == model.OpCreateNode && e.NodeID == "TEST-1" {
			create = e
			break
		}
	}
	require.NotNil(t, create, "a create_node for TEST-1 must be pending")

	require.Equalf(t, nodeUID, create.UID,
		"MTIX-91: the pending read must carry the node's stable uid. Got %q. "+
			"An empty value here means the projection dropped the column and "+
			"every pushed event will land on the hub with uid NULL, which makes "+
			"the same-logical-node no-op unreachable and turns every re-emitted "+
			"create into a spurious renumber.", create.UID)

	require.NotEqual(t, create.EventID, create.UID,
		"a re-emitted create must carry the node's ORIGINAL uid, not its own "+
			"fresh event id — otherwise the hub cannot tell it is the same node")
}

// TestReadPendingEvents_Tolerform covers the legitimate NULL: a row whose
// uid was never backfilled must read back as empty rather than erroring,
// because apply falls back to node_id in that case.
func TestReadPendingEvents_TolerateNullUID(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	require.NoError(t, s.CreateNode(ctx, &model.Node{
		ID:        "TEST-1",
		Project:   "TEST",
		Title:     "node",
		Status:    model.StatusOpen,
		NodeType:  model.NodeTypeAuto,
		Priority:  model.PriorityMedium,
		Weight:    1.0,
		Creator:   "test",
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
		Seq:       1,
	}))

	_, err := s.WriteDB().ExecContext(ctx, `UPDATE sync_events SET uid = NULL`)
	require.NoError(t, err)

	events, err := s.ReadPendingEvents(ctx, 100)
	require.NoError(t, err, "a NULL uid must not fail the read")
	require.NotEmpty(t, events)
	require.Empty(t, events[0].UID, "a NULL uid reads back as empty")
}
