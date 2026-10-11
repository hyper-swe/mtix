// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"database/sql"
	"testing"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/stretchr/testify/require"
)

func snapshotSeed(t *testing.T, store *Store) {
	t.Helper()
	now := snapshotClock()
	require.NoError(t, store.CreateNode(context.Background(), &model.Node{
		ID: "SNAP-1", Project: "SNAP", Seq: 1, Title: "Original coherent task", Status: model.StatusOpen,
		Priority: model.PriorityMedium, Weight: 1, NodeType: model.NodeTypeEpic, CreatedAt: now, UpdatedAt: now,
	}))
	require.NoError(t, store.WithTx(context.Background(), func(tx *sql.Tx) error {
		if _, err := tx.Exec(`INSERT INTO agents (agent_id,project,state,current_node_id) VALUES ('worker-a','SNAP','idle','SNAP-1')`); err != nil {
			return err
		}
		_, err := tx.Exec(`INSERT INTO sessions (id,agent_id,project,started_at,status) VALUES ('session-a','worker-a','SNAP','2026-10-11T00:00:00Z','active')`)
		return err
	}))
}
func TestExportSnapshot_ProcessInterleavingKeepsCoherentTables(t *testing.T) {
	dir := t.TempDir()
	store := snapshotProcessStore(t, dir)
	snapshotSeed(t, store)
	before, err := store.Export(context.Background(), "SNAP", "test")
	require.NoError(t, err)
	writer := startSnapshotWriter(t, dir)
	callbacks := 0
	connector := &snapshotConnector{path: store.dbPath, afterClose: func() error {
		callbacks++
		return writer.commitAndJoin()
	}}
	wrapped := sql.OpenDB(connector)
	original := store.readDB
	store.readDB = wrapped
	t.Cleanup(func() { store.readDB = original; require.NoError(t, wrapped.Close()) })
	actual, err := store.Export(context.Background(), "SNAP", "test")
	require.NoError(t, err, "native writer/SQL fixture must complete before behavioral comparison")
	require.Equal(t, 1, callbacks, "barrier fires once after actual node rows Close")
	after, err := store.Export(context.Background(), "SNAP", "test")
	require.NoError(t, err)
	require.Equal(t, 2, after.NodeCount, "real child writer committed coherent stateB")
	require.Len(t, after.Dependencies, 1)
	require.Len(t, after.Agents, 2)
	require.Len(t, after.Sessions, 2)
	require.NoError(t, ValidateExport(after))
	t.Logf("old nodes/deps/agents/sessions=%d/%d/%d/%d, interleaved=%d/%d/%d/%d, committed=%d/%d/%d/%d",
		before.NodeCount, len(before.Dependencies), len(before.Agents), len(before.Sessions),
		actual.NodeCount, len(actual.Dependencies), len(actual.Agents), len(actual.Sessions),
		after.NodeCount, len(after.Dependencies), len(after.Agents), len(after.Sessions))
	require.Equal(t, before, actual, "all exported tables must describe the same old WAL snapshot")
}
func TestExportSnapshot_ProcessWriterProtocol(t *testing.T) {
	dir := t.TempDir()
	store := snapshotProcessStore(t, dir)
	snapshotSeed(t, store)
	writer := startSnapshotWriter(t, dir)
	require.NoError(t, writer.commitAndJoin())
	data, err := store.Export(context.Background(), "SNAP", "test")
	require.NoError(t, err)
	require.NoError(t, ValidateExport(data))
	require.Equal(t, 2, data.NodeCount)
	require.Len(t, data.Dependencies, 1)
	require.Len(t, data.Agents, 2)
	require.Len(t, data.Sessions, 2)
}

// Independently checks that the fixture forwards a real read-only transaction
// and preserves its WAL snapshot while the same child writer commits.
func TestExportSnapshot_NativeTransactionProtocol(t *testing.T) {
	dir := t.TempDir()
	store := snapshotProcessStore(t, dir)
	snapshotSeed(t, store)
	writer := startSnapshotWriter(t, dir)
	connector := &snapshotConnector{path: store.dbPath, afterClose: writer.commitAndJoin}
	db := sql.OpenDB(connector)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	tx, err := db.BeginTx(context.Background(), &sql.TxOptions{ReadOnly: true})
	require.NoError(t, err)
	t.Cleanup(func() {
		err := tx.Rollback()
		if err != nil {
			require.ErrorIs(t, err, sql.ErrTxDone)
		}
	})
	nodes, err := store.exportNodes(context.Background(), tx)
	require.NoError(t, err)
	require.Len(t, nodes, 1)
	for _, table := range []string{"nodes", "agents", "sessions"} {
		var count int
		require.NoError(t, tx.QueryRow("SELECT COUNT(*) FROM "+table).Scan(&count)) //nolint:gosec // fixed fixture table allowlist above
		require.Equal(t, 1, count, "native read transaction must retain old "+table)
	}
	var deps int
	require.NoError(t, tx.QueryRow("SELECT COUNT(*) FROM dependencies").Scan(&deps))
	require.Zero(t, deps)
	require.NoError(t, tx.Commit())
	data, err := store.Export(context.Background(), "SNAP", "test")
	require.NoError(t, err)
	require.Equal(t, 2, data.NodeCount, "child commit succeeds independently of retained native read snapshot")
}
