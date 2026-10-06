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

func TestCreateNodesAllocated_Refusal_PreservesCallerAndDatabase(t *testing.T) {
	for _, tc := range []struct {
		name    string
		mutate  func(*model.Node)
		opts    store.CreateNodeOptions
		trigger string
	}{
		{name: "invalid later node", mutate: func(n *model.Node) { n.Priority = 9 }},
		{name: "invalid claim state", mutate: func(n *model.Node) { n.Assignee = "already-assigned" }, opts: store.CreateNodeOptions{Assignee: "worker"}},
		{name: "nonopen auto-claim", mutate: func(n *model.Node) { n.Status = model.StatusDone }, opts: store.CreateNodeOptions{ClaimParentAssignee: true}},
		{name: "missing parent", mutate: func(n *model.Node) { n.ParentID = "TEST-99" }},
		{name: "root provisional", opts: store.CreateNodeOptions{Provisional: true}},
		{name: "second claim event", opts: store.CreateNodeOptions{Assignee: "worker"}, trigger: `CREATE TRIGGER reject_batch BEFORE INSERT ON sync_events WHEN NEW.node_id = 'TEST-2' AND NEW.op_type = 'claim' BEGIN SELECT RAISE(ABORT, 'claim refused'); END`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := newTestStore(t)
			ctx := context.Background()
			nodes := []*model.Node{seqNode("TEST-0", 0), seqNode("TEST-0", 0)}
			if tc.mutate != nil {
				tc.mutate(nodes[1])
			}
			before := []model.Node{*nodes[0], *nodes[1]}
			if tc.trigger != "" {
				_, err := st.WriteDB().ExecContext(ctx, tc.trigger)
				require.NoError(t, err)
			}
			require.Error(t, st.CreateNodesAllocated(ctx, nodes, tc.opts))
			assert.Equal(t, before[0], *nodes[0])
			assert.Equal(t, before[1], *nodes[1])
			for _, q := range []string{`SELECT COUNT(*) FROM nodes`, `SELECT COUNT(*) FROM sequences`, `SELECT COUNT(*) FROM sync_events`, `SELECT COUNT(*) FROM agents`} {
				var count int
				require.NoError(t, st.QueryRow(ctx, q).Scan(&count))
				assert.Zero(t, count)
			}
		})
	}
}

func TestCreateNodesAllocated_InvalidBatch_ReturnsInvalidInput(t *testing.T) {
	st := newTestStore(t)
	for _, nodes := range [][]*model.Node{nil, {}, {seqNode("TEST-0", 0), nil}} {
		require.ErrorIs(t, st.CreateNodesAllocated(context.Background(), nodes, store.CreateNodeOptions{}), model.ErrInvalidInput)
	}
}

func TestCreateNodesAllocated_ExplicitClaim_CommitsResults(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	nodes := []*model.Node{seqNode("TEST-0", 0), seqNode("TEST-0", 0)}
	require.NoError(t, st.CreateNodesAllocated(ctx, nodes, store.CreateNodeOptions{Assignee: "worker"}))
	for i, node := range nodes {
		assert.Equal(t, model.BuildID("TEST", "", i+1), node.ID)
		assert.Equal(t, i+1, node.Seq)
		assert.NotEmpty(t, node.UID)
		assert.Equal(t, model.StatusInProgress, node.Status)
		assert.Equal(t, "worker", node.Assignee)
		stored, err := st.GetNode(ctx, node.ID)
		require.NoError(t, err)
		assert.Equal(t, node.UID, stored.UID)
		assert.Equal(t, node.Assignee, stored.Assignee)
	}
	var current string
	require.NoError(t, st.QueryRow(ctx, `SELECT current_node_id FROM agents WHERE agent_id = ?`, "worker").Scan(&current))
	assert.Equal(t, "TEST-2", current)
}
