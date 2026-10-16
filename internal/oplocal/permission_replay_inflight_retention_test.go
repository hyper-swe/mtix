// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0
package oplocal

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Exercise the durable writer with a nonpinned inflight receipt. The public
// transaction layer separately enforces one active pending batch.
func TestWritePermissionReplay_AtCapacity_PreservesInflightReceipts(t *testing.T) {
	s := alertFixture(t)
	rows := replayCapacityReceipts(t, s, maxPermissionReplays)
	for i := range rows {
		rows[i].Batch.Time = alertTime().Add(time.Duration(i) * time.Second)
		rows[i].BatchHash = permissionBatchHash(rows[i].Batch)
		rows[i].CompletedAt = rows[i].Batch.Time
	}
	rows[0].Completed = false
	rows[0].CompletedAt = time.Time{}
	require.NoError(t, s.WriteJSON("permissions-replays", rows))
	host, err := s.HostID()
	require.NoError(t, err)
	dir, err := s.directory(false)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, closeFile(dir)) })
	incoming, err := replayPrepare(t.Context(), replayIntent())
	require.NoError(t, err)
	incoming.ID = "33333333333333333333333333333333"
	incoming.IntentHash = permissionIntentHash(incoming, host)
	tx := permissionTransaction{ctx: t.Context(), dir: dir, host: host}
	require.NoError(t, s.writePermissionReplay(tx, incoming, false))
	after, err := readPermissionReplays(dir, host)
	require.NoError(t, err)
	require.Len(t, after, maxPermissionReplays)
	byID := map[string]permissionReplay{}
	for _, row := range after {
		byID[row.Batch.ID] = row
	}
	inflight, retained := byID[rows[0].Batch.ID]
	require.True(t, retained, "nonpinned inflight receipt must survive actual write")
	require.False(t, inflight.Completed)
	require.Contains(t, byID, incoming.ID)
	require.NotContains(t, byID, rows[1].Batch.ID, "oldest completed receipt is evicted")
	require.Contains(t, byID, rows[len(rows)-1].Batch.ID)
}
