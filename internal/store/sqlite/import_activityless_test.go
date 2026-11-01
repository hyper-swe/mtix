// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// TestDiffReplace_ActivitylessCopy_ReportsBlankedPrompt prevents a newer
// timestamp from making an empty activity stream evidence of shared edits.
func TestDiffReplace_ActivitylessCopy_ReportsBlankedPrompt(t *testing.T) {
	local := &sqlite.ExportData{}
	require.NoError(t, json.Unmarshal([]byte(`{"schema_version":"2.0.0","nodes":[{"id":"TEST-1","title":"Activity-less prompt","created_at":"2026-09-01T00:00:00Z","updated_at":"2026-09-02T00:00:00Z","prompt":"Unexported local instructions"}]}`), local))
	for _, stamp := range []string{"2020-01-01T00:00:00Z", "2099-01-01T00:00:00Z", ""} {
		t.Run(stamp, func(t *testing.T) {
			file := editExport(t, local, func(doc map[string]any) {
				n := docNode(t, doc, "TEST-1")
				n["prompt"] = ""
				if stamp != "" {
					n["updated_at"] = stamp
				}
			})
			diff, err := sqlite.DiffReplace(local, file)
			require.NoError(t, err)
			loss := lossOf(diff, "TEST-1")
			require.NotNil(t, loss)
			assert.Equal(t, []string{"prompt"}, loss.Fields)
			same, err := sqlite.DiffReplace(local, local)
			require.NoError(t, err)
			assert.False(t, same.Lossy(), "unchanged activity-less tasks remain safe")
		})
	}
}

// TestResolveWithBase_ActivitylessClear_UsesBaseline ensures the conservative
// timestamp rule does not override a clear proved by a field-by-field baseline.
func TestResolveWithBase_ActivitylessClear_UsesBaseline(t *testing.T) {
	local := &sqlite.ExportData{}
	require.NoError(t, json.Unmarshal([]byte(`{"schema_version":"1.0.0","nodes":[{"id":"TEST-1","title":"Exchanged prompt","created_at":"2026-09-01T00:00:00Z","updated_at":"2026-09-02T00:00:00Z","prompt":"Shared instructions"}]}`), local))
	base, err := sqlite.ImportBaseFromBoard(local, "test exchanged board")
	require.NoError(t, err)
	file := editExport(t, local, func(doc map[string]any) {
		doc["schema_version"] = "2.0.0"
		docNode(t, doc, "TEST-1")["prompt"] = ""
	})
	resolved, err := sqlite.ResolveWithBase(local, file, base)
	require.NoError(t, err)
	assert.False(t, resolved.Diff.Lossy(), "baseline proves this clear is upstream: %+v", resolved.Diff.Losses)
	assert.Empty(t, resolved.Resolved.Nodes[0].Prompt)
}

// TestDiffReplace_ActivitylessNonemptyUpdate_NotLossy retains the existing
// policy for incoming values and for an activity-less task left unchanged.
func TestDiffReplace_ActivitylessNonemptyUpdate_NotLossy(t *testing.T) {
	local := &sqlite.ExportData{}
	require.NoError(t, json.Unmarshal([]byte(`{"schema_version":"2.0.0","nodes":[{"id":"TEST-1","title":"Activity-less update","created_at":"2026-09-01T00:00:00Z","updated_at":"2026-09-02T00:00:00Z","prompt":"Original instructions"}]}`), local))
	file := editExport(t, local, func(doc map[string]any) {
		docNode(t, doc, "TEST-1")["prompt"] = "Teammate instructions"
	})
	diff, err := sqlite.DiffReplace(local, file)
	require.NoError(t, err)
	assert.False(t, diff.Lossy())
	assert.Equal(t, []string{"TEST-1"}, diff.Updated)
}

// TestDiffReplace_ActivitylessLocalTaskWithActivityInCopy_StillReportsBlankedPrompt
// pins the rule's key input: it is the LOCAL task that must have activity. A
// teammate copy that carries an activity entry (for example after an unclaim)
// says nothing about whether the teammate saw this machine's local-only prompt.
func TestDiffReplace_ActivitylessLocalTaskWithActivityInCopy_StillReportsBlankedPrompt(t *testing.T) {
	local := &sqlite.ExportData{}
	require.NoError(t, json.Unmarshal([]byte(`{"schema_version":"2.0.0","nodes":[{"id":"TEST-1","title":"Activity-less prompt","created_at":"2026-09-01T00:00:00Z","updated_at":"2026-09-02T00:00:00Z","prompt":"Local only instructions"}]}`), local))
	file := editExport(t, local, func(doc map[string]any) {
		n := docNode(t, doc, "TEST-1")
		n["prompt"] = ""
		n["updated_at"] = "2099-01-01T00:00:00Z"
	})
	file.Nodes[0].Activity = []model.ActivityEntry{{
		ID: "01K0000000000000000000000A", Type: model.ActivityTypeUnclaim, Author: "teammate",
		Text: "released", CreatedAt: time.Date(2026, 9, 3, 0, 0, 0, 0, time.UTC),
	}}
	require.NoError(t, sqlite.RecomputeExportChecksum(file))
	diff, err := sqlite.DiffReplace(local, file)
	require.NoError(t, err)
	loss := lossOf(diff, "TEST-1")
	require.NotNil(t, loss, "the teammate's activity does not prove they saw the local prompt")
	assert.Equal(t, []string{"prompt"}, loss.Fields)
}

// TestImportReconcile_MergeOfNewerActivitylessCopy_KeepsLocalValueTheCopyBlanks
// pins that the merge a refusal recommends uses the same currency rule: with
// no activity on either side, a copy whose timestamp is not older still must
// not clear a local-only value.
func TestImportReconcile_MergeOfNewerActivitylessCopy_KeepsLocalValueTheCopyBlanks(t *testing.T) {
	ctx := context.Background()
	later := sameIDTime.Add(time.Hour)
	local, teammate := newTestStore(t), newTestStore(t)
	uid := taskUID(t)
	createPlainTask(t, local, "REC-1", 1, uid, sameIDTime)
	createPlainTask(t, teammate, "REC-1", 1, uid, sameIDTime)
	d := "Local description"
	require.NoError(t, local.UpdateNode(ctx, "REC-1", &store.NodeUpdate{Description: &d}))
	setUpdatedAt(t, local, "REC-1", later)
	// A created task holds a created entry, which the copy shares; clear the
	// local stream so the task is activity-less, as the ticket describes.
	_, err := local.WriteDB().ExecContext(ctx, `UPDATE nodes SET activity = '[]' WHERE id = 'REC-1'`)
	require.NoError(t, err)
	title := "Retitled by the teammate"
	require.NoError(t, teammate.UpdateNode(ctx, "REC-1", &store.NodeUpdate{Title: &title}))
	setUpdatedAt(t, teammate, "REC-1", later.Add(time.Hour)) // newer than the local copy

	mergeFile(t, local, exportOf(t, teammate))

	got, err := local.GetNode(ctx, "REC-1")
	require.NoError(t, err)
	assert.Equal(t, "Retitled by the teammate", got.Title, "the copy's other change applies")
	assert.Equal(t, "Local description", got.Description, "the value the copy blanks stays")
}
