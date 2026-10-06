// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Allocated creation preserves both the persisted counter and caller result on refusal (MTIX-106).
package sqlite_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store"
)

func TestCreateNodeAllocated_Refusal_WritesNoCounter(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*model.Node)
		opts   store.CreateNodeOptions
	}{
		{"invalid priority", func(n *model.Node) { n.Priority = 9 }, store.CreateNodeOptions{}},
		{"root provisional", func(*model.Node) {}, store.CreateNodeOptions{Provisional: true}},
		{"claimed initial state", func(n *model.Node) { n.Assignee = "already-assigned" }, store.CreateNodeOptions{Assignee: "worker"}},
		{"nonopen auto-claim", func(n *model.Node) { n.Status = model.StatusDone }, store.CreateNodeOptions{ClaimParentAssignee: true}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st := newTestStore(t)
			node := seqNode("TEST-0", 0)
			node.Project = "TEST"
			tc.mutate(node)
			err := st.CreateNodeAllocated(context.Background(), node, tc.opts)
			require.Error(t, err)
			assert.Equal(t, "TEST-0", node.ID)
			assert.Zero(t, node.Seq)
			var count int
			for _, q := range []string{`SELECT COUNT(*) FROM nodes`, `SELECT COUNT(*) FROM sequences`, `SELECT COUNT(*) FROM sync_events`} {
				require.NoError(t, st.QueryRow(context.Background(), q).Scan(&count))
				assert.Zero(t, count)
			}
			next, err := st.NextSequence(context.Background(), "TEST:")
			require.NoError(t, err)
			assert.Equal(t, 1, next)
		})
	}
}
