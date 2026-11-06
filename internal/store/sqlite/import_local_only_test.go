// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
	"github.com/hyper-swe/mtix/internal/sync/clock"
)

// Tests of the record an automatic import leaves for the changes it did not
// journal (MTIX-112.17.16.16, round 2): it keeps the changed fields per task,
// a task settles only when each recorded field has a later event, and the
// record prunes what settled.

func hubEvent(t *testing.T, nodeID string, op model.OpType, payload any, lamport int64) *model.SyncEvent {
	t.Helper()
	pl, err := model.EncodePayload(payload)
	require.NoError(t, err)
	return &model.SyncEvent{EventID: clock.MustNewEventID(), ProjectPrefix: "REC", NodeID: nodeID, OpType: op,
		Payload: pl, WallClockTS: 1000 + lamport, LamportClock: lamport, VectorClock: model.VectorClock{"bob": lamport},
		AuthorID: "bob", AuthorMachineHash: "0123456789abcdef", SyncStatus: model.SyncStatusApplied}
}

func fieldEvent(t *testing.T, nodeID, field string, value any, lamport int64) *model.SyncEvent {
	t.Helper()
	raw, err := json.Marshal(value)
	require.NoError(t, err)
	return hubEvent(t, nodeID, model.OpUpdateField, &model.UpdateFieldPayload{FieldName: field, NewValue: raw}, lamport)
}

func liveOnly(t *testing.T, s *sqlite.Store) ([]sqlite.LocalOnlyImport, int) {
	t.Helper()
	live, overflow, err := s.LocalOnlyImports(context.Background())
	require.NoError(t, err)
	return live, overflow
}

// TestLocalOnlyImports_SettleOnlyWhenEveryRecordedFieldHasALaterEvent: an
// event for another field, or a comment, settles nothing; the task drops off
// when its last recorded field has an event.
func TestLocalOnlyImports_SettleOnlyWhenEveryRecordedFieldHasALaterEvent(t *testing.T) {
	s := syncedStoreWithTask(t)
	require.NoError(t, s.RecordLocalOnlyImports(context.Background(),
		[]sqlite.LocalOnlyImport{{ID: "REC-1", Fields: []string{"title", "priority"}}}))

	applyHub(t, s, fieldEvent(t, "REC-1", "priority", 1, 5))
	live, _ := liveOnly(t, s)
	require.Equal(t, []sqlite.LocalOnlyImport{{ID: "REC-1", Fields: []string{"title"}}}, live,
		"the priority event settles priority only")

	applyHub(t, s, hubEvent(t, "REC-1", model.OpComment, &model.CommentPayload{AuthorID: "bob", Body: "hi"}, 6))
	applyHub(t, s, fieldEvent(t, "REC-1", "assignee", "carol", 7))
	live, _ = liveOnly(t, s)
	require.Len(t, live, 1, "a comment and an unrelated field settle nothing")

	applyHub(t, s, fieldEvent(t, "REC-1", "title", "Hub title", 8))
	live, _ = liveOnly(t, s)
	require.Empty(t, live, "every recorded field has a later event")
}

// TestLocalOnlyImports_RecordPrunesSettledTasksAndOverflowFalls: tasks beyond
// the record limit are counted, a task that settled is pruned when the next
// import records, and the count clears once every recorded task has settled.
func TestLocalOnlyImports_RecordPrunesSettledTasksAndOverflowFalls(t *testing.T) {
	s := newTestStore(t)
	for i := 1; i <= 3; i++ {
		createPlainTask(t, s, fmt.Sprintf("REC-%d", i), i, "", sameIDTime)
	}
	markJournalPushed(t, s)
	s.SetLocalOnlyRecordLimit(2)
	title := func(id string) sqlite.LocalOnlyImport {
		return sqlite.LocalOnlyImport{ID: id, Fields: []string{"title"}}
	}
	require.NoError(t, s.RecordLocalOnlyImports(context.Background(), []sqlite.LocalOnlyImport{title("REC-1"), title("REC-2"), title("REC-3")}))
	live, overflow := liveOnly(t, s)
	require.Len(t, live, 2)
	require.Equal(t, 1, overflow, "one task did not fit")

	applyHub(t, s, fieldEvent(t, "REC-1", "title", "A", 5))
	applyHub(t, s, fieldEvent(t, "REC-2", "title", "B", 6))

	live, overflow = liveOnly(t, s)
	require.Empty(t, live)
	require.Zero(t, overflow, "with every recorded task settled the count clears")
	require.NoError(t, s.RecordLocalOnlyImports(context.Background(), []sqlite.LocalOnlyImport{title("REC-3")}))
	live, overflow = liveOnly(t, s)
	require.Equal(t, []sqlite.LocalOnlyImport{title("REC-3")}, live, "the pruned record has room again")
	require.Zero(t, overflow)
}

// TestHubKnownNodes_CountsAnyLoggedTask: a task whose create is still pending
// (a relay publisher's events stay pending, an unpushed create carries the old
// payload) is named too, and a task with no event is not.
func TestHubKnownNodes_CountsAnyLoggedTask(t *testing.T) {
	s := newTestStore(t)
	createPlainTask(t, s, "REC-1", 1, "", sameIDTime) // create pending
	createPlainTask(t, s, "REC-2", 2, "", sameIDTime)
	_, err := s.WriteDB().Exec(`DELETE FROM sync_events WHERE node_id = 'REC-2'`)
	require.NoError(t, err)

	known, err := s.HubKnownNodes(context.Background(), []string{"REC-1", "REC-2"})

	require.NoError(t, err)
	require.Equal(t, []string{"REC-1"}, known)
}

// TestReplaceFieldChanges_NamesTheFieldsAndDependenciesThatDiffer: the changed
// fields per task and the tasks a dependency change touches, in both directions.
func TestReplaceFieldChanges_NamesTheFieldsAndDependenciesThatDiffer(t *testing.T) {
	local, other := newTestStore(t), newTestStore(t)
	for _, s := range []*sqlite.Store{local, other} {
		createPlainTask(t, s, "REC-1", 1, "", sameIDTime)
		createPlainTask(t, s, "REC-2", 2, "", sameIDTime)
	}
	incoming := exportOf(t, other)
	incoming.Nodes[0].Title, incoming.Nodes[0].Priority, incoming.Nodes[0].Weight = "New", 1, 4
	incoming.Dependencies = append(incoming.Dependencies, struct {
		FromID    string `json:"from_id"`
		ToID      string `json:"to_id"`
		DepType   string `json:"dep_type"`
		CreatedAt string `json:"created_at"`
	}{"REC-2", "REC-1", "related", sameIDTime.Format("2006-01-02T15:04:05Z")})
	localData := exportOf(t, local)
	diff, err := sqlite.DiffReplace(localData, incoming)
	require.NoError(t, err)
	back, err := sqlite.DiffReplace(incoming, localData)
	require.NoError(t, err)

	got := sqlite.ReplaceFieldChanges(localData, incoming, diff)
	removed := sqlite.ReplaceFieldChanges(incoming, localData, back)

	require.Equal(t, []sqlite.LocalOnlyImport{
		{ID: "REC-1", Fields: []string{"priority", "title", "weight"}},
		{ID: "REC-2", Fields: []string{"dependencies"}},
	}, got)
	require.Contains(t, removed, sqlite.LocalOnlyImport{ID: "REC-2", Fields: []string{"dependencies"}}, "a removed dependency counts too")
}
