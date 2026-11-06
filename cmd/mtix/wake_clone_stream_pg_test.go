// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
)

// These real-hub tests exercise durable clone intent and the local-write gap
// after the final committed batch, without a production test hook.
func TestWakeFirstClone_PGReopenAfterPause_HistoryQuiet(t *testing.T) {
	nodeID, history := firstWakeCloneHistory(t)
	f := newDiscardPullWakeRig(t)
	interruptFirstWakeClone(t, history)
	require.NoError(t, f.scanner.Scan(t.Context()))
	assert.Empty(t, f.sent, "committed history is quiet even while the clone is paused")
	require.NoError(t, app.store.Close())
	require.NoError(t, initApp(&cobra.Command{Use: "test"}, ""))
	f = newDiscardPullWakeRig(t)
	var stdout, stderr bytes.Buffer
	require.NoError(t, runSyncClone(t.Context(), &stdout, &stderr, nil, cloudOpts, true, 2), stderr.String())
	for range 3 {
		require.NoError(t, f.scanner.Scan(t.Context()))
	}
	assert.Empty(t, f.sent, "the verified stream survives closing and reopening SQLite")
	require.NoError(t, app.promptSvc.AddAnnotation(t.Context(), nodeID, "fresh after reopened clone", "sender", "worker"))
	tail, err := app.store.JournalTail(t.Context())
	require.NoError(t, err)
	for range 3 {
		require.NoError(t, f.scanner.Scan(t.Context()))
	}
	assert.Equal(t, []int64{tail}, f.sent)
}

func TestWakeFirstClone_PGLocalBeforeResume_RemainsEligibleAcrossBatches(t *testing.T) {
	nodeID, history := firstWakeCloneHistoryCount(t, 7)
	f := wakeRigAtTail(t, 0)
	interruptFirstWakeClone(t, history)
	before := cloneWakeMarks(t, f)
	require.Equal(t, []int64{2, 2, 2}, before)
	require.NoError(t, app.promptSvc.AddAnnotation(t.Context(), nodeID, "local after interruption", "sender", "worker"))
	local, err := app.store.JournalTail(t.Context())
	require.NoError(t, err)
	var stdout, stderr bytes.Buffer
	require.NoError(t, runSyncClone(t.Context(), &stdout, &stderr, nil, cloudOpts, true, 2), stderr.String())
	require.Contains(t, stdout.String(), "6 events applied across 3 batches")
	assert.Equal(t, before, cloneWakeMarks(t, f), "invalidation persists through all three later batches")
	for range 3 {
		require.NoError(t, f.scanner.Scan(t.Context()))
	}
	require.NotEmpty(t, f.sent)
	assert.Equal(t, local, f.sent[0], "the local mail gets the first wake")
	count := 0
	for _, seq := range f.sent {
		if seq == local {
			count++
		}
	}
	assert.Equal(t, 1, count)
}

type cloneMailOnProgress struct {
	bytes.Buffer
	t      *testing.T
	nodeID string
	wrote  bool
	err    error
}

func (w *cloneMailOnProgress) Write(p []byte) (int, error) {
	if !w.wrote && strings.Contains(string(p), "clone progress: batch 2 (") {
		w.wrote = true
		w.err = app.promptSvc.AddAnnotation(w.t.Context(), w.nodeID, "local after final batch", "sender", "worker")
	}
	return w.Buffer.Write(p)
}

func TestWakeFirstClone_PGLocalAfterFinalBatch_WakesOnce(t *testing.T) {
	nodeID, _ := firstWakeCloneHistory(t)
	f := newDiscardPullWakeRig(t)
	stderr := &cloneMailOnProgress{t: t, nodeID: nodeID}
	require.NoError(t, runSyncClone(t.Context(), &bytes.Buffer{}, stderr, nil, cloudOpts, false, 2), stderr.String())
	require.True(t, stderr.wrote)
	require.NoError(t, stderr.err)
	tail, err := app.store.JournalTail(t.Context())
	require.NoError(t, err)
	require.Equal(t, int64(5), tail)
	for range 3 {
		require.NoError(t, f.scanner.Scan(t.Context()))
	}
	assert.Equal(t, []int64{tail}, f.sent, "completion must not seed over the local write after batch 2")
}

func TestWakeFirstClone_PGRepeatedInterruption_StreamContinues(t *testing.T) {
	nodeID, history := firstWakeCloneHistoryCount(t, 7)
	f := newDiscardPullWakeRig(t)
	interruptFirstWakeClone(t, history)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	stderr := &interruptWakeClone{cancel: cancel}
	err := runSyncClone(ctx, &bytes.Buffer{}, stderr, nil, cloudOpts, true, 2)
	require.True(t, stderr.interrupted)
	require.ErrorContains(t, err, "context canceled")
	checkpoint, err := readCloneCheckpoint(t.Context(), app.store, true)
	require.NoError(t, err)
	require.Equal(t, transport.CursorAt(history[3]), checkpoint)
	require.NoError(t, f.scanner.Scan(t.Context()))
	assert.Empty(t, f.sent)
	var out, errors bytes.Buffer
	require.NoError(t, runSyncClone(t.Context(), &out, &errors, nil, cloudOpts, true, 2), errors.String())
	for range 3 {
		require.NoError(t, f.scanner.Scan(t.Context()))
	}
	assert.Empty(t, f.sent, "seven unread historical comments never consume the default five-wake budget")
	require.NoError(t, app.promptSvc.AddAnnotation(t.Context(), nodeID, "fresh after two interruptions", "sender", "worker"))
	tail, err := app.store.JournalTail(t.Context())
	require.NoError(t, err)
	for range 3 {
		require.NoError(t, f.scanner.Scan(t.Context()))
	}
	assert.Equal(t, []int64{tail}, f.sent)
}
