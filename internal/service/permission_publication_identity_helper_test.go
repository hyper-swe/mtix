// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0
package service_test

import (
	"testing"

	"github.com/google/uuid"
	"github.com/hyper-swe/mtix/internal/oplocal"
	"github.com/stretchr/testify/require"
)

// Read the actual retained canonical batch; never infer event id from caller id.
func permissionAcceptedPublicationID(t *testing.T, f *wakePermissionFixture, caller string) string {
	t.Helper()
	batch, available, err := f.state.PermissionReplayResult(t.Context(), f.board.ID, caller)
	require.NoError(t, err)
	if !available {
		var pending struct {
			Batch *oplocal.PermissionBatch `json:"batch"`
		}
		reports, readErr := f.state.ReadJSON("permissions-pending", &pending)
		require.NoError(t, readErr)
		require.Empty(t, reports)
		require.NotNil(t, pending.Batch)
		batch = *pending.Batch
	}
	require.True(t, batch.ID == caller || batch.CallerID == caller)
	require.NotEqual(t, caller, batch.PublicationID)
	id, err := uuid.Parse(batch.PublicationID)
	require.NoError(t, err)
	require.Equal(t, uuid.Version(7), id.Version())
	require.Equal(t, id.String(), batch.PublicationID)
	return batch.PublicationID
}
