// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Tests legacy comment, status and dependency fidelity through journal payloads and replay.
package sqlite_test

import (
	"database/sql"
	"encoding/json"
	"testing"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
	"github.com/stretchr/testify/require"
)

func backfillEvents(t *testing.T, s *sqlite.Store) []*model.SyncEvent {
	t.Helper()
	events, err := s.ReadPendingEvents(t.Context(), 1000)
	require.NoError(t, err)
	return events
}

func replayBackfill(t *testing.T, replica *sqlite.Store, events []*model.SyncEvent) {
	t.Helper()
	for _, event := range events {
		require.NoError(t, replica.WithTx(t.Context(), func(tx *sql.Tx) error { return replica.IdempotentApply(t.Context(), tx, event) }))
	}
}

func TestBackfillHistory_LegacyAnnotations_PreserveBodyCreatorAndEventTime(t *testing.T) {
	tests := []struct {
		name, raw string
		count     int
		timestamp int64
	}{
		{"explicit time", `[{"id":"source-id","author":"source-author","text":"Legacy body","kind":"note","resolved":true,"created_at":"2026-01-02T05:04:05Z"}]`, 1, backfillCommentTime.UnixMilli()},
		{"missing time", `[{"text":"Legacy body"}]`, 1, backfillCreated.UnixMilli()},
		{"invalid time", `[{"text":"Legacy body","created_at":"not-a-time"}]`, 1, backfillCreated.UnixMilli()},
		{"empty text", `[{"text":""}]`, 0, 0}, {"non-string text", `[{"text":7}]`, 0, 0},
		{"empty", ``, 0, 0}, {"list", `[]`, 0, 0}, {"null", `null`, 0, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			source, replica := backfillBehaviorStore(t), backfillBehaviorStore(t)
			require.NoError(t, source.CreateNode(t.Context(), backfillBehaviorNode(1)))
			_, err := source.WriteDB().ExecContext(t.Context(), "UPDATE nodes SET annotations=? WHERE id=?", tt.raw, "TEST-1")
			require.NoError(t, err)
			clearBackfillJournal(t, source)
			before := backfillSourceState(t, source)
			result, err := source.Backfill(t.Context(), false)
			require.NoError(t, err)
			require.Equal(t, tt.count, result.AnnotateEvents)
			require.Equal(t, 1+tt.count, result.TotalEvents)
			events := backfillEvents(t, source)
			comments := 0
			originalUID := backfillNodeUID(t, source, "TEST-1")
			for _, event := range events {
				if event.OpType == model.OpComment {
					comments++
					assertBackfillComment(t, event, originalUID, tt.timestamp)
				}
			}
			require.Equal(t, tt.count, comments)
			require.Equal(t, before, backfillSourceState(t, source))
			replayBackfill(t, replica, events)
			node, err := replica.GetNode(t.Context(), "TEST-1")
			require.NoError(t, err)
			require.Len(t, node.Annotations, tt.count)
			if tt.count == 1 {
				require.Equal(t, "Legacy body", node.Annotations[0].Text)
				require.Equal(t, "legacy-owner", node.Annotations[0].Author)
				require.Equal(t, tt.timestamp, node.Annotations[0].CreatedAt.UnixMilli())
				require.NotEmpty(t, node.Annotations[0].ID)
			}
		})
	}
}

func TestBackfillHistory_StatusAndDependencies_PersistThroughReplica(t *testing.T) {
	source, replica := backfillBehaviorStore(t), backfillBehaviorStore(t)
	seedBackfillRich(t, source)
	_, err := source.WriteDB().ExecContext(t.Context(), "UPDATE dependencies SET created_at=?", backfillCommentTime.Format("2006-01-02T15:04:05Z07:00"))
	require.NoError(t, err)
	before := backfillSourceState(t, source)
	result, err := source.Backfill(t.Context(), false)
	require.NoError(t, err)
	require.Equal(t, 1, result.TransitionEvents)
	require.Equal(t, 2, result.LinkDepEvents)
	events := backfillEvents(t, source)
	transitions := 0
	links := 0
	for _, event := range events {
		if event.OpType == model.OpTransitionStatus {
			transitions++
			var payload model.TransitionStatusPayload
			require.NoError(t, json.Unmarshal(event.Payload, &payload))
			require.Equal(t, model.TransitionStatusPayload{From: model.StatusOpen, To: model.StatusDone}, payload)
			require.Equal(t, "TEST-1", event.NodeID)
			require.Equal(t, backfillUpdated.UnixMilli(), event.WallClockTS)
			require.Equal(t, "legacy-owner", event.AuthorID)
			require.Equal(t, backfillNodeUID(t, source, event.NodeID), event.UID)
		}
		if event.OpType == model.OpLinkDep {
			links++
			assertBackfillLink(t, source, event)
		}
	}
	require.Equal(t, 1, transitions)
	require.Equal(t, 2, links)
	require.Equal(t, before, backfillSourceState(t, source))
	replayBackfill(t, replica, events)
	node, err := replica.GetNode(t.Context(), "TEST-1")
	require.NoError(t, err)
	require.Equal(t, model.StatusDone, node.Status)
	require.Len(t, node.Annotations, 2)
	require.Equal(t, backfillNodeUID(t, source, "TEST-1"), node.UID)
	texts := []string{}
	for _, annotation := range node.Annotations {
		texts = append(texts, annotation.Text)
		require.Equal(t, "legacy-owner", annotation.Author)
		require.Equal(t, backfillCommentTime.UnixMilli(), annotation.CreatedAt.UnixMilli())
	}
	require.ElementsMatch(t, []string{"First legacy comment", "Second legacy comment"}, texts)
	require.Equal(t, [][]any{
		{"TEST-1", "TEST-2", "related", "named-owner"},
		{"TEST-2", "TEST-1", "discovered_from", "fixture-default"},
	}, backfillRawRows(t, replica.WriteDB(), "SELECT from_id,to_id,dep_type,created_by FROM dependencies ORDER BY from_id,to_id,dep_type"))
}

func assertBackfillLink(t *testing.T, source *sqlite.Store, event *model.SyncEvent) {
	t.Helper()
	var payload model.LinkDepPayload
	require.NoError(t, json.Unmarshal(event.Payload, &payload))
	if event.NodeID == "TEST-1" {
		require.Equal(t, model.LinkDepPayload{DependsOnNodeID: "TEST-2", DepType: "related"}, payload)
		require.Equal(t, "named-owner", event.AuthorID)
	} else {
		require.Equal(t, "TEST-2", event.NodeID)
		require.Equal(t, model.LinkDepPayload{DependsOnNodeID: "TEST-1", DepType: "discovered_from"}, payload)
		require.Equal(t, "fixture-default", event.AuthorID)
	}
	require.Equal(t, backfillCommentTime.UnixMilli(), event.WallClockTS)
	require.Equal(t, backfillNodeUID(t, source, event.NodeID), event.UID)
}

func backfillNodeUID(t *testing.T, s *sqlite.Store, id string) string {
	t.Helper()
	var uid string
	require.NoError(t, s.WriteDB().QueryRowContext(t.Context(), "SELECT uid FROM nodes WHERE id=?", id).Scan(&uid))
	require.NotEmpty(t, uid)
	return uid
}

func assertBackfillComment(t *testing.T, event *model.SyncEvent, uid string, timestamp int64) {
	t.Helper()
	var payload model.CommentPayload
	require.NoError(t, json.Unmarshal(event.Payload, &payload))
	require.Equal(t, model.CommentPayload{AuthorID: "legacy-owner", Body: "Legacy body"}, payload)
	require.Equal(t, "TEST-1", event.NodeID)
	require.Equal(t, "legacy-owner", event.AuthorID)
	require.Equal(t, timestamp, event.WallClockTS)
	require.Equal(t, uid, event.UID)
}
