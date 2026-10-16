// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0
package oplocal

import (
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func replayCapacityReceipts(t *testing.T, s *State, count int) []permissionReplay {
	t.Helper()
	require.NoError(t, s.withPermissions(t.Context(), func(*os.File, string) error { return nil }))
	host, err := s.HostID()
	require.NoError(t, err)
	receipts := []permissionReplay{}
	for i := range count {
		b := replayIntent()
		b.ID = fmt.Sprintf("%032x", i+100)
		b.Time = alertTime()
		b.IntentHash = permissionIntentHash(b, host)
		receipts = append(receipts, permissionReplay{Batch: b, BatchHash: permissionBatchHash(b), Completed: true})
	}
	return receipts
}
func TestPermissions_ReplayCountCapacityAcceptsNewRetainsBound(t *testing.T) {
	s := alertFixture(t)
	receipts := replayCapacityReceipts(t, s, maxPermissionReplays)
	require.NoError(t, s.WriteJSON("permissions-replays", receipts))
	require.NoError(t, s.ApplyPermissionIntent(t.Context(), replayIntent(), replayPrepare, nil))
	old := receipts[len(receipts)-1].Batch
	old.IntentHash = ""
	old.Time = old.Time.AddDate(1, 0, 0)
	before := alertStateSnapshot(t, s)
	require.NoError(t, s.ApplyPermissionIntent(t.Context(), old, nil, nil))
	require.Equal(t, before, alertStateSnapshot(t, s))
	usage, err := s.PermissionReplayUsage(t.Context())
	require.NoError(t, err)
	require.Equal(t, maxPermissionReplays, usage.Count)
}
func TestPermissions_ReplayByteCapacityAcceptsNewRetainsBound(t *testing.T) {
	s := alertFixture(t)
	receipts := replayCapacityReceipts(t, s, 1)
	host, err := s.HostID()
	require.NoError(t, err)
	b := receipts[0].Batch
	b.Changes = []PermissionChange{}
	for i := range 1024 {
		b.Changes = append(b.Changes, PermissionChange{AgentID: fmt.Sprintf("agent-%04d-%s", i, strings.Repeat("x", 48)), Action: "allow"})
	}
	receipts = []permissionReplay{}
	for i := 0; ; i++ {
		b.ID = fmt.Sprintf("%032x", i+100)
		b.IntentHash = permissionIntentHash(b, host)
		next := append(append([]permissionReplay{}, receipts...), permissionReplay{Batch: b, BatchHash: permissionBatchHash(b), Completed: true})
		if _, err := encodePermissionReplays(next, host); err != nil {
			break
		}
		receipts = next
	}
	require.NotEmpty(t, receipts)
	require.Less(t, len(receipts), 1024)
	require.NoError(t, s.WriteJSON("permissions-replays", receipts))
	intent := b
	intent.ID = "11111111111111111111111111111111"
	intent.IntentHash = ""
	intent.Time = replayIntent().Time
	require.NoError(t, s.ApplyPermissionIntent(t.Context(), intent, replayPrepare, nil))
	usage, err := s.PermissionReplayUsage(t.Context())
	require.NoError(t, err)
	require.LessOrEqual(t, usage.Bytes, maxRecordSize)
	require.LessOrEqual(t, usage.Count, maxPermissionReplays)
	result, available, err := s.PermissionReplayResult(t.Context(), intent.BoardID, intent.ID)
	require.NoError(t, err)
	require.True(t, available)
	require.Equal(t, intent.ID, result.ID)

}
func TestPermissions_ReplayUsageMissingDoesNotCreate(t *testing.T) {
	s := alertFixture(t)
	usage, err := s.PermissionReplayUsage(t.Context())
	require.NoError(t, err)
	require.Zero(t, usage.Count)
	require.Zero(t, usage.Bytes)
	require.Equal(t, 1024, usage.MaxCount)
}
