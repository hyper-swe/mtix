// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service_test

import (
	"testing"

	"github.com/google/uuid"
	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/oplocal"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestApplyBatch_InvalidMode_RefusesBeforeDependencies(t *testing.T) {
	svc := service.NewWakePermissions(service.PermissionDeps{})
	batch := oplocal.PermissionBatch{Mode: "untrusted-mode", Source: "review fixture", Changes: []oplocal.PermissionChange{{AgentID: "first", Action: "allow"}}}
	var err error
	assert.NotPanics(t, func() {
		err = svc.ApplyBatch(t.Context(), service.PermissionBoard{ID: "11111111-1111-4111-8111-111111111111"}, batch)
	})
	assert.ErrorIs(t, err, model.ErrInvalidInput)
}

func TestApplyBatch_AcrossHostsAndRetries_KeepsPublicationIdentityIndependent(t *testing.T) {
	batch := oplocal.PermissionBatch{ID: "0123456789abcdef0123456789abcdef", Mode: "file", Source: "same orchestrator file", Changes: []oplocal.PermissionChange{{AgentID: "first", Action: "allow"}}}
	var notices []string
	var hosts []string
	for range 2 {
		f := newWakePermissionFixture(t)
		reviewR1Hub(t, f, "first")
		require.NoError(t, f.svc.ApplyBatch(t.Context(), f.board, batch))
		node, err := f.store.GetNode(t.Context(), "TEST-1")
		require.NoError(t, err)
		require.Len(t, node.Annotations, 1)
		notice := node.Annotations[0].ID
		parsed, err := uuid.Parse(notice)
		require.NoError(t, err)
		assert.Equal(t, uuid.Version(7), parsed.Version())
		assert.Equal(t, parsed.String(), notice)
		assert.NotEqual(t, batch.ID, notice)
		require.NoError(t, f.svc.ApplyBatch(t.Context(), f.board, batch))
		node, err = f.store.GetNode(t.Context(), "TEST-1")
		require.NoError(t, err)
		require.Len(t, node.Annotations, 1)
		assert.Equal(t, notice, node.Annotations[0].ID)
		assert.Equal(t, 1, permissionWireEventCount(t, f.store, notice))
		notices, hosts = append(notices, notice), append(hosts, f.svc.HostID)
	}
	require.NotEqual(t, hosts[0], hosts[1], "the fixture exercised distinct protected host states")
	assert.NotEqual(t, notices[0], notices[1], "a shared caller id must not correlate host publications")
}

func TestApplyFileBatch_ExistingOperatorDeny_DoesNotClear(t *testing.T) {
	f := newWakePermissionFixture(t)
	reviewR1Hub(t, f, "first")
	require.NoError(t, reviewR1Apply(t, f, "first", "deny"))
	batch := filePermissionReplayBatch()
	batch.Mode = "yes"
	require.NoError(t, f.svc.ApplyFileBatch(t.Context(), f.board, batch))
	reviewR1Denied(t, f, "first")
	result, available, err := f.state.PermissionReplayResult(t.Context(), f.board.ID, batch.ID)
	require.NoError(t, err)
	require.True(t, available)
	assert.Equal(t, "file", result.Mode)
	require.Len(t, result.Changes, 1)
	assert.Equal(t, "denied_requires_operator", result.Changes[0].Outcome)
}
