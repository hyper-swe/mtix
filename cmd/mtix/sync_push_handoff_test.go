// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
	"github.com/hyper-swe/mtix/internal/sync/pushlock"
	"github.com/stretchr/testify/require"
)

func handoffCreate(t *testing.T, s *sqlite.Store, n int, prompt string) {
	t.Helper()
	now := time.Now().UTC()
	require.NoError(t, s.CreateNode(context.Background(), &model.Node{ID: fmt.Sprintf("HAND-%d", n), Project: "HAND", Seq: n, Title: "Native handoff task", Prompt: prompt, Status: model.StatusOpen, Priority: model.PriorityMedium, Weight: 1, NodeType: model.NodeTypeEpic, CreatedAt: now, UpdatedAt: now}))
}

type handoffSummaryFunc func()

func (f handoffSummaryFunc) Write(p []byte) (int, error) {
	if strings.HasPrefix(string(p), "push complete:") {
		f()
	}
	return len(p), nil
}
func handoffUnlocked(t *testing.T, dir string) {
	t.Helper()
	lock, err := pushlock.Acquire(dir)
	require.NoError(t, err)
	require.NoError(t, lock.Release())
}
func TestPushHandoff_BoundsContinuingArrivals(t *testing.T) {
	dir := t.TempDir()
	s := newDaemonTestStore(t, dir)
	hub := newFakePushHub()
	calls := 0
	summaries := 0
	var stderr bytes.Buffer
	output := handoffSummaryFunc(func() { summaries++; handoffCreate(t, s, summaries, "") })
	err := pushWithHandoff(context.Background(), &stderr, s, pushHandoffOptions{mtixDir: dir}, func() (bool, error) {
		calls++
		lock, err := pushlock.Acquire(dir)
		require.ErrorIs(t, err, pushlock.ErrLockHeld)
		require.Nil(t, lock)
		return pushAndReportState(context.Background(), output, &stderr, hub, s)
	})
	require.NoError(t, err)
	require.Equal(t, 3, calls)
	require.Equal(t, 3, summaries)
	require.Len(t, hub.calls, 2)
	require.Len(t, hub.events, 2)
	remaining, err := readPendingBatch(context.Background(), s, 10)
	require.NoError(t, err)
	require.Len(t, remaining, 1)
	require.Equal(t, "HAND-3", remaining[0].NodeID)
	handoffUnlocked(t, dir)
	laterCalls := 0
	require.NoError(t, pushWithHandoff(context.Background(), &stderr, s, pushHandoffOptions{mtixDir: dir}, func() (bool, error) {
		laterCalls++
		return pushAndReportState(context.Background(), io.Discard, &stderr, hub, s)
	}))
	require.Equal(t, 1, laterCalls)
	require.Len(t, hub.events, 3)
	require.Equal(t, "HAND-3", hub.events[2].NodeID)
	remaining, err = readPendingBatch(context.Background(), s, 10)
	require.NoError(t, err)
	require.Empty(t, remaining)
	handoffUnlocked(t, dir)
}
func TestPushHandoff_TerminalStatesDoNotRepeat(t *testing.T) {
	for _, mode := range []string{"empty", "held", "deferred", "no-progress"} {
		t.Run(mode, func(t *testing.T) { handoffTerminal(t, mode) })
	}
}
func handoffTerminal(t *testing.T, mode string) {
	t.Helper()
	dir := t.TempDir()
	s := newDaemonTestStore(t, dir)
	hub := newFakePushHub()
	if mode != "empty" {
		prompt := ""
		if mode == "held" {
			prompt = overWireCap()
		}
		handoffCreate(t, s, 1, prompt)
		events, err := readPendingBatch(context.Background(), s, 10)
		require.NoError(t, err)
		require.Len(t, events, 1)
		if mode == "no-progress" {
			hub.refuse = map[string]bool{events[0].EventID: true}
		}
		if mode == "deferred" {
			require.NoError(t, s.ClaimNode(context.Background(), "HAND-1", "handoff-worker"))
			// Fixture a pending creation behind the initial cursor; the native claim read must defer.
			_, err = s.WriteDB().Exec(`UPDATE sync_events SET lamport_clock = -1 WHERE op_type = ?`, model.OpCreateNode)
			require.NoError(t, err)
		}
	}
	calls := 0
	var stderr bytes.Buffer
	require.NoError(t, pushWithHandoff(context.Background(), &stderr, s, pushHandoffOptions{mtixDir: dir}, func() (bool, error) {
		calls++
		return pushAndReportState(context.Background(), io.Discard, &stderr, hub, s)
	}))
	require.Equal(t, 1, calls, "held/deferred/no-progress work must not drive fruitless passes")
	handoffUnlocked(t, dir)
	if mode == "empty" {
		require.Empty(t, hub.calls)
		return
	}
	var pending int
	// Pending work is retained even when screening or the hub prevents progress.
	require.NoError(t, s.WriteDB().QueryRow(`SELECT COUNT(*) FROM sync_events WHERE sync_status = 'pending'`).Scan(&pending))
	require.Positive(t, pending)
	if mode == "held" || mode == "deferred" {
		require.Empty(t, hub.calls)
	} else {
		require.Len(t, hub.calls, 1)
	}
}
func TestPushHandoff_ForceBypassesAndRunsOnce(t *testing.T) {
	dir := t.TempDir()
	s := newDaemonTestStore(t, dir)
	lock, err := pushlock.Acquire(dir)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, lock.Release()) })
	calls := 0
	require.NoError(t, pushWithHandoff(context.Background(), io.Discard, s, pushHandoffOptions{mtixDir: dir, force: true}, func() (bool, error) {
		calls++
		handoffCreate(t, s, calls, "")
		return true, nil
	}))
	require.Equal(t, 1, calls)
	held, err := pushlock.Acquire(dir)
	require.ErrorIs(t, err, pushlock.ErrLockHeld)
	require.Nil(t, held)
}
func TestPushHandoff_ContentionDoesNotPromiseDrain(t *testing.T) {
	dir := t.TempDir()
	s := newDaemonTestStore(t, dir)
	lock, err := pushlock.Acquire(dir)
	require.NoError(t, err)
	defer func() { require.NoError(t, lock.Release()) }()
	calls := 0
	var stderr bytes.Buffer
	require.NoError(t, pushWithHandoff(context.Background(), &stderr, s, pushHandoffOptions{mtixDir: dir}, func() (bool, error) { calls++; return true, nil }))
	require.Zero(t, calls)
	require.Contains(t, stderr.String(), "queued work remains pending")
	require.Contains(t, stderr.String(), "retry the next mtix sync push")
}
func TestPushHandoff_ErrorsReleaseOwnership(t *testing.T) {
	for _, hook := range []bool{false, true} {
		t.Run(fmt.Sprintf("hook-%t", hook), func(t *testing.T) {
			t.Setenv("MTIX_SYNC_HOOK", map[bool]string{false: "", true: "1"}[hook])
			dir := t.TempDir()
			s := newDaemonTestStore(t, dir)
			calls := 0
			var stderr bytes.Buffer
			err := pushWithHandoff(context.Background(), &stderr, s, pushHandoffOptions{mtixDir: dir}, func() (bool, error) {
				calls++
				return false, wrapSyncErr(&stderr, "push loop", &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection refused: owned fixture")})
			})
			if hook {
				require.NoError(t, err)
				require.Contains(t, stderr.String(), "continuing per MTIX_SYNC_HOOK=1")
			} else {
				require.Error(t, err)
			}
			require.Equal(t, 1, calls)
			handoffUnlocked(t, dir)
		})
	}
}
func TestPushHandoff_QueueReadFailureStopsAfterUnlock(t *testing.T) {
	dir := t.TempDir()
	s := newDaemonTestStore(t, dir)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	calls := 0
	err := pushWithHandoff(ctx, io.Discard, s, pushHandoffOptions{mtixDir: dir}, func() (bool, error) { calls++; cancel(); return true, nil })
	require.Error(t, err)
	require.Equal(t, 1, calls)
	handoffUnlocked(t, dir)
}
func TestPushHandoff_LockIOFailureNeverRunsPass(t *testing.T) {
	dir := t.TempDir()
	s := newDaemonTestStore(t, dir)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "data"), []byte("owned obstruction"), 0o600))
	calls := 0
	err := pushWithHandoff(context.Background(), io.Discard, s, pushHandoffOptions{mtixDir: dir}, func() (bool, error) { calls++; return true, nil })
	require.Error(t, err)
	require.Zero(t, calls)
}
