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

func seqsByEvent(t *testing.T, s *sqlite.Store, agent string) map[string]int64 {
	t.Helper()
	hist, err := s.InboxHistory(context.Background(), agent)
	require.NoError(t, err)
	out := map[string]int64{}
	for _, h := range hist {
		out[h.EventID] = h.Seq
	}
	return out
}

func requireFoldKinds(t *testing.T, s *sqlite.Store, agent string, unknownSeq, timedSeq int64) {
	t.Helper()
	ctx := context.Background()
	info, err := s.InboxAckInfo(ctx, agent, unknownSeq)
	require.NoError(t, err)
	require.Equal(t, sqlite.AckInfo{Kind: sqlite.AckWatermark}, info, "time-unknown ack before the fold")
	info, err = s.InboxAckInfo(ctx, agent, timedSeq)
	require.NoError(t, err)
	require.Equal(t, sqlite.AckSelective, info.Kind)
	require.True(t, info.TimeKnown)
	// Ack everything: the watermark now covers both acks and folds them.
	hist, err := s.InboxHistory(ctx, agent)
	require.NoError(t, err)
	var top int64
	for _, h := range hist {
		top = max(top, h.Seq)
	}
	require.NoError(t, s.InboxAckThrough(ctx, agent, top))
	info, err = s.InboxAckInfo(ctx, agent, unknownSeq)
	require.NoError(t, err)
	require.Equal(t, sqlite.AckInfo{Kind: sqlite.AckWatermark}, info, "a folded time-unknown ack keeps its kind and stays time-unknown")
	info, err = s.InboxAckInfo(ctx, agent, timedSeq)
	require.NoError(t, err)
	require.Equal(t, sqlite.AckSelective, info.Kind, "a folded timed selective ack stays selective")
	require.True(t, info.TimeKnown)
}

// MTIX-112.3: a carried ack with no time, folded by a later through-ack, stays
// a watermark ack of unknown time; a timed selective ack stays selective.
func TestInboxAckThrough_FoldsCarriedUnknownTimeAck_KeepsWatermarkKind(t *testing.T) {
	ctx := context.Background()
	oldS, newS, ids := carryFixture(t)
	carry, err := oldS.ExportInboxCarry(ctx)
	require.NoError(t, err)
	_, err = newS.ImportInboxCarry(ctx, carry)
	require.NoError(t, err)
	seqOf := seqsByEvent(t, newS, "opus")
	requireFoldKinds(t, newS, "opus", seqOf[ids[0]], seqOf[ids[2]])
}

// MTIX-112.3: the same for an ack restored from a transport file whose source
// had compacted away its time.
func TestInboxAckThrough_FoldsRestoredUnknownTimeAck_KeepsWatermarkKind(t *testing.T) {
	source := newTestStore(t)
	first, _, third := addressThree(t, source, "recipient")
	require.NoError(t, source.InboxAckThrough(t.Context(), "recipient", first))
	require.NoError(t, source.InboxAck(t.Context(), "recipient", third))
	file := transportExport(t, source)
	srcSeq := seqsByEvent(t, source, "recipient")
	var unknownID, timedID string
	for id, seq := range srcSeq {
		switch seq {
		case first:
			unknownID = id
		case third:
			timedID = id
		}
	}
	for i := range file.InboxJournal.Events {
		if file.InboxJournal.Events[i].EventID == unknownID {
			file.InboxJournal.Events[i].AckedAt = ""
		}
	}
	dest := newTestStore(t)
	_, err := dest.Import(t.Context(), file, sqlite.ImportModeMerge, false)
	require.NoError(t, err)
	destSeq := seqsByEvent(t, dest, "recipient")
	require.NotZero(t, destSeq[unknownID])
	require.NotZero(t, destSeq[timedID])
	requireFoldKinds(t, dest, "recipient", destSeq[unknownID], destSeq[timedID])
}

// MTIX-112.3: a folded single-event mark covers exactly its own event. The
// event just below an individually acked one (the next journal row) is covered
// by the watermark alone, so it reports a watermark ack and never the selective
// ack's kind or time.
func TestInboxAckThrough_FoldedSelectiveAck_CoversOnlyItsOwnEvent(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	now := time.Now().UTC().Truncate(time.Second)
	// Create every node first so the three inbox events land on adjacent seqs.
	for _, id := range []string{"PROJ-1", "PROJ-2", "PROJ-3"} {
		require.NoError(t, s.CreateNode(ctx, makeRootNode(id, "PROJ", "Task", now)))
	}
	for _, id := range []string{"PROJ-1", "PROJ-2", "PROJ-3"} {
		require.NoError(t, s.SetAnnotations(ctx, id, []model.Annotation{
			{ID: "ann" + id, Author: "worker", Text: "ruling", CreatedAt: now, Addressee: "recipient"}}))
	}
	events, err := s.InboxList(ctx, "recipient")
	require.NoError(t, err)
	require.Len(t, events, 3)
	first, second, third := events[0].Seq, events[1].Seq, events[2].Seq
	require.Equal(t, third-1, second, "adjacent seqs: the fixture must put an event right below the acked one")
	require.NoError(t, s.InboxAck(ctx, "recipient", third))
	require.NoError(t, s.InboxAckThrough(ctx, "recipient", third))
	info, err := s.InboxAckInfo(ctx, "recipient", third)
	require.NoError(t, err)
	require.Equal(t, sqlite.AckSelective, info.Kind, "the individually acked event keeps its kind")
	for _, seq := range []int64{first, second} {
		info, err = s.InboxAckInfo(ctx, "recipient", seq)
		require.NoError(t, err)
		require.Equal(t, sqlite.AckWatermark, info.Kind, "event %d is covered by the watermark mark only", seq)
	}
}
