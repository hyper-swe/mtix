// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// MTIX-112.17.16.16: the doctor reads the pending import refusal through the
// service, with the exact fix resolutionFor names.

// TestPendingImportRefusal_NoRefusal_NotPending: a project with no refusal
// record reports nothing pending.
func TestPendingImportRefusal_NoRefusal_NotPending(t *testing.T) {
	f := newGuardFixture(t)
	st, err := f.svc.PendingImportRefusal(f.mtixDir)
	require.NoError(t, err)
	assert.False(t, st.Pending)
	assert.Empty(t, st.Fix)
}

// TestPendingImportRefusal_Refused_ReportsKindFileAndFix: a refused lossy
// import is pending, with its kind, the file and the resolution text.
func TestPendingImportRefusal_Refused_ReportsKindFileAndFix(t *testing.T) {
	f, _ := refusedFixture(t)
	st, err := f.svc.PendingImportRefusal(f.mtixDir)
	require.NoError(t, err)
	require.True(t, st.Pending)
	assert.Equal(t, "lossy", st.Kind)
	assert.Equal(t, ".mtix/tasks.json", st.File)
	assert.NotEmpty(t, st.Reason)
	assert.Contains(t, st.Fix, "mtix sync --fix")
	assert.Contains(t, st.Fix, "--mode replace")
}

// TestPendingImportRefusal_SyncedStore_FixOffersRebuildNotReplace: on a store
// that syncs the fix is the synced one.
func TestPendingImportRefusal_SyncedStore_FixOffersRebuildNotReplace(t *testing.T) {
	f := newGuardFixture(t)
	markFixtureSynced(t, f)
	f.pull(t, f.teammateBoard(t, func(d *sqlite.ExportData) { d.Nodes[nodeIndex(t, d, "PROJ-1")].Annotations = nil }))
	require.ErrorIs(t, f.svc.AutoImport(context.Background(), f.mtixDir), service.ErrAutoImportRefused)

	st, err := f.svc.PendingImportRefusal(f.mtixDir)
	require.NoError(t, err)
	require.True(t, st.Pending)
	assert.Contains(t, st.Fix, sqlite.SyncLogRecovery)
	assert.NotContains(t, st.Fix, "--mode replace")
}

// TestPendingImportRefusal_ResolvedByFix_NotPending: ending the refusal with
// a forced export clears it.
func TestPendingImportRefusal_ResolvedByFix_NotPending(t *testing.T) {
	f, _ := refusedFixture(t)
	require.NoError(t, f.svc.ForceExport(context.Background(), f.mtixDir))
	st, err := f.svc.PendingImportRefusal(f.mtixDir)
	require.NoError(t, err)
	assert.False(t, st.Pending)
}

// TestStoreSyncs_ReportsTheHubMark: the handlers ask the service whether the
// store syncs.
func TestStoreSyncs_ReportsTheHubMark(t *testing.T) {
	ctx := context.Background()
	f := newGuardFixture(t)
	synced, err := f.svc.StoreSyncs(ctx)
	require.NoError(t, err)
	assert.False(t, synced)
	markFixtureSynced(t, f)
	synced, err = f.svc.StoreSyncs(ctx)
	require.NoError(t, err)
	assert.True(t, synced)
}

// refusalRecord reads the refusal record of f as a map.
func refusalRecord(t *testing.T, f *guardFixture) map[string]any {
	t.Helper()
	var m map[string]any
	require.NoError(t, json.Unmarshal(f.read(t, "data/auto-import-refusal.json"), &m))
	return m
}

// writeRefusalRecord replaces the refusal record of f.
func writeRefusalRecord(t *testing.T, f *guardFixture, m map[string]any) {
	t.Helper()
	raw, err := json.Marshal(m)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(f.mtixDir, "data", "auto-import-refusal.json"), raw, 0o644))
}

// TestPendingImportRefusal_CorruptRecord_ReturnsError: a refusal record that
// cannot be parsed is an error naming the record, never "nothing pending".
func TestPendingImportRefusal_CorruptRecord_ReturnsError(t *testing.T) {
	f, _ := refusedFixture(t)
	require.NoError(t, os.WriteFile(filepath.Join(f.mtixDir, "data", "auto-import-refusal.json"), []byte("{not json"), 0o644))
	st, err := f.svc.PendingImportRefusal(f.mtixDir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "auto-import-refusal.json")
	assert.False(t, st.Pending)
}

// TestPendingImportRefusal_PendingPredicate: a refusal is pending only while
// it is unresolved, for the board on disk, and not already imported.
func TestPendingImportRefusal_PendingPredicate(t *testing.T) {
	tests := []struct {
		name        string
		edit        func(t *testing.T, f *guardFixture, m map[string]any)
		wantPending bool
	}{
		{"unresolved, same file", func(*testing.T, *guardFixture, map[string]any) {}, true},
		{"resolved", func(_ *testing.T, _ *guardFixture, m map[string]any) { m["resolved_at"] = "2026-11-12T10:00:00Z" }, false},
		{"record for another file", func(_ *testing.T, _ *guardFixture, m map[string]any) { m["file_hash"] = "deadbeef" }, false},
		{"file already imported", func(t *testing.T, f *guardFixture, m map[string]any) {
			require.NoError(t, os.WriteFile(filepath.Join(f.mtixDir, "data", "sync.sha256"), []byte(m["file_hash"].(string)), 0o644))
		}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f, _ := refusedFixture(t)
			m := refusalRecord(t, f)
			tt.edit(t, f, m)
			writeRefusalRecord(t, f, m)
			st, err := f.svc.PendingImportRefusal(f.mtixDir)
			require.NoError(t, err)
			assert.Equal(t, tt.wantPending, st.Pending)
		})
	}
}

// TestStoreSyncs_UnreadableStore_ErrorDoesNotNameAutoImport: a manual import
// that cannot read whether the store syncs is not told about auto-import.
func TestStoreSyncs_UnreadableStore_ErrorDoesNotNameAutoImport(t *testing.T) {
	ctx := context.Background()
	f := newGuardFixture(t)
	_, err := f.store.WriteDB().ExecContext(ctx, `DROP TABLE applied_events`)
	require.NoError(t, err)
	_, err = f.svc.StoreSyncs(ctx)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "read whether the store syncs with a hub")
	assert.NotContains(t, err.Error(), "auto-import")
}

// TestStoreHoldsTasks_ReportsEmptyAndFilledStores: the replace guard lets an
// empty store through.
func TestStoreHoldsTasks_ReportsEmptyAndFilledStores(t *testing.T) {
	ctx := context.Background()
	empty := newProjectFixture(t)
	holds, err := empty.svc.StoreHoldsTasks(ctx)
	require.NoError(t, err)
	assert.False(t, holds)
	filled := newGuardFixture(t)
	holds, err = filled.svc.StoreHoldsTasks(ctx)
	require.NoError(t, err)
	assert.True(t, holds)
}

// TestPendingImportRefusal_RestoredInbox_FixKeepsTheMail: with restored mail
// in the store the fix names the mail-safe recovery, not the replace import.
func TestPendingImportRefusal_RestoredInbox_FixKeepsTheMail(t *testing.T) {
	f := newGuardFixture(t)
	_, err := f.store.WriteDB().Exec(`INSERT INTO sync_events(event_id,project_prefix,node_id,op_type,payload,wall_clock_ts,lamport_clock,vector_clock,author_id,author_machine_hash,sync_status,created_at) VALUES('restored-safety-mail','PROJ','PROJ-1','comment','{"to":"worker","body":"private"}',0,0,'{}','sender','','applied','2026-11-05T00:00:00Z')`)
	require.NoError(t, err)
	_, err = f.store.WriteDB().Exec(`INSERT INTO inbox_restored_events VALUES('restored-safety-mail')`)
	require.NoError(t, err)
	f.pull(t, f.teammateBoard(t, func(d *sqlite.ExportData) { d.Nodes[nodeIndex(t, d, "PROJ-1")].Annotations = nil }))
	require.ErrorIs(t, f.svc.AutoImport(context.Background(), f.mtixDir), service.ErrAutoImportRefused)

	st, err := f.svc.PendingImportRefusal(f.mtixDir)
	require.NoError(t, err)
	require.True(t, st.Pending)
	assert.Contains(t, st.Fix, sqlite.RestoredInboxRecovery)
	assert.NotContains(t, st.Fix, "--mode replace")
}
