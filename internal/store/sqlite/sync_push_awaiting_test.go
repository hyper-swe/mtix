// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

import (
	"context"
	"sort"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
)

// describeEvents names each event "op@number", for comparing sets of events.
func describeEvents(events []*model.SyncEvent, pick map[string]struct{}) []string {
	var out []string
	for _, e := range events {
		if _, ok := pick[e.EventID]; ok {
			out = append(out, string(e.OpType)+"@"+e.NodeID)
		}
	}
	sort.Strings(out)
	return out
}

// TestEventsAwaitingCreation_WaitsForEveryPendingCreationAboveOrBehindTheEvent
// is the creation gate of push (MTIX-95.37): an event waits while the
// creation of its task, of a task above it, or of the task a link points to
// is pending; a creation waits only for the tasks above it.
func TestEventsAwaitingCreation_WaitsForEveryPendingCreationAboveOrBehindTheEvent(t *testing.T) {
	tests := []struct {
		name   string
		pushed []string // numbers whose creation is marked pushed
		want   []string
	}{
		{"nothing is on the hub", nil, []string{
			"claim@PRJX-1.1", "create_node@PRJX-1.1", "create_node@PRJX-1.1.1", "link_dep@PRJX-1",
			"link_dep@PRJX-2", "unlink_dep@PRJX-1", "update_field@PRJX-1",
		}},
		{"the roots are on the hub", []string{"PRJX-1", "PRJX-2"}, []string{
			"claim@PRJX-1.1", "create_node@PRJX-1.1.1", "link_dep@PRJX-2",
		}},
		{"the child is on the hub too", []string{"PRJX-1", "PRJX-2", "PRJX-1.1"}, []string{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, _ := subtreeFixture(t)
			ctx := context.Background()
			for _, number := range tt.pushed {
				_, err := s.WriteDB().ExecContext(ctx, `
					UPDATE sync_events SET sync_status = 'pushed'
					WHERE op_type = 'create_node' AND event_id = (SELECT uid FROM nodes WHERE id = ?)`, number)
				require.NoError(t, err)
			}
			events, err := s.ReadPendingEventsAfter(ctx, 0, "", 100)
			require.NoError(t, err)

			waiting, err := s.EventsAwaitingCreation(ctx, events)
			require.NoError(t, err)

			got := describeEvents(events, waiting)
			if got == nil {
				got = []string{}
			}
			require.Equal(t, tt.want, got)
		})
	}
}

// TestEventsAwaitingCreation_FindsTheTaskByUidNotByNumber: after a renumber
// the events carry the task's uid, and another task can hold the old number;
// the event waits for the creation of the task its uid names.
func TestEventsAwaitingCreation_FindsTheTaskByUidNotByNumber(t *testing.T) {
	s, uid := subtreeFixture(t)
	ctx := context.Background()
	_, err := s.RenumberForHubRejection(ctx, uid)
	require.NoError(t, err)
	// A teammate's task takes the old number PRJX-1 and is on the hub.
	require.NoError(t, s.CreateNode(ctx, mkNode("PRJX-1", "", "PRJX", "teammate")))
	_, err = s.WriteDB().ExecContext(ctx, `
		UPDATE sync_events SET sync_status = 'pushed'
		WHERE op_type = 'create_node' AND event_id = (SELECT uid FROM nodes WHERE id = 'PRJX-1')`)
	require.NoError(t, err)
	events, err := s.ReadPendingEventsAfter(ctx, 0, "", 100)
	require.NoError(t, err)

	waiting, err := s.EventsAwaitingCreation(ctx, events)
	require.NoError(t, err)

	for _, e := range events {
		if e.OpType == model.OpUpdateField {
			require.Contains(t, waiting, e.EventID, "the root's edit waits for the renumbered creation")
		}
	}
}

// TestEventsAwaitingCreation_EmptyAndUnknown: no events wait for nothing; an
// event whose uid no node holds waits for nothing.
func TestEventsAwaitingCreation_EmptyAndUnknown(t *testing.T) {
	s := newUIDTestStore(t)
	ctx := context.Background()
	waiting, err := s.EventsAwaitingCreation(ctx, nil)
	require.NoError(t, err)
	require.Empty(t, waiting)

	orphan := &model.SyncEvent{EventID: "e1", NodeID: "PRJX-9", UID: "uid-gone", OpType: model.OpClaim}
	waiting, err = s.EventsAwaitingCreation(ctx, []*model.SyncEvent{orphan})
	require.NoError(t, err)
	require.Empty(t, waiting)
}

// TestEventsAwaitingCreation_StaleNumberCurrentUID_FollowsTheUID: a pending
// event whose node_id is stale (the task moved) but whose uid is current
// waits for the creation of the task the uid names, not of the number it
// still carries. Mutation guard: resolving the task by the event's own
// node_id instead of by uid fails this.
func TestEventsAwaitingCreation_StaleNumberCurrentUID_FollowsTheUID(t *testing.T) {
	s, uid := subtreeFixture(t)
	ctx := context.Background()
	// PRJX-1's creation is pending; its edit names a number nobody holds.
	_, err := s.WriteDB().ExecContext(ctx,
		`UPDATE sync_events SET node_id = 'PRJX-99' WHERE op_type = 'update_field'`)
	require.NoError(t, err)
	events, err := s.ReadPendingEventsAfter(ctx, 0, "", 100)
	require.NoError(t, err)

	waiting, err := s.EventsAwaitingCreation(ctx, events)
	require.NoError(t, err)

	var found bool
	for _, e := range events {
		if e.OpType == model.OpUpdateField {
			found = true
			require.Equal(t, uid, e.UID)
			require.Contains(t, waiting, e.EventID, "waits for the creation its uid names")
		}
	}
	require.True(t, found)
}

// TestEventsAwaitingCreation_AdoptedUID_FindsThePendingCreation: a task whose
// uid a merge import replaced with the file's (its create event carries it
// in the uid column, not as the event id) still has its creation found.
func TestEventsAwaitingCreation_AdoptedUID_FindsThePendingCreation(t *testing.T) {
	s, _ := subtreeFixture(t)
	ctx := context.Background()
	const adopted = "00000000-0000-8000-8000-0000000000aa"
	_, err := s.WriteDB().ExecContext(ctx, `UPDATE sync_events SET uid = ? WHERE op_type = 'create_node'
		AND event_id = (SELECT uid FROM nodes WHERE id = 'PRJX-1')`, adopted)
	require.NoError(t, err)
	_, err = s.WriteDB().ExecContext(ctx, `UPDATE sync_events SET uid = ? WHERE uid = (SELECT uid FROM nodes WHERE id = 'PRJX-1') AND op_type <> 'create_node'`, adopted)
	require.NoError(t, err)
	_, err = s.WriteDB().ExecContext(ctx, `UPDATE nodes SET uid = ? WHERE id = 'PRJX-1'`, adopted)
	require.NoError(t, err)
	events, err := s.ReadPendingEventsAfter(ctx, 0, "", 100)
	require.NoError(t, err)

	waiting, err := s.EventsAwaitingCreation(ctx, events)
	require.NoError(t, err)

	for _, e := range events {
		if e.OpType == model.OpUpdateField {
			require.Contains(t, waiting, e.EventID, "the adopted-uid creation is still pending")
		}
	}
}
