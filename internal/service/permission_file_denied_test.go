// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/hyper-swe/mtix/internal/oplocal"
	"github.com/hyper-swe/mtix/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func deniedFileFixture(t *testing.T) *wakePermissionFixture {
	t.Helper()
	f := newWakePermissionFixture(t)
	f.register(t, "first")
	f.register(t, "second")
	permissionRoot(t, f, "TEST-1")
	require.NoError(t, f.svc.ApplyBatch(t.Context(), f.board, oplocal.PermissionBatch{Mode: "yes", Source: "mtix agent deny", Changes: []oplocal.PermissionChange{{AgentID: "first", Action: "deny"}}}))
	return f
}

func TestApplyBatch_FileAllowDenied_WithAndWithoutID_NoOpAndRestApplies(t *testing.T) {
	for _, supplied := range []bool{true, false} {
		t.Run(map[bool]string{true: "supplied_id", false: "generated_id"}[supplied], func(t *testing.T) {
			f := deniedFileFixture(t)
			before, err := f.state.AgentGrants()
			require.NoError(t, err)
			require.Len(t, before, 1)
			batch := filePermissionReplayBatch()
			if !supplied {
				batch.ID = ""
			}
			batch.Changes = append(batch.Changes, oplocal.PermissionChange{AgentID: "second", Action: "allow"})
			require.NoError(t, f.svc.ApplyBatch(t.Context(), f.board, batch))
			after, err := f.state.AgentGrants()
			require.NoError(t, err)
			require.Len(t, after, 2)
			for _, grant := range after {
				if grant.AgentID == "first" {
					assert.Equal(t, before[0], grant)
				}
				if grant.AgentID == "second" {
					assert.False(t, grant.Denied)
				}
			}
			ok, reason, err := f.svc.Wakeable(t.Context(), f.board, "first")
			require.NoError(t, err)
			assert.False(t, ok)
			assert.Equal(t, "denied", reason)
			ok, _, err = f.svc.Wakeable(t.Context(), f.board, "second")
			require.NoError(t, err)
			assert.True(t, ok)
		})
	}
}

func TestConfirmations_FileAllowDenied_ExistingJSONReportsExactOutcome(t *testing.T) {
	f := deniedFileFixture(t)
	batch := filePermissionReplayBatch()
	require.NoError(t, f.svc.ApplyBatch(t.Context(), f.board, batch))
	rows, err := f.state.Confirmations(t.Context())
	require.NoError(t, err)
	encoded, err := json.Marshal(rows)
	require.NoError(t, err)
	var records []map[string]any
	require.NoError(t, json.Unmarshal(encoded, &records))
	found := 0
	for _, row := range records {
		if row["id"] == batch.ID && row["agent_id"] == "first" {
			found++
			assert.Equal(t, "allow", row["action"])
			assert.Equal(t, "file", row["mode"])
			assert.Equal(t, "denied_requires_operator", row["outcome"])
		}
	}
	assert.Equal(t, 1, found)
}

func TestApplyBatch_FileDeny_StillAppliesToImplicitRegisteredID(t *testing.T) {
	f := newWakePermissionFixture(t)
	f.register(t, "first")
	batch := filePermissionReplayBatch()
	batch.Changes[0].Action = "deny"
	require.NoError(t, f.svc.ApplyBatch(t.Context(), f.board, batch))
	ok, reason, err := f.svc.Wakeable(t.Context(), f.board, "first")
	require.NoError(t, err)
	assert.False(t, ok)
	assert.Equal(t, "denied", reason)
}

func TestApplyBatch_FileAllowEvictedEmptyBoard_RetainsLaterDenyProjection(t *testing.T) {
	f := newWakePermissionFixture(t)
	f.svc.Clock = func() time.Time { return f.now }
	f.register(t, "first")
	legacy := []oplocal.Grant{{BoardID: "22222222-2222-4222-8222-222222222222", Hub: f.board.Hub}}
	require.NoError(t, f.state.WriteJSON("grants", map[string]any{"board_grants": legacy}))
	batch := filePermissionReplayBatch()
	require.NoError(t, f.svc.ApplyBatch(t.Context(), f.board, batch))
	require.NoError(t, f.svc.ApplyBatch(t.Context(), f.board, oplocal.PermissionBatch{Mode: "yes", Source: "mtix agent deny", Changes: []oplocal.PermissionChange{{AgentID: "first", Action: "deny"}}}))
	rows, err := f.state.Confirmations(t.Context())
	require.NoError(t, err)
	require.Len(t, rows, 2)
	require.Equal(t, "deny", rows[1].Action)
	dir, err := f.state.Path()
	require.NoError(t, err)
	before, err := os.ReadFile(filepath.Join(dir, "grants.json"))
	require.NoError(t, err)
	syntheticEvictedPermissionHistory(t, f, rows[1])
	f.now = f.now.Add(time.Hour)
	require.NoError(t, f.svc.ApplyBatch(t.Context(), f.board, batch))
	after, err := os.ReadFile(filepath.Join(dir, "grants.json"))
	require.NoError(t, err)
	assert.Equal(t, before, after, "later deny and every protected grant field remain byte-exact")
	ok, reason, err := f.svc.Wakeable(t.Context(), f.board, "first")
	require.NoError(t, err)
	assert.False(t, ok)
	assert.Equal(t, "denied", reason)
	retained, reports, err := f.state.BoardGrants()
	require.NoError(t, err)
	assert.Empty(t, reports)
	assert.Equal(t, legacy, retained)
	nodes, _, err := f.store.ListNodes(t.Context(), store.NodeFilter{}, store.ListOptions{Limit: -1})
	require.NoError(t, err)
	assert.Empty(t, nodes, "empty-board safety cannot rely on a published annotation")
}

// This fixture represents already-evicted completed receipt/history state.
// Actual bounded eviction execution is witnessed separately in oplocal tests.
func syntheticEvictedPermissionHistory(t *testing.T, f *wakePermissionFixture, deny oplocal.Confirmation) {
	t.Helper()
	require.NoError(t, f.state.WriteJSON("permissions-replays", []any{}))
	data, err := json.Marshal(deny)
	require.NoError(t, err)
	dir, err := f.state.Path()
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "confirmations.jsonl"), append(data, '\n'), 0600))
	remaining, err := f.state.Confirmations(t.Context())
	require.NoError(t, err)
	assert.Equal(t, []oplocal.Confirmation{deny}, remaining)
}

func TestApplyBatch_FileAllowEvictedRetainedHistory_NoConflictAndRestApplies(t *testing.T) {
	f := newWakePermissionFixture(t)
	f.svc.Clock = func() time.Time { return f.now }
	for _, id := range []string{"first", "second"} {
		f.register(t, id)
	}
	permissionRoot(t, f, "TEST-1")
	request := filePermissionReplayBatch()
	request.Changes = append(request.Changes, oplocal.PermissionChange{AgentID: "second", Action: "deny"})
	require.NoError(t, f.svc.ApplyBatch(t.Context(), f.board, request))
	require.NoError(t, f.svc.ApplyBatch(t.Context(), f.board, oplocal.PermissionBatch{Mode: "yes", Source: "fixture operator", Changes: []oplocal.PermissionChange{{AgentID: "first", Action: "deny"}, {AgentID: "second", Action: "allow"}}}))
	before, err := f.state.Confirmations(t.Context())
	require.NoError(t, err)
	require.Len(t, before, 4)
	node, err := f.store.GetNode(t.Context(), "TEST-1")
	require.NoError(t, err)
	require.Len(t, node.Annotations, 2)
	require.NoError(t, f.state.WriteJSON("permissions-replays", []any{}))
	retained, err := f.state.Confirmations(t.Context())
	require.NoError(t, err)
	assert.Equal(t, before, retained, "synthetic completed-receipt eviction keeps all confirmation history")
	f.now = f.now.Add(time.Hour)
	require.NoError(t, f.svc.ApplyBatch(t.Context(), f.board, request), "expired caller id must not collide with retained confirmation or ordinary annotation ids")
	for _, id := range []string{"first", "second"} {
		ok, reason, err := f.svc.Wakeable(t.Context(), f.board, id)
		require.NoError(t, err)
		assert.False(t, ok)
		assert.Equal(t, "denied", reason)
	}
	rows, err := f.state.Confirmations(t.Context())
	require.NoError(t, err)
	data, err := json.Marshal(rows)
	require.NoError(t, err)
	assert.Contains(t, string(data), "denied_requires_operator")
}
