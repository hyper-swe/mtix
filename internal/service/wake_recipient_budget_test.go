// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service_test

import (
	"errors"
	"os"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
)

func TestWakeBudget_DefaultUnlimited(t *testing.T) {
	f := newWakeBudgetFixture(t, "")
	b := f.board(t, 15)
	require.NoError(t, b.dispatcher(t, true, nil).Pass(t.Context(), b.keys))
	require.Equal(t, int64(15), f.sent.Load())
	require.Equal(t, int64(15), budgetLedgerRows(t, b))
}

func TestWakeBudget_Configured12_13thHeld(t *testing.T) {
	f := newWakeBudgetFixture(t, "12")
	b := f.board(t, 13)
	d := b.dispatcher(t, true, nil)
	require.NoError(t, d.Pass(t.Context(), b.keys))
	require.NoError(t, d.Pass(t.Context(), b.keys))
	require.Equal(t, int64(12), f.sent.Load())
	require.Equal(t, int64(12), budgetLedgerRows(t, b), "the held event must not be claimed")
	entries, err := b.store.InboxList(t.Context(), "budget-worker")
	require.NoError(t, err)
	require.Len(t, entries, 1, "the held event remains unacknowledged")
	require.Equal(t, b.keys[12].EventSeq, entries[0].Seq)
	rows, err := f.alerts.Recent(t.Context(), 0)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, "wake.budget", rows[0].Class)
	require.Contains(t, rows[0].Fix, "mtix config set wake.wakes_per_hour 13")
}

func TestWakeBudget_CrossBoardRecipient_SharedAllowance(t *testing.T) {
	f := newWakeBudgetFixture(t, "12")
	a, b := f.board(t, 6), f.board(t, 7)
	require.NoError(t, a.dispatcher(t, true, nil).Pass(t.Context(), a.keys))
	require.NoError(t, b.dispatcher(t, true, nil).Pass(t.Context(), b.keys))
	require.Equal(t, int64(12), f.sent.Load())
	require.Equal(t, int64(6), budgetLedgerRows(t, a))
	require.Equal(t, int64(6), budgetLedgerRows(t, b))
}

func TestWakeBudget_DuplicateClaims_DoNotSpendAllowance(t *testing.T) {
	f := newWakeBudgetFixture(t, "2")
	b := f.board(t, 3)
	d := b.dispatcher(t, true, nil)
	for _, key := range []model.WakeKey{b.keys[0], b.keys[0], b.keys[1], b.keys[2]} {
		require.NoError(t, d.Pass(t.Context(), []model.WakeKey{key}))
	}
	require.Equal(t, int64(2), f.sent.Load())
	require.Equal(t, int64(2), budgetLedgerRows(t, b))
}

func TestWakeBudget_SendErrors_ChargeExactlyOneAttempt(t *testing.T) {
	f := newWakeBudgetFixture(t, "2")
	b := f.board(t, 3)
	sendErr := errors.New("transport failed after attempt")
	d := b.dispatcher(t, false, sendErr)
	require.ErrorIs(t, d.Pass(t.Context(), b.keys), sendErr)
	require.NoError(t, d.Pass(t.Context(), b.keys), "terminal duplicate attempts and the held event never send")
	require.Equal(t, int64(2), f.sent.Load())
	require.Equal(t, int64(2), budgetLedgerRows(t, b))
}

func TestWakeBudget_ConcurrentBoards_OnlyOneLastSlot(t *testing.T) {
	f := newWakeBudgetFixture(t, "1")
	a, b := f.board(t, 1), f.board(t, 1)
	da, db := a.dispatcher(t, false, nil), b.dispatcher(t, false, nil)
	start := make(chan struct{})
	results := make(chan error, 2)
	var workers sync.WaitGroup
	for _, run := range []func() error{
		func() error { return da.Pass(t.Context(), a.keys) },
		func() error { return db.Pass(t.Context(), b.keys) },
	} {
		workers.Go(func() { <-start; results <- run() })
	}
	close(start)
	workers.Wait()
	close(results)
	for err := range results {
		require.NoError(t, err)
	}
	require.Equal(t, int64(1), f.sent.Load())
	require.Equal(t, int64(1), budgetLedgerRows(t, a)+budgetLedgerRows(t, b))
}

func TestBudgetCounter_StoreRewound_RaisesAlert(t *testing.T) {
	f := newWakeBudgetFixture(t, "12")
	b := f.board(t, 13)
	require.NoError(t, b.dispatcher(t, true, nil).Pass(t.Context(), b.keys[:3]))
	require.NoError(t, b.store.Close())
	earlier, err := os.ReadFile(b.path)
	require.NoError(t, err)
	b.open(t)
	require.NoError(t, b.dispatcher(t, true, nil).Pass(t.Context(), b.keys[3:12]))
	require.Equal(t, int64(12), f.sent.Load())
	require.NoError(t, b.store.Close())
	require.NoError(t, os.WriteFile(b.path, earlier, 0o600))
	b.open(t)
	require.NoError(t, b.dispatcher(t, true, nil).Pass(t.Context(), b.keys[12:]))
	require.Equal(t, int64(12), f.sent.Load(), "a restored board cannot refund protected allowance")
	rows, err := f.alerts.Recent(t.Context(), 0)
	require.NoError(t, err)
	classes := make([]string, 0, len(rows))
	for _, row := range rows {
		classes = append(classes, row.Class)
	}
	require.Contains(t, classes, "budget.rewind")
	require.Contains(t, classes, "wake.budget")
}

func TestBudgetCounter_StoreRestoredBeforeFirstWake_RaisesAlert(t *testing.T) {
	f := newWakeBudgetFixture(t, "12")
	b := f.board(t, 13)
	require.NoError(t, b.store.Close())
	earlier, err := os.ReadFile(b.path)
	require.NoError(t, err)
	b.open(t)
	require.NoError(t, b.dispatcher(t, true, nil).Pass(t.Context(), b.keys[:12]))
	require.NoError(t, b.store.Close())
	require.NoError(t, os.WriteFile(b.path, earlier, 0o600))
	b.open(t)
	require.NoError(t, b.dispatcher(t, true, nil).Pass(t.Context(), b.keys[12:]))
	require.Equal(t, int64(12), f.sent.Load())
	rows, err := f.alerts.Recent(t.Context(), 0)
	require.NoError(t, err)
	classes := make([]string, 0, len(rows))
	for _, row := range rows {
		classes = append(classes, row.Class)
	}
	require.Contains(t, classes, "budget.rewind", "restoring the counter's identity metadata must not hide the rewind")
}
