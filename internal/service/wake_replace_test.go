// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service_test

import (
	"fmt"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

type wakeReplaceFixture struct {
	*scanFixture
	board        *sqlite.ExportData
	prompts      *service.PromptService
	survivorTail int64
	droppedTail  int64
	oldSends     []int64
}

func newWakeReplaceFixture(t *testing.T, count int, settleTail bool) wakeReplaceFixture {
	t.Helper()
	f := newScanFixture(t)
	_, err := wakeConfig(t, f.state, "", true).Set("wake.backoff_after_unacked", "1000")
	require.NoError(t, err)
	// Exercise the production hop read and budget claim, not scanStore's overrides.
	f.d = f.dispatcherOver(f.store)
	survivorTail := f.addEvent("sender")
	require.NoError(t, f.scanner(always).Scan(t.Context()))
	require.Equal(t, []int64{survivorTail}, f.adapter.seqs())
	board, err := f.store.Export(t.Context(), "", "")
	require.NoError(t, err)
	nodes := service.NewNodeService(f.store, &service.NoopBroadcaster{}, &service.StaticConfig{}, slog.Default(), time.Now)
	prompts := service.NewPromptService(f.store, &service.NoopBroadcaster{}, slog.Default(), time.Now)
	dropped, err := nodes.CreateNode(t.Context(), &service.CreateNodeRequest{Project: "PROJ", Title: "removed tail", Creator: "sender"})
	require.NoError(t, err)
	for i := range count {
		require.NoError(t, prompts.AddAnnotation(t.Context(), dropped.ID, fmt.Sprintf("removed %d", i), "sender", f.key.AgentID))
	}
	if settleTail {
		require.NoError(t, f.scanner(always).Scan(t.Context()))
	}
	oldSends := f.adapter.seqs()
	droppedTail, err := f.store.JournalTail(t.Context())
	require.NoError(t, err)
	if settleTail {
		require.Len(t, oldSends, count+1)
		require.Equal(t, droppedTail, oldSends[len(oldSends)-1])
	}
	return wakeReplaceFixture{scanFixture: f, board: board, prompts: prompts, survivorTail: survivorTail, droppedTail: droppedTail, oldSends: oldSends}
}

func (f wakeReplaceFixture) replaceAndAppend(t *testing.T, count int) []int64 {
	t.Helper()
	_, err := f.store.Import(t.Context(), f.board, sqlite.ImportModeReplace, false)
	require.NoError(t, err)
	tail, err := f.store.JournalTail(t.Context())
	require.NoError(t, err)
	require.Equal(t, f.survivorTail, tail, "the real replace removed the task holding the newest journal rows")
	for i := range count {
		require.NoError(t, f.prompts.AddAnnotation(t.Context(), "PROJ-1", fmt.Sprintf("fresh %d", i), "sender", f.key.AgentID))
	}
	events, err := f.store.InboxList(t.Context(), f.key.AgentID)
	require.NoError(t, err)
	var fresh []int64
	for _, event := range events {
		if event.Seq > f.survivorTail {
			require.Equal(t, "PROJ-1", event.NodeID)
			fresh = append(fresh, event.Seq)
		}
	}
	require.Len(t, fresh, count)
	require.Equal(t, f.survivorTail+1, fresh[0])
	require.Equal(t, f.droppedTail, fresh[len(fresh)-1], "fresh mail reuses the old addressed event's exact sequence")
	return fresh
}

func TestWakeScan_ReplaceReusesJournalSequences_FreshMailWakesOnce(t *testing.T) {
	for _, removedComments := range []int{1, 2} {
		t.Run(fmt.Sprintf("removed_comments_%d", removedComments), func(t *testing.T) {
			f := newWakeReplaceFixture(t, removedComments, true)
			fresh := f.replaceAndAppend(t, removedComments+1)
			mark, hold := f.mark()
			t.Logf("surviving tail=%d, removed tail=%d, registration mark=%d, hold=%t, fresh=%v", f.survivorTail, f.droppedTail, mark, hold, fresh)
			require.NoError(t, f.scanner(always).Scan(t.Context()))
			require.NoError(t, f.scanner(always).Scan(t.Context()))
			want := append(append([]int64(nil), f.oldSends...), fresh...)
			assert.Equal(t, want, f.adapter.seqs(), "every new addressed event must wake once despite reused sequences")
			mark, _ = f.mark()
			assert.Equal(t, fresh[len(fresh)-1], mark)
		})
	}
}
