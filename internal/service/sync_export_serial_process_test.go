// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

//go:build !windows

package service

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

func TestExportPass_LocalOverlap(t *testing.T) {
	for _, mode := range []string{"tick-debounce", "tick-force", "tick-force-crash", "explicit-tick", "explicit-postrun-late", "tick-debounce-crash"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			store := pendingProcessStore(t, dir)
			pendingProcessNode(t, store, 1)
			svc := NewSyncService(store, slog.Default(), pendingProcessClock)
			require.NoError(t, svc.RequestExport(dir))
			child := startExportRequestChild(t, dir, mode)
			child.awaitSignal(t, "ready")
			if strings.HasSuffix(mode, "-crash") {
				state, err := store.ReadExportRequest(t.Context())
				require.NoError(t, err)
				require.NotNil(t, state.Active)
				require.Greater(t, state.Requested, *state.Active)
				require.NoError(t, child.cmd.Process.Kill())
				<-child.done
				require.Error(t, child.err)
				require.NoError(t, svc.DrainPendingExport(t.Context(), dir))
			} else {
				_, err := io.WriteString(child.stdin, "release\n")
				require.NoError(t, err)
				child.awaitExit(t)
			}
			wantNodes := 1
			if strings.HasSuffix(mode, "-crash") || mode == "explicit-postrun-late" {
				wantNodes = 2
			}
			require.Equal(t, wantNodes, pendingProcessMirrorCount(t, dir))
			status, err := svc.PendingExports(t.Context(), dir)
			require.NoError(t, err)
			require.Zero(t, status.Count)
			log := child.stderr.String()
			require.NotContains(t, log, "sync lock busy")
			require.NotContains(t, log, "level=ERROR")
			if !strings.HasSuffix(mode, "-crash") {
				wantPublications := wantNodes
				if mode == "tick-force" {
					wantPublications = 2 // Explicit export always rewrites.
				}
				require.Equal(t, wantPublications, strings.Count(log, "msg=sync_export_completed "))
			}
		})
	}
}

// Hold the first pass after its database snapshot, before publishing tasks.json.
// Only the parent can release it or terminate the process.
func serialExportChild(t *testing.T, svc *SyncService, st *sqlite.Store, dir, mode string) {
	t.Helper()
	svc.logger = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))
	held := make(chan struct{})
	var first sync.Once
	svc.marshalExport = func(data *sqlite.ExportData) ([]byte, error) {
		var err error
		first.Do(func() {
			close(held)
			_, err = bufio.NewReader(os.Stdin).ReadString('\n')
		})
		if err != nil {
			return nil, fmt.Errorf("release paused export: %w", err)
		}
		return json.MarshalIndent(data, "", "  ")
	}
	holder := svc.DrainPendingExport
	if strings.HasPrefix(mode, "explicit") {
		holder = svc.ForceExport
	}
	done := make(chan error, 1)
	go func() { done <- holder(t.Context(), dir) }()
	select {
	case <-held:
	case err := <-done:
		t.Fatalf("holder returned before snapshot barrier: %v", err)
	}
	if strings.HasSuffix(mode, "-crash") || mode == "explicit-postrun-late" {
		pendingProcessNode(t, st, 2)
	}
	queued := make(chan struct{})
	var queuedOnce sync.Once
	svc.passQueued = func() { queuedOnce.Do(func() { close(queued) }) }
	waiter := make(chan struct{})
	var waitErr error
	go func() {
		waitErr = serialExportWaiter(t.Context(), svc, dir, mode)
		close(waiter)
	}()
	awaitSerialExportWaiter(t, queued, waiter)
	_, err := fmt.Fprintln(os.Stdout, "ready")
	require.NoError(t, err)
	require.NoError(t, <-done)
	<-waiter
	require.NoError(t, waitErr)
}

// The parent must release or kill the holder only after the second export has
// actually run or queued. On the regressing build it returns from flock; on
// the fixed build it queues behind the pass gate, which the unexported
// passQueued hook reports right before it blocks. Elapsed time proves neither
// state, and no runtime stack text is matched (MTIX-130.20.4).
func awaitSerialExportWaiter(t *testing.T, queued, done <-chan struct{}) {
	t.Helper()
	select {
	case <-queued:
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the second export neither returned nor queued behind the running pass")
	}
}

func serialExportWaiter(ctx context.Context, svc *SyncService, dir, mode string) error {
	switch mode {
	case "tick-force", "tick-force-crash":
		return svc.ForceExport(ctx, dir)
	case "explicit-tick":
		return svc.DrainPendingExport(ctx, dir)
	case "explicit-postrun-late":
		return svc.AutoExport(ctx, dir)
	default:
		db := NewExportDebouncer(func(ctx context.Context) error {
			return svc.AutoExport(ctx, dir)
		}, svc.logger, time.Hour, time.Hour)
		db.Trigger()
		db.Close()
		return nil
	}
}

// A queued explicit export must report a failed durable mark without a
// misleading flock error from the pass already publishing in this process.
func TestForceExport_QueuedRequestRecordFailure(t *testing.T) {
	dir := t.TempDir()
	store := pendingProcessStore(t, dir)
	svc := NewSyncService(store, slog.Default(), pendingProcessClock)
	held, release := make(chan struct{}), make(chan struct{})
	var first sync.Once
	svc.marshalExport = func(data *sqlite.ExportData) ([]byte, error) {
		first.Do(func() { close(held) })
		<-release
		return json.MarshalIndent(data, "", "  ")
	}
	holder := make(chan error, 1)
	go func() { holder <- svc.ForceExport(t.Context(), dir) }()
	select {
	case <-held:
	case err := <-holder:
		t.Fatalf("export returned before marshal: %v", err)
	}
	var waiters sync.WaitGroup
	defer func() {
		close(release)
		waiters.Wait()
		require.NoError(t, <-holder)
	}()
	require.NoError(t, os.WriteFile(pendingExportDir(dir), nil, 0600))
	waiter := make(chan error, 1)
	waiters.Go(func() { waiter <- svc.ForceExport(t.Context(), dir) })
	select {
	case err := <-waiter:
		require.ErrorIs(t, err, syscall.ENOTDIR)
		require.NotErrorIs(t, err, syscall.EWOULDBLOCK)
	case <-time.After(10 * time.Second):
		t.Fatal("explicit export waited despite failing to record its request")
	}
}
