// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Reader cancellation and damaged retained input must not write a replacement
// snapshot or turn unreadable accepted work into an empty successful result.
package oplocal

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestPermissions_ReaderRevalidationRejectsPendingReadFailure(t *testing.T) {
	s := alertFixture(t)
	require.NoError(t, s.ApplyPermissions(t.Context(), permissionFixtureBatch(), nil))
	original, exists := contractReadFile(t, s, "permissions-pending.json")
	require.True(t, exists)
	before := alertStateSnapshot(t, s)
	dir, err := s.Path()
	require.NoError(t, err)
	fired := false
	s.permissionReadStage = func(string) {
		fired = true
		require.NoError(t, os.WriteFile(filepath.Join(dir, "permissions-pending.json"), []byte(strings.Repeat("x", maxRecordSize+1)), 0600))
	}
	_, err = s.Confirmations(t.Context())
	s.permissionReadStage = nil
	require.True(t, fired)
	require.Error(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "permissions-pending.json"), original, 0600))
	require.Equal(t, before, alertStateSnapshot(t, s))
	_, err = s.Confirmations(t.Context())
	require.NoError(t, err)
}

func TestPermissions_ReaderCanceledAfterRealWriterLeavesWriterBytesUnchanged(t *testing.T) {
	s := alertFixture(t)
	require.NoError(t, s.ApplyPermissions(t.Context(), permissionFixtureBatch(), nil))
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var written map[string]string
	s.permissionReadStage = func(string) {
		s.permissionReadStage = nil
		batch := permissionFixtureBatch()
		batch.ID = strings.Repeat("2", 32)
		batch.Changes[0].AgentID = "second-agent"
		require.NoError(t, New(s.env).ApplyPermissions(t.Context(), batch, nil))
		written = alertStateSnapshot(t, s)
		cancel()
	}
	_, err := s.Confirmations(ctx)
	require.ErrorIs(t, err, context.Canceled)
	require.NotNil(t, written)
	require.Equal(t, written, alertStateSnapshot(t, s))
	rows, err := s.Confirmations(t.Context())
	require.NoError(t, err)
	require.Len(t, rows, 2)
}

func TestPermissions_HeldPermissionLockHonorsDeadlineWithoutPreparing(t *testing.T) {
	s := alertFixture(t)
	require.NoError(t, s.ApplyPermissions(t.Context(), permissionFixtureBatch(), nil))
	before := alertStateSnapshot(t, s)
	require.NoError(t, s.withPermissions(t.Context(), func(*os.File, string) error {
		ctx, cancel := context.WithTimeout(t.Context(), 150*time.Millisecond)
		defer cancel()
		prepared := false
		prepare := func(ctx context.Context, b PermissionBatch) (PermissionBatch, error) {
			prepared = true
			return replayPrepare(ctx, b)
		}
		err := New(s.env).ApplyPermissionIntent(ctx, replayIntent(), prepare, nil)
		require.ErrorIs(t, err, context.DeadlineExceeded)
		require.False(t, prepared)
		return nil
	}))
	require.Equal(t, before, alertStateSnapshot(t, s))
}

func TestPermissions_CallerCannotSupplyDerivedRegistrationState(t *testing.T) {
	for _, outcome := range []bool{false, true} {
		t.Run(map[bool]string{false: "pending", true: "outcome"}[outcome], func(t *testing.T) {
			s := alertFixture(t)
			require.NoError(t, s.ApplyPermissions(t.Context(), permissionFixtureBatch(), nil))
			before := alertStateSnapshot(t, s)
			intent := replayIntent()
			intent.Changes[0].PendingRegistration = !outcome
			if outcome {
				intent.Changes[0].Outcome = "denied_requires_operator"
			}
			require.Error(t, s.ApplyPermissionIntent(t.Context(), intent, replayPrepare, nil))
			require.Equal(t, before, alertStateSnapshot(t, s))
		})
	}
}

func TestPermissions_InvalidRetainedConfirmationNeverAcceptsAnotherBatch(t *testing.T) {
	for _, kind := range []string{"hash", "publication_state"} {
		t.Run(kind, func(t *testing.T) {
			s := alertFixture(t)
			require.NoError(t, s.ApplyPermissions(t.Context(), permissionFixtureBatch(), nil))
			rows, err := s.Confirmations(t.Context())
			require.NoError(t, err)
			require.Len(t, rows, 1)
			if kind == "hash" {
				rows[0].BatchHash = "invalid"
			} else {
				rows[0].BoardLine = "invalid"
			}
			raw, err := json.Marshal(rows[0])
			require.NoError(t, err)
			writePermissionFixture(t, s, "confirmations.jsonl", append(raw, '\n'))
			before := alertStateSnapshot(t, s)
			_, err = s.Confirmations(t.Context())
			require.Error(t, err)
			require.Error(t, s.ApplyPermissionIntent(t.Context(), replayIntent(), replayPrepare, nil))
			require.Equal(t, before, alertStateSnapshot(t, s))
		})
	}
}
