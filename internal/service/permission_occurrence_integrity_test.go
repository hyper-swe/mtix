// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Board annotation IDs cannot stand in for an operator-local accepted grant.
package service_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/service"
)

func TestApplyBatch_ExistingBoardOccurrenceKeepsCallerAndGetsFreshHostReceipt(t *testing.T) {
	f := newWakePermissionFixture(t)
	f.register(t, "first")
	permissionRoot(t, f, "TEST-1")
	batch := filePermissionReplayBatch()
	old := model.Annotation{ID: batch.ID, Author: "mtix-permissions", Text: service.PermissionNotice, CreatedAt: f.now}
	require.NoError(t, f.store.AppendAnnotationOnce(t.Context(), "TEST-1", old))
	before, err := f.store.GetNode(t.Context(), "TEST-1")
	require.NoError(t, err)
	require.Len(t, before.Annotations, 1)
	require.NoError(t, f.svc.ApplyBatch(t.Context(), f.board, batch))
	rows, err := f.state.Confirmations(t.Context())
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, batch.ID, rows[0].CallerID)
	require.NotEqual(t, batch.ID, rows[0].ID)
	accepted, ok, err := f.state.PermissionReplayResult(t.Context(), f.board.ID, batch.ID)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, rows[0].ID, accepted.ID)
	require.NoError(t, f.svc.ApplyBatch(t.Context(), f.board, batch))
	after, err := f.store.GetNode(t.Context(), "TEST-1")
	require.NoError(t, err)
	require.Len(t, after.Annotations, 2)
	require.Equal(t, before.Annotations[0], after.Annotations[0])
	require.Equal(t, permissionAcceptedPublicationID(t, f, batch.ID), after.Annotations[1].ID)
	require.Equal(t, service.PermissionNotice, after.Annotations[1].Text)
}

type permissionCollisionStore struct {
	service.PermissionStore
	lookups int
}

func (s *permissionCollisionStore) PermissionPublicationExists(context.Context, string) (bool, error) {
	s.lookups++
	return true, nil
}

func TestApplyBatch_UnavailableUniqueOccurrenceBoundsLookupsAndDoesNotAccept(t *testing.T) {
	f := newWakePermissionFixture(t)
	permissionRoot(t, f, "TEST-1")
	fault := &permissionCollisionStore{PermissionStore: f.store}
	f.svc.Store = fault
	require.ErrorIs(t, f.svc.ApplyBatch(t.Context(), f.board, filePermissionReplayBatch()), model.ErrConflict)
	require.Equal(t, 4, fault.lookups)
	rows, err := f.state.Confirmations(t.Context())
	require.NoError(t, err)
	require.Empty(t, rows)
	grants, err := f.state.AgentGrants()
	require.NoError(t, err)
	require.Empty(t, grants)
	node, err := f.store.GetNode(t.Context(), "TEST-1")
	require.NoError(t, err)
	require.Empty(t, node.Annotations)
}
