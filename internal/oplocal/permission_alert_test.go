// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0
package oplocal

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func permissionAlertOccurrence() PermissionAlertOccurrence {
	return PermissionAlertOccurrence{Slot: "synthetic-board-agent", Key: "synthetic-transition"}
}
func TestPermissionAlert_SameOccurrenceSurvivesOtherBoardAndWindow(t *testing.T) {
	s := alertFixture(t)
	occ := permissionAlertOccurrence()
	input := alertInput()
	row, err := s.RecordPermissionAlert(t.Context(), input, alertTime(), occ)
	require.NoError(t, err)
	other := occ
	other.Slot = "other-board-agent"
	input.Detail = "other board"
	second, err := s.RecordPermissionAlert(t.Context(), input, alertTime(), other)
	require.NoError(t, err)
	require.Equal(t, uint64(2), second.Count)
	later, err := s.RecordAlert(t.Context(), input, alertTime().Add(2*time.Hour))
	require.NoError(t, err)
	require.NotEqual(t, row.ID, later.ID)
	retry, err := New(s.env).RecordPermissionAlert(t.Context(), alertInput(), alertTime().Add(3*time.Hour), occ)
	require.NoError(t, err)
	require.Equal(t, row, retry)
	rows, err := s.RecentAlerts(t.Context(), 0)
	require.NoError(t, err)
	require.Len(t, rows, 2)
}
func TestPermissionAlert_FaultRecoveryWithOrdinaryInterleave(t *testing.T) {
	name := permissionAlertReceiptName(permissionAlertOccurrence().Slot)
	stages := []string{"before:alerts-pending.json", "after:alerts-pending.json", "before:alerts.jsonl", "after:alerts.jsonl", "before:rate", "after:rate", "before:" + name, "after:" + name, "before:clear", "after:clear"}
	for _, stage := range stages {
		t.Run(stage, func(t *testing.T) { testPermissionAlertFault(t, stage) })
	}
}
func testPermissionAlertFault(t *testing.T, stage string) {
	t.Helper()
	s := alertFixture(t)
	occ := permissionAlertOccurrence()
	fired := false
	s.alertStage = func(at string) error {
		if at == stage && !fired {
			fired = true
			return errors.New("injected")
		}
		return nil
	}
	_, err := s.RecordPermissionAlert(t.Context(), alertInput(), alertTime(), occ)
	require.Error(t, err)
	require.True(t, fired)
	s = New(s.env)
	ordinary := alertInput()
	ordinary.Detail = "ordinary interleave"
	_, err = s.RecordAlert(t.Context(), ordinary, alertTime())
	require.NoError(t, err)
	retry, err := s.RecordPermissionAlert(t.Context(), alertInput(), alertTime(), occ)
	require.NoError(t, err)
	again, err := s.RecordPermissionAlert(t.Context(), alertInput(), alertTime(), occ)
	require.NoError(t, err)
	require.Equal(t, retry, again)
	rows, err := s.RecentAlerts(t.Context(), 0)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, uint64(2), rows[0].Count)
	if stage != "before:alerts-pending.json" {
		require.Equal(t, "ordinary interleave", rows[0].Detail)
	}
}
func TestPermissionAlert_ConcurrentSameKeyCountsOnce(t *testing.T) {
	s := alertFixture(t)
	var wg sync.WaitGroup
	results := make(chan error, 16)
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := New(s.env).RecordPermissionAlert(t.Context(), alertInput(), alertTime(), permissionAlertOccurrence())
			results <- err
		}()
	}
	wg.Wait()
	close(results)
	for err := range results {
		require.NoError(t, err)
	}
	rows, err := s.RecentAlerts(t.Context(), 0)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, uint64(1), rows[0].Count)
}
func TestPermissionAlert_RequestConflictLeavesBytesUnchanged(t *testing.T) {
	s := alertFixture(t)
	occ := permissionAlertOccurrence()
	_, err := s.RecordPermissionAlert(t.Context(), alertInput(), alertTime(), occ)
	require.NoError(t, err)
	before := alertStateSnapshot(t, s)
	input := alertInput()
	input.Detail = "changed request"
	_, err = s.RecordPermissionAlert(t.Context(), input, alertTime(), occ)
	require.Error(t, err)
	require.Equal(t, before, alertStateSnapshot(t, s))
	for _, bad := range []PermissionAlertOccurrence{{Slot: "", Key: "x"}, {Slot: "x", Key: ""}, {Slot: "x", Key: "bad\nkey"}} {
		_, err = s.RecordPermissionAlert(t.Context(), alertInput(), alertTime(), bad)
		require.Error(t, err)
	}
	_, err = s.RecordPermissionAlert(t.Context(), alertInput(), time.Time{}, occ)
	require.Error(t, err)
}
