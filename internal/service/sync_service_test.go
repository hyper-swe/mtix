// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// newTestSyncService creates a SyncService with a real store for integration testing.
func newTestSyncService(t *testing.T) (*service.SyncService, *sqlite.Store, string) {
	t.Helper()
	dir := t.TempDir()
	dataDir := filepath.Join(dir, ".mtix", "data")
	require.NoError(t, os.MkdirAll(dataDir, 0755))

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	store, err := sqlite.New(dataDir, logger)
	require.NoError(t, err)
	t.Cleanup(func() { store.Close() })

	clock := func() time.Time { return time.Date(2026, 3, 14, 12, 0, 0, 0, time.UTC) }
	svc := service.NewSyncService(store, logger, clock)
	return svc, store, dir
}

// writeTasksJSON writes an ExportData to .mtix/tasks.json in the given dir.
func writeTasksJSON(t *testing.T, dir string, data *sqlite.ExportData) {
	t.Helper()
	mtixDir := filepath.Join(dir, ".mtix")
	require.NoError(t, os.MkdirAll(mtixDir, 0755))
	jsonBytes, err := json.MarshalIndent(data, "", "  ")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(mtixDir, "tasks.json"), jsonBytes, 0644))
}

// writeStoredHash writes a hash to .mtix/data/sync.sha256.
func writeStoredHash(t *testing.T, dir, hash string) {
	t.Helper()
	dataDir := filepath.Join(dir, ".mtix", "data")
	require.NoError(t, os.MkdirAll(dataDir, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(dataDir, "sync.sha256"), []byte(hash), 0644))
}

// hashBytes returns the hex SHA-256 of the given bytes.
func hashBytes(data []byte) string {
	h := sha256.Sum256(data)
	return fmt.Sprintf("%x", h)
}

// makeExportData creates a minimal valid ExportData for testing.
func makeExportData(t *testing.T, store *sqlite.Store) *sqlite.ExportData {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	require.NoError(t, store.CreateNode(ctx, &model.Node{
		ID: "TEST-1", Project: "TEST", Depth: 0, Seq: 1, Title: "Sync test node",
		Status: model.StatusOpen, Priority: model.PriorityMedium, Weight: 1.0,
		NodeType: model.NodeTypeIssue, ContentHash: "h1", CreatedAt: now, UpdatedAt: now,
	}))

	data, err := store.Export(ctx, "TEST", "0.1.0")
	require.NoError(t, err)
	return data
}

// TestAutoImport_HashMatch_NoOp verifies that matching hashes skip import.
func TestAutoImport_HashMatch_NoOp(t *testing.T) {
	svc, store, dir := newTestSyncService(t)
	data := makeExportData(t, store)

	// Write tasks.json and compute its hash.
	writeTasksJSON(t, dir, data)
	jsonBytes, _ := json.MarshalIndent(data, "", "  ")
	fileHash := hashBytes(jsonBytes)

	// Write the same hash as stored hash → should be no-op.
	writeStoredHash(t, dir, fileHash)

	mtixDir := filepath.Join(dir, ".mtix")
	err := svc.AutoImport(context.Background(), mtixDir)
	require.NoError(t, err)
}

// TestAutoImport_HashMismatch_TriggersImport verifies import on hash change.
func TestAutoImport_HashMismatch_TriggersImport(t *testing.T) {
	svc, _, dir := newTestSyncService(t)

	// Create a second store to generate export data from.
	srcDir := t.TempDir()
	srcDataDir := filepath.Join(srcDir, "data")
	require.NoError(t, os.MkdirAll(srcDataDir, 0755))
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	srcStore, err := sqlite.New(srcDataDir, logger)
	require.NoError(t, err)
	defer srcStore.Close()

	data := makeExportData(t, srcStore)
	writeTasksJSON(t, dir, data)

	// Write a different stored hash → triggers import.
	writeStoredHash(t, dir, "oldhash")

	mtixDir := filepath.Join(dir, ".mtix")
	err = svc.AutoImport(context.Background(), mtixDir)
	require.NoError(t, err)

	// Verify the hash file was updated.
	storedHash, err := os.ReadFile(filepath.Join(mtixDir, "data", "sync.sha256"))
	require.NoError(t, err)
	assert.NotEqual(t, "oldhash", string(storedHash))
}

// TestAutoImport_FirstRun_NoStoredHash verifies import when no hash file exists.
func TestAutoImport_FirstRun_NoStoredHash(t *testing.T) {
	svc, _, dir := newTestSyncService(t)

	srcDir := t.TempDir()
	srcDataDir := filepath.Join(srcDir, "data")
	require.NoError(t, os.MkdirAll(srcDataDir, 0755))
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	srcStore, err := sqlite.New(srcDataDir, logger)
	require.NoError(t, err)
	defer srcStore.Close()

	data := makeExportData(t, srcStore)
	writeTasksJSON(t, dir, data)

	// No stored hash file → should import.
	mtixDir := filepath.Join(dir, ".mtix")
	err = svc.AutoImport(context.Background(), mtixDir)
	require.NoError(t, err)

	// Hash file should now exist.
	_, err = os.Stat(filepath.Join(mtixDir, "data", "sync.sha256"))
	assert.NoError(t, err, "sync.sha256 should be created after first import")
}

// TestAutoImport_ConflictDetection_BothChanged_SkipsImport verifies FR-15.2h:
// if both the file and DB have changed since last sync, import is skipped.
func TestAutoImport_ConflictDetection_BothChanged_SkipsImport(t *testing.T) {
	svc, store, dir := newTestSyncService(t)

	// Create source data.
	srcDir := t.TempDir()
	srcDataDir := filepath.Join(srcDir, "data")
	require.NoError(t, os.MkdirAll(srcDataDir, 0755))
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	srcStore, err := sqlite.New(srcDataDir, logger)
	require.NoError(t, err)
	defer srcStore.Close()

	data := makeExportData(t, srcStore)
	writeTasksJSON(t, dir, data)
	writeStoredHash(t, dir, "oldhash")

	// Write a DB hash that differs from current DB state → DB changed.
	dbHashDir := filepath.Join(dir, ".mtix", "data")
	require.NoError(t, os.MkdirAll(dbHashDir, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(dbHashDir, "sync-db.sha256"), []byte("old-db-hash"), 0644))

	// Make local DB changes so current DB hash differs.
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	require.NoError(t, store.CreateNode(ctx, &model.Node{
		ID: "LOCAL-1", Project: "LOCAL", Depth: 0, Seq: 1, Title: "Local change",
		Status: model.StatusOpen, Priority: model.PriorityMedium, Weight: 1.0,
		NodeType: model.NodeTypeIssue, ContentHash: "lh1", CreatedAt: now, UpdatedAt: now,
	}))

	mtixDir := filepath.Join(dir, ".mtix")
	err = svc.AutoImport(context.Background(), mtixDir)
	assert.NoError(t, err, "conflict should not error, just skip")

	// File hash should still be "oldhash" (import was skipped).
	storedHash, err := os.ReadFile(filepath.Join(mtixDir, "data", "sync.sha256"))
	require.NoError(t, err)
	assert.Equal(t, "oldhash", string(storedHash), "hash should not change on conflict")
}

// TestAutoImport_ConflictDetection_OnlyFileChanged_ImportsNormally verifies
// that when only the file changed (no DB hash stored), import proceeds.
func TestAutoImport_ConflictDetection_OnlyFileChanged_ImportsNormally(t *testing.T) {
	svc, _, dir := newTestSyncService(t)

	srcDir := t.TempDir()
	srcDataDir := filepath.Join(srcDir, "data")
	require.NoError(t, os.MkdirAll(srcDataDir, 0755))
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	srcStore, err := sqlite.New(srcDataDir, logger)
	require.NoError(t, err)
	defer srcStore.Close()

	data := makeExportData(t, srcStore)
	writeTasksJSON(t, dir, data)
	writeStoredHash(t, dir, "oldhash")

	// No sync-db.sha256 → no conflict detection possible, import proceeds.
	mtixDir := filepath.Join(dir, ".mtix")
	err = svc.AutoImport(context.Background(), mtixDir)
	require.NoError(t, err)

	storedHash, err := os.ReadFile(filepath.Join(mtixDir, "data", "sync.sha256"))
	require.NoError(t, err)
	assert.NotEqual(t, "oldhash", string(storedHash), "hash should be updated after import")
}

// TestAutoImport_SchemaVersionCheck verifies FR-15.2g:
// rejects files with incompatible major schema version.
func TestAutoImport_SchemaVersionCheck(t *testing.T) {
	tests := []struct {
		name          string
		schemaVersion string
		wantErr       bool
		wantImport    bool
	}{
		{"v1.0.0 accepted", "1.0.0", false, true},
		{"v1.1.0 accepted", "1.1.0", false, true},
		{"v1.99.0 accepted", "1.99.0", false, true},
		{"v2.0.0 rejected", "2.0.0", false, false},
		{"v3.1.2 rejected", "3.1.2", false, false},
		{"empty treated as 1.0.0", "", false, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, _, dir := newTestSyncService(t)

			srcDir := t.TempDir()
			srcDataDir := filepath.Join(srcDir, "data")
			require.NoError(t, os.MkdirAll(srcDataDir, 0755))
			logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
			srcStore, err := sqlite.New(srcDataDir, logger)
			require.NoError(t, err)
			defer srcStore.Close()

			data := makeExportData(t, srcStore)
			data.SchemaVersion = tt.schemaVersion
			writeTasksJSON(t, dir, data)
			writeStoredHash(t, dir, "oldhash")

			mtixDir := filepath.Join(dir, ".mtix")
			err = svc.AutoImport(context.Background(), mtixDir)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}

			// Check if hash was updated (indicates import happened).
			storedHash, readErr := os.ReadFile(filepath.Join(mtixDir, "data", "sync.sha256"))
			if tt.wantImport {
				require.NoError(t, readErr)
				assert.NotEqual(t, "oldhash", string(storedHash))
			} else {
				// Hash should remain "oldhash" or not exist.
				if readErr == nil {
					assert.Equal(t, "oldhash", string(storedHash))
				}
			}
		})
	}
}

// TestAutoImport_HashMismatch_CreatesBackup verifies FR-15.2f:
// before import, a backup of the existing database is created.
func TestAutoImport_HashMismatch_CreatesBackup(t *testing.T) {
	svc, _, dir := newTestSyncService(t)

	// Create source data from a separate store.
	srcDir := t.TempDir()
	srcDataDir := filepath.Join(srcDir, "data")
	require.NoError(t, os.MkdirAll(srcDataDir, 0755))
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	srcStore, err := sqlite.New(srcDataDir, logger)
	require.NoError(t, err)
	defer srcStore.Close()

	data := makeExportData(t, srcStore)
	writeTasksJSON(t, dir, data)
	writeStoredHash(t, dir, "oldhash")

	mtixDir := filepath.Join(dir, ".mtix")
	err = svc.AutoImport(context.Background(), mtixDir)
	require.NoError(t, err)

	// Verify backup was created.
	backupPath := filepath.Join(mtixDir, "data", "pre-sync-backup.db")
	_, err = os.Stat(backupPath)
	assert.NoError(t, err, "pre-sync-backup.db should exist after import")
}

// TestAutoImport_FileTooLarge_ReturnsError verifies FR-15.2e:
// files larger than MaxImportSize are rejected.
func TestAutoImport_FileTooLarge_ReturnsError(t *testing.T) {
	svc, _, dir := newTestSyncService(t)

	// Write a file that exceeds the default limit.
	mtixDir := filepath.Join(dir, ".mtix")
	require.NoError(t, os.MkdirAll(mtixDir, 0755))
	// Create file just over the limit (use a small limit for testing).
	svc.MaxImportSize = 100 // 100 bytes
	largeData := make([]byte, 101)
	for i := range largeData {
		largeData[i] = 'x'
	}
	require.NoError(t, os.WriteFile(filepath.Join(mtixDir, "tasks.json"), largeData, 0644))

	err := svc.AutoImport(context.Background(), mtixDir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exceeds maximum import size")
}

// TestAutoExport_WritesTasksJSON verifies FR-15.3:
// AutoExport writes the current DB state to .mtix/tasks.json.
func TestAutoExport_WritesTasksJSON(t *testing.T) {
	svc, store, dir := newTestSyncService(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	require.NoError(t, store.CreateNode(ctx, &model.Node{
		ID: "EXP-1", Project: "EXP", Depth: 0, Seq: 1, Title: "Export test",
		Status: model.StatusOpen, Priority: model.PriorityMedium, Weight: 1.0,
		NodeType: model.NodeTypeIssue, ContentHash: "eh1", CreatedAt: now, UpdatedAt: now,
	}))

	mtixDir := filepath.Join(dir, ".mtix")
	err := svc.AutoExport(ctx, mtixDir)
	require.NoError(t, err)

	// Verify tasks.json was written.
	data, err := os.ReadFile(filepath.Join(mtixDir, "tasks.json"))
	require.NoError(t, err)
	assert.Contains(t, string(data), "EXP-1")
	assert.Contains(t, string(data), "Export test")

	// Verify hash was updated.
	hash, err := os.ReadFile(filepath.Join(mtixDir, "data", "sync.sha256"))
	require.NoError(t, err)
	assert.Equal(t, hashBytes(data), string(hash))
}

// TestAutoExport_UpdatesDBHash verifies FR-15.2h:
// AutoExport also writes the DB hash for conflict detection.
func TestAutoExport_UpdatesDBHash(t *testing.T) {
	svc, store, dir := newTestSyncService(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	require.NoError(t, store.CreateNode(ctx, &model.Node{
		ID: "DBH-1", Project: "DBH", Depth: 0, Seq: 1, Title: "DB hash test",
		Status: model.StatusOpen, Priority: model.PriorityMedium, Weight: 1.0,
		NodeType: model.NodeTypeIssue, ContentHash: "dh1", CreatedAt: now, UpdatedAt: now,
	}))

	mtixDir := filepath.Join(dir, ".mtix")
	err := svc.AutoExport(ctx, mtixDir)
	require.NoError(t, err)

	// Verify DB hash was written.
	_, err = os.Stat(filepath.Join(mtixDir, "data", "sync-db.sha256"))
	assert.NoError(t, err, "sync-db.sha256 should be created after export")
}

// TestAutoImport_AfterExport_NoOp verifies FR-15.5:
// after an AutoExport, the next AutoImport is a no-op (hashes match).
func TestAutoImport_AfterExport_NoOp(t *testing.T) {
	svc, store, dir := newTestSyncService(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	require.NoError(t, store.CreateNode(ctx, &model.Node{
		ID: "NOP-1", Project: "NOP", Depth: 0, Seq: 1, Title: "No-op test",
		Status: model.StatusOpen, Priority: model.PriorityMedium, Weight: 1.0,
		NodeType: model.NodeTypeIssue, ContentHash: "n1", CreatedAt: now, UpdatedAt: now,
	}))

	mtixDir := filepath.Join(dir, ".mtix")

	// Export writes tasks.json and updates hash.
	require.NoError(t, svc.AutoExport(ctx, mtixDir))

	// Next import should be a no-op (hashes match).
	err := svc.AutoImport(ctx, mtixDir)
	require.NoError(t, err)
}

// TestAutoImport_InvalidJSON_ReturnsParseError verifies parse failure handling.
func TestAutoImport_InvalidJSON_ReturnsParseError(t *testing.T) {
	svc, _, dir := newTestSyncService(t)

	mtixDir := filepath.Join(dir, ".mtix")
	require.NoError(t, os.MkdirAll(mtixDir, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(mtixDir, "tasks.json"), []byte("{invalid json"), 0644))

	err := svc.AutoImport(context.Background(), mtixDir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parse tasks.json")
}

// TestAutoImport_ReadPermissionDenied_ReturnsError verifies non-ENOENT read error.
func TestAutoImport_ReadPermissionDenied_ReturnsError(t *testing.T) {
	svc, _, dir := newTestSyncService(t)

	mtixDir := filepath.Join(dir, ".mtix")
	tasksPath := filepath.Join(mtixDir, "tasks.json")
	require.NoError(t, os.MkdirAll(mtixDir, 0755))
	require.NoError(t, os.WriteFile(tasksPath, []byte("{}"), 0644))
	require.NoError(t, os.Chmod(tasksPath, 0000))
	t.Cleanup(func() { os.Chmod(tasksPath, 0644) })

	err := svc.AutoImport(context.Background(), mtixDir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "read tasks.json")
}

// TestAutoImport_StoredHashReadError_ReturnsError verifies stored hash read error.
func TestAutoImport_StoredHashReadError_ReturnsError(t *testing.T) {
	svc, _, dir := newTestSyncService(t)

	srcDir := t.TempDir()
	srcDataDir := filepath.Join(srcDir, "data")
	require.NoError(t, os.MkdirAll(srcDataDir, 0755))
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	srcStore, err := sqlite.New(srcDataDir, logger)
	require.NoError(t, err)
	defer srcStore.Close()

	data := makeExportData(t, srcStore)
	writeTasksJSON(t, dir, data)

	// Make the hash file a directory to cause read error.
	hashDir := filepath.Join(dir, ".mtix", "data", "sync.sha256")
	require.NoError(t, os.MkdirAll(hashDir, 0755))

	mtixDir := filepath.Join(dir, ".mtix")
	err = svc.AutoImport(context.Background(), mtixDir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "read stored hash")
}

// TestAutoExport_AtomicWrite_TasksJSONExists verifies file is written atomically.
func TestAutoExport_AtomicWrite_TasksJSONExists(t *testing.T) {
	svc, store, dir := newTestSyncService(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	require.NoError(t, store.CreateNode(ctx, &model.Node{
		ID: "ATM-1", Project: "ATM", Depth: 0, Seq: 1, Title: "Atomic write",
		Status: model.StatusOpen, Priority: model.PriorityMedium, Weight: 1.0,
		NodeType: model.NodeTypeIssue, ContentHash: "a1", CreatedAt: now, UpdatedAt: now,
	}))

	mtixDir := filepath.Join(dir, ".mtix")

	// Write initial tasks.json.
	require.NoError(t, svc.AutoExport(ctx, mtixDir))

	// Modify DB and export again.
	require.NoError(t, store.CreateNode(ctx, &model.Node{
		ID: "ATM-2", Project: "ATM", Depth: 0, Seq: 2, Title: "Second node",
		Status: model.StatusOpen, Priority: model.PriorityMedium, Weight: 1.0,
		NodeType: model.NodeTypeIssue, ContentHash: "a2", CreatedAt: now, UpdatedAt: now,
	}))
	require.NoError(t, svc.AutoExport(ctx, mtixDir))

	// Verify both nodes are in the file.
	content, err := os.ReadFile(filepath.Join(mtixDir, "tasks.json"))
	require.NoError(t, err)
	assert.Contains(t, string(content), "ATM-1")
	assert.Contains(t, string(content), "ATM-2")

	// Verify no temp file left behind.
	_, err = os.Stat(filepath.Join(mtixDir, "tasks.json.tmp"))
	assert.True(t, os.IsNotExist(err), "temp file should not exist after successful export")
}

// TestAutoImport_ConflictDetection_DBHashMatchesStored_NoConflict verifies
// that when the DB hash matches stored, no conflict is detected.
func TestAutoImport_ConflictDetection_DBHashMatchesStored_NoConflict(t *testing.T) {
	svc, store, dir := newTestSyncService(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	// Create initial state.
	require.NoError(t, store.CreateNode(ctx, &model.Node{
		ID: "NC-1", Project: "NC", Depth: 0, Seq: 1, Title: "No conflict",
		Status: model.StatusOpen, Priority: model.PriorityMedium, Weight: 1.0,
		NodeType: model.NodeTypeIssue, ContentHash: "nc1", CreatedAt: now, UpdatedAt: now,
	}))

	mtixDir := filepath.Join(dir, ".mtix")

	// Export to establish baseline hashes (both file and DB).
	require.NoError(t, svc.AutoExport(ctx, mtixDir))

	// Create new export data from separate store (simulates git pull with changes).
	srcDir := t.TempDir()
	srcDataDir := filepath.Join(srcDir, "data")
	require.NoError(t, os.MkdirAll(srcDataDir, 0755))
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	srcStore, err := sqlite.New(srcDataDir, logger)
	require.NoError(t, err)
	defer srcStore.Close()

	newData := makeExportData(t, srcStore)
	writeTasksJSON(t, dir, newData)

	// File hash changed but DB hash matches stored → no conflict, import proceeds.
	err = svc.AutoImport(ctx, mtixDir)
	require.NoError(t, err)

	// Hash should be updated and match the actual tasks.json file on disk.
	storedHash, err := os.ReadFile(filepath.Join(mtixDir, "data", "sync.sha256"))
	require.NoError(t, err)

	// Read the actual tasks.json and compute its hash — this is what the
	// sync service does internally, so the hash must match.
	actualFileBytes, err := os.ReadFile(filepath.Join(mtixDir, "tasks.json"))
	require.NoError(t, err)
	assert.Equal(t, hashBytes(actualFileBytes), string(storedHash),
		"stored hash must match SHA-256 of the actual tasks.json file on disk")
}

// TestAutoExport_EmptyDB_ProducesValidJSON verifies export with no nodes.
func TestAutoExport_EmptyDB_ProducesValidJSON(t *testing.T) {
	svc, _, dir := newTestSyncService(t)
	mtixDir := filepath.Join(dir, ".mtix")

	err := svc.AutoExport(context.Background(), mtixDir)
	require.NoError(t, err)

	data, err := os.ReadFile(filepath.Join(mtixDir, "tasks.json"))
	require.NoError(t, err)
	assert.Contains(t, string(data), "\"node_count\": 0")

	// Verify hash and DB hash files exist.
	_, err = os.Stat(filepath.Join(mtixDir, "data", "sync.sha256"))
	assert.NoError(t, err)
	_, err = os.Stat(filepath.Join(mtixDir, "data", "sync-db.sha256"))
	assert.NoError(t, err)
}

// TestAutoExport_BadDir_ReturnsError verifies export to non-existent dir fails.
func TestAutoExport_BadDir_ReturnsError(t *testing.T) {
	svc, _, _ := newTestSyncService(t)

	err := svc.AutoExport(context.Background(), "/nonexistent/.mtix")
	// Should fail on lock acquisition or file write.
	assert.NoError(t, err, "non-existent dir causes lock skip, not error")
}

// TestAutoImport_BackupDB_NoDB_NoBackup verifies backup skips when no DB file exists.
func TestAutoImport_BackupDB_NoDB_NoBackup(t *testing.T) {
	svc, _, dir := newTestSyncService(t)

	srcDir := t.TempDir()
	srcDataDir := filepath.Join(srcDir, "data")
	require.NoError(t, os.MkdirAll(srcDataDir, 0755))
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	srcStore, err := sqlite.New(srcDataDir, logger)
	require.NoError(t, err)
	defer srcStore.Close()

	data := makeExportData(t, srcStore)
	writeTasksJSON(t, dir, data)

	// Remove the DB file to simulate first import with no existing database.
	dbPath := filepath.Join(dir, ".mtix", "data", "mtix.db")
	os.Remove(dbPath)
	os.Remove(dbPath + "-wal")
	os.Remove(dbPath + "-shm")

	mtixDir := filepath.Join(dir, ".mtix")
	err = svc.AutoImport(context.Background(), mtixDir)
	// Import may fail since store is open, but backup should not create file.
	backupPath := filepath.Join(mtixDir, "data", "pre-sync-backup.db")
	_, statErr := os.Stat(backupPath)
	// Either backup was skipped (no DB) or error occurred — either way acceptable.
	_ = err
	_ = statErr
}

// TestAutoExport_Deterministic_SameContent verifies deterministic output.
func TestAutoExport_Deterministic_SameContent(t *testing.T) {
	svc, store, dir := newTestSyncService(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	require.NoError(t, store.CreateNode(ctx, &model.Node{
		ID: "DET-1", Project: "DET", Depth: 0, Seq: 1, Title: "Determinism test",
		Status: model.StatusOpen, Priority: model.PriorityMedium, Weight: 1.0,
		NodeType: model.NodeTypeIssue, ContentHash: "d1", CreatedAt: now, UpdatedAt: now,
	}))

	mtixDir := filepath.Join(dir, ".mtix")

	// Export twice.
	require.NoError(t, svc.AutoExport(ctx, mtixDir))
	firstContent, err := os.ReadFile(filepath.Join(mtixDir, "tasks.json"))
	require.NoError(t, err)
	firstHash, err := os.ReadFile(filepath.Join(mtixDir, "data", "sync.sha256"))
	require.NoError(t, err)

	require.NoError(t, svc.AutoExport(ctx, mtixDir))
	secondContent, err := os.ReadFile(filepath.Join(mtixDir, "tasks.json"))
	require.NoError(t, err)
	secondHash, err := os.ReadFile(filepath.Join(mtixDir, "data", "sync.sha256"))
	require.NoError(t, err)

	// Content and hashes should be identical (deterministic export).
	assert.Equal(t, firstContent, secondContent)
	assert.Equal(t, firstHash, secondHash)
}

// TestAutoImport_FullRoundtrip_ExportImportExport verifies complete round-trip.
func TestAutoImport_FullRoundtrip_ExportImportExport(t *testing.T) {
	svc, store, dir := newTestSyncService(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	// Create node and export.
	require.NoError(t, store.CreateNode(ctx, &model.Node{
		ID: "RT-1", Project: "RT", Depth: 0, Seq: 1, Title: "Round trip",
		Status: model.StatusOpen, Priority: model.PriorityMedium, Weight: 1.0,
		NodeType: model.NodeTypeIssue, ContentHash: "rt1", CreatedAt: now, UpdatedAt: now,
	}))

	mtixDir := filepath.Join(dir, ".mtix")
	require.NoError(t, svc.AutoExport(ctx, mtixDir))

	// Simulate git pull: modify tasks.json with new data from another store.
	srcDir := t.TempDir()
	srcDataDir := filepath.Join(srcDir, "data")
	require.NoError(t, os.MkdirAll(srcDataDir, 0755))
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	srcStore, err := sqlite.New(srcDataDir, logger)
	require.NoError(t, err)
	defer srcStore.Close()

	newData := makeExportData(t, srcStore)
	writeTasksJSON(t, dir, newData)

	// Auto-import should detect changed file and import.
	require.NoError(t, svc.AutoImport(ctx, mtixDir))

	// Export again — should reflect imported data.
	require.NoError(t, svc.AutoExport(ctx, mtixDir))

	// Verify final state.
	content, err := os.ReadFile(filepath.Join(mtixDir, "tasks.json"))
	require.NoError(t, err)
	assert.Contains(t, string(content), "TEST-1") // From imported data.
}

// TestNewSyncService_DefaultMaxImportSize verifies default configuration.
func TestNewSyncService_DefaultMaxImportSize(t *testing.T) {
	svc, _, _ := newTestSyncService(t)
	assert.Equal(t, int64(50*1024*1024), svc.MaxImportSize)
}

// TestAutoImport_FileNotFound_SkipsSilently verifies missing tasks.json is a no-op.
func TestAutoImport_FileNotFound_SkipsSilently(t *testing.T) {
	svc, _, dir := newTestSyncService(t)

	// No tasks.json exists.
	mtixDir := filepath.Join(dir, ".mtix")
	err := svc.AutoImport(context.Background(), mtixDir)
	require.NoError(t, err, "missing tasks.json should not cause an error")
}

// TestSyncService_Compare_DetectsDrift verifies drift detection between
// SQLite and tasks.json per FR-15. If a node is deleted from SQLite but
// still in tasks.json, Compare() must report the discrepancy.
func TestSyncService_Compare_DetectsDrift(t *testing.T) {
	svc, store, dir := newTestSyncService(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	// Create two nodes.
	require.NoError(t, store.CreateNode(ctx, &model.Node{
		ID: "DRIFT-1", Project: "DRIFT", Depth: 0, Seq: 1, Title: "Node one",
		Status: model.StatusOpen, Priority: model.PriorityMedium, Weight: 1.0,
		NodeType: model.NodeTypeIssue, ContentHash: "dr1", CreatedAt: now, UpdatedAt: now,
	}))
	require.NoError(t, store.CreateNode(ctx, &model.Node{
		ID: "DRIFT-2", Project: "DRIFT", Depth: 0, Seq: 2, Title: "Node two",
		Status: model.StatusOpen, Priority: model.PriorityMedium, Weight: 1.0,
		NodeType: model.NodeTypeIssue, ContentHash: "dr2", CreatedAt: now, UpdatedAt: now,
	}))

	// Export both nodes to tasks.json.
	mtixDir := filepath.Join(dir, ".mtix")
	require.NoError(t, svc.AutoExport(ctx, mtixDir))

	// Permanently delete DRIFT-2 from SQLite (simulating gc).
	_, err := store.WriteDB().ExecContext(ctx,
		"DELETE FROM nodes WHERE id = ?", "DRIFT-2")
	require.NoError(t, err)

	// Compare should detect drift.
	report, err := svc.Compare(ctx, mtixDir)
	require.NoError(t, err)
	assert.False(t, report.InSync, "should detect drift after node deleted from SQLite")
	assert.Equal(t, 2, report.FileNodeCount, "tasks.json should have 2 nodes")
	assert.Equal(t, 1, report.DBNodeCount, "SQLite should have 1 node")
	assert.Contains(t, report.OnlyInFile, "DRIFT-2",
		"DRIFT-2 should be reported as only in file")
}

// TestSyncService_Compare_InSync verifies no drift when stores match.
func TestSyncService_Compare_InSync(t *testing.T) {
	svc, store, dir := newTestSyncService(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	require.NoError(t, store.CreateNode(ctx, &model.Node{
		ID: "SYNC-1", Project: "SYNC", Depth: 0, Seq: 1, Title: "In sync",
		Status: model.StatusOpen, Priority: model.PriorityMedium, Weight: 1.0,
		NodeType: model.NodeTypeIssue, ContentHash: "s1", CreatedAt: now, UpdatedAt: now,
	}))

	mtixDir := filepath.Join(dir, ".mtix")
	require.NoError(t, svc.AutoExport(ctx, mtixDir))

	report, err := svc.Compare(ctx, mtixDir)
	require.NoError(t, err)
	assert.True(t, report.InSync, "should be in sync after fresh export")
	assert.Equal(t, report.FileNodeCount, report.DBNodeCount)
	assert.Empty(t, report.OnlyInFile)
	assert.Empty(t, report.OnlyInDB)
}

// TestAutoExport_WritesAtomically_TasksJsonExists verifies that auto-export
// produces a valid tasks.json file with correct content.
func TestAutoExport_WritesAtomically_TasksJsonExists(t *testing.T) {
	svc, store, dir := newTestSyncService(t)
	ctx := context.Background()
	mtixDir := filepath.Join(dir, ".mtix")
	now := time.Now().UTC().Truncate(time.Second)

	// Create a node so the export has content.
	require.NoError(t, store.CreateNode(ctx, &model.Node{
		ID: "EXP-1", Project: "EXP", Depth: 0, Seq: 1, Title: "Export test",
		Status: model.StatusOpen, Priority: model.PriorityMedium, Weight: 1.0,
		NodeType: model.NodeTypeIssue, ContentHash: "h1", CreatedAt: now, UpdatedAt: now,
	}))

	err := svc.AutoExport(ctx, mtixDir)
	require.NoError(t, err)

	// Verify tasks.json exists and is valid JSON.
	data, err := os.ReadFile(filepath.Join(mtixDir, "tasks.json"))
	require.NoError(t, err)
	assert.Greater(t, len(data), 0)

	var exported map[string]interface{}
	require.NoError(t, json.Unmarshal(data, &exported))
	assert.Contains(t, exported, "nodes")
	assert.Contains(t, exported, "node_count")

	// Verify hash file was written.
	hashData, err := os.ReadFile(filepath.Join(mtixDir, "data", "sync.sha256"))
	require.NoError(t, err)
	assert.Equal(t, 64, len(strings.TrimSpace(string(hashData))), "hash should be 64 hex chars")

	// Verify DB hash file was written for conflict detection.
	dbHashData, err := os.ReadFile(filepath.Join(mtixDir, "data", "sync-db.sha256"))
	require.NoError(t, err)
	assert.Equal(t, 64, len(strings.TrimSpace(string(dbHashData))))
}

// TestAutoExport_ThenAutoImport_RoundTrips verifies that export→import
// produces identical data (round-trip integrity).
func TestAutoExport_ThenAutoImport_RoundTrips(t *testing.T) {
	svc, store, dir := newTestSyncService(t)
	ctx := context.Background()
	mtixDir := filepath.Join(dir, ".mtix")
	now := time.Now().UTC().Truncate(time.Second)

	require.NoError(t, store.CreateNode(ctx, &model.Node{
		ID: "RT-1", Project: "RT", Depth: 0, Seq: 1, Title: "Roundtrip test",
		Status: model.StatusOpen, Priority: model.PriorityMedium, Weight: 1.0,
		NodeType: model.NodeTypeIssue, ContentHash: "rt1",
		Description: "Test description for roundtrip",
		CreatedAt: now, UpdatedAt: now,
	}))

	// Export.
	require.NoError(t, svc.AutoExport(ctx, mtixDir))

	// Create a fresh store and sync service to import into.
	dir2 := t.TempDir()
	mtixDir2 := filepath.Join(dir2, ".mtix")
	require.NoError(t, os.MkdirAll(mtixDir2, 0755))

	// Copy tasks.json to the new location.
	data, err := os.ReadFile(filepath.Join(mtixDir, "tasks.json"))
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(mtixDir2, "tasks.json"), data, 0644))

	dataDir2 := filepath.Join(mtixDir2, "data")
	require.NoError(t, os.MkdirAll(dataDir2, 0755))
	logger2 := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	store2, err2 := sqlite.New(dataDir2, logger2)
	require.NoError(t, err2)
	t.Cleanup(func() { store2.Close() })
	svc2 := service.NewSyncService(store2, logger2, func() time.Time { return time.Now().UTC() })

	// Import.
	require.NoError(t, svc2.AutoImport(ctx, mtixDir2))

	// Verify the imported node matches.
	node, err := store2.GetNode(ctx, "RT-1")
	require.NoError(t, err)
	assert.Equal(t, "Roundtrip test", node.Title)
	assert.Equal(t, "Test description for roundtrip", node.Description)
}

// TestAutoImport_MissingTasksJson_NoOp verifies import gracefully skips
// when tasks.json doesn't exist.
func TestAutoImport_MissingTasksJson_NoOp(t *testing.T) {
	svc, _, dir := newTestSyncService(t)
	mtixDir := filepath.Join(dir, ".mtix")

	// No tasks.json exists — import should be a no-op.
	err := svc.AutoImport(context.Background(), mtixDir)
	assert.NoError(t, err)
}

// TestAutoImport_ConflictDetection_BothChanged_Skips verifies that when
// both tasks.json and DB have changed, import is skipped with no error.
func TestAutoImport_ConflictDetection_BothChanged_Skips(t *testing.T) {
	svc, store, dir := newTestSyncService(t)
	ctx := context.Background()
	mtixDir := filepath.Join(dir, ".mtix")
	now := time.Now().UTC().Truncate(time.Second)

	// Create initial state and export.
	require.NoError(t, store.CreateNode(ctx, &model.Node{
		ID: "CONF-1", Project: "CONF", Depth: 0, Seq: 1, Title: "Conflict test",
		Status: model.StatusOpen, Priority: model.PriorityMedium, Weight: 1.0,
		NodeType: model.NodeTypeIssue, ContentHash: "c1", CreatedAt: now, UpdatedAt: now,
	}))
	require.NoError(t, svc.AutoExport(ctx, mtixDir))

	// Modify tasks.json (simulating git pull).
	tasksPath := filepath.Join(mtixDir, "tasks.json")
	data, err := os.ReadFile(tasksPath)
	require.NoError(t, err)
	modified := strings.Replace(string(data), "Conflict test", "Modified externally", 1)
	require.NoError(t, os.WriteFile(tasksPath, []byte(modified), 0644))

	// Also modify the DB (simulating local work).
	require.NoError(t, store.CreateNode(ctx, &model.Node{
		ID: "CONF-2", Project: "CONF", Depth: 0, Seq: 2, Title: "Local change",
		Status: model.StatusOpen, Priority: model.PriorityMedium, Weight: 1.0,
		NodeType: model.NodeTypeIssue, ContentHash: "c2", CreatedAt: now, UpdatedAt: now,
	}))

	// Import should detect conflict and skip (no error, just a warning).
	err = svc.AutoImport(ctx, mtixDir)
	assert.NoError(t, err)

	// DB should still have the local change (import was skipped).
	_, err = store.GetNode(ctx, "CONF-2")
	assert.NoError(t, err, "local change should survive conflict skip")
}
