// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service_test

// Sender admission tests use the recipient fixture's injected clock and real store.
import (
	"errors"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/oplocal"
)

func senderLimit(t *testing.T, f *wakeBudgetFixture, value string) {
	t.Helper()
	require.NoError(t, f.state.UpdateWakeSetting(t.Context(), oplocal.WakeSettingUpdate{Key: "wake.per_sender_per_hour", Value: &value}, nil))
}

func TestWakeSenderBudget_HoldsAndRollingHourReleases(t *testing.T) {
	f := newWakeBudgetFixture(t, "")
	senderLimit(t, f, "2")
	b := f.board(t, 3)
	d := b.dispatcher(t, true, nil)
	require.NoError(t, d.Pass(t.Context(), b.keys))
	require.Equal(t, int64(2), f.sent.Load())
	require.Equal(t, int64(2), budgetLedgerRows(t, b))
	entries, err := b.store.InboxList(t.Context(), "budget-worker")
	require.NoError(t, err)
	require.Len(t, entries, 1)
	rows, err := f.alerts.Recent(t.Context(), 0)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, "wake.budget", rows[0].Class)
	require.Equal(t, "mtix config set wake.per_sender_per_hour 3 --agent budget-worker", rows[0].Fix)
	require.Contains(t, rows[0].Detail, "sender")
	f.now = f.now.Add(time.Hour - time.Nanosecond)
	require.NoError(t, d.Pass(t.Context(), b.keys))
	require.Equal(t, int64(2), f.sent.Load())
	f.now = f.now.Add(time.Nanosecond)
	require.NoError(t, d.Pass(t.Context(), b.keys))
	require.Equal(t, int64(3), f.sent.Load())
}

func TestWakeSenderBudget_CrossBoardAndDuplicate(t *testing.T) {
	f := newWakeBudgetFixture(t, "")
	senderLimit(t, f, "2")
	a, b := f.board(t, 1), f.board(t, 2)
	d := a.dispatcher(t, true, nil)
	require.NoError(t, d.Pass(t.Context(), a.keys))
	require.NoError(t, d.Pass(t.Context(), a.keys))
	require.NoError(t, b.dispatcher(t, true, nil).Pass(t.Context(), b.keys))
	require.Equal(t, int64(2), f.sent.Load())
}

func TestWakeSenderBudget_StoreRewindRetainsAllowance(t *testing.T) {
	f := newWakeBudgetFixture(t, "")
	senderLimit(t, f, "2")
	b := f.board(t, 3)
	require.NoError(t, b.store.Close())
	earlier, err := os.ReadFile(b.path)
	require.NoError(t, err)
	b.open(t)
	require.NoError(t, b.dispatcher(t, true, nil).Pass(t.Context(), b.keys[:2]))
	require.NoError(t, b.store.Close())
	require.NoError(t, os.WriteFile(b.path, earlier, 0o600))
	b.open(t)
	require.NoError(t, b.dispatcher(t, true, nil).Pass(t.Context(), b.keys[2:]))
	require.Equal(t, int64(2), f.sent.Load())
	rows, err := f.alerts.Recent(t.Context(), 0)
	require.NoError(t, err)
	classes := make([]string, 0, len(rows))
	for _, row := range rows {
		classes = append(classes, row.Class)
	}
	require.Contains(t, classes, "budget.rewind")
	require.Contains(t, classes, "wake.budget")
}

func TestWakeSenderBudget_SendersAndRecipientsIndependent(t *testing.T) {
	f := newWakeBudgetFixture(t, "")
	senderLimit(t, f, "1")
	a := f.boardFrom(t, 2, "budget-worker", "sender-a")
	b := f.boardFrom(t, 1, "budget-worker", "sender-b")
	c := f.boardFrom(t, 1, "other-worker", "sender-a")
	for _, board := range []*wakeBudgetBoard{a, b, c} {
		require.NoError(t, board.dispatcher(t, true, nil).Pass(t.Context(), board.keys))
	}
	require.Equal(t, int64(3), f.sent.Load())
}

func TestWakeSenderBudget_ConcurrentBoardsOneLastSlot(t *testing.T) {
	f := newWakeBudgetFixture(t, "")
	senderLimit(t, f, "1")
	a, b := f.board(t, 1), f.board(t, 1)
	da, db := a.dispatcher(t, false, nil), b.dispatcher(t, false, nil)
	start, results := make(chan struct{}), make(chan error, 2)
	for _, run := range []func() error{
		func() error { return da.Pass(t.Context(), a.keys) },
		func() error { return db.Pass(t.Context(), b.keys) },
	} {
		go func() { <-start; results <- run() }()
	}
	close(start)
	for range 2 {
		require.NoError(t, <-results)
	}
	require.Equal(t, int64(1), f.sent.Load())
}

func TestWakeSenderBudget_AttributedAuthorMatchesInbox(t *testing.T) {
	f := newWakeBudgetFixture(t, "")
	senderLimit(t, f, "1")
	a := f.boardFrom(t, 1, "budget-worker", "sender-a")
	b := f.boardFrom(t, 1, "budget-worker", "sender-b")
	// The payload author is authoritative even when the event envelope disagrees.
	_, err := b.store.WriteDB().ExecContext(t.Context(), "UPDATE sync_events SET payload=json_set(payload,'$.author_id',?) WHERE rowid=?", "sender-a", b.keys[0].EventSeq)
	require.NoError(t, err)
	require.NoError(t, a.dispatcher(t, true, nil).Pass(t.Context(), a.keys))
	require.NoError(t, b.dispatcher(t, true, nil).Pass(t.Context(), b.keys))
	require.Equal(t, int64(1), f.sent.Load())
}

func TestWakeSenderBudget_HoldRearmsFromOwnHighWater(t *testing.T) {
	f := newBudgetHoldFixture(t, map[string]string{"wake.per_sender_per_hour": "1"}, nil)
	require.NoError(t, f.d.Pass(t.Context(), []model.WakeKey{f.at(7), f.at(8), f.at(9)}))
	require.Equal(t, []int64{7}, f.sent)
	mark, held := f.registration(t)
	require.Equal(t, int64(7), mark)
	require.True(t, held)
	_, err := wakeConfig(t, f.state, "", true).Set("wake.per_sender_per_hour", "3")
	require.NoError(t, err)
	require.NoError(t, f.d.Pass(t.Context(), []model.WakeKey{f.at(8), f.at(9)}))
	require.Equal(t, []int64{7, 8, 9}, f.sent)
	mark, held = f.registration(t)
	require.Equal(t, int64(9), mark)
	require.False(t, held)
}

func TestWakeSenderBudget_SendErrorsRemainCharged(t *testing.T) {
	f := newWakeBudgetFixture(t, "")
	senderLimit(t, f, "1")
	b := f.board(t, 2)
	failure := errors.New("sender transport attempt failed")
	d := b.dispatcher(t, false, failure)
	require.ErrorIs(t, d.Pass(t.Context(), b.keys), failure)
	require.NoError(t, d.Pass(t.Context(), b.keys))
	require.Equal(t, int64(1), f.sent.Load())
	require.Equal(t, int64(1), budgetLedgerRows(t, b))
}

func TestWakeSenderBudget_ClockRollbackDoesNotRefund(t *testing.T) {
	f := newWakeBudgetFixture(t, "")
	senderLimit(t, f, "1")
	b := f.board(t, 2)
	d := b.dispatcher(t, true, nil)
	require.NoError(t, d.Pass(t.Context(), b.keys[:1]))
	f.now = f.now.Add(-2 * time.Hour)
	require.NoError(t, d.Pass(t.Context(), b.keys[1:]))
	require.Equal(t, int64(1), f.sent.Load())
}
