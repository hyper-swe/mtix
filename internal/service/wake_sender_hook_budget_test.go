// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Hook-delivered events share InboxList attribution and independent sender limits.
package service_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
)

func TestWakeSenderBudget_HookDeliveryUsesInboxAuthor(t *testing.T) {
	for _, tc := range []struct {
		name            string
		op              model.OpType
		payload, author string
	}{
		{"other addressee", model.OpComment, `{"to":"other","author_id":"sender-b"}`, "sender-b"},
		{"non-comment", model.OpCreateNode, `{}`, "sender-b"},
		{"non-comment malformed", model.OpCreateNode, `{`, "sender-b"},
		{"non-comment wrong author type", model.OpCreateNode, `{"author_id":42}`, "sender-b"},
		{"non-comment payload author", model.OpCreateNode, `{"author_id":"sender-c"}`, "sender-c"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newWakeBudgetFixture(t, "")
			senderLimit(t, f, "1")
			a := f.boardFrom(t, 1, "budget-worker", "sender-a")
			b := hookSenderBoard(t, f, tc.op, tc.payload)
			entries, err := b.store.InboxList(t.Context(), "budget-worker")
			require.NoError(t, err)
			require.Len(t, entries, 2)
			require.Equal(t, tc.author, entries[0].Author)
			require.NoError(t, a.dispatcher(t, true, nil).Pass(t.Context(), a.keys))
			require.NoError(t, b.dispatcher(t, true, nil).Pass(t.Context(), b.keys))
			require.Equal(t, int64(2), f.sent.Load(), "different hook sender gets its own allowance")
			require.Equal(t, int64(1), budgetLedgerRows(t, b), "second hook event is held without a claim")
			entries, err = b.store.InboxList(t.Context(), "budget-worker")
			require.NoError(t, err)
			require.Len(t, entries, 1, "over-limit hook remains unacknowledged")
			require.Equal(t, b.keys[1].EventSeq, entries[0].Seq)
		})
	}
}

func hookSenderBoard(t *testing.T, f *wakeBudgetFixture, op model.OpType, payload string) *wakeBudgetBoard {
	t.Helper()
	b := f.boardFrom(t, 2, "budget-worker", "sender-b")
	for _, key := range b.keys {
		_, err := b.store.WriteDB().ExecContext(t.Context(), "UPDATE sync_events SET op_type=?,payload=? WHERE rowid=?", op, payload, key.EventSeq)
		require.NoError(t, err)
		_, err = b.store.WriteDB().ExecContext(t.Context(), `INSERT INTO inbox_deliveries(agent_id,event_seq,hook_name,delivered_at) VALUES(?,?,?,?)`, key.AgentID, key.EventSeq, "sender-hook", "2026-10-27T12:00:00Z")
		require.NoError(t, err)
	}
	return b
}

func TestWakeSenderBudget_MalformedHookRetainsHopGuard(t *testing.T) {
	for _, payload := range []string{`{`, `{"author_id":42}`} {
		t.Run(payload, func(t *testing.T) {
			f := newWakeBudgetFixture(t, "")
			senderLimit(t, f, "1")
			b := hookSenderBoard(t, f, model.OpComment, payload)
			// The existing hop parser refuses malformed comments before admission.
			err := b.dispatcher(t, true, nil).Pass(t.Context(), b.keys)
			require.ErrorContains(t, err, "read candidate chain depth")
			require.Zero(t, f.sent.Load())
			require.Zero(t, budgetLedgerRows(t, b))
			entries, err := b.store.InboxList(t.Context(), "budget-worker")
			require.NoError(t, err)
			require.Len(t, entries, 2)
			require.Equal(t, "sender-b", entries[0].Author)
		})
	}
}
