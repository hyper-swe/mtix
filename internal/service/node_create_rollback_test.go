// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Rejected local creates preserve sequence counters and every transactional write (MTIX-106/107.99).
package service_test

import (
	"context"
	"log/slog"
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

func TestCreateNode_Refusal_PreservesCounterAndNextNumber(t *testing.T) {
	cases := []struct {
		name    string
		status  model.Status
		mutate  func(*service.CreateNodeRequest)
		trigger string
	}{
		{name: "done", status: model.StatusDone},
		{name: "cancelled", status: model.StatusCancelled},
		{name: "invalidated", status: model.StatusInvalidated},
		{name: "priority", mutate: func(r *service.CreateNodeRequest) { r.Priority = 9 }},
		{name: "wake time", mutate: func(r *service.CreateNodeRequest) {
			bad := time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)
			r.DeferUntil = &bad
		}},
		{name: "insert", trigger: `CREATE TRIGGER reject_create BEFORE INSERT ON nodes BEGIN SELECT RAISE(ABORT, 'insert refused'); END`},
		{name: "create event", trigger: `CREATE TRIGGER reject_create BEFORE INSERT ON sync_events BEGIN SELECT RAISE(ABORT, 'event refused'); END`},
		{name: "rollup", trigger: `CREATE TRIGGER reject_create BEFORE UPDATE OF progress ON nodes BEGIN SELECT RAISE(ABORT, 'progress refused'); END`},
		{name: "explicit claim", mutate: func(r *service.CreateNodeRequest) { r.Assignee = "worker" }, trigger: `CREATE TRIGGER reject_create BEFORE INSERT ON sync_events WHEN NEW.op_type = 'claim' BEGIN SELECT RAISE(ABORT, 'claim refused'); END`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, initial := range []int{0, 1} {
				t.Run(string(rune('0'+initial)), func(t *testing.T) {
					svc, st, bc := newTestNodeService(t)
					ctx := context.Background()
					parent, err := svc.CreateNode(ctx, &service.CreateNodeRequest{Project: "TEST", Title: "Parent", Creator: "author"})
					require.NoError(t, err)
					if initial > 0 {
						_, err = svc.CreateNode(ctx, &service.CreateNodeRequest{Project: "TEST", ParentID: parent.ID, Title: "Existing child"})
						require.NoError(t, err)
					}
					if tc.status != "" {
						_, err = st.WriteDB().ExecContext(ctx, `UPDATE nodes SET status = ? WHERE id = ?`, tc.status, parent.ID)
						require.NoError(t, err)
					}
					if tc.trigger != "" {
						_, err = st.WriteDB().ExecContext(ctx, tc.trigger)
						require.NoError(t, err)
					}
					bc.Reset()
					before := createRowCounts(t, st)
					req := &service.CreateNodeRequest{Project: "TEST", ParentID: parent.ID, Title: "Refused child", Creator: "author"}
					if tc.mutate != nil {
						tc.mutate(req)
					}
					_, err = svc.CreateNode(ctx, req)
					require.Error(t, err)
					assert.Equal(t, initial, sequenceCounterValue(t, st, "TEST:"+parent.ID))
					assert.Equal(t, before, createRowCounts(t, st))
					assert.Empty(t, bc.Events())
					if tc.trigger != "" {
						_, err = st.WriteDB().ExecContext(ctx, `DROP TRIGGER reject_create`)
						require.NoError(t, err)
					}
					if tc.status != "" {
						_, err = st.WriteDB().ExecContext(ctx, `UPDATE nodes SET status = ? WHERE id = ?`, model.StatusOpen, parent.ID)
						require.NoError(t, err)
					}
					next, err := svc.CreateNode(ctx, &service.CreateNodeRequest{Project: "TEST", ParentID: parent.ID, Title: "Accepted child", Creator: "author"})
					require.NoError(t, err)
					assert.Equal(t, model.BuildID("TEST", parent.ID, initial+1), next.ID)
				})
			}
		})
	}
}

func createRowCounts(t *testing.T, st *sqlite.Store) []int {
	t.Helper()
	counts := make([]int, 4)
	for i, q := range []string{`SELECT COUNT(*) FROM nodes`, `SELECT COUNT(*) FROM sync_events`, `SELECT COUNT(*) FROM agents`, `SELECT COUNT(*) FROM sequences`} {
		require.NoError(t, st.QueryRow(context.Background(), q).Scan(&counts[i]))
	}
	return counts
}

func TestCreateNode_ParentAutoClaimFailure_RollsBackEntireCreate(t *testing.T) {
	cases := []struct{ name, trigger string }{
		{"claim event", `CREATE TRIGGER reject_auto_claim BEFORE INSERT ON sync_events WHEN NEW.op_type = 'claim' BEGIN SELECT RAISE(ABORT, 'claim refused'); END`},
		{"agent update", `CREATE TRIGGER reject_auto_claim BEFORE UPDATE ON agents BEGIN SELECT RAISE(ABORT, 'agent update refused'); END`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st, err := sqlite.New(t.TempDir(), slog.Default())
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, st.Close()) })
			bc := newRecordingBroadcaster()
			svc := service.NewNodeService(st, bc, &service.StaticConfig{AutoClaimEnabled: true}, nil, fixedClock(time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)))
			ctx := context.Background()
			parent, err := svc.CreateNode(ctx, &service.CreateNodeRequest{Project: "TEST", Title: "Claimed parent", Creator: "author", Assignee: "parent-worker"})
			require.NoError(t, err)
			before := createRowCounts(t, st)
			bc.Reset()
			_, err = st.WriteDB().ExecContext(ctx, tc.trigger)
			require.NoError(t, err)
			_, err = svc.CreateNode(ctx, &service.CreateNodeRequest{Project: "TEST", ParentID: parent.ID, Title: "Auto-claimed child", Creator: "author"})
			require.Error(t, err)
			assert.Equal(t, before, createRowCounts(t, st))
			assert.Zero(t, sequenceCounterValue(t, st, "TEST:"+parent.ID))
			assert.Empty(t, bc.Events())
			var current string
			require.NoError(t, st.QueryRow(ctx, `SELECT current_node_id FROM agents WHERE agent_id = ?`, "parent-worker").Scan(&current))
			assert.Equal(t, parent.ID, current)
			_, err = st.WriteDB().ExecContext(ctx, `DROP TRIGGER reject_auto_claim`)
			require.NoError(t, err)
			child, err := svc.CreateNode(ctx, &service.CreateNodeRequest{Project: "TEST", ParentID: parent.ID, Title: "Accepted auto-claim", Creator: "author"})
			require.NoError(t, err)
			assert.Equal(t, parent.ID+".1", child.ID)
			assert.Equal(t, model.StatusInProgress, child.Status)
			assert.Equal(t, "parent-worker", child.Assignee)
			events := bc.Events()
			require.Len(t, events, 2)
			assert.Equal(t, service.EventNodeCreated, events[0].Type)
			assert.Equal(t, service.EventNodeClaimed, events[1].Type)
		})
	}
}

func TestDecompose_RefusedChild_PreservesCounter(t *testing.T) {
	svc, st, _ := newTestNodeService(t)
	ctx := context.Background()
	parent, err := svc.CreateNode(ctx, &service.CreateNodeRequest{Project: "TEST", Title: "Parent"})
	require.NoError(t, err)
	_, err = svc.Decompose(ctx, parent.ID, []service.DecomposeInput{{Title: "Bad child", Priority: 9}}, "author")
	require.Error(t, err)
	assert.Zero(t, sequenceCounterValue(t, st, "TEST:"+parent.ID))
	ids, err := svc.Decompose(ctx, parent.ID, []service.DecomposeInput{{Title: "Good child"}}, "author")
	require.NoError(t, err)
	assert.Equal(t, []string{parent.ID + ".1"}, ids)
}

// parentChangedBeforeCreate models another writer updating a parent before the create transaction begins.
type parentChangedBeforeCreate struct {
	store.Store
	sqlite   *sqlite.Store
	status   model.Status
	assignee string
}

func (s *parentChangedBeforeCreate) CreateNodeAllocated(ctx context.Context, node *model.Node, opts store.CreateNodeOptions) error {
	if node.ParentID != "" {
		if _, err := s.sqlite.WriteDB().ExecContext(ctx, `UPDATE nodes SET status = ?, assignee = ? WHERE id = ?`, s.status, s.assignee, node.ParentID); err != nil {
			return err
		}
	}
	return s.Store.CreateNodeAllocated(ctx, node, opts)
}

func TestCreateNode_AutoClaim_ReadsParentWithinCreateTransaction(t *testing.T) {
	cases := []struct {
		name           string
		status         model.Status
		assignee, want string
	}{
		{"assignment changed", model.StatusInProgress, "new-worker", "new-worker"},
		{"parent reopened", model.StatusOpen, "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc, st, _ := newTestNodeService(t)
			ctx := context.Background()
			parent, err := svc.CreateNode(ctx, &service.CreateNodeRequest{Project: "TEST", Title: "Parent", Assignee: "old-worker"})
			require.NoError(t, err)
			changed := &parentChangedBeforeCreate{Store: st, sqlite: st, status: tc.status, assignee: tc.assignee}
			svc = service.NewNodeService(changed, nil, &service.StaticConfig{AutoClaimEnabled: true}, nil, fixedClock(time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)))
			child, err := svc.CreateNode(ctx, &service.CreateNodeRequest{Project: "TEST", ParentID: parent.ID, Title: "Child"})
			require.NoError(t, err)
			assert.Equal(t, tc.want, child.Assignee)
		})
	}
}

func TestCreateNode_ConcurrentRefusals_DoNotLeaveSequenceGaps(t *testing.T) {
	svc, st, _ := newTestNodeService(t)
	ctx := context.Background()
	parent, err := svc.CreateNode(ctx, &service.CreateNodeRequest{Project: "TEST", Title: "Parent"})
	require.NoError(t, err)
	const count = 8
	var wg sync.WaitGroup
	ids := make([]string, count)
	errs := make([]error, count*2)
	for i := 0; i < count; i++ {
		wg.Add(2)
		go func(i int) {
			defer wg.Done()
			node, err := svc.CreateNode(ctx, &service.CreateNodeRequest{Project: "TEST", ParentID: parent.ID, Title: "Accepted child"})
			errs[i] = err
			if node != nil {
				ids[i] = node.ID
			}
		}(i)
		go func(i int) {
			defer wg.Done()
			_, err := svc.CreateNode(ctx, &service.CreateNodeRequest{Project: "TEST", ParentID: parent.ID, Title: "Refused child", Priority: 9})
			errs[count+i] = err
		}(i)
	}
	wg.Wait()
	want := make([]string, count)
	for i := range count {
		require.NoError(t, errs[i])
		require.ErrorIs(t, errs[count+i], model.ErrInvalidInput)
		want[i] = model.BuildID("TEST", parent.ID, i+1)
	}
	assert.ElementsMatch(t, want, ids)
	assert.Equal(t, count, sequenceCounterValue(t, st, "TEST:"+parent.ID))
}
