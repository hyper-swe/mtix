// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0
package oplocal

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestPermissions_PreacceptPrepareFailuresPreserveDurableState(t *testing.T) {
	cases := map[string]func(context.Context, PermissionBatch) (PermissionBatch, error){
		"missing": nil,
		"sentinel": func(_ context.Context, b PermissionBatch) (PermissionBatch, error) {
			return b, errors.New("prepare sentinel")
		},
		"changed-intent": func(_ context.Context, b PermissionBatch) (PermissionBatch, error) {
			b.Source = "changed"
			b.Time = alertTime()
			return b, nil
		},
		"zero-time": func(_ context.Context, b PermissionBatch) (PermissionBatch, error) { return b, nil },
	}
	for name, prepare := range cases {
		t.Run(name, func(t *testing.T) {
			s := alertFixture(t)
			require.NoError(t, s.ApplyPermissions(t.Context(), permissionFixtureBatch(), nil))
			before := alertStateSnapshot(t, s)
			publishes := 0
			err := s.ApplyPermissionIntentScoped(t.Context(), replayIntent().BoardID, replayIntent(), prepare, func(context.Context, PermissionBatch) error { publishes++; return nil })
			require.Error(t, err)
			if name == "sentinel" {
				require.ErrorContains(t, err, "prepare sentinel")
			}
			require.Zero(t, publishes)
			require.Equal(t, before, alertStateSnapshot(t, s))
		})
	}
}
func TestPermissions_PreacceptScopeAndIntentHashNeverPrepare(t *testing.T) {
	s := alertFixture(t)
	require.NoError(t, s.ApplyPermissions(t.Context(), permissionFixtureBatch(), nil))
	before := alertStateSnapshot(t, s)
	calls := 0
	prepare := func(ctx context.Context, b PermissionBatch) (PermissionBatch, error) {
		calls++
		return replayPrepare(ctx, b)
	}
	intent := replayIntent()
	require.Error(t, s.ApplyPermissionIntentScoped(t.Context(), "019a5cc9-8000-7000-8000-000000000002", intent, prepare, nil))
	intent.IntentHash = strings.Repeat("0", 64)
	require.ErrorContains(t, s.ApplyPermissionIntent(t.Context(), intent, prepare, nil), "identity conflicts")
	require.Zero(t, calls)
	require.Equal(t, before, alertStateSnapshot(t, s))
}
func TestPermissions_PreacceptGrantCapacityThenRetryPreservesUnknownFields(t *testing.T) {
	s := alertFixture(t)
	require.NoError(t, s.ApplyPermissions(t.Context(), permissionFixtureBatch(), nil))
	padding := strings.Repeat("p", maxRecordSize-120)
	require.NoError(t, s.WriteJSON("grants", map[string]any{"future": padding}))
	before := alertStateSnapshot(t, s)
	require.ErrorContains(t, s.ApplyPermissionIntent(t.Context(), replayIntent(), replayPrepare, nil), "grant projection exceeds limit")
	require.Equal(t, before, alertStateSnapshot(t, s))
	require.NoError(t, s.WriteJSON("grants", map[string]any{"future": "retained"}))
	require.NoError(t, s.ApplyPermissionIntent(t.Context(), replayIntent(), replayPrepare, nil))
	var stored map[string]any
	_, err := s.ReadJSON("grants", &stored)
	require.NoError(t, err)
	require.Equal(t, "retained", stored["future"])
}
func TestPermissions_PreacceptInvalidCompletionClockRecoversWithoutReprepare(t *testing.T) {
	fixture := alertFixture(t)
	s := NewWithPermissionClock(fixture.env, func() time.Time { return time.Time{} })
	intent := replayIntent()
	prepares := 0
	prepare := func(ctx context.Context, b PermissionBatch) (PermissionBatch, error) {
		prepares++
		return replayPrepare(ctx, b)
	}
	require.ErrorContains(t, s.ApplyPermissionIntent(t.Context(), intent, prepare, nil), "completion time")
	p := generationPending(t, s)
	require.NotNil(t, p.Batch)
	_, available, err := s.PermissionReplayResult(t.Context(), intent.BoardID, intent.ID)
	require.NoError(t, err)
	require.False(t, available)
	reopened := NewWithPermissionClock(fixture.env, func() time.Time { return alertTime().Add(time.Hour) })
	require.NoError(t, reopened.ApplyPermissionIntent(t.Context(), intent, nil, nil))
	require.Equal(t, 1, prepares)
	result, available, err := reopened.PermissionReplayResult(t.Context(), intent.BoardID, intent.ID)
	require.NoError(t, err)
	require.True(t, available)
	require.Equal(t, *p.Batch, result)
	require.Equal(t, p.Generation, generationPending(t, reopened).Generation)
}
