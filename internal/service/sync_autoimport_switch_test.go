// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Tests for MTIX-95.31.2 (FR-15.2j): sync.auto_sync was documented but never
// read, so auto-import could not be turned off. With it set to false a
// changed .mtix/tasks.json is never imported automatically, while
// auto-export keeps .mtix/tasks.json current. Written red-first.
package service_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// autoSyncSwitch is a fixed sync.auto_sync value.
type autoSyncSwitch bool

// AutoSyncSetting returns the fixed value.
func (a autoSyncSwitch) AutoSyncSetting() (string, bool, error) {
	return strconv.FormatBool(bool(a)), bool(a), nil
}

// TestAutoImport_AutoSyncFalse_NeverImportsAndExportStillRuns verifies that
// with sync.auto_sync false a teammate's changed board (a pure addition,
// which would otherwise import) is not imported, and nothing is printed or
// backed up; that the next write does not overwrite that pulled board but
// keeps the change in the store and says so (MTIX-95.31.2, round 3); that
// once the board is imported explicitly, auto-export rewrites tasks.json
// again; and that switching it back on imports the next change.
func TestAutoImport_AutoSyncFalse_NeverImportsAndExportStillRuns(t *testing.T) {
	ctx := context.Background()
	f := newGuardFixture(t)
	f.svc.SetAutoSyncConfig(autoSyncSwitch(false))
	assert.False(t, f.svc.AutoImportEnabled())

	board := f.teammateBoard(t, func(d *sqlite.ExportData) { addTeammateNode(t, d, "PROJ-2", "PROJ-3", 3) })
	hash := string(f.read(t, "data/sync.sha256"))
	f.pull(t, board)

	require.NoError(t, f.svc.AutoImport(ctx, f.mtixDir))
	_, err := f.store.GetNode(ctx, "PROJ-3")
	assert.ErrorIs(t, err, model.ErrNotFound, "auto-import is off: the file is not imported")
	assert.Equal(t, hash, string(f.read(t, "data/sync.sha256")), "the stored hash is not updated")
	assert.Empty(t, f.notices.String(), "nothing is printed")
	assert.Empty(t, f.preSyncBackups(t), "no backup is taken")

	// A write does not overwrite the pulled board.
	now := time.Date(2026, 9, 24, 11, 0, 0, 0, time.UTC)
	require.NoError(t, f.store.CreateNode(ctx, &model.Node{
		ID: "PROJ-4", Project: "PROJ", Depth: 0, Seq: 4, Title: "Local task",
		Status: model.StatusOpen, Priority: model.PriorityMedium, Weight: 1.0,
		NodeType: model.NodeTypeEpic, ContentHash: "h-PROJ-4", CreatedAt: now, UpdatedAt: now,
	}))
	require.NoError(t, f.svc.AutoExport(ctx, f.mtixDir))
	assert.Equal(t, string(board), string(f.read(t, "tasks.json")), "the pulled board is not overwritten")
	assert.Contains(t, f.notices.String(), ".mtix/tasks.json was not rewritten")
	assert.Contains(t, f.notices.String(), "mtix import .mtix/tasks.json --mode merge")
	report, err := f.svc.Compare(ctx, f.mtixDir)
	require.NoError(t, err)
	require.NotNil(t, report.AutoImport.LastRefusal)
	assert.True(t, report.AutoImport.LastRefusal.Pending)
	assert.Equal(t, "not_imported", report.AutoImport.LastRefusal.Kind)

	// Imported explicitly (mtix import --mode merge), the board is written again.
	data, err := sqlite.DecodeExportData(bytes.NewReader(board))
	require.NoError(t, err)
	_, _, err = f.store.ImportReconcile(ctx, data, sqlite.ImportReconcileOptions{Mode: sqlite.ImportModeMerge})
	require.NoError(t, err)
	require.NoError(t, f.svc.ResolveRefusalByImport(f.mtixDir, filepath.Join(f.mtixDir, "tasks.json")))
	require.NoError(t, f.svc.AutoExport(ctx, f.mtixDir))
	exported := string(f.read(t, "tasks.json"))
	assert.Contains(t, exported, `"PROJ-4"`, "auto-export writes the local store again")
	assert.Contains(t, exported, `"PROJ-3"`)
	assert.Equal(t, hashBytes([]byte(exported)), string(f.read(t, "data/sync.sha256")))

	// Switched back on, the next changed board is imported.
	f.svc.SetAutoSyncConfig(autoSyncSwitch(true))
	assert.True(t, f.svc.AutoImportEnabled())
	f.pull(t, f.teammateBoard(t, func(d *sqlite.ExportData) { addTeammateNode(t, d, "PROJ-2", "PROJ-5", 5) }))
	require.NoError(t, f.svc.AutoImport(ctx, f.mtixDir))
	_, err = f.store.GetNode(ctx, "PROJ-5")
	assert.NoError(t, err, "auto-import is on again")
}

// TestAutoImport_AutoSyncFromConfigFile_Honoured verifies the switch as read
// from .mtix/config.yaml by ConfigService: false in any form turns
// auto-import off; the default (no key) and a value that is neither true
// nor false leave it on, the latter with a one-line warning printed once.
func TestAutoImport_AutoSyncFromConfigFile_Honoured(t *testing.T) {
	tests := []struct {
		name       string
		config     string
		wantImport bool
		wantWarn   bool
	}{
		{"key absent (default true)", "prefix: PROJ\n", true, false},
		{"true", "sync:\n  auto_sync: true\n", true, false},
		{"false", "sync:\n  auto_sync: false\n", false, false},
		{"quoted false", "sync:\n  auto_sync: \"false\"\n", false, false},
		{"FALSE", "sync:\n  auto_sync: FALSE\n", false, false},
		{"0", "sync:\n  auto_sync: 0\n", false, false},
		{"mistyped value falls back to on", "sync:\n  auto_sync: ture\n", true, true},
		{"yes falls back to on", "sync:\n  auto_sync: yes\n", true, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			f := newGuardFixture(t)
			configPath := filepath.Join(f.mtixDir, "config.yaml")
			require.NoError(t, os.WriteFile(configPath, []byte(tt.config), 0o644))
			cfg, err := service.NewConfigService(configPath)
			require.NoError(t, err)
			f.svc.SetAutoSyncConfig(cfg)

			f.pull(t, f.teammateBoard(t, func(d *sqlite.ExportData) { addTeammateNode(t, d, "PROJ-2", "PROJ-3", 3) }))
			require.NoError(t, f.svc.AutoImport(ctx, f.mtixDir))
			require.NoError(t, f.svc.AutoImport(ctx, f.mtixDir)) // the next command
			_, err = f.store.GetNode(ctx, "PROJ-3")
			assert.Equal(t, tt.wantImport, err == nil, "imported: %v", err)
			assert.Equal(t, tt.wantImport, strings.Contains(f.notices.String(), "mtix: imported"))
			wantWarnings := 0
			if tt.wantWarn {
				wantWarnings = 1
			}
			assert.Equal(t, wantWarnings, strings.Count(f.notices.String(), "neither true nor false"),
				"the warning is printed once per process")
		})
	}
}

// TestAutoImport_AutoSyncFalse_EmptyStoreStillImported is the fresh-clone
// case (FR-15.2a): .mtix/config.yaml is tracked, so a clone inherits
// sync.auto_sync false, but its empty store is imported anyway, since
// nothing in it can be lost. Once the store holds nodes, the switch
// applies again.
func TestAutoImport_AutoSyncFalse_EmptyStoreStillImported(t *testing.T) {
	ctx := context.Background()
	teammate := newGuardFixture(t)
	clone := newProjectFixture(t)
	clone.svc.SetAutoSyncConfig(autoSyncSwitch(false))
	clone.pull(t, teammate.read(t, "tasks.json"))

	require.NoError(t, clone.svc.AutoImport(ctx, clone.mtixDir))
	for _, id := range []string{"PROJ-1", "PROJ-2"} {
		_, err := clone.store.GetNode(ctx, id)
		require.NoError(t, err, "the empty store imports the board")
	}
	assert.Contains(t, clone.notices.String(), "mtix: imported the changed .mtix/tasks.json: nodes 2 added")

	clone.pull(t, clone.teammateBoard(t, func(d *sqlite.ExportData) { addTeammateNode(t, d, "PROJ-2", "PROJ-3", 3) }))
	require.NoError(t, clone.svc.AutoImport(ctx, clone.mtixDir))
	_, err := clone.store.GetNode(ctx, "PROJ-3")
	assert.ErrorIs(t, err, model.ErrNotFound, "with nodes in the store, the switch applies")
}
