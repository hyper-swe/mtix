// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0
package oplocal

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPermissions_ReplayMalformedForeignLedgerRefused(t *testing.T) {
	for _, kind := range []string{"foreign", "null", "duplicate", "hash", "mode", "time", "incomplete", "unknown", "canonical-time", "canonical-pending", "canonical-node"} {
		t.Run(kind, func(t *testing.T) { testReplayRefusal(t, kind) })
	}
}
func testReplayRefusal(t *testing.T, kind string) {
	t.Helper()
	s := alertFixture(t)
	require.NoError(t, s.ApplyPermissionIntent(t.Context(), replayIntent(), replayPrepare, nil))
	host, err := s.HostID()
	require.NoError(t, err)
	dir, err := s.directory(false)
	require.NoError(t, err)
	receipts, err := readPermissionReplays(dir, host)
	require.NoError(t, err)
	require.NoError(t, closeFile(dir))
	mutateReplayRefusalFixture(t, s, kind, receipts)
	before := alertStateSnapshot(t, s)
	require.Error(t, s.ApplyPermissionIntent(t.Context(), replayIntent(), nil, nil))
	require.Equal(t, before, alertStateSnapshot(t, s))
	_, err = s.AgentGrants()
	require.Error(t, err)
	_, err = s.Confirmations(t.Context())
	require.Error(t, err)
	_, err = s.PermissionReplayUsage(t.Context())
	require.Error(t, err)
}

func mutateReplayRefusalFixture(t *testing.T, s *State, kind string, receipts []permissionReplay) {
	t.Helper()
	switch kind {
	case "foreign":
		raw, _ := json.Marshal(record{HostID: "foreign", Data: json.RawMessage(`[]`)})
		writePermissionFixture(t, s, "permissions-replays.json", raw)
	case "null":
		require.NoError(t, s.WriteJSON("permissions-replays", nil))
	case "duplicate":
		require.NoError(t, s.WriteJSON("permissions-replays", append(receipts, receipts[0])))
	case "hash":
		receipts[0].Batch.IntentHash = "bad"
		require.NoError(t, s.WriteJSON("permissions-replays", receipts))
	case "mode":
		receipts[0].Batch.Mode = "automatic"
		require.NoError(t, s.WriteJSON("permissions-replays", receipts))
	case "time":
		receipts[0].Batch.Time = replayIntent().Time
		require.NoError(t, s.WriteJSON("permissions-replays", receipts))
	case "incomplete":
		receipts[0].Completed = false
		require.NoError(t, s.WriteJSON("permissions-replays", receipts))
	case "canonical-node":
		receipts[0].Batch.NodeID = "changed-target"
		require.NoError(t, s.WriteJSON("permissions-replays", receipts))
	case "canonical-time":
		receipts[0].Batch.Time = receipts[0].Batch.Time.AddDate(1, 0, 0)
		require.NoError(t, s.WriteJSON("permissions-replays", receipts))
	case "canonical-pending":
		receipts[0].Batch.Changes[0].PendingRegistration = true
		require.NoError(t, s.WriteJSON("permissions-replays", receipts))
	case "unknown":
		require.NoError(t, s.WriteJSON("permissions-replays", []map[string]any{{"unknown": true}}))
	}
}
