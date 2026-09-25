// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Tests for MTIX-95.31.11 (FR-15.2h): when a pulled tasks.json and the
// local store both changed, every message that names the ways out leads
// with the merge import, which keeps both sides' data, before mtix sync
// --fix and the replace import, which each keep one side and drop the
// other. The merge is never described as combining both: for a task whose
// content the teammate did not change, it keeps the local status, assignee
// and wake time over the teammate's, so it can revert a teammate's claim.
// The warning and the line a write prints therefore say so, and point to
// the way out for a conflict although nothing changed locally (the
// recovery the user manual documents with the upgrade notes).
package service_test

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// mergeCaveat is how the warning and the pending line say what the merge
// keeps; recoveryPointer is the step of the recovery that puts the pulled
// board back.
const (
	mergeCaveat     = "the local status, assignee and wake time win for a task whose content the teammate did not change"
	recoveryPointer = "if you changed nothing locally: mtix sync --fix, then git checkout HEAD -- .mtix/tasks.json, then any command that imports, such as mtix list"
)

// assertMergeLeads checks text names the merge import before both options
// that drop a side.
func assertMergeLeads(t *testing.T, text string) {
	t.Helper()
	merge := strings.Index(text, "mtix import .mtix/tasks.json --mode merge")
	fix := strings.Index(text, "mtix sync --fix")
	replace := strings.Index(text, "mtix import .mtix/tasks.json --mode replace")
	require.GreaterOrEqual(t, merge, 0, "the merge import is named: %s", text)
	require.GreaterOrEqual(t, fix, 0, "mtix sync --fix is named: %s", text)
	require.GreaterOrEqual(t, replace, 0, "the replace import is named: %s", text)
	assert.Less(t, merge, fix, "the merge import comes before mtix sync --fix: %s", text)
	assert.Less(t, merge, replace, "the merge import comes before the replace import: %s", text)
}

// TestConflictGuidance_BothChanged_LeadsWithMergeImport verifies the order
// of the ways out in each message a conflict prints: the warning of a
// conflict whose replace would lose nothing, the line the next write
// prints while that conflict is pending, and the refusal of a conflict
// whose replace would delete local data.
func TestConflictGuidance_BothChanged_LeadsWithMergeImport(t *testing.T) {
	tests := []struct {
		name    string
		change  func(t *testing.T, f *guardFixture)
		message func(t *testing.T, f *guardFixture) string
		caveat  bool // the message says what the merge keeps and names the recovery
		refused bool // the auto-import returns ErrAutoImportRefused; otherwise nil
	}{
		{"the warning of a lossless conflict", retitleLocally, func(_ *testing.T, f *guardFixture) string {
			return lineWith(f.logs.String(), "conflict detected")
		}, true, false},
		{"the line a write prints while the conflict is pending", retitleLocally, func(t *testing.T, f *guardFixture) string {
			writeLocalTask(t, f, "PROJ-4", 4)
			require.NoError(t, f.svc.AutoExport(context.Background(), f.mtixDir))
			return lineWith(f.notices.String(), "Both it and the local store changed")
		}, true, false},
		{"the refusal of a conflict that would delete local data", func(t *testing.T, f *guardFixture) {
			require.NoError(t, f.store.SetAnnotations(context.Background(), "PROJ-2", []model.Annotation{{
				ID: "01J9GUIDANCE000000000000001", Author: "dev", Text: "local only", CreatedAt: f.now,
			}}))
		}, func(_ *testing.T, f *guardFixture) string {
			return f.notices.String()
		}, false, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newGuardFixture(t)
			f.pull(t, f.teammateBoard(t, func(d *sqlite.ExportData) { addTeammateNode(t, d, "PROJ-2", "PROJ-3", 3) }))
			tt.change(t, f)
			err := f.svc.AutoImport(context.Background(), f.mtixDir) // the command's own auto-import: a conflict
			if tt.refused {
				require.ErrorIs(t, err, service.ErrAutoImportRefused)
			} else {
				require.NoError(t, err, "a conflict that loses nothing is recorded and logged, not returned")
			}

			text := tt.message(t, f)
			assertMergeLeads(t, text)
			if tt.caveat {
				assert.Contains(t, text, mergeCaveat)
				assert.Contains(t, text, recoveryPointer)
				assert.NotContains(t, text, "combine them")
			}
		})
	}
}

// retitleLocally changes PROJ-2's title in the store without exporting it:
// a local change a replace of the pulled board would not lose.
func retitleLocally(t *testing.T, f *guardFixture) {
	t.Helper()
	f.exec(t, `UPDATE nodes SET title = 'Task PROJ-2, retitled locally' WHERE id = 'PROJ-2'`)
}

// TestPendingGuidance_NotImported_NamesWhatMergeKeeps verifies the line a
// write prints while a pulled board waits with automatic import off
// (kind not_imported) names the merge first and says what it keeps, as the
// conflict lines do: it never presents the merge as combining both sides.
func TestPendingGuidance_NotImported_NamesWhatMergeKeeps(t *testing.T) {
	ctx := context.Background()
	f := newGuardFixture(t)
	f.svc.SetAutoSyncConfig(autoSyncSwitch(false))
	f.pull(t, f.teammateBoard(t, func(d *sqlite.ExportData) { addTeammateNode(t, d, "PROJ-2", "PROJ-3", 3) }))
	require.NoError(t, f.svc.AutoImport(ctx, f.mtixDir), "automatic import is off: nothing is imported, nothing refused")
	writeLocalTask(t, f, "PROJ-4", 4)

	require.NoError(t, f.svc.AutoExport(ctx, f.mtixDir))

	text := lineWith(f.notices.String(), "was not rewritten")
	merge := strings.Index(text, "mtix import .mtix/tasks.json --mode merge")
	fix := strings.Index(text, "mtix sync --fix")
	require.GreaterOrEqual(t, merge, 0, "the merge import is named: %s", text)
	require.GreaterOrEqual(t, fix, 0, "mtix sync --fix is named: %s", text)
	assert.Less(t, merge, fix, "the merge import comes first: %s", text)
	assert.Contains(t, text, mergeCaveat)
	assert.NotContains(t, text, "combine them")
}
