// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
)

// TestLWWPriorQuery_NoUID_Plan_AvoidsOpTypeIndex: both UID-less branches of
// lwwPriorQuery (a pulled update_field and any other LWW op, such as
// set_prompt) search the node's events by idx_sync_events_node, not every
// event of the op by idx_sync_events_op_type followed by a temp B-tree sort.
// The plan comes from annotationWinnerPlan (CLAUDE.md SQL Rule 1b).
func TestLWWPriorQuery_NoUID_Plan_AvoidsOpTypeIndex(t *testing.T) {
	tests := []struct {
		name string
		op   model.OpType
	}{
		{"update_field", model.OpUpdateField},
		{"set_prompt", model.OpSetPrompt},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := &model.SyncEvent{NodeID: "MTIX-1", OpType: tt.op, EventID: "e-probe"}
			query, args := lwwPriorQuery(e, "title")
			require.Equal(t, "MTIX-1", args[0], "the UID-less variant is scoped by the event's node number")
			plan := annotationWinnerPlan(t, query, args...)
			require.NotContains(t, plan, "idx_sync_events_op_type", plan)
			require.True(t, strings.Contains(plan, "idx_sync_events_node"), "plan must use idx_sync_events_node: %s", plan)
		})
	}
}
