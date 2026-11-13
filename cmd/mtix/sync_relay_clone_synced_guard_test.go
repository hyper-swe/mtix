// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/relay/bootstrap"
	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

const cloneGuardSnapshotPeer = "0123456789abcdef"

// describedExporter exports the local store with the first task's
// description changed, as a snapshot from a peer that edited it.
type describedExporter struct{}

func (describedExporter) Export(ctx context.Context, project, version string) (*sqlite.ExportData, error) {
	data, err := app.store.Export(ctx, project, version)
	if err != nil {
		return nil, err
	}
	data.Nodes[0].Description = "edited by the other peer"
	if err := sqlite.RecomputeExportChecksum(data); err != nil {
		return nil, err
	}
	return data, nil
}

// cloneGuardSnapshot writes a snapshot of the current store with a changed
// description into a new relay directory and returns that directory.
func cloneGuardSnapshot(t *testing.T) string {
	t.Helper()
	relayDir := t.TempDir()
	_, err := bootstrap.ExportSnapshot(t.Context(), bootstrap.ExportRequest{
		Store: describedExporter{}, RelayDir: relayDir, Project: "TEST", ExportedBy: cloneGuardSnapshotPeer,
		CreatedAt: time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC),
		Positions: map[string]uint64{cloneGuardSnapshotPeer: 5},
	})
	require.NoError(t, err)
	return relayDir
}

func cloneGuardCmd() (*cobra.Command, *bytes.Buffer) {
	var stderr bytes.Buffer
	cmd := &cobra.Command{Use: "clone"}
	cmd.SetErr(&stderr)
	cmd.SetOut(&bytes.Buffer{})
	return cmd, &stderr
}

func ingestRS(t *testing.T) uint64 {
	t.Helper()
	pos, err := app.store.RelayIngestCursor(t.Context(), cloneGuardSnapshotPeer)
	require.NoError(t, err)
	return pos.RS
}

// TestRunRelayCloneImport_SyncedStoreWithTasks_RefusesAndWritesNothing
// verifies a relay clone into a store that holds tasks and already syncs is
// refused before any write or cursor advance (MTIX-112.17.16.43).
func TestRunRelayCloneImport_SyncedStoreWithTasks_RefusesAndWritesNothing(t *testing.T) {
	tests := []struct {
		name string
		sync func(t *testing.T)
	}{
		{"relay publisher cursor past zero", func(t *testing.T) {
			_, err := app.store.WriteDB().Exec(`INSERT INTO relay_push_cursor (id, cursor, pub_epoch, next_rs) VALUES (1, 0, 1, 7)`)
			require.NoError(t, err)
		}},
		{"relay ingest cursor for another peer", func(t *testing.T) {
			require.NoError(t, app.store.AdvanceRelayIngestCursor(t.Context(), "1123456789abcdef", sqlite.RelayIngestPosition{RS: 3}))
		}},
		{"events pushed to a hub", func(t *testing.T) { markEventsPushed(t) }},
		{"hub configured, nothing pushed yet", func(t *testing.T) { configureHub(t) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("MTIX_SYNC_DSN", "")
			initTestApp(t)
			seedTickets(t, 1)
			relayDir := cloneGuardSnapshot(t)
			tt.sync(t)
			boardBefore, eventsBefore := storeBoard(t), syncEventCount(t)
			cmd, _ := cloneGuardCmd()

			err := runRelayCloneImport(t.Context(), cmd, relayDir, importFlags{prefer: "theirs"})

			require.ErrorIs(t, err, sqlite.ErrRelayCloneOnSyncedStore)
			assert.Contains(t, err.Error(), "mtix init", "the refusal names the recovery")
			assert.Contains(t, err.Error(), "nothing was imported")
			assert.Equal(t, boardBefore, storeBoard(t), "a refusal writes nothing")
			assert.Equal(t, eventsBefore, syncEventCount(t), "a refusal journals nothing")
			assert.Zero(t, ingestRS(t), "a refusal does not advance the ingest cursor")
		})
	}
}

// TestRunRelayCloneImport_SyncServiceMissing_RefusesFailClosed verifies a
// store whose sync state cannot be read refuses rather than merging.
func TestRunRelayCloneImport_SyncServiceMissing_RefusesFailClosed(t *testing.T) {
	t.Setenv("MTIX_SYNC_DSN", "")
	initTestApp(t)
	seedTickets(t, 1)
	relayDir := cloneGuardSnapshot(t)
	boardBefore := storeBoard(t)
	app.syncSvc = nil
	cmd, _ := cloneGuardCmd()

	err := runRelayCloneImport(t.Context(), cmd, relayDir, importFlags{})

	require.Error(t, err)
	assert.Equal(t, boardBefore, storeBoard(t))
	assert.Zero(t, ingestRS(t))
}

// TestRunRelayCloneImport_UnreadableEvidence_RefusesFailClosed verifies a
// store holding tasks whose sync evidence cannot be read (a dangling secrets
// link, an unreadable task table) refuses and writes nothing.
func TestRunRelayCloneImport_UnreadableEvidence_RefusesFailClosed(t *testing.T) {
	t.Run("dangling secrets link", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("symlinks need privileges on this platform")
		}
		t.Setenv("MTIX_SYNC_DSN", "")
		initTestApp(t)
		seedTickets(t, 1)
		relayDir := cloneGuardSnapshot(t)
		boardBefore := storeBoard(t)
		link := filepath.Join(app.mtixDir, transport.SecretsFilename)
		require.NoError(t, os.Symlink(filepath.Join(t.TempDir(), "missing"), link))
		cmd, _ := cloneGuardCmd()

		err := runRelayCloneImport(t.Context(), cmd, relayDir, importFlags{})

		require.ErrorIs(t, err, sqlite.ErrRelayCloneOnSyncedStore)
		assert.Equal(t, boardBefore, storeBoard(t))
		assert.Zero(t, ingestRS(t))
	})
	t.Run("tasks readable but sync state unreadable", func(t *testing.T) {
		t.Setenv("MTIX_SYNC_DSN", "")
		initTestApp(t)
		seedTickets(t, 1)
		relayDir := cloneGuardSnapshot(t)
		boardBefore := storeBoard(t)
		_, err := app.store.WriteDB().Exec(`DROP TABLE relay_ingest_cursor`)
		require.NoError(t, err)
		cmd, _ := cloneGuardCmd()

		err = runRelayCloneImport(t.Context(), cmd, relayDir, importFlags{})

		require.ErrorContains(t, err, "relay clone failed")
		require.ErrorContains(t, err, "read whether the store syncs")
		assert.Equal(t, boardBefore, storeBoard(t), "an unreadable sync state writes nothing")
	})
	t.Run("closed store", func(t *testing.T) {
		t.Setenv("MTIX_SYNC_DSN", "")
		initTestApp(t)
		seedTickets(t, 1)
		relayDir := cloneGuardSnapshot(t)
		require.NoError(t, app.store.Close())
		cmd, _ := cloneGuardCmd()

		err := runRelayCloneImport(t.Context(), cmd, relayDir, importFlags{})

		require.ErrorContains(t, err, "relay clone failed")
	})
}

// TestRunRelayCloneImport_UnsyncedOrEmptyStore_StillImports verifies the
// guard leaves the fresh-joiner case and a store that does not sync alone.
func TestRunRelayCloneImport_UnsyncedOrEmptyStore_StillImports(t *testing.T) {
	t.Run("store with tasks that does not sync", func(t *testing.T) {
		t.Setenv("MTIX_SYNC_DSN", "")
		initTestApp(t)
		seedTickets(t, 1)
		relayDir := cloneGuardSnapshot(t)
		cmd, _ := cloneGuardCmd()

		require.NoError(t, runRelayCloneImport(t.Context(), cmd, relayDir, importFlags{}))
		assert.Equal(t, uint64(5), ingestRS(t))
	})
	t.Run("empty store that already has relay cursors", func(t *testing.T) {
		t.Setenv("MTIX_SYNC_DSN", "")
		initTestApp(t)
		seedTickets(t, 1)
		relayDir := cloneGuardSnapshot(t)
		_, err := app.store.WriteDB().Exec(`DELETE FROM nodes`)
		require.NoError(t, err)
		require.NoError(t, app.store.AdvanceRelayIngestCursor(t.Context(), "1123456789abcdef", sqlite.RelayIngestPosition{RS: 3}))
		cmd, _ := cloneGuardCmd()

		require.NoError(t, runRelayCloneImport(t.Context(), cmd, relayDir, importFlags{}))
		assert.Equal(t, uint64(5), ingestRS(t))
	})
}

func mustPeerID(t *testing.T) string {
	t.Helper()
	id, err := relayPeerID()
	require.NoError(t, err)
	return id
}
