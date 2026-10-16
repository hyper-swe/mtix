// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0
package oplocal

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// Retained completed history is synthetic; cancellation uses the actual public
// transaction and protected writer at the real byte limit.
func TestReviewR1_CancellationAtReplayByteLimitPreservesTerminalCaller(t *testing.T) {
	s, intent := contractAcceptedPending(t, "after:permission-replay-accepted")
	s.alertStage = nil
	host, err := s.HostID()
	require.NoError(t, err)
	var rows []permissionReplay
	reports, err := s.ReadJSON("permissions-replays", &rows)
	require.NoError(t, err)
	require.Empty(t, reports)
	require.Len(t, rows, 1)
	rows = reviewR1FullReplayLedger(t, host, rows)
	require.NoError(t, s.WriteJSON("permissions-replays", rows))
	require.NoError(t, s.CancelPermissionBatch(t.Context(), intent.ID))
	var after []permissionReplay
	_, err = s.ReadJSON("permissions-replays", &after)
	require.NoError(t, err)
	ids := map[string]permissionReplay{}
	for _, row := range after {
		ids[row.Batch.ID] = row
	}
	require.Contains(t, ids, intent.ID)
	require.True(t, ids[intent.ID].Completed)
	require.True(t, ids[intent.ID].Cancelled)
	require.NotContains(t, ids, rows[1].Batch.ID, "oldest completed receipt makes room for terminal metadata")
	require.ErrorContains(t, s.ApplyPermissionIntent(t.Context(), intent, nil, nil), "cancelled or quarantined")
}

func reviewR1FullReplayLedger(t *testing.T, host string, rows []permissionReplay) []permissionReplay {
	t.Helper()
	counter := 100
	for _, count := range []int{64, 1} {
		for {
			row := reviewR1CapacityReceipt(host, counter, count)
			counter++
			next := append(append([]permissionReplay{}, rows...), row)
			if _, err := encodePermissionReplays(next, host); err != nil {
				break
			}
			rows = next
		}
	}
	data, err := encodePermissionReplays(rows, host)
	require.NoError(t, err)
	remaining := maxRecordSize - len(data)
	for i := 1; i < len(rows) && remaining > 0; i++ {
		padding := min(255, remaining)
		rows[i].Batch.Source += strings.Repeat("p", padding)
		rows[i].Batch.IntentHash = permissionIntentHash(rows[i].Batch, host)
		rows[i].BatchHash = permissionBatchHash(rows[i].Batch)
		remaining -= padding
	}
	require.Zero(t, remaining)
	data, err = encodePermissionReplays(rows, host)
	require.NoError(t, err)
	require.Len(t, data, maxRecordSize)
	return rows
}

func reviewR1CapacityReceipt(host string, id, count int) permissionReplay {
	batch := replayIntent()
	batch.ID = fmt.Sprintf("%032x", id)
	batch.Time = alertTime()
	batch.Source = "p"
	batch.Changes = []PermissionChange{}
	for i := range count {
		batch.Changes = append(batch.Changes, PermissionChange{AgentID: fmt.Sprintf("agent-%04d-%s", i, strings.Repeat("x", 48)), Action: "allow"})
	}
	batch.IntentHash = permissionIntentHash(batch, host)
	return permissionReplay{Batch: batch, BatchHash: permissionBatchHash(batch), Completed: true, CompletedAt: alertTime()}
}
