// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service_test

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// MTIX-128: a late refusal restores the entire batch, including earlier claims.
func TestDecompose_LateRefusal_RollsBackWholeBatch(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(*service.DecomposeInput)
		trigger string
	}{
		{name: "third priority", mutate: func(c *service.DecomposeInput) { c.Priority = 9 }},
		{name: "third oversized prompt", mutate: func(c *service.DecomposeInput) { c.Prompt = strings.Repeat("x", model.MaxPromptSize+1) }},
		{name: "third insert", trigger: `CREATE TRIGGER reject_batch BEFORE INSERT ON nodes WHEN NEW.id = 'TEST-1.4' BEGIN SELECT RAISE(ABORT, 'third insert refused'); END`},
		{name: "third create event", trigger: `CREATE TRIGGER reject_batch BEFORE INSERT ON sync_events WHEN NEW.node_id = 'TEST-1.4' AND NEW.op_type = 'create_node' BEGIN SELECT RAISE(ABORT, 'third event refused'); END`},
		{name: "third progress rollup", trigger: `CREATE TRIGGER reject_batch BEFORE UPDATE OF progress ON nodes WHEN NEW.id = 'TEST-1' AND EXISTS (SELECT 1 FROM nodes WHERE id = 'TEST-1.4') BEGIN SELECT RAISE(ABORT, 'third rollup refused'); END`},
		{name: "third claim event", trigger: `CREATE TRIGGER reject_batch BEFORE INSERT ON sync_events WHEN NEW.node_id = 'TEST-1.4' AND NEW.op_type = 'claim' BEGIN SELECT RAISE(ABORT, 'third claim refused'); END`},
		{name: "third agent update", trigger: `CREATE TRIGGER reject_batch BEFORE UPDATE ON agents WHEN NEW.current_node_id = 'TEST-1.4' BEGIN SELECT RAISE(ABORT, 'third agent refused'); END`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, st, bc := newTestNodeService(t)
			svc := service.NewNodeService(st, bc, &service.StaticConfig{AutoClaimEnabled: true}, nil, fixedClock(time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)))
			ctx := context.Background()
			parent, err := svc.CreateNode(ctx, &service.CreateNodeRequest{Project: "TEST", Title: "Parent", Creator: "author"})
			require.NoError(t, err)
			prior, err := svc.CreateNode(ctx, &service.CreateNodeRequest{Project: "TEST", ParentID: parent.ID, Title: "Completed child", Creator: "author"})
			require.NoError(t, err)
			require.NoError(t, svc.ClaimNode(ctx, prior.ID, "prior-worker"))
			require.NoError(t, svc.TransitionStatus(ctx, prior.ID, model.StatusDone, "Complete", "prior-worker"))
			require.NoError(t, svc.ClaimNode(ctx, parent.ID, "parent-worker"))
			parentBefore, err := st.GetNode(ctx, parent.ID)
			require.NoError(t, err)
			require.Equal(t, 1.0, parentBefore.Progress)
			priorBefore, err := st.GetNode(ctx, prior.ID)
			require.NoError(t, err)
			rowsBefore := createRowCounts(t, st)
			agentsBefore := decomposeAgentSnapshot(t, st)
			eventsBefore := decomposeEventSnapshot(t, st)
			require.Equal(t, 1, sequenceCounterValue(t, st, "TEST:"+parent.ID))
			if tc.trigger != "" {
				_, err = st.WriteDB().ExecContext(ctx, tc.trigger)
				require.NoError(t, err)
			}
			bc.Reset()
			children := []service.DecomposeInput{{Title: "First"}, {Title: "Second"}, {Title: "Third"}}
			if tc.mutate != nil {
				tc.mutate(&children[2])
			}
			ids, err := svc.Decompose(ctx, parent.ID, children, "author")
			require.Error(t, err)
			assert.Empty(t, ids)
			assert.Equal(t, rowsBefore, createRowCounts(t, st))
			assert.Equal(t, 1, sequenceCounterValue(t, st, "TEST:"+parent.ID))
			assert.Equal(t, agentsBefore, decomposeAgentSnapshot(t, st))
			assert.Equal(t, eventsBefore, decomposeEventSnapshot(t, st))
			parentAfter, err := st.GetNode(ctx, parent.ID)
			require.NoError(t, err)
			assert.Equal(t, parentBefore, parentAfter)
			priorAfter, err := st.GetNode(ctx, prior.ID)
			require.NoError(t, err)
			assert.Equal(t, priorBefore, priorAfter)
			assert.Empty(t, bc.Events())
			if tc.trigger != "" {
				_, err = st.WriteDB().ExecContext(ctx, `DROP TRIGGER reject_batch`)
				require.NoError(t, err)
			}
			children[2] = service.DecomposeInput{Title: "Third"}
			ids, err = svc.Decompose(ctx, parent.ID, children, "author")
			require.NoError(t, err)
			assert.Equal(t, []string{"TEST-1.2", "TEST-1.3", "TEST-1.4"}, ids)
		})
	}
}

type observingDecomposeStore struct {
	store.Store
	t      *testing.T
	sqlite *sqlite.Store
	bc     *recordingBroadcaster
	parent string
	status model.Status
	worker string
}

func (s *observingDecomposeStore) CreateNodesAllocated(ctx context.Context, nodes []*model.Node, opts store.CreateNodeOptions) error {
	for _, node := range nodes {
		require.NotEmpty(s.t, node.UID, "every UID exists before the storage write lock")
		require.Empty(s.t, node.ID)
	}
	require.Empty(s.t, s.bc.Events(), "no broadcast before the batch transaction")
	if s.status != "" {
		_, err := s.sqlite.WriteDB().ExecContext(ctx, `UPDATE nodes SET status = ?, assignee = ? WHERE id = ?`, s.status, s.worker, s.parent)
		require.NoError(s.t, err)
	}
	err := s.Store.CreateNodesAllocated(ctx, nodes, opts)
	require.Empty(s.t, s.bc.Events(), "no broadcast until the complete batch returns")
	return err
}

func TestDecompose_BatchCommit_PreservesIdentityAndEventOrder(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status model.Status
		worker string
	}{
		{"current assignment", model.StatusInProgress, "new-worker"},
		{"reopened parent", model.StatusOpen, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc, st, bc := newTestNodeService(t)
			ctx := context.Background()
			parent, err := svc.CreateNode(ctx, &service.CreateNodeRequest{Project: "TEST", Title: "Parent", Assignee: "old-worker"})
			require.NoError(t, err)
			bc.Reset()
			observed := &observingDecomposeStore{Store: st, t: t, sqlite: st, bc: bc, parent: parent.ID, status: tc.status, worker: tc.worker}
			svc = service.NewNodeService(observed, bc, &service.StaticConfig{AutoClaimEnabled: true}, nil, fixedClock(time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)))
			ids, err := svc.Decompose(ctx, parent.ID, []service.DecomposeInput{{Title: "First"}, {Title: "Second"}, {Title: "Third"}}, "author")
			require.NoError(t, err)
			require.Equal(t, []string{"TEST-1.1", "TEST-1.2", "TEST-1.3"}, ids)
			events := bc.Events()
			step := 1
			if tc.worker != "" {
				step = 2
			}
			require.Len(t, events, step*len(ids)+1)
			for i, id := range ids {
				node, err := st.GetNode(ctx, id)
				require.NoError(t, err)
				assert.Equal(t, tc.worker, node.Assignee)
				assert.NotEmpty(t, node.ContentHash)
				var eventID, uid string
				require.NoError(t, st.QueryRow(ctx, `SELECT event_id, uid FROM sync_events WHERE node_id = ? AND op_type = 'create_node'`, id).Scan(&eventID, &uid))
				assert.Equal(t, node.UID, eventID)
				assert.Equal(t, node.UID, uid)
				assert.Equal(t, service.EventNodeCreated, events[i*step].Type)
				if tc.worker != "" {
					assert.Equal(t, service.EventNodeClaimed, events[i*step+1].Type)
				}
			}
			assert.Equal(t, service.EventProgressChanged, events[len(events)-1].Type)
			rows, err := st.Query(ctx, `SELECT node_id, op_type FROM sync_events WHERE node_id IN ('TEST-1.1', 'TEST-1.2', 'TEST-1.3') ORDER BY rowid`)
			require.NoError(t, err)
			defer rows.Close()
			for _, id := range ids {
				require.True(t, rows.Next())
				var nodeID, op string
				require.NoError(t, rows.Scan(&nodeID, &op))
				assert.Equal(t, id, nodeID)
				assert.Equal(t, "create_node", op)
				if tc.worker != "" {
					require.True(t, rows.Next())
					require.NoError(t, rows.Scan(&nodeID, &op))
					assert.Equal(t, id, nodeID)
					assert.Equal(t, "claim", op)
				}
			}
			assert.False(t, rows.Next())
			require.NoError(t, rows.Err())
		})
	}
}

func decomposeAgentSnapshot(t *testing.T, st *sqlite.Store) string {
	t.Helper()
	var snapshot string
	require.NoError(t, st.QueryRow(context.Background(), `SELECT COALESCE(group_concat(value, '|'), '') FROM (SELECT agent_id || ':' || project || ':' || COALESCE(state, '') || ':' || COALESCE(state_changed_at, '') || ':' || COALESCE(current_node_id, '') || ':' || COALESCE(last_heartbeat, '') AS value FROM agents ORDER BY agent_id)`).Scan(&snapshot))
	return snapshot
}

func decomposeEventSnapshot(t *testing.T, st *sqlite.Store) string {
	t.Helper()
	var snapshot string
	require.NoError(t, st.QueryRow(context.Background(), `SELECT COALESCE(group_concat(value, '|'), '') FROM (SELECT event_id || ':' || hex(payload) AS value FROM sync_events ORDER BY rowid)`).Scan(&snapshot))
	return snapshot
}

func TestDecompose_ConcurrentBatches_NoInterleavingOrRefusalGaps(t *testing.T) {
	svc, st, _ := newTestNodeService(t)
	ctx := context.Background()
	parent, err := svc.CreateNode(ctx, &service.CreateNodeRequest{Project: "TEST", Title: "Parent"})
	require.NoError(t, err)
	const batches = 4
	results := make([][]string, batches)
	errs := make([]error, batches*2)
	var wg sync.WaitGroup
	for i := range batches {
		wg.Add(2)
		go func(i int) {
			defer wg.Done()
			results[i], errs[i] = svc.Decompose(ctx, parent.ID, []service.DecomposeInput{{Title: "First"}, {Title: "Second"}, {Title: "Third"}}, "author")
		}(i)
		go func(i int) {
			defer wg.Done()
			_, errs[batches+i] = svc.Decompose(ctx, parent.ID, []service.DecomposeInput{{Title: "First"}, {Title: "Second"}, {Title: "Refused", Priority: 9}}, "author")
		}(i)
	}
	wg.Wait()
	seen := make(map[int]bool)
	for i, ids := range results {
		require.NoError(t, errs[i])
		require.ErrorIs(t, errs[batches+i], model.ErrInvalidInput)
		require.Len(t, ids, 3)
		first, err := st.GetNode(ctx, ids[0])
		require.NoError(t, err)
		for j, id := range ids {
			node, err := st.GetNode(ctx, id)
			require.NoError(t, err)
			assert.Equal(t, first.Seq+j, node.Seq, "each batch is consecutive without interleaved writers")
			assert.False(t, seen[node.Seq])
			seen[node.Seq] = true
		}
	}
	assert.Equal(t, batches*3, sequenceCounterValue(t, st, "TEST:"+parent.ID))
	for seq := 1; seq <= batches*3; seq++ {
		assert.True(t, seen[seq], "no gap at sequence %d", seq)
	}
}
