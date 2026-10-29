// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
)

func TestWakeBudgetIdentity_ReadFailuresAndInvalidDecisionAreContextual(t *testing.T) {
	for _, name := range []string{"cancelled", "closed", "missing meta", "invalid decision"} {
		t.Run(name, func(t *testing.T) {
			s, key := budgetDataStore(t)
			ctx := t.Context()
			switch name {
			case "cancelled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			case "closed":
				require.NoError(t, s.Close())
			case "missing meta":
				_, err := s.WriteDB().Exec("DROP TABLE meta")
				require.NoError(t, err)
			}
			if name == "invalid decision" {
				_, err := s.ClaimWakeBudget(ctx, key, time.Minute, func(model.WakeBudgetProposal) (model.WakeBudgetDecision, error) {
					return model.WakeBudgetDecision{Allow: true, BoardID: "invalid"}, nil
				})
				require.ErrorIs(t, err, model.ErrInvalidInput)
				require.ErrorContains(t, err, "accept wake board identity")
				return
			}
			_, err := s.WakeBudgetBoardID(ctx)
			require.ErrorContains(t, err, "read board identity for wake accounting")
			if name == "cancelled" {
				require.ErrorIs(t, err, context.Canceled)
			}
		})
	}
}

func TestWakeBudgetIdentity_LaterSQLFailureRollsBackIdentityCounterAndFence(t *testing.T) {
	for _, trigger := range []string{
		"CREATE TRIGGER refuse_fence BEFORE INSERT ON wake_ledger BEGIN SELECT RAISE(ABORT,'fence storage failed'); END",
		"CREATE TRIGGER refuse_counter BEFORE INSERT ON wake_budget_counters BEGIN SELECT RAISE(ABORT,'counter storage failed'); END",
	} {
		t.Run(trigger, func(t *testing.T) {
			s, key := budgetDataStore(t)
			_, err := s.WriteDB().Exec(trigger)
			require.NoError(t, err)
			_, err = s.ClaimWakeBudget(t.Context(), key, time.Minute, func(model.WakeBudgetProposal) (model.WakeBudgetDecision, error) {
				return model.WakeBudgetDecision{Allow: true}, nil
			})
			require.Error(t, err)
			id, err := s.WakeBudgetBoardID(t.Context())
			require.NoError(t, err)
			require.Empty(t, id, "identity cannot commit separately from its admitted fence and counter")
			counters, err := s.WakeBudgetCounters(t.Context())
			require.NoError(t, err)
			require.Empty(t, counters)
			ledger, err := s.WakeLedgerStats(t.Context())
			require.NoError(t, err)
			require.Zero(t, ledger.Rows)
		})
	}
}
