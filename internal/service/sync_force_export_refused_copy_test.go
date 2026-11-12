// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// mtix sync --fix ends a merge_synced refusal by rewriting
// .mtix/tasks.json from the store. The refused file is kept first, in a
// timestamped copy under data/refused-boards, so its edits can be re-made.

// refusedSyncedFixture leaves a merge_synced refusal pending and returns the
// refused file's bytes.
func refusedSyncedFixture(t *testing.T) (*guardFixture, []byte) {
	t.Helper()
	f := newGuardFixture(t)
	createTask(t, f, "PROJ-3", 3)
	require.NoError(t, f.svc.AutoExport(context.Background(), f.mtixDir))
	pushAll(t, f)
	board := f.teammateBoard(t, func(d *sqlite.ExportData) { retitle(d, "PROJ-3", "Teammate retitle") })
	f.pull(t, board)
	require.ErrorIs(t, f.svc.AutoImport(context.Background(), f.mtixDir), service.ErrAutoImportRefused)
	return f, board
}

func refusedCopies(t *testing.T, f *guardFixture) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(f.mtixDir, "data", "refused-boards"))
	if os.IsNotExist(err) {
		return nil
	}
	require.NoError(t, err)
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

func TestForceExport_MergeSyncedRefusal_KeepsTimestampedCopyOfRefusedFile(t *testing.T) {
	f, board := refusedSyncedFixture(t)
	f.notices.Reset()

	require.NoError(t, f.svc.ForceExport(context.Background(), f.mtixDir))

	names := refusedCopies(t, f)
	require.Len(t, names, 1)
	want := "tasks-" + f.now.UTC().Format("20060102T150405Z") + ".json"
	assert.Equal(t, want, names[0])
	kept, err := os.ReadFile(filepath.Join(f.mtixDir, "data", "refused-boards", names[0]))
	require.NoError(t, err)
	assert.Equal(t, string(board), string(kept), "the copy holds the refused bytes")
	assert.NotEqual(t, string(board), string(f.read(t, "tasks.json")), "tasks.json was rewritten from the store")
	assert.Contains(t, f.notices.String(), filepath.Join("data", "refused-boards", want), "the notice names the actual copy")
}

func TestForceExport_MergeSyncedRefusal_NeverOverwritesAnExistingCopy(t *testing.T) {
	f, board := refusedSyncedFixture(t)
	dir := filepath.Join(f.mtixDir, "data", "refused-boards")
	require.NoError(t, os.MkdirAll(dir, 0o750))
	taken := filepath.Join(dir, "tasks-"+f.now.UTC().Format("20060102T150405Z")+".json")
	require.NoError(t, os.WriteFile(taken, []byte("earlier copy"), 0o600))

	require.NoError(t, f.svc.ForceExport(context.Background(), f.mtixDir))

	earlier, err := os.ReadFile(taken)
	require.NoError(t, err)
	assert.Equal(t, "earlier copy", string(earlier), "an existing copy is never replaced")
	names := refusedCopies(t, f)
	require.Len(t, names, 2)
	var other string
	for _, n := range names {
		if filepath.Join(dir, n) != taken {
			other = n
		}
	}
	kept, err := os.ReadFile(filepath.Join(dir, other))
	require.NoError(t, err)
	assert.Equal(t, string(board), string(kept))
}

func TestForceExport_WithoutMergeSyncedRefusal_KeepsNoCopy(t *testing.T) {
	f := newGuardFixture(t)
	createTask(t, f, "PROJ-3", 3)
	require.NoError(t, f.svc.AutoExport(context.Background(), f.mtixDir))
	require.NoError(t, f.svc.ForceExport(context.Background(), f.mtixDir))
	assert.Empty(t, refusedCopies(t, f))
}

func TestMergeSyncedRefusal_NamesTheCopyInMessageAndPendingLine(t *testing.T) {
	f, _ := refusedSyncedFixture(t)
	const where = ".mtix/data/refused-boards/tasks-<UTC time>.json"
	assert.Contains(t, f.notices.String(), where, "the refusal message")
	f.notices.Reset()
	writeLocalTask(t, f, "PROJ-4", 4)
	require.NoError(t, f.svc.AutoExport(context.Background(), f.mtixDir))
	assert.Contains(t, f.notices.String(), where, "the pending line")
	assert.True(t, strings.Contains(f.notices.String(), "mtix sync --fix"))
}

// Any unresolved import refusal keeps the refused file before --fix replaces
// it (MTIX-112.17.16.42): a pull or a local write turns merge_synced into a
// conflict, and a lossy file is refused the same way.
func TestForceExport_OtherRefusalKind_KeepsCopy(t *testing.T) {
	f := newGuardFixture(t)
	markFixtureSynced(t, f)
	board := f.teammateBoard(t, func(d *sqlite.ExportData) { d.Nodes[nodeIndex(t, d, "PROJ-1")].Annotations = nil })
	f.pull(t, board)
	require.ErrorIs(t, f.svc.AutoImport(context.Background(), f.mtixDir), service.ErrAutoImportRefused)
	var rec service.AutoImportRefusal
	require.NoError(t, json.Unmarshal(f.read(t, "data/auto-import-refusal.json"), &rec))
	require.NotEqual(t, "merge_synced", rec.Kind)
	require.NoError(t, f.svc.ForceExport(context.Background(), f.mtixDir))
	requireOneCopyOf(t, f, board)
}

func requireOneCopyOf(t *testing.T, f *guardFixture, want []byte) {
	t.Helper()
	names := refusedCopies(t, f)
	require.Len(t, names, 1)
	path := filepath.Join(f.mtixDir, "data", "refused-boards", names[0])
	kept, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, string(want), string(kept))
	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm(), "the copy is private")
}

func TestForceExport_MergeSyncedThenPull_KeepsCopy(t *testing.T) {
	f, board := refusedSyncedFixture(t)
	// A pull changes the store while the refused file stays on disk.
	writeLocalTask(t, f, "PROJ-5", 5)
	err := f.svc.AutoImport(context.Background(), f.mtixDir)
	require.ErrorIs(t, err, service.ErrAutoImportRefused)
	var rec service.AutoImportRefusal
	require.NoError(t, json.Unmarshal(f.read(t, "data/auto-import-refusal.json"), &rec))
	require.Equal(t, "conflict", rec.Kind, "the pull turned the refusal into a conflict")
	require.NoError(t, f.svc.ForceExport(context.Background(), f.mtixDir))
	requireOneCopyOf(t, f, board)
}

func TestForceExport_MergeSyncedThenLocalWrite_KeepsCopy(t *testing.T) {
	f, board := refusedSyncedFixture(t)
	writeLocalTask(t, f, "PROJ-4", 4)
	require.NoError(t, f.svc.AutoExport(context.Background(), f.mtixDir))
	_ = f.svc.AutoImport(context.Background(), f.mtixDir)
	require.NoError(t, f.svc.ForceExport(context.Background(), f.mtixDir))
	requireOneCopyOf(t, f, board)
}

func TestForceExport_CopyFails_BlocksOverwriteAndKeepsRefusalPending(t *testing.T) {
	f, board := refusedSyncedFixture(t)
	// A file where the copy directory should be makes the copy fail.
	require.NoError(t, os.WriteFile(filepath.Join(f.mtixDir, "data", "refused-boards"), []byte("x"), 0o600))

	err := f.svc.ForceExport(context.Background(), f.mtixDir)

	require.Error(t, err)
	assert.Equal(t, string(board), string(f.read(t, "tasks.json")), "tasks.json is not overwritten")
	var rec service.AutoImportRefusal
	require.NoError(t, json.Unmarshal(f.read(t, "data/auto-import-refusal.json"), &rec))
	assert.Empty(t, rec.ResolvedAt, "the refusal stays pending")
}

func TestForceExport_RefusedBoardUnreadable_FailsAndKeepsRefusalPending(t *testing.T) {
	f, _ := refusedSyncedFixture(t)
	path := filepath.Join(f.mtixDir, "tasks.json")
	require.NoError(t, os.Chmod(path, 0o000))
	t.Cleanup(func() { _ = os.Chmod(path, 0o600) })
	if _, err := os.ReadFile(path); err == nil {
		t.Skip("file permissions are not enforced for this user")
	}
	require.Error(t, f.svc.ForceExport(context.Background(), f.mtixDir))
	var rec service.AutoImportRefusal
	require.NoError(t, json.Unmarshal(f.read(t, "data/auto-import-refusal.json"), &rec))
	assert.Empty(t, rec.ResolvedAt)
}

func TestForceExport_RefusalButNoBoardOnDisk_ExportsWithoutCopy(t *testing.T) {
	f, _ := refusedSyncedFixture(t)
	require.NoError(t, os.Remove(filepath.Join(f.mtixDir, "tasks.json")))
	require.NoError(t, f.svc.ForceExport(context.Background(), f.mtixDir))
	assert.Empty(t, refusedCopies(t, f))
	assert.NotEmpty(t, f.read(t, "tasks.json"))
}

func TestForceExport_ResolvedMergeSyncedRefusal_KeepsNoSecondCopy(t *testing.T) {
	f, _ := refusedSyncedFixture(t)
	require.NoError(t, f.svc.ForceExport(context.Background(), f.mtixDir))
	f.now = f.now.Add(time.Hour)
	require.NoError(t, f.svc.ForceExport(context.Background(), f.mtixDir))
	assert.Len(t, refusedCopies(t, f), 1, "a resolved refusal keeps nothing more")
}

func TestForceExport_EveryRefusalKind_KeepsCopyOfTheRefusedFile(t *testing.T) {
	for _, kind := range []string{"merge_synced", "conflict", "lossy", "newer_schema", "invalid_file",
		"unreadable_store", "backup_failed", "not_imported"} {
		t.Run(kind, func(t *testing.T) {
			f := newGuardFixture(t)
			createTask(t, f, "PROJ-3", 3)
			require.NoError(t, f.svc.AutoExport(context.Background(), f.mtixDir))
			board := []byte(`{"hand":"edited"}`)
			f.pull(t, board)
			sum := sha256.Sum256(board)
			rec, err := json.Marshal(service.AutoImportRefusal{FileHash: fmt.Sprintf("%x", sum), Kind: kind, Reason: "test"})
			require.NoError(t, err)
			require.NoError(t, os.WriteFile(filepath.Join(f.mtixDir, "data", "auto-import-refusal.json"), rec, 0o600))

			require.NoError(t, f.svc.ForceExport(context.Background(), f.mtixDir))

			requireOneCopyOf(t, f, board)
		})
	}
}

// The rule does not depend on the refusal record: a tasks.json that differs
// from what mtix last wrote is kept whatever the record says.
func TestForceExport_BoardEditedAfterRefusal_KeepsCopyOfTheEditedFile(t *testing.T) {
	f, _ := refusedSyncedFixture(t)
	edited := append(f.read(t, "tasks.json"), []byte("\n")...)
	f.pull(t, edited)
	require.NoError(t, f.svc.ForceExport(context.Background(), f.mtixDir))
	requireOneCopyOf(t, f, edited)
}

func TestForceExport_RefusalRecordUnreadable_KeepsCopyOfTheChangedFile(t *testing.T) {
	f, board := refusedSyncedFixture(t)
	require.NoError(t, os.WriteFile(filepath.Join(f.mtixDir, "data", "auto-import-refusal.json"), []byte("{not json"), 0o600))
	require.NoError(t, f.svc.ForceExport(context.Background(), f.mtixDir))
	requireOneCopyOf(t, f, board)
}

func TestForceExport_RefusalRecordMissing_KeepsCopyOfTheChangedFile(t *testing.T) {
	f, board := refusedSyncedFixture(t)
	require.NoError(t, os.Remove(filepath.Join(f.mtixDir, "data", "auto-import-refusal.json")))
	require.NoError(t, f.svc.ForceExport(context.Background(), f.mtixDir))
	requireOneCopyOf(t, f, board)
}

func TestForceExport_BoardEqualToLastExport_KeepsNoCopy(t *testing.T) {
	f := newGuardFixture(t)
	createTask(t, f, "PROJ-3", 3)
	require.NoError(t, f.svc.AutoExport(context.Background(), f.mtixDir))
	rec, err := json.Marshal(service.AutoImportRefusal{FileHash: "00", Kind: "conflict", Reason: "stale record"})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(f.mtixDir, "data", "auto-import-refusal.json"), rec, 0o600))
	require.NoError(t, f.svc.ForceExport(context.Background(), f.mtixDir))
	assert.Empty(t, refusedCopies(t, f), "an unchanged file loses nothing")
}

func TestForceExport_NoLastExportRecord_KeepsCopyOfTheBoard(t *testing.T) {
	f, board := refusedSyncedFixture(t)
	require.NoError(t, os.Remove(filepath.Join(f.mtixDir, "data", "sync.sha256")))
	require.NoError(t, f.svc.ForceExport(context.Background(), f.mtixDir))
	requireOneCopyOf(t, f, board)
}
