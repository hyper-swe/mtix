// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Received records survive later local-only changes.
package service_test

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestExchangeRecord_WrittenOnImportNotOnMutation(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("receipt publication is unavailable on this platform")
	}
	ctx := context.Background()
	svc, destination, dir := newTestSyncService(t)
	_, source, _ := newTestSyncService(t)
	incoming := makeExportData(t, source)
	writeTasksJSON(t, dir, incoming)
	mtixDir := filepath.Join(dir, ".mtix")
	require.NoError(t, svc.AutoImport(ctx, mtixDir))
	path := filepath.Join(mtixDir, "data", "exchange-record.json")
	received, err := os.ReadFile(path)
	require.NoError(t, err, "successful import records received provenance")
	info, err := os.Stat(path)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	require.NoError(t, destination.ClaimNode(ctx, "TEST-1", "local-agent"))
	require.NoError(t, svc.AutoExport(ctx, mtixDir))
	after, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, received, after, "local mutation and export retain prior received record")
}
