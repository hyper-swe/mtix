// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// MTIX-107.31.7: the sync service the commands build carries the local hub
// and relay evidence, so the automatic import records a configured relay as
// the hub-connected mark before it imports, and a replace guard counts it.
func TestNewSyncService_RelayConfigured_AutoImportRecordsTheHubMark(t *testing.T) {
	tests := []struct {
		name      string
		configure func(t *testing.T)
		wantMark  bool
	}{
		{"relay directory configured", func(t *testing.T) {
			_, err := app.configSvc.Set("sync.relay.dir", t.TempDir())
			require.NoError(t, err)
		}, true},
		{"nothing configured", func(*testing.T) {}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("MTIX_SYNC_DSN", "")
			initTestApp(t)
			seedTickets(t, 2)
			tt.configure(t)
			// A changed tasks.json for the automatic import to read.
			board, err := os.ReadFile(writeExportOf(t))
			require.NoError(t, err)
			require.NoError(t, os.WriteFile(filepath.Join(app.mtixDir, "tasks.json"), board, 0o644))

			svc := newSyncService(time.Now, syncServiceOptions{})
			require.NoError(t, svc.AutoImport(context.Background(), app.mtixDir))

			mark, err := app.store.HubMark(context.Background())
			require.NoError(t, err)
			assert.Equal(t, tt.wantMark, mark.Set)
		})
	}
}
