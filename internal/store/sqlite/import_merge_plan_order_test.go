// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"database/sql"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
)

// TestPlanMergeEvents_ChildNotedBeforeParent_PlansTheParentCreateFirst: the
// plan orders new tasks by depth whatever order the merge noted them in, so a
// peer applies a parent before its child (MTIX-112.17.16.16).
func TestPlanMergeEvents_ChildNotedBeforeParent_PlansTheParentCreateFirst(t *testing.T) {
	s, _ := applyTestStore(t)
	require.NoError(t, applyOnce(t, s, makeApplyEvent(t, model.OpCreateNode, "MTIX-1", "alice", 1,
		&model.CreateNodePayload{Title: "parent"})))
	require.NoError(t, applyOnce(t, s, makeApplyEvent(t, model.OpCreateNode, "MTIX-1.1", "alice", 2,
		&model.CreateNodePayload{Title: "child", ParentID: "MTIX-1"})))
	j := &mergeJournal{created: []string{"MTIX-1.1", "MTIX-1"}}

	var order []string
	require.NoError(t, s.WithTx(context.Background(), func(tx *sql.Tx) error {
		planned, err := s.planMergeEvents(context.Background(), tx, j, 1)
		for _, p := range planned {
			if p.OpType == model.OpCreateNode {
				order = append(order, p.NodeID)
			}
		}
		return err
	}))

	require.Equal(t, []string{"MTIX-1", "MTIX-1.1"}, order)
}
