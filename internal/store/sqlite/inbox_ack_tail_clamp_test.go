// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// addressOneMore posts one more comment addressed to agent on a fresh node and
// returns its inbox seq.
func addressOneMore(t *testing.T, s *sqlite.Store, agent, id string) int64 {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	require.NoError(t, s.CreateNode(ctx, makeRootNode(id, "PROJ", "Task", now)))
	require.NoError(t, s.SetAnnotations(ctx, id, []model.Annotation{
		{ID: "ann" + id, Author: "worker", Text: "ruling", CreatedAt: now, Addressee: agent},
	}))
	got, err := s.InboxList(ctx, agent)
	require.NoError(t, err)
	require.NotEmpty(t, got)
	return got[len(got)-1].Seq
}

// These are the FR-21 §12.4 prerequisite regressions: an ack referencing a seq
// beyond the journal tail must be REJECTED, never recorded. An unclamped ack
// pre-acknowledges FUTURE events — a message the agent has never seen arrives
// already-seen and is silently dropped from the inbox (observed in the field:
// `inbox ack --through 99` on a tail-8 journal swallowed every subsequent
// addressed comment; wake hooks fired into an empty inbox).

// TestInboxAckThrough_RejectsBeyondTail: the cumulative watermark must not
// advance past the current journal tail.
func TestInboxAckThrough_RejectsBeyondTail(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	s1, s2, s3 := addressThree(t, s, "opus")

	// Beyond-tail watermark is rejected and changes nothing.
	err := s.InboxAckThrough(ctx, "opus", s3+91) // the field case: --through 99
	require.ErrorIs(t, err, model.ErrInvalidInput)
	require.ErrorContains(t, err, "outside the journal")

	got, err := s.InboxList(ctx, "opus")
	require.NoError(t, err)
	require.Len(t, got, 3, "rejected ack must not consume any event")

	// The exact tail is legal (ack everything that exists today)...
	require.NoError(t, s.InboxAckThrough(ctx, "opus", s3))
	got, err = s.InboxList(ctx, "opus")
	require.NoError(t, err)
	require.Empty(t, got)

	// ...and an event that arrives AFTER that ack is still delivered — the
	// exact behavior the unclamped watermark broke.
	s4 := addressOneMore(t, s, "opus", "PROJ-4")
	got, err = s.InboxList(ctx, "opus")
	require.NoError(t, err)
	require.Len(t, got, 1, "post-ack event must be delivered, not pre-acked")
	require.Equal(t, s4, got[0].Seq)

	_, _ = s1, s2
}

// TestInboxAck_RejectsBeyondTailAndNonPositive: the selective ledger ack has
// the same bound — a ledger row for a not-yet-existing seq is the selective
// form of the same future-event swallow — and rejects nonsense seqs.
func TestInboxAck_RejectsBeyondTailAndNonPositive(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	_, _, s3 := addressThree(t, s, "opus")

	cases := []struct {
		name string
		seq  int64
	}{
		{"beyond tail", s3 + 1},
		{"far beyond tail", s3 + 1000},
		{"zero", 0},
		{"negative", -1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := s.InboxAck(ctx, "opus", tc.seq)
			require.ErrorIs(t, err, model.ErrInvalidInput)
		})
	}

	got, err := s.InboxList(ctx, "opus")
	require.NoError(t, err)
	require.Len(t, got, 3, "no rejected ack may consume an event")

	// In-journal seqs remain ackable.
	require.NoError(t, s.InboxAck(ctx, "opus", s3))
}
