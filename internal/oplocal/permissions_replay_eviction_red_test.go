// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0
package oplocal

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Root7440 supersedes finite refusal. Existing refusal witnesses remain historical.
func TestPermissions_ReplayCapacityAcceptsNewAndEvictsCompleted(t *testing.T) {
	s := alertFixture(t)
	receipts := replayCapacityReceipts(t, s, maxPermissionReplays)
	for i := range receipts {
		receipts[i].Batch.Time = alertTime().Add(time.Duration(i) * time.Second)
		receipts[i].BatchHash = permissionBatchHash(receipts[i].Batch)
	}
	require.NoError(t, s.WriteJSON("permissions-replays", receipts))
	require.NoError(t, s.ApplyPermissionIntent(t.Context(), replayIntent(), replayPrepare, nil))
	host, err := s.HostID()
	require.NoError(t, err)
	dir, err := s.directory(false)
	require.NoError(t, err)
	after, err := readPermissionReplays(dir, host)
	require.NoError(t, err)
	require.NoError(t, closeFile(dir))
	require.Len(t, after, maxPermissionReplays)
	ids := map[string]bool{}
	for _, row := range after {
		ids[row.Batch.ID] = true
		require.True(t, row.Completed)
	}
	require.True(t, ids[replayIntent().ID])
	require.False(t, ids[receipts[0].Batch.ID])
	require.True(t, ids[receipts[len(receipts)-1].Batch.ID])
}
func TestPermissions_ReplayCapacityNeverEvictsInflight(t *testing.T) {
	s := alertFixture(t)
	intent := replayIntent()
	fired := false
	s.alertStage = func(stage string) error {
		if stage == "after:permission-replay-accepted" && !fired {
			fired = true
			return errors.New("leave inflight")
		}
		return nil
	}
	require.Error(t, s.ApplyPermissionIntent(t.Context(), intent, replayPrepare, nil))
	require.True(t, fired)
	host, err := s.HostID()
	require.NoError(t, err)
	dir, err := s.directory(false)
	require.NoError(t, err)
	rows, err := readPermissionReplays(dir, host)
	require.NoError(t, err)
	require.NoError(t, closeFile(dir))
	require.Len(t, rows, 1)
	require.False(t, rows[0].Completed)
	completed := replayCapacityReceipts(t, s, maxPermissionReplays-1)
	rows = append(rows, completed...)
	require.NoError(t, s.WriteJSON("permissions-replays", rows))
	usage, err := s.PermissionReplayUsage(t.Context())
	require.NoError(t, err)
	require.Equal(t, maxPermissionReplays, usage.Count)
	dir, err = s.directory(false)
	require.NoError(t, err)
	incoming, _ := replayPrepare(t.Context(), intent)
	incoming.ID = "33333333333333333333333333333333"
	incoming.IntentHash = permissionIntentHash(incoming, host)
	before := alertStateSnapshot(t, s)
	err = checkPermissionReplayCapacity(dir, host, incoming)
	require.NoError(t, err)
	require.Equal(t, before, alertStateSnapshot(t, s))
	after, err := readPermissionReplays(dir, host)
	require.NoError(t, err)
	require.NoError(t, closeFile(dir))
	require.Len(t, after, maxPermissionReplays)
	retained := false
	for _, row := range after {
		if row.Batch.ID == intent.ID {
			retained = true
			require.False(t, row.Completed)
		}
	}
	require.True(t, retained)
}
