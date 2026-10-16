// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// An injected clock outside the serializable timestamp range must never leave
// an accepted permission transaction with no readable audit record.
package oplocal

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestPermissions_UnserializableCanonicalTimeRefusesBeforeDurableAcceptance(t *testing.T) {
	for _, mode := range []string{"legacy", "file_intent"} {
		t.Run(mode, func(t *testing.T) {
			s := alertFixture(t)
			require.NoError(t, s.ApplyPermissions(t.Context(), permissionFixtureBatch(), nil))
			before := alertStateSnapshot(t, s)
			invalid := time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)
			if mode == "legacy" {
				batch := permissionFixtureBatch()
				batch.Time = invalid
				require.Error(t, s.ApplyPermissions(t.Context(), batch, nil))
			} else {
				prepare := func(_ context.Context, batch PermissionBatch) (PermissionBatch, error) {
					batch.Time = invalid
					return batch, nil
				}
				require.Error(t, s.ApplyPermissionIntent(t.Context(), replayIntent(), prepare, nil))
			}
			require.Equal(t, before, alertStateSnapshot(t, s))
		})
	}
}
