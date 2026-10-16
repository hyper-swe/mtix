// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// A new hub epoch must not overwrite a contradictory retained Alert receipt.
package oplocal

import (
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPermissionAlert_InterruptedEpochRefusesDamagedPriorReceiptThenRecoversOnce(t *testing.T) {
	for _, kind := range []string{"slot", "stored_row", "same_epoch_row"} {
		t.Run(kind, func(t *testing.T) { testPermissionAlertPreimageRepair(t, kind) })
	}
}

func testPermissionAlertPreimageRepair(t *testing.T, kind string) {
	t.Helper()
	s := alertFixture(t)
	occ := permissionAlertOccurrence()
	_, err := s.RecordPermissionAlert(t.Context(), alertInput(), alertTime(), occ)
	require.NoError(t, err)
	occ.Key = "next-synthetic-transition"
	s.alertStage = func(stage string) error {
		if stage == "after:alerts-pending.json" {
			return errors.New("interrupt before next epoch projection")
		}
		return nil
	}
	_, err = s.RecordPermissionAlert(t.Context(), alertInput(), alertTime(), occ)
	require.Error(t, err)
	name := permissionAlertReceiptName(occ.Slot)
	original, present := contractReadFile(t, s, name)
	require.True(t, present)
	var receipt permissionAlertReceipt
	_, err = s.ReadJSON(strings.TrimSuffix(name, ".json"), &receipt)
	require.NoError(t, err)
	switch kind {
	case "slot":
		receipt.Occurrence.Slot = "another-synthetic-slot"
	case "stored_row":
		receipt.Row.Agent = ""
	default:
		receipt.Occurrence.Key = occ.Key
	}
	require.NoError(t, s.WriteJSON(strings.TrimSuffix(name, ".json"), receipt))
	before := alertStateSnapshot(t, s)
	_, err = New(s.env).RecordPermissionAlert(t.Context(), alertInput(), alertTime(), occ)
	require.Error(t, err)
	require.Equal(t, before, alertStateSnapshot(t, s))
	writePermissionFixture(t, s, name, original)
	row, err := New(s.env).RecordPermissionAlert(t.Context(), alertInput(), alertTime(), occ)
	require.NoError(t, err)
	require.Equal(t, uint64(2), row.Count)
	again, err := New(s.env).RecordPermissionAlert(t.Context(), alertInput(), alertTime(), occ)
	require.NoError(t, err)
	require.Equal(t, row, again)
}
