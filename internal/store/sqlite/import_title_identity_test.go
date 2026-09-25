// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
)

// createUIDlessTask creates root task id in s, without a uid.
func createUIDlessTask(t *testing.T, s *Store, id string, seq int, title string) {
	t.Helper()
	ctx := context.Background()
	created := time.Date(2026, 9, 24, 8, 0, 0, 0, time.UTC)
	require.NoError(t, s.CreateNode(ctx, &model.Node{
		ID: id, Project: "REC", Depth: 0, Seq: seq, Title: title,
		Status: model.StatusOpen, Priority: model.PriorityMedium, Weight: 1.0,
		NodeType: model.NodeTypeEpic, ContentHash: "h-" + id, CreatedAt: created, UpdatedAt: created,
	}))
	_, err := s.writeDB.ExecContext(ctx, `UPDATE nodes SET uid = NULL WHERE id = ?`, id)
	require.NoError(t, err)
}

// nodesJSON returns the nodes of s's export as JSON.
func nodesJSON(t *testing.T, s *Store) string {
	t.Helper()
	data, err := s.Export(context.Background(), "", "")
	require.NoError(t, err)
	raw, err := json.Marshal(data.Nodes)
	require.NoError(t, err)
	return string(raw)
}

// TestStampNewUIDs_StoreChangedAfterPlan_ConflictWritesNothing verifies the
// uid a merge plan minted for a local task without one is written only
// while the task still sits at its planned id without a uid
// (MTIX-95.31.9): a task given a uid, or moved, after the plan makes the
// import fail with ErrConflict, naming the check, and write nothing.
func TestStampNewUIDs_StoreChangedAfterPlan_ConflictWritesNothing(t *testing.T) {
	tests := []struct {
		name   string
		change string // what happened after the plan
	}{
		{"the task was given a uid", `UPDATE nodes SET uid = '01a0d56f-0000-7000-8000-00000000c009' WHERE id = 'REC-1'`},
		{"the task moved", `UPDATE nodes SET id = 'REC-5', seq = 5 WHERE id = 'REC-1'`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			s := newInternalTestStore(t)
			createUIDlessTask(t, s, "REC-1", 1, "Local task")
			minted, err := model.NewBackfillUID()
			require.NoError(t, err)
			writes := localWrites{stamps: []uidStamp{{id: "REC-1", uid: minted}},
				moves: []localRenumber{{uid: minted, oldID: "REC-1", newID: "REC-2", seq: 2}}}
			_, err = s.writeDB.ExecContext(ctx, tt.change)
			require.NoError(t, err)
			data, err := s.Export(ctx, "", "")
			require.NoError(t, err)
			before := nodesJSON(t, s)

			_, err = s.Import(ctx, data, ImportModeMerge, false, renumberLocalFirst(writes))
			require.ErrorIs(t, err, model.ErrConflict)
			assert.ErrorContains(t, err, "give local task REC-1 a uid")
			assert.Equal(t, before, nodesJSON(t, s), "nothing was written")
		})
	}
}

// TestImportReconcile_MergePlanCannotReadTitles_ReturnsItsErrorWritesNothing
// verifies a merge whose plan must compare titles (a file task without a
// uid) and cannot read the local titles stops with that error, wrapped,
// and writes nothing (MTIX-95.31.9). The failure is made by renaming the
// title column.
func TestImportReconcile_MergePlanCannotReadTitles_ReturnsItsErrorWritesNothing(t *testing.T) {
	ctx := context.Background()
	s := newInternalTestStore(t)
	created := time.Date(2026, 9, 24, 8, 0, 0, 0, time.UTC)
	require.NoError(t, s.CreateNode(ctx, &model.Node{
		ID: "REC-1", Project: "REC", Depth: 0, Seq: 1, Title: "Local title",
		Status: model.StatusOpen, Priority: model.PriorityMedium, Weight: 1.0,
		NodeType: model.NodeTypeEpic, ContentHash: "h-local", CreatedAt: created, UpdatedAt: created,
	}))
	data, err := s.Export(ctx, "", "")
	require.NoError(t, err)
	data.Nodes[0].UID, data.Nodes[0].Title, data.Nodes[0].ContentHash = "", "File title", "h-file"
	require.NoError(t, RecomputeExportChecksum(data))
	// From here on, every read of nodes.title fails.
	_, err = s.writeDB.ExecContext(ctx, `ALTER TABLE nodes RENAME COLUMN title TO title_unreadable`)
	require.NoError(t, err)
	before := nodeRowsSnapshot(t, s)

	report, result, err := s.ImportReconcile(ctx, data, ImportReconcileOptions{Mode: ImportModeMerge, Confirm: true})
	require.Error(t, err)
	assert.ErrorContains(t, err, "read the local titles for the merge plan")
	assert.ErrorContains(t, err, "title", "the read error is wrapped, not replaced")
	assert.Nil(t, result)
	require.NotNil(t, report)
	assert.False(t, report.Applied)
	assert.Equal(t, before, nodeRowsSnapshot(t, s), "nothing was written")
}
