// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// firstWakeCloneHistory uses a distinct sender store, then initializes an
// actually fresh receiver. No discard or fabricated checkpoint stands in for it.
func firstWakeCloneHistory(t *testing.T) (string, []*model.SyncEvent) {
	t.Helper()
	return firstWakeCloneHistoryCount(t, 3)
}

func firstWakeCloneHistoryCount(t *testing.T, comments int) (string, []*model.SyncEvent) {
	t.Helper()
	pool := openCmdHub(t)
	t.Setenv(sqlite.AuthorIDEnv, "sender")
	t.Setenv("MTIX_WAKE_HOP", "0")
	var nodeID string
	var history []*model.SyncEvent
	require.True(t, t.Run("sender", func(t *testing.T) {
		initTestApp(t)
		node, err := app.nodeSvc.CreateNode(t.Context(), &service.CreateNodeRequest{Project: "TEST", Title: "first clone", Creator: "sender"})
		require.NoError(t, err)
		nodeID = node.ID
		for range comments {
			require.NoError(t, app.promptSvc.AddAnnotation(t.Context(), nodeID, "history", "sender", "worker"))
		}
		history, err = app.store.ReadPendingEvents(t.Context(), 100)
		require.NoError(t, err)
		require.Len(t, history, comments+1)
		pushToHub(t, pool, history...)
		requireHubOrder(t, pool, history)
	}))
	initTestApp(t)
	tail, err := app.store.JournalTail(t.Context())
	require.NoError(t, err)
	require.Zero(t, tail, "receiver has never held a local or cloned event")
	return nodeID, history
}

func interruptFirstWakeClone(t *testing.T, history []*model.SyncEvent) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	stderr := &interruptWakeClone{cancel: cancel}
	err := runSyncClone(ctx, &bytes.Buffer{}, stderr, nil, cloudOpts, false, 2)
	require.True(t, stderr.interrupted)
	require.ErrorIs(t, ctx.Err(), context.Canceled)
	require.ErrorContains(t, err, "context canceled", stderr.String())
	checkpoint, err := readCloneCheckpoint(t.Context(), app.store, true)
	require.NoError(t, err)
	require.Equal(t, transport.CursorAt(history[1]), checkpoint)
	tail, err := app.store.JournalTail(t.Context())
	require.NoError(t, err)
	require.Equal(t, int64(2), tail)
}

func TestWakeFirstClone_PGInterruptedResume_HistoryQuietFreshOnce(t *testing.T) {
	for _, before := range []bool{true, false} {
		t.Run(map[bool]string{true: "registered-before-clone", false: "registered-after-interruption"}[before], func(t *testing.T) {
			nodeID, history := firstWakeCloneHistory(t)
			var f *discardPullWakeRig
			if before {
				f = newDiscardPullWakeRig(t)
			}
			interruptFirstWakeClone(t, history)
			if !before {
				f = newDiscardPullWakeRig(t)
			}
			var stdout, stderr bytes.Buffer
			require.NoError(t, runSyncClone(t.Context(), &stdout, &stderr, nil, cloudOpts, true, 2), stderr.String())
			inbox, err := app.store.InboxList(t.Context(), "worker")
			require.NoError(t, err)
			require.Len(t, inbox, 3, "all cloned mail remains unread")
			for range 3 {
				require.NoError(t, f.scanner.Scan(t.Context()))
			}
			assert.Empty(t, f.sent, "resumed first-clone history must not spend wake budget")
			require.NoError(t, app.promptSvc.AddAnnotation(t.Context(), nodeID, "fresh after clone", "sender", "worker"))
			tail, err := app.store.JournalTail(t.Context())
			require.NoError(t, err)
			for range 3 {
				require.NoError(t, f.scanner.Scan(t.Context()))
			}
			assert.Equal(t, []int64{tail}, f.sent, "exactly one fresh wake after completed clone")
		})
	}
}
