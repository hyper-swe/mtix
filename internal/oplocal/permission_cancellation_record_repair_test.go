// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0
package oplocal

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCancelPermissionBatch_ProtectedRecordRepaired_Resumes(t *testing.T) {
	for _, name := range []string{"grants.json", "confirmations.jsonl", "permissions-replays.json"} {
		t.Run(name, func(t *testing.T) { reviewR1RepairCancellation(t, name) })
	}
}

func reviewR1RepairCancellation(t *testing.T, name string) {
	t.Helper()
	s, intent := contractAcceptedPending(t, "after:grants.json")
	dir, err := s.Path()
	require.NoError(t, err)
	file := filepath.Join(dir, name)
	saved, err := os.ReadFile(file)
	require.NoError(t, err)
	stages := map[string]string{"grants.json": "after:" + permissionCancellationName(intent.ID), "confirmations.jsonl": "after:grants.json", "permissions-replays.json": "after:cancel:confirmations.jsonl"}
	fired := false
	s.alertStage = func(stage string) error {
		if stage == stages[name] && !fired {
			fired = true
			return os.WriteFile(file, []byte("{synthetic damaged record"), 0600)
		}
		return nil
	}
	require.Error(t, s.CancelPermissionBatch(t.Context(), intent.ID))
	require.True(t, fired)
	_, err = s.AgentGrants()
	require.Error(t, err, "damaged protected records cannot become an empty successful state")
	s.alertStage = nil
	require.NoError(t, os.WriteFile(file, saved, 0600))
	require.NoError(t, s.CancelPermissionBatch(t.Context(), intent.ID))
	grants, err := s.AgentGrants()
	require.NoError(t, err)
	require.Empty(t, grants, "repair never commits the cancelled allow")
	rows, err := s.Confirmations(t.Context())
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, "quarantined", rows[0].BoardLine)
	require.ErrorContains(t, s.ApplyPermissionIntent(t.Context(), intent, nil, nil), "cancelled or quarantined")
}
