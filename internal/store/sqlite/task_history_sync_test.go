// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"database/sql"
	"encoding/json"
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
)

// syncHistoryFields returns the sorted fields of the entries written for an event.
func syncHistoryFields(t *testing.T, raw *sql.DB, eventID string) (fields []string, origins, actors map[string]bool) {
	t.Helper()
	rows, err := raw.Query(`SELECT field, origin, actor, old_value, new_value FROM task_history WHERE event_id = ?`, eventID)
	require.NoError(t, err)
	defer func() { require.NoError(t, rows.Close()) }()
	origins, actors = map[string]bool{}, map[string]bool{}
	for rows.Next() {
		var field, origin, actor string
		var oldV, newV sql.NullString
		require.NoError(t, rows.Scan(&field, &origin, &actor, &oldV, &newV))
		fields = append(fields, field)
		origins[origin], actors[actor] = true, true
		if model.HistoryFieldClassOf(field) == model.HistoryFieldValueFree {
			assert.False(t, oldV.Valid || newV.Valid, "%s carries no text", field)
		}
	}
	require.NoError(t, rows.Err())
	sort.Strings(fields)
	return fields, origins, actors
}

func historyRowCount(t *testing.T, raw *sql.DB) int {
	t.Helper()
	var n int
	require.NoError(t, raw.QueryRow(`SELECT COUNT(*) FROM task_history`).Scan(&n))
	return n
}

// TestTaskHistorySync_ReplayedEvents_RecordHubEntriesOncePerEvent applies one
// event of every changing op: each writes exactly the entries of the fields it
// changed, with origin hub, the event's author and its event id, and replaying
// the same event adds nothing (FR-18.9).
func TestTaskHistorySync_ReplayedEvents_RecordHubEntriesOncePerEvent(t *testing.T) {
	s, raw := applyTestStore(t)
	id := "MTIX-1"
	steps := []struct {
		name   string
		event  *model.SyncEvent
		fields []string
	}{
		{"update priority", makeApplyEvent(t, model.OpUpdateField, id, "alice", 2, &model.UpdateFieldPayload{FieldName: "priority", NewValue: json.RawMessage(`5`)}), []string{"priority"}},
		{"update title", makeApplyEvent(t, model.OpUpdateField, id, "alice", 3, &model.UpdateFieldPayload{FieldName: "title", NewValue: json.RawMessage(`"new"`)}), []string{"title"}},
		{"transition", makeApplyEvent(t, model.OpTransitionStatus, id, "alice", 4, &model.TransitionStatusPayload{From: model.StatusOpen, To: model.StatusInProgress}), []string{"status"}},
		{"set prompt", makeApplyEvent(t, model.OpSetPrompt, id, "alice", 5, &model.SetPromptPayload{PromptText: "do the thing"}), []string{"prompt"}},
		{"set acceptance", makeApplyEvent(t, model.OpSetAcceptance, id, "alice", 6, &model.SetAcceptancePayload{AcceptanceText: "it works"}), []string{"acceptance"}},
		{"comment", makeApplyEvent(t, model.OpComment, id, "alice", 7, &model.CommentPayload{AuthorID: "alice", Body: "looks good"}), []string{"annotations"}},
		{"delete", makeApplyEvent(t, model.OpDelete, id, "alice", 8, &model.DeletePayload{}), []string{"deleted_at"}},
	}
	create := makeApplyEvent(t, model.OpCreateNode, id, "alice", 1, &model.CreateNodePayload{Title: "old", Priority: model.Priority(3)})
	require.NoError(t, applyOnce(t, s, create))
	created, _, _ := syncHistoryFields(t, raw, create.EventID)
	assert.Contains(t, created, "title", "a replayed create records the initial fields")

	for _, st := range steps {
		t.Run(st.name, func(t *testing.T) {
			require.NoError(t, applyOnce(t, s, st.event))
			fields, origins, actors := syncHistoryFields(t, raw, st.event.EventID)
			assert.Equal(t, st.fields, fields)
			assert.Equal(t, map[string]bool{"hub": true}, origins)
			assert.Equal(t, map[string]bool{"alice": true}, actors)
			before := historyRowCount(t, raw)
			require.NoError(t, applyOnce(t, s, st.event))
			assert.Equal(t, before, historyRowCount(t, raw), "a replay of the same event_id adds no entry")
		})
	}
}

// TestTaskHistorySync_DependentUnblockedByAReplay_RecordsOriginSystem: a task
// the replay reaches only through the automatic unblock is a system change,
// not hub, and keeps the event id.
func TestTaskHistorySync_DependentUnblockedByAReplay_RecordsOriginSystem(t *testing.T) {
	s, raw := applyTestStore(t)
	require.NoError(t, applyOnce(t, s, makeApplyEvent(t, model.OpCreateNode, "MTIX-1", "alice", 1, &model.CreateNodePayload{Title: "blocker"})))
	require.NoError(t, applyOnce(t, s, makeApplyEvent(t, model.OpCreateNode, "MTIX-2", "alice", 2, &model.CreateNodePayload{Title: "dependent"})))
	require.NoError(t, applyOnce(t, s, makeApplyEvent(t, model.OpLinkDep, "MTIX-1", "alice", 3, &model.LinkDepPayload{DependsOnNodeID: "MTIX-2", DepType: string(model.DepTypeBlocks)})))
	require.NoError(t, applyOnce(t, s, makeApplyEvent(t, model.OpTransitionStatus, "MTIX-1", "alice", 4, &model.TransitionStatusPayload{From: model.StatusOpen, To: model.StatusInProgress})))
	_, err := raw.Exec(`DELETE FROM task_history`)
	require.NoError(t, err)

	done := makeApplyEvent(t, model.OpTransitionStatus, "MTIX-1", "alice", 5, &model.TransitionStatusPayload{From: model.StatusInProgress, To: model.StatusDone})
	require.NoError(t, applyOnce(t, s, done))

	var origin, field string
	require.NoError(t, raw.QueryRow(`SELECT h.origin, h.field FROM task_history h JOIN nodes n ON n.uid = h.task_uid
		WHERE n.id = 'MTIX-2' AND h.event_id = ?`, done.EventID).Scan(&origin, &field))
	assert.Equal(t, "status", field)
	assert.Equal(t, "system", origin, "the dependent was unblocked automatically")
}
