// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
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

// pushReadPaths are the three store reads that feed a push: the pending
// queue from its start (ReadPendingEvents, also the e2e harness), the
// pending queue after a cursor (ReadPendingEventsAfter, the push loop's
// batches), and the re-read of held events by id before a hold is released
// (ReadPendingEventsByID, MTIX-95.12). Each returns the pending events it
// reads that are among ids; after is the position the cursor read starts
// from.
var pushReadPaths = []struct {
	name string
	read func(ctx context.Context, s *sqlite.Store, after *model.SyncEvent, ids []string) ([]*model.SyncEvent, error)
}{
	{"pending queue from the start", func(ctx context.Context, s *sqlite.Store, _ *model.SyncEvent, _ []string) ([]*model.SyncEvent, error) {
		return s.ReadPendingEvents(ctx, 100)
	}},
	{"pending queue after a cursor", func(ctx context.Context, s *sqlite.Store, after *model.SyncEvent, _ []string) ([]*model.SyncEvent, error) {
		return s.ReadPendingEventsAfter(ctx, after.LamportClock, after.EventID, 100)
	}},
	{"held events re-read by id", func(ctx context.Context, s *sqlite.Store, _ *model.SyncEvent, ids []string) ([]*model.SyncEvent, error) {
		return s.ReadPendingEventsByID(ctx, ids)
	}},
}

// createPendingTestNode creates node id (sequence seq) in project TEST,
// which emits its create_node event as pending.
func createPendingTestNode(t *testing.T, s *sqlite.Store, id string, seq int) {
	t.Helper()
	require.NoError(t, s.CreateNode(context.Background(), &model.Node{
		ID:        id,
		Project:   "TEST",
		Title:     "node " + id,
		Status:    model.StatusOpen,
		NodeType:  model.NodeTypeAuto,
		Priority:  model.PriorityMedium,
		Weight:    1.0,
		Creator:   "test",
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
		Seq:       seq,
	}))
}

// pendingCreateOf returns the pending create_node event of nodeID in the
// store, read straight from sync_events (event id and lamport only).
func pendingCreateOf(t *testing.T, s *sqlite.Store, nodeID string) *model.SyncEvent {
	t.Helper()
	e := &model.SyncEvent{NodeID: nodeID}
	require.NoError(t, s.WriteDB().QueryRowContext(context.Background(), `
		SELECT event_id, lamport_clock FROM sync_events
		WHERE node_id = ? AND op_type = 'create_node' AND sync_status = 'pending'`, nodeID,
	).Scan(&e.EventID, &e.LamportClock))
	return e
}

// findCreate returns the create_node event of nodeID among events, or nil.
func findCreate(events []*model.SyncEvent, nodeID string) *model.SyncEvent {
	for _, e := range events {
		if e.OpType == model.OpCreateNode && e.NodeID == nodeID {
			return e
		}
	}
	return nil
}

// TestPushReads_EveryPath_CarriesUID is the merge-forward regression for
// MTIX-91 against MTIX-95.12: MTIX-95.12 added a cursor read of the pending
// queue and a re-read of held events by id, each with a projection that
// selected no uid. Every read that feeds a push must carry the node's
// stable uid, distinct from the event's own id, whichever path read it.
func TestPushReads_EveryPath_CarriesUID(t *testing.T) {
	for _, path := range pushReadPaths {
		t.Run(path.name, func(t *testing.T) {
			s := newTestStore(t)
			ctx := context.Background()
			createPendingTestNode(t, s, "TEST-1", 1)
			createPendingTestNode(t, s, "TEST-2", 2)
			// Re-emit the creates the way backfill does, so each event id
			// and its node's uid are DISTINCT.
			_, err := s.WriteDB().ExecContext(ctx, `DELETE FROM sync_events`)
			require.NoError(t, err)
			_, err = s.Backfill(ctx, false)
			require.NoError(t, err)
			var nodeUID string
			require.NoError(t, s.WriteDB().QueryRowContext(ctx,
				`SELECT COALESCE(uid, '') FROM nodes WHERE id = 'TEST-2'`).Scan(&nodeUID))
			require.NotEmpty(t, nodeUID, "the node must have a stable uid to carry")
			first, want := pendingCreateOf(t, s, "TEST-1"), pendingCreateOf(t, s, "TEST-2")
			require.Less(t, first.LamportClock, want.LamportClock, "backfill emits TEST-1 first")

			events, err := path.read(ctx, s, first, []string{want.EventID})
			require.NoError(t, err)
			create := findCreate(events, "TEST-2")
			require.NotNil(t, create, "the create_node of TEST-2 must be read")
			require.Equalf(t, nodeUID, create.UID,
				"MTIX-91: %s must carry the node's stable uid; an empty value "+
					"means the projection dropped the column and the event "+
					"reaches the hub with uid NULL", path.name)
			require.NotEqual(t, create.EventID, create.UID,
				"the create must carry the node's ORIGINAL uid, not its own event id")
		})
	}
}

// TestPushReads_EveryPath_TolerateNullUID covers the legitimate NULL on
// every push read: a row whose uid was never backfilled must read back as
// empty rather than erroring, because apply falls back to node_id in that
// case.
func TestPushReads_EveryPath_TolerateNullUID(t *testing.T) {
	for _, path := range pushReadPaths {
		t.Run(path.name, func(t *testing.T) {
			s := newTestStore(t)
			ctx := context.Background()
			createPendingTestNode(t, s, "TEST-1", 1)
			createPendingTestNode(t, s, "TEST-2", 2)
			_, err := s.WriteDB().ExecContext(ctx, `UPDATE sync_events SET uid = NULL`)
			require.NoError(t, err)
			first, want := pendingCreateOf(t, s, "TEST-1"), pendingCreateOf(t, s, "TEST-2")

			events, err := path.read(ctx, s, first, []string{want.EventID})
			require.NoError(t, err, "a NULL uid must not fail the read")
			create := findCreate(events, "TEST-2")
			require.NotNil(t, create)
			require.Empty(t, create.UID, "a NULL uid reads back as empty")
		})
	}
}

// TestReadPendingEvents_LeavesOutPushHeldEvents: the pending reads, from the
// start (the e2e harness too) and after a cursor, leave out an event push
// holds (MTIX-95.12), while the re-read by id still returns it, with its
// uid, so push can validate it again before it releases the hold.
func TestReadPendingEvents_LeavesOutPushHeldEvents(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	createPendingTestNode(t, s, "TEST-1", 1)
	createPendingTestNode(t, s, "TEST-2", 2)
	createPendingTestNode(t, s, "TEST-3", 3)
	first, held := pendingCreateOf(t, s, "TEST-1"), pendingCreateOf(t, s, "TEST-2")
	require.NoError(t, s.HoldPushEvents(ctx, []sqlite.QuarantinedEvent{
		{EventID: held.EventID, RawEvent: `{}`, Reason: "too large: test"}}, "test"))

	fromStart, err := s.ReadPendingEvents(ctx, 100)
	require.NoError(t, err)
	require.Nil(t, findCreate(fromStart, "TEST-2"), "a held event is left out of the queue")
	require.NotNil(t, findCreate(fromStart, "TEST-3"), "the events after a held one are read")

	afterFirst, err := s.ReadPendingEventsAfter(ctx, first.LamportClock, first.EventID, 100)
	require.NoError(t, err)
	require.Nil(t, findCreate(afterFirst, "TEST-1"), "the cursor read starts after its position")
	require.Nil(t, findCreate(afterFirst, "TEST-2"), "a held event is left out after a cursor too")
	require.NotNil(t, findCreate(afterFirst, "TEST-3"))

	byID, err := s.ReadPendingEventsByID(ctx, []string{held.EventID})
	require.NoError(t, err)
	require.Len(t, byID, 1, "the held event is re-read by id")
	require.NotEmpty(t, byID[0].UID, "the re-read carries the uid (MTIX-91)")
}

// TestReadPendingEventsByID_Boundaries: no ids reads nothing, and an id
// whose event is no longer pending (pushed) or does not exist is left out.
func TestReadPendingEventsByID_Boundaries(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	createPendingTestNode(t, s, "TEST-1", 1)
	createPendingTestNode(t, s, "TEST-2", 2)
	pushed, pending := pendingCreateOf(t, s, "TEST-1"), pendingCreateOf(t, s, "TEST-2")
	_, err := s.WriteDB().ExecContext(ctx,
		`UPDATE sync_events SET sync_status = 'pushed' WHERE event_id = ?`, pushed.EventID)
	require.NoError(t, err)

	tests := []struct {
		name string
		ids  []string
		want []string
	}{
		{"no ids", nil, nil},
		{"a pushed event", []string{pushed.EventID}, nil},
		{"an unknown id", []string{"no-such-event"}, nil},
		{"pending among others", []string{pushed.EventID, "no-such-event", pending.EventID}, []string{pending.EventID}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			events, err := s.ReadPendingEventsByID(ctx, tt.ids)
			require.NoError(t, err)
			var got []string
			for _, e := range events {
				got = append(got, e.EventID)
			}
			require.Equal(t, tt.want, got)
		})
	}
}
