// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service_test

import (
	"context"
	"testing"
	"time"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestApplyBatch_SharedHostPending_AnotherBoardNeverPublishesPriorNotice(t *testing.T) {
	a := newWakePermissionFixture(t)
	b := newWakePermissionFixture(t)
	b.state, b.svc.State, b.svc.HostID = a.state, a.state, a.svc.HostID
	b.board.ID = "22222222-2222-4222-8222-222222222222"
	for _, f := range []*wakePermissionFixture{a, b} {
		f.register(t, "first")
		permissionRoot(t, f, "TEST-1")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	fault := &permissionFaultStore{PermissionStore: a.store, fail: true}
	a.svc.Store = fault
	batch := filePermissionReplayBatch()
	batch.Changes[0].Action = "deny"
	require.Error(t, a.svc.ApplyBatch(ctx, a.board, batch))
	before, err := a.state.Confirmations(ctx)
	require.NoError(t, err)
	require.Len(t, before, 1)
	require.True(t, before[0].Pending)
	require.Equal(t, a.board.ID, before[0].BoardID)
	grants, err := a.state.AgentGrants()
	require.NoError(t, err)
	other := filePermissionReplayBatch()
	other.ID = "fedcba9876543210fedcba9876543210"
	assert.Error(t, b.svc.ApplyBatch(ctx, b.board, other), "a foreign board must refuse recovery of the host-global pending batch")
	assertNoPermissionPublication(t, b)
	after, err := a.state.Confirmations(ctx)
	require.NoError(t, err)
	assert.Equal(t, before, after, "board A pending audit identity must remain intact")
	retained, err := a.state.AgentGrants()
	require.NoError(t, err)
	assert.Equal(t, grants, retained, "board B must not change the protected projection")
	fault.fail = false
	require.NoError(t, a.svc.ApplyBatch(ctx, a.board, batch))
	assertRecoveredPermission(t, a, batch.ID)
	require.NoError(t, ctx.Err())
}

func assertNoPermissionPublication(t *testing.T, f *wakePermissionFixture) {
	t.Helper()
	node, err := f.store.GetNode(t.Context(), "TEST-1")
	require.NoError(t, err)
	assert.Empty(t, node.Annotations, "same display ID on board B cannot receive board A publication")
	var count int
	require.NoError(t, f.store.QueryRow(t.Context(), `SELECT COUNT(*) FROM sync_events WHERE op_type = ?`, model.OpComment).Scan(&count))
	assert.Zero(t, count, "board B must have no permission comment event")
}
