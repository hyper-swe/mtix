// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Permission APIs must refuse unavailable state and contradictory pending work
// without silently creating replacement grants or losing accepted transactions.
package oplocal

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestPermissions_StateDirectoryReplacedByFileIsNeverReinitialized(t *testing.T) {
	s := alertFixture(t)
	require.NoError(t, s.ApplyPermissionIntent(t.Context(), replayIntent(), replayPrepare, nil))
	before := alertStateSnapshot(t, s)
	path, err := s.Path()
	require.NoError(t, err)
	saved := path + ".saved"
	require.NoError(t, os.Rename(path, saved))
	t.Cleanup(func() {
		require.NoError(t, os.Remove(path))
		require.NoError(t, os.Rename(saved, path))
		require.Equal(t, before, alertStateSnapshot(t, s))
	})
	content := []byte("synthetic unavailable state directory\n")
	require.NoError(t, os.WriteFile(path, content, 0600))
	_, _, resultErr := s.PermissionReplayResult(t.Context(), replayIntent().BoardID, replayIntent().ID)
	_, usageErr := s.PermissionReplayUsage(t.Context())
	_, grantsErr := s.AgentGrants()
	_, rowsErr := s.Confirmations(t.Context())
	for _, err := range []error{resultErr, usageErr, grantsErr, rowsErr} {
		require.Error(t, err)
	}
	require.Error(t, s.ApplyPermissionIntent(t.Context(), replayIntent(), replayPrepare, nil))
	require.Error(t, s.ApplyPermissions(t.Context(), permissionFixtureBatch(), nil))
	require.Error(t, s.WithHubTransition(t.Context(), func() error { t.Fatal("unavailable directory invoked transition"); return nil }))
	after, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, content, after)
}

func TestPermissions_InvalidDerivedInputDoesNotCreateProtectedState(t *testing.T) {
	for _, kind := range []string{"caller", "intent_hash", "deny_pending", "invalid_action"} {
		t.Run(kind, func(t *testing.T) {
			s := alertFixture(t)
			batch := permissionFixtureBatch()
			switch kind {
			case "caller":
				batch.CallerID = strings.Repeat("1", 32)
			case "intent_hash":
				batch.IntentHash = strings.Repeat("a", 64)
			case "deny_pending":
				batch.Changes[0].Action = "deny"
				batch.Changes[0].PendingRegistration = true
			case "invalid_action":
				batch.Changes[0].Action = "invalid"
			}
			require.Error(t, s.ApplyPermissions(t.Context(), batch, nil))
			path, err := s.Path()
			require.NoError(t, err)
			_, err = os.Stat(path)
			require.ErrorIs(t, err, os.ErrNotExist)
		})
	}
}

func TestPermissions_PendingReceiptContradictionNeverOverwritesAcceptedEvidence(t *testing.T) {
	for _, kind := range []string{"intent_hash", "canonical_time", "completed_marker", "caller_shape"} {
		t.Run(kind, func(t *testing.T) {
			s, _ := contractAcceptedPending(t, "after:permission-replay-accepted")
			pending := generationPending(t, s)
			switch kind {
			case "intent_hash":
				pending.Batch.IntentHash = strings.Repeat("0", 64)
			case "canonical_time":
				pending.Batch.Time = pending.Batch.Time.Add(time.Hour)
			case "completed_marker":
				pending.CompletedID = pending.Batch.ID
			case "caller_shape":
				pending.Batch.CallerID = "invalid"
			}
			require.NoError(t, s.WriteJSON("permissions-pending", pending))
			requirePermissionReadersRefuseUnchanged(t, s)
		})
	}
}

func TestPermissions_PrepareDerivedOutcomeCannotBypassCanonicalDeny(t *testing.T) {
	s := alertFixture(t)
	require.NoError(t, s.ApplyPermissions(t.Context(), permissionFixtureBatch(), nil))
	before := alertStateSnapshot(t, s)
	prepare := func(ctx context.Context, b PermissionBatch) (PermissionBatch, error) {
		b, err := replayPrepare(ctx, b)
		b.Changes[0].Outcome = "denied_requires_operator"
		return b, err
	}
	require.ErrorContains(t, s.ApplyPermissionIntent(t.Context(), replayIntent(), prepare, nil), "derived outcome")
	require.Equal(t, before, alertStateSnapshot(t, s))
}

func TestPermissions_NewCallerCannotSkipPendingPublicationFailure(t *testing.T) {
	s := alertFixture(t)
	batch := permissionFixtureBatch()
	batch.ID = strings.Repeat("2", 32)
	batch.NodeID = "TEST-1"
	sentinel := errors.New("synthetic unavailable publication")
	publish := func(context.Context, PermissionBatch) error { return sentinel }
	require.ErrorIs(t, s.ApplyPermissions(t.Context(), batch, publish), sentinel)
	pending := generationPending(t, s)
	before := alertStateSnapshot(t, s)
	require.ErrorIs(t, New(s.env).ApplyPermissionIntent(t.Context(), replayIntent(), replayPrepare, publish), sentinel)
	require.Equal(t, before, alertStateSnapshot(t, s))
	require.Equal(t, pending, generationPending(t, s))
	accepted := 0
	require.NoError(t, New(s.env).ApplyPermissionIntent(t.Context(), replayIntent(), replayPrepare,
		func(context.Context, PermissionBatch) error { accepted++; return nil }))
	require.Equal(t, 1, accepted)
	rows, err := s.Confirmations(t.Context())
	require.NoError(t, err)
	require.Len(t, rows, 2)
	require.False(t, rows[0].Pending)
	require.False(t, rows[1].Pending)
	_, available, err := s.PermissionReplayResult(t.Context(), replayIntent().BoardID, replayIntent().ID)
	require.NoError(t, err)
	require.True(t, available)
}

func TestPermissions_ProtectedLockPathRefusesWithoutReplacingExistingBytes(t *testing.T) {
	s := alertFixture(t)
	require.NoError(t, s.ApplyPermissions(t.Context(), permissionFixtureBatch(), nil))
	path, err := s.Path()
	require.NoError(t, err)
	lock := filepath.Join(path, "permissions.lock")
	require.NoError(t, os.Rename(lock, lock+".saved"))
	require.NoError(t, os.Mkdir(lock, 0700))
	require.Error(t, s.ApplyPermissionIntent(t.Context(), replayIntent(), replayPrepare, nil))
	require.NoError(t, os.Remove(lock))
	require.NoError(t, os.Rename(lock+".saved", lock))
	rows, err := s.Confirmations(t.Context())
	require.NoError(t, err)
	require.Len(t, rows, 1)
}

func TestPermissions_OversizedPendingInputNeverBecomesEmptyHistory(t *testing.T) {
	s := alertFixture(t)
	require.NoError(t, s.ApplyPermissionIntent(t.Context(), replayIntent(), replayPrepare, nil))
	writePermissionFixture(t, s, "permissions-pending.json", []byte(strings.Repeat("x", maxRecordSize+1)))
	requirePermissionReadersRefuseUnchanged(t, s)
}

func TestPermissions_GrantEnvelopeCorruptionCannotBeReplacedByFileAllow(t *testing.T) {
	for _, kind := range []string{"foreign_host", "array_data", "null_data"} {
		t.Run(kind, func(t *testing.T) {
			s := alertFixture(t)
			require.NoError(t, s.ApplyPermissionIntent(t.Context(), replayIntent(), replayPrepare, nil))
			switch kind {
			case "foreign_host":
				raw, err := json.Marshal(record{HostID: strings.Repeat("0", 32), Data: json.RawMessage(`{}`)})
				require.NoError(t, err)
				writePermissionFixture(t, s, "grants.json", raw)
			case "array_data":
				require.NoError(t, s.WriteJSON("grants", []string{"synthetic"}))
			case "null_data":
				require.NoError(t, s.WriteJSON("grants", nil))
			}
			before := alertStateSnapshot(t, s)
			_, err := s.AgentGrants()
			require.Error(t, err)
			_, err = s.Confirmations(t.Context())
			require.Error(t, err)
			intent := replayIntent()
			intent.ID = strings.Repeat("2", 32)
			require.Error(t, s.ApplyPermissionIntent(t.Context(), intent, replayPrepare, nil))
			require.Equal(t, before, alertStateSnapshot(t, s))
		})
	}
}

func TestPermissions_ConflictingRetainedCopiesCannotPrepareAnotherBatch(t *testing.T) {
	s := alertFixture(t)
	require.NoError(t, s.ApplyPermissions(t.Context(), permissionFixtureBatch(), nil))
	rows, err := s.Confirmations(t.Context())
	require.NoError(t, err)
	require.Len(t, rows, 1)
	rows[0].Source = "conflicting retained copy"
	raw, err := encodeConfirmations(rows)
	require.NoError(t, err)
	writePermissionFixture(t, s, "confirmations.1.jsonl", raw)
	before := alertStateSnapshot(t, s)
	_, err = s.Confirmations(t.Context())
	require.Error(t, err)
	require.Error(t, s.ApplyPermissionIntent(t.Context(), replayIntent(), replayPrepare, nil))
	require.Equal(t, before, alertStateSnapshot(t, s))
}

func TestPermissions_ReusedLegacyBatchIdentityCannotChangeAcceptedCardinality(t *testing.T) {
	s := alertFixture(t)
	batch := permissionFixtureBatch()
	batch.ID = strings.Repeat("1", 32)
	require.NoError(t, s.ApplyPermissions(t.Context(), batch, nil))
	before := alertStateSnapshot(t, s)
	batch.Changes = append(batch.Changes, PermissionChange{AgentID: "second-agent", Action: "allow"})
	require.Error(t, s.ApplyPermissions(t.Context(), batch, nil))
	require.Equal(t, before, alertStateSnapshot(t, s))
}
