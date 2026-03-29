// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

import (
	"context"
	"database/sql"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// newTestStore creates a fresh SQLite store for testing.
// Uses t.TempDir() for file-based DB (not :memory:) per EXECUTION-PLAN.md.
func newTestStore(t *testing.T) *sqlite.Store {
	t.Helper()

	dbPath := filepath.Join(t.TempDir(), "test.db")
	s, err := sqlite.New(dbPath, slog.Default())
	require.NoError(t, err, "failed to create test store")

	t.Cleanup(func() {
		require.NoError(t, s.Close(), "failed to close test store")
	})

	return s
}

// newTestDB opens a raw database connection for verification queries.
func newTestDB(t *testing.T, dbPath string) *sql.DB {
	t.Helper()

	db, err := sql.Open("sqlite", dbPath)
	require.NoError(t, err)

	_, err = db.Exec("PRAGMA foreign_keys = ON")
	require.NoError(t, err)

	t.Cleanup(func() {
		require.NoError(t, db.Close())
	})

	return db
}

// TestNew_CreatesDB verifies the store creates a database file.
func TestNew_CreatesDB(t *testing.T) {
	s := newTestStore(t)
	require.NotNil(t, s)
}

// TestNew_WALModeActive verifies WAL journal mode is enabled.
func TestNew_WALModeActive(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")
	s, err := sqlite.New(dbPath, slog.Default())
	require.NoError(t, err)
	defer func() { require.NoError(t, s.Close()) }()

	db := newTestDB(t, dbPath)

	var journalMode string
	err = db.QueryRow("PRAGMA journal_mode").Scan(&journalMode)
	require.NoError(t, err)
	assert.Equal(t, "wal", journalMode, "journal mode should be WAL")
}

// TestNew_ForeignKeysEnabled verifies foreign keys are enforced.
func TestNew_ForeignKeysEnabled(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")
	s, err := sqlite.New(dbPath, slog.Default())
	require.NoError(t, err)
	defer func() { require.NoError(t, s.Close()) }()

	db := newTestDB(t, dbPath)

	var fkEnabled int
	err = db.QueryRow("PRAGMA foreign_keys").Scan(&fkEnabled)
	require.NoError(t, err)
	assert.Equal(t, 1, fkEnabled, "foreign keys should be enabled")
}

// TestClose_ShutsDownCleanly verifies clean shutdown.
func TestClose_ShutsDownCleanly(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")
	s, err := sqlite.New(dbPath, slog.Default())
	require.NoError(t, err)

	err = s.Close()
	assert.NoError(t, err, "close should succeed cleanly")
}

// TestSchema_AllTablesExist verifies all required tables are created per NFR-2.2.
func TestSchema_AllTablesExist(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")
	s, err := sqlite.New(dbPath, slog.Default())
	require.NoError(t, err)
	defer func() { require.NoError(t, s.Close()) }()

	db := newTestDB(t, dbPath)

	requiredTables := []string{
		"nodes",
		"dependencies",
		"sync_events",
		"agents",
		"sessions",
		"meta",
		"sequences",
		"nodes_fts",
	}

	for _, table := range requiredTables {
		t.Run(table, func(t *testing.T) {
			var name string
			// Use parameterized query for table name lookup.
			err := db.QueryRow(
				"SELECT name FROM sqlite_master WHERE type IN ('table', 'virtual table') AND name = ?",
				table,
			).Scan(&name)
			require.NoError(t, err, "table %s should exist", table)
			assert.Equal(t, table, name)
		})
	}
}

// TestSchema_AllIndexesExist verifies all required indexes per NFR-2.2.
func TestSchema_AllIndexesExist(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")
	s, err := sqlite.New(dbPath, slog.Default())
	require.NoError(t, err)
	defer func() { require.NoError(t, s.Close()) }()

	db := newTestDB(t, dbPath)

	requiredIndexes := []string{
		"idx_nodes_parent",
		"idx_nodes_status",
		"idx_nodes_priority",
		"idx_nodes_assignee",
		"idx_nodes_deleted",
		"idx_nodes_deferred",
		"idx_nodes_updated",
		"idx_deps_to",
	}

	for _, idx := range requiredIndexes {
		t.Run(idx, func(t *testing.T) {
			var name string
			err := db.QueryRow(
				"SELECT name FROM sqlite_master WHERE type = 'index' AND name = ?",
				idx,
			).Scan(&name)
			require.NoError(t, err, "index %s should exist", idx)
			assert.Equal(t, idx, name)
		})
	}
}

// TestSchema_FTSTriggersExist verifies FTS sync triggers per NFR-2.7.
func TestSchema_FTSTriggersExist(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")
	s, err := sqlite.New(dbPath, slog.Default())
	require.NoError(t, err)
	defer func() { require.NoError(t, s.Close()) }()

	db := newTestDB(t, dbPath)

	requiredTriggers := []string{
		"nodes_ai", // AFTER INSERT
		"nodes_ad", // AFTER DELETE
		"nodes_au", // AFTER UPDATE
	}

	for _, trigger := range requiredTriggers {
		t.Run(trigger, func(t *testing.T) {
			var name string
			err := db.QueryRow(
				"SELECT name FROM sqlite_master WHERE type = 'trigger' AND name = ?",
				trigger,
			).Scan(&name)
			require.NoError(t, err, "trigger %s should exist", trigger)
			assert.Equal(t, trigger, name)
		})
	}
}

// TestSchema_SchemaVersionStored verifies schema_version is in meta table.
func TestSchema_SchemaVersionStored(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")
	s, err := sqlite.New(dbPath, slog.Default())
	require.NoError(t, err)
	defer func() { require.NoError(t, s.Close()) }()

	db := newTestDB(t, dbPath)

	var version string
	err = db.QueryRow(
		"SELECT value FROM meta WHERE key = ?", "schema_version",
	).Scan(&version)
	require.NoError(t, err)
	assert.Equal(t, "1", version)
}

// TestSchema_ForeignKeysEnforced verifies FK constraints are active.
func TestSchema_ForeignKeysEnforced(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")
	s, err := sqlite.New(dbPath, slog.Default())
	require.NoError(t, err)
	defer func() { require.NoError(t, s.Close()) }()

	db := newTestDB(t, dbPath)

	// Attempt to insert a node with a non-existent parent.
	// This should fail if foreign keys are enforced.
	_, err = db.Exec(
		`INSERT INTO nodes (id, parent_id, depth, seq, project, title, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		"TEST-1.1", "NONEXISTENT", 1, 1, "TEST", "Child",
		"2026-03-10T00:00:00Z", "2026-03-10T00:00:00Z",
	)
	assert.Error(t, err, "FK violation should be rejected")
}

// TestNextSequence_FirstCall_Returns1 verifies first sequence is 1.
func TestNextSequence_FirstCall_Returns1(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	seq, err := s.NextSequence(ctx, "TEST:")
	require.NoError(t, err)
	assert.Equal(t, 1, seq)
}

// TestNextSequence_SecondCall_Returns2 verifies monotonic increment.
func TestNextSequence_SecondCall_Returns2(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	_, err := s.NextSequence(ctx, "TEST:")
	require.NoError(t, err)

	seq, err := s.NextSequence(ctx, "TEST:")
	require.NoError(t, err)
	assert.Equal(t, 2, seq)
}

// TestNextSequence_DifferentParents_IndependentCounters verifies isolation.
func TestNextSequence_DifferentParents_IndependentCounters(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	// Advance parent A to 3.
	for i := 0; i < 3; i++ {
		_, err := s.NextSequence(ctx, "TEST:PROJ-1")
		require.NoError(t, err)
	}

	// Parent B should start at 1 — independent counter.
	seq, err := s.NextSequence(ctx, "TEST:PROJ-2")
	require.NoError(t, err)
	assert.Equal(t, 1, seq, "different parent should have independent counter")

	// Parent A should be at 4.
	seq, err = s.NextSequence(ctx, "TEST:PROJ-1")
	require.NoError(t, err)
	assert.Equal(t, 4, seq)
}

// TestNextSequence_ConcurrentCalls_NoCollisions verifies no lost updates
// when multiple goroutines call NextSequence concurrently.
func TestNextSequence_ConcurrentCalls_NoCollisions(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	const goroutines = 10
	const callsPerGoroutine = 10
	totalCalls := goroutines * callsPerGoroutine

	results := make(chan int, totalCalls)
	errCh := make(chan error, totalCalls)

	var wg sync.WaitGroup
	wg.Add(goroutines)

	for g := 0; g < goroutines; g++ {
		go func() {
			defer wg.Done()
			for i := 0; i < callsPerGoroutine; i++ {
				seq, err := s.NextSequence(ctx, "CONCURRENT:")
				if err != nil {
					errCh <- err
					return
				}
				results <- seq
			}
		}()
	}

	wg.Wait()
	close(results)
	close(errCh)

	// No errors should have occurred.
	for err := range errCh {
		t.Fatalf("unexpected error during concurrent NextSequence: %v", err)
	}

	// Collect all returned values — every value should be unique.
	seen := make(map[int]bool, totalCalls)
	for seq := range results {
		assert.False(t, seen[seq], "duplicate sequence value %d detected", seq)
		seen[seq] = true
	}

	// Final sequence value should equal total calls.
	assert.Equal(t, totalCalls, len(seen),
		"should have exactly %d unique sequence values", totalCalls)
}

// TestSchema_Idempotent verifies schema creation is idempotent.
func TestSchema_Idempotent(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")

	// Create store twice on the same DB — should not error.
	s1, err := sqlite.New(dbPath, slog.Default())
	require.NoError(t, err)
	require.NoError(t, s1.Close())

	s2, err := sqlite.New(dbPath, slog.Default())
	require.NoError(t, err)
	require.NoError(t, s2.Close())
}

// TestNew_DirectoryPath_CreatesMtixDB verifies resolveDBPath creates mtix.db in dir.
func TestNew_DirectoryPath_CreatesMtixDB(t *testing.T) {
	dir := t.TempDir()

	s, err := sqlite.New(dir, slog.Default())
	require.NoError(t, err)
	require.NoError(t, s.Close())

	// Verify mtix.db was created inside the directory.
	_, err = os.Stat(filepath.Join(dir, "mtix.db"))
	assert.NoError(t, err, "mtix.db should be created inside the directory")
}

// TestUpdateProgress_ExistingNode_UpdatesProgress verifies progress update.
func TestUpdateProgress_ExistingNode_UpdatesProgress(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	node := makeRootNode("PROJ-1", "PROJ", "Progress Node", now)
	require.NoError(t, s.CreateNode(ctx, node))

	err := s.UpdateProgress(ctx, "PROJ-1", 0.75)
	require.NoError(t, err)

	got, err := s.GetNode(ctx, "PROJ-1")
	require.NoError(t, err)
	assert.InDelta(t, 0.75, got.Progress, 0.001)
}

// TestUpdateProgress_NonExistent_ReturnsNotFound verifies missing node handling.
func TestUpdateProgress_NonExistent_ReturnsNotFound(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	err := s.UpdateProgress(ctx, "NONEXISTENT-1", 0.5)
	assert.ErrorIs(t, err, model.ErrNotFound)
}

// TestUpdateProgress_DeletedNode_ReturnsNotFound verifies deleted node handling.
func TestUpdateProgress_DeletedNode_ReturnsNotFound(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	node := makeRootNode("PROJ-1", "PROJ", "To Delete", now)
	require.NoError(t, s.CreateNode(ctx, node))
	require.NoError(t, s.DeleteNode(ctx, "PROJ-1", false, "admin"))

	err := s.UpdateProgress(ctx, "PROJ-1", 0.5)
	assert.ErrorIs(t, err, model.ErrNotFound)
}

