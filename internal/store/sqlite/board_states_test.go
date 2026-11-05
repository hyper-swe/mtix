// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

var boardStatesAt = time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)

func boardStatesStore(t *testing.T) *sqlite.Store {
	t.Helper()
	s := newTestStore(t)
	ctx := context.Background()
	require.NoError(t, s.CreateNode(ctx, makeRootNode("TEST-1", "TEST", "one", boardStatesAt)))
	require.NoError(t, s.CreateNode(ctx, makeRootNode("TEST-2", "TEST", "two", boardStatesAt)))
	return s
}

func stateOf(t *testing.T, s *sqlite.Store, id string) sqlite.BoardNodeState {
	t.Helper()
	states, err := s.BoardNodeStates(context.Background())
	require.NoError(t, err)
	for _, st := range states {
		if st.ID == id {
			return st
		}
	}
	t.Fatalf("no state for %s in %v", id, states)
	return sqlite.BoardNodeState{}
}

// TestBoardNodeStates_EveryFieldAnAgentCanChange_ChangesTheFingerprint: a
// field left out of the fingerprint is a change the enrolment could not see,
// so each one is edited behind the journal's back and must show.
func TestBoardNodeStates_EveryFieldAnAgentCanChange_ChangesTheFingerprint(t *testing.T) {
	fields := map[string]string{
		"title": "'x'", "description": "'x'", "prompt": "'x'", "acceptance": "'x'", "issue_type": "'bug'",
		"priority": "1", "labels": `'["x"]'`, "status": "'done'", "previous_status": "'open'", "assignee": "'someone'",
		"agent_state": "'working'", "defer_until": "'2030-01-01T00:00:00Z'", "estimate_min": "5", "actual_min": "6",
		"code_refs": `'[{"file":"a.go"}]'`, "commit_refs": `'["abc"]'`, "annotations": `'[{"id":"a","text":"t"}]'`,
		"metadata": `'{"k":"v"}'`, "parent_id": "'TEST-2'", "deleted_at": "'2026-07-01T00:00:00Z'",
	}
	ctx := context.Background()
	for col, val := range fields {
		t.Run(col, func(t *testing.T) {
			s := boardStatesStore(t)
			before := stateOf(t, s, "TEST-1")
			_, err := s.WriteDB().ExecContext(ctx, `UPDATE nodes SET `+col+` = `+val+` WHERE id = 'TEST-1'`) //nolint:gosec // G202: a test builds an UPDATE from a constant column list
			require.NoError(t, err)

			after := stateOf(t, s, "TEST-1")

			assert.NotEqual(t, before.Fingerprint, after.Fingerprint)
			assert.Equal(t, before.UID, after.UID)
		})
	}
}

// TestBoardNodeStates_ClockAndDerivedColumns_DoNotChangeTheFingerprint: a
// replica rebuilt from the same events may stamp these differently.
func TestBoardNodeStates_ClockAndDerivedColumns_DoNotChangeTheFingerprint(t *testing.T) {
	s := boardStatesStore(t)
	before := stateOf(t, s, "TEST-1")

	_, err := s.WriteDB().ExecContext(context.Background(),
		`UPDATE nodes SET created_at = '2031-01-01T00:00:00Z', updated_at = '2031-01-01T00:00:00Z', progress = 0.5, content_hash = 'other', creator = 'else' WHERE id = 'TEST-1'`)
	require.NoError(t, err)

	assert.Equal(t, before.Fingerprint, stateOf(t, s, "TEST-1").Fingerprint)
}

// TestBoardNodeStates_NullAndEmptyJSONColumns_AreTheSameState: a node written
// directly and the same node rebuilt from the journal differ only in NULL
// versus an empty array.
func TestBoardNodeStates_NullAndEmptyJSONColumns_AreTheSameState(t *testing.T) {
	s := boardStatesStore(t)
	before := stateOf(t, s, "TEST-1")

	_, err := s.WriteDB().ExecContext(context.Background(),
		`UPDATE nodes SET labels = '[]', annotations = '[]', code_refs = '[]', commit_refs = '[]', metadata = '{}' WHERE id = 'TEST-1'`)
	require.NoError(t, err)

	assert.Equal(t, before.Fingerprint, stateOf(t, s, "TEST-1").Fingerprint)
}

func TestBoardNodeStates_Dependency_ChangesTheSourceNode(t *testing.T) {
	s := boardStatesStore(t)
	one := stateOf(t, s, "TEST-1")

	require.NoError(t, s.AddDependency(context.Background(), &model.Dependency{FromID: "TEST-1", ToID: "TEST-2", DepType: model.DepTypeBlocks, CreatedAt: boardStatesAt}))

	assert.NotEqual(t, one.Fingerprint, stateOf(t, s, "TEST-1").Fingerprint)
}

// TestBoardNodeStates_SoftDeletedNodes_AreIncludedAndMarked: a deletion is a
// state to compare, not a row to skip.
func TestBoardNodeStates_SoftDeletedNodes_AreIncludedAndMarked(t *testing.T) {
	s := boardStatesStore(t)
	require.NoError(t, s.DeleteNode(context.Background(), "TEST-2", false, "tester"))

	states, err := s.BoardNodeStates(context.Background())

	require.NoError(t, err)
	require.Len(t, states, 2)
	assert.True(t, stateOf(t, s, "TEST-2").Deleted)
	assert.False(t, stateOf(t, s, "TEST-1").Deleted)
}

// TestBoardNodeStates_EventsAreFoundByUIDAndByDisplayIDWhenThereIsNoUID.
func TestBoardNodeStates_EventsAreFoundByUIDAndByDisplayIDWhenThereIsNoUID(t *testing.T) {
	s := boardStatesStore(t)
	st := stateOf(t, s, "TEST-1")
	require.NotEmpty(t, st.UID)
	require.NotEmpty(t, st.EventIDs, "the create event is journalled under the node's uid")
	assert.Equal(t, st.UID, st.Key())

	_, err := s.WriteDB().ExecContext(context.Background(), `UPDATE sync_events SET uid = '' WHERE node_id = 'TEST-1'`)
	require.NoError(t, err)
	_, err = s.WriteDB().ExecContext(context.Background(), `UPDATE nodes SET uid = '' WHERE id = 'TEST-1'`)
	require.NoError(t, err)

	noUID := stateOf(t, s, "TEST-1")
	assert.Equal(t, "id:TEST-1", noUID.Key())
	assert.Equal(t, st.EventIDs, noUID.EventIDs, "events without a uid are matched by display id")
}

func TestBoardNodeStates_JournalLessNode_HasNoEvents(t *testing.T) {
	s := boardStatesStore(t)
	_, err := s.WriteDB().ExecContext(context.Background(), `DELETE FROM sync_events WHERE node_id = 'TEST-2'`)
	require.NoError(t, err)

	assert.Empty(t, stateOf(t, s, "TEST-2").EventIDs)
	assert.NotEmpty(t, stateOf(t, s, "TEST-1").EventIDs)
}

func TestBoardNodeStates_ClosedStore_ReturnsAnError(t *testing.T) {
	s := boardStatesStore(t)
	require.NoError(t, s.Close())

	_, err := s.BoardNodeStates(context.Background())

	require.Error(t, err)
}
