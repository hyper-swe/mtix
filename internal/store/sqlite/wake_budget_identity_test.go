// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/testutil"
	"github.com/stretchr/testify/require"
)

func TestWakeBudgetIdentity_ReadOnlyUntilAdmissionAndRollback(t *testing.T) {
	s, key := budgetDataStore(t)
	state := testutil.OperatorState(t)
	id, err := s.WakeBudgetBoardID(t.Context())
	require.NoError(t, err)
	require.Empty(t, id)
	require.Equal(t, service.DoctorInfo, service.WakeBudgetDoctorCheck(s, state).Run(t.Context()).Status)
	cause := errors.New("admission rejected")
	_, err = s.ClaimWakeBudget(t.Context(), key, time.Minute, func(p model.WakeBudgetProposal) (model.WakeBudgetDecision, error) {
		require.NotEmpty(t, p.BoardCandidate)
		var count int
		require.NoError(t, s.ReadDB().QueryRow("SELECT count(*) FROM meta WHERE key=?", "wake_budget_board_id").Scan(&count))
		require.Zero(t, count, "identity must not precede the admission transaction")
		return model.WakeBudgetDecision{}, cause
	})
	require.ErrorIs(t, err, cause)
	id, err = s.WakeBudgetBoardID(t.Context())
	require.NoError(t, err)
	require.Empty(t, id)
	_, err = s.WriteDB().Exec("CREATE TRIGGER refuse_identity BEFORE INSERT ON meta WHEN NEW.key='wake_budget_board_id' BEGIN SELECT RAISE(ABORT,'identity unavailable'); END")
	require.NoError(t, err)
	_, err = s.ClaimWakeBudget(t.Context(), key, time.Minute, func(model.WakeBudgetProposal) (model.WakeBudgetDecision, error) {
		return model.WakeBudgetDecision{Allow: true}, nil
	})
	require.ErrorContains(t, err, "persist local wake board identity")
	stats, err := s.WakeLedgerStats(t.Context())
	require.NoError(t, err)
	require.Zero(t, stats.Rows, "identity, counter and fence must commit together")
}

func TestWakeBudgetIdentity_ExportAndLedgerNeverCarryLocalUUID(t *testing.T) {
	s, key := budgetDataStore(t)
	result, err := s.ClaimWakeBudget(t.Context(), key, time.Minute, func(model.WakeBudgetProposal) (model.WakeBudgetDecision, error) {
		return model.WakeBudgetDecision{Allow: true}, nil
	})
	require.NoError(t, err)
	id, err := s.WakeBudgetBoardID(t.Context())
	require.NoError(t, err)
	require.NotEmpty(t, id)
	require.Equal(t, id, result.Snapshot.BoardID)
	require.NotEqual(t, id, result.Attempt.Token)
	exported, err := s.Export(t.Context(), "TEST", "test")
	require.NoError(t, err)
	raw, err := json.Marshal(exported)
	require.NoError(t, err)
	require.NotContains(t, string(raw), id)
	require.NotContains(t, string(raw), "wake_budget_board_id")
	var ledger string
	require.NoError(t, s.ReadDB().QueryRow("SELECT agent_id||host_id||uid||event_seq||epoch||claimed_at||outcome||attempt_token FROM wake_ledger").Scan(&ledger))
	require.NotContains(t, ledger, id)
}

func TestWakeBudgetIdentity_InvalidAndConflictingIDsAbortAdmission(t *testing.T) {
	for _, value := range []string{"", "not-a-uuid", "00000000-0000-0000-0000-000000000000", "AAAAAAAA-AAAA-4AAA-8AAA-AAAAAAAAAAAA"} {
		t.Run(value, func(t *testing.T) {
			s, key := budgetDataStore(t)
			_, err := s.WriteDB().Exec("INSERT INTO meta(key,value) VALUES(?,?)", "wake_budget_board_id", value)
			require.NoError(t, err)
			_, err = s.WakeBudgetBoardID(t.Context())
			require.ErrorIs(t, err, model.ErrInvalidInput)
			_, err = s.ClaimWakeBudget(t.Context(), key, time.Minute, func(model.WakeBudgetProposal) (model.WakeBudgetDecision, error) {
				t.Fatal("invalid identity reached admission")
				return model.WakeBudgetDecision{}, nil
			})
			require.ErrorIs(t, err, model.ErrInvalidInput)
		})
	}
	s, key := budgetDataStore(t)
	allow := func(model.WakeBudgetProposal) (model.WakeBudgetDecision, error) {
		return model.WakeBudgetDecision{Allow: true}, nil
	}
	_, err := s.ClaimWakeBudget(t.Context(), key, time.Minute, allow)
	require.NoError(t, err)
	key.EventSeq++
	_, err = s.ClaimWakeBudget(t.Context(), key, time.Minute, func(model.WakeBudgetProposal) (model.WakeBudgetDecision, error) {
		return model.WakeBudgetDecision{Allow: true, BoardID: "00000000-0000-4000-8000-000000000099"}, nil
	})
	require.ErrorIs(t, err, model.ErrConflict)
}
