// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

//go:build !windows

package service

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
	"github.com/stretchr/testify/require"
)

type cleanForceResult struct {
	Error    string `json:"error"`
	Conflict bool   `json:"conflict"`
}

func TestPendingExports_ProcessCleanForceSnapshotKeepsLateRequest(t *testing.T) {
	dir := t.TempDir()
	store := pendingProcessStore(t, dir)
	pendingProcessNode(t, store, 1)
	svc := NewSyncService(store, slog.Default(), pendingProcessClock)
	require.NoError(t, svc.ForceExport(context.Background(), dir))
	holder := startExportRequestChild(t, dir, "clean-force-holder")
	holder.awaitSignal(t, "snapshot-held")
	before, err := store.ReadExportRequest(context.Background())
	require.NoError(t, err)
	require.Equal(t, before.Requested, before.Covered, "holder starts with genuinely clean intent")
	require.Nil(t, before.Active)
	writer := startExportRequestChild(t, dir, "generation-writer")
	writer.awaitSignal(t, "committed 2")
	writer.awaitExit(t)
	_, err = io.WriteString(holder.stdin, "continue\n")
	require.NoError(t, err)
	result := awaitCleanForceResult(t, holder)
	holder.awaitExit(t)
	require.Equal(t, 1, pendingProcessMirrorCount(t, dir), "native ForceExport published the earlier snapshot")
	late, err := store.ReadExportRequest(context.Background())
	require.NoError(t, err)
	require.Equal(t, before.Covered, late.Covered)
	require.Equal(t, before.Requested+1, late.Requested)
	require.Equal(t, pendingProcessClock().Add(time.Hour).Format(time.RFC3339), late.Oldest)
	t.Logf("completed public ForceExport result after joined late writer: conflict=%v error=%q", result.Conflict, result.Error)
	require.Empty(t, result.Error, "completed public ForceExport must succeed while retaining later intent")
	require.False(t, result.Conflict)
	recovery := startExportRequestChild(t, dir, "recover-import-off")
	recovery.awaitExit(t)
	require.Equal(t, 2, pendingProcessMirrorCount(t, dir))
	status, err := svc.PendingExports(context.Background(), dir)
	require.NoError(t, err)
	require.Zero(t, status.Count)
}

func generationCleanForceHolder(t *testing.T, store *sqlite.Store, svc *SyncService, dir string) {
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
	err := svc.ForceExport(context.Background(), dir)
	result := cleanForceResult{Conflict: errors.Is(err, model.ErrConflict)}
	if err != nil {
		result.Error = err.Error()
	}
	body, marshalErr := json.Marshal(result)
	require.NoError(t, marshalErr)
	_, writeErr := fmt.Fprintf(os.Stdout, "force-result %s\n", body)
	require.NoError(t, writeErr)
	// Return actual API errors as protocol data. Fixture exit stays successful;
	// the parent owns the desired behavioral assertion after joining both peers.
}

func awaitCleanForceResult(t *testing.T, holder *coalescingWriter) cleanForceResult {
	t.Helper()
	guard := time.NewTimer(60 * time.Second)
	defer guard.Stop()
	for {
		select {
		case line, ok := <-holder.lines:
			require.True(t, ok, "native ForceExport result protocol missing")
			if strings.HasPrefix(line, "force-result ") {
				var result cleanForceResult
				require.NoError(t, json.Unmarshal([]byte(strings.TrimPrefix(line, "force-result ")), &result))
				return result
			}
		case <-guard.C:
			t.Fatal("owned clean ForceExport did not return its actual result")
		}
	}
}
