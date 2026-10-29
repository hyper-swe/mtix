// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Tests injected entropy and SQL-result failures at the fenced persistence boundary.
package sqlite

import (
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
)

func TestClaimWake_FenceGenerationFailure_PreservesCauseAndRows(t *testing.T) {
	s, err := New(t.TempDir(), slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, s.Close()) })
	key := model.WakeKey{AgentID: "test-agent", HostID: "test-host", UID: "test-uid", EventSeq: 1, Epoch: 0}
	cause := errors.New("entropy unavailable")
	for _, tc := range []struct {
		name string
		err  error
		want error
	}{{"entropy", cause, cause}, {"empty token", nil, model.ErrConflict}} {
		t.Run(tc.name, func(t *testing.T) {
			claim, won, err := s.claimWake(t.Context(), key, time.Minute, func() (string, error) { return "", tc.err })
			require.ErrorIs(t, err, tc.want)
			require.False(t, won)
			require.Empty(t, claim.Token)
			stats, err := s.WakeLedgerStats(t.Context())
			require.NoError(t, err)
			require.Equal(t, model.WakeLedgerStats{}, stats)
		})
	}
}

type wakeSQLResult struct {
	count int64
	err   error
}

func (r wakeSQLResult) LastInsertId() (int64, error) { return 0, nil }
func (r wakeSQLResult) RowsAffected() (int64, error) { return r.count, r.err }

func TestWakeWriteResult_FailedOrUnexpectedCount_NeverSucceeds(t *testing.T) {
	cause := errors.New("storage result unavailable")
	for _, tc := range []struct {
		name   string
		result wakeSQLResult
		exec   error
		want   error
	}{
		{"exec error", wakeSQLResult{}, cause, cause},
		{"result error", wakeSQLResult{err: cause}, nil, cause},
		{"no row", wakeSQLResult{}, nil, model.ErrConflict},
		{"several rows", wakeSQLResult{count: 2}, nil, model.ErrConflict},
		{"one row", wakeSQLResult{count: 1}, nil, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := checkWakeWrite(tc.result, tc.exec, "persist wake")
			if tc.want == nil {
				require.NoError(t, err)
			} else {
				require.ErrorIs(t, err, tc.want)
			}
		})
	}
}

func TestWakeFenceMigration_AbsentTable_ReportsStorageFailure(t *testing.T) {
	s, err := New(t.TempDir(), slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, s.Close()) })
	_, err = s.WriteDB().Exec("DROP TABLE wake_ledger")
	require.NoError(t, err)
	require.ErrorContains(t, s.initializeWakeLedger(t.Context()), "add wake attempt fence")
}
