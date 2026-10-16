// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0
package oplocal

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestReviewR1_RecoveryRefusesMalformedReceiptAndPendingWithoutWrites(t *testing.T) {
	for _, kind := range []string{"pending", "replay", "wrong-caller", "notice", "orphan-notice", "publication"} {
		t.Run(kind, func(t *testing.T) {
			s, intent := contractAcceptedPending(t, "after:grants.json")
			s.alertStage = nil
			p := generationPending(t, s)
			switch kind {
			case "pending":
				writePermissionFixture(t, s, "permissions-pending.json", []byte(`{broken`))
			case "replay":
				writePermissionFixture(t, s, "permissions-replays.json", []byte(`{broken`))
			case "wrong-caller":
				intent.ID = strings.Repeat("2", 32)
			case "notice":
				p.NoticeOutcome = "published"
			case "orphan-notice":
				p = permissionPending{NoticeOutcome: "undeliverable"}
			case "publication":
				p.Batch.PublicationID = "22222222-2222-4222-8222-222222222222"
			}
			if kind == "notice" || kind == "orphan-notice" || kind == "publication" {
				require.NoError(t, s.WriteJSON("permissions-pending", p))
			}
			before := alertStateSnapshot(t, s)
			require.Error(t, s.CancelPermissionBatch(t.Context(), intent.ID))
			if kind == "replay" {
				deny := permissionFixtureBatch()
				deny.Changes[0].Action = "deny"
				require.Error(t, s.ApplyPermissions(t.Context(), deny, nil))
			}
			if kind != "wrong-caller" {
				_, err := s.PendingPermissionBatches(t.Context())
				require.Error(t, err)
			}
			require.Equal(t, before, alertStateSnapshot(t, s))
		})
	}
}

func TestReviewR1_CancelledCallerRefusesOperatorAndCompletedLookup(t *testing.T) {
	s, intent := contractAcceptedPending(t, "after:grants.json")
	s.alertStage = nil
	require.NoError(t, s.CancelPermissionBatch(t.Context(), intent.ID))
	before := alertStateSnapshot(t, s)
	b := permissionFixtureBatch()
	b.ID = intent.ID
	require.ErrorContains(t, s.ApplyPermissions(t.Context(), b, nil), "cancelled or quarantined")
	_, available, err := s.PermissionReplayResult(t.Context(), intent.BoardID, intent.ID)
	require.ErrorContains(t, err, "cancelled or quarantined")
	require.False(t, available)
	require.ErrorContains(t, s.CancelPermissionBatch(t.Context(), intent.ID), "not pending")
	require.Equal(t, before, alertStateSnapshot(t, s))
}

func TestReviewR1_CancellationBodyMustBeAnAuditObject(t *testing.T) {
	s, intent := contractAcceptedPending(t, "after:grants.json")
	s.alertStage = nil
	name := strings.TrimSuffix(permissionCancellationName(intent.ID), ".json")
	require.NoError(t, s.WriteJSON(name, []string{"not-an-audit"}))
	before := alertStateSnapshot(t, s)
	require.Error(t, s.CancelPermissionBatch(t.Context(), intent.ID))
	require.Error(t, s.ApplyPermissionIntent(t.Context(), intent, nil, nil))
	require.Equal(t, before, alertStateSnapshot(t, s))
}

func TestReviewR1_CancellationLogWriteFailureIsReportedAndResumes(t *testing.T) {
	s, intent := contractAcceptedPending(t, "after:permissions-pending.json")
	cause := errors.New("confirmation storage unavailable")
	s.alertStage = func(stage string) error {
		if stage == "before:confirmations.jsonl" {
			return cause
		}
		return nil
	}
	require.ErrorIs(t, s.CancelPermissionBatch(t.Context(), intent.ID), cause)
	s.alertStage = nil
	require.NoError(t, s.CancelPermissionBatch(t.Context(), intent.ID))
	grants, err := s.AgentGrants()
	require.NoError(t, err)
	require.Empty(t, grants)
	rows, err := s.Confirmations(t.Context())
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, "quarantined", rows[0].BoardLine)
}

func TestReviewR1_CancellationInvalidCompletionClockRetainsTerminalAudit(t *testing.T) {
	s, intent := contractAcceptedPending(t, "after:grants.json")
	s.alertStage = nil
	calls := 0
	s.permissionClock = func() time.Time {
		calls++
		if calls == 1 {
			return alertTime()
		}
		return time.Time{}
	}
	require.ErrorContains(t, s.CancelPermissionBatch(t.Context(), intent.ID), "completion time")
	s.permissionClock = alertTime
	require.NoError(t, s.CancelPermissionBatch(t.Context(), intent.ID))
	require.ErrorContains(t, s.ApplyPermissionIntent(t.Context(), intent, nil, nil), "cancelled or quarantined")
}

type reviewR1CancelOnSecondCheck struct {
	context.Context
	calls int
}

func (c *reviewR1CancelOnSecondCheck) Err() error {
	c.calls++
	if c.calls > 1 {
		return context.Canceled
	}
	return nil
}

func TestReviewR1_ReplayReadCancellationAfterIdentityLookupDoesNotWrite(t *testing.T) {
	s := alertFixture(t)
	intent := replayIntent()
	require.NoError(t, s.ApplyPermissionIntent(t.Context(), intent, replayPrepare, nil))
	before := alertStateSnapshot(t, s)
	_, _, err := s.PermissionReplayResult(&reviewR1CancelOnSecondCheck{Context: t.Context()}, intent.BoardID, intent.ID)
	require.ErrorIs(t, err, context.Canceled)
	_, err = s.PermissionReplayUsage(&reviewR1CancelOnSecondCheck{Context: t.Context()})
	require.ErrorIs(t, err, context.Canceled)
	require.Equal(t, before, alertStateSnapshot(t, s))
}

func TestReviewR1_UnreadableReplayRecordDoesNotBecomeEmptySuccessfulLookup(t *testing.T) {
	s := alertFixture(t)
	intent := replayIntent()
	require.NoError(t, s.ApplyPermissionIntent(t.Context(), intent, replayPrepare, nil))
	writePermissionFixture(t, s, "permissions-replays.json", []byte(`{broken`))
	before := alertStateSnapshot(t, s)
	_, _, err := s.PermissionReplayResult(t.Context(), intent.BoardID, intent.ID)
	require.Error(t, err)
	_, err = s.PermissionReplayUsage(t.Context())
	require.Error(t, err)
	require.Equal(t, before, alertStateSnapshot(t, s))
}

func TestReviewR1_CancellationReaderRejectsInvalidPrivateIdentity(t *testing.T) {
	s := alertFixture(t)
	require.NoError(t, s.withPermissions(t.Context(), func(dir *os.File, host string) error {
		_, err := readPermissionCancellation(dir, host, "../../outside")
		require.ErrorContains(t, err, "identity is invalid")
		return nil
	}))
}
