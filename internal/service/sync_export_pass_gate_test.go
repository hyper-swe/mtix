// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

//go:build !windows

package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// pausedPass starts an explicit export on svc and holds it after its database
// snapshot, while it owns the pass gate and sync.lock. The returned function
// releases it and returns its result; the counter counts board serializations.
func pausedPass(t *testing.T, svc *SyncService, dir string) (func() error, *atomic.Int32) {
	t.Helper()
	held, release := make(chan struct{}), make(chan struct{})
	var first sync.Once
	var marshals atomic.Int32
	svc.marshalExport = func(data *sqlite.ExportData) ([]byte, error) {
		marshals.Add(1)
		first.Do(func() { close(held); <-release })
		return json.MarshalIndent(data, "", "  ")
	}
	done := make(chan error, 1)
	go func() { done <- svc.ForceExport(t.Context(), dir) }()
	select {
	case <-held:
	case err := <-done:
		t.Fatalf("export returned before the pause: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("export never reached the pause")
	}
	return func() error { close(release); return <-done }, &marshals
}

// MTIX-130.20.4: the pulled-board preflight runs inside the pass gate. A
// queued automatic pass and a queued drain must not run it while another pass
// holds sync.lock over a board whose hash is not yet published: their import
// would meet the busy lock and record a spurious not-imported refusal.
func TestExportPass_QueuedPreflight_RunsUnderPassGate(t *testing.T) {
	dir := t.TempDir()
	store := pendingProcessStore(t, dir)
	pendingProcessNode(t, store, 1)
	svc := NewSyncService(store, slog.Default(), pendingProcessClock)
	var notices bytes.Buffer
	svc.SetNoticeWriter(&notices)
	require.NoError(t, svc.ForceExport(t.Context(), dir))
	// The board on disk is ahead of its published hash, as between the
	// holder's rename and its hash publication.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "data", "sync.sha256"), []byte("stale"), 0o600))
	queued := make(chan struct{}, 2)
	svc.passQueued = func() { queued <- struct{}{} }
	finish, _ := pausedPass(t, svc, dir)
	require.NoError(t, svc.RequestExport(dir))
	var waiters sync.WaitGroup
	errs := make(chan error, 2)
	waiters.Go(func() { errs <- svc.AutoExport(t.Context(), dir) })
	waiters.Go(func() { errs <- svc.DrainPendingExport(t.Context(), dir) })
	for range 2 {
		select {
		case <-queued:
		case <-time.After(15 * time.Second):
			// A preflight outside the gate refuses the half-published board
			// and returns before it ever queues; name that cause first.
			require.Nil(t, svc.readRefusal(dir), "a pass recorded a refusal for a board its holder was publishing")
			t.Fatal("a waiter never queued behind the running pass")
		}
	}
	require.NoError(t, finish())
	waiters.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	require.Nil(t, svc.readRefusal(dir), "a queued pass recorded a refusal for a board its holder was publishing")
	require.NotContains(t, notices.String(), "not imported")
	require.NotContains(t, notices.String(), "changed on disk")
}

// MTIX-130.20.4: a queued pass waits for the running one only as long as its
// context lives. On cancellation it returns the context error, starts no
// pass, and its request stays pending for a retry.
func TestForceExport_QueuedWaiterContextExpires_StartsNoPass(t *testing.T) {
	dir := t.TempDir()
	store := pendingProcessStore(t, dir)
	svc := NewSyncService(store, slog.Default(), pendingProcessClock)
	finish, marshals := pausedPass(t, svc, dir)
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	result := make(chan error, 1)
	go func() { result <- svc.ForceExport(ctx, dir) }()
	var err error
	select {
	case err = <-result:
	case <-time.After(10 * time.Second):
		require.NoError(t, finish())
		t.Fatal("a queued export ignored its expired context")
	}
	require.True(t, errors.Is(err, context.DeadlineExceeded), "got %v", err)
	require.NoError(t, finish())
	require.Equal(t, int32(1), marshals.Load(), "the cancelled waiter started a pass")
	status, statusErr := svc.PendingExports(t.Context(), dir)
	require.NoError(t, statusErr)
	require.Equal(t, 1, status.Count, "the cancelled waiter's request must stay pending")
}
