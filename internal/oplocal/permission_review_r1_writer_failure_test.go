// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0
package oplocal

import (
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestReviewR1_ReplayWriterCompletionClockFailureDoesNotPartiallyCommit(t *testing.T) {
	s, _ := contractAcceptedPending(t, "after:permissions-pending.json")
	s.alertStage = nil
	p := generationPending(t, s)
	require.NoError(t, s.withPermissions(t.Context(), func(dir *os.File, host string) error {
		tx := permissionTransaction{ctx: t.Context(), dir: dir, host: host}
		s.permissionClock = func() time.Time { return time.Time{} }
		before := alertStateSnapshot(t, s)
		require.ErrorContains(t, s.writePermissionReplay(tx, *p.Batch, true), "completion time")
		require.Equal(t, before, alertStateSnapshot(t, s))
		require.NoError(t, s.writePermissionReplay(tx, *p.Batch, false))
		before = alertStateSnapshot(t, s)
		require.ErrorContains(t, s.writePermissionReplay(tx, *p.Batch, true), "completion time")
		require.Equal(t, before, alertStateSnapshot(t, s))
		s.permissionClock = alertTime
		require.NoError(t, s.writePermissionReplay(tx, *p.Batch, true))
		return nil
	}))
}

func TestReviewR1_ReplayWriterRefusesUnencodableCanonicalBatchWithoutWriting(t *testing.T) {
	s, _ := contractAcceptedPending(t, "after:permissions-pending.json")
	s.alertStage = nil
	b := *generationPending(t, s).Batch
	b.Time = time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)
	before := alertStateSnapshot(t, s)
	require.NoError(t, s.withPermissions(t.Context(), func(dir *os.File, host string) error {
		err := s.writePermissionReplay(permissionTransaction{ctx: t.Context(), dir: dir, host: host}, b, false)
		require.Error(t, err)
		return nil
	}))
	require.Equal(t, before, alertStateSnapshot(t, s))
}

// This is the storage writer's capacity contract, not a claim that the public
// host-global transaction API permits several simultaneously pending batches.
func TestReviewR1_ReplayWriterFullInflightLedgerRefusesWithoutEviction(t *testing.T) {
	s := alertFixture(t)
	require.NoError(t, s.withPermissions(t.Context(), func(dir *os.File, host string) error {
		rows := make([]permissionReplay, maxPermissionReplays)
		for i := range rows {
			b := replayIntent()
			b.Time = alertTime()
			b.ID = fmt.Sprintf("%032x", i+1)
			b.IntentHash = permissionIntentHash(b, host)
			rows[i] = permissionReplay{Batch: b, BatchHash: permissionBatchHash(b)}
		}
		require.NoError(t, s.WriteJSON("permissions-replays", rows))
		before := alertStateSnapshot(t, s)
		b := replayIntent()
		b.Time = alertTime()
		b.ID = fmt.Sprintf("%032x", maxPermissionReplays+1)
		b.IntentHash = permissionIntentHash(b, host)
		err := s.writePermissionReplay(permissionTransaction{ctx: t.Context(), dir: dir, host: host}, b, false)
		require.ErrorContains(t, err, "inflight receipts exceed capacity")
		require.Equal(t, before, alertStateSnapshot(t, s))
		return nil
	}))
}
