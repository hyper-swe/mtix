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

// addressThree posts three comments addressed to agent on three nodes, returning
// their inbox seqs in ascending order.
func addressThree(t *testing.T, s *sqlite.Store, agent string) (int64, int64, int64) {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	for i, id := range []string{"PROJ-1", "PROJ-2", "PROJ-3"} {
		require.NoError(t, s.CreateNode(ctx, makeRootNode(id, "PROJ", "Task", now)))
		require.NoError(t, s.SetAnnotations(ctx, id, []model.Annotation{
			{ID: "ann" + id, Author: "worker", Text: "ruling", CreatedAt: now, Addressee: agent},
		}))
		_ = i
	}
	got, err := s.InboxList(ctx, agent)
	require.NoError(t, err)
	require.Len(t, got, 3, "three addressed comments")
	return got[0].Seq, got[1].Seq, got[2].Seq
}

// TestInboxAck_Selective_KeepsLowerUnacked is the MTIX-55 regression: acking a
// HIGHER seq must NOT drop lower, still-unprocessed events. Under the old
// cumulative watermark, acking s3 hid s1+s2 forever (silent loss) — the
// at-least-once break. Selective ack removes only the acked event.
func TestInboxAck_Selective_KeepsLowerUnacked(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	s1, s2, s3 := addressThree(t, s, "opus")

	// Agent processes s3 out of order and acks ONLY s3.
	require.NoError(t, s.InboxAck(ctx, "opus", s3))

	got, err := s.InboxList(ctx, "opus")
	require.NoError(t, err)
	seqs := []int64{}
	for _, e := range got {
		seqs = append(seqs, e.Seq)
	}
	require.Equal(t, []int64{s1, s2}, seqs, "s1 and s2 must still be delivered; only s3 was acked")
}

// TestInboxAck_Selective_Idempotent: acking the same seq twice is a no-op.
func TestInboxAck_Selective_Idempotent(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	s1, s2, s3 := addressThree(t, s, "opus")

	require.NoError(t, s.InboxAck(ctx, "opus", s2))
	require.NoError(t, s.InboxAck(ctx, "opus", s2)) // again

	got, err := s.InboxList(ctx, "opus")
	require.NoError(t, err)
	require.Len(t, got, 2, "only s2 acked, twice, still two remain")
	require.Equal(t, s1, got[0].Seq)
	require.Equal(t, s3, got[1].Seq)
}

// TestInboxAckThrough_CumulativeBulk: the explicit bulk path acks everything up
// through a seq (watermark), for callers that DO process in order.
func TestInboxAckThrough_CumulativeBulk(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	_, s2, s3 := addressThree(t, s, "opus")

	require.NoError(t, s.InboxAckThrough(ctx, "opus", s2))

	got, err := s.InboxList(ctx, "opus")
	require.NoError(t, err)
	require.Len(t, got, 1, "s1 and s2 acked through; only s3 remains")
	require.Equal(t, s3, got[0].Seq)
}

// TestInboxAckThrough_PrunesLedger: an ack-through past a selectively-acked seq
// absorbs it (the ledger is compacted below the watermark).
func TestInboxAckThrough_PrunesLedger(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	s1, s2, s3 := addressThree(t, s, "opus")

	require.NoError(t, s.InboxAck(ctx, "opus", s1))        // selective
	require.NoError(t, s.InboxAckThrough(ctx, "opus", s3)) // watermark past all

	got, err := s.InboxList(ctx, "opus")
	require.NoError(t, err)
	require.Empty(t, got, "everything acked through s3")
	_ = s2
}
