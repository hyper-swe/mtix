// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service_test

import (
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// MTIX-112.2.2.1.1: a replace import frees the journal tail and the next
// events reuse those seqs. The per-agent returned mark must not keep covering
// them, or a fresh event reads as already returned and the read charges the
// wake budget nothing for it (ADR-007 A12).
func TestInboxRead_AfterReplaceReusesReturnedSeq_ChargesFreshEvent(t *testing.T) {
	f := newWakeBudgetFixture(t, "10")
	b := f.board(t, 1)
	nodes := service.NewNodeService(b.store, &service.NoopBroadcaster{}, &service.StaticConfig{}, slog.Default(), func() time.Time { return f.now })
	prompts := service.NewPromptService(b.store, &service.NoopBroadcaster{}, slog.Default(), func() time.Time { return f.now })
	dropped, err := nodes.CreateNode(t.Context(), &service.CreateNodeRequest{Project: "TEST", Title: "removed by replace", Creator: "sender"})
	require.NoError(t, err)
	require.NoError(t, prompts.AddAnnotation(t.Context(), dropped.ID, "doomed event", "sender", "budget-worker"))

	svc := service.NewInboxService(b.store).WithInboxPolicy(budgetedPolicy(f))
	res, err := svc.InboxRead(t.Context(), "budget-worker", service.InboxReadOptions{})
	require.NoError(t, err)
	require.Len(t, res.Events, 2)
	require.Equal(t, int64(2), credits(t, f, "budget-worker"))
	oldTail := res.Events[1].Seq

	board, err := b.store.Export(t.Context(), "", "")
	require.NoError(t, err)
	kept := board.Nodes[:0]
	for _, n := range board.Nodes {
		if n.ID != dropped.ID {
			kept = append(kept, n)
		}
	}
	board.Nodes, board.NodeCount = kept, len(kept)
	require.NoError(t, sqlite.RecomputeExportChecksum(board))
	_, err = b.store.Import(t.Context(), board, sqlite.ImportModeReplace, false)
	require.NoError(t, err)

	fresh, err := nodes.CreateNode(t.Context(), &service.CreateNodeRequest{Project: "TEST", Title: "after replace", Creator: "sender"})
	require.NoError(t, err)
	require.NoError(t, prompts.AddAnnotation(t.Context(), fresh.ID, "fresh event", "sender", "budget-worker"))
	pending, err := b.store.InboxList(t.Context(), "budget-worker")
	require.NoError(t, err)
	var freshSeq int64
	for _, e := range pending {
		if e.NodeID == fresh.ID {
			freshSeq = e.Seq
		}
	}
	require.NotZero(t, freshSeq)
	require.LessOrEqual(t, freshSeq, oldTail, "premise: the fresh event reuses a freed seq")

	_, err = svc.InboxRead(t.Context(), "budget-worker", service.InboxReadOptions{})
	require.NoError(t, err)
	assert.Equal(t, int64(3), credits(t, f, "budget-worker"), "the fresh event is charged once; consumed totals are not reset")
}
