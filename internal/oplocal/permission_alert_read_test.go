// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0
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

func TestPermissionAlert_AcceptedLookupMissingInvalidAndCanceledCreatesNothing(t *testing.T) {
	s := alertFixture(t)
	occ := permissionAlertOccurrence()
	accepted, err := s.PermissionAlertAccepted(t.Context(), alertInput(), occ)
	require.NoError(t, err)
	require.False(t, accepted)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	accepted, err = s.PermissionAlertAccepted(ctx, alertInput(), occ)
	require.ErrorIs(t, err, context.Canceled)
	require.False(t, accepted)
	for _, bad := range []PermissionAlertOccurrence{{Slot: "", Key: "x"}, {Slot: "x", Key: "bad\nkey"}} {
		accepted, err = s.PermissionAlertAccepted(t.Context(), alertInput(), bad)
		require.Error(t, err)
		require.False(t, accepted)
	}
	accepted, err = s.PermissionAlertAccepted(t.Context(), Alert{}, occ)
	require.Error(t, err)
	require.False(t, accepted)
	path, err := s.Path()
	require.NoError(t, err)
	_, err = os.Stat(path)
	require.ErrorIs(t, err, os.ErrNotExist)
}
func TestPermissionAlert_AcceptedLookupExactEpochAndInputWithoutRecount(t *testing.T) {
	s := alertFixture(t)
	occ := permissionAlertOccurrence()
	_, err := s.RecordPermissionAlert(t.Context(), alertInput(), alertTime(), occ)
	require.NoError(t, err)
	other := alertInput()
	other.Detail = "later ordinary diagnostic"
	_, err = s.RecordAlert(t.Context(), other, alertTime().Add(time.Minute))
	require.NoError(t, err)
	before := alertStateSnapshot(t, s)
	accepted, err := New(s.env).PermissionAlertAccepted(t.Context(), alertInput(), occ)
	require.NoError(t, err)
	require.True(t, accepted)
	accepted, err = s.PermissionAlertAccepted(t.Context(), other, occ)
	require.ErrorContains(t, err, "request conflicts")
	require.False(t, accepted)
	for _, missing := range []PermissionAlertOccurrence{{Slot: occ.Slot, Key: "other-epoch"}, {Slot: "other-slot", Key: occ.Key}} {
		accepted, err = s.PermissionAlertAccepted(t.Context(), alertInput(), missing)
		require.NoError(t, err)
		require.False(t, accepted)
	}
	require.Equal(t, before, alertStateSnapshot(t, s))
	rows, err := s.RecentAlerts(t.Context(), 0)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, uint64(2), rows[0].Count)
	require.Equal(t, other.Detail, rows[0].Detail)
}
func TestPermissionAlert_AcceptedLookupRecoveryNeverAddsOccurrence(t *testing.T) {
	name := permissionAlertReceiptName(permissionAlertOccurrence().Slot)
	for _, stage := range []string{"before:alerts-pending.json", "after:alerts-pending.json", "after:alerts.jsonl", "before:" + name, "after:" + name, "before:clear", "after:clear"} {
		t.Run(stage, func(t *testing.T) { testAcceptedAlertRecovery(t, stage) })
	}
}
func testAcceptedAlertRecovery(t *testing.T, stage string) {
	t.Helper()
	s := alertFixture(t)
	s.alertStage = func(at string) error {
		if at == stage {
			return errors.New("accepted lookup fault")
		}
		return nil
	}
	_, err := s.RecordPermissionAlert(t.Context(), alertInput(), alertTime(), permissionAlertOccurrence())
	require.Error(t, err)
	restarted := New(s.env)
	accepted, err := restarted.PermissionAlertAccepted(t.Context(), alertInput(), permissionAlertOccurrence())
	require.NoError(t, err)
	require.Equal(t, stage != "before:alerts-pending.json", accepted)
	before := alertStateSnapshot(t, restarted)
	again, err := restarted.PermissionAlertAccepted(t.Context(), alertInput(), permissionAlertOccurrence())
	require.NoError(t, err)
	require.Equal(t, accepted, again)
	require.Equal(t, before, alertStateSnapshot(t, restarted))
	rows, err := restarted.RecentAlerts(t.Context(), 0)
	require.NoError(t, err)
	if accepted {
		require.Len(t, rows, 1)
		require.Equal(t, uint64(1), rows[0].Count)
	} else {
		require.Empty(t, rows)
	}
}
func TestPermissionAlert_AcceptedLookupCorruptReceiptFailsClosedUnchanged(t *testing.T) {
	for _, kind := range []string{"foreign-envelope", "foreign-row", "slot", "input-hash", "count", "malformed"} {
		t.Run(kind, func(t *testing.T) { testAcceptedAlertCorruption(t, kind) })
	}
}
func testAcceptedAlertCorruption(t *testing.T, kind string) {
	t.Helper()
	s := alertFixture(t)
	occ := permissionAlertOccurrence()
	_, err := s.RecordPermissionAlert(t.Context(), alertInput(), alertTime(), occ)
	require.NoError(t, err)
	name := permissionAlertReceiptName(occ.Slot)
	var receipt permissionAlertReceipt
	_, err = s.ReadJSON(strings.TrimSuffix(name, ".json"), &receipt)
	require.NoError(t, err)
	switch kind {
	case "foreign-row":
		receipt.Row.HostID = strings.Repeat("f", 32)
	case "slot":
		receipt.Occurrence.Slot = "wrong-slot"
	case "input-hash":
		receipt.Occurrence.InputHash = strings.Repeat("0", 64)
	case "count":
		receipt.Row.Count = 0
	}
	require.NoError(t, s.WriteJSON(strings.TrimSuffix(name, ".json"), receipt))
	if kind == "foreign-envelope" {
		data, err := json.Marshal(record{HostID: strings.Repeat("f", 32), Data: mustPermissionJSON(receipt)})
		require.NoError(t, err)
		writePermissionFixture(t, s, name, data)
	}
	if kind == "malformed" {
		writePermissionFixture(t, s, name, []byte("{synthetic_private_receipt"))
	}
	before := alertStateSnapshot(t, s)
	accepted, err := New(s.env).PermissionAlertAccepted(t.Context(), alertInput(), occ)
	require.Error(t, err)
	require.False(t, accepted)
	require.NotContains(t, err.Error(), "synthetic_private_receipt")
	require.Equal(t, before, alertStateSnapshot(t, s))
}
func TestPermissionAlert_AcceptedLookupPendingRecoveryErrorPropagates(t *testing.T) {
	s := alertFixture(t)
	s.alertStage = func(stage string) error {
		if stage == "after:alerts-pending.json" {
			return errors.New("pending accepted")
		}
		return nil
	}
	_, err := s.RecordPermissionAlert(t.Context(), alertInput(), alertTime(), permissionAlertOccurrence())
	require.Error(t, err)
	path, err := s.Path()
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(path, "alerts.jsonl"), []byte("incomplete synthetic log"), 0600))
	before := alertStateSnapshot(t, s)
	accepted, err := New(s.env).PermissionAlertAccepted(t.Context(), alertInput(), permissionAlertOccurrence())
	require.Error(t, err)
	require.False(t, accepted)
	require.Equal(t, before, alertStateSnapshot(t, s))
}
