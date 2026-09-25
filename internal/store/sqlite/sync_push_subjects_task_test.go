// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/store"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// TestPushSubjects_CreationTask_FoundByUIDSelfAnchorOrNumber: the task a
// create_node event created (MTIX-95.12 run 2, review r1 S1 and S2), read
// by PushSubjects and by HeldPushEvents alike, is the node that has the
// event's uid; else, for an event without a uid, the node whose uid is the
// event's own id (the pre-v3 backfill's), under its current number; else
// the node at the number the event names, soft-deleted or not (review r2
// S2). TaskNodeID is that node's current number. An edit gets no task
// number, and a creation whose task no node is left for gets none.
func TestPushSubjects_CreationTask_FoundByUIDSelfAnchorOrNumber(t *testing.T) {
	tests := []struct {
		name        string
		change      func(t *testing.T, s *sqlite.Store, create string)
		edit        bool // read the task's edit instead of its creation
		wantUID     func(create string) string
		wantCurrent string
		wantTask    string
	}{
		{"found by uid, renumbered", func(t *testing.T, s *sqlite.Store, _ string) {
			require.NoError(t, s.RenumberSubtree(context.Background(), "PROJ-1", 5))
		}, false, creationID, "PROJ-5", "PROJ-5"},
		{"uid adopted by a merge: by the number", func(t *testing.T, s *sqlite.Store, _ string) {
			setNodeUID(t, s, "PROJ-1", "file-uid")
		}, false, creationID, "", "PROJ-1"},
		{"uid adopted by a merge, task soft-deleted: by the number", func(t *testing.T, s *sqlite.Store, _ string) {
			setNodeUID(t, s, "PROJ-1", "file-uid")
			require.NoError(t, s.DeleteNode(context.Background(), "PROJ-1", true, "tester"))
		}, false, creationID, "", "PROJ-1"},
		{"queued without a uid: by its own id, renumbered", func(t *testing.T, s *sqlite.Store, create string) {
			clearEventUID(t, s, create)
			require.NoError(t, s.RenumberSubtree(context.Background(), "PROJ-1", 5))
		}, false, literalUID(""), "", "PROJ-5"},
		{"queued without a uid, uid adopted: by the number", func(t *testing.T, s *sqlite.Store, create string) {
			clearEventUID(t, s, create)
			setNodeUID(t, s, "PROJ-1", "file-uid")
		}, false, literalUID(""), "", "PROJ-1"},
		{"task purged: no task", func(t *testing.T, s *sqlite.Store, _ string) {
			_, err := s.WriteDB().ExecContext(context.Background(), `DELETE FROM nodes WHERE id = 'PROJ-1'`)
			require.NoError(t, err)
		}, false, creationID, "", ""},
		{"an edit of a task whose uid was adopted: no task number", func(t *testing.T, s *sqlite.Store, _ string) {
			setNodeUID(t, s, "PROJ-1", "file-uid")
		}, true, creationID, "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			s := newTestStore(t)
			now := time.Date(2026, 9, 25, 9, 0, 0, 0, time.UTC)
			require.NoError(t, s.CreateNode(ctx, makeRootNode("PROJ-1", "PROJ", "held", now)))
			title := "edited"
			require.NoError(t, s.UpdateNode(ctx, "PROJ-1", &store.NodeUpdate{Title: &title}))
			create := eventOf(t, s, "PROJ-1", "create_node")
			id := create
			if tt.edit {
				id = eventOf(t, s, "PROJ-1", "update_field")
			}
			tt.change(t, s, create)
			want := sqlite.PushSubject{UID: tt.wantUID(create), CurrentNodeID: tt.wantCurrent, TaskNodeID: tt.wantTask}

			got, err := s.PushSubjects(ctx, []string{id})
			require.NoError(t, err)
			require.Equal(t, want, got[id], "PushSubjects")
			require.NoError(t, s.HoldPushEvents(ctx, []sqlite.QuarantinedEvent{pushHold(id)}, "v"))
			held, err := s.HeldPushEvents(ctx, -1)
			require.NoError(t, err)
			require.Len(t, held, 1)
			require.Equal(t, want, held[0].PushSubject, "HeldPushEvents")
		})
	}
}

// creationID returns the creation's own event id.
func creationID(create string) string { return create }

// literalUID returns a func that always returns v.
func literalUID(v string) func(string) string { return func(string) string { return v } }

// setNodeUID gives node id the uid uid, as a merge import that adopts the
// file's uid does (MTIX-95.31.6).
func setNodeUID(t *testing.T, s *sqlite.Store, id, uid string) {
	t.Helper()
	_, err := s.WriteDB().ExecContext(context.Background(), `UPDATE nodes SET uid = ? WHERE id = ?`, uid, id)
	require.NoError(t, err)
}

// clearEventUID makes eventID one queued before events carried a uid.
func clearEventUID(t *testing.T, s *sqlite.Store, eventID string) {
	t.Helper()
	_, err := s.WriteDB().ExecContext(context.Background(),
		`UPDATE sync_events SET uid = NULL WHERE event_id = ?`, eventID)
	require.NoError(t, err)
}
