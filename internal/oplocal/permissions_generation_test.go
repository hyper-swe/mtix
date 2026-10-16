// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0
package oplocal

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

func generationPending(t *testing.T, s *State) permissionPending {
	t.Helper()
	var p permissionPending
	reports, err := s.ReadJSON("permissions-pending", &p)
	require.NoError(t, err)
	require.Empty(t, reports)
	return p
}
func TestPermissions_GenerationFaultRecoveryRetainsAcceptedNonce(t *testing.T) {
	stages := []string{"before:permissions-pending.json", "after:permissions-pending.json", "before:grants.json", "after:grants.json", "before:permission-replay-completed", "after:permission-replay-completed", "before:permissions-clear", "after:permissions-clear"}
	for _, stage := range stages {
		t.Run(stage, func(t *testing.T) { testGenerationFault(t, stage) })
	}
}
func testGenerationFault(t *testing.T, stage string) {
	t.Helper()
	s := alertFixture(t)
	intent := replayIntent()
	fired := false
	s.alertStage = func(at string) error {
		if at == stage && !fired {
			fired = true
			return errors.New("generation fault")
		}
		return nil
	}
	require.Error(t, s.ApplyPermissionIntentScoped(t.Context(), intent.BoardID, intent, replayPrepare, nil))
	require.True(t, fired)
	accepted := generationPending(t, s)
	if stage != "before:permissions-pending.json" {
		require.True(t, permissionID(accepted.Generation))
		require.NotEqual(t, intent.ID, accepted.Generation)
	}
	require.NoError(t, New(s.env).ApplyPermissionIntentScoped(t.Context(), intent.BoardID, intent, replayPrepare, nil))
	completed := generationPending(t, s)
	require.Nil(t, completed.Batch)
	require.Equal(t, intent.ID, completed.CompletedID)
	require.True(t, permissionID(completed.Generation))
	if accepted.Generation != "" {
		require.Equal(t, accepted.Generation, completed.Generation)
	}
	before := alertStateSnapshot(t, s)
	require.NoError(t, New(s.env).ApplyPermissionIntentScoped(t.Context(), intent.BoardID, intent, nil, nil))
	require.Equal(t, before, alertStateSnapshot(t, s))
}
func TestPermissions_GenerationLegacyInflightUpgradeGuardedAndDurable(t *testing.T) {
	s := alertFixture(t)
	intent := replayIntent()
	s.alertStage = func(stage string) error {
		if stage == "after:permission-replay-accepted" {
			return errors.New("accepted")
		}
		return nil
	}
	require.Error(t, s.ApplyPermissionIntentScoped(t.Context(), intent.BoardID, intent, replayPrepare, nil))
	p := generationPending(t, s)
	p.Generation = ""
	require.NoError(t, s.WriteJSON("permissions-pending", p))
	before := alertStateSnapshot(t, s)
	other := intent
	other.ID = "22222222222222222222222222222222"
	other.BoardID = "019a5cc9-8000-7000-8000-000000000002"
	require.ErrorContains(t, New(s.env).ApplyPermissionIntentScoped(t.Context(), other.BoardID, other, replayPrepare, nil), "another board")
	require.Equal(t, before, alertStateSnapshot(t, s))
	restarted := New(s.env)
	fired := false
	restarted.alertStage = func(stage string) error {
		if stage == "after:permissions-pending.json" && !fired {
			fired = true
			return errors.New("upgraded")
		}
		return nil
	}
	require.Error(t, restarted.ApplyPermissionIntentScoped(t.Context(), intent.BoardID, intent, nil, nil))
	require.True(t, fired)
	upgraded := generationPending(t, s)
	require.True(t, permissionID(upgraded.Generation))
	require.Equal(t, p.Batch, upgraded.Batch)
	receiptsBefore := generationReplayRows(t, s)
	require.NoError(t, New(s.env).ApplyPermissionIntentScoped(t.Context(), intent.BoardID, intent, nil, nil))
	completed := generationPending(t, s)
	require.Equal(t, upgraded.Generation, completed.Generation)
	receiptsAfter := generationReplayRows(t, s)
	require.Equal(t, receiptsBefore[0].Batch, receiptsAfter[0].Batch)
	require.Equal(t, receiptsBefore[0].BatchHash, receiptsAfter[0].BatchHash)
	require.True(t, receiptsAfter[0].Completed)
}
func generationReplayRows(t *testing.T, s *State) []permissionReplay {
	t.Helper()
	var rows []permissionReplay
	_, err := s.ReadJSON("permissions-replays", &rows)
	require.NoError(t, err)
	return rows
}
func TestPermissions_GenerationLegacyCompletedMarkerNoEagerMutation(t *testing.T) {
	s := alertFixture(t)
	intent := replayIntent()
	require.NoError(t, s.ApplyPermissionIntent(t.Context(), intent, replayPrepare, nil))
	p := generationPending(t, s)
	p.Generation = ""
	require.NoError(t, s.WriteJSON("permissions-pending", p))
	before := alertStateSnapshot(t, s)
	_, err := s.AgentGrants()
	require.NoError(t, err)
	_, err = s.Confirmations(t.Context())
	require.NoError(t, err)
	require.NoError(t, s.ApplyPermissionIntent(t.Context(), intent, nil, nil))
	require.Equal(t, before, alertStateSnapshot(t, s))
	bad := intent
	bad.ID = "22222222222222222222222222222222"
	bad.Changes[0].Outcome = "denied_requires_operator"
	require.Error(t, s.ApplyPermissionIntent(t.Context(), bad, replayPrepare, nil))
	require.Equal(t, before, alertStateSnapshot(t, s))
	next := replayIntent()
	next.ID = "22222222222222222222222222222222"
	require.NoError(t, s.ApplyPermissionIntent(t.Context(), next, replayPrepare, nil))
	after := generationPending(t, s)
	require.True(t, permissionID(after.Generation))
	require.NotEqual(t, after.CompletedID, after.Generation)
	oldResult, ok, err := s.PermissionReplayResult(t.Context(), intent.BoardID, intent.ID)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, intent.ID, oldResult.ID)
}
func TestPermissions_GenerationIndependentAcrossRepeatedCallerOccurrence(t *testing.T) {
	s := alertFixture(t)
	intent := replayIntent()
	generations := map[string]bool{}
	actions := []string{"allow", "deny", "deny"}
	for _, action := range actions {
		require.NoError(t, s.WriteJSON("permissions-replays", []permissionReplay{}))
		for i := range 3 {
			seedConfirmationSegment(t, s, i, []byte{})
		}
		intent.Changes = []PermissionChange{{AgentID: "fixture-agent", Action: action}}
		require.NoError(t, s.ApplyPermissionIntent(t.Context(), intent, replayPrepare, nil))
		p := generationPending(t, s)
		require.Equal(t, intent.ID, p.CompletedID)
		require.False(t, generations[p.Generation])
		require.NotEqual(t, intent.ID, p.Generation)
		generations[p.Generation] = true
	}
	require.Len(t, generations, 3)
}
func TestPermissions_GenerationMalformedFailsClosed(t *testing.T) {
	s := alertFixture(t)
	intent := replayIntent()
	require.NoError(t, s.ApplyPermissionIntent(t.Context(), intent, replayPrepare, nil))
	p := generationPending(t, s)
	p.Generation = "bad"
	require.NoError(t, s.WriteJSON("permissions-pending", p))
	before := alertStateSnapshot(t, s)
	_, err := s.AgentGrants()
	require.Error(t, err)
	require.Error(t, s.ApplyPermissionIntent(t.Context(), intent, nil, nil))
	require.Equal(t, before, alertStateSnapshot(t, s))
}
