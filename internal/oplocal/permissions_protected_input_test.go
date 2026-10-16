// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Public permission readers and writers must reject damaged protected inputs
// without treating corruption as missing history or changing accepted state.
package oplocal

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func requirePermissionReadersRefuseUnchanged(t *testing.T, s *State) {
	t.Helper()
	before := alertStateSnapshot(t, s)
	_, _, resultErr := s.PermissionReplayResult(t.Context(), replayIntent().BoardID, replayIntent().ID)
	_, usageErr := s.PermissionReplayUsage(t.Context())
	_, grantsErr := s.AgentGrants()
	_, rowsErr := s.Confirmations(t.Context())
	for _, err := range []error{resultErr, usageErr, grantsErr, rowsErr} {
		require.Error(t, err)
	}
	require.Equal(t, before, alertStateSnapshot(t, s))
	require.Error(t, New(s.env).ApplyPermissionIntent(t.Context(), replayIntent(), replayPrepare, nil))
	require.Equal(t, before, alertStateSnapshot(t, s))
}

func TestPermissions_ProtectedHostIdentityFailureNeverReturnsEmptySuccess(t *testing.T) {
	s := alertFixture(t)
	require.NoError(t, s.ApplyPermissionIntent(t.Context(), replayIntent(), replayPrepare, nil))
	writePermissionFixture(t, s, "host-id", []byte("invalid-synthetic-host\n"))
	requirePermissionReadersRefuseUnchanged(t, s)
}

func TestPermissions_ProtectedCompletionMarkerCorruptionIsReadOnly(t *testing.T) {
	for name, pending := range map[string]permissionPending{
		"invalid_generation":     {Generation: "invalid"},
		"invalid_completion":     {CompletedID: "invalid"},
		"rotation_without_batch": {Rotate: true},
		"preimage_without_batch": {ActiveHash: strings.Repeat("a", 64)},
	} {
		t.Run(name, func(t *testing.T) {
			s := alertFixture(t)
			require.NoError(t, s.ApplyPermissionIntent(t.Context(), replayIntent(), replayPrepare, nil))
			require.NoError(t, s.WriteJSON("permissions-pending", pending))
			requirePermissionReadersRefuseUnchanged(t, s)
		})
	}
}

func TestPermissions_ProtectedReplayLedgerRejectsConflictingAcceptedRecords(t *testing.T) {
	for _, kind := range []string{"foreign_host", "wrong_shape", "invalid_batch", "invalid_intent", "duplicate_caller", "duplicate_occurrence", "inflight_with_time", "orphan_inflight"} {
		t.Run(kind, func(t *testing.T) {
			s := alertFixture(t)
			require.NoError(t, s.ApplyPermissionIntent(t.Context(), replayIntent(), replayPrepare, nil))
			corruptPermissionLedger(t, s, kind)
			requirePermissionReadersRefuseUnchanged(t, s)
		})
	}
}

func corruptPermissionLedger(t *testing.T, s *State, kind string) {
	t.Helper()
	var rows []permissionReplay
	reports, err := s.ReadJSON("permissions-replays", &rows)
	require.NoError(t, err)
	require.Empty(t, reports)
	require.Len(t, rows, 1)
	host, err := s.HostID()
	require.NoError(t, err)
	switch kind {
	case "foreign_host":
		data, err := json.Marshal(record{HostID: strings.Repeat("0", 32), Data: mustPermissionJSON(rows)})
		require.NoError(t, err)
		writePermissionFixture(t, s, "permissions-replays.json", data)
		return
	case "wrong_shape":
		require.NoError(t, s.WriteJSON("permissions-replays", map[string]string{"invalid": "shape"}))
		return
	case "invalid_batch":
		rows[0].Batch.BoardID = "invalid"
	case "invalid_intent":
		rows[0].Batch.IntentHash = strings.Repeat("0", 64)
		rows[0].BatchHash = permissionBatchHash(rows[0].Batch)
	case "duplicate_caller":
		rows = append(rows, rows[0])
	case "duplicate_occurrence":
		other := rows[0]
		other.Batch.CallerID = "22222222222222222222222222222222"
		other.Batch.IntentHash = permissionIntentHash(other.Batch, host)
		other.BatchHash = permissionBatchHash(other.Batch)
		rows = append(rows, other)
	case "inflight_with_time", "orphan_inflight":
		rows[0].Completed = false
		if kind == "orphan_inflight" {
			rows[0].CompletedAt = PermissionBatch{}.Time
		}
	default:
		t.Fatalf("unknown ledger fixture %s", kind)
	}
	require.NoError(t, s.WriteJSON("permissions-replays", rows))
}

func TestPermissions_ProtectedGrantShapeNeverOverwritesPriorRecord(t *testing.T) {
	for _, raw := range []string{`{"board_grants":[{"board_id":"11111111-1111-4111-8111-111111111111","hub_identity":{"kind":"invalid"}}]}`, `{"board_grants":{}}`, `{"agent_grants":{}}`} {
		t.Run(raw, func(t *testing.T) {
			s := alertFixture(t)
			require.NoError(t, s.ApplyPermissions(t.Context(), permissionFixtureBatch(), nil))
			var value map[string]json.RawMessage
			require.NoError(t, json.Unmarshal([]byte(raw), &value))
			require.NoError(t, s.WriteJSON("grants", value))
			before := alertStateSnapshot(t, s)
			_, err := s.AgentGrants()
			require.Error(t, err)
			require.Error(t, s.ApplyPermissions(t.Context(), permissionFixtureBatch(), nil))
			require.Equal(t, before, alertStateSnapshot(t, s))
		})
	}
}
