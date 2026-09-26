// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Tests for MTIX-95.31.2 (FR-15.2f, amended): before every replace-mode
// auto-import the local database is backed up to a timestamped file in
// .mtix/data/backups, and the newest service.PreSyncBackupsKept of those
// backups are kept. The single, overwritten .mtix/data/pre-sync-backup.db
// kept only the state before the last import. Written red-first.
package service_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// backupNodeIDs returns the node ids a backup database holds.
func backupNodeIDs(t *testing.T, path string) []string {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	require.NoError(t, err)
	defer func() { require.NoError(t, db.Close()) }()
	rows, err := db.QueryContext(context.Background(), `SELECT id FROM nodes ORDER BY id`)
	require.NoError(t, err)
	defer func() { require.NoError(t, rows.Close()) }()
	var ids []string
	for rows.Next() {
		var id string
		require.NoError(t, rows.Scan(&id))
		ids = append(ids, id)
	}
	require.NoError(t, rows.Err())
	return ids
}

// TestAutoImport_BeforeReplace_BacksUpAndKeepsNewest verifies each applied
// auto-import is preceded by a backup named after the clock's UTC time that
// holds the store as it was before that import, that only the newest
// PreSyncBackupsKept pre-import backups are kept, and that other files in
// the directory (rolling backups, the legacy single backup, anything else)
// are never touched.
func TestAutoImport_BeforeReplace_BacksUpAndKeepsNewest(t *testing.T) {
	require.Equal(t, 5, service.PreSyncBackupsKept, "the documented retention is 5")
	ctx := context.Background()
	f := newGuardFixture(t)
	backupsDir := filepath.Join(f.mtixDir, "data", "backups")
	require.NoError(t, os.MkdirAll(backupsDir, 0o755))
	foreign := []string{
		filepath.Join(backupsDir, "mtix-20260101-000000.db"),
		filepath.Join(backupsDir, "notes.txt"),
		filepath.Join(f.mtixDir, "data", "pre-sync-backup.db"),
	}
	for _, p := range foreign {
		require.NoError(t, os.WriteFile(p, []byte("not ours"), 0o644))
	}

	base := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	var names []string
	for i := 1; i <= 7; i++ {
		f.now = base.Add(time.Duration(i) * time.Minute)
		id := fmt.Sprintf("PROJ-%d", 2+i)
		f.pull(t, f.teammateBoard(t, func(d *sqlite.ExportData) { addTeammateNode(t, d, "PROJ-2", id, 2+i) }))
		require.NoError(t, f.svc.AutoImport(ctx, f.mtixDir))
		// A writing command re-exports the board, which refreshes the
		// conflict baseline before the next pull.
		require.NoError(t, f.svc.AutoExport(ctx, f.mtixDir))

		name := "pre-sync-" + f.now.Format("20060102-150405") + ".db"
		names = append(names, name)
		held := backupNodeIDs(t, filepath.Join(backupsDir, name))
		assert.Len(t, held, 1+i, "the backup holds the store before import %d", i)
		assert.NotContains(t, held, id, "the backup is taken before the import")
	}

	got := f.preSyncBackups(t)
	sort.Strings(got)
	assert.Equal(t, names[2:], got, "the newest five pre-import backups are kept")
	for _, p := range foreign {
		content, err := os.ReadFile(p)
		require.NoError(t, err, "%s must not be removed", p)
		assert.Equal(t, "not ours", string(content))
	}
}

// TestAutoImport_BackupsWithinOneSecond_KeepsNewest verifies imports within
// the same second get distinct backup names and that retention orders them
// by time and then by their sequence, not by name.
func TestAutoImport_BackupsWithinOneSecond_KeepsNewest(t *testing.T) {
	ctx := context.Background()
	f := newGuardFixture(t)
	f.now = time.Date(2026, 9, 24, 13, 0, 0, 0, time.UTC)
	for i := 1; i <= 6; i++ {
		id := fmt.Sprintf("PROJ-%d", 2+i)
		f.pull(t, f.teammateBoard(t, func(d *sqlite.ExportData) { addTeammateNode(t, d, "PROJ-2", id, 2+i) }))
		require.NoError(t, f.svc.AutoImport(ctx, f.mtixDir))
		require.NoError(t, f.svc.AutoExport(ctx, f.mtixDir)) // a writing command
	}
	stamp := "pre-sync-20260924-130000"
	want := []string{stamp + "-2.db", stamp + "-3.db", stamp + "-4.db", stamp + "-5.db", stamp + "-6.db"}
	got := f.preSyncBackups(t)
	sort.Strings(got)
	assert.Equal(t, want, got, "the first backup of the second is the oldest and is removed")
	held := backupNodeIDs(t, filepath.Join(f.mtixDir, "data", "backups", stamp+"-6.db"))
	assert.Len(t, held, 7, "the newest backup holds the store before the sixth import")
}

// TestAutoImport_BackupFails_SkipsImport verifies FR-15.2f: when the backup
// cannot be written the auto-import is skipped and nothing is imported.
func TestAutoImport_BackupFails_SkipsImport(t *testing.T) {
	ctx := context.Background()
	f := newGuardFixture(t)
	// A file where the backups directory belongs makes the backup fail.
	require.NoError(t, os.WriteFile(filepath.Join(f.mtixDir, "data", "backups"), []byte("x"), 0o644))
	hash := string(f.read(t, "data/sync.sha256"))
	f.pull(t, f.teammateBoard(t, func(d *sqlite.ExportData) { addTeammateNode(t, d, "PROJ-2", "PROJ-3", 3) }))

	require.NoError(t, f.svc.AutoImport(ctx, f.mtixDir))
	_, err := f.store.GetNode(ctx, "PROJ-3")
	assert.Error(t, err, "no import without a backup")
	assert.Equal(t, hash, string(f.read(t, "data/sync.sha256")))
	assert.NotContains(t, f.notices.String(), "mtix: imported")
	report, err := f.svc.Compare(ctx, f.mtixDir)
	require.NoError(t, err)
	require.NotNil(t, report.AutoImport.LastRefusal, "mtix sync reports the skipped import")
	assert.Contains(t, report.AutoImport.LastRefusal.Reason, "could not be backed up")
	assert.True(t, report.AutoImport.LastRefusal.Pending)
}

// TestAutoImport_FailingImports_NeverRotateBackups verifies a file that is
// refused or fails a check (checksum, node count, newer schema, lossy)
// takes no backup, so it can never rotate good backups away (MTIX-95.31.2).
func TestAutoImport_FailingImports_NeverRotateBackups(t *testing.T) {
	ctx := context.Background()
	f := newGuardFixture(t)
	base := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	for i := 1; i <= 5; i++ {
		f.now = base.Add(time.Duration(i) * time.Minute)
		id := fmt.Sprintf("PROJ-%d", 2+i)
		f.pull(t, f.teammateBoard(t, func(d *sqlite.ExportData) { addTeammateNode(t, d, "PROJ-2", id, 2+i) }))
		require.NoError(t, f.svc.AutoImport(ctx, f.mtixDir))
	}
	before := f.preSyncBackups(t)
	require.Len(t, before, 5)

	failing := []struct {
		edit       func(d *sqlite.ExportData)
		resealLast bool // false: the edit breaks the seal on purpose
	}{
		{func(d *sqlite.ExportData) { d.Checksum = "0000" }, false},
		{func(d *sqlite.ExportData) { d.NodeCount++ }, false},
		{func(d *sqlite.ExportData) { d.SchemaVersion = "3.0.0" }, false},
		{func(d *sqlite.ExportData) { d.Nodes[nodeIndex(t, d, "PROJ-1")].Annotations = nil }, true},
	}
	for i, tc := range failing {
		f.now = base.Add(time.Hour + time.Duration(i)*time.Minute)
		data, err := f.store.Export(ctx, "", "")
		require.NoError(t, err)
		addTeammateNode(t, data, "PROJ-2", "PROJ-99", 99)
		require.NoError(t, sqlite.RecomputeExportChecksum(data))
		tc.edit(data)
		if tc.resealLast {
			require.NoError(t, sqlite.RecomputeExportChecksum(data))
		}
		raw, err := json.MarshalIndent(data, "", "  ")
		require.NoError(t, err)
		f.pull(t, raw)
		_ = f.svc.AutoImport(ctx, f.mtixDir)
		_, getErr := f.store.GetNode(ctx, "PROJ-99")
		require.Error(t, getErr, "failing board %d is not imported", i)
		assert.Equal(t, before, f.preSyncBackups(t), "failing board %d takes no backup", i)
	}
}

// TestAutoImport_RetriedFailingImport_TakesOneBackup verifies an import that
// passes every check but fails while writing (a parent that does not
// exist) takes one backup, and its retries on later commands reuse it
// (MTIX-95.31.2).
func TestAutoImport_RetriedFailingImport_TakesOneBackup(t *testing.T) {
	ctx := context.Background()
	f := newGuardFixture(t)
	f.pull(t, f.teammateBoard(t, func(d *sqlite.ExportData) {
		addTeammateNode(t, d, "PROJ-2", "PROJ-3", 3)
		d.Nodes[nodeIndex(t, d, "PROJ-3")].ParentID = "PROJ-404"
	}))
	for attempt := 0; attempt < 3; attempt++ {
		f.now = time.Date(2026, 9, 24, 15, attempt, 0, 0, time.UTC)
		require.Error(t, f.svc.AutoImport(ctx, f.mtixDir), "the import fails while writing")
	}
	assert.Equal(t, []string{"pre-sync-20260924-150000.db"}, f.preSyncBackups(t), "one backup per file")
}

// TestAutoImport_Retention_IgnoresDirectoriesNamedLikeBackups verifies the
// rotation deletes only regular pre-sync files: a directory with a backup's
// name is neither counted nor removed.
func TestAutoImport_Retention_IgnoresDirectoriesNamedLikeBackups(t *testing.T) {
	ctx := context.Background()
	f := newGuardFixture(t)
	backupsDir := filepath.Join(f.mtixDir, "data", "backups")
	require.NoError(t, os.MkdirAll(filepath.Join(backupsDir, "pre-sync-20200101-000000.db"), 0o755))
	for day := 1; day <= 5; day++ {
		name := fmt.Sprintf("pre-sync-202601%02d-000000.db", day)
		require.NoError(t, os.WriteFile(filepath.Join(backupsDir, name), []byte("old"), 0o644))
	}
	f.now = time.Date(2026, 9, 24, 16, 0, 0, 0, time.UTC)
	f.pull(t, f.teammateBoard(t, func(d *sqlite.ExportData) { addTeammateNode(t, d, "PROJ-2", "PROJ-3", 3) }))
	require.NoError(t, f.svc.AutoImport(ctx, f.mtixDir))

	info, err := os.Stat(filepath.Join(backupsDir, "pre-sync-20200101-000000.db"))
	require.NoError(t, err, "the directory is not removed")
	assert.True(t, info.IsDir())
	_, err = os.Stat(filepath.Join(backupsDir, "pre-sync-20260101-000000.db"))
	assert.ErrorIs(t, err, os.ErrNotExist, "the oldest regular backup is rotated out")
	assert.Len(t, f.preSyncBackups(t), 6, "five regular backups and the directory")
}

// TestAutoImport_SameFileAgainAfterLocalWrite_TakesNewBackup is the round-3
// regression: after file X is imported (backup b1) and a local write
// follows, X appears again (a checkout). Its import must take a new backup
// holding the local write, and the notice must name it, not b1.
func TestAutoImport_SameFileAgainAfterLocalWrite_TakesNewBackup(t *testing.T) {
	ctx := context.Background()
	f := newGuardFixture(t)
	x := f.teammateBoard(t, func(d *sqlite.ExportData) { addTeammateNode(t, d, "PROJ-2", "PROJ-3", 3) })
	f.now = time.Date(2026, 9, 24, 17, 0, 0, 0, time.UTC)
	f.pull(t, x)
	require.NoError(t, f.svc.AutoImport(ctx, f.mtixDir))

	title := "Local title written after the import"
	require.NoError(t, f.store.UpdateNode(ctx, "PROJ-2", &store.NodeUpdate{Title: &title}))
	require.NoError(t, f.svc.AutoExport(ctx, f.mtixDir)) // the writing command

	f.now = time.Date(2026, 9, 24, 17, 5, 0, 0, time.UTC)
	f.notices.Reset()
	f.pull(t, x) // X checked out again
	require.NoError(t, f.svc.AutoImport(ctx, f.mtixDir))

	second := "pre-sync-20260924-170500.db"
	assert.Equal(t, []string{"pre-sync-20260924-170000.db", second}, f.preSyncBackups(t))
	assert.Contains(t, f.notices.String(), second, "the notice names the new backup")
	db, err := sql.Open("sqlite", "file:"+filepath.Join(f.mtixDir, "data", "backups", second)+"?mode=ro")
	require.NoError(t, err)
	defer func() { require.NoError(t, db.Close()) }()
	var held string
	require.NoError(t, db.QueryRowContext(ctx, `SELECT title FROM nodes WHERE id = ?`, "PROJ-2").Scan(&held))
	assert.Equal(t, title, held, "the new backup holds the local write")
}

// TestAutoImport_RetryAfterLocalWrite_TakesNewBackup verifies a failed
// import's backup is reused only while the store is unchanged: after a
// local write, the retry takes a new backup.
func TestAutoImport_RetryAfterLocalWrite_TakesNewBackup(t *testing.T) {
	ctx := context.Background()
	f := newGuardFixture(t)
	f.pull(t, f.teammateBoard(t, func(d *sqlite.ExportData) {
		addTeammateNode(t, d, "PROJ-2", "PROJ-3", 3)
		d.Nodes[nodeIndex(t, d, "PROJ-3")].ParentID = "PROJ-404"
	}))
	require.NoError(t, os.Remove(filepath.Join(f.mtixDir, "data", "sync-db.sha256"))) // no conflict baseline
	f.now = time.Date(2026, 9, 24, 18, 0, 0, 0, time.UTC)
	require.Error(t, f.svc.AutoImport(ctx, f.mtixDir))

	title := "Local title between the attempts"
	require.NoError(t, f.store.UpdateNode(ctx, "PROJ-1", &store.NodeUpdate{Title: &title}))
	f.now = time.Date(2026, 9, 24, 18, 1, 0, 0, time.UTC)
	require.Error(t, f.svc.AutoImport(ctx, f.mtixDir))
	f.now = time.Date(2026, 9, 24, 18, 2, 0, 0, time.UTC)
	require.Error(t, f.svc.AutoImport(ctx, f.mtixDir)) // store unchanged: reuses the second

	assert.Equal(t, []string{"pre-sync-20260924-180000.db", "pre-sync-20260924-180100.db"}, f.preSyncBackups(t))
}

// TestAutoImport_SameFileAfterSuccessfulImport_TakesNewBackup verifies a
// backup is reused only by the retry of a failed import: after file X was
// imported, X checked out again takes a new backup, even when the store
// holds exactly what it held before (MTIX-95.31.2).
func TestAutoImport_SameFileAfterSuccessfulImport_TakesNewBackup(t *testing.T) {
	ctx := context.Background()
	f := newGuardFixture(t)
	x := f.teammateBoard(t, func(d *sqlite.ExportData) { d.ExportedAt = "2020-01-01T00:00:00Z" })
	f.now = time.Date(2026, 9, 24, 19, 0, 0, 0, time.UTC)
	f.pull(t, x)
	require.NoError(t, f.svc.AutoImport(ctx, f.mtixDir))
	require.NoError(t, f.svc.AutoExport(ctx, f.mtixDir)) // a writing command rewrites the board

	f.now = time.Date(2026, 9, 24, 19, 5, 0, 0, time.UTC)
	f.pull(t, x) // X checked out again
	require.NoError(t, f.svc.AutoImport(ctx, f.mtixDir))
	assert.Equal(t, []string{"pre-sync-20260924-190000.db", "pre-sync-20260924-190500.db"}, f.preSyncBackups(t))
}

// TestAutoImport_RetryAfterAnnotationOnlyChange_TakesNewBackup verifies a
// failed import's backup is reused only while the whole store is unchanged,
// its annotations included, not only the fields an older export form
// carries (MTIX-95.31.11): after a change to nothing but an annotation, the
// retry takes a new backup, which holds that change.
func TestAutoImport_RetryAfterAnnotationOnlyChange_TakesNewBackup(t *testing.T) {
	ctx := context.Background()
	f := newGuardFixture(t)
	f.pull(t, f.teammateBoard(t, func(d *sqlite.ExportData) {
		addTeammateNode(t, d, "PROJ-2", "PROJ-3", 3)
		d.Nodes[nodeIndex(t, d, "PROJ-3")].ParentID = "PROJ-404"
	}))
	require.NoError(t, os.Remove(filepath.Join(f.mtixDir, "data", "sync-db.sha256"))) // no conflict baseline
	f.now = time.Date(2026, 9, 24, 19, 0, 0, 0, time.UTC)
	require.Error(t, f.svc.AutoImport(ctx, f.mtixDir), "the import fails while writing")

	// Only the annotations column changes: PROJ-1 keeps one of its two
	// annotations, which the board still carries, so nothing is lost.
	kept, err := json.Marshal(guardAnnotations()[:1])
	require.NoError(t, err)
	f.exec(t, `UPDATE nodes SET annotations = ? WHERE id = 'PROJ-1'`, string(kept))
	f.now = time.Date(2026, 9, 24, 19, 1, 0, 0, time.UTC)
	require.Error(t, f.svc.AutoImport(ctx, f.mtixDir), "the retry fails the same way")

	second := "pre-sync-20260924-190100.db"
	assert.Equal(t, []string{"pre-sync-20260924-190000.db", second}, f.preSyncBackups(t))
	db, err := sql.Open("sqlite", "file:"+filepath.Join(f.mtixDir, "data", "backups", second)+"?mode=ro")
	require.NoError(t, err)
	defer func() { require.NoError(t, db.Close()) }()
	var held int
	// How many annotations PROJ-1 holds in the new backup.
	require.NoError(t, db.QueryRowContext(ctx,
		`SELECT json_array_length(annotations) FROM nodes WHERE id = ?`, "PROJ-1").Scan(&held))
	assert.Equal(t, 1, held, "the new backup holds the annotation change")
}
