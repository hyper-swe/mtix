// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0
package oplocal

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestPermissions_ReplayForeignBoardCannotRecoverPending(t *testing.T) {
	s := alertFixture(t)
	intent := replayIntent()
	s.alertStage = func(stage string) error {
		if stage == "after:permissions-pending.json" {
			return errors.New("pending")
		}
		return nil
	}
	require.Error(t, s.ApplyPermissionIntentScoped(t.Context(), intent.BoardID, intent, replayPrepare, nil))
	before := alertStateSnapshot(t, s)
	other := replayIntent()
	other.ID = "22222222222222222222222222222222"
	other.BoardID = "019a5cc9-8000-7000-8000-000000000002"
	calls := 0
	prepare := func(context.Context, PermissionBatch) (PermissionBatch, error) {
		calls++
		return PermissionBatch{}, nil
	}
	publish := func(context.Context, PermissionBatch) error { calls++; return nil }
	require.ErrorContains(t, New(s.env).ApplyPermissionIntentScoped(t.Context(), other.BoardID, other, prepare, publish), "another board")
	operator := other
	operator.Time = alertTime()
	require.ErrorContains(t, New(s.env).ApplyPermissionsScoped(t.Context(), other.BoardID, operator, publish), "another board")
	require.Zero(t, calls)
	require.Equal(t, before, alertStateSnapshot(t, s))
	require.NoError(t, New(s.env).ApplyPermissionIntentScoped(t.Context(), intent.BoardID, intent, nil, nil))
}
func TestPermissions_ReplayFileAllowCannotOverrideCurrentDeny(t *testing.T) {
	s := alertFixture(t)
	deny := permissionFixtureBatch()
	deny.Changes[0].Action = "deny"
	require.NoError(t, s.ApplyPermissions(t.Context(), deny, nil))
	intent := replayIntent()
	require.NoError(t, s.ApplyPermissionIntentScoped(t.Context(), intent.BoardID, intent, replayPrepare, nil))
	grants, err := s.AgentGrants()
	require.NoError(t, err)
	require.True(t, grants[0].Denied)
	result, ok, err := s.PermissionReplayResult(t.Context(), intent.BoardID, intent.ID)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, "denied_requires_operator", result.Changes[0].Outcome)
	rows, err := s.Confirmations(t.Context())
	require.NoError(t, err)
	require.Equal(t, "denied_requires_operator", rows[len(rows)-1].Outcome)
	fileNoID := permissionFixtureBatch()
	fileNoID.Mode = "file"
	require.NoError(t, s.ApplyPermissionsScoped(t.Context(), fileNoID.BoardID, fileNoID, nil))
	grants, err = s.AgentGrants()
	require.NoError(t, err)
	require.True(t, grants[0].Denied)
	operator := permissionFixtureBatch()
	require.NoError(t, s.ApplyPermissionsScoped(t.Context(), operator.BoardID, operator, nil))
	grants, err = s.AgentGrants()
	require.NoError(t, err)
	require.False(t, grants[0].Denied)
}
func TestPermissions_ReplayEvictedCallerGetsFreshOccurrenceAndCompleteResult(t *testing.T) {
	s := alertFixture(t)
	intent := replayIntent()
	require.NoError(t, s.ApplyPermissionIntent(t.Context(), intent, replayPrepare, nil))
	require.NoError(t, s.WriteJSON("permissions-replays", []permissionReplay{}))
	_, ok, err := s.PermissionReplayResult(t.Context(), intent.BoardID, intent.ID)
	require.NoError(t, err)
	require.False(t, ok)
	preparedID := ""
	prepare := func(ctx context.Context, b PermissionBatch) (PermissionBatch, error) {
		preparedID = b.ID
		return replayPrepare(ctx, b)
	}
	require.NoError(t, s.ApplyPermissionIntent(t.Context(), intent, prepare, nil))
	require.NotEqual(t, intent.ID, preparedID)
	result, ok, err := s.PermissionReplayResult(t.Context(), intent.BoardID, intent.ID)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, preparedID, result.ID)
	require.Equal(t, intent.ID, result.CallerID)
	rows, err := s.Confirmations(t.Context())
	require.NoError(t, err)
	require.Len(t, rows, 2)
	require.Equal(t, intent.ID, rows[1].CallerID)
	before := alertStateSnapshot(t, s)
	require.NoError(t, s.ApplyPermissionIntent(t.Context(), intent, nil, nil))
	require.Equal(t, before, alertStateSnapshot(t, s))
}
func TestPermissions_ReplayCompletionClockImmutableAndInflightResultUnavailable(t *testing.T) {
	fixture := alertFixture(t)
	now := alertTime()
	s := NewWithPermissionClock(fixture.env, func() time.Time { return now })
	intent := replayIntent()
	s.alertStage = func(stage string) error {
		if stage == "after:permission-replay-accepted" {
			return errors.New("inflight")
		}
		return nil
	}
	require.Error(t, s.ApplyPermissionIntent(t.Context(), intent, replayPrepare, nil))
	_, ok, err := s.PermissionReplayResult(t.Context(), intent.BoardID, intent.ID)
	require.NoError(t, err)
	require.False(t, ok)
	s.alertStage = nil
	require.NoError(t, s.ApplyPermissionIntent(t.Context(), intent, nil, nil))
	before := alertStateSnapshot(t, s)
	now = now.Add(time.Hour)
	require.NoError(t, s.ApplyPermissionIntent(t.Context(), intent, nil, nil))
	require.Equal(t, before, alertStateSnapshot(t, s))
	host, err := s.HostID()
	require.NoError(t, err)
	dir, err := s.directory(false)
	require.NoError(t, err)
	rows, err := readPermissionReplays(dir, host)
	require.NoError(t, err)
	require.NoError(t, closeFile(dir))
	require.Equal(t, alertTime(), rows[0].CompletedAt)
}
func TestPermissions_ReplayDerivedCallerInputsRejected(t *testing.T) {
	s := alertFixture(t)
	b := replayIntent()
	b.CallerID = "22222222222222222222222222222222"
	require.Error(t, s.ApplyPermissionIntent(t.Context(), b, replayPrepare, nil))
	b.Time = alertTime()
	require.Error(t, s.ApplyPermissions(t.Context(), b, nil))
	b.CallerID = ""
	b.Changes[0].Outcome = "denied_requires_operator"
	require.Error(t, s.ApplyPermissionIntent(t.Context(), b, replayPrepare, nil))
	require.Error(t, s.ApplyPermissions(t.Context(), b, nil))
}

func TestPermissions_ReplayEvictionUsesActualCompletionTime(t *testing.T) {
	fixture := alertFixture(t)
	now := alertTime()
	s := NewWithPermissionClock(fixture.env, func() time.Time { return now })
	ids := []string{"11111111111111111111111111111111", "22222222222222222222222222222222", "33333333333333333333333333333333"}
	offsets := []time.Duration{3 * time.Hour, time.Hour, 2 * time.Hour}
	for i, id := range ids {
		now = alertTime().Add(offsets[i])
		intent := replayIntent()
		intent.ID = id
		require.NoError(t, s.ApplyPermissionIntent(t.Context(), intent, replayPrepare, nil))
	}
	host, err := s.HostID()
	require.NoError(t, err)
	dir, err := s.directory(false)
	require.NoError(t, err)
	rows, err := readPermissionReplays(dir, host)
	require.NoError(t, err)
	require.NoError(t, closeFile(dir))
	fillers := replayCapacityReceipts(t, s, maxPermissionReplays-len(rows))
	for i := range fillers {
		fillers[i].CompletedAt = alertTime().Add(100 * time.Hour)
	}
	rows = append(rows, fillers...)
	require.NoError(t, s.WriteJSON("permissions-replays", rows))
	now = alertTime().Add(4 * time.Hour)
	incoming := replayIntent()
	incoming.ID = "44444444444444444444444444444444"
	require.NoError(t, s.ApplyPermissionIntent(t.Context(), incoming, replayPrepare, nil))
	_, available, err := s.PermissionReplayResult(t.Context(), incoming.BoardID, ids[1])
	require.NoError(t, err)
	require.False(t, available)
	_, available, err = s.PermissionReplayResult(t.Context(), incoming.BoardID, ids[0])
	require.NoError(t, err)
	require.True(t, available)
	_, available, err = s.PermissionReplayResult(t.Context(), incoming.BoardID, incoming.ID)
	require.NoError(t, err)
	require.True(t, available)
}

func TestPermissions_ReplayPrepareCannotInjectOutcome(t *testing.T) {
	s := alertFixture(t)
	intent := replayIntent()
	prepare := func(ctx context.Context, b PermissionBatch) (PermissionBatch, error) {
		b, err := replayPrepare(ctx, b)
		b.Changes = append([]PermissionChange{}, b.Changes...)
		b.Changes[0].Outcome = "denied_requires_operator"
		return b, err
	}
	require.ErrorContains(t, s.ApplyPermissionIntent(t.Context(), intent, prepare, nil), "derived outcome")
	grants, err := s.AgentGrants()
	require.NoError(t, err)
	require.Empty(t, grants)
	rows, err := s.Confirmations(t.Context())
	require.NoError(t, err)
	require.Empty(t, rows)
	usage, err := s.PermissionReplayUsage(t.Context())
	require.NoError(t, err)
	require.Zero(t, usage.Count)
}
