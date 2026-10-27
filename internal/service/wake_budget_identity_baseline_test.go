// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/oplocal"
	"github.com/hyper-swe/mtix/internal/service"
)

func TestWakeBudget_IdentityLessUnseenPathBaselinesWithoutRefundOrHistoryCharge(t *testing.T) {
	f := newWakeBackoffFixture(t)
	s := newBudgetSequence(t, f)
	s.step("wake")
	s.step("wake")
	oldID, err := s.boards["A"].board.store.WakeBudgetBoardID(t.Context())
	require.NoError(t, err)
	_, err = s.boards["A"].board.store.WriteDB().Exec("DELETE FROM meta WHERE key=?", "wake_budget_board_id")
	require.NoError(t, err)
	s.step("move:B")
	// The old UUID cannot be inferred at an unseen path. Only new admissions charge.
	s.step("wake")
	s.assertAccounting()
	newID, err := s.boards["B"].board.store.WakeBudgetBoardID(t.Context())
	require.NoError(t, err)
	require.NotEqual(t, oldID, newID)
	s.step("move:A")
	s.step("wake")
	s.assertAccounting()
	m, _, err := f.state.ReadWakeBudget(s.boards["A"].board.keys[0].Recipient())
	require.NoError(t, err)
	require.Equal(t, int64(2), m.Boards[oldID].Total, "rebinding a path must retain its earlier lineage floor")
}

func TestWakeBudget_LegacyMirrorAssociationPreservesCreditsDebtAndPathFloor(t *testing.T) {
	f := newWakeBackoffFixture(t)
	s := newBudgetSequence(t, f)
	s.step("wake")
	b := s.boards["A"].board
	require.NoError(t, f.state.UpdateWakeBudget(t.Context(), b.keys[0].Recipient(), func(m *oplocal.WakeBudgetMirror, save func() error) error {
		for path, row := range m.Sources {
			row.BoardID = ""
			m.Sources[path] = row
		}
		m.Boards = nil
		return save()
	}))
	_, err := b.store.WriteDB().Exec("UPDATE wake_budget_counters SET total=0")
	require.NoError(t, err)
	s.step("wake")
	s.assertAccounting()
	m, _, err := f.state.ReadWakeBudget(b.keys[0].Recipient())
	require.NoError(t, err)
	require.Positive(t, m.Rewinds)
	require.Equal(t, 1, m.Version, "the additive mirror fields must preserve version1 readers")
}

func TestWakeBudget_DoctorOnUnseenCopyUsesBoardFloorAndDoesNotCreateIdentity(t *testing.T) {
	f := newWakeBackoffFixture(t)
	s := newBudgetSequence(t, f)
	s.step("wake")
	s.step("copy:B")
	s.step("wake")
	b := s.boards["B"].board
	result := service.WakeBudgetDoctorCheck(b.store, f.state).Run(t.Context())
	require.Equal(t, service.DoctorWarn, result.Status)
	require.Equal(t, 1, result.Data["lower_store_counters"])
	before, err := b.store.WakeBudgetCounters(t.Context())
	require.NoError(t, err)
	require.Equal(t, int64(1), before[0].Total, "doctor must not heal a copied counter")
	_, err = b.store.WriteDB().Exec("DELETE FROM meta WHERE key=?", "wake_budget_board_id")
	require.NoError(t, err)
	result = service.WakeBudgetDoctorCheck(b.store, f.state).Run(t.Context())
	require.Contains(t, result.Detail, "Identity-less restores may under-count history, never over-count")
	id, err := b.store.WakeBudgetBoardID(t.Context())
	require.NoError(t, err)
	require.Empty(t, id, "doctor cannot invent identity at an unseen path")
}
