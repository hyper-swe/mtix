// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

import (
	"context"
	"database/sql"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// MTIX-107.31: a replace import must not drop a node whose events the local
// sync log holds on a store that has exchanged events with a hub. The node
// would be gone locally while its events stay in the log, and a later pull
// acknowledges those events as held (MTIX-95.2) instead of applying them, so
// the node never comes back.

// exportWithout returns the store's export without the node id, with its
// node count and checksum recomputed: an older board that predates id.
func exportWithout(t *testing.T, s *sqlite.Store, id string) *sqlite.ExportData {
	t.Helper()
	data, err := s.Export(context.Background(), "", "")
	require.NoError(t, err)
	kept := data.Nodes[:0]
	for _, n := range data.Nodes {
		if n.ID != id {
			kept = append(kept, n)
		}
	}
	data.Nodes = kept
	data.NodeCount = len(kept)
	require.NoError(t, sqlite.RecomputeExportChecksum(data))
	return data
}

// teammateCreateEvents returns the events of a node a teammate created in
// its own store, as the hub returns them on pull.
func teammateCreateEvents(t *testing.T, id string) []*model.SyncEvent {
	t.Helper()
	other, err := sqlite.New(filepath.Join(t.TempDir(), "teammate.db"), slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { _ = other.Close() })
	mustCreateNode(t, other, id, "")
	evs, err := other.ReadPendingEvents(context.Background(), 100)
	require.NoError(t, err)
	require.NotEmpty(t, evs)
	return evs
}

func nodeExists(t *testing.T, raw *sql.DB, id string) bool {
	t.Helper()
	return countRows(t, raw, `SELECT COUNT(*) FROM nodes WHERE id = ?`, id) == 1
}

func TestImport_ReplaceOnSyncedStore_KeepsNodesTheLogHolds(t *testing.T) {
	tests := []struct {
		name string
		// setup prepares the store and returns the events a later pull
		// returns, nil for a store that never synced.
		setup     func(t *testing.T, s *sqlite.Store, raw *sql.DB) []*model.SyncEvent
		drop      string
		wantError bool
	}{
		{"own pushed node the file lacks", func(t *testing.T, s *sqlite.Store, raw *sql.DB) []*model.SyncEvent {
			mustCreateNode(t, s, "MTIX-1", "")
			mustCreateNode(t, s, "MTIX-2", "")
			return pushPendingOwnEvents(t, raw)
		}, "MTIX-2", true},
		{"pulled node the file lacks", func(t *testing.T, s *sqlite.Store, _ *sql.DB) []*model.SyncEvent {
			mustCreateNode(t, s, "MTIX-1", "")
			evs := teammateCreateEvents(t, "MTIX-2")
			pullEvents(t, s, evs)
			return evs
		}, "MTIX-2", true},
		{"synced store, file holds every node", func(t *testing.T, s *sqlite.Store, raw *sql.DB) []*model.SyncEvent {
			mustCreateNode(t, s, "MTIX-1", "")
			return pushPendingOwnEvents(t, raw)
		}, "", false},
		{"store that never synced", func(t *testing.T, s *sqlite.Store, _ *sql.DB) []*model.SyncEvent {
			mustCreateNode(t, s, "MTIX-1", "")
			mustCreateNode(t, s, "MTIX-2", "")
			return nil
		}, "MTIX-2", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			s, raw := mutationTestStore(t)
			pulled := tt.setup(t, s, raw)
			board := exportWithout(t, s, tt.drop)

			_, err := s.Import(ctx, board, sqlite.ImportModeReplace, false)
			if !tt.wantError {
				require.NoError(t, err)
				if tt.drop != "" {
					assert.False(t, nodeExists(t, raw, tt.drop), "a store that never synced takes the file whole")
				}
				return
			}
			if err == nil {
				pullEvents(t, s, pulled)
				require.True(t, nodeExists(t, raw, tt.drop),
					"the replace dropped %s, and the pull after it did not restore it", tt.drop)
			}
			require.Error(t, err, "the replace would drop %s while the sync log keeps its events", tt.drop)
			assert.Contains(t, err.Error(), tt.drop, "the refusal names the node")
			assert.Contains(t, err.Error(), "mtix sync reconcile --discard-local", "the refusal names the recovery")
			require.True(t, nodeExists(t, raw, tt.drop), "a refused replace changes nothing")

			pullEvents(t, s, pulled) // the next pull returns the held events
			assert.True(t, nodeExists(t, raw, tt.drop), "the node is still on the board after the pull")
		})
	}
}
