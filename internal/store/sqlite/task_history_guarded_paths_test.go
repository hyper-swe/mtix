// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store"
)

// clearHistory drops the entries written by test setup.
func (f *historyFixture) clearHistory(t *testing.T) {
	t.Helper()
	_, err := f.db.Exec("DELETE FROM task_history")
	require.NoError(t, err)
}

// TestTaskHistory_GuardedAndBackgroundPaths_AppendEntries covers the status
// writers that bypass TransitionStatus (F1), force-reclaim (F10) and the
// allocated create family (F4), each of which once wrote no history.
func TestTaskHistory_GuardedAndBackgroundPaths_AppendEntries(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name   string
		node   string
		act    func(t *testing.T, f *historyFixture)
		fields []string
	}{
		{"guarded transition", ccRoot, func(t *testing.T, f *historyFixture) {
			require.NoError(t, f.s.TransitionStatusFrom(ctx, ccRoot, model.StatusOpen, model.StatusInProgress, "go", "alice"))
		}, []string{"status"}},
		{"restore invalidated", ccRoot, func(t *testing.T, f *historyFixture) {
			_, err := f.db.Exec(`UPDATE nodes SET status = 'invalidated', previous_status = 'open' WHERE id = ?`, ccRoot)
			require.NoError(t, err)
			restored, err := f.s.RestoreInvalidated(ctx, ccRoot, "alice")
			require.NoError(t, err)
			assert.Equal(t, model.StatusOpen, restored)
		}, []string{"status"}},
		{"wake deferred", ccRoot, func(t *testing.T, f *historyFixture) {
			until := historyTestTime.Add(-time.Hour)
			require.NoError(t, f.s.DeferNode(ctx, ccRoot, &until, "later", "alice"))
			f.clearHistory(t)
			woken, err := f.s.WakeDeferredNode(ctx, ccRoot, historyTestTime)
			require.NoError(t, err)
			require.True(t, woken)
			for _, r := range f.rows(t, ccRoot) {
				assert.Equal(t, "system", r.origin, "the wake pass is automatic")
				assert.Equal(t, "system", r.actor)
			}
		}, []string{"defer_until", "status"}},
		{"force reclaim", ccRoot, func(t *testing.T, f *historyFixture) {
			_, err := f.db.Exec(`INSERT INTO agents (agent_id, project, state, last_heartbeat) VALUES ('agent-1', 'PROJ', 'working', ?)`,
				historyTestTime.Add(-48*time.Hour).Format(time.RFC3339))
			require.NoError(t, err)
			require.NoError(t, f.s.ClaimNode(ctx, ccRoot, "agent-1"))
			f.clearHistory(t)
			require.NoError(t, f.s.ForceReclaimNode(ctx, ccRoot, "agent-2", 24*time.Hour))
		}, []string{"assignee"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newHistoryFixture(t)
			f.seed(t)
			tt.act(t, f)
			assert.Equal(t, tt.fields, f.fields(t, tt.node))
		})
	}
}

func TestTaskHistory_AllocatedCreates_RecordInitialFieldsPerNode(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name string
		run  func(t *testing.T, f *historyFixture) []*model.Node
	}{
		{"CreateNodeAllocated", func(t *testing.T, f *historyFixture) []*model.Node {
			n := seqNode("TEST-0", 0)
			require.NoError(t, f.s.CreateNodeAllocated(ctx, n, store.CreateNodeOptions{}))
			return []*model.Node{n}
		}},
		{"CreateNodesAllocated batch of two", func(t *testing.T, f *historyFixture) []*model.Node {
			ns := []*model.Node{seqNode("TEST-0", 0), seqNode("TEST-0", 0)}
			require.NoError(t, f.s.CreateNodesAllocated(ctx, ns, store.CreateNodeOptions{}))
			return ns
		}},
		{"CreateNodeAndClaim", func(t *testing.T, f *historyFixture) []*model.Node {
			n := seqNode("TEST-1", 1)
			require.NoError(t, f.s.CreateNodeAndClaim(ctx, n, "worker"))
			return []*model.Node{n}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newHistoryFixture(t)
			for _, n := range tt.run(t, f) {
				fields := f.fields(t, n.ID)
				assert.Contains(t, fields, "title", n.ID)
				assert.Contains(t, fields, "status", n.ID)
				seen := map[string]bool{}
				for _, field := range fields {
					assert.False(t, seen[field], "%s: %s recorded twice", n.ID, field)
					seen[field] = true
				}
			}
		})
	}
}
