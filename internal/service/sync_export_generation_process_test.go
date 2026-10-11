// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

//go:build !windows

package service

import (
	"bufio"
	"context"
	"database/sql"
	"fmt"
	"io"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

func TestPendingExports_ProcessLateGenerationSurvivesSnapshot(t *testing.T) {
	for _, killed := range []bool{false, true} {
		t.Run(fmt.Sprintf("kill-holder=%v", killed), func(t *testing.T) { generationProcessRecovery(t, killed) })
	}
}

func generationProcessRecovery(t *testing.T, killed bool) {
	dir := t.TempDir()
	store := pendingProcessStore(t, dir)
	pendingProcessNode(t, store, 1)
	svc := NewSyncService(store, slog.Default(), pendingProcessClock)
	require.NoError(t, svc.AutoExport(context.Background(), dir))
	require.NoError(t, svc.RequestExport(dir))
	holder := startExportRequestChild(t, dir, "snapshot-holder")
	holder.awaitSignal(t, "snapshot-held")
	state, err := store.ReadExportRequest(context.Background())
	require.NoError(t, err)
	require.NotNil(t, state.Active, "cutoff commit is visible while the real holder is still exporting")
	writer := startExportRequestChild(t, dir, "generation-writer")
	writer.awaitSignal(t, "committed 2")
	writer.awaitExit(t)
	if killed {
		require.NoError(t, holder.cmd.Process.Kill())
		select {
		case <-holder.done:
			require.Error(t, holder.err)
		case <-time.After(60 * time.Second):
			t.Fatal("killed owned exporter/readers did not join")
		}
	} else {
		_, err := io.WriteString(holder.stdin, "continue\n")
		require.NoError(t, err)
		holder.awaitExit(t)
		status, err := svc.PendingExports(context.Background(), dir)
		require.NoError(t, err)
		require.Equal(t, 1, status.Count, "post-cutoff commit must remain pending")
		require.Equal(t, pendingProcessClock().Add(time.Hour).Format(time.RFC3339), status.OldestAt)
	}
	require.Equal(t, 1, pendingProcessMirrorCount(t, dir), "first pass cannot contain the joined later writer")
	restarted := startExportRequestChild(t, dir, "recover-import-off")
	restarted.awaitExit(t)
	require.Equal(t, 2, pendingProcessMirrorCount(t, dir), "restart recovers without a new mutation, even with AutoImport off")
	status, err := svc.PendingExports(context.Background(), dir)
	require.NoError(t, err)
	require.Zero(t, status.Count)
}

func generationProcessHelper(t *testing.T, store *sqlite.Store, dir, mode string) {
	t.Helper()
	svc := NewSyncService(store, slog.Default(), pendingProcessClock)
	switch mode {
	case "snapshot-holder":
		generationSnapshotHolder(t, store, svc, dir)
	case "generation-writer":
		pendingProcessNode(t, store, 2)
		svc.clock = func() time.Time { return pendingProcessClock().Add(time.Hour) }
		require.NoError(t, svc.RequestExport(dir))
		_, err := fmt.Fprintln(os.Stdout, "committed 2")
		require.NoError(t, err)
	case "recover-import-off":
		svc.SetAutoSyncConfig(pendingImportOff{})
		require.NoError(t, svc.AutoImport(context.Background(), dir))
	case "hold-metadata-lock":
		generationMetadataHolder(t, store)
	default:
		t.Fatalf("unknown fixed export-request helper mode %q", mode)
	}
}

func generationMetadataHolder(t *testing.T, store *sqlite.Store) {
	t.Helper()
	err := store.WithTx(context.Background(), func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(context.Background(), "INSERT INTO meta(key,value) VALUES (?,?)", "owned-blocking-writer", "held"); err != nil {
			return fmt.Errorf("hold native metadata writer: %w", err)
		}
		if _, err := fmt.Fprintln(os.Stdout, "writer-held"); err != nil {
			return fmt.Errorf("signal native metadata writer: %w", err)
		}
		_, err := bufio.NewReader(os.Stdin).ReadString('\n')
		if err != nil {
			return fmt.Errorf("release native metadata writer: %w", err)
		}
		return nil
	})
	require.NoError(t, err)
}

func generationSnapshotHolder(t *testing.T, store *sqlite.Store, svc *SyncService, dir string) {
	t.Helper()
	once := false
	store.SetClock(func() time.Time {
		if !once {
			once = true
			_, err := fmt.Fprintln(os.Stdout, "snapshot-held")
			require.NoError(t, err)
			line, err := bufio.NewReader(os.Stdin).ReadString('\n')
			require.NoError(t, err)
			require.Equal(t, "continue\n", line)
		}
		return pendingProcessClock()
	})
	require.NoError(t, svc.exportBoardProtected(context.Background(), dir, true))
}
