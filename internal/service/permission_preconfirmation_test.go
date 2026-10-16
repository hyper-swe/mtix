// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service_test

import (
	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/oplocal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestWakeable_Preconfirmation_NotRegisteredUntilExactLocalRegistration(t *testing.T) {
	f := newWakePermissionFixture(t)
	f.apply(t, oplocal.PermissionChange{AgentID: "future-fixture", Action: "allow"})
	ok, reason, err := f.svc.Wakeable(t.Context(), f.board, "future-fixture")
	require.NoError(t, err)
	assert.False(t, ok)
	assert.Equal(t, "not_registered", reason)
	f.register(t, "future-fixture")
	ok, _, err = f.svc.Wakeable(t.Context(), f.board, "future-fixture")
	require.NoError(t, err)
	assert.True(t, ok)
	rows, err := f.state.Confirmations(t.Context())
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, "future-fixture", rows[0].AgentID)
}

func TestWakeable_Preconfirmation_DenyWinsBeforeAndAfterRegistration(t *testing.T) {
	f := newWakePermissionFixture(t)
	f.apply(t, oplocal.PermissionChange{AgentID: "future-fixture", Action: "allow"})
	f.apply(t, oplocal.PermissionChange{AgentID: "future-fixture", Action: "deny"})
	for _, registered := range []bool{false, true} {
		if registered {
			f.register(t, "future-fixture")
		}
		ok, reason, err := f.svc.Wakeable(t.Context(), f.board, "future-fixture")
		require.NoError(t, err)
		assert.False(t, ok)
		assert.Equal(t, "denied", reason)
	}
}

func TestWakeable_Preconfirmation_HubUnknownAtConfirmation_StaysHeld(t *testing.T) {
	f := newWakePermissionFixture(t)
	known := f.board.Hub
	f.board.Hub = oplocal.HubIdentity{}
	_, err := f.store.MarkHubConnected(t.Context(), "config")
	require.NoError(t, err)
	f.apply(t, oplocal.PermissionChange{AgentID: "future-fixture", Action: "allow"})
	f.register(t, "future-fixture")
	f.board.Hub = known
	ok, reason, err := f.svc.Wakeable(t.Context(), f.board, "future-fixture")
	require.NoError(t, err)
	assert.False(t, ok, "preconfirmation without matching hub identity is insufficient even with default strict matching off")
	assert.Equal(t, "unconfirmed", reason)
}

func TestApplyBatch_Preconfirmation_InvalidID_RefusedWithoutChanges(t *testing.T) {
	f := newWakePermissionFixture(t)
	err := f.svc.ApplyBatch(t.Context(), f.board, oplocal.PermissionBatch{Mode: "file", Source: "fixture",
		Changes: []oplocal.PermissionChange{{AgentID: "future-fixture", Action: "allow"}, {AgentID: "\x00invalid", Action: "allow"}}})
	require.ErrorIs(t, err, model.ErrInvalidInput)
	rows, err := f.state.Confirmations(t.Context())
	require.NoError(t, err)
	assert.Empty(t, rows)
	grants, err := f.state.AgentGrants()
	require.NoError(t, err)
	assert.Empty(t, grants)
}
