// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"io"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

func TestClaudeHookWait_ReplaceReusesSequence_SkipReceivesFreshIdentity(t *testing.T) {
	node := wakeStatusApp(t)
	board, err := app.store.Export(t.Context(), "", "")
	require.NoError(t, err)
	dropped, err := app.nodeSvc.CreateNode(t.Context(), &service.CreateNodeRequest{Project: "TEST", Title: "removed", Creator: "sender"})
	require.NoError(t, err)
	addressHookMail(t, dropped.ID, "old-hook-event", "old")
	old, err := app.store.InboxList(t.Context(), "worker")
	require.NoError(t, err)
	require.Len(t, old, 1)
	_, err = app.store.Import(t.Context(), board, sqlite.ImportModeReplace, false)
	require.NoError(t, err)
	_, err = app.nodeSvc.CreateNode(t.Context(), &service.CreateNodeRequest{Project: "TEST", Title: "unaddressed padding", Creator: "sender"})
	require.NoError(t, err)
	addressHookMail(t, node, "fresh-hook-event", "fresh")
	fresh, err := app.store.InboxList(t.Context(), "worker")
	require.NoError(t, err)
	require.Len(t, fresh, 1)
	require.Equal(t, old[0].Seq, fresh[0].Seq)
	require.NotEqual(t, old[0].EventID, fresh[0].EventID)
	var observed []string
	skip := func(event sqlite.InboxEvent) bool {
		observed = append(observed, event.EventID)
		return event.Seq == old[0].Seq && event.EventID == old[0].EventID
	}
	join := func(string, io.Writer) func() { return func() {} }
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	got, err := claudeHookWait(ctx, "worker", time.Second, skip, join)
	require.NoError(t, err)
	assert.Equal(t, fresh, got)
	assert.Equal(t, []string{fresh[0].EventID}, observed)
}
