// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store"
	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// TestEnrolWorktree_RealHub_UnjournaledLocalChange_HubEditedSameField_Refuses_PG
// is the F17 scenario against the real hub: an import merge changed a journalled
// node's title with no journal row, tasks.json equals the store, and a teammate
// retitled the same node on the hub. The store is compared with a clone of the
// events it applied, so the local title is seen and the enrolment refuses with
// a report; nothing moved.
func TestEnrolWorktree_RealHub_UnjournaledLocalChange_HubEditedSameField_Refuses_PG(t *testing.T) {
	ctx := context.Background()
	f := newEnrolPGFixture(t)
	st := f.open(t)
	_, err := st.WriteDB().ExecContext(ctx, `UPDATE nodes SET title = 'imported title' WHERE id = 'TEST-1'`)
	require.NoError(t, err)
	require.NoError(t, st.Close())
	f.writeBoardFromWorktree(t)

	peer, err := sqlite.New(filepath.Join(t.TempDir(), "peer"), slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { _ = peer.Close() })
	_, _, err = pullLoop(ctx, testIngest(nil), f.pool, peer, transport.PullCursor{}, 100)
	require.NoError(t, err)
	title := "teammate title"
	require.NoError(t, peer.UpdateNode(ctx, "TEST-1", &store.NodeUpdate{Title: &title}))
	var pushErr bytes.Buffer
	_, err = pushLoop(ctx, &pushErr, f.pool, peer)
	require.NoError(t, err, pushErr.String())
	board, storeBefore := fileBytes(t, boardFilePath(f.root)), treeBytes(t, f.data)

	_, err = f.enrol()

	var refusal *service.WorktreeBoardError
	require.ErrorAs(t, err, &refusal)
	assert.Equal(t, service.BoardHubDiffers, refusal.Reason)
	assert.Contains(t, refusal.Diff["TEST-1"], "title")
	assert.Empty(t, f.enrolled)
	assert.Equal(t, storeBefore, treeBytes(t, f.data), "the store is byte-identical")
	assert.Equal(t, board, fileBytes(t, boardFilePath(f.root)), "tasks.json is byte-identical")
}

// TestEnrolWorktree_RealHub_HistoryExportOn_PrintedFixThenEnrols_PG: with
// history.export on a board written before the switch refuses (the file rule),
// the printed fix writes it through the real path, and the enrolment then
// succeeds against the real hub.
func TestEnrolWorktree_RealHub_HistoryExportOn_PrintedFixThenEnrols_PG(t *testing.T) {
	f := newEnrolPGFixture(t)
	f.writeBoardFromWorktree(t)
	require.NoError(t, os.WriteFile(filepath.Join(f.root, ".mtix", "config.yaml"),
		[]byte("prefix: TEST\nmax_depth: 10\nagent_stale_threshold: 30m\n"+historyConfig), 0o600))

	_, err := f.enrol()

	var refusal *service.WorktreeBoardError
	require.ErrorAs(t, err, &refusal)
	assert.Equal(t, service.BoardFileDiffers, refusal.Reason)
	assert.Empty(t, f.enrolled)

	runMtixSyncFixWithConfig(t, f.root, historyConfig)
	_, err = f.enrol()
	if errors.As(err, &refusal) {
		t.Logf("DIFF %v", refusal.Diff)
	}
	require.NoError(t, err)
	require.Len(t, f.enrolled, 1)
}
