// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Tests counted operator diagnostics with deterministic observation times.
package oplocal

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
)

func alertFixture(t *testing.T) *State {
	t.Helper()
	root, err := filepath.EvalSymlinks(safeFixtureRoot(t))
	require.NoError(t, err)
	env := Env{GOOS: runtime.GOOS, Home: root, Values: map[string]string{}}
	env.Values["APPDATA"] = filepath.Join(root, "config")
	return New(env)
}

func alertTime() time.Time { return time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC) }
func alertInput() Alert {
	return Alert{Agent: "test-agent", Class: "wake.hop-depth", Detail: "guard reported", Fix: "mtix config set wake.max_hop_depth 64"}
}

func TestAlertRaise_SameClassWithinHour_CountIncrements(t *testing.T) {
	s := alertFixture(t)
	first, err := s.RecordAlert(t.Context(), alertInput(), alertTime())
	require.NoError(t, err)
	require.Equal(t, uint64(1), first.Count)
	restarted := New(s.env)
	second, err := restarted.RecordAlert(t.Context(), alertInput(), alertTime().Add(time.Minute))
	require.NoError(t, err)
	require.Equal(t, uint64(2), second.Count)
	require.Equal(t, first.ID, second.ID)
	rows, err := restarted.RecentAlerts(t.Context(), 0)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, second, rows[0])
}

func TestAlertRaise_ClockBoundaries_PreservesWindows(t *testing.T) {
	for _, tc := range []struct {
		name  string
		delta time.Duration
		count uint64
		same  bool
	}{{"before", time.Hour - time.Nanosecond, 2, true}, {"exact", time.Hour, 1, false}, {"backward", -time.Minute, 2, true}} {
		t.Run(tc.name, func(t *testing.T) {
			s := alertFixture(t)
			first, err := s.RecordAlert(t.Context(), alertInput(), alertTime())
			require.NoError(t, err)
			next, err := s.RecordAlert(t.Context(), alertInput(), alertTime().Add(tc.delta))
			require.NoError(t, err)
			require.Equal(t, tc.count, next.Count)
			require.Equal(t, tc.same, first.ID == next.ID)
			require.False(t, next.LastAt.Before(first.LastAt))
		})
	}
}

func TestAlertRaise_DistinctKeys_IsolatesCounts(t *testing.T) {
	s := alertFixture(t)
	for _, input := range []Alert{alertInput(), {Agent: "other-agent", Class: "wake.hop-depth"}, {Agent: "test-agent", Class: "wake.backoff"}} {
		row, err := s.RecordAlert(t.Context(), input, alertTime())
		require.NoError(t, err)
		require.Equal(t, uint64(1), row.Count)
	}
	rows, err := s.RecentAlerts(t.Context(), 0)
	require.NoError(t, err)
	require.Len(t, rows, 3)
}

func TestAlertRaise_InvalidInput_RefusesBeforeStateCreation(t *testing.T) {
	for _, tc := range []struct {
		name  string
		input Alert
	}{{"agent-empty", Alert{Class: "wake.backoff"}}, {"unknown-class", Alert{Agent: "test", Class: "unknown"}}, {"invalid-text", Alert{Agent: "test", Class: "wake.backoff", Detail: "\xff"}}, {"control-text", Alert{Agent: "test", Class: "wake.backoff", Detail: "\x1b[0m"}}} {
		t.Run(tc.name, func(t *testing.T) {
			s := alertFixture(t)
			_, err := s.RecordAlert(t.Context(), tc.input, alertTime())
			require.ErrorIs(t, err, model.ErrInvalidInput)
			dir, pathErr := s.Path()
			require.NoError(t, pathErr)
			_, err = os.Stat(dir)
			require.ErrorIs(t, err, os.ErrNotExist)
		})
	}
}

func TestAlertRecent_AbsentAndLimits_ReadOnly(t *testing.T) {
	s := alertFixture(t)
	rows, err := s.RecentAlerts(t.Context(), 0)
	require.NoError(t, err)
	require.Empty(t, rows)
	dir, err := s.Path()
	require.NoError(t, err)
	_, err = os.Stat(dir)
	require.ErrorIs(t, err, os.ErrNotExist)
	_, err = s.RecentAlerts(t.Context(), -1)
	require.ErrorIs(t, err, model.ErrInvalidInput)
	for i := range 3 {
		in := alertInput()
		in.Agent = string(rune('a' + i))
		_, err = s.RecordAlert(t.Context(), in, alertTime().Add(time.Duration(i)*time.Minute))
		require.NoError(t, err)
	}
	rows, err = s.RecentAlerts(t.Context(), 2)
	require.NoError(t, err)
	require.Len(t, rows, 2)
	require.Equal(t, "c", rows[0].Agent)
}
