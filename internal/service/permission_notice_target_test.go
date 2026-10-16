// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service_test

import (
	"testing"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/oplocal"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestApplyBatch_OneNeutralNoticePerBatch_FirstLiveRootAndIndependentBodies(t *testing.T) {
	f := newWakePermissionFixture(t)
	for _, id := range []string{"first", "second"} {
		f.register(t, id)
	}
	for _, id := range []string{"TEST-1", "TEST-3", "TEST-2"} {
		permissionRoot(t, f, id)
	}
	require.NoError(t, f.store.DeleteNode(t.Context(), "TEST-1", false, "fixture"))
	require.NoError(t, f.store.CreateNode(t.Context(), &model.Node{ID: "TEST-2.1", Project: "TEST", ParentID: "TEST-2", Depth: 1,
		Title: "Synthetic child", Status: model.StatusOpen, Creator: "fixture", CreatedAt: f.now, UpdatedAt: f.now}))
	f.apply(t, oplocal.PermissionChange{AgentID: "first", Action: "allow"}, oplocal.PermissionChange{AgentID: "second", Action: "deny"})
	target, err := f.store.GetNode(t.Context(), "TEST-2")
	require.NoError(t, err)
	require.Len(t, target.Annotations, 1)
	assert.Equal(t, service.PermissionNotice, target.Annotations[0].Text)
	assert.Equal(t, "mtix-permissions", target.Annotations[0].Author)
	assert.Empty(t, target.Annotations[0].Addressee)
	firstID := target.Annotations[0].ID
	for _, id := range []string{"TEST-3", "TEST-2.1"} {
		untouched, err := f.store.GetNode(t.Context(), id)
		require.NoError(t, err)
		assert.Empty(t, untouched.Annotations)
	}
	rows, err := f.state.Confirmations(t.Context())
	require.NoError(t, err)
	require.Len(t, rows, 2)
	for _, row := range rows {
		assert.Equal(t, rows[0].ID, row.ID)
		assert.NotEqual(t, firstID, row.ID)
		assert.Equal(t, firstID, permissionAcceptedPublicationID(t, f, row.ID))
		assert.Equal(t, "published", row.BoardLine)
	}
	f.apply(t, oplocal.PermissionChange{AgentID: "first", Action: "deny"})
	target, err = f.store.GetNode(t.Context(), "TEST-2")
	require.NoError(t, err)
	require.Len(t, target.Annotations, 2)
	assert.NotEqual(t, firstID, target.Annotations[1].ID)
	assert.Equal(t, target.Annotations[0].Text, target.Annotations[1].Text)
}
