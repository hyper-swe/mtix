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

// TestApplyLocalRenumbers_StoreChangedAfterPlan_WritesNothing verifies the
// import's transaction re-checks each planned local renumber (MTIX-95.31.4):
// when the node at the planned id is missing, holds another uid, or would
// no longer land on the planned id, the import fails with ErrConflict and
// writes nothing.
func TestApplyLocalRenumbers_StoreChangedAfterPlan_WritesNothing(t *testing.T) {
	const localUID = "01a0d56f-0000-7000-8000-00000000c001"
	tests := []struct {
		name string
		move localRenumber
	}{
		{"another task holds the id", localRenumber{uid: "01a0d56f-0000-7000-8000-00000000c002",
			oldID: "REC-1", newID: "REC-2", seq: 2}},
		{"the id is free", localRenumber{uid: localUID, oldID: "REC-9", newID: "REC-10", seq: 10}},
		{"the planned id is under another parent", localRenumber{uid: localUID,
			oldID: "REC-1", newID: "REC-3.2", seq: 2}},
		{"two nodes hold the uid", localRenumber{uid: localUID, oldID: "REC-1", newID: "REC-5", seq: 5}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			s := newInternalTestStore(t)
			now := time.Date(2026, 9, 24, 8, 0, 0, 0, time.UTC)
			require.NoError(t, s.CreateNode(ctx, &model.Node{
				ID: "REC-1", Project: "REC", Depth: 0, Seq: 1, Title: "Local task",
				Status: model.StatusOpen, Priority: model.PriorityMedium, Weight: 1.0,
				NodeType: model.NodeTypeEpic, ContentHash: "h1", UID: localUID, CreatedAt: now, UpdatedAt: now,
			}))
			if tt.name == "two nodes hold the uid" {
				require.NoError(t, s.CreateNode(ctx, &model.Node{
					ID: "REC-2", Project: "REC", Depth: 0, Seq: 2, Title: "Same uid",
					Status: model.StatusOpen, Priority: model.PriorityMedium, Weight: 1.0,
					NodeType: model.NodeTypeEpic, ContentHash: "h2", CreatedAt: now, UpdatedAt: now,
				}))
				_, err := s.writeDB.ExecContext(ctx, `UPDATE nodes SET uid = ? WHERE id = 'REC-2'`, localUID)
				require.NoError(t, err)
			}
			data, err := s.Export(ctx, "", "")
			require.NoError(t, err)
			before, err := json.Marshal(data.Nodes)
			require.NoError(t, err)

			_, err = s.Import(ctx, data, ImportModeMerge, false, renumberLocalFirst([]localRenumber{tt.move}))
			require.ErrorIs(t, err, model.ErrConflict)
			after, err := s.Export(ctx, "", "")
			require.NoError(t, err)
			afterNodes, err := json.Marshal(after.Nodes)
			require.NoError(t, err)
			assert.JSONEq(t, string(before), string(afterNodes), "nothing was written")
		})
	}
}
