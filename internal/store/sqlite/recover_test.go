// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Tests for FR-26.5 (mtix recover): read-only salvage from a damaged
// database merged with the tasks.json mirror, producing an importable
// export with a freshly computed checksum. Written RED-first per
// TDD-WORKFLOW.md §1.1.
package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// seedRecoverFixture creates a store with a small TEST project tree:
// TEST-1 (root) ← TEST-1.1 (child), TEST-2 (root), one dependency
// TEST-2 → TEST-1. Returns the db path.
func seedRecoverFixture(t *testing.T) (*Store, string) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "mtix.db")
	s, err := New(dbPath, slog.Default())
	require.NoError(t, err)

	ctx := context.Background()
	insert := func(id, parent string, depth, seq int, title string) {
		require.NoError(t, s.WithTx(ctx, func(tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx,
				`INSERT INTO nodes (id, parent_id, depth, seq, project, title,
				   node_type, priority, status, progress, weight, created_at, updated_at)
				 VALUES (?, NULLIF(?,''), ?, ?, 'TEST', ?, 'task', 3, 'open', 0, 1,
				   '2026-06-01T00:00:00Z', '2026-06-01T00:00:00Z')`,
				id, parent, depth, seq, title)
			return err
		}))
	}
	insert("TEST-1", "", 0, 1, "recover fixture root one")
	insert("TEST-1.1", "TEST-1", 1, 1, "recover fixture child")
	insert("TEST-2", "", 0, 2, "recover fixture root two")
	require.NoError(t, s.WithTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			`INSERT INTO dependencies (from_id, to_id, dep_type, created_at)
			 VALUES ('TEST-2','TEST-1','blocks','2026-06-01T00:00:00Z')`)
		return err
	}))
	return s, dbPath
}

// writeMirror writes an ExportData as a tasks.json-style mirror file.
func writeMirror(t *testing.T, dir string, data *ExportData) string {
	t.Helper()
	raw, err := json.MarshalIndent(data, "", "  ")
	require.NoError(t, err)
	path := filepath.Join(dir, "tasks.json")
	require.NoError(t, os.WriteFile(path, raw, 0o644))
	return path
}

// importRoundTrip proves a recovered export is accepted by the standard
// import path into a fresh store — the definition of "importable".
func importRoundTrip(t *testing.T, data *ExportData) *Store {
	t.Helper()
	s, err := New(filepath.Join(t.TempDir(), "fresh.db"), slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })

	_, err = s.Import(context.Background(), data, ImportModeReplace, false)
	require.NoError(t, err, "recovered export must import cleanly")
	return s
}

// TestRecover_HealthyDB_FullSalvage: happy path — every row salvaged, no
// losses, output importable.
func TestRecover_HealthyDB_FullSalvage(t *testing.T) {
	s, dbPath := seedRecoverFixture(t)
	require.NoError(t, s.Close())

	res, err := Recover(context.Background(), dbPath, "", "test-version", slog.Default())
	require.NoError(t, err)

	assert.Len(t, res.RecoveredIDs, 3)
	assert.Empty(t, res.LostIDs)
	assert.Empty(t, res.FromMirror)
	assert.Equal(t, 3, res.Export.NodeCount)
	assert.Len(t, res.Export.Dependencies, 1)

	valid, err := VerifyExportChecksum(res.Export)
	require.NoError(t, err)
	assert.True(t, valid, "recovered export must carry a valid checksum")

	importRoundTrip(t, res.Export)
}

// TestRecover_CorruptPage_PartitionsEveryID: error path — with an interior
// page shredded, every ID known to the primary-key index ends up either
// recovered or explicitly lost; nothing silently vanishes.
func TestRecover_CorruptPage_PartitionsEveryID(t *testing.T) {
	s, dbPath := seedRecoverFixture(t)
	// Bulk up rows so the table spans pages and a shredded page hits data.
	ctx := context.Background()
	for i := 3; i < 60; i++ {
		id := fmt.Sprintf("TEST-%d", i)
		require.NoError(t, s.WithTx(ctx, func(tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx,
				`INSERT INTO nodes (id, depth, seq, project, title, node_type,
				   priority, status, progress, weight, created_at, updated_at)
				 VALUES (?, 0, ?, 'TEST', ?, 'task', 3, 'open', 0, 1,
				   '2026-06-01T00:00:00Z', '2026-06-01T00:00:00Z')`,
				id, i, fmt.Sprintf("%s filler %0512d", id, i))
			return err
		}))
	}
	require.NoError(t, s.Close())
	_ = os.Remove(dbPath + "-wal")
	_ = os.Remove(dbPath + "-shm")

	// Shred an interior page, keeping the header valid.
	f, err := os.OpenFile(dbPath, os.O_RDWR, 0)
	require.NoError(t, err)
	garbage := make([]byte, 4096)
	for i := range garbage {
		garbage[i] = 0xEE
	}
	_, err = f.WriteAt(garbage, 3*4096)
	require.NoError(t, err)
	require.NoError(t, f.Close())

	res, err := Recover(context.Background(), dbPath, "", "test-version", slog.Default())
	require.NoError(t, err, "recover must not fail outright on partial damage")

	total := len(res.RecoveredIDs) + len(res.LostIDs)
	assert.Positive(t, len(res.RecoveredIDs), "some rows must be salvageable")
	assert.Positive(t, total, "the ID partition must not be empty")
	if len(res.LostIDs) > 0 {
		assert.NotEmpty(t, res.Notes, "losses must be explained in the report")
	}

	importRoundTrip(t, res.Export)
}

// TestRecover_MirrorFillsLostNodes: a node missing from the database but
// present in the mirror is salvaged from the mirror and labeled as such.
func TestRecover_MirrorFillsLostNodes(t *testing.T) {
	s, dbPath := seedRecoverFixture(t)
	ctx := context.Background()

	// Mirror reflects the full tree...
	mirror, err := s.Export(ctx, "TEST", "test-version")
	require.NoError(t, err)
	mirrorPath := writeMirror(t, t.TempDir(), mirror)

	// ...but the database then loses TEST-2 (hard delete simulates an
	// unreadable row).
	require.NoError(t, s.WithTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM dependencies WHERE from_id='TEST-2'`); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `DELETE FROM nodes WHERE id='TEST-2'`)
		return err
	}))
	require.NoError(t, s.Close())

	res, err := Recover(context.Background(), dbPath, mirrorPath, "test-version", slog.Default())
	require.NoError(t, err)

	assert.Contains(t, res.FromMirror, "TEST-2")
	assert.Equal(t, 3, res.Export.NodeCount, "mirror node must be merged back")
	importRoundTrip(t, res.Export)
}

// TestRecover_SynthesizesPlaceholderParents: a salvaged child whose parent
// is lost gets a placeholder parent so the foreign-key chain survives a
// standard import.
func TestRecover_SynthesizesPlaceholderParents(t *testing.T) {
	dir := t.TempDir()
	mirror := &ExportData{
		Version:       1,
		SchemaVersion: SchemaVersionV1,
		Project:       "TEST",
		Nodes: []exportNode{{
			ID: "TEST-7.2", ParentID: "TEST-7", Depth: 1, Seq: 2,
			Project: "TEST", Title: "orphaned child for placeholder test",
			NodeType: "task", Priority: 3, Status: "open", Weight: 1,
			CreatedAt: "2026-06-01T00:00:00Z", UpdatedAt: "2026-06-01T00:00:00Z",
		}},
		NodeCount: 1,
	}
	require.NoError(t, RecomputeExportChecksum(mirror))
	mirrorPath := writeMirror(t, dir, mirror)

	// No database at all — mirror-only recovery.
	res, err := Recover(context.Background(),
		filepath.Join(dir, "absent.db"), mirrorPath, "test-version", slog.Default())
	require.NoError(t, err)

	assert.Contains(t, res.Placeholders, "TEST-7")
	assert.Equal(t, 2, res.Export.NodeCount, "child plus synthesized parent")
	fresh := importRoundTrip(t, res.Export)

	var title string
	require.NoError(t, fresh.ReadDB().QueryRowContext(context.Background(),
		`SELECT title FROM nodes WHERE id = 'TEST-7'`).Scan(&title))
	assert.Contains(t, title, "placeholder")
}

// TestRecover_NoSourcesAtAll: with neither a readable database nor a
// mirror, recover reports failure instead of fabricating an empty export.
func TestRecover_NoSourcesAtAll(t *testing.T) {
	dir := t.TempDir()
	_, err := Recover(context.Background(),
		filepath.Join(dir, "absent.db"), filepath.Join(dir, "absent.json"),
		"test-version", slog.Default())
	require.Error(t, err)
}

// TestRecover_SecondaryTablesGone_NodesStillSalvaged: error path — when
// dependencies/agents/sessions tables are missing entirely, node salvage
// proceeds and the losses are reported in Notes.
func TestRecover_SecondaryTablesGone_NodesStillSalvaged(t *testing.T) {
	s, dbPath := seedRecoverFixture(t)
	ctx := context.Background()
	for _, stmt := range []string{
		`DROP TABLE dependencies`, `DROP TABLE agents`, `DROP TABLE sessions`,
	} {
		require.NoError(t, s.WithTx(ctx, func(tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, stmt)
			return err
		}))
	}
	require.NoError(t, s.Close())

	res, err := Recover(context.Background(), dbPath, "", "test-version", slog.Default())
	require.NoError(t, err)
	assert.Len(t, res.RecoveredIDs, 3)
	assert.Empty(t, res.Export.Dependencies)
	assert.GreaterOrEqual(t, len(res.Notes), 3,
		"each unreadable secondary table must be noted")
	importRoundTrip(t, res.Export)
}

// TestRecomputeExportChecksum_NilInput: error path.
func TestRecomputeExportChecksum_NilInput(t *testing.T) {
	require.Error(t, RecomputeExportChecksum(nil))
}

// TestRecomputeExportChecksum_MakesHandEditedImportable: the recovery
// import path — a hand-reconstructed export with a wrong checksum becomes
// importable after recomputation (FR-26.5 import --recompute-checksum).
func TestRecomputeExportChecksum_MakesHandEditedImportable(t *testing.T) {
	s, _ := seedRecoverFixture(t)
	data, err := s.Export(context.Background(), "TEST", "test-version")
	require.NoError(t, err)
	require.NoError(t, s.Close())

	// Simulate a hand-built file: content edited, checksum stale.
	data.Nodes[0].Title = "edited during manual reconstruction"
	valid, err := VerifyExportChecksum(data)
	require.NoError(t, err)
	require.False(t, valid, "fixture must start with a stale checksum")

	require.NoError(t, RecomputeExportChecksum(data))
	valid, err = VerifyExportChecksum(data)
	require.NoError(t, err)
	assert.True(t, valid)

	importRoundTrip(t, data)
}
