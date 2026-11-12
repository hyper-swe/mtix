// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
)

type interruptRecoveryPull struct {
	bytes.Buffer
	cancel      context.CancelFunc
	interrupted bool
}

func (w *interruptRecoveryPull) Write(p []byte) (int, error) {
	if !w.interrupted && strings.Contains(string(p), "pull progress: batch 1 (") {
		w.interrupted = true
		w.cancel()
	}
	return w.Buffer.Write(p)
}

func interruptFirstCloneAtFour(t *testing.T, history []*model.SyncEvent) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	stderr := &interruptCloneBeforeCursor{cancel: cancel}
	err := runSyncClone(ctx, &bytes.Buffer{}, stderr, nil, cloudOpts, false, 2)
	require.True(t, stderr.interrupted)
	require.ErrorContains(t, err, "context canceled")
	tail, err := app.store.JournalTail(t.Context())
	require.NoError(t, err)
	require.Equal(t, int64(4), tail)
	checkpoint, err := readCloneCheckpoint(t.Context(), app.store, true)
	require.NoError(t, err)
	require.Equal(t, transport.CursorAt(history[3]), checkpoint)
	cursor, err := readLastPulledClock(t.Context(), app.store)
	require.NoError(t, err)
	require.Zero(t, cursor.Lamport)
	require.NotEmpty(t, cloneCompletionWitness(t))
}

func failRecoveryPullAfterReplayPage(t *testing.T, history []*model.SyncEvent) {
	t.Helper()
	before := cloneCompletionWitness(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	stderr := &interruptRecoveryPull{cancel: cancel}
	err := runSyncPull(ctx, &bytes.Buffer{}, stderr, nil, cloudOpts, 2)
	require.True(t, stderr.interrupted, "real cursor page committed before cancellation")
	require.ErrorContains(t, err, "context canceled", stderr.String())
	require.Contains(t, stderr.String(), "pull progress: batch 1 (")
	require.NotContains(t, stderr.String(), "pull progress: batch 2 (")
	cursor, err := readLastPulledClock(t.Context(), app.store)
	require.NoError(t, err)
	require.Equal(t, transport.CursorAt(history[1]), cursor)
	tail, err := app.store.JournalTail(t.Context())
	require.NoError(t, err)
	require.Equal(t, int64(4), tail, "the partial pull only replayed existing events")
	assert.Equal(t, before, cloneCompletionWitness(t), "a failed partial pull must not finish the first clone")
}

func TestWakeFirstClone_PGFailedPartialPull_PreservesHistoryFloor(t *testing.T) {
	for _, partial := range []bool{false, true} {
		t.Run(map[bool]string{false: "no-partial-pull", true: "failed-partial-pull"}[partial], func(t *testing.T) {
			_, history := firstWakeCloneHistoryCount(t, 7)
			f := newDiscardPullWakeRig(t)
			interruptFirstCloneAtFour(t, history)
			if partial {
				failRecoveryPullAfterReplayPage(t, history)
			}
			var stdout, stderr bytes.Buffer
			require.NoError(t, runSyncClone(t.Context(), &stdout, &stderr, nil, cloudOpts, true, 2), stderr.String())
			inbox, err := app.store.InboxList(t.Context(), "worker")
			require.NoError(t, err)
			require.Len(t, inbox, 7, "all history remains unread")
			for range 3 {
				require.NoError(t, f.scanner.Scan(t.Context()))
			}
			assert.Empty(t, f.sent, "an unfinished pull with no journal writes must leave history quiet")
			pushFreshCloneHubMail(t, history[len(history)-1])
			stdout.Reset()
			stderr.Reset()
			require.NoError(t, runSyncClone(t.Context(), &stdout, &stderr, nil, cloudOpts, true, 2), stderr.String())
			for range 3 {
				require.NoError(t, f.scanner.Scan(t.Context()))
			}
			assert.Equal(t, []int64{9}, f.sent, "one fresh hub wake after successful completion, with no repeats")
		})
	}
}
