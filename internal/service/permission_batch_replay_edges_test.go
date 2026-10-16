// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service_test

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestApplyBatch_FileIDReplay_PostcommitDrift_ReusesAcceptedPendingMetadata(t *testing.T) {
	f := newWakePermissionFixture(t)
	f.svc.Clock = func() time.Time { return f.now }
	permissionRoot(t, f, "TEST-1")
	fault := &permissionFaultStore{PermissionStore: f.store, after: true, fail: true}
	f.svc.Store = fault
	batch := filePermissionReplayBatch()
	require.Error(t, f.svc.ApplyBatch(t.Context(), f.board, batch))
	before, err := f.state.Confirmations(t.Context())
	require.NoError(t, err)
	require.Len(t, before, 1)
	assert.True(t, before[0].Pending)
	assert.True(t, before[0].PendingRegistration)
	assert.Equal(t, "pending", before[0].BoardLine)
	f.now = f.now.Add(time.Hour)
	f.register(t, "first")
	fault.fail = false
	require.NoError(t, f.svc.ApplyBatch(t.Context(), f.board, batch))
	expected := before[0]
	expected.Pending = false
	expected.BoardLine = "published"
	assertFilePermissionReplay(t, f, expected)
	pending, err := f.svc.PendingRegistrations(t.Context(), f.board)
	require.NoError(t, err)
	assert.Empty(t, pending)
}

func TestApplyBatch_FileIDReplay_OlderAllowAfterDeny_NeverRegrants(t *testing.T) {
	f := newWakePermissionFixture(t)
	f.svc.Clock = func() time.Time { return f.now }
	f.register(t, "first")
	permissionRoot(t, f, "TEST-1")
	allow := filePermissionReplayBatch()
	require.NoError(t, f.svc.ApplyBatch(t.Context(), f.board, allow))
	deny := filePermissionReplayBatch()
	deny.ID = "fedcba9876543210fedcba9876543210"
	deny.Changes[0].Action = "deny"
	require.NoError(t, f.svc.ApplyBatch(t.Context(), f.board, deny))
	before, err := f.state.Confirmations(t.Context())
	require.NoError(t, err)
	grants, err := f.state.AgentGrants()
	require.NoError(t, err)
	require.Len(t, grants, 1)
	require.True(t, grants[0].Denied)
	f.now = f.now.Add(time.Hour)
	require.NoError(t, f.svc.ApplyBatch(t.Context(), f.board, allow))
	after, err := f.state.Confirmations(t.Context())
	require.NoError(t, err)
	assert.Equal(t, before, after)
	retained, err := f.state.AgentGrants()
	require.NoError(t, err)
	assert.Equal(t, grants, retained)
	ok, reason, err := f.svc.Wakeable(t.Context(), f.board, "first")
	require.NoError(t, err)
	assert.False(t, ok)
	assert.Equal(t, "denied", reason)
	node, err := f.store.GetNode(t.Context(), "TEST-1")
	require.NoError(t, err)
	assert.Len(t, node.Annotations, 2)
}

func TestApplyBatch_FileIDReplay_ConcurrentCallers_OneAcceptedBatchAndEvent(t *testing.T) {
	f := newWakePermissionFixture(t)
	f.register(t, "first")
	permissionRoot(t, f, "TEST-1")
	var clock atomic.Int64
	f.svc.Clock = func() time.Time { return f.now.Add(time.Duration(clock.Add(1))) }
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	results := make([]error, 16)
	var joined sync.WaitGroup
	for i := range results {
		joined.Add(1)
		go func(index int) {
			defer joined.Done()
			results[index] = f.svc.ApplyBatch(ctx, f.board, filePermissionReplayBatch())
		}(i)
	}
	joined.Wait()
	require.NoError(t, ctx.Err())
	for _, err := range results {
		require.NoError(t, err)
	}
	rows, err := f.state.Confirmations(t.Context())
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assertFilePermissionReplay(t, f, rows[0])
}

func TestApplyBatch_FileIDReplay_NoCallerID_RemainsIndependentBatches(t *testing.T) {
	f := newWakePermissionFixture(t)
	f.register(t, "first")
	permissionRoot(t, f, "TEST-1")
	for range 2 {
		batch := filePermissionReplayBatch()
		batch.ID = ""
		require.NoError(t, f.svc.ApplyBatch(t.Context(), f.board, batch))
	}
	rows, err := f.state.Confirmations(t.Context())
	require.NoError(t, err)
	require.Len(t, rows, 2)
	assert.NotEqual(t, rows[0].ID, rows[1].ID)
	node, err := f.store.GetNode(t.Context(), "TEST-1")
	require.NoError(t, err)
	assert.Len(t, node.Annotations, 2)
}
