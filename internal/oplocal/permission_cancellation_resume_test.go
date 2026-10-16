// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0
package oplocal

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCancelPermissionBatch_CrashRetry_PreservesCommittedStateAndDenies(t *testing.T) {
	journal := permissionCancellationName(replayIntent().ID)
	for _, stage := range []string{"before:" + journal, "after:" + journal, "after:grants.json", "after:cancel:confirmations.jsonl", "after:permission-replay-cancelled", "after:permissions-clear"} {
		t.Run(stage, func(t *testing.T) { reviewR1CancellationCrash(t, stage) })
	}
}

func reviewR1CancellationCrash(t *testing.T, stage string) {
	t.Helper()
	s := alertFixture(t)
	require.NoError(t, s.ApplyPermissions(t.Context(), permissionFixtureBatch(), nil))
	intent := replayIntent()
	intent.Mode = "yes"
	intent.Changes = []PermissionChange{{AgentID: "fixture-agent", Action: "allow"}, {AgentID: "new", Action: "allow"}, {AgentID: "held", Action: "deny"}}
	s.alertStage = func(at string) error {
		if at == "after:grants.json" {
			return errors.New("accepted pending")
		}
		return nil
	}
	require.Error(t, s.ApplyPermissionIntent(t.Context(), intent, replayPrepare, nil))
	fired := false
	s.alertStage = func(at string) error {
		if at == stage && !fired {
			fired = true
			return errors.New("cancellation fault")
		}
		return nil
	}
	require.Error(t, s.CancelPermissionBatch(t.Context(), intent.ID))
	require.True(t, fired)
	s.alertStage = nil
	pending, err := s.PendingPermissionBatches(t.Context())
	require.NoError(t, err)
	_, err = s.AgentGrants()
	require.NoError(t, err, "interrupted cancellation must remain readable for operator recovery")
	if len(pending) != 0 {
		require.NoError(t, s.CancelPermissionBatch(t.Context(), intent.ID))
	}
	grants, err := s.AgentGrants()
	require.NoError(t, err)
	require.Len(t, grants, 2)
	byAgent := map[string]AgentGrant{}
	for _, grant := range grants {
		byAgent[grant.AgentID] = grant
	}
	require.False(t, byAgent["fixture-agent"].Denied)
	require.Equal(t, permissionFixtureBatch().Source, byAgent["fixture-agent"].Source)
	require.True(t, byAgent["held"].Denied)
	require.NotContains(t, byAgent, "new")
	require.NoError(t, s.WriteJSON("permissions-replays", []permissionReplay{}))
	require.ErrorContains(t, New(s.env).ApplyPermissionIntent(t.Context(), intent, replayPrepare, nil), "cancelled or quarantined")
}

func TestApplyPermissions_EncodingError_PreservesGrants(t *testing.T) {
	s := alertFixture(t)
	require.NoError(t, s.ApplyPermissions(t.Context(), permissionFixtureBatch(), nil))
	before := alertStateSnapshot(t, s)
	batch := permissionFixtureBatch()
	batch.Time = time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)
	_, err := checkedPermissionBatchHash(batch)
	require.Error(t, err)
	_, err = checkedBatchConfirmations(batch, "fixture-host")
	require.Error(t, err)
	require.Error(t, s.ApplyPermissions(t.Context(), batch, nil))
	require.Equal(t, before, alertStateSnapshot(t, s))
}

func TestApplyPermissions_LegacyCompletedReceipt_PreservesCommittedGrant(t *testing.T) {
	s := alertFixture(t)
	s.alertStage = func(stage string) error {
		if stage == "before:permissions-clear" {
			return errors.New("cleanup unavailable")
		}
		return nil
	}
	intent := replayIntent()
	require.Error(t, s.ApplyPermissionIntent(t.Context(), intent, replayPrepare, nil))
	pending := generationPending(t, s)
	pending.BeforeGrants = nil
	pending.BeforeHash = ""
	require.NoError(t, s.WriteJSON("permissions-pending", pending))
	before := alertStateSnapshot(t, s)
	grants, err := s.AgentGrants()
	require.NoError(t, err)
	require.Len(t, grants, 1)
	require.False(t, grants[0].Quarantined, "completed receipt proves the grant committed before cleanup failed")
	holds, err := s.QuarantinedPermissionGrants(t.Context())
	require.NoError(t, err)
	require.Empty(t, holds)
	require.ErrorContains(t, s.CancelPermissionBatch(t.Context(), intent.ID), "already committed")
	require.Equal(t, before, alertStateSnapshot(t, s))
	s.alertStage = nil
	require.NoError(t, s.ApplyPermissionIntent(t.Context(), intent, nil, nil))
	after, err := s.AgentGrants()
	require.NoError(t, err)
	require.Equal(t, grants, after)
}

func reviewR1CompletedNoticePending(t *testing.T) (*State, PermissionBatch) {
	t.Helper()
	s := alertFixture(t)
	intent := replayIntent()
	s.alertStage = func(stage string) error {
		if stage == "before:permissions-clear" {
			return errors.New("cleanup unavailable")
		}
		return nil
	}
	prepare := func(ctx context.Context, b PermissionBatch) (PermissionBatch, error) {
		b, err := replayPrepare(ctx, b)
		b.NodeID = "TEST-1"
		return b, err
	}
	publish := func(context.Context, PermissionBatch) error { return nil }
	require.Error(t, s.ApplyPermissionIntentScoped(t.Context(), intent.BoardID, intent, prepare, publish))
	s.alertStage = nil
	return s, intent
}

func TestPermissionReplayResult_CleanupFailure_PreservesCompletedHistory(t *testing.T) {
	s, _ := reviewR1CompletedNoticePending(t)
	rows, err := s.Confirmations(t.Context())
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.False(t, rows[0].Pending, "committed publication must not be reported as awaiting publication")
	assert.Equal(t, "published", rows[0].BoardLine)
}

func TestApplyPermissions_CompletedReceiptCleanup_DoesNotBlockForeignBoardDeny(t *testing.T) {
	s, intent := reviewR1CompletedNoticePending(t)
	deny := permissionFixtureBatch()
	deny.BoardID = "22222222-2222-4222-8222-222222222222"
	deny.Changes[0].Action = "deny"
	require.NoError(t, s.ApplyPermissionsScoped(t.Context(), deny.BoardID, deny, nil))
	grants, err := s.AgentGrants()
	require.NoError(t, err)
	require.Len(t, grants, 2)
	byBoard := map[string]AgentGrant{}
	for _, grant := range grants {
		byBoard[grant.BoardID] = grant
	}
	assert.False(t, byBoard[intent.BoardID].Denied)
	assert.False(t, byBoard[intent.BoardID].Quarantined)
	assert.True(t, byBoard[deny.BoardID].Denied)
}
