// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/relay/bootstrap"
)

// TestRunRelayCloneImport_SyncedStore_JournalsNothing verifies a relay
// bootstrap snapshot, a copy of the board the relay already carries, changes
// a task of a store that syncs without journaling the change, so the board
// is not published again under new event ids (MTIX-112.17.16.16).
func TestRunRelayCloneImport_SyncedStore_JournalsNothing(t *testing.T) {
	initTestApp(t)
	require.NoError(t, runCreate("Local task", "", "", 3, "", "", "", "", ""))
	_, err := app.store.WriteDB().ExecContext(t.Context(), `UPDATE sync_events SET sync_status = 'pushed'`)
	require.NoError(t, err)
	relayDir := t.TempDir()
	_, err = bootstrap.ExportSnapshot(t.Context(), bootstrap.ExportRequest{
		Store: claimedExporter{}, RelayDir: relayDir, Project: "TEST", ExportedBy: "0123456789abcdef",
		CreatedAt: time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC), Positions: map[string]uint64{"0123456789abcdef": 1},
	})
	require.NoError(t, err)
	cmd := &cobra.Command{Use: "clone"}
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetOut(&bytes.Buffer{})

	require.NoError(t, runRelayCloneImport(t.Context(), cmd, relayDir, importFlags{prefer: "theirs"}))

	n, err := app.store.GetNode(t.Context(), "TEST-1")
	require.NoError(t, err)
	require.Equal(t, model.StatusInProgress, n.Status, "the snapshot's status was merged")
	var pending int
	require.NoError(t, app.store.WriteDB().QueryRowContext(t.Context(),
		`SELECT COUNT(*) FROM sync_events WHERE sync_status = 'pending'`).Scan(&pending))
	require.Zero(t, pending, "the snapshot merge journaled nothing")
}
