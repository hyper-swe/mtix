// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store"
)

// TestCreateNodeAllocated_ParentClaimRule_ClaimsOnlyForParentAssignee pins the
// FR-11.2a rule in initialClaimAssignee for single and batch creates. The
// parent's creator ("planner") differs from its assignee ("owner"), so a
// comparison against the parent creator would be caught.
func TestCreateNodeAllocated_ParentClaimRule_ClaimsOnlyForParentAssignee(t *testing.T) {
	tests := []struct {
		name         string
		creator      string
		explicit     string
		wantStatus   model.Status
		wantAssignee string
	}{
		{"creator is parent assignee", "owner", "", model.StatusInProgress, "owner"},
		{"other creator", "other", "", model.StatusOpen, ""},
		{"parent creator is not the assignee", "planner", "", model.StatusOpen, ""},
		{"empty creator", "", "", model.StatusOpen, ""},
		{"explicit assignee wins for other creator", "other", "lane-2", model.StatusInProgress, "lane-2"},
	}
	for _, tt := range tests {
		for _, batch := range []bool{false, true} {
			name := tt.name + " single"
			if batch {
				name = tt.name + " batch"
			}
			t.Run(name, func(t *testing.T) {
				st := newTestStore(t)
				ctx := context.Background()
				parent := seqNode("TEST-0", 0)
				parent.Creator = "planner"
				require.NoError(t, st.CreateNodeAllocated(ctx, parent, store.CreateNodeOptions{}))
				require.NoError(t, st.ClaimNode(ctx, parent.ID, "owner"))

				child := seqNode("TEST-1.0", 0)
				child.ParentID = parent.ID
				child.Creator = tt.creator
				opts := store.CreateNodeOptions{ClaimParentAssignee: true, Assignee: tt.explicit}
				if batch {
					require.NoError(t, st.CreateNodesAllocated(ctx, []*model.Node{child}, opts))
				} else {
					require.NoError(t, st.CreateNodeAllocated(ctx, child, opts))
				}
				got, err := st.GetNode(ctx, child.ID)
				require.NoError(t, err)
				assert.Equal(t, tt.wantStatus, got.Status)
				assert.Equal(t, tt.wantAssignee, got.Assignee)
			})
		}
	}
}
