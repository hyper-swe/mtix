// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0
package oplocal

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPermissionAlert_ForeignMalformedReceiptAndPendingRefused(t *testing.T) {
	for _, kind := range []string{"foreign-receipt", "empty-receipt", "hash-receipt", "bad-pending-key", "bad-pending-hash"} {
		t.Run(kind, func(t *testing.T) { testPermissionAlertRefusal(t, kind) })
	}
}
func testPermissionAlertRefusal(t *testing.T, kind string) {
	t.Helper()
	s := alertFixture(t)
	occ := permissionAlertOccurrence()
	s.alertStage = func(stage string) error {
		if stage == "before:clear" {
			return errors.New("leave pending")
		}
		return nil
	}
	_, err := s.RecordPermissionAlert(t.Context(), alertInput(), alertTime(), occ)
	require.Error(t, err)
	s = New(s.env)
	name := permissionAlertReceiptName(occ.Slot)
	switch kind {
	case "foreign-receipt":
		raw, _ := json.Marshal(record{HostID: "foreign", Data: json.RawMessage(`{}`)})
		writePermissionFixture(t, s, name, raw)
	case "empty-receipt", "hash-receipt":
		var receipt permissionAlertReceipt
		_, err := s.ReadJSON(strings.TrimSuffix(name, ".json"), &receipt)
		require.NoError(t, err)
		if kind == "empty-receipt" {
			receipt.Row = nil
		} else {
			receipt.Occurrence.InputHash = "bad"
		}
		require.NoError(t, s.WriteJSON(strings.TrimSuffix(name, ".json"), receipt))
	default:
		var pending alertPending
		_, err := s.ReadJSON("alerts-pending", &pending)
		require.NoError(t, err)
		if kind == "bad-pending-key" {
			pending.PermissionOccurrence.Key = "bad\nkey"
		} else {
			pending.PermissionOccurrence.InputHash = "bad"
		}
		require.NoError(t, s.WriteJSON("alerts-pending", pending))
	}
	before := alertStateSnapshot(t, s)
	_, err = s.RecordPermissionAlert(t.Context(), alertInput(), alertTime(), occ)
	require.Error(t, err)
	_, err = s.RecordAlert(t.Context(), alertInput(), alertTime())
	require.Error(t, err)
	_, err = s.RecentAlerts(t.Context(), 0)
	require.Error(t, err)
	require.Equal(t, before, alertStateSnapshot(t, s))
}
