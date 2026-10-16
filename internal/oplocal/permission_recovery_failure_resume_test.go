// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0
package oplocal

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCancelPermissionBatch_BeforeProjection_PreservesAcceptedDenyAndTerminalCaller(t *testing.T) {
	for _, stage := range []string{"after:permissions-pending.json", "after:permission-replay-accepted", "before:grants.json"} {
		t.Run(stage, func(t *testing.T) {
			s := alertFixture(t)
			intent := replayIntent()
			intent.Changes = append(intent.Changes, PermissionChange{AgentID: "held", Action: "deny"})
			s.alertStage = func(at string) error {
				if at == stage {
					return errors.New("acceptance interrupted")
				}
				return nil
			}
			require.Error(t, s.ApplyPermissionIntent(t.Context(), intent, replayPrepare, nil))
			s.alertStage = nil
			require.NoError(t, s.CancelPermissionBatch(t.Context(), intent.ID))
			grants, err := s.AgentGrants()
			require.NoError(t, err)
			require.Len(t, grants, 1)
			require.Equal(t, "held", grants[0].AgentID)
			require.True(t, grants[0].Denied)
			rows, err := s.Confirmations(t.Context())
			require.NoError(t, err)
			require.Len(t, rows, 2)
			pending, err := s.PendingPermissionBatches(t.Context())
			require.NoError(t, err)
			require.Empty(t, pending)
			before := alertStateSnapshot(t, s)
			require.ErrorContains(t, s.ApplyPermissionIntent(t.Context(), intent, nil, nil), "cancelled or quarantined")
			require.Equal(t, before, alertStateSnapshot(t, s))
		})
	}
}

func TestApplyPermissionIntent_UndeliverableNotice_RetainsOutcomeWithoutRepublishing(t *testing.T) {
	for _, stage := range []string{"after:permissions-pending.json", "before:final:confirmations.jsonl", "after:final:confirmations.jsonl", "before:permission-replay-completed", "before:permissions-clear"} {
		t.Run(stage, func(t *testing.T) { reviewR1UndeliverableStage(t, stage) })
	}
}

func reviewR1UndeliverableStage(t *testing.T, stage string) {
	t.Helper()
	s := alertFixture(t)
	intent := replayIntent()
	intent.Changes[0].Action = "deny"
	pendingWrites := 0
	fired := false
	s.alertStage = func(at string) error {
		if at == "after:permissions-pending.json" {
			pendingWrites++
		}
		if at == stage && !fired && (stage != "after:permissions-pending.json" || pendingWrites == 2) {
			fired = true
			return errors.New("undeliverable completion interrupted")
		}
		return nil
	}
	prepare := func(ctx context.Context, b PermissionBatch) (PermissionBatch, error) {
		b, err := replayPrepare(ctx, b)
		b.NodeID = "TEST-1"
		return b, err
	}
	calls := 0
	publish := func(context.Context, PermissionBatch) error { calls++; return ErrPermissionNoticeUndeliverable }
	require.Error(t, s.ApplyPermissionIntent(t.Context(), intent, prepare, publish))
	require.True(t, fired)
	s.alertStage = nil
	require.NoError(t, s.ApplyPermissionIntent(t.Context(), intent, nil, publish))
	require.Equal(t, 1, calls)
	rows, err := s.Confirmations(t.Context())
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, "undeliverable", rows[0].BoardLine)
	require.False(t, rows[0].Pending)
	grants, err := s.AgentGrants()
	require.NoError(t, err)
	require.Len(t, grants, 1)
	require.True(t, grants[0].Denied)
}
