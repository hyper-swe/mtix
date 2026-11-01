// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
	"github.com/hyper-swe/mtix/internal/testutil"
)

func budgetDataStore(t *testing.T) (*sqlite.Store, model.WakeKey) {
	t.Helper()
	testutil.OperatorState(t)
	s, err := sqlite.New(t.TempDir(), slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, s.Close()) })
	return s, model.WakeKey{AgentID: "worker", HostID: "host", UID: "user", EventSeq: 1, Epoch: 1}
}

func TestWakeBudgetStore_InvalidOrRefusedClaim_NeverWritesLedger(t *testing.T) {
	cause := errors.New("admission unavailable")
	for _, name := range []string{"invalid key", "nil admission", "cancelled", "negative floor", "admission error", "counter exhausted"} {
		t.Run(name, func(t *testing.T) {
			s, key := budgetDataStore(t)
			ctx := t.Context()
			admit := func(model.WakeBudgetProposal) (model.WakeBudgetDecision, error) {
				return model.WakeBudgetDecision{Allow: true}, nil
			}
			want := model.ErrInvalidInput
			switch name {
			case "invalid key":
				key.EventSeq = 0
			case "nil admission":
				admit = nil
			case "cancelled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
				want = context.Canceled
			case "negative floor":
				admit = func(model.WakeBudgetProposal) (model.WakeBudgetDecision, error) {
					return model.WakeBudgetDecision{Floor: -1}, nil
				}
			case "admission error":
				admit = func(model.WakeBudgetProposal) (model.WakeBudgetDecision, error) {
					return model.WakeBudgetDecision{}, cause
				}
				want = cause
			case "counter exhausted":
				_, err := s.WriteDB().ExecContext(ctx, "INSERT INTO wake_budget_counters(agent_id,host_id,uid,total) VALUES(?,?,?,?)", key.AgentID, key.HostID, key.UID, int64(math.MaxInt64))
				require.NoError(t, err)
			}
			_, err := s.ClaimWakeBudget(ctx, key, time.Minute, admit)
			require.ErrorIs(t, err, want)
			stats, err := s.WakeLedgerStats(t.Context())
			require.NoError(t, err)
			require.Zero(t, stats.Rows)
		})
	}
}

type budgetDataFault struct {
	name     string
	setup    []string
	admitted bool
}

func budgetDataFaults() []budgetDataFault {
	return []budgetDataFault{
		{"counter query", []string{"DROP TABLE wake_budget_counters"}, false},
		{"ack query", []string{"DROP TABLE agent_inbox_ack"}, false},
		{"ledger query", []string{"DROP TABLE wake_ledger"}, false},
		{"negative ack cursor", nil, false},
		{"counter write", []string{"CREATE TRIGGER reject_budget_counter BEFORE INSERT ON wake_budget_counters BEGIN SELECT RAISE(ABORT,'counter write fixture'); END"}, true},
		{"ledger write", []string{"CREATE TRIGGER reject_budget_ledger BEFORE INSERT ON wake_ledger BEGIN SELECT RAISE(ABORT,'ledger write fixture'); END"}, true},
	}
}

func TestWakeBudgetStore_StorageFailures_PropagateAndRollback(t *testing.T) {
	for _, tc := range budgetDataFaults() {
		t.Run(tc.name, func(t *testing.T) {
			s, key := budgetDataStore(t)
			if tc.name == "negative ack cursor" {
				_, err := s.WriteDB().ExecContext(t.Context(), "INSERT INTO agent_inbox_cursor(agent_id,cursor) VALUES(?,?)", key.AgentID, -1)
				require.NoError(t, err)
			}
			for _, statement := range tc.setup {
				require.NoError(t, s.WithTx(t.Context(), func(tx *sql.Tx) error { _, err := tx.ExecContext(t.Context(), statement); return err }))
			}
			called := false
			_, err := s.ClaimWakeBudget(t.Context(), key, time.Minute, func(model.WakeBudgetProposal) (model.WakeBudgetDecision, error) {
				called = true
				return model.WakeBudgetDecision{Allow: true}, nil
			})
			require.Error(t, err)
			require.Equal(t, tc.admitted, called)
			if tc.admitted {
				stats, err := s.WakeLedgerStats(t.Context())
				require.NoError(t, err)
				require.Zero(t, stats.Rows, "a failed counter or ledger write rolls back the whole fence")
			}
		})
	}
}

func TestWakeBudgetStore_FloorHoldAndLeaseRetry_StayAtomic(t *testing.T) {
	s, key := budgetDataStore(t)
	now := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	s.SetClock(func() time.Time { return now })
	hold, err := s.ClaimWakeBudget(t.Context(), key, time.Minute, func(model.WakeBudgetProposal) (model.WakeBudgetDecision, error) {
		return model.WakeBudgetDecision{Floor: 7}, nil
	})
	require.NoError(t, err)
	require.True(t, hold.Held)
	require.False(t, hold.Won)
	require.Equal(t, int64(7), hold.Snapshot.Total)
	allow := func(model.WakeBudgetProposal) (model.WakeBudgetDecision, error) {
		return model.WakeBudgetDecision{Allow: true, Floor: 7}, nil
	}
	first, err := s.ClaimWakeBudget(t.Context(), key, time.Minute, allow)
	require.NoError(t, err)
	require.True(t, first.Won)
	duplicate, err := s.ClaimWakeBudget(t.Context(), key, time.Minute, allow)
	require.NoError(t, err)
	require.False(t, duplicate.Won)
	require.Equal(t, int64(8), duplicate.Snapshot.Total)
	now = now.Add(time.Minute)
	retry, err := s.ClaimWakeBudget(t.Context(), key, time.Minute, allow)
	require.NoError(t, err)
	require.True(t, retry.Won)
	require.NotEqual(t, first.Attempt.Token, retry.Attempt.Token)
	require.Equal(t, int64(9), retry.Snapshot.Total)
}
