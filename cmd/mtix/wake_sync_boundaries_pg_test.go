// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"testing"
	"time"

	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func wakeRigAtTail(t *testing.T, tail int64) *discardPullWakeRig {
	t.Helper()
	f := newDiscardPullWakeRig(t)
	require.NoError(t, app.store.AdvanceWakeScanCursor(t.Context(), f.host, f.uid, tail, time.Minute))
	_, err := app.store.AcquireWakeLease(t.Context(), sqlite.WakeLeaseKey{AgentID: "worker", HostID: f.host, UID: f.uid}, "", time.Minute)
	require.NoError(t, err)
	require.Equal(t, []int64{tail, tail, tail}, cloneWakeMarks(t, f))
	return f
}

func TestWakeCloneResume_PGNothingPulled_PreservesPendingMail(t *testing.T) {
	nodeID := interruptedWakeCloneNode(t)
	var stdout, stderr bytes.Buffer
	require.NoError(t, runSyncClone(t.Context(), &stdout, &stderr, nil, cloudOpts, true, 2), stderr.String())
	tail, err := app.store.JournalTail(t.Context())
	require.NoError(t, err)
	f := wakeRigAtTail(t, tail)
	before := cloneWakeMarks(t, f)
	require.NoError(t, app.promptSvc.AddAnnotation(t.Context(), nodeID, "local mail after drained hub", "sender", "worker"))
	stdout.Reset()
	stderr.Reset()
	require.NoError(t, runSyncClone(t.Context(), &stdout, &stderr, nil, cloudOpts, true, 2), stderr.String())
	require.Contains(t, stdout.String(), "0 events applied across 0 batches", "a real repeat resume has no remaining hub events")
	assert.Equal(t, before, cloneWakeMarks(t, f))
	for range 3 {
		require.NoError(t, f.scanner.Scan(t.Context()))
	}
	assert.Equal(t, []int64{tail + 1}, f.sent, "zero-pull resume keeps pending mail eligible exactly once")
}

func TestWakePull_PGOneEventBoundaries(t *testing.T) {
	for _, initial := range []int64{0, 1} {
		t.Run(map[int64]string{0: "first-fill", 1: "nonempty"}[initial], func(t *testing.T) {
			pool := openCmdHub(t)
			initTestApp(t)
			if initial == 1 {
				_, err := app.nodeSvc.CreateNode(t.Context(), &service.CreateNodeRequest{Project: "TEST", Title: "already here", Creator: "sender"})
				require.NoError(t, err)
			}
			tail, err := app.store.JournalTail(t.Context())
			require.NoError(t, err)
			require.Equal(t, initial, tail)
			f := wakeRigAtTail(t, tail)
			before, hookBefore := cloneWakeMarks(t, f), hookFloorForClone(t)
			pushToHub(t, pool, remoteCreateAt(t, "TEST-9", 10))
			var stdout, stderr bytes.Buffer
			require.NoError(t, runSyncPull(t.Context(), &stdout, &stderr, nil, cloudOpts, 2), stderr.String())
			require.Contains(t, stdout.String(), "1 events applied", "exactly one event in this first fill or ordinary pull")
			if initial == 0 {
				assert.Equal(t, []int64{1, 1, 1}, cloneWakeMarks(t, f))
				assert.Equal(t, int64(1), hookFloorForClone(t))
			} else {
				assert.Equal(t, before, cloneWakeMarks(t, f))
				assert.Equal(t, hookBefore, hookFloorForClone(t))
			}
			require.NoError(t, app.promptSvc.AddAnnotation(t.Context(), "TEST-9", "fresh after pull", "sender", "worker"))
			require.NoError(t, f.scanner.Scan(t.Context()))
			assert.Equal(t, []int64{initial + 2}, f.sent)
		})
	}
}
