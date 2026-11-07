// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// interruptWakeClone cancels only after the real clone reports its committed
// first batch and checkpoint. Hub reads, application and resume remain real.
type interruptWakeClone struct {
	bytes.Buffer
	cancel      context.CancelFunc
	interrupted bool
}

func (w *interruptWakeClone) Write(p []byte) (int, error) {
	if strings.Contains(string(p), "clone progress: batch 1 (") {
		w.interrupted = true
		w.cancel()
	}
	return w.Buffer.Write(p)
}

func wakeCloneHubHistory(t *testing.T) (string, []*model.SyncEvent) {
	t.Helper()
	pool := openCmdHub(t)
	t.Setenv(sqlite.AuthorIDEnv, "sender")
	t.Setenv("MTIX_WAKE_HOP", "0")
	initTestApp(t)
	node, err := app.nodeSvc.CreateNode(t.Context(), &service.CreateNodeRequest{Project: "TEST", Title: "clone history", Creator: "sender"})
	require.NoError(t, err)
	for range 3 {
		require.NoError(t, app.promptSvc.AddAnnotation(t.Context(), node.ID, "history", "sender", "worker"))
	}
	history, err := app.store.ReadPendingEvents(t.Context(), 100)
	require.NoError(t, err)
	require.Len(t, history, 4)
	pushToHub(t, pool, history...)
	requireHubOrder(t, pool, history)
	require.NoError(t, markPushed(t.Context(), app.store, eventIDs(history)))
	require.NoError(t, sqlite.DiscardLocal(t.Context(), app.store, app.mtixDir))
	tail, err := app.store.JournalTail(t.Context())
	require.NoError(t, err)
	require.Zero(t, tail)
	return node.ID, history
}

func interruptedWakeCloneNode(t *testing.T) string {
	t.Helper()
	nodeID, history := wakeCloneHubHistory(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	stderr := &interruptWakeClone{cancel: cancel}
	err := runSyncClone(ctx, &bytes.Buffer{}, stderr, nil, cloudOpts, false, 2)
	require.True(t, stderr.interrupted, "interrupt after the committed batch, not during preflight")
	require.ErrorIs(t, ctx.Err(), context.Canceled)
	require.ErrorContains(t, err, "context canceled", stderr.String())
	checkpoint, err := readCloneCheckpoint(t.Context(), app.store, true)
	require.NoError(t, err)
	require.Equal(t, transport.CursorAt(history[1]), checkpoint)
	tail, err := app.store.JournalTail(t.Context())
	require.NoError(t, err)
	require.Equal(t, int64(2), tail, "the first real hub batch survived interruption")
	return nodeID
}

func cloneWakeMarks(t *testing.T, f *discardPullWakeRig) []int64 {
	t.Helper()
	regs, err := app.store.ListRegistrations(t.Context(), f.host, f.uid)
	require.NoError(t, err)
	require.Len(t, regs, 1)
	cursor, err := app.store.WakeScanCursor(t.Context(), f.host, f.uid)
	require.NoError(t, err)
	var lease int64
	err = app.store.ReadDB().QueryRowContext(t.Context(),
		`SELECT high_water FROM wake_leases WHERE agent_id='worker' AND host_id=? AND uid=?`, f.host, f.uid).Scan(&lease)
	require.NoError(t, err)
	return []int64{regs[0].HighWater, cursor, lease}
}

func verifyPendingWakeAfterInterruptedClone(t *testing.T, resume bool) {
	t.Helper()
	nodeID := interruptedWakeCloneNode(t)
	f := newDiscardPullWakeRig(t)
	require.NoError(t, app.store.AdvanceWakeScanCursor(t.Context(), f.host, f.uid, 2, time.Minute))
	_, err := app.store.AcquireWakeLease(t.Context(), sqlite.WakeLeaseKey{AgentID: "worker", HostID: f.host, UID: f.uid}, "", time.Minute)
	require.NoError(t, err)
	before := cloneWakeMarks(t, f)
	require.Equal(t, []int64{2, 2, 2}, before)
	require.NoError(t, app.promptSvc.AddAnnotation(t.Context(), nodeID, "local mail after interruption", "sender", "worker"))
	localTail, err := app.store.JournalTail(t.Context())
	require.NoError(t, err)
	var stdout, stderr bytes.Buffer
	if resume {
		err = runSyncClone(t.Context(), &stdout, &stderr, nil, cloudOpts, true, 2)
	} else {
		err = runSyncPull(t.Context(), &stdout, &stderr, nil, cloudOpts, 2)
	}
	require.NoError(t, err, stderr.String())
	assert.Equal(t, before, cloneWakeMarks(t, f), "nonempty sync must not cover unseen mail with a floor")
	inbox, err := app.store.InboxList(t.Context(), "worker")
	require.NoError(t, err)
	require.Len(t, inbox, 4, "one old, one local and two remaining hub annotations stay unread")
	for range 3 {
		require.NoError(t, f.scanner.Scan(t.Context()))
	}
	assert.Equal(t, []int64{localTail, localTail + 1, localTail + 2}, f.sent, "resume wakes exactly like the nonempty pull control, once each")
}

func TestWakeCloneResume_PGNonEmptyJournal_PreservesPendingMail(t *testing.T) {
	verifyPendingWakeAfterInterruptedClone(t, true)
}

func TestWakePull_PGNonEmptyJournal_DoesNotSeedWakeFloor(t *testing.T) {
	verifyPendingWakeAfterInterruptedClone(t, false)
}

func TestWakeCloneResume_PGEmptyJournal_SeedsHistoryFloor(t *testing.T) {
	for _, resume := range []bool{false, true} {
		t.Run(map[bool]string{false: "fresh", true: "resume"}[resume], func(t *testing.T) {
			nodeID, _ := wakeCloneHubHistory(t)
			f := newDiscardPullWakeRig(t)
			var stdout, stderr bytes.Buffer
			require.NoError(t, runSyncClone(t.Context(), &stdout, &stderr, nil, cloudOpts, resume, 2), stderr.String())
			for range 3 {
				require.NoError(t, f.scanner.Scan(t.Context()))
			}
			assert.Empty(t, f.sent, "an empty fresh or resumed clone still bootstraps history")
			require.NoError(t, app.promptSvc.AddAnnotation(t.Context(), nodeID, "new after the floor", "sender", "worker"))
			tail, err := app.store.JournalTail(t.Context())
			require.NoError(t, err)
			for range 3 {
				require.NoError(t, f.scanner.Scan(t.Context()))
			}
			assert.Equal(t, []int64{tail}, f.sent)

		})
	}
}
