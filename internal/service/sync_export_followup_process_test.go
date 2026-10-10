// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

//go:build !windows

package service

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

// Publication logs while the exclusive lock is still held. Each acknowledged
// barrier therefore places a real writer strictly after that pass's snapshot.
type pendingPublicationBarrier struct {
	remaining int
	pass      int
}

func (*pendingPublicationBarrier) Enabled(context.Context, slog.Level) bool { return true }
func (h *pendingPublicationBarrier) WithAttrs([]slog.Attr) slog.Handler     { return h }
func (h *pendingPublicationBarrier) WithGroup(string) slog.Handler          { return h }
func (h *pendingPublicationBarrier) Handle(_ context.Context, record slog.Record) error {
	if record.Message != "sync_export_completed" || h.remaining == 0 {
		return nil
	}
	h.remaining--
	h.pass++
	if _, err := fmt.Fprintf(os.Stdout, "published-%d\n", h.pass); err != nil {
		return err
	}
	_, err := bufio.NewReader(os.Stdin).ReadString('\n')
	return err
}

func TestPendingExport_ProcessDrainLateArrival(t *testing.T) {
	for _, mode := range []string{"drainholder", "importdrainholder"} {
		t.Run(mode, func(t *testing.T) { pendingFollowupProcess(t, mode, 1) })
	}
}
func TestPendingExport_ProcessSecondAutoExportLateArrival(t *testing.T) {
	pendingFollowupProcess(t, "exportholder2", 2)
}
func TestPendingExport_ProcessAutoExportHolderFollowup(t *testing.T) {
	pendingFollowupProcess(t, "exportholder", 1)
}
func pendingFollowupProcess(t *testing.T, mode string, passes int) {
	t.Helper()
	dir := t.TempDir()
	store := pendingProcessStore(t, dir)
	pendingProcessNode(t, store, 1)
	svc := NewSyncService(store, slog.Default(), pendingProcessClock)
	require.NoError(t, svc.AutoExport(context.Background(), dir))
	require.NoError(t, markPendingExport(dir))
	holder := startPendingProcess(t, mode, dir)
	for pass := 1; pass <= passes; pass++ {
		holder.awaitLine(t, fmt.Sprintf("published-%d", pass))
		writerMode := "writer"
		if pass == 2 {
			writerMode = "writer3"
		}
		writer := startPendingProcess(t, writerMode, dir)
		writer.awaitExit(t)
		lock, err := svc.acquireLock(dir, lockExclusive)
		require.Error(t, err, "publication barrier must still hold exclusive lock")
		require.Nil(t, lock)
		require.Equal(t, pass, pendingProcessMirrorCount(t, dir))
		_, err = io.WriteString(holder.stdin, "release\n")
		require.NoError(t, err)
	}
	holder.awaitExit(t)
	require.Equal(t, passes+1, pendingProcessMirrorCount(t, dir), "holder must recover late requests without a new command")
	requests, err := pendingExportRequests(dir)
	require.NoError(t, err)
	require.Empty(t, requests)
}
