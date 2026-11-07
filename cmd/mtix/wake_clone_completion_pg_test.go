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
	"github.com/hyper-swe/mtix/internal/sync/clock"
)

type interruptCloneBeforeCursor struct {
	bytes.Buffer
	cancel      context.CancelFunc
	interrupted bool
}

func (w *interruptCloneBeforeCursor) Write(p []byte) (int, error) {
	if !w.interrupted && strings.Contains(string(p), "clone progress: batch 2 (") {
		w.interrupted = true
		w.cancel()
	}
	return w.Buffer.Write(p)
}

func cloneCompletionWitness(t *testing.T) string {
	t.Helper()
	var witness string
	require.NoError(t, app.store.QueryRow(t.Context(), `SELECT COALESCE((SELECT value FROM meta WHERE key='meta.sync.clone.wake_bootstrap'),'')`).Scan(&witness))
	return witness
}

func interruptFirstCloneBeforeCursor(t *testing.T, history []*model.SyncEvent) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	stderr := &interruptCloneBeforeCursor{cancel: cancel}
	err := runSyncClone(ctx, &bytes.Buffer{}, stderr, nil, cloudOpts, false, 2)
	require.True(t, stderr.interrupted)
	require.ErrorContains(t, err, "context canceled")
	tail, err := app.store.JournalTail(t.Context())
	require.NoError(t, err)
	require.Equal(t, int64(len(history)), tail)
	checkpoint, err := readCloneCheckpoint(t.Context(), app.store, true)
	require.NoError(t, err)
	require.Equal(t, transport.CursorAt(history[len(history)-1]), checkpoint)
	cursor, err := readLastPulledClock(t.Context(), app.store)
	require.NoError(t, err)
	require.Zero(t, cursor.Lamport, "final cursor did not commit")
	require.NotEmpty(t, cloneCompletionWitness(t))
}

func pushFreshCloneHubMail(t *testing.T, previous *model.SyncEvent) {
	t.Helper()
	dsn, err := resolveSyncDSN(nil)
	require.NoError(t, err)
	pool, err := transport.New(t.Context(), dsn, cloudOpts)
	require.NoError(t, err)
	defer pool.Close()
	fresh := *previous
	fresh.EventID = clock.MustNewEventID()
	fresh.LamportClock++
	fresh.VectorClock = model.VectorClock{fresh.AuthorID: fresh.LamportClock}
	fresh.Payload, err = model.EncodePayload(&model.CommentPayload{AuthorID: fresh.AuthorID, Body: "fresh hub mail after completion", To: "worker"})
	require.NoError(t, err)
	pushToHub(t, pool, &fresh)
}

func TestWakeFirstClone_PGCompletionRetiresWitness_FreshHubMailWakesOnce(t *testing.T) {
	for _, completion := range []string{"ordinary-pull", "zero-batch-clone-resume"} {
		t.Run(completion, func(t *testing.T) {
			_, history := firstWakeCloneHistory(t)
			f := newDiscardPullWakeRig(t)
			interruptFirstCloneBeforeCursor(t, history)
			var stdout, stderr bytes.Buffer
			if completion == "ordinary-pull" {
				require.NoError(t, runSyncPull(t.Context(), &stdout, &stderr, nil, cloudOpts, 2), stderr.String())
			} else {
				require.NoError(t, runSyncClone(t.Context(), &stdout, &stderr, nil, cloudOpts, true, 2), stderr.String())
				require.Contains(t, stdout.String(), "0 events applied across 0 batches")
			}
			assert.Empty(t, cloneCompletionWitness(t), "any successful completion revokes first-fill authority")
			require.NoError(t, f.scanner.Scan(t.Context()))
			assert.Empty(t, f.sent, "all history already committed quietly before the interruption")
			pushFreshCloneHubMail(t, history[len(history)-1])
			stdout.Reset()
			stderr.Reset()
			require.NoError(t, runSyncClone(t.Context(), &stdout, &stderr, nil, cloudOpts, true, 2), stderr.String())
			require.Contains(t, stdout.String(), "1 events applied across 1 batches")
			inbox, err := app.store.InboxList(t.Context(), "worker")
			require.NoError(t, err)
			require.Len(t, inbox, 4, "unread history and fresh mail are preserved")
			for range 3 {
				require.NoError(t, f.scanner.Scan(t.Context()))
			}
			assert.Equal(t, []int64{5}, f.sent, "new hub mail after completion wakes once, including on later resumes")
		})
	}
}
