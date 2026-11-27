// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Regression tests for MTIX-95.31.12 (FR-15.2h): an applied automatic import
// refreshes the conflict baseline (.mtix/data/sync-db.sha256), so the next
// changed board imports instead of being refused as a false conflict. 0.5.3
// never refreshed it. Written red-first.
package service_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// TestAutoImport_AppliedImport_NextChangedBoardImportsWithoutConflict
// verifies two pulls in a row, each changing the board, both apply, and the
// baseline follows the store after each one.
func TestAutoImport_AppliedImport_NextChangedBoardImportsWithoutConflict(t *testing.T) {
	ctx := context.Background()
	f := newGuardFixture(t)

	f.pull(t, f.teammateBoard(t, teammateChange(t)))
	require.NoError(t, f.svc.AutoImport(ctx, f.mtixDir))
	assert.Equal(t, f.currentBaseline(t), string(f.read(t, "data/sync-db.sha256")),
		"the baseline is the imported store's")

	f.pull(t, f.teammateBoard(t, func(d *sqlite.ExportData) {
		d.Nodes[nodeIndex(t, d, "PROJ-2")].Title = "Task PROJ-2, retitled by a teammate"
		addTeammateNode(t, d, "PROJ-2", "PROJ-4", 4)
	}))
	require.NoError(t, f.svc.AutoImport(ctx, f.mtixDir))

	_, err := f.store.GetNode(ctx, "PROJ-4")
	require.NoError(t, err, "the second pulled board is imported")
	report, err := f.svc.Compare(ctx, f.mtixDir)
	require.NoError(t, err)
	assert.Nil(t, report.AutoImport.LastRefusal, "no conflict is recorded: log: %s", f.logs)
	assert.Equal(t, f.currentBaseline(t), string(f.read(t, "data/sync-db.sha256")))
}

// TestAutoImport_BaselineStaleFrom053Import_RefusedWithMergeRecovery pins the
// state 0.5.3 leaves (MTIX-95.31.12): its auto-import never refreshed the
// baseline, so the store differs from it by the imported changes. That is
// not told from a local change without a base, so the next changed board is
// refused, never applied over it: nothing is written, and the refusal names
// the recovery documented in the upgrade notes (mtix import --mode merge).
func TestAutoImport_BaselineStaleFrom053Import_RefusedWithMergeRecovery(t *testing.T) {
	ctx := context.Background()
	f := newUpgradeFixture(t, upgradeSetup{})
	f.writeBaseline(t, v053BaselineHash(t, f.store))

	// 0.5.3 imported a teammate's board and left the baseline as it was.
	f.pull(t, f.teammateBoard(t, teammateChange(t)))
	_, err := f.store.Import(ctx, mustDecodeBoard(t, f.read(t, "tasks.json")), sqlite.ImportModeReplace, false)
	require.NoError(t, err)
	snapshot := f.storeSnapshot(t)
	f.pull(t, f.teammateBoard(t, func(d *sqlite.ExportData) {
		addTeammateNode(t, d, "PROJ-2", "PROJ-4", 4)
	}))

	require.NoError(t, f.svc.AutoImport(ctx, f.mtixDir))

	_, err = f.store.GetNode(ctx, "PROJ-4")
	require.Error(t, err, "the board is not applied over a store the baseline does not describe")
	assert.Equal(t, snapshot, f.storeSnapshot(t), "the store is unchanged")
	assert.Contains(t, f.logs.String(), "mtix import .mtix/tasks.json --mode merge")
}

// mustDecodeBoard decodes a tasks.json the way an import reads it.
func mustDecodeBoard(t *testing.T, raw []byte) *sqlite.ExportData {
	t.Helper()
	var data sqlite.ExportData
	require.NoError(t, json.Unmarshal(raw, &data))
	return &data
}
