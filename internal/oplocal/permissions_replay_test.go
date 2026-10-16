// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0
package oplocal

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func replayIntent() PermissionBatch {
	b := permissionFixtureBatch()
	b.ID = "11111111111111111111111111111111"
	b.Mode = "file"
	b.Time = time.Time{}
	return b
}
func replayPrepare(_ context.Context, b PermissionBatch) (PermissionBatch, error) {
	b.Time = alertTime()
	return b, nil
}
func TestPermissions_ReplayCompletedPreservesLaterDeny(t *testing.T) {
	s := alertFixture(t)
	intent := replayIntent()
	calls := 0
	prepare := func(ctx context.Context, b PermissionBatch) (PermissionBatch, error) {
		calls++
		return replayPrepare(ctx, b)
	}
	require.NoError(t, s.ApplyPermissionIntent(t.Context(), intent, prepare, nil))
	deny := intent
	deny.ID = "22222222222222222222222222222222"
	deny.Changes = []PermissionChange{{AgentID: "fixture-agent", Action: "deny"}}
	require.NoError(t, s.ApplyPermissionIntent(t.Context(), deny, prepare, nil))
	before := alertStateSnapshot(t, s)
	require.NoError(t, New(s.env).ApplyPermissionIntent(t.Context(), intent, nil, nil))
	require.Equal(t, 2, calls)
	require.Equal(t, before, alertStateSnapshot(t, s))
	grants, err := s.AgentGrants()
	require.NoError(t, err)
	require.True(t, grants[0].Denied)
	rows, err := s.Confirmations(t.Context())
	require.NoError(t, err)
	require.Len(t, rows, 2)
	usage, err := s.PermissionReplayUsage(t.Context())
	require.NoError(t, err)
	require.Equal(t, 2, usage.Count)
	require.Positive(t, usage.Bytes)
	require.Equal(t, 1024, usage.MaxCount)
	require.Equal(t, maxRecordSize, usage.MaxBytes)
}
func TestPermissions_ReplayChangedIntentBeforePendingRecovery(t *testing.T) {
	s := alertFixture(t)
	intent := replayIntent()
	s.alertStage = func(stage string) error {
		if stage == "after:permission-replay-accepted" {
			return errors.New("fault")
		}
		return nil
	}
	require.Error(t, s.ApplyPermissionIntent(t.Context(), intent, replayPrepare, nil))
	before := alertStateSnapshot(t, s)
	intent.Changes = []PermissionChange{{AgentID: "fixture-agent", Action: "deny"}}
	prepareCalls, publishCalls := 0, 0
	prepare := func(context.Context, PermissionBatch) (PermissionBatch, error) {
		prepareCalls++
		return PermissionBatch{}, nil
	}
	publish := func(context.Context, PermissionBatch) error { publishCalls++; return nil }
	require.ErrorContains(t, New(s.env).ApplyPermissionIntent(t.Context(), intent, prepare, publish), "permission batch identity conflicts")
	require.Zero(t, prepareCalls)
	require.Zero(t, publishCalls)
	require.Equal(t, before, alertStateSnapshot(t, s))
}
func TestPermissions_ReplayFaultRetainsPreparedMetadata(t *testing.T) {
	stages := []string{"before:permissions-pending.json", "after:permissions-pending.json", "before:permission-replay-accepted", "after:permission-replay-accepted", "before:confirmations.jsonl", "after:confirmations.jsonl", "before:grants.json", "after:grants.json", "before:final:confirmations.jsonl", "after:final:confirmations.jsonl", "before:permission-replay-completed", "after:permission-replay-completed", "before:permissions-clear", "after:permissions-clear"}
	for _, stage := range stages {
		t.Run(stage, func(t *testing.T) { testReplayFault(t, stage) })
	}
}
func testReplayFault(t *testing.T, stage string) {
	t.Helper()
	s := alertFixture(t)
	intent := replayIntent()
	fired := false
	prepared := 0
	published := map[string]bool{}
	s.alertStage = func(at string) error {
		if at == stage && !fired {
			fired = true
			return errors.New("fault")
		}
		return nil
	}
	prepare := func(_ context.Context, b PermissionBatch) (PermissionBatch, error) {
		prepared++
		b.Time = alertTime().Add(time.Duration(prepared) * time.Hour)
		b.NodeID = fmt.Sprintf("node-%d", prepared)
		b.Changes = append([]PermissionChange{}, b.Changes...)
		b.Changes[0].PendingRegistration = true
		return b, nil
	}
	publish := func(_ context.Context, b PermissionBatch) error { published[b.ID] = true; return nil }
	require.Error(t, s.ApplyPermissionIntent(t.Context(), intent, prepare, publish))
	require.True(t, fired)
	require.NoError(t, New(s.env).ApplyPermissionIntent(t.Context(), intent, prepare, publish))
	wantPrepared := 1
	if stage == "before:permissions-pending.json" {
		wantPrepared = 2
	}
	require.Equal(t, wantPrepared, prepared)
	require.Len(t, published, 1)
	rows, err := s.Confirmations(t.Context())
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, alertTime().Add(time.Duration(wantPrepared)*time.Hour), rows[0].Time)
	require.True(t, rows[0].PendingRegistration)
	require.False(t, rows[0].Pending)
	require.Equal(t, "published", rows[0].BoardLine)
	grants, err := s.AgentGrants()
	require.NoError(t, err)
	require.True(t, grants[0].PendingRegistration)
	require.Equal(t, rows[0].Time, grants[0].ConfirmedAt)
	usage, err := s.PermissionReplayUsage(t.Context())
	require.NoError(t, err)
	require.Equal(t, 1, usage.Count)
}
func TestPermissions_ReplayConcurrentPrepareOnce(t *testing.T) {
	s := alertFixture(t)
	var wg sync.WaitGroup
	calls := 0
	results := make(chan error, 16)
	prepare := func(ctx context.Context, b PermissionBatch) (PermissionBatch, error) {
		calls++
		return replayPrepare(ctx, b)
	}
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results <- New(s.env).ApplyPermissionIntent(t.Context(), replayIntent(), prepare, nil)
		}()
	}
	wg.Wait()
	close(results)
	for err := range results {
		require.NoError(t, err)
	}
	require.Equal(t, 1, calls)
	rows, err := s.Confirmations(t.Context())
	require.NoError(t, err)
	require.Len(t, rows, 1)
}
