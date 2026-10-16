// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0
package oplocal

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Validate the upgrade boundary independently of its caller's preliminary
// checks. Refusal must leave the accepted legacy generation byte exact.
func TestValidateLegacyGenerationUpgrade_InvalidContextScopeOrPreimages_Refuses(t *testing.T) {
	for _, kind := range []string{"context", "metadata", "publisher", "replay", "log", "grants", "scope"} {
		t.Run(kind, func(t *testing.T) { reviewR1LegacyUpgradeRefusal(t, kind) })
	}
}

func reviewR1LegacyUpgradeRefusal(t *testing.T, kind string) {
	t.Helper()
	s, _ := contractAcceptedPending(t, "after:permissions-pending.json")
	s.alertStage = nil
	p := generationPending(t, s)
	p.Generation = ""
	require.NoError(t, s.WriteJSON("permissions-pending", p))
	ctx := t.Context()
	if kind == "context" {
		cancelled, cancel := context.WithCancel(ctx)
		cancel()
		ctx = cancelled
	}
	switch kind {
	case "metadata":
		p.NoticeOutcome = "unrecognized"
	case "publisher":
		p.Batch.NodeID = "TEST-1"
	case "replay":
		writePermissionFixture(t, s, "permissions-replays.json", []byte(`{broken`))
	case "log":
		writePermissionFixture(t, s, "confirmations.jsonl", []byte(`{broken`))
	case "grants":
		writePermissionFixture(t, s, "grants.json", []byte(`{broken`))
	}
	before := alertStateSnapshot(t, s)
	require.NoError(t, s.withPermissions(t.Context(), func(dir *os.File, host string) error {
		tx := permissionTransaction{ctx: ctx, dir: dir, host: host, boardID: p.Batch.BoardID}
		if kind == "scope" {
			tx.boardID = "22222222-2222-4222-8222-222222222222"
			_, err := s.ensurePermissionGeneration(tx, p)
			require.ErrorContains(t, err, "another board")
		} else {
			require.Error(t, validateLegacyGenerationUpgrade(tx, p))
		}
		return nil
	}))
	require.Equal(t, before, alertStateSnapshot(t, s))
}

func TestPreparePermissionFinish_WrongBoardOrCancelledContext_DoesNotUpgrade(t *testing.T) {
	s, _ := contractAcceptedPending(t, "after:permissions-pending.json")
	p := generationPending(t, s)
	before := alertStateSnapshot(t, s)
	require.NoError(t, s.withPermissions(t.Context(), func(dir *os.File, host string) error {
		tx := permissionTransaction{ctx: t.Context(), dir: dir, host: host, boardID: "22222222-2222-4222-8222-222222222222"}
		_, err := s.preparePermissionFinish(tx, p)
		require.ErrorContains(t, err, "another board")
		tx.boardID = p.Batch.BoardID
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		tx.ctx = ctx
		_, err = s.preparePermissionFinish(tx, p)
		require.ErrorIs(t, err, context.Canceled)
		return nil
	}))
	require.Equal(t, before, alertStateSnapshot(t, s))
}

func TestApplyPermissionIntent_LegacyPreProjectionHold_RequiresNoExistingGrant(t *testing.T) {
	s, intent := contractAcceptedPending(t, "after:permissions-pending.json")
	s.alertStage = nil
	p := generationPending(t, s)
	p.BeforeGrants, p.BeforeHash = nil, ""
	require.NoError(t, s.WriteJSON("permissions-pending", p))
	before := alertStateSnapshot(t, s)
	grants, err := s.AgentGrants()
	require.NoError(t, err)
	require.Len(t, grants, 1)
	require.True(t, grants[0].Quarantined)
	require.False(t, grants[0].Denied)
	require.Equal(t, intent.ID, grants[0].QuarantineBatchID)
	require.Equal(t, before, alertStateSnapshot(t, s), "reading a synthetic legacy hold never projects it onto disk")
	require.NoError(t, s.CancelPermissionBatch(t.Context(), intent.ID))
	after, err := s.AgentGrants()
	require.NoError(t, err)
	require.Equal(t, grants, after)
}

func TestApplyPermissionIntent_LegacyPendingAllow_PreservesExistingDeny(t *testing.T) {
	s, intent := contractAcceptedPending(t, "after:permissions-pending.json")
	s.alertStage = nil
	p := generationPending(t, s)
	p.BeforeGrants, p.BeforeHash = nil, ""
	require.NoError(t, s.WriteJSON("permissions-pending", p))
	host, err := s.HostID()
	require.NoError(t, err)
	deny := AgentGrant{HostID: host, BoardID: intent.BoardID, AgentID: intent.Changes[0].AgentID, Denied: true, Mode: "yes", Source: "existing operator deny", ConfirmedAt: alertTime()}
	require.NoError(t, s.WriteJSON("grants", map[string]any{"agent_grants": []AgentGrant{deny}}))
	grants, err := s.AgentGrants()
	require.NoError(t, err)
	require.Equal(t, []AgentGrant{deny}, grants)
	require.NoError(t, s.CancelPermissionBatch(t.Context(), intent.ID))
	grants, err = s.AgentGrants()
	require.NoError(t, err)
	require.Equal(t, []AgentGrant{deny}, grants)
}

func TestValidatePermissionReplays_ImpossibleTerminalState_Refuses(t *testing.T) {
	s := alertFixture(t)
	intent := replayIntent()
	require.NoError(t, s.ApplyPermissionIntent(t.Context(), intent, replayPrepare, nil))
	host, err := s.HostID()
	require.NoError(t, err)
	baseline := generationReplayRows(t, s)[0]
	for _, kind := range []string{"cancelled-inflight", "invalid-completion", "bad-intent"} {
		t.Run(kind, func(t *testing.T) {
			row := baseline
			switch kind {
			case "cancelled-inflight":
				row.Cancelled, row.Completed, row.CompletedAt = true, false, time.Time{}
			case "invalid-completion":
				row.CompletedAt = time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)
			case "bad-intent":
				row.Batch.IntentHash = strings.Repeat("0", 64)
				row.BatchHash, err = checkedPermissionBatchHash(row.Batch)
				require.NoError(t, err)
			}
			require.Error(t, validatePermissionReplays([]permissionReplay{row}, host))
		})
	}
}
