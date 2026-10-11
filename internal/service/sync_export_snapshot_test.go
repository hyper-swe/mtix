// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

//go:build !windows

package service

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/hyper-swe/mtix/internal/store/sqlite"
	"github.com/stretchr/testify/require"
)

func snapshotBaselineWriter(t *testing.T, store *sqlite.Store) {
	t.Helper()
	_, err := fmt.Fprintln(os.Stdout, "ready")
	require.NoError(t, err)
	_, err = bufio.NewReader(os.Stdin).ReadString('\n')
	if err == io.EOF {
		return
	}
	require.NoError(t, err)
	pendingProcessNode(t, store, 2)
	_, err = fmt.Fprintln(os.Stdout, "committed")
	require.NoError(t, err)
}
func TestExportSnapshot_ProcessPublicationBaselineMatchesMirror(t *testing.T) {
	dir := t.TempDir()
	store := pendingProcessStore(t, dir)
	armed, consumed := false, false
	callbacks := 0
	var writer *pendingChild
	// Set once before store use; arm only at native export construction boundary.
	store.SetClock(func() time.Time {
		if armed && !consumed {
			consumed = true
			callbacks++
			_, err := io.WriteString(writer.stdin, "commit\n")
			require.NoError(t, err)
			writer.awaitLine(t, "committed")
			writer.awaitExit(t)
		}
		return pendingProcessClock()
	})
	pendingProcessNode(t, store, 1)
	writer = startPendingProcess(t, "snapshotwriter", dir)
	writer.awaitLine(t, "ready")
	svc := NewSyncService(store, slog.Default(), pendingProcessClock)
	armed = true
	require.NoError(t, svc.ForceExport(context.Background(), dir), "native publication must complete before hash comparison")
	require.Equal(t, 1, callbacks)
	content, err := os.ReadFile(filepath.Join(dir, "tasks.json"))
	require.NoError(t, err)
	var mirror sqlite.ExportData
	require.NoError(t, json.Unmarshal(content, &mirror))
	require.NoError(t, sqlite.ValidateExport(&mirror))
	require.Equal(t, 1, mirror.NodeCount, "writer committed after first snapshot table reads")
	committed, err := store.Export(context.Background(), "", "")
	require.NoError(t, err)
	require.Equal(t, 2, committed.NodeCount, "real writer committed before snapshot returned")
	fileHash, err := os.ReadFile(filepath.Join(dir, "data", "sync.sha256"))
	require.NoError(t, err)
	require.Equal(t, fmt.Sprintf("%x", sha256.Sum256(content)), string(fileHash))
	expected, err := exportHash(&mirror, formCurrent)
	require.NoError(t, err)
	baseline, err := os.ReadFile(filepath.Join(dir, "data", "sync-db.sha256"))
	require.NoError(t, err)
	t.Logf("file nodes=%d committed nodes=%d mirror baseline=%s published baseline=%s", mirror.NodeCount, committed.NodeCount, expected, baseline)
	require.Equal(t, expected, string(baseline), "conflict baseline must describe the exact published snapshot")
}
