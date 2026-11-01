// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/oplocal"
	"github.com/hyper-swe/mtix/internal/service"
)

func newWakeBackoffFixture(t *testing.T) *wakeBudgetFixture {
	t.Helper()
	f := newWakeBudgetFixture(t, "")
	threshold := "5"
	require.NoError(t, f.state.UpdateWakeSetting(t.Context(), oplocal.WakeSettingUpdate{Key: "wake.backoff_after_unacked", Value: &threshold}, nil))
	return f
}

func TestBackoff_FiveUnacked_DoublesToThirtyMin(t *testing.T) {
	f := newWakeBackoffFixture(t)
	b := f.board(t, 12)
	require.NoError(t, b.dispatcher(t, false, nil).Pass(t.Context(), b.keys[:5]))
	require.Equal(t, int64(5), f.sent.Load())
	for i, minutes := range []int{1, 2, 4, 8, 16, 30, 30} {
		next := b.keys[5+i : 6+i]
		// Reconstruct the dispatcher on every attempt: the deadline is durable.
		require.NoError(t, b.dispatcher(t, false, nil).Pass(t.Context(), next))
		require.Equal(t, int64(5+i), f.sent.Load(), "the next wake waits %d minutes", minutes)
		f.now = f.now.Add(time.Duration(minutes)*time.Minute - time.Nanosecond)
		require.NoError(t, b.dispatcher(t, false, nil).Pass(t.Context(), next))
		require.Equal(t, int64(5+i), f.sent.Load())
		f.now = f.now.Add(time.Nanosecond)
		require.NoError(t, b.dispatcher(t, false, nil).Pass(t.Context(), next))
		require.Equal(t, int64(6+i), f.sent.Load())
	}
	require.Equal(t, int64(12), budgetLedgerRows(t, b))
	rows, err := f.alerts.Recent(t.Context(), 0)
	require.NoError(t, err)
	require.NotEmpty(t, rows)
	for _, row := range rows {
		require.Equal(t, "wake.backoff", row.Class)
		require.Contains(t, row.Fix, "mtix config set wake.backoff_after_unacked")
		require.Regexp(t, `\[chain: event [0-9]+, [0-9]+ unacked wakes\]`, row.Detail, "the trip records its chain identity")
	}
}

func TestBackoff_Ack_ResetsAcrossBoards(t *testing.T) {
	f := newWakeBackoffFixture(t)
	a, b := f.board(t, 4), f.board(t, 3)
	require.NoError(t, a.dispatcher(t, false, nil).Pass(t.Context(), a.keys[:3]))
	require.NoError(t, b.dispatcher(t, false, nil).Pass(t.Context(), b.keys[:2]))
	require.NoError(t, a.dispatcher(t, false, nil).Pass(t.Context(), a.keys[3:]))
	require.Equal(t, int64(5), f.sent.Load())
	require.NoError(t, service.NewInboxService(b.store).InboxAck(t.Context(), "budget-worker", b.keys[0].EventSeq))
	require.NoError(t, a.dispatcher(t, false, nil).Pass(t.Context(), a.keys[3:]))
	require.Equal(t, int64(6), f.sent.Load(), "a new ack on another board resets the host recipient immediately")
}

func TestBackoff_Ack_AlreadyAcknowledgedDoesNotReset(t *testing.T) {
	for _, cumulative := range []bool{false, true} {
		t.Run(map[bool]string{false: "selective", true: "cumulative"}[cumulative], func(t *testing.T) {
			f := newWakeBackoffFixture(t)
			b := f.board(t, 7)
			ack := func() error {
				return service.NewInboxService(b.store).InboxAck(t.Context(), "budget-worker", b.keys[0].EventSeq)
			}
			if cumulative {
				ack = func() error {
					return service.NewInboxService(b.store).InboxAckThrough(t.Context(), "budget-worker", b.keys[0].EventSeq)
				}
			}
			require.NoError(t, ack())
			require.NoError(t, b.dispatcher(t, false, nil).Pass(t.Context(), b.keys[1:6]))
			require.NoError(t, ack())
			require.NoError(t, b.dispatcher(t, false, nil).Pass(t.Context(), b.keys[6:]))
			require.Equal(t, int64(5), f.sent.Load())
			require.Equal(t, int64(5), budgetLedgerRows(t, b))
		})
	}
}

func TestBackoff_Ack_LowerSelectiveSequenceResets(t *testing.T) {
	f := newWakeBackoffFixture(t)
	b := f.board(t, 7)
	require.NoError(t, b.store.InboxAck(t.Context(), "budget-worker", b.keys[6].EventSeq))
	require.NoError(t, b.dispatcher(t, false, nil).Pass(t.Context(), b.keys[:5]))
	require.NoError(t, b.dispatcher(t, false, nil).Pass(t.Context(), b.keys[5:6]))
	require.Equal(t, int64(5), f.sent.Load())
	require.NoError(t, service.NewInboxService(b.store).InboxAck(t.Context(), "budget-worker", b.keys[0].EventSeq))
	require.NoError(t, b.dispatcher(t, false, nil).Pass(t.Context(), b.keys[5:6]))
	require.Equal(t, int64(6), f.sent.Load(), "a lower newly acknowledged sequence is still a new ack")
}

func TestBackoff_Ack_CumulativeNewCoverageResets(t *testing.T) {
	f := newWakeBackoffFixture(t)
	b := f.board(t, 6)
	require.NoError(t, b.dispatcher(t, false, nil).Pass(t.Context(), b.keys[:5]))
	require.NoError(t, b.dispatcher(t, false, nil).Pass(t.Context(), b.keys[5:]))
	require.Equal(t, int64(5), f.sent.Load())
	require.NoError(t, service.NewInboxService(b.store).InboxAckThrough(t.Context(), "budget-worker", b.keys[4].EventSeq))
	require.NoError(t, b.dispatcher(t, false, nil).Pass(t.Context(), b.keys[5:]))
	require.Equal(t, int64(6), f.sent.Load())
}

func TestBackoff_HeldCandidate_NeverClaimedOrAcknowledged(t *testing.T) {
	f := newWakeBackoffFixture(t)
	b := f.board(t, 6)
	d := b.dispatcher(t, false, nil)
	require.NoError(t, d.Pass(t.Context(), b.keys[:5]))
	for range 3 {
		require.NoError(t, d.Pass(t.Context(), []model.WakeKey{b.keys[5]}))
	}
	require.Equal(t, int64(5), budgetLedgerRows(t, b))
	entries, err := b.store.InboxList(t.Context(), "budget-worker")
	require.NoError(t, err)
	require.Len(t, entries, 6, "dispatcher holds never consume inbox events")
}

func TestBackoff_Ack_AfterRestoredCoverageResets(t *testing.T) {
	f := newWakeBackoffFixture(t)
	b := f.board(t, 7)
	require.NoError(t, b.store.InboxAckThrough(t.Context(), "budget-worker", b.keys[0].EventSeq))
	require.NoError(t, b.dispatcher(t, false, nil).Pass(t.Context(), b.keys[1:6]))
	require.NoError(t, b.dispatcher(t, false, nil).Pass(t.Context(), b.keys[6:]))
	require.Equal(t, int64(5), f.sent.Load())
	// Simulate the acknowledgement portion of an older store snapshot.
	_, err := b.store.WriteDB().ExecContext(t.Context(), "DELETE FROM agent_inbox_ack WHERE agent_id=?", "budget-worker")
	require.NoError(t, err)
	_, err = b.store.WriteDB().ExecContext(t.Context(), "DELETE FROM agent_inbox_cursor WHERE agent_id=?", "budget-worker")
	require.NoError(t, err)
	require.NoError(t, service.NewInboxService(b.store).InboxAck(t.Context(), "budget-worker", b.keys[0].EventSeq))
	require.NoError(t, b.dispatcher(t, false, nil).Pass(t.Context(), b.keys[6:]))
	require.Equal(t, int64(6), f.sent.Load(), "a genuinely new effective ack is not hidden by an older higher coverage mark")
}

func TestBackoff_Ack_RestoredCoverageEqualsEarlierMarkStillResets(t *testing.T) {
	f := newWakeBackoffFixture(t)
	b := f.board(t, 7)
	require.NoError(t, b.store.InboxAck(t.Context(), "budget-worker", b.keys[0].EventSeq))
	require.NoError(t, b.dispatcher(t, false, nil).Pass(t.Context(), b.keys[1:6]))
	require.NoError(t, b.dispatcher(t, false, nil).Pass(t.Context(), b.keys[6:]))
	require.Equal(t, int64(5), f.sent.Load())
	_, err := b.store.WriteDB().ExecContext(t.Context(), "DELETE FROM agent_inbox_ack WHERE agent_id=?", "budget-worker")
	require.NoError(t, err)
	require.NoError(t, service.NewInboxService(b.store).InboxAck(t.Context(), "budget-worker", b.keys[0].EventSeq))
	require.NoError(t, b.dispatcher(t, false, nil).Pass(t.Context(), b.keys[6:]))
	require.Equal(t, int64(6), f.sent.Load(), "equal ordinal marks do not make a new effective acknowledgement idempotent")
}
