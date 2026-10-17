// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service_test

import (
	"testing"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Post-API integrity controls: these do not claim pre-field compiling RED.
func TestApplyBatch_CallerCannotSupplyDerivedAssociationOrOutcome(t *testing.T) {
	for _, field := range []string{"caller_id", "outcome"} {
		t.Run(field, func(t *testing.T) {
			f := newWakePermissionFixture(t)
			f.register(t, "first")
			batch := filePermissionReplayBatch()
			if field == "caller_id" {
				batch.CallerID = "fedcba9876543210fedcba9876543210"
			} else {
				batch.Changes[0].Outcome = "denied_requires_operator"
			}
			require.ErrorIs(t, f.svc.ApplyBatch(t.Context(), f.board, batch), model.ErrInvalidInput)
			rows, err := f.state.Confirmations(t.Context())
			require.NoError(t, err)
			assert.Empty(t, rows)
			grants, err := f.state.AgentGrants()
			require.NoError(t, err)
			assert.Empty(t, grants)
		})
	}
}

func TestPermissionReplayResult_CompleteDeniedOutcomeAndUnavailableAfterEviction(t *testing.T) {
	f := deniedFileFixture(t)
	batch := filePermissionReplayBatch()
	require.NoError(t, f.svc.ApplyBatch(t.Context(), f.board, batch))
	result, available, err := f.state.PermissionReplayResult(t.Context(), f.board.ID, batch.ID)
	require.NoError(t, err)
	require.True(t, available)
	require.Len(t, result.Changes, 1)
	assert.Equal(t, "denied_requires_operator", result.Changes[0].Outcome)
	assert.Equal(t, "first", result.Changes[0].AgentID)
	assert.Equal(t, "allow", result.Changes[0].Action)
	assert.Equal(t, batch.ID, result.ID, "first use preserves the accepted publication id")
	assert.Empty(t, result.CallerID)
	_, available, err = f.state.PermissionReplayResult(t.Context(), "22222222-2222-4222-8222-222222222222", batch.ID)
	require.NoError(t, err)
	assert.False(t, available)
	require.NoError(t, f.state.WriteJSON("permissions-replays", []any{}))
	_, available, err = f.state.PermissionReplayResult(t.Context(), f.board.ID, batch.ID)
	require.NoError(t, err)
	assert.False(t, available, "retained confirmation history cannot fabricate a partial result")
	require.NoError(t, f.state.WriteJSON("permissions-replays", map[string]any{"malformed": true}))
	_, available, err = f.state.PermissionReplayResult(t.Context(), f.board.ID, batch.ID)
	require.Error(t, err, "integrity errors cannot be presented as ordinary unavailable results")
	assert.False(t, available)
}
