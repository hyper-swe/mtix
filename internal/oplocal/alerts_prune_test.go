// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Tests that operator alert rate records stay bounded and text guards hold.
package oplocal

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
)

func rateRecordNames(t *testing.T, s *State) []string {
	t.Helper()
	dir, err := s.Path()
	require.NoError(t, err)
	names, err := filepath.Glob(filepath.Join(dir, "alert-rate-*.json"))
	require.NoError(t, err)
	return names
}

func TestAlertRaise_ExpiredRateRecords_ArePruned(t *testing.T) {
	s := alertFixture(t)
	stale := alertInput()
	_, err := s.RecordAlert(t.Context(), stale, alertTime())
	require.NoError(t, err)
	live := alertInput()
	live.Class = AlertClassWakeBackoff
	_, err = s.RecordAlert(t.Context(), live, alertTime().Add(30*time.Minute))
	require.NoError(t, err)
	require.Len(t, rateRecordNames(t, s), 2, "both windows are still open")

	later := alertTime().Add(time.Hour + 31*time.Minute)
	third := alertInput()
	third.Agent = "other-agent"
	_, err = s.RecordAlert(t.Context(), third, later)
	require.NoError(t, err)

	dir, err := s.Path()
	require.NoError(t, err)
	require.NoFileExists(t, filepath.Join(dir, alertRateName(stale.Agent, stale.Class)), "expired record is removed")
	require.NoFileExists(t, filepath.Join(dir, alertRateName(live.Agent, live.Class)), "second expired record is removed")
	require.FileExists(t, filepath.Join(dir, alertRateName(third.Agent, third.Class)), "current record stays")
	rows, err := s.RecentAlerts(t.Context(), 0)
	require.NoError(t, err)
	require.Len(t, rows, 3, "pruning never drops retained diagnostics")
}

func TestAlertRaise_UnexpiredRateRecord_SurvivesPrune(t *testing.T) {
	s := alertFixture(t)
	_, err := s.RecordAlert(t.Context(), alertInput(), alertTime())
	require.NoError(t, err)
	other := alertInput()
	other.Agent = "other-agent"
	_, err = s.RecordAlert(t.Context(), other, alertTime().Add(time.Hour-time.Nanosecond))
	require.NoError(t, err)
	require.Len(t, rateRecordNames(t, s), 2)
	again, err := s.RecordAlert(t.Context(), alertInput(), alertTime().Add(time.Hour-time.Nanosecond))
	require.NoError(t, err)
	require.Equal(t, uint64(2), again.Count)
}

func TestAlertPrune_UnreadableRateRecord_IsLeftForVisibleRefusal(t *testing.T) {
	s := alertFixture(t)
	_, err := s.RecordAlert(t.Context(), alertInput(), alertTime())
	require.NoError(t, err)
	dir, err := s.Path()
	require.NoError(t, err)
	foreign := filepath.Join(dir, "alert-rate-00000000000000000000000000000000.json")
	require.NoError(t, os.WriteFile(foreign, []byte("not json"), 0o600))
	other := alertInput()
	other.Agent = "other-agent"
	_, err = s.RecordAlert(t.Context(), other, alertTime().Add(2*time.Hour))
	require.NoError(t, err)
	require.FileExists(t, foreign, "unreadable records are never deleted blindly")
}

func TestAlertRaise_FormatCharactersInDetailAndFix_AreRefused(t *testing.T) {
	for _, tc := range []struct{ name, text string }{{"bidi override", "safe\u202etext"}, {"zero width space", "safe\u200btext"}} {
		for _, field := range []string{"detail", "fix"} {
			t.Run(tc.name+" in "+field, func(t *testing.T) {
				s := alertFixture(t)
				in := alertInput()
				if field == "detail" {
					in.Detail = tc.text
				} else {
					in.Fix = tc.text
				}
				_, err := s.RecordAlert(t.Context(), in, alertTime())
				require.ErrorIs(t, err, model.ErrInvalidInput)
				dir, pathErr := s.Path()
				require.NoError(t, pathErr)
				_, err = os.Stat(dir)
				require.ErrorIs(t, err, os.ErrNotExist, "refusal creates no state")
			})
		}
	}
}

func TestRemoveData_ExistingAndAbsentRecord_ReportsOutcome(t *testing.T) {
	s := alertFixture(t)
	_, err := s.RecordAlert(t.Context(), alertInput(), alertTime())
	require.NoError(t, err)
	dir, err := s.directory(false)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, closeFile(dir)) })
	name := alertRateName(alertInput().Agent, alertInput().Class)
	require.NoError(t, removeData(dir, name))
	require.ErrorIs(t, removeData(dir, name), os.ErrNotExist)
	path, err := s.Path()
	require.NoError(t, err)
	require.NoFileExists(t, filepath.Join(path, name))
}
