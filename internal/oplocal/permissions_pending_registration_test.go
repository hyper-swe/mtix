// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0
package oplocal

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func pendingRegistrationChange(t *testing.T, action string, pending bool) PermissionChange {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"agent_id": "fixture-agent", "action": action, "pending_registration": pending})
	require.NoError(t, err)
	var change PermissionChange
	require.NoError(t, json.Unmarshal(raw, &change))
	return change
}
func assertPendingRegistrationJSON(t *testing.T, value any, want bool) {
	t.Helper()
	raw, err := json.Marshal(value)
	require.NoError(t, err)
	var data map[string]any
	require.NoError(t, json.Unmarshal(raw, &data))
	if want {
		require.Equal(t, true, data["pending_registration"])
	} else {
		require.NotContains(t, data, "pending_registration")
	}
}
func TestPermissions_PendingRegistrationProjectionAndImmutableReplay(t *testing.T) {
	s := alertFixture(t)
	batch := permissionFixtureBatch()
	batch.ID = "11111111111111111111111111111111"
	batch.Changes = []PermissionChange{pendingRegistrationChange(t, "allow", true)}
	pendingHash := permissionBatchHash(batch)
	require.NoError(t, s.ApplyPermissions(t.Context(), batch, nil))
	grants, err := s.AgentGrants()
	require.NoError(t, err)
	require.Len(t, grants, 1)
	rows, err := s.Confirmations(t.Context())
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assertPendingRegistrationJSON(t, grants[0], true)
	assertPendingRegistrationJSON(t, rows[0], true)
	require.Equal(t, pendingHash, rows[0].BatchHash)
	batch.Changes = []PermissionChange{pendingRegistrationChange(t, "allow", false)}
	require.NotEqual(t, pendingHash, permissionBatchHash(batch))
	before := alertStateSnapshot(t, s)
	require.Error(t, s.ApplyPermissions(t.Context(), batch, nil))
	require.Equal(t, before, alertStateSnapshot(t, s))
	batch.ID = "22222222222222222222222222222222"
	require.NoError(t, s.ApplyPermissions(t.Context(), batch, nil))
	grants, err = s.AgentGrants()
	require.NoError(t, err)
	assertPendingRegistrationJSON(t, grants[0], false)
	rows, err = s.Confirmations(t.Context())
	require.NoError(t, err)
	require.Len(t, rows, 2)
	assertPendingRegistrationJSON(t, rows[0], true)
	assertPendingRegistrationJSON(t, rows[1], false)
}
func TestPermissions_PendingRegistrationDenyRejectedBeforeState(t *testing.T) {
	s := alertFixture(t)
	batch := permissionFixtureBatch()
	batch.Changes = []PermissionChange{pendingRegistrationChange(t, "deny", true)}
	require.Error(t, s.ApplyPermissions(t.Context(), batch, nil))
	grants, err := s.AgentGrants()
	require.NoError(t, err)
	require.Empty(t, grants)
	rows, err := s.Confirmations(t.Context())
	require.NoError(t, err)
	require.Empty(t, rows)
}
func TestPermissions_PendingRegistrationDenyGrantRefused(t *testing.T) {
	s := alertFixture(t)
	batch := permissionFixtureBatch()
	require.NoError(t, s.ApplyPermissions(t.Context(), batch, nil))
	grants, err := s.AgentGrants()
	require.NoError(t, err)
	raw, err := json.Marshal(grants[0])
	require.NoError(t, err)
	var invalid map[string]any
	require.NoError(t, json.Unmarshal(raw, &invalid))
	invalid["denied"] = true
	invalid["pending_registration"] = true
	require.NoError(t, s.WriteJSON("grants", map[string]any{"agent_grants": []any{invalid}}))
	before := alertStateSnapshot(t, s)
	_, err = s.AgentGrants()
	require.Error(t, err)
	require.Error(t, s.ApplyPermissions(t.Context(), batch, nil))
	require.Equal(t, before, alertStateSnapshot(t, s))
}
