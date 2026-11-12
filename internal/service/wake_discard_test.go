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

func TestWakeScan_DiscardLocalReusesSequences_FreshMailWakesOnce(t *testing.T) {
	f := newWakeReplaceFixture(t, 2, true)
	require.NoError(t, sqlite.DiscardLocal(t.Context(), f.store, t.TempDir()))
	tail, err := f.store.JournalTail(t.Context())
	require.NoError(t, err)
	require.Zero(t, tail)
	nodes := service.NewNodeService(f.store, &service.NoopBroadcaster{}, &service.StaticConfig{}, slog.Default(), time.Now)
	node, err := nodes.CreateNode(t.Context(), &service.CreateNodeRequest{Project: "PROJ", Title: "after discard", Creator: "sender"})
	require.NoError(t, err)
	require.NoError(t, f.prompts.AddAnnotation(t.Context(), node.ID, "fresh", "sender", f.key.AgentID))
	fresh, err := f.store.InboxList(t.Context(), f.key.AgentID)
	require.NoError(t, err)
	require.Len(t, fresh, 1)
	require.Less(t, fresh[0].Seq, f.droppedTail)
	require.NoError(t, f.scanner(always).Scan(t.Context()))
	require.NoError(t, f.scanner(always).Scan(t.Context()))
	want := append(append([]int64(nil), f.oldSends...), fresh[0].Seq)
	assert.Equal(t, want, f.adapter.seqs())
	mark, _ := f.mark()
	assert.Equal(t, fresh[0].Seq, mark)
}
