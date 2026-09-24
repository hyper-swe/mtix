// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Tests for MTIX-95.31.2: mtix sync (SyncService.Compare) reports the
// auto-import state, whether sync.auto_sync leaves it on, and the last
// auto-import mtix refused with its reason and whether the refused
// .mtix/tasks.json is still waiting. Written red-first.
package service_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// TestCompare_AutoImportSwitch_ReportsEnabledOrDisabled verifies the report
// carries the sync.auto_sync switch and no refusal before any.
func TestCompare_AutoImportSwitch_ReportsEnabledOrDisabled(t *testing.T) {
	tests := []struct {
		name    string
		sw      *autoSyncSwitch
		enabled bool
	}{
		{"no switch wired (default on)", nil, true},
		{"switched on", func() *autoSyncSwitch { v := autoSyncSwitch(true); return &v }(), true},
		{"switched off", func() *autoSyncSwitch { v := autoSyncSwitch(false); return &v }(), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newGuardFixture(t)
			if tt.sw != nil {
				f.svc.SetAutoSyncConfig(*tt.sw)
			}
			report, err := f.svc.Compare(context.Background(), f.mtixDir)
			require.NoError(t, err)
			assert.Equal(t, tt.enabled, report.AutoImport.Enabled)
			assert.Nil(t, report.AutoImport.LastRefusal)
		})
	}
}

// TestCompare_AfterLossyRefusal_ReportsPendingThenResolved verifies the last
// refusal is reported with its time, the refused file's hash and a reason
// naming the loss, as pending while that file waits, and as no longer
// pending once tasks.json is rewritten from the store (mtix sync --fix).
func TestCompare_AfterLossyRefusal_ReportsPendingThenResolved(t *testing.T) {
	ctx := context.Background()
	f := newGuardFixture(t)
	f.now = time.Date(2026, 9, 24, 14, 30, 0, 0, time.UTC)
	board := f.teammateBoard(t, func(d *sqlite.ExportData) { d.Nodes[nodeIndex(t, d, "PROJ-1")].Annotations = nil })
	f.pull(t, board)
	require.Error(t, f.svc.AutoImport(ctx, f.mtixDir))

	report, err := f.svc.Compare(ctx, f.mtixDir)
	require.NoError(t, err)
	assert.True(t, report.AutoImport.Enabled)
	refusal := report.AutoImport.LastRefusal
	require.NotNil(t, refusal)
	assert.Equal(t, "2026-09-24T14:30:00Z", refusal.RefusedAt)
	assert.Equal(t, hashBytes(board), refusal.FileHash)
	assert.Contains(t, refusal.Reason, "PROJ-1")
	assert.Contains(t, refusal.Reason, "annotation")
	assert.True(t, refusal.Pending, "the refused file is still waiting")

	f.now = time.Date(2026, 9, 24, 14, 45, 0, 0, time.UTC)
	require.NoError(t, f.svc.ForceExport(ctx, f.mtixDir)) // what mtix sync --fix runs
	report, err = f.svc.Compare(ctx, f.mtixDir)
	require.NoError(t, err)
	require.NotNil(t, report.AutoImport.LastRefusal, "the last refusal stays on record")
	assert.False(t, report.AutoImport.LastRefusal.Pending, "tasks.json was rewritten from the store")
	assert.Equal(t, "2026-09-24T14:45:00Z", report.AutoImport.LastRefusal.ResolvedAt)
	assert.Equal(t, refusal.Reason, report.AutoImport.LastRefusal.Reason)
}

// TestCompare_OtherRefusals_ReportedWithReason verifies the other auto-import
// refusals are recorded too: a conflict (both sides changed, FR-15.2h), a
// file from a newer schema (FR-15.2g) and a local store that cannot be
// exported (MTIX-95.31.1).
func TestCompare_OtherRefusals_ReportedWithReason(t *testing.T) {
	tests := []struct {
		name   string
		setup  func(t *testing.T, f *guardFixture)
		board  func(t *testing.T, f *guardFixture) []byte
		after  func(t *testing.T, f *guardFixture)
		reason string
	}{
		{"conflict", func(t *testing.T, f *guardFixture) {
			title := "Local edit after the last export"
			require.NoError(t, f.store.UpdateNode(context.Background(), "PROJ-2", &store.NodeUpdate{Title: &title}))
		}, func(t *testing.T, f *guardFixture) []byte {
			return f.teammateBoard(t, func(d *sqlite.ExportData) { addTeammateNode(t, d, "PROJ-2", "PROJ-3", 3) })
		}, nil, "changed since the last sync"},
		{"newer schema", nil, func(t *testing.T, f *guardFixture) []byte {
			return f.teammateBoard(t, func(d *sqlite.ExportData) { d.SchemaVersion = "3.0.0" })
		}, nil, "newer than supported"},
		{"local store cannot be exported", func(t *testing.T, f *guardFixture) {
			_, err := f.store.WriteDB().ExecContext(context.Background(),
				`UPDATE nodes SET annotations = ? WHERE id = ?`, `[{"id": "torn"`, "PROJ-2")
			require.NoError(t, err)
		}, func(t *testing.T, f *guardFixture) []byte {
			raw, err := os.ReadFile(filepath.Join(f.mtixDir, "tasks.json"))
			require.NoError(t, err)
			return append(raw, '\n') // any change to the file
		}, func(t *testing.T, f *guardFixture) {
			// Repair the cell so the store can be compared again.
			_, err := f.store.WriteDB().ExecContext(context.Background(),
				`UPDATE nodes SET annotations = NULL WHERE id = ?`, "PROJ-2")
			require.NoError(t, err)
		}, "mtix recover"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			f := newGuardFixture(t)
			board := tt.board(t, f)
			if tt.setup != nil {
				tt.setup(t, f)
			}
			f.pull(t, board)
			_ = f.svc.AutoImport(ctx, f.mtixDir) // refused or skipped
			_, err := f.store.GetNode(ctx, "PROJ-3")
			assert.ErrorIs(t, err, model.ErrNotFound, "nothing was imported")
			if tt.after != nil {
				tt.after(t, f)
			}

			report, err := f.svc.Compare(ctx, f.mtixDir)
			require.NoError(t, err)
			require.NotNil(t, report.AutoImport.LastRefusal)
			assert.Contains(t, report.AutoImport.LastRefusal.Reason, tt.reason)
			assert.True(t, report.AutoImport.LastRefusal.Pending)
		})
	}
}

// TestCompare_UnreadableRefusalRecord_ReportsIt verifies a refusal record
// that cannot be read is reported as such rather than hidden.
func TestCompare_UnreadableRefusalRecord_ReportsIt(t *testing.T) {
	f := newGuardFixture(t)
	require.NoError(t, os.WriteFile(filepath.Join(f.mtixDir, "data", "auto-import-refusal.json"), []byte("{torn"), 0o644))

	report, err := f.svc.Compare(context.Background(), f.mtixDir)
	require.NoError(t, err)
	require.NotNil(t, report.AutoImport.LastRefusal)
	assert.Contains(t, report.AutoImport.LastRefusal.Reason, "cannot be read")
	assert.False(t, report.AutoImport.LastRefusal.Pending)
}
