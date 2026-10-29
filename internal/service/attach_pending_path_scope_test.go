//go:build !windows

// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service_test

import (
	"encoding/json"
	"testing"

	"github.com/hyper-swe/mtix/internal/store/sqlite"
	"github.com/stretchr/testify/require"
)

func TestAttachPending_ForgedAlignedOtherPath_DroppedWithAlert(t *testing.T) {
	for _, kind := range []string{"lane", "list"} {
		t.Run(kind, func(t *testing.T) {
			f, s, loc := identityFixture(t)
			other := t.TempDir()
			payload, err := json.Marshal(map[string]string{"agent_id": "forged", "harness": "codex", "lane_path": other})
			require.NoError(t, err)
			row := sqlite.AttachPending{Kind: kind, BoardKey: loc.BoardKey, Path: other, Payload: string(payload), QueuedAt: "now"}
			_, err = f.store.EnqueueAttachPending(t.Context(), row)
			require.NoError(t, err)
			result, err := f.svc.DrainAttachPending(t.Context(), f.svc.Routing.Board)
			require.NoError(t, err)
			require.Equal(t, 1, result.Dropped)
			require.Zero(t, result.Processed)
			alerts, err := s.RecentAlerts(t.Context(), 0)
			require.NoError(t, err)
			require.Len(t, alerts, 1)
			require.Equal(t, "attach.pending-rejected", alerts[0].Class)
			rows, err := f.store.ListAttachPending(t.Context())
			require.NoError(t, err)
			require.Empty(t, rows)
			assertRejectedRoutingUnchanged(t, s, loc)
		})
	}
}
