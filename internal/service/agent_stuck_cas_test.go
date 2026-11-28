// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service_test

import (
	"context"
	"database/sql"
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

// queryHookStore runs hook once, right after the first Query returns: the
// window between the stuck sweep's read of the agents table and its release
// of the node (MTIX-325.1).
type queryHookStore struct {
	store.Store
	once sync.Once
	hook func()
}

func (q *queryHookStore) Query(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	rows, err := q.Store.Query(ctx, query, args...)
	q.once.Do(func() { q.hook() })
	return rows, err
}

// stuckSweepFixture is a node claimed by a stuck agent past the timeout.
type stuckSweepFixture struct {
	agentSvc *service.AgentService
	s        *sqlite.Store
	hs       *queryHookStore
	nodeID   string
}

func newStuckSweepFixture(t *testing.T) *stuckSweepFixture {
	t.Helper()
	s, err := sqlite.New(t.TempDir(), slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })

	cfg := &service.StaticConfig{StaleThreshold: 24 * time.Hour, StuckTimeout: 30 * time.Minute}
	now := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)
	hs := &queryHookStore{Store: s, hook: func() {}}
	nodeSvc := service.NewNodeService(s, &service.NoopBroadcaster{}, cfg, slog.Default(), fixedClock(now))
	agentSvc := service.NewAgentService(hs, &service.NoopBroadcaster{}, cfg, slog.Default(), fixedClock(now))
	ctx := context.Background()

	registerAgent(t, s, "agent-1", "PROJ", now)
	registerAgent(t, s, "agent-c", "PROJ", now)
	node, err := nodeSvc.CreateNode(ctx, &service.CreateNodeRequest{Project: "PROJ", Title: "Stuck CAS", Creator: "admin"})
	require.NoError(t, err)
	require.NoError(t, s.ClaimNode(ctx, node.ID, "agent-1"))
	require.NoError(t, agentSvc.UpdateAgentState(ctx, "agent-1", model.AgentStateStuck))
	_, err = s.WriteDB().ExecContext(ctx,
		`UPDATE agents SET state_changed_at = ?, current_node_id = ? WHERE agent_id = ?`,
		now.Add(-31*time.Minute).UTC().Format(time.RFC3339), node.ID, "agent-1")
	require.NoError(t, err)
	return &stuckSweepFixture{agentSvc: agentSvc, s: s, hs: hs, nodeID: node.ID}
}

// TestCheckStuckTimeouts_ReassignedAfterQuery_KeepsNewClaim verifies the sweep
// does not release a claim that moved to another agent between its read of the
// stuck agents and its release (MTIX-325.1): nothing is written for that node.
func TestCheckStuckTimeouts_ReassignedAfterQuery_KeepsNewClaim(t *testing.T) {
	f := newStuckSweepFixture(t)
	ctx := context.Background()
	f.hs.hook = func() {
		require.NoError(t, f.s.UpdateNode(ctx, f.nodeID, assigneeUpdate("agent-c")))
	}

	require.NoError(t, f.agentSvc.CheckStuckTimeouts(ctx))

	got, err := f.s.GetNode(ctx, f.nodeID)
	require.NoError(t, err)
	assert.Equal(t, "agent-c", got.Assignee)
	assert.Equal(t, model.StatusInProgress, got.Status)
	state, err := f.agentSvc.GetAgentState(ctx, "agent-1")
	require.NoError(t, err)
	assert.Equal(t, model.AgentStateStuck, state, "a skipped release leaves the stuck agent row untouched")
}

// TestCheckStuckTimeouts_ReleasedAfterQuery_SkipsWithoutError verifies a claim
// cleared after the query is skipped, not an error, and the node is not
// unclaimed a second time (its status stays as the clearing update left it).
func TestCheckStuckTimeouts_ReleasedAfterQuery_SkipsWithoutError(t *testing.T) {
	f := newStuckSweepFixture(t)
	ctx := context.Background()
	f.hs.hook = func() {
		require.NoError(t, f.s.UpdateNode(ctx, f.nodeID, assigneeUpdate("")))
	}

	require.NoError(t, f.agentSvc.CheckStuckTimeouts(ctx))

	got, err := f.s.GetNode(ctx, f.nodeID)
	require.NoError(t, err)
	assert.Empty(t, got.Assignee)
	assert.Equal(t, model.StatusInProgress, got.Status)
}
